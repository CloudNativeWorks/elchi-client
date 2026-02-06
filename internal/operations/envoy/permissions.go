package envoy

import (
	"github.com/CloudNativeWorks/elchi-client/internal/operations/common"
	"github.com/sirupsen/logrus"
)

func NewPermissionManager() *common.PermissionManager {
	return common.NewPermissionManager(
		DefaultBaseDir,
		0750,
		0750,
		logrus.WithField("component", "envoy-permissions"),
	)
}
