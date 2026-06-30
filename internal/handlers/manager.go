package handlers

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	elchigrpc "github.com/CloudNativeWorks/elchi-client/internal/grpc"
	"github.com/CloudNativeWorks/elchi-client/internal/services"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// commandTimeout is a generous upper bound on how long a single command handler
// may run. It exists to stop one hung handler (e.g. a blocked systemctl/exec or
// a stuck download) from stalling the single command-processing loop forever.
// Real operations finish in seconds to a couple of minutes; this only trips on
// genuine hangs.
const commandTimeout = 10 * time.Minute

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
	registry.Register(client.CommandType_SHIELD, &ShieldCommandHandler{services: services})

	return registry
}

// NewCommandManagerWithGRPC creates command manager with gRPC client
func NewCommandManagerWithGRPC(grpcClient *elchigrpc.Client) *CommandManager {
	services := services.NewServices()
	services.SetGRPCClient(grpcClient)
	return &CommandManager{
		registry: NewCommandRegistry(services),
		services: services,
		logger:   logger.NewLogger("command-manager"),
	}
}

func (r *CommandRegistry) Register(cmdType client.CommandType, handler CommandHandlerInterface) {
	r.handlers[cmdType] = handler
}

func (r *CommandRegistry) GetHandler(cmdType client.CommandType) (CommandHandlerInterface, bool) {
	handler, exists := r.handlers[cmdType]
	return handler, exists
}

func (m *CommandManager) HandleCommand(ctx context.Context, cmd *client.Command) (resp *client.CommandResponse) {
	handler, exists := m.registry.GetHandler(cmd.Type)
	if !exists {
		return helper.NewErrorResponse(cmd, fmt.Sprintf("unsupported command type: %v", cmd.Type))
	}

	// Bound each command so one hung handler can't stall the command loop.
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	// Recover from a panic inside any handler and turn it into a failure
	// response. Previously a panic unwound past this point and killed the
	// command-stream goroutine, dropping the gRPC stream and forcing a full
	// reconnect + re-registration for a single malformed command.
	defer func() {
		if r := recover(); r != nil {
			m.logger.Errorf("PANIC recovered handling %v command: %v\nStack: %s", cmd.Type, r, debug.Stack())
			resp = helper.NewErrorResponse(cmd, fmt.Sprintf("internal error handling command: %v", r))
		}
	}()

	return handler.Handle(ctx, cmd)
}
