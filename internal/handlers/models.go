package handlers

import (
	"context"

	"github.com/CloudNativeWorks/elchi-client/internal/services"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type CommandHandlerInterface interface {
	Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse
}

type CommandRegistry struct {
	handlers map[client.CommandType]CommandHandlerInterface
}

type CommandManager struct {
	registry *CommandRegistry
	services *services.Services
	logger   *logger.Logger
}

type DeployCommandHandler struct {
	services *services.Services
}

type SystemdCommandHandler struct {
	services *services.Services
}

type BootstrapCommandHandler struct {
	services *services.Services
}

type UndeployCommandHandler struct {
	services *services.Services
}

type ProxyCommandHandler struct {
	services *services.Services
}

type GeneralLogCommandHandler struct {
	services *services.Services
}

type ClientStatsCommandHandler struct {
	services *services.Services
}

type NetworkCommandHandler struct {
	services *services.Services
}

type FrrCommandHandler struct {
	services *services.Services
}

type EnvoyVersionCommandHandler struct {
	services *services.Services
}

type WafVersionCommandHandler struct {
	services *services.Services
}

type FilebeatCommandHandler struct {
	services *services.Services
}

type RsyslogCommandHandler struct {
	services *services.Services
}

type UpgradeListenerCommandHandler struct {
	services *services.Services
}

type ShieldCommandHandler struct {
	services *services.Services
}
