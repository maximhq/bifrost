// Package plugins provides a framework for dynamically loading and managing plugins
package plugins

import (
	"context"

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

// DynamicPluginManager is the manager for the dynamic plugins
type DynamicPluginManager struct {
	ctx     context.Context
	config  *Config
	plugins []schemas.Plugin
	logger  schemas.Logger
}

// LoadPlugins loads the plugins from the config
func (m *DynamicPluginManager) LoadPlugins() ([]schemas.Plugin, error) {
	for _, dp := range m.config.Plugins {
		if !dp.Enabled {
			continue
		}
		plugin, err := loadDynamicPlugin(dp.Path)
		if err != nil {
			return nil, err
		}
		m.plugins = append(m.plugins, plugin)
	}
	return m.plugins, nil
}

// NewDynamicPluginManager creates a new DynamicPluginManager
func NewDynamicPluginManager(ctx context.Context, config *Config, logger schemas.Logger) (*DynamicPluginManager, error) {
	return &DynamicPluginManager{
		ctx:     ctx,
		config:  config,
		plugins: []schemas.Plugin{},
		logger:  logger,
	}, nil
}
