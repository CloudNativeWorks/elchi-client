package network

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/CloudNativeWorks/elchi-proto/client"
	"gopkg.in/yaml.v3"
)

// RouteFileConfig represents the structure for route/policy netplan files
type RouteFileConfig struct {
	Network RouteNetworkConfig `yaml:"network"`
}

type RouteNetworkConfig struct {
	Version   int                       `yaml:"version"`
	Renderer  string                    `yaml:"renderer,omitempty"`
	Ethernets map[string]RouteEthernetConfig `yaml:"ethernets"`
}

type RouteEthernetConfig struct {
	Routes        []RouteFileEntry        `yaml:"routes,omitempty"`
	RoutingPolicy []RoutingPolicyFileEntry `yaml:"routing-policy,omitempty"`
}

type RouteFileEntry struct {
	To        string `yaml:"to"`
	Via       string `yaml:"via,omitempty"`
	Scope     string `yaml:"scope,omitempty"`
	Table     int    `yaml:"table,omitempty"`
	Metric    *int   `yaml:"metric,omitempty"`
	Source    string `yaml:"from,omitempty"` // Note: netplan uses 'from' for source
	IsDefault bool   `yaml:"-"`              // Don't write to YAML, handled by To field
}

type RoutingPolicyFileEntry struct {
	From  string `yaml:"from,omitempty"`
	To    string `yaml:"to,omitempty"`
	Table int    `yaml:"table,omitempty"`
}

// RouteHelper manages route and routing-policy configurations
type RouteHelper struct {
	config *RouteFileConfig
}

// NewRouteHelper creates a new RouteHelper instance
func NewRouteHelper() *RouteHelper {
	return &RouteHelper{
		config: &RouteFileConfig{
			Network: RouteNetworkConfig{
				Version:   2,
				Renderer:  "networkd",
				Ethernets: make(map[string]RouteEthernetConfig),
			},
		},
	}
}

// LoadFromFile loads existing route configuration from file
func (rh *RouteHelper) LoadFromFile(filePath string) error {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// File doesn't exist, use empty config
		return nil
	}
	
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read route file: %v", err)
	}
	
	if err := yaml.Unmarshal(data, rh.config); err != nil {
		return fmt.Errorf("failed to parse route YAML: %v", err)
	}
	
	return nil
}

// SaveToFile saves route configuration to file using sudo
func (rh *RouteHelper) SaveToFile(filePath string) error {
	data, err := yaml.Marshal(rh.config)
	if err != nil {
		return fmt.Errorf("failed to marshal route config: %v", err)
	}
	
	// Use sudo tee to write file (same approach as WriteNetplanInterface)
	cmd := exec.Command("sudo", "tee", filePath)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write route file: %v", err)
	}

	// Set correct permissions
	if err := exec.Command("sudo", "chmod", "0600", filePath).Run(); err != nil {
		return fmt.Errorf("failed to set file permissions: %v", err)
	}
	
	return nil
}

// SetRoutes sets routes for an interface (replaces existing routes)
func (rh *RouteHelper) SetRoutes(ifname string, routes []*client.Route) error {
	tableID := InterfaceTableID(ifname)
	if tableID == 0 {
		return fmt.Errorf("interface table ID not found for %s", ifname)
	}
	
	if rh.config.Network.Ethernets == nil {
		rh.config.Network.Ethernets = make(map[string]RouteEthernetConfig)
	}
	
	ethernetConfig := rh.config.Network.Ethernets[ifname]
	ethernetConfig.Routes = nil // Clear existing routes
	
	for _, route := range routes {
		routeEntry := RouteFileEntry{
			Via:   route.Via,
			Table: tableID,
		}
		
		// Handle default route vs specific destination
		if route.IsDefault {
			routeEntry.To = "0.0.0.0/0"
			routeEntry.IsDefault = true
			routeEntry.Table = 0 // Use main table for default routes
		} else {
			routeEntry.To = route.To
			if route.Table != nil {
				routeEntry.Table = int(*route.Table)
			}
		}
		
		if route.Scope != "" {
			routeEntry.Scope = route.Scope
		}
		if route.Source != "" {
			routeEntry.Source = route.Source
		}
		if route.Metric != nil {
			metric := int(*route.Metric)
			routeEntry.Metric = &metric
		}
		ethernetConfig.Routes = append(ethernetConfig.Routes, routeEntry)
	}
	
	rh.config.Network.Ethernets[ifname] = ethernetConfig
	return nil
}

