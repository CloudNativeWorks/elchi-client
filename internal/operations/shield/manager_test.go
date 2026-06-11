package shield

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "api-public.yaml"))
	if err != nil || string(got) != "hosts: [\"*\"]\n" {
		t.Fatalf("policy file content = %q, err=%v", got, err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "api-public.yaml")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("policy mode = %v, want 0640", fi.Mode().Perm())
	}
	nf := filepath.Join(root, "feeds", "spamhaus.json")
	if fi, err := os.Stat(nf); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("nested file mode = %v err=%v, want 0600", fi, err)
	}
	assertNoTempFiles(t, root)
}

func TestFullSyncDeletesUnmanaged(t *testing.T) {
	root := t.TempDir()
	log := testLogger()
	first := &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("a.yaml", []byte("a"), ""),
		inlineFile("feeds/x.json", []byte("x"), ""),
	}}
	if _, err := syncInto(context.Background(), root, first, log); err != nil {
		t.Fatal(err)
	}
	second := &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("b.yaml", []byte("b"), ""),
	}}
	if _, err := syncInto(context.Background(), root, second, log); err != nil {
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
	if _, err := syncInto(context.Background(), root, &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("keep.yaml", []byte("keep"), ""),
	}}, log); err != nil {
		t.Fatal(err)
	}
	if _, err := syncInto(context.Background(), root, &client.ShieldConfig{FullSync: false, Files: []*client.ShieldFile{
		inlineFile("new.yaml", []byte("new"), ""),
	}}, log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.yaml")); err != nil {
		t.Fatalf("keep.yaml should survive a non-full-sync: %v", err)
	}
}

// TestManagedTmpNameKept guards the prune bug where a managed file whose name ends
// in ".tmp" was deleted by its own full-sync pass.
func TestManagedTmpNameKept(t *testing.T) {
	root := t.TempDir()
	cfg := &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("notes.tmp", []byte("keep me"), ""),
	}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.tmp")); err != nil {
		t.Fatalf("a managed .tmp-named file must not be pruned: %v", err)
	}
}

// TestEmptyFullSyncWipes pins the (intended-but-drastic) behavior that full_sync
// with no files clears the directory.
func TestEmptyFullSyncWipes(t *testing.T) {
	root := t.TempDir()
	log := testLogger()
	if _, err := syncInto(context.Background(), root, &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("a.yaml", []byte("a"), ""),
	}}, log); err != nil {
		t.Fatal(err)
	}
	if _, err := syncInto(context.Background(), root, &client.ShieldConfig{FullSync: true}, log); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("empty full_sync should wipe the dir, found %d entries", len(entries))
	}
}

func TestSha256MismatchErrors(t *testing.T) {
	root := t.TempDir()
	bad := &client.ShieldConfig{Files: []*client.ShieldFile{{
		Path:   "x.yaml",
		Source: &client.ShieldFile_Inline{Inline: []byte("real")},
		Sha256: sum([]byte("different")),
	}}}
	if _, err := syncInto(context.Background(), root, bad, testLogger()); err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if _, err := os.Stat(filepath.Join(root, "x.yaml")); !os.IsNotExist(err) {
		t.Fatal("a file failing integrity must not be written")
	}
}

// TestPartialFailureLeavesNoLiveChange verifies the two-phase guarantee: if any
// file in the bundle fails to prepare, NONE of the bundle is committed.
func TestPartialFailureLeavesNoLiveChange(t *testing.T) {
	root := t.TempDir()
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{
		inlineFile("good1.yaml", []byte("g1"), ""),
		inlineFile("good2.yaml", []byte("g2"), ""),
		{Path: "bad.yaml", Source: &client.ShieldFile_Inline{Inline: []byte("real")}, Sha256: sum([]byte("nope"))},
	}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err == nil {
		t.Fatal("expected error")
	}
	for _, f := range []string{"good1.yaml", "good2.yaml", "bad.yaml"} {
		if _, err := os.Stat(filepath.Join(root, f)); !os.IsNotExist(err) {
			t.Fatalf("%s must not be committed when a sibling fails", f)
		}
	}
	assertNoTempFiles(t, root)
}

func TestIdempotentSkip(t *testing.T) {
	root := t.TempDir()
	log := testLogger()
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{inlineFile("a.yaml", []byte("same"), "0644")}}
	if _, err := syncInto(context.Background(), root, cfg, log); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "a.yaml")
	fi1, _ := os.Stat(p)
	if _, err := syncInto(context.Background(), root, cfg, log); err != nil {
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
		if _, err := syncInto(context.Background(), root, cfg, testLogger()); err == nil {
			t.Fatalf("path %q should be rejected", bad)
		}
	}
}

