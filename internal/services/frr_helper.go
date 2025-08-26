package services

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr/bgp"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) GetBgpConfig(cmd *client.Command, manager bgp.BGPManagerInterface) *client.CommandResponse {
	s.logger.Info("Getting BGP configuration")

	config, err := manager.GetConfig()
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to get BGP config: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to get BGP config: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_GET_CONFIG,
		Config:    config,
		Message:   "BGP configuration retrieved successfully",
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) SetBgpConfig(cmd *client.Command, bgpReq *client.RequestBgp, bgpManager bgp.BGPManagerInterface) *client.CommandResponse {
	s.logger.Info("Setting BGP configuration")

	if bgpReq.GetConfig() == nil {
		s.logger.Error("BGP configuration is nil")
		return helper.NewErrorResponse(cmd, "BGP configuration is nil")
	}

	// Log configuration details
	s.logger.Info(fmt.Sprintf("Applying BGP configuration: AS=%d, RouterID=%s",
		bgpReq.GetConfig().AutonomousSystem, bgpReq.GetConfig().RouterId))

	// Apply configuration
	if err := bgpManager.SetConfig(bgpReq.GetConfig()); err != nil {
		s.logger.Error(fmt.Sprintf("Failed to set BGP configuration: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to set BGP configuration: %v", err))
	}

	// Create success response
	response := &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp: &client.ResponseBgp{
					Operation: client.BgpOperationType_BGP_SET_CONFIG,
					Message:   "BGP configuration applied successfully",
					Success:   true,
					Config:    bgpReq.GetConfig(),
				},
			},
		},
	}

	s.logger.Info("BGP configuration set successfully")
	return response
}

func (s *Services) HandleBgpNeighbor(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	neighbor := bgpReq.GetNeighbor()

	if neighbor == nil {
		return helper.NewErrorResponse(cmd, "Neighbor configuration is required")
	}

	if neighbor.PeerIp == "" || bgpReq.AsNumber == 0 {
		return helper.NewErrorResponse(cmd, "Peer IP and AS number are required")
	}

	_, err := manager.GetNeighborManager().GetNeighborByIP(neighbor.PeerIp)
	if err == nil {
		// Neighbor exists, update it
		s.logger.Info(fmt.Sprintf("Current BGP neighbor updated: %s", neighbor.PeerIp))
		err = manager.GetNeighborManager().UpdateNeighbor(bgpReq.AsNumber, neighbor)
	} else {
		// Neighbor doesn't exist, add it
		s.logger.Info(fmt.Sprintf("New BGP neighbor added: %s", neighbor.PeerIp))
		err = manager.GetNeighborManager().AddNeighbor(bgpReq.AsNumber, neighbor)
	}

	if err != nil {
		s.logger.Error(fmt.Sprintf("BGP neighbor operation failed: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("BGP neighbor operation failed: %v", err))
	}

	// Get updated neighbor details
	updatedNeighbor, err := manager.GetNeighborManager().ParseNeighborDetails(neighbor.PeerIp, bgpReq.AsNumber)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Neighbor details not found: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Neighbor details not found: %v", err))
	}

	response := &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp: &client.ResponseBgp{
					Operation: bgpReq.Operation,
					Success:   true,
					Message:   fmt.Sprintf("BGP neighbor operation successful: %s", neighbor.PeerIp),
					Neighbor:  updatedNeighbor,
				},
			},
		},
	}

	return response
}