// AddRoute adds a single route for an interface (checks for duplicates)
func (rh *RouteHelper) AddRoute(ifname string, route *client.Route) error {
	tableID := InterfaceTableID(ifname)
	if tableID == 0 {
		return fmt.Errorf("interface table ID not found for %s", ifname)
	}
	
	if rh.config.Network.Ethernets == nil {
		rh.config.Network.Ethernets = make(map[string]RouteEthernetConfig)
	}
	
	ethernetConfig := rh.config.Network.Ethernets[ifname]
	
	routeEntry := RouteFileEntry{
		Via:   route.Via,
		Table: tableID,
	}
	
	// Handle default route vs specific destination
	if route.IsDefault {
		routeEntry.To = "0.0.0.0/0"
		routeEntry.IsDefault = true
		routeEntry.Table = 0 // Use main table for default routes
	} else {
		routeEntry.To = route.To
		if route.Table != nil {
			routeEntry.Table = int(*route.Table)
		}
	}
	
	// Check for duplicate routes
	for _, existingRoute := range ethernetConfig.Routes {
		if existingRoute.To == routeEntry.To && existingRoute.Via == routeEntry.Via {
			// Route already exists, skip
			return nil
		}
	}
	
	if route.Scope != "" {
		routeEntry.Scope = route.Scope
	}
	if route.Source != "" {
		routeEntry.Source = route.Source
	}
	if route.Metric != nil {
		metric := int(*route.Metric)
		routeEntry.Metric = &metric
	}
	
	ethernetConfig.Routes = append(ethernetConfig.Routes, routeEntry)
	rh.config.Network.Ethernets[ifname] = ethernetConfig
	return nil
}

// RemoveRoute removes a specific route for an interface
func (rh *RouteHelper) RemoveRoute(ifname string, route *client.Route) error {
	if rh.config.Network.Ethernets == nil {
		return nil
	}
	
	ethernetConfig, exists := rh.config.Network.Ethernets[ifname]
	if !exists {
		return nil
	}
	
	var filteredRoutes []RouteFileEntry
	targetTo := route.To
	if route.IsDefault {
		targetTo = "0.0.0.0/0"
	}
	
	// Determine target table ID
	var targetTable int
	if route.Table != nil {
		targetTable = int(*route.Table)
	} else {
		targetTable = InterfaceTableID(ifname)
		if targetTable == 0 {
			// If no interface table ID, use default table
			targetTable = 0
		}
	}
	
	for _, existingRoute := range ethernetConfig.Routes {
		// Keep routes that don't match the one we want to remove
		shouldKeep := true
		
		// Check all route fields for exact match
		if existingRoute.To == targetTo && existingRoute.Via == route.Via {
			// Check table
			if existingRoute.Table == targetTable {
				// Check scope if specified
				if route.Scope != "" && existingRoute.Scope != route.Scope {
					shouldKeep = true
				} else if route.Scope == "" || existingRoute.Scope == route.Scope {
					// Check metric if specified
					if route.Metric != nil {
						metric := int(*route.Metric)
						if existingRoute.Metric != nil && *existingRoute.Metric != metric {
							shouldKeep = true
						} else if existingRoute.Metric == nil && metric != 0 {
							shouldKeep = true
						} else {
							// Check source if specified
							if route.Source != "" && existingRoute.Source != route.Source {
								shouldKeep = true
							} else {
								// All fields match, remove this route
								shouldKeep = false
							}
						}
					} else {
						// No metric specified, check source
						if route.Source != "" && existingRoute.Source != route.Source {
							shouldKeep = true
						} else {
							// All fields match, remove this route
							shouldKeep = false
						}
					}
				}
			}
		}
		
		if shouldKeep {
			filteredRoutes = append(filteredRoutes, existingRoute)
		}
	}
	
	ethernetConfig.Routes = filteredRoutes
	rh.config.Network.Ethernets[ifname] = ethernetConfig
	return nil
}

// SetRoutingPolicies sets routing policies for an interface (replaces existing policies)
func (rh *RouteHelper) SetRoutingPolicies(ifname string, policies []*client.RoutingPolicy) error {
	tableID := InterfaceTableID(ifname)
	if tableID == 0 {
		return fmt.Errorf("interface table ID not found for %s", ifname)
	}
	
	if rh.config.Network.Ethernets == nil {
		rh.config.Network.Ethernets = make(map[string]RouteEthernetConfig)
	}
	
	ethernetConfig := rh.config.Network.Ethernets[ifname]
	ethernetConfig.RoutingPolicy = nil // Clear existing policies
	
	for _, policy := range policies {
		policyEntry := RoutingPolicyFileEntry{
			From:  policy.From,
			To:    policy.To,
			Table: tableID,
		}
		if policy.Table != 0 {
			policyEntry.Table = int(policy.Table)
		}
		ethernetConfig.RoutingPolicy = append(ethernetConfig.RoutingPolicy, policyEntry)
	}
	
	rh.config.Network.Ethernets[ifname] = ethernetConfig
	return nil
}

