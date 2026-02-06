package services

import (
	"context"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/client"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	proto "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) ClientStats(_ context.Context, cmd *proto.Command) *proto.CommandResponse {
	response, err := client.CollectSystemStats()
	if err != nil {
		s.logger.Errorf("Failed to collect system stats: %v", err)
		return helper.NewErrorResponse(cmd, err.Error())
	}

	return &proto.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &proto.CommandResponse_ClientStats{
			ClientStats: response,
		},
	}
}
