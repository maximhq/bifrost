// Package plugins provides a framework for dynamically loading and managing plugins
package plugins

import (
	"github.com/maximhq/bifrost/core/schemas"
)

type DynamicPluginConfig struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Config  any    `json:"config"`
}

// Config is the configuration for the plugins framework
type Config struct {
	
	Plugins []DynamicPluginConfig `json:"plugins"`
}

// LoadPlugins loads the plugins from the config
func LoadPlugins(loader PluginLoader, config *Config) ([]schemas.Plugin, error) {
	plugins := []schemas.Plugin{}
	if config == nil {
		return plugins, nil
	}
	for _, dp := range config.Plugins {
		if !dp.Enabled {
			continue
		}
		plugin, err := loader.LoadDynamicPlugin(dp.Path, dp.Config)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}
