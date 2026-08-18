package nginx

import (
	"strings"
	"testing"

	"deploycp/internal/config"
	"deploycp/internal/models"
)

func TestBuildWebsiteConfigUsesCertificateOnlyForMatchingDomain(t *testing.T) {
	site := &models.Website{
		Name:          "example",
		RootPath:      "/srv/example/htdocs",
		Type:          "static",
		Enabled:       true,
		AccessLogPath: "/srv/example/logs/access.log",
		ErrorLogPath:  "/srv/example/logs/error.log",
		Domains: []models.WebsiteDomain{
			{Domain: "secure.example.com", Primary: true},
			{Domain: "plain.example.com"},
		},
	}
	generated := BuildWebsiteConfig(&config.Config{}, site, WebsiteConfigOptions{
		Certificates: map[string]*models.SSLCertificate{
			"secure.example.com": {
				Domain:   "secure.example.com",
				Status:   "active",
				CertPath: "/certs/secure.pem",
				KeyPath:  "/certs/secure.key",
			},
		},
	})

	if strings.Count(generated.Content, "listen 443 ssl http2;") != 1 {
		t.Fatalf("expected one TLS server:\n%s", generated.Content)
	}
	if !strings.Contains(generated.Content, "server_name secure.example.com;\n    access_log") ||
		!strings.Contains(generated.Content, "ssl_certificate /certs/secure.pem;") {
		t.Fatalf("secure domain did not receive its certificate:\n%s", generated.Content)
	}
	plainBlock := generated.Content[strings.LastIndex(generated.Content, "server {\n"):]
	if !strings.Contains(plainBlock, "server_name plain.example.com;") || strings.Contains(plainBlock, "ssl_certificate") {
		t.Fatalf("plain domain unexpectedly received TLS:\n%s", plainBlock)
	}
}

func TestBuildWebsiteConfigIgnoresInactiveCertificate(t *testing.T) {
	site := &models.Website{
		Name:          "example",
		RootPath:      "/srv/example/htdocs",
		Type:          "static",
		Enabled:       true,
		AccessLogPath: "/srv/example/logs/access.log",
		ErrorLogPath:  "/srv/example/logs/error.log",
		Domains:       []models.WebsiteDomain{{Domain: "example.com", Primary: true}},
	}
	generated := BuildWebsiteConfig(&config.Config{}, site, WebsiteConfigOptions{
		Certificates: map[string]*models.SSLCertificate{
			"example.com": {Domain: "example.com", Status: "pending", CertPath: "/cert.pem", KeyPath: "/key.pem"},
		},
	})
	if strings.Contains(generated.Content, "listen 443") || strings.Contains(generated.Content, "return 301 https://") {
		t.Fatalf("inactive certificate enabled HTTPS:\n%s", generated.Content)
	}
}

func TestBuildWebsiteConfigUsesStructuredUploadLimitOnlyOncePerServer(t *testing.T) {
	site := &models.Website{
		Name:              "example",
		RootPath:          "/srv/example/htdocs",
		Type:              "static",
		Enabled:           true,
		ClientMaxBodySize: "1G",
		CustomDirectives:  "client_max_body_size 8M;\nadd_header X-Platform example;",
		AccessLogPath:     "/srv/example/logs/access.log",
		ErrorLogPath:      "/srv/example/logs/error.log",
		Domains:           []models.WebsiteDomain{{Domain: "example.com", Primary: true}},
	}
	generated := BuildWebsiteConfig(&config.Config{}, site, WebsiteConfigOptions{})

	if got := strings.Count(generated.Content, "client_max_body_size"); got != 1 {
		t.Fatalf("expected one generated upload limit, got %d:\n%s", got, generated.Content)
	}
	if !strings.Contains(generated.Content, "client_max_body_size 1G;") {
		t.Fatalf("structured upload limit missing:\n%s", generated.Content)
	}
	if strings.Contains(generated.Content, "client_max_body_size 8M;") {
		t.Fatalf("legacy custom upload limit was not removed:\n%s", generated.Content)
	}
}
