package bgp

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

// NeighborManager implements BGP neighbor management
type NeighborManager struct {
	vtysh        *frr.VtyshManager
	logger       *logger.Logger
	validator    ValidationManagerInterface
	errorHandler ErrorHandlerInterface
}

// NeighborCommand represents a BGP neighbor command
type NeighborCommand struct {
	action          string
	command         string
	value           any
	isAddressFamily bool
}

// NeighborCommandBuilder handles building BGP neighbor commands
type NeighborCommandBuilder struct {
	peerIP     string
	commands   []string
	afCommands []string
	logger     *logger.Logger
}

// NewNeighborManager creates a new neighbor manager
func NewNeighborManager(vtysh *frr.VtyshManager, logger *logger.Logger) NeighborManagerInterface {
	return &NeighborManager{
		vtysh:        vtysh,
		logger:       logger,
		validator:    NewValidationManager(logger),
		errorHandler: NewErrorHandler(logger),
	}
}

// NewNeighborCommandBuilder creates a new command builder
func NewNeighborCommandBuilder(peerIP string, logger *logger.Logger) *NeighborCommandBuilder {
	return &NeighborCommandBuilder{
		peerIP:     peerIP,
		commands:   make([]string, 0),
		afCommands: make([]string, 0),
		logger:     logger,
	}
}

// AddCommand adds a command with proper context
func (b *NeighborCommandBuilder) AddCommand(cmd NeighborCommand) {
	cmdStr := b.generateCommand(cmd)
	if cmd.isAddressFamily {
		b.afCommands = append(b.afCommands, cmdStr)
	} else {
		b.commands = append(b.commands, cmdStr)
	}
}

// generateCommand generates a neighbor command string
func (b *NeighborCommandBuilder) generateCommand(cmd NeighborCommand) string {
	var command string

	if cmd.action == "no" {
		command = fmt.Sprintf("no neighbor %s %s", b.peerIP, cmd.command)
	} else if cmd.value != nil {
		command = fmt.Sprintf("neighbor %s %s %v", b.peerIP, cmd.command, cmd.value)
	} else {
		command = fmt.Sprintf("neighbor %s %s", b.peerIP, cmd.command)
	}

	b.logger.Debug(fmt.Sprintf("Generated %s command: %s",
		map[bool]string{true: "address-family", false: "neighbor"}[cmd.isAddressFamily], command))

	return command
}

// Build returns the final command list
func (b *NeighborCommandBuilder) Build() []string {
	var allCommands []string
	allCommands = append(allCommands, b.commands...)

	if len(b.afCommands) > 0 {
		allCommands = append(allCommands, "address-family ipv4 unicast")
		allCommands = append(allCommands, b.afCommands...)
		allCommands = append(allCommands, "exit-address-family")
	}

	return allCommands
}

// AddNeighbor adds a new BGP neighbor with idempotency guarantee
func (nm *NeighborManager) AddNeighbor(asNumber uint32, neighbor *client.BgpNeighbor) error {
	if neighbor == nil {
		return nm.errorHandler.NewValidationError("neighbor", nil, "Neighbor configuration required")
	}

	nm.logger.Info(fmt.Sprintf("Adding BGP neighbor %s (idempotent operation)", neighbor.PeerIp))

	// Validate IP address
	validationErr := nm.validator.ValidateIPAddresses([]string{neighbor.PeerIp})
	if validationErr != nil {
		return nm.errorHandler.NewValidationError("peer_ip", neighbor.PeerIp, "Invalid IP address format")
	}

	// IDEMPOTENCY CHECK: Check if neighbor already exists with same configuration
	existingNeighbor, err := nm.GetNeighborByIP(neighbor.PeerIp)
	if err == nil {
		// Neighbor exists, compare configurations
		if !nm.neighborNeedsUpdate(existingNeighbor, neighbor) {
			nm.logger.Info(fmt.Sprintf("BGP neighbor %s already exists with same configuration - idempotent success", neighbor.PeerIp))
			return nil // Idempotent success
		} else {
			nm.logger.Info(fmt.Sprintf("BGP neighbor %s exists but with different configuration - will update", neighbor.PeerIp))
			return nm.UpdateNeighbor(asNumber, neighbor) // Update instead of add
		}
	}

	// Generate commands
	commands, err := nm.generateAddNeighborCommands(asNumber, neighbor)
	if err != nil {
		return err
	}

	// Apply commands in a single session
	if err := nm.vtysh.ExecuteSimpleSession(commands); err != nil {
		nm.logger.Error(fmt.Sprintf("Failed to execute neighbor commands: %v", err))
		return nm.errorHandler.NewOperationError("add_neighbor", err)
	}

	nm.logger.Info(fmt.Sprintf("BGP neighbor %s added successfully", neighbor.PeerIp))
	return nil
}

// RemoveNeighbor removes a BGP neighbor with idempotency guarantee
func (nm *NeighborManager) RemoveNeighbor(asNumber uint32, peerIP string) error {
	nm.logger.Info(fmt.Sprintf("Removing BGP neighbor %s (idempotent operation)", peerIP))

	// Validate IP address
	validationErr := nm.validator.ValidateIPAddresses([]string{peerIP})
	if validationErr != nil {
		return nm.errorHandler.NewValidationError("peer_ip", peerIP, "Invalid IP address format")
	}

	// IDEMPOTENCY CHECK: Check if neighbor exists
	_, checkErr := nm.GetNeighborByIP(peerIP)
	if checkErr != nil {
		if IsValidationError(checkErr) {
			// Neighbor doesn't exist - idempotent success
			nm.logger.Info(fmt.Sprintf("BGP neighbor %s does not exist - idempotent success", peerIP))
			return nil
		}
		// Other error (not validation) - return it
		return checkErr
	}

	// Generate commands for removing neighbor
	commands, err := nm.generateRemoveNeighborCommands(asNumber, peerIP)
	if err != nil {
		return nm.errorHandler.NewOperationError("generate_remove_commands", err)
	}

	// Apply commands
	if err := nm.applyCommands(commands); err != nil {
		return err
	}

	nm.logger.Info(fmt.Sprintf("BGP neighbor %s removed successfully", peerIP))
	return nil
}

