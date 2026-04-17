package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/models"
	"deploycp/internal/platform"
	"deploycp/internal/repositories"
	"deploycp/internal/system/nginx"
	"deploycp/internal/utils"
	"deploycp/internal/validators"
)

type WebsiteInput struct {
	Name             string
	RootPath         string
	Type             string
	AppRuntime       string
	PHPVersion       string
	ProxyTarget      string
	Domains          []string
	CustomDirectives string
	SiteUserID       *uint
	Enabled          bool
}

type PhpSettingsData struct {
	MemoryLimit          string `json:"memory_limit"`
	MaxExecutionTime     string `json:"max_execution_time"`
	MaxInputTime         string `json:"max_input_time"`
	MaxInputVars         string `json:"max_input_vars"`
	PostMaxSize          string `json:"post_max_size"`
	UploadMaxFilesize    string `json:"upload_max_filesize"`
	AdditionalDirectives string `json:"additional_directives"`
}

type WebsiteService struct {
	cfg       *config.Config
	repo      *repositories.WebsiteRepository
	nginxRepo *repositories.NginxSiteRepository
	appRepo   *repositories.GoAppRepository
	services  *repositories.ManagedServiceRepository
	siteUsers *repositories.SiteUserRepository
	ftpUsers  *repositories.FTPUserRepository
	dbRepo    *repositories.DatabaseConnectionRepository
	redisRepo *repositories.RedisConnectionRepository
	sslRepo   *repositories.SSLCertificateRepository
	varnish   *repositories.VarnishConfigRepository
	ipBlocks  *repositories.IPBlockRepository
	botBlocks *repositories.BotBlockRepository
	basicAuth *repositories.BasicAuthRepository
	adapter   platform.Adapter
	audit     *AuditService
	ssl       *SSLService
	runtime   *RuntimeService
	cron      *CronService
	database  *DatabaseService
	ftp       *FTPService
	varnishOS *VarnishService
}

func NewWebsiteService(
	cfg *config.Config,
	repo *repositories.WebsiteRepository,
	nginxRepo *repositories.NginxSiteRepository,
	appRepo *repositories.GoAppRepository,
	servicesRepo *repositories.ManagedServiceRepository,
	siteUsers *repositories.SiteUserRepository,
	ftpUsers *repositories.FTPUserRepository,
	dbRepo *repositories.DatabaseConnectionRepository,
	redisRepo *repositories.RedisConnectionRepository,
	sslRepo *repositories.SSLCertificateRepository,
	varnish *repositories.VarnishConfigRepository,
	ipBlocks *repositories.IPBlockRepository,
	botBlocks *repositories.BotBlockRepository,
	basicAuth *repositories.BasicAuthRepository,
	adapter platform.Adapter,
	audit *AuditService,
	ssl *SSLService,
	runtime *RuntimeService,
	cron *CronService,
	database *DatabaseService,
	ftp *FTPService,
	varnishOS *VarnishService,
) *WebsiteService {
	return &WebsiteService{
		cfg:       cfg,
		repo:      repo,
		nginxRepo: nginxRepo,
		appRepo:   appRepo,
		services:  servicesRepo,
		siteUsers: siteUsers,
		ftpUsers:  ftpUsers,
		dbRepo:    dbRepo,
		redisRepo: redisRepo,
		sslRepo:   sslRepo,
		varnish:   varnish,
		ipBlocks:  ipBlocks,
		botBlocks: botBlocks,
		basicAuth: basicAuth,
		adapter:   adapter,
		audit:     audit,
		ssl:       ssl,
		runtime:   runtime,
		cron:      cron,
		database:  database,
		ftp:       ftp,
		varnishOS: varnishOS,
	}
}

func (s *WebsiteService) List() ([]models.Website, error) {
	return s.repo.List()
}

func (s *WebsiteService) Find(id uint) (*models.Website, error) {
	return s.repo.Find(id)
}

