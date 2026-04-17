package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/models"
	"deploycp/internal/repositories"
	"deploycp/internal/services"
	"deploycp/internal/utils"
)

type settingsEventView struct {
	Time     string
	Username string
	Event    string
	Details  string
}

type settingsUserView struct {
	ID          uint
	Username    string
	Email       string
	Name        string
	Role        string
	IsActive    bool
	IsProtected bool
	PlatformIDs []uint
	PlatformCSV string
}

type settingsPlatformOption struct {
	ID      uint
	Name    string
	Domain  string
	Runtime string
	Kind    string
	Label   string
}

type SettingsHandler struct {
	base               *BaseHandler
	service            *services.SettingsService
	svcService         *services.ServiceService
	userService        *services.PanelUserService
	auditRepo          *repositories.AuditLogRepository
	firewalls          *repositories.PanelFirewallRuleRepository
	userPlatformAccess *repositories.UserPlatformAccessRepository
	websiteService     *services.WebsiteService
	appService         *services.AppService
	audit              *services.AuditService
	firewallService    *services.FirewallService
	runtimeService     *services.RuntimeService
	ftpService         *services.FTPService
	updateService      *services.UpdateService
}

func NewSettingsHandler(
	cfg *config.Config,
	sessions *middleware.SessionManager,
	service *services.SettingsService,
	svcService *services.ServiceService,
	userService *services.PanelUserService,
	auditRepo *repositories.AuditLogRepository,
	firewalls *repositories.PanelFirewallRuleRepository,
	userPlatformAccess *repositories.UserPlatformAccessRepository,
	websiteService *services.WebsiteService,
	appService *services.AppService,
	audit *services.AuditService,
	firewallService *services.FirewallService,
	runtimeService *services.RuntimeService,
	ftpService *services.FTPService,
	updateService *services.UpdateService,
) *SettingsHandler {
	return &SettingsHandler{
		base:               &BaseHandler{Config: cfg, Sessions: sessions},
		service:            service,
		svcService:         svcService,
		userService:        userService,
		auditRepo:          auditRepo,
		firewalls:          firewalls,
		userPlatformAccess: userPlatformAccess,
		websiteService:     websiteService,
		appService:         appService,
		audit:              audit,
		firewallService:    firewallService,
		runtimeService:     runtimeService,
		ftpService:         ftpService,
		updateService:      updateService,
	}
}

