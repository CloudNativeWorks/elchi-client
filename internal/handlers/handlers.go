package handlers

import client "github.com/CloudNativeWorks/elchi-proto/client"

func (h *DeployCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.DeployService(cmd)
}

func (h *SystemdCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.SystemdService(cmd)
}

func (h *BootstrapCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.UpdateBootstrapService(cmd)
}

func (h *UndeployCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.UndeployService(cmd)
}

func (h *ProxyCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.ProxyEnvoyAdmin(cmd)
}

func (h *GeneralLogCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.GeneralLog(cmd)
}

func (h *ClientStatsCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.ClientStats(cmd)
}

func (h *NetworkCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.NetworkService(cmd)
}

func (h *FrrCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.FrrService(cmd)
}

func (h *EnvoyVersionCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.EnvoyVersionService(cmd)
}

func (h *WafVersionCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.WafVersionService(cmd)
}

func (h *FilebeatCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.FilebeatService(cmd)
}

func (h *RsyslogCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.RsyslogService(cmd)
}

func (h *UpgradeListenerCommandHandler) Handle(cmd *client.Command) *client.CommandResponse {
	return h.services.UpgradeListenerService(cmd)
}
