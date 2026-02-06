package waf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/common"
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
		logger: logrus.WithField("component", "waf-downloader"),
	}
}

// GetAvailableVersions fetches available WAF versions from archive API
func (d *Downloader) GetAvailableVersions(ctx context.Context) (*ArchiveResponse, error) {
	d.logger.WithField("url", ArchiveURL).Info("Fetching available WAF versions")

	// Create a child context with timeout, respecting parent cancellation
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", ArchiveURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		d.logger.WithError(err).Error("Failed to fetch archive index")
		return nil, fmt.Errorf("failed to fetch archive index: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive API returned status %d", resp.StatusCode)
	}

	var fullArchiveResp struct {
		Releases       []interface{}           `json:"releases"` // Ignore envoy releases
		CorozaReleases []common.ArchiveRelease `json:"coroza_releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fullArchiveResp); err != nil {
		d.logger.WithError(err).Error("Failed to decode archive response")
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	archiveResp := &ArchiveResponse{
		CorozaReleases: fullArchiveResp.CorozaReleases,
	}

	d.logger.WithField("count", len(archiveResp.CorozaReleases)).Info("Successfully fetched available WAF versions")
	return archiveResp, nil
}

// DownloadBinary downloads a WASM binary for the given version and architecture
func (d *Downloader) DownloadBinary(ctx context.Context, version string, destPath string) error {
	d.logger.WithFields(logrus.Fields{
		"version": version,
		"dest":    destPath,
	}).Info("Starting WASM binary download")

	// Get available versions to find download info
	archiveResp, err := d.GetAvailableVersions(ctx)
	if err != nil {
		return err
	}

	// Find the requested version
	var targetRelease *common.ArchiveRelease
	for _, release := range archiveResp.CorozaReleases {
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
	var targetBinary *common.ArchiveBinary
	for _, binary := range targetRelease.Binaries {
		if binary.Arch == arch {
			targetBinary = &binary
			break
		}
	}

	if targetBinary == nil {
		return fmt.Errorf("WASM binary for architecture %s not found for version %s", arch, version)
	}

	// Create temporary file
	tempFile, err := os.CreateTemp("", "waf-download-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}()

	// Download binary
	if err := d.downloadFile(ctx, targetBinary.DownloadURL, tempFile); err != nil {
		return err
	}

	// Verify checksum
	if err := common.VerifyChecksum(d.logger, tempFile.Name(), targetBinary.SHA256); err != nil {
		return err
	}

	// Move to destination (handle cross-device links)
	tempFile.Close()
	if err := common.MoveFile(d.logger, tempFile.Name(), destPath); err != nil {
		return fmt.Errorf("failed to move WASM binary to destination: %w", err)
	}

	d.logger.WithField("version", version).Info("Successfully downloaded WASM binary")
	return nil
}

// downloadFile downloads a file from URL to the given file handle with context-aware cancellation
func (d *Downloader) downloadFile(ctx context.Context, url string, dest *os.File) error {
	d.logger.WithField("url", url).Debug("Downloading file")

	// Create a child context with timeout for the download
	downloadCtx, cancel := context.WithTimeout(ctx, time.Duration(DownloadTimeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(downloadCtx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.logger.WithError(err).Error("Failed to download file")
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Context-aware copy instead of io.Copy
	written, err := common.CopyWithContext(ctx, dest, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save file: %w", err)
	}

	d.logger.WithField("bytes", written).Debug("File download completed")
	return nil
}

// getCurrentArch returns the current architecture in the format expected by the archive
func (d *Downloader) getCurrentArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "wasm-amd64"
	case "arm64":
		return "wasm-arm64"
	default:
		return DefaultArch
	}
}
