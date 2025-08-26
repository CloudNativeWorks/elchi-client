package network

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"net"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/network/parsers"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Returns the files in the Netplan directory with the specified prefix
func listNetplanFiles(prefix string) ([]string, error) {
	entries, err := os.ReadDir(models.NetplanPath)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), prefix) {
			files = append(files, filepath.Join(models.NetplanPath, entry.Name()))
		}
	}
	return files, nil
}

func isPhysicalInterface(link netlink.Link) bool {
	if _, ok := link.(*netlink.Device); !ok {
		return false
	}
	name := link.Attrs().Name
	if name == "lo" || strings.HasPrefix(name, "dummy") || strings.HasPrefix(name, "veth") ||
		strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
		return false
	}
	return true
}

func isSystemGeneratedRoute(r netlink.Route) string {
	// DHCP routes
	if r.Protocol == unix.RTPROT_DHCP {
		return "dhcp"
	}
	// Kernel routes
	if r.Protocol == unix.RTPROT_KERNEL {
		return "kernel"
	}
	// Boot routes
	if r.Protocol == unix.RTPROT_BOOT {
		return "boot"
	}
	// static routes
	if r.Protocol == unix.RTPROT_STATIC {
		return "static"
	}
	// Other system routes
	return "system"
}

func getRuntimeRoutes(link netlink.Link) []*client.Route {
	var result []*client.Route
	ifname := link.Attrs().Name
	
	// Get interface table ID
	tableMap, err := helper.LoadInterfaceTableMap()
	if err != nil {
		return result
	}
	
	interfaceTableID, hasInterfaceTable := tableMap[ifname]
	
	// Determine tables to check based on interface mapping
	tablesToCheck := []int{0} // Always check main table
	if hasInterfaceTable && interfaceTableID != 0 {
		tablesToCheck = append(tablesToCheck, interfaceTableID)
	}
	
	// Track seen routes to avoid duplicates
	seenRoutes := make(map[string]bool)
	
	for _, tableID := range tablesToCheck {
		// List routes from specific table
		routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{
			Table: tableID,
		}, netlink.RT_FILTER_TABLE)
		if err != nil {
			continue // Skip this table if error
		}
		
		for _, r := range routes {
			// Only include routes that use this interface
			if r.LinkIndex != link.Attrs().Index {
				continue
			}
			
			route := &client.Route{}
			
			// Handle destination
			if r.Dst == nil {
				// Default route
				route.To = "0.0.0.0/0"
				route.IsDefault = true
			} else {
				route.To = r.Dst.String()
			}
			
			// Handle gateway - gateway is optional for link-local routes
			if r.Gw != nil {
				route.Via = r.Gw.String()
			}
			
			// Handle source - use protocol type instead of source IP
			route.Source = isSystemGeneratedRoute(r)
			
			// Handle scope
			switch r.Scope {
			case netlink.SCOPE_UNIVERSE:
				route.Scope = "global"
			case netlink.SCOPE_LINK:
				route.Scope = "link"
			case netlink.SCOPE_HOST:
				route.Scope = "host"
			case netlink.SCOPE_SITE:
				route.Scope = "site"
			}
			
			// Handle table
			if r.Table != 0 {
				route.Table = uint32(r.Table)
			}
			
			// Handle metric
			if r.Priority != 0 {
				route.Metric = uint32(r.Priority)
			}
			
			// Create unique key for duplicate detection (simplified)
			tableStr := "0"
			if route.Table != 0 {
				tableStr = fmt.Sprintf("%d", route.Table)
			}
			routeKey := fmt.Sprintf("%s|%s|%s", route.To, route.Via, tableStr)
			
			// Skip if we've already seen this route
			if seenRoutes[routeKey] {
				continue
			}
			seenRoutes[routeKey] = true
			
			result = append(result, route)
		}
	}
	
	return result
}

