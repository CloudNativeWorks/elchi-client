package services

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr/bgp"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-proto/client"
)

// Handle FRR protocol requests
func (s *Services) FrrService(cmd *client.Command) *client.CommandResponse {
	s.logger.Info("Starting FRR service request processing")
	defer func() {
		s.logger.Info("Completed FRR service request processing")
	}()

	frrReq := cmd.GetFrr()
	if frrReq == nil {
		s.logger.Error("FRR request is nil - command structure may be incorrect")
		s.logger.Debug(fmt.Sprintf("Command type: %s, available oneofs: deploy=%v, service=%v, frr=%v",
			cmd.Type.String(), cmd.GetDeploy() != nil, cmd.GetService() != nil, cmd.GetFrr() != nil))
		return helper.NewErrorResponse(cmd, "FRR request is nil")
	}

	s.logger.Debug(fmt.Sprintf("Full FRR request: %+v", frrReq))
	s.logger.Debug(fmt.Sprintf("BGP request: %+v", frrReq.Bgp))
	s.logger.Info(fmt.Sprintf("Processing FRR request for protocol: %s", frrReq.Protocol.String()))

	var response *client.CommandResponse
	switch frrReq.Protocol {
	case client.FrrProtocolType_FRR_PROTOCOL_BGP:
		if frrReq.Bgp == nil {
			s.logger.Error("BGP request is nil in FRR request")
			s.logger.Debug(fmt.Sprintf("FRR request content: protocol=%v, bgp=%+v",
				frrReq.Protocol, frrReq.Bgp))
			return helper.NewErrorResponse(cmd, "BGP request is nil in FRR request")
		}
		response = s.handleBgpProtocol(cmd, frrReq.Bgp)
		if response == nil {
			s.logger.Error("BGP protocol handler returned nil response")
			return helper.NewErrorResponse(cmd, "Internal error: nil response from BGP protocol handler")
		}
		s.logger.Info(fmt.Sprintf("BGP protocol handler returned response with success=%v", response.Success))
	default:
		response = helper.NewErrorResponse(cmd, fmt.Sprintf("Unsupported FRR protocol: %s", frrReq.Protocol.String()))
		s.logger.Error(fmt.Sprintf("Unsupported protocol: %s", frrReq.Protocol.String()))
	}

	// Ensure response has proper identity and command ID
	if response != nil {
		response.Identity = cmd.Identity
		response.CommandId = cmd.CommandId
	}

	// Log response details
	if response != nil && response.GetFrr() != nil && response.GetFrr().GetBgp() != nil {
		s.logger.Info(fmt.Sprintf("FRR service request completed with success=%v, message=%v",
			response.Success, response.GetFrr().GetBgp().GetMessage()))
	} else {
		s.logger.Info(fmt.Sprintf("FRR service request completed with success=%v", response.Success))
	}

	return response
}

// handleBgpProtocol handles BGP protocol operations using the new manager-based approach
func (s *Services) handleBgpProtocol(cmd *client.Command, bgpReq *client.RequestBgp) *client.CommandResponse {
	if bgpReq == nil {
		s.logger.Error("BGP request is nil")
		return helper.NewErrorResponse(cmd, "BGP request is nil")
	}

	s.logger.Info(fmt.Sprintf("Processing BGP operation: %s", bgpReq.Operation.String()))

	bgpManager := bgp.NewManager(s.vtysh, s.logger)

	var response *client.CommandResponse
	switch bgpReq.Operation {
	case client.BgpOperationType_BGP_GET_CONFIG: // ok
		response = s.GetBgpConfig(cmd, bgpManager)
	case client.BgpOperationType_BGP_SET_CONFIG: // ok
		response = s.SetBgpConfig(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_ADD_NEIGHBOR, client.BgpOperationType_BGP_UPDATE_NEIGHBOR: // ok
		response = s.HandleBgpNeighbor(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_LIST_NEIGHBORS: // ok
		response = s.ListBgpNeighbors(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_GET_NEIGHBOR: // ok
		response = s.GetBgpNeighbor(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_REMOVE_NEIGHBOR:
		response = s.RemoveBgpNeighbor(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_GET_POLICY_CONFIG:
		response = s.GetBgpPolicyConfig(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_APPLY_PREFIX_LIST:
		response = s.ApplyPrefixList(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_REMOVE_PREFIX_LIST:
		response = s.RemovePrefixList(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_APPLY_COMMUNITY_LIST:
		response = s.ApplyCommunityList(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_REMOVE_COMMUNITY_LIST:
		response = s.RemoveCommunityList(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_APPLY_ROUTE_MAP:
		response = s.ApplyRouteMap(cmd, bgpReq, bgpManager)
	case client.BgpOperationType_BGP_REMOVE_ROUTE_MAP:
		response = s.RemoveRouteMap(cmd, bgpReq, bgpManager)

	case client.BgpOperationType_BGP_SHOW_ROUTES:
		response = s.ShowBgpRoutes(cmd, bgpManager)

	case client.BgpOperationType_BGP_GET_SUMMARY:
		response = s.GetBgpSummary(cmd, bgpManager)

	case client.BgpOperationType_BGP_CLEAR_ROUTES:
		response = s.ClearBgpRoutes(cmd, bgpReq, bgpManager)

	default:
		response = helper.NewErrorResponse(cmd, fmt.Sprintf("Unsupported BGP operation: %s", bgpReq.Operation.String()))
	}

	return response
}
