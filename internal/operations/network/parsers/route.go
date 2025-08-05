package parsers

import (
	"os"

	"github.com/CloudNativeWorks/elchi-proto/client"
	"gopkg.in/yaml.v3"
)

// Parses route configuration from a map
func parseRouteConfig(ifaceMap map[string]any) *client.Route {
	if routes, ok := ifaceMap["routes"].([]any); ok && len(routes) > 0 {
		if rMap, ok := routes[0].(map[string]any); ok {
			route := &client.Route{}
			if to, ok := rMap["to"].(string); ok {
				route.To = to
			}
			if via, ok := rMap["via"].(string); ok {
				route.Via = via
			}
			if table, ok := rMap["table"].(float64); ok {
				t := uint32(table)
				route.Table = &t
			}
			if metric, ok := rMap["metric"].(float64); ok {
				m := uint32(metric)
				route.Metric = &m
			}
			return route
		}
	}
	return nil
}

// Parses routing policy from a map
func parseRoutingPolicy(ifaceMap map[string]any) *client.RoutingPolicy {
	if policies, ok := ifaceMap["routing-policy"].([]any); ok && len(policies) > 0 {
		if pMap, ok := policies[0].(map[string]any); ok {
			policy := &client.RoutingPolicy{}
			if from, ok := pMap["from"].(string); ok {
				policy.From = from
			}
			if table, ok := pMap["table"].(uint32); ok {
				policy.Table = table
			}
			return policy
		}
	}
	return nil
}

// Parses a netplan route file and returns a *client.Interfaces
func ParseNetplanRouteFile(path string) ([]*client.Interfaces, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	var result []*client.Interfaces
	if network, ok := parsed["network"].(map[string]any); ok {
		if ethernets, ok := network["ethernets"].(map[string]any); ok {
			for ifname, ifaceData := range ethernets {
				iface := &client.Interfaces{Ifname: ifname}
				if ifaceMap, ok := ifaceData.(map[string]any); ok {
					iface.Routes = append(iface.Routes, parseRouteConfig(ifaceMap))
					iface.RoutingPolicies = append(iface.RoutingPolicies, parseRoutingPolicy(ifaceMap))
				}
				result = append(result, iface)
			}
		}
	}
	return result, nil
}
