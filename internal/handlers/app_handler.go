package handlers

import (
	"fmt"
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

type AppHandler struct {
	base            *BaseHandler
	service         *services.AppService
	websiteService  *services.WebsiteService
	settings        *services.SettingsService
	siteUserService *services.SiteUserService
	siteUsers       *repositories.SiteUserRepository
	databaseService *services.DatabaseService
	sslService      *services.SSLService
	databases       *repositories.DatabaseConnectionRepository
	ftpUsers        *repositories.FTPUserRepository
	ftpService      *services.FTPService
}

func NewAppHandler(
	cfg *config.Config,
	sessions *middleware.SessionManager,
	service *services.AppService,
	websiteService *services.WebsiteService,
	settings *services.SettingsService,
	siteUserService *services.SiteUserService,
	siteUsers *repositories.SiteUserRepository,
	databaseService *services.DatabaseService,
	sslService *services.SSLService,
	databases *repositories.DatabaseConnectionRepository,
	ftpUsers *repositories.FTPUserRepository,
	ftpService *services.FTPService,
) *AppHandler {
	return &AppHandler{
		base:            &BaseHandler{Config: cfg, Sessions: sessions},
		service:         service,
		websiteService:  websiteService,
		settings:        settings,
		siteUserService: siteUserService,
		siteUsers:       siteUsers,
		databaseService: databaseService,
		sslService:      sslService,
		databases:       databases,
		ftpUsers:        ftpUsers,
		ftpService:      ftpService,
	}
}

func (h *AppHandler) SitesApps(c *fiber.Ctx) error {
	websitesAll, err := h.websiteService.List()
	if err != nil {
		h.base.Sessions.SetFlash(c, "Failed to load websites: "+err.Error())
	}
	apps, err := h.service.List()
	if err != nil {
		h.base.Sessions.SetFlash(c, "Failed to load apps: "+err.Error())
	}
	if authUserRole(c) == "user" {
		allowed := allowedPlatformIDSet(c)
		filteredWebsites := make([]models.Website, 0, len(websitesAll))
		for _, site := range websitesAll {
			if _, ok := allowed[site.ID]; ok {
				filteredWebsites = append(filteredWebsites, site)
			}
		}
		websitesAll = filteredWebsites

		filteredApps := make([]models.GoApp, 0, len(apps))
		for _, app := range apps {
			if _, ok := allowed[app.ID]; ok {
				filteredApps = append(filteredApps, app)
			}
		}
		apps = filteredApps
	}

	// Hide website rows that are already represented in the runtime apps list.
	linkedWebsiteIDs := make(map[uint]struct{}, len(apps))
	for _, app := range apps {
		if app.WebsiteID != nil {
			linkedWebsiteIDs[*app.WebsiteID] = struct{}{}
		}
	}
	websiteByID := make(map[uint]models.Website, len(websitesAll))
	for _, site := range websitesAll {
		websiteByID[site.ID] = site
	}
	for i := range apps {
		if apps[i].WebsiteID == nil {
			continue
		}
		if site, ok := websiteByID[*apps[i].WebsiteID]; ok {
			siteCopy := site
			apps[i].Website = &siteCopy
		}
	}
	websites := make([]models.Website, 0, len(websitesAll))
	for _, site := range websitesAll {
		if _, exists := linkedWebsiteIDs[site.ID]; exists {
			continue
		}
		websites = append(websites, site)
	}

	view := fiber.Map{
		"Title":    "Platforms",
		"Websites": websites,
		"Apps":     apps,
	}
	if authUserRole(c) != "user" {
		view["PageActionHref"] = "/platforms/new"
		view["PageActionLabel"] = "Create Platform"
	}
	return h.base.Render(c, "platforms_index", view)
}

func (h *AppHandler) SitesAppsNew(c *fiber.Ctx) error {
	phpVersions := h.runtimeVersions("php")
	goVersions := h.runtimeVersions("go")
	pythonVersions := h.runtimeVersions("python")
	nodeVersions := h.runtimeVersions("node")
	return h.base.Render(c, "platforms_new", fiber.Map{
		"Title":           "Create Platform",
		"DefaultSiteRoot": strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/"),
		"IsDryRun":        h.base.Config.Features.PlatformMode == "dryrun",
		"PHPVersions":     phpVersions,
		"GoVersions":      goVersions,
		"PythonVersions":  pythonVersions,
		"NodeVersions":    nodeVersions,
	})
}

func (h *AppHandler) SitesAppsCreate(c *fiber.Ctx) error {
	kind := strings.TrimSpace(c.FormValue("create_kind"))
	category := strings.ToLower(strings.TrimSpace(c.FormValue("platform_category")))
	domain := strings.TrimSpace(strings.ToLower(c.FormValue("application_domain")))
	if domain == "" {
		h.base.Sessions.SetFlash(c, "application domain is required")
		return c.Redirect("/platforms/new")
	}
	expectedCategory := platformCategoryFromKind(kind)
	if expectedCategory == "" {
		h.base.Sessions.SetFlash(c, "select a valid platform tile")
		return c.Redirect("/platforms/new")
	}
	if category == "" {
		category = expectedCategory
	}
	if category != expectedCategory {
		h.base.Sessions.SetFlash(c, "selected category does not match the chosen stack")
		return c.Redirect("/platforms/new")
	}
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		name = appNameFromDomain(domain)
	}
	siteUsername := strings.TrimSpace(c.FormValue("site_username"))
	sitePassword := c.FormValue("site_password")

	switch kind {
	case "static", "php":
		root := strings.TrimSpace(c.FormValue("root_path"))
		if root == "" {
			root = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + strings.ReplaceAll(domain, "*.", "wildcard-") + "/htdocs"
		}
		var siteUserID *uint
		if siteUsername != "" {
			platformHome := platformHomeFromRoot(root)
			user, generatedPassword, err := h.siteUserService.Create(c.Context(), services.SiteUserInput{
				Username:      siteUsername,
				HomeDirectory: platformHome,
				AllowedRoot:   platformHome,
				Password:      sitePassword,
				SSHEnabled:    true,
			}, currentUserID(c), c.IP())
			if err != nil {
				h.base.Sessions.SetFlash(c, err.Error())
				return c.Redirect("/platforms/new")
			}
			siteUserID = &user.ID
			if strings.TrimSpace(sitePassword) == "" {
				h.base.Sessions.SetFlash(c, "Site user created. Generated password: "+generatedPassword)
			}
		}
		in := services.WebsiteInput{
			Name:             name,
			RootPath:         root,
			Type:             kind,
			PHPVersion:       strings.TrimSpace(c.FormValue("php_version")),
			Domains:          []string{domain},
			CustomDirectives: "",
			MaintenanceBypassIPs: "",
			SiteUserID:       siteUserID,
			Enabled:          true,
		}
		if kind == "php" && strings.TrimSpace(in.PHPVersion) == "" {
			in.PHPVersion = "8.2"
		}
		site, err := h.websiteService.Create(c.Context(), in, currentUserID(c), c.IP())
		if err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/platforms/new")
		}
		h.base.Sessions.SetFlash(c, "Platform created")
		return c.Redirect(platformURL("website", site.ID))
	case "go", "python", "node", "binary":
		port, err := validators.ValidatePort(c.FormValue("port"))
		if err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/platforms/new")
		}
		root := strings.TrimSpace(c.FormValue("root_path"))
		if root == "" {
			root = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + strings.ReplaceAll(domain, "*.", "wildcard-") + "/htdocs"
		}
		var siteUserID *uint
		if siteUsername != "" {
			platformHome := platformHomeFromRoot(root)
			user, generatedPassword, err := h.siteUserService.Create(c.Context(), services.SiteUserInput{
				Username:      siteUsername,
				HomeDirectory: platformHome,
				AllowedRoot:   platformHome,
				Password:      sitePassword,
				SSHEnabled:    true,
			}, currentUserID(c), c.IP())
			if err != nil {
				h.base.Sessions.SetFlash(c, err.Error())
				return c.Redirect("/platforms/new")
			}
			siteUserID = &user.ID
			if strings.TrimSpace(sitePassword) == "" {
				h.base.Sessions.SetFlash(c, "Site user created. Generated password: "+generatedPassword)
			}
		}
		siteInput := services.WebsiteInput{
			Name:             name,
			RootPath:         root,
			Type:             "proxy",
			AppRuntime:       kind,
			ProxyTarget:      fmt.Sprintf("http://127.0.0.1:%d", port),
			Domains:          []string{domain},
			CustomDirectives: "",
			MaintenanceBypassIPs: "",
			SiteUserID:       siteUserID,
			Enabled:          true,
		}
		site, err := h.websiteService.Create(c.Context(), siteInput, currentUserID(c), c.IP())
		if err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/platforms/new")
		}
		execMode := "compiled"
		entryPoint := ""
		startArgs := ""
		processManager := "systemd"
		binaryPath := ""
		if kind == "python" || kind == "node" {
			execMode = "interpreted"
		}
		if binaryPath == "" {
			switch kind {
			case "go":
				binaryPath = "/usr/bin/env"
			case "python":
				binaryPath = "/usr/bin/env"
			case "node":
				binaryPath = "/usr/bin/env"
			case "binary":
				binaryPath = "/usr/bin/env"
			}
		}
		if entryPoint == "" && execMode == "interpreted" {
			switch kind {
			case "python":
				entryPoint = "app.py"
			case "node":
				entryPoint = "index.js"
			}
		}
		envMap := map[string]string{}
		if v := strings.TrimSpace(c.FormValue("runtime_version")); v != "" {
			envMap["RUNTIME_VERSION"] = v
		}
		_, err = h.service.Create(c.Context(), services.AppInput{
			Name:             name,
			Runtime:          kind,
			ExecutionMode:    execMode,
			ProcessManager:   processManager,
			BinaryPath:       binaryPath,
			EntryPoint:       entryPoint,
			WorkingDirectory: root,
			Host:             "127.0.0.1",
			Port:             port,
			StartArgs:        startArgs,
			HealthPath:       "/health",
			RestartPolicy:    "on-failure",
			WebsiteID:        &site.ID,
			Enabled:          true,
			Env:              envMap,
		}, currentUserID(c), c.IP())
		if err != nil {
			_ = h.websiteService.Delete(c.Context(), site.ID, currentUserID(c), c.IP())
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/platforms/new")
		}
		h.base.Sessions.SetFlash(c, "Platform created")
		return c.Redirect(platformURL("website", site.ID))
	default:
		h.base.Sessions.SetFlash(c, "select a valid platform tile")
		return c.Redirect("/platforms/new")
	}
}