// Update Neighbor updates an existing BGP neighbor
func (nm *NeighborManager) UpdateNeighbor(asNumber uint32, neighbor *client.BgpNeighbor) error {
	if neighbor == nil {
		return nm.errorHandler.NewValidationError("neighbor", nil, "Neighbor configuration required")
	}

	// Validate IP address
	if net.ParseIP(neighbor.PeerIp) == nil {
		return nm.errorHandler.NewValidationError("peer_ip", neighbor.PeerIp, "Invalid IP address format")
	}

	// Get current neighbor configuration
	current, err := nm.GetNeighborByIP(neighbor.PeerIp)
	if err != nil {
		nm.logger.Error(fmt.Sprintf("Failed to get current neighbor config: %v", err))
		return err
	}

	// Generate update commands
	commands, err := nm.generateNeighborUpdateCommands(current, neighbor)
	if err != nil {
		nm.logger.Error(fmt.Sprintf("Failed to generate update commands: %v", err))
		return err
	}

	if len(commands) == 0 {
		nm.logger.Info("No changes needed for neighbor configuration")
		return nil
	}

	// Prepare full command sequence
	fullCommands := []string{
		"configure terminal",
		fmt.Sprintf("router bgp %d", asNumber),
	}
	fullCommands = append(fullCommands, commands...)
	fullCommands = append(fullCommands, "exit")

	// Apply commands in a single session
	if err := nm.vtysh.ExecuteSimpleSession(fullCommands); err != nil {
		nm.logger.Error(fmt.Sprintf("Failed to execute commands: %v", err))
		return nm.errorHandler.NewOperationError("update_neighbor", err)
	}

	nm.logger.Info(fmt.Sprintf("BGP neighbor update completed for %s", neighbor.PeerIp))
	return nil
}

// GetNeighborByIP retrieves a neighbor by IP address without requiring AS number
func (nm *NeighborManager) GetNeighborByIP(peerIP string) (*client.BgpNeighbor, error) {
	if peerIP == "" {
		return nil, nm.errorHandler.NewValidationError("peer_ip", peerIP, "Peer IP cannot be empty")
	}

	nm.logger.Debug(fmt.Sprintf("Getting BGP neighbor %s by IP only", peerIP))

	// First get the AS number from basic config parsing
	asNumber, err := nm.getASNumberForNeighbor(peerIP)
	if err != nil {
		return nil, err
	}

	// Get detailed configuration including address-family settings
	detailedNeighbor, err := nm.ParseNeighborDetails(peerIP, asNumber)
	if err != nil {
		return nil, err
	}

	nm.logger.Debug(fmt.Sprintf("Retrieved detailed neighbor %s: prefix_lists_in=%v, route_maps_in=%v",
		peerIP, detailedNeighbor.PrefixLists.PrefixListIn, detailedNeighbor.RouteMaps.RouteMapIn))

	return detailedNeighbor, nil
}

// ListNeighbors retrieves all BGP neighbors
func (nm *NeighborManager) ListNeighbors(asNumber uint32) ([]*client.BgpNeighbor, error) {
	nm.logger.Debug("Listing all BGP neighbors")

	// Parse neighbors from running configuration
	neighbors, err := nm.parseNeighborsFromConfig()
	if err != nil {
		return nil, nm.errorHandler.NewOperationError("parse_neighbors", err)
	}

	return neighbors, nil
}

// generateAddNeighborCommands generates FRR commands to add a neighbor
func (nm *NeighborManager) generateAddNeighborCommands(asNumber uint32, neighbor *client.BgpNeighbor) ([]string, error) {
	var commands []string

	nm.logger.Debug(fmt.Sprintf("Generating commands for neighbor %s", neighbor.PeerIp))

	// Enter configuration mode
	commands = append(commands, "configure terminal")

	// Enter BGP configuration mode
	commands = append(commands, fmt.Sprintf("router bgp %d", asNumber))

	// Generate neighbor-specific commands
	neighborCommands, err := nm.generateNeighborCommands(neighbor)
	if err != nil {
		return nil, err
	}
	commands = append(commands, neighborCommands...)

	// Exit configuration mode
	commands = append(commands, "exit")
	commands = append(commands, "exit")

	return commands, nil
}

// generateRemoveNeighborCommands generates FRR commands to remove a neighbor
func (nm *NeighborManager) generateRemoveNeighborCommands(asNumber uint32, peerIP string) ([]string, error) {
	var commands []string

	// Enter BGP configuration mode
	commands = append(commands, "configure terminal")
	commands = append(commands, fmt.Sprintf("router bgp %d", asNumber))

	// Remove neighbor
	commands = append(commands, fmt.Sprintf("no neighbor %s", peerIP))

	// Exit configuration mode
	commands = append(commands, "exit")
	commands = append(commands, "exit")

	return commands, nil
}

