package handlers

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/repositories"
	"deploycp/internal/services"
)

type SSLHandler struct {
	base    *BaseHandler
	service *services.SSLService
}

func NewSSLHandler(cfg *config.Config, sessions *middleware.SessionManager, service *services.SSLService) *SSLHandler {
	return &SSLHandler{base: &BaseHandler{Config: cfg, Sessions: sessions}, service: service}
}

func (h *SSLHandler) Index(c *fiber.Ctx) error {
	items, err := h.service.List()
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return h.base.Render(c, "ssl_index", fiber.Map{"Title": "SSL Certificates", "Items": items})
}

func (h *SSLHandler) Create(c *fiber.Ctx) error {
	domain := strings.TrimSpace(c.FormValue("domain"))
	if domain == "" {
		h.base.Sessions.SetFlash(c, "domain is required")
		return c.Redirect("/ssl")
	}
	if err := h.service.Create(domain, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "certificate record created")
	}
	return c.Redirect("/ssl")
}

func (h *SSLHandler) Renew(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.Renew(id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	} else {
		h.base.Sessions.SetFlash(c, "renewal hook executed")
	}
	return c.Redirect("/ssl")
}

func (h *SSLHandler) Delete(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.Delete(id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect("/ssl")
}
