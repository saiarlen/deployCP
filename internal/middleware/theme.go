package middleware

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"deploycp/internal/repositories"
)

const (
	defaultTheme     = "light"
	themeCookieName  = "deploycp_theme"
	userThemePrefKey = "ui_theme"
)

func InjectTheme(sm *SessionManager, settings *repositories.SettingRepository, prefs *repositories.UserPreferenceRepository) func(c *fiber.Ctx) error {
	return func(c *fiber.Ctx) error {
		theme := defaultTheme

		if global, err := settings.Get("theme"); err == nil && isAllowedTheme(global) {
			theme = global
		}

		if cookieTheme := c.Cookies(themeCookieName); isAllowedTheme(cookieTheme) {
			theme = cookieTheme
		}

		uid, _ := sm.GetUserID(c)
		if uid != 0 {
			if userTheme, err := prefs.Get(uid, userThemePrefKey); err == nil && isAllowedTheme(userTheme) {
				theme = userTheme
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				// Keep selected fallback theme when preference fetch fails.
			}
		}

		c.Locals("ui_theme", theme)
		return c.Next()
	}
}

func isAllowedTheme(theme string) bool {
	return theme == "dark" || theme == "light"
}
