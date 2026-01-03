// Package plugins provides a framework for dynamically loading and managing plugins
package plugins

import (
	"github.com/maximhq/bifrost/core/schemas"
)

type DynamicLLMPluginConfig struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Config  any    `json:"config"`
}

// Config is the configuration for the plugins framework
type Config struct {
	LLMPlugins []DynamicLLMPluginConfig `json:"llm_plugins"`
}

// AsLLMPlugin checks if a base plugin implements LLMPlugin and returns it.
// Returns nil if the plugin does not implement the interface.
func AsLLMPlugin(plugin schemas.BasePlugin) schemas.LLMPlugin {
	if llmPlugin, ok := plugin.(schemas.LLMPlugin); ok {
		return llmPlugin
	}
	return nil
}

// AsMCPPlugin checks if a base plugin implements MCPPlugin and returns it.
// Returns nil if the plugin does not implement the interface.
func AsMCPPlugin(plugin schemas.BasePlugin) schemas.MCPPlugin {
	if mcpPlugin, ok := plugin.(schemas.MCPPlugin); ok {
		return mcpPlugin
	}
	return nil
}

// LoadLLMPlugins loads the LLM plugins from the config
func LoadLLMPlugins(loader PluginLoader, config *Config) ([]schemas.LLMPlugin, error) {
	plugins := []schemas.LLMPlugin{}
	if config == nil {
		return plugins, nil
	}
	for _, dp := range config.LLMPlugins {
		if !dp.Enabled {
			continue
		}
		plugin, err := loader.LoadDynamicLLMPlugin(dp.Path, dp.Config)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}