func (s *WebsiteService) Create(ctx context.Context, in WebsiteInput, actor *uint, ip string) (*models.Website, error) {
	if err := s.validate(in); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(in.RootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create site root: %w", err)
	}
	if err := s.ensurePublicWebRoot(in.RootPath); err != nil {
		return nil, err
	}
	site := &models.Website{
		Name:             in.Name,
		RootPath:         in.RootPath,
		Type:             in.Type,
		AppRuntime:       in.AppRuntime,
		PHPVersion:       in.PHPVersion,
		ProxyTarget:      in.ProxyTarget,
		CustomDirectives: in.CustomDirectives,
		SiteUserID:       in.SiteUserID,
		Enabled:          in.Enabled,
		AccessLogPath:    filepath.Join(s.cfg.Paths.LogRoot, "sites", in.Name, "access.log"),
		ErrorLogPath:     filepath.Join(s.cfg.Paths.LogRoot, "sites", in.Name, "error.log"),
	}
	if err := os.MkdirAll(filepath.Dir(site.AccessLogPath), 0o755); err != nil {
		return nil, err
	}
	if err := s.repo.Create(site, in.Domains); err != nil {
		return nil, err
	}
	if err := s.applyPlatformRuntime(site, actor, ip); err != nil {
		return nil, err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return nil, err
	}
	s.audit.Record(actor, "website.create", "website", fmt.Sprintf("%d", site.ID), ip, in)
	return site, nil
}

func (s *WebsiteService) Update(ctx context.Context, id uint, in WebsiteInput, actor *uint, ip string) error {
	if err := s.validate(in); err != nil {
		return err
	}
	if err := s.ensurePublicWebRoot(in.RootPath); err != nil {
		return err
	}
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	site.Name = in.Name
	site.RootPath = in.RootPath
	site.Type = in.Type
	site.PHPVersion = in.PHPVersion
	site.ProxyTarget = in.ProxyTarget
	site.CustomDirectives = in.CustomDirectives
	site.SiteUserID = in.SiteUserID
	site.Enabled = in.Enabled
	if err := s.repo.Update(site, in.Domains); err != nil {
		return err
	}
	if err := s.applyPlatformRuntime(site, actor, ip); err != nil {
		return err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return err
	}
	s.audit.Record(actor, "website.update", "website", fmt.Sprintf("%d", id), ip, in)
	return nil
}

