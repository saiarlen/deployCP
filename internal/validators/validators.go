package validators

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	domainRe = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
	userRe   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{2,31}$`)
	phpRe    = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)
)

func Require(v, field string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func ValidateDomains(domains []string) error {
	if len(domains) == 0 {
		return fmt.Errorf("at least one domain is required")
	}
	for _, d := range domains {
		if !domainRe.MatchString(d) {
			return fmt.Errorf("invalid domain: %s", d)
		}
	}
	return nil
}

func ValidatePort(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return n, nil
}

func ValidateIPAddress(v string) error {
	if net.ParseIP(strings.TrimSpace(v)) == nil {
		return fmt.Errorf("invalid IP address")
	}
	return nil
}

func ValidatePath(path string) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return fmt.Errorf("path is required")
	}
	if strings.Contains(p, "\x00") {
		return fmt.Errorf("path contains invalid bytes")
	}
	if filepath.Clean(p) == "." {
		return fmt.Errorf("invalid path")
	}
	return nil
}

func ValidateUsername(username string) error {
	if !userRe.MatchString(strings.TrimSpace(username)) {
		return fmt.Errorf("username must match %s", userRe.String())
	}
	return nil
}

func ValidatePHPVersion(version string) error {
	v := strings.TrimSpace(version)
	if v == "" {
		return fmt.Errorf("php version is required")
	}
	if !phpRe.MatchString(v) {
		return fmt.Errorf("unsupported php version: %s", v)
	}
	return nil
}
