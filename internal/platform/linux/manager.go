package linux

import (
	"context"
	"fmt"
	"os"
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
set -euo pipefail
allowed=$(cat "$HOME/.deploycp_allowed_root" 2>/dev/null || echo "$HOME")
if [ ! -d "$allowed" ]; then
  allowed="$HOME"
fi
export PATH=/usr/local/bin:/usr/bin:/bin
runtime_env="$allowed/.deploycp/runtime.env"
if [ -f "$runtime_env" ]; then
  . "$runtime_env"
fi
cd "$allowed"
exec /bin/rbash
`
	if err := utils.WriteFileAtomic(shellPath, []byte(script), 0o755); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chmod", Args: []string{"755", shellPath}, Timeout: 5 * time.Second, AuditAction: "site_user.shell.ensure"}); err != nil {
		return err
	}
	return nil
}

func (u *userManager) Create(ctx context.Context, spec platform.SiteUserSpec) (int, int, error) {
	if err := os.MkdirAll(spec.HomeDir, 0o750); err != nil {
		return 0, 0, err
	}
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/useradd", Args: []string{"-m", "-d", spec.HomeDir, "-s", spec.ShellPath, spec.Username}, Timeout: 20 * time.Second, AuditAction: "site_user.create"})
	if err != nil {
		return 0, 0, err
	}
	if err := u.SetPassword(ctx, spec.Username, spec.Password); err != nil {
		return 0, 0, err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chown", Args: []string{"-R", spec.Username + ":" + spec.Username, spec.HomeDir}, Timeout: 15 * time.Second, AuditAction: "site_user.chown"}); err != nil {
		return 0, 0, err
	}
	allowed := filepath.Join(spec.HomeDir, ".deploycp_allowed_root")
	if err := utils.WriteFileAtomic(allowed, []byte(spec.AllowedRoot+"\n"), 0o600); err != nil {
		return 0, 0, err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chown", Args: []string{spec.Username + ":" + spec.Username, allowed}, Timeout: 5 * time.Second}); err != nil {
		return 0, 0, err
	}
	return 0, 0, nil
}

func (u *userManager) SetPassword(ctx context.Context, username, password string) error {
	stdin := fmt.Sprintf("%s:%s\n", username, password)
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/chpasswd", Stdin: stdin, Timeout: 10 * time.Second, AuditAction: "site_user.password.reset"})
	return err
}
func (u *userManager) Disable(ctx context.Context, username string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/usermod", Args: []string{"-L", username}, Timeout: 10 * time.Second, AuditAction: "site_user.disable"})
	return err
}
func (u *userManager) Delete(ctx context.Context, username string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/userdel", Args: []string{"-r", username}, Timeout: 15 * time.Second, AuditAction: "site_user.delete"})
	return err
}
func (u *userManager) ChownRecursive(ctx context.Context, username, path string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chown", Args: []string{"-R", username + ":" + username, path}, Timeout: 20 * time.Second, AuditAction: "site_user.chown"})
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