// generateNeighborCommands generates all commands for a neighbor configuration
func (nm *NeighborManager) generateNeighborCommands(neighbor *client.BgpNeighbor) ([]string, error) {
	if neighbor == nil {
		return nil, fmt.Errorf("neighbor configuration is required")
	}

	// Validate IP address format
	if net.ParseIP(neighbor.PeerIp) == nil {
		return nil, fmt.Errorf("invalid peer IP address: %s", neighbor.PeerIp)
	}

	builder := NewNeighborCommandBuilder(neighbor.PeerIp, nm.logger)

	// Basic neighbor configuration
	builder.AddCommand(NeighborCommand{command: fmt.Sprintf("remote-as %d", neighbor.RemoteAs)})

	// Description
	if neighbor.Description != "" {
		builder.AddCommand(NeighborCommand{
			command: "description",
			value:   nm.sanitizeDescription(neighbor.Description),
		})
	}

	// Password
	if neighbor.Password != "" {
		builder.AddCommand(NeighborCommand{
			command: "password",
			value:   neighbor.Password,
		})
	}

	// Update source
	if neighbor.UpdateSource != "" {
		builder.AddCommand(NeighborCommand{
			command: "update-source",
			value:   neighbor.UpdateSource,
		})
	}

	// EBGP multihop
	if neighbor.EbgpMultihop {
		cmd := NeighborCommand{command: "ebgp-multihop"}
		if neighbor.EbgpMultihopTtl > 0 {
			cmd.value = neighbor.EbgpMultihopTtl
		}
		builder.AddCommand(cmd)
	}

	// Disable connected check
	if neighbor.DisableConnectedCheck {
		builder.AddCommand(NeighborCommand{command: "disable-connected-check"})
	}

	// Next hop self (address-family specific)
	if neighbor.NextHopSelf {
		builder.AddCommand(NeighborCommand{
			command:         "next-hop-self",
			isAddressFamily: true,
		})
	}

	// Soft reconfiguration (new field name)
	if neighbor.SoftReconfiguration {
		builder.AddCommand(NeighborCommand{
			command:         "soft-reconfiguration inbound",
			isAddressFamily: true,
		})
	}

	// Allowas in
	if neighbor.AllowasIn > 0 {
		builder.AddCommand(NeighborCommand{
			command: "allowas-in",
			value:   neighbor.AllowasIn,
		})
	}

	// Weight
	if neighbor.Weight > 0 {
		builder.AddCommand(NeighborCommand{
			command: "weight",
			value:   neighbor.Weight,
		})
	}

	// Maximum prefix (new field - address-family specific)
	if neighbor.MaximumPrefix > 0 {
		builder.AddCommand(NeighborCommand{
			command:         fmt.Sprintf("maximum-prefix %d", neighbor.MaximumPrefix),
			isAddressFamily: true,
		})
	}

	// Maximum prefix out (new field - address-family specific)
	if neighbor.MaximumPrefixOut > 0 {
		builder.AddCommand(NeighborCommand{
			command:         fmt.Sprintf("maximum-prefix %d out", neighbor.MaximumPrefixOut),
			isAddressFamily: true,
		})
	}

	// Timers
	if neighbor.Timers != nil {
		nm.addTimerCommands(builder, neighbor.Timers)
	}

	// Route maps
	if neighbor.RouteMaps != nil {
		nm.addRouteMapCommands(builder, neighbor.RouteMaps)
	}

	// Prefix lists
	if neighbor.PrefixLists != nil {
		nm.addPrefixListCommands(builder, neighbor.PrefixLists)
	}

	// Shutdown state
	if neighbor.Shutdown {
		builder.AddCommand(NeighborCommand{command: "shutdown"})
	}

	return builder.Build(), nil
}

// generateNeighborUpdateCommands generates commands to update a neighbor
func (nm *NeighborManager) generateNeighborUpdateCommands(current, desired *client.BgpNeighbor) ([]string, error) {
	if current == nil || desired == nil {
		return nil, fmt.Errorf("both current and desired configurations are required")
	}

	// If remote AS changed, recreate the entire neighbor
	if current.RemoteAs != desired.RemoteAs {
		return nm.generateNeighborCommands(desired)
	}

	builder := NewNeighborCommandBuilder(desired.PeerIp, nm.logger)

	// Process each attribute for changes
	nm.processAttributeChanges(builder, current, desired)

	return builder.Build(), nil
}

// Helper methods for command generation
func (nm *NeighborManager) addTimerCommands(builder *NeighborCommandBuilder, timers *client.BgpNeighborTimers) {
	if timers.Keepalive > 0 && timers.Holdtime > 0 {
		builder.AddCommand(NeighborCommand{
			command: fmt.Sprintf("timers %d %d", timers.Keepalive, timers.Holdtime),
		})
	}
	if timers.ConnectRetry > 0 {
		builder.AddCommand(NeighborCommand{
			command: fmt.Sprintf("timers connect %d", timers.ConnectRetry),
		})
	}
}

func (nm *NeighborManager) addRouteMapCommands(builder *NeighborCommandBuilder, routeMaps *client.BgpNeighborRouteMaps) {
	// Handle multiple route maps in (array)
	for _, routeMapIn := range routeMaps.RouteMapIn {
		if routeMapIn != "" {
			builder.AddCommand(NeighborCommand{
				command:         fmt.Sprintf("route-map %s in", routeMapIn),
				isAddressFamily: true,
			})
		}
	}

	// Handle multiple route maps out (array)
	for _, routeMapOut := range routeMaps.RouteMapOut {
		if routeMapOut != "" {
			builder.AddCommand(NeighborCommand{
				command:         fmt.Sprintf("route-map %s out", routeMapOut),
				isAddressFamily: true,
			})
		}
	}
}

func (nm *NeighborManager) addPrefixListCommands(builder *NeighborCommandBuilder, prefixLists *client.BgpNeighborPrefixLists) {
	// Handle multiple prefix lists in (array)
	for _, prefixListIn := range prefixLists.PrefixListIn {
		if prefixListIn != "" {
			builder.AddCommand(NeighborCommand{
				command:         fmt.Sprintf("prefix-list %s in", prefixListIn),
				isAddressFamily: true,
			})
		}
	}

	// Handle multiple prefix lists out (array)
	for _, prefixListOut := range prefixLists.PrefixListOut {
		if prefixListOut != "" {
			builder.AddCommand(NeighborCommand{
				command:         fmt.Sprintf("prefix-list %s out", prefixListOut),
				isAddressFamily: true,
			})
		}
	}
}

