package handlers

import (
	"strings"

	"deploycp/internal/models"
)

func envVarValue(envVars []models.AppEnvVar, key string) string {
	want := strings.TrimSpace(key)
	if want == "" {
		return ""
	}
	for _, ev := range envVars {
		if strings.EqualFold(strings.TrimSpace(ev.Key), want) {
			return strings.TrimSpace(ev.Value)
		}
	}
	return ""
}
