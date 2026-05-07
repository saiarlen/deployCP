package handlers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"proxy-connection":    {},
}

func proxyToolRequest(c *fiber.Ctx, baseURL string, fallbackQuery url.Values) error {
	return proxyToolRequestWithHeaders(c, baseURL, fallbackQuery, nil)
}

func proxyToolRequestWithHeaders(c *fiber.Ctx, baseURL string, fallbackQuery url.Values, extraHeaders http.Header) error {
	target, err := buildProxyTarget(c, baseURL, fallbackQuery)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(c.Method(), target.String(), bytes.NewReader(c.Body()))
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, err.Error())
	}
	copyRequestHeaders(c, req)
	for key, values := range extraHeaders {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Close = true
	req.Host = target.Host

	client := &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, fmt.Sprintf("database UI helper request failed: %v", err))
	}
	defer resp.Body.Close()

	copyResponseHeaders(c, resp, target)
	c.Status(resp.StatusCode)
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, fmt.Sprintf("database UI helper response failed: %v", err))
	}
	return c.Send(responseBody)
}

func buildProxyTarget(c *fiber.Ctx, baseURL string, fallbackQuery url.Values) (*url.URL, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fiber.NewError(fiber.StatusBadGateway, "database UI is not configured")
	}
	target, err := url.Parse(baseURL)
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadGateway, err.Error())
	}
	target.Path = joinProxyPath(target.Path, c.Params("*"))
	if target.Path == "" {
		target.Path = "/"
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
				key = strings.TrimSpace(key)
				if key == "" {
					continue
				}
				if _, locked := protected[strings.ToLower(strings.TrimSpace(key))]; locked {
					continue
				}
				q.Del(key)
				for _, value := range values {
					q.Add(key, value)
				}
			}
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
	return target, nil
}

func joinProxyPath(basePath, suffix string) string {
	basePath = strings.TrimSpace(basePath)
	suffix = strings.TrimPrefix(strings.TrimSpace(suffix), "/")
	switch {
	case basePath == "" && suffix == "":
		return "/"
	case basePath == "":
		return "/" + suffix
	case suffix == "":
		if strings.HasPrefix(basePath, "/") {
			return basePath
		}
		return "/" + basePath
	default:
		return strings.TrimRight("/"+strings.TrimPrefix(basePath, "/"), "/") + "/" + suffix
	}
}

func copyRequestHeaders(c *fiber.Ctx, req *http.Request) {
	c.Request().Header.VisitAll(func(key, value []byte) {
		header := strings.ToLower(strings.TrimSpace(string(key)))
		if _, skip := hopByHopHeaders[header]; skip {
			return
		}
		if header == "x-deploycp-adminer-token" {
			return
		}
		req.Header.Add(string(key), string(value))
	})
	req.Header.Set("Connection", "close")
	req.Header.Del("Proxy-Connection")
}

func copyResponseHeaders(c *fiber.Ctx, resp *http.Response, target *url.URL) {
	for key, values := range resp.Header {
		if _, skip := hopByHopHeaders[strings.ToLower(key)]; skip {
			continue
		}
		if strings.EqualFold(key, "Location") && len(values) > 0 {
			c.Set(key, rewriteProxyLocation(values[0], c, target))
			continue
		}
		for _, value := range values {
			c.Append(key, value)
		}
	}
	c.Set("Connection", "close")
}

func rewriteProxyLocation(location string, c *fiber.Ctx, target *url.URL) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return location
	}
	loc, err := url.Parse(location)
	if err != nil {
		return location
	}
	basePath := proxyBasePath(c)
	if !loc.IsAbs() {
		query := loc.Query()
		query.Del("deploycp_token")
		if loc.RawQuery != "" {
			loc.RawQuery = query.Encode()
			location = loc.String()
		}
		switch {
		case strings.HasPrefix(location, "?"):
			return basePath + location
		case strings.HasPrefix(location, "/"):
			return strings.TrimRight(basePath, "/") + location
		default:
			return strings.TrimRight(basePath, "/") + "/" + strings.TrimPrefix(location, "/")
		}
	}
	if !strings.EqualFold(loc.Scheme, target.Scheme) || !strings.EqualFold(loc.Host, target.Host) {
		return location
	}
	query := loc.Query()
	query.Del("deploycp_token")
	rewritten := &url.URL{
		Path:     strings.TrimRight(basePath, "/") + loc.Path,
		RawQuery: query.Encode(),
		Fragment: loc.Fragment,
	}
	return rewritten.String()
}

func proxyBasePath(c *fiber.Ctx) string {
	path := c.Path()
	suffix := strings.TrimSpace(c.Params("*"))
	if suffix == "" {
		return path
	}
	suffix = "/" + strings.TrimPrefix(suffix, "/")
	if strings.HasSuffix(path, suffix) {
		return strings.TrimSuffix(path, suffix)
	}
	return path
}
