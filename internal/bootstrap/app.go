package bootstrap

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/csrf"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/gofiber/fiber/v2/middleware/session"
	storage "github.com/gofiber/storage/sqlite3/v2"
	"gorm.io/gorm"

	"deploycp/internal/config"
	"deploycp/internal/handlers"
	"deploycp/internal/middleware"
	"deploycp/internal/platform"
	platformdarwin "deploycp/internal/platform/darwin"
	platformdryrun "deploycp/internal/platform/dryrun"
	platformlinux "deploycp/internal/platform/linux"
	"deploycp/internal/repositories"
	"deploycp/internal/services"
	"deploycp/internal/system"
	"deploycp/internal/utils"
	"deploycp/internal/views"
)

type Application struct {
	Config   *config.Config
	DB       *gorm.DB
	Fiber    *fiber.App
	Repos    *repositories.Repositories
	Platform platform.Adapter

	Sessions *middleware.SessionManager

	RuntimeService   *services.RuntimeService
	FirewallService  *services.FirewallService
	CronService      *services.CronService
	DatabaseService  *services.DatabaseService
	FTPService       *services.FTPService
	VarnishService   *services.VarnishService
	WebsiteService   *services.WebsiteService
	AppService       *services.AppService
	SettingsService  *services.SettingsService
	PreflightService *services.PreflightService
	ReconcileService *services.ReconcileService
	HostLifecycle    *services.HostLifecycleService
	UpdateService    *services.UpdateService
	PlatformOps      *services.PlatformOpsService
	PanelDomain      *services.PanelDomainService

	AuthHandler      *handlers.AuthHandler
	DashboardHandler *handlers.DashboardHandler
	WebsiteHandler   *handlers.WebsiteHandler
	AppHandler       *handlers.AppHandler
	ServiceHandler   *handlers.ServiceHandler
	SettingsHandler  *handlers.SettingsHandler
	UpdateHandler    *handlers.UpdateHandler
	ElfinderHandler  *handlers.ElfinderHandler
}

type panelRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]panelRateBucket
}

type panelRateBucket struct {
	Count   int
	ResetAt time.Time
}

