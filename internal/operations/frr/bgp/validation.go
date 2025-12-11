package bgp

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

type validationCache struct {
	mutex          sync.RWMutex
	ipValidation   map[string]bool
	cidrValidation map[string]bool
	nameValidation map[string]bool
}

var (
	globalValidationCache = &validationCache{
		ipValidation:   make(map[string]bool),
		cidrValidation: make(map[string]bool),
		nameValidation: make(map[string]bool),
	}

	// Pre-compiled regex for performance
	interfaceNameRegex  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*[0-9]*(/[0-9]+)*$`)
	routeMapNameRegex   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
	prefixListNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
)

type ValidationManager struct {
	logger *logger.Logger
}

// NewValidationManager creates a new validation manager
func NewValidationManager(logger *logger.Logger) ValidationManagerInterface {
	return &ValidationManager{
		logger: logger,
	}
}

func (vm *ValidationManager) ValidateBgpConfig(config *client.BgpConfig) *ValidationResult {
	result := &ValidationResult{Valid: true}

	if config == nil {
		result.AddError("config", "BGP configuration cannot be nil", "MISSING_CONFIG", nil)
		return result
	}

	// Validate AS number
	if config.AutonomousSystem == 0 {
		result.AddError("autonomous_system", "AS number is required and must be greater than 0", "INVALID_AS", config.AutonomousSystem)
	} else if config.AutonomousSystem > 4294967294 {
		result.AddError("autonomous_system", "AS number exceeds maximum value (4294967295)", "INVALID_AS", config.AutonomousSystem)
	}

	// Validate Router ID
	if config.RouterId != "" {
		if !vm.isValidIPv4(config.RouterId) {
			result.AddError("router_id", "Router ID must be a valid IPv4 address", "INVALID_ROUTER_ID", config.RouterId)
		}
	}

	// Validate BGP timers (simplified - direct fields)
	if config.KeepaliveTime > 0 && config.HoldTime > 0 {
		vm.validateBgpTimersSimplified(config.KeepaliveTime, config.HoldTime, result)
	}

	// Validate neighbors
	for i, neighbor := range config.Neighbors {
		neighborResult := vm.ValidateNeighbor(neighbor)
		if !neighborResult.Valid {
			for _, err := range neighborResult.Errors {
				result.AddError(fmt.Sprintf("neighbors[%d].%s", i, err.Field), err.Message, err.Code, err.Value)
			}
		}
		for _, warn := range neighborResult.Warnings {
			result.AddWarning(fmt.Sprintf("neighbors[%d].%s", i, warn.Field), warn.Message, warn.Code, warn.Value)
		}
	}

	// Validate additional BGP config fields
	if config.MaximumPaths > 256 {
		result.AddError("maximum_paths", "Maximum paths cannot exceed 256", "INVALID_MAX_PATHS", config.MaximumPaths)
	}

	// Administrative distance validation
	if config.AdministrativeDistance != "" {
		distances := strings.Split(config.AdministrativeDistance, "-")
		if len(distances) != 3 {
			result.AddError("administrative_distance", "Administrative distance must be in format 'external-internal-local'", "INVALID_ADMIN_DISTANCE_FORMAT", config.AdministrativeDistance)
		} else {
			// Check each value
			for i, d := range distances {
				distType := []string{"external", "internal", "local"}[i]
				val, err := strconv.ParseUint(d, 10, 32)
				if err != nil {
					result.AddError("administrative_distance", fmt.Sprintf("%s distance must be a number", distType), "INVALID_ADMIN_DISTANCE_VALUE", d)
				} else if val < 1 || val > 255 {
					result.AddError("administrative_distance", fmt.Sprintf("%s distance must be between 1 and 255", distType), "INVALID_ADMIN_DISTANCE_RANGE", val)
				}
			}
		}
	}

	// Validate route maps
	for i, routeMap := range config.RouteMaps {
		if routeMap.Name == "" {
			result.AddError(fmt.Sprintf("route_maps[%d].name", i), "Route map name is required", "MISSING_ROUTE_MAP_NAME", routeMap.Name)
		}
		if routeMap.Sequence == 0 {
			result.AddError(fmt.Sprintf("route_maps[%d].sequence", i), "Route map sequence is required and must be greater than 0", "INVALID_SEQUENCE", routeMap.Sequence)
		}
	}

	// Validate community lists
	for i, communityList := range config.CommunityLists {
		if communityList.Name == "" {
			result.AddError(fmt.Sprintf("community_lists[%d].name", i), "Community list name is required", "MISSING_COMMUNITY_LIST_NAME", communityList.Name)
		}
		if communityList.CommunityValues == "" {
			result.AddError(fmt.Sprintf("community_lists[%d].community_values", i), "Community values are required", "MISSING_COMMUNITY_VALUES", communityList.CommunityValues)
		}
	}

	// Validate prefix lists
	for i, prefixList := range config.PrefixLists {
		if prefixList.Name == "" {
			result.AddError(fmt.Sprintf("prefix_lists[%d].name", i), "Prefix list name is required", "MISSING_PREFIX_LIST_NAME", prefixList.Name)
		}
		if prefixList.Prefix == "" {
			result.AddError(fmt.Sprintf("prefix_lists[%d].prefix", i), "Prefix is required", "MISSING_PREFIX", prefixList.Prefix)
		} else if !vm.isValidCIDR(prefixList.Prefix) {
			result.AddError(fmt.Sprintf("prefix_lists[%d].prefix", i), "Prefix must be a valid CIDR notation", "INVALID_PREFIX", prefixList.Prefix)
		}
		if prefixList.Le > 0 && prefixList.Ge > 0 && prefixList.Le < prefixList.Ge {
			result.AddError(fmt.Sprintf("prefix_lists[%d].le", i), "LE value must be greater than or equal to GE value", "INVALID_LE_GE", fmt.Sprintf("le=%d, ge=%d", prefixList.Le, prefixList.Ge))
		}
	}

	return result
}

// ValidateNeighbor validates a BGP neighbor configuration
func (vm *ValidationManager) ValidateNeighbor(neighbor *client.BgpNeighbor) *ValidationResult {
	result := &ValidationResult{Valid: true}

	if neighbor == nil {
		result.AddError("neighbor", "BGP neighbor cannot be nil", "MISSING_NEIGHBOR", nil)
		return result
	}

	// Validate neighbor IP
	if neighbor.PeerIp == "" {
		result.AddError("peer_ip", "Neighbor IP is required", "MISSING_IP", neighbor.PeerIp)
	} else if !vm.isValidIPv4(neighbor.PeerIp) {
		result.AddError("peer_ip", "Neighbor IP must be a valid IPv4 address", "INVALID_IP", neighbor.PeerIp)
	}

	// Validate remote AS
	if neighbor.RemoteAs == 0 {
		result.AddError("remote_as", "Remote AS number is required and must be greater than 0", "INVALID_AS", neighbor.RemoteAs)
	} else if neighbor.RemoteAs > 4294967294 {
		result.AddError("remote_as", "Remote AS number exceeds maximum value (4294967295)", "INVALID_AS", neighbor.RemoteAs)
	}

	// Validate description (optional but limited length)
	if neighbor.Description != "" && len(neighbor.Description) > 80 {
		result.AddWarning("description", "Description should be 80 characters or less", "LONG_DESCRIPTION", neighbor.Description)
	}

	// Validate password (optional but should be secure)
	if neighbor.Password != "" {
		if len(neighbor.Password) < 6 {
			result.AddWarning("password", "Password should be at least 6 characters for security", "WEAK_PASSWORD", len(neighbor.Password))
		}
	}

	// Validate update source
	if neighbor.UpdateSource != "" {
		if !vm.isValidIPv4(neighbor.UpdateSource) && !vm.isValidInterfaceName(neighbor.UpdateSource) {
			result.AddError("update_source", "Update source must be a valid IPv4 address or interface name", "INVALID_UPDATE_SOURCE", neighbor.UpdateSource)
		}
	}

	// Validate EBGP multihop
	if neighbor.EbgpMultihop && neighbor.EbgpMultihopTtl > 0 && neighbor.EbgpMultihopTtl > 255 {
		result.AddError("ebgp_multihop_ttl", "EBGP multihop TTL must be between 1 and 255", "INVALID_TTL", neighbor.EbgpMultihopTtl)
	}

	// Validate next hop self
	// No validation needed for boolean fields

	// Validate route maps
	if routeMaps := neighbor.GetRouteMaps(); routeMaps != nil {
		// Validate inbound route maps (array)
		for _, routeMapIn := range routeMaps.RouteMapIn {
			if routeMapIn != "" && !vm.isValidRouteMapName(routeMapIn) {
				result.AddError("route_maps.in", "Invalid route map name for incoming routes", "INVALID_ROUTE_MAP_NAME", routeMapIn)
			}
		}

		// Validate outbound route maps (array)
		for _, routeMapOut := range routeMaps.RouteMapOut {
			if routeMapOut != "" && !vm.isValidRouteMapName(routeMapOut) {
				result.AddError("route_maps.out", "Invalid route map name for outgoing routes", "INVALID_ROUTE_MAP_NAME", routeMapOut)
			}
		}
	}

	// Validate prefix lists
	if prefixLists := neighbor.GetPrefixLists(); prefixLists != nil {
		// Validate inbound prefix lists (array)
		for _, prefixListIn := range prefixLists.PrefixListIn {
			if prefixListIn != "" && !vm.isValidPrefixListName(prefixListIn) {
				result.AddError("prefix_lists.in", "Invalid prefix list name for incoming routes", "INVALID_PREFIX_LIST_NAME", prefixListIn)
			}
		}

		// Validate outbound prefix lists (array)
		for _, prefixListOut := range prefixLists.PrefixListOut {
			if prefixListOut != "" && !vm.isValidPrefixListName(prefixListOut) {
				result.AddError("prefix_lists.out", "Invalid prefix list name for outgoing routes", "INVALID_PREFIX_LIST_NAME", prefixListOut)
			}
		}
	}

	// Note: BFD validation removed as it's not in the simplified proto structure

	return result
}

func (vm *ValidationManager) validateBgpTimersSimplified(keepalive, holdtime uint32, result *ValidationResult) {
	if keepalive > 0 && holdtime > 0 {
		// RFC 4271: Keepalive should be 1/3 of holdtime
		if keepalive > holdtime/3 {
			result.AddWarning("keepalive_time", "Keepalive timer should be approximately 1/3 of holdtime", "TIMER_RATIO", keepalive)
		}

		// Minimum values
		if holdtime < 3 {
			result.AddError("hold_time", "Holdtime must be at least 3 seconds", "INVALID_HOLDTIME", holdtime)
		}
		if keepalive < 1 {
			result.AddError("keepalive_time", "Keepalive must be at least 1 second", "INVALID_KEEPALIVE", keepalive)
		}

	}
}

// isValidIPv4 validates IPv4 address format with caching
func (vm *ValidationManager) isValidIPv4(ip string) bool {
	if ip == "" {
		return false
	}

	// Check cache first
	globalValidationCache.mutex.RLock()
	if result, exists := globalValidationCache.ipValidation[ip]; exists {
		globalValidationCache.mutex.RUnlock()
		return result
	}
	globalValidationCache.mutex.RUnlock()

	// Perform validation
	parsed := net.ParseIP(ip)
	result := parsed != nil && parsed.To4() != nil

	// Cache result
	globalValidationCache.mutex.Lock()
	globalValidationCache.ipValidation[ip] = result
	globalValidationCache.mutex.Unlock()

	return result
}

// isValidCIDR validates CIDR notation with caching
func (vm *ValidationManager) isValidCIDR(cidr string) bool {
	if cidr == "" {
		return false
	}

	// Check cache first
	globalValidationCache.mutex.RLock()
	if result, exists := globalValidationCache.cidrValidation[cidr]; exists {
		globalValidationCache.mutex.RUnlock()
		return result
	}
	globalValidationCache.mutex.RUnlock()

	// Perform validation
	_, _, err := net.ParseCIDR(cidr)
	result := err == nil

	// Cache result
	globalValidationCache.mutex.Lock()
	globalValidationCache.cidrValidation[cidr] = result
	globalValidationCache.mutex.Unlock()

	return result
}

// isValidInterfaceName validates interface name format with pre-compiled regex
func (vm *ValidationManager) isValidInterfaceName(name string) bool {
	if name == "" {
		return false
	}
	return interfaceNameRegex.MatchString(name)
}

// isValidRouteMapName validates route map name format with pre-compiled regex and caching
func (vm *ValidationManager) isValidRouteMapName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}

	// Check cache first
	globalValidationCache.mutex.RLock()
	if result, exists := globalValidationCache.nameValidation["rm_"+name]; exists {
		globalValidationCache.mutex.RUnlock()
		return result
	}
	globalValidationCache.mutex.RUnlock()

	// Perform validation with pre-compiled regex
	result := routeMapNameRegex.MatchString(name)

	// Cache result
	globalValidationCache.mutex.Lock()
	globalValidationCache.nameValidation["rm_"+name] = result
	globalValidationCache.mutex.Unlock()

	return result
}

// isValidPrefixListName validates prefix list name format with pre-compiled regex and caching
func (vm *ValidationManager) isValidPrefixListName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}

	// Check cache first
	globalValidationCache.mutex.RLock()
	if result, exists := globalValidationCache.nameValidation["pl_"+name]; exists {
		globalValidationCache.mutex.RUnlock()
		return result
	}
	globalValidationCache.mutex.RUnlock()

	// Perform validation with pre-compiled regex
	result := prefixListNameRegex.MatchString(name)

	// Cache result
	globalValidationCache.mutex.Lock()
	globalValidationCache.nameValidation["pl_"+name] = result
	globalValidationCache.mutex.Unlock()

	return result
}

func (vm *ValidationManager) ValidateIPAddresses(addresses []string) error {
	var err error

	for i, addr := range addresses {
		if addr == "" {
			return fmt.Errorf("address[%d]: IP address cannot be empty", i)
		}
		if !vm.isValidIPv4(addr) {
			return fmt.Errorf("address[%d]: Invalid IPv4 address format", i)
		}
	}
	return err
}
