// Package shield reconciles elchi-shield's watched config directory on the edge
// host to a control-plane-supplied desired state. elchi-shield self-watches that
// directory (fsnotify + debounce + atomic hot-reload + last-good), so the agent
// only needs to land files atomically — it never signals shield to reload.
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

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	dirMode         os.FileMode = 0o750
	defaultFileMode os.FileMode = 0o600
	tmpSuffix                   = ".tmp"
)

// SyncConfig reconciles shield's watched config directory (models.ShieldConfigPath)
// to match cfg. Every file in cfg.Files is written atomically (temp + rename); when
// cfg.FullSync is set, any managed file NOT in the set is removed so deletions
// propagate. Writes are idempotent: a file whose on-disk sha256 already matches is
// left untouched. Returns an error if any file fails integrity/IO; on error the
// directory is left in a partially-applied state, but shield's last-good config
// keeps serving until a clean bundle lands.
func SyncConfig(ctx context.Context, cfg *client.ShieldConfig, log *logger.Logger) error {
	return syncInto(ctx, models.ShieldConfigPath, cfg, log)
}

// syncInto is SyncConfig parametrized on the root directory (testable).
func syncInto(ctx context.Context, root string, cfg *client.ShieldConfig, log *logger.Logger) error {
	if cfg == nil {
		return fmt.Errorf("nil shield config")
	}
	if err := os.MkdirAll(root, dirMode); err != nil {
		return fmt.Errorf("create shield config dir %q: %w", root, err)
	}

	kept := make(map[string]struct{}, len(cfg.GetFiles()))
	for _, f := range cfg.GetFiles() {
		rel, err := safeRel(f.GetPath())
		if err != nil {
			return err
		}
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), dirMode); err != nil {
			return fmt.Errorf("create dir for %q: %w", rel, err)
		}
		if err := writeOne(ctx, f, abs); err != nil {
			return fmt.Errorf("sync %q: %w", rel, err)
		}
		kept[rel] = struct{}{}
	}

	if cfg.GetFullSync() {
		if err := pruneUnmanaged(root, kept, log); err != nil {
			return err
		}
	}
	return nil
}

// writeOne lands a single bundle file at abs. It is idempotent (skips when the
// on-disk sha256 already matches), verifies integrity against f.sha256, and writes
// atomically via a temp file + rename so shield never reads a half-written file.
func writeOne(ctx context.Context, f *client.ShieldFile, abs string) error {
	mode := fileMode(f.GetMode())
	want := f.GetSha256()

	// Idempotency: if the file already has the wanted content, just fix the mode.
	if want != "" {
		if cur, err := fileSHA256(abs); err == nil && cur == want {
			return os.Chmod(abs, mode)
		}
	}

	switch src := f.GetSource().(type) {
	case *client.ShieldFile_Inline:
		content := src.Inline
		if want != "" {
			if got := sha256Hex(content); got != want {
				return fmt.Errorf("sha256 mismatch (inline): got %s want %s", got, want)
			}
		}
		return atomicWrite(abs, content, mode)

	case *client.ShieldFile_Download:
		tmp := abs + tmpSuffix
		if err := downloadTo(ctx, src.Download.GetUrl(), tmp); err != nil {
			return err
		}
		if want != "" {
			got, err := fileSHA256(tmp)
			if err != nil {
				_ = os.Remove(tmp)
				return err
			}
			if got != want {
				_ = os.Remove(tmp)
				return fmt.Errorf("sha256 mismatch (download): got %s want %s", got, want)
			}
		}
		if err := os.Chmod(tmp, mode); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, abs); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("install downloaded file: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("no content source")
	}
}

// ListConfig returns the files currently under shield's config dir (path relative
// to the root, sha256, and octal mode); content is intentionally omitted. A
// missing directory yields an empty list (not an error).
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

// pruneUnmanaged removes any file under root whose relative path is not in kept
// (and any leftover temp file), then prunes emptied subdirectories. It only ever
// operates under root.
func pruneUnmanaged(root string, kept map[string]struct{}, log *logger.Logger) error {
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
		if _, ok := kept[rel]; ok && !strings.HasSuffix(rel, tmpSuffix) {
			return nil
		}
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return fmt.Errorf("remove stale file %q: %w", rel, rerr)
		}
		log.Debugf("shield: removed unmanaged config file %s", rel)
		return nil
	})
	if err != nil {
		return err
	}
	pruneEmptyDirs(root)
	return nil
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
	// Remove deepest-first so parents become empty after their children go.
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
}

// safeRel validates a bundle file path and returns it cleaned, relative to the
// config root. It rejects empty paths, absolute paths, and any traversal that
// would escape the root.
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

func atomicWrite(abs string, content []byte, mode os.FileMode) error {
	tmp := abs + tmpSuffix
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	// WriteFile honors mode only when creating; force it in case tmp pre-existed.
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install file: %w", err)
	}
	return nil
}

func downloadTo(ctx context.Context, url, dst string) error {
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
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("download %s: %w", url, err)
	}
	return nil
}

func fileMode(s string) os.FileMode {
	if s == "" {
		return defaultFileMode
	}
	if v, err := strconv.ParseUint(s, 8, 32); err == nil && v != 0 {
		return os.FileMode(v)
	}
	return defaultFileMode
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}
