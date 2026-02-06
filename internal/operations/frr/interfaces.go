package frr

import "time"

// VtyshManagerInterface defines the interface for vtysh operations
type VtyshManagerInterface interface {
	ExecuteCommand(command string) (string, error)
	ExecuteSession(session *VtyshSession) error
	ExecuteSimpleSession(commands []string) error
	ExecuteCommandsInContext(context string, commands []string) error
	GetCurrentConfig(section string) (string, error)
	CheckProtocolRunning(protocol string) (bool, error)
	ParseConfigSection(section string) (map[string][]string, error)
	ValidateVtyshAvailable() error
	WaitForProtocolReady(protocol string, timeout time.Duration) error
}
