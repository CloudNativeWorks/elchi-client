package frr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

// VtyshManager manages vtysh operations with enhanced error handling
type VtyshManager struct {
	logger         *logger.Logger
	errorCollector *ErrorCollector
}

// ErrorCollector collects command execution results
type ErrorCollector struct {
	commands  []string
	errors    []CommandError
	startTime time.Time
}

// CommandError represents a failed command execution
type CommandError struct {
	Command   string
	Error     error
	Context   string
	Timestamp time.Time
	Output    string
}

// ExecutionResult represents the result of command execution
type ExecutionResult struct {
	Output           string
	Success          bool
	Error            error
	ExecutionTime    time.Duration
	CommandsExecuted int
	ErrorCount       int
}

// VtyshCommand represents a single vtysh command
type VtyshCommand struct {
	Command     string
	Description string
	Required    bool
}

// VtyshSession represents a vtysh configuration session
type VtyshSession struct {
	Commands []VtyshCommand
	Context  string // e.g., "configure terminal", "router bgp 65001"
}

// NewVtyshManager creates a new VtyshManager with error tracking
func NewVtyshManager(logger *logger.Logger) *VtyshManager {
	return &VtyshManager{
		logger: logger,
		errorCollector: &ErrorCollector{
			commands:  make([]string, 0),
			errors:    make([]CommandError, 0),
			startTime: time.Now(),
		},
	}
}

// ExecuteCommand executes a single vtysh command and returns its output
func (vm *VtyshManager) ExecuteCommand(command string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("empty command")
	}

	vm.logger.Debug(fmt.Sprintf("Executing single command: %s", command))

	// Create command with sudo and context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create command with sudo
	cmd := exec.CommandContext(ctx, "sudo", "vtysh", "-c", command)

	// Capture both stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute command
	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	// Log outputs regardless of error
	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	vm.logger.Debug(fmt.Sprintf("Command execution completed in %v", duration))
	vm.logger.Debug(fmt.Sprintf("vtysh stdout length: %d bytes", len(stdoutStr)))
	vm.logger.Debug(fmt.Sprintf("vtysh stderr length: %d bytes", len(stderrStr)))

	if len(stdoutStr) > 0 {
		vm.logger.Debug(fmt.Sprintf("vtysh stdout:\n%s", stdoutStr))
	}
	if len(stderrStr) > 0 {
		vm.logger.Debug(fmt.Sprintf("vtysh stderr:\n%s", stderrStr))
	}

	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out after %v", duration)
	}

	// Handle errors
	if err != nil {
		// Check if it's an exit error
		if exitErr, ok := err.(*exec.ExitError); ok {
			errMsg := fmt.Sprintf("vtysh command failed: %v", exitErr)
			if stderr.Len() > 0 {
				errMsg = fmt.Sprintf("%s (stderr: %s)", errMsg, stderr.String())
			}
			return "", errors.New(errMsg)
		}
		return "", fmt.Errorf("vtysh execution failed: %v", err)
	}

	// Check for error indicators in output
	if strings.Contains(stdoutStr, "% Invalid") ||
		strings.Contains(stdoutStr, "% Unknown") ||
		strings.Contains(stdoutStr, "% No such") ||
		strings.Contains(stdoutStr, "% Incomplete") ||
		strings.Contains(stdoutStr, "% Error") {
		return "", fmt.Errorf("command failed: %s", stdoutStr)
	}

	// Additional check for BGP-specific issues
	if strings.Contains(command, "show bgp") {
		if strings.Contains(stdoutStr, "BGP is not running") ||
			strings.Contains(stdoutStr, "No BGP process is configured") ||
			len(strings.TrimSpace(stdoutStr)) == 0 {
			vm.logger.Warn(fmt.Sprintf("BGP issue detected for command '%s': empty or BGP not running", command))
		}
	}

	return stdoutStr, nil
}

// WriteMemory saves the running configuration to startup configuration
func (vm *VtyshManager) WriteMemory() error {
	vm.logger.Info("Saving FRR configuration to memory")

	cmd := exec.Command("sudo", "vtysh", "-c", "write memory")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		vm.logger.Error(fmt.Sprintf("Failed to save configuration: %v", err))
		if stderr.Len() > 0 {
			vm.logger.Error(fmt.Sprintf("Error details: %s", stderr.String()))
		}
		return fmt.Errorf("failed to save configuration: %v", err)
	}

	vm.logger.Info("Configuration saved successfully")
	return nil
}

