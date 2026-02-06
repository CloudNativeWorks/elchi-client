package bgp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

// ============================================================================
// Policy Manager Implementation
// ============================================================================

// PolicyManager implements BGP policy management (route maps, prefix lists, community lists)
type PolicyManager struct {
	vtysh        *frr.VtyshManager
	logger       *logger.Logger
	validator    ValidationManagerInterface
	errorHandler ErrorHandlerInterface
}

// NewPolicyManager creates a new policy manager
func NewPolicyManager(vtysh *frr.VtyshManager, logger *logger.Logger) PolicyManagerInterface {
	return &PolicyManager{
		vtysh:        vtysh,
		logger:       logger,
		validator:    NewValidationManager(logger),
		errorHandler: NewErrorHandler(logger),
	}
}

// ============================================================================
// Route Map Operations
// ============================================================================

// ApplyRouteMap applies a route map configuration with idempotent behavior (similar to neighbor approach)
func (pm *PolicyManager) ApplyRouteMap(routeMap *client.BgpRouteMap) error {
	if routeMap == nil {
		return pm.errorHandler.NewValidationError("route_map", nil, "Route map cannot be nil")
	}

	pm.logger.Info(fmt.Sprintf("Processing route map %s", routeMap.Name))

	// Validate route map
	if err := pm.ValidateRouteMap(routeMap); err != nil {
		return err
	}

	// Check if route map already exists with same configuration (idempotent check)
	_, err := pm.GetRouteMapDetails(routeMap.Name, routeMap.Sequence)
	if err == nil {
		// Route map exists, check if it needs updating
		pm.logger.Info(fmt.Sprintf("Current route map updated: %s", routeMap.Name))
		err = pm.updateRouteMap(routeMap)
	} else {
		// Route map doesn't exist, add it
		pm.logger.Info(fmt.Sprintf("New route map added: %s", routeMap.Name))
		err = pm.addRouteMap(routeMap)
	}

	if err != nil {
		pm.logger.Error(fmt.Sprintf("Route map operation failed: %v", err))
		return pm.errorHandler.NewOperationError("apply_route_map", err)
	}

	pm.logger.Info(fmt.Sprintf("Route map %s applied successfully", routeMap.Name))
	return nil
}

// RemoveRouteMap removes a route map configuration
func (pm *PolicyManager) RemoveRouteMap(name string) error {
	if name == "" {
		return pm.errorHandler.NewValidationError("route_map_name", name, "Route map name cannot be empty")
	}

	pm.logger.Info(fmt.Sprintf("Removing route map %s", name))

	// Check if route map exists
	exists, err := pm.isRouteMapExists(name)
	if err != nil {
		return pm.errorHandler.NewOperationError("check_route_map_exists", err)
	}
	if !exists {
		pm.logger.Info(fmt.Sprintf("Route map %s does not exist", name))
		return nil // Idempotent operation
	}

	// Generate and apply remove commands
	commands := pm.generateRemoveRouteMapCommands(name)
	if err := pm.applyCommands(commands); err != nil {
		return pm.errorHandler.NewConfigError("remove_route_map", err)
	}

	pm.logger.Info(fmt.Sprintf("Route map %s removed successfully", name))
	return nil
}

// GetRouteMapDetails retrieves details of a specific route map (similar to ParseNeighborDetails)
func (pm *PolicyManager) GetRouteMapDetails(name string, sequence uint32) (*client.BgpRouteMap, error) {
	if name == "" {
		return nil, pm.errorHandler.NewValidationError("route_map_name", name, "Route map name cannot be empty")
	}

	pm.logger.Debug(fmt.Sprintf("Getting route map details: %s seq %d", name, sequence))

	// Get running configuration
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return nil, pm.errorHandler.NewOperationError("get_running_config", err)
	}

	// Parse route map from running config
	routeMap, err := pm.parseRouteMapFromConfig(output, name, sequence)
	if err != nil {
		return nil, pm.errorHandler.NewOperationError("parse_route_map", err)
	}

	if routeMap == nil {
		return nil, pm.errorHandler.NewOperationError("route_map_not_found",
			fmt.Errorf("route map %s seq %d not found", name, sequence))
	}

	pm.logger.Debug(fmt.Sprintf("Retrieved route map details: %s", name))
	return routeMap, nil
}

// addRouteMap adds a new route map
func (pm *PolicyManager) addRouteMap(routeMap *client.BgpRouteMap) error {
	pm.logger.Info(fmt.Sprintf("Adding new route map: %s", routeMap.Name))

	// Generate and apply route map commands
	commands, err := pm.generateRouteMapCommands(routeMap)
	if err != nil {
		return pm.errorHandler.NewConfigError("generate_route_map_commands", err)
	}

	if err := pm.applyCommands(commands); err != nil {
		return pm.errorHandler.NewConfigError("apply_route_map_commands", err)
	}

	return nil
}

// updateRouteMap updates an existing route map with smart array handling
func (pm *PolicyManager) updateRouteMap(routeMap *client.BgpRouteMap) error {
	pm.logger.Info(fmt.Sprintf("Updating existing route map: %s", routeMap.Name))

	// Get current route map configuration
	currentRouteMap, err := pm.GetRouteMapDetails(routeMap.Name, routeMap.Sequence)
	if err != nil {
		return pm.errorHandler.NewOperationError("get_current_route_map", err)
	}

	// Check if update is needed using smart comparison
	needsUpdate, err := pm.routeMapNeedsUpdate(currentRouteMap, routeMap)
	if err != nil {
		return pm.errorHandler.NewOperationError("compare_route_maps", err)
	}

	if !needsUpdate {
		pm.logger.Info(fmt.Sprintf("Route map %s already configured correctly", routeMap.Name))
		return nil // Idempotent operation
	}

	// Apply smart updates
	err = pm.applyRouteMapUpdates(currentRouteMap, routeMap)
	if err != nil {
		return pm.errorHandler.NewOperationError("apply_route_map_updates", err)
	}

	return nil
}

// parseRouteMapFromConfig parses a specific route map from running configuration
func (pm *PolicyManager) parseRouteMapFromConfig(config, name string, sequence uint32) (*client.BgpRouteMap, error) {
	lines := strings.Split(config, "\n")

	// Look for specific route map configuration
	routeMapPattern := fmt.Sprintf("route-map %s", name)
	sequencePattern := fmt.Sprintf("%d", sequence)

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, routeMapPattern) && strings.Contains(line, sequencePattern) {
			// Found the route map, parse the block
			routeMap, _, err := pm.parseRouteMapBlock(lines, i)
			if err != nil {
				return nil, err
			}
			// Verify sequence matches
			if routeMap != nil && routeMap.Sequence == sequence {
				return routeMap, nil
			}
		}
	}

	return nil, fmt.Errorf("route map %s seq %d not found in configuration", name, sequence)
}

// routeMapNeedsUpdate compares current and desired route maps to determine if update is needed
func (pm *PolicyManager) routeMapNeedsUpdate(current, desired *client.BgpRouteMap) (bool, error) {
	// Compare basic fields
	if current.Action != desired.Action ||
		current.Description != desired.Description {
		pm.logger.Debug("Route map basic fields differ")
		return true, nil
	}

	// Compare match conditions (arrays)
	if !pm.matchConditionsEqual(current.MatchConditions, desired.MatchConditions) {
		pm.logger.Debug("Route map match conditions differ")
		return true, nil
	}

	// Compare set actions (single objects)
	if !pm.setActionsEqual(current.SetActions, desired.SetActions) {
		pm.logger.Debug("Route map set actions differ")
		return true, nil
	}

	return false, nil
}

// applyRouteMapUpdates applies smart updates to route map (similar to neighbor processAttributeChanges)
func (pm *PolicyManager) applyRouteMapUpdates(current, desired *client.BgpRouteMap) error {
	var commands []string

	// Enter configuration mode and route map context
	commands = append(commands, "configure terminal")

	actionStr := "permit"
	if desired.Action == client.BgpRouteMapAction_ROUTE_MAP_DENY {
		actionStr = "deny"
	}
	commands = append(commands, fmt.Sprintf("route-map %s %s %d", desired.Name, actionStr, desired.Sequence))

	// Update description if changed
	if current.Description != desired.Description {
		if desired.Description != "" {
			commands = append(commands, fmt.Sprintf("description %s", desired.Description))
		} else {
			commands = append(commands, "no description")
		}
	}

	// Smart match conditions update
	err := pm.updateMatchConditions(&commands, current.MatchConditions, desired.MatchConditions)
	if err != nil {
		return err
	}

	// Update set actions
	err = pm.updateSetActions(&commands, current.SetActions, desired.SetActions)
	if err != nil {
		return err
	}

	// Exit configuration
	commands = append(commands, "exit", "exit")

	// Apply commands
	return pm.applyCommands(commands)
}

