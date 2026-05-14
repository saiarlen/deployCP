package handlers

import (
	"testing"
	"time"

	"deploycp/internal/config"
)

func TestDisplayServerAddressPrefersExternalPublicIPOverPrivateCandidates(t *testing.T) {
	publicIPv4Cache.Lock()
	publicIPv4Cache.value = "203.0.113.10"
	publicIPv4Cache.checkedAt = time.Now()
	publicIPv4Cache.Unlock()
	defer func() {
		publicIPv4Cache.Lock()
		publicIPv4Cache.value = ""
		publicIPv4Cache.checkedAt = time.Time{}
		publicIPv4Cache.Unlock()
	}()

	cfg := &config.Config{}
	cfg.App.Host = "10.0.0.5"
	cfg.App.BaseURL = "http://localhost:2024"

	if got := displayServerAddress(cfg, "192.168.1.10"); got != "203.0.113.10" {
		t.Fatalf("displayServerAddress() = %q, want cached public IP", got)
	}
}

func TestDisplayServerAddressUsesConfiguredPublicIP(t *testing.T) {
	cfg := &config.Config{}
	cfg.App.Host = "198.51.100.20"
	cfg.App.BaseURL = "http://localhost:2024"

	if got := displayServerAddress(cfg, "127.0.0.1"); got != "198.51.100.20" {
		t.Fatalf("displayServerAddress() = %q, want configured public IP", got)
	}
}

func TestDisplayServerAddressUsesPublicBaseURLBeforePrivateInterfaceFallback(t *testing.T) {
	publicIPv4Cache.Lock()
	publicIPv4Cache.value = "Unavailable"
	publicIPv4Cache.checkedAt = time.Now()
	publicIPv4Cache.Unlock()
	defer func() {
		publicIPv4Cache.Lock()
		publicIPv4Cache.value = ""
		publicIPv4Cache.checkedAt = time.Time{}
		publicIPv4Cache.Unlock()
	}()

	cfg := &config.Config{}
	cfg.App.Host = "0.0.0.0"
	cfg.App.BaseURL = "https://panel.example.com"

	if got := displayServerAddress(cfg, "192.168.1.10"); got != "panel.example.com" {
		t.Fatalf("displayServerAddress() = %q, want configured public hostname", got)
	}
}
