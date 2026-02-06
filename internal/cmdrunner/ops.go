package cmdrunner

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func (r *CommandsRunner) SetCommand(ctx context.Context, cmd string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, cmd, args...)
}

func (r *CommandsRunner) SetCommandWithS(ctx context.Context, cmd string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "sudo", append([]string{cmd}, args...)...)
}

func (r *CommandsRunner) CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}

func (r *CommandsRunner) Run(ctx context.Context, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	output, err := c.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.logger.Errorf("command failed: %s %v\n%s", cmd, args, string(output))
		return fmt.Errorf("command error: %w\n%s", err, string(output))
	}
	return nil
}

func (r *CommandsRunner) RunWithOutput(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, cmd, args...)
	output, err := c.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r.logger.Errorf("command failed: %s %v\n%s", cmd, args, string(output))
		return nil, fmt.Errorf("command error: %w\n%s", err, string(output))
	}
	return output, nil
}

func (r *CommandsRunner) RunWithS(ctx context.Context, cmd string, args ...string) error {
	return r.Run(ctx, "sudo", append([]string{cmd}, args...)...)
}

func (r *CommandsRunner) RunWithOutputS(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	return r.RunWithOutput(ctx, "sudo", append([]string{cmd}, args...)...)
}

func (r *CommandsRunner) RunAndTrimmedOutput(ctx context.Context, cmd string, args ...string) (string, error) {
	out, err := r.RunWithOutput(ctx, cmd, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RunWithOutputSNoErrLog runs command with sudo and returns output without logging errors
// Useful for commands like "systemctl status" where non-zero exit codes are expected
func (r *CommandsRunner) RunWithOutputSNoErrLog(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "sudo", append([]string{cmd}, args...)...)
	output, err := c.CombinedOutput()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Don't log error - caller will handle it
	return output, err
}
