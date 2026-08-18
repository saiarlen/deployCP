package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/session"
	storage "github.com/gofiber/storage/sqlite3/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"deploycp/internal/repositories"
)

func TestAuthRequiredPreservesSessionWhenUserLookupIsTemporarilyUnavailable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "deploycp.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	repos := repositories.New(db)

	store := session.New(session.Config{
		Storage:    storage.New(storage.Config{Database: dbPath, Table: "fiber_sessions"}),
		KeyLookup:  "cookie:deploycp_session",
		CookiePath: "/",
	})
	sessions := NewSessionManager(store)
	seed := fiber.New()
	seed.Get("/", func(c *fiber.Ctx) error {
		return sessions.SetUserID(c, 42)
	})
	seedResponse, err := seed.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(seedResponse.Cookies()) != 1 {
		t.Fatalf("expected a session cookie, got %d", len(seedResponse.Cookies()))
	}
	cookie := seedResponse.Cookies()[0]

	// Closing only the application database simulates the short outage during a
	// database replacement or migration. The session store remains available.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Use(InjectAuthUser(sessions, repos.Users, nil))
	app.Get("/protected", AuthRequired(sessions), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(cookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.StatusCode, fiber.StatusServiceUnavailable)
	}

	verify := fiber.New()
	verify.Get("/", func(c *fiber.Ctx) error {
		uid, err := sessions.GetUserID(c)
		if err != nil {
			return err
		}
		if uid != 42 {
			return fiber.NewError(fiber.StatusUnauthorized, "session was cleared")
		}
		return c.SendStatus(fiber.StatusOK)
	})
	verifyRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	verifyRequest.AddCookie(cookie)
	verifyResponse, err := verify.Test(verifyRequest)
	if err != nil {
		t.Fatal(err)
	}
	if verifyResponse.StatusCode != fiber.StatusOK {
		t.Fatalf("session was not preserved: status = %d", verifyResponse.StatusCode)
	}
}
