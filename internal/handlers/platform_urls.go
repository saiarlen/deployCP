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
	kind, _, ok := platformRefFromReferer(ref)
	if !ok {
		return ""
	}
	return kind
}

func platformRefFromReferer(ref string) (kind string, id uint, ok bool) {
	if strings.TrimSpace(ref) == "" {
		return "", 0, false
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "", 0, false
	}
	path := strings.TrimSpace(u.Path)
	if !strings.HasPrefix(path, "/platforms/") {
		return "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/platforms/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", 0, false
	}
	kind, id, err = utils.DecodePlatformRef(parts[0])
	if err != nil {
		return "", 0, false
	}
	return kind, id, true
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
