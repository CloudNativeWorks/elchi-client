package waf

import (
	"context"
	"os"
	"strings"
	"testing"

	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_GetDownloadedVersions_EmptyDirectory(t *testing.T) {
	manager := NewManager()

	// Test may fail due to permissions, so we handle both cases
	versions, err := manager.GetDownloadedVersions()
	if err != nil && strings.Contains(err.Error(), "permission denied") {
		t.Skip("Skipping test due to permission denied - this is expected in user environment")
	}
	require.NoError(t, err)
	assert.NotNil(t, versions) // Should return empty slice, not nil
}

func TestManager_ProcessWafVersionCommand_GetVersions(t *testing.T) {
	manager := NewManager()

	request := &client.RequestWafVersion{
		Operation: client.VersionOperation_GET_VERSIONS,
	}

	response := manager.ProcessWafVersionCommand(context.Background(), request)

	// If it fails due to permissions, that's expected in user environment
	if response.Status == client.VersionStatus_DIRECTORY_ERROR &&
		response.ErrorMessage != "" {
		t.Skip("Skipping test due to permission error - this is expected in user environment")
	}

	assert.Equal(t, client.VersionStatus_SUCCESS, response.Status)
	assert.NotNil(t, response.DownloadedVersions)
	assert.Empty(t, response.ErrorMessage)
}

func TestManager_ProcessWafVersionCommand_SetVersionWithoutVersion(t *testing.T) {
	manager := NewManager()

	request := &client.RequestWafVersion{
		Operation: client.VersionOperation_SET_VERSION,
		Version:   "", // Empty version should fail
	}

	response := manager.ProcessWafVersionCommand(context.Background(), request)

	assert.Equal(t, client.VersionStatus_VERSION_NOT_FOUND, response.Status)
	assert.Contains(t, response.ErrorMessage, "version not specified")
}

func TestManager_ProcessWafVersionCommand_UnknownOperation(t *testing.T) {
	manager := NewManager()

	request := &client.RequestWafVersion{
		Operation: client.VersionOperation(999), // Invalid operation
	}

	response := manager.ProcessWafVersionCommand(context.Background(), request)

	assert.Equal(t, client.VersionStatus_VERSION_NOT_FOUND, response.Status)
	assert.Contains(t, response.ErrorMessage, "unknown operation")
}

func TestPermissionManager_EnsureBaseDirectory(t *testing.T) {
	pm := NewPermissionManager()

	// This test will skip if permission denied - expected in user environment
	err := pm.EnsureBaseDirectory()
	if err != nil && strings.Contains(err.Error(), "permission denied") {
		t.Skip("Skipping test due to permission denied - this is expected in user environment")
	}
	assert.NoError(t, err)

	// Verify directory exists (this will check the actual DefaultBaseDir)
	info, err := os.Stat(DefaultBaseDir)
	if err == nil {
		assert.True(t, info.IsDir())
	}
}

func TestGetCurrentArch(t *testing.T) {
	downloader := NewDownloader()
	arch := downloader.getCurrentArch()

	// Should return one of the supported architectures
	assert.Contains(t, []string{"wasm-amd64", "wasm-arm64"}, arch)
}
