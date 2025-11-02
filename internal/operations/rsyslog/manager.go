package rsyslog

import (
	"fmt"
	"os"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	rsyslogConfigPath = "/etc/rsyslog.d/50-elchi.conf"
	rsyslogService    = "rsyslog"
	syslogSocket      = "syslog.socket"
)

// GetCurrentConfig reads the current rsyslog configuration
func GetCurrentConfig(logger *logger.Logger) (*client.RequestRsyslog, error) {
	data, err := os.ReadFile(rsyslogConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read rsyslog config: %w", err)
	}

	// Parse the config file to extract target, port, protocol
	protoConfig := &client.RequestRsyslog{
		RsyslogConfig: &client.RsyslogConfig{
			RsyslogOutput: &client.RsyslogOutput{},
		},
	}

	// Parse lines to find action configuration
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip commented lines and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// Look for target
		if strings.Contains(line, "target=") {
			parts := strings.Split(line, "target=")
			if len(parts) > 1 {
				target := strings.Trim(strings.Split(parts[1], "\"")[1], "\"")
				protoConfig.RsyslogConfig.RsyslogOutput.Target = target
			}
		}

		// Look for port
		if strings.Contains(line, "port=") {
			parts := strings.Split(line, "port=")
			if len(parts) > 1 {
				var port int32
				fmt.Sscanf(strings.Split(parts[1], "\"")[1], "%d", &port)
				protoConfig.RsyslogConfig.RsyslogOutput.Port = port
			}
		}

		// Look for protocol
		if strings.Contains(line, "protocol=") {
			parts := strings.Split(line, "protocol=")
			if len(parts) > 1 {
				protocol := strings.Trim(strings.Split(parts[1], "\"")[1], "\"")
				protoConfig.RsyslogConfig.RsyslogOutput.Protocol = protocol
			}
		}
	}

	return protoConfig, nil
}

// UpdateConfig writes new rsyslog configuration
func UpdateConfig(config *client.RequestRsyslog, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	if config.RsyslogConfig == nil || config.RsyslogConfig.RsyslogOutput == nil {
		return fmt.Errorf("rsyslog config is nil")
	}

	output := config.RsyslogConfig.RsyslogOutput

	// Validate input
	if output.Target == "" {
		return fmt.Errorf("target is required")
	}
	if output.Port <= 0 || output.Port > 65535 {
		return fmt.Errorf("invalid port: %d", output.Port)
	}
	if output.Protocol != "udp" && output.Protocol != "tcp" {
		return fmt.Errorf("protocol must be 'udp' or 'tcp', got: %s", output.Protocol)
	}

	// Build rsyslog configuration with static values and dynamic output
	configContent := fmt.Sprintf(`module(load="imfile")

template(name="WithFilenamePrefix" type="list") {
  property(name="$!metadata!filename" field.extract="basename")
  constant(value=" ")
  property(name="msg")
  constant(value="\n")
}

input(type="imfile"
      File="/var/log/elchi/*_access.log"
      Tag="elchi-access"
      Severity="info"
      Facility="local7"
      addMetadata="on")

input(type="imfile"
      File="/var/log/elchi/*_system.log"
      Tag="elchi-system"
      Severity="info"
      Facility="local7"
      addMetadata="on")

action(
  type="omfwd"
  target="%s"
  port="%d"
  protocol="%s"
  template="WithFilenamePrefix"
  action.resumeRetryCount="2"
  queue.type="linkedList"
  queue.size="10000"
)
`, output.Target, output.Port, output.Protocol)

	// Write config directly
	cmd := runner.SetCommandWithS("tee", rsyslogConfigPath)
	cmd.Stdin = strings.NewReader(configContent)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Set proper permissions
	chmodCmd := runner.SetCommandWithS("chmod", "644", rsyslogConfigPath)
	if err := chmodCmd.Run(); err != nil {
		logger.Warnf("Failed to set permissions: %v", err)
	}

	logger.Infof("Rsyslog configuration updated successfully")

	// Restart rsyslog service to apply changes
	logger.Infof("Restarting rsyslog service to apply configuration changes...")
	if err := RestartService(logger, runner); err != nil {
		return fmt.Errorf("config updated but failed to restart service: %w", err)
	}

	return nil
}

// GetServiceStatus returns the current rsyslog service status using systemd package
func GetServiceStatus(logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	return systemd.GetServiceStatus(rsyslogService, logger, runner)
}

// ServiceControl performs service control operations (start/stop/restart/status)
// For rsyslog, we need to control both rsyslog.service and syslog.socket
func ServiceControl(serviceName string, action client.SubCommandType, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	// Control syslog.socket first for stop/restart operations
	if action == client.SubCommandType_SUB_STOP || action == client.SubCommandType_SUB_RESTART {
		logger.Infof("Stopping syslog.socket...")
		_, err := systemd.ServiceControl(syslogSocket, client.SubCommandType_SUB_STOP, logger, runner)
		if err != nil {
			logger.Warnf("Failed to stop syslog.socket: %v (continuing anyway)", err)
		}
	}

	// Control the main rsyslog service
	status, err := systemd.ServiceControl(serviceName, action, logger, runner)
	if err != nil {
		return nil, err
	}

	// Start syslog.socket for start/restart operations
	if action == client.SubCommandType_SUB_START || action == client.SubCommandType_SUB_RESTART {
		logger.Infof("Starting syslog.socket...")
		_, err := systemd.ServiceControl(syslogSocket, client.SubCommandType_SUB_START, logger, runner)
		if err != nil {
			logger.Warnf("Failed to start syslog.socket: %v (continuing anyway)", err)
		}
	}

	return status, nil
}

// RestartService restarts the rsyslog service and syslog.socket
func RestartService(logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	// Stop syslog.socket first
	logger.Infof("Stopping syslog.socket...")
	_, err := systemd.ServiceControl(syslogSocket, client.SubCommandType_SUB_STOP, logger, runner)
	if err != nil {
		logger.Warnf("Failed to stop syslog.socket: %v (continuing anyway)", err)
	}

	// Restart rsyslog service
	_, err = systemd.ServiceControl(rsyslogService, client.SubCommandType_SUB_RESTART, logger, runner)
	if err != nil {
		return fmt.Errorf("failed to restart rsyslog: %w", err)
	}

	// Start syslog.socket
	logger.Infof("Starting syslog.socket...")
	_, err = systemd.ServiceControl(syslogSocket, client.SubCommandType_SUB_START, logger, runner)
	if err != nil {
		logger.Warnf("Failed to start syslog.socket: %v (continuing anyway)", err)
	}

	logger.Infof("Rsyslog service and syslog.socket restarted successfully")
	return nil
}
