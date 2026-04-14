package utils

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Platform kinds used for routing canonical platform pages.
const (
	PlatformKindWebsite = "website"
	PlatformKindApp     = "app"
)

func normalizePlatformKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "website", "site", "w":
		return PlatformKindWebsite, true
	case "app", "application", "a":
		return PlatformKindApp, true
	default:
		return "", false
	}
}

// EncodePlatformRef creates a stable opaque route token for a platform item.
func EncodePlatformRef(kind string, id uint) string {
	k, ok := normalizePlatformKind(kind)
	if !ok || id == 0 {
		return ""
	}
	raw := fmt.Sprintf("%s:%d", k, id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodePlatformRef parses a token created by EncodePlatformRef.
func DecodePlatformRef(ref string) (kind string, id uint, err error) {
	if strings.TrimSpace(ref) == "" {
		return "", 0, fmt.Errorf("platform ref is empty")
	}
	buf, decErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(ref))
	if decErr != nil {
		return "", 0, fmt.Errorf("invalid platform ref")
	}
	parts := strings.SplitN(string(buf), ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid platform ref payload")
	}
	k, ok := normalizePlatformKind(parts[0])
	if !ok {
		return "", 0, fmt.Errorf("invalid platform kind")
	}
	v, parseErr := strconv.ParseUint(parts[1], 10, 64)
	if parseErr != nil || v == 0 {
		return "", 0, fmt.Errorf("invalid platform id")
	}
	return k, uint(v), nil
}

// PlatformShowURL returns the canonical manage URL for a platform item.
func PlatformShowURL(kind string, id uint) string {
	ref := EncodePlatformRef(kind, id)
	if ref == "" {
		return "/platforms"
	}
	return "/platforms/" + ref
}

// PlatformShowURLWithAnchor returns canonical manage URL with a tab/hash anchor.
func PlatformShowURLWithAnchor(kind string, id uint, anchor string) string {
	base := PlatformShowURL(kind, id)
	a := strings.TrimSpace(strings.TrimPrefix(anchor, "#"))
	if a == "" {
		return base
	}
	return base + "#" + a
}
