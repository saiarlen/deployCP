package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/platform"
	"deploycp/internal/restrictedtransfer"
	"deploycp/internal/system"
	"deploycp/internal/utils"
)

type adapter struct {
	services *serviceManager
	users    *userManager
	nginx    *nginxManager
}

func New(cfg *config.Config, runner *system.Runner) platform.Adapter {
	return &adapter{
		services: &serviceManager{cfg: cfg, runner: runner},
		users:    &userManager{cfg: cfg, runner: runner},
		nginx:    &nginxManager{runner: runner},
	}
}

func (a *adapter) Name() string                      { return "linux" }
func (a *adapter) Services() platform.ServiceManager { return a.services }
func (a *adapter) Users() platform.UserManager       { return a.users }
func (a *adapter) Nginx() platform.NginxManager      { return a.nginx }

type serviceManager struct {
	cfg    *config.Config
	runner *system.Runner
}

func (m *serviceManager) Install(ctx context.Context, def platform.ServiceDefinition) (string, error) {
	if err := validateServiceDefinition(def); err != nil {
		return "", err
	}
	unitPath := filepath.Join("/etc/systemd/system", def.Name+".service")
	content := renderUnit(def)
	if err := utils.WriteFileAtomic(unitPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	if err := m.verifyUnitFile(ctx, unitPath); err != nil {
		return "", err
	}
	if _, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"daemon-reload"}, Timeout: 10 * time.Second, AuditAction: "systemd.daemon_reload"}); err != nil {
		return "", err
	}
	return unitPath, nil
}

func (m *serviceManager) Start(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"start", name}, Timeout: 15 * time.Second, AuditAction: "service.start"})
	if err == nil {
		return nil
	}
	status, _ := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"status", name, "--no-pager", "-l"}, Timeout: 8 * time.Second})
	detail := strings.TrimSpace(status.Stdout + "\n" + status.Stderr)
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, detail)
}
func (m *serviceManager) Stop(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"stop", name}, Timeout: 15 * time.Second, AuditAction: "service.stop"})
	return err
}
func (m *serviceManager) Restart(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"restart", name}, Timeout: 20 * time.Second, AuditAction: "service.restart"})
	return err
}
func (m *serviceManager) Enable(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"enable", name}, Timeout: 10 * time.Second, AuditAction: "service.enable"})
	return err
}
func (m *serviceManager) Disable(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"disable", name}, Timeout: 10 * time.Second, AuditAction: "service.disable"})
	return err
}
func (m *serviceManager) Status(ctx context.Context, name string) (platform.ServiceStatus, error) {
	res, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"is-active", name}, Timeout: 5 * time.Second})
	if err != nil {
		return platform.ServiceStatus{Name: name, Active: false, RawOutput: strings.TrimSpace(res.Stdout + res.Stderr)}, nil
	}
	enRes, _ := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"is-enabled", name}, Timeout: 5 * time.Second})
	status := platform.ServiceStatus{Name: name, Active: strings.TrimSpace(res.Stdout) == "active", Enabled: strings.TrimSpace(enRes.Stdout) == "enabled", RawOutput: strings.TrimSpace(res.Stdout)}
	if status.Active {
		status.SubState = "running"
	}
	return status, nil
}
func (m *serviceManager) Logs(ctx context.Context, name string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	res, err := m.runner.Run(ctx, system.CommandRequest{Binary: "/bin/journalctl", Args: []string{"-u", name, "-n", fmt.Sprintf("%d", lines), "--no-pager"}, Timeout: 8 * time.Second})
	if err != nil {
		return res.Stdout + "\n" + res.Stderr, err
	}
	return res.Stdout, nil
}

func (m *serviceManager) verifyUnitFile(ctx context.Context, unitPath string) error {
	binary, err := exec.LookPath("systemd-analyze")
	if err != nil {
		return nil
	}
	_, err = m.runner.Run(ctx, system.CommandRequest{
		Binary:      binary,
		Args:        []string{"verify", unitPath},
		Timeout:     10 * time.Second,
		AuditAction: "systemd.verify_unit",
	})
	if err != nil {
		return fmt.Errorf("invalid systemd unit file %s: %w", unitPath, err)
	}
	return nil
}