func (h *SettingsHandler) Index(c *fiber.Ctx) error {
	items, err := h.service.Combined()
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}

	svcItems, svcErr := h.svcService.ListSystem(c.Context())
	if svcErr != nil {
		h.base.Sessions.SetFlash(c, svcErr.Error())
	}
	total := len(svcItems)
	running := 0
	enabled := 0
	for _, item := range svcItems {
		if item.Status.Active {
			running++
		}
		if item.Record.Enabled {
			enabled++
		}
	}

	users, userErr := h.userService.List()
	if userErr != nil {
		h.base.Sessions.SetFlash(c, userErr.Error())
		users = []models.User{}
	}
	userAccess := h.userPlatformAccessMap(users)
	userRows := make([]settingsUserView, 0, len(users))
	for _, u := range users {
		role := strings.ToLower(strings.TrimSpace(u.Role))
		switch role {
		case "admin", "site_manager", "user":
		default:
			if u.IsAdmin {
				role = "admin"
			} else {
				role = "user"
			}
		}
		ids := userAccess[u.ID]
		if role != "user" {
			ids = nil
		}
		csvParts := make([]string, 0, len(ids))
		for _, id := range ids {
			csvParts = append(csvParts, strconv.FormatUint(uint64(id), 10))
		}
		userRows = append(userRows, settingsUserView{
			ID:          u.ID,
			Username:    u.Username,
			Email:       u.Email,
			Name:        u.Name,
			Role:        role,
			IsActive:    u.IsActive,
			IsProtected: h.isProtectedUsername(u.Username),
			PlatformIDs: ids,
			PlatformCSV: strings.Join(csvParts, ","),
		})
	}

	platformOptions := h.platformOptions()

	eventsPage := parsePositiveInt(c.Query("events_page"), 1)
	const eventsPerPage = 25
	events, eventsTotal := h.eventsForView(users, eventsPage, eventsPerPage)
	eventsPages := 0
	if eventsTotal > 0 {
		eventsPages = int((eventsTotal + int64(eventsPerPage) - 1) / int64(eventsPerPage))
	}
	if eventsPages == 0 {
		eventsPages = 1
	}
	if eventsPage > eventsPages {
		eventsPage = eventsPages
		events, eventsTotal = h.eventsForView(users, eventsPage, eventsPerPage)
	}
	eventsStart := 0
	eventsEnd := 0
	if eventsTotal > 0 && len(events) > 0 {
		eventsStart = (eventsPage-1)*eventsPerPage + 1
		eventsEnd = eventsStart + len(events) - 1
	}
	firewallRules, fwErr := h.firewalls.List()
	if fwErr != nil {
		h.base.Sessions.SetFlash(c, fwErr.Error())
		firewallRules = []models.PanelFirewallRule{}
	}
	firewallBackend := ""
	firewallHostActive := false
	hostFirewallRules := []models.PanelFirewallRule{}
	if h.firewallService != nil {
		backend, active, rules, err := h.firewallService.HostStatus(c.Context())
		if err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
		} else {
			firewallBackend = backend
			firewallHostActive = active
			hostFirewallRules = rules
		}
	}

	customDomain, _ := h.service.Get("panel_custom_domain")
	proftpdMasqueradeAddress, _ := h.service.Get("proftpd_masquerade_address")
	panelTimezone, _ := h.service.Get("panel_timezone")
	if panelTimezone == "" {
		panelTimezone = "UTC"
	}
	basicAuthEnabled := false
	if v, _ := h.service.Get("panel_basic_auth_enabled"); strings.EqualFold(strings.TrimSpace(v), "true") || strings.TrimSpace(v) == "1" || strings.EqualFold(strings.TrimSpace(v), "on") {
		basicAuthEnabled = true
	}
	basicAuthUsername, _ := h.service.Get("panel_basic_auth_username")

	activeTab := strings.TrimSpace(strings.ToLower(c.Query("tab")))
	switch activeTab {
	case "general", "users", "events", "services", "firewall":
	default:
		activeTab = "general"
	}

	updateView := services.UpdateView{}
	if h.updateService != nil {
		updateView = h.updateService.FooterView()
	}

	return h.base.Render(c, "settings_index", fiber.Map{
		"Title":                    "Settings",
		"Items":                    items,
		"SvcItems":                 svcItems,
		"Types":                    h.svcService.Types(),
		"PlatformName":             h.svcService.PlatformName(),
		"TotalCount":               total,
		"RunningCount":             running,
		"StoppedCount":             total - running,
		"EnabledCount":             enabled,
		"DisabledCount":            total - enabled,
		"Users":                    userRows,
		"PlatformOptions":          platformOptions,
		"Events":                   events,
		"EventsPage":               eventsPage,
		"EventsPages":              eventsPages,
		"EventsTotal":              eventsTotal,
		"EventsStart":              eventsStart,
		"EventsEnd":                eventsEnd,
		"FirewallRules":            firewallRules,
		"HostFirewallRules":        hostFirewallRules,
		"FirewallBackend":          firewallBackend,
		"FirewallHostActive":       firewallHostActive,
		"CustomDomain":             customDomain,
		"ProftpdMasqueradeAddress": proftpdMasqueradeAddress,
		"PanelTimezone":            panelTimezone,
		"SupportedTimezones":       h.service.SupportedTimezones(),
		"PanelBasicEnabled":        basicAuthEnabled,
		"PanelBasicUser":           basicAuthUsername,
		"GoVersions":               h.service.RuntimeVersions("go"),
		"NodeVersions":             h.service.RuntimeVersions("node"),
		"PythonVersions":           h.service.RuntimeVersions("python"),
		"PHPVersions":              h.service.RuntimeVersions("php"),
		"ActiveTab":                activeTab,
		"UpdateView":               updateView,
	})
}

