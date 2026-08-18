package nginx

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
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

// Cloudflare IP ranges (IPv4 + IPv6) — used for real IP restoration.
// Source: https://www.cloudflare.com/ips/
var cloudflareIPs = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

var clientMaxBodySizePattern = regexp.MustCompile(`^[1-9][0-9]{0,5}([kKmMgG])?$`)
var clientMaxBodySizeDirectivePattern = regexp.MustCompile(`(?i)^\s*client_max_body_size(?:\s|$)`)

type WebsiteConfigOptions struct {
	Certificates      map[string]*models.SSLCertificate
	BasicAuth         *models.BasicAuth
	BasicAuthPath     string
	IPBlocks          []models.IPBlock
	BotBlocks         []models.BotBlock
	CloudflareEnabled bool
}

func BuildWebsiteConfig(cfg *config.Config, site *models.Website, opts WebsiteConfigOptions) GeneratedConfig {
	domains := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		domains = append(domains, d.Domain)
	}
	if len(domains) == 0 {
		domains = []string{"_"}
	}
	body := strings.Builder{}
	body.WriteString("# Managed by DeployCP. Do not edit.\n")
	for index, domain := range domains {
		if index > 0 {
			body.WriteString("\n")
		}
		cert := opts.Certificates[strings.ToLower(strings.TrimSpace(domain))]
		hasCert := usableCertificate(cert)
		body.WriteString("server {\n")
		body.WriteString("    listen 80;\n")
		body.WriteString(fmt.Sprintf("    server_name %s;\n", domain))
		body.WriteString(fmt.Sprintf("    access_log %s;\n", site.AccessLogPath))
		body.WriteString(fmt.Sprintf("    error_log %s warn;\n", site.ErrorLogPath))
		body.WriteString("    location ^~ /.well-known/acme-challenge/ {\n")
		body.WriteString(fmt.Sprintf("        root %s;\n", site.RootPath))
		body.WriteString("        allow all;\n")
		body.WriteString("    }\n")
		if hasCert {
			body.WriteString("    location / {\n")
			body.WriteString("        return 301 https://$host$request_uri;\n")
			body.WriteString("    }\n")
		} else {
			renderServerContent(&body, site, opts)
		}
		body.WriteString("}\n")

		if hasCert {
			body.WriteString("\nserver {\n")
			body.WriteString("    listen 443 ssl http2;\n")
			body.WriteString(fmt.Sprintf("    server_name %s;\n", domain))
			body.WriteString(fmt.Sprintf("    access_log %s;\n", site.AccessLogPath))
			body.WriteString(fmt.Sprintf("    error_log %s warn;\n", site.ErrorLogPath))
			body.WriteString(fmt.Sprintf("    ssl_certificate %s;\n", cert.CertPath))
			body.WriteString(fmt.Sprintf("    ssl_certificate_key %s;\n", cert.KeyPath))
			body.WriteString("    ssl_session_cache shared:deploycp_ssl:10m;\n")
			body.WriteString("    ssl_session_timeout 10m;\n")
			renderServerContent(&body, site, opts)
			body.WriteString("}\n")
		}
	}

	configPath := filepath.Join(cfg.Paths.NginxAvailableDir, site.Name+".conf")
	enabledPath := filepath.Join(cfg.Paths.NginxEnabledDir, site.Name+".conf")
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(body.String())))

	return GeneratedConfig{Content: body.String(), ConfigPath: configPath, EnabledPath: enabledPath, Checksum: checksum}
}

func usableCertificate(cert *models.SSLCertificate) bool {
	return cert != nil && strings.EqualFold(strings.TrimSpace(cert.Status), "active") &&
		strings.TrimSpace(cert.CertPath) != "" && strings.TrimSpace(cert.KeyPath) != ""
}

