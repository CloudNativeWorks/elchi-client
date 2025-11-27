package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/network"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-client/pkg/template"
	"github.com/CloudNativeWorks/elchi-client/pkg/tools"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
	"gopkg.in/yaml.v3"
)

// DeploymentCheckResult contains information about what needs to be updated
type DeploymentCheckResult struct {
	Exists              bool
	NeedsUpdate         bool
	BootstrapChanged    bool
	InterfaceChanged    bool
	ServiceChanged      bool
	ServiceNeedsRestart bool
}

// CheckExistingDeployment checks if a deployment exists and if it needs updates
func CheckExistingDeployment(deployReq *client.RequestDeploy, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*DeploymentCheckResult, error) {
	result := &DeploymentCheckResult{
		Exists:              false,
		NeedsUpdate:         false,
		BootstrapChanged:    false,
		InterfaceChanged:    false,
		ServiceChanged:      false,
		ServiceNeedsRestart: false,
	}

	filename := fmt.Sprintf("%s-%d", deployReq.GetName(), deployReq.GetPort())
	serviceName := fmt.Sprintf("%s.service", filename)
	ifaceName := fmt.Sprintf("elchi-if-%d", deployReq.GetPort())

	// Check if service exists
	serviceExists := false
	if output, _ := runner.RunWithOutput("systemctl", "show", "-p", "LoadState", serviceName); strings.Contains(string(output), "LoadState=loaded") {
		serviceExists = true
		result.Exists = true
		logger.Debugf("Found existing deployment: %s", serviceName)
	}

	if !serviceExists {
		// No existing deployment found
		logger.Debugf("No existing deployment found for %s", serviceName)
		return result, nil
	}

	// Check bootstrap file changes
	bootstrapPath := filepath.Join(models.ElchiLibPath, "bootstraps", filename+".yaml")
	if changed, err := checkBootstrapChanged(bootstrapPath, deployReq.GetBootstrap(), logger); err != nil {
		logger.Warnf("Failed to check bootstrap changes: %v", err)
		result.BootstrapChanged = true
		result.NeedsUpdate = true
	} else if changed {
		logger.Infof("Bootstrap file has changes")
		result.BootstrapChanged = true
		result.NeedsUpdate = true
		result.ServiceNeedsRestart = true
	}

	// Check interface changes
	if changed, err := checkInterfaceChanged(ifaceName, deployReq.GetDownstreamAddress(), logger); err != nil {
		logger.Warnf("Failed to check interface changes: %v", err)
		result.InterfaceChanged = true
		result.NeedsUpdate = true
	} else if changed {
		logger.Infof("Interface configuration has changes")
		result.InterfaceChanged = true
		result.NeedsUpdate = true
	}

	// Check systemd service file changes
	servicePath := filepath.Join(models.SystemdPath, serviceName)
	if changed, err := checkServiceChanged(servicePath, deployReq, filename, logger); err != nil {
		logger.Warnf("Failed to check service changes: %v", err)
		result.ServiceChanged = true
		result.NeedsUpdate = true
		result.ServiceNeedsRestart = true
	} else if changed {
		logger.Infof("Service file has changes")
		result.ServiceChanged = true
		result.NeedsUpdate = true
		result.ServiceNeedsRestart = true
	}

	if !result.NeedsUpdate {
		logger.Infof("No changes detected for deployment %s", serviceName)
	}

	return result, nil
}

// checkBootstrapChanged compares existing bootstrap file with new content
func checkBootstrapChanged(existingPath string, newContent []byte, logger *logger.Logger) (bool, error) {
	// Read existing file
	existingData, err := os.ReadFile(existingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // File doesn't exist, needs to be created
		}
		return true, fmt.Errorf("failed to read existing bootstrap: %w", err)
	}

	// Convert new content (JSON) to YAML for comparison
	var jsonObj map[string]any
	if err := json.Unmarshal(newContent, &jsonObj); err != nil {
		return true, fmt.Errorf("failed to unmarshal new bootstrap json: %w", err)
	}
	newYamlBytes, err := yaml.Marshal(jsonObj)
	if err != nil {
		return true, fmt.Errorf("failed to marshal new bootstrap to yaml: %w", err)
	}

	// Compare content
	if bytes.Equal(existingData, newYamlBytes) {
		logger.Debugf("Bootstrap file unchanged")
		return false, nil
	}

	return true, nil
}

// checkInterfaceChanged checks if interface exists and has the correct IP
func checkInterfaceChanged(ifaceName, downstreamAddress string, logger *logger.Logger) (bool, error) {
	// Check netplan file first
	netplanPath := filepath.Join(models.NetplanPath, fmt.Sprintf("90-%s.yaml", ifaceName))
	if _, err := os.Stat(netplanPath); os.IsNotExist(err) {
		logger.Debugf("Netplan file missing for interface %s", ifaceName)
		return true, nil
	}

	// Check if interface exists in runtime
	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			logger.Debugf("Interface %s not found in runtime, needs to be created", ifaceName)
			return true, nil // Interface doesn't exist, needs to be created
		}
		return true, fmt.Errorf("error checking interface: %w", err)
	}

	// Get expected IP
	ipv4CIDR, err := tools.GetIPv4CIDR(downstreamAddress)
	if err != nil {
		return true, fmt.Errorf("invalid IP address format: %w", err)
	}

	// Parse expected IP
	expectedIP := strings.Split(ipv4CIDR, "/")[0]

	// Check current IPs on interface
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return true, fmt.Errorf("failed to list addresses: %w", err)
	}

	// Check if expected IP exists
	hasCorrectIP := false
	for _, addr := range addrs {
		if addr.IP.String() == expectedIP {
			hasCorrectIP = true
		}
	}

	if !hasCorrectIP {
		logger.Debugf("Interface %s IP changed or missing: expected %s", ifaceName, expectedIP)
		return true, nil
	}

	// Check if there are extra IPs that shouldn't be there
	if len(addrs) > 1 {
		logger.Debugf("Interface %s has extra IPs, needs cleanup", ifaceName)
		return true, nil
	}

	logger.Debugf("Interface %s unchanged", ifaceName)
	return false, nil
}

