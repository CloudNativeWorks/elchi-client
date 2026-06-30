package rsyslog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/common"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	// ConfigPath is the live rsyslog drop-in the agent manages. Exported so the
	// reconcile loop can read it to detect drift/deletion.
	ConfigPath     = "/etc/rsyslog.d/50-elchi.conf"
	rsyslogService = "rsyslog"
	syslogSocket   = "syslog.socket"
)

// updateMu serializes UpdateConfig so a control-plane UPDATE and the reconcile loop
// (a separate goroutine) can never stage to the shared temp path or restart the
// service at the same time.
var updateMu sync.Mutex

// extractQuotedValue returns the value assigned to key in a line of the form
// `key="value"`. It tolerates malformed / hand-edited lines: it never panics
// and returns ("", false) when the key or its quoted value is absent. The
// previous implementation indexed Split(...)[1] directly, which panicked on an
// unquoted hand-edited line and killed the whole command stream (a DoS via a
// single GET_RSYSLOG_CONFIG against a manually-edited file).
func extractQuotedValue(line, key string) (string, bool) {
	idx := strings.Index(line, key)
	if idx < 0 {
		return "", false
	}
	rest := line[idx+len(key):]

	open := strings.IndexByte(rest, '"')
	if open < 0 {
		return "", false
	}
	rest = rest[open+1:]

	closeIdx := strings.IndexByte(rest, '"')
	if closeIdx < 0 {
		return "", false
	}
	return rest[:closeIdx], true
}

// GetCurrentConfig reads the current rsyslog configuration
func GetCurrentConfig(logger *logger.Logger) (*client.RequestRsyslog, error) {
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read rsyslog config: %w", err)
	}

	return parseRsyslogConfig(string(data)), nil
}

// parseRsyslogConfig extracts target/port/protocol from a 50-elchi.conf body.
// It is pure and panic-free for any input, including hand-edited/garbage files.
func parseRsyslogConfig(data string) *client.RequestRsyslog {
	// Parse the config file to extract target, port, protocol
	protoConfig := &client.RequestRsyslog{
		RsyslogConfig: &client.RsyslogConfig{
			RsyslogOutput: &client.RsyslogOutput{},
		},
	}

	// Parse lines to find action configuration
	lines := strings.Split(data, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip commented lines and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// Look for target
		if target, ok := extractQuotedValue(line, "target="); ok {
			protoConfig.RsyslogConfig.RsyslogOutput.Target = target
		}

		// Look for port
		if portStr, ok := extractQuotedValue(line, "port="); ok {
			var port int32
			if _, err := fmt.Sscanf(portStr, "%d", &port); err == nil {
				protoConfig.RsyslogConfig.RsyslogOutput.Port = port
			}
		}

		// Look for protocol
		if protocol, ok := extractQuotedValue(line, "protocol="); ok {
			protoConfig.RsyslogConfig.RsyslogOutput.Protocol = protocol
		}
	}

	return protoConfig
}

// RenderConfig validates the request and returns the exact 50-elchi.conf content
// that UpdateConfig writes. It is the single source of truth for the rendered file,
// shared by UpdateConfig and the reconcile loop (which compares it against the live
// file to detect drift), so the two can never diverge.
func RenderConfig(config *client.RequestRsyslog) (string, error) {
	if config.GetRsyslogConfig() == nil || config.GetRsyslogConfig().GetRsyslogOutput() == nil {
		return "", fmt.Errorf("rsyslog config is nil")
	}

	output := config.RsyslogConfig.RsyslogOutput

	// Validate input
	if output.Target == "" {
		return "", fmt.Errorf("target is required")
	}
	if output.Port <= 0 || output.Port > 65535 {
		return "", fmt.Errorf("invalid port: %d", output.Port)
	}
	if output.Protocol != "udp" && output.Protocol != "tcp" {
		return "", fmt.Errorf("protocol must be 'udp' or 'tcp', got: %s", output.Protocol)
	}

	// Build rsyslog configuration with static values and dynamic output
	return fmt.Sprintf(`module(load="imfile")

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
`, output.Target, output.Port, output.Protocol), nil
}

