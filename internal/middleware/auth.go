package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/models"
	"deploycp/internal/repositories"
)

func AuthRequired(sm *SessionManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		uid, err := sm.GetUserID(c)
		if err != nil || uid == 0 || c.Locals("auth_user") == nil {
			_ = sm.Clear(c)
			return c.Redirect("/login")
		}
		c.Locals("auth_user_id", uid)
		return c.Next()
	}
}

func InjectAuthUser(sm *SessionManager, users *repositories.UserRepository, access *repositories.UserPlatformAccessRepository) fiber.Handler {
	return func(c *fiber.Ctx) error {
		uid, _ := sm.GetUserID(c)
		if uid != 0 {
			if u, err := users.FindByID(uid); err == nil && u.IsActive {
				c.Locals("auth_user", u)
				role := normalizeUserRole(u)
				c.Locals("auth_user_role", role)
				c.Locals("auth_can_settings", role == "admin")
				if role == "user" && access != nil {
					platformIDs, _ := access.ListPlatformIDsByUser(uid)
					c.Locals("auth_platform_access_ids", platformIDs)
				}
			} else {
				_ = sm.Clear(c)
			}
		}
		return c.Next()
	}
}

func normalizeUserRole(u *models.User) string {
	if u == nil {
		return ""
	}
	role := strings.ToLower(strings.TrimSpace(u.Role))
	role = strings.ReplaceAll(role, " ", "_")
	role = strings.ReplaceAll(role, "-", "_")
	if role == "sitemanager" {
		role = "site_manager"
	}
	if role == "" && u.IsAdmin {
		return "admin"
	}
	switch role {
	case "admin", "site_manager", "user":
		return role
	default:
		if u.IsAdmin {
			return "admin"
		}
		return "user"
	}
}