func renderServerContent(body *strings.Builder, site *models.Website, opts WebsiteConfigOptions) {
	if opts.CloudflareEnabled {
		for _, ip := range cloudflareIPs {
			body.WriteString(fmt.Sprintf("    set_real_ip_from %s;\n", ip))
		}
		body.WriteString("    real_ip_header CF-Connecting-IP;\n")
		body.WriteString("    real_ip_recursive on;\n")
	}
	if limit := strings.TrimSpace(site.ClientMaxBodySize); clientMaxBodySizePattern.MatchString(limit) {
		body.WriteString(fmt.Sprintf("    client_max_body_size %s;\n", limit))
	}
	body.WriteString("    location ~ /\\. { deny all; }\n")
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
	if !site.Enabled {
		body.WriteString(fmt.Sprintf("    root %s;\n", site.RootPath))
		body.WriteString("    error_page 403 =503 /_deploycp_maintenance.html;\n")
		body.WriteString("    error_page 503 /_deploycp_maintenance.html;\n")
		body.WriteString("    location = /_deploycp_maintenance.html {\n")
		body.WriteString("        internal;\n")
		body.WriteString("    }\n")
		renderMaintenanceProtectedContent(body, site)
		return
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
		body.WriteString("        proxy_set_header X-Forwarded-Host $host;\n")
		body.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
		body.WriteString("        proxy_set_header X-Forwarded-Port $server_port;\n")
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
		body.WriteString("        fastcgi_param HTTPS $https if_not_empty;\n")
		body.WriteString("        fastcgi_param HTTP_X_FORWARDED_PROTO $scheme;\n")
		body.WriteString("        fastcgi_param HTTP_X_FORWARDED_HOST $host;\n")
		body.WriteString("        fastcgi_param HTTP_X_FORWARDED_PORT $server_port;\n")
		body.WriteString("        fastcgi_index index.php;\n")
		body.WriteString(fmt.Sprintf("        fastcgi_pass unix:%s;\n", phpFPMSocketPath(phpVersion)))
		body.WriteString("    }\n")
	} else {
		body.WriteString(fmt.Sprintf("    root %s;\n", site.RootPath))
		body.WriteString("    index index.html index.htm;\n")
		body.WriteString("    error_page 404 /_deploycp_404.html;\n")
		body.WriteString("    location = /_deploycp_404.html {\n")
		body.WriteString("        internal;\n")
		body.WriteString("    }\n")
		body.WriteString("    location / {\n")
		body.WriteString("        try_files $uri $uri/ =404;\n")
		body.WriteString("    }\n")
	}
	if strings.TrimSpace(site.CustomDirectives) != "" {
		body.WriteString("\n    # Custom directives\n")
		for _, line := range strings.Split(site.CustomDirectives, "\n") {
			if strings.TrimSpace(line) == "" || clientMaxBodySizeDirectivePattern.MatchString(line) {
				continue
			}
			body.WriteString("    " + line + "\n")
		}
	}
}

func renderMaintenanceProtectedContent(body *strings.Builder, site *models.Website) {
	bypass := strings.FieldsFunc(site.MaintenanceBypassIPs, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' })
	writeAllowDeny := func() {
		for _, ip := range bypass {
			ip = strings.TrimSpace(ip)
			if ip == "" {
				continue
			}
			body.WriteString(fmt.Sprintf("        allow %s;\n", ip))
		}
		body.WriteString("        deny all;\n")
	}
	if site.Type == "proxy" && site.ProxyTarget != "" {
		body.WriteString("    location / {\n")
		writeAllowDeny()
		body.WriteString(fmt.Sprintf("        proxy_pass %s;\n", site.ProxyTarget))
		body.WriteString("        proxy_http_version 1.1;\n")
		body.WriteString("        proxy_set_header Host $host;\n")
		body.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
		body.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
		body.WriteString("        proxy_set_header X-Forwarded-Host $host;\n")
		body.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
		body.WriteString("        proxy_set_header X-Forwarded-Port $server_port;\n")
		body.WriteString("    }\n")
		return
	}
	if site.Type == "php" {
		phpVersion := strings.TrimSpace(site.PHPVersion)
		if phpVersion == "" {
			phpVersion = "8.2"
		}
		body.WriteString("    index index.php index.html index.htm;\n")
		body.WriteString("    location / {\n")
		writeAllowDeny()
		body.WriteString("        try_files $uri $uri/ /index.php?$query_string;\n")
		body.WriteString("    }\n")
		body.WriteString("    location ~ \\.php$ {\n")
		writeAllowDeny()
		body.WriteString("        include fastcgi_params;\n")
		body.WriteString("        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n")
		body.WriteString("        fastcgi_param HTTPS $https if_not_empty;\n")
		body.WriteString("        fastcgi_param HTTP_X_FORWARDED_PROTO $scheme;\n")
		body.WriteString("        fastcgi_param HTTP_X_FORWARDED_HOST $host;\n")
		body.WriteString("        fastcgi_param HTTP_X_FORWARDED_PORT $server_port;\n")
		body.WriteString("        fastcgi_index index.php;\n")
		body.WriteString(fmt.Sprintf("        fastcgi_pass unix:%s;\n", phpFPMSocketPath(phpVersion)))
		body.WriteString("    }\n")
		return
	}
	body.WriteString("    index index.html index.htm;\n")
	body.WriteString("    error_page 404 /_deploycp_404.html;\n")
	body.WriteString("    location = /_deploycp_404.html {\n")
	body.WriteString("        internal;\n")
	body.WriteString("    }\n")
	body.WriteString("    location / {\n")
	writeAllowDeny()
	body.WriteString("        try_files $uri $uri/ =404;\n")
	body.WriteString("    }\n")
}

func escapeNginxString(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return value
}

func phpFPMSocketPath(version string) string {
	version = phpFPMRuntimeVersion(version)
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("/opt/homebrew/var/run/php@%s-fpm.sock", version)
	}
	return fmt.Sprintf("/run/php/php%s-fpm.sock", version)
}

func phpFPMRuntimeVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return strings.TrimSpace(version)
}