// ============================================================================
// Community List Operations
// ============================================================================

// ApplyCommunityList applies a community list configuration with idempotent behavior (similar to prefix list approach)
func (pm *PolicyManager) ApplyCommunityList(communityList *client.BgpCommunityList) error {
	if communityList == nil {
		return pm.errorHandler.NewValidationError("community_list", nil, "Community list cannot be nil")
	}

	pm.logger.Info(fmt.Sprintf("Processing community list %s", communityList.Name))

	// Validate community list
	if err := pm.ValidateCommunityList(communityList); err != nil {
		return err
	}

	// Check if community list already exists with same configuration (sequence-specific check)
	_, err := pm.GetCommunityListDetails(communityList.Name, communityList.Sequence)
	if err == nil {
		// Community list exists with same sequence, check if it needs updating
		pm.logger.Info(fmt.Sprintf("Current community list updated: %s seq %d", communityList.Name, communityList.Sequence))
		err = pm.updateCommunityList(communityList)
	} else {
		// Community list doesn't exist with this sequence
		// Check if there are other sequences with same name that need to be removed first
		pm.logger.Info(fmt.Sprintf("Community list %s seq %d not found, checking for other sequences", communityList.Name, communityList.Sequence))
		err = pm.addCommunityListWithCleanup(communityList)
	}

	if err != nil {
		pm.logger.Error(fmt.Sprintf("Community list operation failed: %v", err))
		return pm.errorHandler.NewOperationError("apply_community_list", err)
	}

	pm.logger.Info(fmt.Sprintf("Community list %s applied successfully", communityList.Name))
	return nil
}

// GetCommunityListDetails retrieves details of a specific community list (similar to GetPrefixListDetails)
func (pm *PolicyManager) GetCommunityListDetails(name string, sequence uint32) (*client.BgpCommunityList, error) {
	if name == "" {
		return nil, pm.errorHandler.NewValidationError("community_list_name", name, "Community list name cannot be empty")
	}

	pm.logger.Debug(fmt.Sprintf("Getting community list details: %s seq %d", name, sequence))

	// Get running configuration
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return nil, pm.errorHandler.NewOperationError("get_running_config", err)
	}

	// Parse community list from running config
	communityList, err := pm.parseCommunityListFromConfig(output, name, sequence)
	if err != nil {
		return nil, pm.errorHandler.NewOperationError("parse_community_list", err)
	}

	if communityList == nil {
		return nil, pm.errorHandler.NewOperationError("community_list_not_found",
			fmt.Errorf("community list %s seq %d not found", name, sequence))
	}

	pm.logger.Debug(fmt.Sprintf("Retrieved community list details: %s", name))
	return communityList, nil
}

// addCommunityList adds a new community list
func (pm *PolicyManager) addCommunityList(communityList *client.BgpCommunityList) error {
	pm.logger.Info(fmt.Sprintf("Adding new community list: %s", communityList.Name))

	// Generate and apply community list commands
	commands, err := pm.generateCommunityListCommands(communityList)
	if err != nil {
		return pm.errorHandler.NewConfigError("generate_community_list_commands", err)
	}

	if err := pm.applyCommands(commands); err != nil {
		return pm.errorHandler.NewConfigError("apply_community_list_commands", err)
	}

	return nil
}

// updateCommunityList updates an existing community list if configuration differs
func (pm *PolicyManager) updateCommunityList(communityList *client.BgpCommunityList) error {
	pm.logger.Info(fmt.Sprintf("Updating existing community list: %s", communityList.Name))

	// Check if community list already exists with same configuration
	exists, err := pm.isCommunityListConfigured(communityList)
	if err != nil {
		return pm.errorHandler.NewOperationError("check_community_list", err)
	}
	if exists {
		pm.logger.Info(fmt.Sprintf("Community list %s already configured correctly", communityList.Name))
		return nil // Idempotent operation
	}

	// Remove existing community list entry and add the new one
	removeCommands := pm.generateRemoveCommunityListCommands(communityList.Name)
	if err := pm.applyCommands(removeCommands); err != nil {
		return pm.errorHandler.NewConfigError("remove_community_list_commands", err)
	}

	// Add updated community list
	addCommands, err := pm.generateCommunityListCommands(communityList)
	if err != nil {
		return pm.errorHandler.NewConfigError("generate_community_list_commands", err)
	}

	if err := pm.applyCommands(addCommands); err != nil {
		return pm.errorHandler.NewConfigError("apply_community_list_commands", err)
	}

	return nil
}

// addCommunityListWithCleanup adds a community list after cleaning up existing sequences (for sequence changes)
func (pm *PolicyManager) addCommunityListWithCleanup(communityList *client.BgpCommunityList) error {
	pm.logger.Info(fmt.Sprintf("Adding community list with cleanup: %s seq %d", communityList.Name, communityList.Sequence))

	// Check if any community list with same name exists (any sequence)
	exists, err := pm.isCommunityListExists(communityList.Name)
	if err != nil {
		return pm.errorHandler.NewOperationError("check_community_list_exists", err)
	}

	if exists {
		pm.logger.Info(fmt.Sprintf("Found existing community list entries for %s, removing them before adding new sequence", communityList.Name))
		// Remove all existing entries with this name (will remove both standard and expanded types)
		removeCommands := pm.generateRemoveCommunityListCommands(communityList.Name)
		if err := pm.applyCommands(removeCommands); err != nil {
			return pm.errorHandler.NewConfigError("remove_existing_community_list", err)
		}
	}

	// Now add the new community list with desired sequence
	return pm.addCommunityList(communityList)
}

// RemoveCommunityList removes a community list configuration
func (pm *PolicyManager) RemoveCommunityList(name string) error {
	if name == "" {
		return pm.errorHandler.NewValidationError("community_list_name", name, "Community list name cannot be empty")
	}

	pm.logger.Info(fmt.Sprintf("Removing community list %s", name))

	// Check if community list exists
	exists, err := pm.isCommunityListExists(name)
	if err != nil {
		return pm.errorHandler.NewOperationError("check_community_list_exists", err)
	}
	if !exists {
		pm.logger.Info(fmt.Sprintf("Community list %s does not exist", name))
		return nil // Idempotent operation
	}

	// Generate and apply remove commands
	commands := pm.generateRemoveCommunityListCommands(name)
	if err := pm.applyCommands(commands); err != nil {
		return pm.errorHandler.NewConfigError("remove_community_list", err)
	}

	pm.logger.Info(fmt.Sprintf("Community list %s removed successfully", name))
	return nil
}

// ============================================================================
// Prefix List Operations
// ============================================================================

// Apply PrefixList applies a prefix list configuration with idempotent behavior (similar to neighbor approach)
func (pm *PolicyManager) ApplyPrefixList(prefixList *client.BgpPrefixList) error {
	if prefixList == nil {
		return pm.errorHandler.NewValidationError("prefix_list", nil, "Prefix list cannot be nil")
	}

	pm.logger.Info(fmt.Sprintf("Processing prefix list %s", prefixList.Name))

	// Validate prefix list
	if err := pm.ValidatePrefixList(prefixList); err != nil {
		return err
	}

	// Check if prefix list already exists with same configuration (idempotent check)
	_, err := pm.GetPrefixListDetails(prefixList.Name, prefixList.Sequence)
	if err == nil {
		// Prefix list exists, check if it needs updating
		pm.logger.Info(fmt.Sprintf("Current prefix list updated: %s", prefixList.Name))
		err = pm.updatePrefixList(prefixList)
	} else {
		// Prefix list doesn't exist, add it
		pm.logger.Info(fmt.Sprintf("New prefix list added: %s", prefixList.Name))
		err = pm.addPrefixList(prefixList)
	}

	if err != nil {
		pm.logger.Error(fmt.Sprintf("Prefix list operation failed: %v", err))
		return pm.errorHandler.NewOperationError("apply_prefix_list", err)
	}

	pm.logger.Info(fmt.Sprintf("Prefix list %s applied successfully", prefixList.Name))
	return nil
}

