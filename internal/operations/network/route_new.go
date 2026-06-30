package network

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
	"gopkg.in/yaml.v3"
)

type RouteManagerNew struct {
	logger *logger.Logger
	// persistWarnings collects cases where the kernel route was applied but the
	// netplan file could NOT be updated. Such a route is live now but will be lost
	// on reboot, so the divergence must be reported to the control plane instead of
	// being swallowed as a silent success.
	persistWarnings []string
}

func NewRouteManagerNew(logger *logger.Logger) *RouteManagerNew {
	return &RouteManagerNew{
		logger: logger,
	}
}

// addPersistWarning records (and logs) a kernel-applied-but-not-persisted route.
func (rm *RouteManagerNew) addPersistWarning(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	rm.logger.Warnf("%s", msg)
	rm.persistWarnings = append(rm.persistWarnings, msg)
}

// PersistWarnings returns the netplan-persistence warnings accumulated during the
// last ManageRoutes call (empty if everything was persisted).
func (rm *RouteManagerNew) PersistWarnings() []string {
	return rm.persistWarnings
}

// ManageRoutes handles route operations (add/delete/replace)
func (rm *RouteManagerNew) ManageRoutes(operations []*client.RouteOperation) error {
	rm.logger.Info("Managing route operations")

	for _, op := range operations {
		switch op.Action {
		case client.RouteOperation_ADD:
			if err := rm.addRoute(op.Route); err != nil {
				return fmt.Errorf("failed to add route: %w", err)
			}
		case client.RouteOperation_DELETE:
			if err := rm.deleteRoute(op.Route); err != nil {
				return fmt.Errorf("failed to delete route: %w", err)
			}
		case client.RouteOperation_REPLACE:
			if err := rm.replaceRoute(op.Route); err != nil {
				return fmt.Errorf("failed to replace route: %w", err)
			}
		}
	}

	return nil
}

// addRoute adds a route to the routing table
func (rm *RouteManagerNew) addRoute(route *client.Route) error {
	rm.logger.Infof("Adding route: to=%s, via=%s, interface=%s", route.To, route.Via, route.Interface)

	netlinkRoute, err := rm.clientRouteToNetlink(route)
	if err != nil {
		rm.logger.Debugf("Route conversion failed: %v", err)
		return err
	}

	rm.logger.Debugf("Converted route - Table:%d, LinkIndex:%d, Dst:%v, Gw:%v, Priority:%d",
		netlinkRoute.Table, netlinkRoute.LinkIndex, netlinkRoute.Dst, netlinkRoute.Gw, netlinkRoute.Priority)

	if err := netlink.RouteAdd(netlinkRoute); err != nil {
		if err == syscall.EEXIST {
			// The route is already in the kernel, but the netplan entry may be
			// missing (e.g. a prior persist failed, or it was added manually).
			// Fall through to persistence instead of returning early, otherwise
			// the route is lost on reboot and every re-add keeps no-op'ing.
			rm.logger.Debugf("Route already exists in kernel: %s via %s (ensuring persistence)", route.To, route.Via)
		} else {
			rm.logger.Debugf("netlink.RouteAdd failed: %v", err)
			return fmt.Errorf("failed to add route: %w", err)
		}
	} else {
		rm.logger.Debugf("Route successfully added to netlink")
	}

	// Add to persistent netplan config
	if err := rm.addRouteToPersistentConfig(route); err != nil {
		// Runtime route was added successfully, so don't fail the operation, but
		// record the divergence: this route will NOT survive a reboot.
		rm.addPersistWarning("route to %s via %s applied to kernel but not persisted to netplan (will be lost on reboot): %v", route.To, route.Via, err)
	} else {
		rm.logger.Debugf("Route successfully persisted to netplan")
	}

	return nil
}

