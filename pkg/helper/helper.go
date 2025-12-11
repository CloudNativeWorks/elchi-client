package helper

import (
	"github.com/CloudNativeWorks/elchi-proto/client"
)

func NewErrorResponse(cmd *client.Command, errMsg string) *client.CommandResponse {
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   false,
		Error:     errMsg,
	}
}