// GetPrefixListDetails retrieves details of a specific prefix list (similar to ParseNeighborDetails)
func (pm *PolicyManager) GetPrefixListDetails(name string, sequence uint32) (*client.BgpPrefixList, error) {
	if name == "" {
		return nil, pm.errorHandler.NewValidationError("prefix_list_name", name, "Prefix list name cannot be empty")
	}

	pm.logger.Debug(fmt.Sprintf("Getting prefix list details: %s seq %d", name, sequence))

	// Get running configuration
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return nil, pm.errorHandler.NewOperationError("get_running_config", err)
	}

	// Parse prefix list from running config
	prefixList, err := pm.parsePrefixListFromConfig(output, name, sequence)
	if err != nil {
		return nil, pm.errorHandler.NewOperationError("parse_prefix_list", err)
	}

	if prefixList == nil {
		return nil, pm.errorHandler.NewOperationError("prefix_list_not_found",
			fmt.Errorf("prefix list %s seq %d not found", name, sequence))
	}

	pm.logger.Debug(fmt.Sprintf("Retrieved prefix list details: %s", name))
	return prefixList, nil
}

// addPrefixList adds a new prefix list
func (pm *PolicyManager) addPrefixList(prefixList *client.BgpPrefixList) error {
	pm.logger.Info(fmt.Sprintf("Adding new prefix list: %s", prefixList.Name))

	// Check for sequence conflicts before adding
	existingEntry, err := pm.checkPrefixListSequenceConflict(prefixList.Name, prefixList.Sequence)
	if err != nil {
		return pm.errorHandler.NewValidationError("prefix_list_sequence_conflict", prefixList.Sequence, err.Error())
	}

	// If exact same entry exists, it's idempotent - no need to add again
	if existingEntry != "" {
		pm.logger.Info(fmt.Sprintf("Prefix list entry already exists (idempotent): %s", existingEntry))
		isConfigured, configErr := pm.isPrefixListConfigured(prefixList)
		if configErr == nil && isConfigured {
			pm.logger.Info(fmt.Sprintf("Prefix list %s seq %d already configured correctly", prefixList.Name, prefixList.Sequence))
			return nil // Idempotent operation
		} else {
			// Same sequence but different configuration - need to update
			pm.logger.Info(fmt.Sprintf("Prefix list %s seq %d needs update", prefixList.Name, prefixList.Sequence))
			// Remove existing entry first
			removeCmd := fmt.Sprintf("no ip prefix-list %s seq %d", prefixList.Name, prefixList.Sequence)
			removeCommands := []string{"configure terminal", removeCmd, "exit"}
			if removeErr := pm.applyCommands(removeCommands); removeErr != nil {
				pm.logger.Warn(fmt.Sprintf("Failed to remove existing prefix list entry: %v", removeErr))
			}
		}
	}

	// Generate and apply prefix list commands
	commands, err := pm.generatePrefixListCommands(prefixList)
	if err != nil {
		return pm.errorHandler.NewConfigError("generate_prefix_list_commands", err)
	}

	if err := pm.applyCommands(commands); err != nil {
		return pm.errorHandler.NewConfigError("apply_prefix_list_commands", err)
	}

	return nil
}

// updatePrefixList updates an existing prefix list if configuration differs
func (pm *PolicyManager) updatePrefixList(prefixList *client.BgpPrefixList) error {
	pm.logger.Info(fmt.Sprintf("Updating existing prefix list: %s", prefixList.Name))

	// Check if prefix list already exists with same configuration
	exists, err := pm.isPrefixListConfigured(prefixList)
	if err != nil {
		return pm.errorHandler.NewOperationError("check_prefix_list", err)
	}
	if exists {
		pm.logger.Info(fmt.Sprintf("Prefix list %s already configured correctly", prefixList.Name))
		return nil // Idempotent operation
	}

	// Remove existing prefix list entry and add the new one
	removeCommands := pm.generateRemovePrefixListCommands(prefixList.Name)
	if err := pm.applyCommands(removeCommands); err != nil {
		return pm.errorHandler.NewConfigError("remove_prefix_list_commands", err)
	}

	// Add updated prefix list
	addCommands, err := pm.generatePrefixListCommands(prefixList)
	if err != nil {
		return pm.errorHandler.NewConfigError("generate_prefix_list_commands", err)
	}

	if err := pm.applyCommands(addCommands); err != nil {
		return pm.errorHandler.NewConfigError("apply_prefix_list_commands", err)
	}

	return nil
}

// parsePrefixListFromConfig parses a prefix list from running configuration
func (pm *PolicyManager) parsePrefixListFromConfig(config, name string, sequence uint32) (*client.BgpPrefixList, error) {
	lines := strings.Split(config, "\n")

	// Look for prefix list configuration line
	prefixPattern := fmt.Sprintf("ip prefix-list %s seq %d", name, sequence)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefixPattern) {
			return pm.parsePrefixListLine(line)
		}
	}

	return nil, fmt.Errorf("prefix list %s seq %d not found in configuration", name, sequence)
}

// parsePrefixListLine parses a single prefix list configuration line
func (pm *PolicyManager) parsePrefixListLine(line string) (*client.BgpPrefixList, error) {
	// Example: "ip prefix-list TEST seq 10 permit 192.168.1.0/24 le 32"
	parts := strings.Fields(line)
	if len(parts) < 6 {
		return nil, fmt.Errorf("invalid prefix list line format: %s", line)
	}

	prefixList := &client.BgpPrefixList{
		Name: parts[2], // prefix list name
	}

	// Parse sequence number
	if seq, err := strconv.ParseUint(parts[4], 10, 32); err == nil {
		prefixList.Sequence = uint32(seq)
	}

	// Parse action (permit/deny)
	switch parts[5] {
	case "permit":
		prefixList.Action = client.BgpRouteMapAction_ROUTE_MAP_PERMIT
	case "deny":
		prefixList.Action = client.BgpRouteMapAction_ROUTE_MAP_DENY
	}

	// Parse prefix
	if len(parts) > 6 {
		prefixList.Prefix = parts[6]
	}

	// Parse length restrictions (le/ge)
	for i := 7; i < len(parts); i++ {
		if parts[i] == "le" && i+1 < len(parts) {
			if le, err := strconv.ParseUint(parts[i+1], 10, 32); err == nil {
				prefixList.Le = uint32(le)
			}
			i++ // Skip the value
		} else if parts[i] == "ge" && i+1 < len(parts) {
			if ge, err := strconv.ParseUint(parts[i+1], 10, 32); err == nil {
				prefixList.Ge = uint32(ge)
			}
			i++ // Skip the value
		}
	}

	return prefixList, nil
}

// RemovePrefixList removes a prefix list configuration
func (pm *PolicyManager) RemovePrefixList(name string) error {
	if name == "" {
		return pm.errorHandler.NewValidationError("prefix_list_name", name, "Prefix list name cannot be empty")
	}

	pm.logger.Info(fmt.Sprintf("Removing prefix list %s", name))

	// Check if prefix list exists
	exists, err := pm.isPrefixListExists(name)
	if err != nil {
		return pm.errorHandler.NewOperationError("check_prefix_list_exists", err)
	}
	if !exists {
		pm.logger.Info(fmt.Sprintf("Prefix list %s does not exist", name))
		return nil // Idempotent operation
	}

	// Generate and apply remove commands
	commands := pm.generateRemovePrefixListCommands(name)
	if err := pm.applyCommands(commands); err != nil {
		return pm.errorHandler.NewConfigError("remove_prefix_list", err)
	}

	pm.logger.Info(fmt.Sprintf("Prefix list %s removed successfully", name))
	return nil
}

// ============================================================================
// Validation Methods
// ============================================================================

