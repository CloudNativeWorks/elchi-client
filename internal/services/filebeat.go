package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/filebeat"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/journal"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) FilebeatService(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	switch cmd.SubType {
	case client.SubCommandType_UPDATE_FILEBEAT_CONFIG:
		return s.UpdateFilebeatConfig(ctx, cmd)
	case client.SubCommandType_GET_FILEBEAT_CONFIG:
		return s.GetFilebeatConfig(ctx, cmd)
	case client.SubCommandType_GET_FILEBEAT_STATUS:
		return s.GetFilebeatStatus(ctx, cmd)
	case client.SubCommandType_SUB_START:
		return s.FilebeatServiceAction(ctx, cmd, "start")
	case client.SubCommandType_SUB_STOP:
		return s.FilebeatServiceAction(ctx, cmd, "stop")
	case client.SubCommandType_SUB_RESTART:
		return s.FilebeatServiceAction(ctx, cmd, "restart")
	case client.SubCommandType_SUB_LOGS:
		return s.FilebeatServiceLogs(cmd)
	}

	return helper.NewErrorResponse(cmd, "invalid sub command")
}

func (s *Services) UpdateFilebeatConfig(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	filebeatReq := cmd.GetFilebeat()
	if filebeatReq == nil {
		return helper.NewErrorResponse(cmd, "filebeat request is nil")
	}

	// Update configuration
	if err := filebeat.UpdateConfig(ctx, filebeatReq, s.logger, s.runner); err != nil {
		s.logger.Errorf("failed to update filebeat config: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to update config: %v", err))
	}

	// Restart service to apply changes
	if err := filebeat.RestartService(ctx, s.logger, s.runner); err != nil {
		s.logger.Errorf("failed to restart filebeat: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("config updated but failed to restart service: %v", err))
	}

	// Get current status
	status, err := filebeat.GetServiceStatus(ctx, s.logger, s.runner)
	if err != nil {
		s.logger.Debugf("failed to get filebeat service status: %v", err)
	}
	statusMsg := "unknown"
	if status != nil {
		statusMsg = status.Active
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Filebeat{
			Filebeat: &client.ResponseFilebeat{
				Success:       true,
				Message:       "Filebeat configuration updated and service restarted",
				ServiceStatus: statusMsg,
				CurrentConfig: filebeatReq,
			},
		},
	}
}

func (s *Services) GetFilebeatConfig(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	config, err := filebeat.GetCurrentConfig(s.logger)
	if err != nil {
		s.logger.Errorf("failed to get filebeat config: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get config: %v", err))
	}

	status, statusErr := filebeat.GetServiceStatus(ctx, s.logger, s.runner)
	if statusErr != nil {
		s.logger.Debugf("failed to get filebeat service status: %v", statusErr)
	}
	statusMsg := "unknown"
	if status != nil {
		statusMsg = status.Active
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Filebeat{
			Filebeat: &client.ResponseFilebeat{
				Success:       true,
				Message:       "Filebeat configuration retrieved",
				CurrentConfig: config,
				ServiceStatus: statusMsg,
			},
		},
	}
}

func (s *Services) GetFilebeatStatus(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	status, err := filebeat.GetServiceStatus(ctx, s.logger, s.runner)
	if err != nil {
		s.logger.Errorf("failed to get filebeat status: %v", err)
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
		Result: &client.CommandResponse_Filebeat{
			Filebeat: &client.ResponseFilebeat{
				Success:       true,
				Message:       fmt.Sprintf("Filebeat service status: %s", fullStatus),
				ServiceStatus: statusMsg,
			},
		},
	}
}

func (s *Services) FilebeatServiceAction(ctx context.Context, cmd *client.Command, action string) *client.CommandResponse {
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
	status, err := filebeat.ServiceControl(ctx, "filebeat", subType, s.logger, s.runner)
	if err != nil {
		s.logger.Errorf("failed to %s filebeat: %v", action, err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to %s filebeat: %v", action, err))
	}

	statusMsg := "unknown"
	if status != nil {
		statusMsg = status.Active
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Filebeat{
			Filebeat: &client.ResponseFilebeat{
				Success:       true,
				Message:       fmt.Sprintf("Filebeat service %sed successfully", action),
				ServiceStatus: statusMsg,
			},
		},
	}
}

func (s *Services) FilebeatServiceLogs(cmd *client.Command) *client.CommandResponse {
	filebeatReq := cmd.GetFilebeat()
	count := uint32(50)
	if filebeatReq != nil {
		// You can extend RequestFilebeat to include count if needed
		// For now using default
	}

	// Use existing journal package to get logs
	logs, err := s.getServiceLogs("filebeat", count)
	if err != nil {
		s.logger.Errorf("failed to get filebeat logs: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to get logs: %v", err))
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Filebeat{
			Filebeat: &client.ResponseFilebeat{
				Success: true,
				Message: "Filebeat logs retrieved",
				Logs:    logs,
			},
		},
	}
}

// Helper to get service logs (using systemd journal library like FRR logs)
func (s *Services) getServiceLogs(serviceName string, count uint32) ([]*client.Logs, error) {
	// Use the same journal reading approach as FRR logs (no sudo required)
	generalLogs, err := journal.GetLastNGeneralLogsFromSystemd(serviceName, count)
	if err != nil {
		return nil, fmt.Errorf("failed to get logs from systemd journal: %w", err)
	}

	// Convert GeneralLogs to Logs format
	logs := make([]*client.Logs, 0, len(generalLogs))
	for _, gl := range generalLogs {
		msg := fmt.Sprintf("[%s] [%s] %s", gl.Timestamp, gl.Level, gl.Message)
		logs = append(logs, &client.Logs{
			Timestamp: gl.Timestamp,
			Message:   msg,
			Level:     gl.Level,
		})
	}

	return logs, nil
}
