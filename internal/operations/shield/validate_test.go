package shield

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeValidator writes a shell script that mimics `elchi-shield validate <dir>`:
// it exits `code`, printing `output`. Returns its path.
func fakeValidator(t *testing.T, code int, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake validator is POSIX-only")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "elchi-shield")
	script := "#!/bin/sh\nprintf '%s' " + shQuote(output) + "\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i)) // single-digit exit codes are enough here
}

func withValidator(t *testing.T, bin string) {
	t.Helper()
	prev := findShieldBinary
	findShieldBinary = func() string { return bin }
	t.Cleanup(func() { findShieldBinary = prev })
}

// stagePlan writes a top-level config file as a staged .tmp under root and returns
// the plan that PREPARE would have produced.
func stagePlan(t *testing.T, root, name, content string) []staged {
	t.Helper()
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(root, name+tmpSuffix)
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return []staged{{rel: name, abs: filepath.Join(root, name), tmp: tmp, mode: 0o600}}
}

func TestValidateStaged_ValidPasses(t *testing.T) {
	withValidator(t, fakeValidator(t, 0, "ok: config valid"))
	root := filepath.Join(t.TempDir(), "elchi-shield")
	plan := stagePlan(t, root, "api.yaml", "spec: {}")
	if err := validateStaged(context.Background(), root, plan, testLogger()); err != nil {
		t.Fatalf("valid config must pass: %v", err)
	}
}

func TestValidateStaged_InvalidReturnsPreciseError(t *testing.T) {
	withValidator(t, fakeValidator(t, 1, "api.yaml: domains[2].match.path_regex: invalid regex"))
	root := filepath.Join(t.TempDir(), "elchi-shield")
	plan := stagePlan(t, root, "api.yaml", "spec: bad")
	err := validateStaged(context.Background(), root, plan, testLogger())
	if err == nil {
		t.Fatal("invalid config must return an error")
	}
	if !strings.Contains(err.Error(), "path_regex: invalid regex") {
		t.Fatalf("the precise shield error must be surfaced, got: %v", err)
	}
}

func TestValidateStaged_BinaryMissingIsBestEffort(t *testing.T) {
	withValidator(t, "") // not found
	root := filepath.Join(t.TempDir(), "elchi-shield")
	plan := stagePlan(t, root, "api.yaml", "spec: {}")
	if err := validateStaged(context.Background(), root, plan, testLogger()); err != nil {
		t.Fatalf("a missing validator must not block the push: %v", err)
	}
}

func TestValidateStaged_NoTopLevelFilesSkips(t *testing.T) {
	// A data-only / clear bundle has nothing for shield to parse → no validation.
	called := false
	prev := findShieldBinary
	findShieldBinary = func() string { called = true; return "" }
	t.Cleanup(func() { findShieldBinary = prev })

	root := filepath.Join(t.TempDir(), "elchi-shield")
	_ = os.MkdirAll(filepath.Join(root, "feeds"), 0o750)
	tmp := filepath.Join(root, "feeds", "x.json.tmp")
	_ = os.WriteFile(tmp, []byte("[]"), 0o600)
	plan := []staged{{rel: "feeds/x.json", abs: filepath.Join(root, "feeds/x.json"), tmp: tmp, mode: 0o600}}

	if err := validateStaged(context.Background(), root, plan, testLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("validation must short-circuit before looking up the binary when there are no top-level config files")
	}
}

func TestLinkOrCopy_CopiesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "sub", "dst")
	_ = os.MkdirAll(filepath.Dir(dst), 0o750)
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := linkOrCopy(src, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "hello" {
		t.Fatalf("content mismatch: %q", got)
	}
}