func (nm *NeighborManager) processAttributeChanges(builder *NeighborCommandBuilder, current, desired *client.BgpNeighbor) {
	// Description
	if current.Description != desired.Description {
		if desired.Description != "" {
			builder.AddCommand(NeighborCommand{
				command: "description",
				value:   nm.sanitizeDescription(desired.Description),
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "description",
			})
		}
	}

	// Password
	if current.Password != desired.Password {
		if desired.Password != "" {
			builder.AddCommand(NeighborCommand{
				command: "password",
				value:   desired.Password,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "password",
			})
		}
	}

	// Update source
	if current.UpdateSource != desired.UpdateSource {
		if desired.UpdateSource != "" {
			builder.AddCommand(NeighborCommand{
				command: "update-source",
				value:   desired.UpdateSource,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "update-source",
			})
		}
	}

	// EBGP multihop
	if current.EbgpMultihop != desired.EbgpMultihop || current.EbgpMultihopTtl != desired.EbgpMultihopTtl {
		if !desired.EbgpMultihop {
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "ebgp-multihop",
			})
		} else {
			cmd := NeighborCommand{command: "ebgp-multihop"}
			if desired.EbgpMultihopTtl > 0 {
				cmd.value = desired.EbgpMultihopTtl
			}
			builder.AddCommand(cmd)
		}
	}

	// Boolean flags
	nm.processBooleanFlags(builder, current, desired)

	// Allowas in
	if current.AllowasIn != desired.AllowasIn {
		if desired.AllowasIn > 0 {
			builder.AddCommand(NeighborCommand{
				command: "allowas-in",
				value:   desired.AllowasIn,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "allowas-in",
			})
		}
	}

	// Weight
	if current.Weight != desired.Weight {
		if desired.Weight > 0 {
			builder.AddCommand(NeighborCommand{
				command: "weight",
				value:   desired.Weight,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "weight",
			})
		}
	}

	// Maximum prefix (new field)
	if current.MaximumPrefix != desired.MaximumPrefix {
		if desired.MaximumPrefix > 0 {
			builder.AddCommand(NeighborCommand{
				command:         fmt.Sprintf("maximum-prefix %d", desired.MaximumPrefix),
				isAddressFamily: true,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:          "no",
				command:         "maximum-prefix",
				isAddressFamily: true,
			})
		}
	}

	// Maximum prefix out (new field)
	if current.MaximumPrefixOut != desired.MaximumPrefixOut {
		if desired.MaximumPrefixOut > 0 {
			builder.AddCommand(NeighborCommand{
				command:         fmt.Sprintf("maximum-prefix %d out", desired.MaximumPrefixOut),
				isAddressFamily: true,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:          "no",
				command:         "maximum-prefix out",
				isAddressFamily: true,
			})
		}
	}

	// Timers
	if !nm.timersEqual(current.Timers, desired.Timers) {
		if desired.Timers != nil {
			nm.addTimerCommands(builder, desired.Timers)
		} else {
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "timers",
			})
			builder.AddCommand(NeighborCommand{
				action:  "no",
				command: "timers connect",
			})
		}
	}

	// Route maps - smart update (remove old, add new)
	if !nm.routeMapsEqual(current.RouteMaps, desired.RouteMaps) {
		nm.logger.Debug(fmt.Sprintf("Route maps differ, updating neighbor %s", builder.peerIP))

		// Get current route maps (handle nil)
		var currentIn, currentOut []string
		if current.RouteMaps != nil {
			currentIn = current.RouteMaps.RouteMapIn
			currentOut = current.RouteMaps.RouteMapOut
		}

		// Get desired route maps (handle nil)
		var desiredIn, desiredOut []string
		if desired.RouteMaps != nil {
			desiredIn = desired.RouteMaps.RouteMapIn
			desiredOut = desired.RouteMaps.RouteMapOut
		}

		nm.logger.Debug(fmt.Sprintf("Current route maps: in=%v, out=%v", currentIn, currentOut))
		nm.logger.Debug(fmt.Sprintf("Desired route maps: in=%v, out=%v", desiredIn, desiredOut))

		// Remove route maps that exist in current but not in desired
		// Remove route maps in that are no longer needed
		for _, routeMapIn := range currentIn {
			if !nm.stringContains(desiredIn, routeMapIn) {
				nm.logger.Debug(fmt.Sprintf("Removing route map in: %s", routeMapIn))
				builder.AddCommand(NeighborCommand{
					action:          "no",
					command:         fmt.Sprintf("route-map %s in", routeMapIn),
					isAddressFamily: true,
				})
			}
		}

		// Remove route maps out that are no longer needed
		for _, routeMapOut := range currentOut {
			if !nm.stringContains(desiredOut, routeMapOut) {
				nm.logger.Debug(fmt.Sprintf("Removing route map out: %s", routeMapOut))
				builder.AddCommand(NeighborCommand{
					action:          "no",
					command:         fmt.Sprintf("route-map %s out", routeMapOut),
					isAddressFamily: true,
				})
			}
		}

		// Add new route maps
		// Add new route maps in
		for _, routeMapIn := range desiredIn {
			if routeMapIn != "" && !nm.stringContains(currentIn, routeMapIn) {
				nm.logger.Debug(fmt.Sprintf("Adding route map in: %s", routeMapIn))
				builder.AddCommand(NeighborCommand{
					command:         fmt.Sprintf("route-map %s in", routeMapIn),
					isAddressFamily: true,
				})
			}
		}

		// Add new route maps out
		for _, routeMapOut := range desiredOut {
			if routeMapOut != "" && !nm.stringContains(currentOut, routeMapOut) {
				nm.logger.Debug(fmt.Sprintf("Adding route map out: %s", routeMapOut))
				builder.AddCommand(NeighborCommand{
					command:         fmt.Sprintf("route-map %s out", routeMapOut),
					isAddressFamily: true,
				})
			}
		}
	}

	// Prefix lists - smart update (remove old, add new)
	if !nm.prefixListsEqual(current.PrefixLists, desired.PrefixLists) {
		nm.logger.Debug(fmt.Sprintf("Prefix lists differ, updating neighbor %s", builder.peerIP))

		// Get current prefix lists (handle nil)
		var currentIn, currentOut []string
		if current.PrefixLists != nil {
			currentIn = current.PrefixLists.PrefixListIn
			currentOut = current.PrefixLists.PrefixListOut
		}

		// Get desired prefix lists (handle nil)
		var desiredIn, desiredOut []string
		if desired.PrefixLists != nil {
			desiredIn = desired.PrefixLists.PrefixListIn
			desiredOut = desired.PrefixLists.PrefixListOut
		}

		nm.logger.Debug(fmt.Sprintf("Current prefix lists: in=%v (len=%d), out=%v (len=%d)",
			currentIn, len(currentIn), currentOut, len(currentOut)))
		nm.logger.Debug(fmt.Sprintf("Desired prefix lists: in=%v (len=%d), out=%v (len=%d)",
			desiredIn, len(desiredIn), desiredOut, len(desiredOut)))

		// Remove prefix lists that exist in current but not in desired
		// Remove prefix lists in that are no longer needed
		for _, prefixListIn := range currentIn {
			if !nm.stringContains(desiredIn, prefixListIn) {
				nm.logger.Debug(fmt.Sprintf("Removing prefix list in: %s (not in desired list)", prefixListIn))
				builder.AddCommand(NeighborCommand{
					action:          "no",
					command:         fmt.Sprintf("prefix-list %s in", prefixListIn),
					isAddressFamily: true,
				})
			}
		}

		// Remove prefix lists out that are no longer needed
		for _, prefixListOut := range currentOut {
			if !nm.stringContains(desiredOut, prefixListOut) {
				nm.logger.Debug(fmt.Sprintf("Removing prefix list out: %s (not in desired list)", prefixListOut))
				builder.AddCommand(NeighborCommand{
					action:          "no",
					command:         fmt.Sprintf("prefix-list %s out", prefixListOut),
					isAddressFamily: true,
				})
			}
		}

		// Add new prefix lists
		// Add new prefix lists in
		for _, prefixListIn := range desiredIn {
			if prefixListIn != "" && !nm.stringContains(currentIn, prefixListIn) {
				nm.logger.Debug(fmt.Sprintf("Adding prefix list in: %s (not in current list)", prefixListIn))
				builder.AddCommand(NeighborCommand{
					command:         fmt.Sprintf("prefix-list %s in", prefixListIn),
					isAddressFamily: true,
				})
			}
		}

		// Add new prefix lists out
		for _, prefixListOut := range desiredOut {
			if prefixListOut != "" && !nm.stringContains(currentOut, prefixListOut) {
				nm.logger.Debug(fmt.Sprintf("Adding prefix list out: %s (not in current list)", prefixListOut))
				builder.AddCommand(NeighborCommand{
					command:         fmt.Sprintf("prefix-list %s out", prefixListOut),
					isAddressFamily: true,
				})
			}
		}
	} else {
		nm.logger.Debug(fmt.Sprintf("Prefix lists are equal for neighbor %s - no changes needed", builder.peerIP))
	}
}