func (h *SettingsHandler) Update(c *fiber.Ctx) error {
	key := strings.TrimSpace(c.FormValue("key"))
	value := c.FormValue("value")
	if key == "" {
		h.base.Sessions.SetFlash(c, "key is required")
		return c.Redirect("/settings?tab=general")
	}
	if err := h.service.Update(key, value, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=general")
	}
	h.base.Sessions.SetFlash(c, "setting updated")
	return c.Redirect("/settings?tab=general")
}

func (h *SettingsHandler) UpdateGeneral(c *fiber.Ctx) error {
	customDomain := strings.TrimSpace(c.FormValue("panel_custom_domain"))
	proftpdMasqueradeAddress := strings.TrimSpace(c.FormValue("proftpd_masquerade_address"))
	panelTimezone := strings.TrimSpace(c.FormValue("panel_timezone"))
	basicEnabled := boolFromForm(c, "panel_basic_auth_enabled")
	username := strings.TrimSpace(strings.ToLower(c.FormValue("panel_basic_auth_username")))
	password := strings.TrimSpace(c.FormValue("panel_basic_auth_password"))
	actor := currentUserID(c)
	ip := c.IP()

	if username == "" {
		existing, _ := h.service.Get("panel_basic_auth_username")
		username = strings.TrimSpace(strings.ToLower(existing))
	}
	if username == "" {
		username = "admin"
	}

	if err := h.service.Update("panel_custom_domain", customDomain, actor, ip); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=general")
	}
	if err := h.service.Update("proftpd_masquerade_address", proftpdMasqueradeAddress, actor, ip); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=general")
	}
	if h.ftpService != nil {
		if err := h.ftpService.ReconcileConfig(c.Context(), actor, ip); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=general")
		}
	}
	if panelTimezone != "" {
		normalizedTZ, err := h.service.NormalizeTimezone(panelTimezone)
		if err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=general")
		}
		panelTimezone = normalizedTZ
	}
	if panelTimezone == "" {
		panelTimezone = "UTC"
	}
	if err := h.service.Update("panel_timezone", panelTimezone, actor, ip); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=general")
	}
	if err := h.service.Update("panel_basic_auth_username", username, actor, ip); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=general")
	}
	if password != "" {
		hash, err := utils.HashPassword(password)
		if err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=general")
		}
		if err := h.service.Update("panel_basic_auth_password_hash", hash, actor, ip); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=general")
		}
	}

	if basicEnabled {
		hash, _ := h.service.Get("panel_basic_auth_password_hash")
		if strings.TrimSpace(hash) == "" {
			h.base.Sessions.SetFlash(c, "basic auth password is required before enabling")
			_ = h.service.Update("panel_basic_auth_enabled", "false", actor, ip)
			return c.Redirect("/settings?tab=general")
		}
		if err := h.service.Update("panel_basic_auth_enabled", "true", actor, ip); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=general")
		}
	} else {
		if err := h.service.Update("panel_basic_auth_enabled", "false", actor, ip); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=general")
		}
	}

	h.base.Sessions.SetFlash(c, "general settings updated")
	return c.Redirect("/settings?tab=general")
}

func (h *SettingsHandler) UsersCreate(c *fiber.Ctx) error {
	_, err := h.userService.Create(services.PanelUserInput{
		Username:    strings.TrimSpace(c.FormValue("username")),
		Email:       strings.TrimSpace(c.FormValue("email")),
		Name:        strings.TrimSpace(c.FormValue("name")),
		Password:    c.FormValue("password"),
		Status:      strings.TrimSpace(c.FormValue("status")),
		Role:        strings.TrimSpace(c.FormValue("role")),
		PlatformIDs: parseUintMultiFormValues(c, "platform_ids"),
	}, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=users")
	}
	h.base.Sessions.SetFlash(c, "user created")
	return c.Redirect("/settings?tab=users")
}

