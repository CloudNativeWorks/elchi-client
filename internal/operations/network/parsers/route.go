package parsers

import (
	"os"

	"github.com/CloudNativeWorks/elchi-proto/client"
	"gopkg.in/yaml.v3"
)

// RouteInfo holds parsed route information
type RouteInfo struct {
	Ifname          string
	Routes          []*client.Route
	RoutingPolicies []*client.RoutingPolicy
}

// Parses route configuration from a map
func parseRoutes(ifaceMap map[string]any) []*client.Route {
	var routes []*client.Route
	if routeList, ok := ifaceMap["routes"].([]any); ok {
		for _, routeData := range routeList {
			if rMap, ok := routeData.(map[string]any); ok {
				route := &client.Route{}
				if to, ok := rMap["to"].(string); ok {
					route.To = to
				}
				if via, ok := rMap["via"].(string); ok {
					route.Via = via
				}
				if table, ok := rMap["table"].(float64); ok {
					route.Table = uint32(table)
				}
				if metric, ok := rMap["metric"].(float64); ok {
					route.Metric = uint32(metric)
				}
				if scope, ok := rMap["scope"].(string); ok {
					route.Scope = scope
				}
				routes = append(routes, route)
			}
		}
	}
	return routes
}

// Parses routing policies from a map
func parseRoutingPolicies(ifaceMap map[string]any) []*client.RoutingPolicy {
	var policies []*client.RoutingPolicy
	if policyList, ok := ifaceMap["routing-policy"].([]any); ok {
		for _, policyData := range policyList {
			if pMap, ok := policyData.(map[string]any); ok {
				policy := &client.RoutingPolicy{}
				if from, ok := pMap["from"].(string); ok {
					policy.From = from
				}
				if to, ok := pMap["to"].(string); ok {
					policy.To = to
				}
				if table, ok := pMap["table"].(float64); ok {
					policy.Table = uint32(table)
				}
				if priority, ok := pMap["priority"].(float64); ok {
					policy.Priority = uint32(priority)
				}
				policies = append(policies, policy)
			}
		}
	}
	return policies
}

// Parses a netplan route file and returns route information
func ParseNetplanRouteFile(path string) ([]*RouteInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	var result []*RouteInfo
	if network, ok := parsed["network"].(map[string]any); ok {
		if ethernets, ok := network["ethernets"].(map[string]any); ok {
			for ifname, ifaceData := range ethernets {
				if ifaceMap, ok := ifaceData.(map[string]any); ok {
					info := &RouteInfo{
						Ifname:          ifname,
						Routes:          parseRoutes(ifaceMap),
						RoutingPolicies: parseRoutingPolicies(ifaceMap),
					}
					result = append(result, info)
				}
			}
		}
	}
	return result, nil
}
