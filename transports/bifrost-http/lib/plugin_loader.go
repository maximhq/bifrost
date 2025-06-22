package lib

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/sdk"
)

// LoadPlugins loads plugins based on the configuration.
// It supports both direct plugin loading (traditional) and RPC plugin loading (new).
// Returns a slice of loaded plugins that can be used with Bifrost.
func LoadPlugins(pluginConfigs []PluginConfig) ([]schemas.Plugin, error) {
	var loadedPlugins []schemas.Plugin

	// Load plugins from configuration
	for _, config := range pluginConfigs {
		if !config.Enabled {
			log.Printf("Plugin %s is disabled, skipping", config.Name)
			continue
		}

		plugin, err := loadPlugin(config)
		if err != nil {
			log.Printf("warning: failed to load plugin %s: %v", config.Name, err)
			continue
		}

		if plugin != nil {
			// Inject logger into the plugin
			if logger := getLoggerForPlugin(config.Name); logger != nil {
				plugin.SetLogger(logger)
			}

			log.Printf("Successfully loaded plugin: %s (type: %s)", config.Name, config.Type)
			loadedPlugins = append(loadedPlugins, plugin)
		}
	}

	return loadedPlugins, nil
}

// loadPlugin loads a single plugin based on its configuration
func loadPlugin(config PluginConfig) (schemas.Plugin, error) {
	switch config.Type {
	case PluginTypeDirect:
		return loadDirectPlugin(config)
	case PluginTypeRPC:
		return loadRPCPlugin(config)
	default:
		return nil, fmt.Errorf("unsupported plugin type: %s", config.Type)
	}
}

// loadDirectPlugin loads a plugin using direct integration (traditional method)
// This validates that the plugin follows the standardized constructor pattern
func loadDirectPlugin(config PluginConfig) (schemas.Plugin, error) {
	switch strings.ToLower(config.Name) {
	case "maxim":
		// For now, we'll handle maxim specially until it's updated to use the standard constructor
		return loadLegacyMaximPlugin()
	default:
		return nil, fmt.Errorf("unknown direct plugin: %s. Please use RPC plugins or update to standard constructor", config.Name)
	}
}

// loadRPCPlugin loads a plugin using RPC (new method)
func loadRPCPlugin(config PluginConfig) (schemas.Plugin, error) {
	if config.BinaryPath == "" {
		return nil, fmt.Errorf("binary_path is required for RPC plugins")
	}

	// Set environment variables for the plugin (for backward compatibility)
	for key, value := range config.EnvVars {
		// Handle environment variable placeholders
		if strings.HasPrefix(value, "env.") {
			envKey := strings.TrimPrefix(value, "env.")
			envValue := os.Getenv(envKey)
			if envValue == "" {
				log.Printf("warning: environment variable %s is not set for plugin %s", envKey, config.Name)
			}
			value = envValue
		}

		if err := os.Setenv(key, value); err != nil {
			return nil, fmt.Errorf("failed to set environment variable %s for plugin %s: %w", key, config.Name, err)
		}
	}

	// Prepare plugin configuration as JSON
	var configJSON json.RawMessage
	if len(config.Config) > 0 {
		configJSON = config.Config
	} else {
		// Default empty configuration
		configJSON = json.RawMessage("{}")
	}

	// Load the plugin using the SDK with configuration passed via RPC
	plugin, err := sdk.LoadPluginWithConfig(config.BinaryPath, configJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to load RPC plugin from %s: %w", config.BinaryPath, err)
	}

	return plugin, nil
}

// loadLegacyMaximPlugin loads the maxim plugin using the legacy method
// TODO: Update maxim plugin to use standardized constructor
func loadLegacyMaximPlugin() (schemas.Plugin, error) {
	if os.Getenv("MAXIM_LOG_REPO_ID") == "" {
		return nil, fmt.Errorf("maxim log repo id is required to initialize maxim plugin")
	}
	if os.Getenv("MAXIM_API_KEY") == "" {
		return nil, fmt.Errorf("maxim api key is required in environment variable MAXIM_API_KEY to initialize maxim plugin")
	}

	maximPlugin, err := maxim.NewMaximLoggerPlugin(os.Getenv("MAXIM_API_KEY"), os.Getenv("MAXIM_LOG_REPO_ID"))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize maxim plugin: %w", err)
	}

	return maximPlugin, nil
}

// getLoggerForPlugin returns a logger instance for the given plugin
// This can be customized to provide different loggers for different plugins
func getLoggerForPlugin(pluginName string) schemas.Logger {
	// For now, return nil - plugins should handle nil logger gracefully
	// In the future, this could return a custom logger per plugin
	return nil
}

// ValidateStandardizedPlugin validates that a plugin follows the standardized constructor pattern
// This is a development-time helper function
func ValidateStandardizedPlugin(pluginName string, constructor schemas.PluginConstructor) error {
	// Test with empty config
	testConfig := json.RawMessage("{}")
	plugin, err := constructor(testConfig)
	if err != nil {
		return fmt.Errorf("plugin %s constructor failed with empty config: %w", pluginName, err)
	}

	// Verify it implements the interface
	if plugin.GetName() == "" {
		return fmt.Errorf("plugin %s GetName() returns empty string", pluginName)
	}

	// Test logger injection
	plugin.SetLogger(nil) // Should not panic

	return nil
}