func (nm *NeighborManager) processBooleanFlags(builder *NeighborCommandBuilder, current, desired *client.BgpNeighbor) {
	// Process each boolean flag
	nm.processBooleanFlag(builder, "disable-connected-check", current.DisableConnectedCheck, desired.DisableConnectedCheck, false)
	nm.processBooleanFlag(builder, "next-hop-self", current.NextHopSelf, desired.NextHopSelf, true)
	nm.processBooleanFlag(builder, "soft-reconfiguration inbound", current.SoftReconfiguration, desired.SoftReconfiguration, true)
	nm.processBooleanFlag(builder, "shutdown", current.Shutdown, desired.Shutdown, false)
	
	// Process graceful restart flags
	nm.processGracefulRestartFlags(builder, current, desired)
}

func (nm *NeighborManager) processGracefulRestartFlags(builder *NeighborCommandBuilder, current, desired *client.BgpNeighbor) {
	// Check if graceful restart configuration changed
	if current.GracefulRestart != desired.GracefulRestart {
		if desired.GracefulRestart {
			builder.AddCommand(NeighborCommand{
				command:         "capability graceful-restart",
				isAddressFamily: false,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:          "no",
				command:         "capability graceful-restart",
				isAddressFamily: false,
			})
		}
	}
	
	// Check if graceful restart helper mode changed
	if current.GracefulRestartHelper != desired.GracefulRestartHelper {
		if desired.GracefulRestartHelper {
			builder.AddCommand(NeighborCommand{
				command:         "capability graceful-restart-helper",
				isAddressFamily: false,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:          "no",
				command:         "capability graceful-restart-helper",
				isAddressFamily: false,
			})
		}
	}
	
	// Check if graceful restart disable changed
	if current.GracefulRestartDisable != desired.GracefulRestartDisable {
		if desired.GracefulRestartDisable {
			builder.AddCommand(NeighborCommand{
				command:         "capability graceful-restart-disable",
				isAddressFamily: false,
			})
		} else {
			builder.AddCommand(NeighborCommand{
				action:          "no",
				command:         "capability graceful-restart-disable",
				isAddressFamily: false,
			})
		}
	}
}

func (nm *NeighborManager) processBooleanFlag(builder *NeighborCommandBuilder, command string, currentValue, desiredValue bool, isAddressFamily bool) {
	if currentValue != desiredValue {
		builder.AddCommand(NeighborCommand{
			action:          boolToAction(desiredValue),
			command:         command,
			isAddressFamily: isAddressFamily,
		})
	}
}

// Helper function to convert boolean to command action
func boolToAction(value bool) string {
	if !value {
		return "no"
	}
	return ""
}