// deleteRoute removes a route from the routing table
func (rm *RouteManagerNew) deleteRoute(route *client.Route) error {
	rm.logger.Infof("Deleting route: to=%s, via=%s, interface=%s", route.To, route.Via, route.Interface)
	rm.logger.Debugf("Route details - To:%s, Protocol:%s", route.To, route.Protocol)

	// Check if route is protected from deletion
	if err := rm.validateRouteDeletion(route); err != nil {
		rm.logger.Warnf("Route deletion blocked: %v", err)
		return err
	}

	netlinkRoute, err := rm.clientRouteToNetlink(route)
	if err != nil {
		rm.logger.Debugf("Route conversion for delete failed: %v", err)
		return err
	}

	rm.logger.Debugf("Deleting netlink route - Table:%d, LinkIndex:%d, Dst:%v, Gw:%v",
		netlinkRoute.Table, netlinkRoute.LinkIndex, netlinkRoute.Dst, netlinkRoute.Gw)

	if err := netlink.RouteDel(netlinkRoute); err != nil {
		rm.logger.Debugf("netlink.RouteDel failed: %v", err)
		return fmt.Errorf("failed to delete route: %w", err)
	}

	rm.logger.Debugf("Route successfully deleted from netlink")

	// Remove from persistent config
	if err := rm.removeRouteFromPersistentConfig(route); err != nil {
		// Runtime route was removed, so don't fail the operation, but record the
		// divergence: the route is still in netplan and will be re-created on reboot.
		rm.addPersistWarning("route to %s via %s removed from kernel but not from netplan (will be re-created on reboot): %v", route.To, route.Via, err)
	} else {
		rm.logger.Debugf("Route successfully removed from netplan")
	}

	return nil
}

// replaceRoute replaces an existing route
func (rm *RouteManagerNew) replaceRoute(route *client.Route) error {
	rm.logger.Infof("Replacing route: to=%s, via=%s, interface=%s", route.To, route.Via, route.Interface)
	rm.logger.Debugf("Route details - To:%s, Protocol:%s", route.To, route.Protocol)

	// Check if route is protected from modification
	if err := rm.validateRouteDeletion(route); err != nil {
		rm.logger.Warnf("Route replacement blocked: %v", err)
		return err
	}

	netlinkRoute, err := rm.clientRouteToNetlink(route)
	if err != nil {
		return err
	}

	if err := netlink.RouteReplace(netlinkRoute); err != nil {
		return fmt.Errorf("failed to replace route: %w", err)
	}

	rm.logger.Debugf("Route successfully replaced in netlink")

	// Persist the replacement so the netplan file matches the kernel; otherwise
	// the old route survives in the file and is restored on reboot.
	if err := rm.replaceRouteInPersistentConfig(route); err != nil {
		// Runtime route was replaced successfully, so don't fail the operation, but
		// record the divergence: the kernel and netplan now disagree and the stale
		// route will be restored on reboot.
		rm.addPersistWarning("route to %s via %s replaced in kernel but not persisted to netplan (kernel/netplan diverged, stale route restored on reboot): %v", route.To, route.Via, err)
	} else {
		rm.logger.Debugf("Replaced route successfully persisted to netplan")
	}

	return nil
}

