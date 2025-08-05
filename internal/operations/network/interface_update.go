package network

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"

	"errors"
	"syscall"

	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// Interface update
func UpdateInterface(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if len(networkReq.Interfaces) == 0 || networkReq.Interfaces[0] == nil {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	iface := networkReq.Interfaces[0]
	if iface.Ifname == "" {
		return helper.NewErrorResponse(cmd, "ifname is required")
	}

	link, err := netlink.LinkByName(iface.Ifname)
	if err != nil {
		if errors.Is(err, syscall.ENODEV) {
			return helper.NewErrorResponse(cmd, fmt.Sprintf("interface '%s' not found (ENODEV)", iface.Ifname))
		}
		return helper.NewErrorResponse(cmd, err.Error())
	}

	if iface.Interface == nil {
		return helper.NewErrorResponse(cmd, "interface config is nil")
	}

	netplanDir := models.NetplanPath
	
	if iface.Interface.Dhcp4 {
		// DHCP mode: clean up static configuration
		logger.Info(fmt.Sprintf("Configuring interface %s for DHCP", iface.Ifname))
		
		if err := flushInterfaceAddresses(link); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		// Remove old route and policy files
		if err := RemoveNetplanRouteFile(iface.Ifname, netplanDir); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		if err := RemoveNetplanPolicyFile(iface.Ifname, netplanDir); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		// Write interface config for DHCP
		ifaceCopy := &client.Interfaces{
			Ifname: iface.Ifname,
			Interface: &client.Interface{
				Dhcp4: true,
			},
		}
		if err := WriteNetplanInterface(ifaceCopy, netplanDir); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		// Apply netplan configuration for DHCP to take effect immediately
		if err := applyNetplan(logger); err != nil {
			return helper.NewErrorResponse(cmd, fmt.Sprintf("netplan apply failed: %v", err))
		}
		
		if err := setInterfaceUp(link); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
	} else {
		// Static IP mode
		logger.Info(fmt.Sprintf("Configuring interface %s for static IP", iface.Ifname))
		
		// Check if we need to flush addresses (only if addresses are different)
		needsAddressUpdate, err := checkAddressesNeedUpdate(link, iface.Interface.Addresses)
		if err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		if needsAddressUpdate {
			logger.Info(fmt.Sprintf("Updating IP addresses for %s", iface.Ifname))
			if err := flushInterfaceAddresses(link); err != nil {
				return helper.NewErrorResponse(cmd, err.Error())
			}
			
			// Set IP addresses
			if err := setInterfaceAddresses(link, iface.Interface.Addresses); err != nil {
				return helper.NewErrorResponse(cmd, err.Error())
			}
		} else {
			logger.Info(fmt.Sprintf("IP addresses already correct for %s", iface.Ifname))
		}
		
		// Set MTU if different
		if err := setInterfaceMTUIfNeeded(link, iface.Interface.Mtu, logger); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		// Apply routes to runtime (with duplicate detection)
		if len(iface.Routes) > 0 {
			for i, route := range iface.Routes {
				logger.Info(fmt.Sprintf("Processing route %d: to=%s, via=%s, scope=%s, table=%v", 
					i, route.To, route.Via, route.Scope, route.Table))
				if err := addInterfaceRouteIdempotent(link, route, iface.Ifname); err != nil {
					return helper.NewErrorResponse(cmd, fmt.Sprintf("route %d failed: %v", i, err))
				}
			}
		}
		
		// Apply routing policies to runtime (with duplicate detection)
		if len(iface.RoutingPolicies) > 0 {
			for i, policy := range iface.RoutingPolicies {
				logger.Info(fmt.Sprintf("Processing policy %d: from=%s, to=%s, table=%d", 
					i, policy.From, policy.To, policy.Table))
				if err := addInterfacePolicyIdempotent(iface.Ifname, policy); err != nil {
					return helper.NewErrorResponse(cmd, fmt.Sprintf("policy %d failed: %v", i, err))
				}
			}
		}
		
		// Write interface config
		if err := WriteNetplanInterface(iface, netplanDir); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		// Write route config using helper
		if err := WriteRouteFile(iface.Ifname, netplanDir, iface.Routes); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		// Write policy config using helper
		if err := WritePolicyFile(iface.Ifname, netplanDir, iface.RoutingPolicies); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
		
		// Set interface up only if needed
		if err := setInterfaceUpIfNeeded(link, logger); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
	}

	cmd.SubType = *client.SubCommandType_SUB_GET_IF_CONFIG.Enum()
	return NetworkServiceGetIfConfig(cmd, logger)
}

func setInterfaceAddresses(link netlink.Link, addresses []string) error {
	for _, addr := range addresses {
		ipNet, err := netlink.ParseIPNet(addr)
		if err != nil {
			return fmt.Errorf("invalid IP address: %s (%v)", addr, err)
		}
		addrObj := &netlink.Addr{IPNet: ipNet}
		if err := netlink.AddrReplace(link, addrObj); err != nil {
			if errors.Is(err, syscall.EEXIST) {
				return fmt.Errorf("address %s already exists", addr)
			}
			return fmt.Errorf("failed to add/replace address %s: %T: %v", addr, err, err)
		}
	}
	return nil
}

func flushInterfaceAddresses(link netlink.Link) error {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		_ = netlink.AddrDel(link, &addr)
	}
	return nil
}

