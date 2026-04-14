package utils

import (
	"fmt"
	"path/filepath"
	"strings"
)

func CleanPath(path string) string {
	return filepath.Clean(strings.TrimSpace(path))
}

func ValidatePathWithin(base, target string) error {
	baseC := filepath.Clean(base)
	targetC := filepath.Clean(target)
	rel, err := filepath.Rel(baseC, targetC)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, "..") || strings.Contains(rel, "../") {
		return fmt.Errorf("path %s escapes base %s", target, base)
	}
	return nil
}

func SplitLinesComma(v string) []string {
	items := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}
