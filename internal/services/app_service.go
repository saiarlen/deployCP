package services

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
	"deploycp/internal/utils"
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

func (s *AppService) FindByWebsiteID(websiteID uint) (*models.GoApp, error) {
	return s.repo.FindByWebsiteID(websiteID)
}

func (s *AppService) RuntimeInspection(app *models.GoApp) RuntimeInspection {
	if s.runtime == nil {
		return RuntimeInspection{}
	}
	return s.runtime.InspectAppRuntime(app)
}

func (s *AppService) Create(ctx context.Context, in AppInput, actor *uint, ip string) (*models.GoApp, error) {
	in = normalizeAppInput(in)
	if err := s.validate(in); err != nil {
		return nil, err
	}
	if err := s.ensurePortAvailable(in.Host, in.Port, 0, false); err != nil {
		return nil, err
	}
	serviceName := appSystemdServiceName(in.Name)
	stdoutPath := absoluteRuntimePath(filepath.Join(s.cfg.Paths.LogRoot, "apps", in.Name, "stdout.log"))
	stderrPath := absoluteRuntimePath(filepath.Join(s.cfg.Paths.LogRoot, "apps", in.Name, "stderr.log"))
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
	rollbackCreate := func(cause error) (*models.GoApp, error) {
		_ = s.rollbackFailedCreate(ctx, app)
		return nil, cause
	}
	if err := s.ensureRuntimeScaffold(app); err != nil {
		return rollbackCreate(err)
	}
	if s.runtime != nil {
		_ = s.runtime.ApplyPlatformRuntime(platformRuntimeRootForApp(app), app.Runtime, in.Env["RUNTIME_VERSION"], actor, ip)
	}
	if app.WebsiteID != nil && s.websites != nil {
		_ = s.websites.SyncShellRuntime(*app.WebsiteID, app.Runtime, in.Env["RUNTIME_VERSION"])
	}
	if err := s.websites.ApplyAppProxy(ctx, app.WebsiteID, app.Host, app.Port, actor, ip); err != nil {
		return rollbackCreate(err)
	}
	if err := s.installService(ctx, app, in.Env); err != nil {
		return rollbackCreate(err)
	}
	s.audit.Record(actor, "app.create", "app", fmt.Sprintf("%d", app.ID), ip, in)
	return app, nil
}