func renderUnit(def platform.ServiceDefinition) string {
	keys := make([]string, 0, len(def.Environment))
	for k := range def.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var env []string
	for _, k := range keys {
		env = append(env, "Environment="+systemdQuote(k+"="+def.Environment[k]))
	}
	args := strings.Builder{}
	for _, arg := range def.Args {
		args.WriteString(" ")
		args.WriteString(systemdQuote(arg))
	}
	builder := strings.Builder{}
	builder.WriteString("[Unit]\n")
	builder.WriteString(fmt.Sprintf("Description=%s\n", def.Description))
	builder.WriteString("After=network.target\n\n")
	builder.WriteString("[Service]\n")
	builder.WriteString("Type=simple\n")
	if def.User != "" {
		builder.WriteString(fmt.Sprintf("User=%s\n", def.User))
	}
	builder.WriteString(fmt.Sprintf("WorkingDirectory=%s\n", systemdPathValue(def.WorkingDir)))
	builder.WriteString(fmt.Sprintf("ExecStart=%s%s\n", systemdQuote(def.ExecPath), args.String()))
	for _, line := range env {
		builder.WriteString(line + "\n")
	}
	restart := def.RestartPolicy
	if restart == "" {
		restart = "on-failure"
	}
	builder.WriteString(fmt.Sprintf("Restart=%s\n", restart))
	builder.WriteString("RestartSec=5\n")
	builder.WriteString("KillMode=control-group\n")
	builder.WriteString("LimitNOFILE=50000\n")
	if strings.TrimSpace(def.MemoryMax) != "" {
		builder.WriteString(fmt.Sprintf("MemoryMax=%s\n", strings.TrimSpace(def.MemoryMax)))
	}
	if def.StdoutPath != "" {
		builder.WriteString(fmt.Sprintf("StandardOutput=append:%s\n", def.StdoutPath))
	}
	if def.StderrPath != "" {
		builder.WriteString(fmt.Sprintf("StandardError=append:%s\n", def.StderrPath))
	}
	builder.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return builder.String()
}

func validateServiceDefinition(def platform.ServiceDefinition) error {
	fields := map[string]string{
		"name":           def.Name,
		"description":    def.Description,
		"exec path":      def.ExecPath,
		"working dir":    def.WorkingDir,
		"user":           def.User,
		"restart policy": def.RestartPolicy,
		"memory max":     def.MemoryMax,
		"stdout path":    def.StdoutPath,
		"stderr path":    def.StderrPath,
	}
	for label, value := range fields {
		if err := rejectSystemdControlChars(label, value); err != nil {
			return err
		}
	}
	for _, arg := range def.Args {
		if err := rejectSystemdControlChars("argument", arg); err != nil {
			return err
		}
	}
	for key, value := range def.Environment {
		if !validEnvironmentKey(key) {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
		if err := rejectSystemdControlChars("environment value", value); err != nil {
			return err
		}
	}
	switch def.RestartPolicy {
	case "", "no", "always", "on-success", "on-failure", "on-abnormal", "on-watchdog", "on-abort":
	default:
		return fmt.Errorf("invalid restart policy %q", def.RestartPolicy)
	}
	if !filepath.IsAbs(strings.TrimSpace(def.ExecPath)) {
		return fmt.Errorf("systemd exec path must be absolute after runtime resolution: %s", def.ExecPath)
	}
	if strings.TrimSpace(def.WorkingDir) != "" && !filepath.IsAbs(strings.TrimSpace(def.WorkingDir)) {
		return fmt.Errorf("systemd working directory must be absolute: %s", def.WorkingDir)
	}
	for label, path := range map[string]string{"stdout path": def.StdoutPath, "stderr path": def.StderrPath} {
		if strings.TrimSpace(path) != "" && !filepath.IsAbs(strings.TrimSpace(path)) {
			return fmt.Errorf("%s must be absolute: %s", label, path)
		}
	}
	return nil
}

func rejectSystemdControlChars(label, value string) error {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains invalid control characters", label)
		}
	}
	return nil
}

func validEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func systemdQuote(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + replacer.Replace(value) + `"`
}

func systemdPathValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, " ", `\x20`, "\t", `\x09`, "%", "%%")
	return replacer.Replace(value)
}

type userManager struct {
	cfg    *config.Config
	runner *system.Runner
}

