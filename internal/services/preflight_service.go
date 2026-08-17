package services

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/sftp"

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

func (s *PreflightService) Run(ctx context.Context) PreflightReport {
	if ctx == nil {
		ctx = context.Background()
	}
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
		{"sudo", "sudo", true},
		{"visudo", "visudo", true},
		{"bubblewrap", "bwrap", true},
		{"setpriv", "setpriv", true},
		{"sshd", "sshd", true},
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
	if s.cfg.Features.PlatformMode != "dryrun" && runtime.GOOS == "linux" {
		if s.platform != nil && s.cfg.Features.EnableNginxManage {
			if err := s.platform.Nginx().Validate(ctx, s.cfg.Paths.NginxBinary); err != nil {
				add("nginx_config", "fail", err.Error())
			} else {
				add("nginx_config", "ok", "nginx configuration is valid")
			}
		}
		if sshd := firstExecutable("sshd", "/usr/sbin/sshd", "/usr/local/sbin/sshd"); sshd == "" {
			add("sshd_config", "fail", "sshd binary not found")
		} else {
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			output, err := exec.CommandContext(checkCtx, sshd, "-t").CombinedOutput()
			cancel()
			if err != nil {
				add("sshd_config", "fail", commandFailureDetail(err, output))
			} else {
				add("sshd_config", "ok", "sshd configuration is valid")
			}
		}
		if detail, err := s.restrictedShellSmokeTest(ctx); err != nil {
			add("restricted_ssh", "fail", err.Error())
		} else if detail != "" {
			add("restricted_ssh", "ok", detail)
		} else {
			add("restricted_ssh", "warn", "no active SSH-enabled site user is available for a sandbox smoke test")
		}
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
		{"logrotate_config", "/etc/logrotate.d/deploycp"},
		{"backup_cron", "/etc/cron.d/deploycp-backup"},
		{"fail2ban_jail", "/etc/fail2ban/jail.d/deploycp.local"},
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
	if status := selinuxStatus(); status == "" {
		add("selinux", "warn", "SELinux not detected or disabled")
	} else {
		add("selinux", "ok", status)
	}
	if status := appArmorStatus(); status == "" {
		add("apparmor", "warn", "AppArmor not detected or disabled")
	} else {
		add("apparmor", "ok", status)
	}
	if fail2banStatus := serviceUnitState("fail2ban"); fail2banStatus == "" {
		add("service:fail2ban", "warn", "fail2ban service not active")
	} else {
		add("service:fail2ban", "ok", fail2banStatus)
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

	if strings.TrimSpace(s.cfg.Managed.MariaDBAdminUser) == "" {
		add("managed:mariadb_admin", "warn", "managed MariaDB provisioning disabled — MARIADB_ADMIN_USER not set")
	} else if strings.TrimSpace(s.cfg.Managed.MariaDBAdminPass) == "" {
		add("managed:mariadb_admin", "ok", "configured (socket auth)")
	} else {
		add("managed:mariadb_admin", "ok", "configured")
	}
	if strings.TrimSpace(s.cfg.Managed.PostgresAdminUser) == "" {
		add("managed:postgres_admin", "warn", "managed PostgreSQL provisioning disabled — POSTGRES_ADMIN_USER not set")
	} else if strings.TrimSpace(s.cfg.Managed.PostgresAdminPass) == "" {
		add("managed:postgres_admin", "ok", "configured (peer/socket auth)")
	} else {
		add("managed:postgres_admin", "ok", "configured")
	}

	return report
}

func firstExecutable(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.ContainsRune(candidate, filepath.Separator) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
				return candidate
			}
			continue
		}
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	return ""
}

func commandFailureDetail(err error, output []byte) string {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return err.Error()
	}
	if len(detail) > 600 {
		detail = detail[:600] + "..."
	}
	return fmt.Sprintf("%v: %s", err, detail)
}

