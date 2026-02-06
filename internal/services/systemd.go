package services

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/journal"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) SystemdService(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	switch cmd.SubType {
	case client.SubCommandType_SUB_LOGS:
		return s.SystemdServiceLogs(cmd)
	case client.SubCommandType_SUB_RELOAD, client.SubCommandType_SUB_START, client.SubCommandType_SUB_STOP, client.SubCommandType_SUB_RESTART, client.SubCommandType_SUB_STATUS:
		return s.SystemdServiceAction(ctx, cmd)
	}

	return helper.NewErrorResponse(cmd, "invalid sub command")
}

func (s *Services) SystemdServiceAction(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	serviceReq := cmd.GetService()
	if serviceReq == nil {
		return helper.NewErrorResponse(cmd, "service request is nil")
	}

	identifier := fmt.Sprintf("%s-%d", serviceReq.GetName(), serviceReq.GetPort())
	action := cmd.GetSubType()

	status, err := systemd.ServiceControl(ctx, identifier, action, s.logger, s.runner)
	if err != nil {
		return helper.NewErrorResponse(cmd, err.Error())
	}

	//logs, err := journal.GetLastNLogs("service-"+identifier, 20)
	logs := []*client.Logs{
		{
			Message: fmt.Sprintf("service %s %s", identifier, action),
		},
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Service{
			Service: &client.ResponseService{
				Name:   serviceReq.GetName(),
				Status: status,
				Logs:   logs,
			},
		},
	}
}

func (s *Services) SystemdServiceLogs(cmd *client.Command) *client.CommandResponse {
	serviceReq := cmd.GetService()
	if serviceReq == nil {
		return helper.NewErrorResponse(cmd, "service request is nil")
	}

	identifier := fmt.Sprintf("%s-%d", serviceReq.GetName(), serviceReq.GetPort())

	logs, err := journal.GetLastNLogs(identifier, serviceReq, s.logger)
	if err != nil {
		s.logger.Errorf("failed to get logs: %v", err)
		return helper.NewErrorResponse(cmd, err.Error())
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Service{
			Service: &client.ResponseService{
				Name: serviceReq.GetName(),
				Logs: logs,
			},
		},
	}
}