// ValidateRouteMap validates a route map configuration
func (pm *PolicyManager) ValidateRouteMap(routeMap *client.BgpRouteMap) error {
	if routeMap == nil {
		return pm.errorHandler.NewValidationError("route_map", nil, "Route map cannot be nil")
	}

	if routeMap.Name == "" {
		return pm.errorHandler.NewValidationError("route_map_name", routeMap.Name, "Route map name cannot be empty")
	}

	if !pm.isValidRouteMapName(routeMap.Name) {
		return pm.errorHandler.NewValidationError("route_map_name", routeMap.Name, "Invalid route map name format")
	}

	// Validate sequence number
	if routeMap.Sequence > 65535 {
		return pm.errorHandler.NewValidationError("route_map_sequence", routeMap.Sequence, "Sequence number must be between 1 and 65535")
	}

	// Validate action
	if routeMap.Action != client.BgpRouteMapAction_ROUTE_MAP_PERMIT &&
		routeMap.Action != client.BgpRouteMapAction_ROUTE_MAP_DENY {
		return pm.errorHandler.NewValidationError("route_map_action", routeMap.Action, "Action must be permit or deny")
	}

	return nil
}

// ValidateCommunityList validates a community list configuration
func (pm *PolicyManager) ValidateCommunityList(communityList *client.BgpCommunityList) error {
	if communityList == nil {
		return pm.errorHandler.NewValidationError("community_list", nil, "Community list cannot be nil")
	}

	if communityList.Name == "" {
		return pm.errorHandler.NewValidationError("community_list_name", communityList.Name, "Community list name cannot be empty")
	}

	if !pm.isValidCommunityListName(communityList.Name) {
		return pm.errorHandler.NewValidationError("community_list_name", communityList.Name, "Invalid community list name format")
	}

	// Validate sequence number
	if communityList.Sequence > 65535 {
		return pm.errorHandler.NewValidationError("community_list_sequence", communityList.Sequence, "Sequence number must be between 1 and 65535")
	}

	// Validate action
	if communityList.Action != client.BgpRouteMapAction_ROUTE_MAP_PERMIT &&
		communityList.Action != client.BgpRouteMapAction_ROUTE_MAP_DENY {
		return pm.errorHandler.NewValidationError("community_list_action", communityList.Action, "Action must be permit or deny")
	}

	// Validate community values
	if communityList.CommunityValues == "" {
		return pm.errorHandler.NewValidationError("community_values", communityList.CommunityValues, "Community values must be specified")
	}

	// Validate each community value format
	communities := strings.Fields(communityList.CommunityValues)
	for _, community := range communities {
		// Skip well-known community strings
		if community == "internet" || community == "no-export" || community == "no-advertise" || community == "local-AS" {
			continue
		}

		// Validate AS:VAL format
		if !pm.isValidCommunity(community) {
			return pm.errorHandler.NewValidationError("community_value", community,
				fmt.Sprintf("Invalid community format: %s (expected AS:VAL format)", community))
		}
	}

	return nil
}

// ValidatePrefixList validates a prefix list configuration
func (pm *PolicyManager) ValidatePrefixList(prefixList *client.BgpPrefixList) error {
	if prefixList == nil {
		return pm.errorHandler.NewValidationError("prefix_list", nil, "Prefix list cannot be nil")
	}

	if prefixList.Name == "" {
		return pm.errorHandler.NewValidationError("prefix_list_name", prefixList.Name, "Prefix list name cannot be empty")
	}

	if !pm.isValidPrefixListName(prefixList.Name) {
		return pm.errorHandler.NewValidationError("prefix_list_name", prefixList.Name, "Invalid prefix list name format")
	}

	// Validate sequence number
	if prefixList.Sequence > 65535 {
		return pm.errorHandler.NewValidationError("prefix_list_sequence", prefixList.Sequence, "Sequence number must be between 1 and 65535")
	}

	// Validate action
	if prefixList.Action != client.BgpRouteMapAction_ROUTE_MAP_PERMIT &&
		prefixList.Action != client.BgpRouteMapAction_ROUTE_MAP_DENY {
		return pm.errorHandler.NewValidationError("prefix_list_action", prefixList.Action, "Action must be permit or deny")
	}

	// Validate prefix
	if prefixList.Prefix == "" {
		return pm.errorHandler.NewValidationError("prefix", prefixList.Prefix, "Prefix cannot be empty")
	}

	if !pm.isValidPrefix(prefixList.Prefix) {
		return pm.errorHandler.NewValidationError("prefix", prefixList.Prefix, fmt.Sprintf("Invalid prefix format: %s", prefixList.Prefix))
	}

	return nil
}

// ============================================================================
// Private Helper Methods
// ============================================================================

// generateRouteMapCommands generates FRR commands for route map configuration
func (pm *PolicyManager) generateRouteMapCommands(routeMap *client.BgpRouteMap) ([]string, error) {
	var commands []string

	// Enter configuration mode
	commands = append(commands, "configure terminal")

	// Get action string
	actionStr := "permit"
	if routeMap.Action == client.BgpRouteMapAction_ROUTE_MAP_DENY {
		actionStr = "deny"
	}

	// Route map command
	routeMapCmd := fmt.Sprintf("route-map %s %s %d", routeMap.Name, actionStr, routeMap.Sequence)
	commands = append(commands, routeMapCmd)

	// Add description if provided
	if routeMap.Description != "" {
		commands = append(commands, fmt.Sprintf("description %s", routeMap.Description))
	}

	// Process match conditions
	for _, match := range routeMap.MatchConditions {
		switch match.MatchType {
		case "prefix-list":
			commands = append(commands, fmt.Sprintf("match ip address prefix-list %s", match.MatchValue))
		case "community":
			commands = append(commands, fmt.Sprintf("match community %s", match.MatchValue))
		case "as-path":
			commands = append(commands, fmt.Sprintf("match as-path %s", match.MatchValue))
		}
	}

	// Process set actions - SetActions is now a single object, not array
	if routeMap.SetActions != nil {
		set := routeMap.SetActions

		// Handle local preference
		if set.SetLocalPreference > 0 {
			commands = append(commands, fmt.Sprintf("set local-preference %d", set.SetLocalPreference))
		}

		// Handle metric
		if set.SetMetric > 0 {
			commands = append(commands, fmt.Sprintf("set metric %d", set.SetMetric))
		}

		// Handle community
		if set.SetCommunity != "" {
			// Handle special community types
			if strings.Contains(set.SetCommunity, "(as-path-prepend)") {
				// Extract AS-path prepend values
				asPathValue := strings.Replace(set.SetCommunity, " (as-path-prepend)", "", 1)
				commands = append(commands, fmt.Sprintf("set as-path prepend %s", asPathValue))
			} else if strings.Contains(set.SetCommunity, "(origin)") {
				// Extract origin value
				originValue := strings.Replace(set.SetCommunity, " (origin)", "", 1)
				commands = append(commands, fmt.Sprintf("set origin %s", originValue))
			} else if strings.Contains(set.SetCommunity, "(weight)") {
				// Extract weight value
				weightValue := strings.Replace(set.SetCommunity, " (weight)", "", 1)
				commands = append(commands, fmt.Sprintf("set weight %s", weightValue))
			} else if strings.Contains(set.SetCommunity, "(unknown)") {
				// Skip unknown commands or log warning
				pm.logger.Warn(fmt.Sprintf("Skipping unknown set command: %s", set.SetCommunity))
			} else {
				// Validate community format before setting
				communityStr := set.SetCommunity
				if strings.HasSuffix(communityStr, " additive") {
					// Handle additive community
					baseCommStr := strings.Replace(communityStr, " additive", "", 1)
					communities := strings.Fields(baseCommStr)
					for _, community := range communities {
						if community != "internet" && community != "no-export" &&
							community != "no-advertise" && community != "local-AS" &&
							!pm.isValidCommunity(community) {
							return nil, fmt.Errorf("invalid community format in route map: %s", community)
						}
					}
					commands = append(commands, fmt.Sprintf("set community %s additive", baseCommStr))
				} else {
					// Regular community
					communities := strings.Fields(communityStr)
					for _, community := range communities {
						if community != "internet" && community != "no-export" &&
							community != "no-advertise" && community != "local-AS" &&
							!pm.isValidCommunity(community) {
							return nil, fmt.Errorf("invalid community format in route map: %s", community)
						}
					}
					commands = append(commands, fmt.Sprintf("set community %s", communityStr))
				}
			}
		}

		// Handle next-hop
		if set.SetNexthop != "" {
			commands = append(commands, fmt.Sprintf("set ip next-hop %s", set.SetNexthop))
		}
	}

	// Exit route map mode
	commands = append(commands, "exit")
	commands = append(commands, "exit")

	return commands, nil
}

