package services

import (
	"fmt"
	"os"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/files"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/network"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/systemd"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type DeployState struct {
	CreatedFiles   []string
	ServiceCreated bool
	ServiceName    string
	DummyIfaceName string
}

func cleanupAndRollback(state DeployState, logger *logger.Logger, runner *cmdrunner.CommandsRunner) {
	if state.ServiceCreated {
		_ = runner.RunWithS("systemctl", "stop", state.ServiceName)
		_ = runner.RunWithS("systemctl", "disable", state.ServiceName)
	}

	for i := len(state.CreatedFiles) - 1; i >= 0; i-- {
		if err := os.Remove(state.CreatedFiles[i]); err != nil && !os.IsNotExist(err) {
			logger.Errorf("failed to cleanup file %s: %v", state.CreatedFiles[i], err)
		}
	}

	if state.DummyIfaceName != "" {
		if err := network.DeleteDummyInterface(state.DummyIfaceName, logger); err != nil {
			logger.Errorf("failed to delete interface %s: %v", state.DummyIfaceName, err)
		}
	}

	_ = runner.RunWithS("systemctl", "daemon-reload")
}

func (s *Services) DeployService(cmd *client.Command) *client.CommandResponse {
	deployReq := cmd.GetDeploy()
	if deployReq == nil {
		s.logger.Errorf("deploy payload is nil")
		return helper.NewErrorResponse(cmd, "deploy payload is nil")
	}

	s.logger.Infof("Deploying service: %s", deployReq.Name)
	filename := fmt.Sprintf("%s-%d", deployReq.GetName(), deployReq.GetPort())
	ifaceName := fmt.Sprintf("elchi-if-%d", deployReq.GetPort())

	state := DeployState{
		CreatedFiles:   []string{},
		ServiceCreated: false,
		ServiceName:    filename + ".service",
		DummyIfaceName: ifaceName,
	}

	bootstrapPath, err := files.WriteBootstrapFile(filename, deployReq.GetBootstrap())
	if err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, err.Error())
	}
	state.CreatedFiles = append(state.CreatedFiles, bootstrapPath)

	netplanPath, dummyIface, err := network.SetupDummyInterface(filename, ifaceName, deployReq.GetDownstreamAddress(), deployReq.GetPort(), s.logger)
	if err != nil {
		state.DummyIfaceName = dummyIface
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, err.Error())
	}
	state.CreatedFiles = append(state.CreatedFiles, netplanPath)
	state.DummyIfaceName = dummyIface

	servicePath, err := files.WriteSystemdServiceFile(filename, deployReq.GetName(), deployReq.GetVersion(), deployReq.GetPort())
	if err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, err.Error())
	}
	state.CreatedFiles = append(state.CreatedFiles, servicePath)
	state.ServiceCreated = true

	journalPath, err := files.WriteJournalConf(filename)
	if err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, err.Error())
	}
	state.CreatedFiles = append(state.CreatedFiles, journalPath)

	if err := systemd.ActivateService(filename, s.logger, s.runner); err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, err.Error())
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Deploy{
			Deploy: &client.ResponseDeploy{
				Files:   bootstrapPath,
				Service: servicePath,
				Network: netplanPath,
			},
		},
	}
}
