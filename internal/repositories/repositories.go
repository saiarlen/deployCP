package repositories

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"deploycp/internal/models"
)

type Repositories struct {
	Users              *UserRepository
	Sessions           *SessionRepository
	UserPlatformAccess *UserPlatformAccessRepository
	Websites           *WebsiteRepository
	SiteUsers          *SiteUserRepository
	GoApps             *GoAppRepository
	Services           *ManagedServiceRepository
	Databases          *DatabaseConnectionRepository
	Redis              *RedisConnectionRepository
	SSL                *SSLCertificateRepository
	Audit              *AuditLogRepository
	Activity           *ActivityLogRepository
	Settings           *SettingRepository
	UserPrefs          *UserPreferenceRepository
	SystemData         *SystemMetricRepository
	NginxSites         *NginxSiteRepository
	CronJobs           *CronJobRepository
	Varnish            *VarnishConfigRepository
	IPBlocks           *IPBlockRepository
	BotBlocks          *BotBlockRepository
	BasicAuths         *BasicAuthRepository
	FTPUsers           *FTPUserRepository
	Firewalls          *PanelFirewallRuleRepository
}

func New(db *gorm.DB) *Repositories {
	return &Repositories{
		Users:              &UserRepository{db},
		Sessions:           &SessionRepository{db},
		UserPlatformAccess: &UserPlatformAccessRepository{db},
		Websites:           &WebsiteRepository{db},
		SiteUsers:          &SiteUserRepository{db},
		GoApps:             &GoAppRepository{db},
		Services:           &ManagedServiceRepository{db},
		Databases:          &DatabaseConnectionRepository{db},
		Redis:              &RedisConnectionRepository{db},
		SSL:                &SSLCertificateRepository{db},
		Audit:              &AuditLogRepository{db},
		Activity:           &ActivityLogRepository{db},
		Settings:           &SettingRepository{db},
		UserPrefs:          &UserPreferenceRepository{db},
		SystemData:         &SystemMetricRepository{db},
		NginxSites:         &NginxSiteRepository{db},
		CronJobs:           &CronJobRepository{db},
		Varnish:            &VarnishConfigRepository{db},
		IPBlocks:           &IPBlockRepository{db},
		BotBlocks:          &BotBlockRepository{db},
		BasicAuths:         &BasicAuthRepository{db},
		FTPUsers:           &FTPUserRepository{db},
		Firewalls:          &PanelFirewallRuleRepository{db},
	}
}

type UserRepository struct{ db *gorm.DB }