// checkServiceChanged compares existing service file with expected content
func checkServiceChanged(existingPath string, deployReq *client.RequestDeploy, filename string, logger *logger.Logger) (bool, error) {
	// Read existing file
	existingData, err := os.ReadFile(existingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // File doesn't exist, needs to be created
		}
		return true, fmt.Errorf("failed to read existing service: %w", err)
	}

	// Generate expected content
	expectedContent := fmt.Sprintf(template.SystemdTemplate,
		deployReq.GetName(),    // Description (%s)
		deployReq.GetVersion(), // ExecStartPre envoy path (%s)
		filename,               // ExecStartPre bootstrap (%s)
		deployReq.GetVersion(), // ExecStart envoy path (%s)
		filename,               // ExecStart bootstrap (%s)
		deployReq.GetPort(),    // base-id (%d)
		filename,               // log-path (%s)
		filename,               // SyslogIdentifier (%s)
	)

	// Compare content
	if string(existingData) == expectedContent {
		logger.Debugf("Service file unchanged")
		return false, nil
	}

	return true, nil
}

// ApplyDeploymentUpdates applies only the changed components
func ApplyDeploymentUpdates(deployReq *client.RequestDeploy, checkResult *DeploymentCheckResult, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	filename := fmt.Sprintf("%s-%d", deployReq.GetName(), deployReq.GetPort())
	serviceName := fmt.Sprintf("%s.service", filename)
	ifaceName := fmt.Sprintf("elchi-if-%d", deployReq.GetPort())

	needsSystemdReload := false

	// Update bootstrap file if changed
	if checkResult.BootstrapChanged {
		logger.Infof("Updating bootstrap file for %s", filename)
		var jsonObj map[string]any
		if err := json.Unmarshal(deployReq.GetBootstrap(), &jsonObj); err != nil {
			return fmt.Errorf("failed to unmarshal bootstrap json: %w", err)
		}
		yamlBytes, err := yaml.Marshal(jsonObj)
		if err != nil {
			return fmt.Errorf("failed to marshal bootstrap to yaml: %w", err)
		}
		bootstrapPath := filepath.Join(models.ElchiLibPath, "bootstraps", filename+".yaml")
		if err := os.WriteFile(bootstrapPath, yamlBytes, 0644); err != nil {
			return fmt.Errorf("failed to write bootstrap file: %w", err)
		}
		logger.Infof("Bootstrap file updated: %s", bootstrapPath)
	}

	// Update interface if changed
	if checkResult.InterfaceChanged {
		logger.Infof("Updating interface %s", ifaceName)

		// Always setup dummy interface with both netplan and runtime configuration
		// SetupDummyInterface writes netplan file and configures interface via netlink
		netplanPath, createdIfaceName, err := network.SetupDummyInterface(filename, ifaceName, deployReq.GetDownstreamAddress(), deployReq.GetPort(), logger)
		if err != nil {
			return fmt.Errorf("failed to setup interface: %w", err)
		}
		logger.Infof("Interface updated: %s (netplan: %s)", createdIfaceName, netplanPath)
	}

	// Update service file if changed
	if checkResult.ServiceChanged {
		logger.Infof("Updating service file for %s", serviceName)
		servicePath := filepath.Join(models.SystemdPath, serviceName)
		content := fmt.Sprintf(template.SystemdTemplate,
			deployReq.GetName(),    // Description (%s)
			deployReq.GetVersion(), // ExecStartPre envoy path (%s)
			filename,               // ExecStartPre bootstrap (%s)
			deployReq.GetVersion(), // ExecStart envoy path (%s)
			filename,               // ExecStart bootstrap (%s)
			deployReq.GetPort(),    // base-id (%d)
			filename,               // log-path (%s)
			filename,               // SyslogIdentifier (%s)
		)
		if err := os.WriteFile(servicePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write service file: %w", err)
		}
		logger.Infof("Service file updated: %s", servicePath)
		needsSystemdReload = true
	}

	// Reload systemd if service file changed
	if needsSystemdReload {
		logger.Infof("Reloading systemd daemon")
		if err := runner.RunWithS("systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("failed to reload systemd: %w", err)
		}
	}

	// Restart service if needed
	if checkResult.ServiceNeedsRestart {
		logger.Infof("Restarting service %s due to configuration changes", serviceName)
		if err := runner.RunWithS("systemctl", "restart", serviceName); err != nil {
			return fmt.Errorf("failed to restart service: %w", err)
		}
		logger.Infof("Service restarted successfully: %s", serviceName)
	}

	return nil
}
