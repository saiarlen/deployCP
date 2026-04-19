package darwin

import (
	"context"
	"encoding/xml"
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

func (a *adapter) Name() string                      { return "darwin" }
func (a *adapter) Services() platform.ServiceManager { return a.services }
func (a *adapter) Users() platform.UserManager       { return a.users }
func (a *adapter) Nginx() platform.NginxManager      { return a.nginx }

type serviceManager struct {
	cfg    *config.Config
	runner *system.Runner
}

func (m *serviceManager) Install(ctx context.Context, def platform.ServiceDefinition) (string, error) {
	plistPath := filepath.Join(m.cfg.Paths.PlistDir, def.Name+".plist")
	if err := utils.WriteFileAtomic(plistPath, []byte(renderPlist(def)), 0o644); err != nil {
		return "", err
	}
	_, _ = m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.LaunchctlBinary, Args: []string{"bootout", "system", plistPath}, Timeout: 10 * time.Second})
	if _, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.LaunchctlBinary, Args: []string{"bootstrap", "system", plistPath}, Timeout: 15 * time.Second, AuditAction: "launchd.bootstrap"}); err != nil {
		return "", err
	}
	return plistPath, nil
}

func (m *serviceManager) Start(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.LaunchctlBinary, Args: []string{"kickstart", "-k", "system/" + name}, Timeout: 10 * time.Second, AuditAction: "service.start"})
	return err
}
func (m *serviceManager) Stop(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.LaunchctlBinary, Args: []string{"bootout", "system/" + name}, Timeout: 10 * time.Second, AuditAction: "service.stop"})
	return err
}
func (m *serviceManager) Restart(ctx context.Context, name string) error {
	if err := m.Stop(ctx, name); err != nil {
		_ = err
	}
	return m.Start(ctx, name)
}
func (m *serviceManager) Enable(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.LaunchctlBinary, Args: []string{"enable", "system/" + name}, Timeout: 10 * time.Second, AuditAction: "service.enable"})
	return err
}
func (m *serviceManager) Disable(ctx context.Context, name string) error {
	_, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.LaunchctlBinary, Args: []string{"disable", "system/" + name}, Timeout: 10 * time.Second, AuditAction: "service.disable"})
	return err
}
func (m *serviceManager) Status(ctx context.Context, name string) (platform.ServiceStatus, error) {
	res, err := m.runner.Run(ctx, system.CommandRequest{Binary: m.cfg.Paths.LaunchctlBinary, Args: []string{"print", "system/" + name}, Timeout: 10 * time.Second})
	if err != nil {
		return platform.ServiceStatus{Name: name, Active: false, RawOutput: res.Stdout + res.Stderr}, nil
	}
	active := strings.Contains(res.Stdout, "state = running")
	enabled := !strings.Contains(res.Stdout, "disabled")
	return platform.ServiceStatus{Name: name, Active: active, Enabled: enabled, SubState: ternary(active, "running", "stopped"), RawOutput: res.Stdout}, nil
}
func (m *serviceManager) Logs(ctx context.Context, name string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	res, err := m.runner.Run(ctx, system.CommandRequest{Binary: "/usr/bin/log", Args: []string{"show", "--style", "syslog", "--predicate", fmt.Sprintf("process == \"%s\"", name), "--last", "2h"}, Timeout: 8 * time.Second})
	if err != nil {
		return res.Stdout + "\n" + res.Stderr, err
	}
	rows := strings.Split(res.Stdout, "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n"), nil
}

type plist struct {
	XMLName xml.Name `xml:"plist"`
	Version string   `xml:"version,attr"`
	Dict    dict     `xml:"dict"`
}

type dict struct {
	Items []any `xml:",any"`
}

type key struct {
	XMLName xml.Name `xml:"key"`
	Value   string   `xml:",chardata"`
}

type stringNode struct {
	XMLName xml.Name `xml:"string"`
	Value   string   `xml:",chardata"`
}

type trueNode struct {
	XMLName xml.Name `xml:"true"`
}

type arrayNode struct {
	XMLName xml.Name     `xml:"array"`
	Strings []stringNode `xml:"string"`
}

