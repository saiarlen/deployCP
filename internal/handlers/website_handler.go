package handlers

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"deploycp/internal/services"
	"deploycp/internal/utils"
	"deploycp/internal/validators"
)

type WebsiteHandler struct {
	base            *BaseHandler
	service         *services.WebsiteService
	siteUsers       *repositories.SiteUserRepository
	siteUserService *services.SiteUserService
	databaseService *services.DatabaseService
	sslService      *services.SSLService
	databases       *repositories.DatabaseConnectionRepository
	sslRepo         *repositories.SSLCertificateRepository
	settings        *services.SettingsService
	nginxSites      *repositories.NginxSiteRepository
	cronJobs        *repositories.CronJobRepository
	varnish         *repositories.VarnishConfigRepository
	ipBlocks        *repositories.IPBlockRepository
	botBlocks       *repositories.BotBlockRepository
	basicAuths      *repositories.BasicAuthRepository
	ftpUsers        *repositories.FTPUserRepository
	appService      *services.AppService
	cronService     *services.CronService
	ftpService      *services.FTPService
	varnishService  *services.VarnishService
}

type websiteFormInput struct {
	Website      services.WebsiteInput
	SiteUsername string
	SitePassword string
}

func NewWebsiteHandler(
	cfg *config.Config,
	sessions *middleware.SessionManager,
	service *services.WebsiteService,
	siteUsers *repositories.SiteUserRepository,
	siteUserService *services.SiteUserService,
	databaseService *services.DatabaseService,
	sslService *services.SSLService,
	databases *repositories.DatabaseConnectionRepository,
	sslRepo *repositories.SSLCertificateRepository,
	settings *services.SettingsService,
	nginxSites *repositories.NginxSiteRepository,
	cronJobs *repositories.CronJobRepository,
	varnish *repositories.VarnishConfigRepository,
	ipBlocks *repositories.IPBlockRepository,
	botBlocks *repositories.BotBlockRepository,
	basicAuths *repositories.BasicAuthRepository,
	ftpUsers *repositories.FTPUserRepository,
	appService *services.AppService,
	cronService *services.CronService,
	ftpService *services.FTPService,
	varnishService *services.VarnishService,
) *WebsiteHandler {
	return &WebsiteHandler{
		base:            &BaseHandler{Config: cfg, Sessions: sessions},
		service:         service,
		siteUsers:       siteUsers,
		siteUserService: siteUserService,
		databaseService: databaseService,
		sslService:      sslService,
		databases:       databases,
		sslRepo:         sslRepo,
		settings:        settings,
		nginxSites:      nginxSites,
		cronJobs:        cronJobs,
		varnish:         varnish,
		ipBlocks:        ipBlocks,
		botBlocks:       botBlocks,
		basicAuths:      basicAuths,
		ftpUsers:        ftpUsers,
		appService:      appService,
		cronService:     cronService,
		ftpService:      ftpService,
		varnishService:  varnishService,
	}
}

func (h *WebsiteHandler) Create(c *fiber.Ctx) error {
	form, err := h.payload(c)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/platforms/new")
	}
	websiteInput := form.Website
	createdSiteUserID, generatedPassword, err := h.ensureSiteUser(c.Context(), &websiteInput, form, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/platforms/new")
	}
	site, err := h.service.Create(c.Context(), websiteInput, currentUserID(c), c.IP())
	if err != nil {
		if createdSiteUserID != 0 {
			_ = h.siteUserService.Delete(c.Context(), createdSiteUserID, currentUserID(c), c.IP())
		}
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/platforms/new")
	}
	if generatedPassword != "" {
		h.base.Sessions.SetFlash(c, "Platform created. SSH user password: "+generatedPassword)
	} else {
		h.base.Sessions.SetFlash(c, "Platform created")
	}
	if err := h.createDatabaseFromScopedForm(c, site.Name, site.ID); err != nil {
		h.base.Sessions.SetFlash(c, "Platform created. DB setup warning: "+err.Error())
	}
	return c.Redirect(platformURL("website", site.ID))
}

func (h *WebsiteHandler) Show(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	return h.ShowByID(c, id)
}

