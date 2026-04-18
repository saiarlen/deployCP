package handlers

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"deploycp/internal/config"
	"deploycp/internal/middleware"
	"deploycp/internal/services"
)

const themeCookieName = "deploycp_theme"

type AuthHandler struct {
	base     *BaseHandler
	auth     *services.AuthService
	settings *services.SettingsService
}

func NewAuthHandler(
	cfg *config.Config,
	sessions *middleware.SessionManager,
	auth *services.AuthService,
	settings *services.SettingsService,
) *AuthHandler {
	return &AuthHandler{
		base:     &BaseHandler{Config: cfg, Sessions: sessions},
		auth:     auth,
		settings: settings,
	}
}

func (h *AuthHandler) SetupPage(c *fiber.Ctx) error {
	if !h.auth.NeedsSetup() {
		return c.Redirect("/login")
	}
	theme := normalizeTheme(c.Locals("ui_theme"))
	if theme == "" {
		theme = "light"
	}
	return c.Render("auth_setup", fiber.Map{
		"Title":        "Create Admin Account",
		"AppName":      h.base.Config.App.Name,
		"Flash":        h.base.Sessions.PullFlash(c),
		"CSRFToken":    csrfTokenFromContext(c),
		"Theme":        theme,
		"AssetVersion": assetVersion(),
	})
}

func (h *AuthHandler) SetupCreate(c *fiber.Ctx) error {
	if !h.auth.NeedsSetup() {
		return c.Redirect("/login")
	}
	name := strings.TrimSpace(c.FormValue("name"))
	email := strings.TrimSpace(c.FormValue("email"))
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	if err := h.auth.CreateInitialAdmin(name, email, username, password); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/setup")
	}
	h.base.Sessions.SetFlash(c, "Admin account created. Sign in to continue.")
	return c.Redirect("/login")
}

func (h *AuthHandler) LoginPage(c *fiber.Ctx) error {
	theme := normalizeTheme(c.Locals("ui_theme"))
	if theme == "" {
		theme = "light"
	}
	captchaExpr, captchaTok, err := h.issueLoginCaptcha(c)
	if err != nil {
		captchaExpr = "1 + 1 = ?"
		captchaTok, _ = signLoginCaptchaToken(h.base.Config.Security.SessionSecret, captchaExpr, "2")
	}
	return c.Render("auth_login", fiber.Map{
		"Title":             "Sign In",
		"AppName":           h.base.Config.App.Name,
		"Flash":             h.base.Sessions.PullFlash(c),
		"CSRFToken":         csrfTokenFromContext(c),
		"Theme":             theme,
		"AssetVersion":      assetVersion(),
		"CaptchaExpression": captchaExpr,
		"CaptchaToken":      captchaTok,
	})
}

// LoginCaptcha returns a fresh math expression for the login form (JSON: { "expression": "…" }).
func (h *AuthHandler) LoginCaptcha(c *fiber.Ctx) error {
	expr, tok, err := h.issueLoginCaptcha(c)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "captcha unavailable"})
	}
	return c.JSON(fiber.Map{"expression": expr, "token": tok})
}

func (h *AuthHandler) issueLoginCaptcha(c *fiber.Ctx) (expression, token string, err error) {
	expr, ans := newLoginCaptcha()
	tok, err := signLoginCaptchaToken(h.base.Config.Security.SessionSecret, expr, ans)
	return expr, tok, err
}

func newLoginCaptcha() (expression, answer string) {
	a := int(randInt63n(18) + 1)
	b := int(randInt63n(18) + 1)
	if randInt63n(2) == 0 {
		expression = fmt.Sprintf("%d + %d = ?", a, b)
		answer = strconv.Itoa(a + b)
		return
	}
	if a < b {
		a, b = b, a
	}
	expression = fmt.Sprintf("%d - %d = ?", a, b)
	answer = strconv.Itoa(a - b)
	return
}

func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0
	}
	return v.Int64()
}

