package envoy

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

type PermissionManager struct {
	logger *logrus.Entry
}

func NewPermissionManager() *PermissionManager {
	return &PermissionManager{
		logger: logrus.WithField("component", "envoy-permissions"),
	}
}

// CreateVersionDirectory creates directory structure for a version
func (pm *PermissionManager) CreateVersionDirectory(version string) (string, error) {
	versionDir := filepath.Join(DefaultBaseDir, version)
	
	pm.logger.WithField("version", version).Info("Creating version directory")
	
	// Create directory with proper permissions (elchi user already owns the process)
	if err := os.MkdirAll(versionDir, 0750); err != nil {
		pm.logger.WithError(err).Error("Failed to create version directory")
		return "", fmt.Errorf("failed to create directory %s: %w", versionDir, err)
	}
	
	return versionDir, nil
}

// SetBinaryPermissions sets proper permissions for binary
func (pm *PermissionManager) SetBinaryPermissions(binaryPath string) error {
	pm.logger.WithField("path", binaryPath).Info("Setting binary permissions")
	
	// Set executable permissions (750) - elchi user already owns the file
	if err := os.Chmod(binaryPath, 0750); err != nil {
		pm.logger.WithError(err).Error("Failed to set binary permissions")
		return fmt.Errorf("failed to set permissions for %s: %w", binaryPath, err)
	}
	
	return nil
}

// EnsureBaseDirectory ensures the base envoy directory exists with proper permissions
func (pm *PermissionManager) EnsureBaseDirectory() error {
	pm.logger.WithField("base_dir", DefaultBaseDir).Info("Ensuring base directory exists")
	
	// Create base directory - elchi user already owns the process
	if err := os.MkdirAll(DefaultBaseDir, 0750); err != nil {
		pm.logger.WithError(err).Error("Failed to create base directory")
		return fmt.Errorf("failed to create base directory: %w", err)
	}
	
	return nil
}