package middleware

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

func LoginRateLimit(max int) fiber.Handler {
	if max <= 0 {
		max = 20
	}
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return fmt.Sprintf("%s:%s", c.IP(), c.Path())
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).SendString("Too many login attempts. Please try again in one minute.")
		},
	})
}
