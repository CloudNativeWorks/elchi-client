package network

import (
	"fmt"
	"os"

	"github.com/CloudNativeWorks/elchi-proto/client"
	"gopkg.in/yaml.v3"
)

// RouteFileConfig represents the structure for route/policy netplan files
type RouteFileConfig struct {
	Network RouteNetworkConfig `yaml:"network"`
}

type RouteNetworkConfig struct {
	Version   int                              `yaml:"version"`
	Ethernets map[string]RouteEthernetConfig `yaml:"ethernets"`
}

type RouteEthernetConfig struct {
	Routes        []RouteFileEntry             `yaml:"routes,omitempty"`
	RoutingPolicy []RoutingPolicyFileEntry     `yaml:"routing-policy,omitempty"`
}

type RouteFileEntry struct {
	To     string `yaml:"to"`
	Via    string `yaml:"via,omitempty"`
	Scope  string `yaml:"scope,omitempty"`
	Table  int    `yaml:"table,omitempty"`
	Metric *int   `yaml:"metric,omitempty"`
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
				Ethernets: make(map[string]RouteEthernetConfig),
			},
		},
	}
}

// LoadFromFile loads configuration from a netplan YAML file
func (rh *RouteHelper) LoadFromFile(filePath string) error {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// File doesn't exist, start with empty config
		return nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %v", err)
	}

	if err := yaml.Unmarshal(data, rh.config); err != nil {
		return fmt.Errorf("failed to parse YAML: %v", err)
	}

	return nil
}

// SaveToFile saves configuration to a netplan YAML file
func (rh *RouteHelper) SaveToFile(filePath string) error {
	data, err := yaml.Marshal(rh.config)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %v", err)
	}

	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write file: %v", err)
	}

	return nil
}

// AddRoute adds a route to the interface configuration
func (rh *RouteHelper) AddRoute(ifname string, route *client.Route) error {
	ethernetConfig := rh.config.Network.Ethernets[ifname]
	
	// Convert client.Route to RouteFileEntry
	routeEntry := RouteFileEntry{
		To:  route.To,
		Via: route.Via,
	}

	if route.Scope != "" {
		routeEntry.Scope = route.Scope
	}

	if route.Table != 0 {
		routeEntry.Table = int(route.Table)
	}

	if route.Metric != 0 {
		metric := int(route.Metric)
		routeEntry.Metric = &metric
	}

	ethernetConfig.Routes = append(ethernetConfig.Routes, routeEntry)
	rh.config.Network.Ethernets[ifname] = ethernetConfig

	return nil
}

// AddRoutingPolicy adds a routing policy to the interface configuration
func (rh *RouteHelper) AddRoutingPolicy(ifname string, policy *client.RoutingPolicy) error {
	ethernetConfig := rh.config.Network.Ethernets[ifname]
	
	// Convert client.RoutingPolicy to RoutingPolicyFileEntry
	policyEntry := RoutingPolicyFileEntry{
		From:  policy.From,
		To:    policy.To,
		Table: int(policy.Table),
	}

	ethernetConfig.RoutingPolicy = append(ethernetConfig.RoutingPolicy, policyEntry)
	rh.config.Network.Ethernets[ifname] = ethernetConfig

	return nil
}

// File path utilities for netplan management

// GetRouteFilePath returns the netplan file path for route configuration
func GetRouteFilePath(ifname, netplanDir string) string {
	return fmt.Sprintf("%s/50-elchi-r-%s.yaml", netplanDir, ifname)
}

// GetPolicyFilePath returns the netplan file path for policy configuration
func GetPolicyFilePath(ifname, netplanDir string) string {
	return fmt.Sprintf("%s/50-elchi-p-%s.yaml", netplanDir, ifname)
}

// WriteRouteFile writes routes to a netplan file (simplified)
func WriteRouteFile(ifname, netplanDir string, routes []*client.Route) error {
	helper := NewRouteHelper()
	
	for _, route := range routes {
		if err := helper.AddRoute(ifname, route); err != nil {
			return err
		}
	}
	
	filePath := GetRouteFilePath(ifname, netplanDir)
	return helper.SaveToFile(filePath)
}

// WritePolicyFile writes policies to a netplan file (simplified)
func WritePolicyFile(ifname, netplanDir string, policies []*client.RoutingPolicy) error {
	helper := NewRouteHelper()
	
	for _, policy := range policies {
		if err := helper.AddRoutingPolicy(ifname, policy); err != nil {
			return err
		}
	}
	
	filePath := GetPolicyFilePath(ifname, netplanDir)
	return helper.SaveToFile(filePath)
}