const (
	sshdPrivilegeSeparationDir = "/run/sshd"
	restrictedTransferHelper   = "/usr/local/libexec/deploycp-transfer"
	restrictedTransferGroup    = "deploycp-site-users"
	restrictedTransferSudoers  = "/etc/sudoers.d/deploycp-transfer"
)

const restrictedShellScript = `#!/bin/bash
export PATH=/usr/local/bin:/usr/bin:/bin
requested_command="${SSH_ORIGINAL_COMMAND:-}"
if [ -z "$requested_command" ] && [ "${1:-}" = "-c" ] && [ -n "${2:-}" ]; then
  current_shell_name="$(basename -- "$0")"
  current_shell_name="${current_shell_name#-}"
  forced_command_name="$(basename -- "$2")"
  if [ "$forced_command_name" != "$current_shell_name" ]; then
    requested_command="$2"
  fi
fi
if [ -n "$requested_command" ]; then
  set -f
  set -- $requested_command
  command_name="$(basename -- "${1:-}")"
  case "$command_name" in
    sftp-server|internal-sftp)
      exec /usr/bin/sudo -n /usr/local/libexec/deploycp-transfer restricted-sftp
      ;;
    scp)
      exec /usr/bin/sudo -n /usr/local/libexec/deploycp-transfer restricted-scp "$requested_command"
      ;;
    *)
      printf 'Command access is restricted for this platform user.\n' >&2
      exit 126
      ;;
  esac
fi
exec /usr/bin/sudo -n /usr/local/libexec/deploycp-transfer restricted-shell
`

const restrictedShellRC = `umask 0002
runtime_env="$HOME/.deploycp/runtime.env"
if [ -f "$runtime_env" ]; then
  . "$runtime_env"
fi
state_root="$HOME/.deploycp-user-state/$USER"
mkdir -p "$state_root/cache" "$state_root/data" "$state_root/config" >/dev/null 2>&1 || true
chmod 700 "$state_root" "$state_root/cache" "$state_root/data" "$state_root/config" >/dev/null 2>&1 || true
export XDG_CACHE_HOME="$state_root/cache"
export XDG_DATA_HOME="$state_root/data"
export XDG_CONFIG_HOME="$state_root/config"
export GOCACHE="${GOCACHE:-$XDG_CACHE_HOME/go-build}"
export GOPATH="${GOPATH:-$XDG_DATA_HOME/go}"
mkdir -p "$GOCACHE" "$GOPATH/pkg/mod" >/dev/null 2>&1 || true
export HISTFILE="$state_root/.bash_history"
PS1='\u@\h:\w\$ '
cd "$HOME"
`

func (u *userManager) EnsureRestrictedShell(ctx context.Context, shellPath string) error {
	if err := u.ensureRestrictedTransferHelper(ctx); err != nil {
		return err
	}
	if err := utils.WriteFileAtomic(shellPath, []byte(restrictedShellScript), 0o755); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chmod", Args: []string{"755", shellPath}, Timeout: 5 * time.Second, AuditAction: "site_user.shell.ensure"}); err != nil {
		return err
	}
	if err := ensureShellListed(shellPath); err != nil {
		return err
	}
	if err := u.ensureSSHPasswordAccess(ctx); err != nil {
		return err
	}
	return nil
}

