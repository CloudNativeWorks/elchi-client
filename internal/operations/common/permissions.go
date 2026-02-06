package common

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// PermissionManager manages directory and file permissions for binary packages
type PermissionManager struct {
	BaseDir  string
	DirPerm  os.FileMode
	FilePerm os.FileMode
	Logger   *logrus.Entry
}

// NewPermissionManager creates a new PermissionManager with the given configuration
func NewPermissionManager(baseDir string, dirPerm, filePerm os.FileMode, logger *logrus.Entry) *PermissionManager {
	return &PermissionManager{
		BaseDir:  baseDir,
		DirPerm:  dirPerm,
		FilePerm: filePerm,
		Logger:   logger,
	}
}

// EnsureBaseDirectory creates the base directory if it doesn't exist
func (pm *PermissionManager) EnsureBaseDirectory() error {
	pm.Logger.WithField("base_dir", pm.BaseDir).Debug("Ensuring base directory exists")

	if err := os.MkdirAll(pm.BaseDir, pm.DirPerm); err != nil {
		pm.Logger.WithError(err).Error("Failed to create base directory")
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	return nil
}

// CreateVersionDirectory creates directory structure for a version
func (pm *PermissionManager) CreateVersionDirectory(version string) (string, error) {
	versionDir := filepath.Join(pm.BaseDir, version)

	pm.Logger.WithField("version", version).Debug("Creating version directory")

	if err := os.MkdirAll(versionDir, pm.DirPerm); err != nil {
		pm.Logger.WithError(err).Error("Failed to create version directory")
		return "", fmt.Errorf("failed to create directory %s: %w", versionDir, err)
	}

	return versionDir, nil
}

// SetBinaryPermissions sets proper permissions for a binary file
func (pm *PermissionManager) SetBinaryPermissions(binaryPath string) error {
	pm.Logger.WithField("path", binaryPath).Debug("Setting binary permissions")

	if err := os.Chmod(binaryPath, pm.FilePerm); err != nil {
		pm.Logger.WithError(err).Error("Failed to set binary permissions")
		return fmt.Errorf("failed to set permissions for %s: %w", binaryPath, err)
	}

	return nil
}
