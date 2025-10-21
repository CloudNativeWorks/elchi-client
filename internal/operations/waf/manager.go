package waf

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/sirupsen/logrus"
)

type Manager struct {
	downloader  *Downloader
	permissions *PermissionManager
	logger      *logrus.Entry
}

func NewManager() *Manager {
	return &Manager{
		downloader:  NewDownloader(),
		permissions: NewPermissionManager(),
		logger:      logrus.WithField("component", "waf-manager"),
	}
}

// GetDownloadedVersions returns list of locally downloaded WAF versions
func (m *Manager) GetDownloadedVersions() ([]string, error) {
	m.logger.Info("Getting downloaded WAF versions")
	
	// Ensure base directory exists
	if err := m.permissions.EnsureBaseDirectory(); err != nil {
		return nil, err
	}
	
	var versions []string
	
	// Read directory entries
	entries, err := os.ReadDir(DefaultBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			m.logger.Info("Base directory does not exist, returning empty list")
			return versions, nil
		}
		m.logger.WithError(err).Error("Failed to read WAF directory")
		return nil, fmt.Errorf("failed to read directory %s: %w", DefaultBaseDir, err)
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
		
		// Check if WASM binary exists in this version
		binaryPath := filepath.Join(DefaultBaseDir, name, "coraza.wasm")
		if _, err := os.Stat(binaryPath); err == nil {
			versions = append(versions, name)
		}
	}
	
	m.logger.WithField("count", len(versions)).Info("Found downloaded WAF versions")
	return versions, nil
}

// SetVersion downloads and installs a specific WAF version
func (m *Manager) SetVersion(version string, forceDownload bool) (string, error) {
	m.logger.WithFields(logrus.Fields{
		"version": version,
		"force":   forceDownload,
	}).Info("Setting WAF version")
	
	// Ensure base directory exists
	if err := m.permissions.EnsureBaseDirectory(); err != nil {
		return "", err
	}
	
	// Create version directory
	versionDir, err := m.permissions.CreateVersionDirectory(version)
	if err != nil {
		return "", err
	}
	
	binaryPath := filepath.Join(versionDir, "coraza.wasm")
	
	// Check if binary already exists
	if !forceDownload {
		if _, err := os.Stat(binaryPath); err == nil {
			m.logger.WithField("version", version).Info("WASM binary already exists, skipping download")
			return binaryPath, nil
		}
	}
	
	// Download binary
	m.logger.WithField("version", version).Info("Downloading WAF WASM binary")
	if err := m.downloader.DownloadBinary(version, binaryPath); err != nil {
		// Clean up on failure
		os.RemoveAll(versionDir)
		return "", fmt.Errorf("failed to download WASM binary: %w", err)
	}
	
	// Set proper permissions
	if err := m.permissions.SetBinaryPermissions(binaryPath); err != nil {
		// Clean up on failure
		os.RemoveAll(versionDir)
		return "", fmt.Errorf("failed to set WASM binary permissions: %w", err)
	}
	
	m.logger.WithFields(logrus.Fields{
		"version": version,
		"path":    binaryPath,
	}).Info("Successfully installed WAF version")
	
	return binaryPath, nil
}

// ProcessWafVersionCommand handles the WAF version command
func (m *Manager) ProcessWafVersionCommand(request *client.RequestWafVersion) *client.ResponseWafVersion {
	response := &client.ResponseWafVersion{
		Status:              client.VersionStatus_SUCCESS,
		DownloadedVersions:  []string{}, // Initialize as empty slice, not nil
	}
	
	switch request.Operation {
	case client.VersionOperation_GET_VERSIONS:
		m.logger.Info("Processing GET_VERSIONS request for WAF")
		versions, err := m.GetDownloadedVersions()
		if err != nil {
			m.logger.WithError(err).Error("Failed to get downloaded WAF versions")
			response.Status = client.VersionStatus_DIRECTORY_ERROR
			response.ErrorMessage = err.Error()
			return response
		}
		response.DownloadedVersions = versions
		
	case client.VersionOperation_SET_VERSION:
		if request.Version == "" {
			m.logger.Error("Version not specified for SET_VERSION operation")
			response.Status = client.VersionStatus_VERSION_NOT_FOUND
			response.ErrorMessage = "version not specified"
			return response
		}
		
		m.logger.WithField("version", request.Version).Info("Processing SET_VERSION request for WAF")
		binaryPath, err := m.SetVersion(request.Version, request.ForceDownload)
		if err != nil {
			m.logger.WithError(err).Error("Failed to set WAF version")
			
			// Determine error type
			if strings.Contains(err.Error(), "not found in archive") {
				response.Status = client.VersionStatus_VERSION_NOT_FOUND
			} else if strings.Contains(err.Error(), "failed to download") {
				response.Status = client.VersionStatus_DOWNLOAD_FAILED
			} else if strings.Contains(err.Error(), "network") || strings.Contains(err.Error(), "connection") {
				response.Status = client.VersionStatus_NETWORK_ERROR
			} else if strings.Contains(err.Error(), "permission") {
				response.Status = client.VersionStatus_PERMISSION_FAILED
			} else {
				response.Status = client.VersionStatus_DOWNLOAD_FAILED
			}
			response.ErrorMessage = err.Error()
			return response
		}
		
		response.InstalledVersion = request.Version
		response.DownloadPath = binaryPath
		
	default:
		m.logger.WithField("operation", request.Operation).Error("Unknown operation")
		response.Status = client.VersionStatus_VERSION_NOT_FOUND
		response.ErrorMessage = "unknown operation"
	}
	
	return response
}

// GetVersionDirectories returns all WAF version directories for debugging
func (m *Manager) GetVersionDirectories() (map[string]fs.FileInfo, error) {
	dirs := make(map[string]fs.FileInfo)
	
	entries, err := os.ReadDir(DefaultBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return dirs, nil
		}
		return nil, err
	}
	
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "v") {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			dirs[entry.Name()] = info
		}
	}
	
	return dirs, nil
}