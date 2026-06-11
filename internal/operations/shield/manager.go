// Package shield reconciles elchi-shield's watched config directory on the edge
// host to a control-plane-supplied desired state. elchi-shield self-watches that
// directory (fsnotify + debounce + atomic hot-reload + last-good), so the agent
// only lands files atomically — it never signals shield to reload.
//
// Sync is two-phase to keep shield's view consistent. PREPARE validates every file
// and stages it into a sibling temp file (".tmp", which shield's loader ignores by
// extension) — slow work like downloads happens here, touching no live file, so any
// error aborts with the directory unchanged. COMMIT then renames the staged temps
// into place in a fast burst that shield's debounce coalesces into a single reload
// of the final state.
package shield

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	dirMode         os.FileMode = 0o750
	defaultFileMode os.FileMode = 0o600
	tmpSuffix                   = ".tmp"

	// downloadTimeout bounds a single artifact fetch so a hung URL can't stall the
	// (sequential) command stream; maxDownloadBytes caps it so a runaway/huge URL
	// can't fill the edge host's disk.
	downloadTimeout  = 2 * time.Minute
	maxDownloadBytes = 512 << 20 // 512 MiB per downloaded artifact
)

// staged is a file prepared during phase 1: its content is validated and written to
// tmp, ready to be atomically renamed onto abs in phase 2. skip marks a file already
// correct on disk (only its mode is reconciled, no rename).
type staged struct {
	rel  string
	abs  string
	tmp  string
	mode os.FileMode
	skip bool
}

// SyncConfig reconciles shield's watched config directory (models.ShieldConfigPath)
// to match cfg. See the package doc for the two-phase model. On full_sync it also
// removes any managed file not in the set (deletions propagate). On a prepare-phase
// error the directory is left unchanged; a commit-phase error (rare — only a
// catastrophic rename failure) may leave earlier files applied, with the rest rolled
// back, and is reported. The returned changed flag reports whether anything on disk
// actually changed (a file committed or pruned) — false means the bundle was already
// fully applied, so shield has nothing to reload and the caller can skip the reload
// confirmation wait entirely (idempotent re-pushes, e.g. on client reconnect, would
// otherwise burn the full confirmation timeout per push).
func SyncConfig(ctx context.Context, cfg *client.ShieldConfig, log *logger.Logger) (bool, error) {
	return syncInto(ctx, models.ShieldConfigPath, cfg, log)
}

// syncInto is SyncConfig parametrized on the root directory (testable).
func syncInto(ctx context.Context, root string, cfg *client.ShieldConfig, log *logger.Logger) (bool, error) {
	if cfg == nil {
		return false, fmt.Errorf("nil shield config")
	}
	if err := os.MkdirAll(root, dirMode); err != nil {
		return false, fmt.Errorf("create shield config dir %q: %w", root, err)
	}

	// Phase 1 — PREPARE: validate + stage every file. No live file is touched.
	plan := make([]staged, 0, len(cfg.GetFiles()))
	kept := make(map[string]struct{}, len(cfg.GetFiles()))
	cleanupStaged := func() {
		for _, s := range plan {
			if s.tmp != "" {
				_ = os.Remove(s.tmp)
			}
		}
	}

	for _, f := range cfg.GetFiles() {
		rel, err := safeRel(f.GetPath())
		if err != nil {
			cleanupStaged()
			return false, err
		}
		if _, dup := kept[rel]; dup {
			cleanupStaged()
			return false, fmt.Errorf("duplicate file path in bundle: %q", rel)
		}
		kept[rel] = struct{}{}

		abs := filepath.Join(root, rel)
		mode := fileMode(f.GetMode())
		want := strings.ToLower(strings.TrimSpace(f.GetSha256()))

		// Idempotency: identical content already on disk → reconcile mode at commit.
		if want != "" {
			if cur, err := fileSHA256(abs); err == nil && cur == want {
				plan = append(plan, staged{rel: rel, abs: abs, mode: mode, skip: true})
				continue
			}
		}

		if err := os.MkdirAll(filepath.Dir(abs), dirMode); err != nil {
			cleanupStaged()
			return false, fmt.Errorf("create dir for %q: %w", rel, err)
		}
		tmp := abs + tmpSuffix
		if err := prepareFile(ctx, f, tmp, want, mode); err != nil {
			_ = os.Remove(tmp)
			cleanupStaged()
			return false, fmt.Errorf("prepare %q: %w", rel, err)
		}
		plan = append(plan, staged{rel: rel, abs: abs, tmp: tmp, mode: mode})
	}

	// Pre-commit gate: validate the staged config against the real shield binary
	// BEFORE touching any live file, so a bad config is rejected with shield's
	// precise file+field error and the live config is left untouched. Best-effort:
	// if the validator can't run, the push proceeds (reload confirmation backstops).
	if err := validateStaged(ctx, root, plan, log); err != nil {
		cleanupStaged()
		return false, err
	}

	// Phase 2 — COMMIT: fast renames; shield's debounce coalesces the burst.
	committed := 0
	for i, s := range plan {
		if s.skip {
			_ = os.Chmod(s.abs, s.mode)
			continue
		}
		if err := os.Rename(s.tmp, s.abs); err != nil {
			// Roll back the not-yet-committed staged temps (already-renamed files
			// stay live — rename is not reversible — but each is individually valid).
			for _, rest := range plan[i:] {
				if rest.tmp != "" {
					_ = os.Remove(rest.tmp)
				}
			}
			return committed > 0, fmt.Errorf("commit %q: %w", s.rel, err)
		}
		committed++
	}

	pruned := 0
	if cfg.GetFullSync() {
		var err error
		pruned, err = pruneUnmanaged(root, kept, log)
		if err != nil {
			return committed > 0 || pruned > 0, err
		}
	}
	return committed > 0 || pruned > 0, nil
}