func (u *userManager) ensureRestrictedTransferHelper(ctx context.Context) error {
	for _, binary := range []string{"bwrap", "setpriv", "sudo", "visudo"} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("%s is required for restricted SSH access", binary)
		}
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/groupadd",
		Args:        []string{"-f", restrictedTransferGroup},
		Timeout:     10 * time.Second,
		AuditAction: "site_user.transfer_group.ensure",
	}); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve transfer helper executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve transfer helper source: %w", err)
	}
	payload, err := os.ReadFile(executable)
	if err != nil {
		return fmt.Errorf("read transfer helper source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(restrictedTransferHelper), 0o755); err != nil {
		return err
	}
	if err := utils.WriteFileAtomic(restrictedTransferHelper, payload, 0o755); err != nil {
		return err
	}
	if err := os.Chown(restrictedTransferHelper, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(restrictedTransferHelper, 0o755); err != nil {
		return err
	}
	runtimeRoot, err := filepath.Abs(filepath.Clean(u.cfg.Paths.RuntimeRoot))
	if err != nil {
		return fmt.Errorf("resolve restricted shell runtime root: %w", err)
	}
	sandboxConfig, err := json.Marshal(struct {
		RuntimeRoot string `json:"runtime_root"`
	}{RuntimeRoot: runtimeRoot})
	if err != nil {
		return err
	}
	if err := utils.WriteFileAtomic(restrictedtransfer.SandboxConfigPath, append(sandboxConfig, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Chown(restrictedtransfer.SandboxConfigPath, 0, 0); err != nil {
		return err
	}
	if err := utils.WriteFileAtomic(restrictedtransfer.ShellRCPath, []byte(restrictedShellRC), 0o644); err != nil {
		return err
	}
	if err := os.Chown(restrictedtransfer.ShellRCPath, 0, 0); err != nil {
		return err
	}
	sudoers := fmt.Sprintf("Cmnd_Alias DEPLOYCP_TRANSFER = %s restricted-sftp, %s restricted-scp *, %s restricted-shell\n%%%s ALL=(root) NOPASSWD: DEPLOYCP_TRANSFER\n", restrictedTransferHelper, restrictedTransferHelper, restrictedTransferHelper, restrictedTransferGroup)
	candidate := restrictedTransferSudoers + ".new"
	if err := utils.WriteFileAtomic(candidate, []byte(sudoers), 0o440); err != nil {
		return err
	}
	if err := os.Chown(candidate, 0, 0); err != nil {
		return err
	}
	if visudo, err := exec.LookPath("visudo"); err == nil {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      visudo,
			Args:        []string{"-cf", candidate},
			Timeout:     8 * time.Second,
			AuditAction: "site_user.transfer_sudoers.validate",
		}); err != nil {
			_ = os.Remove(candidate)
			return err
		}
	} else {
		_ = os.Remove(candidate)
		return errors.New("visudo is required for restricted transfer setup")
	}
	return os.Rename(candidate, restrictedTransferSudoers)
}

func ensureShellListed(shellPath string) error {
	const shellsFile = "/etc/shells"
	content, err := os.ReadFile(shellsFile)
	if err != nil {
		return err
	}
	entry := strings.TrimSpace(shellPath)
	if entry == "" {
		return nil
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	updated := strings.TrimRight(string(content), "\n") + "\n" + entry + "\n"
	return utils.WriteFileAtomic(shellsFile, []byte(updated), 0o644)
}

func (u *userManager) ensureSSHPasswordAccess(ctx context.Context) error {
	const (
		sshdMainConfig = "/etc/ssh/sshd_config"
		sshdConfigDir  = "/etc/ssh/sshd_config.d"
		managedSnippet = "/etc/ssh/sshd_config.d/99-deploycp.conf"
	)
	if err := os.MkdirAll(sshdConfigDir, 0o755); err != nil {
		return err
	}
	snippet, err := managedSSHDConfig(u.cfg.Paths.RestrictedShellPath)
	if err != nil {
		return err
	}
	mainConfig, err := os.ReadFile(sshdMainConfig)
	if err != nil {
		return err
	}
	snapshots, err := captureManagedPaths(sshdMainConfig, managedSnippet)
	if err != nil {
		return err
	}
	rollback := func() {
		_ = restoreManagedPaths(snapshots)
	}
	mainConfig = []byte(ensureManagedSSHDInclude(string(mainConfig), managedSnippet))
	if err := utils.WriteFileAtomic(managedSnippet, []byte(snippet), 0o644); err != nil {
		return err
	}
	mainMode := os.FileMode(0o600)
	if len(snapshots) > 0 && snapshots[0].exists {
		mainMode = snapshots[0].mode.Perm()
	}
	if err := utils.WriteFileAtomic(sshdMainConfig, mainConfig, mainMode); err != nil {
		rollback()
		return err
	}

	sshdBinary := sshdBinaryPath()
	if sshdBinary == "" {
		rollback()
		return errors.New("sshd is required for restricted SSH access")
	}
	if err := ensureSSHDPrivilegeSeparationDir(sshdPrivilegeSeparationDir); err != nil {
		rollback()
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      sshdBinary,
		Args:        []string{"-t"},
		Timeout:     8 * time.Second,
		AuditAction: "ssh.validate",
	}); err != nil {
		rollback()
		return fmt.Errorf("validate managed SSH configuration: %w", err)
	}

	var reloadErrors []error
	for _, serviceName := range []string{"ssh", "sshd"} {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      u.cfg.Paths.SystemctlBinary,
			Args:        []string{"reload", serviceName},
			Timeout:     10 * time.Second,
			AuditAction: "ssh.reload",
		}); err == nil {
			return nil
		} else {
			reloadErrors = append(reloadErrors, err)
		}
	}
	rollback()
	for _, serviceName := range []string{"ssh", "sshd"} {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      u.cfg.Paths.SystemctlBinary,
			Args:        []string{"reload", serviceName},
			Timeout:     10 * time.Second,
			AuditAction: "ssh.rollback_reload",
		}); err == nil {
			break
		}
	}
	return fmt.Errorf("activate managed SSH configuration: %w", errors.Join(reloadErrors...))
}

