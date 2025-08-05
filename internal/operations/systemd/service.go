package systemd

import (
	"bufio"
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

func GetServiceStatus(serviceName string, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	if !strings.HasSuffix(serviceName, ".service") {
		serviceName = serviceName + ".service"
	}

	output, err := runner.RunWithOutputS("systemctl", "status", serviceName)
	if err != nil {
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

func ServiceControl(serviceName string, action client.SubCommandType, logger *logger.Logger, runner *cmdrunner.CommandsRunner) (*client.ServiceStatus, error) {
	if !strings.HasSuffix(serviceName, ".service") {
		serviceName = serviceName + ".service"
	}

	var cmd *exec.Cmd
	switch action {
	case client.SubCommandType_SUB_START:
		cmd = runner.SetCommandWithS("systemctl", "start", serviceName)
	case client.SubCommandType_SUB_STOP:
		cmd = runner.SetCommandWithS("systemctl", "stop", serviceName)
	case client.SubCommandType_SUB_RESTART:
		cmd = runner.SetCommandWithS("systemctl", "restart", serviceName)
	case client.SubCommandType_SUB_RELOAD:
		cmd = runner.SetCommandWithS("systemctl", "reload", serviceName)
	case client.SubCommandType_SUB_STATUS:
		return GetServiceStatus(serviceName, logger, runner)
	default:
		return nil, fmt.Errorf("unsupported service action: %s", action)
	}

	if output, err := runner.CombinedOutput(cmd); err != nil {
		logger.Errorf("Failed to %s service %s: %v\nOutput: %s", action, serviceName, err, string(output))
		return nil, fmt.Errorf("failed to %s service: %w", action, err)
	}

	logger.Debugf("Successfully performed %s on service %s", action, serviceName)

	status, err := GetServiceStatus(serviceName, logger, runner)
	if err != nil {
		logger.Warnf("Service action successful but failed to get status: %v", err)
		return nil, nil
	}

	return status, nil
}

func ActivateService(filename string, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	if err := runner.RunWithS("systemctl", "daemon-reload"); err != nil {
		logger.Errorf("Failed to reload systemd: %v", err)
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	serviceName := filename + ".service"
	if err := runner.RunWithS("systemctl", "enable", "--now", serviceName); err != nil {
		logger.Errorf("Failed to enable/start service: %v", err)
		return fmt.Errorf("failed to enable/start service: %w", err)
	}
	logger.Debugf("Service %s activated", serviceName)

	return nil
}
