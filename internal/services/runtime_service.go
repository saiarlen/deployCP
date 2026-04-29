package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/system"
	"deploycp/internal/utils"
)

type RuntimeService struct {
	cfg    *config.Config
	runner *system.Runner
	audit  *AuditService
}

type RuntimeDefaultStatus struct {
	Runtime string
	Version string
	Binary  string
	Managed bool
}

type RuntimeActionResult struct {
	Stdout string
	Stderr string
}

type RuntimeInspection struct {
	Applicable      bool
	Runtime         string
	SelectedVersion string
	SSHVersion      string
	SSHBinary       string
	ServiceVersion  string
	ServiceBinary   string
	Healthy         bool
	Issues          []string
}

var (
	goVersionOutRe     = regexp.MustCompile(`go([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
	nodeVersionOutRe   = regexp.MustCompile(`v([0-9]+(?:\.[0-9]+){0,2})`)
	pythonVersionOutRe = regexp.MustCompile(`Python\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
	phpVersionOutRe    = regexp.MustCompile(`PHP\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
)

func NewRuntimeService(cfg *config.Config, runner *system.Runner, audit *AuditService) *RuntimeService {
	return &RuntimeService{cfg: cfg, runner: runner, audit: audit}
}

func (s *RuntimeService) InstallVersion(ctx context.Context, runtime, version string, actor *uint, ip string) (RuntimeActionResult, error) {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if runtime == "" || version == "" {
		return RuntimeActionResult{}, fmt.Errorf("runtime and version are required")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return RuntimeActionResult{}, s.ensureRuntimeBinDir(runtime, version)
	}
	result, err := s.runRuntimeAction(ctx, "install", runtime, version, 15*time.Minute, "runtime.install", actor, ip)
	if err != nil {
		return result, err
	}
	s.audit.Record(actor, "runtime.install", "runtime_version", runtime+":"+version, ip, nil)
	return result, nil
}

func (s *RuntimeService) RemoveVersion(ctx context.Context, runtime, version string, actor *uint, ip string) (RuntimeActionResult, error) {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if runtime == "" || version == "" {
		return RuntimeActionResult{}, fmt.Errorf("runtime and version are required")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return RuntimeActionResult{}, os.RemoveAll(s.runtimeVersionDir(runtime, version))
	}
	result, err := s.runRuntimeAction(ctx, "remove", runtime, version, 10*time.Minute, "runtime.remove", actor, ip)
	if err != nil {
		return result, err
	}
	s.audit.Record(actor, "runtime.remove", "runtime_version", runtime+":"+version, ip, nil)
	return result, nil
}

func (s *RuntimeService) SetSystemDefaultVersion(ctx context.Context, runtime, version string, actor *uint, ip string) (RuntimeActionResult, error) {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if runtime == "" || version == "" {
		return RuntimeActionResult{}, fmt.Errorf("runtime and version are required")
	}
	if s.cfg.Features.PlatformMode == "dryrun" {
		return RuntimeActionResult{}, nil
	}
	result, err := s.runRuntimeAction(ctx, "set-default", runtime, version, 2*time.Minute, "runtime.default.set", actor, ip)
	if err != nil {
		return result, err
	}
	s.audit.Record(actor, "runtime.default.set", "runtime_default", runtime+":"+version, ip, nil)
	return result, nil
}

func (s *RuntimeService) SystemDefaultVersion(runtime string) RuntimeDefaultStatus {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	command := defaultRuntimeCommand(runtime)
	if command == "" {
		return RuntimeDefaultStatus{Runtime: runtime}
	}
	binary, err := exec.LookPath(command)
	if err != nil {
		return RuntimeDefaultStatus{Runtime: runtime}
	}
	version := detectRuntimeVersion(runtime, binary)
	status := RuntimeDefaultStatus{
		Runtime: runtime,
		Version: version,
		Binary:  binary,
	}
	runtimeRoot := filepath.Clean(strings.TrimSpace(s.cfg.Paths.RuntimeRoot))
	if runtimeRoot != "" && strings.HasPrefix(filepath.Clean(binary), runtimeRoot+string(filepath.Separator)) {
		status.Managed = true
	}
	return status
}

func (s *RuntimeService) ApplyPlatformRuntime(rootPath, runtime, version string, actor *uint, ip string) error {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if rootPath == "" || rootPath == "." {
		return nil
	}
	runtimeEnvPath := filepath.Join(rootPath, ".deploycp", "runtime.env")
	if runtime == "" || version == "" {
		if err := os.Remove(runtimeEnvPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	binDir := filepath.Join(s.runtimeVersionDir(runtime, version), "bin")
	lines := []string{
		"# DeployCP runtime selection",
		fmt.Sprintf("export DEPLOYCP_RUNTIME=%q", runtime),
		fmt.Sprintf("export DEPLOYCP_RUNTIME_VERSION=%q", version),
	}
	if runtime == "go" {
		lines = append(lines, fmt.Sprintf("export GOROOT=%q", s.runtimeVersionDir(runtime, version)))
	}
	lines = append(lines, fmt.Sprintf("export PATH=%q:$PATH", binDir))
	if err := utils.WriteFileAtomic(runtimeEnvPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	s.audit.Record(actor, "runtime.apply", "runtime_env", rootPath, ip, map[string]string{"runtime": runtime, "version": version})
	return nil
}

func (s *RuntimeService) ApplyPHPWebsiteRuntime(rootPath, version string, actor *uint, ip string) error {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	version = strings.TrimSpace(version)
	if rootPath == "" || rootPath == "." {
		return nil
	}
	if version == "" {
		return s.ApplyPlatformRuntime(rootPath, "", "", actor, ip)
	}
	if binary := s.findSystemPHPBinary(version); binary != "" {
		return s.applyDirectBinaryRuntime(rootPath, "php", version, "php", binary, actor, ip)
	}
	return s.ApplyPlatformRuntime(rootPath, "php", version, actor, ip)
}

func (s *RuntimeService) VerifyInstalledVersion(runtime, version string) bool {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if runtime == "" || version == "" {
		return false
	}
	binary := s.runtimeManagedBinary(runtime, version)
	if binary == "" {
		return false
	}
	actual := detectRuntimeVersion(runtime, binary)
	return runtimeVersionMatches(runtime, version, actual)
}

func (s *RuntimeService) InspectAppRuntime(app *models.GoApp) RuntimeInspection {
	if app == nil {
		return RuntimeInspection{}
	}
	runtimeName := strings.ToLower(strings.TrimSpace(app.Runtime))
	inspection := RuntimeInspection{
		Runtime:    runtimeName,
		Applicable: runtimeName != "" && runtimeName != "binary",
	}
	if !inspection.Applicable {
		inspection.Healthy = true
		return inspection
	}
	selected := appEnvValue(app.EnvVars, "RUNTIME_VERSION")
	inspection.SelectedVersion = selected
	if selected == "" {
		inspection.Issues = append(inspection.Issues, "No runtime version is selected for this platform.")
		return inspection
	}
	root := platformRuntimeRootForApp(app)
	inspection.SSHBinary = s.PlatformShellBinaryPath(root, runtimeName, selected)
	inspection.SSHVersion = s.PlatformShellVersion(root, runtimeName, selected)
	if inspection.SSHVersion == "" {
		inspection.Issues = append(inspection.Issues, "SSH/runtime shell could not resolve the selected version.")
	} else if !runtimeVersionMatches(runtimeName, selected, inspection.SSHVersion) {
		inspection.Issues = append(inspection.Issues, fmt.Sprintf("SSH resolves %s instead of %s.", inspection.SSHVersion, selected))
	}

	serviceBinary, serviceVersion, serviceIssue := s.inspectAppServiceRuntime(app, selected)
	inspection.ServiceBinary = serviceBinary
	inspection.ServiceVersion = serviceVersion
	if serviceIssue != "" {
		inspection.Issues = append(inspection.Issues, serviceIssue)
	} else if serviceVersion == "" {
		inspection.Issues = append(inspection.Issues, "Service runtime version could not be resolved.")
	} else if !runtimeVersionMatches(runtimeName, selected, serviceVersion) {
		inspection.Issues = append(inspection.Issues, fmt.Sprintf("Service resolves %s instead of %s.", serviceVersion, selected))
	}

	inspection.Healthy = len(inspection.Issues) == 0
	return inspection
}

func (s *RuntimeService) PlatformShellVersion(rootPath, runtime, version string) string {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if rootPath == "" || rootPath == "." || runtime == "" {
		return ""
	}
	if binary := s.platformShellBinary(rootPath, runtime, version); binary != "" {
		return detectRuntimeVersion(runtime, binary)
	}
	return ""
}

func (s *RuntimeService) PlatformShellBinaryPath(rootPath, runtime, version string) string {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	if rootPath == "" || rootPath == "." || runtime == "" {
		return ""
	}
	return s.platformShellBinary(rootPath, runtime, version)
}

func (s *RuntimeService) UsesManagedPlatformRuntime(rootPath, runtime, version string) bool {
	binary := s.PlatformShellBinaryPath(rootPath, runtime, version)
	if binary == "" {
		return false
	}
	expectedRoot := filepath.Clean(s.runtimeVersionDir(runtime, version))
	return strings.HasPrefix(filepath.Clean(binary), expectedRoot+string(filepath.Separator))
}

func (s *RuntimeService) ResolveBinary(runtime, version, requestedBinary string) (string, error) {
	binary := strings.TrimSpace(requestedBinary)
	if binary == "" {
		return "", fmt.Errorf("binary path is required")
	}
	version = strings.TrimSpace(version)
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	if version != "" {
		if preferred := s.preferredRuntimeBinary(runtime, binary); preferred != "" {
			candidate := filepath.Join(s.runtimeVersionDir(runtime, version), "bin", preferred)
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				return candidate, nil
			}
		}
	}
	if filepath.IsAbs(binary) {
		return binary, nil
	}
	if version != "" {
		candidate := filepath.Join(s.runtimeVersionDir(runtime, version), "bin", filepath.Base(binary))
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	lookedUp, err := exec.LookPath(binary)
	if err == nil {
		return lookedUp, nil
	}
	return binary, nil
}

func (s *RuntimeService) preferredRuntimeBinary(runtime, requestedBinary string) string {
	switch strings.TrimSpace(requestedBinary) {
	case "env", "/usr/bin/env", "/bin/env":
		switch runtime {
		case "go":
			return "go"
		case "node":
			return "node"
		case "python":
			return "python3"
		case "php":
			return "php"
		}
	}
	return ""
}

func (s *RuntimeService) MergeRuntimeEnv(runtime, version string, env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+4)
	for k, v := range env {
		out[k] = v
	}
	version = strings.TrimSpace(version)
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	if runtime == "" || version == "" {
		return out
	}
	binDir := filepath.Join(s.runtimeVersionDir(runtime, version), "bin")
	if existing := strings.TrimSpace(out["PATH"]); existing != "" {
		out["PATH"] = binDir + ":" + existing
	} else {
		out["PATH"] = binDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	out["DEPLOYCP_RUNTIME"] = runtime
	out["DEPLOYCP_RUNTIME_VERSION"] = version
	if runtime == "go" {
		out["GOROOT"] = s.runtimeVersionDir(runtime, version)
	}
	return out
}

func (s *RuntimeService) applyDirectBinaryRuntime(rootPath, runtime, version, commandName, binaryPath string, actor *uint, ip string) error {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	version = strings.TrimSpace(version)
	commandName = strings.TrimSpace(commandName)
	binaryPath = strings.TrimSpace(binaryPath)
	if rootPath == "" || rootPath == "." || runtime == "" || version == "" || commandName == "" || binaryPath == "" {
		return nil
	}
	bridgeDir := filepath.Join(rootPath, ".deploycp", "runtime-bin")
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		return err
	}
	wrapper := filepath.Join(bridgeDir, commandName)
	script := fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", binaryPath)
	if err := utils.WriteFileAtomic(wrapper, []byte(script), 0o755); err != nil {
		return err
	}
	runtimeEnvPath := filepath.Join(rootPath, ".deploycp", "runtime.env")
	lines := []string{
		"# DeployCP runtime selection",
		fmt.Sprintf("export DEPLOYCP_RUNTIME=%q", runtime),
		fmt.Sprintf("export DEPLOYCP_RUNTIME_VERSION=%q", version),
		fmt.Sprintf("export DEPLOYCP_RUNTIME_BINARY=%q", binaryPath),
		fmt.Sprintf("export PATH=%q:$PATH", bridgeDir),
	}
	if err := utils.WriteFileAtomic(runtimeEnvPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	s.audit.Record(actor, "runtime.apply", "runtime_env", rootPath, ip, map[string]string{
		"runtime": runtime,
		"version": version,
		"binary":  binaryPath,
		"mode":    "direct",
	})
	return nil
}

func (s *RuntimeService) findSystemPHPBinary(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	candidates := []string{
		"php" + version,
		"php" + strings.ReplaceAll(version, ".", ""),
	}
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path
		}
	}
	return ""
}

func (s *RuntimeService) helperScriptPath() (string, error) {
	candidates := []string{
		filepath.Join(".", "scripts", "linux", "runtime-manager.sh"),
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "scripts", "linux", "runtime-manager.sh"))
	}
	for _, candidate := range candidates {
		clean := filepath.Clean(candidate)
		if st, err := os.Stat(clean); err == nil && !st.IsDir() {
			return clean, nil
		}
	}
	return "", fmt.Errorf("runtime helper script not found")
}

func (s *RuntimeService) inspectAppServiceRuntime(app *models.GoApp, selected string) (string, string, string) {
	if app == nil {
		return "", "", ""
	}
	runtimeName := strings.ToLower(strings.TrimSpace(app.Runtime))
	processManager := normalizeProcessManager(app.ProcessManager)
	if processManager == "pm2" {
		if binary, version, ok := s.inspectLiveProcessRuntime(app, selected); ok {
			return binary, version, ""
		}
		return "", "", "PM2-managed services could not be live-verified from the current process list."
	}
	if processManager == "gunicorn" || processManager == "uwsgi" {
		if binary, version, ok := s.inspectLiveProcessRuntime(app, selected); ok {
			return binary, version, ""
		}
		return "", "", "Gunicorn/uWSGI services could not be live-verified from the current process list."
	}
	binary, err := s.ResolveBinary(runtimeName, selected, app.BinaryPath)
	if err != nil {
		return "", "", err.Error()
	}
	return binary, detectRuntimeVersion(runtimeName, binary), ""
}

func (s *RuntimeService) inspectLiveProcessRuntime(app *models.GoApp, selected string) (string, string, bool) {
	if app == nil || s.runner == nil {
		return "", "", false
	}
	patterns := []string{
		strings.TrimSpace(app.EntryPoint),
		filepath.Base(strings.TrimSpace(app.EntryPoint)),
		strings.TrimSpace(app.ServiceName),
	}
	seen := map[string]struct{}{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		res, err := s.runner.Run(context.Background(), system.CommandRequest{
			Binary:  "pgrep",
			Args:    []string{"-af", pattern},
			Timeout: 5 * time.Second,
		})
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 2 {
				continue
			}
			cmd := fields[1:]
			for _, candidate := range cmd {
				candidate = strings.TrimSpace(candidate)
				if candidate == "" {
					continue
				}
				if candidate == strings.TrimSpace(app.EntryPoint) || filepath.Base(candidate) == filepath.Base(strings.TrimSpace(app.EntryPoint)) {
					continue
				}
				version := detectRuntimeVersion(app.Runtime, candidate)
				if version == "" {
					continue
				}
				if selected == "" || runtimeVersionMatches(app.Runtime, selected, version) {
					return candidate, version, true
				}
			}
		}
	}
	return "", "", false
}

func (s *RuntimeService) platformShellBinary(rootPath, runtime, version string) string {
	if runtime == "php" {
		if binary := parseRuntimeEnvBinary(filepath.Join(rootPath, ".deploycp", "runtime.env")); binary != "" {
			return binary
		}
		wrapper := filepath.Join(rootPath, ".deploycp", "runtime-bin", defaultRuntimeCommand(runtime))
		if st, err := os.Stat(wrapper); err == nil && !st.IsDir() {
			return wrapper
		}
	}
	return s.runtimeManagedBinary(runtime, version)
}

func (s *RuntimeService) runtimeManagedBinary(runtime, version string) string {
	command := defaultRuntimeCommand(runtime)
	if command == "" || strings.TrimSpace(version) == "" {
		return ""
	}
	candidate := filepath.Join(s.runtimeVersionDir(runtime, version), "bin", command)
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate
	}
	return ""
}

func parseRuntimeEnvBinary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "export DEPLOYCP_RUNTIME_BINARY=") {
			continue
		}
		value := strings.TrimPrefix(line, "export DEPLOYCP_RUNTIME_BINARY=")
		value = strings.Trim(value, `"`)
		return strings.TrimSpace(value)
	}
	return ""
}

func (s *RuntimeService) runRuntimeAction(ctx context.Context, action, runtime, version string, timeout time.Duration, auditAction string, actor *uint, ip string) (RuntimeActionResult, error) {
	script, err := s.helperScriptPath()
	if err != nil {
		return RuntimeActionResult{}, err
	}
	res, runErr := s.runner.Run(ctx, system.CommandRequest{
		Binary:      "/bin/bash",
		Args:        []string{script, action, runtime, version, s.cfg.Paths.RuntimeRoot},
		Timeout:     timeout,
		AuditAction: auditAction,
		ActorUserID: actor,
		IP:          ip,
	})
	result := RuntimeActionResult{
		Stdout: strings.TrimSpace(res.Stdout),
		Stderr: strings.TrimSpace(res.Stderr),
	}
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

func (s *RuntimeService) ensureRuntimeBinDir(runtime, version string) error {
	return os.MkdirAll(filepath.Join(s.runtimeVersionDir(runtime, version), "bin"), 0o755)
}

func (s *RuntimeService) runtimeVersionDir(runtime, version string) string {
	return filepath.Join(s.cfg.Paths.RuntimeRoot, strings.ToLower(strings.TrimSpace(runtime)), strings.TrimSpace(version))
}

func defaultRuntimeCommand(runtime string) string {
	switch runtime {
	case "go":
		return "go"
	case "node":
		return "node"
	case "python":
		return "python3"
	case "php":
		return "php"
	default:
		return ""
	}
}

func detectRuntimeVersion(runtime, binary string) string {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return ""
	}
	var cmd *exec.Cmd
	switch runtime {
	case "go":
		cmd = exec.Command(binary, "version")
	case "node":
		cmd = exec.Command(binary, "--version")
	case "python":
		cmd = exec.Command(binary, "--version")
	case "php":
		cmd = exec.Command(binary, "-v")
	default:
		return ""
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(output))
	switch runtime {
	case "go":
		if m := goVersionOutRe.FindStringSubmatch(text); len(m) == 2 {
			return "go" + m[1]
		}
	case "node":
		if m := nodeVersionOutRe.FindStringSubmatch(text); len(m) == 2 {
			return "node" + m[1]
		}
	case "python":
		if m := pythonVersionOutRe.FindStringSubmatch(text); len(m) == 2 {
			return "python" + m[1]
		}
	case "php":
		if m := phpVersionOutRe.FindStringSubmatch(text); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

func runtimeVersionMatches(runtime, selected, actual string) bool {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	selected = strings.TrimSpace(selected)
	actual = strings.TrimSpace(actual)
	if selected == "" || actual == "" {
		return false
	}
	switch runtime {
	case "go":
		return strings.HasPrefix(actual, selected)
	case "node":
		return strings.HasPrefix(actual, selected)
	case "python":
		return strings.HasPrefix(actual, selected)
	case "php":
		return strings.HasPrefix(actual, selected)
	default:
		return strings.EqualFold(actual, selected)
	}
}
