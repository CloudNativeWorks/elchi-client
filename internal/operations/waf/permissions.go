package waf

import (
	"github.com/CloudNativeWorks/elchi-client/internal/operations/common"
	"github.com/sirupsen/logrus"
)

func NewPermissionManager() *common.PermissionManager {
	return common.NewPermissionManager(
		DefaultBaseDir,
		0755,
		0755,
		logrus.WithField("component", "waf-permissions"),
	)
}
