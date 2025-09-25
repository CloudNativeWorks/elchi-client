package cmdrunner

import (
	"fmt"
	"os/exec"
	"strings"
)

func (r *CommandsRunner) SetCommand(cmd string, args ...string) *exec.Cmd {
	return exec.Command(cmd, args...)
}

func (r *CommandsRunner) SetCommandWithS(cmd string, args ...string) *exec.Cmd {
	return exec.Command("sudo", append([]string{cmd}, args...)...)
}

func (r *CommandsRunner) CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}

func (r *CommandsRunner) Run(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	output, err := c.CombinedOutput()
	if err != nil {
		r.logger.Errorf("command failed: %s %v\n%s", cmd, args, string(output))
		return fmt.Errorf("command error: %w\n%s", err, string(output))
	}
	return nil
}

func (r *CommandsRunner) RunWithOutput(cmd string, args ...string) ([]byte, error) {
	c := exec.Command(cmd, args...)
	output, err := c.CombinedOutput()
	if err != nil {
		r.logger.Errorf("command failed: %s %v\n%s", cmd, args, string(output))
		return nil, fmt.Errorf("command error: %w\n%s", err, string(output))
	}
	return output, nil
}

func (r *CommandsRunner) RunWithS(cmd string, args ...string) error {
	return r.Run("sudo", append([]string{cmd}, args...)...)
}

func (r *CommandsRunner) RunWithOutputS(cmd string, args ...string) ([]byte, error) {
	return r.RunWithOutput("sudo", append([]string{cmd}, args...)...)
}

func (r *CommandsRunner) RunAndTrimmedOutput(cmd string, args ...string) (string, error) {
	out, err := r.RunWithOutput(cmd, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
