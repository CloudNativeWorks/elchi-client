package waf

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
		logger: logrus.WithField("component", "waf-permissions"),
	}
}

// EnsureBaseDirectory creates the base directory for WAF WASM files if it doesn't exist
func (pm *PermissionManager) EnsureBaseDirectory() error {
	pm.logger.WithField("path", DefaultBaseDir).Debug("Ensuring base directory exists")
	
	// Check if directory exists
	if _, err := os.Stat(DefaultBaseDir); os.IsNotExist(err) {
		pm.logger.WithField("path", DefaultBaseDir).Info("Creating base directory")
		if err := os.MkdirAll(DefaultBaseDir, 0755); err != nil {
			pm.logger.WithError(err).Error("Failed to create base directory")
			return fmt.Errorf("failed to create directory %s: %w", DefaultBaseDir, err)
		}
	} else if err != nil {
		pm.logger.WithError(err).Error("Failed to check base directory")
		return fmt.Errorf("failed to check directory %s: %w", DefaultBaseDir, err)
	}
	
	pm.logger.Debug("Base directory exists")
	return nil
}

// CreateVersionDirectory creates a directory for a specific WAF version
func (pm *PermissionManager) CreateVersionDirectory(version string) (string, error) {
	versionDir := filepath.Join(DefaultBaseDir, version)
	
	pm.logger.WithFields(logrus.Fields{
		"version": version,
		"path":    versionDir,
	}).Debug("Creating version directory")
	
	if err := os.MkdirAll(versionDir, 0755); err != nil {
		pm.logger.WithError(err).Error("Failed to create version directory")
		return "", fmt.Errorf("failed to create version directory %s: %w", versionDir, err)
	}
	
	pm.logger.WithField("path", versionDir).Info("Version directory created")
	return versionDir, nil
}

// SetBinaryPermissions sets appropriate permissions for WAF WASM files
func (pm *PermissionManager) SetBinaryPermissions(filePath string) error {
	pm.logger.WithField("path", filePath).Debug("Setting WASM file permissions")
	
	// WASM files should be readable and executable (0755)
	if err := os.Chmod(filePath, 0755); err != nil {
		pm.logger.WithError(err).Error("Failed to set WASM file permissions")
		return fmt.Errorf("failed to set permissions for %s: %w", filePath, err)
	}
	
	pm.logger.WithField("path", filePath).Debug("WASM file permissions set successfully")
	return nil
}