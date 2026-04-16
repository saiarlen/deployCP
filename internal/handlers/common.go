package handlers

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
)

type BaseHandler struct {
	Config   *config.Config
	Sessions *middleware.SessionManager
}

func (h *BaseHandler) Render(c *fiber.Ctx, page string, data fiber.Map) error {
	if data == nil {
		data = fiber.Map{}
	}
	data["Page"] = page
	section := sectionFromPage(page)
	data["Section"] = section
	data["AppName"] = h.Config.App.Name
	data["BaseURL"] = h.Config.App.BaseURL
	data["Flash"] = h.Sessions.PullFlash(c)
	data["AuthUser"] = c.Locals("auth_user")
	data["AuthUserRole"] = authUserRole(c)
	data["CanAccessSettings"] = canAccessSettings(c)
	theme := fmt.Sprintf("%v", c.Locals("ui_theme"))
	if theme != "light" && theme != "dark" {
		theme = "light"
	}
	data["Theme"] = theme
	data["CSRFToken"] = csrfTokenFromContext(c)
	data["AssetVersion"] = assetVersion()
	data["CurrentYear"] = time.Now().Year()
	data["AppVersion"] = strings.TrimSpace(fmt.Sprintf("%v", c.Locals("app_version_display")))
	data["AppVersionIsDev"] = strings.EqualFold(strings.TrimSpace(fmt.Sprintf("%v", c.Locals("app_version_is_dev"))), "true")
	return c.Render(page, data, "layouts_base")
}

func currentUserID(c *fiber.Ctx) *uint {
	v := fmt.Sprintf("%v", c.Locals("auth_user_id"))
	if v == "" || v == "<nil>" {
		return nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return nil
	}
	u := uint(n)
	return &u
}

func authUserRole(c *fiber.Ctx) string {
	role := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", c.Locals("auth_user_role"))))
	switch role {
	case "admin", "site_manager", "user":
		return role
	default:
		return ""
	}
}

func canAccessSettings(c *fiber.Ctx) bool {
	return authUserRole(c) == "admin"
}

func allowedPlatformIDSet(c *fiber.Ctx) map[uint]struct{} {
	out := map[uint]struct{}{}
	raw := c.Locals("auth_platform_access_ids")
	switch ids := raw.(type) {
	case []uint:
		for _, id := range ids {
			if id != 0 {
				out[id] = struct{}{}
			}
		}
	case []int:
		for _, id := range ids {
			if id > 0 {
				out[uint(id)] = struct{}{}
			}
		}
	case []any:
		for _, value := range ids {
			n, err := strconv.ParseUint(strings.TrimSpace(fmt.Sprintf("%v", value)), 10, 64)
			if err == nil && n > 0 {
				out[uint(n)] = struct{}{}
			}
		}
	}
	return out
}

func boolFromForm(c *fiber.Ctx, key string) bool {
	v := c.FormValue(key)
	return v == "on" || v == "1" || v == "true"
}

func displayServerAddress(cfg *config.Config, requestHost string) string {
	candidates := make([]string, 0, 3)
	if cfg != nil {
		candidates = append(candidates, strings.TrimSpace(cfg.App.Host))
		baseURL := strings.TrimSpace(cfg.App.BaseURL)
		if baseURL != "" {
			if u, err := url.Parse(baseURL); err == nil {
				candidates = append(candidates, strings.TrimSpace(u.Hostname()))
			}
		}
	}
	candidates = append(candidates, strings.TrimSpace(requestHost))

	for _, raw := range candidates {
		host := normalizeHostCandidate(raw)
		switch strings.ToLower(host) {
		case "", "0.0.0.0", "::", "[::]":
			continue
		case "localhost":
			return "127.0.0.1"
		default:
			return host
		}
	}
	return "127.0.0.1"
}

func normalizeHostCandidate(v string) string {
	host := strings.TrimSpace(v)
	if host == "" {
		return ""
	}
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	return strings.TrimSpace(host)
}

func csrfTokenFromContext(c *fiber.Ctx) string {
	v := c.Locals("csrf")
	switch token := v.(type) {
	case nil:
		return ""
	case string:
		return token
	case []byte:
		return string(token)
	case *string:
		if token == nil {
			return ""
		}
		return *token
	case func() string:
		return token()
	case fiber.Handler:
		return ""
	default:
		return fmt.Sprintf("%v", token)
	}
}

func assetVersion() string {
	candidates := []string{
		"./frontend/assets/css/app.css",
		"../frontend/assets/css/app.css",
		"../../frontend/assets/css/app.css",
		"./assets/css/app.css",
		"../assets/css/app.css",
		"../../assets/css/app.css",
	}
	for _, p := range candidates {
		clean := filepath.Clean(p)
		if info, err := os.Stat(clean); err == nil {
			return strconv.FormatInt(info.ModTime().Unix(), 10)
		}
	}
	return strconv.FormatInt(time.Now().Unix(), 10)
}

func sectionFromPage(page string) string {
	if idx := strings.Index(page, "/"); idx >= 0 {
		return page[:idx]
	}
	known := []string{
		"dashboard",
		"platforms",
		"sites",
		"websites",
		"apps",
		"services",
		"site_users",
		"databases",
		"ssl",
		"logs",
		"settings",
		"updates",
		"auth",
	}
	for _, prefix := range known {
		if page == prefix || strings.HasPrefix(page, prefix+"_") {
			return prefix
		}
	}
	return page
}
