package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/system"
	"deploycp/internal/utils"
)

type VarnishService struct {
	cfg    *config.Config
	runner *system.Runner
	audit  *AuditService
}

func NewVarnishService(cfg *config.Config, runner *system.Runner, audit *AuditService) *VarnishService {
	return &VarnishService{cfg: cfg, runner: runner, audit: audit}
}

func (s *VarnishService) ApplyWebsiteConfig(ctx context.Context, site *models.Website, cfgItem *models.VarnishConfig, actor *uint, ip string) error {
	if site == nil || cfgItem == nil {
		return fmt.Errorf("website and varnish config are required")
	}
	path := s.configPath(site.ID)
	if !cfgItem.Enabled {
		if err := s.purgeWebsiteCache(ctx, site, actor, ip); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := s.syncAggregateVCL(); err != nil {
			return err
		}
		return s.reload(ctx, actor, ip)
	}
	if err := utils.WriteFileAtomic(path, []byte(renderVarnishSiteConfig(site, cfgItem)), 0o644); err != nil {
		return err
	}
	if err := s.syncAggregateVCL(); err != nil {
		return err
	}
	s.audit.Record(actor, "varnish.apply", "website", fmt.Sprintf("%d", site.ID), ip, map[string]any{"enabled": cfgItem.Enabled})
	return s.reload(ctx, actor, ip)
}

func (s *VarnishService) DeleteWebsiteConfig(ctx context.Context, site *models.Website, actor *uint, ip string) error {
	if site == nil {
		return fmt.Errorf("website is required")
	}
	if err := s.purgeWebsiteCache(ctx, site, actor, ip); err != nil {
		return err
	}
	if err := os.Remove(s.configPath(site.ID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.syncAggregateVCL(); err != nil {
		return err
	}
	s.audit.Record(actor, "varnish.delete", "website", fmt.Sprintf("%d", site.ID), ip, nil)
	return s.reload(ctx, actor, ip)
}

func (s *VarnishService) configPath(websiteID uint) string {
	return filepath.Join(s.cfg.Managed.VarnishConfigDir, fmt.Sprintf("website-%d.vcl", websiteID))
}

func (s *VarnishService) aggregatePath() string {
	if strings.TrimSpace(s.cfg.Managed.VarnishIncludeVCL) != "" {
		return s.cfg.Managed.VarnishIncludeVCL
	}
	return filepath.Join(s.cfg.Managed.VarnishConfigDir, "deploycp.vcl")
}

func (s *VarnishService) reload(ctx context.Context, actor *uint, ip string) error {
	if s.cfg.Features.PlatformMode == "dryrun" || strings.TrimSpace(s.cfg.Managed.VarnishServiceName) == "" {
		return nil
	}
	if err := s.validate(ctx, actor, ip); err != nil {
		return err
	}
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      s.cfg.Paths.SystemctlBinary,
		Args:        []string{"reload", s.cfg.Managed.VarnishServiceName},
		Timeout:     20 * time.Second,
		AuditAction: "varnish.reload",
		ActorUserID: actor,
		IP:          ip,
	})
	return err
}

func (s *VarnishService) syncAggregateVCL() error {
	if err := os.MkdirAll(s.cfg.Managed.VarnishConfigDir, 0o755); err != nil {
		return err
	}
	entries, err := filepath.Glob(filepath.Join(s.cfg.Managed.VarnishConfigDir, "website-*.vcl"))
	if err != nil {
		return err
	}
	sort.Strings(entries)
	body := strings.Builder{}
	body.WriteString("sub deploycp_recv {\n")
	for _, entry := range entries {
		body.WriteString(fmt.Sprintf("    call deploycp_recv_%s;\n", snippetIDFromPath(entry)))
	}
	body.WriteString("}\n\n")
	body.WriteString("sub deploycp_backend_response {\n")
	for _, entry := range entries {
		body.WriteString(fmt.Sprintf("    call deploycp_backend_response_%s;\n", snippetIDFromPath(entry)))
	}
	body.WriteString("}\n")
	if len(entries) > 0 {
		body.WriteString("\n")
		for _, entry := range entries {
			body.WriteString(fmt.Sprintf("include %q;\n", entry))
		}
	}
	return utils.WriteFileAtomic(s.aggregatePath(), []byte(body.String()), 0o644)
}

func (s *VarnishService) validate(ctx context.Context, actor *uint, ip string) error {
	mainVCL := strings.TrimSpace(s.cfg.Managed.VarnishMainVCL)
	binary := strings.TrimSpace(s.cfg.Managed.VarnishdBinary)
	if mainVCL == "" || binary == "" {
		return nil
	}
	if _, err := os.Stat(mainVCL); err != nil {
		return nil
	}
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      binary,
		Args:        []string{"-C", "-f", mainVCL},
		Timeout:     20 * time.Second,
		AuditAction: "varnish.validate",
		ActorUserID: actor,
		IP:          ip,
	})
	return err
}