func managedSSHDConfig(shellPath string) (string, error) {
	cleanShellPath := strings.TrimSpace(shellPath)
	if !filepath.IsAbs(cleanShellPath) || strings.ContainsAny(cleanShellPath, " \t\r\n") {
		return "", fmt.Errorf("restricted shell path must be an absolute path without whitespace: %s", shellPath)
	}
	return fmt.Sprintf(`Match Group %s
	PasswordAuthentication yes
	KbdInteractiveAuthentication yes
	AuthenticationMethods any
	DisableForwarding yes
    ForceCommand %s

Match all
`, restrictedTransferGroup, cleanShellPath), nil
}

func ensureManagedSSHDInclude(content, managedSnippet string) string {
	directive := "Include " + managedSnippet
	trimmed := strings.TrimLeft(content, "\r\n")
	if strings.HasPrefix(trimmed, directive+"\n") || trimmed == directive {
		return content
	}
	return directive + "\n" + content
}

type managedPathSnapshot struct {
	path       string
	exists     bool
	mode       os.FileMode
	content    []byte
	symlink    bool
	linkTarget string
}

func captureManagedPaths(paths ...string) ([]managedPathSnapshot, error) {
	snapshots := make([]managedPathSnapshot, 0, len(paths))
	for _, itemPath := range paths {
		snapshot := managedPathSnapshot{path: itemPath}
		info, err := os.Lstat(itemPath)
		if err != nil {
			if os.IsNotExist(err) {
				snapshots = append(snapshots, snapshot)
				continue
			}
			return nil, err
		}
		snapshot.exists = true
		snapshot.mode = info.Mode()
		if info.Mode()&os.ModeSymlink != 0 {
			snapshot.symlink = true
			snapshot.linkTarget, err = os.Readlink(itemPath)
		} else {
			snapshot.content, err = os.ReadFile(itemPath)
		}
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func restoreManagedPaths(snapshots []managedPathSnapshot) error {
	var restoreErrors []error
	for i := len(snapshots) - 1; i >= 0; i-- {
		snapshot := snapshots[i]
		if err := os.Remove(snapshot.path); err != nil && !os.IsNotExist(err) {
			restoreErrors = append(restoreErrors, err)
			continue
		}
		if !snapshot.exists {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(snapshot.path), 0o755); err != nil {
			restoreErrors = append(restoreErrors, err)
			continue
		}
		if snapshot.symlink {
			if err := os.Symlink(snapshot.linkTarget, snapshot.path); err != nil {
				restoreErrors = append(restoreErrors, err)
			}
			continue
		}
		if err := utils.WriteFileAtomic(snapshot.path, snapshot.content, snapshot.mode.Perm()); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
	}
	return errors.Join(restoreErrors...)
}

func ensureSSHDPrivilegeSeparationDir(path string) error {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || !filepath.IsAbs(clean) {
		return fmt.Errorf("invalid sshd privilege separation directory: %s", path)
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return fmt.Errorf("prepare sshd privilege separation directory %s: %w", clean, err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(clean, 0, 0); err != nil {
			return fmt.Errorf("set sshd privilege separation directory owner %s: %w", clean, err)
		}
	}
	if err := os.Chmod(clean, 0o755); err != nil {
		return fmt.Errorf("set sshd privilege separation directory mode %s: %w", clean, err)
	}
	return nil
}

func sshdBinaryPath() string {
	for _, candidate := range []string{"sshd", "/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
		if strings.HasPrefix(candidate, "/") {
			if _, err := os.Stat(candidate); err == nil {
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

func (u *userManager) Create(ctx context.Context, spec platform.SiteUserSpec) (int, int, error) {
	if err := os.MkdirAll(spec.HomeDir, 0o755); err != nil {
		return 0, 0, err
	}
	// Ensure every parent directory up to the home is world-traversable (o+x)
	// so SSH and the login shell can reach the home directory.
	ensureParentTraversable(spec.HomeDir)
	_, lookupErr := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/id",
		Args:        []string{"-u", spec.Username},
		Timeout:     5 * time.Second,
		AuditAction: "site_user.lookup",
	})
	if lookupErr != nil {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      "/usr/sbin/useradd",
			Args:        []string{"-M", "-d", spec.HomeDir, "-s", spec.ShellPath, spec.Username},
			Timeout:     20 * time.Second,
			AuditAction: "site_user.create",
		}); err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return 0, 0, err
		}
	}
	if err := u.SyncHome(ctx, spec.Username, spec.HomeDir, spec.AllowedRoot, spec.ShellPath); err != nil {
		return 0, 0, err
	}
	if err := u.SetPassword(ctx, spec.Username, spec.Password); err != nil {
		return 0, 0, err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chmod", Args: []string{"755", spec.HomeDir}, Timeout: 5 * time.Second, AuditAction: "site_user.home.chmod"}); err != nil {
		return 0, 0, err
	}
	return 0, 0, nil
}

func (u *userManager) SyncHome(ctx context.Context, username, homeDir, allowedRoot, shellPath string) error {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return err
	}
	ensureParentTraversable(homeDir)
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/usermod",
		Args:        []string{"-d", homeDir, "-s", shellPath, username},
		Timeout:     15 * time.Second,
		AuditAction: "site_user.home.sync",
	}); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/usermod",
		Args:        []string{"-a", "-G", restrictedTransferGroup, username},
		Timeout:     15 * time.Second,
		AuditAction: "site_user.transfer_group.member",
	}); err != nil {
		return err
	}
	_ = allowedRoot
	_ = os.Remove(filepath.Join(homeDir, ".deploycp_allowed_root"))
	return nil
}

