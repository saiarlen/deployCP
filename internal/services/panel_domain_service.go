package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
	"deploycp/internal/system"
	"deploycp/internal/utils"
	"deploycp/internal/validators"
)

const panelDomainConfigName = "00-deploycp-panel.conf"

type PanelDomainService struct {
	cfg      *config.Config
	settings *SettingsService
	websites *repositories.WebsiteRepository
	adapter  platform.Adapter
	runner   *system.Runner
	audit    *AuditService
}

func NewPanelDomainService(cfg *config.Config, settings *SettingsService, websites *repositories.WebsiteRepository, adapter platform.Adapter, runner *system.Runner, audit *AuditService) *PanelDomainService {
	return &PanelDomainService{cfg: cfg, settings: settings, websites: websites, adapter: adapter, runner: runner, audit: audit}
}

func (s *PanelDomainService) Configure(ctx context.Context, domain string, actor *uint, ip string) error {
	domain = NormalizePanelDomain(domain)
	if domain == "" {
		return s.Remove(ctx, actor, ip)
	}
	if err := validatePanelDomain(domain); err != nil {
		return err
	}
	if s.websites != nil {
		owner, err := s.websites.FindDomainOwner(domain, 0)
		if err != nil {
			return err
		}
		if owner != nil {
			return fmt.Errorf("domain %s is already used by a platform", domain)
		}
	}
	if !s.cfg.Features.EnableNginxManage {
		return fmt.Errorf("nginx management is disabled")
	}
	if s.adapter == nil {
		return fmt.Errorf("platform adapter is unavailable")
	}

	webroot := s.panelWebroot()
	if err := os.MkdirAll(filepath.Join(webroot, ".well-known", "acme-challenge"), 0o755); err != nil {
		return fmt.Errorf("prepare panel ACME webroot: %w", err)
	}
	if err := os.MkdirAll(panelLogDir(s.cfg), 0o755); err != nil {
		return fmt.Errorf("prepare panel log directory: %w", err)
	}
	restore := s.restorePoint()

	if err := s.writeAndReload(ctx, renderPanelNginxConfig(s.cfg, domain, webroot, "", "")); err != nil {
		return err
	}

	certPath := filepath.Join("/etc/letsencrypt/live", domain, "fullchain.pem")
	keyPath := filepath.Join("/etc/letsencrypt/live", domain, "privkey.pem")
	if s.cfg.Features.PlatformMode != "dryrun" && (!regularFileExists(certPath) || !regularFileExists(keyPath)) {
		if _, err := exec.LookPath(s.cfg.Paths.CertbotBinary); err != nil {
			restore(ctx)
			return fmt.Errorf("certbot is not available: %w", err)
		}
		if err := s.preflightHTTPChallenge(ctx, domain, webroot); err != nil {
			restore(ctx)
			return err
		}
		args := []string{"certonly", "--non-interactive", "--agree-tos", "--webroot", "-w", webroot, "-d", domain, "--cert-name", domain}
		if email := strings.TrimSpace(os.Getenv("DEPLOYCP_LETSENCRYPT_EMAIL")); email != "" {
			args = append(args, "--email", email)
		} else {
			args = append(args, "--register-unsafely-without-email")
		}
		if _, err := s.runner.Run(ctx, system.CommandRequest{
			Binary:      s.cfg.Paths.CertbotBinary,
			Args:        args,
			Timeout:     3 * time.Minute,
			AuditAction: "panel.ssl.certbot.issue",
			ActorUserID: actor,
			IP:          ip,
		}); err != nil {
			restore(ctx)
			return err
		}
	}

	if err := s.writeAndReload(ctx, renderPanelNginxConfig(s.cfg, domain, webroot, certPath, keyPath)); err != nil {
		restore(ctx)
		return err
	}
	if err := s.updateBaseURL("https://" + domain); err != nil {
		restore(ctx)
		return err
	}
	s.audit.Record(actor, "panel.domain.configure", "panel", domain, ip, map[string]string{"base_url": "https://" + domain})
	return nil
}

func (s *PanelDomainService) Remove(ctx context.Context, actor *uint, ip string) error {
	for _, path := range []string{s.panelAvailablePath(), s.panelEnabledPath()} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if s.cfg.Features.EnableNginxManage && s.adapter != nil {
		if err := s.adapter.Nginx().Validate(ctx, s.cfg.Paths.NginxBinary); err != nil {
			return err
		}
		if err := s.adapter.Nginx().Reload(ctx, s.cfg.Paths.NginxBinary); err != nil {
			return err
		}
	}
	baseURL := fmt.Sprintf("http://localhost:%d", s.cfg.App.Port)
	if err := s.updateBaseURL(baseURL); err != nil {
		return err
	}
	s.audit.Record(actor, "panel.domain.remove", "panel", "", ip, map[string]string{"base_url": baseURL})
	return nil
}

