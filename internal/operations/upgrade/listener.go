package upgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// envoyBinaryPath returns the on-disk path of the envoy binary for a version.
func envoyBinaryPath(version string) string {
	return fmt.Sprintf("/var/lib/elchi/envoys/%s/envoy", version)
}

// replaceEnvoyVersionPath rewrites the envoy binary path from fromVersion to
// toVersion in the given file content.
func replaceEnvoyVersionPath(content, fromVersion, toVersion string) string {
	return strings.ReplaceAll(content, envoyBinaryPath(fromVersion), envoyBinaryPath(toVersion))
}

// UpgradeResult contains the result of an upgrade operation
type UpgradeResult struct {
	SystemdServiceUpdated string
	BootstrapFileUpdated  string
	RestartStatus         string
	ServiceActive         bool
}

// ValidateServiceExists checks if the service exists
func ValidateServiceExists(ctx context.Context, serviceName string, runner *cmdrunner.CommandsRunner) error {
	serviceFile := serviceName + ".service"
	output, _ := runner.RunWithOutput(ctx, "systemctl", "show", "-p", "LoadState", serviceFile)
	if !strings.Contains(string(output), "LoadState=loaded") {
		return fmt.Errorf("service %s does not exist", serviceFile)
	}
	return nil
}

// UpdateSystemdServiceVersion updates the version in systemd service file
func UpdateSystemdServiceVersion(serviceName, fromVersion, toVersion string, logger *logger.Logger) (string, error) {
	serviceFile := serviceName + ".service"
	systemdPath := filepath.Join(models.SystemdPath, serviceFile)

	// Read current service file
	currentContent, err := os.ReadFile(systemdPath)
	if err != nil {
		return "", fmt.Errorf("failed to read systemd service file: %w", err)
	}

	// Update version paths
	updatedContent := replaceEnvoyVersionPath(string(currentContent), fromVersion, toVersion)

	// Write updated service file
	if err := os.WriteFile(systemdPath, []byte(updatedContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write updated systemd service file: %w", err)
	}

	logger.Infof("Updated systemd service file: %s", systemdPath)
	return systemdPath, nil
}

// UpdateBootstrapVersion updates the version in bootstrap YAML file
func UpdateBootstrapVersion(serviceName, fromVersion, toVersion string, logger *logger.Logger) (string, error) {
	bootstrapPath := filepath.Join(models.ElchiLibPath, "bootstraps", serviceName+".yaml")

	// Read current bootstrap file
	currentContent, err := os.ReadFile(bootstrapPath)
	if err != nil {
		return "", fmt.Errorf("failed to read bootstrap file: %w", err)
	}

	// Update version paths in bootstrap file
	updatedContent := replaceEnvoyVersionPath(string(currentContent), fromVersion, toVersion)

	// Update envoy-version metadata
	oldVersionMetadata := fmt.Sprintf("value: %s", fromVersion)
	newVersionMetadata := fmt.Sprintf("value: %s", toVersion)
	updatedContent = strings.ReplaceAll(updatedContent, oldVersionMetadata, newVersionMetadata)

	// Write updated bootstrap file
	if err := os.WriteFile(bootstrapPath, []byte(updatedContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write updated bootstrap file: %w", err)
	}

	logger.Infof("Updated bootstrap file: %s", bootstrapPath)
	return bootstrapPath, nil
}

// ReloadSystemdDaemon reloads systemd daemon
func ReloadSystemdDaemon(ctx context.Context, runner *cmdrunner.CommandsRunner, logger *logger.Logger) error {
	if err := runner.RunWithS(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd daemon: %w", err)
	}
	logger.Infof("Reloaded systemd daemon")
	return nil
}

// RestartService restarts the service so the new binary takes over.
//
// A binary upgrade ALWAYS requires a full restart — there is no in-process
// "graceful"/hot path here (the new binary cannot take over the running
// process). The graceful flag is kept for API compatibility, but we no longer
// report a "graceful restart" that never happened: the previous code ran an
// identical SUB_RESTART in both branches yet told the control plane "graceful
// restart completed", masking the connection drop.
func RestartService(ctx context.Context, serviceName string, graceful bool, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (string, error) {
	serviceFile := serviceName + ".service"

	if graceful {
		logger.Infof("Upgrade requires a full restart for %s (new binary); no hot-restart path available", serviceFile)
	}
	logger.Infof("Restarting service %s to activate the new binary", serviceFile)

	if _, err := systemd.ServiceControl(ctx, serviceFile, client.SubCommandType_SUB_RESTART, logger, runner); err != nil {
		return "", fmt.Errorf("failed to restart service: %w", err)
	}

	logger.Infof("Service %s restarted successfully", serviceFile)
	return "restarted to activate new binary", nil
}

// VerifyServiceActive verifies that the service is running
func VerifyServiceActive(ctx context.Context, serviceName string, runner *cmdrunner.CommandsRunner) error {
	serviceFile := serviceName + ".service"
	status, err := runner.RunWithOutput(ctx, "systemctl", "is-active", serviceFile)
	if err != nil || strings.TrimSpace(string(status)) != "active" {
		return fmt.Errorf("service is not active after restart: %w", err)
	}
	return nil
}

// UpgradeListener performs the complete listener upgrade operation
func UpgradeListener(
	ctx context.Context,
	serviceName string,
	fromVersion string,
	toVersion string,
	graceful bool,
	logger *logger.Logger,
	runner *cmdrunner.CommandsRunner,
) (*UpgradeResult, error) {
	result := &UpgradeResult{}

	// 1. Validate service exists
	if err := ValidateServiceExists(ctx, serviceName, runner); err != nil {
		return nil, err
	}

	// 1b. Verify the TARGET binary is present before touching anything. Rewriting
	// the unit/bootstrap and restarting onto a missing binary would stop the
	// working old process and fail to start the new one — a guaranteed outage.
	targetBinary := envoyBinaryPath(toVersion)
	if _, err := os.Stat(targetBinary); err != nil {
		return nil, fmt.Errorf("target envoy binary for version %s not found at %s: %w", toVersion, targetBinary, err)
	}

	// Capture the current unit + bootstrap bytes so we can roll back if any later
	// step fails. Without this, a failed daemon-reload/restart/verify left the
	// unit and bootstrap pointing at the (possibly broken) new version.
	systemdPath := filepath.Join(models.SystemdPath, serviceName+".service")
	bootstrapPath := filepath.Join(models.ElchiLibPath, "bootstraps", serviceName+".yaml")
	origService, err := os.ReadFile(systemdPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read systemd service file: %w", err)
	}
	origBootstrap, err := os.ReadFile(bootstrapPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read bootstrap file: %w", err)
	}

	// rollback restores both files and restarts onto the old version. It uses a
	// FRESH context (not ctx) so a cancelled command context — e.g. SIGTERM
	// mid-upgrade — cannot prevent the host from being returned to a working
	// state. The original error is returned to the caller.
	rollback := func(reason error) error {
		logger.Errorf("Upgrade of %s failed (%v); rolling back to version %s", serviceName, reason, fromVersion)
		rbCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if werr := os.WriteFile(systemdPath, origService, 0644); werr != nil {
			logger.Errorf("rollback: failed to restore systemd unit %s: %v", systemdPath, werr)
		}
		if werr := os.WriteFile(bootstrapPath, origBootstrap, 0644); werr != nil {
			logger.Errorf("rollback: failed to restore bootstrap %s: %v", bootstrapPath, werr)
		}
		if rerr := ReloadSystemdDaemon(rbCtx, runner, logger); rerr != nil {
			logger.Errorf("rollback: daemon-reload failed: %v", rerr)
		}
		if _, rerr := systemd.ServiceControl(rbCtx, serviceName+".service", client.SubCommandType_SUB_RESTART, logger, runner); rerr != nil {
			logger.Errorf("rollback: restart onto old version failed: %v", rerr)
		}
		return reason
	}

	// 2. Update systemd service file
	if _, err := UpdateSystemdServiceVersion(serviceName, fromVersion, toVersion, logger); err != nil {
		return nil, rollback(err)
	}
	result.SystemdServiceUpdated = systemdPath

	// 3. Update bootstrap file
	if _, err := UpdateBootstrapVersion(serviceName, fromVersion, toVersion, logger); err != nil {
		return nil, rollback(err)
	}
	result.BootstrapFileUpdated = bootstrapPath

	// 4. Reload systemd daemon
	if err := ReloadSystemdDaemon(ctx, runner, logger); err != nil {
		return nil, rollback(err)
	}

	// 5. Restart service
	restartStatus, err := RestartService(ctx, serviceName, graceful, logger, runner)
	if err != nil {
		return nil, rollback(err)
	}
	result.RestartStatus = restartStatus

	// 6. Verify service is active
	if err := VerifyServiceActive(ctx, serviceName, runner); err != nil {
		return nil, rollback(err)
	}
	result.ServiceActive = true

	return result, nil
}
