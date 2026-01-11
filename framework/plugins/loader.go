package plugins

import "github.com/maximhq/bifrost/core/schemas"

// PluginLoader is the contract for a plugin loader
type PluginLoader interface {
	LoadDynamicLLMPlugin(path string, config any) (schemas.LLMPlugin, error)
}
