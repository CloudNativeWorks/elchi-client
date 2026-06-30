package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

// GetDownloadedVersions returns list of locally downloaded versions by scanning
// the base directory for version directories containing the specified binary
func GetDownloadedVersions(baseDir, binaryName string, logger *logrus.Entry) ([]string, error) {
	// Non-nil empty slice: an empty result must serialize/marshal as [] and satisfy
	// callers (and tests) that expect "empty, not nil". A nil here previously only
	// surfaced when the base dir was readable-and-empty (otherwise a permission error
	// masked it), making it an environment-dependent latent bug.
	versions := make([]string, 0)

	// Read directory entries
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("Base directory does not exist, returning empty list")
			return versions, nil
		}
		logger.WithError(err).Error("Failed to read directory")
		return nil, fmt.Errorf("failed to read directory %s: %w", baseDir, err)
	}

	// Filter and validate version directories
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasPrefix(name, "v") {
			continue
		}

		// Check if binary exists in this version
		binaryPath := filepath.Join(baseDir, name, binaryName)
		if _, err := os.Stat(binaryPath); err == nil {
			versions = append(versions, name)
		}
	}

	logger.WithField("count", len(versions)).Info("Found downloaded versions")
	return versions, nil
}