func (h *AppHandler) Create(c *fiber.Ctx) error {
	payload, err := h.payload(c)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/platforms/new")
	}
	item, err := h.service.Create(c.Context(), payload, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/platforms/new")
	}
	h.base.Sessions.SetFlash(c, "Platform created")
	if err := h.createDatabaseFromScopedForm(c, item.Name, item.ID); err != nil {
		h.base.Sessions.SetFlash(c, "Platform created. DB setup warning: "+err.Error())
	}
	return c.Redirect(platformURL("app", item.ID))
}

func (h *AppHandler) Show(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	return h.ShowByID(c, id)
}

func (h *AppHandler) ShowByID(c *fiber.Ctx, id uint) error {
	status, err := h.service.Status(c.Context(), id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	if status.App != nil && status.App.WebsiteID != nil {
		if _, err := h.websiteService.Find(*status.App.WebsiteID); err == nil {
			return c.Redirect(platformURL("website", *status.App.WebsiteID))
		}
	}
	stdout, stderr, _ := h.service.Logs(id, 100)
	dbItems, _ := h.databases.List()
	scopedDB := appDBItems(status.App.ID, status.App.Name, dbItems)
	redisItems, _ := h.databaseService.ListRedis()
	scopedRedis := appRedisItems(status.App.ID, status.App.Name, redisItems)

	var ftpUsers []models.FTPUser
	var sshUsers []models.SiteUser
	primarySSHUserID := uint(0)
	defaultHomeDir := ""
	primaryDomain := ""
	if status.App.WebsiteID != nil {
		ws, _ := h.websiteService.Find(*status.App.WebsiteID)
		if ws != nil {
			ftpUsers, _ = h.ftpUsers.ListByWebsite(ws.ID)
			defaultHomeDir = ws.RootPath
			primaryDomain = primaryWebsiteDomain(ws.Domains)
			if ws.SiteUser != nil {
				sshUsers = append(sshUsers, *ws.SiteUser)
				primarySSHUserID = ws.SiteUser.ID
			}
			additionalSSH, _ := h.siteUsers.ListByWebsite(ws.ID)
			for _, u := range additionalSSH {
				if u.ID != primarySSHUserID {
					sshUsers = append(sshUsers, u)
				}
			}
		}
	}
	if defaultHomeDir == "" && status.App.WorkingDirectory != "" {
		defaultHomeDir = status.App.WorkingDirectory
	}
	serverAddress := strings.TrimSpace(status.App.Host)
	switch serverAddress {
	case "", "0.0.0.0", "::":
		serverAddress = displayServerAddress(h.base.Config, c.Hostname())
	}

	return h.base.Render(c, "platforms_show", fiber.Map{
		"Title":            status.App.Name,
		"PlatformKind":     "app",
		"Status":           status,
		"RuntimeVersion":   envVarValue(status.App.EnvVars, "RUNTIME_VERSION"),
		"GoVersions":       h.runtimeVersions("go"),
		"NodeVersions":     h.runtimeVersions("node"),
		"PythonVersions":   h.runtimeVersions("python"),
		"PHPVersions":      h.runtimeVersions("php"),
		"PrimaryDomain":    primaryDomain,
		"ServerAddress":    serverAddress,
		"StdoutLog":        stdout,
		"StderrLog":        stderr,
		"MariaDBItems":     filterAppDBByEngine(scopedDB, "mariadb"),
		"PostgresItems":    filterAppDBByEngine(scopedDB, "postgres"),
		"RedisItems":       scopedRedis,
		"AdminerURL":       h.databaseService.AdminerURL(),
		"SSHUsers":         sshUsers,
		"PrimarySSHUserID": primarySSHUserID,
		"FTPUsers":         ftpUsers,
		"DefaultHomeDir":   defaultHomeDir,
	})
}

func (h *AppHandler) Update(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	payload, err := h.payload(c)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURL("app", id))
	}
	if err := h.service.Update(c.Context(), id, payload, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURL("app", id))
	}
	h.base.Sessions.SetFlash(c, "Platform updated")
	return c.Redirect(platformURL("app", id))
}

