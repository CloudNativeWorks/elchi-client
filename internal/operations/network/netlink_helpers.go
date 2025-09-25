package network

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
)

// NetlinkHelper provides low-level netlink operations
type NetlinkHelper struct {
	logger *logger.Logger
}

func NewNetlinkHelper(logger *logger.Logger) *NetlinkHelper {
	return &NetlinkHelper{logger: logger}
}

// Interface management functions

// SetInterfaceAddresses sets IP addresses on a network interface
func (nh *NetlinkHelper) SetInterfaceAddresses(link netlink.Link, addresses []string) error {
	for _, addrStr := range addresses {
		addr, err := netlink.ParseAddr(addrStr)
		if err != nil {
			return fmt.Errorf("invalid address %s: %v", addrStr, err)
		}
		addr.Label = ""

		if err := netlink.AddrAdd(link, addr); err != nil {
			if err == syscall.EEXIST {
				continue // Address already exists, skip
			}
			return fmt.Errorf("failed to add address %s: %v", addrStr, err)
		}
	}
	return nil
}

// FlushInterfaceAddresses removes all IP addresses from a network interface
func (nh *NetlinkHelper) FlushInterfaceAddresses(link netlink.Link) error {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("failed to list addresses: %v", err)
	}

	for _, addr := range addrs {
		if err := netlink.AddrDel(link, &addr); err != nil {
			return fmt.Errorf("failed to delete address %s: %v", addr.IPNet, err)
		}
	}
	return nil
}

// SetInterfaceMTU sets MTU on a network interface if needed
func (nh *NetlinkHelper) SetInterfaceMTU(link netlink.Link, mtu uint32) error {
	if mtu == 0 || uint32(link.Attrs().MTU) == mtu {
		return nil // No change needed
	}

	if err := netlink.LinkSetMTU(link, int(mtu)); err != nil {
		return fmt.Errorf("failed to set MTU to %d: %v", mtu, err)
	}

	nh.logger.Infof("Set interface %s MTU to %d", link.Attrs().Name, mtu)
	return nil
}

// SetInterfaceUp brings up a network interface
func (nh *NetlinkHelper) SetInterfaceUp(link netlink.Link) error {
	return netlink.LinkSetUp(link)
}

// SetInterfaceUpIfNeeded brings up interface only if it's down
func (nh *NetlinkHelper) SetInterfaceUpIfNeeded(link netlink.Link) error {
	if link.Attrs().Flags&net.FlagUp != 0 {
		return nil // Already up
	}
	
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring interface up: %v", err)
	}
	
	nh.logger.Infof("Brought interface %s up", link.Attrs().Name)
	return nil
}

// CheckAddressesNeedUpdate checks if interface addresses need to be updated
func (nh *NetlinkHelper) CheckAddressesNeedUpdate(link netlink.Link, addresses []string) (bool, error) {
	currentAddrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return false, fmt.Errorf("failed to list current addresses: %v", err)
	}

	if len(currentAddrs) != len(addresses) {
		return true, nil
	}

	// Create map of current addresses
	currentMap := make(map[string]bool)
	for _, addr := range currentAddrs {
		currentMap[addr.IPNet.String()] = true
	}

	// Check if all desired addresses exist
	for _, addr := range addresses {
		if !currentMap[addr] {
			return true, nil
		}
	}

	return false, nil
}

// Route management functions

// AddRouteIdempotent adds a route in idempotent way
func (nh *NetlinkHelper) AddRouteIdempotent(link netlink.Link, route *client.Route, ifname string) error {
	netlinkRoute, err := nh.clientRouteToNetlink(route, link.Attrs().Index)
	if err != nil {
		return err
	}

	if err := netlink.RouteAdd(netlinkRoute); err != nil {
		if err == syscall.EEXIST {
			nh.logger.Debugf("Route already exists: %s via %s", route.To, route.Via)
			return nil
		}
		return fmt.Errorf("failed to add route: %v", err)
	}

	nh.logger.Infof("Added route: %s via %s on %s", route.To, route.Via, ifname)
	return nil
}

// RemoveRouteIdempotent removes a route in idempotent way
func (nh *NetlinkHelper) RemoveRouteIdempotent(link netlink.Link, route *client.Route, ifname string) error {
	netlinkRoute, err := nh.clientRouteToNetlink(route, link.Attrs().Index)
	if err != nil {
		return err
	}

	if err := netlink.RouteDel(netlinkRoute); err != nil {
		if err == syscall.ESRCH || err == syscall.ENOENT {
			nh.logger.Debugf("Route doesn't exist: %s via %s", route.To, route.Via)
			return nil
		}
		return fmt.Errorf("failed to remove route: %v", err)
	}

	nh.logger.Infof("Removed route: %s via %s from %s", route.To, route.Via, ifname)
	return nil
}

