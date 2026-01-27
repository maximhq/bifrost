package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/litellmcompat"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/otel"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/maximhq/bifrost/plugins/telemetry"
)

// isBuiltinPlugin checks if a plugin is a built-in plugin
func isBuiltinPlugin(name string) bool {
	return name == telemetry.PluginName ||
		name == logging.PluginName ||
		name == governance.PluginName ||
		name == litellmcompat.PluginName ||
		name == maxim.PluginName ||
		name == semanticcache.PluginName ||
		name == otel.PluginName
}

// GetDefaultConfigDir returns the OS-specific default configuration directory for Bifrost.
// This follows standard conventions:
// - Linux/macOS: ~/.config/bifrost
// - Windows: %APPDATA%\bifrost
// - If appDir is provided (non-empty), it returns that instead
func GetDefaultConfigDir(appDir string) string {
	// If appDir is provided, use it directly
	if appDir != "" {
		return appDir
	}

	// Get OS-specific config directory
	var configDir string
	switch runtime.GOOS {
	case "windows":
		// Windows: %APPDATA%\bifrost
		if appData := os.Getenv("APPDATA"); appData != "" {
			configDir = filepath.Join(appData, "bifrost")
		} else {
			// Fallback to user home directory
			if homeDir, err := os.UserHomeDir(); err == nil {
				configDir = filepath.Join(homeDir, "AppData", "Roaming", "bifrost")
			}
		}
	default:
		// Linux, macOS and other Unix-like systems: ~/.config/bifrost
		if homeDir, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(homeDir, ".config", "bifrost")
		}
	}

	// If we couldn't determine the config directory, fall back to current directory
	if configDir == "" {
		configDir = "./bifrost-data"
	}

	return configDir
}

// registerPluginWithStatus instantiates, registers, and updates status for a plugin (used by builtin plugins)
func (s *BifrostHTTPServer) registerPluginWithStatus(ctx context.Context, name string, path *string, config any, failOnError bool) error {
	plugin, err := InstantiatePlugin(ctx, name, path, config, s.Config)
	if err != nil {
		logger.Error("failed to initialize %s plugin: %v", name, err)
		// Use name since plugin may be nil when InstantiatePlugin returns an error
		s.Config.UpdatePluginOverallStatus(name, name, schemas.PluginStatusError,
			[]string{fmt.Sprintf("error initializing %s plugin: %v", name, err)}, []schemas.PluginType{})
		if failOnError {
			return err
		}
		return nil
	}

	// Ensure plugin is not nil before using it (defensive check)
	if plugin == nil {
		logger.Error("plugin %s instantiated but returned nil", name)
		s.Config.UpdatePluginOverallStatus(name, name, schemas.PluginStatusError,
			[]string{fmt.Sprintf("plugin %s instantiated but returned nil", name)}, []schemas.PluginType{})
		if failOnError {
			return fmt.Errorf("plugin %s instantiated but returned nil", name)
		}
		return nil
	}

	s.Config.ReloadPlugin(plugin)
	s.Config.UpdatePluginOverallStatus(name, name, schemas.PluginStatusActive,
		[]string{fmt.Sprintf("%s plugin initialized successfully", name)}, InferPluginTypes(plugin))
	return nil
}

// CollectObservabilityPlugins gathers all loaded plugins that implement ObservabilityPlugin interface
func (s *BifrostHTTPServer) CollectObservabilityPlugins() []schemas.ObservabilityPlugin {
	var observabilityPlugins []schemas.ObservabilityPlugin

	// Check LLM plugins
	for _, plugin := range s.Config.GetLoadedLLMPlugins() {
		if observabilityPlugin, ok := plugin.(schemas.ObservabilityPlugin); ok {
			observabilityPlugins = append(observabilityPlugins, observabilityPlugin)
		}
	}

	// Check MCP plugins
	for _, plugin := range s.Config.GetLoadedMCPPlugins() {
		if observabilityPlugin, ok := plugin.(schemas.ObservabilityPlugin); ok {
			observabilityPlugins = append(observabilityPlugins, observabilityPlugin)
		}
	}

	return observabilityPlugins
}

// MarshalPluginConfig marshals the plugin configuration
func MarshalPluginConfig[T any](source any) (*T, error) {
	// If its a *T, then we will confirm
	if config, ok := source.(*T); ok {
		return config, nil
	}
	// Initialize a new instance for unmarshaling
	config := new(T)
	// If its a map[string]any, then we will JSON parse and confirm
	if configMap, ok := source.(map[string]any); ok {
		configString, err := sonic.Marshal(configMap)
		if err != nil {
			return nil, err
		}
		if err := sonic.Unmarshal([]byte(configString), config); err != nil {
			return nil, err
		}
		return config, nil
	}
	// If its a string, then we will JSON parse and confirm
	if configStr, ok := source.(string); ok {
		if err := sonic.Unmarshal([]byte(configStr), config); err != nil {
			return nil, err
		}
		return config, nil
	}
	return nil, fmt.Errorf("invalid config type")
}
