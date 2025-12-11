package envoy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/sirupsen/logrus"
)

type Downloader struct {
	httpClient *http.Client
	logger     *logrus.Entry
}

func NewDownloader() *Downloader {
	return &Downloader{
		httpClient: &http.Client{
			Timeout: time.Duration(DownloadTimeout) * time.Second,
		},
		logger: logrus.WithField("component", "envoy-downloader"),
	}
}

// GetAvailableVersions fetches available versions from archive API
func (d *Downloader) GetAvailableVersions() (*ArchiveResponse, error) {
	d.logger.WithField("url", ArchiveURL).Info("Fetching available versions")
	
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(ctx, "GET", ArchiveURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	
	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.logger.WithError(err).Error("Failed to fetch archive index")
		return nil, fmt.Errorf("failed to fetch archive index: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive API returned status %d", resp.StatusCode)
	}
	
	var archiveResp ArchiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&archiveResp); err != nil {
		d.logger.WithError(err).Error("Failed to decode archive response")
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	
	d.logger.WithField("count", len(archiveResp.Releases)).Info("Successfully fetched available versions")
	return &archiveResp, nil
}

// DownloadBinary downloads a binary for the given version and architecture
func (d *Downloader) DownloadBinary(version string, destPath string) error {
	d.logger.WithFields(logrus.Fields{
		"version":  version,
		"dest":     destPath,
	}).Info("Starting binary download")
	
	// Get available versions to find download info
	archiveResp, err := d.GetAvailableVersions()
	if err != nil {
		return err
	}
	
	// Find the requested version
	var targetRelease *ArchiveRelease
	for _, release := range archiveResp.Releases {
		if release.Version == version {
			targetRelease = &release
			break
		}
	}
	
	if targetRelease == nil {
		return fmt.Errorf("version %s not found in archive", version)
	}
	
	// Find binary for current architecture
	arch := d.getCurrentArch()
	var targetBinary *ArchiveBinary
	for _, binary := range targetRelease.Binaries {
		if binary.Arch == arch {
			targetBinary = &binary
			break
		}
	}
	
	if targetBinary == nil {
		return fmt.Errorf("binary for architecture %s not found for version %s", arch, version)
	}
	
	// Create temporary file
	tempFile, err := os.CreateTemp("", "envoy-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}()
	
	// Download binary
	if err := d.downloadFile(targetBinary.DownloadURL, tempFile); err != nil {
		return err
	}
	
	// Verify checksum
	if err := d.verifyChecksum(tempFile.Name(), targetBinary.SHA256); err != nil {
		return err
	}
	
	// Move to destination (handle cross-device links)
	tempFile.Close()
	if err := d.moveFile(tempFile.Name(), destPath); err != nil {
		return fmt.Errorf("failed to move binary to destination: %w", err)
	}
	
	d.logger.WithField("version", version).Info("Successfully downloaded binary")
	return nil
}

// downloadFile downloads a file from URL to the given file handle
func (d *Downloader) downloadFile(url string, dest *os.File) error {
	d.logger.WithField("url", url).Debug("Downloading file")
	
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(DownloadTimeout)*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}
	
	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.logger.WithError(err).Error("Failed to download file")
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	
	// Copy with progress logging
	written, err := io.Copy(dest, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save file: %w", err)
	}
	
	d.logger.WithField("bytes", written).Debug("File download completed")
	return nil
}

// verifyChecksum verifies the SHA256 checksum of a file
func (d *Downloader) verifyChecksum(filePath, expectedSHA256 string) error {
	d.logger.WithField("expected", expectedSHA256).Debug("Verifying checksum")
	
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
		d.logger.WithFields(logrus.Fields{
			"expected": expectedSHA256,
			"actual":   actualSHA256,
		}).Error("Checksum mismatch")
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSHA256, actualSHA256)
	}
	
	d.logger.Debug("Checksum verification successful")
	return nil
}

// moveFile moves a file from src to dst, handling cross-device links
func (d *Downloader) moveFile(src, dst string) error {
	// First try rename (fast path)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	
	d.logger.Debug("Rename failed, falling back to copy+delete")
	
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
		d.logger.WithError(err).Warning("Failed to remove temporary file")
		// Don't return error, file was copied successfully
	}
	
	return nil
}

// getCurrentArch returns the current architecture in the format expected by the archive
func (d *Downloader) getCurrentArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "linux-amd64"
	case "arm64":
		return "linux-arm64"
	default:
		// Default to amd64 if unknown
		return DefaultArch
	}
}