// AddRoutingPolicy adds a single routing policy for an interface (checks for duplicates)
func (rh *RouteHelper) AddRoutingPolicy(ifname string, policy *client.RoutingPolicy) error {
	tableID := InterfaceTableID(ifname)
	if tableID == 0 {
		return fmt.Errorf("interface table ID not found for %s", ifname)
	}
	
	if rh.config.Network.Ethernets == nil {
		rh.config.Network.Ethernets = make(map[string]RouteEthernetConfig)
	}
	
	ethernetConfig := rh.config.Network.Ethernets[ifname]
	
	// Check for duplicate policies
	for _, existingPolicy := range ethernetConfig.RoutingPolicy {
		if existingPolicy.From == policy.From && existingPolicy.To == policy.To {
			// Policy already exists, skip
			return nil
		}
	}
	
	policyEntry := RoutingPolicyFileEntry{
		From:  policy.From,
		To:    policy.To,
		Table: tableID,
	}
	if policy.Table != 0 {
		policyEntry.Table = int(policy.Table)
	}
	
	ethernetConfig.RoutingPolicy = append(ethernetConfig.RoutingPolicy, policyEntry)
	rh.config.Network.Ethernets[ifname] = ethernetConfig
	return nil
}

// RemoveRoutingPolicy removes a specific routing policy for an interface
func (rh *RouteHelper) RemoveRoutingPolicy(ifname string, policy *client.RoutingPolicy) error {
	if rh.config.Network.Ethernets == nil {
		return nil
	}
	
	ethernetConfig, exists := rh.config.Network.Ethernets[ifname]
	if !exists {
		return nil
	}
	
	// Determine target table ID
	var targetTable int
	if policy.Table != 0 {
		targetTable = int(policy.Table)
	} else {
		targetTable = InterfaceTableID(ifname)
		if targetTable == 0 {
			// If no interface table ID, use default table
			targetTable = 0
		}
	}
	
	var filteredPolicies []RoutingPolicyFileEntry
	for _, existingPolicy := range ethernetConfig.RoutingPolicy {
		// Keep policies that don't match the one we want to remove
		shouldKeep := true
		
		// Check all policy fields for exact match
		if existingPolicy.From == policy.From && existingPolicy.To == policy.To {
			// Check table
			if existingPolicy.Table == targetTable {
				// All fields match, remove this policy
				shouldKeep = false
			}
		}
		
		if shouldKeep {
			filteredPolicies = append(filteredPolicies, existingPolicy)
		}
	}
	
	ethernetConfig.RoutingPolicy = filteredPolicies
	rh.config.Network.Ethernets[ifname] = ethernetConfig
	return nil
}

// GetRouteFilePath returns the path for route configuration file
func GetRouteFilePath(ifname, netplanDir string) string {
	return fmt.Sprintf("%s/50-elchi-r-%s.yaml", netplanDir, ifname)
}

// GetPolicyFilePath returns the path for routing policy configuration file
func GetPolicyFilePath(ifname, netplanDir string) string {
	return fmt.Sprintf("%s/50-elchi-p-%s.yaml", netplanDir, ifname)
}

// WriteRouteFile writes route configuration to file using helper
func WriteRouteFile(ifname, netplanDir string, routes []*client.Route) error {
	if len(routes) == 0 {
		// Remove file if no routes
		return RemoveNetplanRouteFile(ifname, netplanDir)
	}
	
	helper := NewRouteHelper()
	filePath := GetRouteFilePath(ifname, netplanDir)
	
	// Load existing config
	if err := helper.LoadFromFile(filePath); err != nil {
		return err
	}
	
	// Set routes
	if err := helper.SetRoutes(ifname, routes); err != nil {
		return err
	}
	
	// Save to file
	return helper.SaveToFile(filePath)
}

// WritePolicyFile writes routing policy configuration to file using helper
func WritePolicyFile(ifname, netplanDir string, policies []*client.RoutingPolicy) error {
	if len(policies) == 0 {
		// Remove file if no policies
		return RemoveNetplanPolicyFile(ifname, netplanDir)
	}
	
	helper := NewRouteHelper()
	filePath := GetPolicyFilePath(ifname, netplanDir)
	
	// Load existing config
	if err := helper.LoadFromFile(filePath); err != nil {
		return err
	}
	
	// Set policies
	if err := helper.SetRoutingPolicies(ifname, policies); err != nil {
		return err
	}
	
	// Save to file
	return helper.SaveToFile(filePath)
} 