// clientRouteToNetlink converts client.Route to netlink.Route
func (rm *RouteManagerNew) clientRouteToNetlink(route *client.Route) (*netlink.Route, error) {
	netlinkRoute := &netlink.Route{}

	// Parse destination
	if route.To != "" && route.To != "0.0.0.0/0" {
		if _, dst, err := net.ParseCIDR(route.To); err != nil {
			return nil, fmt.Errorf("invalid destination %s: %w", route.To, err)
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

	// Parse source
	if route.Source != "" {
		src := net.ParseIP(route.Source)
		if src == nil {
			return nil, fmt.Errorf("invalid source %s", route.Source)
		}
		netlinkRoute.Src = src
	}

	// Set interface
	if route.Interface != "" {
		link, err := netlink.LinkByName(route.Interface)
		if err != nil {
			return nil, fmt.Errorf("interface %s not found: %w", route.Interface, err)
		}
		netlinkRoute.LinkIndex = link.Attrs().Index
	}

	// Set onlink flag for gateway reachability on interfaces without IP
	if route.Onlink {
		netlinkRoute.Flags = int(netlink.FLAG_ONLINK)
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

// RouteManage handles SUB_ROUTE_MANAGE command
func RouteManage(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if networkReq == nil {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	if len(networkReq.GetRouteOperations()) == 0 {
		return helper.NewErrorResponse(cmd, "no route operations specified")
	}

	manager := NewRouteManagerNew(logger)

	if err := manager.ManageRoutes(networkReq.GetRouteOperations()); err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("route management failed: %v", err))
	}

	// Routes applied to the kernel. If any failed to persist to netplan, the change
	// is live but will not survive a reboot — report that explicitly instead of a
	// bare success so the control plane can see the kernel/netplan divergence.
	message := "Route operations applied"
	netSuccess := true
	if warnings := manager.PersistWarnings(); len(warnings) > 0 {
		netSuccess = false
		message = "Routes applied to kernel but not fully persisted: " + strings.Join(warnings, "; ")
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success: netSuccess,
				Message: message,
			},
		},
	}
}

// RouteList handles SUB_ROUTE_LIST command
func RouteList(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	routes, err := getCurrentRoutes()
	if err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to list routes: %v", err))
	}

	logger.Infof("Listed %d routes", len(routes))

	// Create network state with routes
	networkState := &client.NetworkState{
		Routes: routes,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Error:     "",
		Result: &client.CommandResponse_Network{
			Network: &client.ResponseNetwork{
				Success:      true,
				Message:      "Routes listed successfully",
				NetworkState: networkState,
			},
		},
	}
}

// validateRouteDeletion checks if route can be safely deleted
func (rm *RouteManagerNew) validateRouteDeletion(route *client.Route) error {
	if route.Protocol == "" {
		rm.logger.Debug("Route has no protocol information, allowing deletion")
		return nil
	}

	// Check for protected protocols
	protectedProtocols := map[string]string{
		"bgp":      "BGP routes are dynamically managed and should not be manually deleted",
		"ospf":     "OSPF routes are dynamically managed and should not be manually deleted",
		"isis":     "ISIS routes are dynamically managed and should not be manually deleted",
		"zebra":    "FRR/Zebra routes are dynamically managed and should not be manually deleted",
		"bird":     "BIRD routes are dynamically managed and should not be manually deleted",
		"kernel":   "Kernel routes are system managed and should not be manually deleted",
		"redirect": "ICMP redirect routes are system managed and should not be manually deleted",
		"dhcp":     "DHCP routes are service managed and deletion may break network connectivity",
		"ra":       "IPv6 Router Advertisement routes are system managed and should not be deleted",
	}

	if reason, isProtected := protectedProtocols[route.Protocol]; isProtected {
		return fmt.Errorf("route deletion denied: %s (protocol: %s)", reason, route.Protocol)
	}

	rm.logger.Debugf("Route protocol '%s' is safe for manual deletion", route.Protocol)
	return nil
}

// Route persistence structures for netplan YAML
type NetplanRouteConfig struct {
	Network NetplanRouteNetwork `yaml:"network"`
}

type NetplanRouteNetwork struct {
	Version   int                              `yaml:"version"`
	Renderer  string                           `yaml:"renderer"`
	Ethernets map[string]NetplanRouteInterface `yaml:"ethernets,omitempty"`
}

type NetplanRouteInterface struct {
	Routes []NetplanRouteEntry `yaml:"routes,omitempty"`
}

type NetplanRouteEntry struct {
	To     string `yaml:"to"`
	Via    string `yaml:"via,omitempty"`
	Table  int    `yaml:"table,omitempty"`
	Metric int    `yaml:"metric,omitempty"`
	Scope  string `yaml:"scope,omitempty"`
	Onlink bool   `yaml:"on-link,omitempty"`
}

// routeEntryFromClient builds a netplan route entry from a client route.
func routeEntryFromClient(route *client.Route) NetplanRouteEntry {
	entry := NetplanRouteEntry{
		To:     route.To,
		Via:    route.Via,
		Onlink: route.Onlink,
	}
	if route.Table != 0 {
		entry.Table = int(route.Table)
	}
	if route.Metric != 0 {
		entry.Metric = int(route.Metric)
	}
	if route.Scope != "" {
		entry.Scope = route.Scope
	}
	return entry
}