// prepareFile validates f's content and writes it to tmp (with mode), without
// touching the live destination. Inline content is hash-checked when a sha256 is
// given; downloads REQUIRE a sha256 (the fetch is otherwise unverified) and are
// bounded by downloadTimeout/maxDownloadBytes.
func prepareFile(ctx context.Context, f *client.ShieldFile, tmp, want string, mode os.FileMode) error {
	switch src := f.GetSource().(type) {
	case *client.ShieldFile_Inline:
		content := src.Inline
		if want != "" {
			if got := sha256Hex(content); got != want {
				return fmt.Errorf("sha256 mismatch (inline): got %s want %s", got, want)
			}
		}
		if err := os.WriteFile(tmp, content, mode); err != nil {
			return err
		}
		// Force the exact mode: os.WriteFile honors the umask (and skips the mode
		// entirely if tmp already existed), so chmod to apply the requested perms.
		return os.Chmod(tmp, mode)

	case *client.ShieldFile_Download:
		if want == "" {
			return fmt.Errorf("download requires a sha256 for integrity verification")
		}
		if err := downloadTo(ctx, src.Download.GetUrl(), tmp); err != nil {
			return err
		}
		got, err := fileSHA256(tmp)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("sha256 mismatch (download): got %s want %s", got, want)
		}
		return os.Chmod(tmp, mode)

	default:
		return fmt.Errorf("no content source")
	}
}

// ListConfig returns the files currently under shield's config dir (path relative to
// the root, sha256, octal mode); content is omitted. A missing dir yields an empty
// list (not an error).
func ListConfig(_ *logger.Logger) ([]*client.ShieldFile, error) {
	return listIn(models.ShieldConfigPath)
}

// listIn is ListConfig parametrized on the root directory (testable).
func listIn(root string) ([]*client.ShieldFile, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	var out []*client.ShieldFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(path, tmpSuffix) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		mode := ""
		if info, ierr := d.Info(); ierr == nil {
			mode = fmt.Sprintf("%#o", info.Mode().Perm())
		}
		out = append(out, &client.ShieldFile{Path: rel, Sha256: sum, Mode: mode})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list shield config: %w", err)
	}
	return out, nil
}

// pruneUnmanaged removes every file under root whose relative path is not in kept
// (this also sweeps stray ".tmp" files from a crashed run), then prunes emptied
// subdirectories. It only ever operates under root. Returns how many files it
// removed (feeds the caller's changed flag).
func pruneUnmanaged(root string, kept map[string]struct{}, log *logger.Logger) (int, error) {
	removed := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, ok := kept[rel]; ok {
			return nil // managed file — keep regardless of its name/extension
		}
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return fmt.Errorf("remove stale file %q: %w", rel, rerr)
		}
		removed++
		log.Debugf("shield: removed unmanaged config file %s", rel)
		return nil
	})
	if err != nil {
		return removed, err
	}
	pruneEmptyDirs(root)
	return removed, nil
}

// pruneEmptyDirs removes empty subdirectories under root (root itself is kept).
func pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Deepest-first so a parent becomes empty after its children are removed.
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
}

// safeRel validates a bundle file path and returns it cleaned, relative to the
// config root. It rejects empty/absolute paths and any traversal that escapes root.
func safeRel(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty file path")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("absolute path not allowed: %q", p)
	}
	clean := filepath.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes config root: %q", p)
	}
	return clean, nil
}

func downloadTo(ctx context.Context, url, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	// Cap the copy so a runaway artifact can't fill the disk (read one extra byte to
	// detect an over-limit body).
	n, err := io.Copy(out, io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("download %s: %w", url, err)
	}
	if n > maxDownloadBytes {
		_ = os.Remove(dst)
		return fmt.Errorf("download %s exceeds %d bytes", url, maxDownloadBytes)
	}
	return nil
}

// fileMode parses an octal mode string (e.g. "0640"), masking to permission bits so
// a stray setuid/setgid/sticky or out-of-range value can't be applied. Empty or
// unparseable input falls back to defaultFileMode (0600).
func fileMode(s string) os.FileMode {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultFileMode
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return defaultFileMode
	}
	if m := os.FileMode(v) & os.ModePerm; m != 0 {
		return m
	}
	return defaultFileMode
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fileSHA256 streams the file through the hasher so a large artifact (e.g. a GeoIP
// .mmdb) isn't read wholly into memory.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