func renderPlist(def platform.ServiceDefinition) string {
	args := []stringNode{{Value: def.ExecPath}}
	for _, arg := range def.Args {
		args = append(args, stringNode{Value: arg})
	}
	envKeys := make([]string, 0, len(def.Environment))
	for k := range def.Environment {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	dictItems := []any{
		key{Value: "Label"}, stringNode{Value: def.Name},
		key{Value: "ProgramArguments"}, arrayNode{Strings: args},
		key{Value: "WorkingDirectory"}, stringNode{Value: def.WorkingDir},
		key{Value: "RunAtLoad"}, trueNode{},
		key{Value: "KeepAlive"}, trueNode{},
	}
	if def.StdoutPath != "" {
		dictItems = append(dictItems, key{Value: "StandardOutPath"}, stringNode{Value: def.StdoutPath})
	}
	if def.StderrPath != "" {
		dictItems = append(dictItems, key{Value: "StandardErrorPath"}, stringNode{Value: def.StderrPath})
	}
	if len(envKeys) > 0 {
		envItems := make([]any, 0, len(envKeys)*2)
		for _, k := range envKeys {
			envItems = append(envItems, key{Value: k}, stringNode{Value: def.Environment[k]})
		}
		dictItems = append(dictItems, key{Value: "EnvironmentVariables"}, dict{Items: envItems})
	}
	p := plist{Version: "1.0", Dict: dict{Items: dictItems}}
	raw, _ := xml.MarshalIndent(p, "", "  ")
	return xml.Header + "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" + string(raw) + "\n"
}

type userManager struct {
	cfg    *config.Config
	runner *system.Runner
}

func (u *userManager) EnsureRestrictedShell(ctx context.Context, shellPath string) error {
	script := `#!/bin/bash
allowed="$HOME"
if [ -f "$HOME/.deploycp_allowed_root" ]; then
  read -r allowed < "$HOME/.deploycp_allowed_root" 2>/dev/null || true
fi
if [ ! -d "$allowed" ]; then
  allowed="$HOME"
fi
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
runtime_env="$allowed/.deploycp/runtime.env"
if [ -f "$runtime_env" ]; then
  . "$runtime_env"
fi
cd "$allowed" 2>/dev/null || cd "$HOME"
if [ -x /bin/rbash ]; then
  exec /bin/rbash
else
  exec /bin/bash --restricted
fi
`
	if err := utils.WriteFileAtomic(shellPath, []byte(script), 0o755); err != nil {
		return err
	}
	if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/bin/chmod", Args: []string{"755", shellPath}, Timeout: 5 * time.Second}); err != nil {
		return err
	}
	return nil
}

func (u *userManager) Create(ctx context.Context, spec platform.SiteUserSpec) (int, int, error) {
	if err := os.MkdirAll(spec.HomeDir, 0o750); err != nil {
		return 0, 0, err
	}
	commands := [][]string{
		{".", "-create", "/Users/" + spec.Username},
		{".", "-create", "/Users/" + spec.Username, "UserShell", spec.ShellPath},
		{".", "-create", "/Users/" + spec.Username, "NFSHomeDirectory", spec.HomeDir},
	}
	for _, args := range commands {
		if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/bin/dscl", Args: args, Timeout: 15 * time.Second, AuditAction: "site_user.create"}); err != nil {
			return 0, 0, err
		}
	}
	if err := u.SetPassword(ctx, spec.Username, spec.Password); err != nil {
		return 0, 0, err
	}
	_, _ = u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/createhomedir", Args: []string{"-c", "-u", spec.Username}, Timeout: 20 * time.Second})
	allowed := filepath.Join(spec.HomeDir, ".deploycp_allowed_root")
	if err := utils.WriteFileAtomic(allowed, []byte(spec.AllowedRoot+"\n"), 0o600); err != nil {
		return 0, 0, err
	}
	if err := u.ChownRecursive(ctx, spec.Username, spec.HomeDir); err != nil {
		return 0, 0, err
	}
	return 0, 0, nil
}
func (u *userManager) SyncHome(ctx context.Context, username, homeDir, allowedRoot, shellPath string) error {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return err
	}
	commands := [][]string{
		{".", "-create", "/Users/" + username, "UserShell", shellPath},
		{".", "-create", "/Users/" + username, "NFSHomeDirectory", homeDir},
	}
	for _, args := range commands {
		if _, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/bin/dscl", Args: args, Timeout: 15 * time.Second, AuditAction: "site_user.home.sync"}); err != nil {
			return err
		}
	}
	allowed := filepath.Join(homeDir, ".deploycp_allowed_root")
	if err := utils.WriteFileAtomic(allowed, []byte(strings.TrimSpace(allowedRoot)+"\n"), 0o600); err != nil {
		return err
	}
	return u.ChownRecursive(ctx, username, homeDir)
}
func (u *userManager) SetPassword(ctx context.Context, username, password string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/sysadminctl", Args: []string{"-resetPasswordFor", username, "-newPassword", password}, Timeout: 20 * time.Second, AuditAction: "site_user.password.reset"})
	return err
}
func (u *userManager) Disable(ctx context.Context, username string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/bin/dscl", Args: []string{".", "-create", "/Users/" + username, "UserShell", "/usr/bin/false"}, Timeout: 10 * time.Second, AuditAction: "site_user.disable"})
	return err
}
func (u *userManager) Delete(ctx context.Context, username string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/bin/dscl", Args: []string{".", "-delete", "/Users/" + username}, Timeout: 10 * time.Second, AuditAction: "site_user.delete"})
	return err
}
func (u *userManager) ChownRecursive(ctx context.Context, username, path string) error {
	_, err := u.runner.Run(ctx, system.CommandRequest{Binary: "/usr/sbin/chown", Args: []string{"-R", username, path}, Timeout: 20 * time.Second, AuditAction: "site_user.chown"})
	return err
}

type nginxManager struct{ runner *system.Runner }

func (n *nginxManager) Validate(ctx context.Context, nginxBinary string) error {
	_, err := n.runner.Run(ctx, system.CommandRequest{Binary: nginxBinary, Args: []string{"-t"}, Timeout: 8 * time.Second, AuditAction: "nginx.validate"})
	return err
}
func (n *nginxManager) Reload(ctx context.Context, nginxBinary string) error {
	_, err := n.runner.Run(ctx, system.CommandRequest{Binary: nginxBinary, Args: []string{"-s", "reload"}, Timeout: 8 * time.Second, AuditAction: "nginx.reload"})
	return err
}

func ternary(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}
