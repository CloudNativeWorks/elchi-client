package initializer

import (
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

type Initializer struct {
	Logger *logger.Logger
}

func NewInitializer() *Initializer {
	return &Initializer{
		Logger: logger.NewLogger("initializer"),
	}
}