func Build() (*Application, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	logger := NewLogger(cfg)
	_ = logger

	db, err := NewDB(cfg)
	if err != nil {
		return nil, err
	}
	if err := SeedDefaults(db); err != nil {
		return nil, err
	}

	repos := repositories.New(db)
	auditService := services.NewAuditService(repos.Audit, repos.Activity)
	runner := system.NewRunner(auditService)
	var platformAdapter platform.Adapter
	switch cfg.Features.PlatformMode {
	case "dryrun":
		platformAdapter = platformdryrun.New(cfg)
		fmt.Println("[bootstrap] platform mode: dryrun — all OS operations are simulated")
	default:
		if runtime.GOOS == "darwin" {
			platformAdapter = platformdarwin.New(cfg, runner)
		} else {
			platformAdapter = platformlinux.New(cfg, runner)
		}
	}

	authService := services.NewAuthService(cfg, repos.Users, repos.Sessions, auditService)
	if err := authService.EnsureBootstrapAdmin(); err != nil {
		return nil, err
	}

	dashboardService := services.NewDashboardService(repos)
	dashboardService.StartCollector()
	runtimeService := services.NewRuntimeService(cfg, runner, auditService)
	firewallService := services.NewFirewallService(cfg, runner, auditService)
	cronService := services.NewCronService(cfg, runner, auditService)
	databaseService := services.NewDatabaseService(cfg, repos.Databases, repos.Redis, repos.Services, platformAdapter, auditService)
	ftpService := services.NewFTPService(cfg, repos.FTPUsers, repos.SiteUsers, repos.Settings, runner, auditService)
	varnishService := services.NewVarnishService(cfg, runner, auditService)
	sslService := services.NewSSLService(cfg, repos.SSL, repos.Settings, runner, auditService)
	packageService := services.NewSystemPackageService(cfg, runner)
	websiteService := services.NewWebsiteService(
		cfg,
		repos.Websites,
		repos.NginxSites,
		repos.GoApps,
		repos.Services,
		repos.SiteUsers,
		repos.FTPUsers,
		repos.Databases,
		repos.Redis,
		repos.SSL,
		repos.Varnish,
		repos.IPBlocks,
		repos.BotBlocks,
		repos.BasicAuths,
		repos.CloudflareConfigs,
		platformAdapter,
		auditService,
		sslService,
		runtimeService,
		cronService,
		databaseService,
		ftpService,
		varnishService,
		packageService,
	)
	appService := services.NewAppService(cfg, repos.GoApps, repos.Services, websiteService, platformAdapter, auditService, runtimeService)
	siteUserService := services.NewSiteUserService(cfg, repos.SiteUsers, platformAdapter, auditService)
	serviceService := services.NewServiceService(cfg, repos.Services, repos.Settings, platformAdapter, auditService, packageService)
	settingsService := services.NewSettingsService(cfg, repos.Settings, repos.UserPrefs, auditService)
	if err := settingsService.ApplyConfiguredTimezone(); err != nil {
		_ = settingsService.ApplyTimezone("UTC")
	}
	panelDomainService := services.NewPanelDomainService(cfg, settingsService, repos.Websites, platformAdapter, runner, auditService)
	updateService := services.NewUpdateService(cfg, repos.Settings, auditService)
	updateService.Start()
	panelUserService := services.NewPanelUserService(repos.Users, repos.UserPlatformAccess, auditService)
	preflightService := services.NewPreflightService(cfg, repos, platformAdapter)
	reconcileService := services.NewReconcileService(repos, websiteService, appService, firewallService, cronService, ftpService, varnishService, databaseService)
	hostLifecycleService := services.NewHostLifecycleService(cfg, repos, platformAdapter, websiteService, appService, siteUserService, databaseService, firewallService, ftpService, sslService)
	platformOpsService := services.NewPlatformOpsService(cfg, repos, platformAdapter, websiteService, appService, auditService)

	engine := views.NewEngine(cfg)

	store := session.New(session.Config{
		Storage:        storage.New(storage.Config{Database: cfg.Database.SQLitePath, Table: "fiber_sessions"}),
		KeyLookup:      "cookie:" + cfg.Security.SessionCookieName,
		CookieHTTPOnly: true,
		CookieSecure:   false,
		CookieSameSite: "Lax",
		Expiration:     24 * time.Hour,
	})
	sessionManager := middleware.NewSessionManager(store)

	app := fiber.New(fiber.Config{
		AppName: fmt.Sprintf("%s (%s)", cfg.App.Name, cfg.App.Env),
		Views:   engine,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			msg := err.Error()
			var fe *fiber.Error
			if errors.As(err, &fe) {
				code = fe.Code
				if fe.Message != "" {
					msg = fe.Message
				}
			}
			acc := strings.ToLower(c.Get("Accept", ""))
			if strings.Contains(acc, "application/json") && !strings.Contains(acc, "text/html") {
				return c.Status(code).JSON(fiber.Map{"error": msg})
			}
			return c.Status(code).Render("partials_error", fiber.Map{
				"Error":        msg,
				"StatusCode":   code,
				"AssetVersion": fmt.Sprintf("%d", time.Now().Unix()),
			})
		},
	})

	app.Use(recover.New())
	app.Use(requestid.New())
	app.Use(func(c *fiber.Ctx) error {
		err := c.Next()
		enforceSecureCookies(c, cfg)
		return err
	})
	app.Use(func(c *fiber.Ctx) error {
		c.Set("X-Frame-Options", "DENY")
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("Referrer-Policy", "same-origin")
		c.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.EqualFold(c.Protocol(), "https") || strings.EqualFold(c.Get("X-Forwarded-Proto"), "https") {
			c.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		return c.Next()
	})
	app.Use(func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasPrefix(path, "/assets/") {
			c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			c.Set("Pragma", "no-cache")
			c.Set("Expires", "0")
		} else if c.Method() == fiber.MethodGet {
			c.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			c.Set("Pragma", "no-cache")
			c.Set("Expires", "0")
		}
		return c.Next()
	})
	app.Static("/assets", "./frontend/assets", fiber.Static{
		Browse:        false,
		MaxAge:        0,
		CacheDuration: 0,
	})

	app.Use(middleware.InjectAuthUser(sessionManager, repos.Users, repos.UserPlatformAccess))
	app.Use(middleware.InjectTheme(sessionManager, repos.Settings, repos.UserPrefs))
	app.Use(func(c *fiber.Ctx) error {
		view := updateService.FooterView()
		c.Locals("app_version_display", view.DisplayVersion)
		c.Locals("app_version_is_dev", view.IsDev)
		return c.Next()
	})
	// Redirect all requests to /setup if no admin exists yet.
	app.Use(func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasPrefix(path, "/setup") || strings.HasPrefix(path, "/assets/") {
			return c.Next()
		}
		if authService.NeedsSetup() {
			return c.Redirect("/setup")
		}
		return c.Next()
	})
	panelLimiter := &panelRateLimiter{}
	app.Use(func(c *fiber.Ctx) error {
		if panelRobotsBlocked(settingsService) {
			c.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		}
		if err := enforcePanelSecurity(c, settingsService, panelLimiter); err != nil {
			return err
		}
		return c.Next()
	})
	app.Use(middleware.PanelBasicAuth(repos.Settings))
	if cfg.Security.CSRFEnabled {
		sm := sessionManager
		app.Use(csrf.New(csrf.Config{
			Next: func(c *fiber.Ctx) bool {
				return isDatabaseUIProxyPath(c.Path())
			},
			KeyLookup:      "header:X-CSRF-Token",
			CookieName:     "deploycp_csrf",
			CookieSecure:   false,
			CookieHTTPOnly: true,
			Expiration:     12 * time.Hour,
			ContextKey:     "csrf",
			Extractor: func(c *fiber.Ctx) (string, error) {
				if token := c.Get("X-CSRF-Token"); token != "" {
					return token, nil
				}
				if token := c.FormValue("_csrf"); token != "" {
					return token, nil
				}
				return "", csrf.ErrTokenNotFound
			},
			ErrorHandler: func(c *fiber.Ctx, _ error) error {
				acc := strings.ToLower(c.Get("Accept", ""))
				if strings.Contains(acc, "application/json") && !strings.Contains(acc, "text/html") {
					return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "forbidden"})
				}
				if c.Path() == "/login" && c.Method() == fiber.MethodPost {
					sm.SetFlash(c, "Your session expired or the security check failed. Please sign in again.")
					return c.Redirect("/login")
				}
				sm.SetFlash(c, "Security token expired or invalid. Please try again.")
				if ref := c.Get("Referer"); ref != "" {
					if u, err := url.Parse(ref); err == nil && u.Hostname() == c.Hostname() {
						return c.Redirect(ref)
					}
				}
				return c.Redirect("/")
			},
		}))
	}

	instance := &Application{
		Config:           cfg,
		DB:               db,
		Fiber:            app,
		Repos:            repos,
		Platform:         platformAdapter,
		Sessions:         sessionManager,
		RuntimeService:   runtimeService,
		FirewallService:  firewallService,
		CronService:      cronService,
		DatabaseService:  databaseService,
		FTPService:       ftpService,
		VarnishService:   varnishService,
		WebsiteService:   websiteService,
		AppService:       appService,
		SettingsService:  settingsService,
		PreflightService: preflightService,
		ReconcileService: reconcileService,
		HostLifecycle:    hostLifecycleService,
		UpdateService:    updateService,
		PlatformOps:      platformOpsService,
		PanelDomain:      panelDomainService,
		AuthHandler:      handlers.NewAuthHandler(cfg, sessionManager, authService, settingsService),
		DashboardHandler: handlers.NewDashboardHandler(cfg, sessionManager, dashboardService),
		WebsiteHandler:   handlers.NewWebsiteHandler(cfg, sessionManager, websiteService, repos.SiteUsers, siteUserService, databaseService, sslService, repos.Databases, repos.SSL, settingsService, repos.NginxSites, repos.CronJobs, repos.Varnish, repos.IPBlocks, repos.BotBlocks, repos.BasicAuths, repos.CloudflareConfigs, repos.FTPUsers, appService, cronService, ftpService, varnishService, platformAdapter, platformOpsService),
		AppHandler:       handlers.NewAppHandler(cfg, sessionManager, appService, websiteService, settingsService, siteUserService, repos.SiteUsers, databaseService, sslService, repos.Databases, repos.FTPUsers, ftpService, platformOpsService),
		ServiceHandler:   handlers.NewServiceHandler(cfg, sessionManager, serviceService),
		SettingsHandler:  handlers.NewSettingsHandler(cfg, sessionManager, settingsService, serviceService, panelUserService, repos.Audit, repos.Firewalls, repos.UserPlatformAccess, websiteService, appService, auditService, firewallService, runtimeService, ftpService, updateService, panelDomainService),
		UpdateHandler:    handlers.NewUpdateHandler(cfg, sessionManager, updateService),
		ElfinderHandler:  handlers.NewElfinderHandler(cfg, sessionManager, websiteService, platformAdapter, runner),
	}

	instance.registerRoutes()
	return instance, nil
}

