package services

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
	"deploycp/internal/validators"
)

type AppInput struct {
	Name             string
	Runtime          string
	ExecutionMode    string
	ProcessManager   string
	BinaryPath       string
	EntryPoint       string
	WorkingDirectory string
	Host             string
	Port             int
	StartArgs        string
	HealthPath       string
	RestartPolicy    string
	Workers          int
	WorkerClass      string
	MaxMemory        string
	Timeout          int
	ExecMode         string
	WebsiteID        *uint
	Enabled          bool
	Env              map[string]string
}

type AppStatus struct {
	App         *models.GoApp
	Service     platform.ServiceStatus
	HealthOK    bool
	HealthError string
}

type AppService struct {
	cfg      *config.Config
	repo     *repositories.GoAppRepository
	services *repositories.ManagedServiceRepository
	websites *WebsiteService
	adapter  platform.Adapter
	audit    *AuditService
	runtime  *RuntimeService
}

func NewAppService(cfg *config.Config, repo *repositories.GoAppRepository, services *repositories.ManagedServiceRepository, websites *WebsiteService, adapter platform.Adapter, audit *AuditService, runtime *RuntimeService) *AppService {
	return &AppService{cfg: cfg, repo: repo, services: services, websites: websites, adapter: adapter, audit: audit, runtime: runtime}
}

func (s *AppService) List() ([]models.GoApp, error) {
	return s.repo.List()
}

func (s *AppService) Find(id uint) (*models.GoApp, error) {
	return s.repo.Find(id)
}

func (s *AppService) Create(ctx context.Context, in AppInput, actor *uint, ip string) (*models.GoApp, error) {
	in = normalizeAppInput(in)
	if err := s.validate(in); err != nil {
		return nil, err
	}
	if err := s.ensurePortAvailable(in.Host, in.Port, 0, false); err != nil {
		return nil, err
	}
	serviceName := "deploycp-app-" + strings.ReplaceAll(strings.ToLower(in.Name), " ", "-")
	stdoutPath := filepath.Join(s.cfg.Paths.LogRoot, "apps", in.Name, "stdout.log")
	stderrPath := filepath.Join(s.cfg.Paths.LogRoot, "apps", in.Name, "stderr.log")
	if err := os.MkdirAll(filepath.Dir(stdoutPath), 0o755); err != nil {
		return nil, err
	}
	app := &models.GoApp{
		Name:             in.Name,
		Runtime:          in.Runtime,
		ExecutionMode:    in.ExecutionMode,
		ProcessManager:   normalizeProcessManager(in.ProcessManager),
		BinaryPath:       in.BinaryPath,
		EntryPoint:       in.EntryPoint,
		WorkingDirectory: in.WorkingDirectory,
		Host:             in.Host,
		Port:             in.Port,
		StartArgs:        in.StartArgs,
		HealthPath:       in.HealthPath,
		RestartPolicy:    in.RestartPolicy,
		Workers:          in.Workers,
		WorkerClass:      in.WorkerClass,
		MaxMemory:        in.MaxMemory,
		Timeout:          in.Timeout,
		ExecMode:         in.ExecMode,
		StdoutLogPath:    stdoutPath,
		StderrLogPath:    stderrPath,
		ServiceName:      serviceName,
		WebsiteID:        in.WebsiteID,
		Enabled:          in.Enabled,
	}
	if err := s.repo.Create(app, in.Env); err != nil {
		return nil, err
	}
	if s.runtime != nil {
		_ = s.runtime.ApplyPlatformRuntime(app.WorkingDirectory, app.Runtime, in.Env["RUNTIME_VERSION"], actor, ip)
	}
	if err := s.installService(ctx, app, in.Env); err != nil {
		return nil, err
	}
	if err := s.websites.ApplyAppProxy(ctx, app.WebsiteID, app.Host, app.Port, actor, ip); err != nil {
		return nil, err
	}
	s.audit.Record(actor, "app.create", "app", fmt.Sprintf("%d", app.ID), ip, in)
	return app, nil
}

