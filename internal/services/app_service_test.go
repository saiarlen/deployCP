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

	if err := svc.cleanupDanglingNginxEnabledConfigs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(badLink); !os.IsNotExist(err) {
		t.Fatalf("dangling nginx symlink still exists: %v", err)
	}
	if _, err := os.Lstat(goodLink); err != nil {
		t.Fatalf("valid nginx symlink removed: %v", err)
	}
}
