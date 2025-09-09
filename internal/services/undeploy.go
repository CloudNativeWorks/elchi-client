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

	// Acquire deployment lock to prevent race conditions
	deploymentLock.Lock()
	defer deploymentLock.Unlock()

	s.logger.WithFields(logger.Fields{
		"service_name": undeployReq.GetName(),
		"port":         undeployReq.GetPort(),
	}).Debug("Undeploying service")

	serviceName := fmt.Sprintf("%s-%d.service", undeployReq.GetName(), undeployReq.GetPort())
	ifaceName := fmt.Sprintf("elchi-if-%d", undeployReq.GetPort())

	// Check if service exists before trying to stop/disable
	serviceExists := false
	if output, _ := s.runner.RunWithOutputS("systemctl", "list-units", "--all", serviceName); strings.Contains(string(output), serviceName) {
		serviceExists = true
	}

	if serviceExists {
		s.logger.WithFields(logger.Fields{
			"service_name": serviceName,
		}).Debug("Stopping and disabling service")

		if err := s.runner.RunWithS("systemctl", "stop", serviceName); err != nil {
			s.logger.Warnf("Failed to stop service %s: %v", serviceName, err)
		}

		if err := s.runner.RunWithS("systemctl", "disable", serviceName); err != nil {
			s.logger.Warnf("Failed to disable service %s: %v", serviceName, err)
		}
	} else {
		s.logger.Infof("Service %s does not exist, skipping stop/disable", serviceName)
	}

	// Step 1: Remove runtime network interface first (netlink)
	s.logger.WithFields(logger.Fields{
		"interface_name": ifaceName,
	}).Debug("Removing network interface from runtime")

	if err := network.DeleteDummyInterface(ifaceName, s.logger); err != nil {
		s.logger.Errorf("Failed to delete interface %s: %v", ifaceName, err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to delete interface: %v", err))
	}

	// Step 2: Remove persistent configuration files (including netplan)
	s.logger.Debug("Removing configuration files")
	filesResult := files.DeleteServiceFiles(undeployReq.GetName(), undeployReq.GetPort(), ifaceName, s.logger)

	// Only reload systemd if service files were deleted or service existed
	if serviceExists || len(filesResult.DeletedFiles) > 0 {
		if err := s.runner.RunWithS("systemctl", "daemon-reload"); err != nil {
			s.logger.Warnf("Failed to reload systemd daemon: %v", err)
		}
	}

	// Remove from active deployments tracking
	activeDeploymentsMu.Lock()
	delete(activeDeployments, undeployReq.GetPort())
	activeDeploymentsMu.Unlock()

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
				Port:              undeployReq.GetPort(),
				InterfaceId:       undeployReq.GetInterfaceId(),
				IpMode:            undeployReq.GetIpMode(),
				Version:           undeployReq.GetVersion(),
			},
		},
	}
}
