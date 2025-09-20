package services

import (
	"github.com/CloudNativeWorks/elchi-client/internal/operations/journal"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) GeneralLog(cmd *client.Command) *client.CommandResponse {
	var service string
	generalLogReq := cmd.GetGeneralLog()
	if generalLogReq == nil {
		return helper.NewErrorResponse(cmd, "general log request is nil")
	}

	switch cmd.Type {
	case client.CommandType_CLIENT_LOGS:
		service = "elchi-client"
	case client.CommandType_FRR_LOGS:
		service = "frr"
	default:
		return helper.NewErrorResponse(cmd, "invalid command type")
	}

	var logs []*client.GeneralLogs
	var err error

	switch service {
	case "elchi-client":
		logs, err = journal.GetLastNGeneralLogs(service, generalLogReq.Count)
	case "frr":
		logs, err = journal.GetLastNGeneralLogsFromSystemd(service, generalLogReq.Count)
	default:
		return helper.NewErrorResponse(cmd, "unsupported service type")
	}

	if err != nil {
		s.logger.Errorf("failed to get logs for service %s: %v", service, err)
		return helper.NewErrorResponse(cmd, err.Error())
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_GeneralLog{
			GeneralLog: &client.ResponseGeneralLog{
				Logs: logs,
			},
		},
	}
}
