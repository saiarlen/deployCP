package services

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
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

func (s *SSLService) CreateForWebsite(ctx context.Context, site *models.Website, domain string, actor *uint, ip string) error {
	if site == nil {
		return fmt.Errorf("website context is required")
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return s.CreateSelfSigned(domain, actor, ip)
	}
	if _, err := exec.LookPath(s.cfg.Paths.CertbotBinary); err != nil {
		return s.Create(domain, actor, ip)
	}

	if err := os.MkdirAll(site.RootPath, 0o755); err != nil {
		return fmt.Errorf("prepare website root: %w", err)
	}
	acmeDir := filepath.Join(site.RootPath, ".well-known", "acme-challenge")
	if err := os.MkdirAll(acmeDir, 0o755); err != nil {
		return fmt.Errorf("prepare acme path: %w", err)
	}
	if err := s.preflightHTTPChallenge(ctx, site, domain); err != nil {
		return err
	}

	args := []string{"certonly", "--non-interactive", "--agree-tos", "--webroot", "-w", site.RootPath, "-d", domain, "--cert-name", domain}
	if email := strings.TrimSpace(os.Getenv("DEPLOYCP_LETSENCRYPT_EMAIL")); email != "" {
		args = append(args, "--email", email)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}
	if _, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      s.cfg.Paths.CertbotBinary,
		Args:        args,
		Timeout:     3 * time.Minute,
		AuditAction: "ssl.certbot.issue",
		ActorUserID: actor,
		IP:          ip,
	}); err != nil {
		return err
	}

	notAfter := time.Now().AddDate(0, 2, 0)
	liveDir := filepath.Join("/etc/letsencrypt/live", domain)
	item := s.upsertIssuedCert(domain, filepath.Join(liveDir, "fullchain.pem"), filepath.Join(liveDir, "privkey.pem"), "Let's Encrypt", notAfter)
	s.audit.Record(actor, "ssl.issue.complete", "ssl_certificate", fmt.Sprintf("%d", item.ID), ip, map[string]string{"domain": domain})
	return nil
}

func (s *SSLService) preflightHTTPChallenge(ctx context.Context, site *models.Website, domain string) error {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return fmt.Errorf("domain is required")
	}

	if _, err := net.LookupIP(domain); err != nil {
		return fmt.Errorf("domain %s does not resolve yet: %w", domain, err)
	}

	acmeDir := filepath.Join(site.RootPath, ".well-known", "acme-challenge")
	token := fmt.Sprintf("deploycp-%d", time.Now().UnixNano())
	challengePath := filepath.Join(acmeDir, token)
	if err := os.WriteFile(challengePath, []byte(token), 0o644); err != nil {
		return fmt.Errorf("write acme challenge file: %w", err)
	}
	defer os.Remove(challengePath)

	client := &http.Client{Timeout: 12 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+domain+"/.well-known/acme-challenge/"+token, nil)
	if err != nil {
		return fmt.Errorf("build acme validation request: %w", err)
	}
	req.Header.Set("User-Agent", "DeployCP-SSL-Preflight/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("domain %s is not reachable on port 80 from this server: %w", domain, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	got := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("domain %s returned HTTP %d for the ACME challenge path instead of 200", domain, resp.StatusCode)
	}
	if got != token {
		return fmt.Errorf("domain %s is not serving the expected ACME challenge file from %s", domain, site.RootPath)
	}
	return nil
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
		if strings.Contains(items[i].CertPath, "/etc/letsencrypt/live/") && strings.TrimSpace(items[i].Domain) != "" && s.cfg.Features.PlatformMode != "dryrun" {
			if _, lookErr := exec.LookPath(s.cfg.Paths.CertbotBinary); lookErr == nil {
				if _, runErr := s.runner.Run(context.Background(), system.CommandRequest{
					Binary:      s.cfg.Paths.CertbotBinary,
					Args:        []string{"renew", "--cert-name", items[i].Domain, "--non-interactive"},
					Timeout:     3 * time.Minute,
					AuditAction: "ssl.certbot.renew",
					ActorUserID: actor,
					IP:          ip,
				}); runErr != nil {
					return runErr
				}
			}
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
	items, err := s.repo.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if strings.Contains(item.CertPath, "/etc/letsencrypt/live/") && strings.TrimSpace(item.Domain) != "" && s.cfg.Features.PlatformMode != "dryrun" {
			if _, lookErr := exec.LookPath(s.cfg.Paths.CertbotBinary); lookErr == nil {
				_, _ = s.runner.Run(context.Background(), system.CommandRequest{
					Binary:      s.cfg.Paths.CertbotBinary,
					Args:        []string{"delete", "--cert-name", item.Domain, "--non-interactive"},
					Timeout:     90 * time.Second,
					AuditAction: "ssl.certbot.delete",
					ActorUserID: actor,
					IP:          ip,
				})
			}
		}
		for _, path := range []string{item.CertPath, item.KeyPath} {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if strings.HasPrefix(filepath.Clean(path), filepath.Clean(s.certDir())) {
				_ = os.Remove(path)
			}
		}
		break
	}
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
		Domain:    domain,
		Issuer:    "Imported",
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

func (s *SSLService) upsertIssuedCert(domain, certPath, keyPath, issuer string, notAfter time.Time) *models.SSLCertificate {
	items, _ := s.repo.List()
	now := time.Now()
	for i := range items {
		if !strings.EqualFold(strings.TrimSpace(items[i].Domain), strings.TrimSpace(domain)) {
			continue
		}
		items[i].Issuer = issuer
		items[i].CertPath = certPath
		items[i].KeyPath = keyPath
		items[i].Status = "active"
		items[i].AutoRenew = issuer == "Let's Encrypt"
		items[i].NotBefore = &now
		items[i].NotAfter = &notAfter
		items[i].RenewalLastAt = &now
		items[i].RenewalNextAt = ptrTime(notAfter.Add(-30 * 24 * time.Hour))
		_ = s.repo.Update(&items[i])
		return &items[i]
	}
	item := &models.SSLCertificate{
		Domain:        domain,
		Issuer:        issuer,
		CertPath:      certPath,
		KeyPath:       keyPath,
		Status:        "active",
		AutoRenew:     issuer == "Let's Encrypt",
		NotBefore:     &now,
		NotAfter:      &notAfter,
		RenewalLastAt: &now,
		RenewalNextAt: ptrTime(notAfter.Add(-30 * 24 * time.Hour)),
	}
	_ = s.repo.Create(item)
	return item
}
