package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"deploycp/internal/config"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
)

type PreflightCheck struct {
	Name   string
	Status string
	Detail string
}

type PreflightReport struct {
	Checks []PreflightCheck
}

func (r PreflightReport) HasFailures() bool {
	for _, item := range r.Checks {
		if item.Status == "fail" {
			return true
		}
	}
	return false
}

type PreflightService struct {
	cfg      *config.Config
	repos    *repositories.Repositories
	platform platform.Adapter
}

func NewPreflightService(cfg *config.Config, repos *repositories.Repositories, platform platform.Adapter) *PreflightService {
	return &PreflightService{cfg: cfg, repos: repos, platform: platform}
}

func (s *PreflightService) Run(_ context.Context) PreflightReport {
	report := PreflightReport{}
	add := func(name, status, detail string) {
		report.Checks = append(report.Checks, PreflightCheck{Name: name, Status: status, Detail: detail})
	}

	if s.cfg.Features.PlatformMode == "dryrun" {
		add("platform_mode", "warn", "running in dryrun; production host verification is limited")
	} else if runtime.GOOS != "linux" {
		add("platform_mode", "fail", "production mode is intended for Linux hosts")
	} else {
		add("platform_mode", "ok", "linux production mode")
	}

	if os.Geteuid() == 0 {
		add("effective_user", "ok", "running with root privileges")
	} else {
		add("effective_user", "fail", "service should run as root to manage users, /etc, firewall, certbot, and cron")
	}

	requiredBinaries := []struct {
		name string
		path string
		hard bool
	}{
		{"nginx", s.cfg.Paths.NginxBinary, true},
		{"systemctl", s.cfg.Paths.SystemctlBinary, true},
		{"runuser", s.cfg.Paths.RunuserBinary, true},
		{"certbot", s.cfg.Paths.CertbotBinary, false},
		{"redis-server", s.cfg.Managed.RedisServerBinary, false},
		{"varnishd", s.cfg.Managed.VarnishdBinary, false},
	}
	for _, item := range requiredBinaries {
		if _, err := exec.LookPath(item.path); err != nil {
			if item.hard {
				add("binary:"+item.name, "fail", fmt.Sprintf("missing %s at %s", item.name, item.path))
			} else {
				add("binary:"+item.name, "warn", fmt.Sprintf("optional binary missing at %s", item.path))
			}
			continue
		}
		add("binary:"+item.name, "ok", item.path)
	}

	firewallAvailable := false
	for _, candidate := range []string{s.cfg.Paths.UFWBinary, s.cfg.Paths.FirewallCMDBinary, s.cfg.Paths.IPTablesBinary} {
		if _, err := exec.LookPath(candidate); err == nil {
			firewallAvailable = true
			add("firewall_backend", "ok", "found "+candidate)
			break
		}
	}
	if !firewallAvailable {
		add("firewall_backend", "warn", "no ufw/firewall-cmd/iptables binary found")
	}
	packageManager := ""
	for _, candidate := range []string{"apt-get", "dnf", "yum", "zypper", "pacman"} {
		if _, err := exec.LookPath(candidate); err == nil {
			packageManager = candidate
			break
		}
	}
	if packageManager == "" {
		add("package_manager", "warn", "no supported linux package manager found")
	} else {
		add("package_manager", "ok", "found "+packageManager)
	}

	for _, dir := range []struct {
		name string
		path string
	}{
		{"storage_root", s.cfg.Paths.StorageRoot},
		{"site_root", s.cfg.Paths.DefaultSiteRoot},
		{"log_root", s.cfg.Paths.LogRoot},
		{"runtime_root", s.cfg.Paths.RuntimeRoot},
		{"cron_dir", s.cfg.Paths.CronDir},
		{"proftpd_conf_dir", s.cfg.Managed.ProFTPDConfDir},
		{"varnish_config_dir", s.cfg.Managed.VarnishConfigDir},
	} {
		if strings.TrimSpace(dir.path) == "" {
			add("dir:"+dir.name, "warn", "not configured")
			continue
		}
		if st, err := os.Stat(dir.path); err != nil || !st.IsDir() {
			add("dir:"+dir.name, "warn", "missing directory "+dir.path)
			continue
		}
		add("dir:"+dir.name, "ok", dir.path)
	}
	for _, file := range []struct {
		name string
		path string
	}{
		{"varnish_main_vcl", s.cfg.Managed.VarnishMainVCL},
		{"varnish_include_vcl", s.cfg.Managed.VarnishIncludeVCL},
	} {
		if strings.TrimSpace(file.path) == "" {
			add("file:"+file.name, "warn", "not configured")
			continue
		}
		if _, err := os.Stat(file.path); err != nil {
			add("file:"+file.name, "warn", "missing file "+file.path)
			continue
		}
		add("file:"+file.name, "ok", file.path)
	}
	if mainVCL := strings.TrimSpace(s.cfg.Managed.VarnishMainVCL); mainVCL != "" {
		if content, err := os.ReadFile(mainVCL); err == nil {
			text := string(content)
			includePath := strings.TrimSpace(s.cfg.Managed.VarnishIncludeVCL)
			if includePath != "" && strings.Contains(text, includePath) {
				add("varnish:include_hook", "ok", "main VCL includes DeployCP managed VCL")
			} else {
				add("varnish:include_hook", "warn", "main VCL does not include the DeployCP managed VCL")
			}
			if strings.Contains(text, "call deploycp_recv;") {
				add("varnish:recv_hook", "ok", "main VCL calls deploycp_recv")
			} else {
				add("varnish:recv_hook", "warn", "main VCL is missing call deploycp_recv;")
			}
			if strings.Contains(text, "call deploycp_backend_response;") {
				add("varnish:backend_response_hook", "ok", "main VCL calls deploycp_backend_response")
			} else {
				add("varnish:backend_response_hook", "warn", "main VCL is missing call deploycp_backend_response;")
			}
		}
	}

	if s.repos != nil {
		if sites, err := s.repos.Websites.Count(); err == nil {
			add("db:platforms", "ok", fmt.Sprintf("%d platform rows", sites))
		}
		if redisCount, err := s.repos.Redis.Count(); err == nil {
			add("db:redis_connections", "ok", fmt.Sprintf("%d redis connections", redisCount))
		}
		if dbCount, err := s.repos.Databases.Count(); err == nil {
			add("db:database_connections", "ok", fmt.Sprintf("%d database connections", dbCount))
		}
	}

	if strings.TrimSpace(s.cfg.Managed.MariaDBAdminUser) == "" || strings.TrimSpace(s.cfg.Managed.MariaDBAdminPass) == "" {
		add("managed:mariadb_admin", "warn", "managed MariaDB provisioning disabled until admin credentials are configured")
	} else {
		add("managed:mariadb_admin", "ok", "configured")
	}
	if strings.TrimSpace(s.cfg.Managed.PostgresAdminUser) == "" || strings.TrimSpace(s.cfg.Managed.PostgresAdminPass) == "" {
		add("managed:postgres_admin", "warn", "managed PostgreSQL provisioning disabled until admin credentials are configured")
	} else {
		add("managed:postgres_admin", "ok", "configured")
	}

	return report
}