func RemoveNetplanRouteFile(ifname, dir string) error {
	routeFile := fmt.Sprintf("%s/50-elchi-r-%s.yaml", dir, ifname)
	err := os.Remove(routeFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func RemoveNetplanPolicyFile(ifname, dir string) error {
	policyFile := fmt.Sprintf("%s/50-elchi-p-%s.yaml", dir, ifname)
	err := os.Remove(policyFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func InterfaceTableID(ifname string) int {
	tableMap, err := helper.LoadInterfaceTableMap()
	if err != nil {
		return 0
	}
	tableID, ok := tableMap[ifname]
	if !ok {
		return 0
	}
	return tableID
}



// Netplan interface file writer
func WriteNetplanInterface(iface *client.Interfaces, dir string) error {
	filePath := fmt.Sprintf("%s/50-elchi-if-%s.yaml", dir, iface.Ifname)
	err := os.Remove(filePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	data := buildNetplanInterfaceYAML(iface)
	out, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	
	cmd := exec.Command("sudo", "tee", filePath)
	cmd.Stdin = bytes.NewReader(out)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write netplan interface file: %v", err)
	}

	// Set correct permissions
	if err := exec.Command("sudo", "chmod", "0600", filePath).Run(); err != nil {
		return fmt.Errorf("failed to set file permissions: %v", err)
	}

	return nil
}

func buildNetplanInterfaceYAML(iface *client.Interfaces) map[string]any {
	if iface.Interface == nil {
		return map[string]any{}
	}
	entry := map[string]any{
		"dhcp4": iface.Interface.Dhcp4,
	}
	if iface.Interface.Dhcp4 {
		return map[string]any{
			"network": map[string]any{
				"ethernets": map[string]any{
					iface.Ifname: entry,
				},
				"version": 2,
			},
		}
	}
	if len(iface.Interface.Addresses) > 0 {
		entry["addresses"] = iface.Interface.Addresses
	}
	if iface.Interface.Mtu >= 68 && iface.Interface.Mtu <= 9000 {
		entry["mtu"] = iface.Interface.Mtu
	}

	return map[string]any{
		"network": map[string]any{
			"ethernets": map[string]any{
				iface.Ifname: entry,
			},
			"version": 2,
		},
	}
}

func setInterfaceMTUIfNeeded(link netlink.Link, mtu uint32, logger *logger.Logger) error {
	if mtu == 0 {
		return nil
	}
	if currentMTU := link.Attrs().MTU; currentMTU != int(mtu) {
		logger.Info(fmt.Sprintf("Updating MTU for %s from %d to %d", link.Attrs().Name, currentMTU, mtu))
		return netlink.LinkSetMTU(link, int(mtu))
	}
	return nil
}

func checkAddressesNeedUpdate(link netlink.Link, addresses []string) (bool, error) {
	currentAddrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return false, err
	}

	currentIPs := make(map[string]struct{})
	for _, addr := range currentAddrs {
		currentIPs[addr.IP.String()] = struct{}{}
	}

	newIPs := make(map[string]struct{})
	for _, addr := range addresses {
		ipNet, err := netlink.ParseIPNet(addr)
		if err != nil {
			return false, fmt.Errorf("invalid IP address: %s (%v)", addr, err)
		}
		newIPs[ipNet.IP.String()] = struct{}{}
	}

	return len(currentIPs) != len(newIPs) || !reflect.DeepEqual(currentIPs, newIPs), nil
}

func addInterfaceRouteIdempotent(link netlink.Link, route *client.Route, ifname string) error {
	if route == nil {
		return nil
	}

	var dst *net.IPNet
	var err error
	
	// Handle default route or specific destination
	if route.IsDefault || route.To == "0.0.0.0/0" {
		// Default route: 0.0.0.0/0
		dst, err = netlink.ParseIPNet("0.0.0.0/0")
	} else {
		dst, err = netlink.ParseIPNet(route.To)
	}
	if err != nil {
		return fmt.Errorf("invalid route destination: %s (%v)", route.To, err)
	}

	// Determine table ID
	var tableID int
	if route.Table != nil {
		tableID = int(*route.Table)
	} else {
		tableID = InterfaceTableID(ifname)
		if tableID == 0 {
			return fmt.Errorf("interface table ID not found for %s", ifname)
		}
	}

	// Check if route already exists to avoid duplicates
	existingRoutes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{
		Table: tableID,
	}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("failed to list existing routes: %v", err)
	}

	r := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Protocol:  unix.RTPROT_STATIC,
		Table:     tableID,
	}

	// Handle gateway - only for non-link routes
	if route.Via != "" {
		gw := net.ParseIP(route.Via)
		if gw == nil {
			return fmt.Errorf("invalid gateway IP: %s", route.Via)
		}
		r.Gw = gw
	}

	// Set scope
	if route.Scope != "" {
		switch route.Scope {
		case "global":
			r.Scope = netlink.SCOPE_UNIVERSE
		case "link":
			r.Scope = netlink.SCOPE_LINK
		case "host":
			r.Scope = netlink.SCOPE_HOST
		case "site":
			r.Scope = netlink.SCOPE_SITE
		default:
			return fmt.Errorf("invalid scope: %s", route.Scope)
		}
	} else {
		// Default scope based on route type
		if r.Gw != nil {
			r.Scope = netlink.SCOPE_UNIVERSE
		} else {
			r.Scope = netlink.SCOPE_LINK
		}
	}

	// Set source address
	if route.Source != "" {
		srcIP := net.ParseIP(route.Source)
		if srcIP == nil {
			return fmt.Errorf("invalid source IP: %s", route.Source)
		}
		r.Src = srcIP
	}

	if route.Metric != nil {
		r.Priority = int(*route.Metric)
	}

	// Check for existing identical route
	for _, existingRoute := range existingRoutes {
		if existingRoute.Dst != nil && r.Dst != nil && 
			existingRoute.Dst.String() == r.Dst.String() &&
			existingRoute.LinkIndex == r.LinkIndex {
			
			// Check gateway match
			gwMatch := (existingRoute.Gw == nil && r.Gw == nil) ||
				(existingRoute.Gw != nil && r.Gw != nil && existingRoute.Gw.Equal(r.Gw))
			
			if gwMatch {
				// Route already exists, skip
				return nil
			}
		}
	}
	
	if err := netlink.RouteAdd(r); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			// Route exists, try replace
			if err := netlink.RouteReplace(r); err != nil {
				return fmt.Errorf("route replace failed for %s: %v", route.To, err)
			}
		} else {
			return fmt.Errorf("route add failed for %s: %v", route.To, err)
		}
	}
	return nil
}