func (s *WebsiteService) Delete(ctx context.Context, id uint, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if app := runtimeFromWebsite(site); app != nil {
		if err := s.deleteLinkedAppRuntime(ctx, app, actor, ip); err != nil {
			return err
		}
	}
	if err := s.deleteWebsiteScopedUsers(ctx, site, actor, ip); err != nil {
		return err
	}
	if err := s.deleteWebsiteLegacyData(site, actor, ip); err != nil {
		return err
	}
	if s.cron != nil {
		if err := s.cron.DeleteWebsiteJobs(ctx, site.ID, actor, ip); err != nil {
			return err
		}
	}
	if err := s.disableConfig(site.Name); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(s.cfg.Paths.NginxAvailableDir, site.Name+".conf")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.TrimSpace(site.RootPath) != "" {
		if err := removeTreeSafe(site.RootPath, s.cfg.Paths.DefaultSiteRoot, s.cfg.Paths.StorageRoot); err != nil {
			return err
		}
	}
	logDir := filepath.Dir(site.AccessLogPath)
	if strings.TrimSpace(logDir) == "" || logDir == "." {
		logDir = filepath.Join(s.cfg.Paths.LogRoot, "sites", site.Name)
	}
	if err := removeTreeSafe(logDir, s.cfg.Paths.LogRoot, s.cfg.Paths.StorageRoot); err != nil {
		return err
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.audit.Record(actor, "website.delete", "website", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *WebsiteService) RemoveRuntime(ctx context.Context, id uint, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	app := runtimeFromWebsite(site)
	if app == nil {
		return nil
	}
	if err := s.deleteLinkedAppRuntime(ctx, app, actor, ip); err != nil {
		return err
	}
	if err := s.appRepo.ClearRuntime(id); err != nil {
		return err
	}
	s.audit.Record(actor, "platform.runtime.delete", "website", fmt.Sprintf("%d", id), ip, nil)
	return nil
}

func (s *WebsiteService) ToggleEnabled(ctx context.Context, id uint, enabled bool, actor *uint, ip string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	site.Enabled = enabled
	if err := s.repo.Update(site, domainsFromModel(site.Domains)); err != nil {
		return err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return err
	}
	s.audit.Record(actor, "website.toggle", "website", fmt.Sprintf("%d", id), ip, map[string]bool{"enabled": enabled})
	return nil
}

// ApplyAppProxy sets a linked website to type proxy targeting the app bind address and reloads nginx.
func (s *WebsiteService) ApplyAppProxy(ctx context.Context, websiteID *uint, host string, port int, actor *uint, ip string) error {
	if websiteID == nil || *websiteID == 0 {
		return nil
	}
	site, err := s.repo.Find(*websiteID)
	if err != nil {
		return err
	}
	site.Type = "proxy"
	site.ProxyTarget = fmt.Sprintf("http://%s:%d", host, port)
	if err := s.repo.Update(site, domainsFromModel(site.Domains)); err != nil {
		return err
	}
	if err := s.writeNginxConfig(ctx, site); err != nil {
		return err
	}
	if actor != nil {
		s.audit.Record(actor, "website.proxy_from_app", "website", fmt.Sprintf("%d", site.ID), ip, map[string]string{"proxy_target": site.ProxyTarget})
	}
	return nil
}

func (s *WebsiteService) RecentLogs(id uint, lines int) (string, string, error) {
	site, err := s.repo.Find(id)
	if err != nil {
		return "", "", err
	}
	if lines <= 0 {
		lines = 120
	}
	access, _ := tailFile(site.AccessLogPath, lines)
	errors, _ := tailFile(site.ErrorLogPath, lines)
	return access, errors, nil
}

func ParsePhpSettings(raw string) PhpSettingsData {
	var data PhpSettingsData
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &data)
	}
	if data.MemoryLimit == "" {
		data.MemoryLimit = "256M"
	}
	if data.MaxExecutionTime == "" {
		data.MaxExecutionTime = "60"
	}
	if data.MaxInputTime == "" {
		data.MaxInputTime = "60"
	}
	if data.MaxInputVars == "" {
		data.MaxInputVars = "1000"
	}
	if data.PostMaxSize == "" {
		data.PostMaxSize = "64M"
	}
	if data.UploadMaxFilesize == "" {
		data.UploadMaxFilesize = "64M"
	}
	return data
}

func (s *WebsiteService) UpdateAppRuntime(id uint, runtime string) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	site.AppRuntime = runtime
	domains := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		domains = append(domains, d.Domain)
	}
	return s.repo.Update(site, domains)
}

func (s *WebsiteService) UpdatePhpSettings(id uint, phpVersion string, data PhpSettingsData) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if phpVersion != "" {
		site.PHPVersion = phpVersion
	}
	b, _ := json.Marshal(data)
	site.PhpSettings = string(b)
	domains := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		domains = append(domains, d.Domain)
	}
	if err := s.repo.Update(site, domains); err != nil {
		return err
	}
	return s.applyPlatformRuntime(site, nil, "")
}

func (s *WebsiteService) RefreshConfig(ctx context.Context, id uint) error {
	site, err := s.repo.Find(id)
	if err != nil {
		return err
	}
	if err := s.applyPlatformRuntime(site, nil, ""); err != nil {
		return err
	}
	return s.writeNginxConfig(ctx, site)
}