func (h *WebsiteHandler) ShowByID(c *fiber.Ctx, id uint) error {
	item, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	dbItems, _ := h.databases.List()
	sslItems, _ := h.sslRepo.List()

	// Vhost config content
	vhostContent := ""
	if nginxCfg, err := h.nginxSites.FindByWebsite(id); err == nil && nginxCfg.ConfigPath != "" {
		if data, err := os.ReadFile(nginxCfg.ConfigPath); err == nil {
			vhostContent = string(data)
		}
	}

	cronJobs, _ := h.cronJobs.ListByWebsite(id)
	varnishCfg, _ := h.varnish.FindByWebsite(id)
	ipBlocks, _ := h.ipBlocks.ListByWebsite(id)
	botBlocks, _ := h.botBlocks.ListByWebsite(id)
	basicAuth, _ := h.basicAuths.FindByWebsite(id)
	ftpUsers, _ := h.ftpUsers.ListByWebsite(id)

	// Collect all SSH users: primary (from Website.SiteUser) + additional (from SiteUser.WebsiteID)
	var sshUsers []models.SiteUser
	primarySSHUserID := uint(0)
	if item.SiteUser != nil {
		sshUsers = append(sshUsers, *item.SiteUser)
		primarySSHUserID = item.SiteUser.ID
	}
	additionalSSH, _ := h.siteUsers.ListByWebsite(id)
	for _, u := range additionalSSH {
		if u.ID != primarySSHUserID {
			sshUsers = append(sshUsers, u)
		}
	}

	phpVersions := h.phpVersions()
	goVersions := h.runtimeVersions("go")
	pythonVersions := h.runtimeVersions("python")
	nodeVersions := h.runtimeVersions("node")

	scopedDB := websiteDBItems(item, dbItems)
	redisItems, _ := h.databaseService.ListRedis()
	scopedRedis := websiteRedisItems(item, redisItems)

	phpCfg := services.ParsePhpSettings(item.PhpSettings)

	var linkedApp *models.GoApp
	var linkedAppStatus *services.AppStatus
	linkedRuntimeVersion := ""
	if item.Type == "proxy" {
		linkedApp = websiteRuntimeApp(item)
		if linkedApp != nil {
			linkedAppStatus, _ = h.appService.Status(c.Context(), linkedApp.ID)
			if linkedAppStatus != nil && linkedAppStatus.App != nil {
				linkedRuntimeVersion = envVarValue(linkedAppStatus.App.EnvVars, "RUNTIME_VERSION")
			}
		}
	}
	primaryDomain := primaryWebsiteDomain(item.Domains)
	serverAddress := displayServerAddress(h.base.Config, c.Hostname())
	if linkedApp != nil {
		host := strings.TrimSpace(linkedApp.Host)
		switch host {
		case "", "0.0.0.0", "::":
		default:
			serverAddress = host
		}
	}

	return h.base.Render(c, "platforms_show", fiber.Map{
		"Title":                item.Name,
		"PlatformKind":         "website",
		"PrimaryDomain":        primaryDomain,
		"ServerAddress":        serverAddress,
		"Item":                 item,
		"PhpCfg":               phpCfg,
		"LinkedApp":            linkedApp,
		"LinkedAppStatus":      linkedAppStatus,
		"LinkedRuntimeVersion": linkedRuntimeVersion,
		"GoVersions":           goVersions,
		"NodeVersions":         nodeVersions,
		"PythonVersions":       pythonVersions,
		"MariaDBItems":         filterDBByEngine(scopedDB, "mariadb"),
		"PostgresItems":        filterDBByEngine(scopedDB, "postgres"),
		"RedisItems":           scopedRedis,
		"SSLItems":             websiteSSLItems(item, sslItems),
		"AdminerURL":           h.databaseService.AdminerURL(),
		"VhostContent":         vhostContent,
		"CronJobs":             cronJobs,
		"VarnishCfg":           varnishCfg,
		"IPBlocks":             ipBlocks,
		"BotBlocks":            botBlocks,
		"BasicAuth":            basicAuth,
		"FTPUsers":             ftpUsers,
		"PHPVersions":          phpVersions,
		"DefaultHomeDir":       platformHomeFromRoot(item.RootPath),
		"FileManagerRoot":      platformHomeFromRoot(item.RootPath),
		"SSHUsers":             sshUsers,
		"PrimarySSHUserID":     primarySSHUserID,
	})
}

func (h *WebsiteHandler) Update(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	form, err := h.payload(c)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURL("website", id))
	}
	if err := h.service.Update(c.Context(), id, form.Website, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURL("website", id))
	}
	h.base.Sessions.SetFlash(c, "Platform updated")
	return c.Redirect(platformURL("website", id))
}

func (h *WebsiteHandler) Delete(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.Delete(c.Context(), id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect("/platforms")
}

func (h *WebsiteHandler) Toggle(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	enabled := boolFromForm(c, "enabled")
	if err := h.service.ToggleEnabled(c.Context(), id, enabled, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else if enabled {
		h.base.Sessions.SetFlash(c, "Maintenance mode disabled")
	} else {
		h.base.Sessions.SetFlash(c, "Maintenance mode enabled")
	}
	return c.Redirect(platformURL("website", id))
}

func (h *WebsiteHandler) ManageCreateDatabase(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	host, port, err := managedDatabaseTarget(strings.TrimSpace(c.FormValue("engine")))
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "db"))
	}
	in := services.DBConnectionInput{
		Label:       strings.TrimSpace(c.FormValue("label")),
		Engine:      strings.TrimSpace(c.FormValue("engine")),
		Host:        host,
		Port:        port,
		Database:    strings.TrimSpace(c.FormValue("database")),
		Username:    strings.TrimSpace(c.FormValue("username")),
		Password:    c.FormValue("password"),
		Environment: strings.TrimSpace(c.FormValue("environment")),
		WebsiteID:   &id,
	}
	if in.Label == "" {
		in.Label = site.Name + "-db"
	}
	if in.Environment == "" {
		in.Environment = "production"
	}
	if err := h.databaseService.CreateDatabase(in, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Database connection saved")
	}
	return c.Redirect(platformURLWithTab("website", id, "db"))
}

func (h *WebsiteHandler) createDatabaseFromScopedForm(c *fiber.Ctx, fallbackLabel string, websiteID uint) error {
	database := strings.TrimSpace(c.FormValue("db_database"))
	if database == "" {
		return nil
	}
	wid := websiteID
	host, port, err := managedDatabaseTarget(strings.TrimSpace(c.FormValue("db_engine")))
	if err != nil {
		return err
	}
	in := services.DBConnectionInput{
		Label:       strings.TrimSpace(c.FormValue("db_label")),
		Engine:      strings.TrimSpace(c.FormValue("db_engine")),
		Host:        host,
		Port:        port,
		Database:    database,
		Username:    strings.TrimSpace(c.FormValue("db_username")),
		Password:    c.FormValue("db_password"),
		Environment: strings.TrimSpace(c.FormValue("db_environment")),
		WebsiteID:   &wid,
	}
	if in.Label == "" {
		in.Label = fallbackLabel + "-db"
	}
	if in.Engine == "" {
		in.Engine = "mariadb"
	}
	if in.Environment == "" {
		in.Environment = "production"
	}
	if strings.TrimSpace(in.Username) == "" || strings.TrimSpace(in.Password) == "" {
		return fmt.Errorf("db username and password are required when database name is set")
	}
	return h.databaseService.CreateDatabase(in, currentUserID(c), c.IP())
}

