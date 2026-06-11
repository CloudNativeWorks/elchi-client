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

// shieldLogLines is how many recent journald lines GET_SHIELD_STATUS returns.
const shieldLogLines uint32 = 100

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

	// Snapshot shield's active state BEFORE the push so the reload can be confirmed
	// (shield's /configz version is a content hash that moves iff the config does).
	before := shield.SnapshotState(ctx)

	changed, err := shield.SyncConfig(ctx, cfg, s.logger)
	if err != nil {
		s.logger.Errorf("shield config sync failed: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("shield config sync failed: %v", err))
	}

	// Confirm shield actually LOADED the new config rather than rejecting it and
	// keeping last-good. applied_version / reload_ok now reflect shield's real
	// state instead of optimistically echoing the pushed bundle. When nothing on
	// disk changed (idempotent re-push, e.g. on reconnect) there is nothing for
	// shield to reload — skip the confirmation wait and answer from the pre-push
	// snapshot, instead of burning the full poll timeout per push.
	var applied string
	var reloadOk bool
	if !changed && before.Reachable && !before.Empty {
		applied, reloadOk = before.Version, true
	} else {
		applied, reloadOk = shield.ConfirmReload(ctx, before, s.logger)
	}
	msg := fmt.Sprintf("shield config applied (%d files)", len(cfg.GetFiles()))
	errMsg := ""
	if !reloadOk {
		msg = fmt.Sprintf("shield config written (%d files) but reload not confirmed", len(cfg.GetFiles()))
		errMsg = "shield did not confirm the new config loaded (rejected and kept last-good, or shield unreachable)"
	}
	if applied == "" {
		applied = cfg.GetVersion() // fall back to the bundle version when shield's active version is unknown
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Shield{
			Shield: &client.ResponseShield{
				Success:        true,
				Message:        msg,
				Error:          errMsg,
				AppliedVersion: applied,
				ReloadOk:       reloadOk,
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

	// Best-effort recent logs from journald, mirroring the filebeat/rsyslog status
	// responses. A read failure (e.g. shield never ran) degrades to no logs, not an
	// error — the status itself is still useful.
	logs, lerr := s.getServiceLogs(shieldServiceName, shieldLogLines)
	if lerr != nil {
		s.logger.Debugf("failed to get shield logs: %v", lerr)
		logs = nil
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Shield{
			Shield: &client.ResponseShield{
				Success:       true,
				ServiceStatus: statusMsg,
				Logs:          logs,
			},
		},
	}
}
