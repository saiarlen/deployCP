package handlers

import (
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	fiberproxy "github.com/gofiber/fiber/v2/middleware/proxy"
)

func proxyToolRequest(c *fiber.Ctx, baseURL string, fallbackQuery url.Values) error {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return fiber.NewError(fiber.StatusBadGateway, "database UI is not configured")
	}
	target, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	q := target.Query()
	if raw := string(c.Context().QueryArgs().QueryString()); strings.TrimSpace(raw) != "" {
		target.RawQuery = raw
	} else if fallbackQuery != nil {
		for key, values := range fallbackQuery {
			for _, value := range values {
				q.Add(key, value)
			}
		}
		target.RawQuery = q.Encode()
	}
	return fiberproxy.Do(c, target.String())
}