func (s *WebsiteService) writeNginxConfig(ctx context.Context, site *models.Website) error {
	if !s.cfg.Features.EnableNginxManage {
		return nil
	}
	var cert *models.SSLCertificate
	if s.sslRepo != nil {
		if items, err := s.sslRepo.List(); err == nil {
			cert = firstWebsiteCert(site, items)
		}
	}
	var basicAuth *models.BasicAuth
	if s.basicAuth != nil {
		basicAuth, _ = s.basicAuth.FindByWebsite(site.ID)
	}
	ipBlocks := []models.IPBlock{}
	if s.ipBlocks != nil {
		ipBlocks, _ = s.ipBlocks.ListByWebsite(site.ID)
	}
	botBlocks := []models.BotBlock{}
	if s.botBlocks != nil {
		botBlocks, _ = s.botBlocks.ListByWebsite(site.ID)
	}
	basicAuthPath := ""
	if basicAuth != nil && basicAuth.Enabled && strings.TrimSpace(basicAuth.Username) != "" && strings.TrimSpace(basicAuth.Password) != "" {
		path, err := s.ensureBasicAuthFile(site, basicAuth)
		if err != nil {
			return err
		}
		basicAuthPath = path
	}
	cfg := nginx.BuildWebsiteConfig(s.cfg, site, nginx.WebsiteConfigOptions{
		Certificate:   cert,
		BasicAuth:     basicAuth,
		BasicAuthPath: basicAuthPath,
		IPBlocks:      ipBlocks,
		BotBlocks:     botBlocks,
	})
	if err := utils.WriteFileAtomic(cfg.ConfigPath, []byte(cfg.Content), 0o644); err != nil {
		return err
	}
	if site.Enabled {
		if err := s.enableConfig(cfg.ConfigPath, cfg.EnabledPath); err != nil {
			return err
		}
	} else {
		if err := os.Remove(cfg.EnabledPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	now := time.Now()
	_ = s.nginxRepo.Upsert(&models.NginxSiteConfig{WebsiteID: site.ID, ConfigPath: cfg.ConfigPath, EnabledPath: cfg.EnabledPath, Checksum: cfg.Checksum, Enabled: site.Enabled, LastValidatedAt: &now})
	if err := s.adapter.Nginx().Validate(ctx, s.cfg.Paths.NginxBinary); err != nil {
		return err
	}
	return s.adapter.Nginx().Reload(ctx, s.cfg.Paths.NginxBinary)
}

func (s *WebsiteService) applyPlatformRuntime(site *models.Website, actor *uint, ip string) error {
	if s.runtime == nil || site == nil {
		return nil
	}
	switch site.Type {
	case "php":
		return s.runtime.ApplyPlatformRuntime(site.RootPath, "php", site.PHPVersion, actor, ip)
	case "proxy":
		if strings.TrimSpace(site.AppRuntime) != "" {
			version := runtimeVersionFromWebsite(site)
			if version != "" {
				return s.runtime.ApplyPlatformRuntime(site.RootPath, site.AppRuntime, version, actor, ip)
			}
		}
	}
	return s.runtime.ApplyPlatformRuntime(site.RootPath, "", "", actor, ip)
}

func (s *WebsiteService) ensurePublicWebRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	if err := os.Chmod(root, 0o755); err != nil {
		return fmt.Errorf("set site root permissions: %w", err)
	}
	hiddenDir := filepath.Join(root, ".deploycp")
	if stat, err := os.Stat(hiddenDir); err == nil && stat.IsDir() {
		if chmodErr := os.Chmod(hiddenDir, 0o700); chmodErr != nil {
			return fmt.Errorf("secure runtime metadata directory: %w", chmodErr)
		}
	}
	return nil
}

func (s *WebsiteService) ensureBasicAuthFile(site *models.Website, auth *models.BasicAuth) (string, error) {
	path := filepath.Join(s.cfg.Paths.HTPasswdRoot, site.Name+".htpasswd")
	hash, err := utils.HashPassword(auth.Password)
	if err != nil {
		return "", err
	}
	content := fmt.Sprintf("%s:%s\n", auth.Username, hash)
	if err := utils.WriteFileAtomic(path, []byte(content), 0o640); err != nil {
		return "", err
	}
	return path, nil
}

func (s *WebsiteService) enableConfig(source, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	_ = os.Remove(link)
	return os.Symlink(absSource, link)
}

func (s *WebsiteService) disableConfig(name string) error {
	return os.Remove(filepath.Join(s.cfg.Paths.NginxEnabledDir, name+".conf"))
}

func (s *WebsiteService) deleteLinkedAppRuntime(ctx context.Context, app *models.GoApp, actor *uint, ip string) error {
	if app == nil {
		return nil
	}
	serviceName := strings.TrimSpace(app.ServiceName)
	unitPath := ""
	if serviceName != "" {
		if managed, err := s.services.FindByName(serviceName); err == nil && managed != nil {
			unitPath = managed.UnitPath
		}
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
	if err := s.deleteLegacyAppDatabases(app, actor, ip); err != nil {
		return err
	}
	return nil
}

func (s *WebsiteService) deleteWebsiteScopedUsers(ctx context.Context, site *models.Website, actor *uint, ip string) error {
	if site == nil {
		return nil
	}
	userIDs := map[uint]struct{}{}
	if site.SiteUserID != nil && *site.SiteUserID > 0 {
		userIDs[*site.SiteUserID] = struct{}{}
	}
	additional, err := s.siteUsers.ListByWebsite(site.ID)
	if err != nil {
		return err
	}
	for _, u := range additional {
		userIDs[u.ID] = struct{}{}
	}
	for uid := range userIDs {
		user, err := s.siteUsers.Find(uid)
		if err != nil {
			if errorsIsGormNotFound(err) {
				continue
			}
			return err
		}
		shouldDelete := true
		if site.SiteUserID != nil && *site.SiteUserID == uid {
			refs, err := s.repo.CountBySiteUserIDExcept(uid, site.ID)
			if err != nil {
				return err
			}
			if refs > 0 {
				shouldDelete = false
			}
		}
		if !shouldDelete {
			continue
		}
		if err := s.adapter.Users().Delete(ctx, user.Username); err != nil {
			return err
		}
		if err := s.siteUsers.Delete(uid); err != nil {
			return err
		}
		s.audit.Record(actor, "site_user.delete", "site_user", fmt.Sprintf("%d", uid), ip, map[string]string{"username": user.Username})
	}
	return nil
}

func (s *WebsiteService) deleteWebsiteLegacyData(site *models.Website, actor *uint, ip string) error {
	if site == nil {
		return nil
	}
	if s.ftp != nil {
		if err := s.ftp.DeleteByWebsite(context.Background(), site.ID, actor, ip); err != nil {
			return err
		}
	} else if err := s.ftpUsers.DeleteByWebsite(site.ID); err != nil {
		return err
	}
	domainSet := map[string]struct{}{}
	for _, d := range site.Domains {
		domain := strings.ToLower(strings.TrimSpace(d.Domain))
		if domain != "" {
			domainSet[domain] = struct{}{}
		}
	}

	dbItems, err := s.dbRepo.List()
	if err != nil {
		return err
	}
	siteToken := strings.ToLower(strings.TrimSpace(site.Name))
	for _, db := range dbItems {
		if db.WebsiteID != nil && *db.WebsiteID == site.ID {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "website-delete"})
			continue
		}
		if db.WebsiteID == nil && db.GoAppID == nil && siteToken != "" && strings.Contains(strings.ToLower(db.Label), siteToken) {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "website-delete-legacy-label"})
		}
	}

	redisItems, err := s.redisRepo.List()
	if err != nil {
		return err
	}
	for _, redis := range redisItems {
		if redis.WebsiteID != nil && *redis.WebsiteID == site.ID {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "website-delete"})
			continue
		}
		if redis.WebsiteID == nil && redis.GoAppID == nil && siteToken != "" && strings.Contains(strings.ToLower(redis.Label), siteToken) {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "website-delete-legacy-label"})
		}
	}

	sslItems, err := s.sslRepo.List()
	if err != nil {
		return err
	}
	for _, cert := range sslItems {
		if _, ok := domainSet[strings.ToLower(strings.TrimSpace(cert.Domain))]; !ok {
			continue
		}
		if s.ssl != nil {
			if err := s.ssl.Delete(cert.ID, actor, ip); err != nil {
				return err
			}
		} else {
			if err := s.sslRepo.Delete(cert.ID); err != nil {
				return err
			}
		}
		s.audit.Record(actor, "ssl.delete", "ssl_certificate", fmt.Sprintf("%d", cert.ID), ip, map[string]any{"source": "website-delete", "domain": cert.Domain})
	}
	if s.varnishOS != nil {
		if err := s.varnishOS.DeleteWebsiteConfig(context.Background(), site.ID, actor, ip); err != nil {
			return err
		}
	}
	return nil
}

