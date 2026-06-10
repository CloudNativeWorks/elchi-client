package shield

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

func TestMain(m *testing.M) {
	// The shared logger is a global that must be initialized before NewLogger.
	if err := logger.Init(logger.Config{Level: "error", Format: "text"}); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func inlineFile(path string, content []byte, mode string) *client.ShieldFile {
	return &client.ShieldFile{
		Path:   path,
		Source: &client.ShieldFile_Inline{Inline: content},
		Sha256: sum(content),
		Mode:   mode,
	}
}

func testLogger() *logger.Logger { return logger.NewLogger("test") }

func TestSyncWritesFilesAndModes(t *testing.T) {
	root := t.TempDir()
	cfg := &client.ShieldConfig{
		FullSync: true,
		Version:  "v1",
		Files: []*client.ShieldFile{
			inlineFile("api-public.yaml", []byte("hosts: [\"*\"]\n"), "0640"),
			inlineFile("feeds/spamhaus.json", []byte("[\"1.2.3.0/24\"]"), ""), // nested + default mode
		},
	}
	if err := syncInto(context.Background(), root, cfg, testLogger()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "api-public.yaml"))
	if err != nil || string(got) != "hosts: [\"*\"]\n" {
		t.Fatalf("policy file content = %q, err=%v", got, err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "api-public.yaml")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("policy mode = %v, want 0640", fi.Mode().Perm())
	}
	// nested file created, default mode 0600
	nf := filepath.Join(root, "feeds", "spamhaus.json")
	if fi, err := os.Stat(nf); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("nested file mode = %v err=%v, want 0600", fi, err)
	}
	// no leftover temp files
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if filepath.Ext(p) == ".tmp" {
			t.Fatalf("leftover temp file: %s", p)
		}
		return nil
	})
}

func TestFullSyncDeletesUnmanaged(t *testing.T) {
	root := t.TempDir()
	log := testLogger()
	first := &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("a.yaml", []byte("a"), ""),
		inlineFile("feeds/x.json", []byte("x"), ""),
	}}
	if err := syncInto(context.Background(), root, first, log); err != nil {
		t.Fatal(err)
	}
	// second bundle drops a.yaml and feeds/x.json, adds b.yaml
	second := &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("b.yaml", []byte("b"), ""),
	}}
	if err := syncInto(context.Background(), root, second, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.yaml")); !os.IsNotExist(err) {
		t.Fatal("a.yaml should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(root, "feeds")); !os.IsNotExist(err) {
		t.Fatal("emptied feeds/ dir should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(root, "b.yaml")); err != nil {
		t.Fatalf("b.yaml should exist: %v", err)
	}
}

func TestNoFullSyncKeepsUnmanaged(t *testing.T) {
	root := t.TempDir()
	log := testLogger()
	if err := syncInto(context.Background(), root, &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("keep.yaml", []byte("keep"), ""),
	}}, log); err != nil {
		t.Fatal(err)
	}
	// upsert (full_sync=false) must not delete keep.yaml
	if err := syncInto(context.Background(), root, &client.ShieldConfig{FullSync: false, Files: []*client.ShieldFile{
		inlineFile("new.yaml", []byte("new"), ""),
	}}, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.yaml")); err != nil {
		t.Fatalf("keep.yaml should survive a non-full-sync: %v", err)
	}
}

func TestSha256MismatchErrors(t *testing.T) {
	root := t.TempDir()
	bad := &client.ShieldConfig{Files: []*client.ShieldFile{{
		Path:   "x.yaml",
		Source: &client.ShieldFile_Inline{Inline: []byte("real")},
		Sha256: sum([]byte("different")),
	}}}
	if err := syncInto(context.Background(), root, bad, testLogger()); err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if _, err := os.Stat(filepath.Join(root, "x.yaml")); !os.IsNotExist(err) {
		t.Fatal("a file failing integrity must not be written")
	}
}

func TestIdempotentSkip(t *testing.T) {
	root := t.TempDir()
	log := testLogger()
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{inlineFile("a.yaml", []byte("same"), "0644")}}
	if err := syncInto(context.Background(), root, cfg, log); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "a.yaml")
	fi1, _ := os.Stat(p)
	// second identical sync must not rewrite (mtime unchanged)
	if err := syncInto(context.Background(), root, cfg, log); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Stat(p)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("unchanged file should not be rewritten (mtime changed)")
	}
}

func TestRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../escape.yaml", "/etc/passwd", "a/../../b.yaml", "..", ""} {
		cfg := &client.ShieldConfig{Files: []*client.ShieldFile{inlineFile(bad, []byte("x"), "")}}
		if err := syncInto(context.Background(), root, cfg, testLogger()); err == nil {
			t.Fatalf("path %q should be rejected", bad)
		}
	}
}

func TestListConfig(t *testing.T) {
	root := t.TempDir()
	content := []byte("hello")
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{inlineFile("feeds/f.json", content, "0600")}}
	if err := syncInto(context.Background(), root, cfg, testLogger()); err != nil {
		t.Fatal(err)
	}
	got, err := listIn(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetPath() != "feeds/f.json" || got[0].GetSha256() != sum(content) {
		t.Fatalf("listIn = %+v", got)
	}
	if got[0].GetInline() != nil {
		t.Fatal("ListConfig must omit file content")
	}
}

func TestListMissingDirEmpty(t *testing.T) {
	got, err := listIn(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil || got != nil {
		t.Fatalf("missing dir should be empty/no-error, got %v err %v", got, err)
	}
}
