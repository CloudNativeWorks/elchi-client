package common

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

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

// MoveFile moves a file from src to dst, handling cross-device links
func MoveFile(logger *logrus.Entry, src, dst string) error {
	// First try rename (fast path)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	logger.Debug("Rename failed, falling back to copy+delete")

	// If rename fails, copy and delete
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	// Copy content
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		os.Remove(dst) // Clean up on failure
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Sync to ensure data is written
	if err := dstFile.Sync(); err != nil {
		os.Remove(dst) // Clean up on failure
		return fmt.Errorf("failed to sync file: %w", err)
	}

	// Remove source file
	if err := os.Remove(src); err != nil {
		logger.WithError(err).Warning("Failed to remove temporary file")
		// Don't return error, file was copied successfully
	}

	return nil
}