// upsertRouteEntry appends entry to the interface's route list unless an entry
// with the same To+Via+Onlink already exists. Returns true if it was added.
func upsertRouteEntry(config *NetplanRouteConfig, iface string, entry NetplanRouteEntry) bool {
	if config.Network.Ethernets == nil {
		config.Network.Ethernets = make(map[string]NetplanRouteInterface)
	}
	ifConfig := config.Network.Ethernets[iface]
	for _, existing := range ifConfig.Routes {
		if existing.To == entry.To && existing.Via == entry.Via && existing.Onlink == entry.Onlink {
			return false
		}
	}
	ifConfig.Routes = append(ifConfig.Routes, entry)
	config.Network.Ethernets[iface] = ifConfig
	return true
}

// removeRoutesToDestination drops every route whose To matches `to` from the
// interface. REPLACE keys on the destination (via/metric/etc may have changed),
// so the stale persisted entry must be removed by destination, not by full
// match. Returns true if anything was removed.
func removeRoutesToDestination(config *NetplanRouteConfig, iface, to string) bool {
	ifConfig, ok := config.Network.Ethernets[iface]
	if !ok {
		return false
	}
	kept := make([]NetplanRouteEntry, 0, len(ifConfig.Routes))
	removed := false
	for _, r := range ifConfig.Routes {
		if r.To == to {
			removed = true
			continue
		}
		kept = append(kept, r)
	}
	ifConfig.Routes = kept
	config.Network.Ethernets[iface] = ifConfig
	return removed
}

// replaceRouteInPersistentConfig updates the netplan file for a REPLACE: it
// removes any persisted route to the same destination and adds the new one.
// Without this the netplan file keeps the OLD route and networkd restores the
// stale value on reboot/`netplan apply` (silent divergence from the kernel).
func (rm *RouteManagerNew) replaceRouteInPersistentConfig(route *client.Route) error {
	if route.Interface == "" {
		return fmt.Errorf("route must specify interface for netplan persistence")
	}

	routeFile := fmt.Sprintf("%s/99-elchi-route-%s.yaml", models.NetplanPath, route.Interface)

	config := &NetplanRouteConfig{
		Network: NetplanRouteNetwork{
			Version:   2,
			Renderer:  "networkd",
			Ethernets: make(map[string]NetplanRouteInterface),
		},
	}
	if data, err := os.ReadFile(routeFile); err == nil {
		if err := yaml.Unmarshal(data, config); err != nil {
			return fmt.Errorf("failed to parse existing route config: %w", err)
		}
	}

	removeRoutesToDestination(config, route.Interface, route.To)
	upsertRouteEntry(config, route.Interface, routeEntryFromClient(route))

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal route config: %w", err)
	}

	cmd := exec.Command("sudo", "tee", routeFile)
	cmd.Stdin = strings.NewReader(string(data))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write route config via sudo tee: %w", err)
	}

	chmodCmd := exec.Command("sudo", "chmod", "0600", routeFile)
	if err := chmodCmd.Run(); err != nil {
		rm.logger.Warnf("Failed to set permissions for %s: %v", routeFile, err)
	}

	rm.logger.Infof("Replaced route persisted to %s", routeFile)
	return nil
}

