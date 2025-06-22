package plugins

import "encoding/json"

// PluginSource defines where a plugin comes from
type PluginSource string

const (
	PluginSourceLocal   PluginSource = "local"   // Local build (for development) - requires binary_path
	PluginSourcePackage PluginSource = "package" // Go package from repository (for production)
)

// PluginConfig represents the configuration for a single plugin
// Only supports two types: local builds and Go packages
type PluginConfig struct {
	Name   string       `json:"name"`   // Plugin name for identification
	Source PluginSource `json:"source"` // Plugin source: "local" or "package"

	// For local source (development)
	PluginPath string `json:"plugin_path,omitempty"` // Path to plugin directory (preferred for local source)

	// For package source (production)
	Package string `json:"package,omitempty"` // Go module path (required for package source)
	Version string `json:"version,omitempty"` // Version for package source (optional, defaults to latest)

	// Common configuration
	Config  json.RawMessage   `json:"config,omitempty"`   // Plugin-specific configuration (JSON)
	EnvVars map[string]string `json:"env_vars,omitempty"` // Environment variables for plugin
	Enabled bool              `json:"enabled"`            // Whether the plugin is enabled
}
