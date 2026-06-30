package common

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// CopyWithContext copies data from src to dst with context cancellation support
func CopyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64

	for {
		// Check for cancellation before each read
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		nr, readErr := src.Read(buf)
		if nr > 0 {
			nw, writeErr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if writeErr != nil {
				return written, writeErr
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, nil
			}
			return written, readErr
		}
	}
}

// VerifyChecksum verifies the SHA256 checksum of a file
func VerifyChecksum(logger *logrus.Entry, filePath, expectedSHA256 string) error {
	logger.WithField("expected", expectedSHA256).Debug("Verifying checksum")

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file for checksum: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to calculate checksum: %w", err)
	}

	actualSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		logger.WithFields(logrus.Fields{
			"expected": expectedSHA256,
			"actual":   actualSHA256,
		}).Error("Checksum mismatch")
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSHA256, actualSHA256)
	}

	logger.Debug("Checksum verification successful")
	return nil
}

// MoveFile moves a file from src to dst, handling cross-device links.
// The cross-device fallback is crash-safe: the destination is only ever made
// visible via an atomic rename, never written in place. The previous code
// io.Copy'd straight into dst, so a crash mid-copy left a truncated dst that a
// later non-force SetVersion would accept as a valid (but corrupt) binary.
func MoveFile(logger *logrus.Entry, src, dst string) error {
	// First try rename (fast path, same filesystem)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	logger.Debug("Rename failed, falling back to copy+rename")

	if err := copyAndReplace(logger, src, dst); err != nil {
		return err
	}

	// Remove source file (best-effort: the data is already safely at dst)
	if err := os.Remove(src); err != nil {
		logger.WithError(err).Warning("Failed to remove temporary file")
	}
	return nil
}

// copyAndReplace copies src into a temp file in dst's directory, fsyncs it, and
// atomically renames it onto dst, preserving src's file mode. dst is never seen
// as a partially-written file.
func copyAndReplace(logger *logrus.Entry, src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	mode := os.FileMode(0o644)
	if info, statErr := srcFile.Stat(); statErr == nil {
		mode = info.Mode()
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file in destination dir: %w", err)
	}
	tmpName := tmpFile.Name()
	committed := false
	defer func() {
		if !committed {
			tmpFile.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmpFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		logger.WithError(err).Warning("failed to set mode on temp file")
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("failed to move temp file into place: %w", err)
	}
	committed = true
	return nil
}
