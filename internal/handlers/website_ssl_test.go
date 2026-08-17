package handlers

import (
	"testing"

	"deploycp/internal/models"
)

func TestApplyWebsiteDomainSSLStateIsPerDomain(t *testing.T) {
	site := &models.Website{Domains: []models.WebsiteDomain{
		{Domain: "secure.example.com", Primary: true},
		{Domain: "plain.example.com"},
	}}
	certs := []models.SSLCertificate{
		{Domain: "secure.example.com", Status: "active", CertPath: "/cert.pem", KeyPath: "/key.pem"},
	}
	applyWebsiteDomainSSLState(site, certs)
	if !site.Domains[0].SSLReady {
		t.Fatal("matching domain should be HTTPS-ready")
	}
	if site.Domains[1].SSLReady {
		t.Fatal("domain without a certificate must remain HTTP")
	}
}