// Policy management functions

// AddPolicyIdempotent adds a routing policy in idempotent way
// DEPRECATED: Use PolicyManager.addRuntimePolicy instead for consistency
func (nh *NetlinkHelper) AddPolicyIdempotent(policy *client.RoutingPolicy, ifname string) error {
	// Delegate to PolicyManager for consistency
	pm := NewPolicyManager(nh.logger)
	return pm.addRuntimePolicy(policy)
}

// RemovePolicyIdempotent removes a routing policy in idempotent way
// DEPRECATED: Use PolicyManager.deleteRuntimePolicy instead for consistency
func (nh *NetlinkHelper) RemovePolicyIdempotent(policy *client.RoutingPolicy, ifname string) error {
	// Delegate to PolicyManager for consistency
	pm := NewPolicyManager(nh.logger)
	return pm.deleteRuntimePolicy(policy)
}

// Utility functions

// GetInterfaceTableID returns routing table ID for interface (new helper function)
func GetInterfaceTableID(ifname string) int {
	rtTablesPath := "/etc/iproute2/rt_tables.d/elchi.conf"
	
	if data, err := os.ReadFile(rtTablesPath); err == nil {
		content := string(data)
		lines := splitLines(content)
		
		for _, line := range lines {
			if len(line) > 0 && line[0] != '#' {
				parts := splitWhitespace(line)
				if len(parts) >= 2 && parts[1] == "elchi-if-"+ifname {
					var tableID int
					n, err := fmt.Sscanf(parts[0], "%d", &tableID)
					if err == nil && n == 1 {
						return tableID
					}
				}
			}
		}
	}
	
	return 0 // Default to main table if not found
}

// ApplyNetplan runs netplan apply
func ApplyNetplan(logger *logger.Logger) error {
	cmd := exec.Command("netplan", "apply")
	if err := cmd.Run(); err != nil {
		logger.Errorf("netplan apply failed: %v", err)
		return err
	}
	logger.Info("netplan apply completed successfully")
	return nil
}

// RemoveNetplanFile removes a netplan YAML file
func RemoveNetplanFile(filename string) error {
	netplanDir := models.NetplanPath
	filePath := fmt.Sprintf("%s/%s", netplanDir, filename)
	
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove file %s: %v", filePath, err)
	}
	
	return nil
}

// Helper functions

func (nh *NetlinkHelper) clientRouteToNetlink(route *client.Route, linkIndex int) (*netlink.Route, error) {
	netlinkRoute := &netlink.Route{
		LinkIndex: linkIndex,
	}

	// Parse destination
	if route.To != "" && route.To != "0.0.0.0/0" {
		if _, dst, err := net.ParseCIDR(route.To); err != nil {
			return nil, fmt.Errorf("invalid destination %s: %v", route.To, err)
		} else {
			netlinkRoute.Dst = dst
		}
	}

	// Parse gateway
	if route.Via != "" {
		gw := net.ParseIP(route.Via)
		if gw == nil {
			return nil, fmt.Errorf("invalid gateway %s", route.Via)
		}
		netlinkRoute.Gw = gw
	}

	// Set table
	if route.Table != 0 {
		netlinkRoute.Table = int(route.Table)
	}

	// Set metric
	if route.Metric != 0 {
		netlinkRoute.Priority = int(route.Metric)
	}

	// Set scope
	switch route.Scope {
	case "global":
		netlinkRoute.Scope = netlink.SCOPE_UNIVERSE
	case "site":
		netlinkRoute.Scope = netlink.SCOPE_SITE
	case "link":
		netlinkRoute.Scope = netlink.SCOPE_LINK
	case "host":
		netlinkRoute.Scope = netlink.SCOPE_HOST
	default:
		netlinkRoute.Scope = netlink.SCOPE_UNIVERSE
	}

	return netlinkRoute, nil
}

// String utility functions
func splitLines(s string) []string {
	var lines []string
	var line string
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, line)
			line = ""
		} else {
			line += string(r)
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func splitWhitespace(s string) []string {
	var parts []string
	var part string
	inSpace := true
	
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !inSpace && part != "" {
				parts = append(parts, part)
				part = ""
				inSpace = true
			}
		} else {
			part += string(r)
			inSpace = false
		}
	}
	
	if part != "" {
		parts = append(parts, part)
	}
	
	return parts
}

// Legacy netplan interface writing removed - use NetplanManager from netplan.go instead