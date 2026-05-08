package services

import (
	"strings"
	"testing"

	"deploycp/internal/config"
)

func TestAppSystemdServiceNameUsesStableDomainSlug(t *testing.T) {
	tests := map[string]string{
		"cpl.appxcube.in":       "deploycp-app-cpl-appxcube-in",
		"  Money Bloom CL  ":    "deploycp-app-money-bloom-cl",
		"app_test.example.com":  "deploycp-app-app-test-example-com",
		"***":                   "deploycp-app-platform",
		"CAPS.Domain.EXAMPLE":   "deploycp-app-caps-domain-example",
		"site--with__symbols..": "deploycp-app-site-with-symbols",
	}

	for input, want := range tests {
		if got := appSystemdServiceName(input); got != want {
			t.Fatalf("appSystemdServiceName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSiteUserRuntimeSudoersContentAllowsOnlyRuntimeServiceActions(t *testing.T) {
	cfg := &config.Config{}
	cfg.Paths.SystemctlBinary = "/bin/systemctl"

	content := siteUserRuntimeSudoersContent(cfg, "cpluser", "deploycp-app-cpl-appxcube-in")
	required := []string{
		"cpluser ALL=(root) NOPASSWD:",
		"/bin/systemctl start deploycp-app-cpl-appxcube-in",
		"/bin/systemctl stop deploycp-app-cpl-appxcube-in",
		"/bin/systemctl restart deploycp-app-cpl-appxcube-in",
		"/bin/systemctl status deploycp-app-cpl-appxcube-in",
		"/bin/systemctl is-active deploycp-app-cpl-appxcube-in",
		"/usr/bin/systemctl restart deploycp-app-cpl-appxcube-in.service",
	}
	for _, item := range required {
		if !strings.Contains(content, item) {
			t.Fatalf("sudoers content missing %q:\n%s", item, content)
		}
	}
	if strings.Contains(content, " enable ") || strings.Contains(content, " disable ") {
		t.Fatalf("sudoers content should not grant enable/disable:\n%s", content)
	}
}