func (s *AppService) Update(ctx context.Context, id uint, in AppInput, actor *uint, ip string) error {
	in = normalizeAppInput(in)
	if err := s.validate(in); err != nil {
		return err
	}
	app, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	sameBinding := sameAppBinding(app.Host, app.Port, in.Host, in.Port)
	if err := s.ensurePortAvailable(in.Host, in.Port, id, sameBinding); err != nil {
		return err
	}
	app.Name = in.Name
	app.Runtime = in.Runtime
	app.ExecutionMode = in.ExecutionMode
	app.ProcessManager = normalizeProcessManager(in.ProcessManager)
	app.BinaryPath = in.BinaryPath
	app.EntryPoint = in.EntryPoint
	app.WorkingDirectory = in.WorkingDirectory
	app.Host = in.Host
	app.Port = in.Port
	app.StartArgs = in.StartArgs
	app.HealthPath = in.HealthPath
	app.RestartPolicy = in.RestartPolicy
	app.Workers = in.Workers
	app.WorkerClass = in.WorkerClass
	app.MaxMemory = in.MaxMemory
	app.Timeout = in.Timeout
	app.ExecMode = in.ExecMode
	app.WebsiteID = in.WebsiteID
	app.Enabled = in.Enabled
	if err := s.repo.Update(app, in.Env); err != nil {
		return err
	}
	if s.runtime != nil {
		_ = s.runtime.ApplyPlatformRuntime(app.WorkingDirectory, app.Runtime, in.Env["RUNTIME_VERSION"], actor, ip)
	}
	if err := s.installService(ctx, app, in.Env); err != nil {
		return err
	}
	if err := s.websites.ApplyAppProxy(ctx, app.WebsiteID, app.Host, app.Port, actor, ip); err != nil {
		return err
	}
	s.audit.Record(actor, "app.update", "app", fmt.Sprintf("%d", app.ID), ip, in)
	return nil
}

