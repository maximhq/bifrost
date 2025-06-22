# Bifrost Plugin SDK

The Bifrost Plugin SDK enables you to create RPC-based plugins using the battle-tested [HashiCorp go-plugin](https://github.com/hashicorp/go-plugin) system. This allows plugins to run as separate processes, providing isolation, cross-language support, and robust error handling.

## Features

✅ **Process Isolation**: Plugin crashes don't affect the host process  
✅ **Cross-Language Support**: Plugins can be written in any language (via gRPC)  
✅ **Battle-Tested**: Built on HashiCorp's plugin system used by Terraform, Vault, Nomad  
✅ **Easy Development**: Simple interface, just implement `schemas.Plugin`  
✅ **Backward Compatible**: Works alongside existing direct plugin loading  
✅ **Dynamic Loading**: Load plugins at runtime without recompilation

## Quick Start

### 1. Creating a Plugin

```go
package main

import (
    "context"
    "github.com/maximhq/bifrost/core/schemas"
    "github.com/maximhq/bifrost/sdk"
)

type MyPlugin struct{}

func (p *MyPlugin) GetName() string {
    return "my-awesome-plugin"
}

func (p *MyPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    // Your plugin logic here
    return req, nil, nil
}

func (p *MyPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    // Your plugin logic here
    return result, err, nil
}

func (p *MyPlugin) Cleanup() error {
    return nil
}

func main() {
    plugin := &MyPlugin{}
    sdk.ServePlugin(plugin) // This serves the plugin over RPC
}
```

### 2. Building the Plugin

```bash
go build -o my-plugin
```

### 3. Loading the Plugin

```go
package main

import (
    "github.com/maximhq/bifrost/sdk"
)

func main() {
    // Load plugin from binary path
    plugin, err := sdk.LoadPlugin("./my-plugin")
    if err != nil {
        panic(err)
    }

    // Use the plugin exactly like a direct plugin
    name := plugin.GetName()
    // ... use PreHook, PostHook, Cleanup
}
```

## Architecture

```
┌─────────────────┐    RPC     ┌─────────────────┐
│   Host Process  │ ◄─────────► │ Plugin Process  │
│                 │            │                 │
│ ┌─────────────┐ │            │ ┌─────────────┐ │
│ │ BifrostRPC  │ │            │ │   Plugin    │ │
│ │   Client    │ │            │ │ Implementation│ │
│ └─────────────┘ │            │ └─────────────┘ │
└─────────────────┘            └─────────────────┘
```

## API Reference

### Core Functions

#### `sdk.ServePlugin(impl schemas.Plugin)`

Serves a plugin implementation over RPC. Call this in your plugin's `main()` function.

#### `sdk.LoadPlugin(pluginPath string) (schemas.Plugin, error)`

Loads a plugin from a binary path and returns a `schemas.Plugin` interface.

#### `sdk.LoadPluginWithConfig(config *plugin.ClientConfig) (schemas.Plugin, error)`

Loads a plugin with custom configuration for advanced use cases.

#### `sdk.IsPluginBinary() bool`

Checks if the current process is running as a plugin binary.

### Configuration

The SDK uses these default settings:

```go
var Handshake = plugin.HandshakeConfig{
    ProtocolVersion:  1,
    MagicCookieKey:   "BIFROST_PLUGIN",
    MagicCookieValue: "bifrost_plugin_v1",
}
```

## Examples

See the `examples/` directory for complete working examples:

- **`simple-plugin/`**: Basic plugin implementation
- **`plugin-host/`**: Host application that loads and tests plugins

### Running the Examples

```bash
# Build the example plugin
cd examples/simple-plugin
go build -o simple-plugin

# Build the host
cd ../plugin-host
go build -o plugin-host

# Test the plugin
./plugin-host ../simple-plugin/simple-plugin
```

## Integration with Transports

The SDK is designed to work seamlessly with Bifrost transports. You can:

1. **Direct Integration**: Use `sdk.LoadPlugin()` in your transport
2. **Configuration-Based**: Load plugins from config.json paths
3. **Hybrid Mode**: Support both direct and RPC plugins

Example transport integration:

```go
// In your transport
func loadPlugins(config *Config) ([]schemas.Plugin, error) {
    var plugins []schemas.Plugin

    for _, pluginConfig := range config.Plugins {
        if pluginConfig.BinaryPath != "" {
            // Load RPC plugin
            plugin, err := sdk.LoadPlugin(pluginConfig.BinaryPath)
            if err != nil {
                return nil, err
            }
            plugins = append(plugins, plugin)
        }
        // ... handle other plugin types
    }

    return plugins, nil
}
```

## Plugin Development Best Practices

### 1. Error Handling

```go
func (p *MyPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    // Always handle errors gracefully
    if req == nil {
        return nil, nil, fmt.Errorf("request cannot be nil")
    }

    // Your logic here
    return req, nil, nil
}
```

### 2. Resource Cleanup

```go
func (p *MyPlugin) Cleanup() error {
    // Always clean up resources
    if p.connection != nil {
        p.connection.Close()
    }
    return nil
}
```

### 3. Logging

```go
import "log"

func (p *MyPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    log.Printf("[%s] Processing request for model: %s", p.GetName(), req.Model)
    // Plugin logs are automatically forwarded to the host
    return req, nil, nil
}
```

## Troubleshooting

### Plugin Won't Start

- Check that the binary is executable: `chmod +x my-plugin`
- Verify the plugin implements all required methods
- Check for import/dependency issues

### RPC Communication Issues

- Ensure both host and plugin use the same SDK version
- Check firewall/network permissions for Unix sockets
- Verify the handshake configuration matches

### Context Serialization

The SDK automatically handles context serialization by creating fresh contexts for each RPC call. You don't need to worry about context serialization issues.

## Migration from Direct Plugins

Migrating from direct plugin loading to RPC plugins is straightforward:

**Before (Direct):**

```go
plugin := myplugin.NewPlugin()
client, err := bifrost.Init({plugins: []schemas.Plugin{plugin}})
```

**After (RPC):**

```go
plugin, err := sdk.LoadPlugin("./myplugin-binary")
if err != nil {
    panic(err)
}
client, err := bifrost.Init({plugins: []schemas.Plugin{plugin}})
```

The plugin interface remains exactly the same!

## Contributing

The SDK is part of the Bifrost project. Contributions are welcome!

## License

Same as Bifrost core - see LICENSE file.
