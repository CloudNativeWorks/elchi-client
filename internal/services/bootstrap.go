package services

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/files"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) UpdateBootstrapService(cmd *client.Command) *client.CommandResponse {
	bootstrapReq := cmd.GetUpdateBootstrap()
	if bootstrapReq == nil {
		return helper.NewErrorResponse(cmd, "bootstrap request is nil")
	}

	fileName := fmt.Sprintf("%s-%d", bootstrapReq.GetName(), bootstrapReq.GetPort())

	_, err := files.WriteBootstrapFile(fileName, bootstrapReq.GetBootstrap())
	if err != nil {
		return helper.NewErrorResponse(cmd, err.Error())
	}

	_, err = systemd.ServiceControl(fileName, client.SubCommandType_SUB_RELOAD, s.logger, s.runner)
	if err != nil {
		return helper.NewErrorResponse(cmd, err.Error())
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_UpdateBootstrap{
			UpdateBootstrap: &client.ResponseUpdateBootstrap{
				Name: fileName,
			},
		},
	}
}
