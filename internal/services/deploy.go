package services

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/files"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/network"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/vishvananda/netlink"
)

var (
	// Mutex to prevent concurrent deployments on the same port
	deploymentLock sync.Mutex
	// Track active deployments by port
	activeDeployments   = make(map[uint32]string)
	activeDeploymentsMu sync.RWMutex
)

type DeployState struct {
	CreatedFiles      []string
	ServiceEnabled    bool // Service has been enabled in systemd
	ServiceStarted    bool // Service has been started
	ServiceName       string
	DummyIfaceName    string
	DummyIfaceCreated bool // Interface was actually created (not just attempted)
	SystemdReloaded   bool // daemon-reload was performed
}

func cleanupAndRollback(state DeployState, logger *logger.Logger, runner *cmdrunner.CommandsRunner) {
	logger.Infof("Starting rollback for deployment")

	// Stop and disable service if it was started or enabled
	if state.ServiceStarted || state.ServiceEnabled {
		// Check if service actually exists before trying to stop/disable (use 'show' which doesn't require sudo)
		if output, _ := runner.RunWithOutput("systemctl", "show", "-p", "LoadState", state.ServiceName); strings.Contains(string(output), "LoadState=loaded") {
			if state.ServiceStarted {
				if err := runner.RunWithS("systemctl", "stop", state.ServiceName); err != nil {
					logger.Errorf("failed to stop service %s: %v", state.ServiceName, err)
				}
			}
			if state.ServiceEnabled {
				if err := runner.RunWithS("systemctl", "disable", state.ServiceName); err != nil {
					logger.Errorf("failed to disable service %s: %v", state.ServiceName, err)
				}
			}
		}
	}

	// Remove created files in reverse order
	for i := len(state.CreatedFiles) - 1; i >= 0; i-- {
		if err := os.Remove(state.CreatedFiles[i]); err != nil && !os.IsNotExist(err) {
			logger.Errorf("failed to cleanup file %s: %v", state.CreatedFiles[i], err)
		}
	}

	// Delete dummy interface if it was created
	if state.DummyIfaceCreated && state.DummyIfaceName != "" {
		if err := network.DeleteDummyInterface(state.DummyIfaceName, logger); err != nil {
			logger.Errorf("failed to delete interface %s: %v", state.DummyIfaceName, err)
		}
	}

	// Reload systemd only if we created service files or modified systemd state
	if state.SystemdReloaded || state.ServiceEnabled || len(state.CreatedFiles) > 0 {
		if err := runner.RunWithS("systemctl", "daemon-reload"); err != nil {
			logger.Errorf("failed to reload systemd during rollback: %v", err)
		}
	}

	logger.Infof("Rollback completed")
}

// validateDeploymentPrerequisites checks if deployment can proceed
func validateDeploymentPrerequisites(deployReq *client.RequestDeploy, logger *logger.Logger, runner *cmdrunner.CommandsRunner) error {
	// Check if port is already in use by another deployment
	activeDeploymentsMu.RLock()
	if existingService, exists := activeDeployments[deployReq.GetPort()]; exists {
		activeDeploymentsMu.RUnlock()
		return fmt.Errorf("port %d is already in use by service %s", deployReq.GetPort(), existingService)
	}
	activeDeploymentsMu.RUnlock()

	// Check if interface already exists
	ifaceName := fmt.Sprintf("elchi-if-%d", deployReq.GetPort())
	if _, err := netlink.LinkByName(ifaceName); err == nil {
		return fmt.Errorf("interface %s already exists", ifaceName)
	}

	// Check if service already exists
	serviceName := fmt.Sprintf("%s-%d.service", deployReq.GetName(), deployReq.GetPort())
	// Use 'systemctl show' which doesn't require sudo
	if output, _ := runner.RunWithOutput("systemctl", "show", "-p", "LoadState", serviceName); strings.Contains(string(output), "LoadState=loaded") {
		// Check if it's actually active (doesn't require sudo)
		if status, _ := runner.RunWithOutput("systemctl", "is-active", serviceName); strings.TrimSpace(string(status)) == "active" {
			return fmt.Errorf("service %s is already active", serviceName)
		}
	}

	return nil
}

// checkIfInterfaceCreated verifies if the interface was actually created
func checkIfInterfaceCreated(ifaceName string) bool {
	_, err := netlink.LinkByName(ifaceName)
	return err == nil
}