func (s *PreflightService) restrictedShellSmokeTest(ctx context.Context) (string, error) {
	if s.repos == nil || s.repos.SiteUsers == nil {
		return "", nil
	}
	users, err := s.repos.SiteUsers.List()
	if err != nil {
		return "", fmt.Errorf("list SSH users: %w", err)
	}
	username := ""
	homeDirectory := ""
	for i := range users {
		if users[i].IsActive && users[i].SSHEnabled && strings.TrimSpace(users[i].Username) != "" {
			username = strings.TrimSpace(users[i].Username)
			homeDirectory = filepath.Clean(strings.TrimSpace(users[i].HomeDirectory))
			break
		}
	}
	if username == "" {
		return "", nil
	}
	sudo := firstExecutable("sudo", "/usr/bin/sudo", "/bin/sudo")
	if sudo == "" {
		return "", fmt.Errorf("sudo is unavailable for SSH sandbox smoke test")
	}
	shellPath := filepath.Clean(strings.TrimSpace(s.cfg.Paths.RestrictedShellPath))
	if shellPath == "." || !filepath.IsAbs(shellPath) {
		return "", fmt.Errorf("restricted shell path is invalid")
	}
	testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	cmd := restrictedShellTestCommand(testCtx, sudo, username, shellPath, "")
	cmd.Stdin = strings.NewReader("exit\n")
	output, err := cmd.CombinedOutput()
	cancel()
	if testCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("restricted SSH sandbox timed out for %s", username)
	}
	if err != nil {
		return "", fmt.Errorf("restricted SSH sandbox failed for %s: %s", username, commandFailureDetail(err, output))
	}

	sftpCtx, cancelSFTP := context.WithTimeout(ctx, 20*time.Second)
	sftpCmd := restrictedShellTestCommand(sftpCtx, sudo, username, shellPath, "internal-sftp")
	sftpStdout, err := sftpCmd.StdoutPipe()
	if err != nil {
		cancelSFTP()
		return "", fmt.Errorf("prepare SFTP smoke test for %s: %w", username, err)
	}
	sftpStdin, err := sftpCmd.StdinPipe()
	if err != nil {
		cancelSFTP()
		return "", fmt.Errorf("prepare SFTP input for %s: %w", username, err)
	}
	var sftpStderr bytes.Buffer
	sftpCmd.Stderr = &sftpStderr
	if err := sftpCmd.Start(); err != nil {
		cancelSFTP()
		return "", fmt.Errorf("start SFTP smoke test for %s: %w", username, err)
	}
	sftpClient, err := sftp.NewClientPipe(sftpStdout, sftpStdin)
	if err == nil {
		_, err = sftpClient.ReadDir(".")
		_ = sftpClient.Close()
	}
	waitErr := sftpCmd.Wait()
	timedOut := sftpCtx.Err() == context.DeadlineExceeded
	cancelSFTP()
	if timedOut {
		return "", fmt.Errorf("SFTP smoke test timed out for %s", username)
	}
	if err != nil {
		return "", fmt.Errorf("SFTP smoke test failed for %s: %w: %s", username, err, strings.TrimSpace(sftpStderr.String()))
	}
	if waitErr != nil {
		return "", fmt.Errorf("SFTP helper failed for %s: %s", username, commandFailureDetail(waitErr, sftpStderr.Bytes()))
	}

	if homeDirectory == "." || !filepath.IsAbs(homeDirectory) {
		return "", fmt.Errorf("SSH home directory is invalid for %s", username)
	}
	smokeName := fmt.Sprintf(".deploycp-scp-smoke-%d", time.Now().UnixNano())
	smokePath := filepath.Join(homeDirectory, smokeName)
	defer os.Remove(smokePath)
	scpCtx, cancelSCP := context.WithTimeout(ctx, 20*time.Second)
	scpCmd := restrictedShellTestCommand(scpCtx, sudo, username, shellPath, "scp -t .")
	scpCmd.Stdin = bytes.NewReader(append([]byte(fmt.Sprintf("C0600 0 %s\n", smokeName)), 0))
	scpOutput, scpErr := scpCmd.CombinedOutput()
	scpTimedOut := scpCtx.Err() == context.DeadlineExceeded
	cancelSCP()
	if scpTimedOut {
		return "", fmt.Errorf("legacy SCP smoke test timed out for %s", username)
	}
	if scpErr != nil {
		return "", fmt.Errorf("legacy SCP smoke test failed for %s: %s", username, commandFailureDetail(scpErr, scpOutput))
	}
	if info, err := os.Stat(smokePath); err != nil || info.Size() != 0 {
		if err == nil {
			err = fmt.Errorf("unexpected smoke file size %d", info.Size())
		}
		return "", fmt.Errorf("legacy SCP did not write inside %s home: %w", username, err)
	}
	return "interactive SSH, SFTP, and legacy SCP launched successfully for " + username, nil
}

func restrictedShellTestCommand(ctx context.Context, sudo, username, shellPath, originalCommand string) *exec.Cmd {
	return exec.CommandContext(ctx, sudo, "-n", "-u", username, "-H", "/usr/bin/env", "TERM=xterm-256color", "SSH_ORIGINAL_COMMAND="+originalCommand, shellPath)
}

func serviceUnitState(name string) string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return ""
	}
	out, err := exec.Command("systemctl", "is-active", name).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func selinuxStatus() string {
	if _, err := exec.LookPath("getenforce"); err == nil {
		out, cmdErr := exec.Command("getenforce").CombinedOutput()
		if cmdErr == nil {
			status := strings.TrimSpace(string(out))
			if status != "" && !strings.EqualFold(status, "disabled") {
				return "SELinux " + status
			}
		}
	}
	if data, err := os.ReadFile("/sys/fs/selinux/enforce"); err == nil {
		switch strings.TrimSpace(string(data)) {
		case "1":
			return "SELinux Enforcing"
		case "0":
			return "SELinux Permissive"
		}
	}
	return ""
}

func appArmorStatus() string {
	if data, err := os.ReadFile("/sys/module/apparmor/parameters/enabled"); err == nil {
		if strings.EqualFold(strings.TrimSpace(string(data)), "Y") {
			return "AppArmor enabled"
		}
	}
	return ""
}