func (s *Services) ListBgpNeighbors(cmd *client.Command, _ *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	s.logger.Info("Listing all BGP neighbors")

	neighbors, err := manager.GetStateManager().ParseBgpNeighbors()
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to list BGP neighbors: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to list BGP neighbors: %v", err))
	}

	result := &client.ResponseBgp{
		Operation:     client.BgpOperationType_BGP_LIST_NEIGHBORS,
		Message:       "BGP neighbors listed successfully",
		ShowNeighbors: neighbors,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) GetBgpNeighbor(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.PeerIp == "" || bgpReq.AsNumber == 0 {
		return helper.NewErrorResponse(cmd, "Peer IP and AS number are required")
	}

	// Get neighbor details
	neighbor, err := manager.GetNeighborManager().ParseNeighborDetails(bgpReq.PeerIp, bgpReq.AsNumber)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to get neighbor details: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to get neighbor details: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_GET_NEIGHBOR,
		Success:   true,
		Message:   fmt.Sprintf("BGP neighbor details retrieved successfully: %s", bgpReq.PeerIp),
		Neighbor:  neighbor,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) GetBgpPolicyConfig(cmd *client.Command, _ *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	policyConfig, err := manager.GetPolicyManager().GetPolicyConfig()
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to get BGP policy config: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to get BGP policy config: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_GET_POLICY_CONFIG,
		Message:   "BGP policy configuration retrieved successfully",
		PolicyConfig: &client.BgpPolicyConfig{
			RouteMaps:      policyConfig.RouteMaps,
			CommunityLists: policyConfig.CommunityLists,
			PrefixLists:    policyConfig.PrefixLists,
		},
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) RemoveBgpNeighbor(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.GetNeighbor() == nil {
		s.logger.Error("BGP neighbor is nil")
		return helper.NewErrorResponse(cmd, "BGP neighbor configuration is required")
	}

	// Get current config to get AS number
	currentConfig, err := manager.GetConfig()
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to get current BGP config: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to get current BGP config: %v", err))
	}

	// Remove neighbor
	if err := manager.RemoveNeighbor(currentConfig.AutonomousSystem, bgpReq.GetNeighbor().PeerIp); err != nil {
		s.logger.Error(fmt.Sprintf("Failed to remove BGP neighbor: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to remove BGP neighbor: %v", err))
	}

	// Create success response
	response := &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp: &client.ResponseBgp{
					Operation: client.BgpOperationType_BGP_REMOVE_NEIGHBOR,
					Message:   fmt.Sprintf("BGP neighbor %s removed successfully", bgpReq.GetNeighbor().PeerIp),
					Success:   true,
				},
			},
		},
	}

	s.logger.Info(fmt.Sprintf("BGP neighbor %s removed successfully", bgpReq.GetNeighbor().PeerIp))
	return response
}