func isDatabaseUIProxyPath(path string) bool {
	parts := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	if len(parts) < 6 {
		return false
	}
	if parts[0] != "websites" && parts[0] != "apps" {
		return false
	}
	return parts[2] == "manage" &&
		parts[3] == "database" &&
		(parts[5] == "adminer" || parts[5] == "postgres-adminer")
}

func secureCookiesForRequest(cfg *config.Config, c *fiber.Ctx) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.Security.SessionSecureCookies)) {
	case "true":
		return true
	case "false":
		return false
	default:
		return strings.EqualFold(c.Protocol(), "https") || strings.EqualFold(c.Get("X-Forwarded-Proto"), "https")
	}
}

func enforceSecureCookies(c *fiber.Ctx, cfg *config.Config) {
	if !secureCookiesForRequest(cfg, c) {
		return
	}
	values := c.Response().Header.PeekAll(fiber.HeaderSetCookie)
	if len(values) == 0 {
		return
	}
	c.Response().Header.Del(fiber.HeaderSetCookie)
	for _, value := range values {
		cookie := string(value)
		lower := strings.ToLower(cookie)
		if !strings.Contains(lower, "; secure") && !strings.HasSuffix(lower, " secure") {
			cookie += "; Secure"
		}
		c.Response().Header.Add(fiber.HeaderSetCookie, cookie)
	}
}

