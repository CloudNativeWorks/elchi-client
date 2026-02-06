package services

import (
	"context"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/network"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) NetworkService(_ context.Context, cmd *client.Command) *client.CommandResponse {
	switch cmd.SubType {
	// Netplan operations
	case client.SubCommandType_SUB_NETPLAN_APPLY:
		if s.grpcClient != nil {
			return network.NetplanApply(cmd, s.logger, s.grpcClient.GetConnection(), s.grpcClient.GetClientID())
		}
		return network.NetplanApply(cmd, s.logger, nil, "")
	case client.SubCommandType_SUB_NETPLAN_GET:
		return network.NetplanGet(cmd, s.logger)
	case client.SubCommandType_SUB_NETPLAN_ROLLBACK:
		return network.NetplanRollback(cmd, s.logger)

	// Route management
	case client.SubCommandType_SUB_ROUTE_MANAGE:
		return network.RouteManage(cmd, s.logger)
	case client.SubCommandType_SUB_ROUTE_LIST:
		return network.RouteList(cmd, s.logger)

	// Policy management
	case client.SubCommandType_SUB_POLICY_MANAGE:
		return network.PolicyManage(cmd, s.logger)
	case client.SubCommandType_SUB_POLICY_LIST:
		return network.PolicyList(cmd, s.logger)

	// Network state
	case client.SubCommandType_SUB_GET_NETWORK_STATE:
		return network.GetNetworkState(cmd, s.logger)

	// Table management
	case client.SubCommandType_SUB_TABLE_MANAGE:
		return network.TableManage(cmd, s.logger)
	case client.SubCommandType_SUB_TABLE_LIST:
		return network.TableList(cmd, s.logger)

		// Legacy support is removed - old subcommands no longer available
		// The system now uses the new netplan-based approach
	}

	return helper.NewErrorResponse(cmd, "invalid sub command")
}