// ensureParentTraversable walks up from the given path and sets o+x on each
// parent directory so that any Linux user can traverse the path to reach their
// home directory. Stops at / or /home.
func ensureParentTraversable(target string) {
	target = filepath.Clean(target)
	var dirs []string
	for d := filepath.Dir(target); d != "/" && d != "." && d != target; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		target = d
	}
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil {
			continue
		}
		perm := info.Mode().Perm()
		if perm&0o001 == 0 {
			_ = os.Chmod(d, perm|0o001)
		}
	}
}

func (u *userManager) SetPassword(ctx context.Context, username, password string) error {
	stdin := fmt.Sprintf("%s:%s\n", username, password)
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/chpasswd", Stdin: stdin, Timeout: 10 * time.Second, AuditAction: "site_user.password.reset"}); err != nil {
		return err
	}
	_, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/usermod",
		Args:        []string{"-s", u.cfg.Paths.RestrictedShellPath, username},
		Timeout:     10 * time.Second,
		AuditAction: "site_user.shell.sync",
	})
	return err
}
func (u *userManager) Enable(ctx context.Context, username string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/usermod",
		Args:        []string{"-U", "-s", u.cfg.Paths.RestrictedShellPath, username},
		Timeout:     10 * time.Second,
		AuditAction: "site_user.enable",
	})
	return err
}
func (u *userManager) Disable(ctx context.Context, username string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/usermod", Args: []string{"-L", username}, Timeout: 10 * time.Second, AuditAction: "site_user.disable"})
	return err
}
func (u *userManager) Delete(ctx context.Context, username string) error {
	// Kill any running processes owned by this user first, otherwise userdel fails.
	_, _ = u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/bin/pkill", Args: []string{"-9", "-u", username}, Timeout: 5 * time.Second})
	// Small delay to let the kernel reap the killed processes.
	time.Sleep(500 * time.Millisecond)
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/userdel", Args: []string{"-rf", username}, Timeout: 15 * time.Second, AuditAction: "site_user.delete"})
	return err
}
func (u *userManager) ChownRecursive(ctx context.Context, username, path string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chown", Args: []string{"-R", username + ":" + username, path}, Timeout: 20 * time.Second, AuditAction: "site_user.chown"})
	return err
}