// generateCommunityListCommands generates FRR commands for community list configuration
func (pm *PolicyManager) generateCommunityListCommands(communityList *client.BgpCommunityList) ([]string, error) {
	var commands []string

	// Enter configuration mode
	commands = append(commands, "configure terminal")

	// Get action string
	actionStr := "permit"
	if communityList.Action == client.BgpRouteMapAction_ROUTE_MAP_DENY {
		actionStr = "deny"
	}

	// Convert comma-separated community values to space-separated
	communityValues := communityList.CommunityValues
	if strings.Contains(communityValues, ",") {
		// Replace commas with spaces
		communityValues = strings.ReplaceAll(communityValues, ",", " ")
		// Clean up multiple spaces
		communityValues = strings.Join(strings.Fields(communityValues), " ")
	}

	// CORRECT FRR 10.3.1 syntax: "bgp community-list TYPE NAME seq SEQUENCE permit VALUES"
	communityListCmd := fmt.Sprintf("bgp community-list %s %s seq %d %s %s",
		communityList.Type, communityList.Name, communityList.Sequence, actionStr, communityValues)
	commands = append(commands, communityListCmd)

	// Exit configuration mode
	commands = append(commands, "exit")

	return commands, nil
}

// generatePrefixListCommands generates FRR commands for prefix list configuration
func (pm *PolicyManager) generatePrefixListCommands(prefixList *client.BgpPrefixList) ([]string, error) {
	var commands []string

	// Enter configuration mode
	commands = append(commands, "configure terminal")

	// Get action string
	actionStr := "permit"
	if prefixList.Action == client.BgpRouteMapAction_ROUTE_MAP_DENY {
		actionStr = "deny"
	}

	// Prefix list command
	prefixListCmd := fmt.Sprintf("ip prefix-list %s seq %d %s %s",
		prefixList.Name, prefixList.Sequence, actionStr, prefixList.Prefix)

	// Add length restrictions if specified - CORRECT FRR syntax: ge X le Y
	if prefixList.Ge > 0 && prefixList.Le > 0 {
		// Validate ge <= le
		if prefixList.Ge > prefixList.Le {
			return nil, fmt.Errorf("invalid prefix list: ge (%d) cannot be greater than le (%d)", prefixList.Ge, prefixList.Le)
		}
		prefixListCmd += fmt.Sprintf(" ge %d le %d", prefixList.Ge, prefixList.Le)
	} else if prefixList.Ge > 0 {
		// Validate ge is meaningful
		prefixLen := pm.getPrefixLength(prefixList.Prefix)
		if prefixLen >= 0 && prefixList.Ge <= uint32(prefixLen) {
			return nil, fmt.Errorf("invalid prefix list: ge (%d) must be greater than prefix length (%d)", prefixList.Ge, prefixLen)
		}
		prefixListCmd += fmt.Sprintf(" ge %d", prefixList.Ge)
	} else if prefixList.Le > 0 {
		prefixListCmd += fmt.Sprintf(" le %d", prefixList.Le)
	}

	commands = append(commands, prefixListCmd)

	// Exit configuration mode
	commands = append(commands, "exit")

	return commands, nil
}

// generateRemoveRouteMapCommands generates commands to remove a route map
func (pm *PolicyManager) generateRemoveRouteMapCommands(name string) []string {
	return []string{
		"configure terminal",
		fmt.Sprintf("no route-map %s", name),
		"exit",
	}
}

// generateRemoveCommunityListCommands generates commands to remove a community list
func (pm *PolicyManager) generateRemoveCommunityListCommands(name string) []string {
	// Remove both standard and expanded types to be safe
	return []string{
		"configure terminal",
		fmt.Sprintf("no bgp community-list standard %s", name),
		fmt.Sprintf("no bgp community-list expanded %s", name),
		"exit",
	}
}

// generateRemovePrefixListCommands generates commands to remove a prefix list
func (pm *PolicyManager) generateRemovePrefixListCommands(name string) []string {
	return []string{
		"configure terminal",
		fmt.Sprintf("no ip prefix-list %s", name),
		"exit",
	}
}

// isCommunityListConfigured checks if a community list is configured with same parameters
func (pm *PolicyManager) isCommunityListConfigured(communityList *client.BgpCommunityList) (bool, error) {
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return false, fmt.Errorf("failed to get running configuration: %w", err)
	}

	// Simple check - look for community list in configuration
	lines := strings.Split(output, "\n")
	actionStr := "permit"
	if communityList.Action == client.BgpRouteMapAction_ROUTE_MAP_DENY {
		actionStr = "deny"
	}

	// Use type from protobuf field
	communityType := "standard" // Default fallback
	if communityList.Type != "" {
		communityType = communityList.Type
	}

	// Convert community values for comparison (comma to space)
	communityValues := communityList.CommunityValues
	if strings.Contains(communityValues, ",") {
		communityValues = strings.ReplaceAll(communityValues, ",", " ")
		communityValues = strings.Join(strings.Fields(communityValues), " ")
	}

	communityListLine := fmt.Sprintf("bgp community-list %s %s seq %d %s %s",
		communityType, communityList.Name, communityList.Sequence, actionStr, communityValues)

	for _, line := range lines {
		if strings.TrimSpace(line) == communityListLine {
			return true, nil
		}
	}

	return false, nil
}

// isPrefixListConfigured checks if a prefix list is configured with same parameters
func (pm *PolicyManager) isPrefixListConfigured(prefixList *client.BgpPrefixList) (bool, error) {
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return false, fmt.Errorf("failed to get running configuration: %w", err)
	}

	// Simple check - look for prefix list in configuration
	lines := strings.Split(output, "\n")
	prefixListPrefix := fmt.Sprintf("ip prefix-list %s seq %d %s %s",
		prefixList.Name, prefixList.Sequence, prefixList.Action, prefixList.Prefix)

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefixListPrefix) {
			return true, nil
		}
	}

	return false, nil
}

// isRouteMapExists checks if a route map exists
func (pm *PolicyManager) isRouteMapExists(name string) (bool, error) {
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return false, fmt.Errorf("failed to get running configuration: %w", err)
	}

	return strings.Contains(output, fmt.Sprintf("route-map %s", name)), nil
}

// isCommunityListExists checks if a community list exists (any type)
func (pm *PolicyManager) isCommunityListExists(name string) (bool, error) {
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return false, fmt.Errorf("failed to get running configuration: %w", err)
	}

	// Check for both standard and expanded types
	standardExists := strings.Contains(output, fmt.Sprintf("bgp community-list standard %s", name))
	expandedExists := strings.Contains(output, fmt.Sprintf("bgp community-list expanded %s", name))

	return standardExists || expandedExists, nil
}

// isPrefixListExists checks if a prefix list exists
func (pm *PolicyManager) isPrefixListExists(name string) (bool, error) {
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return false, fmt.Errorf("failed to get running configuration: %w", err)
	}

	return strings.Contains(output, fmt.Sprintf("ip prefix-list %s", name)), nil
}

