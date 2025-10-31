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
func LoadPlugins(config *Config) ([]schemas.Plugin, error) {
	plugins := []schemas.Plugin{}
	for _, dp := range config.Plugins {
		if !dp.Enabled {
			continue
		}
		plugin, err := loadDynamicPlugin(dp.Path, dp.Config)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}