func (h *SettingsHandler) UsersUpdate(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		h.base.Sessions.SetFlash(c, "invalid user id")
		return c.Redirect("/settings?tab=users")
	}
	if uid := currentUserID(c); uid != nil && *uid == id {
		role := strings.ToLower(strings.TrimSpace(c.FormValue("role")))
		status := strings.ToLower(strings.TrimSpace(c.FormValue("status")))
		if role != "admin" {
			h.base.Sessions.SetFlash(c, "you cannot remove your own admin role")
			return c.Redirect("/settings?tab=users")
		}
		if status != "active" {
			h.base.Sessions.SetFlash(c, "you cannot deactivate your own account")
			return c.Redirect("/settings?tab=users")
		}
	}
	err = h.userService.Update(id, services.PanelUserInput{
		Username:    strings.TrimSpace(c.FormValue("username")),
		Email:       strings.TrimSpace(c.FormValue("email")),
		Name:        strings.TrimSpace(c.FormValue("name")),
		Password:    c.FormValue("password"),
		Status:      strings.TrimSpace(c.FormValue("status")),
		Role:        strings.TrimSpace(c.FormValue("role")),
		PlatformIDs: parseUintMultiFormValues(c, "platform_ids"),
	}, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=users")
	}
	h.base.Sessions.SetFlash(c, "user updated")
	return c.Redirect("/settings?tab=users")
}

func (h *SettingsHandler) UsersDelete(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		h.base.Sessions.SetFlash(c, "invalid user id")
		return c.Redirect("/settings?tab=users")
	}
	if uid := currentUserID(c); uid != nil && *uid == id {
		h.base.Sessions.SetFlash(c, "you cannot delete your own account")
		return c.Redirect("/settings?tab=users")
	}
	target, findErr := h.userService.Find(id)
	if findErr != nil {
		h.base.Sessions.SetFlash(c, "user not found")
		return c.Redirect("/settings?tab=users")
	}
	if h.isProtectedUsername(target.Username) {
		h.base.Sessions.SetFlash(c, "cannot delete the default bootstrap admin user")
		return c.Redirect("/settings?tab=users")
	}
	if err := h.userService.Delete(id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=users")
	}
	h.base.Sessions.SetFlash(c, "user deleted")
	return c.Redirect("/settings?tab=users")
}

func (h *SettingsHandler) RuntimeVersionAdd(c *fiber.Ctx) error {
	runtime := strings.ToLower(strings.TrimSpace(c.Params("runtime")))
	version := strings.TrimSpace(c.FormValue("version"))
	if err := h.service.AddRuntimeVersion(runtime, version, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=services")
	}
	if h.runtimeService != nil {
		if err := h.runtimeService.InstallVersion(c.Context(), runtime, version, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=services")
		}
	}
	h.base.Sessions.SetFlash(c, "version added")
	return c.Redirect("/settings?tab=services")
}

func (h *SettingsHandler) RuntimeVersionRemove(c *fiber.Ctx) error {
	runtime := strings.ToLower(strings.TrimSpace(c.Params("runtime")))
	version := strings.TrimSpace(c.FormValue("version"))
	usageCount, usageNames, usageErr := h.runtimeVersionUsage(runtime, version)
	if usageErr != nil {
		h.base.Sessions.SetFlash(c, usageErr.Error())
		return c.Redirect("/settings?tab=services")
	}
	if usageCount > 0 {
		msg := fmt.Sprintf("cannot remove %s: in use by %d platform(s)", version, usageCount)
		if len(usageNames) > 0 {
			show := usageNames
			if len(show) > 3 {
				show = show[:3]
			}
			msg = msg + " (" + strings.Join(show, ", ")
			if len(usageNames) > len(show) {
				msg += ", ...)"
			} else {
				msg += ")"
			}
		}
		h.base.Sessions.SetFlash(c, msg)
		return c.Redirect("/settings?tab=services")
	}
	if err := h.service.RemoveRuntimeVersion(runtime, version, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=services")
	}
	if h.runtimeService != nil {
		if err := h.runtimeService.RemoveVersion(c.Context(), runtime, version, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=services")
		}
	}
	h.base.Sessions.SetFlash(c, "version removed")
	return c.Redirect("/settings?tab=services")
}

