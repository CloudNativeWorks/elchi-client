package shield

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

// validateTimeout bounds the pre-commit validation exec. config.Load is fast
// (parse + schema check, no engine compile), so this is generous.
const validateTimeout = 15 * time.Second

// shieldBinaryFallbacks are tried when elchi-shield isn't on PATH.
var shieldBinaryFallbacks = []string{"/usr/local/bin/elchi-shield", "/usr/bin/elchi-shield"}

// findShieldBinary locates the elchi-shield binary used for pre-commit validation,
// or "" if it can't be found (validation is then skipped — best-effort). A var so
// tests can substitute a fake validator.
var findShieldBinary = func() string {
	if p, err := exec.LookPath("elchi-shield"); err == nil {
		return p
	}
	for _, c := range shieldBinaryFallbacks {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// validateStaged runs `elchi-shield validate` against the would-be-committed config
// set BEFORE any live file is touched, so a bad config is rejected with shield's
// precise file+field error (which propagates back to the control plane) instead of
// landing on disk and being silently rejected at reload.
//
// It stages the bundle's TOP-LEVEL config files (the ones shield's loader reads;
// subdir data files are referenced, not parsed) into a throwaway sibling dir under
// their real names, then validates that dir. It is BEST-EFFORT: if the shield
// binary or a staging step is unavailable, it returns nil so a deploy is never
// blocked on a missing validator (ConfirmReload remains the backstop). Only an
// actual "config invalid" verdict (non-zero exit) returns an error.
func validateStaged(ctx context.Context, root string, plan []staged, log *logger.Logger) error {
	// Only top-level files are config files to shield's loader; staging just those
	// keeps validation cheap and avoids copying large subdir data artifacts.
	topLevel := make([]staged, 0, len(plan))
	for _, s := range plan {
		if filepath.Dir(s.rel) == "." {
			topLevel = append(topLevel, s)
		}
	}
	if len(topLevel) == 0 {
		return nil // nothing for shield to parse (e.g. a clear, or data-only bundle)
	}

	bin := findShieldBinary()
	if bin == "" {
		log.Debugf("shield validate skipped: elchi-shield binary not found")
		return nil
	}

	// Sibling of root so hardlinks stay on one filesystem (copy fallback on EXDEV),
	// and outside root so the running shield's watcher never sees the temp dir.
	vdir, err := os.MkdirTemp(filepath.Dir(root), ".shield-validate-")
	if err != nil {
		log.Debugf("shield validate skipped: temp dir: %v", err)
		return nil
	}
	defer func() { _ = os.RemoveAll(vdir) }()

	for _, s := range topLevel {
		src := s.tmp
		if s.skip {
			src = s.abs // unchanged file already live
		}
		if src == "" {
			continue
		}
		if err := linkOrCopy(src, filepath.Join(vdir, s.rel)); err != nil {
			log.Debugf("shield validate skipped: stage %q: %v", s.rel, err)
			return nil
		}
	}

	cctx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	out, runErr := exec.CommandContext(cctx, bin, "validate", vdir).CombinedOutput()
	if runErr == nil {
		return nil // valid
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		// Non-zero exit = config invalid; shield printed the precise file+field error.
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = exitErr.String()
		}
		return fmt.Errorf("shield rejected the config:\n%s", detail)
	}
	// Couldn't run the validator (timeout, exec error) — not an invalid-config
	// signal, so don't block the push; the live reload + ConfirmReload backstop it.
	log.Debugf("shield validate did not run: %v", runErr)
	return nil
}

// linkOrCopy hardlinks src→dst (cheap, same-filesystem), falling back to a content
// copy on a cross-device or unsupported link.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
