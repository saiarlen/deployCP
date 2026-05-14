package services

import (
	"path/filepath"
	"testing"

	"deploycp/internal/config"
)

func TestManagedRuntimeVersionFromBinaryUsesRuntimeDirectoryVersion(t *testing.T) {
	root := t.TempDir()
	service := NewRuntimeService(&config.Config{
		Paths: config.PathsConfig{RuntimeRoot: root},
	}, nil, nil)

	binary := filepath.Join(root, "php", "8.5", "bin", "php")
	if got := service.managedRuntimeVersionFromBinary("php", binary); got != "8.5" {
		t.Fatalf("managedRuntimeVersionFromBinary() = %q, want 8.5", got)
	}
}