// ExecuteSimpleSession executes commands in a single vtysh session with proper validation
func (vm *VtyshManager) ExecuteSimpleSession(commands []string) error {
	if len(commands) == 0 {
		return nil
	}

	// Always start with configure terminal unless it's already included
	var fullCommands []string
	if len(commands) == 0 || commands[0] != "configure terminal" {
		fullCommands = append(fullCommands, "configure terminal")
	}
	fullCommands = append(fullCommands, commands...)

	var cmdLog strings.Builder
	fmt.Printf("\n=== BEGIN COMMAND SEQUENCE ===\n")
	for _, cmd := range fullCommands {
		fmt.Println(cmd)
	}
	fmt.Println("=== END COMMAND SEQUENCE ===")
	vm.logger.Info(cmdLog.String())

	// Create command string
	cmdStr := strings.Join(fullCommands, "\n")

	// Create command with sudo
	cmd := exec.Command("sudo", "vtysh")
	cmd.Stdin = strings.NewReader(cmdStr)

	// Capture both stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute command
	startTime := time.Now()
	err := cmd.Run()
	duration := time.Since(startTime)

	// Create detailed execution log
	var execLog strings.Builder
	fmt.Printf("\n=== COMMAND EXECUTION DETAILS ===\n")
	fmt.Printf("Execution Time: %v\n", duration)
	if stdout.Len() > 0 {
		fmt.Printf("\nSTDOUT:\n")
		fmt.Println(stdout.String())
	}
	if stderr.Len() > 0 {
		fmt.Printf("\nSTDERR:\n")
		fmt.Println(stderr.String())
	}
	fmt.Printf("\n=== END EXECUTION DETAILS ===")
	vm.logger.Debug(execLog.String())

	if err != nil {
		// Log detailed error information
		vm.logger.Error(fmt.Sprintf("❌ FRR Configuration FAILED - Error: %v", err))
		if stderr.Len() > 0 {
			vm.logger.Error(fmt.Sprintf("Error details: %s", stderr.String()))
		}

		// Check for specific FRR error patterns
		errOutput := stderr.String()
		if strings.Contains(errOutput, "% Invalid") ||
			strings.Contains(errOutput, "% Malformed") ||
			strings.Contains(errOutput, "% Unknown") ||
			strings.Contains(errOutput, "Error:") {
			return fmt.Errorf("FRR configuration error: %s", errOutput)
		}

		// Check for permission errors
		if strings.Contains(errOutput, "permission denied") ||
			strings.Contains(errOutput, "Permission denied") {
			return fmt.Errorf("vtysh permission denied: %s", errOutput)
		}

		// Check for connection errors
		if strings.Contains(errOutput, "Connection refused") ||
			strings.Contains(errOutput, "No such file or directory") {
			return fmt.Errorf("FRR daemon connection error: %s", errOutput)
		}

		return fmt.Errorf("vtysh execution failed: %v (stdout: %s, stderr: %s)",
			err, stdout.String(), stderr.String())
	}

	// Check for warnings in stdout
	if stdout.Len() > 0 {
		output := stdout.String()
		if strings.Contains(output, "Warning:") ||
			strings.Contains(output, "WARN:") {
			vm.logger.Warn(fmt.Sprintf("⚠️ FRR warnings:\n%s", output))
		}
		
		// Check for Unknown command errors in stdout
		if strings.Contains(output, "% Unknown command:") {
			return fmt.Errorf("FRR configuration error - Unknown commands found:\n%s", output)
		}
	}

	// Save configuration after successful execution
	if err := vm.WriteMemory(); err != nil {
		return fmt.Errorf("configuration applied but failed to save: %v", err)
	}

	vm.logger.Info("✓ FRR Configuration SUCCESS - All commands executed successfully")
	return nil
}

