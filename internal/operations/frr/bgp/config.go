package bgp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

// ConfigManager implements BGP configuration management
type ConfigManager struct {
	vtysh        *frr.VtyshManager
	logger       *logger.Logger
	validator    ValidationManagerInterface
	errorHandler ErrorHandlerInterface
}

// NewConfigManager creates a new config manager
func NewConfigManager(vtysh *frr.VtyshManager, logger *logger.Logger) ConfigManagerInterface {
	return &ConfigManager{
		vtysh:        vtysh,
		logger:       logger,
		validator:    NewValidationManager(logger),
		errorHandler: NewErrorHandler(logger),
	}
}

// ValidateConfig validates a BGP configuration
func (cm *ConfigManager) ValidateConfig(config *client.BgpConfig) error {
	if config == nil {
		return cm.errorHandler.NewValidationError("config", nil, "BGP configuration cannot be nil")
	}

	result := cm.validator.ValidateBgpConfig(config)
	if !result.Valid {
		// Return the first validation error
		if len(result.Errors) > 0 {
			err := result.Errors[0]
			return cm.errorHandler.NewValidationError(err.Field, err.Value, err.Message)
		}
	}

	return nil
}

// ApplyConfig applies a BGP configuration
func (cm *ConfigManager) ApplyConfig(config *client.BgpConfig) error {
	if config == nil {
		return cm.errorHandler.NewValidationError("config", nil, "Configuration required")
	}

	cm.logger.Info("BGP configuration is being applied")

	// Generate configuration commands
	commands, err := cm.generateConfigCommands(config)
	if err != nil {
		return cm.errorHandler.NewOperationError("generate_commands", err)
	}

	// Log commands to be applied
	cm.logger.Info(fmt.Sprintf("BGP commands to be applied (%d):", len(commands)))
	for i, cmd := range commands {
		cm.logger.Info(fmt.Sprintf("  %d. %s", i+1, cmd))
	}

	// Apply commands
	if err := cm.vtysh.ExecuteSimpleSession(commands); err != nil {
		return cm.errorHandler.NewOperationError("apply_config", err)
	}

	return nil
}

// GetCurrentConfig retrieves the current BGP configuration
func (cm *ConfigManager) GetCurrentConfig() (*client.BgpConfig, error) {
	cm.logger.Info("Retrieving current BGP configuration")

	config := &client.BgpConfig{}

	// Get global BGP configuration
	if err := cm.parseGlobalConfig(config); err != nil {
		return nil, cm.errorHandler.NewConfigError("parse_global_config", err)
	}

	return config, nil
}

// generateConfigCommands generates FRR commands for a BGP configuration
func (cm *ConfigManager) generateConfigCommands(config *client.BgpConfig) ([]string, error) {
	var commands []string

	// Get current config to check if AS number changed
	currentConfig, err := cm.GetCurrentConfig()
	if err != nil {
		return nil, err
	}

	// Start BGP configuration
	commands = append(commands, "configure terminal")

	// If AS number changed, remove old BGP process
	if currentConfig != nil && currentConfig.AutonomousSystem != 0 &&
		currentConfig.AutonomousSystem != config.AutonomousSystem {
		cm.logger.Info(fmt.Sprintf("AS number changing from %d to %d - removing old BGP process",
			currentConfig.AutonomousSystem, config.AutonomousSystem))
		commands = append(commands, fmt.Sprintf("no router bgp %d", currentConfig.AutonomousSystem))
	}

	// Start new BGP process
	commands = append(commands, fmt.Sprintf("router bgp %d", config.AutonomousSystem))

	// Router ID
	if config.RouterId != "" {
		commands = append(commands, fmt.Sprintf("bgp router-id %s", config.RouterId))
	}

	// Timers
	if config.KeepaliveTime > 0 && config.HoldTime > 0 {
		commands = append(commands, fmt.Sprintf("timers bgp %d %d", config.KeepaliveTime, config.HoldTime))
	}

	// Log neighbor changes
	if config.LogNeighborChanges {
		commands = append(commands, "bgp log-neighbor-changes")
	} else {
		commands = append(commands, "no bgp log-neighbor-changes")
	}

	// Deterministic MED
	if config.DeterministicMed {
		commands = append(commands, "bgp deterministic-med")
	} else {
		commands = append(commands, "no bgp deterministic-med")
	}

	// Always compare MED
	if config.AlwaysCompareMed {
		commands = append(commands, "bgp always-compare-med")
	} else {
		commands = append(commands, "no bgp always-compare-med")
	}

	// Graceful Restart Configuration
	if config.GracefulRestartEnabled {
		commands = append(commands, "bgp graceful-restart")
		
		// Graceful restart timer (only set if provided)
		if config.GracefulRestartTime > 0 {
			commands = append(commands, fmt.Sprintf("bgp graceful-restart restart-time %d", config.GracefulRestartTime))
		}
		
		// Stale path time (only set if provided)
		if config.GracefulStalePathTime > 0 {
			commands = append(commands, fmt.Sprintf("bgp graceful-restart stalepath-time %d", config.GracefulStalePathTime))
		}
		
		// Route selection defer time (only set if provided)
		if config.SelectDeferTime > 0 {
			commands = append(commands, fmt.Sprintf("bgp graceful-restart select-defer-time %d", config.SelectDeferTime))
		}
		
		// RIB stale time (only set if provided)
		if config.RibStaleTime > 0 {
			commands = append(commands, fmt.Sprintf("bgp graceful-restart rib-stale-time %d", config.RibStaleTime))
		}
		
		// Preserve forwarding state
		if config.PreserveForwardingState {
			commands = append(commands, "bgp graceful-restart preserve-fw-state")
		}
	} else if config.GracefulRestartDisable {
		// Explicitly disable graceful restart
		commands = append(commands, "no bgp graceful-restart")
	}

	// Enter address-family ipv4 unicast
	commands = append(commands, "address-family ipv4 unicast")

	// Maximum paths
	if config.MaximumPaths > 0 {
		commands = append(commands, fmt.Sprintf("maximum-paths %d", config.MaximumPaths))
	} else {
		commands = append(commands, "no maximum-paths")
	}

	// Administrative distance
	if config.AdministrativeDistance != "" {
		// split distance values separated by -
		distances := strings.Split(config.AdministrativeDistance, "-")
		if len(distances) == 3 {
			commands = append(commands, fmt.Sprintf("distance bgp %s %s %s",
				distances[0], distances[1], distances[2]))
		} else {
			// if, given value is used three times
			commands = append(commands, fmt.Sprintf("distance bgp %s %s %s",
				config.AdministrativeDistance,
				config.AdministrativeDistance,
				config.AdministrativeDistance))
		}
	} else {
		commands = append(commands, "no distance bgp")
	}

	// Redistribution
	if config.RedistributeConnected {
		commands = append(commands, "redistribute connected")
	} else {
		commands = append(commands, "no redistribute connected")
	}
	if config.RedistributeStatic {
		commands = append(commands, "redistribute static")
	} else {
		commands = append(commands, "no redistribute static")
	}
	if config.RedistributeKernel {
		commands = append(commands, "redistribute kernel")
	} else {
		commands = append(commands, "no redistribute kernel")
	}
	if config.RedistributeLocal {
		commands = append(commands, "redistribute local")
	} else {
		commands = append(commands, "no redistribute local")
	}

	// Exit address-family
	commands = append(commands, "exit-address-family")

	// Exit router bgp
	commands = append(commands, "exit")

	return commands, nil
}