// parseNeighborsFromConfig parses BGP neighbors from FRR running configuration
func (nm *NeighborManager) parseNeighborsFromConfig() ([]*client.BgpNeighbor, error) {
	output, err := nm.vtysh.ExecuteCommand("show running-config bgp")
	if err != nil {
		return nil, fmt.Errorf("failed to get BGP configuration: %v", err)
	}

	neighbors := make(map[string]*client.BgpNeighbor)
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "neighbor ") {
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}

			peerIP := parts[1]
			command := parts[2]

			// Initialize neighbor if not exists
			if neighbors[peerIP] == nil {
				neighbors[peerIP] = &client.BgpNeighbor{
					PeerIp: peerIP,
					Timers: &client.BgpNeighborTimers{},
				}
			}

			neighbor := neighbors[peerIP]

			// Parse different neighbor commands
			switch command {
			case "remote-as":
				if len(parts) >= 4 {
					if remoteAS, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
						neighbor.RemoteAs = uint32(remoteAS)
					}
				}
			case "description":
				if len(parts) >= 4 {
					// Join all remaining parts for description and remove all quotes
					description := strings.Join(parts[3:], " ")
					description = strings.Trim(description, "\"'")          // Remove surrounding quotes
					description = strings.ReplaceAll(description, "\"", "") // Remove any remaining quotes
					description = strings.ReplaceAll(description, "'", "")  // Remove any single quotes
					neighbor.Description = strings.TrimSpace(description)
				}
			case "password":
				if len(parts) >= 4 {
					neighbor.Password = parts[3]
				}
			case "update-source":
				if len(parts) >= 4 {
					neighbor.UpdateSource = parts[3]
				}
			case "timers":
				if len(parts) >= 5 && parts[3] != "connect" {
					if neighbor.Timers == nil {
						neighbor.Timers = &client.BgpNeighborTimers{}
					}
					if keepalive, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
						neighbor.Timers.Keepalive = uint32(keepalive)
					}
					if holdtime, err := strconv.ParseUint(parts[4], 10, 32); err == nil {
						neighbor.Timers.Holdtime = uint32(holdtime)
					}
				} else if len(parts) >= 5 && parts[3] == "connect" {
					if neighbor.Timers == nil {
						neighbor.Timers = &client.BgpNeighborTimers{}
					}
					if connectRetry, err := strconv.ParseUint(parts[4], 10, 32); err == nil {
						neighbor.Timers.ConnectRetry = uint32(connectRetry)
					}
				}
			case "next-hop-self":
				neighbor.NextHopSelf = true
			case "soft-reconfiguration":
				if len(parts) >= 4 && parts[3] == "inbound" {
					neighbor.SoftReconfiguration = true
				}
			case "shutdown":
				neighbor.Shutdown = true
			case "ebgp-multihop":
				neighbor.EbgpMultihop = true
				if len(parts) >= 4 {
					if ttl, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
						neighbor.EbgpMultihopTtl = uint32(ttl)
					}
				}
			case "disable-connected-check":
				neighbor.DisableConnectedCheck = true
			case "maximum-prefix":
				if len(parts) >= 4 {
					if len(parts) >= 5 && parts[4] == "out" {
						// maximum-prefix X out
						if maxPrefix, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
							neighbor.MaximumPrefixOut = uint32(maxPrefix)
						}
					} else {
						// maximum-prefix X
						if maxPrefix, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
							neighbor.MaximumPrefix = uint32(maxPrefix)
						}
					}
				}
			case "allowas-in":
				if len(parts) >= 4 {
					if allowasIn, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
						neighbor.AllowasIn = uint32(allowasIn)
					}
				}
			case "weight":
				if len(parts) >= 4 {
					if weight, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
						neighbor.Weight = uint32(weight)
					}
				}
			}
		}
	}

	// Convert map to slice
	var result []*client.BgpNeighbor
	for _, neighbor := range neighbors {
		result = append(result, neighbor)
	}

	return result, nil
}

// applyCommands applies a list of commands via optimized session execution
func (nm *NeighborManager) applyCommands(commands []string) error {
	if len(commands) == 0 {
		return nil
	}

	nm.logger.Info(fmt.Sprintf("Applying neighbor commands with %d operations", len(commands)))
	for _, cmd := range commands {
		nm.logger.Debug(fmt.Sprintf("Command: %s", cmd))
	}

	// Use session-based execution for better performance
	err := nm.vtysh.ExecuteSimpleSession(commands)
	if err != nil {
		return nm.errorHandler.NewOperationError("apply_neighbor_commands", err)
	}

	nm.logger.Info("Neighbor commands applied successfully")
	return nil
}