func (h *WebsiteHandler) ManageCreateSiteUser(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	root := strings.TrimSpace(site.RootPath)
	if root == "" {
		root = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + site.Name
	}
	platformHome := platformHomeFromRoot(root)
	item, generatedPassword, err := h.siteUserService.Create(c.Context(), services.SiteUserInput{
		Username:      strings.TrimSpace(c.FormValue("username")),
		HomeDirectory: platformHome,
		AllowedRoot:   platformHome,
		Password:      c.FormValue("password"),
		SSHEnabled:    true,
		WebsiteID:     &id,
	}, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "ssh"))
	}
	_ = item
	if strings.TrimSpace(c.FormValue("password")) == "" {
		h.base.Sessions.SetFlash(c, "SSH user created. Generated password: "+generatedPassword)
	} else {
		h.base.Sessions.SetFlash(c, "SSH user created")
	}
	return c.Redirect(platformURLWithTab("website", id, "ssh"))
}

func (h *WebsiteHandler) ManageResetSiteUserPassword(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	if site.SiteUserID == nil {
		h.base.Sessions.SetFlash(c, "No site user to update")
		return c.Redirect(platformURLWithTab("website", id, "settings"))
	}
	password := c.FormValue("password")
	generated, err := h.siteUserService.ResetPassword(c.Context(), *site.SiteUserID, password, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else if strings.TrimSpace(password) == "" {
		h.base.Sessions.SetFlash(c, "Password reset. Generated password: "+generated)
	} else {
		h.base.Sessions.SetFlash(c, "Password updated")
	}
	return c.Redirect(platformURLWithTab("website", id, "settings"))
}

func (h *WebsiteHandler) ManageDeleteSiteUser(c *fiber.Ctx) error {
	id, _ := repositories.ParseID(c.Params("id"))
	uid, err := repositories.ParseID(c.Params("uid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	if site.SiteUserID != nil && *site.SiteUserID == uid {
		h.base.Sessions.SetFlash(c, "Cannot delete the primary SSH user")
		return c.Redirect(platformURLWithTab("website", id, "ssh"))
	}
	if err := h.siteUserService.Delete(c.Context(), uid, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "SSH user deleted")
	}
	return c.Redirect(platformURLWithTab("website", id, "ssh"))
}

func (h *WebsiteHandler) ManageAddDomain(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	newDomain := strings.TrimSpace(strings.ToLower(c.FormValue("domain")))
	if newDomain == "" {
		h.base.Sessions.SetFlash(c, "domain is required")
		return c.Redirect(platformURLWithTab("website", id, "domains"))
	}
	domains := domainsFromWebsite(site.Domains)
	domains = append(domains, newDomain)
	if err := h.service.Update(c.Context(), id, services.WebsiteInput{
		Name:             site.Name,
		RootPath:         site.RootPath,
		Type:             site.Type,
		PHPVersion:       site.PHPVersion,
		ProxyTarget:      site.ProxyTarget,
		Domains:          domains,
		CustomDirectives: site.CustomDirectives,
		SiteUserID:       site.SiteUserID,
		Enabled:          site.Enabled,
	}, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Domain added")
	}
	return c.Redirect(platformURLWithTab("website", id, "domains"))
}

func (h *WebsiteHandler) ManageSSLLetsEncrypt(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	if len(site.Domains) == 0 {
		h.base.Sessions.SetFlash(c, "no domains configured — add a domain first")
		return c.Redirect(platformURLWithTab("website", id, "ssl"))
	}
	for _, d := range site.Domains {
		if err := h.sslService.CreateForWebsite(c.Context(), site, d.Domain, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, "Let's Encrypt for "+d.Domain+": "+err.Error())
			return c.Redirect(platformURLWithTab("website", id, "ssl"))
		}
	}
	_ = h.service.RefreshConfig(c.Context(), id)
	h.base.Sessions.SetFlash(c, "Let's Encrypt certificate requested for all domains")
	return c.Redirect(platformURLWithTab("website", id, "ssl"))
}

func (h *WebsiteHandler) ManageSSLImport(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	domain := strings.TrimSpace(c.FormValue("domain"))
	if domain == "" && len(site.Domains) > 0 {
		domain = site.Domains[0].Domain
	}
	if domain == "" {
		h.base.Sessions.SetFlash(c, "domain is required")
		return c.Redirect(platformURLWithTab("website", id, "ssl"))
	}
	privateKey := c.FormValue("private_key")
	certificate := c.FormValue("certificate")
	bundle := c.FormValue("certificate_chain")
	if err := h.sslService.CreateImport(domain, privateKey, certificate, bundle, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		_ = h.service.RefreshConfig(c.Context(), id)
		h.base.Sessions.SetFlash(c, "Certificate imported for "+domain)
	}
	return c.Redirect(platformURLWithTab("website", id, "ssl"))
}

func (h *WebsiteHandler) ManageSSLSelfSigned(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	domain := ""
	if len(site.Domains) > 0 {
		domain = site.Domains[0].Domain
	}
	if domain == "" {
		domain = site.Name
	}
	if err := h.sslService.CreateSelfSigned(domain, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		_ = h.service.RefreshConfig(c.Context(), id)
		h.base.Sessions.SetFlash(c, "Self-signed certificate created for "+domain)
	}
	return c.Redirect(platformURLWithTab("website", id, "ssl"))
}

// ── Redis ──

