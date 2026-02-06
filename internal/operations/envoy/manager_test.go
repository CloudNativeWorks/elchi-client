package envoy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func TestGetDownloadedVersions(t *testing.T) {
	// Create temporary directory for testing
	tempDir := filepath.Join(os.TempDir(), "elchi-envoy-test")
	defer os.RemoveAll(tempDir)

	// Note: DefaultBaseDir is a const, so we'll test with actual directory

	manager := NewManager()

	// Test with empty directory
	versions, err := manager.GetDownloadedVersions()
	if err != nil {
		t.Errorf("GetDownloadedVersions() error = %v", err)
		return
	}

	// Should return empty list when no versions exist
	if len(versions) != 0 {
		t.Errorf("Expected 0 versions, got %d", len(versions))
	}
}

func TestProcessEnvoyVersionCommand_GetVersions(t *testing.T) {
	manager := NewManager()

	request := &client.RequestEnvoyVersion{
		Operation: client.VersionOperation_GET_VERSIONS,
	}

	response := manager.ProcessEnvoyVersionCommand(context.Background(), request)

	if response == nil {
		t.Fatal("Response should not be nil")
	}

	if response.Status != client.VersionStatus_SUCCESS {
		t.Errorf("Expected SUCCESS status, got %v", response.Status)
	}

	// Downloaded versions should be a slice (even if empty)
	t.Logf("DownloadedVersions: %+v (len: %d, nil: %v)", response.DownloadedVersions, len(response.DownloadedVersions), response.DownloadedVersions == nil)

	// Should be empty since no versions are downloaded
	if len(response.DownloadedVersions) != 0 {
		t.Errorf("Expected 0 downloaded versions, got %d", len(response.DownloadedVersions))
	}
}

func TestProcessEnvoyVersionCommand_SetVersion_EmptyVersion(t *testing.T) {
	manager := NewManager()

	request := &client.RequestEnvoyVersion{
		Operation: client.VersionOperation_SET_VERSION,
		Version:   "", // Empty version should fail
	}

	response := manager.ProcessEnvoyVersionCommand(context.Background(), request)

	if response == nil {
		t.Fatal("Response should not be nil")
	}

	if response.Status == client.VersionStatus_SUCCESS {
		t.Error("Expected non-SUCCESS status for empty version")
	}

	if response.ErrorMessage == "" {
		t.Error("Expected error message for empty version")
	}
}