// neighborNeedsUpdate checks if a neighbor configuration needs to be updated
func (nm *NeighborManager) neighborNeedsUpdate(current, desired *client.BgpNeighbor) bool {
	needsUpdate := current.RemoteAs != desired.RemoteAs ||
		current.Description != desired.Description ||
		current.Password != desired.Password ||
		current.UpdateSource != desired.UpdateSource ||
		current.NextHopSelf != desired.NextHopSelf ||
		current.SoftReconfiguration != desired.SoftReconfiguration ||
		current.Shutdown != desired.Shutdown ||
		current.EbgpMultihop != desired.EbgpMultihop ||
		current.EbgpMultihopTtl != desired.EbgpMultihopTtl ||
		current.DisableConnectedCheck != desired.DisableConnectedCheck ||
		current.AllowasIn != desired.AllowasIn ||
		current.Weight != desired.Weight ||
		current.MaximumPrefix != desired.MaximumPrefix ||
		current.MaximumPrefixOut != desired.MaximumPrefixOut

	nm.logger.Debug(fmt.Sprintf("Neighbor %s needs update: %t", desired.PeerIp, needsUpdate))

	if needsUpdate {
		nm.logger.Debug("Differences found:")
		if current.RemoteAs != desired.RemoteAs {
			nm.logger.Debug(fmt.Sprintf("  RemoteAs: %d -> %d", current.RemoteAs, desired.RemoteAs))
		}
		if current.Description != desired.Description {
			nm.logger.Debug(fmt.Sprintf("  Description: '%s' -> '%s'", current.Description, desired.Description))
		}
		if current.Password != desired.Password {
			nm.logger.Debug(fmt.Sprintf("  Password: '%s' -> '%s'", current.Password, desired.Password))
		}
		if current.UpdateSource != desired.UpdateSource {
			nm.logger.Debug(fmt.Sprintf("  UpdateSource: '%s' -> '%s'", current.UpdateSource, desired.UpdateSource))
		}
		if current.NextHopSelf != desired.NextHopSelf {
			nm.logger.Debug(fmt.Sprintf("  NextHopSelf: %t -> %t", current.NextHopSelf, desired.NextHopSelf))
		}
		if current.SoftReconfiguration != desired.SoftReconfiguration {
			nm.logger.Debug(fmt.Sprintf("  SoftReconfiguration: %t -> %t", current.SoftReconfiguration, desired.SoftReconfiguration))
		}
		if current.Shutdown != desired.Shutdown {
			nm.logger.Debug(fmt.Sprintf("  Shutdown: %t -> %t", current.Shutdown, desired.Shutdown))
		}
		if current.MaximumPrefix != desired.MaximumPrefix {
			nm.logger.Debug(fmt.Sprintf("  MaximumPrefix: %d -> %d", current.MaximumPrefix, desired.MaximumPrefix))
		}
		if current.MaximumPrefixOut != desired.MaximumPrefixOut {
			nm.logger.Debug(fmt.Sprintf("  MaximumPrefixOut: %d -> %d", current.MaximumPrefixOut, desired.MaximumPrefixOut))
		}
	}

	return needsUpdate
}

// sanitizeDescription efficiently sanitizes neighbor description
func (nm *NeighborManager) sanitizeDescription(description string) string {
	// Use strings.Builder for efficient string manipulation
	var builder strings.Builder
	builder.Grow(len(description)) // Pre-allocate capacity

	// Process character by character (more efficient than multiple ReplaceAll calls)
	for _, char := range description {
		if char != '"' && char != '\'' {
			builder.WriteRune(char)
		}
	}

	return strings.TrimSpace(builder.String())
}

// timersEqual checks if two timers are equal
func (nm *NeighborManager) timersEqual(current, desired *client.BgpNeighborTimers) bool {
	if current == nil && desired == nil {
		return true
	}
	if current == nil || desired == nil {
		return false
	}
	return current.Keepalive == desired.Keepalive &&
		current.Holdtime == desired.Holdtime &&
		current.ConnectRetry == desired.ConnectRetry
}

// routeMapsEqual checks if two route maps are equal (now handling arrays)
func (nm *NeighborManager) routeMapsEqual(current, desired *client.BgpNeighborRouteMaps) bool {
	// Handle nil cases
	if current == nil && desired == nil {
		return true
	}

	// Convert nil to empty to normalize comparison
	var currentIn, currentOut, desiredIn, desiredOut []string
	if current != nil {
		currentIn = current.RouteMapIn
		currentOut = current.RouteMapOut
	}
	if desired != nil {
		desiredIn = desired.RouteMapIn
		desiredOut = desired.RouteMapOut
	}

	equal := nm.stringArraysEqual(currentIn, desiredIn) && nm.stringArraysEqual(currentOut, desiredOut)

	nm.logger.Debug(fmt.Sprintf("Route maps equal check: current_in=%v, current_out=%v, desired_in=%v, desired_out=%v, equal=%t",
		currentIn, currentOut, desiredIn, desiredOut, equal))

	return equal
}

// prefixListsEqual checks if two prefix lists are equal (now handling arrays)
func (nm *NeighborManager) prefixListsEqual(current, desired *client.BgpNeighborPrefixLists) bool {
	// Handle nil cases - normalize comparison
	if current == nil && desired == nil {
		return true
	}

	// Convert nil to empty to normalize comparison
	var currentIn, currentOut, desiredIn, desiredOut []string
	if current != nil {
		currentIn = current.PrefixListIn
		currentOut = current.PrefixListOut
	}
	if desired != nil {
		desiredIn = desired.PrefixListIn
		desiredOut = desired.PrefixListOut
	}

	// Handle edge case: empty object vs nil - treat {} as empty arrays
	if desired != nil && len(desiredIn) == 0 && len(desiredOut) == 0 {
		nm.logger.Debug("Desired prefix lists is empty object - normalizing to empty arrays")
		desiredIn = []string{}
		desiredOut = []string{}
	}

	equal := nm.stringArraysEqual(currentIn, desiredIn) && nm.stringArraysEqual(currentOut, desiredOut)

	nm.logger.Debug(fmt.Sprintf("Prefix lists equal check: current_in=%v, current_out=%v, desired_in=%v, desired_out=%v, equal=%t",
		currentIn, currentOut, desiredIn, desiredOut, equal))

	return equal
}

// stringArraysEqual helper function to compare string arrays
func (nm *NeighborManager) stringArraysEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stringContains checks if a string is present in a slice of strings
func (nm *NeighborManager) stringContains(slice []string, str string) bool {
	for _, s := range slice {
		if s == str {
			return true
		}
	}
	return false
}