// Validation helper methods
func (pm *PolicyManager) isValidRouteMapName(name string) bool {
	// Basic validation - alphanumeric with dashes and underscores
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, char := range name {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func (pm *PolicyManager) isValidCommunityListName(name string) bool {
	// Same validation as route map names
	return pm.isValidRouteMapName(name)
}

func (pm *PolicyManager) isValidPrefixListName(name string) bool {
	// Same validation as route map names
	return pm.isValidRouteMapName(name)
}

func (pm *PolicyManager) isValidCommunity(community string) bool {
	// Basic community validation (AS:VAL format)
	parts := strings.Split(community, ":")
	if len(parts) != 2 {
		return false
	}

	// Check if both parts are numbers
	if _, err := strconv.ParseUint(parts[0], 10, 16); err != nil {
		return false
	}
	if _, err := strconv.ParseUint(parts[1], 10, 16); err != nil {
		return false
	}

	return true
}

func (pm *PolicyManager) isValidPrefix(prefix string) bool {
	// Basic CIDR validation
	parts := strings.Split(prefix, "/")
	if len(parts) != 2 {
		return false
	}

	// Validate subnet mask
	if mask, err := strconv.Atoi(parts[1]); err != nil || mask < 0 || mask > 32 {
		return false
	}

	// Basic IP validation (simplified)
	ipParts := strings.Split(parts[0], ".")
	if len(ipParts) != 4 {
		return false
	}

	for _, part := range ipParts {
		if octet, err := strconv.Atoi(part); err != nil || octet < 0 || octet > 255 {
			return false
		}
	}

	return true
}

// getPrefixLength extracts the prefix length from a CIDR notation (e.g., "10.10.20.0/24" returns 24)
func (pm *PolicyManager) getPrefixLength(prefix string) int {
	parts := strings.Split(prefix, "/")
	if len(parts) != 2 {
		return -1 // Invalid format
	}

	if length, err := strconv.Atoi(parts[1]); err == nil {
		return length
	}

	return -1 // Invalid length
}

// checkPrefixListSequenceConflict checks if a sequence number is already used in the prefix list
func (pm *PolicyManager) checkPrefixListSequenceConflict(name string, sequence uint32) (string, error) {
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return "", fmt.Errorf("failed to get running configuration: %w", err)
	}

	// Look for existing prefix list entries with same name and sequence
	lines := strings.Split(output, "\n")
	conflictPattern := fmt.Sprintf("ip prefix-list %s seq %d", name, sequence)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, conflictPattern) {
			pm.logger.Info(fmt.Sprintf("Found existing prefix list entry: %s", line))
			// Return the existing entry line
			return line, nil
		}
	}

	return "", nil // No conflict found
}

// applyCommands applies a list of commands via vtysh
func (pm *PolicyManager) applyCommands(commands []string) error {
	if len(commands) == 0 {
		return nil
	}

	// Use ExecuteSimpleSession to maintain vtysh session context across commands
	err := pm.vtysh.ExecuteSimpleSession(commands)
	if err != nil {
		// Enhanced error logging for prefix list debugging
		pm.logger.Error(fmt.Sprintf("Command execution failed for commands: %v", commands))
		pm.logger.Error(fmt.Sprintf("Error: %v", err))

		// Check if it's a prefix list command that failed
		for _, cmd := range commands {
			if strings.Contains(cmd, "ip prefix-list") {
				// Try to get current configuration to see if prefix list already exists
				configOutput, configErr := pm.vtysh.ExecuteCommand("show running-config | grep prefix-list")
				if configErr == nil {
					pm.logger.Error(fmt.Sprintf("Current prefix lists: %s", configOutput))
				}
				break
			}
		}

		return fmt.Errorf("commands %v failed: %w", commands, err)
	}

	return nil
}

// GetPolicyConfig retrieves all BGP policy configurations (route maps, community lists, prefix lists)
func (pm *PolicyManager) GetPolicyConfig() (*client.BgpPolicyConfig, error) {
	pm.logger.Info("Getting BGP policy configuration")

	// Get running configuration
	output, err := pm.vtysh.ExecuteCommand("show running-config")
	if err != nil {
		return nil, pm.errorHandler.NewOperationError("get_running_config", err)
	}

	policyConfig := &client.BgpPolicyConfig{}

	// Parse all policy objects from running config
	policyConfig.RouteMaps, err = pm.parseRouteMapsFromConfig(output)
	if err != nil {
		pm.logger.Warn(fmt.Sprintf("Failed to parse route maps: %v", err))
		policyConfig.RouteMaps = []*client.BgpRouteMap{} // Initialize empty slice
	}

	policyConfig.CommunityLists, err = pm.parseCommunityListsFromConfig(output)
	if err != nil {
		pm.logger.Warn(fmt.Sprintf("Failed to parse community lists: %v", err))
		policyConfig.CommunityLists = []*client.BgpCommunityList{} // Initialize empty slice
	}

	policyConfig.PrefixLists, err = pm.parsePrefixListsFromConfig(output)
	if err != nil {
		pm.logger.Warn(fmt.Sprintf("Failed to parse prefix lists: %v", err))
		policyConfig.PrefixLists = []*client.BgpPrefixList{} // Initialize empty slice
	}

	pm.logger.Info(fmt.Sprintf("Retrieved policy config: %d route maps, %d community lists, %d prefix lists",
		len(policyConfig.RouteMaps), len(policyConfig.CommunityLists), len(policyConfig.PrefixLists)))

	return policyConfig, nil
}

// parseRouteMapsFromConfig parses all route maps from running configuration
func (pm *PolicyManager) parseRouteMapsFromConfig(config string) ([]*client.BgpRouteMap, error) {
	var routeMaps []*client.BgpRouteMap
	lines := strings.Split(config, "\n")

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		// Look for route-map lines: "route-map NAME permit|deny SEQUENCE"
		if strings.HasPrefix(line, "route-map ") {
			routeMap, nextIndex, err := pm.parseRouteMapBlock(lines, i)
			if err != nil {
				pm.logger.Warn(fmt.Sprintf("Failed to parse route map block starting at line '%s': %v", line, err))
				continue
			}
			if routeMap != nil {
				routeMaps = append(routeMaps, routeMap)
			}
			i = nextIndex // Skip to the end of this route map block
		}
	}

	return routeMaps, nil
}

// parseRouteMapBlock parses a complete route map block including match/set statements
func (pm *PolicyManager) parseRouteMapBlock(lines []string, startIndex int) (*client.BgpRouteMap, int, error) {
	line := strings.TrimSpace(lines[startIndex])

	// Parse the route map header line
	routeMap, err := pm.parseRouteMapLine(line)
	if err != nil {
		return nil, startIndex, err
	}

	// Initialize match conditions (still array) and set actions (now single object)
	var matchConditions []*client.BgpRouteMapMatch
	var setActions *client.BgpRouteMapSet

	// Parse the route map content (match, set statements)
	i := startIndex + 1
	for i < len(lines) {
		line = strings.TrimSpace(lines[i])

		// Stop at next route-map, exit, or end of config
		if strings.HasPrefix(line, "route-map ") ||
			strings.HasPrefix(line, "exit") ||
			strings.HasPrefix(line, "!") ||
			line == "" {
			break
		}

		// Parse match statements (still array)
		if strings.HasPrefix(line, "match ") {
			matchCondition := pm.parseMatchStatement(line)
			if matchCondition != nil {
				matchConditions = append(matchConditions, matchCondition)
			}
		}

		// Parse set statements (merge into single object)
		if strings.HasPrefix(line, "set ") {
			setAction := pm.parseSetStatement(line)
			if setAction != nil {
				if setActions == nil {
					setActions = setAction
				} else {
					// Merge multiple set statements into single object
					pm.mergeSetActions(setActions, setAction)
				}
			}
		}

		// Parse description
		if strings.HasPrefix(line, "description ") {
			description := strings.TrimPrefix(line, "description ")
			routeMap.Description = strings.TrimSpace(description)
		}

		i++
	}

	// Set the parsed match and set conditions
	if len(matchConditions) > 0 {
		routeMap.MatchConditions = matchConditions
	}
	if setActions != nil {
		routeMap.SetActions = setActions
	}

	return routeMap, i, nil
}

// parseRouteMapLine parses a single route map configuration line
func (pm *PolicyManager) parseRouteMapLine(line string) (*client.BgpRouteMap, error) {
	// Example: "route-map TEST permit 10"
	parts := strings.Fields(line)
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid route map line format: %s", line)
	}

	routeMap := &client.BgpRouteMap{
		Name: parts[1], // route map name
	}

	// Parse action (permit/deny)
	switch parts[2] {
	case "permit":
		routeMap.Action = client.BgpRouteMapAction_ROUTE_MAP_PERMIT
	case "deny":
		routeMap.Action = client.BgpRouteMapAction_ROUTE_MAP_DENY
	default:
		return nil, fmt.Errorf("invalid route map action: %s", parts[2])
	}

	// Parse sequence number
	if seq, err := strconv.ParseUint(parts[3], 10, 32); err == nil {
		routeMap.Sequence = uint32(seq)
	} else {
		return nil, fmt.Errorf("invalid sequence number: %s", parts[3])
	}

	return routeMap, nil
}

