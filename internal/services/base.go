package services

import (
	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

type Services struct {
	logger *logger.Logger
	runner *cmdrunner.CommandsRunner
	vtysh  *frr.VtyshManager
}

func NewServices() *Services {
	log := logger.NewLogger("vtysh")
	return &Services{
		runner: cmdrunner.NewCommandsRunner(),
		logger: logger.NewLogger("services"),
		vtysh:  frr.NewVtyshManager(log),
	}
}


