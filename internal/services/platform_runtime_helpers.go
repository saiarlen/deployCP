package services

import (
	"path/filepath"
	"strings"

	"deploycp/internal/models"
)

func platformHomeFromPath(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if filepath.Base(clean) == "htdocs" {
		return filepath.Dir(clean)
	}
	return clean
}

func platformRuntimeRootForApp(app *models.GoApp) string {
	if app == nil {
		return ""
	}
	root := strings.TrimSpace(app.WorkingDirectory)
	if app.WebsiteID != nil && *app.WebsiteID > 0 {
		return platformHomeFromPath(root)
	}
	return filepath.Clean(root)
}

func appEnvValue(envVars []models.AppEnvVar, key string) string {
	key = strings.TrimSpace(key)
	for _, item := range envVars {
		if strings.EqualFold(strings.TrimSpace(item.Key), key) {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}