func (s *WebsiteService) deleteLegacyAppDatabases(app *models.GoApp, actor *uint, ip string) error {
	if app == nil {
		return nil
	}
	dbItems, err := s.dbRepo.List()
	if err != nil {
		return err
	}
	appToken := strings.ToLower(strings.TrimSpace(app.Name))
	for _, db := range dbItems {
		if db.GoAppID != nil && *db.GoAppID == app.ID {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "app-delete"})
			continue
		}
		if db.WebsiteID == nil && db.GoAppID == nil && appToken != "" && strings.Contains(strings.ToLower(db.Label), appToken) {
			if s.database != nil {
				if err := s.database.DeleteDatabaseRecord(&db, actor, ip); err != nil {
					return err
				}
			} else if err := s.dbRepo.Delete(db.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "database.delete", "database_connection", fmt.Sprintf("%d", db.ID), ip, map[string]any{"source": "app-delete-legacy-label"})
		}
	}

	redisItems, err := s.redisRepo.List()
	if err != nil {
		return err
	}
	for _, redis := range redisItems {
		if redis.GoAppID != nil && *redis.GoAppID == app.ID {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "app-delete"})
			continue
		}
		if redis.WebsiteID == nil && redis.GoAppID == nil && appToken != "" && strings.Contains(strings.ToLower(redis.Label), appToken) {
			if s.database != nil {
				if err := s.database.DeleteRedisRecord(&redis, actor, ip); err != nil {
					return err
				}
			} else if err := s.redisRepo.Delete(redis.ID); err != nil {
				return err
			}
			s.audit.Record(actor, "redis.delete", "redis_connection", fmt.Sprintf("%d", redis.ID), ip, map[string]any{"source": "app-delete-legacy-label"})
		}
	}
	return nil
}