func panelRobotsBlocked(settings *services.SettingsService) bool {
	if settings == nil {
		return true
	}
	value, err := settings.Get("panel_robots_block_enabled")
	if err != nil {
		return true
	}
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "on")
}

func panelSetting(settings *services.SettingsService, key string) string {
	if settings == nil {
		return ""
	}
	value, err := settings.Get(key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func panelSettingBool(settings *services.SettingsService, key string, fallback bool) bool {
	value := panelSetting(settings, key)
	if value == "" {
		return fallback
	}
	return strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "on")
}

func panelSettingInt(settings *services.SettingsService, key string, fallback int) int {
	value := panelSetting(settings, key)
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func enforcePanelSecurity(c *fiber.Ctx, settings *services.SettingsService, limiter *panelRateLimiter) error {
	clientIP := c.IP()
	allowlist := panelSetting(settings, "panel_ip_allowlist")
	denylist := panelSetting(settings, "panel_ip_denylist")
	if denylist != "" && panelIPMatchesList(clientIP, denylist) {
		return c.Status(fiber.StatusForbidden).SendString("Panel access denied from this IP.")
	}
	if allowlist != "" && !panelIPMatchesList(clientIP, allowlist) {
		return c.Status(fiber.StatusForbidden).SendString("Panel access is restricted from this IP.")
	}

	userAgent := strings.ToLower(strings.TrimSpace(c.Get("User-Agent")))
	for _, item := range strings.Split(panelSetting(settings, "panel_user_agent_denylist"), "\n") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" && strings.Contains(userAgent, item) {
			return c.Status(fiber.StatusForbidden).SendString("Panel access denied for this user agent.")
		}
	}

	if limiter != nil && panelSettingBool(settings, "panel_rate_limit_enabled", true) && !strings.HasPrefix(c.Path(), "/assets/") {
		limit := panelSettingInt(settings, "panel_rate_limit_per_min", 300)
		if !limiter.allow(clientIP, limit, time.Now()) {
			c.Set("Retry-After", "60")
			return c.Status(fiber.StatusTooManyRequests).SendString("Too many panel requests. Please try again shortly.")
		}
	}
	return nil
}

