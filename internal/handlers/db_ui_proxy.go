package handlers

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberproxy "github.com/gofiber/fiber/v2/middleware/proxy"
)

func ensureToolReachable(baseURL string) error {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return fmt.Errorf("database UI is not configured")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return err
	}
	host := strings.TrimSpace(u.Host)
	if host == "" {
		return fmt.Errorf("database UI host is not configured")
	}
	if !strings.Contains(host, ":") {
		switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
		case "https":
			host += ":443"
		default:
			host += ":80"
		}
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return fmt.Errorf("database UI helper is not reachable at %s", host)
	}
	_ = conn.Close()
	return nil
}

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
