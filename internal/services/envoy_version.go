package services

import (
	"github.com/CloudNativeWorks/elchi-client/internal/operations/envoy"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/sirupsen/logrus"
)

// EnvoyVersionService handles envoy version management commands
func (s *Services) EnvoyVersionService(cmd *client.Command) *client.CommandResponse {
	logger := logrus.WithFields(logrus.Fields{
		"command_id": cmd.CommandId,
		"service":    "envoy-version",
	})

	logger.Info("Processing envoy version command")
	
	// Validate command payload
	envoyVersionRequest := cmd.GetEnvoyVersion()
	if envoyVersionRequest == nil {
		logger.Error("EnvoyVersion payload is nil")
		return &client.CommandResponse{
			Identity:  cmd.Identity,
			CommandId: cmd.CommandId,
			Success:   false,
			Error:     "envoy version payload is required",
			Result: &client.CommandResponse_EnvoyVersion{
				EnvoyVersion: &client.ResponseEnvoyVersion{
					Status:       client.EnvoyVersionStatus_VERSION_NOT_FOUND,
					ErrorMessage: "envoy version payload is required",
				},
			},
		}
	}

	// Create envoy manager
	manager := envoy.NewManager()
	
	// Process the request
	response := manager.ProcessEnvoyVersionCommand(envoyVersionRequest)

	// Log the result
	if response.Status == client.EnvoyVersionStatus_SUCCESS {
		logger.WithFields(logrus.Fields{
			"operation":          envoyVersionRequest.Operation,
			"downloaded_count":   len(response.DownloadedVersions),
			"installed_version":  response.InstalledVersion,
		}).Info("Envoy version command completed successfully")
	} else {
		logger.WithFields(logrus.Fields{
			"operation": envoyVersionRequest.Operation,
			"status":    response.Status,
			"error":     response.ErrorMessage,
		}).Error("Envoy version command failed")
	}

	// Return command response
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   response.Status == client.EnvoyVersionStatus_SUCCESS,
		Error:     response.ErrorMessage,
		Result: &client.CommandResponse_EnvoyVersion{
			EnvoyVersion: response,
		},
	}
}