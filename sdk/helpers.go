package sdk

import (
	"encoding/json"
	"log"
	"net/rpc"
	"os"
	"os/exec"

	"github.com/hashicorp/go-plugin"
	"github.com/maximhq/bifrost/core/schemas"
)

// handshakeConfig are used to just do a basic handshake between
// a plugin and host. If the handshake fails, a user friendly error is shown.
// This prevents users from executing bad plugins or executing a plugin
// directory. It is a UX feature, not a security feature.
var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "BIFROST_PLUGIN",
	MagicCookieValue: "hello",
}

// pluginMap is the map of plugins we can dispense.
var pluginMap = map[string]plugin.Plugin{
	"bifrost": &BifrostPlugin{},
}

// BifrostPlugin is the implementation of plugin.Plugin so we can serve/consume this
//
// This has two methods: Server must return an RPC server for this plugin
// type. We construct a BifrostRPCServer for this.
//
// Client must return an implementation of our interface that communicates
// over an RPC client. We return BifrostRPC for this.
//
// Ignore MuxBroker. That is used to create more multiplexed streams on our
// plugin connection and is a more advanced use case.
type BifrostPlugin struct {
	// Impl Injection
	Impl interface{}
}

func (p *BifrostPlugin) Server(*plugin.MuxBroker) (interface{}, error) {
	switch impl := p.Impl.(type) {
	case schemas.Plugin:
		return &BifrostRPCServer{Impl: impl}, nil
	case schemas.PluginConstructor:
		return &PluginFactoryRPCServer{Factory: impl}, nil
	default:
		log.Fatalf("Invalid plugin implementation type: %T", impl)
		return nil, nil
	}
}

func (p *BifrostPlugin) Client(b *plugin.MuxBroker, c *rpc.Client) (interface{}, error) {
	return &BifrostRPC{client: c}, nil
}

// ServePlugin serves a plugin over RPC using HashiCorp's go-plugin
func ServePlugin(pluginImpl schemas.Plugin) {
	// We're a host! Start by launching the plugin process.
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshakeConfig,
		Plugins: map[string]plugin.Plugin{
			"bifrost": &BifrostPlugin{Impl: pluginImpl},
		},
	})
}

// ServePluginFactory serves a plugin factory over RPC
// The factory will be called with configuration during plugin initialization
func ServePluginFactory(factory schemas.PluginConstructor) {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshakeConfig,
		Plugins: map[string]plugin.Plugin{
			"bifrost": &BifrostPlugin{Impl: factory},
		},
	})
}

// LoadPlugin loads a plugin from the given path and returns a Plugin interface
func LoadPlugin(pluginPath string) (schemas.Plugin, error) {
	// We're a host. Start by launching the plugin process.
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
		Cmd:             exec.Command(pluginPath),
	})

	// Connect via RPC
	rpcClient, err := client.Client()
	if err != nil {
		return nil, err
	}

	// Request the plugin
	raw, err := rpcClient.Dispense("bifrost")
	if err != nil {
		return nil, err
	}

	// We should have a Plugin now! This feels like a normal interface
	// implementation but is in fact over an RPC connection.
	pluginInstance := raw.(schemas.Plugin)
	return pluginInstance, nil
}

// LoadPluginWithConfig loads a plugin and initializes it with the given configuration
func LoadPluginWithConfig(pluginPath string, config json.RawMessage) (schemas.Plugin, error) {
	// Load the plugin
	pluginInstance, err := LoadPlugin(pluginPath)
	if err != nil {
		return nil, err
	}

	// Check if it supports RPC initialization
	if rpcPlugin, ok := pluginInstance.(*BifrostRPC); ok {
		// Initialize with configuration via RPC
		if err := rpcPlugin.Initialize(config); err != nil {
			return nil, err
		}
	}

	return pluginInstance, nil
}

// IsPluginBinary checks if the current process is running as a plugin binary
func IsPluginBinary() bool {
	return os.Getenv("BIFROST_PLUGIN") != ""
}
