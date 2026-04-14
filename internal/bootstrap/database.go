package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"deploycp/internal/config"
	"deploycp/internal/models"
)

func NewDB(cfg *config.Config) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Database.SQLitePath), 0o755); err != nil {
		return nil, err
	}
	db, err := gorm.Open(sqlite.Open(cfg.Database.SQLitePath), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}

func migrate(db *gorm.DB) error {
	if err := migrateLegacyTableNames(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.AuthSession{},
		&models.UserPlatformAccess{},
		&models.SiteUser{},
		&models.Website{},
		&models.WebsiteDomain{},
		&models.GoApp{},
		&models.AppEnvVar{},
		&models.ManagedService{},
		&models.DatabaseConnection{},
		&models.RedisConnection{},
		&models.SSLCertificate{},
		&models.NginxSiteConfig{},
		&models.AuditLog{},
		&models.ActivityLog{},
		&models.Setting{},
		&models.UserPreference{},
		&models.PanelFirewallRule{},
		&models.SystemMetricSnapshot{},
		&models.CronJob{},
		&models.VarnishConfig{},
		&models.IPBlock{},
		&models.BotBlock{},
		&models.BasicAuth{},
		&models.FTPUser{},
	); err != nil {
		return err
	}
	return mergeLegacyAppsIntoPlatforms(db)
}

func migrateLegacyTableNames(db *gorm.DB) error {
	migrator := db.Migrator()

	// Legacy app runtime table naming.
	if migrator.HasTable("go_apps") && !migrator.HasTable("apps") {
		if err := db.Exec("ALTER TABLE go_apps RENAME TO apps").Error; err != nil {
			return fmt.Errorf("rename legacy table go_apps -> apps: %w", err)
		}
	}
	if migrator.HasTable("go_apps") && migrator.HasTable("apps") {
		if err := db.Exec("DROP TABLE go_apps").Error; err != nil {
			return fmt.Errorf("drop legacy table go_apps: %w", err)
		}
	}

	// Canonical platform table naming.
	hasWebsites := migrator.HasTable("websites")
	hasPlatforms := migrator.HasTable("platforms")
	if hasWebsites && !hasPlatforms {
		if err := db.Exec("ALTER TABLE websites RENAME TO platforms").Error; err != nil {
			return fmt.Errorf("rename table websites -> platforms: %w", err)
		}
	}
	if hasWebsites && hasPlatforms {
		var websitesCount int64
		var platformsCount int64
		if err := db.Table("websites").Count(&websitesCount).Error; err != nil {
			return fmt.Errorf("count websites table: %w", err)
		}
		if err := db.Table("platforms").Count(&platformsCount).Error; err != nil {
			return fmt.Errorf("count platforms table: %w", err)
		}
		if platformsCount == 0 && websitesCount > 0 {
			if err := db.Exec("DROP TABLE platforms").Error; err != nil {
				return fmt.Errorf("drop empty platforms table before rename: %w", err)
			}
			if err := db.Exec("ALTER TABLE websites RENAME TO platforms").Error; err != nil {
				return fmt.Errorf("rename table websites -> platforms after cleanup: %w", err)
			}
		} else if websitesCount == 0 {
			if err := db.Exec("DROP TABLE websites").Error; err != nil {
				return fmt.Errorf("drop empty legacy websites table: %w", err)
			}
		} else {
			if err := mergeLegacyWebsitesIntoPlatforms(db); err != nil {
				return err
			}
			if err := db.Exec("DROP TABLE websites").Error; err != nil {
				return fmt.Errorf("drop merged legacy websites table: %w", err)
			}
		}
	}
	return nil
}

func mergeLegacyWebsitesIntoPlatforms(db *gorm.DB) error {
	var rows []models.Website
	if err := db.Table("websites").Find(&rows).Error; err != nil {
		return fmt.Errorf("load legacy websites rows: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			var existing models.Website
			err := tx.Table("platforms").Where("id = ?", row.ID).First(&existing).Error
			if err == nil {
				continue
			}
			if err != nil && err != gorm.ErrRecordNotFound {
				return fmt.Errorf("check platform row %d: %w", row.ID, err)
			}
			if err := tx.Table("platforms").Create(&row).Error; err != nil {
				return fmt.Errorf("insert legacy website %d into platforms: %w", row.ID, err)
			}
		}
		return nil
	})
}

type legacyAppRow struct {
	ID               uint
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
	StdoutLogPath    string
	StderrLogPath    string
	ServiceName      string
	WebsiteID        *uint
	Enabled          bool
}