func (h *WebsiteHandler) ManageCreateRedis(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	port, _ := strconv.Atoi(c.FormValue("port", "6379"))
	db, _ := strconv.Atoi(c.FormValue("db", "0"))
	in := services.RedisInput{
		Label:       strings.TrimSpace(c.FormValue("label")),
		Host:        strings.TrimSpace(c.FormValue("host")),
		Port:        port,
		Password:    c.FormValue("password"),
		DB:          db,
		Environment: strings.TrimSpace(c.FormValue("environment")),
		WebsiteID:   &id,
	}
	if in.Label == "" {
		in.Label = site.Name + "-redis"
	}
	if in.Host == "" {
		in.Host = "127.0.0.1"
	}
	if in.Environment == "" {
		in.Environment = "production"
	}
	if err := h.databaseService.CreateRedis(in, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Redis connection saved")
	}
	return c.Redirect(platformURLWithTab("website", id, "databases"))
}

func (h *WebsiteHandler) ManageDeleteDatabase(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	dbid, err := repositories.ParseID(c.Params("dbid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, item, err := h.websiteDatabaseItem(id, dbid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "databases"))
	}
	if err := h.databaseService.DeleteDatabase(item.ID, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", site.ID, "databases"))
	}
	h.base.Sessions.SetFlash(c, "Database connection deleted")
	return c.Redirect(platformURLWithTab("website", site.ID, "databases"))
}

func (h *WebsiteHandler) ManageOpenPostgresGUI(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	dbid, err := repositories.ParseID(c.Params("dbid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_, item, err := h.websiteDatabaseItem(id, dbid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "databases"))
	}
	url, err := h.databaseService.PostgresGUIURL(item.ID)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "databases"))
	}
	return c.Redirect(url)
}

func (h *WebsiteHandler) ManageRedisInfo(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	rid, err := repositories.ParseID(c.Params("rid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_, item, err := h.websiteRedisItem(id, rid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "databases"))
	}
	info, err := h.databaseService.RedisInfo(item.ID)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "databases"))
	}
	return h.base.Render(c, "redis_info", fiber.Map{
		"Title":   "Redis Diagnostics",
		"Info":    info,
		"BackURL": platformURLWithTab("website", id, "databases"),
	})
}

