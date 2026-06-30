package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
)

func testLogger() *logrus.Entry {
	l := logrus.New()
	l.SetOutput(os.NewFile(0, os.DevNull)) // silence
	return logrus.NewEntry(l)
}

func TestCopyAndReplace_PreservesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")

	want := []byte("elchi-binary-contents")
	if err := os.WriteFile(src, want, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyAndReplace(testLogger(), src, dst); err != nil {
		t.Fatalf("copyAndReplace: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content mismatch: got %q want %q", got, want)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}

	// No leftover temp files in the destination directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".bin" && len(e.Name()) > 4 && e.Name()[:4] == ".tmp" {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestMoveFile_MovesAndRemovesSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "sub", "dst.bin")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MoveFile(testLogger(), src, dst); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone, stat err = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "data" {
		t.Fatalf("dst content = %q err = %v", got, err)
	}
}

func TestCopyAndReplace_MissingSource(t *testing.T) {
	dir := t.TempDir()
	err := copyAndReplace(testLogger(), filepath.Join(dir, "nope"), filepath.Join(dir, "dst"))
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}