// parseGlobalConfig parses global BGP configuration from FRR
func (cm *ConfigManager) parseGlobalConfig(config *client.BgpConfig) error {
	output, err := cm.vtysh.ExecuteCommand("show running-config bgp")
	if err != nil {
		return fmt.Errorf("failed to get BGP configuration: %v", err)
	}

	lines := strings.Split(output, "\n")
	inAddressFamily := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Parse router bgp AS
		if strings.HasPrefix(line, "router bgp ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				if as, err := strconv.ParseUint(parts[2], 10, 32); err == nil {
					config.AutonomousSystem = uint32(as)
				}
			}
		}

		// Check if we're entering address-family section
		if strings.HasPrefix(line, "address-family ipv4 unicast") {
			inAddressFamily = true
			continue
		}

		// Check if we're exiting address-family section
		if line == "exit-address-family" {
			inAddressFamily = false
			continue
		}

		// Parse maximum-paths inside address-family
		if inAddressFamily && strings.HasPrefix(line, "maximum-paths ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if maxPaths, err := strconv.ParseUint(parts[1], 10, 32); err == nil {
					config.MaximumPaths = uint32(maxPaths)
				}
			}
		}

		// Parse distance inside address-family
		if inAddressFamily && strings.HasPrefix(line, "distance bgp ") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				distanceStr := fmt.Sprintf("%s-%s-%s", parts[2], parts[3], parts[4])
				config.AdministrativeDistance = distanceStr
			}
		}

		// Parse router ID
		if strings.HasPrefix(line, "bgp router-id ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				config.RouterId = parts[2]
			}
		}

		// Parse log neighbor changes
		if line == "bgp log-neighbor-changes" {
			config.LogNeighborChanges = true
		}

		// Parse deterministic med
		if line == "bgp deterministic-med" {
			config.DeterministicMed = true
		}

		// Parse always compare med
		if line == "bgp always-compare-med" {
			config.AlwaysCompareMed = true
		}

		// Parse timers
		if strings.HasPrefix(line, "timers bgp ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if keepalive, err := strconv.ParseUint(parts[2], 10, 32); err == nil {
					config.KeepaliveTime = uint32(keepalive)
				}
				if holdtime, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					config.HoldTime = uint32(holdtime)
				}
			}
		}

		// Parse redistribute
		if strings.Contains(line, "redistribute connected") {
			config.RedistributeConnected = true
		}
		if strings.Contains(line, "redistribute static") {
			config.RedistributeStatic = true
		}
		if strings.Contains(line, "redistribute kernel") {
			config.RedistributeKernel = true
		}
		if strings.Contains(line, "redistribute local") {
			config.RedistributeLocal = true
		}

		// Parse graceful restart
		if line == "bgp graceful-restart" {
			config.GracefulRestartEnabled = true
		}
		if strings.HasPrefix(line, "bgp graceful-restart restart-time ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if restartTime, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					config.GracefulRestartTime = uint32(restartTime)
				}
			}
		}
		if strings.HasPrefix(line, "bgp graceful-restart stalepath-time ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if staleTime, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					config.GracefulStalePathTime = uint32(staleTime)
				}
			}
		}
		if strings.HasPrefix(line, "bgp graceful-restart select-defer-time ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if deferTime, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					config.SelectDeferTime = uint32(deferTime)
				}
			}
		}
		if strings.HasPrefix(line, "bgp graceful-restart rib-stale-time ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if ribTime, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					config.RibStaleTime = uint32(ribTime)
				}
			}
		}
		if line == "bgp graceful-restart preserve-fw-state" {
			config.PreserveForwardingState = true
		}
	}

	return nil
}