func (h *WebsiteHandler) ManageDeleteRedis(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	rid, err := repositories.ParseID(c.Params("rid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, item, err := h.websiteRedisItem(id, rid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "databases"))
	}
	if err := h.databaseService.DeleteRedis(item.ID, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", site.ID, "databases"))
	}
	h.base.Sessions.SetFlash(c, "Redis connection deleted")
	return c.Redirect(platformURLWithTab("website", site.ID, "databases"))
}

func (h *WebsiteHandler) ManageRenewSSL(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	cid, err := repositories.ParseID(c.Params("cid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_, cert, err := h.websiteCertificateItem(id, cid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "ssl"))
	}
	if err := h.sslService.Renew(cert.ID, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "renewal hook executed")
	}
	return c.Redirect(platformURLWithTab("website", id, "ssl"))
}

func (h *WebsiteHandler) ManageDeleteSSL(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	cid, err := repositories.ParseID(c.Params("cid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_, cert, err := h.websiteCertificateItem(id, cid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("website", id, "ssl"))
	}
	if err := h.sslService.Delete(cert.ID, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Certificate deleted")
	}
	return c.Redirect(platformURLWithTab("website", id, "ssl"))
}

// ── Cron Jobs ──

func (h *WebsiteHandler) ManageCreateCronJob(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	schedule := strings.TrimSpace(c.FormValue("schedule"))
	command := strings.TrimSpace(c.FormValue("command"))
	if schedule == "" || command == "" {
		h.base.Sessions.SetFlash(c, "schedule and command are required")
		return c.Redirect(platformURLWithTab("website", id, "cron-jobs"))
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	job := &models.CronJob{WebsiteID: id, Schedule: schedule, Command: command}
	if err := h.cronJobs.Create(job); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else if h.cronService != nil {
		var siteUser *models.SiteUser
		if site.SiteUser != nil {
			siteUser = site.SiteUser
		}
		if err := h.cronService.UpsertWebsiteJob(c.Context(), site, siteUser, job, currentUserID(c), c.IP()); err != nil {
			_ = h.cronJobs.Delete(job.ID)
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect(platformURLWithTab("website", id, "cron-jobs"))
		}
		h.base.Sessions.SetFlash(c, "Cron job added")
	} else {
		h.base.Sessions.SetFlash(c, "Cron job added")
	}
	return c.Redirect(platformURLWithTab("website", id, "cron-jobs"))
}

func (h *WebsiteHandler) ManageDeleteCronJob(c *fiber.Ctx) error {
	id, _ := repositories.ParseID(c.Params("id"))
	cid, err := repositories.ParseID(c.Params("cid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if h.cronService != nil {
		if err := h.cronService.DeleteJob(c.Context(), cid, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect(platformURLWithTab("website", id, "cron-jobs"))
		}
	}
	if err := h.cronJobs.Delete(cid); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Cron job deleted")
	}
	return c.Redirect(platformURLWithTab("website", id, "cron-jobs"))
}

// ── Varnish ──

func (h *WebsiteHandler) ManageUpdateVarnish(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	lifetime, _ := strconv.Atoi(c.FormValue("cache_lifetime", "604800"))
	item := &models.VarnishConfig{
		WebsiteID:      id,
		Enabled:        boolFromForm(c, "enabled"),
		Server:         strings.TrimSpace(c.FormValue("server", "127.0.0.1:6081")),
		CacheLifetime:  lifetime,
		CacheTagPrefix: strings.TrimSpace(c.FormValue("cache_tag_prefix")),
		ExcludedParams: strings.TrimSpace(c.FormValue("excluded_params")),
		Excludes:       strings.TrimSpace(c.FormValue("excludes")),
	}
	site, findErr := h.service.Find(id)
	if findErr != nil {
		return c.Status(404).SendString("website not found")
	}
	if err := h.varnish.Upsert(item); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		if h.varnishService != nil {
			_ = h.varnishService.ApplyWebsiteConfig(c.Context(), site, item, currentUserID(c), c.IP())
		}
		_ = h.service.RefreshConfig(c.Context(), id)
		h.base.Sessions.SetFlash(c, "Varnish config saved")
	}
	return c.Redirect(platformURLWithTab("website", id, "varnish"))
}

// ── Security: IP Blocks ──

func (h *WebsiteHandler) ManageAddIPBlock(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	ip := strings.TrimSpace(c.FormValue("ip"))
	if ip == "" {
		h.base.Sessions.SetFlash(c, "IP address is required")
		return c.Redirect(platformURLWithTab("website", id, "security"))
	}
	if err := h.ipBlocks.Create(&models.IPBlock{WebsiteID: id, IP: ip}); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		_ = h.service.RefreshConfig(c.Context(), id)
		h.base.Sessions.SetFlash(c, "IP blocked")
	}
	return c.Redirect(platformURLWithTab("website", id, "security"))
}

func (h *WebsiteHandler) ManageDeleteIPBlock(c *fiber.Ctx) error {
	id, _ := repositories.ParseID(c.Params("id"))
	bid, err := repositories.ParseID(c.Params("bid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_ = h.ipBlocks.Delete(bid)
	_ = h.service.RefreshConfig(c.Context(), id)
	return c.Redirect(platformURLWithTab("website", id, "security"))
}

// ── Security: Bot Blocks ──

func (h *WebsiteHandler) ManageAddBotBlock(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	name := strings.TrimSpace(c.FormValue("bot_name"))
	if name == "" {
		h.base.Sessions.SetFlash(c, "Bot name is required")
		return c.Redirect(platformURLWithTab("website", id, "security"))
	}
	if err := h.botBlocks.Create(&models.BotBlock{WebsiteID: id, BotName: name}); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		_ = h.service.RefreshConfig(c.Context(), id)
		h.base.Sessions.SetFlash(c, "Bot blocked")
	}
	return c.Redirect(platformURLWithTab("website", id, "security"))
}

func (h *WebsiteHandler) ManageDeleteBotBlock(c *fiber.Ctx) error {
	id, _ := repositories.ParseID(c.Params("id"))
	bid, err := repositories.ParseID(c.Params("bid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_ = h.botBlocks.Delete(bid)
	_ = h.service.RefreshConfig(c.Context(), id)
	return c.Redirect(platformURLWithTab("website", id, "security"))
}

// ── Security: Basic Auth ──

func (h *WebsiteHandler) ManageUpdateBasicAuth(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	existing, _ := h.basicAuths.FindByWebsite(id)
	item := &models.BasicAuth{
		WebsiteID:      id,
		Enabled:        boolFromForm(c, "enabled"),
		Username:       strings.TrimSpace(c.FormValue("username")),
		WhitelistedIPs: strings.TrimSpace(c.FormValue("whitelisted_ips")),
	}
	password := c.FormValue("password")
	if strings.TrimSpace(password) == "" && existing != nil {
		item.PasswordEnc = existing.PasswordEnc
	} else if strings.TrimSpace(password) != "" {
		enc, encErr := utils.EncryptString(h.base.Config.Security.SessionSecret, password)
		if encErr != nil {
			h.base.Sessions.SetFlash(c, encErr.Error())
			return c.Redirect(platformURLWithTab("website", id, "security"))
		}
		item.PasswordEnc = enc
	} else if item.Enabled {
		h.base.Sessions.SetFlash(c, "basic auth password is required")
		return c.Redirect(platformURLWithTab("website", id, "security"))
	}
	if err := h.basicAuths.Upsert(item); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		_ = h.service.RefreshConfig(c.Context(), id)
		h.base.Sessions.SetFlash(c, "Basic auth settings saved")
	}
	return c.Redirect(platformURLWithTab("website", id, "security"))
}

// ── FTP Users ──

func (h *WebsiteHandler) ManageCreateFTPUser(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	username := strings.TrimSpace(c.FormValue("username"))
	if username == "" {
		h.base.Sessions.SetFlash(c, "username is required")
		return c.Redirect(platformURLWithTab("website", id, "ssh"))
	}
	password := strings.TrimSpace(c.FormValue("password"))
	generated := password == ""
	if password == "" {
		password = utils.GeneratePassword()
	}
	homeDir := strings.TrimSpace(c.FormValue("home_dir"))
	if homeDir == "" {
		homeDir = platformHomeFromRoot(site.RootPath)
		if homeDir == "" {
			homeDir = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + site.Name
		}
	}
	item := &models.FTPUser{WebsiteID: id, Username: username, Password: password, HomeDir: homeDir}
	if h.ftpService != nil {
		if err := h.ftpService.Create(c.Context(), item, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect(platformURLWithTab("website", id, "ssh"))
		}
	} else if err := h.ftpUsers.Create(item); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "FTP user created")
		return c.Redirect(platformURLWithTab("website", id, "ssh"))
	}
	if generated {
		h.base.Sessions.SetFlash(c, "FTP user created. Generated password: "+password)
	} else {
		h.base.Sessions.SetFlash(c, "FTP user created")
	}
	return c.Redirect(platformURLWithTab("website", id, "ssh"))
}

func (h *WebsiteHandler) ManageDeleteFTPUser(c *fiber.Ctx) error {
	id, _ := repositories.ParseID(c.Params("id"))
	fid, err := repositories.ParseID(c.Params("fid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if h.ftpService != nil {
		_ = h.ftpService.DeleteByID(c.Context(), fid, currentUserID(c), c.IP())
	} else {
		_ = h.ftpUsers.Delete(fid)
	}
	return c.Redirect(platformURLWithTab("website", id, "ssh"))
}

// ── Vhost Save ──

func (h *WebsiteHandler) ManageSaveVhost(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	content := c.FormValue("vhost_content")
	nginxCfg, err := h.nginxSites.FindByWebsite(id)
	if err != nil || nginxCfg.ConfigPath == "" {
		h.base.Sessions.SetFlash(c, "No nginx config found for this website")
		return c.Redirect(platformURLWithTab("website", id, "vhost"))
	}
	if err := os.WriteFile(nginxCfg.ConfigPath, []byte(content), 0o644); err != nil {
		h.base.Sessions.SetFlash(c, "Failed to save: "+err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Vhost config saved")
	}
	return c.Redirect(platformURLWithTab("website", id, "vhost"))
}

func domainsFromWebsite(items []models.WebsiteDomain) []string {
	out := make([]string, 0, len(items))
	for _, d := range items {
		out = append(out, d.Domain)
	}
	return out
}

func websiteDBItems(item *models.Website, all []models.DatabaseConnection) []models.DatabaseConnection {
	if item == nil {
		return nil
	}
	out := make([]models.DatabaseConnection, 0, len(all))
	prefix := strings.ToLower(item.Name)
	for _, db := range all {
		if db.WebsiteID != nil {
			if *db.WebsiteID == item.ID {
				out = append(out, db)
			}
			continue
		}
		if db.GoAppID != nil {
			continue
		}
		label := strings.ToLower(db.Label)
		if prefix != "" && strings.Contains(label, prefix) {
			out = append(out, db)
		}
	}
	return out
}

func filterDBByEngine(items []models.DatabaseConnection, engine string) []models.DatabaseConnection {
	out := make([]models.DatabaseConnection, 0, len(items))
	for _, db := range items {
		if db.Engine == engine {
			out = append(out, db)
		}
	}
	return out
}

func websiteRedisItems(item *models.Website, all []models.RedisConnection) []models.RedisConnection {
	if item == nil {
		return nil
	}
	out := make([]models.RedisConnection, 0, len(all))
	prefix := strings.ToLower(item.Name)
	for _, r := range all {
		if r.WebsiteID != nil {
			if *r.WebsiteID == item.ID {
				out = append(out, r)
			}
			continue
		}
		if r.GoAppID != nil {
			continue
		}
		label := strings.ToLower(r.Label)
		if prefix != "" && strings.Contains(label, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func websiteSSLItems(item *models.Website, all []models.SSLCertificate) []models.SSLCertificate {
	if item == nil {
		return nil
	}
	domains := map[string]bool{}
	for _, d := range item.Domains {
		domains[strings.ToLower(strings.TrimSpace(d.Domain))] = true
	}
	out := make([]models.SSLCertificate, 0, len(all))
	for _, cert := range all {
		if domains[strings.ToLower(strings.TrimSpace(cert.Domain))] {
			out = append(out, cert)
		}
	}
	return out
}

func (h *WebsiteHandler) websiteDatabaseItem(websiteID, dbID uint) (*models.Website, *models.DatabaseConnection, error) {
	site, err := h.service.Find(websiteID)
	if err != nil {
		return nil, nil, fmt.Errorf("website not found")
	}
	items, err := h.databases.List()
	if err != nil {
		return site, nil, err
	}
	for _, item := range websiteDBItems(site, items) {
		if item.ID == dbID {
			db := item
			return site, &db, nil
		}
	}
	return site, nil, fmt.Errorf("database connection not found for this platform")
}

func (h *WebsiteHandler) websiteRedisItem(websiteID, redisID uint) (*models.Website, *models.RedisConnection, error) {
	site, err := h.service.Find(websiteID)
	if err != nil {
		return nil, nil, fmt.Errorf("website not found")
	}
	items, err := h.databaseService.ListRedis()
	if err != nil {
		return site, nil, err
	}
	for _, item := range websiteRedisItems(site, items) {
		if item.ID == redisID {
			redis := item
			return site, &redis, nil
		}
	}
	return site, nil, fmt.Errorf("redis connection not found for this platform")
}

func (h *WebsiteHandler) websiteCertificateItem(websiteID, certID uint) (*models.Website, *models.SSLCertificate, error) {
	site, err := h.service.Find(websiteID)
	if err != nil {
		return nil, nil, fmt.Errorf("website not found")
	}
	items, err := h.sslRepo.List()
	if err != nil {
		return site, nil, err
	}
	for _, item := range websiteSSLItems(site, items) {
		if item.ID == certID {
			cert := item
			return site, &cert, nil
		}
	}
	return site, nil, fmt.Errorf("certificate not found for this platform")
}

func websiteRuntimeApp(item *models.Website) *models.GoApp {
	if item == nil {
		return nil
	}
	if strings.TrimSpace(item.AppRuntime) == "" && strings.TrimSpace(item.ServiceName) == "" {
		return nil
	}
	wid := item.ID
	return &models.GoApp{
		ID:               item.ID,
		Name:             item.Name,
		Runtime:          item.AppRuntime,
		ExecutionMode:    item.ExecutionMode,
		ProcessManager:   item.ProcessManager,
		BinaryPath:       item.BinaryPath,
		EntryPoint:       item.EntryPoint,
		WorkingDirectory: item.RootPath,
		Host:             item.Host,
		Port:             item.Port,
		StartArgs:        item.StartArgs,
		HealthPath:       item.HealthPath,
		RestartPolicy:    item.RestartPolicy,
		Workers:          item.Workers,
		WorkerClass:      item.WorkerClass,
		MaxMemory:        item.MaxMemory,
		Timeout:          item.Timeout,
		ExecMode:         item.ExecMode,
		StdoutLogPath:    item.StdoutLogPath,
		StderrLogPath:    item.StderrLogPath,
		ServiceName:      item.ServiceName,
		WebsiteID:        &wid,
		Enabled:          item.Enabled,
	}
}

func (h *WebsiteHandler) payload(c *fiber.Ctx) (websiteFormInput, error) {
	siteType := strings.TrimSpace(c.FormValue("type"))
	if siteType == "" {
		siteType = "static"
	}

	domains := utils.SplitLinesComma(c.FormValue("domains"))
	if len(domains) == 0 {
		applicationDomain := strings.TrimSpace(strings.ToLower(c.FormValue("application_domain")))
		if applicationDomain != "" {
			domains = []string{applicationDomain}
		}
	}
	if len(domains) == 0 {
		primaryDomain := strings.TrimSpace(strings.ToLower(c.FormValue("primary_domain")))
		if primaryDomain != "" {
			domains = []string{primaryDomain}
		}
	}

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" && len(domains) > 0 {
		name = websiteNameFromDomain(domains[0])
	}
	if name == "" {
		return websiteFormInput{}, fmt.Errorf("name is required")
	}

	root := strings.TrimSpace(c.FormValue("root_path"))
	if root == "" {
		folder := name
		if len(domains) > 0 {
			folder = strings.ToLower(strings.TrimSpace(domains[0]))
			folder = strings.ReplaceAll(folder, "*.", "wildcard-")
			folder = strings.ReplaceAll(folder, "/", "-")
		}
		root = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + folder + "/htdocs"
	}
	if err := validators.ValidatePath(root); err != nil {
		return websiteFormInput{}, err
	}

	var siteUserID *uint
	if v := c.FormValue("site_user_id"); v != "" {
		if id, err := repositories.ParseID(v); err == nil {
			siteUserID = &id
		}
	}

	phpVersion := strings.TrimSpace(c.FormValue("php_version"))
	proxyTarget := strings.TrimSpace(c.FormValue("proxy_target"))
	if siteType != "php" {
		phpVersion = ""
	}
	if siteType != "proxy" {
		proxyTarget = ""
	}

	enabled := true
	if c.FormValue("enabled") != "" {
		enabled = boolFromForm(c, "enabled")
	}

	return websiteFormInput{
		Website: services.WebsiteInput{
			Name:             name,
			RootPath:         root,
			Type:             siteType,
			PHPVersion:       phpVersion,
			ProxyTarget:      proxyTarget,
			Domains:          domains,
			CustomDirectives: c.FormValue("custom_directives"),
			SiteUserID:       siteUserID,
			Enabled:          enabled,
		},
		SiteUsername: strings.TrimSpace(c.FormValue("site_username")),
		SitePassword: c.FormValue("site_password"),
	}, nil
}

func (h *WebsiteHandler) ensureSiteUser(
	ctx context.Context,
	in *services.WebsiteInput,
	form websiteFormInput,
	actor *uint,
	ip string,
) (uint, string, error) {
	if in.SiteUserID != nil {
		return 0, "", nil
	}
	if form.SiteUsername == "" {
		return 0, "", fmt.Errorf("site username is required")
	}
	if strings.TrimSpace(form.SitePassword) != "" && len(form.SitePassword) < 10 {
		return 0, "", fmt.Errorf("site password must be at least 10 characters or leave blank to auto-generate")
	}

	item, generatedPassword, err := h.siteUserService.Create(ctx, services.SiteUserInput{
		Username:      form.SiteUsername,
		HomeDirectory: platformHomeFromRoot(in.RootPath),
		AllowedRoot:   platformHomeFromRoot(in.RootPath),
		Password:      form.SitePassword,
		SSHEnabled:    true,
	}, actor, ip)
	if err != nil {
		return 0, "", err
	}
	in.SiteUserID = &item.ID
	if strings.TrimSpace(form.SitePassword) == "" {
		return item.ID, generatedPassword, nil
	}
	return item.ID, "", nil
}

// platformHomeFromRoot returns the platform home directory from a web root path.
// If the root ends with /htdocs, the platform home is the parent directory.
// Otherwise the root itself is the platform home.
func platformHomeFromRoot(webRoot string) string {
	clean := filepath.Clean(strings.TrimSpace(webRoot))
	if filepath.Base(clean) == "htdocs" {
		return filepath.Dir(clean)
	}
	return clean
}

func websiteNameFromDomain(domain string) string {
	raw := strings.ToLower(strings.TrimSpace(domain))
	raw = strings.TrimPrefix(raw, "*.")
	raw = strings.TrimPrefix(raw, "www.")

	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if (r == '.' || r == '-') && b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "website"
	}
	if len(name) > 110 {
		name = strings.Trim(name[:110], "-")
	}
	if name == "" {
		return "website"
	}
	return name
}

func managedDatabaseTarget(engine string) (string, int, error) {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "", "mariadb":
		return "127.0.0.1", 3306, nil
	case "postgres":
		return "127.0.0.1", 5432, nil
	default:
		return "", 0, fmt.Errorf("unsupported database engine")
	}
}

func (h *WebsiteHandler) phpVersions() []string {
	return h.runtimeVersions("php")
}

func (h *WebsiteHandler) runtimeVersions(runtime string) []string {
	versions := h.settings.RuntimeVersions(runtime)
	if len(versions) > 0 {
		return versions
	}
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "go":
		return []string{"go1.25", "go1.24", "go1.23", "go1.22", "go1.21"}
	case "python":
		return []string{"python3.13", "python3.12", "python3.11", "python3.10", "python3.9"}
	case "node":
		return []string{"node24", "node22", "node20", "node18"}
	case "php":
		return []string{"8.4", "8.3", "8.2", "8.1", "8.0", "7.4"}
	default:
		return []string{}
	}
}

// LogFiles returns available log files for a website (JSON).
func (h *WebsiteHandler) LogFiles(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	files, err := h.service.ListLogFiles(id)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"files": files})
}