func (r *UserRepository) Create(u *models.User) error { return r.db.Create(u).Error }
func (r *UserRepository) List() ([]models.User, error) {
	var items []models.User
	err := r.db.Order("id asc").Find(&items).Error
	return items, err
}
func (r *UserRepository) FirstAdmin() (*models.User, error) {
	var u models.User
	if err := r.db.Where("is_admin = ? OR role = ?", true, "admin").First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
func (r *UserRepository) FindByID(id uint) (*models.User, error) {
	var u models.User
	if err := r.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
func (r *UserRepository) FindByUsername(username string) (*models.User, error) {
	var u models.User
	if err := r.db.Where("LOWER(username) = LOWER(?)", username).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
func (r *UserRepository) FindByEmail(email string) (*models.User, error) {
	var u models.User
	if err := r.db.Where("LOWER(email) = LOWER(?)", email).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
func (r *UserRepository) Update(u *models.User) error { return r.db.Save(u).Error }
func (r *UserRepository) Delete(id uint) error        { return r.db.Delete(&models.User{}, id).Error }
func (r *UserRepository) UpdatePassword(id uint, hash string) error {
	return r.db.Model(&models.User{}).Where("id = ?", id).Update("password_hash", hash).Error
}
func (r *UserRepository) UpdateLastLogin(id uint) error {
	return r.db.Exec("UPDATE users SET last_login_at = CURRENT_TIMESTAMP WHERE id = ?", id).Error
}

type SessionRepository struct{ db *gorm.DB }

func (r *SessionRepository) Create(s *models.AuthSession) error { return r.db.Create(s).Error }
func (r *SessionRepository) DeleteBySessionID(sessionID string) error {
	return r.db.Where("session_id = ?", sessionID).Delete(&models.AuthSession{}).Error
}

type UserPlatformAccessRepository struct{ db *gorm.DB }

func (r *UserPlatformAccessRepository) List() ([]models.UserPlatformAccess, error) {
	var items []models.UserPlatformAccess
	err := r.db.Order("id asc").Find(&items).Error
	return items, err
}

func (r *UserPlatformAccessRepository) ListPlatformIDsByUser(userID uint) ([]uint, error) {
	var rows []models.UserPlatformAccess
	if err := r.db.Where("user_id = ?", userID).Order("platform_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]uint, 0, len(rows))
	for _, row := range rows {
		if row.PlatformID == 0 {
			continue
		}
		out = append(out, row.PlatformID)
	}
	return out, nil
}

func (r *UserPlatformAccessRepository) ReplaceForUser(userID uint, platformIDs []uint) error {
	seen := make(map[uint]struct{}, len(platformIDs))
	clean := make([]uint, 0, len(platformIDs))
	for _, id := range platformIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&models.UserPlatformAccess{}).Error; err != nil {
			return err
		}
		for _, pid := range clean {
			row := models.UserPlatformAccess{UserID: userID, PlatformID: pid}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *UserPlatformAccessRepository) DeleteByUser(userID uint) error {
	return r.db.Where("user_id = ?", userID).Delete(&models.UserPlatformAccess{}).Error
}

type WebsiteRepository struct{ db *gorm.DB }

func (r *WebsiteRepository) List() ([]models.Website, error) {
	var items []models.Website
	err := r.db.Preload("Domains").Preload("SiteUser").Order("id desc").Find(&items).Error
	return items, err
}
func (r *WebsiteRepository) Count() (int64, error) {
	var c int64
	return c, r.db.Model(&models.Website{}).Count(&c).Error
}
func (r *WebsiteRepository) Find(id uint) (*models.Website, error) {
	var item models.Website
	err := r.db.Preload("Domains").Preload("SiteUser").First(&item, id).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *WebsiteRepository) Create(item *models.Website, domains []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(item).Error; err != nil {
			return err
		}
		for i, d := range domains {
			if d == "" {
				continue
			}
			dom := models.WebsiteDomain{WebsiteID: item.ID, Domain: d, Primary: i == 0}
			if err := tx.Create(&dom).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *WebsiteRepository) Update(item *models.Website, domains []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(item).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", item.ID).Delete(&models.WebsiteDomain{}).Error; err != nil {
			return err
		}
		for i, d := range domains {
			if d == "" {
				continue
			}
			dom := models.WebsiteDomain{WebsiteID: item.ID, Domain: d, Primary: i == 0}
			if err := tx.Create(&dom).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *WebsiteRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("website_id = ?", id).Delete(&models.WebsiteDomain{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.NginxSiteConfig{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.CronJob{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.VarnishConfig{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.IPBlock{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.BotBlock{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.BasicAuth{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.FTPUser{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.DatabaseConnection{}).Error; err != nil {
			return err
		}
		if err := tx.Where("website_id = ?", id).Delete(&models.RedisConnection{}).Error; err != nil {
			return err
		}
		if err := tx.Where("go_app_id = ?", id).Delete(&models.AppEnvVar{}).Error; err != nil {
			return err
		}
		if err := tx.Where("go_app_id = ?", id).Delete(&models.DatabaseConnection{}).Error; err != nil {
			return err
		}
		if err := tx.Where("go_app_id = ?", id).Delete(&models.RedisConnection{}).Error; err != nil {
			return err
		}
		if err := tx.Where("platform_id = ?", id).Delete(&models.UserPlatformAccess{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Website{}, id).Error
	})
}

func (r *WebsiteRepository) CountBySiteUserIDExcept(siteUserID, excludeWebsiteID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Website{}).
		Where("site_user_id = ? AND id <> ?", siteUserID, excludeWebsiteID).
		Count(&count).Error
	return count, err
}

type SiteUserRepository struct{ db *gorm.DB }

func (r *SiteUserRepository) List() ([]models.SiteUser, error) {
	var items []models.SiteUser
	err := r.db.Order("id desc").Find(&items).Error
	return items, err
}
func (r *SiteUserRepository) Count() (int64, error) {
	var c int64
	return c, r.db.Model(&models.SiteUser{}).Count(&c).Error
}
func (r *SiteUserRepository) Find(id uint) (*models.SiteUser, error) {
	var item models.SiteUser
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *SiteUserRepository) FindByUsername(username string) (*models.SiteUser, error) {
	var item models.SiteUser
	if err := r.db.Where("LOWER(username) = LOWER(?)", username).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *SiteUserRepository) ListByWebsite(websiteID uint) ([]models.SiteUser, error) {
	var items []models.SiteUser
	err := r.db.Where("website_id = ?", websiteID).Order("id asc").Find(&items).Error
	return items, err
}
func (r *SiteUserRepository) Create(item *models.SiteUser) error { return r.db.Create(item).Error }
func (r *SiteUserRepository) Update(item *models.SiteUser) error { return r.db.Save(item).Error }
func (r *SiteUserRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Website{}).Where("site_user_id = ?", id).Update("site_user_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&models.SiteUser{}, id).Error
	})
}

type GoAppRepository struct{ db *gorm.DB }

func (r *GoAppRepository) List() ([]models.GoApp, error) {
	var items []models.GoApp
	err := r.db.Preload("EnvVars").Where("app_runtime <> ''").Order("id desc").Find(&items).Error
	if err == nil {
		for i := range items {
			wid := items[i].ID
			items[i].WebsiteID = &wid
		}
	}
	return items, err
}
func (r *GoAppRepository) Count() (int64, error) {
	var c int64
	return c, r.db.Model(&models.GoApp{}).Where("app_runtime <> ''").Count(&c).Error
}
func (r *GoAppRepository) Find(id uint) (*models.GoApp, error) {
	var item models.GoApp
	if err := r.db.Preload("EnvVars").Where("id = ? AND app_runtime <> ''", id).First(&item).Error; err != nil {
		return nil, err
	}
	wid := item.ID
	item.WebsiteID = &wid
	return &item, nil
}
func (r *GoAppRepository) Create(item *models.GoApp, env map[string]string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if item.Type == "" {
			item.Type = "proxy"
		}
		if item.WorkingDirectory == "" {
			item.WorkingDirectory = filepath.Join("./storage/sites", strings.ToLower(strings.ReplaceAll(strings.TrimSpace(item.Name), " ", "-")))
		}
		if item.ProxyTarget == "" {
			item.ProxyTarget = fmt.Sprintf("http://%s:%d", item.Host, item.Port)
		}

		targetID := uint(0)
		if item.WebsiteID != nil && *item.WebsiteID > 0 {
			targetID = *item.WebsiteID
			if err := tx.Model(&models.GoApp{}).Where("id = ?", targetID).Updates(map[string]any{
				"name":            item.Name,
				"type":            item.Type,
				"app_runtime":     item.Runtime,
				"execution_mode":  item.ExecutionMode,
				"process_manager": item.ProcessManager,
				"binary_path":     item.BinaryPath,
				"entry_point":     item.EntryPoint,
				"root_path":       item.WorkingDirectory,
				"host":            item.Host,
				"port":            item.Port,
				"start_args":      item.StartArgs,
				"health_path":     item.HealthPath,
				"restart_policy":  item.RestartPolicy,
				"workers":         item.Workers,
				"worker_class":    item.WorkerClass,
				"max_memory":      item.MaxMemory,
				"timeout":         item.Timeout,
				"exec_mode":       item.ExecMode,
				"stdout_log_path": item.StdoutLogPath,
				"stderr_log_path": item.StderrLogPath,
				"service_name":    item.ServiceName,
				"proxy_target":    item.ProxyTarget,
				"enabled":         item.Enabled,
			}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Create(item).Error; err != nil {
				return err
			}
			targetID = item.ID
		}
		item.ID = targetID
		wid := targetID
		item.WebsiteID = &wid

		if err := tx.Where("go_app_id = ?", targetID).Delete(&models.AppEnvVar{}).Error; err != nil {
			return err
		}
		for k, v := range env {
			if err := tx.Create(&models.AppEnvVar{GoAppID: targetID, Key: k, Value: v}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *GoAppRepository) Update(item *models.GoApp, env map[string]string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if item.Type == "" {
			item.Type = "proxy"
		}
		if item.ProxyTarget == "" {
			item.ProxyTarget = fmt.Sprintf("http://%s:%d", item.Host, item.Port)
		}
		if err := tx.Model(&models.GoApp{}).Where("id = ? AND app_runtime <> ''", item.ID).Updates(map[string]any{
			"name":            item.Name,
			"type":            item.Type,
			"app_runtime":     item.Runtime,
			"execution_mode":  item.ExecutionMode,
			"process_manager": item.ProcessManager,
			"binary_path":     item.BinaryPath,
			"entry_point":     item.EntryPoint,
			"root_path":       item.WorkingDirectory,
			"host":            item.Host,
			"port":            item.Port,
			"start_args":      item.StartArgs,
			"health_path":     item.HealthPath,
			"restart_policy":  item.RestartPolicy,
			"workers":         item.Workers,
			"worker_class":    item.WorkerClass,
			"max_memory":      item.MaxMemory,
			"timeout":         item.Timeout,
			"exec_mode":       item.ExecMode,
			"stdout_log_path": item.StdoutLogPath,
			"stderr_log_path": item.StderrLogPath,
			"service_name":    item.ServiceName,
			"proxy_target":    item.ProxyTarget,
			"enabled":         item.Enabled,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("go_app_id = ?", item.ID).Delete(&models.AppEnvVar{}).Error; err != nil {
			return err
		}
		for k, v := range env {
			if err := tx.Create(&models.AppEnvVar{GoAppID: item.ID, Key: k, Value: v}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *GoAppRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("go_app_id = ?", id).Delete(&models.AppEnvVar{}).Error; err != nil {
			return err
		}
		if err := tx.Where("platform_id = ?", id).Delete(&models.UserPlatformAccess{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.GoApp{}, id).Error
	})
}

func (r *GoAppRepository) ClearRuntime(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("go_app_id = ?", id).Delete(&models.AppEnvVar{}).Error; err != nil {
			return err
		}
		return tx.Model(&models.GoApp{}).Where("id = ?", id).Updates(map[string]any{
			"app_runtime":     "",
			"execution_mode":  "",
			"process_manager": "",
			"binary_path":     "",
			"entry_point":     "",
			"host":            "",
			"port":            0,
			"start_args":      "",
			"health_path":     "",
			"restart_policy":  "",
			"workers":         0,
			"worker_class":    "",
			"max_memory":      "",
			"timeout":         0,
			"exec_mode":       "",
			"stdout_log_path": "",
			"stderr_log_path": "",
			"service_name":    "",
		}).Error
	})
}

func (r *GoAppRepository) FindByWebsiteID(websiteID uint) (*models.GoApp, error) {
	return r.Find(websiteID)
}

func (r *GoAppRepository) ListByWebsiteID(websiteID uint) ([]models.GoApp, error) {
	item, err := r.Find(websiteID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []models.GoApp{}, nil
		}
		return nil, err
	}
	return []models.GoApp{*item}, nil
}

type ManagedServiceRepository struct{ db *gorm.DB }

func (r *ManagedServiceRepository) List() ([]models.ManagedService, error) {
	var items []models.ManagedService
	err := r.db.Order("id desc").Find(&items).Error
	return items, err
}
func (r *ManagedServiceRepository) Count() (int64, error) {
	var c int64
	return c, r.db.Model(&models.ManagedService{}).Count(&c).Error
}
func (r *ManagedServiceRepository) FindByName(name string) (*models.ManagedService, error) {
	var item models.ManagedService
	if err := r.db.Where("name = ?", name).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *ManagedServiceRepository) Find(id uint) (*models.ManagedService, error) {
	var item models.ManagedService
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *ManagedServiceRepository) Create(item *models.ManagedService) error {
	return r.db.Create(item).Error
}
func (r *ManagedServiceRepository) Update(item *models.ManagedService) error {
	return r.db.Save(item).Error
}
func (r *ManagedServiceRepository) Upsert(item *models.ManagedService) error {
	var existing models.ManagedService
	err := r.db.Where("name = ?", item.Name).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(item).Error
	}
	if err != nil {
		return err
	}
	existing.Type = item.Type
	existing.PlatformName = item.PlatformName
	existing.UnitPath = item.UnitPath
	existing.Tags = item.Tags
	existing.Description = item.Description
	existing.Enabled = item.Enabled
	return r.db.Save(&existing).Error
}

func (r *ManagedServiceRepository) DeleteByName(name string) error {
	return r.db.Where("name = ?", name).Delete(&models.ManagedService{}).Error
}
func (r *ManagedServiceRepository) Delete(id uint) error {
	return r.db.Delete(&models.ManagedService{}, id).Error
}

type DatabaseConnectionRepository struct{ db *gorm.DB }

func (r *DatabaseConnectionRepository) List() ([]models.DatabaseConnection, error) {
	var items []models.DatabaseConnection
	err := r.db.Order("id desc").Find(&items).Error
	return items, err
}
func (r *DatabaseConnectionRepository) Count() (int64, error) {
	var c int64
	return c, r.db.Model(&models.DatabaseConnection{}).Count(&c).Error
}
func (r *DatabaseConnectionRepository) Find(id uint) (*models.DatabaseConnection, error) {
	var item models.DatabaseConnection
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *DatabaseConnectionRepository) Create(item *models.DatabaseConnection) error {
	return r.db.Create(item).Error
}
func (r *DatabaseConnectionRepository) Update(item *models.DatabaseConnection) error {
	return r.db.Save(item).Error
}
func (r *DatabaseConnectionRepository) Delete(id uint) error {
	return r.db.Delete(&models.DatabaseConnection{}, id).Error
}

func (r *DatabaseConnectionRepository) ListByWebsiteID(websiteID uint) ([]models.DatabaseConnection, error) {
	var items []models.DatabaseConnection
	err := r.db.Where("website_id = ?", websiteID).Order("id desc").Find(&items).Error
	return items, err
}

func (r *DatabaseConnectionRepository) ListByGoAppID(goAppID uint) ([]models.DatabaseConnection, error) {
	var items []models.DatabaseConnection
	err := r.db.Where("go_app_id = ?", goAppID).Order("id desc").Find(&items).Error
	return items, err
}

type RedisConnectionRepository struct{ db *gorm.DB }

func (r *RedisConnectionRepository) List() ([]models.RedisConnection, error) {
	var items []models.RedisConnection
	err := r.db.Order("id desc").Find(&items).Error
	return items, err
}
func (r *RedisConnectionRepository) Count() (int64, error) {
	var c int64
	return c, r.db.Model(&models.RedisConnection{}).Count(&c).Error
}
func (r *RedisConnectionRepository) Find(id uint) (*models.RedisConnection, error) {
	var item models.RedisConnection
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *RedisConnectionRepository) Create(item *models.RedisConnection) error {
	return r.db.Create(item).Error
}
func (r *RedisConnectionRepository) Update(item *models.RedisConnection) error {
	return r.db.Save(item).Error
}
func (r *RedisConnectionRepository) Delete(id uint) error {
	return r.db.Delete(&models.RedisConnection{}, id).Error
}

func (r *RedisConnectionRepository) ListByWebsiteID(websiteID uint) ([]models.RedisConnection, error) {
	var items []models.RedisConnection
	err := r.db.Where("website_id = ?", websiteID).Order("id desc").Find(&items).Error
	return items, err
}

func (r *RedisConnectionRepository) ListByGoAppID(goAppID uint) ([]models.RedisConnection, error) {
	var items []models.RedisConnection
	err := r.db.Where("go_app_id = ?", goAppID).Order("id desc").Find(&items).Error
	return items, err
}

type SSLCertificateRepository struct{ db *gorm.DB }

func (r *SSLCertificateRepository) List() ([]models.SSLCertificate, error) {
	var items []models.SSLCertificate
	err := r.db.Order("id desc").Find(&items).Error
	return items, err
}
func (r *SSLCertificateRepository) Create(item *models.SSLCertificate) error {
	return r.db.Create(item).Error
}
func (r *SSLCertificateRepository) Update(item *models.SSLCertificate) error {
	return r.db.Save(item).Error
}
func (r *SSLCertificateRepository) Delete(id uint) error {
	return r.db.Delete(&models.SSLCertificate{}, id).Error
}

type AuditLogRepository struct{ db *gorm.DB }

func (r *AuditLogRepository) Create(item *models.AuditLog) error { return r.db.Create(item).Error }
func (r *AuditLogRepository) List(limit int) ([]models.AuditLog, error) {
	var items []models.AuditLog
	err := r.db.Order("id desc").Limit(limit).Find(&items).Error
	return items, err
}
func (r *AuditLogRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.AuditLog{}).Count(&count).Error
	return count, err
}
func (r *AuditLogRepository) ListPage(limit, offset int) ([]models.AuditLog, error) {
	var items []models.AuditLog
	query := r.db.Order("id desc")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	err := query.Find(&items).Error
	return items, err
}

type ActivityLogRepository struct{ db *gorm.DB }

func (r *ActivityLogRepository) Create(item *models.ActivityLog) error {
	return r.db.Create(item).Error
}
func (r *ActivityLogRepository) List(limit int) ([]models.ActivityLog, error) {
	var items []models.ActivityLog
	err := r.db.Order("id desc").Limit(limit).Find(&items).Error
	return items, err
}

type NginxSiteRepository struct{ db *gorm.DB }

func (r *NginxSiteRepository) FindByWebsite(websiteID uint) (*models.NginxSiteConfig, error) {
	var item models.NginxSiteConfig
	err := r.db.Where("website_id = ?", websiteID).First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *NginxSiteRepository) Upsert(item *models.NginxSiteConfig) error {
	var existing models.NginxSiteConfig
	err := r.db.Where("website_id = ?", item.WebsiteID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(item).Error
	}
	if err != nil {
		return err
	}
	existing.ConfigPath = item.ConfigPath
	existing.EnabledPath = item.EnabledPath
	existing.Checksum = item.Checksum
	existing.Enabled = item.Enabled
	existing.LastValidatedAt = item.LastValidatedAt
	return r.db.Save(&existing).Error
}

func (r *NginxSiteRepository) DeleteByWebsite(websiteID uint) error {
	return r.db.Where("website_id = ?", websiteID).Delete(&models.NginxSiteConfig{}).Error
}

type SettingRepository struct{ db *gorm.DB }

func (r *SettingRepository) Set(key, value string, secret bool) error {
	var item models.Setting
	err := r.db.Where("key = ?", key).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(&models.Setting{Key: key, Value: value, Secret: secret}).Error
	}
	if err != nil {
		return err
	}
	item.Value = value
	item.Secret = secret
	return r.db.Save(&item).Error
}
func (r *SettingRepository) Get(key string) (string, error) {
	var item models.Setting
	if err := r.db.Where("key = ?", key).First(&item).Error; err != nil {
		return "", err
	}
	return item.Value, nil
}
func (r *SettingRepository) List() ([]models.Setting, error) {
	var items []models.Setting
	err := r.db.Order("key asc").Find(&items).Error
	return items, err
}

type UserPreferenceRepository struct{ db *gorm.DB }

func (r *UserPreferenceRepository) Set(userID uint, key, value string) error {
	var item models.UserPreference
	err := r.db.Where("user_id = ? AND key = ?", userID, key).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(&models.UserPreference{
			UserID: userID,
			Key:    key,
			Value:  value,
		}).Error
	}
	if err != nil {
		return err
	}
	item.Value = value
	return r.db.Save(&item).Error
}

func (r *UserPreferenceRepository) Get(userID uint, key string) (string, error) {
	var item models.UserPreference
	if err := r.db.Where("user_id = ? AND key = ?", userID, key).First(&item).Error; err != nil {
		return "", err
	}
	return item.Value, nil
}

type SystemMetricRepository struct{ db *gorm.DB }

func (r *SystemMetricRepository) Create(item *models.SystemMetricSnapshot) error {
	return r.db.Create(item).Error
}

func (r *SystemMetricRepository) ListSince(since time.Time, max int) ([]models.SystemMetricSnapshot, error) {
	var items []models.SystemMetricSnapshot
	query := r.db.Where("created_at >= ?", since).Order("created_at asc")
	if max > 0 {
		query = query.Limit(max)
	}
	err := query.Find(&items).Error
	return items, err
}

func (r *SystemMetricRepository) DeleteOlderThan(before time.Time) error {
	return r.db.Where("created_at < ?", before).Delete(&models.SystemMetricSnapshot{}).Error
}

// ── CronJob ──

type CronJobRepository struct{ db *gorm.DB }

func (r *CronJobRepository) ListByWebsite(websiteID uint) ([]models.CronJob, error) {
	var items []models.CronJob
	err := r.db.Where("website_id = ?", websiteID).Order("id desc").Find(&items).Error
	return items, err
}
func (r *CronJobRepository) Create(item *models.CronJob) error { return r.db.Create(item).Error }
func (r *CronJobRepository) Delete(id uint) error {
	return r.db.Delete(&models.CronJob{}, id).Error
}

func (r *CronJobRepository) DeleteByWebsite(websiteID uint) error {
	return r.db.Where("website_id = ?", websiteID).Delete(&models.CronJob{}).Error
}

// ── VarnishConfig ──

type VarnishConfigRepository struct{ db *gorm.DB }

func (r *VarnishConfigRepository) FindByWebsite(websiteID uint) (*models.VarnishConfig, error) {
	var item models.VarnishConfig
	err := r.db.Where("website_id = ?", websiteID).First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *VarnishConfigRepository) Upsert(item *models.VarnishConfig) error {
	var existing models.VarnishConfig
	err := r.db.Where("website_id = ?", item.WebsiteID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(item).Error
	}
	if err != nil {
		return err
	}
	existing.Enabled = item.Enabled
	existing.Server = item.Server
	existing.CacheLifetime = item.CacheLifetime
	existing.CacheTagPrefix = item.CacheTagPrefix
	existing.ExcludedParams = item.ExcludedParams
	existing.Excludes = item.Excludes
	return r.db.Save(&existing).Error
}

func (r *VarnishConfigRepository) DeleteByWebsite(websiteID uint) error {
	return r.db.Where("website_id = ?", websiteID).Delete(&models.VarnishConfig{}).Error
}

// ── IPBlock ──

type IPBlockRepository struct{ db *gorm.DB }

func (r *IPBlockRepository) ListByWebsite(websiteID uint) ([]models.IPBlock, error) {
	var items []models.IPBlock
	err := r.db.Where("website_id = ?", websiteID).Order("id desc").Find(&items).Error
	return items, err
}
func (r *IPBlockRepository) Create(item *models.IPBlock) error { return r.db.Create(item).Error }
func (r *IPBlockRepository) Delete(id uint) error {
	return r.db.Delete(&models.IPBlock{}, id).Error
}

func (r *IPBlockRepository) DeleteByWebsite(websiteID uint) error {
	return r.db.Where("website_id = ?", websiteID).Delete(&models.IPBlock{}).Error
}

// ── BotBlock ──

type BotBlockRepository struct{ db *gorm.DB }

func (r *BotBlockRepository) ListByWebsite(websiteID uint) ([]models.BotBlock, error) {
	var items []models.BotBlock
	err := r.db.Where("website_id = ?", websiteID).Order("id desc").Find(&items).Error
	return items, err
}
func (r *BotBlockRepository) Create(item *models.BotBlock) error { return r.db.Create(item).Error }
func (r *BotBlockRepository) Delete(id uint) error {
	return r.db.Delete(&models.BotBlock{}, id).Error
}

func (r *BotBlockRepository) DeleteByWebsite(websiteID uint) error {
	return r.db.Where("website_id = ?", websiteID).Delete(&models.BotBlock{}).Error
}

// ── BasicAuth ──

type BasicAuthRepository struct{ db *gorm.DB }

func (r *BasicAuthRepository) FindByWebsite(websiteID uint) (*models.BasicAuth, error) {
	var item models.BasicAuth
	err := r.db.Where("website_id = ?", websiteID).First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *BasicAuthRepository) Upsert(item *models.BasicAuth) error {
	var existing models.BasicAuth
	err := r.db.Where("website_id = ?", item.WebsiteID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(item).Error
	}
	if err != nil {
		return err
	}
	existing.Enabled = item.Enabled
	existing.Username = item.Username
	if strings.TrimSpace(item.PasswordEnc) != "" {
		existing.PasswordEnc = item.PasswordEnc
	}
	existing.WhitelistedIPs = item.WhitelistedIPs
	return r.db.Save(&existing).Error
}

func (r *BasicAuthRepository) DeleteByWebsite(websiteID uint) error {
	return r.db.Where("website_id = ?", websiteID).Delete(&models.BasicAuth{}).Error
}

// ── FTPUser ──

type FTPUserRepository struct{ db *gorm.DB }

func (r *FTPUserRepository) List() ([]models.FTPUser, error) {
	var items []models.FTPUser
	err := r.db.Order("id desc").Find(&items).Error
	return items, err
}

func (r *FTPUserRepository) ListByWebsite(websiteID uint) ([]models.FTPUser, error) {
	var items []models.FTPUser
	err := r.db.Where("website_id = ?", websiteID).Order("id desc").Find(&items).Error
	return items, err
}
func (r *FTPUserRepository) Find(id uint) (*models.FTPUser, error) {
	var item models.FTPUser
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *FTPUserRepository) FindByUsername(username string) (*models.FTPUser, error) {
	var item models.FTPUser
	if err := r.db.Where("LOWER(username) = LOWER(?)", username).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}
func (r *FTPUserRepository) Create(item *models.FTPUser) error { return r.db.Create(item).Error }
func (r *FTPUserRepository) Delete(id uint) error {
	return r.db.Delete(&models.FTPUser{}, id).Error
}

func (r *FTPUserRepository) DeleteByWebsite(websiteID uint) error {
	return r.db.Where("website_id = ?", websiteID).Delete(&models.FTPUser{}).Error
}

type PanelFirewallRuleRepository struct{ db *gorm.DB }

func (r *PanelFirewallRuleRepository) List() ([]models.PanelFirewallRule, error) {
	var items []models.PanelFirewallRule
	err := r.db.Order("id desc").Find(&items).Error
	return items, err
}

func (r *PanelFirewallRuleRepository) Find(id uint) (*models.PanelFirewallRule, error) {
	var item models.PanelFirewallRule
	if err := r.db.First(&item, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *PanelFirewallRuleRepository) Create(item *models.PanelFirewallRule) error {
	return r.db.Create(item).Error
}

func (r *PanelFirewallRuleRepository) Update(item *models.PanelFirewallRule) error {
	return r.db.Save(item).Error
}

func (r *PanelFirewallRuleRepository) Delete(id uint) error {
	return r.db.Delete(&models.PanelFirewallRule{}, id).Error
}

func ParseID(v string) (uint, error) {
	var id uint
	if _, err := fmt.Sscanf(v, "%d", &id); err != nil || id == 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}
