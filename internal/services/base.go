package services

import (
	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	elchigrpc "github.com/CloudNativeWorks/elchi-client/internal/grpc"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/frr"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

type Services struct {
	logger     *logger.Logger
	runner     *cmdrunner.CommandsRunner
	vtysh      *frr.VtyshManager
	grpcClient *elchigrpc.Client
}

func NewServices() *Services {
	log := logger.NewLogger("vtysh")
	return &Services{
		runner: cmdrunner.NewCommandsRunner(),
		logger: logger.NewLogger("services"),
		vtysh:  frr.NewVtyshManager(log),
	}
}

// SetGRPCClient sets the gRPC client for services that need it
func (s *Services) SetGRPCClient(client *elchigrpc.Client) {
	s.grpcClient = client
}


