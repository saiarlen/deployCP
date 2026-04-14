package handlers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/services"
)

type ServiceHandler struct {
	base    *BaseHandler
	service *services.ServiceService
}

func NewServiceHandler(cfg *config.Config, sessions *middleware.SessionManager, service *services.ServiceService) *ServiceHandler {
	return &ServiceHandler{base: &BaseHandler{Config: cfg, Sessions: sessions}, service: service}
}

func (h *ServiceHandler) Create(c *fiber.Ctx) error {
	input := services.ServiceInput{
		Name:         strings.TrimSpace(c.FormValue("name")),
		Type:         strings.TrimSpace(c.FormValue("type")),
		PlatformName: strings.TrimSpace(c.FormValue("platform_name")),
		UnitPath:     strings.TrimSpace(c.FormValue("unit_path")),
		Tags:         strings.TrimSpace(c.FormValue("tags")),
		Description:  strings.TrimSpace(c.FormValue("description")),
		Enabled:      boolFromForm(c, "enabled"),
		Upsert:       boolFromForm(c, "upsert"),
	}
	created, err := h.service.Create(input, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=services")
	}
	if created {
		h.base.Sessions.SetFlash(c, fmt.Sprintf("service %s added", input.Name))
	} else {
		h.base.Sessions.SetFlash(c, fmt.Sprintf("service %s updated from catalog", input.Name))
	}
	return c.Redirect("/settings?tab=services")
}

func (h *ServiceHandler) Update(c *fiber.Ctx) error {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		h.base.Sessions.SetFlash(c, "invalid service id")
		return c.Redirect("/settings?tab=services")
	}
	input := services.ServiceInput{
		Name:         strings.TrimSpace(c.FormValue("name")),
		Type:         strings.TrimSpace(c.FormValue("type")),
		PlatformName: strings.TrimSpace(c.FormValue("platform_name")),
		UnitPath:     strings.TrimSpace(c.FormValue("unit_path")),
		Tags:         strings.TrimSpace(c.FormValue("tags")),
		Description:  strings.TrimSpace(c.FormValue("description")),
		Enabled:      boolFromForm(c, "enabled"),
	}
	if err := h.service.Update(uint(id), input, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=services")
	}
	h.base.Sessions.SetFlash(c, fmt.Sprintf("service %s updated", input.Name))
	return c.Redirect("/settings?tab=services")
}

func (h *ServiceHandler) Delete(c *fiber.Ctx) error {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		h.base.Sessions.SetFlash(c, "invalid service id")
		return c.Redirect("/settings?tab=services")
	}
	if err := h.service.Delete(uint(id), currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=services")
	}
	h.base.Sessions.SetFlash(c, "service removed")
	return c.Redirect("/settings?tab=services")
}

func (h *ServiceHandler) Action(c *fiber.Ctx) error {
	ref := strings.TrimSpace(c.Params("ref"))
	action := strings.TrimSpace(c.Params("action"))
	if err := h.service.ActionByRef(c.Context(), ref, action, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=services")
	}
	h.base.Sessions.SetFlash(c, fmt.Sprintf("service action applied: %s", action))
	return c.Redirect("/settings?tab=services")
}

func (h *ServiceHandler) Logs(c *fiber.Ctx) error {
	ref := strings.TrimSpace(c.Params("ref"))
	lines, _ := strconv.Atoi(c.Query("lines", "200"))
	name, logs, err := h.service.LogsByRef(c.Context(), ref, lines)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/settings?tab=services")
	}
	return h.base.Render(c, "services_logs", fiber.Map{"Title": fmt.Sprintf("Logs: %s", name), "Name": name, "Logs": logs})
}
