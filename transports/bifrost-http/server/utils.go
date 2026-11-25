package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

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

// CollectObservabilityLimits builds the name-keyed schemas.ObservabilityLimits map the
// tracer uses to size each plugin's concurrency budget and inject timeout, from the
// generic semaphore_size/inject_timeout fields on each plugin's PluginConfig entry (see
// schemas.PluginConfig) — not from anything the plugin itself declares. A plugin with an
// unset or malformed field is simply absent from the map (or has a zero field), so the
// tracer falls back to its own defaults; config-schema validation (minimum/pattern on
// plugins[].semaphore_size/inject_timeout) is what actually rejects bad values upstream.
func (s *BifrostHTTPServer) CollectObservabilityLimits() map[string]schemas.ObservabilityLimits {
	limits := make(map[string]schemas.ObservabilityLimits, len(s.Config.PluginConfigs))
	for _, cfg := range s.Config.PluginConfigs {
		if cfg == nil || (cfg.SemaphoreSize == nil && cfg.InjectTimeout == nil) {
			continue
		}
		var l schemas.ObservabilityLimits
		if cfg.SemaphoreSize != nil && *cfg.SemaphoreSize > 0 {
			l.SemaphoreSize = *cfg.SemaphoreSize
		}
		if cfg.InjectTimeout != nil && *cfg.InjectTimeout != "" {
			if d, err := time.ParseDuration(*cfg.InjectTimeout); err == nil && d > 0 {
				l.InjectTimeout = d
			} else {
				logger.Warn("plugin %s has an invalid inject_timeout %q, using the tracer default", cfg.Name, *cfg.InjectTimeout)
			}
		}
		limits[cfg.Name] = l
	}
	return limits
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

// currentKeyStatus returns the status and description currently held in memory
// for the target of a KeyStatus update, and whether that target was found.
//
// A keyless provider is addressed by ks.KeyID == "" and carries its status on
// the provider config; a keyed provider carries it on the matching key. found
// is false when the provider is absent, when the key is absent, or when a
// provider-level update arrives for a keyed provider — in every one of those
// cases the caller has nothing to compare against and must not skip the write.
func (s *BifrostHTTPServer) currentKeyStatus(ks schemas.KeyStatus) (status string, description string, found bool) {
	stored, ok := s.Config.LiveStore.Collections.Providers.Get(string(ks.Provider))
	if !ok {
		return "", "", false
	}
	providerConfig := &stored.Config // read only

	if ks.KeyID == "" {
		if providerConfig.CustomProviderConfig == nil || !providerConfig.CustomProviderConfig.IsKeyLess {
			return "", "", false
		}
		return providerConfig.Status, providerConfig.Description, true
	}

	rec, ok := s.Config.LiveStore.Collections.ProviderKeys.Get(ks.KeyID)
	if !ok || rec.Provider != ks.Provider {
		return "", "", false
	}
	return string(rec.Key.Status), rec.Key.Description, true
}

// updateKeyStatus updates the model discovery status for keys or providers based on key statuses.
// For keyed providers: updates individual key status
// For keyless providers: updates provider-level status
//
// Statuses that match what is already stored are dropped before the write.
// ConfigStore.UpdateStatus is an unconditional SQL UPDATE, and list-models runs
// on a background interval on every node, so writing unconditionally would put
// a steady keys x nodes x interval stream of no-op UPDATEs on the keys table
// for rows whose values almost never change. A batch that reports a key more
// than once counts only its last status.
func (s *BifrostHTTPServer) updateKeyStatus(
	ctx context.Context,
	keyStatuses []schemas.KeyStatus,
) {
	if s.Config == nil || s.Config.ConfigStore == nil || len(keyStatuses) == 0 {
		return
	}

	// One batch can report a key more than once, as list-models does when the key succeeds on one
	// page and fails on the next. Memory is re-read once, after the loop, so every status is
	// compared with the value from before the batch: only the last one per key is kept, in order.
	type statusTarget struct {
		provider schemas.ModelProvider
		keyID    string
	}
	last := make(map[statusTarget]int, len(keyStatuses))
	for i, ks := range keyStatuses {
		last[statusTarget{ks.Provider, ks.KeyID}] = i
	}

	// Update each key/provider status individually, then re-read each changed key, or the provider
	// for a keyless provider's own status, once.
	var changed []livestore.Ref
	for i, ks := range keyStatuses {
		if last[statusTarget{ks.Provider, ks.KeyID}] != i {
			continue
		}
		errorMsg := ""
		if ks.Error != nil && ks.Error.Error != nil {
			errorMsg = ks.Error.Error.Message
		}

		// A provider-level status belongs to keyless providers only; a keyed provider carries its
		// status on each key. Memory always matches the database, so the row is left alone too.
		if ks.KeyID == "" {
			if stored, ok := s.Config.LiveStore.Collections.Providers.Get(string(ks.Provider)); ok &&
				(stored.Config.CustomProviderConfig == nil || !stored.Config.CustomProviderConfig.IsKeyLess) {
				logger.Warn("received provider-level status update for keyed provider %s; skipping", ks.Provider)
				continue
			}
		}

		// Compared before the DB write, not after: a status that has not moved
		// needs neither the UPDATE nor the refresh below.
		if currentStatus, currentDesc, found := s.currentKeyStatus(ks); found &&
			currentStatus == string(ks.Status) && currentDesc == errorMsg {
			continue
		}

		if err := s.Config.ConfigStore.UpdateStatus(ctx, ks.Provider, ks.KeyID, string(ks.Status), errorMsg); err != nil {
			target := ks.KeyID
			if target == "" {
				target = string(ks.Provider)
			}
			logger.Error("failed to update model discovery status for %s: %v", target, err)
			continue // Skip the refresh if the DB update failed
		}
		ref := livestore.Ref{Kind: livestore.KindProviderKey, ID: ks.KeyID}
		if ks.KeyID == "" {
			ref = livestore.Ref{Kind: livestore.KindProvider, ID: string(ks.Provider)}
		}
		if !slices.Contains(changed, ref) {
			changed = append(changed, ref)
		}
	}
	// Records are never changed in place: re-read the changed rows so memory holds them as written.
	// This runs in the background, with no caller to report to, so the read retries as a peer's
	// does and a failure is logged.
	if len(changed) == 0 {
		return
	}
	if err := s.Config.LiveStore.Refresh(ctx, changed, lib.DBLookupRetry()...); err != nil {
		logger.Error("failed to refresh %d keys or providers after status updates: %v", len(changed), err)
	}
}
