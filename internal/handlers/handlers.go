package handlers

import (
	"context"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func (h *DeployCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.DeployService(ctx, cmd)
}

func (h *SystemdCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.SystemdService(ctx, cmd)
}

func (h *BootstrapCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.UpdateBootstrapService(ctx, cmd)
}

func (h *UndeployCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.UndeployService(ctx, cmd)
}

func (h *ProxyCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.ProxyEnvoyAdmin(ctx, cmd)
}

func (h *GeneralLogCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.GeneralLog(ctx, cmd)
}

func (h *ClientStatsCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.ClientStats(ctx, cmd)
}

func (h *NetworkCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.NetworkService(ctx, cmd)
}

func (h *FrrCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.FrrService(ctx, cmd)
}

func (h *EnvoyVersionCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.EnvoyVersionService(ctx, cmd)
}

func (h *WafVersionCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.WafVersionService(ctx, cmd)
}

func (h *FilebeatCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.FilebeatService(ctx, cmd)
}

func (h *RsyslogCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.RsyslogService(ctx, cmd)
}

func (h *UpgradeListenerCommandHandler) Handle(ctx context.Context, cmd *client.Command) *client.CommandResponse {
	return h.services.UpgradeListenerService(ctx, cmd)
}