func (s *AppService) Delete(ctx context.Context, id uint, actor *uint, ip string) error {
	app, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	serviceName := strings.TrimSpace(app.ServiceName)
	unitPath := ""
	if serviceName != "" {
		if managed, err := s.services.FindByName(serviceName); err == nil && managed != nil {
			unitPath = managed.UnitPath
		}
	}
	if serviceName != "" {
		_ = s.adapter.Services().Stop(ctx, serviceName)
		_ = s.adapter.Services().Disable(ctx, serviceName)
		if err := s.services.DeleteByName(serviceName); err != nil {
			return err
		}
		if err := removeServiceUnitFile(s.cfg, s.adapter.Name(), serviceName, unitPath); err != nil {
			return err
		}
	}
	for _, logPath := range []string{app.StdoutLogPath, app.StderrLogPath} {
		if strings.TrimSpace(logPath) == "" {
			continue
		}
		logDir := filepath.Dir(logPath)
		if err := removeTreeSafe(logDir, s.cfg.Paths.LogRoot, s.cfg.Paths.StorageRoot); err != nil {
			return err
		}
	}
	if app.WebsiteID == nil && strings.TrimSpace(app.WorkingDirectory) != "" {
		if err := removeTreeSafe(app.WorkingDirectory, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot); err != nil {
			return err
		}
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "app.delete", "app", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *AppService) Status(ctx context.Context, id uint) (*AppStatus, error) {
	app, err := s.repo.Find(id)
	if err != nil {
		return nil, err
	}
	status, _ := s.adapter.Services().Status(ctx, app.ServiceName)
	healthURL := fmt.Sprintf("http://%s:%d%s", app.Host, app.Port, app.HealthPath)
	healthOK := false
	healthErr := ""
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil {
		healthErr = err.Error()
	} else {
		healthOK = resp.StatusCode >= 200 && resp.StatusCode < 400
		_ = resp.Body.Close()
	}
	return &AppStatus{App: app, Service: status, HealthOK: healthOK, HealthError: healthErr}, nil
}

func (s *AppService) Action(ctx context.Context, id uint, action string, actor *uint, ip string) error {
	app, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	svc := s.adapter.Services()
	switch action {
	case "start":
		err = svc.Start(ctx, app.ServiceName)
	case "stop":
		err = svc.Stop(ctx, app.ServiceName)
	case "restart":
		err = svc.Restart(ctx, app.ServiceName)
	case "enable":
		err = svc.Enable(ctx, app.ServiceName)
	case "disable":
		err = svc.Disable(ctx, app.ServiceName)
	default:
		return fmt.Errorf("invalid action")
	}
	if err != nil {
		return err
	}
	s.audit.Record(actor, "app.action."+action, "app", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

// ListLogFiles returns the known log files for an app (stdout, stderr, and any
// files discovered in the app's working directory or log subdirectory).
func (s *AppService) ListLogFiles(id uint) ([]LogFileInfo, error) {
	app, err := s.repo.Find(id)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	files := make([]LogFileInfo, 0, 4)
	addFile := func(path, logType string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		name := filepath.Base(path)
		if name == "." || name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		files = append(files, LogFileInfo{Name: name, Type: logType, Path: path})
	}
	addFile(app.StdoutLogPath, "stdout")
	addFile(app.StderrLogPath, "stderr")
	// Also look in the app's log storage directory.
	if app.StdoutLogPath != "" {
		logDir := filepath.Dir(app.StdoutLogPath)
		if entries, err := os.ReadDir(logDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				lt := "other"
				if strings.Contains(name, "stdout") {
					lt = "stdout"
				} else if strings.Contains(name, "stderr") || strings.Contains(name, "error") {
					lt = "stderr"
				}
				addFile(filepath.Join(logDir, name), lt)
			}
		}
	}
	return files, nil
}

// ReadLogFile reads the last N lines from a specific log file belonging to an app.
func (s *AppService) ReadLogFile(id uint, filename string, lines int) (string, error) {
	app, err := s.repo.Find(id)
	if err != nil {
		return "", err
	}
	safe := filepath.Base(filename)
	var fp string
	switch safe {
	case filepath.Base(app.StdoutLogPath):
		fp = app.StdoutLogPath
	case filepath.Base(app.StderrLogPath):
		fp = app.StderrLogPath
	default:
		if app.StdoutLogPath != "" {
			fp = filepath.Join(filepath.Dir(app.StdoutLogPath), safe)
		} else {
			return "", fmt.Errorf("log file not found")
		}
	}
	// Validate path is within an allowed directory.
	abs, err := filepath.Abs(fp)
	if err != nil {
		return "", err
	}
	allowed := false
	for _, root := range []string{
		filepath.Dir(strings.TrimSpace(app.StdoutLogPath)),
		filepath.Dir(strings.TrimSpace(app.StderrLogPath)),
	} {
		if root == "" || root == "." {
			continue
		}
		if absRoot, e := filepath.Abs(root); e == nil && strings.HasPrefix(abs, absRoot+string(filepath.Separator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("log file path is not allowed")
	}
	return tailFile(abs, lines)
}

func (s *AppService) Logs(id uint, lines int) (string, string, error) {
	app, err := s.repo.Find(id)
	if err != nil {
		return "", "", err
	}
	if lines <= 0 {
		lines = 200
	}
	stdout, _ := tailFile(app.StdoutLogPath, lines)
	stderr, _ := tailFile(app.StderrLogPath, lines)
	return stdout, stderr, nil
}

func (s *AppService) UpdateRuntimeSettings(ctx context.Context, id uint, processManager string, workers int, workerClass, maxMemory string, timeout int, execMode, restartPolicy string, port int, runtimeVersion, applyAction string, actor *uint, ip string) error {
	app, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	originalHost := app.Host
	originalPort := app.Port
	if pm := normalizeProcessManager(processManager); pm != "" {
		app.ProcessManager = pm
	}
	app.Workers = workers
	app.WorkerClass = workerClass
	app.MaxMemory = maxMemory
	app.Timeout = timeout
	app.ExecMode = execMode
	if strings.TrimSpace(app.Host) == "" {
		app.Host = "127.0.0.1"
	}
	if port > 0 {
		app.Port = port
	}
	if err := s.ensurePortAvailable(app.Host, app.Port, id, sameAppBinding(originalHost, originalPort, app.Host, app.Port)); err != nil {
		return err
	}
	if restartPolicy != "" {
		app.RestartPolicy = restartPolicy
	}
	envMap := make(map[string]string)
	for _, ev := range app.EnvVars {
		envMap[ev.Key] = ev.Value
	}
	if rv := strings.TrimSpace(runtimeVersion); rv != "" {
		envMap["RUNTIME_VERSION"] = rv
	} else {
		delete(envMap, "RUNTIME_VERSION")
	}
	if err := s.repo.Update(app, envMap); err != nil {
		return err
	}
	if s.runtime != nil {
		if err := s.runtime.ApplyPlatformRuntime(app.WorkingDirectory, app.Runtime, envMap["RUNTIME_VERSION"], actor, ip); err != nil {
			return err
		}
	}
	if err := s.websites.ApplyAppProxy(ctx, app.WebsiteID, app.Host, app.Port, actor, ip); err != nil {
		return err
	}
	if err := s.installService(ctx, app, envMap); err != nil {
		return err
	}
	if app.Enabled && strings.TrimSpace(app.ServiceName) != "" {
		switch strings.ToLower(strings.TrimSpace(applyAction)) {
		case "reset":
			_ = s.adapter.Services().Stop(ctx, app.ServiceName)
			if err := s.adapter.Services().Start(ctx, app.ServiceName); err != nil {
				return err
			}
		default:
			if err := s.adapter.Services().Restart(ctx, app.ServiceName); err != nil {
				return err
			}
		}
	}
	auditAction := "app.runtime.update"
	if strings.EqualFold(strings.TrimSpace(applyAction), "reset") {
		auditAction = "app.runtime.reset"
	}
	s.audit.Record(actor, auditAction, "app", fmt.Sprintf("%d", id), ip, map[string]any{
		"port":            app.Port,
		"runtime_version": envMap["RUNTIME_VERSION"],
	})
	return nil
}

func (s *AppService) Reconcile(ctx context.Context, id uint, actor *uint, ip string) error {
	app, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	envMap := make(map[string]string, len(app.EnvVars))
	for _, ev := range app.EnvVars {
		envMap[ev.Key] = ev.Value
	}
	if s.runtime != nil {
		if err := s.runtime.ApplyPlatformRuntime(app.WorkingDirectory, app.Runtime, envMap["RUNTIME_VERSION"], actor, ip); err != nil {
			return err
		}
	}
	if err := s.websites.ApplyAppProxy(ctx, app.WebsiteID, app.Host, app.Port, actor, ip); err != nil {
		return err
	}
	return s.installService(ctx, app, envMap)
}

func (s *AppService) installService(ctx context.Context, app *models.GoApp, env map[string]string) error {
	if !s.cfg.Features.EnableServiceManage {
		return nil
	}
	if s.runtime != nil {
		env = s.runtime.MergeRuntimeEnv(app.Runtime, env["RUNTIME_VERSION"], env)
		if resolvedBinary, err := s.runtime.ResolveBinary(app.Runtime, env["RUNTIME_VERSION"], app.BinaryPath); err == nil {
			app.BinaryPath = resolvedBinary
		}
	}
	def := buildAppServiceDefinition(app, env)
	unitPath, err := s.adapter.Services().Install(ctx, def)
	if err != nil {
		return err
	}
	_ = s.services.Upsert(&models.ManagedService{Name: app.ServiceName, Type: "application", PlatformName: s.adapter.Name(), UnitPath: unitPath, Enabled: app.Enabled})
	if app.Enabled {
		if err := s.adapter.Services().Enable(ctx, app.ServiceName); err != nil {
			return err
		}
		if err := s.adapter.Services().Start(ctx, app.ServiceName); err != nil {
			return err
		}
	}
	return nil
}

func (s *AppService) validate(in AppInput) error {
	if err := validators.Require(in.Name, "name"); err != nil {
		return err
	}
	switch in.Runtime {
	case "go", "node", "python", "php", "binary":
	default:
		return fmt.Errorf("runtime must be one of go, node, python, php, binary")
	}
	if in.ExecutionMode != "compiled" && in.ExecutionMode != "interpreted" {
		return fmt.Errorf("execution mode must be compiled or interpreted")
	}
	pm := normalizeProcessManager(in.ProcessManager)
	switch pm {
	case "systemd", "pm2", "gunicorn", "uwsgi":
	default:
		return fmt.Errorf("process manager must be systemd, pm2, gunicorn, or uwsgi")
	}
	if err := validators.ValidatePath(in.BinaryPath); err != nil {
		return err
	}
	if err := validators.ValidatePath(in.WorkingDirectory); err != nil {
		return err
	}
	if s.cfg.Features.PlatformMode != "dryrun" {
		if err := validateExecutablePath(in.BinaryPath); err != nil {
			return err
		}
	}
	if in.ExecutionMode == "interpreted" {
		if err := validators.Require(strings.TrimSpace(in.EntryPoint), "entry point"); err != nil {
			return err
		}
		if pm == "pm2" {
			if err := validators.ValidatePath(in.EntryPoint); err != nil {
				return err
			}
		}
	}
	if pm == "pm2" && in.ExecutionMode != "interpreted" {
		return fmt.Errorf("pm2 requires interpreted mode with a script entry file")
	}
	if (pm == "gunicorn" || pm == "uwsgi") && in.ExecutionMode != "interpreted" {
		return fmt.Errorf("%s requires interpreted mode with a WSGI module or script in entry point", pm)
	}
	if err := validators.ValidateIPAddress(in.Host); err != nil {
		return err
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("invalid port")
	}
	if in.HealthPath == "" {
		in.HealthPath = "/health"
	}
	if in.RestartPolicy == "" {
		in.RestartPolicy = "on-failure"
	}
	return nil
}

func (s *AppService) ensurePortAvailable(host string, port int, excludeID uint, allowCurrentBinding bool) error {
	host = normalizeAppBindHost(host)
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port")
	}
	items, err := s.repo.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		if excludeID != 0 && item.ID == excludeID {
			continue
		}
		if item.Port != port {
			continue
		}
		if appBindingsConflict(item.Host, item.Port, host, port) {
			return fmt.Errorf("port %d is already used by platform %s", port, strings.TrimSpace(item.Name))
		}
	}
	if allowCurrentBinding || s.cfg.Features.PlatformMode == "dryrun" {
		return nil
	}
	if !isLocalBindableHost(host) {
		return nil
	}
	if appPortUnavailable(host, port) {
		return fmt.Errorf("port %d is already occupied on this server", port)
	}
	return nil
}

func sameAppBinding(hostA string, portA int, hostB string, portB int) bool {
	return portA == portB && normalizeAppBindHost(hostA) == normalizeAppBindHost(hostB)
}

func appBindingsConflict(hostA string, portA int, hostB string, portB int) bool {
	if portA != portB {
		return false
	}
	a := normalizeAppBindHost(hostA)
	b := normalizeAppBindHost(hostB)
	if a == b {
		return true
	}
	return isWildcardBindHost(a) || isWildcardBindHost(b)
}

func normalizeAppBindHost(host string) string {
	h := strings.TrimSpace(strings.ToLower(host))
	switch h {
	case "", "localhost":
		return "127.0.0.1"
	case "::":
		return "::"
	default:
		return h
	}
}

func isWildcardBindHost(host string) bool {
	switch normalizeAppBindHost(host) {
	case "0.0.0.0", "::":
		return true
	default:
		return false
	}
}

func isLocalBindableHost(host string) bool {
	h := normalizeAppBindHost(host)
	switch h {
	case "127.0.0.1", "::1", "0.0.0.0", "::":
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		var candidate net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			candidate = v.IP
		case *net.IPAddr:
			candidate = v.IP
		}
		if candidate != nil && candidate.Equal(ip) {
			return true
		}
	}
	return false
}

func appPortUnavailable(host string, port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(normalizeAppBindHost(host), fmt.Sprintf("%d", port)))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func defaultExecutionMode(runtime string) string {
	switch runtime {
	case "node", "python", "php":
		return "interpreted"
	default:
		return "compiled"
	}
}

func normalizeAppInput(in AppInput) AppInput {
	if strings.TrimSpace(in.Runtime) == "" {
		in.Runtime = "go"
	}
	if strings.TrimSpace(in.ExecutionMode) == "" {
		in.ExecutionMode = defaultExecutionMode(in.Runtime)
	}
	in.ProcessManager = normalizeProcessManager(in.ProcessManager)
	return in
}

func normalizeProcessManager(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "systemd"
	}
	return v
}

