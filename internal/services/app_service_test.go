package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"deploycp/internal/config"
	"deploycp/internal/models"
)

func TestAppSystemdServiceNameUsesStableDomainSlug(t *testing.T) {
	tests := map[string]string{
		"cpl.appxcube.in":       "deploycp-app-cpl-appxcube-in",
		"  Money Bloom CL  ":    "deploycp-app-money-bloom-cl",
		"app_test.example.com":  "deploycp-app-app-test-example-com",
		"***":                   "deploycp-app-platform",
		"CAPS.Domain.EXAMPLE":   "deploycp-app-caps-domain-example",
		"site--with__symbols..": "deploycp-app-site-with-symbols",
	}

	for input, want := range tests {
		if got := appSystemdServiceName(input); got != want {
			t.Fatalf("appSystemdServiceName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSiteUserRuntimeSudoersContentAllowsOnlyRuntimeServiceActions(t *testing.T) {
	cfg := &config.Config{}
	cfg.Paths.SystemctlBinary = "/bin/systemctl"

	content := siteUserRuntimeSudoersContent(cfg, "cpluser", "deploycp-app-cpl-appxcube-in")
	required := []string{
		"cpluser ALL=(root) NOPASSWD:",
		"/bin/systemctl start deploycp-app-cpl-appxcube-in",
		"/bin/systemctl stop deploycp-app-cpl-appxcube-in",
		"/bin/systemctl restart deploycp-app-cpl-appxcube-in",
		"/bin/systemctl status deploycp-app-cpl-appxcube-in",
		"/bin/systemctl is-active deploycp-app-cpl-appxcube-in",
		"/usr/bin/systemctl restart deploycp-app-cpl-appxcube-in.service",
	}
	for _, item := range required {
		if !strings.Contains(content, item) {
			t.Fatalf("sudoers content missing %q:\n%s", item, content)
		}
	}
	if strings.Contains(content, " enable ") || strings.Contains(content, " disable ") {
		t.Fatalf("sudoers content should not grant enable/disable:\n%s", content)
	}
}

func TestLinkedAppServiceUsesHtdocsWorkingDirectory(t *testing.T) {
	websiteID := uint(42)
	app := &models.GoApp{
		Name:             "example.com",
		ServiceName:      "deploycp-app-example-com",
		Runtime:          "python",
		ProcessManager:   "uwsgi",
		BinaryPath:       "/srv/example/.deploycp/python-venv/bin/uwsgi",
		EntryPoint:       "app:app",
		WorkingDirectory: "/srv/example/htdocs",
		WebsiteID:        &websiteID,
		Host:             "127.0.0.1",
		Port:             5000,
	}

	def := buildAppServiceDefinition(app, map[string]string{})
	if def.WorkingDir != "/srv/example/htdocs" {
		t.Fatalf("WorkingDir = %q, want htdocs", def.WorkingDir)
	}
	if got := pythonRuntimeVenvPathForApp(app); got != "/srv/example/.deploycp/python-venv" {
		t.Fatalf("python venv path = %q", got)
	}
}

func TestPythonVenvVersionMarker(t *testing.T) {
	dir := t.TempDir()
	if err := writePythonVenvVersionMarker(dir, "3.12.4"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".deploycp-runtime-version"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "3.12.4" {
		t.Fatalf("marker = %q", string(content))
	}
}

func TestNodeRuntimeToolsPathForLinkedApp(t *testing.T) {
	websiteID := uint(42)
	app := &models.GoApp{
		WorkingDirectory: "/srv/example/htdocs",
		WebsiteID:        &websiteID,
	}

	if got := nodeRuntimeToolsPathForApp(app); got != "/srv/example/.deploycp/node-tools" {
		t.Fatalf("node tools path = %q", got)
	}
}

func TestNodeToolsVersionMarker(t *testing.T) {
	dir := t.TempDir()
	if err := writeNodeToolsVersionMarker(dir, "node22.16.0"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".deploycp-runtime-version"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "node22.16.0" {
		t.Fatalf("marker = %q", string(content))
	}
}

func TestNodeInstallArgsUsesPackageLockWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if got := strings.Join(nodeInstallArgs(dir), " "); got != "install --omit=dev" {
		t.Fatalf("node install args without lock = %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(nodeInstallArgs(dir), " "); got != "ci --omit=dev" {
		t.Fatalf("node install args with lock = %q", got)
	}
}

func TestPM2RuntimeServiceDefinitionOmitsUnsupportedExecModeFlag(t *testing.T) {
	app := &models.GoApp{
		Name:             "example.com",
		ServiceName:      "deploycp-app-example-com",
		Runtime:          "node",
		ProcessManager:   "pm2",
		BinaryPath:       "/srv/example/.deploycp/node-tools/node_modules/.bin/pm2-runtime",
		EntryPoint:       "index.js",
		WorkingDirectory: "/srv/example/htdocs",
		Workers:          2,
		ExecMode:         "cluster",
	}

	def := buildAppServiceDefinition(app, nil)
	got := strings.Join(def.Args, " ")
	if strings.Contains(got, "--exec-mode") {
		t.Fatalf("pm2-runtime args contain unsupported --exec-mode flag: %q", got)
	}
	if !strings.Contains(got, "-i 2") {
		t.Fatalf("pm2-runtime args should keep worker count: %q", got)
	}
}

func TestPM2NodeNPMEntryPointResolvesFromRuntimePath(t *testing.T) {
	binDir := t.TempDir()
	npmPath := filepath.Join(binDir, "npm")
	if err := os.WriteFile(npmPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := &models.GoApp{
		Name:             "example.com",
		ServiceName:      "deploycp-app-example-com",
		Runtime:          "node",
		ProcessManager:   "pm2",
		BinaryPath:       "/srv/example/.deploycp/node-tools/node_modules/.bin/pm2-runtime",
		EntryPoint:       "npm",
		WorkingDirectory: "/srv/example/htdocs",
		StartArgs:        "-- start",
	}

	def := buildAppServiceDefinition(app, map[string]string{"PATH": binDir})
	got := strings.Join(def.Args, " ")
	if !strings.Contains(got, npmPath) {
		t.Fatalf("pm2 npm entrypoint was not resolved from PATH: %q", got)
	}
	if strings.Contains(got, " start npm ") {
		t.Fatalf("pm2 args still use bare npm entrypoint: %q", got)
	}
}

func TestNormalizeAppInputDefaultsNodePM2PackageStart(t *testing.T) {
	in := normalizeAppInput(AppInput{
		Runtime:        "node",
		ProcessManager: "pm2",
	})
	if in.BinaryPath != "pm2" {
		t.Fatalf("BinaryPath = %q, want pm2", in.BinaryPath)
	}
	if in.EntryPoint != "npm" {
		t.Fatalf("EntryPoint = %q, want npm", in.EntryPoint)
	}
	if in.StartArgs != "-- start" {
		t.Fatalf("StartArgs = %q, want -- start", in.StartArgs)
	}
}

func TestBuildAppServiceDefinitionNormalizesMemoryLimit(t *testing.T) {
	app := &models.GoApp{
		Name:             "example.com",
		ServiceName:      "deploycp-app-example-com",
		Runtime:          "go",
		ProcessManager:   "systemd",
		BinaryPath:       "/srv/example/app",
		WorkingDirectory: "/srv/example/htdocs",
		MaxMemory:        "512",
	}

	def := buildAppServiceDefinition(app, nil)
	if def.MemoryMax != "512M" {
		t.Fatalf("MemoryMax = %q, want 512M", def.MemoryMax)
	}
}

func TestNormalizeAppMaxMemoryTreatsZeroAsUnset(t *testing.T) {
	for _, input := range []string{"0", "0m", "0MB"} {
		got, err := normalizeAppMaxMemory(input)
		if err != nil {
			t.Fatalf("normalizeAppMaxMemory(%q) returned error: %v", input, err)
		}
		if got != "" {
			t.Fatalf("normalizeAppMaxMemory(%q) = %q, want empty", input, got)
		}
	}
}

func TestPM2RuntimeServiceDefinitionUsesMemoryRestartOnly(t *testing.T) {
	app := &models.GoApp{
		Name:             "example.com",
		ServiceName:      "deploycp-app-example-com",
		Runtime:          "node",
		ProcessManager:   "pm2",
		BinaryPath:       "/srv/example/.deploycp/node-tools/node_modules/.bin/pm2-runtime",
		EntryPoint:       "index.js",
		WorkingDirectory: "/srv/example/htdocs",
		MaxMemory:        "512mb",
	}

	def := buildAppServiceDefinition(app, nil)
	got := strings.Join(def.Args, " ")
	if !strings.Contains(got, "--max-memory-restart 512M") {
		t.Fatalf("pm2 args missing normalized memory restart: %q", got)
	}
	if def.MemoryMax != "" {
		t.Fatalf("pm2 service should not also set systemd MemoryMax, got %q", def.MemoryMax)
	}
}

func TestCleanupDanglingNginxEnabledConfigs(t *testing.T) {
	enabledDir := t.TempDir()
	availableDir := t.TempDir()
	goodTarget := filepath.Join(availableDir, "good.conf")
	if err := os.WriteFile(goodTarget, []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goodLink := filepath.Join(enabledDir, "good.conf")
	if err := os.Symlink(goodTarget, goodLink); err != nil {
		t.Fatal(err)
	}
	badLink := filepath.Join(enabledDir, "missing.conf")
	if err := os.Symlink(filepath.Join(availableDir, "missing.conf"), badLink); err != nil {
		t.Fatal(err)
	}
	svc := &WebsiteService{cfg: &config.Config{}}
	svc.cfg.Paths.NginxEnabledDir = enabledDir
	svc.cfg.Paths.NginxAvailableDir = availableDir

	if err := svc.cleanupDanglingNginxConfigEntries(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(badLink); !os.IsNotExist(err) {
		t.Fatalf("dangling nginx symlink still exists: %v", err)
	}
	if _, err := os.Lstat(goodLink); err != nil {
		t.Fatalf("valid nginx symlink removed: %v", err)
	}
}

func TestBuildAppServiceDefinitionMakesServicePathsAbsolute(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	app := &models.GoApp{
		Name:             "example.com",
		ServiceName:      "deploycp-app-example-com",
		Runtime:          "python",
		ProcessManager:   "systemd",
		BinaryPath:       "/usr/bin/python3",
		EntryPoint:       "app.py",
		WorkingDirectory: "storage/sites/example.com/htdocs",
		StdoutLogPath:    "storage/logs/apps/example.com/stdout.log",
		StderrLogPath:    "storage/logs/apps/example.com/stderr.log",
	}

	def := buildAppServiceDefinition(app, nil)
	for label, path := range map[string]string{
		"working dir": def.WorkingDir,
		"stdout":      def.StdoutPath,
		"stderr":      def.StderrPath,
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s is not absolute: %s", label, path)
		}
		if !strings.HasPrefix(path, cwd) {
			t.Fatalf("%s = %s, want path under %s", label, path, cwd)
		}
	}
}

func TestNormalizePythonProcessManagerEntryPoint(t *testing.T) {
	tests := map[string]string{
		"":              "app:app",
		"app.py":        "app:app",
		"server.py":     "server:app",
		"src/server.py": "src.server:app",
		"custom:wsgi":   "custom:wsgi",
	}
	for input, want := range tests {
		if got := normalizePythonProcessManagerEntryPoint("python", "uwsgi", input); got != want {
			t.Fatalf("entry %q = %q, want %q", input, got, want)
		}
	}
	if got := normalizePythonProcessManagerEntryPoint("python", "systemd", "app.py"); got != "app.py" {
		t.Fatalf("systemd entry changed to %q", got)
	}
}
