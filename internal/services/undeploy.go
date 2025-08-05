package services

import (
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/files"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/network"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) UndeployService(cmd *client.Command) *client.CommandResponse {
	undeployReq := cmd.GetUndeploy()
	if undeployReq == nil {
		s.logger.Errorf("undeploy payload is nil")
		return helper.NewErrorResponse(cmd, "undeploy payload is nil")
	}

	s.logger.WithFields(logger.Fields{
		"service_name": undeployReq.GetName(),
		"port":         undeployReq.GetPort(),
	}).Debug("Undeploying service")

	serviceName := fmt.Sprintf("%s-%d.service", undeployReq.GetName(), undeployReq.GetPort())
	ifaceName := fmt.Sprintf("elchi-if-%d", undeployReq.GetPort())

	s.logger.WithFields(logger.Fields{
		"service_name": serviceName,
	}).Debug("Stopping and disabling service")

	if err := s.runner.RunWithS("systemctl", "stop", serviceName); err != nil {
		s.logger.Warnf("Failed to stop service %s: %v", serviceName, err)
	}

	if err := s.runner.RunWithS("systemctl", "disable", serviceName); err != nil {
		s.logger.Warnf("Failed to disable service %s: %v", serviceName, err)
	}

	s.logger.WithFields(logger.Fields{
		"interface_name": ifaceName,
	}).Debug("Removing network interface")

	if err := network.DeleteDummyInterface(ifaceName, s.logger); err != nil {
		s.logger.Errorf("Failed to delete interface %s: %v", ifaceName, err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to delete interface: %v", err))
	}

	filesResult := files.DeleteServiceFiles(undeployReq.GetName(), undeployReq.GetPort(), ifaceName, s.logger)

	if err := s.runner.RunWithS("systemctl", "daemon-reload"); err != nil {
		s.logger.Warnf("Failed to reload systemd daemon: %v", err)
	}

	fileList := strings.Join(filesResult.DeletedFiles, ", ")
	if fileList == "" {
		fileList = "No files were deleted"
	}

	s.logger.WithFields(logger.Fields{
		"service_name":  undeployReq.GetName(),
		"port":          undeployReq.GetPort(),
		"deleted_files": fileList,
	}).Debug("Successfully undeployed service")

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Undeploy{
			Undeploy: &client.ResponseUnDeploy{
				Files:             fileList,
				Service:           serviceName,
				Network:           ifaceName,
				DownstreamAddress: undeployReq.GetDownstreamAddress(),
			},
		},
	}
}
