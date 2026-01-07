package plugins

import "github.com/maximhq/bifrost/core/schemas"

// PluginLoader is the contract for a plugin loader
type PluginLoader interface {
	// LoadPlugin loads a generic plugin from the given path with the provided config
	// Returns a BasePlugin that can be type-asserted to specific plugin interfaces
	LoadPlugin(path string, config any) (schemas.BasePlugin, error)

	// VerifyBasePlugin verifies a plugin at the given path
	// Returns the name of the plugin or an empty string if the plugin is invalid
	// Returns an error if the plugin is invalid
	// This method is used to verify that the plugin is a valid base plugin and has the required symbols
	VerifyBasePlugin(path string) (string, error)
}
