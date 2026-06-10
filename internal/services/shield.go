package services

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/shield"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// shieldServiceName is the systemd unit name of the local elchi-shield sidecar.
const shieldServiceName = "elchi-shield"

// ShieldService handles SHIELD commands: deliver shield's watched config bundle,
// read what's on disk, or report shield's service status. elchi-shield self-watches
// its config dir and hot-reloads, so UPDATE only writes files — it never restarts
// shield.
func (s *Services) ShieldService(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	switch cmd.SubType {
	case client.SubCommandType_UPDATE_SHIELD_CONFIG:
		return s.updateShieldConfig(ctx, cmd)
	case client.SubCommandType_GET_SHIELD_CONFIG:
		return s.getShieldConfig(cmd)
	case client.SubCommandType_GET_SHIELD_STATUS:
		return s.getShieldStatus(ctx, cmd)
	default:
		return helper.NewErrorResponse(cmd, "invalid sub command")
	}
}

func (s *Services) updateShieldConfig(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	req := cmd.GetShield()
	if req == nil || req.GetConfig() == nil {
		return helper.NewErrorResponse(cmd, "shield config is required")
	}
	cfg := req.GetConfig()

	if err := shield.SyncConfig(ctx, cfg, s.logger); err != nil {
		s.logger.Errorf("shield config sync failed: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("shield config sync failed: %v", err))
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Shield{
			Shield: &client.ResponseShield{
				Success:        true,
				Message:        fmt.Sprintf("shield config applied (%d files)", len(cfg.GetFiles())),
				AppliedVersion: cfg.GetVersion(),
				// shield self-watches the dir and hot-reloads (atomic, last-good on
				// failure); the agent only lands the files, so reload_ok reflects a
				// successful write, not a confirmed reload.
				ReloadOk: true,
			},
		},
	}
}

func (s *Services) getShieldConfig(cmd *client.Command) *client.CommandResponse {
	files, err := shield.ListConfig(s.logger)
	if err != nil {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("list shield config failed: %v", err))
	}
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Shield{
			Shield: &client.ResponseShield{
				Success:      true,
				Message:      fmt.Sprintf("%d config files", len(files)),
				CurrentFiles: files,
			},
		},
	}
}

func (s *Services) getShieldStatus(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	status, err := systemd.GetServiceStatus(ctx, shieldServiceName, s.logger, s.runner)
	statusMsg := "unknown"
	if err != nil {
		s.logger.Debugf("failed to get shield service status: %v", err)
	} else if status != nil {
		statusMsg = status.Active
	}
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Shield{
			Shield: &client.ResponseShield{
				Success:       true,
				ServiceStatus: statusMsg,
			},
		},
	}
}
