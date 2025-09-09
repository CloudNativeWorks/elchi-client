package bgp

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

// Ensure Manager implements BGPManagerInterface
var _ BGPManagerInterface = (*Manager)(nil)

// Manager implements BGP management
type Manager struct {
	vtysh             *frr.VtyshManager
	logger            *logger.Logger
	configManager     ConfigManagerInterface
	neighborManager   NeighborManagerInterface
	policyManager     PolicyManagerInterface
	stateManager      StateManagerInterface
	validationManager ValidationManagerInterface
	errorHandler      ErrorHandlerInterface
}

// NewManager creates a new BGP manager
func NewManager(vtysh *frr.VtyshManager, logger *logger.Logger) BGPManagerInterface {
	return &Manager{
		vtysh:             vtysh,
		logger:            logger,
		configManager:     NewConfigManager(vtysh, logger),
		neighborManager:   NewNeighborManager(vtysh, logger),
		policyManager:     NewPolicyManager(vtysh, logger),
		stateManager:      NewStateManager(vtysh, logger),
		validationManager: NewValidationManager(logger),
		errorHandler:      NewErrorHandler(logger),
	}
}

// SetConfig applies BGP configuration using ConfigManager
func (m *Manager) SetConfig(config *client.BgpConfig) error {
	if config == nil {
		return m.errorHandler.NewConfigError("set_config", fmt.Errorf("BGP config is nil"))
	}

	m.logger.Info(fmt.Sprintf("Applying BGP configuration for AS %d", config.AutonomousSystem))

	// Validate configuration first
	if err := m.configManager.ValidateConfig(config); err != nil {
		return err
	}

	// Apply configuration using ConfigManager
	if err := m.configManager.ApplyConfig(config); err != nil {
		return err
	}

	return nil
}

// GetConfig returns current BGP configuration by parsing running configuration
func (m *Manager) GetConfig() (*client.BgpConfig, error) {
	m.logger.Info("Retrieving current BGP configuration from running config")

	// Use ConfigManager to get current configuration
	config, err := m.configManager.GetCurrentConfig()
	if err != nil {
		return nil, m.errorHandler.NewOperationError("get_config", err)
	}

	m.logger.Debug(fmt.Sprintf("Retrieved BGP config for AS %d with %d neighbors",
		config.AutonomousSystem, len(config.Neighbors)))

	return config, nil
}

// RemoveNeighbor removes a BGP neighbor
func (m *Manager) RemoveNeighbor(asNumber uint32, peerIP string) error {
	return m.neighborManager.RemoveNeighbor(asNumber, peerIP)
}

// GetState retrieves BGP state
func (m *Manager) GetState() (*client.Ipv4UnicastSummary, error) {
	return m.stateManager.GetBgpState()
}

// GetBgpRoutes retrieves BGP routes with new Routes structure
func (m *Manager) GetBgpRoutes() (*client.Routes, error) {
	return m.stateManager.ParseBgpRoutesNew()
}

// GetPolicyManager returns policy manager
func (m *Manager) GetPolicyManager() PolicyManagerInterface {
	return m.policyManager
}

// GetNeighborManager returns neighbor manager
func (m *Manager) GetNeighborManager() NeighborManagerInterface {
	return m.neighborManager
}

// GetStateManager returns state manager
func (m *Manager) GetStateManager() StateManagerInterface {
	return m.stateManager
}
