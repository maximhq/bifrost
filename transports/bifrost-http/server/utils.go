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

// updateKeyStatus updates the model discovery status for keys or providers based on key statuses.
// For keyed providers: updates individual key status
// For keyless providers: updates provider-level status
func (s *BifrostHTTPServer) updateKeyStatus(
	ctx context.Context,
	keyStatuses []schemas.KeyStatus,
) {
	if s.Config == nil || s.Config.ConfigStore == nil || len(keyStatuses) == 0 {
		return
	}

	// Update each key/provider status individually
	for _, ks := range keyStatuses {
		errorMsg := ""
		if ks.Error != nil && ks.Error.Error != nil {
			errorMsg = ks.Error.Error.Message
		}

		if err := s.Config.ConfigStore.UpdateStatus(ctx, ks.Provider, ks.KeyID, string(ks.Status), errorMsg); err != nil {
			target := ks.KeyID
			if target == "" {
				target = string(ks.Provider)
			}
			logger.Error("failed to update model discovery status for %s: %v", target, err)
			continue // Skip in-memory update if DB update failed
		}

		s.Config.Mu.Lock()

		providerConfig, exists := s.Config.Providers[ks.Provider]
		if !exists {
			s.Config.Mu.Unlock()
			logger.Warn("provider %s not found in memory during status update", ks.Provider)
			continue
		}

		// Find and update the specific key in the Keys slice
		updated := false
		for i := range providerConfig.Keys {
			if providerConfig.Keys[i].ID == ks.KeyID {
				// Update Status and Description fields
				providerConfig.Keys[i].Status = ks.Status
				providerConfig.Keys[i].Description = errorMsg
				updated = true
				break
			}
		}

		if updated {
			// Write the modified config back to the map
			s.Config.Providers[ks.Provider] = providerConfig
			logger.Debug("updated in-memory status for key %s of provider %s", ks.KeyID, ks.Provider)
		} else {
			logger.Warn("key %s not found in provider %s during in-memory update", ks.KeyID, ks.Provider)
		}

		s.Config.Mu.Unlock()
	}
}
