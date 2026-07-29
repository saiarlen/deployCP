package services

import (
	"testing"

	"deploycp/internal/models"
)

func TestRuleCoversActiveSSHPort(t *testing.T) {
	ports := map[string]struct{}{"7722": {}}
	rule := &models.PanelFirewallRule{
		Protocol: "tcp",
		Port:     "7722",
		Source:   "0.0.0.0/0",
		Action:   "allow",
		Enabled:  true,
	}
	if !ruleCoversActiveSSHPort(rule, ports) {
		t.Fatal("expected tcp allow rule for active SSH port to be protected")
	}

	rule.Port = "7000:8000"
	if !ruleCoversActiveSSHPort(rule, ports) {
		t.Fatal("expected port range containing active SSH port to be protected")
	}

	rule.Port = "22"
	if ruleCoversActiveSSHPort(rule, ports) {
		t.Fatal("did not expect a different port to be protected")
	}
}

func TestRuleCoversActiveSSHPortRequiresPublicTCPRule(t *testing.T) {
	ports := map[string]struct{}{"7722": {}}
	cases := []models.PanelFirewallRule{
		{Protocol: "udp", Port: "7722", Source: "0.0.0.0/0", Action: "allow", Enabled: true},
		{Protocol: "tcp", Port: "7722", Source: "192.0.2.10", Action: "allow", Enabled: true},
		{Protocol: "tcp", Port: "7722", Source: "0.0.0.0/0", Action: "allow", Enabled: false},
	}
	for _, rule := range cases {
		if ruleCoversActiveSSHPort(&rule, ports) {
			t.Fatalf("did not expect rule to be protected: %+v", rule)
		}
	}
}

func TestSameFirewallHostEffectAllowsMetadataEdits(t *testing.T) {
	a := &models.PanelFirewallRule{
		Name:        "ssh",
		Protocol:    "tcp",
		Port:        "7722",
		Source:      "any",
		Action:      "allow",
		Description: "old",
		Enabled:     true,
	}
	b := &models.PanelFirewallRule{
		Name:        "main ssh",
		Protocol:    "tcp",
		Port:        "7722",
		Source:      "0.0.0.0/0",
		Action:      "ALLOW",
		Description: "new",
		Enabled:     true,
	}
	if !sameFirewallHostEffect(a, b) {
		t.Fatal("expected metadata-only firewall rule edit to preserve host effect")
	}

	b.Port = "22"
	if sameFirewallHostEffect(a, b) {
		t.Fatal("expected port change to alter host effect")
	}
}

func TestSourceAllowsIP(t *testing.T) {
	cases := []struct {
		source    string
		requester string
		want      bool
	}{
		{source: "203.0.113.10", requester: "203.0.113.10", want: true},
		{source: "203.0.113.0/24", requester: "203.0.113.10", want: true},
		{source: "203.0.113.0/24", requester: "198.51.100.20", want: false},
		{source: "any", requester: "198.51.100.20", want: true},
		{source: "203.0.113.10", requester: "", want: false},
	}
	for _, tc := range cases {
		if got := sourceAllowsIP(tc.source, tc.requester); got != tc.want {
			t.Fatalf("sourceAllowsIP(%q, %q) = %v, want %v", tc.source, tc.requester, got, tc.want)
		}
	}
}

func TestNarrowedRuleUsesActiveSSHPortAndRequesterIP(t *testing.T) {
	rule := &models.PanelFirewallRule{
		Protocol: "tcp",
		Port:     "7722",
		Source:   "203.0.113.10",
		Action:   "allow",
		Enabled:  true,
	}
	if !ruleUsesActiveSSHPort(rule, map[string]struct{}{"7722": {}}) {
		t.Fatal("expected narrowed rule to use active SSH port")
	}
	if !sourceAllowsIP(rule.Source, "203.0.113.10") {
		t.Fatal("expected narrowed rule to allow the current requester")
	}
	if sourceAllowsIP(rule.Source, "198.51.100.20") {
		t.Fatal("did not expect narrowed rule to allow a different requester")
	}
}

func TestAddSSHDPorts(t *testing.T) {
	ports := map[string]struct{}{}
	addSSHDPorts(ports, "port 7722\naddressfamily any\nport 2222\n")
	for _, port := range []string{"7722", "2222"} {
		if _, ok := ports[port]; !ok {
			t.Fatalf("expected port %s to be detected", port)
		}
	}
}
