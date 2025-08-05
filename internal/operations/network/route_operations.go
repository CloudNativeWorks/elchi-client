package network

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
)

// AddInterfaceRoute adds a route to an interface (runtime + persistent)
func AddInterfaceRoute(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if len(networkReq.Interfaces) == 0 || networkReq.Interfaces[0] == nil {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	iface := networkReq.Interfaces[0]
	if iface.Ifname == "" {
		return helper.NewErrorResponse(cmd, "ifname is required")
	}

	if len(iface.Routes) == 0 {
		return helper.NewErrorResponse(cmd, "no routes to add")
	}

	link, err := netlink.LinkByName(iface.Ifname)
	if err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("interface '%s' not found", iface.Ifname))
	}

	netplanDir := models.NetplanPath
	
	// Apply routes to runtime
	for _, route := range iface.Routes {
		if err := addInterfaceRouteIdempotent(link, route, iface.Ifname); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
	}

	// Load existing route config
	routeHelper := NewRouteHelper()
	filePath := GetRouteFilePath(iface.Ifname, netplanDir)
	
	if err := routeHelper.LoadFromFile(filePath); err != nil {
		return helper.NewErrorResponse(cmd, err.Error())
	}

	// Add routes to config
	for _, route := range iface.Routes {
		if err := routeHelper.AddRoute(iface.Ifname, route); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
	}

	// Save updated config
	if err := routeHelper.SaveToFile(filePath); err != nil {
		return helper.NewErrorResponse(cmd, err.Error())
	}

	cmd.SubType = *client.SubCommandType_SUB_GET_IF_CONFIG.Enum()
	return NetworkServiceGetIfConfig(cmd, logger)
}

// RemoveInterfaceRoute removes a route from an interface (runtime + persistent)
func RemoveInterfaceRoute(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if len(networkReq.Interfaces) == 0 {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	netplanDir := models.NetplanPath
	
	// Process each interface separately
	for _, iface := range networkReq.Interfaces {
		if iface == nil {
			continue
		}
		
		if iface.Ifname == "" {
			return helper.NewErrorResponse(cmd, "ifname is required")
		}

		if len(iface.Routes) == 0 {
			continue // Skip interfaces with no routes to remove
		}

		link, err := netlink.LinkByName(iface.Ifname)
		if err != nil {
			return helper.NewErrorResponse(cmd, fmt.Sprintf("interface '%s' not found", iface.Ifname))
		}
		
		// Remove routes from runtime (idempotent)
		for i, route := range iface.Routes {
			logger.Info(fmt.Sprintf("Removing route %d from %s: to=%s, via=%s, table=%v", i, iface.Ifname, route.To, route.Via, route.Table))
			if err := removeInterfaceRouteIdempotent(link, route, iface.Ifname, logger); err != nil {
				// Log but don't fail if route doesn't exist
				logger.Warn(fmt.Sprintf("Failed to remove route %s via %s on %s: %v", route.To, route.Via, iface.Ifname, err))
			}
		}

		// Load existing route config for this interface
		routeHelper := NewRouteHelper()
		filePath := GetRouteFilePath(iface.Ifname, netplanDir)
		
		if err := routeHelper.LoadFromFile(filePath); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}

		// Remove routes from config - remove all specified routes in one pass
		ethernetConfig, exists := routeHelper.config.Network.Ethernets[iface.Ifname]
		if exists {
			var filteredRoutes []RouteFileEntry
			for _, existingRoute := range ethernetConfig.Routes {
				shouldKeep := true
				
				// Check if this existing route matches any of the routes to remove
				for _, routeToRemove := range iface.Routes {
					targetTo := routeToRemove.To
					if routeToRemove.IsDefault {
						targetTo = "0.0.0.0/0"
					}
					
					// Determine target table ID
					var targetTable int
					if routeToRemove.Table != nil {
						targetTable = int(*routeToRemove.Table)
					} else {
						targetTable = InterfaceTableID(iface.Ifname)
						if targetTable == 0 {
							targetTable = 0
						}
					}
					
					// Check if all route fields match
					if existingRoute.To == targetTo && existingRoute.Via == routeToRemove.Via && existingRoute.Table == targetTable {
						// Additional field checks for exact match
						scopeMatch := (routeToRemove.Scope == "" || existingRoute.Scope == routeToRemove.Scope)
						sourceMatch := (routeToRemove.Source == "" || existingRoute.Source == routeToRemove.Source)
						
						metricMatch := true
						if routeToRemove.Metric != nil {
							metric := int(*routeToRemove.Metric)
							metricMatch = (existingRoute.Metric != nil && *existingRoute.Metric == metric) || 
										  (existingRoute.Metric == nil && metric == 0)
						}
						
						if scopeMatch && sourceMatch && metricMatch {
							shouldKeep = false
							break
						}
					}
				}
				
				if shouldKeep {
					filteredRoutes = append(filteredRoutes, existingRoute)
				}
			}
			
			ethernetConfig.Routes = filteredRoutes
			routeHelper.config.Network.Ethernets[iface.Ifname] = ethernetConfig
		}

		// Save updated config or remove file if empty
		if len(routeHelper.config.Network.Ethernets[iface.Ifname].Routes) == 0 {
			if err := RemoveNetplanRouteFile(iface.Ifname, netplanDir); err != nil {
				return helper.NewErrorResponse(cmd, err.Error())
			}
		} else {
			if err := routeHelper.SaveToFile(filePath); err != nil {
				return helper.NewErrorResponse(cmd, err.Error())
			}
		}
	}

	cmd.SubType = *client.SubCommandType_SUB_GET_IF_CONFIG.Enum()
	return NetworkServiceGetIfConfig(cmd, logger)
}

