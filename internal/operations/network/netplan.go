package network

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"
)

const (
	NetplanConfigFile    = "99-elchi-interfaces.yaml"
	NetplanBackupSuffix  = ".backup"
	DefaultTestTimeout   = 10 // seconds
	ConnectionCheckDelay = 3  // seconds grace period for netplan apply
	MaxFailedChecks      = 5  // consecutive failures before rollback
	CheckInterval        = 500 * time.Millisecond
)

type NetplanManager struct {
	netplanPath string
	configPath  string
	backupPath  string
	logger      *logger.Logger
	monitor     *ConnectionMonitor
	grpcConn    *grpc.ClientConn
	clientID    string
}

func NewNetplanManager(logger *logger.Logger) *NetplanManager {
	netplanPath := models.NetplanPath
	configPath := filepath.Join(netplanPath, NetplanConfigFile)
	backupPath := configPath + NetplanBackupSuffix

	return &NetplanManager{
		netplanPath: netplanPath,
		configPath:  configPath,
		backupPath:  backupPath,
		logger:      logger,
		monitor:     NewConnectionMonitor(logger),
	}
}

// SetGRPCConnection sets the gRPC connection for ping testing
func (nm *NetplanManager) SetGRPCConnection(conn *grpc.ClientConn, clientID string) {
	nm.grpcConn = conn
	nm.clientID = clientID
	if nm.monitor != nil {
		nm.monitor.SetGRPCConnection(conn, clientID)
	}
}

// ApplyNetplanConfig applies netplan configuration with connection protection
func (nm *NetplanManager) ApplyNetplanConfig(config *client.NetplanConfig) error {
	nm.logger.Info("Starting netplan configuration apply")
	nm.logger.Debugf("Config parameters - TestMode:%t, PreserveConnection:%t, Timeout:%d", 
		config.TestMode, config.PreserveControllerConnection, config.TestTimeoutSeconds)

	// Validate config
	if config.YamlContent == "" {
		nm.logger.Debug("Netplan YAML content is empty")
		return fmt.Errorf("netplan YAML content is empty")
	}
	nm.logger.Debugf("YAML content length: %d bytes", len(config.YamlContent))
	
	// Validate YAML syntax early to prevent invalid config application
	nm.logger.Debug("Validating netplan YAML syntax")
	if err := nm.ValidateConfig(config.YamlContent); err != nil {
		nm.logger.Debugf("YAML validation failed: %v", err)
		return fmt.Errorf("invalid netplan configuration: %v", err)
	}
	nm.logger.Debug("YAML validation successful")

	// Create backup before making changes
	nm.logger.Debug("Creating configuration backup")
	if err := nm.createBackup(); err != nil {
		nm.logger.Debugf("Backup creation failed: %v", err)
		return fmt.Errorf("failed to create backup: %v", err)
	}

	// Write new configuration
	nm.logger.Debug("Writing new netplan configuration")
	if err := nm.writeConfig(config.YamlContent); err != nil {
		nm.logger.Debugf("Config write failed: %v", err)
		return fmt.Errorf("failed to write netplan config: %v", err)
	}

	// Apply configuration based on mode
	if config.TestMode {
		nm.logger.Debug("Using test mode for configuration apply")
		return nm.applyWithTest(config)
	}

	// Direct apply (no safety checks)
	nm.logger.Debug("Using direct apply mode (no safety checks)")
	return nm.applyDirect()
}

// createBackup creates backup of current netplan configuration
func (nm *NetplanManager) createBackup() error {
	nm.logger.Debugf("Checking for existing config: %s", nm.configPath)
	if _, err := os.Stat(nm.configPath); os.IsNotExist(err) {
		nm.logger.Debug("No existing config to backup")
		return nil
	}

	nm.logger.Debug("Reading current config for backup")
	data, err := os.ReadFile(nm.configPath)
	if err != nil {
		nm.logger.Debugf("Failed to read current config: %v", err)
		return fmt.Errorf("failed to read current config: %v", err)
	}
	nm.logger.Debugf("Read %d bytes from current config", len(data))

	nm.logger.Debugf("Writing backup to: %s", nm.backupPath)
	// Use sudo tee to write backup with root ownership
	cmd := exec.Command("sudo", "tee", nm.backupPath)
	cmd.Stdin = strings.NewReader(string(data))
	if err := cmd.Run(); err != nil {
		nm.logger.Debugf("Failed to write backup via sudo tee: %v", err)
		return fmt.Errorf("failed to write backup: %v", err)
	}
	
	// Set proper permissions
	chmodCmd := exec.Command("sudo", "chmod", "0600", nm.backupPath)
	if err := chmodCmd.Run(); err != nil {
		nm.logger.Warnf("Failed to set backup permissions: %v", err)
	}

	nm.logger.Info("Backup created successfully")
	return nil
}

