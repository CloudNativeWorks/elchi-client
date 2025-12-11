package handlers

import (
	"fmt"

	elchigrpc "github.com/CloudNativeWorks/elchi-client/internal/grpc"
	"github.com/CloudNativeWorks/elchi-client/internal/services"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func NewCommandRegistry(services *services.Services) *CommandRegistry {
	registry := &CommandRegistry{
		handlers: make(map[client.CommandType]CommandHandlerInterface),
	}

	registry.Register(client.CommandType_DEPLOY, &DeployCommandHandler{services: services})
	registry.Register(client.CommandType_SERVICE, &SystemdCommandHandler{services: services})
	registry.Register(client.CommandType_UPDATE_BOOTSTRAP, &BootstrapCommandHandler{services: services})
	registry.Register(client.CommandType_UNDEPLOY, &UndeployCommandHandler{services: services})
	registry.Register(client.CommandType_PROXY, &ProxyCommandHandler{services: services})
	registry.Register(client.CommandType_CLIENT_LOGS, &GeneralLogCommandHandler{services: services})
	registry.Register(client.CommandType_CLIENT_STATS, &ClientStatsCommandHandler{services: services})
	registry.Register(client.CommandType_NETWORK, &NetworkCommandHandler{services: services})
	registry.Register(client.CommandType_FRR, &FrrCommandHandler{services: services})
	registry.Register(client.CommandType_FRR_LOGS, &GeneralLogCommandHandler{services: services})
	registry.Register(client.CommandType_ENVOY_VERSION, &EnvoyVersionCommandHandler{services: services})
	registry.Register(client.CommandType_WAF_VERSION, &WafVersionCommandHandler{services: services})
	registry.Register(client.CommandType_FILEBEAT, &FilebeatCommandHandler{services: services})
	registry.Register(client.CommandType_RSYSLOG, &RsyslogCommandHandler{services: services})
	registry.Register(client.CommandType_UPGRADE_LISTENER, &UpgradeListenerCommandHandler{services: services})

	return registry
}

// NewCommandManagerWithGRPC creates command manager with gRPC client
func NewCommandManagerWithGRPC(grpcClient *elchigrpc.Client) *CommandManager {
	services := services.NewServices()
	services.SetGRPCClient(grpcClient)
	return &CommandManager{
		registry: NewCommandRegistry(services),
		services: services,
	}
}

func (r *CommandRegistry) Register(cmdType client.CommandType, handler CommandHandlerInterface) {
	r.handlers[cmdType] = handler
}

func (r *CommandRegistry) GetHandler(cmdType client.CommandType) (CommandHandlerInterface, bool) {
	handler, exists := r.handlers[cmdType]
	return handler, exists
}

func (m *CommandManager) HandleCommand(cmd *client.Command) *client.CommandResponse {
	handler, exists := m.registry.GetHandler(cmd.Type)
	if !exists {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("unsupported command type: %v", cmd.Type))
	}

	return handler.Handle(cmd)
}
