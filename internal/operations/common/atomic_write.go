package common

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
)

// tmpConfigSuffix is appended (together with a leading dot) to a staged config's
// base name. The leading dot + non-".conf" suffix keeps the temp from matching
// wildcard includes such as /etc/rsyslog.d/*.conf before it is committed.
const tmpConfigSuffix = ".elchi-tmp"

// TempSiblingPath returns a hidden temp path in the SAME directory as dst, so a
// later rename onto dst is an atomic same-filesystem move. It is pure (no I/O) so
// the naming can be unit-tested.
func TempSiblingPath(dst string) string {
	return filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+tmpConfigSuffix)
}

// ValidationOutcome is the interpreted result of running a config validator.
type ValidationOutcome int

const (
	// ConfigValid means the validator ran and accepted the config.
	ConfigValid ValidationOutcome = iota
	// ConfigInvalid means the validator ran and rejected the config — the caller
	// must NOT commit the staged file.
	ConfigInvalid
	// ConfigValidatorUnavailable means the validator could not run at all (missing
	// binary, no permission, missing keystore, …). Callers treat this as
	// best-effort and proceed, exactly as the shield sync does, since refusing
	// every push because the validator is broken would be worse than today's
	// no-validation behaviour.
	ConfigValidatorUnavailable
)

// validatorUnavailableMarkers are substrings (lower-cased) that indicate the
// validator itself could not run, as opposed to the config being rejected. Kept
// here so the classifier is a pure, table-driven, unit-testable function.
var validatorUnavailableMarkers = []string{
	"permission denied",
	"could not open config file",
	"operation not permitted",
	"keystore",
	"could not initialize",
	"executable file not found",
	"command not found",
	"no such file or directory",
	// sudo refusing/blocking the validator (rule missing, password required, no tty)
	// is an environment problem, not a config rejection — proceed best-effort.
	"not allowed to execute",
	"a password is required",
	"a terminal is required",
}

// ClassifyValidatorResult interprets a config-validator invocation. exitErr is the
// error from running the validator (nil ⇒ exit 0); output is its combined output.
// A clean exit is ConfigValid; a non-zero exit whose output looks like the
// validator could not run is ConfigValidatorUnavailable; any other non-zero exit is
// a genuine ConfigInvalid. It is pure so it can be unit-tested without a validator
// binary.
func ClassifyValidatorResult(exitErr error, output string) ValidationOutcome {
	if exitErr == nil {
		return ConfigValid
	}
	// Scan both the validator's output and the run error: a missing binary surfaces
	// in the error string ("executable file not found"), while permission/keystore
	// failures surface in the output.
	lower := strings.ToLower(output + "\n" + exitErr.Error())
	for _, marker := range validatorUnavailableMarkers {
		if strings.Contains(lower, marker) {
			return ConfigValidatorUnavailable
		}
	}
	return ConfigInvalid
}

// AtomicReplaceFileWithS writes content to a sibling temp file (via sudo, because
// the target directories are root-owned and the client runs as the elchi user),
// sets its mode, runs validate against the staged temp, then atomically renames it
// onto dst with `mv -f` (atomic on the same filesystem). The live dst is never
// touched until validation passes, so an interrupted write or a rejected config can
// never leave a broken file in place. The temp is removed on any failure.
//
// validate may be nil. It receives the staged temp path and should return a non-nil
// error only for a genuine config rejection; a validator that cannot run should be
// treated as best-effort by the caller (see ClassifyValidatorResult) and return nil.
func AtomicReplaceFileWithS(
	ctx context.Context,
	runner *cmdrunner.CommandsRunner,
	dst, content, mode string,
	validate func(ctx context.Context, tmpPath string) error,
) error {
	tmp := TempSiblingPath(dst)

	// removeTemp cleans up the staged temp on any failure. It uses a fresh context so
	// that a cancelled ctx (e.g. SIGTERM mid-write) can't prevent the cleanup and leave
	// the temp behind. A leftover temp is otherwise harmless (its name never matches a
	// *.conf include) and the next call's pre-clean removes it, but cleaning eagerly
	// keeps the directory tidy.
	removeTemp := func() { _ = runner.RunWithS(context.Background(), "rm", "-f", tmp) }

	// Clear any stale temp left by a previously interrupted run.
	removeTemp()

	// Stage the content into the temp via sudo tee.
	teeCmd := runner.SetCommandWithS(ctx, "tee", tmp)
	teeCmd.Stdin = strings.NewReader(content)
	teeCmd.Stdout = io.Discard
	teeCmd.Stderr = os.Stderr
	if err := teeCmd.Run(); err != nil {
		removeTemp()
		return fmt.Errorf("failed to stage temp config %q: %w", tmp, err)
	}

	// Apply the requested mode before the file goes live.
	if err := runner.RunWithS(ctx, "chmod", mode, tmp); err != nil {
		removeTemp()
		return fmt.Errorf("failed to chmod temp config %q: %w", tmp, err)
	}

	// Validate the staged file. A genuine rejection aborts with dst untouched.
	if validate != nil {
		if err := validate(ctx, tmp); err != nil {
			removeTemp()
			return err
		}
	}

	// Commit: atomic rename onto the live path (same directory ⇒ same filesystem).
	if err := runner.RunWithS(ctx, "mv", "-f", tmp, dst); err != nil {
		removeTemp()
		return fmt.Errorf("failed to commit config to %q: %w", dst, err)
	}

	return nil
}