func addInterfacePolicyIdempotent(ifname string, policy *client.RoutingPolicy) error {
	if policy == nil {
		return nil
	}

	// Use policy table if specified, otherwise use interface table
	finalTableID := int(policy.Table)
	if finalTableID == 0 {
		tableID := InterfaceTableID(ifname)
		if tableID == 0 {
			return fmt.Errorf("interface table ID not found for %s", ifname)
		}
		finalTableID = tableID
	}

	// Check if policy already exists to avoid duplicates
	existingRules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list existing rules: %v", err)
	}

	for _, existingRule := range existingRules {
		if existingRule.Table == finalTableID {
			srcMatch := true
			dstMatch := true
			
			// Check From (source) match
			if policy.From != "" {
				if existingRule.Src == nil || existingRule.Src.String() != policy.From {
					srcMatch = false
				}
			} else {
				if existingRule.Src != nil {
					srcMatch = false
				}
			}
			
			// Check To (destination) match
			if policy.To != "" {
				if existingRule.Dst == nil || existingRule.Dst.String() != policy.To {
					dstMatch = false
				}
			} else {
				if existingRule.Dst != nil {
					dstMatch = false
				}
			}
			
			if srcMatch && dstMatch {
				// Policy already exists, skip
				return nil
			}
		}
	}

	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V4
	rule.Table = finalTableID

	if policy.From != "" {
		_, ipNet, err := net.ParseCIDR(policy.From)
		if err != nil {
			return fmt.Errorf("invalid policy source: %s (%v)", policy.From, err)
		}
		rule.Src = ipNet
	}

	if policy.To != "" {
		_, ipNet, err := net.ParseCIDR(policy.To)
		if err != nil {
			return fmt.Errorf("invalid policy destination: %s (%v)", policy.To, err)
		}
		rule.Dst = ipNet
	}

	if err := netlink.RuleAdd(rule); err != nil {
		if !errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("failed to add routing policy: %v", err)
		}
	}

	return nil
}

func setInterfaceUp(link netlink.Link) error {
	return netlink.LinkSetUp(link)
}

