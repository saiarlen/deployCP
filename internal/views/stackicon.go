package views

import (
	"fmt"
	"strings"
)

// Devicon CDN base (pinned minor version for stable asset paths).
const deviconCDN = "https://cdn.jsdelivr.net/gh/devicons/devicon@2.16.0/icons/%s/%s-%s.svg"

// WebsiteStackIconURL returns a Devicon SVG URL for a website row (static / php / proxy + app runtime).
func WebsiteStackIconURL(websiteType, appRuntime string) string {
	t := strings.ToLower(strings.TrimSpace(websiteType))
	switch t {
	case "php":
		return deviconURL("php", "original")
	case "static":
		return deviconURL("html5", "original")
	case "proxy":
		r := strings.ToLower(strings.TrimSpace(appRuntime))
		if r == "" {
			return deviconURL("nginx", "original")
		}
		return runtimeDeviconURL(r)
	default:
		return deviconURL("html5", "original")
	}
}

// AppStackIconURL returns a Devicon SVG URL for an application row.
func AppStackIconURL(runtime string) string {
	r := strings.ToLower(strings.TrimSpace(runtime))
	if r == "" {
		return deviconURL("go", "original")
	}
	return runtimeDeviconURL(r)
}

func runtimeDeviconURL(r string) string {
	switch r {
	case "go":
		return deviconURL("go", "original")
	case "python":
		return deviconURL("python", "original")
	case "node":
		return deviconURL("nodejs", "original")
	case "php":
		return deviconURL("php", "original")
	case "binary":
		return deviconURL("cplusplus", "original")
	default:
		return deviconURL("cplusplus", "plain")
	}
}

func deviconURL(iconName, variant string) string {
	return fmt.Sprintf(deviconCDN, iconName, iconName, variant)
}
