package services

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/upgrade"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) UpgradeListenerService(cmd *client.Command) *client.CommandResponse {
	upgradeReq := cmd.GetUpgradeListener()
	if upgradeReq == nil {
		s.logger.Errorf("upgrade listener request is nil")
		return helper.NewErrorResponse(cmd, "upgrade listener request is nil")
	}

	s.logger.Infof("Upgrading listener: %s from version %s to %s on port %d",
		upgradeReq.GetName(),
		upgradeReq.GetFromVersion(),
		upgradeReq.GetToVersion(),
		upgradeReq.GetPort())

	serviceName := fmt.Sprintf("%s-%d", upgradeReq.GetName(), upgradeReq.GetPort())

	// Perform upgrade operation
	result, err := upgrade.UpgradeListener(
		serviceName,
		upgradeReq.GetFromVersion(),
		upgradeReq.GetToVersion(),
		upgradeReq.GetGraceful(),
		s.logger,
		s.runner,
	)
	if err != nil {
		s.logger.Errorf("Failed to upgrade listener: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to upgrade listener: %v", err))
	}

	s.logger.Infof("Successfully upgraded listener %s from %s to %s",
		upgradeReq.GetName(),
		upgradeReq.GetFromVersion(),
		upgradeReq.GetToVersion())

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_UpgradeListener{
			UpgradeListener: &client.ResponseUpgradeListener{
				Name:                  upgradeReq.GetName(),
				FromVersion:           upgradeReq.GetFromVersion(),
				ToVersion:             upgradeReq.GetToVersion(),
				Port:                  upgradeReq.GetPort(),
				Graceful:              upgradeReq.GetGraceful(),
				SystemdServiceUpdated: fmt.Sprintf("updated and reloaded: %s", result.SystemdServiceUpdated),
				EnvoyRestarted:        result.RestartStatus,
			},
		},
	}
}