func (s *VarnishService) purgeWebsiteCache(ctx context.Context, site *models.Website, actor *uint, ip string) error {
	if site == nil || s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	binary := strings.TrimSpace(s.cfg.Managed.VarnishadmBinary)
	addr := strings.TrimSpace(s.cfg.Managed.VarnishAdminAddr)
	secret := strings.TrimSpace(s.cfg.Managed.VarnishSecretFile)
	if binary == "" || addr == "" || secret == "" {
		return nil
	}
	if _, err := os.Stat(binary); err != nil {
		return nil
	}
	hostExpr := renderVarnishHostPattern(site)
	if hostExpr == "" {
		return nil
	}
	_, err := s.runner.Run(ctx, system.CommandRequest{
		Binary:      binary,
		Args:        []string{"-T", addr, "-S", secret, "ban", fmt.Sprintf("req.http.host ~ %q", hostExpr)},
		Timeout:     20 * time.Second,
		AuditAction: "varnish.ban",
		ActorUserID: actor,
		IP:          ip,
	})
	if err == nil {
		s.audit.Record(actor, "varnish.ban", "website", fmt.Sprintf("%d", site.ID), ip, map[string]any{"host_pattern": hostExpr})
	}
	return err
}

func snippetIDFromPath(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return strings.ReplaceAll(base, "-", "_")
}

func renderVarnishHostPattern(site *models.Website) string {
	if site == nil {
		return ""
	}
	hosts := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		if strings.TrimSpace(d.Domain) == "" {
			continue
		}
		hosts = append(hosts, regexp.QuoteMeta(strings.TrimSpace(d.Domain)))
	}
	if len(hosts) == 0 {
		hosts = append(hosts, regexp.QuoteMeta(site.Name))
	}
	return "(?i)^(" + strings.Join(hosts, "|") + ")$"
}

func renderVarnishSiteConfig(site *models.Website, cfgItem *models.VarnishConfig) string {
	hostPattern := renderVarnishHostPattern(site)

	excludes := []string{}
	for _, line := range strings.FieldsFunc(cfgItem.Excludes, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line != "" {
			excludes = append(excludes, line)
		}
	}

	params := strings.TrimSpace(cfgItem.ExcludedParams)
	if params == "" {
		params = "__SID,noCache"
	}
	paramNames := make([]string, 0)
	for _, part := range strings.FieldsFunc(params, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		paramNames = append(paramNames, regexp.QuoteMeta(part))
	}

	snippetID := fmt.Sprintf("website_%d", site.ID)
	lines := []string{
		fmt.Sprintf("# DeployCP managed varnish rules for website %d (%s)", site.ID, site.Name),
		fmt.Sprintf("# Generated at %s", time.Now().Format(time.RFC3339)),
		fmt.Sprintf("# Desired varnish frontend/listen target: %s", cfgItem.Server),
		fmt.Sprintf("sub deploycp_recv_%s {", snippetID),
		fmt.Sprintf("    if (req.http.host ~ %q) {", hostPattern),
		"        if (req.method != \"GET\" && req.method != \"HEAD\") {",
		"            return (pass);",
		"        }",
		"        if (req.http.Authorization || req.http.Cookie) {",
		"            return (pass);",
		"        }",
	}
	if len(paramNames) > 0 {
		lines = append(lines, fmt.Sprintf("        if (req.url ~ %q) {", "([?&])("+strings.Join(paramNames, "|")+")="))
		lines = append(lines, "            return (pass);")
		lines = append(lines, "        }")
	}
	for _, item := range excludes {
		lines = append(lines, fmt.Sprintf("        if (req.url ~ %q) {", item))
		lines = append(lines, "            return (pass);")
		lines = append(lines, "        }")
	}
	lines = append(lines, fmt.Sprintf("        set req.http.X-DeployCP-Website = %q;", fmt.Sprintf("%d", site.ID)))
	if strings.TrimSpace(cfgItem.CacheTagPrefix) != "" {
		lines = append(lines, fmt.Sprintf("        set req.http.X-DeployCP-Cache-Tag-Prefix = %q;", strings.TrimSpace(cfgItem.CacheTagPrefix)))
	}
	lines = append(lines,
		"    }",
		"}",
		"",
		fmt.Sprintf("sub deploycp_backend_response_%s {", snippetID),
		fmt.Sprintf("    if (bereq.http.X-DeployCP-Website == %q) {", fmt.Sprintf("%d", site.ID)),
		"        if (beresp.http.Set-Cookie) {",
		"            set beresp.uncacheable = true;",
		"            set beresp.ttl = 0s;",
		"            return (deliver);",
		"        }",
		fmt.Sprintf("        set beresp.ttl = %ds;", varnishPositiveInt(cfgItem.CacheLifetime, 1)),
		"        set beresp.grace = 1h;",
		"        if (bereq.http.X-DeployCP-Cache-Tag-Prefix) {",
		"            set beresp.http.X-DeployCP-Cache-Tag-Prefix = bereq.http.X-DeployCP-Cache-Tag-Prefix;",
		"        }",
		"    }",
		"}",
	)
	return strings.Join(lines, "\n") + "\n"
}

func varnishPositiveInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