// parseCommunityListsFromConfig parses all community lists from running configuration
func (pm *PolicyManager) parseCommunityListsFromConfig(config string) ([]*client.BgpCommunityList, error) {
	var communityLists []*client.BgpCommunityList
	lines := strings.Split(config, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for community list lines: "bgp community-list TYPE NAME seq SEQUENCE permit|deny COMMUNITY_VALUES"
		if strings.HasPrefix(line, "bgp community-list standard ") ||
			strings.HasPrefix(line, "bgp community-list expanded ") {
			communityList, err := pm.parseCommunityListLine(line)
			if err != nil {
				pm.logger.Warn(fmt.Sprintf("Failed to parse community list line '%s': %v", line, err))
				continue
			}
			if communityList != nil {
				communityLists = append(communityLists, communityList)
			}
		}
	}

	return communityLists, nil
}

// parseCommunityListLine parses a single community list configuration line
func (pm *PolicyManager) parseCommunityListLine(line string) (*client.BgpCommunityList, error) {
	// Example: "bgp community-list standard DEDEWE seq 11 permit 65333:12 65444:11"
	// Example: "bgp community-list expanded REGEX seq 11 deny .*"
	parts := strings.Fields(line)
	if len(parts) < 7 {
		return nil, fmt.Errorf("invalid community list line format: %s", line)
	}

	communityList := &client.BgpCommunityList{
		Type: parts[2], // community list type (standard/expanded)
		Name: parts[3], // community list name
	}

	// Parse sequence number
	if seq, err := strconv.ParseUint(parts[5], 10, 32); err == nil {
		communityList.Sequence = uint32(seq)
	} else {
		return nil, fmt.Errorf("invalid sequence number: %s", parts[5])
	}

	// Parse action (permit/deny)
	switch parts[6] {
	case "permit":
		communityList.Action = client.BgpRouteMapAction_ROUTE_MAP_PERMIT
	case "deny":
		communityList.Action = client.BgpRouteMapAction_ROUTE_MAP_DENY
	default:
		return nil, fmt.Errorf("invalid community list action: %s", parts[6])
	}

	// Parse community values (remaining parts)
	if len(parts) > 7 {
		communityValues := strings.Join(parts[7:], " ")
		communityList.CommunityValues = communityValues
	}

	return communityList, nil
}

// parseCommunityListFromConfig parses a specific community list from running configuration (similar to parsePrefixListFromConfig)
func (pm *PolicyManager) parseCommunityListFromConfig(config, name string, sequence uint32) (*client.BgpCommunityList, error) {
	lines := strings.Split(config, "\n")

	// Look for specific community list configuration line
	communityPattern := fmt.Sprintf("bgp community-list standard %s seq %d", name, sequence)
	expandedPattern := fmt.Sprintf("bgp community-list expanded %s seq %d", name, sequence)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, communityPattern) || strings.HasPrefix(line, expandedPattern) {
			return pm.parseCommunityListLine(line)
		}
	}

	return nil, fmt.Errorf("community list %s seq %d not found in configuration", name, sequence)
}

// parsePrefixListsFromConfig parses all prefix lists from running configuration
func (pm *PolicyManager) parsePrefixListsFromConfig(config string) ([]*client.BgpPrefixList, error) {
	var prefixLists []*client.BgpPrefixList
	lines := strings.Split(config, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for prefix list lines: "ip prefix-list NAME seq SEQUENCE permit|deny PREFIX [le X] [ge Y]"
		if strings.HasPrefix(line, "ip prefix-list ") {
			prefixList, err := pm.parsePrefixListLine(line)
			if err != nil {
				pm.logger.Warn(fmt.Sprintf("Failed to parse prefix list line '%s': %v", line, err))
				continue
			}
			if prefixList != nil {
				prefixLists = append(prefixLists, prefixList)
			}
		}
	}

	return prefixLists, nil
}

// parseMatchStatement parses a match statement into BgpRouteMapMatch struct
func (pm *PolicyManager) parseMatchStatement(line string) *client.BgpRouteMapMatch {
	// Remove "match " prefix
	matchPart := strings.TrimPrefix(line, "match ")
	parts := strings.Fields(matchPart)

	if len(parts) == 0 {
		return nil
	}

	match := &client.BgpRouteMapMatch{}

	// Parse different match types
	switch {
	case strings.HasPrefix(matchPart, "ip address prefix-list "):
		// "match ip address prefix-list PL-ACCEPT"
		match.MatchType = "prefix-list"
		if len(parts) >= 4 {
			match.MatchValue = parts[3] // PL-ACCEPT
		}
	case strings.HasPrefix(matchPart, "community "):
		// "match community COMM-LIST"
		match.MatchType = "community"
		if len(parts) >= 2 {
			match.MatchValue = parts[1]
		}
	case strings.HasPrefix(matchPart, "as-path "):
		// "match as-path AS-PATH-LIST"
		match.MatchType = "as-path"
		if len(parts) >= 2 {
			match.MatchValue = parts[1]
		}
	case strings.HasPrefix(matchPart, "local-preference "):
		// "match local-preference 100"
		match.MatchType = "local-preference"
		if len(parts) >= 2 {
			match.MatchValue = parts[1]
		}
	case strings.HasPrefix(matchPart, "metric "):
		// "match metric 50"
		match.MatchType = "metric"
		if len(parts) >= 2 {
			match.MatchValue = parts[1]
		}
	default:
		// Generic match - use first part as type, rest as value
		match.MatchType = parts[0]
		if len(parts) > 1 {
			match.MatchValue = strings.Join(parts[1:], " ")
		}
	}

	return match
}

// parseSetStatement parses a set statement into BgpRouteMapSet struct
func (pm *PolicyManager) parseSetStatement(line string) *client.BgpRouteMapSet {
	// Remove "set " prefix
	setPart := strings.TrimPrefix(line, "set ")
	parts := strings.Fields(setPart)

	if len(parts) == 0 {
		return nil
	}

	set := &client.BgpRouteMapSet{}

	// Parse different set types using specific fields
	switch {
	case strings.HasPrefix(setPart, "local-preference "):
		// "set local-preference 100"
		if len(parts) >= 2 {
			// Convert string to uint32
			if localPref, err := strconv.ParseUint(parts[1], 10, 32); err == nil {
				set.SetLocalPreference = uint32(localPref)
			}
		}
	case strings.HasPrefix(setPart, "metric "):
		// "set metric 50"
		if len(parts) >= 2 {
			// Convert string to uint32
			if metric, err := strconv.ParseUint(parts[1], 10, 32); err == nil {
				set.SetMetric = uint32(metric)
			}
		}
	case strings.HasPrefix(setPart, "community "):
		// "set community 65001:100" or "set community 65001:100 additive"
		if len(parts) >= 2 {
			// Handle additive community (combine with existing value if needed)
			if len(parts) > 2 && parts[len(parts)-1] == "additive" {
				// For additive, we can append "additive" to the community value
				communityValues := strings.Join(parts[1:len(parts)-1], " ")
				set.SetCommunity = communityValues + " additive"
			} else {
				set.SetCommunity = strings.Join(parts[1:], " ")
			}
		}
	case strings.HasPrefix(setPart, "ip next-hop ") || strings.HasPrefix(setPart, "nexthop "):
		// "set ip next-hop 192.168.1.1" or "set nexthop 192.168.1.1"
		if strings.HasPrefix(setPart, "ip next-hop ") && len(parts) >= 3 {
			set.SetNexthop = parts[2]
		} else if strings.HasPrefix(setPart, "nexthop ") && len(parts) >= 2 {
			set.SetNexthop = parts[1]
		}
	case strings.HasPrefix(setPart, "as-path prepend "):
		// "set as-path prepend 65001 65001" - store in community field
		if len(parts) >= 3 {
			set.SetCommunity = strings.Join(parts[2:], " ") + " (as-path-prepend)"
		}
	case strings.HasPrefix(setPart, "weight "):
		// "set weight 100" - store in community field since SetLocalPreference is uint32
		if len(parts) >= 2 {
			set.SetCommunity = parts[1] + " (weight)"
		}
	case strings.HasPrefix(setPart, "origin "):
		// "set origin igp" - store in community field
		if len(parts) >= 2 {
			set.SetCommunity = parts[1] + " (origin)"
		}
	default:
		// For unknown set types, store in community field
		pm.logger.Warn(fmt.Sprintf("Unknown set statement: %s", setPart))
		set.SetCommunity = setPart + " (unknown)"
	}

	return set
}

