package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"deploycp/internal/system"
)

type SSLService struct {
	cfg      *config.Config
	repo     *repositories.SSLCertificateRepository
	settings *repositories.SettingRepository
	runner   *system.Runner
	audit    *AuditService
}

func NewSSLService(cfg *config.Config, repo *repositories.SSLCertificateRepository, settings *repositories.SettingRepository, runner *system.Runner, audit *AuditService) *SSLService {
	return &SSLService{cfg: cfg, repo: repo, settings: settings, runner: runner, audit: audit}
}

func (s *SSLService) List() ([]models.SSLCertificate, error) {
	return s.repo.List()
}

func (s *SSLService) Create(domain string, actor *uint, ip string) error {
	item := &models.SSLCertificate{Domain: domain, Status: "pending", AutoRenew: true}
	if err := s.repo.Create(item); err != nil {
		return err
	}
	s.audit.Record(actor, "ssl.create", "ssl_certificate", fmt.Sprintf("%d", item.ID), ip, map[string]string{"domain": domain})
	return s.runHook(context.Background(), "ssl_issue_hook", domain)
}

func (s *SSLService) MarkIssued(id uint, certPath, keyPath, issuer string, notAfter time.Time, actor *uint, ip string) error {
	items, err := s.repo.List()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		now := time.Now()
		items[i].CertPath = certPath
		items[i].KeyPath = keyPath
		items[i].Issuer = issuer
		items[i].Status = "active"
		items[i].NotAfter = &notAfter
		items[i].RenewalLastAt = &now
		items[i].RenewalNextAt = ptrTime(notAfter.Add(-30 * 24 * time.Hour))
		if err := s.repo.Update(&items[i]); err != nil {
			return err
		}
		s.audit.Record(actor, "ssl.issue.complete", "ssl_certificate", fmt.Sprintf("%d", id), ip, nil)
		return nil
	}
	return fmt.Errorf("certificate not found")
}

func (s *SSLService) Renew(id uint, actor *uint, ip string) error {
	items, err := s.repo.List()
	if err != nil {
		return err
	}
	for i := range items {
		if items[i].ID != id {
			continue
		}
		if err := s.runHook(context.Background(), "ssl_renew_hook", items[i].Domain); err != nil {
			return err
		}
		now := time.Now()
		items[i].RenewalLastAt = &now
		if items[i].NotAfter != nil {
			items[i].RenewalNextAt = ptrTime(items[i].NotAfter.Add(-30 * 24 * time.Hour))
		}
		if err := s.repo.Update(&items[i]); err != nil {
			return err
		}
		s.audit.Record(actor, "ssl.renew", "ssl_certificate", fmt.Sprintf("%d", id), ip, nil)
		return nil
	}
	return fmt.Errorf("certificate not found")
}

func (s *SSLService) Delete(id uint, actor *uint, ip string) error {
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "ssl.delete", "ssl_certificate", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *SSLService) certDir() string {
	return filepath.Join(s.cfg.Paths.StorageRoot, "ssl")
}

func (s *SSLService) CreateImport(domain, privateKey, certificate, bundle string, actor *uint, ip string) error {
	if strings.TrimSpace(privateKey) == "" || strings.TrimSpace(certificate) == "" {
		return fmt.Errorf("private key and certificate are required")
	}

	dir := filepath.Join(s.certDir(), strings.ReplaceAll(domain, "*", "_wildcard"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cert dir: %w", err)
	}
	keyPath := filepath.Join(dir, "privkey.pem")
	certPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(keyPath, []byte(strings.TrimSpace(privateKey)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	certContent := strings.TrimSpace(certificate)
	if strings.TrimSpace(bundle) != "" {
		certContent += "\n" + strings.TrimSpace(bundle)
	}
	if err := os.WriteFile(certPath, []byte(certContent+"\n"), 0o600); err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}

	now := time.Now()
	notAfter := now.AddDate(1, 0, 0)
	item := &models.SSLCertificate{
		Domain:   domain,
		Issuer:   "Imported",
		CertPath: certPath,
		KeyPath:  keyPath,
		Status:   "active",
		NotBefore: &now,
		NotAfter:  &notAfter,
		AutoRenew: false,
	}
	if err := s.repo.Create(item); err != nil {
		return err
	}
	s.audit.Record(actor, "ssl.import", "ssl_certificate", fmt.Sprintf("%d", item.ID), ip, map[string]string{"domain": domain})
	return nil
}

func (s *SSLService) CreateSelfSigned(domain string, actor *uint, ip string) error {
	dir := filepath.Join(s.certDir(), strings.ReplaceAll(domain, "*", "_wildcard"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cert dir: %w", err)
	}
	keyPath := filepath.Join(dir, "privkey.pem")
	certPath := filepath.Join(dir, "cert.pem")

	_, err := s.runner.Run(context.Background(), system.CommandRequest{
		Binary: "openssl",
		Args: []string{
			"req", "-x509", "-newkey", "rsa:2048",
			"-keyout", keyPath,
			"-out", certPath,
			"-days", "365",
			"-nodes",
			"-subj", fmt.Sprintf("/CN=%s", domain),
		},
		Timeout:     30 * time.Second,
		AuditAction: "ssl.self-signed",
	})
	if err != nil {
		return fmt.Errorf("openssl: %w", err)
	}

	now := time.Now()
	notAfter := now.AddDate(0, 0, 365)
	item := &models.SSLCertificate{
		Domain:    domain,
		Issuer:    "Self-Signed",
		CertPath:  certPath,
		KeyPath:   keyPath,
		Status:    "active",
		NotBefore: &now,
		NotAfter:  &notAfter,
		AutoRenew: false,
	}
	if err := s.repo.Create(item); err != nil {
		return err
	}
	s.audit.Record(actor, "ssl.self-signed", "ssl_certificate", fmt.Sprintf("%d", item.ID), ip, map[string]string{"domain": domain})
	return nil
}

func (s *SSLService) runHook(ctx context.Context, key, domain string) error {
	value, err := s.settings.Get(key)
	if err != nil || strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return nil
	}
	args := append(parts[1:], domain)
	_, err = s.runner.Run(ctx, system.CommandRequest{Binary: parts[0], Args: args, Timeout: 45 * time.Second, AuditAction: key})
	return err
}

func ptrTime(t time.Time) *time.Time { return &t }