func (u *userManager) SyncSharedAccess(ctx context.Context, root, primaryUser, groupName string, members []string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	groupName = strings.TrimSpace(groupName)
	if root == "" || root == "." || groupName == "" {
		return nil
	}
	if err := os.MkdirAll(root, 0o775); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/groupadd",
		Args:        []string{"-f", groupName},
		Timeout:     15 * time.Second,
		AuditAction: "site_user.group.ensure",
	}); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	allMembers := make([]string, 0, len(members)+1)
	if primary := strings.TrimSpace(primaryUser); primary != "" {
		seen[primary] = struct{}{}
		allMembers = append(allMembers, primary)
	}
	for _, member := range members {
		member = strings.TrimSpace(member)
		if member == "" {
			continue
		}
		if _, ok := seen[member]; ok {
			continue
		}
		seen[member] = struct{}{}
		allMembers = append(allMembers, member)
	}
	for _, member := range allMembers {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      "/usr/sbin/usermod",
			Args:        []string{"-a", "-G", groupName, member},
			Timeout:     15 * time.Second,
			AuditAction: "site_user.group.member",
		}); err != nil {
			return err
		}
	}
	if primary := strings.TrimSpace(primaryUser); primary != "" {
		_, _ = u.runner.Run(ctx, system.CommandRequest{
			Binary:      "/bin/chown",
			Args:        []string{primary + ":" + groupName, root},
			Timeout:     10 * time.Second,
			AuditAction: "site_user.shared.chown_root",
		})
		_, _ = u.runner.Run(ctx, system.CommandRequest{
			Binary:      "/usr/bin/find",
			Args:        []string{root, "-user", "root", "-exec", "/bin/chown", primary + ":" + groupName, "{}", "+"},
			Timeout:     60 * time.Second,
			AuditAction: "site_user.shared.chown_root_owned",
		})
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/bin/chgrp",
		Args:        []string{"-R", groupName, root},
		Timeout:     60 * time.Second,
		AuditAction: "site_user.shared.chgrp",
	}); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/bin/chmod",
		Args:        []string{"-R", "g+rwX", root},
		Timeout:     60 * time.Second,
		AuditAction: "site_user.shared.chmod_group",
	}); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/bin/find",
		Args:        []string{root, "-type", "d", "-exec", "/bin/chmod", "g+s", "{}", "+"},
		Timeout:     60 * time.Second,
		AuditAction: "site_user.shared.setgid",
	}); err != nil {
		return err
	}
	if setfacl, err := exec.LookPath("setfacl"); err == nil {
		_, _ = u.runner.Run(ctx, system.CommandRequest{
			Binary:      setfacl,
			Args:        []string{"-R", "-m", "g:" + groupName + ":rwX", root},
			Timeout:     60 * time.Second,
			AuditAction: "site_user.shared.acl",
		})
		_, _ = u.runner.Run(ctx, system.CommandRequest{
			Binary:      setfacl,
			Args:        []string{"-R", "-d", "-m", "g:" + groupName + ":rwX", root},
			Timeout:     60 * time.Second,
			AuditAction: "site_user.shared.acl_default",
		})
	}
	return nil
}

func (u *userManager) DeleteSharedAccess(ctx context.Context, groupName string) error {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return nil
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:  "/usr/bin/getent",
		Args:    []string{"group", groupName},
		Timeout: 5 * time.Second,
	}); err != nil {
		return nil
	}
	_, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:      "/usr/sbin/groupdel",
		Args:        []string{groupName},
		Timeout:     15 * time.Second,
		AuditAction: "site_user.group.delete",
	})
	return err
}

type nginxManager struct{ runner *system.Runner }

func (n *nginxManager) Validate(ctx context.Context, nginxBinary string) error {
	_, err := n.runner.Run(ctx, system.CommandRequest{Binary: nginxBinary, Args: []string{"-t"}, Timeout: 8 * time.Second, AuditAction: "nginx.validate"})
	return err
}
func (n *nginxManager) Reload(ctx context.Context, nginxBinary string) error {
	_, err := n.runner.Run(ctx, system.CommandRequest{Binary: "/bin/systemctl", Args: []string{"reload", "nginx"}, Timeout: 10 * time.Second, AuditAction: "nginx.reload"})
	if err == nil {
		return nil
	}
	_, err = n.runner.Run(ctx, system.CommandRequest{Binary: nginxBinary, Args: []string{"-s", "reload"}, Timeout: 10 * time.Second, AuditAction: "nginx.reload.signal"})
	return err
}