func (h *SettingsHandler) FirewallCreate(c *fiber.Ctx) error {
	rule, err := h.firewallInputFromForm(c, 0)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=firewall")
	}
	if h.firewallService != nil {
		if err := h.firewallService.ApplyRule(c.Context(), rule, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=firewall")
		}
	}
	if err := h.firewalls.Create(rule); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=firewall")
	}
	h.audit.Record(currentUserID(c), "firewall_rule.create", "firewall_rule", fmt.Sprintf("%d", rule.ID), c.IP(), rule)
	h.base.Sessions.SetFlash(c, "firewall rule added")
	return c.Redirect("/settings?tab=firewall")
}

func (h *SettingsHandler) FirewallUpdate(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		h.base.Sessions.SetFlash(c, "invalid firewall rule id")
		return c.Redirect("/settings?tab=firewall")
	}
	existing, err := h.firewalls.Find(id)
	if err != nil {
		h.base.Sessions.SetFlash(c, "firewall rule not found")
		return c.Redirect("/settings?tab=firewall")
	}
	rule, err := h.firewallInputFromForm(c, id)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=firewall")
	}
	if h.firewallService != nil {
		if err := h.firewallService.DeleteRule(c.Context(), existing, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=firewall")
		}
		if err := h.firewallService.ApplyRule(c.Context(), rule, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=firewall")
		}
	}
	existing.Name = rule.Name
	existing.Protocol = rule.Protocol
	existing.Port = rule.Port
	existing.Source = rule.Source
	existing.Action = rule.Action
	existing.Description = rule.Description
	existing.Enabled = rule.Enabled
	if err := h.firewalls.Update(existing); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=firewall")
	}
	h.audit.Record(currentUserID(c), "firewall_rule.update", "firewall_rule", fmt.Sprintf("%d", existing.ID), c.IP(), existing)
	h.base.Sessions.SetFlash(c, "firewall rule updated")
	return c.Redirect("/settings?tab=firewall")
}

func (h *SettingsHandler) FirewallDelete(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		h.base.Sessions.SetFlash(c, "invalid firewall rule id")
		return c.Redirect("/settings?tab=firewall")
	}
	existing, _ := h.firewalls.Find(id)
	if h.firewallService != nil && existing != nil {
		if err := h.firewallService.DeleteRule(c.Context(), existing, currentUserID(c), c.IP()); err != nil {
			h.base.Sessions.SetFlash(c, err.Error())
			return c.Redirect("/settings?tab=firewall")
		}
	}
	if err := h.firewalls.Delete(id); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=firewall")
	}
	h.audit.Record(currentUserID(c), "firewall_rule.delete", "firewall_rule", fmt.Sprintf("%d", id), c.IP(), nil)
	h.base.Sessions.SetFlash(c, "firewall rule deleted")
	return c.Redirect("/settings?tab=firewall")
}

func (h *SettingsHandler) firewallInputFromForm(c *fiber.Ctx, id uint) (*models.PanelFirewallRule, error) {
	name := strings.TrimSpace(c.FormValue("name"))
	protocol := strings.ToLower(strings.TrimSpace(c.FormValue("protocol")))
	port := strings.TrimSpace(c.FormValue("port"))
	source := strings.TrimSpace(c.FormValue("source"))
	action := strings.ToLower(strings.TrimSpace(c.FormValue("action")))
	status := strings.ToLower(strings.TrimSpace(c.FormValue("status")))
	description := strings.TrimSpace(c.FormValue("description"))

	if name == "" {
		return nil, fmt.Errorf("rule name is required")
	}
	if source == "" {
		source = "0.0.0.0/0"
	}
	if port == "" {
		return nil, fmt.Errorf("port is required")
	}
	switch protocol {
	case "tcp", "udp", "icmp", "any":
	default:
		return nil, fmt.Errorf("protocol must be tcp, udp, icmp, or any")
	}
	switch action {
	case "allow", "deny":
	default:
		return nil, fmt.Errorf("action must be allow or deny")
	}
	enabled := true
	switch status {
	case "", "active":
		enabled = true
	case "notactive", "inactive":
		enabled = false
	default:
		return nil, fmt.Errorf("status must be active or notactive")
	}

	return &models.PanelFirewallRule{
		ID:          id,
		Name:        name,
		Protocol:    protocol,
		Port:        port,
		Source:      source,
		Action:      action,
		Description: description,
		Enabled:     enabled,
	}, nil
}

