package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/operations/proxy"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (s *Services) ProxyEnvoyAdmin(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	req := cmd.GetEnvoyAdmin()
	if req == nil {
		s.logger.Errorf("envoy admin payload is nil")
		return helper.NewErrorResponse(cmd, "envoy admin payload is nil")
	}

	if req.Method != client.HttpMethod_GET && req.Method != client.HttpMethod_POST {
		s.logger.Errorf("invalid method: %v", req.Method)
		return helper.NewErrorResponse(cmd, "invalid method")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if req.GetPath() == "/envoy" {
		paths := []string{
			"/certs",
			"/ready",
			"/config_dump",
			"/hot_restart_version",
			"/listeners",
			"/memory",
			"/server_info",
			"/stats",
			"/runtime",
		}

		results := make(map[string]string)
		for _, path := range paths {
			clearKey := strings.TrimPrefix(path, "/")
			adminReq := &client.RequestEnvoyAdmin{
				Name:    req.Name,
				Method:  req.Method,
				Path:    path,
				Body:    req.Body,
				Port:    req.Port,
				Queries: req.Queries,
			}
			resp, err := proxy.Do(ctx, adminReq)
			if err != nil {
				s.logger.Errorf("ProxyEnvoyAdmin proxy.Do error for path %s: %v", path, err)
				results[clearKey] = fmt.Sprintf("error: %v", err)
				continue
			}
			results[clearKey] = resp.Body
		}

		jsonData, err := json.Marshal(results)
		if err != nil {
			s.logger.Errorf("Failed to marshal results to JSON: %v", err)
			return helper.NewErrorResponse(cmd, fmt.Sprintf("failed to marshal results: %v", err))
		}

		return &client.CommandResponse{
			Identity:  cmd.Identity,
			CommandId: cmd.CommandId,
			Success:   true,
			Result: &client.CommandResponse_EnvoyAdmin{
				EnvoyAdmin: &client.ResponseEnvoyAdmin{
					Body: string(jsonData),
				},
			},
		}
	}

	resp, err := proxy.Do(ctx, req)
	if err != nil {
		s.logger.Errorf("ProxyEnvoyAdmin proxy.Do error: %v", err)
		return helper.NewErrorResponse(cmd, fmt.Sprintf("proxy error: %v", err))
	}

	return &client.CommandResponse{
		Identity:  cmd.Identity,
		CommandId: cmd.CommandId,
		Success:   true,
		Result: &client.CommandResponse_EnvoyAdmin{
			EnvoyAdmin: resp,
		},
	}
}
