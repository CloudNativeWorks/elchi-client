package cmdrunner

import (
	"os/exec"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

type CommandRunner interface {
	SetCommand(cmd string, args ...string) *exec.Cmd
	SetCommandWithS(cmd string, args ...string) *exec.Cmd
	CombinedOutput(cmd *exec.Cmd) ([]byte, error)
	Run(cmd string, args ...string) error
	RunWithOutput(cmd string, args ...string) ([]byte, error)
	RunWithS(cmd string, args ...string) error
	RunWithOutputS(cmd string, args ...string) ([]byte, error)
	RunAndTrimmedOutput(cmd string, args ...string) (string, error)
}

type CommandsRunner struct {
	logger *logger.Logger
}

func NewCommandsRunner() *CommandsRunner {
	return &CommandsRunner{logger: logger.NewLogger("command_runner")}
}
