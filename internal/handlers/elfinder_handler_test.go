package handlers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestElfinderRejectsSymlinkTargetOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}

	if err := ensureExistingPathWithinRoot(root, link); err == nil {
		t.Fatal("expected symlink outside root to be rejected")
	}
}

func TestElfinderRejectsCreateThroughSymlinkDirectoryOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	linkDir := filepath.Join(root, "outside")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatal(err)
	}

	if err := ensureCreatablePathWithinRoot(root, filepath.Join(linkDir, "new.txt")); err == nil {
		t.Fatal("expected create through symlink directory outside root to be rejected")
	}
}

func TestElfinderAllowsSymlinkTargetInsideRoot(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	if err := ensureCreatablePathWithinRoot(root, filepath.Join(linkDir, "new.txt")); err != nil {
		t.Fatalf("expected symlink inside root to be allowed: %v", err)
	}
}
