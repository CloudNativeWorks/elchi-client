package handlers

import (
	"github.com/CloudNativeWorks/elchi-client/internal/services"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type CommandHandlerInterface interface {
	Handle(cmd *client.Command) *client.CommandResponse
}

type CommandRegistry struct {
	handlers map[client.CommandType]CommandHandlerInterface
}

type CommandManager struct {
	registry *CommandRegistry
	services *services.Services
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
