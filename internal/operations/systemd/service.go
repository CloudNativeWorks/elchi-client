package systemd

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func cleanCGroupLine(line string) string {
	line = strings.TrimPrefix(line, "├─")
	line = strings.TrimPrefix(line, "└─")
	line = strings.TrimPrefix(line, "│")
	return strings.TrimSpace(line)
}

func parseServiceStatus(output string) (*client.ServiceStatus, error) {
	status := &client.ServiceStatus{}
	scanner := bufio.NewScanner(strings.NewReader(output))

	var cgroupLines []string
	inCGroupSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if inCGroupSection {
			if !strings.HasPrefix(line, "├") && !strings.HasPrefix(line, "└") && !strings.HasPrefix(line, "│") {
				inCGroupSection = false
			} else {
				cgroupLines = append(cgroupLines, cleanCGroupLine(line))
				continue
			}
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "Loaded":
			status.Loaded = value
		case "Active":
			status.Active = value
		case "Main PID":
			status.MainPid = value
		case "Tasks":
			status.Tasks = value
		case "Memory":
			status.Memory = value
		case "CPU":
			status.Cpu = value
		case "CGroup":
			inCGroupSection = true
			cgroupLines = append(cgroupLines, value)
		}
	}

	if len(cgroupLines) > 0 {
		status.Cgroup = cgroupLines
	}

	return status, nil
}

func GetServiceStatus(ctx context.Context, serviceName string, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	if !strings.HasSuffix(serviceName, ".service") {
		serviceName = serviceName + ".service"
	}

	// Use RunWithOutputSNoErrLog because systemctl status returns non-zero exit codes
	// for inactive/failed services, which is expected behavior
	output, err := runner.RunWithOutputSNoErrLog(ctx, "systemctl", "status", serviceName)
	if err != nil {
		// Even if command failed, we can still parse the output
		if !strings.Contains(string(output), "could not be found") {
			status, parseErr := parseServiceStatus(string(output))
			if parseErr == nil {
				return status, nil
			}
		}
		return nil, fmt.Errorf("failed to get status: %w", err)
	}

	status, err := parseServiceStatus(string(output))
	if err != nil {
		return nil, fmt.Errorf("failed to parse status: %w", err)
	}

	return status, nil
}

func ServiceControl(ctx context.Context, serviceName string, action client.SubCommandType, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	if !strings.HasSuffix(serviceName, ".service") {
		serviceName = serviceName + ".service"
	}

	if strings.HasPrefix(serviceName, "syslog.socket") {
		serviceName = "syslog.socket"
	}

	var cmd *exec.Cmd
	switch action {
	case client.SubCommandType_SUB_START:
		cmd = runner.SetCommandWithS(ctx, "systemctl", "start", serviceName)
	case client.SubCommandType_SUB_STOP:
		cmd = runner.SetCommandWithS(ctx, "systemctl", "stop", serviceName)
	case client.SubCommandType_SUB_RESTART:
		cmd = runner.SetCommandWithS(ctx, "systemctl", "restart", serviceName)
	case client.SubCommandType_SUB_RELOAD:
		cmd = runner.SetCommandWithS(ctx, "systemctl", "reload", serviceName)
	case client.SubCommandType_SUB_STATUS:
		return GetServiceStatus(ctx, serviceName, logger, runner)
	default:
		return nil, fmt.Errorf("unsupported service action: %s", action)
	}

	if output, err := runner.CombinedOutput(cmd); err != nil {
		logger.Errorf("Failed to %s service %s: %v\nOutput: %s", action, serviceName, err, string(output))
		return nil, fmt.Errorf("failed to %s service: %w", action, err)
	}

	logger.Debugf("Successfully performed %s on service %s", action, serviceName)

	// systemctl's exit code only says the command was accepted, not that the
	// unit stayed up. For start/restart/reload, confirm the service did not land
	// in a definitively failed state (a unit that exits 0 on start then crashes
	// would otherwise be reported as a successful start). We only reject the
	// unambiguous bad states so a still-"activating" service is not false-failed.
	switch action {
	case client.SubCommandType_SUB_START, client.SubCommandType_SUB_RESTART, client.SubCommandType_SUB_RELOAD:
		state := serviceActiveState(ctx, serviceName, runner)
		if isFailedActiveState(state) {
			logger.Errorf("%s on %s reported success but service state is %q", action, serviceName, state)
			return nil, fmt.Errorf("%s on %s did not take effect: service state is %q", action, serviceName, state)
		}
	}

	status, err := GetServiceStatus(ctx, serviceName, logger, runner)
	if err != nil {
		// The action (and state check) succeeded; we just couldn't read the
		// detailed status. Return a minimal NON-nil status instead of (nil,nil),
		// so callers don't mistake "couldn't read status" for a confirmed-clean
		// result with no detail.
		logger.Warnf("Service action successful but failed to get status: %v", err)
		return &client.ServiceStatus{Active: "unknown"}, nil
	}

	return status, nil
}

// serviceActiveState returns the trimmed `systemctl is-active` state for a unit
// (e.g. "active", "activating", "inactive", "failed"). Errors are folded into
// the returned string by systemctl itself (it prints the state and exits
// non-zero for non-active units), so the caller inspects the string.
func serviceActiveState(ctx context.Context, serviceName string, runner *cmdrunner.CommandsRunner) string {
	out, _ := runner.RunWithOutputSNoErrLog(ctx, "systemctl", "is-active", serviceName)
	return strings.TrimSpace(string(out))
}

// isFailedActiveState reports whether an is-active state is a definitive failure
// for a unit that was just started/restarted/reloaded. Transient states like
// "activating" are intentionally NOT treated as failures.
func isFailedActiveState(state string) bool {
	return state == "failed" || state == "inactive"
}
