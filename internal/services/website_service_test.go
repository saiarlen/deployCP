package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/platform/dryrun"
	"deploycp/internal/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type rejectingNginxManager struct{}

func (rejectingNginxManager) Validate(context.Context, string) error {
	return errors.New("invalid staged config")
}
func (rejectingNginxManager) Reload(context.Context, string) error { return nil }

type nginxOnlyAdapter struct {
	platform.Adapter
	manager platform.NginxManager
}

func (a nginxOnlyAdapter) Nginx() platform.NginxManager { return a.manager }

func TestNginxTransactionRestoresPreviousConfigOnValidationFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "site.conf")
	if err := os.WriteFile(configPath, []byte("old config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &WebsiteService{
		cfg:     &config.Config{Paths: config.PathsConfig{NginxBinary: "/usr/sbin/nginx"}},
		adapter: nginxOnlyAdapter{manager: rejectingNginxManager{}},
	}
	err := svc.applyNginxTransaction(context.Background(), []string{configPath}, func() error {
		return os.WriteFile(configPath, []byte("broken config\n"), 0o644)
	})
	if err == nil {
		t.Fatal("expected staged nginx validation to fail")
	}
	content, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "old config\n" {
		t.Fatalf("nginx rollback content = %q", content)
	}
}

func TestSiteUserUsesPlatformHome(t *testing.T) {
	platformHome := "/home/deploycp/platforms/sites/example.test"
	cases := []struct {
		name string
		user models.SiteUser
		want bool
	}{
		{name: "matching home", user: models.SiteUser{HomeDirectory: platformHome}, want: true},
		{name: "matching allowed root", user: models.SiteUser{AllowedRoot: platformHome}, want: true},
		{name: "cleaned matching home", user: models.SiteUser{HomeDirectory: platformHome + "/."}, want: true},
		{name: "different platform", user: models.SiteUser{HomeDirectory: "/home/deploycp/platforms/sites/other.test"}, want: false},
		{name: "nested directory", user: models.SiteUser{HomeDirectory: platformHome + "/htdocs"}, want: false},
		{name: "empty paths", user: models.SiteUser{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := siteUserUsesPlatformHome(tc.user, platformHome); got != tc.want {
				t.Fatalf("siteUserUsesPlatformHome() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestUpdatePhpSettingsRewritesNginxSocket(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{}
	cfg.Features.PlatformMode = "dryrun"
	cfg.Features.EnableNginxManage = true
	cfg.Paths.NginxAvailableDir = filepath.Join(tmp, "sites-available")
	cfg.Paths.NginxEnabledDir = filepath.Join(tmp, "sites-enabled")
	cfg.Paths.NginxBinary = "/bin/echo"

	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "deploycp.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Website{}, &models.WebsiteDomain{}, &models.NginxSiteConfig{}, &models.AuditLog{}, &models.ActivityLog{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	repos := repositories.New(db)
	audit := NewAuditService(repos.Audit, repos.Activity)
	svc := NewWebsiteService(
		cfg,
		repos.Websites,
		repos.NginxSites,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		dryrun.New(cfg),
		audit,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	site := &models.Website{
		Name:          "example.com",
		RootPath:      filepath.Join(tmp, "sites", "example.com", "htdocs"),
		Type:          "php",
		PHPVersion:    "8.3",
		AccessLogPath: filepath.Join(tmp, "sites", "example.com", "logs", "access.log"),
		ErrorLogPath:  filepath.Join(tmp, "sites", "example.com", "logs", "error.log"),
		Enabled:       true,
	}
	if err := repos.Websites.Create(site, []string{"example.com"}); err != nil {
		t.Fatalf("create website: %v", err)
	}
	if err := svc.RefreshConfig(context.Background(), site.ID); err != nil {
		t.Fatalf("refresh config: %v", err)
	}
	configPath := filepath.Join(cfg.Paths.NginxAvailableDir, "example.com.conf")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read initial config: %v", err)
	}
	if !strings.Contains(string(before), "8.3-fpm.sock") {
		t.Fatalf("initial config does not contain php8.3 socket:\n%s", before)
	}
	catchall, err := os.ReadFile(filepath.Join(cfg.Paths.NginxAvailableDir, "00-deploycp-catchall.conf"))
	if err != nil {
		t.Fatalf("read unknown-host catchall: %v", err)
	}
	catchallText := string(catchall)
	if !strings.Contains(catchallText, "listen 443 ssl default_server;") ||
		(!strings.Contains(catchallText, "ssl_reject_handshake on;") &&
			(!strings.Contains(catchallText, "ssl_certificate ") || !strings.Contains(catchallText, "return 444;"))) {
		t.Fatalf("catchall does not reject unknown TLS hosts:\n%s", catchall)
	}

	if err := svc.UpdatePhpSettings(context.Background(), site.ID, "8.4", PhpSettingsData{
		MemoryLimit:          "512M",
		MaxExecutionTime:     "120",
		MaxInputTime:         "60",
		MaxInputVars:         "5000",
		PostMaxSize:          "128M",
		UploadMaxFilesize:    "64M",
		AdditionalDirectives: "display_errors = Off",
	}, nil, ""); err != nil {
		t.Fatalf("update php settings: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read updated config: %v", err)
	}
	if !strings.Contains(string(after), "8.4-fpm.sock") {
		t.Fatalf("updated config does not contain php8.4 socket:\n%s", after)
	}
	if strings.Contains(string(after), "8.3-fpm.sock") {
		t.Fatalf("updated config still contains old php8.3 socket:\n%s", after)
	}
	userINI, err := os.ReadFile(filepath.Join(site.RootPath, ".user.ini"))
	if err != nil {
		t.Fatalf("read .user.ini: %v", err)
	}
	userINIText := string(userINI)
	for _, want := range []string{
		"memory_limit = 512M",
		"max_execution_time = 120",
		"max_input_vars = 5000",
		"post_max_size = 128M",
		"upload_max_filesize = 64M",
		"display_errors = Off",
	} {
		if !strings.Contains(userINIText, want) {
			t.Fatalf(".user.ini missing %q:\n%s", want, userINIText)
		}
	}
}

func TestNormalizePHPAdditionalDirectivesRejectsInvalidLines(t *testing.T) {
	if _, err := normalizePHPAdditionalDirectives("[bad]\ndisplay_errors = Off"); err == nil {
		t.Fatalf("expected section syntax to be rejected")
	}
	if _, err := normalizePHPAdditionalDirectives("display_errors Off"); err == nil {
		t.Fatalf("expected missing equals syntax to be rejected")
	}
}