// Parse NeighborDetails parses detailed neighbor configuration from FRR
func (nm *NeighborManager) ParseNeighborDetails(peerIP string, asNumber uint32) (*client.BgpNeighbor, error) {
	output, err := nm.vtysh.ExecuteCommand("show running-config bgp")
	if err != nil {
		return nil, fmt.Errorf("command execution error: %v", err)
	}

	neighbor := &client.BgpNeighbor{
		PeerIp:   peerIP,
		RemoteAs: asNumber,
		Timers:   &client.BgpNeighborTimers{},
		RouteMaps: &client.BgpNeighborRouteMaps{
			RouteMapIn:  []string{},
			RouteMapOut: []string{},
		},
		PrefixLists: &client.BgpNeighborPrefixLists{
			PrefixListIn:  []string{},
			PrefixListOut: []string{},
		},
	}

	lines := strings.Split(output, "\n")
	inAddressFamily := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check address-family context
		if strings.HasPrefix(line, "address-family ipv4 unicast") {
			inAddressFamily = true
			continue
		} else if line == "exit-address-family" {
			inAddressFamily = false
			continue
		}

		// Process neighbor configuration (both global and address-family context)
		if !strings.HasPrefix(line, fmt.Sprintf("neighbor %s ", peerIP)) {
			continue
		}

		nm.logger.Debug(fmt.Sprintf("Parsing neighbor line: %s (in_af: %t)", line, inAddressFamily))

		// Parse neighbor settings
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		// Parse basic settings
		switch {
		case strings.Contains(line, "remote-as"):
			if val, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
				neighbor.RemoteAs = uint32(val)
			}
		case strings.Contains(line, "description"):
			neighbor.Description = strings.Join(parts[3:], " ")
		case strings.Contains(line, "password"):
			neighbor.Password = parts[3]
		case strings.Contains(line, "update-source"):
			neighbor.UpdateSource = parts[3]
		case strings.Contains(line, "ebgp-multihop"):
			neighbor.EbgpMultihop = true
			if len(parts) > 3 {
				if val, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					neighbor.EbgpMultihopTtl = uint32(val)
				}
			}
		case strings.Contains(line, "disable-connected-check"):
			neighbor.DisableConnectedCheck = true
		case strings.Contains(line, "next-hop-self"):
			// Only process if in address-family context
			if inAddressFamily {
				neighbor.NextHopSelf = true
			}
		case strings.Contains(line, "soft-reconfiguration inbound"):
			// Only process if in address-family context
			if inAddressFamily {
				neighbor.SoftReconfiguration = true
			}
		case strings.Contains(line, "shutdown"):
			neighbor.Shutdown = true
		case strings.Contains(line, "maximum-prefix"):
			// Only process if in address-family context
			if inAddressFamily {
				if strings.Contains(line, " out") && len(parts) >= 5 {
					// maximum-prefix X out
					if val, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
						neighbor.MaximumPrefixOut = uint32(val)
					}
				} else if len(parts) >= 4 {
					// maximum-prefix X
					if val, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
						neighbor.MaximumPrefix = uint32(val)
					}
				}
			}
		case strings.Contains(line, "allowas-in"):
			if len(parts) > 3 {
				if val, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					neighbor.AllowasIn = uint32(val)
				}
			}
		case strings.Contains(line, "capability graceful-restart-disable"):
			neighbor.GracefulRestartDisable = true
		case strings.Contains(line, "capability graceful-restart-helper"):
			neighbor.GracefulRestartHelper = true
		case strings.Contains(line, "capability graceful-restart"):
			// Make sure this is not disable or helper
			if !strings.Contains(line, "disable") && !strings.Contains(line, "helper") {
				neighbor.GracefulRestart = true
			}
		case strings.Contains(line, "weight"):
			if val, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
				neighbor.Weight = uint32(val)
			}
		case strings.Contains(line, "timers"):
			if !strings.Contains(line, "connect") && len(parts) >= 4 {
				if keepalive, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					neighbor.Timers.Keepalive = uint32(keepalive)
				}
				if holdtime, err := strconv.ParseUint(parts[4], 10, 32); err == nil {
					neighbor.Timers.Holdtime = uint32(holdtime)
				}
			} else if strings.Contains(line, "connect") && len(parts) >= 4 {
				if connectRetry, err := strconv.ParseUint(parts[4], 10, 32); err == nil {
					neighbor.Timers.ConnectRetry = uint32(connectRetry)
				}
			}
		case strings.Contains(line, "route-map"):
			if len(parts) >= 5 {
				routeMapName := parts[3]
				direction := parts[4]
				nm.logger.Debug(fmt.Sprintf("Found route-map: %s %s", routeMapName, direction))

				switch direction {
				case "in":
					neighbor.RouteMaps.RouteMapIn = append(neighbor.RouteMaps.RouteMapIn, routeMapName)
				case "out":
					neighbor.RouteMaps.RouteMapOut = append(neighbor.RouteMaps.RouteMapOut, routeMapName)
				}
			}
		case strings.Contains(line, "prefix-list"):
			if len(parts) >= 5 {
				prefixListName := parts[3]
				direction := parts[4]
				nm.logger.Debug(fmt.Sprintf("Found prefix-list: %s %s", prefixListName, direction))
				switch direction {
				case "in":
					neighbor.PrefixLists.PrefixListIn = append(neighbor.PrefixLists.PrefixListIn, prefixListName)
				case "out":
					neighbor.PrefixLists.PrefixListOut = append(neighbor.PrefixLists.PrefixListOut, prefixListName)
				}
			}
		}
	}

	nm.logger.Debug(fmt.Sprintf("Parsed neighbor %s: route_maps_in=%v, prefix_lists_in=%v",
		peerIP, neighbor.RouteMaps.RouteMapIn, neighbor.PrefixLists.PrefixListIn))

	return neighbor, nil
}

// getASNumberForNeighbor gets the AS number for a specific neighbor (minimal parsing)
func (nm *NeighborManager) getASNumberForNeighbor(peerIP string) (uint32, error) {
	output, err := nm.vtysh.ExecuteCommand("show running-config bgp")
	if err != nil {
		return 0, nm.errorHandler.NewOperationError("get_bgp_config", err)
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Look for neighbor remote-as command
		if strings.HasPrefix(line, fmt.Sprintf("neighbor %s remote-as ", peerIP)) {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if remoteAS, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
					return uint32(remoteAS), nil
				}
			}
		}
	}

	return 0, nm.errorHandler.NewValidationError("neighbor", peerIP, fmt.Sprintf("Neighbor %s not found", peerIP))
}