// writeConfig writes netplan YAML configuration to file
func (nm *NetplanManager) writeConfig(yamlContent string) error {
	// Ensure netplan directory exists
	nm.logger.Debugf("Ensuring netplan directory exists: %s", nm.netplanPath)
	if err := os.MkdirAll(nm.netplanPath, 0755); err != nil {
		nm.logger.Debugf("Failed to create netplan directory: %v", err)
		return fmt.Errorf("failed to create netplan directory: %v", err)
	}

	// Write config with proper permissions using sudo
	nm.logger.Debugf("Writing config to: %s (%d bytes)", nm.configPath, len(yamlContent))
	cmd := exec.Command("sudo", "tee", nm.configPath)
	cmd.Stdin = strings.NewReader(yamlContent)
	if err := cmd.Run(); err != nil {
		nm.logger.Debugf("Failed to write netplan config via sudo tee: %v", err)
		return fmt.Errorf("failed to write netplan config: %v", err)
	}
	
	// Set proper permissions
	chmodCmd := exec.Command("sudo", "chmod", "0600", nm.configPath)
	if err := chmodCmd.Run(); err != nil {
		nm.logger.Warnf("Failed to set config permissions: %v", err)
	}

	nm.logger.Info("Netplan configuration written successfully")
	return nil
}

// applyWithTest applies configuration with test mode and rollback capability
func (nm *NetplanManager) applyWithTest(config *client.NetplanConfig) error {
	timeout := config.TestTimeoutSeconds
	if timeout == 0 {
		timeout = DefaultTestTimeout
		nm.logger.Debug("Using default timeout for test mode")
	}

	nm.logger.Info(fmt.Sprintf("Applying netplan with test mode (timeout: %d seconds)", timeout))

	if config.PreserveControllerConnection {
		nm.logger.Debug("Using connection monitoring during apply")
		return nm.applyWithConnectionMonitoring(timeout)
	}

	// Standard netplan try command
	nm.logger.Debug("Using standard netplan try command")
	return nm.executeNetplanTry(timeout)
}

// applyWithConnectionMonitoring applies with active connection monitoring
func (nm *NetplanManager) applyWithConnectionMonitoring(timeout uint32) error {
	nm.logger.Debug("Starting connection monitoring apply process")
	// Start connection monitoring
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Start monitoring before apply
	nm.logger.Debug("Starting connection monitor in background")
	monitorResult := make(chan bool, 1)
	go func() {
		result := nm.monitor.MonitorConnectionDuringApply(ctx)
		nm.logger.Debugf("Connection monitor completed with result: %t", result)
		monitorResult <- result
	}()

	// Apply netplan in background
	nm.logger.Debug("Starting netplan apply in background")
	applyDone := make(chan error, 1)
	go func() {
		// Use direct apply instead of try when monitoring connection ourselves
		err := nm.executeNetplanApply()
		nm.logger.Debugf("Netplan apply completed with error: %v", err)
		applyDone <- err
	}()

	// Wait for apply completion
	select {
	case err := <-applyDone:
		if err != nil {
			nm.logger.Debugf("Netplan apply failed, initiating rollback: %v", err)
			nm.rollback()
			return fmt.Errorf("netplan apply failed: %v", err)
		}
		nm.logger.Debug("Netplan apply succeeded, waiting for connection check")
		
		// Apply succeeded, wait for connection check
		select {
		case connectionOK := <-monitorResult:
			if !connectionOK {
				nm.logger.Debug("Connection check failed, initiating rollback")
				nm.rollback()
				return fmt.Errorf("connection monitoring timeout, rolled back")
			}
			nm.logger.Info("Netplan apply successful with controller connection preserved")
			return nil
			
		case <-time.After(5 * time.Second):
			// Monitor didn't respond quickly enough
			nm.logger.Debug("Connection monitor timeout, initiating rollback")
			nm.rollback()
			return fmt.Errorf("connection monitoring timeout, rolled back")
		}
		
	case <-time.After(time.Duration(timeout) * time.Second):
		// Apply timeout
		nm.logger.Debug("Netplan apply timeout, initiating rollback")
		nm.rollback()
		return fmt.Errorf("netplan apply timeout, rolled back")
	}
}

// executeNetplanTry executes netplan try command with sudo
func (nm *NetplanManager) executeNetplanTry(timeout uint32) error {
	nm.logger.Debugf("Executing netplan try with timeout %d seconds", timeout)
	cmd := exec.Command("sudo", "netplan", "try", "--timeout", fmt.Sprintf("%d", timeout))
	cmd.Dir = nm.netplanPath
	nm.logger.Debugf("Command: %s, Working dir: %s", cmd.String(), cmd.Dir)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		nm.logger.Debugf("netplan try command failed: %v", err)
		nm.logger.Error(fmt.Sprintf("netplan try failed: %s", string(output)))
		return fmt.Errorf("netplan try failed: %v, output: %s", err, string(output))
	}
	nm.logger.Debugf("netplan try output: %s", string(output))
	
	nm.logger.Info("Netplan try completed successfully")
	return nil
}

