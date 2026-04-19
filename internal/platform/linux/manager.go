package linux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/platform"
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
	unitPath := filepath.Join("/etc/systemd/system", def.Name+".service")
	content := renderUnit(def)
	if err := utils.WriteFileAtomic(unitPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	if _, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"daemon-reload"}, Timeout: 10 * time.Second, AuditAction: "systemd.daemon_reload"}); err != nil {
		return "", err
	}
	return unitPath, nil
}

func (m *serviceManager) Start(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.SystemctlBinary, Args: []string{"start", name}, Timeout: 15 * time.Second, AuditAction: "service.start"})
	return err
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

func renderUnit(def platform.ServiceDefinition) string {
	keys := make([]string, 0, len(def.Environment))
	for k := range def.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var env []string
	for _, k := range keys {
		env = append(env, fmt.Sprintf("Environment=%s=%s", k, strings.ReplaceAll(def.Environment[k], "\"", "\\\"")))
	}
	args := strings.Join(def.Args, " ")
	if args != "" {
		args = " " + args
	}
	builder := strings.Builder{}
	builder.WriteString("[Unit]\n")
	builder.WriteString(fmt.Sprintf("Description=%s\n", def.Description))
	builder.WriteString("After=network.target\n\n")
	builder.WriteString("[Service]\n")
	if def.User != "" {
		builder.WriteString(fmt.Sprintf("User=%s\n", def.User))
	}
	builder.WriteString(fmt.Sprintf("WorkingDirectory=%s\n", def.WorkingDir))
	builder.WriteString(fmt.Sprintf("ExecStart=%s%s\n", def.ExecPath, args))
	for _, line := range env {
		builder.WriteString(line + "\n")
	}
	restart := def.RestartPolicy
	if restart == "" {
		restart = "on-failure"
	}
	builder.WriteString(fmt.Sprintf("Restart=%s\n", restart))
	if def.StdoutPath != "" {
		builder.WriteString(fmt.Sprintf("StandardOutput=append:%s\n", def.StdoutPath))
	}
	if def.StderrPath != "" {
		builder.WriteString(fmt.Sprintf("StandardError=append:%s\n", def.StderrPath))
	}
	builder.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return builder.String()
}

type userManager struct {
	cfg    *config.Config
	runner *system.Runner
}

func (u *userManager) EnsureRestrictedShell(ctx context.Context, shellPath string) error {
	script := `#!/bin/bash
original_home="$HOME"
allowed="$original_home"
if [ -f "$original_home/.deploycp_allowed_root" ]; then
  read -r allowed < "$original_home/.deploycp_allowed_root" 2>/dev/null || true
fi
if [ ! -d "$allowed" ]; then
  if [ "$(basename "$original_home")" = "htdocs" ] && [ -d "$(dirname "$original_home")" ]; then
    allowed="$(dirname "$original_home")"
  else
    allowed="$original_home"
  fi
fi
export PATH=/usr/local/bin:/usr/bin:/bin
umask 0002
runtime_env="$allowed/.deploycp/runtime.env"
if [ -f "$runtime_env" ]; then
  . "$runtime_env"
fi
export HOME="$allowed"
export DEPLOYCP_ALLOWED_ROOT="$allowed"
rcfile="$(mktemp /tmp/deploycp-shell.XXXXXX)"
cat > "$rcfile" <<'EOF'
deploycp_resolve_path() {
  local target="$1"
  if [ -z "$target" ]; then
    target="$HOME"
  fi
  readlink -m -- "$target"
}
deploycp_guarded_cd() {
  local target="$1"
  local resolved
  resolved="$(deploycp_resolve_path "$target")" || return 1
  case "$resolved" in
    "$DEPLOYCP_ALLOWED_ROOT"|"$DEPLOYCP_ALLOWED_ROOT"/*)
      builtin cd "$resolved"
      ;;
    *)
      printf 'Access denied outside platform root: %s\n' "$DEPLOYCP_ALLOWED_ROOT"
      return 1
      ;;
  esac
}
cd() {
  deploycp_guarded_cd "$1"
}
pushd() {
  deploycp_guarded_cd "${1:-$HOME}" >/dev/null || return 1
  dirs -v
}
popd() {
  builtin popd "$@"
}
PROMPT_COMMAND='pwd_now="$(pwd -P 2>/dev/null || pwd)"; case "$pwd_now" in "$DEPLOYCP_ALLOWED_ROOT"|"$DEPLOYCP_ALLOWED_ROOT"/*) ;; *) builtin cd "$DEPLOYCP_ALLOWED_ROOT" >/dev/null 2>&1 || true ;; esac'
PS1='\u@\h:\w\$ '
EOF
chmod 600 "$rcfile"
cd "$allowed" 2>/dev/null || cd "$HOME"
exec /bin/bash --noprofile --rcfile "$rcfile"
`
	if err := utils.WriteFileAtomic(shellPath, []byte(script), 0o755); err != nil {
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
		sshdConfigDir  = "/etc/ssh/sshd_config.d"
		managedSnippet = "/etc/ssh/sshd_config.d/99-deploycp.conf"
	)
	if err := os.MkdirAll(sshdConfigDir, 0o755); err != nil {
		return err
	}
	snippet := strings.TrimSpace(`
PasswordAuthentication yes
KbdInteractiveAuthentication yes
UsePAM yes
`) + "\n"
	if err := utils.WriteFileAtomic(managedSnippet, []byte(snippet), 0o644); err != nil {
		return err
	}

	sshdBinary := sshdBinaryPath()
	if sshdBinary != "" {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      sshdBinary,
			Args:        []string{"-t"},
			Timeout:     8 * time.Second,
			AuditAction: "ssh.validate",
		}); err != nil {
			return err
		}
	}

	for _, serviceName := range []string{"ssh", "sshd"} {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      u.cfg.Paths.SystemctlBinary,
			Args:        []string{"reload", serviceName},
			Timeout:     10 * time.Second,
			AuditAction: "ssh.reload",
		}); err == nil {
			return nil
		}
	}
	for _, serviceName := range []string{"ssh", "sshd"} {
		if _, err := u.runner.Run(ctx, system.CommandRequest{
			Binary:      u.cfg.Paths.SystemctlBinary,
			Args:        []string{"restart", serviceName},
			Timeout:     10 * time.Second,
			AuditAction: "ssh.restart",
		}); err == nil {
			return nil
		}
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
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/useradd", Args: []string{"-M", "-d", spec.HomeDir, "-s", spec.ShellPath, spec.Username}, Timeout: 20 * time.Second, AuditAction: "site_user.create"})
	if err != nil {
		return 0, 0, err
	}
	if err := u.SetPassword(ctx, spec.Username, spec.Password); err != nil {
		return 0, 0, err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chmod", Args: []string{"755", spec.HomeDir}, Timeout: 5 * time.Second, AuditAction: "site_user.home.chmod"}); err != nil {
		return 0, 0, err
	}
	allowed := filepath.Join(spec.HomeDir, ".deploycp_allowed_root")
	if err := utils.WriteFileAtomic(allowed, []byte(spec.AllowedRoot+"\n"), 0o644); err != nil {
		return 0, 0, err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chown", Args: []string{spec.Username + ":" + spec.Username, allowed}, Timeout: 5 * time.Second}); err != nil {
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
	allowed := filepath.Join(homeDir, ".deploycp_allowed_root")
	if err := utils.WriteFileAtomic(allowed, []byte(strings.TrimSpace(allowedRoot)+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{
		Binary:  "/bin/chown",
		Args:    []string{username + ":" + username, allowed},
		Timeout: 5 * time.Second,
	}); err != nil {
		return err
	}
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
