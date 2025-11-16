package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// UpgradeResult contains the result of an upgrade operation
type UpgradeResult struct {
	SystemdServiceUpdated string
	BootstrapFileUpdated  string
	RestartStatus         string
	ServiceActive         bool
}

// ValidateServiceExists checks if the service exists
func ValidateServiceExists(serviceName string, runner *cmdrunner.CommandsRunner) error {
	serviceFile := serviceName + ".service"
	output, _ := runner.RunWithOutput("systemctl", "show", "-p", "LoadState", serviceFile)
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
	oldVersionPath := fmt.Sprintf("/var/lib/elchi/envoys/%s/envoy", fromVersion)
	newVersionPath := fmt.Sprintf("/var/lib/elchi/envoys/%s/envoy", toVersion)

	updatedContent := strings.ReplaceAll(string(currentContent), oldVersionPath, newVersionPath)

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
	oldVersionPath := fmt.Sprintf("/var/lib/elchi/envoys/%s/envoy", fromVersion)
	newVersionPath := fmt.Sprintf("/var/lib/elchi/envoys/%s/envoy", toVersion)

	updatedContent := strings.ReplaceAll(string(currentContent), oldVersionPath, newVersionPath)

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
func ReloadSystemdDaemon(runner *cmdrunner.CommandsRunner, logger *logger.Logger) error {
	if err := runner.RunWithS("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd daemon: %w", err)
	}
	logger.Infof("Reloaded systemd daemon")
	return nil
}

// RestartService restarts the service gracefully or hard restart
func RestartService(serviceName string, graceful bool, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (string, error) {
	serviceFile := serviceName + ".service"

	var restartStatus string
	var err error

	if graceful {
		// Graceful restart - using restart instead of reload because binary changes
		logger.Infof("Performing graceful restart for service %s", serviceFile)
		_, err = systemd.ServiceControl(serviceFile, client.SubCommandType_SUB_RESTART, logger, runner)
		if err != nil {
			return "", fmt.Errorf("failed to restart service: %w", err)
		}
		restartStatus = "graceful restart completed"
	} else {
		// Hard restart
		logger.Infof("Performing hard restart for service %s", serviceFile)
		_, err = systemd.ServiceControl(serviceFile, client.SubCommandType_SUB_RESTART, logger, runner)
		if err != nil {
			return "", fmt.Errorf("failed to restart service: %w", err)
		}
		restartStatus = "hard restart completed"
	}

	logger.Infof("Service %s restarted successfully", serviceFile)
	return restartStatus, nil
}

// VerifyServiceActive verifies that the service is running
func VerifyServiceActive(serviceName string, runner *cmdrunner.CommandsRunner) error {
	serviceFile := serviceName + ".service"
	status, err := runner.RunWithOutput("systemctl", "is-active", serviceFile)
	if err != nil || strings.TrimSpace(string(status)) != "active" {
		return fmt.Errorf("service is not active after restart: %v", err)
	}
	return nil
}

// UpgradeListener performs the complete listener upgrade operation
func UpgradeListener(
	serviceName string,
	fromVersion string,
	toVersion string,
	graceful bool,
	logger *logger.Logger,
	runner *cmdrunner.CommandsRunner,
) (*UpgradeResult, error) {
	result := &UpgradeResult{}

	// 1. Validate service exists
	if err := ValidateServiceExists(serviceName, runner); err != nil {
		return nil, err
	}

	// 2. Update systemd service file
	systemdPath, err := UpdateSystemdServiceVersion(serviceName, fromVersion, toVersion, logger)
	if err != nil {
		return nil, err
	}
	result.SystemdServiceUpdated = systemdPath

	// 3. Update bootstrap file
	bootstrapPath, err := UpdateBootstrapVersion(serviceName, fromVersion, toVersion, logger)
	if err != nil {
		return nil, err
	}
	result.BootstrapFileUpdated = bootstrapPath

	// 4. Reload systemd daemon
	if err := ReloadSystemdDaemon(runner, logger); err != nil {
		return nil, err
	}

	// 5. Restart service
	restartStatus, err := RestartService(serviceName, graceful, logger, runner)
	if err != nil {
		return nil, err
	}
	result.RestartStatus = restartStatus

	// 6. Verify service is active
	if err := VerifyServiceActive(serviceName, runner); err != nil {
		return nil, err
	}
	result.ServiceActive = true

	return result, nil
}