// mergeSetActions merges two set actions into one
func (pm *PolicyManager) mergeSetActions(target, source *client.BgpRouteMapSet) {
	if source.SetLocalPreference > 0 {
		target.SetLocalPreference = source.SetLocalPreference
	}
	if source.SetMetric > 0 {
		target.SetMetric = source.SetMetric
	}
	if source.SetCommunity != "" {
		target.SetCommunity = source.SetCommunity
	}
	if source.SetNexthop != "" {
		target.SetNexthop = source.SetNexthop
	}
}

// ============================================================================
// Smart Array Handling Functions (similar to neighbor pattern)
// ============================================================================

// matchConditionsEqual compares two match condition arrays
func (pm *PolicyManager) matchConditionsEqual(current, desired []*client.BgpRouteMapMatch) bool {
	if len(current) != len(desired) {
		pm.logger.Debug(fmt.Sprintf("Match conditions length differ: current=%d, desired=%d", len(current), len(desired)))
		return false
	}

	// Create maps for easier comparison
	currentMap := make(map[string]*client.BgpRouteMapMatch)
	for _, match := range current {
		key := fmt.Sprintf("%s:%s", match.MatchType, match.MatchValue)
		currentMap[key] = match
	}

	for _, match := range desired {
		key := fmt.Sprintf("%s:%s", match.MatchType, match.MatchValue)
		if _, exists := currentMap[key]; !exists {
			pm.logger.Debug(fmt.Sprintf("Desired match condition not found in current: %s", key))
			return false
		}
	}

	return true
}

// setActionsEqual compares two set action objects
func (pm *PolicyManager) setActionsEqual(current, desired *client.BgpRouteMapSet) bool {
	// Handle nil cases
	if current == nil && desired == nil {
		return true
	}
	if current == nil || desired == nil {
		pm.logger.Debug("One set action is nil, other is not")
		return false
	}

	// Compare all fields
	if current.SetLocalPreference != desired.SetLocalPreference ||
		current.SetMetric != desired.SetMetric ||
		current.SetCommunity != desired.SetCommunity ||
		current.SetNexthop != desired.SetNexthop {
		pm.logger.Debug("Set action fields differ")
		return false
	}

	return true
}

// updateMatchConditions applies smart updates to match conditions (similar to neighbor array handling)
func (pm *PolicyManager) updateMatchConditions(commands *[]string, current, desired []*client.BgpRouteMapMatch) error {
	pm.logger.Debug(fmt.Sprintf("Updating match conditions: current=%d, desired=%d", len(current), len(desired)))

	// Create maps for easier lookup
	currentMap := make(map[string]*client.BgpRouteMapMatch)
	desiredMap := make(map[string]*client.BgpRouteMapMatch)

	for _, match := range current {
		key := fmt.Sprintf("%s:%s", match.MatchType, match.MatchValue)
		currentMap[key] = match
	}

	for _, match := range desired {
		key := fmt.Sprintf("%s:%s", match.MatchType, match.MatchValue)
		desiredMap[key] = match
	}

	// Remove match conditions that exist in current but not in desired
	for key, match := range currentMap {
		if _, exists := desiredMap[key]; !exists {
			pm.logger.Debug(fmt.Sprintf("Removing match condition: %s", key))
			*commands = append(*commands, pm.generateRemoveMatchCommand(match))
		}
	}

	// Add match conditions that exist in desired but not in current
	for key, match := range desiredMap {
		if _, exists := currentMap[key]; !exists {
			pm.logger.Debug(fmt.Sprintf("Adding match condition: %s", key))
			*commands = append(*commands, pm.generateAddMatchCommand(match))
		}
	}

	return nil
}

// updateSetActions updates set actions
func (pm *PolicyManager) updateSetActions(commands *[]string, current, desired *client.BgpRouteMapSet) error {
	pm.logger.Debug("Updating set actions")

	// If desired is nil, remove all current set actions
	if desired == nil {
		if current != nil {
			*commands = append(*commands, pm.generateRemoveSetCommands(current)...)
		}
		return nil
	}

	// If current is nil, add all desired set actions
	if current == nil {
		*commands = append(*commands, pm.generateAddSetCommands(desired)...)
		return nil
	}

	// Compare and update individual fields
	if current.SetLocalPreference != desired.SetLocalPreference {
		if desired.SetLocalPreference > 0 {
			*commands = append(*commands, fmt.Sprintf("set local-preference %d", desired.SetLocalPreference))
		} else {
			*commands = append(*commands, "no set local-preference")
		}
	}

	if current.SetMetric != desired.SetMetric {
		if desired.SetMetric > 0 {
			*commands = append(*commands, fmt.Sprintf("set metric %d", desired.SetMetric))
		} else {
			*commands = append(*commands, "no set metric")
		}
	}

	if current.SetCommunity != desired.SetCommunity {
		if desired.SetCommunity != "" {
			*commands = append(*commands, fmt.Sprintf("set community %s", desired.SetCommunity))
		} else {
			*commands = append(*commands, "no set community")
		}
	}

	if current.SetNexthop != desired.SetNexthop {
		if desired.SetNexthop != "" {
			*commands = append(*commands, fmt.Sprintf("set ip next-hop %s", desired.SetNexthop))
		} else {
			*commands = append(*commands, "no set ip next-hop")
		}
	}

	return nil
}

// generateRemoveMatchCommand generates command to remove a match condition
func (pm *PolicyManager) generateRemoveMatchCommand(match *client.BgpRouteMapMatch) string {
	switch match.MatchType {
	case "prefix-list":
		return fmt.Sprintf("no match ip address prefix-list %s", match.MatchValue)
	case "community":
		return fmt.Sprintf("no match community %s", match.MatchValue)
	case "as-path":
		return fmt.Sprintf("no match as-path %s", match.MatchValue)
	default:
		return fmt.Sprintf("no match %s %s", match.MatchType, match.MatchValue)
	}
}

// generateAddMatchCommand generates command to add a match condition
func (pm *PolicyManager) generateAddMatchCommand(match *client.BgpRouteMapMatch) string {
	switch match.MatchType {
	case "prefix-list":
		return fmt.Sprintf("match ip address prefix-list %s", match.MatchValue)
	case "community":
		return fmt.Sprintf("match community %s", match.MatchValue)
	case "as-path":
		return fmt.Sprintf("match as-path %s", match.MatchValue)
	default:
		return fmt.Sprintf("match %s %s", match.MatchType, match.MatchValue)
	}
}

// generateRemoveSetCommands generates commands to remove set actions
func (pm *PolicyManager) generateRemoveSetCommands(set *client.BgpRouteMapSet) []string {
	var commands []string

	if set.SetLocalPreference > 0 {
		commands = append(commands, "no set local-preference")
	}
	if set.SetMetric > 0 {
		commands = append(commands, "no set metric")
	}
	if set.SetCommunity != "" {
		commands = append(commands, "no set community")
	}
	if set.SetNexthop != "" {
		commands = append(commands, "no set ip next-hop")
	}

	return commands
}

// generateAddSetCommands generates commands to add set actions
func (pm *PolicyManager) generateAddSetCommands(set *client.BgpRouteMapSet) []string {
	var commands []string

	if set.SetLocalPreference > 0 {
		commands = append(commands, fmt.Sprintf("set local-preference %d", set.SetLocalPreference))
	}
	if set.SetMetric > 0 {
		commands = append(commands, fmt.Sprintf("set metric %d", set.SetMetric))
	}
	if set.SetCommunity != "" {
		commands = append(commands, fmt.Sprintf("set community %s", set.SetCommunity))
	}
	if set.SetNexthop != "" {
		commands = append(commands, fmt.Sprintf("set ip next-hop %s", set.SetNexthop))
	}

	return commands
}
