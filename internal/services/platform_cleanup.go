package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"deploycp/internal/config"
)

func removeTreeSafe(path string, protected ...string) error {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." || clean == string(filepath.Separator) {
		return fmt.Errorf("refusing to delete unsafe path: %q", path)
	}
	for _, p := range protected {
		pclean := filepath.Clean(strings.TrimSpace(p))
		if pclean == "" || pclean == "." {
			continue
		}
		if clean == pclean {
			return fmt.Errorf("refusing to delete protected path: %s", clean)
		}
	}
	if err := os.RemoveAll(clean); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func inferServiceUnitPath(cfg *config.Config, platformName, serviceName string) string {
	switch platformName {
	case "darwin":
		return filepath.Join(cfg.Paths.PlistDir, serviceName+".plist")
	case "dryrun":
		return filepath.Join(cfg.Paths.StorageRoot, "generated", serviceName+".service")
	default:
		return filepath.Join("/etc/systemd/system", serviceName+".service")
	}
}

func removeServiceUnitFile(cfg *config.Config, platformName, serviceName, unitPath string) error {
	path := strings.TrimSpace(unitPath)
	if path == "" {
		path = inferServiceUnitPath(cfg, platformName, serviceName)
	}
	path = filepath.Clean(path)
	if path == "" || path == "." || path == string(filepath.Separator) {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
