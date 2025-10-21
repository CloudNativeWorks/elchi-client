package services

import (
	"github.com/CloudNativeWorks/elchi-client/internal/operations/waf"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/sirupsen/logrus"
)

// WafVersionService handles WAF version management commands
func (s *Services) WafVersionService(cmd *client.Command) *client.CommandResponse {
	logger := logrus.WithFields(logrus.Fields{
		"command_id": cmd.CommandId,
		"service":    "waf-version",
	})

	logger.Info("Processing WAF version command")
	
	// Validate command payload
	wafVersionRequest := cmd.GetWafVersion()
	if wafVersionRequest == nil {
		logger.Error("WafVersion payload is nil")
		return &client.CommandResponse{
			Identity:  cmd.Identity,
			CommandId: cmd.CommandId,
			Success:   false,
			Error:     "WAF version payload is required",
			Result: &client.CommandResponse_WafVersion{
				WafVersion: &client.ResponseWafVersion{
					Status:       client.VersionStatus_VERSION_NOT_FOUND,
					ErrorMessage: "WAF version payload is required",
				},
			},
		}
	}

	// Create WAF manager
	manager := waf.NewManager()
	
	// Process the request
	response := manager.ProcessWafVersionCommand(wafVersionRequest)

	// Log the result
	if response.Status == client.VersionStatus_SUCCESS {
		logger.WithFields(logrus.Fields{
			"operation":          wafVersionRequest.Operation,
			"downloaded_count":   len(response.DownloadedVersions),
			"installed_version":  response.InstalledVersion,
		}).Info("WAF version command completed successfully")
	} else {
		logger.WithFields(logrus.Fields{
			"operation": wafVersionRequest.Operation,
			"status":    response.Status,
			"error":     response.ErrorMessage,
		}).Error("WAF version command failed")
	}

	// Return command response
	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   response.Status == client.VersionStatus_SUCCESS,
		Error:     response.ErrorMessage,
		Result: &client.CommandResponse_WafVersion{
			WafVersion: response,
		},
	}
}