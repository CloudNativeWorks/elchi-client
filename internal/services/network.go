package services

import (
	"github.com/CloudNativeWorks/elchi-client/internal/operations/network"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) NetworkService(cmd *client.Command) *client.CommandResponse {
	switch cmd.SubType {
	// Interface management
	case client.SubCommandType_SUB_GET_IF_CONFIG:
		return network.NetworkServiceGetIfConfig(cmd, s.logger)
	case client.SubCommandType_SUB_SET_IF_CONFIG:
		return network.UpdateInterface(cmd, s.logger)
	case client.SubCommandType_SUB_ADD_ROUTE:
		return network.AddInterfaceRoute(cmd, s.logger)
	case client.SubCommandType_SUB_ADD_ROUTING_POLICY:
		return network.AddInterfaceRoutingPolicy(cmd, s.logger)
	case client.SubCommandType_SUB_REMOVE_ROUTE:
		return network.RemoveInterfaceRoute(cmd, s.logger)
	case client.SubCommandType_SUB_REMOVE_ROUTING_POLICY:
		return network.RemoveInterfaceRoutingPolicy(cmd, s.logger)
	}

	return helper.NewErrorResponse(cmd, "invalid sub command")
}
