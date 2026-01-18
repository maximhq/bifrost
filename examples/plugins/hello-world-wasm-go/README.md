# Hello World WASM Plugin

A minimal example of a Bifrost plugin written in Go and compiled to WebAssembly using TinyGo.

## Prerequisites

### TinyGo Installation

TinyGo is required to compile Go code to WebAssembly with a small binary size.

**macOS:**
```bash
brew install tinygo
```

**Linux (Ubuntu/Debian):**
```bash
wget https://github.com/tinygo-org/tinygo/releases/download/v0.32.0/tinygo_0.32.0_amd64.deb
sudo dpkg -i tinygo_0.32.0_amd64.deb
```

**Other platforms:**
See [TinyGo Installation Guide](https://tinygo.org/getting-started/install/)

## Building

```bash
# Build the WASM plugin
make build

# Build with size optimizations
make build-optimized

# Clean build artifacts
make clean
```

The compiled plugin will be at `build/hello-world.wasm`.

## Plugin Structure

WASM plugins must export the following functions:

| Export | Signature | Description |
|--------|-----------|-------------|
| `plugin_malloc` | `(size: u32) -> u32` | Allocate memory for host to write data (or `malloc` for non-TinyGo) |
| `plugin_free` | `(ptr: u32)` | Free allocated memory (optional, or `free` for non-TinyGo) |
| `get_name` | `() -> u64` | Returns packed ptr+len of plugin name |
| `http_transport_intercept` | `(ctx_ptr, ctx_len, req_ptr, req_len: u32) -> u64` | HTTP transport intercept |
| `pre_hook` | `(ctx_ptr, ctx_len, req_ptr, req_len: u32) -> u64` | Pre-request hook |
| `post_hook` | `(ctx_ptr, ctx_len, resp_ptr, resp_len, err_ptr, err_len: u32) -> u64` | Post-response hook |
| `cleanup` | `() -> i32` | Cleanup resources (0 = success) |
| `init` | `(config_ptr, config_len: u32) -> i32` | Initialize with config (optional) |

### Return Value Format

Functions returning data use a packed `u64` format:
- Upper 32 bits: pointer to data in WASM memory
- Lower 32 bits: length of data

### Data Exchange

All complex data is exchanged as JSON:

**HTTPTransportIntercept Input:**
- `ctx`: `{"request_id": "..."}` (context info)
- `req`: HTTP request JSON
```json
{
  "method": "POST",
  "path": "/v1/chat/completions",
  "headers": {"Content-Type": "application/json"},
  "query": {},
  "body": "base64-encoded-body"
}
```

**HTTPTransportIntercept Output:**
```json
{
  "response": null,
  "error": ""
}
```
To short-circuit, return a response:
```json
{
  "response": {
    "status_code": 401,
    "headers": {"Content-Type": "application/json"},
    "body": "base64-encoded-body"
  },
  "error": ""
}
```

**PreLLMHook Input:**
- `ctx`: `{"request_id": "..."}` (context info)
- `req`: Bifrost request JSON

**PreLLMHook Output:**
```json
{
  "request": { ... },
  "short_circuit": null,
  "error": ""
}
```

**PostLLMHook Input:**
- `ctx`: Context JSON
- `resp`: Bifrost response JSON
- `err`: Bifrost error JSON (or null)

**PostLLMHook Output:**
```json
{
  "response": { ... },
  "bifrost_error": null,
  "error": ""
}
```

## Usage with Bifrost

Configure the plugin in your Bifrost config:

```json
{
  "plugins": [
    {
      "path": "/path/to/hello-world.wasm",
      "name": "hello-world-wasm",
      "enabled": true
    }
  ]
}
```

Or load from URL:

```json
{
  "plugins": [
    {
      "path": "https://example.com/plugins/hello-world.wasm",
      "name": "hello-world-wasm",
      "enabled": true
    }
  ]
}
```

## Limitations

WASM plugins have some limitations compared to native `.so` plugins:

1. **Performance**: JSON serialization/deserialization adds overhead compared to native plugins.

2. **Memory**: WASM modules have a linear memory model with limited addressing.

3. **TinyGo Constraints**: Some Go standard library features are not available in TinyGo.

## Benefits

1. **Cross-platform**: Single `.wasm` binary runs on any OS/architecture
2. **Security**: WASM provides sandboxed execution
3. **No CGO**: Pure Go compilation, no C dependencies needed on the host
4. **Portability**: Easy to distribute and deploy
5. **Full feature parity**: HTTP transport intercept, PreLLMHook, and PostLLMHook all supported