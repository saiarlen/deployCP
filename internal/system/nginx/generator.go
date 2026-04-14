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

func BuildWebsiteConfig(cfg *config.Config, site *models.Website) GeneratedConfig {
	domains := make([]string, 0, len(site.Domains))
	for _, d := range site.Domains {
		domains = append(domains, d.Domain)
	}
	if len(domains) == 0 {
		domains = []string{"_"}
	}
	serverNames := strings.Join(domains, " ")

	body := strings.Builder{}
	body.WriteString("server {\n")
	body.WriteString("    listen 80;\n")
	body.WriteString(fmt.Sprintf("    server_name %s;\n", serverNames))
	body.WriteString(fmt.Sprintf("    access_log %s;\n", site.AccessLogPath))
	body.WriteString(fmt.Sprintf("    error_log %s warn;\n", site.ErrorLogPath))
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
	body.WriteString("}\n")

	configPath := filepath.Join(cfg.Paths.NginxAvailableDir, site.Name+".conf")
	enabledPath := filepath.Join(cfg.Paths.NginxEnabledDir, site.Name+".conf")
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(body.String())))

	return GeneratedConfig{Content: body.String(), ConfigPath: configPath, EnabledPath: enabledPath, Checksum: checksum}
}

func phpFPMSocketPath(version string) string {
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("/opt/homebrew/var/run/php@%s-fpm.sock", version)
	}
	return fmt.Sprintf("/run/php/php%s-fpm.sock", version)
}