func (h *SettingsHandler) eventsForView(users []models.User, page, perPage int) ([]settingsEventView, int64) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 25
	}
	total, err := h.auditRepo.Count()
	if err != nil {
		return []settingsEventView{}, 0
	}
	offset := (page - 1) * perPage
	items, err := h.auditRepo.ListPage(perPage, offset)
	if err != nil {
		return []settingsEventView{}, total
	}
	usernameByID := make(map[uint]string, len(users))
	for _, u := range users {
		label := strings.TrimSpace(u.Username)
		if strings.TrimSpace(u.Name) != "" {
			label = strings.TrimSpace(u.Name) + " (" + label + ")"
		}
		usernameByID[u.ID] = label
	}
	out := make([]settingsEventView, 0, len(items))
	for _, item := range items {
		username := "system"
		if item.ActorUserID != nil {
			if label, ok := usernameByID[*item.ActorUserID]; ok {
				username = label
			} else {
				username = "user#" + strconv.Itoa(int(*item.ActorUserID))
			}
		}
		eventText := strings.TrimSpace(item.Action)
		if eventText == "" {
			eventText = strings.TrimSpace(item.Resource + " " + item.ResourceID)
		}
		out = append(out, settingsEventView{
			Time:     item.CreatedAt.In(time.Local).Format("2006-01-02 15:04:05"),
			Username: username,
			Event:    eventText,
			Details:  prettyEventDetails(item),
		})
	}
	return out, total
}

func (h *SettingsHandler) userPlatformAccessMap(users []models.User) map[uint][]uint {
	out := make(map[uint][]uint, len(users))
	if h.userPlatformAccess == nil || len(users) == 0 {
		return out
	}
	rows, err := h.userPlatformAccess.List()
	if err != nil {
		return out
	}
	allowedUsers := make(map[uint]struct{}, len(users))
	for _, u := range users {
		allowedUsers[u.ID] = struct{}{}
	}
	for _, row := range rows {
		if _, ok := allowedUsers[row.UserID]; !ok {
			continue
		}
		if row.PlatformID == 0 {
			continue
		}
		out[row.UserID] = append(out[row.UserID], row.PlatformID)
	}
	for uid, ids := range out {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		out[uid] = ids
	}
	return out
}

func (h *SettingsHandler) platformOptions() []settingsPlatformOption {
	options := []settingsPlatformOption{}
	if h.websiteService == nil || h.appService == nil {
		return options
	}
	websites, wErr := h.websiteService.List()
	if wErr != nil {
		return options
	}
	apps, aErr := h.appService.List()
	if aErr != nil {
		return options
	}

	linkedWebsiteIDs := make(map[uint]struct{}, len(apps))
	for _, app := range apps {
		if app.WebsiteID != nil && *app.WebsiteID > 0 {
			linkedWebsiteIDs[*app.WebsiteID] = struct{}{}
		}
	}
	websiteByID := make(map[uint]models.Website, len(websites))
	for _, site := range websites {
		websiteByID[site.ID] = site
	}

	for _, site := range websites {
		if _, linked := linkedWebsiteIDs[site.ID]; linked {
			continue
		}
		domain := primaryWebsiteDomain(site.Domains)
		runtime := strings.TrimSpace(site.Type)
		if runtime == "" {
			runtime = "website"
		}
		label := strings.TrimSpace(site.Name)
		if domain != "" {
			label = fmt.Sprintf("%s (%s)", label, domain)
		}
		options = append(options, settingsPlatformOption{
			ID:      site.ID,
			Name:    site.Name,
			Domain:  domain,
			Runtime: runtime,
			Kind:    "website",
			Label:   label,
		})
	}

	for _, app := range apps {
		domain := ""
		if app.WebsiteID != nil {
			if site, ok := websiteByID[*app.WebsiteID]; ok {
				domain = primaryWebsiteDomain(site.Domains)
			}
		}
		runtime := strings.TrimSpace(strings.ToLower(app.Runtime))
		if runtime == "" {
			runtime = "app"
		}
		label := strings.TrimSpace(app.Name)
		if domain != "" {
			label = fmt.Sprintf("%s (%s · %s)", label, domain, runtime)
		} else {
			label = fmt.Sprintf("%s (%s)", label, runtime)
		}
		options = append(options, settingsPlatformOption{
			ID:      app.ID,
			Name:    app.Name,
			Domain:  domain,
			Runtime: runtime,
			Kind:    "app",
			Label:   label,
		})
	}

	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Label) < strings.ToLower(options[j].Label)
	})
	return options
}