func (s *PanelDomainService) writeAndReload(ctx context.Context, content string) error {
	available := s.panelAvailablePath()
	enabled := s.panelEnabledPath()
	previous, readErr := os.ReadFile(available)
	hadPrevious := readErr == nil
	if err := utils.WriteFileAtomic(available, []byte(content), 0o644); err != nil {
		return err
	}
	if err := s.enableConfig(available, enabled); err != nil {
		return err
	}
	if err := s.adapter.Nginx().Validate(ctx, s.cfg.Paths.NginxBinary); err != nil {
		if hadPrevious {
			_ = utils.WriteFileAtomic(available, previous, 0o644)
		}
		return err
	}
	return s.adapter.Nginx().Reload(ctx, s.cfg.Paths.NginxBinary)
}

func (s *PanelDomainService) restorePoint() func(context.Context) {
	available := s.panelAvailablePath()
	enabled := s.panelEnabledPath()
	previous, readErr := os.ReadFile(available)
	hadPrevious := readErr == nil
	enabledTarget, linkErr := os.Readlink(enabled)
	hadEnabled := linkErr == nil
	return func(ctx context.Context) {
		if hadPrevious {
			_ = utils.WriteFileAtomic(available, previous, 0o644)
		} else {
			_ = os.Remove(available)
		}
		_ = os.Remove(enabled)
		if hadEnabled {
			_ = os.Symlink(enabledTarget, enabled)
		}
		if s.adapter != nil && s.cfg.Features.EnableNginxManage {
			if err := s.adapter.Nginx().Validate(ctx, s.cfg.Paths.NginxBinary); err == nil {
				_ = s.adapter.Nginx().Reload(ctx, s.cfg.Paths.NginxBinary)
			}
		}
	}
}

func (s *PanelDomainService) enableConfig(source, link string) error {
	if filepath.Clean(source) == filepath.Clean(link) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if existing, err := os.Readlink(link); err == nil && existing == source {
		return nil
	}
	_ = os.Remove(link)
	return os.Symlink(source, link)
}

