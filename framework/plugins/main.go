// Package plugins provides a framework for dynamically loading and managing plugins
package plugins

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// PluginConfig is the generic configuration for any plugin type
// Plugin types are automatically detected based on implemented interfaces
type PluginConfig struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Config  any    `json:"config,omitempty"`
}

// Config is the configuration for the plugins framework
type Config struct {
	// Plugins is the unified configuration for all plugin types
	Plugins []PluginConfig `json:"plugins"`
}

// AsLLMPlugin checks if a base plugin implements LLMPlugin and actually has LLM hooks.
// For DynamicPlugin, it checks if the hook function pointers are not nil.
// Returns nil if the plugin does not implement the interface or has no LLM hooks.
func AsLLMPlugin(plugin schemas.BasePlugin) schemas.LLMPlugin {
	// Check if it's a DynamicPlugin first
	if dp, ok := plugin.(*DynamicPlugin); ok {
		// Only return as LLMPlugin if it actually has LLM hooks
		if dp.preLLMHook != nil || dp.postLLMHook != nil {
			return dp
		}
		return nil
	}
	// For non-DynamicPlugin types, use normal type assertion
	if llmPlugin, ok := plugin.(schemas.LLMPlugin); ok {
		return llmPlugin
	}
	return nil
}

// AsMCPPlugin checks if a base plugin implements MCPPlugin and actually has MCP hooks.
// For DynamicPlugin, it checks if the hook function pointers are not nil.
// Returns nil if the plugin does not implement the interface or has no MCP hooks.
func AsMCPPlugin(plugin schemas.BasePlugin) schemas.MCPPlugin {
	// Check if it's a DynamicPlugin first
	if dp, ok := plugin.(*DynamicPlugin); ok {
		// Only return as MCPPlugin if it actually has MCP hooks
		if dp.preMCPHook != nil || dp.postMCPHook != nil {
			return dp
		}
		return nil
	}
	// For non-DynamicPlugin types, use normal type assertion
	if mcpPlugin, ok := plugin.(schemas.MCPPlugin); ok {
		return mcpPlugin
	}
	return nil
}

// AsHTTPTransportPlugin checks if a base plugin implements HTTPTransportPlugin and actually has HTTP transport hooks.
// For DynamicPlugin, it checks if the hook function pointers are not nil.
// Returns nil if the plugin does not implement the interface or has no HTTP transport hooks.
func AsHTTPTransportPlugin(plugin schemas.BasePlugin) schemas.HTTPTransportPlugin {
	// Check if it's a DynamicPlugin first
	if dp, ok := plugin.(*DynamicPlugin); ok {
		// Only return as HTTPTransportPlugin if it actually has HTTP transport hooks
		if dp.httpTransportPreHook != nil || dp.httpTransportPostHook != nil {
			return dp
		}
		return nil
	}
	// For non-DynamicPlugin types, use normal type assertion
	if httpPlugin, ok := plugin.(schemas.HTTPTransportPlugin); ok {
		return httpPlugin
	}
	return nil
}

// AsObservabilityPlugin checks if a base plugin implements ObservabilityPlugin and actually has observability hooks.
// For DynamicPlugin, it checks if the hook function pointer is not nil.
// Returns nil if the plugin does not implement the interface or has no observability hooks.
func AsObservabilityPlugin(plugin schemas.BasePlugin) schemas.ObservabilityPlugin {
	// Check if it's a DynamicPlugin first
	if dp, ok := plugin.(*DynamicPlugin); ok {
		// Only return as ObservabilityPlugin if it actually has the Inject hook
		if dp.inject != nil {
			return dp
		}
		return nil
	}
	// For non-DynamicPlugin types, use normal type assertion
	if obsPlugin, ok := plugin.(schemas.ObservabilityPlugin); ok {
		return obsPlugin
	}
	return nil
}

// LoadPlugins loads all plugins from the config
func LoadPlugins(loader PluginLoader, config *Config) ([]schemas.BasePlugin, error) {
	plugins := []schemas.BasePlugin{}
	if config == nil {
		return plugins, nil
	}

	for _, pc := range config.Plugins {
		if !pc.Enabled {
			continue
		}
		plugin, err := loader.LoadPlugin(pc.Path, pc.Config)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, plugin)
	}

	return plugins, nil
}

// FilterLLMPlugins filters a list of BasePlugins to only include those implementing LLMPlugin
func FilterLLMPlugins(plugins []schemas.BasePlugin) []schemas.LLMPlugin {
	result := []schemas.LLMPlugin{}
	for _, p := range plugins {
		if llmPlugin := AsLLMPlugin(p); llmPlugin != nil {
			result = append(result, llmPlugin)
		}
	}
	return result
}

// FilterMCPPlugins filters a list of BasePlugins to only include those implementing MCPPlugin
func FilterMCPPlugins(plugins []schemas.BasePlugin) []schemas.MCPPlugin {
	result := []schemas.MCPPlugin{}
	for _, p := range plugins {
		if mcpPlugin := AsMCPPlugin(p); mcpPlugin != nil {
			result = append(result, mcpPlugin)
		}
	}
	return result
}

// FilterHTTPTransportPlugins filters a list of BasePlugins to only include those implementing HTTPTransportPlugin
func FilterHTTPTransportPlugins(plugins []schemas.BasePlugin) []schemas.HTTPTransportPlugin {
	result := []schemas.HTTPTransportPlugin{}
	for _, p := range plugins {
		if httpPlugin := AsHTTPTransportPlugin(p); httpPlugin != nil {
			result = append(result, httpPlugin)
		}
	}
	return result
}

// FilterObservabilityPlugins filters a list of BasePlugins to only include those implementing ObservabilityPlugin
func FilterObservabilityPlugins(plugins []schemas.BasePlugin) []schemas.ObservabilityPlugin {
	result := []schemas.ObservabilityPlugin{}
	for _, p := range plugins {
		if obsPlugin := AsObservabilityPlugin(p); obsPlugin != nil {
			result = append(result, obsPlugin)
		}
	}
	return result
}
