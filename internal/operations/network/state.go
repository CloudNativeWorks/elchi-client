package network

import (
	"fmt"
	"net"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
)

// GetNetworkState handles SUB_GET_NETWORK_STATE command
func GetNetworkState(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	logger.Info("Getting complete network state")
	logger.Debug("Starting comprehensive network state collection")

	// Get interface state
	logger.Debug("Collecting interface states")
	interfaces, err := getCurrentInterfaceStates()
	if err != nil {
		logger.Debugf("Failed to get interface states: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get interface states: %v", err))
	}
	logger.Debugf("Collected %d interface states", len(interfaces))

	// Get route state
	logger.Debug("Collecting route states")
	routes, err := getCurrentRoutes()
	if err != nil {
		logger.Debugf("Failed to get routes: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get routes: %v", err))
	}
	logger.Debugf("Collected %d routes", len(routes))

	// Get policy state
	logger.Debug("Collecting policy states")
	policyManager := NewPolicyManager(logger)
	policies, err := policyManager.GetCurrentPolicies()
	if err != nil {
		logger.Debugf("Failed to get policies: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get policies: %v", err))
	}
	logger.Debugf("Collected %d policies", len(policies))

	// Get routing table definitions
	logger.Debug("Collecting routing table definitions")
	tableManager := NewTableManager(logger)
	routingTables, err := tableManager.GetCurrentTables()
	if err != nil {
		logger.Warnf("Failed to get routing tables: %v", err)
		routingTables = []*client.RoutingTableDefinition{} // Empty list as fallback
	}
	logger.Debugf("Collected %d routing table definitions", len(routingTables))

	// Get current netplan config
	logger.Debug("Collecting current netplan config")
	netplanManager := NewNetplanManager(logger)
	currentYaml, err := netplanManager.GetCurrentConfig()
	if err != nil {
		logger.Warnf("Failed to get current netplan config: %v", err)
		currentYaml = ""
	} else {
		logger.Debugf("Collected netplan config (%d bytes)", len(currentYaml))
	}

	// Build network state
	logger.Debug("Building complete network state response")
	networkState := &client.NetworkState{
		Interfaces:         interfaces,
		Routes:             routes,
		Policies:           policies,
		RoutingTables:      routingTables,
		CurrentNetplanYaml: currentYaml,
	}
	logger.Debugf("Network state built successfully with %d interfaces, %d routes, %d policies, %d tables",
		len(interfaces), len(routes), len(policies), len(routingTables))

	// Return the network state in the response
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success:      true,
				Message:      "Network state retrieved successfully",
				NetworkState: networkState,
			},
		},
	}
}

// getCurrentInterfaceStates returns current interface states
func getCurrentInterfaceStates() ([]*client.InterfaceState, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list links: %w", err)
	}

	var interfaces []*client.InterfaceState
	processedCount := 0
	skippedCount := 0

	for _, link := range links {
		// Skip loopback and virtual interfaces
		if link.Type() == "loopback" || strings.HasPrefix(link.Attrs().Name, "veth") {
			skippedCount++
			continue
		}

		interfaceState := &client.InterfaceState{
			Name:       link.Attrs().Name,
			HasCarrier: link.Attrs().Flags&net.FlagUp != 0,
			MacAddress: link.Attrs().HardwareAddr.String(),
			Mtu:        uint32(link.Attrs().MTU),
		}

		// Determine state
		if link.Attrs().Flags&net.FlagUp != 0 {
			interfaceState.State = "up"
		} else {
			interfaceState.State = "down"
		}

		// Get IP addresses
		addresses, err := netlink.AddrList(link, netlink.FAMILY_ALL)
		if err == nil {
			for _, addr := range addresses {
				interfaceState.Addresses = append(interfaceState.Addresses, addr.IPNet.String())
			}
		}

		interfaces = append(interfaces, interfaceState)
		processedCount++
	}

	return interfaces, nil
}

// getCurrentRoutes returns current routes from all routing tables
func getCurrentRoutes() ([]*client.Route, error) {
	var clientRoutes []*client.Route

	// Get all routing tables first
	tableManager := NewTableManager(logger.NewLogger("network"))
	tables, err := tableManager.GetCurrentTables()
	if err != nil {
		// Fallback to default tables if we can't get table list
		tables = []*client.RoutingTableDefinition{
			{Id: 254, Name: "main"},
			{Id: 253, Name: "default"},
			{Id: 111, Name: "sadeee2"},
			{Id: 144, Name: "sadasd"},
		}
	}

	// Query each table separately
	for _, table := range tables {
		// Skip reserved tables that don't contain user routes
		if table.Id == 255 || table.Id == 0 {
			continue
		}

		routes, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Table: int(table.Id)}, netlink.RT_FILTER_TABLE)
		if err != nil {
			continue // Skip tables that can't be queried
		}

		for _, route := range routes {
			// Skip local, host and link-local routes
			if route.Scope == netlink.SCOPE_HOST {
				continue
			}

			clientRoute := &client.Route{
				Table:  uint32(route.Table),
				Metric: uint32(route.Priority),
			}

			// Set destination
			if route.Dst != nil {
				clientRoute.To = route.Dst.String()
			} else {
				clientRoute.To = "0.0.0.0/0"
				clientRoute.IsDefault = true
			}

			// Set gateway
			if route.Gw != nil {
				clientRoute.Via = route.Gw.String()
			}

			// Set source
			if route.Src != nil {
				clientRoute.Source = route.Src.String()
			}

			// Set interface
			if route.LinkIndex > 0 {
				if link, err := netlink.LinkByIndex(route.LinkIndex); err == nil {
					clientRoute.Interface = link.Attrs().Name
				}
			}

			// Set scope
			switch route.Scope {
			case netlink.SCOPE_UNIVERSE:
				clientRoute.Scope = "global"
			case netlink.SCOPE_SITE:
				clientRoute.Scope = "site"
			case netlink.SCOPE_LINK:
				clientRoute.Scope = "link"
			case netlink.SCOPE_HOST:
				clientRoute.Scope = "host"
			default:
				clientRoute.Scope = "global"
			}

			// Set protocol
			clientRoute.Protocol = getRouteProtocolName(int(route.Protocol))

			clientRoutes = append(clientRoutes, clientRoute)
		}
	}

	return clientRoutes, nil
}

// getRouteProtocolName converts netlink protocol number to human-readable name
func getRouteProtocolName(protocol int) string {
	switch protocol {
	case 0:
		return "unspecified"
	case 1:
		return "redirect"
	case 2:
		return "kernel"
	case 3:
		return "boot"
	case 4:
		return "static"
	case 8:
		return "gated"
	case 9:
		return "ra"
	case 10:
		return "mrt"
	case 11:
		return "zebra"
	case 12:
		return "bird"
	case 13:
		return "dnrouted"
	case 14:
		return "xorp"
	case 15:
		return "ntk"
	case 16:
		return "dhcp"
	case 17:
		return "mrouted"
	case 18:
		return "keepalived"
	case 19:
		return "babel"
	case 186:
		return "bgp" // Common BGP protocol ID
	case 188:
		return "ospf" // Common OSPF protocol ID
	case 189:
		return "isis" // Common ISIS protocol ID
	default:
		return fmt.Sprintf("unknown-%d", protocol)
	}
}

// getCurrentRoutingTables removed - use TableManager.GetCurrentTables() instead