// addRouteToPersistentConfig adds route to netplan persistent configuration
func (rm *RouteManagerNew) addRouteToPersistentConfig(route *client.Route) error {
	if route.Interface == "" {
		return fmt.Errorf("route must specify interface for netplan persistence")
	}

	routeFile := fmt.Sprintf("%s/99-elchi-route-%s.yaml", models.NetplanPath, route.Interface)

	// Load existing config
	config := &NetplanRouteConfig{
		Network: NetplanRouteNetwork{
			Version:   2,
			Renderer:  "networkd",
			Ethernets: make(map[string]NetplanRouteInterface),
		},
	}

	// Load existing file if it exists
	if data, err := os.ReadFile(routeFile); err == nil {
		yaml.Unmarshal(data, config)
	}

	// Build the entry and add it (no-op if an identical entry already exists).
	if !upsertRouteEntry(config, route.Interface, routeEntryFromClient(route)) {
		return nil // Route already exists
	}

	// Write back to file
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal route config: %w", err)
	}

	// Use tee with sudo to write file directly as root (bypass ownership issues)
	cmd := exec.Command("sudo", "tee", routeFile)
	cmd.Stdin = strings.NewReader(string(data))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write route config via sudo tee: %w", err)
	}

	// Set proper permissions
	chmodCmd := exec.Command("sudo", "chmod", "0600", routeFile)
	if err := chmodCmd.Run(); err != nil {
		rm.logger.Warnf("Failed to set permissions for %s: %v", routeFile, err)
	}

	rm.logger.Infof("Route persisted to %s", routeFile)
	return nil
}

// removeRouteFromPersistentConfig removes route from netplan persistent configuration
func (rm *RouteManagerNew) removeRouteFromPersistentConfig(route *client.Route) error {
	if route.Interface == "" {
		return fmt.Errorf("route must specify interface for netplan removal")
	}

	routeFile := fmt.Sprintf("%s/99-elchi-route-%s.yaml", models.NetplanPath, route.Interface)

	// Load existing config
	config := &NetplanRouteConfig{
		Network: NetplanRouteNetwork{
			Version:   2,
			Renderer:  "networkd",
			Ethernets: make(map[string]NetplanRouteInterface),
		},
	}

	// Load existing file
	if data, err := os.ReadFile(routeFile); err != nil {
		return nil // File doesn't exist, nothing to remove
	} else {
		if err := yaml.Unmarshal(data, config); err != nil {
			return fmt.Errorf("failed to parse existing route config: %w", err)
		}
	}

	// Get interface config
	ifConfig, exists := config.Network.Ethernets[route.Interface]
	if !exists {
		return nil // Interface config doesn't exist
	}

	// Remove matching routes - must match ALL significant fields
	var filteredRoutes []NetplanRouteEntry
	for _, existingRoute := range ifConfig.Routes {
		// Match core fields (to, via) - required
		if existingRoute.To != route.To || existingRoute.Via != route.Via {
			filteredRoutes = append(filteredRoutes, existingRoute)
			continue
		}

		// Match optional fields (table, metric, onlink) - only if specified
		tableMatch := (route.Table == 0) || (existingRoute.Table == int(route.Table))
		metricMatch := (route.Metric == 0) || (existingRoute.Metric == int(route.Metric))
		onlinkMatch := existingRoute.Onlink == route.Onlink

		if tableMatch && metricMatch && onlinkMatch {
			continue // Skip this route (remove it)
		}

		filteredRoutes = append(filteredRoutes, existingRoute)
	}

	ifConfig.Routes = filteredRoutes

	// Update or remove the config
	if len(filteredRoutes) == 0 {
		// Remove interface config if no routes left
		delete(config.Network.Ethernets, route.Interface)

		// If no interfaces left, remove the entire file
		if len(config.Network.Ethernets) == 0 {
			if err := os.Remove(routeFile); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove empty route config: %w", err)
			}
			rm.logger.Infof("Removed empty route config file %s", routeFile)
			return nil
		}
	} else {
		// Update the interface config
		config.Network.Ethernets[route.Interface] = ifConfig
	}

	// Write back to file
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal route config: %w", err)
	}

	// Use tee with sudo to write file directly as root
	cmd := exec.Command("sudo", "tee", routeFile)
	cmd.Stdin = strings.NewReader(string(data))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write route config via sudo tee: %w", err)
	}

	// Set proper permissions
	chmodCmd := exec.Command("sudo", "chmod", "0600", routeFile)
	if err := chmodCmd.Run(); err != nil {
		rm.logger.Warnf("Failed to set permissions for %s: %v", routeFile, err)
	}

	rm.logger.Infof("Route removed from persistent config %s for interface %s", routeFile, route.Interface)
	return nil
}
