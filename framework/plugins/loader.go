package plugins

import "github.com/maximhq/bifrost/core/schemas"

// PluginLoader is the contract for a plugin loader
type PluginLoader interface {
	LoadDynamicPlugin(path string, config any) (schemas.Plugin, error)
}