func (h *AppHandler) Delete(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, findErr := h.service.Find(id)
	if findErr == nil && app != nil && app.WebsiteID != nil {
		if err := h.websiteService.Delete(c.Context(), *app.WebsiteID, currentUserID(c), c.IP()); err != nil {
			if fallbackErr := h.service.Delete(c.Context(), id, currentUserID(c), c.IP()); fallbackErr != nil {
				h.base.Sessions.SetFlash(c, err.Error())
			}
		}
		return c.Redirect("/platforms")
	}
	if err := h.service.Delete(c.Context(), id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect("/platforms")
}

func (h *AppHandler) Action(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	action := c.Params("action")
	if err := h.service.Action(c.Context(), id, action, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	if ref := c.Get("Referer"); platformKindFromReferer(ref) == utils.PlatformKindWebsite {
		return c.Redirect(ref)
	}
	return c.Redirect(platformURL("app", id))
}

func (h *AppHandler) ManageCreateDatabase(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	host, port, err := managedDatabaseTarget(strings.TrimSpace(c.FormValue("engine")))
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
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
		GoAppID:     &id,
	}
	if in.Label == "" {
		in.Label = app.Name + "-db"
	}
	if in.Environment == "" {
		in.Environment = "production"
	}
	if err := h.databaseService.CreateDatabase(in, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Database connection saved")
	}
	return c.Redirect(platformURLWithTab("app", id, "databases"))
}

func (h *AppHandler) ManageDeleteDatabase(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	dbid, err := repositories.ParseID(c.Params("dbid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, item, err := h.appDatabaseItem(id, dbid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	if err := h.databaseService.DeleteDatabase(item.ID, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", app.ID, "databases"))
	}
	h.base.Sessions.SetFlash(c, "Database connection deleted")
	return c.Redirect(platformURLWithTab("app", app.ID, "databases"))
}

