package views

import (
	"bytes"
	"strings"
	"testing"

	"deploycp/internal/config"
	"deploycp/internal/models"
)

func TestTemplatesLoad(t *testing.T) {
	engine := NewEngine(&config.Config{})
	if err := engine.Load(); err != nil {
		t.Fatalf("load templates: %v", err)
	}
}

func TestPlatformsIndexRendersPerDomainSchemes(t *testing.T) {
	engine := NewEngine(&config.Config{})
	if err := engine.Load(); err != nil {
		t.Fatal(err)
	}
	websites := []models.Website{{
		ID:      1,
		Name:    "example",
		Type:    "static",
		Enabled: true,
		Domains: []models.WebsiteDomain{
			{Domain: "secure.example.com", Primary: true, SSLReady: true},
			{Domain: "plain.example.com", SSLReady: false},
		},
	}}
	var rendered bytes.Buffer
	if err := engine.Render(&rendered, "platforms_index", map[string]any{
		"Websites": websites,
		"Apps":     []models.GoApp{},
	}); err != nil {
		t.Fatal(err)
	}
	content := rendered.String()
	for _, want := range []string{
		`href="https://secure.example.com/"`,
		`href="http://plain.example.com/"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered platform links missing %q", want)
		}
	}
}
