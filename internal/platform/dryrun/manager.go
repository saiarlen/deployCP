package dryrun

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"deploycp/internal/config"
	"deploycp/internal/platform"
)

type adapter struct {
	services *serviceManager
	users    *userManager
	nginx    *nginxManager
}

func New(cfg *config.Config) platform.Adapter {
	return &adapter{
		services: &serviceManager{cfg: cfg},
		users:    &userManager{cfg: cfg},
		nginx:    &nginxManager{cfg: cfg},
	}
}

func (a *adapter) Name() string                      { return "dryrun" }
func (a *adapter) Services() platform.ServiceManager { return a.services }
func (a *adapter) Users() platform.UserManager       { return a.users }
func (a *adapter) Nginx() platform.NginxManager      { return a.nginx }

func drylog(category, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[dryrun/%s] %s", category, msg)
}

// serviceManager simulates systemd/launchd service management.
type serviceManager struct {
	cfg     *config.Config
	nextUID atomic.Int64
	store   map[string]platform.ServiceStatus
}

func (m *serviceManager) ensureStore() {
	if m.store == nil {
		m.store = make(map[string]platform.ServiceStatus)
	}
}

func (m *serviceManager) Install(_ context.Context, def platform.ServiceDefinition) (string, error) {
	m.ensureStore()
	unitPath := filepath.Join(m.cfg.Paths.StorageRoot, "generated", def.Name+".service")
	content := fmt.Sprintf("# [dryrun] simulated unit for %s\nExecStart=%s %s\nWorkingDirectory=%s\n",
		def.Name, def.ExecPath, strings.Join(def.Args, " "), def.WorkingDir)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	m.store[def.Name] = platform.ServiceStatus{Name: def.Name, Active: false, Enabled: false, SubState: "stopped"}
	drylog("service", "installed %s → %s", def.Name, unitPath)
	return unitPath, nil
}

func (m *serviceManager) Start(_ context.Context, name string) error {
	m.ensureStore()
	s := m.store[name]
	s.Name = name
	s.Active = true
	s.SubState = "running"
	m.store[name] = s
	drylog("service", "start %s", name)
	return nil
}

func (m *serviceManager) Stop(_ context.Context, name string) error {
	m.ensureStore()
	s := m.store[name]
	s.Name = name
	s.Active = false
	s.SubState = "stopped"
	m.store[name] = s
	drylog("service", "stop %s", name)
	return nil
}

func (m *serviceManager) Restart(_ context.Context, name string) error {
	m.ensureStore()
	s := m.store[name]
	s.Name = name
	s.Active = true
	s.SubState = "running"
	m.store[name] = s
	drylog("service", "restart %s", name)
	return nil
}

func (m *serviceManager) Enable(_ context.Context, name string) error {
	m.ensureStore()
	s := m.store[name]
	s.Name = name
	s.Enabled = true
	m.store[name] = s
	drylog("service", "enable %s", name)
	return nil
}

func (m *serviceManager) Disable(_ context.Context, name string) error {
	m.ensureStore()
	s := m.store[name]
	s.Name = name
	s.Enabled = false
	m.store[name] = s
	drylog("service", "disable %s", name)
	return nil
}

func (m *serviceManager) Status(_ context.Context, name string) (platform.ServiceStatus, error) {
	m.ensureStore()
	if s, ok := m.store[name]; ok {
		return s, nil
	}
	return platform.ServiceStatus{Name: name, Active: false, Enabled: false, SubState: "not-found"}, nil
}

func (m *serviceManager) Logs(_ context.Context, name string, lines int) (string, error) {
	drylog("service", "logs %s (last %d lines)", name, lines)
	return fmt.Sprintf("[dryrun] no real logs for %s — running in simulation mode\n", name), nil
}

// userManager simulates Linux user provisioning (useradd, chpasswd, etc.).
type userManager struct {
	cfg     *config.Config
	nextUID atomic.Int64
}

func (u *userManager) EnsureRestrictedShell(_ context.Context, shellPath string) error {
	drylog("user", "ensure restricted shell at %s", shellPath)
	return nil
}

func (u *userManager) Create(_ context.Context, spec platform.SiteUserSpec) (int, int, error) {
	uid := int(u.nextUID.Add(1)) + 5000
	gid := uid
	if err := os.MkdirAll(spec.HomeDir, 0o750); err != nil {
		return 0, 0, err
	}
	allowed := filepath.Join(spec.HomeDir, ".deploycp_allowed_root")
	_ = os.WriteFile(allowed, []byte(spec.AllowedRoot+"\n"), 0o600)
	drylog("user", "created %s (uid=%d gid=%d home=%s)", spec.Username, uid, gid, spec.HomeDir)
	return uid, gid, nil
}

func (u *userManager) SyncHome(_ context.Context, username, homeDir, allowedRoot, shellPath string) error {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return err
	}
	allowed := filepath.Join(homeDir, ".deploycp_allowed_root")
	_ = os.WriteFile(allowed, []byte(allowedRoot+"\n"), 0o600)
	drylog("user", "sync home for %s -> %s (allowed=%s shell=%s)", username, homeDir, allowedRoot, shellPath)
	return nil
}

func (u *userManager) SetPassword(_ context.Context, username, password string) error {
	masked := "***"
	if len(password) > 2 {
		masked = password[:2] + "***"
	}
	drylog("user", "set password for %s (%s)", username, masked)
	return nil
}

func (u *userManager) Disable(_ context.Context, username string) error {
	drylog("user", "disable %s", username)
	return nil
}

func (u *userManager) Delete(_ context.Context, username string) error {
	drylog("user", "delete %s", username)
	return nil
}

func (u *userManager) ChownRecursive(_ context.Context, username, path string) error {
	drylog("user", "chown -R %s %s", username, path)
	return nil
}

// nginxManager simulates nginx validate and reload.
type nginxManager struct {
	cfg *config.Config
}

func (n *nginxManager) Validate(_ context.Context, nginxBinary string) error {
	drylog("nginx", "validate (would run: %s -t)", nginxBinary)
	return nil
}

func (n *nginxManager) Reload(_ context.Context, nginxBinary string) error {
	drylog("nginx", "reload (would run: systemctl reload nginx)")
	return nil
}
