package handlers

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/repositories"
	"deploycp/internal/services"
)

type DatabaseHandler struct {
	base    *BaseHandler
	service *services.DatabaseService
}

func NewDatabaseHandler(cfg *config.Config, sessions *middleware.SessionManager, service *services.DatabaseService) *DatabaseHandler {
	return &DatabaseHandler{base: &BaseHandler{Config: cfg, Sessions: sessions}, service: service}
}

func (h *DatabaseHandler) Index(c *fiber.Ctx) error {
	dbItems, _ := h.service.ListDatabases()
	redisItems, _ := h.service.ListRedis()
	return h.base.Render(c, "databases_index", fiber.Map{
		"Title":           "Databases",
		"DBItems":         dbItems,
		"RedisItems":      redisItems,
		"AdminerURL":      h.service.AdminerURL(),
		"PostgresGUIBase": h.base.Config.Integrations.PostgresGUIURL,
	})
}

func (h *DatabaseHandler) CreateDatabase(c *fiber.Ctx) error {
	port, _ := strconv.Atoi(c.FormValue("port", "3306"))
	in := services.DBConnectionInput{
		Label:       strings.TrimSpace(c.FormValue("label")),
		Engine:      c.FormValue("engine"),
		Host:        strings.TrimSpace(c.FormValue("host")),
		Port:        port,
		Database:    strings.TrimSpace(c.FormValue("database")),
		Username:    strings.TrimSpace(c.FormValue("username")),
		Password:    c.FormValue("password"),
		Environment: strings.TrimSpace(c.FormValue("environment")),
	}
	if err := h.service.CreateDatabase(in, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/databases")
	}
	h.base.Sessions.SetFlash(c, "Database connection saved")
	return c.Redirect("/databases")
}

func (h *DatabaseHandler) CreateRedis(c *fiber.Ctx) error {
	port, _ := strconv.Atoi(c.FormValue("port", "6379"))
	db, _ := strconv.Atoi(c.FormValue("db", "0"))
	in := services.RedisInput{
		Label:       strings.TrimSpace(c.FormValue("label")),
		Host:        strings.TrimSpace(c.FormValue("host")),
		Port:        port,
		Password:    c.FormValue("password"),
		DB:          db,
		Environment: strings.TrimSpace(c.FormValue("environment")),
	}
	if err := h.service.CreateRedis(in, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect("/databases")
}

func (h *DatabaseHandler) TestDatabase(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.TestDatabase(id); err != nil {
		h.base.Sessions.SetFlash(c, "DB test failed: "+err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "DB connection test succeeded")
	}
	return c.Redirect("/databases")
}

func (h *DatabaseHandler) TestRedis(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.TestRedis(id); err != nil {
		h.base.Sessions.SetFlash(c, "Redis test failed: "+err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "Redis connection test succeeded")
	}
	return c.Redirect("/databases")
}

func (h *DatabaseHandler) RedisInfo(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	info, err := h.service.RedisInfo(id)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/databases")
	}
	return h.base.Render(c, "databases_redis_info", fiber.Map{"Title": "Redis Diagnostics", "Info": info})
}

func (h *DatabaseHandler) OpenPostgresGUI(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	url, err := h.service.PostgresGUIURL(id)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/databases")
	}
	return c.Redirect(url)
}

func (h *DatabaseHandler) DeleteDatabase(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.DeleteDatabase(id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect("/databases")
}

func (h *DatabaseHandler) DeleteRedis(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.DeleteRedis(id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect("/databases")
}

func (h *DatabaseHandler) Adminer(c *fiber.Ctx) error {
	return c.Redirect(h.service.AdminerURL())
}
