package services

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateGitRefNameRejectsUnsafeRefs(t *testing.T) {
	valid := []string{"main", "release/2026-05", "feature.deploy"}
	for _, ref := range valid {
		if err := validateGitRefName(ref); err != nil {
			t.Fatalf("expected %q to be valid: %v", ref, err)
		}
	}

	invalid := []string{"", "-main", "../main", "feature..main", "main;rm", "main\nnext", "main lock"}
	for _, ref := range invalid {
		if err := validateGitRefName(ref); err == nil {
			t.Fatalf("expected %q to be rejected", ref)
		}
	}
}

func TestExtractTarGzSafeRejectsTraversal(t *testing.T) {
	tmp := t.TempDir()
	archivePath := filepath.Join(tmp, "bad.tar.gz")
	if err := writeTestArchive(archivePath, "../escape.txt", "owned"); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGzSafe(archivePath, filepath.Join(tmp, "dest")); err == nil {
		t.Fatal("expected traversal archive to be rejected")
	}
	if _, err := os.Stat(filepath.Join(tmp, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape file should not exist, stat err=%v", err)
	}
}

func TestWriteAndExtractTarGzRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dest := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(filepath.Join(src, "htdocs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "htdocs", "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(tmp, "backup.tar.gz")
	if err := writeTarGz(src, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGzSafe(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "htdocs", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("unexpected restored content %q", string(got))
	}
}

func TestWriteTarGzAddsMetadataAndSkipsDeployPrivateKey(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(filepath.Join(src, ".deploycp", "deploy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".deploycp", "deploy", "id_deploy"), []byte("private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(tmp, "manifest.json")
	if err := os.WriteFile(metaPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(tmp, "backup.tar.gz")
	if err := writeTarGz(src, archivePath, map[string]string{".deploycp-backup/manifest.json": metaPath}); err != nil {
		t.Fatal(err)
	}
	names, err := archiveNames(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !names[".deploycp-backup/manifest.json"] {
		t.Fatal("backup metadata manifest was not included")
	}
	if names[".deploycp/deploy/id_deploy"] {
		t.Fatal("deploy private key must not be archived")
	}
}

func TestExtractBackupMetadataOnlyExtractsDeployCPMetadata(t *testing.T) {
	tmp := t.TempDir()
	archivePath := filepath.Join(tmp, "backup.tar.gz")
	if err := writeTestArchiveFiles(archivePath, map[string]string{
		".deploycp-backup/manifest.json": `{"version":1}`,
		"htdocs/index.html":              "ok",
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, "metadata")
	if err := extractBackupMetadata(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".deploycp-backup", "manifest.json")); err != nil {
		t.Fatalf("manifest should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "htdocs", "index.html")); !os.IsNotExist(err) {
		t.Fatalf("site file should not be extracted by metadata extraction, stat err=%v", err)
	}
}

func writeTestArchive(path, name, body string) error {
	return writeTestArchiveFiles(path, map[string]string{name: body})
}

func writeTestArchiveFiles(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			return err
		}
	}
	return nil
}

func archiveNames(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		header, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return names, nil
			}
			return nil, err
		}
		names[header.Name] = true
	}
}
