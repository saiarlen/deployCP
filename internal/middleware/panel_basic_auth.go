package middleware

import (
	"encoding/base64"
	"strings"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/repositories"
	"deploycp/internal/utils"
)

func PanelBasicAuth(settings *repositories.SettingRepository) fiber.Handler {
	return func(c *fiber.Ctx) error {
		path := c.Path()
		if strings.HasPrefix(path, "/assets/") {
			return c.Next()
		}
		enabled := settingBool(settings, "panel_basic_auth_enabled", false)
		if !enabled {
			return c.Next()
		}

		authHeader := strings.TrimSpace(c.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(authHeader), "basic ") {
			return panelBasicAuthChallenge(c)
		}

		rawToken := strings.TrimSpace(authHeader[6:])
		decoded, err := base64.StdEncoding.DecodeString(rawToken)
		if err != nil {
			return panelBasicAuthChallenge(c)
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return panelBasicAuthChallenge(c)
		}
		username := strings.TrimSpace(parts[0])
		password := parts[1]

		wantUser := settingString(settings, "panel_basic_auth_username", "admin")
		wantHash := settingString(settings, "panel_basic_auth_password_hash", "")
		if wantHash == "" {
			return panelBasicAuthChallenge(c)
		}
		if username != wantUser || !utils.VerifyPassword(wantHash, password) {
			return panelBasicAuthChallenge(c)
		}
		return c.Next()
	}
}

func panelBasicAuthChallenge(c *fiber.Ctx) error {
	c.Set("WWW-Authenticate", `Basic realm="DeployCP Panel", charset="UTF-8"`)
	return c.Status(fiber.StatusUnauthorized).SendString("Authentication required")
}

func settingBool(repo *repositories.SettingRepository, key string, fallback bool) bool {
	raw, err := repo.Get(key)
	if err != nil {
		return fallback
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

func settingString(repo *repositories.SettingRepository, key, fallback string) string {
	raw, err := repo.Get(key)
	if err != nil {
		return fallback
	}
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	return v
}
