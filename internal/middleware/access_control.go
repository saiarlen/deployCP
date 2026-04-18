package middleware

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/repositories"
	"deploycp/internal/utils"
)

func AdminOnly(sm *SessionManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if roleFromLocals(c) == "admin" {
			return c.Next()
		}
		if expectsJSON(c) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "forbidden"})
		}
		sm.SetFlash(c, "settings access is restricted to admin users")
		return c.Redirect("/platforms")
	}
}

func ProvisioningAllowed(sm *SessionManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		switch roleFromLocals(c) {
		case "admin", "site_manager":
			return c.Next()
		}
		if expectsJSON(c) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "forbidden"})
		}
		sm.SetFlash(c, "platform creation is restricted to admin and site manager users")
		return c.Redirect("/platforms")
	}
}

func PlatformAccess(sm *SessionManager, access *repositories.UserPlatformAccessRepository) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if roleFromLocals(c) != "user" {
			return c.Next()
		}

		platformID, scoped := platformIDFromPath(c.Path())
		if !scoped {
			return c.Next()
		}

		allowedIDs := platformIDsFromLocals(c.Locals("auth_platform_access_ids"))
		if len(allowedIDs) == 0 && access != nil {
			uid := authUserIDFromLocals(c)
			if uid != 0 {
				dbIDs, _ := access.ListPlatformIDsByUser(uid)
				allowedIDs = dbIDs
				c.Locals("auth_platform_access_ids", allowedIDs)
			}
		}
		allowed := make(map[uint]struct{}, len(allowedIDs))
		for _, id := range allowedIDs {
			allowed[id] = struct{}{}
		}
		if _, ok := allowed[platformID]; ok {
			return c.Next()
		}

		if expectsJSON(c) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "forbidden"})
		}
		sm.SetFlash(c, "you do not have access to this platform")
		return c.Redirect("/platforms")
	}
}

func roleFromLocals(c *fiber.Ctx) string {
	role := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", c.Locals("auth_user_role"))))
	switch role {
	case "admin", "site_manager", "user":
		return role
	default:
		return ""
	}
}

func authUserIDFromLocals(c *fiber.Ctx) uint {
	raw := strings.TrimSpace(fmt.Sprintf("%v", c.Locals("auth_user_id")))
	if raw == "" || raw == "<nil>" {
		return 0
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return uint(n)
}

func platformIDFromPath(path string) (uint, bool) {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return 0, false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return 0, false
	}

	switch parts[0] {
	case "platforms":
		_, id, err := utils.DecodePlatformRef(parts[1])
		if err != nil || id == 0 {
			return 0, false
		}
		return id, true
	case "websites", "apps":
		n, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil || n == 0 {
			return 0, false
		}
		return uint(n), true
	default:
		return 0, false
	}
}

func platformIDsFromLocals(v any) []uint {
	switch ids := v.(type) {
	case []uint:
		return ids
	case []int:
		out := make([]uint, 0, len(ids))
		for _, id := range ids {
			if id > 0 {
				out = append(out, uint(id))
			}
		}
		return out
	case []any:
		out := make([]uint, 0, len(ids))
		for _, raw := range ids {
			val := strings.TrimSpace(fmt.Sprintf("%v", raw))
			if n, err := strconv.ParseUint(val, 10, 64); err == nil && n > 0 {
				out = append(out, uint(n))
			}
		}
		return out
	default:
		return nil
	}
}

func expectsJSON(c *fiber.Ctx) bool {
	acc := strings.ToLower(c.Get("Accept", ""))
	return strings.Contains(acc, "application/json") && !strings.Contains(acc, "text/html")
}
