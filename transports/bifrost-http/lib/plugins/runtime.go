package plugins

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/hashicorp/go-plugin"
	"github.com/maximhq/bifrost/core/schemas"
)

// HandshakeConfig for plugin communication
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "BIFROST_PLUGIN",
	MagicCookieValue: "bifrost",
}

// PluginMap is the map of plugins we can dispense
var PluginMap = map[string]plugin.Plugin{
	"plugin": &PluginPlugin{},
}

// LoadPlugin loads a plugin from the given path and returns the plugin instance
func LoadPlugin(pluginPath string) (schemas.Plugin, error) {
	// Check if plugin binary exists
	if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("plugin binary not found at %s", pluginPath)
	}

	// Make sure plugin is executable
	if err := os.Chmod(pluginPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to make plugin executable: %v", err)
	}

	// Create the plugin client
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap,
		Cmd:             exec.Command(pluginPath),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolNetRPC,
		},
	})

	// Connect via RPC
	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("failed to connect to plugin: %v", err)
	}

	// Request the plugin
	raw, err := rpcClient.Dispense("plugin")
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("failed to dispense plugin: %v", err)
	}

	// Type assert to our plugin interface
	pluginInstance, ok := raw.(schemas.Plugin)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("plugin does not implement Plugin interface")
	}

	return pluginInstance, nil
}

// IsPluginBinary checks if a binary is a Bifrost plugin by attempting a quick handshake
func IsPluginBinary(path string) bool {
	// Check if file exists and is executable
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return false
	}

	// Try a quick plugin client connection
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap,
		Cmd:             exec.Command(path),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolNetRPC,
		},
	})
	defer client.Kill()

	// Try to connect - if it fails, it's not a plugin
	rpcClient, err := client.Client()
	if err != nil {
		return false
	}

	// Try to dispense - if it fails, it's not our plugin
	_, err = rpcClient.Dispense("plugin")
	return err == nil
}
