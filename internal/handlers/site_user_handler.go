package handlers

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/repositories"
	"deploycp/internal/services"
)

type SiteUserHandler struct {
	base    *BaseHandler
	service *services.SiteUserService
}

func NewSiteUserHandler(cfg *config.Config, sessions *middleware.SessionManager, service *services.SiteUserService) *SiteUserHandler {
	return &SiteUserHandler{base: &BaseHandler{Config: cfg, Sessions: sessions}, service: service}
}

func (h *SiteUserHandler) Index(c *fiber.Ctx) error {
	items, err := h.service.List()
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return h.base.Render(c, "site_users_index", fiber.Map{"Title": "Site Users", "Items": items})
}

func (h *SiteUserHandler) New(c *fiber.Ctx) error {
	return h.base.Render(c, "site_users_form", fiber.Map{"Title": "New Site User", "Action": "/site-users"})
}

func (h *SiteUserHandler) Create(c *fiber.Ctx) error {
	input := services.SiteUserInput{
		Username:      strings.TrimSpace(c.FormValue("username")),
		HomeDirectory: strings.TrimSpace(c.FormValue("home_directory")),
		AllowedRoot:   strings.TrimSpace(c.FormValue("allowed_root")),
		Password:      c.FormValue("password"),
		SSHEnabled:    boolFromForm(c, "ssh_enabled"),
	}
	item, generatedPassword, err := h.service.Create(c.Context(), input, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/site-users/new")
	}
	h.base.Sessions.SetFlash(c, "Site user created. Generated password: "+generatedPassword)
	return c.Redirect(fmt.Sprintf("/site-users/%d", item.ID))
}

func (h *SiteUserHandler) Show(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	item, err := h.service.Find(id)
	if err != nil {
		return c.Status(404).SendString("site user not found")
	}
	return h.base.Render(c, "site_users_show", fiber.Map{"Title": item.Username, "Item": item})
}

func (h *SiteUserHandler) ResetPassword(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	password := c.FormValue("password")
	newPassword, err := h.service.ResetPassword(c.Context(), id, password, currentUserID(c), c.IP())
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(fmt.Sprintf("/site-users/%d", id))
	}
	h.base.Sessions.SetFlash(c, "Password reset successfully: "+newPassword)
	return c.Redirect(fmt.Sprintf("/site-users/%d", id))
}

func (h *SiteUserHandler) Toggle(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	enabled := boolFromForm(c, "enabled")
	if err := h.service.ToggleEnabled(c.Context(), id, enabled, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect(fmt.Sprintf("/site-users/%d", id))
}

func (h *SiteUserHandler) Delete(c *fiber.Ctx) error {
	id, err := repositories.ParseID(c.Params("id"))
	if err != nil {
		return c.Status(400).SendString(err.Error())
	}
	if err := h.service.Delete(c.Context(), id, currentUserID(c), c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
	}
	return c.Redirect("/site-users")
}
