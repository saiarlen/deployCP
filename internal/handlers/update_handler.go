package handlers

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/services"
)

type UpdateHandler struct {
	base    *BaseHandler
	updates *services.UpdateService
}

func NewUpdateHandler(cfg *config.Config, sessions *middleware.SessionManager, updates *services.UpdateService) *UpdateHandler {
	return &UpdateHandler{
		base:    &BaseHandler{Config: cfg, Sessions: sessions},
		updates: updates,
	}
}

func (h *UpdateHandler) Index(c *fiber.Ctx) error {
	view := h.updates.FullView()
	return h.base.Render(c, "updates_index", fiber.Map{
		"Title":      "Updates",
		"UpdateView": view,
	})
}

func (h *UpdateHandler) Status(c *fiber.Ctx) error {
	return c.JSON(h.updates.FullView())
}

func (h *UpdateHandler) Check(c *fiber.Ctx) error {
	if err := h.updates.CheckNow(context.Background()); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(h.updates.FullView())
}

func (h *UpdateHandler) Install(c *fiber.Ctx) error {
	if err := h.updates.StartInstall(currentUserID(c), c.IP()); err != nil {
		if strings.Contains(strings.ToLower(c.Get("Accept")), "application/json") {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/updates")
	}
	if strings.Contains(strings.ToLower(c.Get("Accept")), "application/json") {
		return c.JSON(fiber.Map{"ok": true})
	}
	h.base.Sessions.SetFlash(c, "update started")
	return c.Redirect("/updates")
}