func mergeLegacyAppsIntoPlatforms(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable("apps") {
		return nil
	}

	var rows []legacyAppRow
	if err := db.Table("apps").Find(&rows).Error; err != nil {
		return fmt.Errorf("load legacy apps rows: %w", err)
	}

	if len(rows) == 0 {
		if err := db.Exec("DROP TABLE apps").Error; err != nil {
			return fmt.Errorf("drop empty legacy apps table: %w", err)
		}
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		idMap := make(map[uint]uint, len(rows))

		for _, row := range rows {
			targetID := uint(0)
			rootPath := strings.TrimSpace(row.WorkingDirectory)
			if rootPath == "" {
				rootPath = filepath.Join("./storage/sites", runtimePlatformFolder(row.Name))
			}
			host := strings.TrimSpace(row.Host)
			if host == "" {
				host = "127.0.0.1"
			}
			port := row.Port
			if port <= 0 {
				port = 3000
			}
			proxyTarget := fmt.Sprintf("http://%s:%d", host, port)

			if row.WebsiteID != nil && *row.WebsiteID > 0 {
				var existing models.Website
				if err := tx.Where("id = ?", *row.WebsiteID).First(&existing).Error; err == nil {
					targetID = *row.WebsiteID
					updates := map[string]any{
						"name":              row.Name,
						"type":              "proxy",
						"app_runtime":       row.Runtime,
						"execution_mode":    row.ExecutionMode,
						"process_manager":   row.ProcessManager,
						"binary_path":       row.BinaryPath,
						"entry_point":       row.EntryPoint,
						"root_path":         rootPath,
						"host":              host,
						"port":              port,
						"start_args":        row.StartArgs,
						"health_path":       row.HealthPath,
						"restart_policy":    row.RestartPolicy,
						"workers":           row.Workers,
						"worker_class":      row.WorkerClass,
						"max_memory":        row.MaxMemory,
						"timeout":           row.Timeout,
						"exec_mode":         row.ExecMode,
						"stdout_log_path":   row.StdoutLogPath,
						"stderr_log_path":   row.StderrLogPath,
						"service_name":      row.ServiceName,
						"proxy_target":      proxyTarget,
						"enabled":           row.Enabled,
						"access_log_path":   existing.AccessLogPath,
						"error_log_path":    existing.ErrorLogPath,
						"custom_directives": existing.CustomDirectives,
					}
					if err := tx.Model(&models.Website{}).Where("id = ?", targetID).Updates(updates).Error; err != nil {
						return fmt.Errorf("merge app %d into platform %d: %w", row.ID, targetID, err)
					}
				}
			}

			if targetID == 0 {
				platformRow := &models.Website{
					Name:             row.Name,
					RootPath:         rootPath,
					Type:             "proxy",
					AppRuntime:       row.Runtime,
					ExecutionMode:    row.ExecutionMode,
					ProcessManager:   row.ProcessManager,
					BinaryPath:       row.BinaryPath,
					EntryPoint:       row.EntryPoint,
					Host:             host,
					Port:             port,
					StartArgs:        row.StartArgs,
					HealthPath:       row.HealthPath,
					RestartPolicy:    row.RestartPolicy,
					Workers:          row.Workers,
					WorkerClass:      row.WorkerClass,
					MaxMemory:        row.MaxMemory,
					Timeout:          row.Timeout,
					ExecMode:         row.ExecMode,
					StdoutLogPath:    row.StdoutLogPath,
					StderrLogPath:    row.StderrLogPath,
					ServiceName:      row.ServiceName,
					ProxyTarget:      proxyTarget,
					Enabled:          row.Enabled,
					AccessLogPath:    filepath.Join("./storage/logs", "sites", runtimePlatformFolder(row.Name), "access.log"),
					ErrorLogPath:     filepath.Join("./storage/logs", "sites", runtimePlatformFolder(row.Name), "error.log"),
					CustomDirectives: "",
				}
				if err := tx.Create(platformRow).Error; err != nil {
					return fmt.Errorf("insert merged platform for legacy app %d: %w", row.ID, err)
				}
				targetID = platformRow.ID
			}

			idMap[row.ID] = targetID
		}

		for oldID, newID := range idMap {
			if err := tx.Table("app_env_vars").Where("go_app_id = ?", oldID).Update("go_app_id", newID).Error; err != nil {
				return fmt.Errorf("rebind app env vars from %d to %d: %w", oldID, newID, err)
			}
			if err := tx.Table("database_connections").Where("go_app_id = ?", oldID).Update("go_app_id", newID).Error; err != nil {
				return fmt.Errorf("rebind database connections from %d to %d: %w", oldID, newID, err)
			}
			if err := tx.Table("redis_connections").Where("go_app_id = ?", oldID).Update("go_app_id", newID).Error; err != nil {
				return fmt.Errorf("rebind redis connections from %d to %d: %w", oldID, newID, err)
			}
		}

		if err := tx.Exec("DROP TABLE apps").Error; err != nil {
			return fmt.Errorf("drop merged legacy apps table: %w", err)
		}

		return nil
	})
}

func runtimePlatformFolder(name string) string {
	raw := strings.ToLower(strings.TrimSpace(name))
	raw = strings.ReplaceAll(raw, " ", "-")
	if raw == "" {
		return "platform"
	}
	return raw
}

func SeedDefaults(db *gorm.DB) error {
	defaults := []models.Setting{
		{Key: "theme", Value: "light", Secret: false},
		{Key: "ssl_issue_hook", Value: "", Secret: false},
		{Key: "ssl_renew_hook", Value: "", Secret: false},
	}
	for _, d := range defaults {
		var existing models.Setting
		err := db.Where("key = ?", d.Key).First(&existing).Error
		if err == nil {
			continue
		}
		if err := db.Create(&d).Error; err != nil {
			return fmt.Errorf("seed %s: %w", d.Key, err)
		}
	}
	return nil
}