func (h *AppHandler) ManageUpdateDatabasePassword(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	dbid, err := repositories.ParseID(c.Params("dbid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, _, err := h.appDatabaseItem(id, dbid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	password := c.FormValue("password")
	if err := h.databaseService.UpdateDatabasePassword(dbid, password, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Database password updated")
	}
	return c.Redirect(platformURLWithTab("app", app.ID, "databases"))
}

func (h *AppHandler) ManageAdminerDB(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	dbid, err := repositories.ParseID(c.Params("dbid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if _, _, err := h.appDatabaseItem(id, dbid); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	adminerURL, err := h.databaseService.AdminerDBURL(dbid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	return c.Redirect(adminerURL)
}

func (h *AppHandler) LogFiles(c *fiber.Ctx) error {
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

func (h *AppHandler) LogContent(c *fiber.Ctx) error {
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

func (h *AppHandler) ManageOpenPostgresGUI(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	dbid, err := repositories.ParseID(c.Params("dbid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_, item, err := h.appDatabaseItem(id, dbid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	url, err := h.databaseService.PostgresGUIURL(item.ID)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	return c.Redirect(url)
}

func (h *AppHandler) runtimeVersions(runtime string) []string {
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

func (h *AppHandler) createDatabaseFromScopedForm(c *fiber.Ctx, fallbackLabel string, appID uint) error {
	database := strings.TrimSpace(c.FormValue("db_database"))
	if database == "" {
		return nil
	}
	gid := appID
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
		GoAppID:     &gid,
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

func (h *AppHandler) ManageCreateSiteUser(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	workdir := strings.TrimSpace(app.WorkingDirectory)
	if workdir == "" {
		workdir = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + app.Name
	}
	userHome := workdir
	if app.WebsiteID != nil {
		userHome = platformHomeFromRoot(workdir)
	}
	var websiteID *uint
	if app.WebsiteID != nil {
		websiteID = app.WebsiteID
	}
	_, generatedPassword, err := h.siteUserService.Create(c.Context(), services.SiteUserInput{
		Username:      strings.TrimSpace(c.FormValue("username")),
		HomeDirectory: userHome,
		AllowedRoot:   userHome,
		Password:      c.FormValue("password"),
		SSHEnabled:    true,
		WebsiteID:     websiteID,
	}, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else if strings.TrimSpace(c.FormValue("password")) == "" {
		h.base.Sessions.SetFlash(c, "SSH user created. Generated password: "+generatedPassword)
	} else {
		h.base.Sessions.SetFlash(c, "SSH user created")
	}
	if app.WebsiteID != nil && h.websiteService != nil {
		_ = h.websiteService.RefreshConfig(c.Context(), *app.WebsiteID)
	}
	return c.Redirect(platformURLWithTab("app", id, "ssh"))
}

func (h *AppHandler) ManageResetSiteUserPassword(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	if app.WebsiteID == nil {
		h.base.Sessions.SetFlash(c, "platform has no linked SSH user")
		return c.Redirect(platformURLWithTab("app", id, "ssh"))
	}
	site, err := h.websiteService.Find(*app.WebsiteID)
	if err != nil {
		return c.Status(404).SendString("linked website not found")
	}
	targetID := uint(0)
	if uidRaw := strings.TrimSpace(c.Params("uid")); uidRaw != "" {
		uid, parseErr := repositories.ParseID(uidRaw)
		if parseErr != nil {
			return c.Status(400).SendString(parseErr.Error())
		}
		targetID = uid
	}
	if targetID == 0 && site.SiteUserID != nil {
		targetID = *site.SiteUserID
	}
	if targetID == 0 {
		h.base.Sessions.SetFlash(c, "No site user to update")
		return c.Redirect(platformURLWithTab("app", id, "ssh"))
	}
	isAllowed := site.SiteUserID != nil && *site.SiteUserID == targetID
	if !isAllowed {
		user, findErr := h.siteUsers.Find(targetID)
		if findErr != nil {
			return c.Status(404).SendString("site user not found")
		}
		isAllowed = user.WebsiteID != nil && *user.WebsiteID == *app.WebsiteID
	}
	if !isAllowed {
		return c.Status(403).SendString("site user does not belong to this platform")
	}
	password := c.FormValue("password")
	generated, err := h.siteUserService.ResetPassword(c.Context(), targetID, password, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else if strings.TrimSpace(password) == "" {
		h.base.Sessions.SetFlash(c, "Password reset. Generated password: "+generated)
	} else {
		h.base.Sessions.SetFlash(c, "Password updated")
	}
	return c.Redirect(platformURLWithTab("app", id, "ssh"))
}

func (h *AppHandler) ManageDeleteSiteUser(c *fiber.Ctx) error {
	id, _ := repositories.ParseID(c.Params("id"))
	uid, err := repositories.ParseID(c.Params("uid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	if app.WebsiteID != nil {
		ws, _ := h.websiteService.Find(*app.WebsiteID)
		if ws != nil && ws.SiteUserID != nil && *ws.SiteUserID == uid {
			h.base.Sessions.SetFlash(c, "Cannot delete the primary SSH user")
			return c.Redirect(platformURLWithTab("app", id, "ssh"))
		}
	}
	if err := h.siteUserService.Delete(c.Context(), uid, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "SSH user deleted")
	}
	if app.WebsiteID != nil && h.websiteService != nil {
		_ = h.websiteService.RefreshConfig(c.Context(), *app.WebsiteID)
	}
	return c.Redirect(platformURLWithTab("app", id, "ssh"))
}

func (h *AppHandler) ManageCreateFTPUser(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	if app.WebsiteID == nil {
		h.base.Sessions.SetFlash(c, "platform has no linked web runtime")
		return c.Redirect(platformURLWithTab("app", id, "ssh"))
	}
	username := strings.TrimSpace(c.FormValue("username"))
	if username == "" {
		h.base.Sessions.SetFlash(c, "username is required")
		return c.Redirect(platformURLWithTab("app", id, "ssh"))
	}
	password := strings.TrimSpace(c.FormValue("password"))
	generated := password == ""
	if password == "" {
		password = utils.GeneratePassword()
	}
	homeDir := strings.TrimSpace(c.FormValue("home_dir"))
	if homeDir == "" {
		homeDir = app.WorkingDirectory
		if homeDir == "" {
			homeDir = strings.TrimSuffix(h.base.Config.Paths.DefaultSiteRoot, "/") + "/" + app.Name
		}
		if app.WebsiteID != nil {
			homeDir = platformHomeFromRoot(homeDir)
		}
	}
	item := &models.FTPUser{WebsiteID: *app.WebsiteID, Username: username, Password: password, HomeDir: homeDir}
	if h.ftpService != nil {
		if err := h.ftpService.Create(c.Context(), item, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect(platformURLWithTab("app", id, "ssh"))
		}
	} else if err := h.ftpUsers.Create(item); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "FTP user created")
		return c.Redirect(platformURLWithTab("app", id, "ssh"))
	}
	if generated {
		h.base.Sessions.SetFlash(c, "FTP user created. Generated password: "+password)
	} else {
		h.base.Sessions.SetFlash(c, "FTP user created")
	}
	if app.WebsiteID != nil && h.websiteService != nil {
		_ = h.websiteService.RefreshConfig(c.Context(), *app.WebsiteID)
	}
	return c.Redirect(platformURLWithTab("app", id, "ssh"))
}

func (h *AppHandler) ManageResetFTPPassword(c *fiber.Ctx) error {
	id, _ := repositories.ParseID(c.Params("id"))
	fid, err := repositories.ParseID(c.Params("fid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	password := c.FormValue("password")
	if h.ftpService != nil {
		generated, resetErr := h.ftpService.ResetPassword(c.Context(), fid, password, currentUserID(c), c.IP())
		if resetErr != nil {
			h.base.Sessions.SetFlash(c, resetErr.Error())
		} else if strings.TrimSpace(password) == "" {
			h.base.Sessions.SetFlash(c, "FTP password reset. Generated password: "+generated)
		} else {
			h.base.Sessions.SetFlash(c, "FTP password updated")
		}
	} else {
		h.base.Sessions.SetFlash(c, "FTP service not available")
	}
	return c.Redirect(platformURLWithTab("app", id, "ssh"))
}

func (h *AppHandler) ManageDeleteFTPUser(c *fiber.Ctx) error {
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
	if app, findErr := h.service.Find(id); findErr == nil && app.WebsiteID != nil && h.websiteService != nil {
		_ = h.websiteService.RefreshConfig(c.Context(), *app.WebsiteID)
	}
	return c.Redirect(platformURLWithTab("app", id, "ssh"))
}

func (h *AppHandler) ManageUpdateRuntime(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	workers, _ := strconv.Atoi(c.FormValue("workers", "0"))
	timeout, _ := strconv.Atoi(c.FormValue("timeout", "0"))
	processManager := strings.TrimSpace(c.FormValue("process_manager"))
	workerClass := strings.TrimSpace(c.FormValue("worker_class"))
	maxMemory := strings.TrimSpace(c.FormValue("max_memory"))
	execMode := strings.TrimSpace(c.FormValue("exec_mode"))
	restartPolicy := strings.TrimSpace(c.FormValue("restart_policy"))
	runtimeVersion := strings.TrimSpace(c.FormValue("runtime_version"))
	runtimeApply := strings.TrimSpace(c.FormValue("runtime_apply"))
	if runtimeApply == "" {
		runtimeApply = "reconfigure"
	}
	port := 0
	if p := strings.TrimSpace(c.FormValue("port")); p != "" {
		parsedPort, portErr := validators.ValidatePort(p)
		if portErr != nil {
			h.base.Sessions.SetFlash(c, portErr.Error())
			if ref := c.Get("Referer"); platformKindFromReferer(ref) == utils.PlatformKindWebsite {
				return c.Redirect(ref)
			}
			return c.Redirect(platformURLWithTab("app", id, "runtime"))
		}
		port = parsedPort
	}
	if err := h.service.UpdateRuntimeSettings(c.Context(), id, processManager, workers, workerClass, maxMemory, timeout, execMode, restartPolicy, port, runtimeVersion, runtimeApply, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else if runtimeApply == "reset" {
		h.base.Sessions.SetFlash(c, "Runtime reset with latest settings")
	} else {
		h.base.Sessions.SetFlash(c, "Runtime reconfigured with latest settings")
	}
	if ref := c.Get("Referer"); platformKindFromReferer(ref) == utils.PlatformKindWebsite {
		return c.Redirect(ref)
	}
	return c.Redirect(platformURLWithTab("app", id, "runtime"))
}

func (h *AppHandler) ManageSSLLetsEncrypt(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	domain := strings.TrimSpace(c.FormValue("domain"))
	if domain == "" {
		h.base.Sessions.SetFlash(c, "domain is required")
		return c.Redirect(platformURLWithTab("app", id, "ssl"))
	}
	websiteID := id
	if app.WebsiteID != nil && *app.WebsiteID > 0 {
		websiteID = *app.WebsiteID
	}
	site, _ := h.websiteService.Find(websiteID)
	if site == nil {
		h.base.Sessions.SetFlash(c, "linked website not found")
		return c.Redirect(platformURLWithTab("app", id, "ssl"))
	}
	if err := h.sslService.CreateForWebsite(c.Context(), site, domain, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, friendlySSLIssueMessage(domain, err))
	} else {
		_ = h.websiteService.RefreshConfig(c.Context(), websiteID)
		h.base.Sessions.SetFlash(c, "Let's Encrypt certificate requested for "+domain)
	}
	return c.Redirect(platformURLWithTab("app", id, "ssl"))
}

func (h *AppHandler) ManageSSLImport(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	domain := strings.TrimSpace(c.FormValue("domain"))
	if domain == "" {
		h.base.Sessions.SetFlash(c, "domain is required")
		return c.Redirect(platformURLWithTab("app", id, "ssl"))
	}
	if err := h.sslService.CreateImport(domain, c.FormValue("private_key"), c.FormValue("certificate"), c.FormValue("certificate_chain"), currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		websiteID := id
		if app.WebsiteID != nil && *app.WebsiteID > 0 {
			websiteID = *app.WebsiteID
		}
		_ = h.websiteService.RefreshConfig(c.Context(), websiteID)
		h.base.Sessions.SetFlash(c, "Certificate imported for "+domain)
	}
	return c.Redirect(platformURLWithTab("app", id, "ssl"))
}

func (h *AppHandler) ManageSSLSelfSigned(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
	}
	domain := strings.TrimSpace(c.FormValue("domain"))
	if domain == "" {
		h.base.Sessions.SetFlash(c, "domain is required")
		return c.Redirect(platformURLWithTab("app", id, "ssl"))
	}
	if err := h.sslService.CreateSelfSigned(domain, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		websiteID := id
		if app.WebsiteID != nil && *app.WebsiteID > 0 {
			websiteID = *app.WebsiteID
		}
		_ = h.websiteService.RefreshConfig(c.Context(), websiteID)
		h.base.Sessions.SetFlash(c, "Self-signed certificate created for "+domain)
	}
	return c.Redirect(platformURLWithTab("app", id, "ssl"))
}

func (h *AppHandler) ManageCreateRedis(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("application not found")
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
		GoAppID:     &id,
	}
	if in.Label == "" {
		in.Label = app.Name + "-redis"
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
	return c.Redirect(platformURLWithTab("app", id, "databases"))
}

func (h *AppHandler) ManageRedisInfo(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	rid, err := repositories.ParseID(c.Params("rid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	_, item, err := h.appRedisItem(id, rid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	info, err := h.databaseService.RedisInfo(item.ID)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	return h.base.Render(c, "redis_info", fiber.Map{
		"Title":   "Redis Diagnostics",
		"Info":    info,
		"BackURL": platformURLWithTab("app", id, "databases"),
	})
}

func (h *AppHandler) ManageDeleteRedis(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	rid, err := repositories.ParseID(c.Params("rid"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	app, item, err := h.appRedisItem(id, rid)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", id, "databases"))
	}
	if err := h.databaseService.DeleteRedis(item.ID, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(platformURLWithTab("app", app.ID, "databases"))
	}
	h.base.Sessions.SetFlash(c, "Redis connection deleted")
	return c.Redirect(platformURLWithTab("app", app.ID, "databases"))
}

func appDBItems(appID uint, appName string, all []models.DatabaseConnection) []models.DatabaseConnection {
	out := make([]models.DatabaseConnection, 0, len(all))
	prefix := strings.ToLower(strings.TrimSpace(appName))
	for _, db := range all {
		if db.GoAppID != nil {
			if *db.GoAppID == appID {
				out = append(out, db)
			}
			continue
		}
		if db.WebsiteID != nil {
			continue
		}
		if prefix != "" && strings.Contains(strings.ToLower(db.Label), prefix) {
			out = append(out, db)
		}
	}
	return out
}

func (h *AppHandler) appDatabaseItem(appID, dbID uint) (*models.GoApp, *models.DatabaseConnection, error) {
	app, err := h.service.Find(appID)
	if err != nil {
		return nil, nil, fmt.Errorf("application not found")
	}
	items, err := h.databases.List()
	if err != nil {
		return app, nil, err
	}
	for _, item := range appDBItems(app.ID, app.Name, items) {
		if item.ID == dbID {
			db := item
			return app, &db, nil
		}
	}
	return app, nil, fmt.Errorf("database connection not found for this platform")
}

func filterAppDBByEngine(items []models.DatabaseConnection, engine string) []models.DatabaseConnection {
	out := make([]models.DatabaseConnection, 0, len(items))
	for _, db := range items {
		if db.Engine == engine {
			out = append(out, db)
		}
	}
	return out
}

func appRedisItems(appID uint, appName string, all []models.RedisConnection) []models.RedisConnection {
	out := make([]models.RedisConnection, 0, len(all))
	prefix := strings.ToLower(strings.TrimSpace(appName))
	for _, r := range all {
		if r.GoAppID != nil {
			if *r.GoAppID == appID {
				out = append(out, r)
			}
			continue
		}
		if r.WebsiteID != nil {
			continue
		}
		if prefix != "" && strings.Contains(strings.ToLower(r.Label), prefix) {
			out = append(out, r)
		}
	}
	return out
}

func (h *AppHandler) appRedisItem(appID, redisID uint) (*models.GoApp, *models.RedisConnection, error) {
	app, err := h.service.Find(appID)
	if err != nil {
		return nil, nil, fmt.Errorf("application not found")
	}
	items, err := h.databaseService.ListRedis()
	if err != nil {
		return app, nil, err
	}
	for _, item := range appRedisItems(app.ID, app.Name, items) {
		if item.ID == redisID {
			redis := item
			return app, &redis, nil
		}
	}
	return app, nil, fmt.Errorf("redis connection not found for this platform")
}

func appNameFromDomain(domain string) string {
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
		return "site"
	}
	return name
}

func platformCategoryFromKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "static", "php":
		return "site"
	case "go", "python", "node", "binary":
		return "app"
	default:
		return ""
	}
}

func (h *AppHandler) payload(c *fiber.Ctx) (services.AppInput, error) {
	port, err := validators.ValidatePort(c.FormValue("port"))
	if err != nil {
		return services.AppInput{}, err
	}
	envMap := map[string]string{}
	for _, row := range strings.Split(c.FormValue("env_vars"), "\n") {
		trim := strings.TrimSpace(row)
		if trim == "" {
			continue
		}
		parts := strings.SplitN(trim, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	var websiteID *uint
	if v := c.FormValue("website_id"); v != "" {
		if id, err := repositories.ParseID(v); err == nil {
			websiteID = &id
		}
	}
	return services.AppInput{
		Name:             strings.TrimSpace(c.FormValue("name")),
		Runtime:          strings.TrimSpace(c.FormValue("runtime")),
		ExecutionMode:    strings.TrimSpace(c.FormValue("execution_mode")),
		ProcessManager:   strings.TrimSpace(c.FormValue("process_manager")),
		BinaryPath:       strings.TrimSpace(c.FormValue("binary_path")),
		EntryPoint:       strings.TrimSpace(c.FormValue("entry_point")),
		WorkingDirectory: strings.TrimSpace(c.FormValue("working_directory")),
		Host:             strings.TrimSpace(c.FormValue("host")),
		Port:             port,
		StartArgs:        strings.TrimSpace(c.FormValue("start_args")),
		HealthPath:       strings.TrimSpace(c.FormValue("health_path")),
		RestartPolicy:    strings.TrimSpace(c.FormValue("restart_policy")),
		WebsiteID:        websiteID,
		Enabled:          boolFromForm(c, "enabled"),
		Env:              envMap,
	}, nil
}
