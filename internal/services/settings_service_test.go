package services

import (
	"os"
	"path/filepath"
	"testing"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testSettingsService(t *testing.T) (*SettingsService, *repositories.Repositories) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{
		Security: config.SecurityConfig{
			SessionSecret: "test-secret",
		},
		Paths: config.PathsConfig{
			RuntimeRoot: filepath.Join(tmp, "runtimes"),
		},
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "deploycp.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Setting{}, &models.UserPreference{}, &models.AuditLog{}, &models.ActivityLog{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	repos := repositories.New(db)
	audit := NewAuditService(repos.Audit, repos.Activity)
	return NewSettingsService(cfg, repos.Settings, repos.UserPrefs, audit), repos
}

func writeRuntimeWrapper(t *testing.T, root, runtime, version, body string) {
	t.Helper()
	command := defaultRuntimeCommand(runtime)
	if command == "" {
		t.Fatalf("no command for runtime %s", runtime)
	}
	path := filepath.Join(root, runtime, version, "bin", command)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
}

func TestInstalledRuntimeVersionsIgnoresIncompleteDirs(t *testing.T) {
	service, _ := testSettingsService(t)

	writeRuntimeWrapper(t, service.cfg.Paths.RuntimeRoot, "go", "go1.26.2", `echo "go version go1.26.2 linux/amd64"`)
	incompleteDir := filepath.Join(service.cfg.Paths.RuntimeRoot, "go", "go1.25.1", "bin")
	if err := os.MkdirAll(incompleteDir, 0o755); err != nil {
		t.Fatalf("mkdir incomplete dir: %v", err)
	}

	got := service.installedRuntimeVersions("go")
	if len(got) != 1 || got[0] != "go1.26.2" {
		t.Fatalf("unexpected installed versions: %#v", got)
	}
}

func TestSyncInstalledRuntimeCatalogClearsStaleConfiguredVersions(t *testing.T) {
	service, repos := testSettingsService(t)
	if err := repos.Settings.Set("go_versions", "go1.26.2", false); err != nil {
		t.Fatalf("seed setting: %v", err)
	}

	if err := service.syncInstalledRuntimeCatalog("go"); err != nil {
		t.Fatalf("sync installed runtime catalog: %v", err)
	}

	value, err := repos.Settings.Get("go_versions")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if value != "" {
		t.Fatalf("expected stale runtime catalog to be cleared, got %q", value)
	}
}

func TestRuntimeVersionStatesMarkVerifiedWrappersReady(t *testing.T) {
	service, _ := testSettingsService(t)

	writeRuntimeWrapper(t, service.cfg.Paths.RuntimeRoot, "node", "node24.3.0", `echo "v24.3.0"`)
	states := service.RuntimeVersionStates("node")
	if len(states) != 1 {
		t.Fatalf("expected one runtime state, got %d", len(states))
	}
	if states[0].Version != "node24.3.0" || !states[0].Verified {
		t.Fatalf("unexpected runtime state: %+v", states[0])
	}
}

func TestAddRuntimeVersionAcceptsFreshVerifiedInstall(t *testing.T) {
	service, repos := testSettingsService(t)

	writeRuntimeWrapper(t, service.cfg.Paths.RuntimeRoot, "go", "go1.26.2", `echo "go version go1.26.2 linux/amd64"`)

	if err := service.AddRuntimeVersion("go", "go1.26.2", nil, ""); err != nil {
		t.Fatalf("add runtime version: %v", err)
	}

	value, err := repos.Settings.Get("go_versions")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if value != "go1.26.2" {
		t.Fatalf("expected persisted runtime version, got %q", value)
	}
}

func TestProtectedRuntimeVersionCannotBeRemoved(t *testing.T) {
	service, _ := testSettingsService(t)

	writeRuntimeWrapper(t, service.cfg.Paths.RuntimeRoot, "python", "python3.12.3", `echo "Python 3.12.3"`)
	metaPath := filepath.Join(service.cfg.Paths.RuntimeRoot, "python", "python3.12.3", ".deploycp-origin")
	if err := os.WriteFile(metaPath, []byte("mode=host-import\n"), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	if !service.ProtectedRuntimeVersion("python", "python3.12.3") {
		t.Fatalf("expected runtime version to be protected")
	}
	if err := service.RemoveRuntimeVersion("python", "python3.12.3", nil, ""); err == nil {
		t.Fatalf("expected protected runtime removal to be blocked")
	}
	states := service.RuntimeVersionStates("python")
	if len(states) != 1 || !states[0].Protected || !states[0].Imported || !states[0].Verified {
		t.Fatalf("unexpected runtime state: %+v", states)
	}
}

func TestPHPFPMVersionChoicesOnlyUsesInstalledFPMOnLiveMode(t *testing.T) {
	service, repos := testSettingsService(t)
	if err := repos.Settings.Set("php_versions", "99.9", false); err != nil {
		t.Fatalf("seed php versions: %v", err)
	}

	for _, version := range service.PHPFPMVersionChoices() {
		if version == "99.9" {
			t.Fatalf("configured PHP CLI/runtime version should not be offered as PHP-FPM choice: %#v", service.PHPFPMVersionChoices())
		}
	}
}

func TestFilterOutFullyInstalledPHPVersionsKeepsHostImportWithoutFPM(t *testing.T) {
	candidates := []string{"8.3", "8.4", "8.5"}
	installedRuntime := []string{"8.3", "8.4.2"}
	installedFPM := []string{"8.4"}

	got := filterOutFullyInstalledPHPVersions(candidates, installedRuntime, installedFPM)
	want := []string{"8.3", "8.5"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