func (s *AppService) rollbackFailedCreate(ctx context.Context, app *models.GoApp) error {
	if app == nil {
		return nil
	}
	if app.WebsiteID == nil {
		return s.Delete(ctx, app.ID, nil, "")
	}
	serviceName := strings.TrimSpace(app.ServiceName)
	unitPath := ""
	if serviceName != "" {
		if managed, err := s.services.FindByName(serviceName); err == nil && managed != nil {
			unitPath = managed.UnitPath
		}
		_ = s.adapter.Services().Stop(ctx, serviceName)
		_ = s.adapter.Services().Disable(ctx, serviceName)
		if s.websites != nil {
			if site, err := s.websites.Find(*app.WebsiteID); err == nil {
				for _, user := range runtimeSudoerUsersForSite(site, s.websites.siteUsers) {
					_ = removeSiteUserRuntimeSudoers(s.cfg, user.Username, serviceName)
				}
			}
		}
		_ = s.services.DeleteByName(serviceName)
		_ = removeServiceUnitFile(s.cfg, s.adapter.Name(), serviceName, unitPath)
	}
	if strings.EqualFold(strings.TrimSpace(app.Runtime), "python") {
		if venvPath := pythonRuntimeVenvPathForApp(app); venvPath != "" {
			_ = removeTreeSafe(venvPath, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot)
		}
	}
	for _, logPath := range []string{app.StdoutLogPath, app.StderrLogPath} {
		if strings.TrimSpace(logPath) == "" {
			continue
		}
		_ = removeTreeSafe(filepath.Dir(logPath), s.cfg.Paths.LogRoot, s.cfg.Paths.StorageRoot)
	}
	if err := s.repo.ClearRuntime(*app.WebsiteID); err != nil {
		return err
	}
	if s.websites != nil {
		_ = s.websites.RefreshConfig(ctx, *app.WebsiteID)
	}
	return nil
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
		_ = s.runtime.ApplyPlatformRuntime(platformRuntimeRootForApp(app), app.Runtime, in.Env["RUNTIME_VERSION"], actor, ip)
	}
	if app.WebsiteID != nil && s.websites != nil {
		_ = s.websites.SyncShellRuntime(*app.WebsiteID, app.Runtime, in.Env["RUNTIME_VERSION"])
	}
	if err := s.websites.ApplyAppProxy(ctx, app.WebsiteID, app.Host, app.Port, actor, ip); err != nil {
		return err
	}
	if err := s.installService(ctx, app, in.Env); err != nil {
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
		if app.WebsiteID != nil && s.websites != nil {
			if site, err := s.websites.Find(*app.WebsiteID); err == nil {
				for _, user := range runtimeSudoerUsersForSite(site, s.websites.siteUsers) {
					if err := removeSiteUserRuntimeSudoers(s.cfg, user.Username, serviceName); err != nil {
						return err
					}
				}
			}
		}
		if err := s.services.DeleteByName(serviceName); err != nil {
			return err
		}
		if err := removeServiceUnitFile(s.cfg, s.adapter.Name(), serviceName, unitPath); err != nil {
			return err
		}
	}
	if strings.EqualFold(strings.TrimSpace(app.Runtime), "python") {
		if venvPath := pythonRuntimeVenvPathForApp(app); venvPath != "" {
			if err := removeTreeSafe(venvPath, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot); err != nil {
				return err
			}
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
	app.EntryPoint = normalizePythonProcessManagerEntryPoint(app.Runtime, app.ProcessManager, app.EntryPoint)
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
		if s.runtime != nil && !s.runtime.VerifyInstalledVersion(app.Runtime, rv) {
			return fmt.Errorf("selected %s runtime %s is not installed or is not verifiable on this server", app.Runtime, rv)
		}
		envMap["RUNTIME_VERSION"] = rv
	} else {
		delete(envMap, "RUNTIME_VERSION")
	}
	if err := s.repo.Update(app, envMap); err != nil {
		return err
	}
	if s.runtime != nil {
		if err := s.runtime.ApplyPlatformRuntime(platformRuntimeRootForApp(app), app.Runtime, envMap["RUNTIME_VERSION"], actor, ip); err != nil {
			return err
		}
	}
	if app.WebsiteID != nil && s.websites != nil {
		if err := s.websites.SyncShellRuntime(*app.WebsiteID, app.Runtime, envMap["RUNTIME_VERSION"]); err != nil {
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
		if err := s.runtime.ApplyPlatformRuntime(platformRuntimeRootForApp(app), app.Runtime, envMap["RUNTIME_VERSION"], actor, ip); err != nil {
			return err
		}
	}
	if app.WebsiteID != nil && s.websites != nil {
		if err := s.websites.SyncShellRuntime(*app.WebsiteID, app.Runtime, envMap["RUNTIME_VERSION"]); err != nil {
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
	if err := s.ensureServiceRuntimeStateDirs(app); err != nil {
		return err
	}
	env = s.serviceRuntimeEnv(app, env)
	if s.runtime != nil {
		env = s.runtime.MergeRuntimeEnv(app.Runtime, env["RUNTIME_VERSION"], env)
		if resolvedBinary, err := s.runtime.ResolveBinary(app.Runtime, env["RUNTIME_VERSION"], app.BinaryPath); err == nil {
			app.BinaryPath = resolvedBinary
		}
	}
	if err := s.preparePythonRuntime(ctx, app, env); err != nil {
		return err
	}
	app.BinaryPath = resolveAbsoluteExecutablePath(app.BinaryPath)
	def := buildAppServiceDefinition(app, env)
	unitPath, err := s.adapter.Services().Install(ctx, def)
	if err != nil {
		return err
	}
	_ = s.services.Upsert(&models.ManagedService{Name: app.ServiceName, Type: "application", PlatformName: s.adapter.Name(), UnitPath: unitPath, Enabled: app.Enabled})
	if err := s.syncSiteUserRuntimeSudoers(ctx, app); err != nil {
		return err
	}
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

func (s *AppService) syncSiteUserRuntimeSudoers(ctx context.Context, app *models.GoApp) error {
	if app == nil || app.WebsiteID == nil || s.websites == nil {
		return nil
	}
	site, err := s.websites.Find(*app.WebsiteID)
	if err != nil {
		return err
	}
	for _, user := range runtimeSudoerUsersForSite(site, s.websites.siteUsers) {
		if !user.IsActive || !user.SSHEnabled {
			if err := removeSiteUserRuntimeSudoers(s.cfg, user.Username, app.ServiceName); err != nil {
				return err
			}
			continue
		}
		if err := writeSiteUserRuntimeSudoers(ctx, s.cfg, user.Username, app.ServiceName); err != nil {
			return err
		}
	}
	return nil
}

func (s *AppService) serviceRuntimeEnv(app *models.GoApp, env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+8)
	for k, v := range env {
		out[k] = v
	}
	if strings.TrimSpace(out["PORT"]) == "" && app.Port > 0 {
		out["PORT"] = strconv.Itoa(app.Port)
	}
	if strings.TrimSpace(out["HOST"]) == "" && strings.TrimSpace(app.Host) != "" {
		out["HOST"] = strings.TrimSpace(app.Host)
	}
	stateRoot := s.serviceRuntimeStateRoot(app)
	cacheRoot := filepath.Join(stateRoot, "cache")
	dataRoot := filepath.Join(stateRoot, "data")
	configRoot := filepath.Join(stateRoot, "config")
	if strings.TrimSpace(out["HOME"]) == "" {
		out["HOME"] = stateRoot
	}
	if strings.TrimSpace(out["XDG_CACHE_HOME"]) == "" {
		out["XDG_CACHE_HOME"] = cacheRoot
	}
	if strings.TrimSpace(out["XDG_DATA_HOME"]) == "" {
		out["XDG_DATA_HOME"] = dataRoot
	}
	if strings.TrimSpace(out["XDG_CONFIG_HOME"]) == "" {
		out["XDG_CONFIG_HOME"] = configRoot
	}
	if strings.EqualFold(strings.TrimSpace(app.Runtime), "go") {
		if strings.TrimSpace(out["GOCACHE"]) == "" {
			out["GOCACHE"] = filepath.Join(cacheRoot, "go-build")
		}
		if strings.TrimSpace(out["GOPATH"]) == "" {
			out["GOPATH"] = filepath.Join(dataRoot, "go")
		}
	}
	return out
}

func (s *AppService) serviceRuntimeStateRoot(app *models.GoApp) string {
	serviceName := "app"
	if app != nil && strings.TrimSpace(app.ServiceName) != "" {
		serviceName = strings.TrimSpace(app.ServiceName)
	}
	return absoluteRuntimePath(filepath.Join(s.cfg.Paths.StorageRoot, "runtime-state", serviceName))
}

func (s *AppService) ensureServiceRuntimeStateDirs(app *models.GoApp) error {
	root := s.serviceRuntimeStateRoot(app)
	dirs := []string{
		root,
		filepath.Join(root, "cache"),
		filepath.Join(root, "cache", "go-build"),
		filepath.Join(root, "data"),
		filepath.Join(root, "data", "go"),
		filepath.Join(root, "config"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *AppService) preparePythonRuntime(ctx context.Context, app *models.GoApp, env map[string]string) error {
	if app == nil || !strings.EqualFold(strings.TrimSpace(app.Runtime), "python") {
		return nil
	}
	pm := normalizeProcessManager(app.ProcessManager)
	if !pythonUsesManagedVenv(pm) {
		return nil
	}
	venvPath := pythonRuntimeVenvPathForApp(app)
	if venvPath == "" {
		return fmt.Errorf("python runtime venv path could not be resolved")
	}
	workingDir := appServiceWorkingDir(app)
	if workingDir == "" || workingDir == "." {
		return fmt.Errorf("python runtime working directory could not be resolved")
	}
	if err := os.MkdirAll(filepath.Dir(venvPath), 0o775); err != nil {
		return err
	}
	selectedVersion := strings.TrimSpace(env["RUNTIME_VERSION"])
	if err := s.recreatePythonVenvWhenVersionChanged(venvPath, selectedVersion); err != nil {
		return err
	}
	pythonBin := "python3"
	if s.runtime != nil {
		if resolved, err := s.runtime.ResolveBinary("python", env["RUNTIME_VERSION"], "python3"); err == nil && strings.TrimSpace(resolved) != "" {
			pythonBin = resolved
		}
	}
	venvPython := filepath.Join(venvPath, "bin", "python")
	if st, err := os.Stat(venvPython); err != nil || st.IsDir() {
		if err := runAppSetupCommand(ctx, pythonBin, []string{"-m", "venv", venvPath}, workingDir, env, 3*time.Minute); err != nil {
			return fmt.Errorf("create python virtualenv: %w", err)
		}
	}
	pmBinary := ""
	switch pm {
	case "gunicorn", "uwsgi":
		pmBinary = filepath.Join(venvPath, "bin", pm)
		if st, err := os.Stat(pmBinary); err != nil || st.IsDir() {
			if err := runAppSetupCommand(ctx, venvPython, []string{"-m", "pip", "install", "--disable-pip-version-check", pm}, workingDir, env, 5*time.Minute); err != nil {
				return fmt.Errorf("install python process manager %s: %w", pm, err)
			}
		}
		app.BinaryPath = pmBinary
	default:
		app.BinaryPath = venvPython
	}
	requirementsPath := filepath.Join(workingDir, "requirements.txt")
	if st, err := os.Stat(requirementsPath); err == nil && !st.IsDir() {
		if err := runAppSetupCommand(ctx, venvPython, []string{"-m", "pip", "install", "--disable-pip-version-check", "-r", requirementsPath}, workingDir, env, 5*time.Minute); err != nil {
			return fmt.Errorf("install python requirements: %w", err)
		}
	}
	if err := writePythonVenvVersionMarker(venvPath, selectedVersion); err != nil {
		return err
	}
	return s.chownPythonRuntimeVenv(ctx, app, venvPath)
}

func (s *AppService) recreatePythonVenvWhenVersionChanged(venvPath, selectedVersion string) error {
	if strings.TrimSpace(venvPath) == "" {
		return nil
	}
	venvPython := filepath.Join(venvPath, "bin", "python")
	if st, err := os.Stat(venvPython); err != nil || st.IsDir() {
		return nil
	}
	existingVersion, err := os.ReadFile(pythonVenvVersionMarkerPath(venvPath))
	if err != nil {
		if os.IsNotExist(err) {
			if strings.TrimSpace(selectedVersion) == "" {
				return nil
			}
			return removeTreeSafe(venvPath, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot)
		}
		return err
	}
	if strings.TrimSpace(string(existingVersion)) == strings.TrimSpace(selectedVersion) {
		return nil
	}
	return removeTreeSafe(venvPath, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot)
}

func pythonVenvVersionMarkerPath(venvPath string) string {
	return filepath.Join(venvPath, ".deploycp-runtime-version")
}

func writePythonVenvVersionMarker(venvPath, selectedVersion string) error {
	if strings.TrimSpace(venvPath) == "" || strings.TrimSpace(selectedVersion) == "" {
		return nil
	}
	return os.WriteFile(pythonVenvVersionMarkerPath(venvPath), []byte(strings.TrimSpace(selectedVersion)+"\n"), 0o664)
}

func pythonUsesManagedVenv(processManager string) bool {
	switch normalizeProcessManager(processManager) {
	case "systemd", "gunicorn", "uwsgi":
		return true
	default:
		return false
	}
}

func runAppSetupCommand(ctx context.Context, binary string, args []string, dir string, env map[string]string, timeout time.Duration) error {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, binary, args...)
	cmd.Dir = dir
	cmd.Env = appSetupCommandEnv(env)
	output, err := cmd.CombinedOutput()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out", strings.Join(append([]string{binary}, args...), " "))
	}
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(append([]string{binary}, args...), " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func appSetupCommandEnv(env map[string]string) []string {
	out := os.Environ()
	for key, value := range env {
		if validEnvName(key) {
			out = append(out, key+"="+value)
		}
	}
	return out
}

func (s *AppService) chownPythonRuntimeVenv(ctx context.Context, app *models.GoApp, venvPath string) error {
	if app == nil || app.WebsiteID == nil || s.websites == nil || strings.TrimSpace(venvPath) == "" {
		return nil
	}
	site, err := s.websites.Find(*app.WebsiteID)
	if err != nil {
		return err
	}
	for _, user := range runtimeSudoerUsersForSite(site, s.websites.siteUsers) {
		if user.IsActive && strings.TrimSpace(user.Username) != "" {
			return s.adapter.Users().ChownRecursive(ctx, user.Username, venvPath)
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
	if s.runtime != nil && strings.TrimSpace(in.Runtime) != "" && !strings.EqualFold(strings.TrimSpace(in.Runtime), "binary") {
		version := strings.TrimSpace(in.Env["RUNTIME_VERSION"])
		if version == "" {
			return fmt.Errorf("runtime version is required for %s platforms", strings.TrimSpace(in.Runtime))
		}
		if !s.runtime.VerifyInstalledVersion(in.Runtime, version) {
			return fmt.Errorf("selected %s runtime %s is not installed or is not verifiable on this server", strings.TrimSpace(in.Runtime), version)
		}
	}
	if s.cfg.Features.PlatformMode != "dryrun" {
		if !pythonUsesManagedVenvForInput(in, pm) {
			if err := validateExecutablePath(in.BinaryPath); err != nil {
				return err
			}
		}
		if pythonUsesManagedVenvForInput(in, pm) {
			if _, err := validatePythonBootstrapCommand(in, s.runtime); err != nil {
				return err
			}
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
	if err := validateRuntimeServiceInput(in); err != nil {
		return err
	}
	return nil
}

func pythonUsesManagedVenvForInput(in AppInput, pm string) bool {
	return strings.EqualFold(strings.TrimSpace(in.Runtime), "python") && pythonUsesManagedVenv(pm)
}

func validatePythonBootstrapCommand(in AppInput, runtime *RuntimeService) (string, error) {
	pythonBin := "python3"
	if runtime != nil {
		if resolved, err := runtime.ResolveBinary("python", strings.TrimSpace(in.Env["RUNTIME_VERSION"]), "python3"); err == nil && strings.TrimSpace(resolved) != "" {
			pythonBin = resolved
		}
	}
	if err := validateExecutablePath(pythonBin); err != nil {
		return "", fmt.Errorf("python runtime bootstrap command is not available: %w", err)
	}
	return pythonBin, nil
}

func resolveAbsoluteExecutablePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if lookedUp, err := exec.LookPath(path); err == nil && strings.TrimSpace(lookedUp) != "" {
		return filepath.Clean(lookedUp)
	}
	if _, err := os.Stat(path); err == nil {
		return absoluteRuntimePath(path)
	}
	return path
}

func absoluteRuntimePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func validateRuntimeServiceInput(in AppInput) error {
	fields := map[string]string{
		"name":              in.Name,
		"runtime":           in.Runtime,
		"execution mode":    in.ExecutionMode,
		"process manager":   in.ProcessManager,
		"binary path":       in.BinaryPath,
		"entry point":       in.EntryPoint,
		"working directory": in.WorkingDirectory,
		"start args":        in.StartArgs,
		"health path":       in.HealthPath,
		"restart policy":    in.RestartPolicy,
	}
	for label, value := range fields {
		if hasControlCharacter(value) {
			return fmt.Errorf("%s contains invalid control characters", label)
		}
	}
	for key, value := range in.Env {
		if !validEnvName(key) {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
		if hasControlCharacter(value) {
			return fmt.Errorf("environment value for %s contains invalid control characters", key)
		}
	}
	switch in.RestartPolicy {
	case "no", "always", "on-success", "on-failure", "on-abnormal", "on-watchdog", "on-abort":
	default:
		return fmt.Errorf("invalid restart policy")
	}
	return nil
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validEnvName(key string) bool {
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

func (s *AppService) ensureRuntimeScaffold(app *models.GoApp) error {
	if app == nil {
		return nil
	}
	root := appServiceWorkingDir(app)
	if root == "" || root == "." {
		return nil
	}
	if err := os.MkdirAll(root, 0o775); err != nil {
		return err
	}
	entryPoint := strings.TrimSpace(app.EntryPoint)
	if entryPoint == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(app.Runtime), "python") && strings.Contains(entryPoint, ":") {
		return nil
	}
	target := entryPoint
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	if st, err := os.Stat(target); err == nil && !st.IsDir() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o775); err != nil {
		return err
	}
	content := ""
	switch strings.ToLower(strings.TrimSpace(app.Runtime)) {
	case "go":
		content = `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "DeployCP Go platform is running on port %s\n", port)
	})
	_ = http.ListenAndServe(":"+port, nil)
}
`
	case "node":
		content = `const http = require('http');
const port = parseInt(process.env.PORT || '3000', 10);

const server = http.createServer((req, res) => {
  if (req.url === '/health') {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('ok');
    return;
  }
  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end('DeployCP Node.js platform is running on port ' + port + '\n');
});

server.listen(port, '0.0.0.0');
`
	case "python":
		content = `import os
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.getenv("PORT", "8000"))

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(f"DeployCP Python platform is running on port {PORT}\n".encode())

server = HTTPServer(("0.0.0.0", PORT), Handler)
server.serve_forever()
`
	default:
		return nil
	}
	return os.WriteFile(target, []byte(content), 0o664)
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
	in.EntryPoint = normalizePythonProcessManagerEntryPoint(in.Runtime, in.ProcessManager, in.EntryPoint)
	return in
}

func normalizePythonProcessManagerEntryPoint(runtime, processManager, entryPoint string) string {
	entryPoint = strings.TrimSpace(entryPoint)
	if !strings.EqualFold(strings.TrimSpace(runtime), "python") {
		return entryPoint
	}
	pm := normalizeProcessManager(processManager)
	switch pm {
	case "gunicorn", "uwsgi":
	default:
		return entryPoint
	}
	if entryPoint == "" {
		return "app:app"
	}
	if strings.Contains(entryPoint, ":") {
		return entryPoint
	}
	withoutExt := strings.TrimSuffix(entryPoint, ".py")
	withoutExt = strings.Trim(strings.ReplaceAll(withoutExt, "\\", "/"), "/")
	if withoutExt == "" {
		return "app:app"
	}
	module := strings.ReplaceAll(withoutExt, "/", ".")
	return module + ":app"
}

func normalizeProcessManager(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "systemd"
	}
	return v
}

func appSystemdServiceName(name string) string {
	return "deploycp-app-" + appSystemdServiceSlug(name)
}

func appSystemdServiceSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "platform"
	}
	return slug
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

func runtimeSudoerUsersForSite(site *models.Website, repo *repositories.SiteUserRepository) []models.SiteUser {
	if site == nil {
		return nil
	}
	var users []models.SiteUser
	seen := map[uint]struct{}{}
	seenNames := map[string]struct{}{}
	add := func(user models.SiteUser) {
		username := strings.TrimSpace(user.Username)
		if username == "" {
			return
		}
		if user.ID > 0 {
			if _, ok := seen[user.ID]; ok {
				return
			}
			seen[user.ID] = struct{}{}
		}
		key := strings.ToLower(username)
		if _, ok := seenNames[key]; ok {
			return
		}
		seenNames[key] = struct{}{}
		users = append(users, user)
	}
	if site.SiteUser != nil {
		add(*site.SiteUser)
	}
	if repo != nil {
		if additional, err := repo.ListByWebsite(site.ID); err == nil {
			for _, user := range additional {
				add(user)
			}
		}
	}
	return users
}

func writeSiteUserRuntimeSudoers(ctx context.Context, cfg *config.Config, username, serviceName string) error {
	if !shouldManageRuntimeSudoers(cfg) {
		return nil
	}
	username = strings.TrimSpace(username)
	serviceName = strings.TrimSpace(serviceName)
	if username == "" || serviceName == "" {
		return nil
	}
	path := siteUserRuntimeSudoersPath(username, serviceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	content := siteUserRuntimeSudoersContent(cfg, username, serviceName)
	if err := utils.WriteFileAtomic(path, []byte(content), 0o440); err != nil {
		return err
	}
	if err := validateSudoersFile(ctx, path); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func removeSiteUserRuntimeSudoers(cfg *config.Config, username, serviceName string) error {
	if !shouldManageRuntimeSudoers(cfg) {
		return nil
	}
	path := siteUserRuntimeSudoersPath(username, serviceName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func shouldManageRuntimeSudoers(cfg *config.Config) bool {
	return cfg != nil && cfg.Features.PlatformMode != "dryrun" && goruntime.GOOS == "linux"
}

func siteUserRuntimeSudoersPath(username, serviceName string) string {
	name := fmt.Sprintf("deploycp-%s-%s", sudoersFileToken(username), sudoersFileToken(serviceName))
	return filepath.Join("/etc/sudoers.d", name)
}

func sudoersFileToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	token := strings.Trim(b.String(), "-")
	if token == "" {
		return "runtime"
	}
	return token
}

func siteUserRuntimeSudoersContent(cfg *config.Config, username, serviceName string) string {
	commands := siteUserRuntimeSudoCommands(cfg, serviceName)
	return fmt.Sprintf("# Managed by DeployCP. Do not edit.\n%s ALL=(root) NOPASSWD: %s\n", username, strings.Join(commands, ", "))
}

func siteUserRuntimeSudoCommands(cfg *config.Config, serviceName string) []string {
	var systemctlPaths []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		for _, existing := range systemctlPaths {
			if existing == path {
				return
			}
		}
		systemctlPaths = append(systemctlPaths, path)
	}
	if cfg != nil {
		add(cfg.Paths.SystemctlBinary)
	}
	add("/bin/systemctl")
	add("/usr/bin/systemctl")

	actions := []string{"start", "stop", "restart", "status", "is-active"}
	var commands []string
	for _, systemctl := range systemctlPaths {
		for _, action := range actions {
			commands = append(commands, fmt.Sprintf("%s %s %s", systemctl, action, serviceName))
			commands = append(commands, fmt.Sprintf("%s %s %s.service", systemctl, action, serviceName))
		}
		commands = append(commands, fmt.Sprintf("%s --no-pager status %s", systemctl, serviceName))
		commands = append(commands, fmt.Sprintf("%s --no-pager status %s.service", systemctl, serviceName))
	}
	return commands
}

func validateSudoersFile(ctx context.Context, path string) error {
	visudo, err := exec.LookPath("visudo")
	if err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, visudo, "-cf", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("validate sudoers file %s: %w: %s", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func buildAppServiceDefinition(app *models.GoApp, env map[string]string) platform.ServiceDefinition {
	base := platform.ServiceDefinition{
		Name:          app.ServiceName,
		Description:   "DeployCP app: " + app.Name,
		WorkingDir:    absoluteRuntimePath(appServiceWorkingDir(app)),
		Environment:   env,
		RestartPolicy: app.RestartPolicy,
		StdoutPath:    absoluteRuntimePath(app.StdoutLogPath),
		StderrPath:    absoluteRuntimePath(app.StderrLogPath),
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
		if app.Runtime == "go" {
			args = append(args, strings.Fields(strings.TrimSpace(app.StartArgs))...)
			args = append(args, app.EntryPoint)
			return args
		}
		args = append(args, app.EntryPoint)
	}
	startArgs := strings.Fields(strings.TrimSpace(app.StartArgs))
	args = append(args, startArgs...)
	return args
}
