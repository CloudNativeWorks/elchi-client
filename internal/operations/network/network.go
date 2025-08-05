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
	
	// Get routes from multiple sources:
	// 1. Main table (0)
	// 2. Interface specific table (if exists)
	// 3. Common elchi table range (100-199)
	tablesToCheck := []int{0} // Always check main table
	if hasInterfaceTable && interfaceTableID != 0 {
		tablesToCheck = append(tablesToCheck, interfaceTableID)
	}
	
	// Also check common elchi table range
	for tableID := 100; tableID <= 199; tableID++ {
		// Skip if already added
		found := false
		for _, existingTable := range tablesToCheck {
			if existingTable == tableID {
				found = true
				break
			}
		}
		if !found {
			tablesToCheck = append(tablesToCheck, tableID)
		}
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
				table := uint32(r.Table)
				route.Table = &table
			}
			
			// Handle metric
			if r.Priority != 0 {
				metric := uint32(r.Priority)
				route.Metric = &metric
			}
			
			// Create unique key for duplicate detection
			// Include to, via, table, scope to identify unique routes
			var tableStr string
			if route.Table != nil {
				tableStr = fmt.Sprintf("%d", *route.Table)
			} else {
				tableStr = "0"
			}
			routeKey := fmt.Sprintf("%s|%s|%s|%s", route.To, route.Via, tableStr, route.Scope)
			
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

func NetworkServiceGetIfConfig(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	// 1. Get dhcp4, route, routing policy values from netplan
	netplanDhcp4 := map[string]bool{}
	netplanRoutes := map[string][]*client.Route{}
	netplanPolicies := map[string][]*client.RoutingPolicy{}
	tableMap, err := helper.LoadInterfaceTableMap()
	if err != nil {
		return helper.NewErrorResponse(cmd, "error loading interface table map: "+err.Error())
	}

	ifFiles, _ := listNetplanFiles(models.NetplanIfPrefix)
	for _, path := range ifFiles {
		parsed, _ := parsers.ParseNetplanInterfaceFile(path)
		for _, iface := range parsed {
			if iface.Interface != nil {
				netplanDhcp4[iface.Ifname] = iface.Interface.Dhcp4
			}
		}
	}

	// Load route files
	routeFiles, _ := listNetplanFiles(models.NetplanRoutePrefix)
	for _, path := range routeFiles {
		parsed, _ := parsers.ParseNetplanRouteFile(path)
		for _, iface := range parsed {
			if len(iface.Routes) > 0 {
				netplanRoutes[iface.Ifname] = iface.Routes
			}
			if len(iface.RoutingPolicies) > 0 {
				netplanPolicies[iface.Ifname] = iface.RoutingPolicies
			}
		}
	}

	// Load policy files (50-elchi-p-*)
	policyFiles, _ := listNetplanFiles("50-elchi-p-")
	for _, path := range policyFiles {
		parsed, _ := parsers.ParseNetplanRouteFile(path) // Use route parser for policy files too
		for _, iface := range parsed {
			if len(iface.RoutingPolicies) > 0 {
				netplanPolicies[iface.Ifname] = iface.RoutingPolicies
			}
		}
	}

	// 2. Get runtime physical interfaces from netlink
	links, err := netlink.LinkList()
	if err != nil {
		return helper.NewErrorResponse(cmd, "netlink error: "+err.Error())
	}

	var interfaces []*client.Interfaces
	for _, link := range links {
		if !isPhysicalInterface(link) {
			continue
		}
		ifname := link.Attrs().Name
		iface := &client.Interfaces{Ifname: ifname}
		iface.Table = uint32(tableMap[ifname])
		ci := &client.Interface{}

		// Runtime IP addresses
		addrs, _ := netlink.AddrList(link, netlink.FAMILY_V4)
		for _, addr := range addrs {
			ci.Addresses = append(ci.Addresses, addr.IPNet.String())
		}
		// Runtime MTU
		ci.Mtu = uint32(link.Attrs().MTU)
		// Runtime state
		if link.Attrs().Flags&net.FlagUp != 0 {
			ci.State = "up"
		} else {
			ci.State = "down"
		}

		// add dhcp4 value from netplan (fallback)
		if dhcp4, ok := netplanDhcp4[ifname]; ok {
			ci.Dhcp4 = dhcp4
		}
		iface.Interface = ci

		// Get routes - priority: runtime > netplan config
		runtimeRoutes := getRuntimeRoutes(link)
		if len(runtimeRoutes) > 0 {
			iface.Routes = runtimeRoutes
		} else if routes, ok := netplanRoutes[ifname]; ok {
			iface.Routes = routes
		}

		// Get routing policies - priority: runtime > netplan config
		runtimePolicies := getRuntimePolicies(ifname)
		if len(runtimePolicies) > 0 {
			iface.RoutingPolicies = runtimePolicies
		} else if policies, ok := netplanPolicies[ifname]; ok {
			iface.RoutingPolicies = policies
		}

		interfaces = append(interfaces, iface)
	}

	result := &client.ResponseNetwork{
		Interfaces: interfaces,
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
