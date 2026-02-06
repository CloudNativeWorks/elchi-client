package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/rsyslog"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) RsyslogService(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	switch cmd.SubType {
	case client.SubCommandType_UPDATE_RSYSLOG_CONFIG:
		return s.UpdateRsyslogConfig(ctx, cmd)
	case client.SubCommandType_GET_RSYSLOG_CONFIG:
		return s.GetRsyslogConfig(ctx, cmd)
	case client.SubCommandType_GET_RSYSLOG_STATUS:
		return s.GetRsyslogStatus(ctx, cmd)
	case client.SubCommandType_SUB_START:
		return s.RsyslogServiceAction(ctx, cmd, "start")
	case client.SubCommandType_SUB_STOP:
		return s.RsyslogServiceAction(ctx, cmd, "stop")
	case client.SubCommandType_SUB_RESTART:
		return s.RsyslogServiceAction(ctx, cmd, "restart")
	case client.SubCommandType_SUB_LOGS:
		return s.RsyslogServiceLogs(cmd)
	}

	return helper.NewErrorResponse(cmd, "invalid sub command")
}

func (s *Services) UpdateRsyslogConfig(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	rsyslogReq := cmd.GetRsyslog()
	if rsyslogReq == nil {
		return helper.NewErrorResponse(cmd, "rsyslog request is nil")
	}

	// Update configuration
	if err := rsyslog.UpdateConfig(ctx, rsyslogReq, s.logger, s.runner); err != nil {
		s.logger.Errorf("failed to update rsyslog config: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to update config: %v", err))
	}

	// Restart service to apply changes
	if err := rsyslog.RestartService(ctx, s.logger, s.runner); err != nil {
		s.logger.Errorf("failed to restart rsyslog: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("config updated but failed to restart service: %v", err))
	}

	// Get current status
	status, err := rsyslog.GetServiceStatus(ctx, s.logger, s.runner)
	if err != nil {
		s.logger.Debugf("failed to get rsyslog service status: %v", err)
	}
	statusMsg := "unknown"
	if status != nil {
		statusMsg = status.Active
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Rsyslog{
			Rsyslog: &client.ResponseRsyslog{
				Success:       true,
				Message:       "Rsyslog configuration updated and service restarted",
				ServiceStatus: statusMsg,
				CurrentConfig: rsyslogReq.RsyslogConfig,
			},
		},
	}
}

func (s *Services) GetRsyslogConfig(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	config, err := rsyslog.GetCurrentConfig(s.logger)
	if err != nil {
		s.logger.Errorf("failed to get rsyslog config: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get config: %v", err))
	}

	status, statusErr := rsyslog.GetServiceStatus(ctx, s.logger, s.runner)
	if statusErr != nil {
		s.logger.Debugf("failed to get rsyslog service status: %v", statusErr)
	}
	statusMsg := "unknown"
	if status != nil {
		statusMsg = status.Active
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Rsyslog{
			Rsyslog: &client.ResponseRsyslog{
				Success:       true,
				Message:       "Rsyslog configuration retrieved",
				CurrentConfig: config.RsyslogConfig,
				ServiceStatus: statusMsg,
			},
		},
	}
}

func (s *Services) GetRsyslogStatus(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	status, err := rsyslog.GetServiceStatus(ctx, s.logger, s.runner)
	if err != nil {
		s.logger.Errorf("failed to get rsyslog status: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get status: %v", err))
	}

	// Format status message
	statusMsg := "unknown"
	fullStatus := "unknown"
	if status != nil {
		fullStatus = status.Active
		// Extract just the status word (e.g., "active" from "active (running) since...")
		if parts := strings.Fields(status.Active); len(parts) > 0 {
			statusMsg = parts[0] // "active", "inactive", "failed"
		}
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Rsyslog{
			Rsyslog: &client.ResponseRsyslog{
				Success:       true,
				Message:       fmt.Sprintf("Rsyslog service status: %s", fullStatus),
				ServiceStatus: statusMsg,
			},
		},
	}
}

func (s *Services) RsyslogServiceAction(ctx context.Context, cmd *client.Command, action string) *client.CommandResponse {
	var subType client.SubCommandType

	switch action {
	case "start":
		subType = client.SubCommandType_SUB_START
	case "stop":
		subType = client.SubCommandType_SUB_STOP
	case "restart":
		subType = client.SubCommandType_SUB_RESTART
	default:
		return helper.NewErrorResponse(cmd, fmt.Sprintf("unknown action: %s", action))
	}

	// Use systemd.ServiceControl for consistency
	status, err := rsyslog.ServiceControl(ctx, "rsyslog", subType, s.logger, s.runner)
	if err != nil {
		s.logger.Errorf("failed to %s rsyslog: %v", action, err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to %s rsyslog: %v", action, err))
	}

	statusMsg := "unknown"
	if status != nil {
		statusMsg = status.Active
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Rsyslog{
			Rsyslog: &client.ResponseRsyslog{
				Success:       true,
				Message:       fmt.Sprintf("Rsyslog service %sed successfully", action),
				ServiceStatus: statusMsg,
			},
		},
	}
}

func (s *Services) RsyslogServiceLogs(cmd *client.Command) *client.CommandResponse {
	rsyslogReq := cmd.GetRsyslog()
	count := uint32(50)
	if rsyslogReq != nil {
		// You can extend RequestRsyslog to include count if needed
		// For now using default
	}

	// Use existing journal package to get logs
	logs, err := s.getServiceLogs("rsyslog", count)
	if err != nil {
		s.logger.Errorf("failed to get rsyslog logs: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get logs: %v", err))
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Rsyslog{
			Rsyslog: &client.ResponseRsyslog{
				Success: true,
				Message: "Rsyslog logs retrieved",
				Logs:    logs,
			},
		},
	}
}