func panelIPMatchesList(ip, list string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, item := range strings.Split(list, "\n") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if candidate := net.ParseIP(item); candidate != nil {
			if candidate.Equal(parsed) {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(item)
		if err == nil && network.Contains(parsed) {
			return true
		}
	}
	return false
}

func (l *panelRateLimiter) allow(key string, limit int, now time.Time) bool {
	if l == nil {
		return true
	}
	if limit < 1 {
		limit = 300
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]panelRateBucket{}
	}
	for itemKey, bucket := range l.buckets {
		if now.After(bucket.ResetAt.Add(time.Minute)) {
			delete(l.buckets, itemKey)
		}
	}
	bucket := l.buckets[key]
	if bucket.ResetAt.IsZero() || !now.Before(bucket.ResetAt) {
		bucket = panelRateBucket{ResetAt: now.Add(time.Minute)}
	}
	bucket.Count++
	l.buckets[key] = bucket
	return bucket.Count <= limit
}

func (a *Application) registerRoutes() {
	app := a.Fiber
	app.Get("/robots.txt", func(c *fiber.Ctx) error {
		c.Type("txt")
		if panelRobotsBlocked(a.SettingsService) {
			return c.SendString("User-agent: *\nDisallow: /\n")
		}
		return c.SendString("User-agent: *\nAllow: /\n")
	})

	app.Get("/setup", a.AuthHandler.SetupPage)
	app.Post("/setup", a.AuthHandler.SetupCreate)
	app.Get("/login", a.AuthHandler.LoginPage)
	app.Get("/login/captcha", a.AuthHandler.LoginCaptcha)
	app.Post("/login", middleware.LoginRateLimit(a.Config.Security.LoginRateLimitPerMin), a.AuthHandler.Login)
	app.Post("/logout", a.AuthHandler.Logout)
	app.Post("/theme", a.AuthHandler.ThemeSwitch)

	secured := app.Group("", middleware.AuthRequired(a.Sessions), middleware.PlatformAccess(a.Sessions, a.Repos.UserPlatformAccess))
	adminOnly := middleware.AdminOnly(a.Sessions)
	provisioningAllowed := middleware.ProvisioningAllowed(a.Sessions)
	secured.Get("/", a.DashboardHandler.Index)
	secured.Get("/dashboard/live", a.DashboardHandler.Live)
	secured.Get("/dashboard/history", a.DashboardHandler.History)
	secured.Get("/profile", a.AuthHandler.ProfilePage)
	secured.Post("/profile", a.AuthHandler.ProfileUpdate)
	secured.Get("/profile/password", a.AuthHandler.PasswordPage)
	secured.Post("/profile/password", a.AuthHandler.PasswordUpdate)
	secured.Post("/profile/theme", a.AuthHandler.ThemeUpdate)

	secured.Post("/websites", provisioningAllowed, a.WebsiteHandler.Create)
	secured.Post("/websites/:id", a.WebsiteHandler.Update)
	secured.Post("/websites/:id/delete", a.WebsiteHandler.Delete)
	secured.Post("/websites/:id/toggle", a.WebsiteHandler.Toggle)
	secured.Post("/websites/:id/manage/database", a.WebsiteHandler.ManageCreateDatabase)
	secured.Post("/websites/:id/manage/database/:dbid/delete", a.WebsiteHandler.ManageDeleteDatabase)
	secured.Post("/websites/:id/manage/database/:dbid/password", a.WebsiteHandler.ManageUpdateDatabasePassword)
	secured.All("/websites/:id/manage/database/:dbid/adminer*", a.WebsiteHandler.ManageAdminerDB)
	secured.All("/websites/:id/manage/database/:dbid/postgres-adminer*", a.WebsiteHandler.ManageOpenPostgresGUI)
	secured.Post("/websites/:id/manage/site-user", a.WebsiteHandler.ManageCreateSiteUser)
	secured.Post("/websites/:id/manage/site-user/password", a.WebsiteHandler.ManageResetSiteUserPassword)
	secured.Post("/websites/:id/manage/site-user/:uid/password", a.WebsiteHandler.ManageResetSiteUserPasswordByUID)
	secured.Post("/websites/:id/manage/site-user/:uid/delete", a.WebsiteHandler.ManageDeleteSiteUser)
	secured.Post("/websites/:id/manage/domain", a.WebsiteHandler.ManageAddDomain)
	secured.Post("/websites/:id/manage/ssl/letsencrypt", a.WebsiteHandler.ManageSSLLetsEncrypt)
	secured.Post("/websites/:id/manage/ssl/import", a.WebsiteHandler.ManageSSLImport)
	secured.Post("/websites/:id/manage/ssl/self-signed", a.WebsiteHandler.ManageSSLSelfSigned)
	secured.Post("/websites/:id/manage/ssl/:cid/renew", a.WebsiteHandler.ManageRenewSSL)
	secured.Post("/websites/:id/manage/ssl/:cid/delete", a.WebsiteHandler.ManageDeleteSSL)
	secured.Post("/websites/:id/manage/redis", a.WebsiteHandler.ManageCreateRedis)
	secured.Get("/websites/:id/manage/redis/:rid/info", a.WebsiteHandler.ManageRedisInfo)
	secured.Post("/websites/:id/manage/redis/:rid/password", a.WebsiteHandler.ManageUpdateRedisPassword)
	secured.Post("/websites/:id/manage/redis/:rid/delete", a.WebsiteHandler.ManageDeleteRedis)
	secured.Post("/websites/:id/manage/vhost", adminOnly, a.WebsiteHandler.ManageSaveVhost)
	secured.Post("/websites/:id/manage/cron-jobs", a.WebsiteHandler.ManageCreateCronJob)
	secured.Post("/websites/:id/manage/cron-jobs/:cid/delete", a.WebsiteHandler.ManageDeleteCronJob)
	secured.Post("/websites/:id/manage/varnish", a.WebsiteHandler.ManageUpdateVarnish)
	secured.Post("/websites/:id/manage/security/ip-block", a.WebsiteHandler.ManageAddIPBlock)
	secured.Post("/websites/:id/manage/security/ip-block/:bid/delete", a.WebsiteHandler.ManageDeleteIPBlock)
	secured.Post("/websites/:id/manage/security/bot-block", a.WebsiteHandler.ManageAddBotBlock)
	secured.Post("/websites/:id/manage/security/bot-block/:bid/delete", a.WebsiteHandler.ManageDeleteBotBlock)
	secured.Post("/websites/:id/manage/security/basic-auth", a.WebsiteHandler.ManageUpdateBasicAuth)
	secured.Post("/websites/:id/manage/security/cloudflare", a.WebsiteHandler.ManageUpdateCloudflare)
	secured.Post("/websites/:id/manage/ftp-users", a.WebsiteHandler.ManageCreateFTPUser)
	secured.Post("/websites/:id/manage/ftp-users/:fid/password", a.WebsiteHandler.ManageResetFTPPassword)
	secured.Post("/websites/:id/manage/ftp-users/:fid/delete", a.WebsiteHandler.ManageDeleteFTPUser)
	secured.Post("/websites/:id/manage/php-settings", a.WebsiteHandler.ManageSavePhpSettings)
	secured.Post("/websites/:id/manage/platform-runtime", a.WebsiteHandler.ManageUpdatePlatformRuntime)
	secured.Post("/websites/:id/manage/create-app", a.WebsiteHandler.ManageCreateLinkedApp)
	secured.Post("/websites/:id/manage/delete-app", a.WebsiteHandler.ManageDeleteLinkedApp)
	secured.Get("/websites/:id/manage/log-files", a.WebsiteHandler.LogFiles)
	secured.Get("/websites/:id/manage/log-content", a.WebsiteHandler.LogContent)
	secured.Post("/platforms/:ref/manage/ops/health", a.WebsiteHandler.ManageOpsHealth)
	secured.Post("/platforms/:ref/manage/ops/deploy-config", adminOnly, a.WebsiteHandler.ManageOpsSaveDeployConfig)
	secured.Post("/platforms/:ref/manage/ops/deploy", adminOnly, a.WebsiteHandler.ManageOpsDeploy)
	secured.Post("/platforms/:ref/manage/ops/runtime-restart", adminOnly, a.WebsiteHandler.ManageOpsRuntimeRestart)
	secured.Post("/platforms/:ref/manage/ops/backup", adminOnly, a.WebsiteHandler.ManageOpsBackup)
	secured.Post("/platforms/:ref/manage/ops/restore", adminOnly, a.WebsiteHandler.ManageOpsRestore)
	secured.Post("/platforms/:ref/manage/ops/repair", adminOnly, a.WebsiteHandler.ManageOpsRepair)

	// elFinder File Manager connector
	secured.Get("/websites/:id/elfinder", a.ElfinderHandler.Connector)
	secured.Post("/websites/:id/elfinder", a.ElfinderHandler.Connector)

	secured.Get("/platforms", a.AppHandler.SitesApps)
	secured.Get("/platforms/new", provisioningAllowed, a.AppHandler.SitesAppsNew)
	secured.Get("/platforms/:ref", func(c *fiber.Ctx) error {
		kind, id, err := utils.DecodePlatformRef(c.Params("ref"))
		if err != nil {
			return c.Status(404).SendString("platform not found")
		}
		switch kind {
		case utils.PlatformKindWebsite:
			return a.WebsiteHandler.ShowByID(c, id)
		case utils.PlatformKindApp:
			return a.AppHandler.ShowByID(c, id)
		default:
			return c.Status(404).SendString("platform not found")
		}
	})
	secured.Post("/platforms", provisioningAllowed, a.AppHandler.SitesAppsCreate)
	secured.Post("/apps", provisioningAllowed, a.AppHandler.Create)
	secured.Post("/apps/:id", a.AppHandler.Update)
	secured.Post("/apps/:id/delete", a.AppHandler.Delete)
	secured.Post("/apps/:id/actions/:action", a.AppHandler.Action)
	secured.Post("/apps/:id/manage/database", a.AppHandler.ManageCreateDatabase)
	secured.Post("/apps/:id/manage/database/:dbid/delete", a.AppHandler.ManageDeleteDatabase)
	secured.Post("/apps/:id/manage/database/:dbid/password", a.AppHandler.ManageUpdateDatabasePassword)
	secured.All("/apps/:id/manage/database/:dbid/adminer*", a.AppHandler.ManageAdminerDB)
	secured.All("/apps/:id/manage/database/:dbid/postgres-adminer*", a.AppHandler.ManageOpenPostgresGUI)
	secured.Get("/apps/:id/manage/log-files", a.AppHandler.LogFiles)
	secured.Get("/apps/:id/manage/log-content", a.AppHandler.LogContent)
	secured.Post("/apps/:id/manage/redis", a.AppHandler.ManageCreateRedis)
	secured.Get("/apps/:id/manage/redis/:rid/info", a.AppHandler.ManageRedisInfo)
	secured.Post("/apps/:id/manage/redis/:rid/password", a.AppHandler.ManageUpdateRedisPassword)
	secured.Post("/apps/:id/manage/redis/:rid/delete", a.AppHandler.ManageDeleteRedis)
	secured.Post("/apps/:id/manage/site-user", a.AppHandler.ManageCreateSiteUser)
	secured.Post("/apps/:id/manage/site-user/:uid/password", a.AppHandler.ManageResetSiteUserPassword)
	secured.Post("/apps/:id/manage/site-user/:uid/delete", a.AppHandler.ManageDeleteSiteUser)
	secured.Post("/apps/:id/manage/ftp-users", a.AppHandler.ManageCreateFTPUser)
	secured.Post("/apps/:id/manage/ftp-users/:fid/password", a.AppHandler.ManageResetFTPPassword)
	secured.Post("/apps/:id/manage/ftp-users/:fid/delete", a.AppHandler.ManageDeleteFTPUser)
	secured.Post("/apps/:id/manage/ssl/letsencrypt", a.AppHandler.ManageSSLLetsEncrypt)
	secured.Post("/apps/:id/manage/ssl/import", a.AppHandler.ManageSSLImport)
	secured.Post("/apps/:id/manage/ssl/self-signed", a.AppHandler.ManageSSLSelfSigned)
	secured.Post("/apps/:id/manage/runtime-settings", a.AppHandler.ManageUpdateRuntime)

	secured.Get("/services", adminOnly, func(c *fiber.Ctx) error { return c.Redirect("/settings?tab=services") })
	secured.Post("/services", adminOnly, a.ServiceHandler.Create)
	secured.Post("/services/:id", adminOnly, a.ServiceHandler.Update)
	secured.Post("/services/:id/delete", adminOnly, a.ServiceHandler.Delete)
	secured.Post("/services/:ref/actions/:action", adminOnly, a.ServiceHandler.Action)
	secured.Get("/services/:ref/logs", adminOnly, a.ServiceHandler.Logs)

	secured.Get("/settings", adminOnly, a.SettingsHandler.Index)
	secured.Post("/settings", adminOnly, a.SettingsHandler.Update)
	secured.Post("/settings/general", adminOnly, a.SettingsHandler.UpdateGeneral)
	secured.Post("/settings/security", adminOnly, a.SettingsHandler.UpdateSecurity)
	secured.Post("/settings/users", adminOnly, a.SettingsHandler.UsersCreate)
	secured.Post("/settings/users/:id", adminOnly, a.SettingsHandler.UsersUpdate)
	secured.Post("/settings/users/:id/delete", adminOnly, a.SettingsHandler.UsersDelete)
	secured.Post("/settings/runtime-versions/:runtime/add", adminOnly, a.SettingsHandler.RuntimeVersionAdd)
	secured.Post("/settings/runtime-versions/:runtime/remove", adminOnly, a.SettingsHandler.RuntimeVersionRemove)
	secured.Post("/settings/runtime-versions/:runtime/default", adminOnly, a.SettingsHandler.RuntimeVersionDefault)
	secured.Post("/settings/runtime-versions/php/repository", adminOnly, a.SettingsHandler.RuntimePHPRepositoryRefresh)
	secured.Post("/settings/firewall", adminOnly, a.SettingsHandler.FirewallCreate)
	secured.Post("/settings/firewall/:id", adminOnly, a.SettingsHandler.FirewallUpdate)
	secured.Post("/settings/firewall/:id/delete", adminOnly, a.SettingsHandler.FirewallDelete)
	secured.Get("/updates", adminOnly, a.UpdateHandler.Index)
	secured.Get("/updates/status", adminOnly, a.UpdateHandler.Status)
	secured.Post("/updates/check", adminOnly, a.UpdateHandler.Check)
	secured.Post("/updates/install", adminOnly, a.UpdateHandler.Install)

	secured.Get("/logs", adminOnly, func(c *fiber.Ctx) error { return c.Redirect("/settings?tab=events") })
}
