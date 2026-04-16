package nginx

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"deploycp/internal/config"
	"deploycp/internal/models"
)

type GeneratedConfig struct {
	Content     string
	ConfigPath  string
	EnabledPath string
	Checksum    string
}

type WebsiteConfigOptions struct {
	Certificate   *models.SSLCertificate
	BasicAuth     *models.BasicAuth
	BasicAuthPath string
	IPBlocks      []models.IPBlock
	BotBlocks     []models.BotBlock
}

func BuildWebsiteConfig(cfg *config.Config, site *models.Website, opts WebsiteConfigOptions) GeneratedConfig {
	domains := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		domains = append(domains, d.Domain)
	}
	if len(domains) == 0 {
		domains = []string{"_"}
	}
	serverNames := strings.Join(domains, " ")

	httpServer := strings.Builder{}
	httpServer.WriteString("server {\n")
	httpServer.WriteString("    listen 80;\n")
	httpServer.WriteString(fmt.Sprintf("    server_name %s;\n", serverNames))
	httpServer.WriteString(fmt.Sprintf("    access_log %s;\n", site.AccessLogPath))
	httpServer.WriteString(fmt.Sprintf("    error_log %s warn;\n", site.ErrorLogPath))
	httpServer.WriteString("    location ^~ /.well-known/acme-challenge/ {\n")
	httpServer.WriteString(fmt.Sprintf("        root %s;\n", site.RootPath))
	httpServer.WriteString("        allow all;\n")
	httpServer.WriteString("    }\n")
	if opts.Certificate != nil && strings.TrimSpace(opts.Certificate.CertPath) != "" && strings.TrimSpace(opts.Certificate.KeyPath) != "" {
		httpServer.WriteString("    location / {\n")
		httpServer.WriteString("        return 301 https://$host$request_uri;\n")
		httpServer.WriteString("    }\n")
		httpServer.WriteString("}\n\n")
	}

	body := strings.Builder{}
	if opts.Certificate == nil || strings.TrimSpace(opts.Certificate.CertPath) == "" || strings.TrimSpace(opts.Certificate.KeyPath) == "" {
		body.WriteString(httpServer.String())
		renderServerContent(&body, site, opts)
		body.WriteString("}\n")
	} else {
		body.WriteString(httpServer.String())
		body.WriteString("server {\n")
		body.WriteString("    listen 443 ssl http2;\n")
		body.WriteString(fmt.Sprintf("    server_name %s;\n", serverNames))
		body.WriteString(fmt.Sprintf("    access_log %s;\n", site.AccessLogPath))
		body.WriteString(fmt.Sprintf("    error_log %s warn;\n", site.ErrorLogPath))
		body.WriteString(fmt.Sprintf("    ssl_certificate %s;\n", opts.Certificate.CertPath))
		body.WriteString(fmt.Sprintf("    ssl_certificate_key %s;\n", opts.Certificate.KeyPath))
		body.WriteString("    ssl_session_cache shared:deploycp_ssl:10m;\n")
		body.WriteString("    ssl_session_timeout 10m;\n")
		renderServerContent(&body, site, opts)
		body.WriteString("}\n")
	}

	configPath := filepath.Join(cfg.Paths.NginxAvailableDir, site.Name+".conf")
	enabledPath := filepath.Join(cfg.Paths.NginxEnabledDir, site.Name+".conf")
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(body.String())))

	return GeneratedConfig{Content: body.String(), ConfigPath: configPath, EnabledPath: enabledPath, Checksum: checksum}
}

func renderServerContent(body *strings.Builder, site *models.Website, opts WebsiteConfigOptions) {
	for _, block := range opts.IPBlocks {
		if strings.TrimSpace(block.IP) == "" {
			continue
		}
		body.WriteString(fmt.Sprintf("    deny %s;\n", strings.TrimSpace(block.IP)))
	}
	if len(opts.IPBlocks) > 0 {
		body.WriteString("    allow all;\n")
	}
	for _, bot := range opts.BotBlocks {
		if strings.TrimSpace(bot.BotName) == "" {
			continue
		}
		body.WriteString(fmt.Sprintf("    if ($http_user_agent ~* \"%s\") { return 403; }\n", escapeNginxString(bot.BotName)))
	}
	if opts.BasicAuth != nil && opts.BasicAuth.Enabled && strings.TrimSpace(opts.BasicAuthPath) != "" {
		body.WriteString("    satisfy any;\n")
		for _, ip := range strings.FieldsFunc(opts.BasicAuth.WhitelistedIPs, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
			ip = strings.TrimSpace(ip)
			if ip == "" {
				continue
			}
			body.WriteString(fmt.Sprintf("    allow %s;\n", ip))
		}
		body.WriteString("    deny all;\n")
		body.WriteString("    auth_basic \"Restricted\";\n")
		body.WriteString(fmt.Sprintf("    auth_basic_user_file %s;\n", opts.BasicAuthPath))
	}
	if site.Type == "proxy" && site.ProxyTarget != "" {
		body.WriteString("    location / {\n")
		body.WriteString(fmt.Sprintf("        proxy_pass %s;\n", site.ProxyTarget))
		body.WriteString("        proxy_http_version 1.1;\n")
		body.WriteString("        proxy_set_header Host $host;\n")
		body.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
		body.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
		body.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
		body.WriteString("    }\n")
	} else if site.Type == "php" {
		phpVersion := strings.TrimSpace(site.PHPVersion)
		if phpVersion == "" {
			phpVersion = "8.2"
		}
		body.WriteString(fmt.Sprintf("    root %s;\n", site.RootPath))
		body.WriteString("    index index.php index.html index.htm;\n")
		body.WriteString("    location / {\n")
		body.WriteString("        try_files $uri $uri/ /index.php?$query_string;\n")
		body.WriteString("    }\n")
		body.WriteString("    location ~ \\.php$ {\n")
		body.WriteString("        include fastcgi_params;\n")
		body.WriteString("        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n")
		body.WriteString("        fastcgi_index index.php;\n")
		body.WriteString(fmt.Sprintf("        fastcgi_pass unix:%s;\n", phpFPMSocketPath(phpVersion)))
		body.WriteString("    }\n")
	} else {
		body.WriteString(fmt.Sprintf("    root %s;\n", site.RootPath))
		body.WriteString("    index index.html index.htm;\n")
		body.WriteString("    location / {\n")
		body.WriteString("        try_files $uri $uri/ /index.html;\n")
		body.WriteString("    }\n")
	}
	if strings.TrimSpace(site.CustomDirectives) != "" {
		body.WriteString("\n    # Custom directives\n")
		for _, line := range strings.Split(site.CustomDirectives, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			body.WriteString("    " + line + "\n")
		}
	}
}

func escapeNginxString(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return value
}

func phpFPMSocketPath(version string) string {
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("/opt/homebrew/var/run/php@%s-fpm.sock", version)
	}
	return fmt.Sprintf("/run/php/php%s-fpm.sock", version)
}