func TestRejectsDuplicatePath(t *testing.T) {
	root := t.TempDir()
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{
		inlineFile("a.yaml", []byte("1"), ""),
		inlineFile("a.yaml", []byte("2"), ""),
	}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err == nil {
		t.Fatal("duplicate path should be rejected")
	}
}

func TestModeMasking(t *testing.T) {
	root := t.TempDir()
	// setuid/setgid/sticky bits must be stripped; garbage falls back to default.
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{
		inlineFile("setuid.yaml", []byte("a"), "07777"),     // -> 0777, no setuid
		inlineFile("garbage.yaml", []byte("b"), "abc"),      // -> default 0600
		inlineFile("overflow.yaml", []byte("c"), "1000000"), // perm bits 0 -> default 0600
	}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "setuid.yaml")); fi.Mode()&os.ModeSetuid != 0 || fi.Mode().Perm() != 0o777 {
		t.Fatalf("setuid mode = %v, want 0777 with no setuid", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(root, "garbage.yaml")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("garbage mode = %v, want default 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(root, "overflow.yaml")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("overflow mode = %v, want default 0600", fi.Mode().Perm())
	}
}

func TestDownloadSuccess(t *testing.T) {
	body := []byte("GeoIP-mmdb-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	root := t.TempDir()
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{{
		Path:   "geo/Country.mmdb",
		Source: &client.ShieldFile_Download{Download: &client.ShieldDownload{Url: srv.URL}},
		Sha256: sum(body),
		Mode:   "0644",
	}}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err != nil {
		t.Fatalf("download sync: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "geo", "Country.mmdb"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("downloaded content = %q err=%v", got, err)
	}
	assertNoTempFiles(t, root)
}

func TestDownloadRequiresSha(t *testing.T) {
	root := t.TempDir()
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{{
		Path:   "geo/x.mmdb",
		Source: &client.ShieldFile_Download{Download: &client.ShieldDownload{Url: "http://example.invalid/x"}},
		// no sha256
	}}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err == nil {
		t.Fatal("download without sha256 must be rejected (unverified)")
	}
}

func TestDownloadShaMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("actual"))
	}))
	defer srv.Close()
	root := t.TempDir()
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{{
		Path:   "geo/x.mmdb",
		Source: &client.ShieldFile_Download{Download: &client.ShieldDownload{Url: srv.URL}},
		Sha256: sum([]byte("expected-different")),
	}}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err == nil {
		t.Fatal("download sha mismatch must error")
	}
	if _, err := os.Stat(filepath.Join(root, "geo", "x.mmdb")); !os.IsNotExist(err) {
		t.Fatal("a mismatched download must not be committed")
	}
}

func TestListConfig(t *testing.T) {
	root := t.TempDir()
	content := []byte("hello")
	cfg := &client.ShieldConfig{Files: []*client.ShieldFile{inlineFile("feeds/f.json", content, "0600")}}
	if _, err := syncInto(context.Background(), root, cfg, testLogger()); err != nil {
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

func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, _ os.DirEntry, _ error) error {
		if filepath.Ext(p) == tmpSuffix {
			t.Fatalf("leftover temp file: %s", p)
		}
		return nil
	})
}

func TestSyncReportsChanged(t *testing.T) {
	root := t.TempDir()
	cfg := &client.ShieldConfig{
		FullSync: true,
		Files:    []*client.ShieldFile{inlineFile("api.yaml", []byte("hosts: [\"*\"]\n"), "0640")},
	}

	// First sync lands a file → changed.
	changed, err := syncInto(context.Background(), root, cfg, testLogger())
	if err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v, want true,nil", changed, err)
	}

	// Identical re-push touches nothing → NOT changed (the caller skips the
	// reload-confirmation wait on this).
	changed, err = syncInto(context.Background(), root, cfg, testLogger())
	if err != nil || changed {
		t.Fatalf("idempotent re-push: changed=%v err=%v, want false,nil", changed, err)
	}

	// A full_sync that drops the file prunes it → changed again.
	clear := &client.ShieldConfig{FullSync: true, Files: []*client.ShieldFile{
		inlineFile("other.yaml", []byte("hosts: [\"*\"]\n"), "0640"),
	}}
	changed, err = syncInto(context.Background(), root, clear, testLogger())
	if err != nil || !changed {
		t.Fatalf("prune sync: changed=%v err=%v, want true,nil", changed, err)
	}
}