// UpdateConfig writes new rsyslog configuration
func UpdateConfig(ctx context.Context, config *client.RequestRsyslog, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	updateMu.Lock()
	defer updateMu.Unlock()

	configContent, err := RenderConfig(config)
	if err != nil {
		return err
	}

	// Stage to a temp file, validate it against the real rsyslog binary, then
	// atomically rename it into place. This keeps an interrupted write or a
	// rejected config from ever leaving a broken 50-elchi.conf that the following
	// restart would load.
	validate := func(ctx context.Context, tmpPath string) error {
		return validateStagedConfig(ctx, tmpPath, logger, runner)
	}
	if err := common.AtomicReplaceFileWithS(ctx, runner, ConfigPath, configContent, "644", validate); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	logger.Infof("Rsyslog configuration updated successfully")

	// Restart rsyslog service to apply changes
	logger.Infof("Restarting rsyslog service to apply configuration changes...")
	if err := RestartService(ctx, logger, runner); err != nil {
		return fmt.Errorf("config updated but failed to restart service: %w", err)
	}

	return nil
}

// validateStagedConfig runs `rsyslogd -N1 -f <tmpPath>` to check the staged config
// before it is committed. A genuine syntax rejection returns an error (so the live
// file is kept); if the validator itself can't run (e.g. binary absent), it logs a
// warning and returns nil so the push still proceeds (best-effort, matching shield).
func validateStagedConfig(ctx context.Context, tmpPath string, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	out, err := runner.RunWithOutputSNoErrLog(ctx, "rsyslogd", "-N1", "-f", tmpPath)
	switch common.ClassifyValidatorResult(err, string(out)) {
	case common.ConfigInvalid:
		return fmt.Errorf("rsyslog config validation failed: %s", strings.TrimSpace(string(out)))
	case common.ConfigValidatorUnavailable:
		logger.Warnf("rsyslog config validator could not run, proceeding without pre-flight validation: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetServiceStatus returns the current rsyslog service status using systemd package
func GetServiceStatus(ctx context.Context, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	return systemd.GetServiceStatus(ctx, rsyslogService, logger, runner)
}

// ServiceControl performs service control operations (start/stop/restart/status)
// For rsyslog, we need to control both rsyslog.service and syslog.socket
func ServiceControl(ctx context.Context, serviceName string, action client.SubCommandType, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	// Control syslog.socket first for stop/restart operations
	if action == client.SubCommandType_SUB_STOP || action == client.SubCommandType_SUB_RESTART {
		logger.Infof("Stopping syslog.socket...")
		_, err := systemd.ServiceControl(ctx, syslogSocket, client.SubCommandType_SUB_STOP, logger, runner)
		if err != nil {
			logger.Warnf("Failed to stop syslog.socket: %v (continuing anyway)", err)
		}
	}

	// Control the main rsyslog service
	status, err := systemd.ServiceControl(ctx, serviceName, action, logger, runner)
	if err != nil {
		return nil, err
	}

	// Start syslog.socket for start/restart operations
	if action == client.SubCommandType_SUB_START || action == client.SubCommandType_SUB_RESTART {
		logger.Infof("Starting syslog.socket...")
		_, err := systemd.ServiceControl(ctx, syslogSocket, client.SubCommandType_SUB_START, logger, runner)
		if err != nil {
			logger.Warnf("Failed to start syslog.socket: %v (continuing anyway)", err)
		}
	}

	return status, nil
}

// RestartService restarts the rsyslog service and syslog.socket
func RestartService(ctx context.Context, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	// Stop syslog.socket first
	logger.Infof("Stopping syslog.socket...")
	_, err := systemd.ServiceControl(ctx, syslogSocket, client.SubCommandType_SUB_STOP, logger, runner)
	if err != nil {
		logger.Warnf("Failed to stop syslog.socket: %v (continuing anyway)", err)
	}

	// Restart rsyslog service
	_, err = systemd.ServiceControl(ctx, rsyslogService, client.SubCommandType_SUB_RESTART, logger, runner)
	if err != nil {
		return fmt.Errorf("failed to restart rsyslog: %w", err)
	}

	// Start syslog.socket
	logger.Infof("Starting syslog.socket...")
	_, err = systemd.ServiceControl(ctx, syslogSocket, client.SubCommandType_SUB_START, logger, runner)
	if err != nil {
		logger.Warnf("Failed to start syslog.socket: %v (continuing anyway)", err)
	}

	logger.Infof("Rsyslog service and syslog.socket restarted successfully")
	return nil
}
