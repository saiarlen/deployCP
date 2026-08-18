package middleware

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"deploycp/internal/models"
	"deploycp/internal/repositories"
)

const authLookupUnavailableKey = "deploycp_auth_lookup_unavailable"

func AuthRequired(sm *SessionManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		uid, err := sm.GetUserID(c)
		if err != nil || c.Locals(authLookupUnavailableKey) == true {
			return fiber.NewError(fiber.StatusServiceUnavailable, "authentication is temporarily unavailable; retry shortly")
		}
		if uid == 0 || c.Locals("auth_user") == nil {
			_ = sm.Clear(c)
			return c.Redirect("/login")
		}
		c.Locals("auth_user_id", uid)
		return c.Next()
	}
}

func InjectAuthUser(sm *SessionManager, users *repositories.UserRepository, access *repositories.UserPlatformAccessRepository) fiber.Handler {
	return func(c *fiber.Ctx) error {
		uid, err := sm.GetUserID(c)
		if err != nil {
			// A transient session-store error must never destroy a valid login.
			c.Locals(authLookupUnavailableKey, true)
			return c.Next()
		}
		if uid != 0 {
			u, err := users.FindByID(uid)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					_ = sm.Clear(c)
				} else {
					// Database restarts, migrations, and short SQLite locks are retryable.
					// Preserve the session cookie so the user remains signed in afterwards.
					c.Locals(authLookupUnavailableKey, true)
				}
				return c.Next()
			}
			if u.IsActive {
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