// executeNetplanApply executes direct netplan apply with sudo
func (nm *NetplanManager) executeNetplanApply() error {
	nm.logger.Debug("Executing netplan apply")
	cmd := exec.Command("sudo", "netplan", "apply")
	cmd.Dir = nm.netplanPath
	nm.logger.Debugf("Command: %s, Working dir: %s", cmd.String(), cmd.Dir)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		nm.logger.Debugf("netplan apply command failed: %v", err)
		nm.logger.Error(fmt.Sprintf("netplan apply failed: %s", string(output)))
		return fmt.Errorf("netplan apply failed: %v, output: %s", err, string(output))
	}
	nm.logger.Debugf("netplan apply output: %s", string(output))
	
	nm.logger.Info("Netplan apply completed successfully")
	return nil
}

// applyDirect applies configuration without safety checks
func (nm *NetplanManager) applyDirect() error {
	nm.logger.Info("Applying netplan configuration directly (no safety checks)")
	return nm.executeNetplanApply()
}

// rollback restores previous configuration
func (nm *NetplanManager) rollback() error {
	nm.logger.Warn("Rolling back to previous netplan configuration")
	nm.logger.Debugf("Checking for backup file: %s", nm.backupPath)

	if _, err := os.Stat(nm.backupPath); os.IsNotExist(err) {
		nm.logger.Debug("No backup file found for rollback")
		return fmt.Errorf("no backup file found for rollback")
	}

	// Restore backup
	nm.logger.Debug("Reading backup configuration")
	backupData, err := os.ReadFile(nm.backupPath)
	if err != nil {
		nm.logger.Debugf("Failed to read backup: %v", err)
		return fmt.Errorf("failed to read backup: %v", err)
	}
	nm.logger.Debugf("Read %d bytes from backup", len(backupData))

	nm.logger.Debug("Restoring backup to config file")
	// Use sudo tee to restore backup with root ownership
	cmd := exec.Command("sudo", "tee", nm.configPath)
	cmd.Stdin = strings.NewReader(string(backupData))
	if err := cmd.Run(); err != nil {
		nm.logger.Debugf("Failed to restore backup via sudo tee: %v", err)
		return fmt.Errorf("failed to restore backup: %v", err)
	}
	
	// Set proper permissions
	chmodCmd := exec.Command("sudo", "chmod", "0600", nm.configPath)
	if err := chmodCmd.Run(); err != nil {
		nm.logger.Warnf("Failed to set restored config permissions: %v", err)
	}

	// Apply restored configuration
	nm.logger.Debug("Applying restored configuration")
	if err := nm.executeNetplanApply(); err != nil {
		nm.logger.Error("Failed to apply rollback configuration")
		return fmt.Errorf("rollback apply failed: %v", err)
	}

	nm.logger.Info("Configuration successfully rolled back")
	return nil
}

// GetCurrentConfig returns current netplan configuration
func (nm *NetplanManager) GetCurrentConfig() (string, error) {
	nm.logger.Debugf("Getting current config from: %s", nm.configPath)
	if _, err := os.Stat(nm.configPath); os.IsNotExist(err) {
		nm.logger.Debug("No current config file exists")
		return "", nil // No config exists
	}

	data, err := os.ReadFile(nm.configPath)
	if err != nil {
		nm.logger.Debugf("Failed to read current config: %v", err)
		return "", fmt.Errorf("failed to read current config: %v", err)
	}
	nm.logger.Debugf("Read %d bytes from current config", len(data))

	return string(data), nil
}