func (s *PanelDomainService) preflightHTTPChallenge(ctx context.Context, domain, webroot string) error {
	tokenBytes := make([]byte, 12)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := "deploycp-panel-" + hex.EncodeToString(tokenBytes)
	body := []byte("ok-" + token)
	path := filepath.Join(webroot, ".well-known", "acme-challenge", token)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write ACME preflight token: %w", err)
	}
	defer os.Remove(path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+domain+"/.well-known/acme-challenge/"+token, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("panel domain HTTP challenge is not reachable for %s: %w", domain, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("panel domain HTTP challenge returned status %d for %s; confirm DNS points here and port 80 is open", resp.StatusCode, domain)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != string(body) {
		return fmt.Errorf("panel domain HTTP challenge did not return the expected token for %s; confirm DNS points here and nginx is serving the DeployCP panel config", domain)
	}
	return nil
}

func (s *PanelDomainService) updateBaseURL(baseURL string) error {
	if s.settings != nil {
		_ = s.settings.repo.Set("panel_base_url", baseURL, false)
	}
	s.cfg.App.BaseURL = baseURL
	_ = os.Setenv("APP_BASE_URL", baseURL)
	return setEnvValue(resolveEnvFilePath(), "APP_BASE_URL", baseURL)
}

func (s *PanelDomainService) panelWebroot() string {
	return filepath.Join(s.cfg.Paths.StorageRoot, "generated", "panel-acme")
}

func (s *PanelDomainService) panelAvailablePath() string {
	return filepath.Join(s.cfg.Paths.NginxAvailableDir, panelDomainConfigName)
}

func (s *PanelDomainService) panelEnabledPath() string {
	return filepath.Join(s.cfg.Paths.NginxEnabledDir, panelDomainConfigName)
}

func renderPanelNginxConfig(cfg *config.Config, domain, webroot, certPath, keyPath string) string {
	hasCert := strings.TrimSpace(certPath) != "" && strings.TrimSpace(keyPath) != ""
	var b strings.Builder
	b.WriteString("# Managed by DeployCP. Do not edit.\n")
	b.WriteString("server {\n")
	b.WriteString("    listen 80;\n")
	b.WriteString("    listen [::]:80;\n")
	b.WriteString(fmt.Sprintf("    server_name %s;\n", domain))
	b.WriteString(fmt.Sprintf("    access_log %s;\n", panelNginxAccessLogPath(cfg)))
	b.WriteString(fmt.Sprintf("    error_log %s warn;\n", panelNginxErrorLogPath(cfg)))
	b.WriteString("    location ^~ /.well-known/acme-challenge/ {\n")
	b.WriteString(fmt.Sprintf("        root %s;\n", webroot))
	b.WriteString("        default_type text/plain;\n")
	b.WriteString("        allow all;\n")
	b.WriteString("    }\n")
	if hasCert {
		b.WriteString("    location / {\n")
		b.WriteString("        return 301 https://$host$request_uri;\n")
		b.WriteString("    }\n")
	} else {
		writePanelProxy(&b, cfg)
	}
	b.WriteString("}\n")
	if hasCert {
		b.WriteString("\nserver {\n")
		b.WriteString("    listen 443 ssl http2;\n")
		b.WriteString("    listen [::]:443 ssl http2;\n")
		b.WriteString(fmt.Sprintf("    server_name %s;\n", domain))
		b.WriteString(fmt.Sprintf("    access_log %s;\n", panelNginxAccessLogPath(cfg)))
		b.WriteString(fmt.Sprintf("    error_log %s warn;\n", panelNginxErrorLogPath(cfg)))
		b.WriteString(fmt.Sprintf("    ssl_certificate %s;\n", certPath))
		b.WriteString(fmt.Sprintf("    ssl_certificate_key %s;\n", keyPath))
		b.WriteString("    ssl_session_cache shared:deploycp_panel_ssl:10m;\n")
		b.WriteString("    ssl_session_timeout 10m;\n")
		writePanelProxy(&b, cfg)
		b.WriteString("}\n")
	}
	return b.String()
}

func writePanelProxy(b *strings.Builder, cfg *config.Config) {
	port := cfg.App.Port
	if port <= 0 {
		port = 2024
	}
	b.WriteString("    location / {\n")
	b.WriteString(fmt.Sprintf("        proxy_pass http://127.0.0.1:%d;\n", port))
	b.WriteString("        proxy_http_version 1.1;\n")
	b.WriteString("        proxy_set_header Host $host;\n")
	b.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
	b.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	b.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
	b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
	b.WriteString("        proxy_set_header Connection \"upgrade\";\n")
	b.WriteString("    }\n")
}

func panelLogDir(cfg *config.Config) string {
	root := strings.TrimSpace(cfg.Paths.LogRoot)
	if root == "" {
		root = filepath.Join(cfg.Paths.StorageRoot, "logs")
	}
	return filepath.Join(root, "panel")
}

func panelNginxAccessLogPath(cfg *config.Config) string {
	return filepath.Join(panelLogDir(cfg), "nginx-access.log")
}

func panelNginxErrorLogPath(cfg *config.Config) string {
	return filepath.Join(panelLogDir(cfg), "nginx-error.log")
}

func validatePanelDomain(domain string) error {
	if err := validators.ValidateDomains([]string{domain}); err != nil {
		return err
	}
	if strings.Contains(domain, "*") {
		return fmt.Errorf("wildcard domains are not supported for panel identity")
	}
	if net.ParseIP(domain) != nil {
		return fmt.Errorf("panel identity must be a domain, not an IP address")
	}
	if !strings.Contains(domain, ".") {
		return fmt.Errorf("panel identity must be a fully qualified domain")
	}
	return nil
}

func NormalizePanelDomain(domain string) string {
	domain = strings.TrimSpace(strings.ToLower(domain))
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimSuffix(strings.Split(domain, "/")[0], ".")
	if host, _, err := net.SplitHostPort(domain); err == nil {
		domain = host
	}
	return domain
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func resolveEnvFilePath() string {
	if explicit := strings.TrimSpace(os.Getenv("DEPLOYCP_ENV_FILE")); explicit != "" {
		return explicit
	}
	return ".env"
}

func setEnvValue(path, key, value string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return utils.WriteFileAtomic(path, []byte(fmt.Sprintf("%s=%s\n", key, value)), 0o600)
		}
		return err
	}
	lines := strings.Split(string(raw), "\n")
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			lines[i] = key + "=" + value
			replaced = true
		}
	}
	if !replaced {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines[len(lines)-1] = key + "=" + value
			lines = append(lines, "")
		} else {
			lines = append(lines, key+"="+value)
		}
	}
	return utils.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")), 0o600)
}