func validateExecutablePath(path string) error {
	p := strings.TrimSpace(path)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if _, err := exec.LookPath(filepath.Base(p)); err == nil {
		return nil
	}
	if _, err := exec.LookPath(p); err == nil {
		return nil
	}
	return fmt.Errorf("executable not found (use absolute path or PATH name): %s", path)
}

func buildAppServiceDefinition(app *models.GoApp, env map[string]string) platform.ServiceDefinition {
	base := platform.ServiceDefinition{
		Name:          app.ServiceName,
		Description:   "DeployCP app: " + app.Name,
		WorkingDir:    app.WorkingDirectory,
		Environment:   env,
		RestartPolicy: app.RestartPolicy,
		StdoutPath:    app.StdoutLogPath,
		StderrPath:    app.StderrLogPath,
	}
	pm := normalizeProcessManager(app.ProcessManager)
	switch pm {
	case "pm2":
		base.ExecPath = app.BinaryPath
		args := []string{"start", app.EntryPoint, "--name", app.ServiceName}
		if app.Workers > 0 {
			args = append(args, "-i", fmt.Sprintf("%d", app.Workers))
		}
		if app.ExecMode != "" {
			args = append(args, "--exec-mode", app.ExecMode)
		}
		if app.MaxMemory != "" {
			args = append(args, "--max-memory-restart", app.MaxMemory)
		}
		if extra := strings.Fields(strings.TrimSpace(app.StartArgs)); len(extra) > 0 {
			args = append(args, extra...)
		}
		base.Args = args
	case "gunicorn":
		base.ExecPath = app.BinaryPath
		bind := fmt.Sprintf("%s:%d", app.Host, app.Port)
		args := strings.Fields(strings.TrimSpace(app.StartArgs))
		if app.Workers > 0 {
			args = append(args, "--workers", fmt.Sprintf("%d", app.Workers))
		}
		if app.WorkerClass != "" {
			args = append(args, "--worker-class", app.WorkerClass)
		}
		if app.Timeout > 0 {
			args = append(args, "--timeout", fmt.Sprintf("%d", app.Timeout))
		}
		args = append(args, "--bind", bind, strings.TrimSpace(app.EntryPoint))
		base.Args = args
	case "uwsgi":
		base.ExecPath = app.BinaryPath
		sock := fmt.Sprintf("%s:%d", app.Host, app.Port)
		args := []string{"--http-socket", sock}
		if app.Workers > 0 {
			args = append(args, "--processes", fmt.Sprintf("%d", app.Workers))
		}
		if app.Timeout > 0 {
			args = append(args, "--harakiri", fmt.Sprintf("%d", app.Timeout))
		}
		if extra := strings.Fields(strings.TrimSpace(app.StartArgs)); len(extra) > 0 {
			args = append(args, extra...)
		}
		if ep := strings.TrimSpace(app.EntryPoint); ep != "" {
			args = append(args, "--module", ep)
		}
		base.Args = args
	default:
		base.ExecPath = app.BinaryPath
		base.Args = serviceArgs(app)
	}
	return base
}

func serviceArgs(app *models.GoApp) []string {
	args := make([]string, 0, 6)
	if app.ExecutionMode == "interpreted" && strings.TrimSpace(app.EntryPoint) != "" {
		args = append(args, app.EntryPoint)
	}
	startArgs := strings.Fields(strings.TrimSpace(app.StartArgs))
	args = append(args, startArgs...)
	return args
}
