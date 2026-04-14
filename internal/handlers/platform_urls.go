package handlers

import (
	"net/url"
	"strings"

	"deploycp/internal/models"
	"deploycp/internal/utils"
)

func platformURL(kind string, id uint) string {
	return utils.PlatformShowURL(kind, id)
}

func platformURLWithTab(kind string, id uint, tab string) string {
	return utils.PlatformShowURLWithAnchor(kind, id, tab)
}

func platformKindFromReferer(ref string) string {
	if strings.TrimSpace(ref) == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(u.Path)
	if !strings.HasPrefix(path, "/platforms/") {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/platforms/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return ""
	}
	kind, _, err := utils.DecodePlatformRef(parts[0])
	if err != nil {
		return ""
	}
	return kind
}

func primaryWebsiteDomain(domains []models.WebsiteDomain) string {
	for _, d := range domains {
		domain := strings.TrimSpace(d.Domain)
		if d.Primary && domain != "" {
			return domain
		}
	}
	for _, d := range domains {
		domain := strings.TrimSpace(d.Domain)
		if domain != "" {
			return domain
		}
	}
	return ""
}