// LogContent returns the last N lines of a specific log file (JSON).
func (h *WebsiteHandler) LogContent(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	filename := c.Query("file")
	if filename == "" {
		return c.Status(400).JSON(fiber.Map{"error": "file parameter required"})
	}
	lines, _ := strconv.Atoi(c.Query("lines", "100"))
	if lines <= 0 || lines > 5000 {
		lines = 100
	}
	content, err := h.service.ReadLogFile(id, filename, lines)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"content": content, "file": filename, "lines": lines})
}

func (h *WebsiteHandler) ManageSavePhpSettings(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString("Invalid ID")
	}
	phpVersion := strings.TrimSpace(c.FormValue("php_version"))
	data := services.PhpSettingsData{
		MemoryLimit:          c.FormValue("memory_limit", "256M"),
		MaxExecutionTime:     c.FormValue("max_execution_time", "60"),
		MaxInputTime:         c.FormValue("max_input_time", "60"),
		MaxInputVars:         c.FormValue("max_input_vars", "1000"),
		PostMaxSize:          c.FormValue("post_max_size", "64M"),
		UploadMaxFilesize:    c.FormValue("upload_max_filesize", "64M"),
		AdditionalDirectives: c.FormValue("additional_directives", ""),
	}
	if err := h.service.UpdatePhpSettings(id, phpVersion, data); err != nil {
		return c.Redirect(platformURLWithTab("website", id, "settings"))
	}
	return c.Redirect(platformURLWithTab("website", id, "settings"))
}