func getRuntimePolicies(ifname string) []*client.RoutingPolicy {
	var result []*client.RoutingPolicy
	
	// Get interface table ID
	tableMap, err := helper.LoadInterfaceTableMap()
	if err != nil {
		return result
	}
	
	interfaceTableID, hasInterfaceTable := tableMap[ifname]
	
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return result
	}
	
	for _, rule := range rules {
		// Include rules that:
		// 1. Are bound to this interface (IifName)
		// 2. Use interface's routing table
		// 3. Are relevant to this interface
		includeRule := false
		
		if rule.IifName == ifname {
			includeRule = true
		} else if hasInterfaceTable && rule.Table == interfaceTableID {
			includeRule = true
		}
		
		if !includeRule {
			continue
		}
		
		policy := &client.RoutingPolicy{}
		
		// Handle source (from)
		if rule.Src != nil {
			policy.From = rule.Src.String()
		}
		
		// Handle destination (to)
		if rule.Dst != nil {
			policy.To = rule.Dst.String()
		}
		
		// Handle table
		policy.Table = uint32(rule.Table)
		
		result = append(result, policy)
	}
	return result
}

// NetworkServiceGetIfConfig returns current network state - simplified approach
// Prioritizes runtime state over netplan config for accuracy
func NetworkServiceGetIfConfig(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	// Load interface table mapping for routing table information
	tableMap, err := helper.LoadInterfaceTableMap()
	if err != nil {
		return helper.NewErrorResponse(cmd, "error loading interface table map: "+err.Error())
	}

	// Load netplan DHCP4 settings as fallback (only essential config)
	netplanDhcp4 := loadNetplanDhcp4Settings()

	// Get runtime physical interfaces from netlink
	links, err := netlink.LinkList()
	if err != nil {
		return helper.NewErrorResponse(cmd, "netlink error: "+err.Error())
	}

	// Build interface list with runtime state
	var interfaces []*client.InterfaceState
	var allRoutes []*client.Route
	var allPolicies []*client.RoutingPolicy
	
	for _, link := range links {
		if !isPhysicalInterface(link) {
			continue
		}
		iface := buildInterfaceInfo(link, tableMap, netplanDhcp4)
		interfaces = append(interfaces, iface)
		
		// Collect all routes and policies for the NetworkState
		ifname := link.Attrs().Name
		routes := getRuntimeRoutes(link)
		policies := getRuntimePolicies(ifname)
		allRoutes = append(allRoutes, routes...)
		allPolicies = append(allPolicies, policies...)
	}

	networkState := &client.NetworkState{
		Interfaces: interfaces,
		Routes:     allRoutes,
		Policies:   allPolicies,
	}
	
	result := &client.ResponseNetwork{
		Success:      true,
		Message:      "Network state retrieved successfully",
		NetworkState: networkState,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Network{
			Network: result,
		},
	}
}

// loadNetplanDhcp4Settings loads DHCP4 settings from netplan files (simplified)
func loadNetplanDhcp4Settings() map[string]bool {
	netplanDhcp4 := map[string]bool{}
	ifFiles, _ := listNetplanFiles(models.NetplanIfPrefix)
	for _, path := range ifFiles {
		parsed, _ := parsers.ParseNetplanInterfaceFile(path)
		for _, info := range parsed {
			netplanDhcp4[info.Ifname] = info.Dhcp4
		}
	}
	return netplanDhcp4
}

// buildInterfaceInfo creates interface info with runtime state (simplified)
func buildInterfaceInfo(link netlink.Link, _ map[string]int, _ map[string]bool) *client.InterfaceState {
	ifname := link.Attrs().Name
	
	// Runtime IP addresses
	addrs, _ := netlink.AddrList(link, netlink.FAMILY_V4)
	var addresses []string
	for _, addr := range addrs {
		addresses = append(addresses, addr.IPNet.String())
	}
	
	// Determine interface state
	state := "down"
	if link.Attrs().Flags&net.FlagUp != 0 {
		state = "up"
	}
	
	// Get MAC address
	macAddr := link.Attrs().HardwareAddr.String()
	
	return &client.InterfaceState{
		Name:       ifname,
		Addresses:  addresses,
		State:      state,
		HasCarrier: state == "up",
		MacAddress: macAddr,
		Mtu:        uint32(link.Attrs().MTU),
	}
}