// AddInterfaceRoutingPolicy adds a routing policy to an interface (runtime + persistent)
func AddInterfaceRoutingPolicy(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if len(networkReq.Interfaces) == 0 || networkReq.Interfaces[0] == nil {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	iface := networkReq.Interfaces[0]
	if iface.Ifname == "" {
		return helper.NewErrorResponse(cmd, "ifname is required")
	}

	if len(iface.RoutingPolicies) == 0 {
		return helper.NewErrorResponse(cmd, "no routing policies to add")
	}

	netplanDir := models.NetplanPath
	
	// Apply policies to runtime
	for _, policy := range iface.RoutingPolicies {
		if err := addInterfacePolicyIdempotent(iface.Ifname, policy); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
	}

	// Load existing policy config
	policyHelper := NewRouteHelper()
	filePath := GetPolicyFilePath(iface.Ifname, netplanDir)
	
	if err := policyHelper.LoadFromFile(filePath); err != nil {
		return helper.NewErrorResponse(cmd, err.Error())
	}

	// Add policies to config
	for _, policy := range iface.RoutingPolicies {
		if err := policyHelper.AddRoutingPolicy(iface.Ifname, policy); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}
	}

	// Save updated config
	if err := policyHelper.SaveToFile(filePath); err != nil {
		return helper.NewErrorResponse(cmd, err.Error())
	}

	cmd.SubType = *client.SubCommandType_SUB_GET_IF_CONFIG.Enum()
	return NetworkServiceGetIfConfig(cmd, logger)
}

// RemoveInterfaceRoutingPolicy removes a routing policy from an interface (runtime + persistent)
func RemoveInterfaceRoutingPolicy(cmd *client.Command, logger *logger.Logger) *client.CommandResponse {
	networkReq := cmd.GetNetwork()
	if len(networkReq.Interfaces) == 0 {
		return helper.NewErrorResponse(cmd, "network request is nil")
	}

	netplanDir := models.NetplanPath
	
	// Process each interface separately
	for _, iface := range networkReq.Interfaces {
		if iface == nil {
			continue
		}
		
		if iface.Ifname == "" {
			return helper.NewErrorResponse(cmd, "ifname is required")
		}

		if len(iface.RoutingPolicies) == 0 {
			continue // Skip interfaces with no policies to remove
		}

		// Remove policies from runtime (idempotent)
		for i, policy := range iface.RoutingPolicies {
			logger.Info(fmt.Sprintf("Removing policy %d from %s: from=%s, to=%s, table=%d", i, iface.Ifname, policy.From, policy.To, policy.Table))
			if err := removeInterfacePolicyIdempotent(iface.Ifname, policy, logger); err != nil {
				// Log but don't fail if policy doesn't exist
				logger.Warn(fmt.Sprintf("Failed to remove policy from=%s to=%s on %s: %v", policy.From, policy.To, iface.Ifname, err))
			}
		}

		// Load existing policy config for this interface
		policyHelper := NewRouteHelper()
		filePath := GetPolicyFilePath(iface.Ifname, netplanDir)
		
		if err := policyHelper.LoadFromFile(filePath); err != nil {
			return helper.NewErrorResponse(cmd, err.Error())
		}

		// Remove policies from config - remove all specified policies in one pass
		ethernetConfig, exists := policyHelper.config.Network.Ethernets[iface.Ifname]
		if exists {
			var filteredPolicies []RoutingPolicyFileEntry
			for _, existingPolicy := range ethernetConfig.RoutingPolicy {
				shouldKeep := true
				
				// Check if this existing policy matches any of the policies to remove
				for _, policyToRemove := range iface.RoutingPolicies {
					// Determine target table ID for the policy to remove
					var targetTable int
					if policyToRemove.Table != 0 {
						targetTable = int(policyToRemove.Table)
					} else {
						targetTable = InterfaceTableID(iface.Ifname)
						if targetTable == 0 {
							targetTable = 0
						}
					}
					
					// Check if all fields match
					if existingPolicy.From == policyToRemove.From && 
					   existingPolicy.To == policyToRemove.To && 
					   existingPolicy.Table == targetTable {
						shouldKeep = false
						break
					}
				}
				
				if shouldKeep {
					filteredPolicies = append(filteredPolicies, existingPolicy)
				}
			}
			
			ethernetConfig.RoutingPolicy = filteredPolicies
			policyHelper.config.Network.Ethernets[iface.Ifname] = ethernetConfig
		}

		// Save updated config or remove file if empty
		if len(policyHelper.config.Network.Ethernets[iface.Ifname].RoutingPolicy) == 0 {
			if err := RemoveNetplanPolicyFile(iface.Ifname, netplanDir); err != nil {
				return helper.NewErrorResponse(cmd, err.Error())
			}
		} else {
			if err := policyHelper.SaveToFile(filePath); err != nil {
				return helper.NewErrorResponse(cmd, err.Error())
			}
		}
	}

	cmd.SubType = *client.SubCommandType_SUB_GET_IF_CONFIG.Enum()
	return NetworkServiceGetIfConfig(cmd, logger)
} 