func (h *WebsiteHandler) ManageCreateLinkedApp(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString("Invalid ID")
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	if site.Type != "proxy" {
		h.base.Sessions.SetFlash(c, "Runtime settings only apply to proxy sites")
		return c.Redirect(platformURLWithTab("website", id, "settings"))
	}
	if websiteRuntimeApp(site) != nil {
		h.base.Sessions.SetFlash(c, "Application already linked")
		return c.Redirect(platformURLWithTab("website", id, "settings"))
	}

	runtime := strings.TrimSpace(c.FormValue("runtime"))
	if runtime == "" {
		runtime = "binary"
	}
	processManager := strings.TrimSpace(c.FormValue("process_manager"))
	if processManager == "" {
		processManager = "systemd"
	}
	binaryPath := strings.TrimSpace(c.FormValue("binary_path"))
	entryPoint := strings.TrimSpace(c.FormValue("entry_point"))
	workdir := strings.TrimSpace(c.FormValue("working_directory"))
	if workdir == "" {
		workdir = site.RootPath
	}

	port := 0
	if site.ProxyTarget != "" {
		if u, uerr := url.Parse(site.ProxyTarget); uerr == nil {
			if p := u.Port(); p != "" {
				port, _ = strconv.Atoi(p)
			}
		}
	}
	if v := strings.TrimSpace(c.FormValue("port")); v != "" {
		port, _ = strconv.Atoi(v)
	}

	execMode := "compiled"
	if runtime == "python" || runtime == "node" {
		execMode = "interpreted"
	}

	if binaryPath == "" {
		switch processManager {
		case "gunicorn":
			binaryPath = "gunicorn"
		case "uwsgi":
			binaryPath = "uwsgi"
		case "pm2":
			binaryPath = "pm2"
		default:
			switch runtime {
			case "python":
				binaryPath = "python3"
			case "node":
				binaryPath = "node"
			}
		}
	}
	if entryPoint == "" && execMode == "interpreted" {
		switch runtime {
		case "python":
			if processManager == "gunicorn" || processManager == "uwsgi" {
				entryPoint = "app:app"
			} else {
				entryPoint = "app.py"
			}
		case "node":
			entryPoint = "index.js"
		}
	}

	workers, _ := strconv.Atoi(c.FormValue("workers"))
	timeout, _ := strconv.Atoi(c.FormValue("timeout"))

	_, err = h.appService.Create(c.Context(), services.AppInput{
		Name:             site.Name,
		Runtime:          runtime,
		ExecutionMode:    execMode,
		ProcessManager:   processManager,
		BinaryPath:       binaryPath,
		EntryPoint:       entryPoint,
		WorkingDirectory: workdir,
		Host:             "127.0.0.1",
		Port:             port,
		HealthPath:       "/health",
		RestartPolicy:    strings.TrimSpace(c.FormValue("restart_policy")),
		Workers:          workers,
		WorkerClass:      strings.TrimSpace(c.FormValue("worker_class")),
		MaxMemory:        strings.TrimSpace(c.FormValue("max_memory")),
		Timeout:          timeout,
		ExecMode:         strings.TrimSpace(c.FormValue("exec_mode")),
		WebsiteID:        &site.ID,
		Enabled:          true,
		Env:              map[string]string{},
	}, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		if site.AppRuntime != runtime {
			site.AppRuntime = runtime
			_ = h.service.UpdateAppRuntime(id, runtime)
		}
		h.base.Sessions.SetFlash(c, "Runtime configuration created")
	}
	return c.Redirect(platformURLWithTab("website", id, "settings"))
}

func (h *WebsiteHandler) ManageDeleteLinkedApp(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString("Invalid ID")
	}
	site, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("website not found")
	}
	if websiteRuntimeApp(site) == nil {
		h.base.Sessions.SetFlash(c, "No linked runtime to delete")
		return c.Redirect(platformURLWithTab("website", id, "settings"))
	}
	if err := h.service.RemoveRuntime(c.Context(), id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Runtime & service deleted")
	}
	return c.Redirect(platformURLWithTab("website", id, "settings"))
}
