package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/models"
)

func TestSyncWebsiteUploadLimitsFromNginxPreservesManualServerValue(t *testing.T) {
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "deploycp.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Website{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	site := &models.Website{Name: "example.test", ClientMaxBodySize: "64M"}
	if err := db.Create(site).Error; err != nil {
		t.Fatalf("create website: %v", err)
	}
	nginxDir := filepath.Join(tmp, "nginx")
	if err := os.MkdirAll(nginxDir, 0o755); err != nil {
		t.Fatalf("create nginx directory: %v", err)
	}
	configPath := filepath.Join(nginxDir, site.Name+".conf")
	if err := os.WriteFile(configPath, []byte("server {\n    client_max_body_size 256m;\n}\n"), 0o644); err != nil {
		t.Fatalf("write nginx config: %v", err)
	}

	cfg := &config.Config{}
	cfg.Paths.NginxAvailableDir = nginxDir
	if err := syncWebsiteUploadLimitsFromNginx(cfg, db); err != nil {
		t.Fatalf("sync upload limit: %v", err)
	}

	var saved models.Website
	if err := db.First(&saved, site.ID).Error; err != nil {
		t.Fatalf("load website: %v", err)
	}
	if saved.ClientMaxBodySize != "256M" {
		t.Fatalf("ClientMaxBodySize = %q, want 256M", saved.ClientMaxBodySize)
	}
}