// ValidateConfig validates netplan YAML syntax
func (nm *NetplanManager) ValidateConfig(yamlContent string) error {
	// Check for forbidden route/routing-policy configurations
	nm.logger.Debug("Checking for forbidden route/routing-policy in YAML")
	if err := nm.validateForbiddenConfigurations(yamlContent); err != nil {
		nm.logger.Debugf("Forbidden configuration detected: %v", err)
		return err
	}

	// Create temporary file for validation
	nm.logger.Debug("Creating temporary file for config validation")
	tmpFile, err := os.CreateTemp("", "netplan-validate-*.yaml")
	if err != nil {
		nm.logger.Debugf("Failed to create temp file: %v", err)
		return fmt.Errorf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	nm.logger.Debugf("Created temp file: %s", tmpFile.Name())

	if _, err := tmpFile.WriteString(yamlContent); err != nil {
		nm.logger.Debugf("Failed to write temp config: %v", err)
		return fmt.Errorf("failed to write temp config: %v", err)
	}
	tmpFile.Close()
	nm.logger.Debugf("Wrote %d bytes to temp file", len(yamlContent))

	// Use netplan generate for validation (no sudo needed for temp dir)
	rootDir := filepath.Dir(tmpFile.Name())
	nm.logger.Debugf("Validating config with netplan generate --root %s", rootDir)
	cmd := exec.Command("netplan", "generate", "--root", rootDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		nm.logger.Debugf("Config validation failed: %v, output: %s", err, string(output))
		return fmt.Errorf("config validation failed: %v, output: %s", err, string(output))
	}
	nm.logger.Debug("Config validation successful")

	return nil
}

// NetplanApply handles SUB_NETPLAN_APPLY command
func NetplanApply(cmd *client.Command, logger *logger.Logger, grpcConn *grpc.ClientConn, clientID string) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if networkReq == nil || networkReq.GetNetplanConfig() == nil {
		return helper.NewErrorResponse(cmd, "netplan config is required")
	}

	manager := NewNetplanManager(logger)
	
	// Set gRPC connection for connectivity testing
	if grpcConn != nil {
		manager.SetGRPCConnection(grpcConn, clientID)
	}
	
	// Process routing tables if provided (bulk update)
	if len(networkReq.GetRoutingTables()) > 0 {
		tableManager := NewTableManager(logger)
		if err := tableManager.ManageRoutingTables(networkReq.GetRoutingTables()); err != nil {
			return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to manage routing tables: %v", err))
		}
	}
	
	// Process table operations if provided (individual operations)
	if len(networkReq.GetTableOperations()) > 0 {
		tableManager := NewTableManager(logger)
		if err := tableManager.ManageTableOperations(networkReq.GetTableOperations()); err != nil {
			return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to manage table operations: %v", err))
		}
	}
	
	// Apply configuration
	if err := manager.ApplyNetplanConfig(networkReq.GetNetplanConfig()); err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to apply netplan config: %v", err))
	}

	// Return success response
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success: true,
				Message: "Netplan configuration applied successfully",
			},
		},
	}
}

// NetplanGet handles SUB_NETPLAN_GET command  
func NetplanGet(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	manager := NewNetplanManager(logger)
	
	currentConfig, err := manager.GetCurrentConfig()
	if err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get current config: %v", err))
	}

	// Build response with current config
	resp := &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success:     true,
				Message:     "Configuration retrieved successfully",
				CurrentYaml: currentConfig,
			},
		},
	}

	return resp
}

// NetplanRollback handles SUB_NETPLAN_ROLLBACK command
func NetplanRollback(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	manager := NewNetplanManager(logger)
	
	if err := manager.rollback(); err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("rollback failed: %v", err))
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success: true,
				Message: "Rollback completed",
			},
		},
	}
}

// validateForbiddenConfigurations checks for route/routing-policy in netplan YAML
func (nm *NetplanManager) validateForbiddenConfigurations(yamlContent string) error {
	nm.logger.Debug("Parsing YAML to check for forbidden configurations")
	
	// Parse YAML content
	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &config); err != nil {
		nm.logger.Debugf("YAML parse error: %v", err)
		return fmt.Errorf("invalid YAML format: %v", err)
	}
	
	// Check for forbidden configurations
	if err := nm.checkForbiddenInConfig(config, ""); err != nil {
		return err
	}
	
	nm.logger.Debug("No forbidden configurations found")
	return nil
}

// checkForbiddenInConfig recursively checks for forbidden keys in config
func (nm *NetplanManager) checkForbiddenInConfig(config interface{}, path string) error {
	switch v := config.(type) {
	case map[string]interface{}:
		for key, value := range v {
			currentPath := key
			if path != "" {
				currentPath = path + "." + key
			}
			
			// Check for forbidden keys
			if key == "routes" {
				nm.logger.Debugf("Found forbidden 'routes' configuration at path: %s", currentPath)
				return fmt.Errorf("routes configuration is forbidden in netplan YAML - routes are managed separately via dedicated route files")
			}
			if key == "routing-policy" {
				nm.logger.Debugf("Found forbidden 'routing-policy' configuration at path: %s", currentPath)
				return fmt.Errorf("routing-policy configuration is forbidden in netplan YAML - routing policies are managed separately via dedicated policy files")
			}
			
			// Recursively check nested objects
			if err := nm.checkForbiddenInConfig(value, currentPath); err != nil {
				return err
			}
		}
	case []interface{}:
		// Check arrays
		for i, item := range v {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			if err := nm.checkForbiddenInConfig(item, itemPath); err != nil {
				return err
			}
		}
	}
	
	return nil
}

// Legacy cleanup function removed - no longer needed with unified netplan approach