func (s *WebsiteService) validate(in WebsiteInput) error {
	if err := validators.Require(in.Name, "name"); err != nil {
		return err
	}
	if err := validators.ValidatePath(in.RootPath); err != nil {
		return err
	}
	if err := validators.ValidateDomains(in.Domains); err != nil {
		return err
	}
	switch in.Type {
	case "static", "proxy", "php":
	default:
		return fmt.Errorf("type must be static, php, or proxy")
	}
	if in.Type == "php" {
		if err := validators.ValidatePHPVersion(in.PHPVersion); err != nil {
			return err
		}
	}
	if in.Type == "proxy" && strings.TrimSpace(in.ProxyTarget) == "" {
		return fmt.Errorf("proxy target is required for proxy websites")
	}
	return nil
}

func domainsFromModel(items []models.WebsiteDomain) []string {
	out := make([]string, 0, len(items))
	for _, d := range items {
		out = append(out, d.Domain)
	}
	return out
}

func runtimeFromWebsite(site *models.Website) *models.GoApp {
	if site == nil {
		return nil
	}
	if strings.TrimSpace(site.AppRuntime) == "" && strings.TrimSpace(site.ServiceName) == "" {
		return nil
	}
	wid := site.ID
	return &models.GoApp{
		ID:               site.ID,
		Name:             site.Name,
		Runtime:          site.AppRuntime,
		ExecutionMode:    site.ExecutionMode,
		ProcessManager:   site.ProcessManager,
		BinaryPath:       site.BinaryPath,
		EntryPoint:       site.EntryPoint,
		WorkingDirectory: site.RootPath,
		Host:             site.Host,
		Port:             site.Port,
		StartArgs:        site.StartArgs,
		HealthPath:       site.HealthPath,
		RestartPolicy:    site.RestartPolicy,
		Workers:          site.Workers,
		WorkerClass:      site.WorkerClass,
		MaxMemory:        site.MaxMemory,
		Timeout:          site.Timeout,
		ExecMode:         site.ExecMode,
		StdoutLogPath:    site.StdoutLogPath,
		StderrLogPath:    site.StderrLogPath,
		ServiceName:      site.ServiceName,
		WebsiteID:        &wid,
		Enabled:          site.Enabled,
	}
}

type LogFileInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"-"`
}

func (s *WebsiteService) LogDir(id uint) (string, error) {
	site, err := s.repo.Find(id)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(site.AccessLogPath)
	if dir == "." || dir == "" {
		dir = filepath.Join(s.cfg.Paths.LogRoot, "sites", site.Name)
	}
	return dir, nil
}

func (s *WebsiteService) ListLogFiles(id uint) ([]LogFileInfo, error) {
	dir, err := s.LogDir(id)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(dir, 0o755)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []LogFileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		logType := "other"
		if strings.Contains(name, "access") {
			logType = "access"
		} else if strings.Contains(name, "error") {
			logType = "error"
		}
		files = append(files, LogFileInfo{
			Name: name,
			Type: logType,
			Path: filepath.Join(dir, name),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name > files[j].Name
	})
	return files, nil
}

func (s *WebsiteService) ReadLogFile(id uint, filename string, lines int) (string, error) {
	dir, err := s.LogDir(id)
	if err != nil {
		return "", err
	}
	safe := filepath.Base(filename)
	fp := filepath.Join(dir, safe)
	abs, _ := filepath.Abs(fp)
	absDir, _ := filepath.Abs(dir)
	if !strings.HasPrefix(abs, absDir) {
		return "", fmt.Errorf("invalid filename")
	}
	if lines <= 0 {
		lines = 100
	}
	content, err := tailFile(fp, lines)
	if err != nil {
		return "", err
	}
	return content, nil
}

func firstWebsiteCert(site *models.Website, items []models.SSLCertificate) *models.SSLCertificate {
	if site == nil {
		return nil
	}
	domainSet := make(map[string]struct{}, len(site.Domains))
	for _, item := range site.Domains {
		domainSet[strings.ToLower(strings.TrimSpace(item.Domain))] = struct{}{}
	}
	for i := range items {
		if _, ok := domainSet[strings.ToLower(strings.TrimSpace(items[i].Domain))]; !ok {
			continue
		}
		if strings.TrimSpace(items[i].CertPath) == "" || strings.TrimSpace(items[i].KeyPath) == "" {
			continue
		}
		return &items[i]
	}
	return nil
}

func runtimeVersionFromWebsite(site *models.Website) string {
	if site == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(site.AppRuntime)) {
	case "node":
		return "node20"
	case "python":
		return "python3.12"
	case "go":
		return "go1.25"
	case "php":
		if strings.TrimSpace(site.PHPVersion) != "" {
			return strings.TrimSpace(site.PHPVersion)
		}
		return "8.3"
	default:
		return ""
	}
}

func tailFile(path string, lines int) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errorsIsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	rows := strings.Split(string(content), "\n")
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n"), nil
}

func errorsIsNotFound(err error) bool {
	return err != nil && (os.IsNotExist(err) || errorsIsGormNotFound(err))
}

func errorsIsGormNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