func (s *Services) DeployService(cmd *client.Command) *client.CommandResponse {
	deployReq := cmd.GetDeploy()
	if deployReq == nil {
		s.logger.Errorf("deploy payload is nil")
		return helper.NewErrorResponse(cmd, "deploy payload is nil")
	}

	// Acquire deployment lock to prevent race conditions
	deploymentLock.Lock()
	defer deploymentLock.Unlock()

	// Validate prerequisites
	if err := validateDeploymentPrerequisites(deployReq, s.logger, s.runner); err != nil {
		s.logger.Errorf("deployment validation failed: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("deployment validation failed: %v", err))
	}

	s.logger.Infof("Deploying service: %s on port %d", deployReq.Name, deployReq.GetPort())
	filename := fmt.Sprintf("%s-%d", deployReq.GetName(), deployReq.GetPort())
	ifaceName := fmt.Sprintf("elchi-if-%d", deployReq.GetPort())

	state := DeployState{
		CreatedFiles:      []string{},
		ServiceEnabled:    false,
		ServiceStarted:    false,
		ServiceName:       filename + ".service",
		DummyIfaceName:    ifaceName,
		DummyIfaceCreated: false,
		SystemdReloaded:   false,
	}

	// Track this deployment as active
	activeDeploymentsMu.Lock()
	activeDeployments[deployReq.GetPort()] = filename
	activeDeploymentsMu.Unlock()

	// Ensure we remove from active deployments on any error
	defer func() {
		if state.ServiceStarted {
			// Deployment successful, keep in active deployments
			return
		}
		// Deployment failed, remove from active deployments
		activeDeploymentsMu.Lock()
		delete(activeDeployments, deployReq.GetPort())
		activeDeploymentsMu.Unlock()
	}()

	bootstrapPath, err := files.WriteBootstrapFile(filename, deployReq.GetBootstrap())
	if err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to write bootstrap file: %v", err))
	}
	state.CreatedFiles = append(state.CreatedFiles, bootstrapPath)

	netplanPath, dummyIface, err := network.SetupDummyInterface(filename, ifaceName, deployReq.GetDownstreamAddress(), deployReq.GetPort(), s.logger)
	if err != nil {
		// Check if interface was partially created
		if checkIfInterfaceCreated(ifaceName) {
			state.DummyIfaceCreated = true
			state.DummyIfaceName = ifaceName
		}
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to setup network interface: %v", err))
	}
	state.CreatedFiles = append(state.CreatedFiles, netplanPath)
	state.DummyIfaceName = dummyIface
	state.DummyIfaceCreated = true

	servicePath, err := files.WriteSystemdServiceFile(filename, deployReq.GetName(), deployReq.GetVersion(), deployReq.GetPort())
	if err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to write service file: %v", err))
	}
	state.CreatedFiles = append(state.CreatedFiles, servicePath)

	journalPath, err := files.WriteJournalConf(filename)
	if err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to write journal config: %v", err))
	}
	state.CreatedFiles = append(state.CreatedFiles, journalPath)

	// Restart journald to apply new journal configuration
	if err := s.runner.RunWithS("systemctl", "restart", "systemd-journald"); err != nil {
		s.logger.Warnf("Failed to restart journald: %v", err)
		// Non-critical error - continue with deployment
	}

	// Reload systemd to recognize new service file
	if err := s.runner.RunWithS("systemctl", "daemon-reload"); err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to reload systemd (check sudo permissions): %v", err))
	}
	state.SystemdReloaded = true

	// Enable the service
	if err := s.runner.RunWithS("systemctl", "enable", state.ServiceName); err != nil {
		s.logger.Errorf("Failed to enable service %s - checking sudo permissions", state.ServiceName)
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to enable service (check sudo permissions for 'systemctl enable'): %v", err))
	}
	state.ServiceEnabled = true

	// Start the service
	if err := s.runner.RunWithS("systemctl", "start", state.ServiceName); err != nil {
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to start service (check sudo permissions): %v", err))
	}
	state.ServiceStarted = true

	// Verify service is actually running (doesn't require sudo)
	if status, err := s.runner.RunWithOutput("systemctl", "is-active", state.ServiceName); err != nil || strings.TrimSpace(string(status)) != "active" {
		s.logger.Errorf("Service %s failed to start properly", state.ServiceName)
		cleanupAndRollback(state, s.logger, s.runner)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("service failed to start properly: %v", err))
	}

	s.logger.Infof("Successfully deployed service %s on port %d", deployReq.Name, deployReq.GetPort())

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_Deploy{
			Deploy: &client.ResponseDeploy{
				Files:             bootstrapPath,
				Service:           servicePath,
				Network:           netplanPath,
				DownstreamAddress: deployReq.GetDownstreamAddress(),
				Port:              deployReq.GetPort(),
				InterfaceId:       deployReq.GetInterfaceId(),
				IpMode:            deployReq.GetIpMode(),
				Version:           deployReq.GetVersion(),
			},
		},
	}
}