func (s *Services) ApplyPrefixList(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.PrefixList == nil {
		return helper.NewErrorResponse(cmd, "Prefix list configuration is required")
	}

	if bgpReq.PrefixList.Name == "" || bgpReq.PrefixList.Prefix == "" {
		return helper.NewErrorResponse(cmd, "Prefix list name and prefix are required")
	}

	// Apply prefix list using policy manager (idempotent operation)
	err := manager.GetPolicyManager().ApplyPrefixList(bgpReq.PrefixList)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to apply prefix list: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to apply prefix list: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_APPLY_PREFIX_LIST,
		Message:   fmt.Sprintf("Prefix list '%s' applied successfully", bgpReq.PrefixList.Name),
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) RemovePrefixList(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.PrefixList == nil {
		return helper.NewErrorResponse(cmd, "Prefix list name is required")
	}

	s.logger.Info(fmt.Sprintf("Removing BGP prefix list: %s", bgpReq.PrefixList.Name))

	err := manager.GetPolicyManager().RemovePrefixList(bgpReq.PrefixList.Name)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to remove prefix list: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to remove prefix list: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_REMOVE_PREFIX_LIST,
		Message:   fmt.Sprintf("Prefix list '%s' removed successfully", bgpReq.PrefixList.Name),
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) ApplyRouteMap(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.RouteMap == nil {
		return helper.NewErrorResponse(cmd, "Route map is required")
	}

	s.logger.Info(fmt.Sprintf("Applying BGP route map: %s", bgpReq.RouteMap.Name))

	err := manager.GetPolicyManager().ApplyRouteMap(bgpReq.RouteMap)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to apply route map: %v", err))
		result := &client.ResponseBgp{
			Operation:        client.BgpOperationType_BGP_APPLY_ROUTE_MAP,
			Message:          fmt.Sprintf("Route map '%s' application failed", bgpReq.RouteMap.Name),
			ValidationErrors: []string{err.Error()},
		}
		return &client.CommandResponse{
			Identity:  cmd.Identity,
			CommandId: cmd.CommandId,
			Success:   false,
			Result: &client.CommandResponse_Frr{
				Frr: &client.ResponseFrr{
					Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
					Success:  false,
					Bgp:      result,
				},
			},
		}
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_APPLY_ROUTE_MAP,
		Message:   fmt.Sprintf("Route map '%s' applied successfully", bgpReq.RouteMap.Name),
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) RemoveRouteMap(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.RouteMap == nil {
		return helper.NewErrorResponse(cmd, "Route map name is required")
	}

	s.logger.Info(fmt.Sprintf("Removing BGP route map: %s", bgpReq.RouteMap.Name))

	err := manager.GetPolicyManager().RemoveRouteMap(bgpReq.RouteMap.Name)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to remove route map: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to remove route map: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_REMOVE_ROUTE_MAP,
		Message:   fmt.Sprintf("Route map '%s' removed successfully", bgpReq.RouteMap.Name),
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) ApplyCommunityList(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.CommunityList == nil {
		return helper.NewErrorResponse(cmd, "Community list is required")
	}

	s.logger.Info(fmt.Sprintf("Applying BGP community list: %s", bgpReq.CommunityList.Name))

	// Use PolicyManager to apply community list
	err := manager.GetPolicyManager().ApplyCommunityList(bgpReq.CommunityList)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to apply community list: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to apply community list: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_APPLY_COMMUNITY_LIST,
		Message:   fmt.Sprintf("Community list '%s' applied successfully", bgpReq.CommunityList.Name),
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) RemoveCommunityList(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	if bgpReq.CommunityList == nil {
		return helper.NewErrorResponse(cmd, "Community list name is required")
	}

	s.logger.Info(fmt.Sprintf("Removing BGP community list: %s", bgpReq.CommunityList.Name))

	err := manager.GetPolicyManager().RemoveCommunityList(bgpReq.CommunityList.Name)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to remove community list: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to remove community list: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_REMOVE_COMMUNITY_LIST,
		Message:   fmt.Sprintf("Community list '%s' removed successfully", bgpReq.CommunityList.Name),
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) ShowBgpRoutes(cmd *client.Command, manager bgp.BGPManagerInterface) *client.CommandResponse {
	s.logger.Info("Getting BGP routes via JSON")

	routes, err := manager.GetBgpRoutes()
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to get BGP routes: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to get BGP routes: %v", err))
	}

	// Calculate total routes for message
	totalReceivedRoutes := routes.GetReceived().GetTotalRoutes()
	totalAdvertisedRoutes := uint32(0)
	for _, advertised := range routes.GetAdvertised() {
		totalAdvertisedRoutes += advertised.GetTotalPrefixCount()
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_SHOW_ROUTES,
		Message:   fmt.Sprintf("BGP routes retrieved successfully - Received: %d, Advertised: %d", totalReceivedRoutes, totalAdvertisedRoutes),
		Routes:    routes,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

func (s *Services) GetBgpSummary(cmd *client.Command, manager bgp.BGPManagerInterface) *client.CommandResponse {
	s.logger.Info("Getting BGP summary with new Ipv4UnicastSummary structure")

	summary, err := manager.GetStateManager().GetBgpState()
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to get BGP summary: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to get BGP summary: %v", err))
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_GET_SUMMARY,
		Success:   true,
		Summary:   summary,
		Message:   fmt.Sprintf("BGP IPv4 unicast summary retrieved with %d peers", len(summary.Peers)),
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}

// ClearBgpRoutes clears BGP routes based on clear parameters
func (s *Services) ClearBgpRoutes(cmd *client.Command, bgpReq *client.RequestBgp, manager bgp.BGPManagerInterface) *client.CommandResponse {
	s.logger.Info("Clearing BGP routes")

	// Get clear parameters from request
	clearBgp := bgpReq.GetClear()
	if clearBgp == nil {
		s.logger.Error("Clear BGP parameters are required")
		return helper.NewErrorResponse(cmd, "Clear BGP parameters are required")
	}

	// Clear BGP routes using state manager
	err := manager.GetStateManager().ClearBgpRoutes(clearBgp)
	if err != nil {
		s.logger.Error(fmt.Sprintf("Failed to clear BGP routes: %v", err))
		return helper.NewErrorResponse(cmd, fmt.Sprintf("Failed to clear BGP routes: %v", err))
	}

	// Build success message
	var message string
	if clearBgp.Neighbor == "*" || clearBgp.Neighbor == "" {
		message = "BGP routes cleared for all neighbors"
	} else {
		message = fmt.Sprintf("BGP routes cleared for neighbor %s", clearBgp.Neighbor)
	}

	if clearBgp.Soft {
		message += " (soft reset)"
	}

	if clearBgp.Direction != "" && clearBgp.Direction != "all" {
		message += fmt.Sprintf(" (%s direction)", clearBgp.Direction)
	}

	result := &client.ResponseBgp{
		Operation: client.BgpOperationType_BGP_CLEAR_ROUTES,
		Success:   true,
		Message:   message,
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Frr{
			Frr: &client.ResponseFrr{
				Protocol: client.FrrProtocolType_FRR_PROTOCOL_BGP,
				Success:  true,
				Bgp:      result,
			},
		},
	}
}