// ExecuteCommandsInContext executes multiple commands in a specific context
func (vm *VtyshManager) ExecuteCommandsInContext(context string, commands []string) error {
	if len(commands) == 0 {
		return nil
	}

	vm.logger.Debug(fmt.Sprintf("Executing commands in context '%s'", context))

	// Prepare session commands
	var sessionCommands []string
	if context != "" {
		sessionCommands = append(sessionCommands, "configure terminal", context)
	} else {
		sessionCommands = append(sessionCommands, "configure terminal")
	}

	sessionCommands = append(sessionCommands, commands...)

	if context != "" {
		sessionCommands = append(sessionCommands, "exit")
	}
	sessionCommands = append(sessionCommands, "end")

	// Create command string
	cmdStr := strings.Join(sessionCommands, "\n")
	vm.logger.Debug(fmt.Sprintf("Executing commands:\n%s", cmdStr))

	// Create command
	cmd := exec.Command("sudo", "vtysh")
	cmd.Stdin = strings.NewReader(cmdStr)

	// Capture both stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute command
	err := cmd.Run()

	// Log outputs regardless of error
	if stdout.Len() > 0 {
		vm.logger.Debug(fmt.Sprintf("vtysh stdout:\n%s", stdout.String()))
	}
	if stderr.Len() > 0 {
		vm.logger.Debug(fmt.Sprintf("vtysh stderr:\n%s", stderr.String()))
	}

	// Handle errors
	if err != nil {
		// Check if it's an exit error
		if exitErr, ok := err.(*exec.ExitError); ok {
			errMsg := fmt.Sprintf("vtysh context session failed: %v", exitErr)
			if stderr.Len() > 0 {
				errMsg = fmt.Sprintf("%s (stderr: %s)", errMsg, stderr.String())
			}
			if stdout.Len() > 0 {
				errMsg = fmt.Sprintf("%s (stdout: %s)", errMsg, stdout.String())
			}
			return errors.New(errMsg)
		}
		return fmt.Errorf("vtysh execution failed: %v", err)
	}

	// Check for error indicators in output
	outputStr := stdout.String()
	if strings.Contains(outputStr, "% Invalid") ||
		strings.Contains(outputStr, "% Unknown") ||
		strings.Contains(outputStr, "% No such") ||
		strings.Contains(outputStr, "% Incomplete") ||
		strings.Contains(outputStr, "% Error") {
		return fmt.Errorf("command failed in context '%s': %s", context, outputStr)
	}

	// Save configuration after successful execution
	if err := vm.WriteMemory(); err != nil {
		return fmt.Errorf("configuration applied but failed to save: %v", err)
	}

	vm.logger.Debug("vtysh context session completed successfully")
	return nil
}

// GetCurrentConfig retrieves current configuration for a specific section
func (vm *VtyshManager) GetCurrentConfig(section string) (string, error) {
	var command string

	switch section {
	case "bgp":
		command = "show running-config bgp"
	case "bgp-summary":
		command = "show bgp summary"
	case "bgp-neighbors":
		command = "show bgp neighbors"
	case "static":
		command = "show running-config zebra"
	case "route":
		command = "show ip route"
	default:
		command = fmt.Sprintf("show running-config %s", section)
	}

	return vm.ExecuteCommand(command)
}

// CheckProtocolRunning checks if a specific FRR protocol is running
func (vm *VtyshManager) CheckProtocolRunning(protocol string) (bool, error) {
	var command string
	var successPattern string

	switch protocol {
	case "bgp":
		command = "show bgp summary"
		successPattern = "BGP router identifier"
	case "static":
		command = "show ip route"
		successPattern = "Codes:"
	default:
		return false, fmt.Errorf("unsupported protocol: %s", protocol)
	}

	output, err := vm.ExecuteCommand(command)
	if err != nil {
		// Protocol might not be configured yet
		if strings.Contains(err.Error(), "not configured") ||
			strings.Contains(err.Error(), "No BGP process is configured") {
			return false, nil
		}
		return false, err
	}

	// Check if output contains expected pattern
	return strings.Contains(output, successPattern), nil
}

// ValidateVtyshAvailable checks if vtysh is available and accessible
func (vm *VtyshManager) ValidateVtyshAvailable() error {
	cmd := exec.Command("which", "vtysh")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vtysh not found in PATH: %v", err)
	}

	// Try to execute a simple command
	_, err := vm.ExecuteCommand("show version")
	if err != nil {
		return fmt.Errorf("vtysh not accessible: %v", err)
	}

	return nil
}

// WaitForProtocolReady waits for a protocol to be ready after configuration changes
func (vm *VtyshManager) WaitForProtocolReady(protocol string, timeout time.Duration) error {
	vm.logger.Info(fmt.Sprintf("Waiting for %s to be ready", protocol))

	start := time.Now()
	for time.Since(start) < timeout {
		running, err := vm.CheckProtocolRunning(protocol)
		if err == nil && running {
			vm.logger.Info(fmt.Sprintf("%s is ready", protocol))
			return nil
		}

		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("%s did not become ready within %v", protocol, timeout)
}
