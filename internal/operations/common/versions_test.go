package common

import (
	"os"
	"path/filepath"
	"testing"
)

// testLogger() is shared with download_utils_test.go in this package.

// An empty (but existing) base dir must yield a non-nil empty slice, not nil —
// callers and the version-command response expect "empty, not nil". This is the
// regression the waf tests caught once the dir became readable in CI.
func TestGetDownloadedVersions_EmptyDirReturnsNonNil(t *testing.T) {
	dir := t.TempDir()
	versions, err := GetDownloadedVersions(dir, "envoy", testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if versions == nil {
		t.Fatal("expected non-nil empty slice for an empty directory")
	}
	if len(versions) != 0 {
		t.Fatalf("expected 0 versions, got %d", len(versions))
	}
}

// A missing base dir is not an error (returns empty, non-nil).
func TestGetDownloadedVersions_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	versions, err := GetDownloadedVersions(dir, "envoy", testLogger())
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if versions == nil {
		t.Fatal("expected non-nil empty slice for a missing directory")
	}
}

// Only directories that start with "v" AND contain the binary count as versions.
func TestGetDownloadedVersions_FiltersValidVersions(t *testing.T) {
	dir := t.TempDir()
	// valid: v1.2.3 with the binary
	good := filepath.Join(dir, "v1.2.3")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "envoy"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// invalid: right name, no binary
	if err := os.MkdirAll(filepath.Join(dir, "v9.9.9"), 0o755); err != nil {
		t.Fatal(err)
	}
	// invalid: doesn't start with v
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	// invalid: a file, not a dir
	if err := os.WriteFile(filepath.Join(dir, "vfile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	versions, err := GetDownloadedVersions(dir, "envoy", testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 1 || versions[0] != "v1.2.3" {
		t.Fatalf("expected exactly [v1.2.3], got %v", versions)
	}
}
