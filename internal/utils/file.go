package utils

import (
	"fmt"
	"os"
	"path/filepath"
)

func WriteFileAtomic(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return fmt.Errorf("write tmp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename tmp file: %w", err)
	}
	return nil
}
