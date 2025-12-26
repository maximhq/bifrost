package lib

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

var logger schemas.Logger

// SetLogger sets the logger for the application.
func SetLogger(l schemas.Logger) {
	logger = l
}

type EnterpriseOverrides interface {
	GetGovernancePluginName() string
	LoadGovernancePlugin(ctx context.Context, config *Config) (schemas.Plugin, error)
	LoadPricingManager(ctx context.Context, pricingConfig *modelcatalog.Config, configStore configstore.ConfigStore) (*modelcatalog.ModelCatalog, error)
}