func (h *SettingsHandler) isProtectedUsername(username string) bool {
	return strings.EqualFold(strings.TrimSpace(username), strings.TrimSpace(h.base.Config.Security.BootstrapAdminUser))
}

func prettyEventDetails(item models.AuditLog) string {
	base := map[string]any{
		"id":          item.ID,
		"action":      item.Action,
		"resource":    item.Resource,
		"resource_id": item.ResourceID,
		"ip":          item.IP,
		"time":        item.CreatedAt.In(time.Local).Format("2006-01-02T15:04:05Z07:00"),
	}
	if item.ActorUserID != nil {
		base["actor_user_id"] = *item.ActorUserID
	}
	payloadRaw := strings.TrimSpace(item.Payload)
	if payloadRaw != "" {
		var payload any
		if err := json.Unmarshal([]byte(payloadRaw), &payload); err == nil {
			base["payload"] = payload
		} else {
			base["payload"] = payloadRaw
		}
	}
	b, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return item.Payload
	}
	return string(b)
}

func parsePositiveInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func parseUintMultiFormValues(c *fiber.Ctx, key string) []uint {
	values := c.Request().PostArgs().PeekMulti(key)
	out := make([]uint, 0, len(values))
	seen := make(map[uint]struct{}, len(values))
	for _, raw := range values {
		n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || n == 0 {
			continue
		}
		id := uint(n)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (h *SettingsHandler) runtimeVersionUsage(runtime, version string) (int, []string, error) {
	rt := strings.ToLower(strings.TrimSpace(runtime))
	ver := strings.TrimSpace(version)
	if rt == "" || ver == "" {
		return 0, nil, nil
	}

	usage := make(map[string]struct{})

	if rt == "php" && h.websiteService != nil {
		websites, err := h.websiteService.List()
		if err != nil {
			return 0, nil, err
		}
		for _, site := range websites {
			if strings.EqualFold(strings.TrimSpace(site.Type), "php") && strings.EqualFold(strings.TrimSpace(site.PHPVersion), ver) {
				name := strings.TrimSpace(site.Name)
				if name == "" {
					name = fmt.Sprintf("platform#%d", site.ID)
				}
				usage[name] = struct{}{}
			}
		}
	}

	if h.appService != nil {
		apps, err := h.appService.List()
		if err != nil {
			return 0, nil, err
		}
		for _, app := range apps {
			if !strings.EqualFold(strings.TrimSpace(app.Runtime), rt) {
				continue
			}
			rv := strings.TrimSpace(envVarValue(app.EnvVars, "RUNTIME_VERSION"))
			if !strings.EqualFold(rv, ver) {
				continue
			}
			name := strings.TrimSpace(app.Name)
			if name == "" {
				name = fmt.Sprintf("platform#%d", app.ID)
			}
			usage[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(usage))
	for name := range usage {
		names = append(names, name)
	}
	sort.Strings(names)
	return len(names), names, nil
}