func applyNetplan(logger *logger.Logger) error {
	cmd := exec.Command("sudo", "netplan", "apply")
	
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		logger.Error(fmt.Sprintf("netplan apply stderr: %s", stderr.String()))
		logger.Error(fmt.Sprintf("netplan apply stdout: %s", stdout.String()))
		return fmt.Errorf("netplan apply failed: %v", err)
	}
	
	logger.Info(fmt.Sprintf("netplan apply successful - stdout: %s", stdout.String()))
	if stderr.Len() > 0 {
		logger.Warn(fmt.Sprintf("netplan apply warnings - stderr: %s", stderr.String()))
	}
	
	return nil
}

func setInterfaceUpIfNeeded(link netlink.Link, logger *logger.Logger) error {
	if err := setInterfaceUp(link); err != nil {
		logger.Error(fmt.Sprintf("failed to set interface %s up: %v", link.Attrs().Name, err))
		return err
	}
	logger.Info(fmt.Sprintf("interface %s is now up", link.Attrs().Name))
	return nil
}

func removeInterfaceRouteIdempotent(link netlink.Link, route *client.Route, ifname string, logger *logger.Logger) error {
	if route == nil {
		return nil
	}
	
	var dst *net.IPNet
	var err error
	
	// Handle default route or specific destination
	if route.IsDefault || route.To == "0.0.0.0/0" {
		dst, err = netlink.ParseIPNet("0.0.0.0/0")
	} else {
		dst, err = netlink.ParseIPNet(route.To)
	}
	if err != nil {
		return fmt.Errorf("invalid route destination: %s (%v)", route.To, err)
	}
	
	// Determine table ID
	var tableID int
	if route.Table != nil {
		tableID = int(*route.Table)
	} else {
		tableID = InterfaceTableID(ifname)
		if tableID == 0 {
			return fmt.Errorf("interface table ID not found for %s", ifname)
		}
	}

	// Find routes in the specific table
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{
		Table: tableID,
	}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("failed to list routes: %v", err)
	}

	// Find and delete matching routes
	var deleted bool
	for _, r := range routes {
		if r.LinkIndex != link.Attrs().Index {
			continue
		}
		
		if r.Dst != nil && r.Dst.String() == dst.String() {
			// Check gateway match if specified
			gwMatch := true
			if route.Via != "" {
				if r.Gw == nil || r.Gw.String() != route.Via {
					gwMatch = false
				}
			}
			
			if gwMatch {
				if err := netlink.RouteDel(&r); err != nil {
					if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.ENOENT) {
						return fmt.Errorf("failed to delete route %s: %v", dst.String(), err)
					}
					// Route already deleted, ignore ESRCH/ENOENT
				}
				deleted = true
				logger.Info(fmt.Sprintf("Successfully removed route %s via %s from table %d", dst.String(), route.Via, tableID))
			}
		}
	}
	
	if !deleted {
		logger.Info(fmt.Sprintf("Route %s via %s not found in table %d (already removed)", dst.String(), route.Via, tableID))
	}
	
	return nil
}

func removeInterfacePolicyIdempotent(ifname string, policy *client.RoutingPolicy, logger *logger.Logger) error {
	if policy == nil {
		return nil
	}

	// Use policy table if specified, otherwise use interface table
	finalTableID := int(policy.Table)
	if finalTableID == 0 {
		tableID := InterfaceTableID(ifname)
		if tableID == 0 {
			return fmt.Errorf("interface table ID not found for %s", ifname)
		}
		finalTableID = tableID
	}

	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list rules: %v", err)
	}

	var deleted bool
	for _, rule := range rules {
		if rule.Table != finalTableID {
			continue
		}
		
		srcMatch := true
		dstMatch := true
		
		// Check From (source) match
		if policy.From != "" {
			if rule.Src == nil || rule.Src.String() != policy.From {
				srcMatch = false
			}
		} else {
			if rule.Src != nil {
				srcMatch = false
			}
		}
		
		// Check To (destination) match
		if policy.To != "" {
			if rule.Dst == nil || rule.Dst.String() != policy.To {
				dstMatch = false
			}
		} else {
			if rule.Dst != nil {
				dstMatch = false
			}
		}
		
		if srcMatch && dstMatch {
			if err := netlink.RuleDel(&rule); err != nil {
				if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.ENOENT) {
					return fmt.Errorf("failed to delete routing policy: %v", err)
				}
				// Rule already deleted, ignore ESRCH/ENOENT
			}
			deleted = true
			logger.Info(fmt.Sprintf("Successfully removed policy from=%s to=%s table=%d", policy.From, policy.To, finalTableID))
		}
	}
	
	if !deleted {
		logger.Info(fmt.Sprintf("Policy from=%s to=%s table=%d not found (already removed)", policy.From, policy.To, finalTableID))
	}
	
	return nil
}