func (h *AuthHandler) Login(c *fiber.Ctx) error {
	if !verifyLoginCaptchaToken(h.base.Config.Security.SessionSecret, c.FormValue("captcha_token"), c.FormValue("captcha_answer")) {
		h.base.Sessions.SetFlash(c, "incorrect or expired captcha answer")
		return c.Redirect("/login")
	}
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	u, err := h.auth.Authenticate(username, password)
	if err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/login")
	}
	if err := h.base.Sessions.SetUserID(c, u.ID); err != nil {
		h.base.Sessions.SetFlash(c, "failed to create session")
		return c.Redirect("/login")
	}
	_ = h.auth.StartUserSession(u, c.Cookies(h.base.Config.Security.SessionCookieName), c.IP(), c.Get("User-Agent"))
	h.base.Sessions.SetFlash(c, "Welcome back, "+u.Username+".")
	return c.Redirect("/")
}

func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	_ = h.auth.EndUserSession(c.Cookies(h.base.Config.Security.SessionCookieName))
	_ = h.base.Sessions.Clear(c)
	return c.Redirect("/login")
}

func (h *AuthHandler) PasswordPage(c *fiber.Ctx) error {
	return c.Redirect("/profile")
}

func (h *AuthHandler) ProfilePage(c *fiber.Ctx) error {
	return h.base.Render(c, "profile_index", fiber.Map{"Title": "Profile"})
}

func (h *AuthHandler) ProfileUpdate(c *fiber.Ctx) error {
	uid := currentUserID(c)
	if uid == nil {
		return c.Redirect("/login")
	}
	name := strings.TrimSpace(c.FormValue("name"))
	email := strings.TrimSpace(c.FormValue("email"))
	if err := h.auth.UpdateProfile(*uid, name, email); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/profile")
	}
	h.base.Sessions.SetFlash(c, "profile updated successfully")
	return c.Redirect("/profile")
}

func (h *AuthHandler) PasswordUpdate(c *fiber.Ctx) error {
	uid := currentUserID(c)
	if uid == nil {
		return c.Redirect("/login")
	}
	current := c.FormValue("current_password")
	next := c.FormValue("new_password")
	confirm := c.FormValue("confirm_password")
	if len(next) < 10 {
		h.base.Sessions.SetFlash(c, "new password must be at least 10 characters")
		return c.Redirect("/profile")
	}
	if next != confirm {
		h.base.Sessions.SetFlash(c, "new password and confirmation do not match")
		return c.Redirect("/profile")
	}
	if err := h.auth.ChangePassword(*uid, current, next); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect("/profile")
	}
	h.base.Sessions.SetFlash(c, "password updated successfully")
	return c.Redirect("/profile")
}

func (h *AuthHandler) ThemeUpdate(c *fiber.Ctx) error {
	uid := currentUserID(c)
	if uid == nil {
		return c.Redirect("/login")
	}
	theme := strings.TrimSpace(c.FormValue("theme"))
	if err := h.settings.SetUserTheme(*uid, theme, uid, c.IP()); err != nil {
		h.base.Sessions.SetFlash(c, err.Error())
		return c.Redirect(c.Get("Referer", "/"))
	}
	h.setThemeCookie(c, theme)
	return c.Redirect(c.Get("Referer", "/"))
}

func (h *AuthHandler) ThemeSwitch(c *fiber.Ctx) error {
	theme := normalizeTheme(strings.TrimSpace(c.FormValue("theme")))
	if theme == "" {
		return c.Redirect(c.Get("Referer", "/login"))
	}
	h.setThemeCookie(c, theme)
	return c.Redirect(c.Get("Referer", "/login"))
}

func (h *AuthHandler) setThemeCookie(c *fiber.Ctx, theme string) {
	safeTheme := normalizeTheme(theme)
	if safeTheme == "" {
		return
	}
	c.Cookie(&fiber.Cookie{
		Name:     themeCookieName,
		Value:    safeTheme,
		Path:     "/",
		HTTPOnly: false,
		Secure:   h.base.Config.Security.SessionSecureCookies,
		Expires:  time.Now().Add(365 * 24 * time.Hour),
	})
}

func normalizeTheme(raw any) string {
	theme := strings.TrimSpace(strings.ToLower(strings.TrimSpace(toString(raw))))
	if theme == "light" || theme == "dark" {
		return theme
	}
	return ""
}

func toString(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}
