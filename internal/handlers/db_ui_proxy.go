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
	protected := make(map[string]struct{}, len(fallbackQuery))
	for key := range fallbackQuery {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			protected[key] = struct{}{}
		}
	}
	if raw := string(c.Context().QueryArgs().QueryString()); strings.TrimSpace(raw) != "" {
		if incoming, err := url.ParseQuery(raw); err == nil {
			for key, values := range incoming {
				if _, locked := protected[strings.ToLower(strings.TrimSpace(key))]; locked {
					continue
				}
				q.Del(key)
				for _, value := range values {
					q.Add(key, value)
				}
			}
			for key, values := range fallbackQuery {
				key = strings.TrimSpace(key)
				if key == "" {
					continue
				}
				q.Del(key)
				for _, value := range values {
					q.Add(key, value)
				}
			}
			target.RawQuery = q.Encode()
		} else {
			for key, values := range fallbackQuery {
				q.Del(key)
				for _, value := range values {
					q.Add(key, value)
				}
			}
			target.RawQuery = q.Encode()
		}
	} else if fallbackQuery != nil {
		for key, values := range fallbackQuery {
			q.Del(key)
			for _, value := range values {
				q.Add(key, value)
			}
		}
		target.RawQuery = q.Encode()
	}
	return fiberproxy.Do(c, target.String())
}
