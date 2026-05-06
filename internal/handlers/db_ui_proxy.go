package handlers

import (
	"bytes"
	"fmt"
	ht "html/template"
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
	target, err := buildProxyTarget(c, baseURL, fallbackQuery)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(c.Method(), target.String(), bytes.NewReader(c.Body()))
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, err.Error())
	}
	copyRequestHeaders(c, req)
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, fmt.Sprintf("database UI helper response failed: %v", err))
	}
	return c.Send(body)
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
	rewritten := &url.URL{
		Path:     strings.TrimRight(basePath, "/") + loc.Path,
		RawQuery: loc.RawQuery,
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

func renderAdminerAutoLogin(c *fiber.Ctx, title string, actionPath string, fields url.Values) error {
	c.Type("html", "utf-8")
	var body strings.Builder
	body.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	body.WriteString("<title>")
	body.WriteString(ht.HTMLEscapeString(title))
	body.WriteString("</title>")
	body.WriteString("<style>body{font-family:system-ui,-apple-system,sans-serif;background:#f5f7fb;color:#111827;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;padding:24px}.card{background:#fff;border:1px solid #d8deea;border-radius:16px;padding:24px;max-width:460px;width:100%;box-shadow:0 20px 40px rgba(15,23,42,.08)}h1{font-size:18px;margin:0 0 8px}p{margin:0 0 16px;color:#475569;line-height:1.5}button{background:#111827;color:#fff;border:0;border-radius:10px;padding:10px 14px;font:inherit;cursor:pointer}</style>")
	body.WriteString("</head><body><div class=\"card\"><h1>")
	body.WriteString(ht.HTMLEscapeString(title))
	body.WriteString("</h1><p>Signing in to the selected database UI.</p>")
	body.WriteString("<form id=\"db-ui-login\" method=\"post\" action=\"")
	body.WriteString(ht.HTMLEscapeString(actionPath))
	body.WriteString("\">")
	for key, values := range fields {
		escapedKey := ht.HTMLEscapeString(key)
		for _, value := range values {
			body.WriteString("<input type=\"hidden\" name=\"")
			body.WriteString(escapedKey)
			body.WriteString("\" value=\"")
			body.WriteString(ht.HTMLEscapeString(value))
			body.WriteString("\">")
		}
	}
	body.WriteString("<noscript><button type=\"submit\">Continue</button></noscript></form><script>document.getElementById('db-ui-login').submit()</script></div></body></html>")
	return c.SendString(body.String())
}
