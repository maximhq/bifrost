# Virtual Key from Configuration Plugin

This native Go plugin adds the `x-bf-vk` header to incoming HTTP requests when
the header is not already present. The injected value is read from the plugin's
`config.virtual_key` setting.

If the request already contains `x-bf-vk` (with any casing), its value is left
unchanged. The plugin fails to initialize if `virtual_key` is missing or empty.

## Build and test

```bash
make test
make build
```

The build produces `build/virtual-key-from-config.so`.

This example is part of the repository's `go.work`, so local builds use the same
Go version, Bifrost core source, and shared dependency graph as `bifrost-http`.
When building it from a separate repository, pin `github.com/maximhq/bifrost/core`
and the Go toolchain to the exact versions used by the target Bifrost binary.

## Configure

Add the plugin to `config.json`:

```json
{
  "plugins": [
    {
      "enabled": true,
      "name": "virtual-key-from-config",
      "path": "/absolute/path/to/virtual-key-from-config.so",
      "config": {
        "virtual_key": "sk-bf-your-virtual-key"
      }
    }
  ]
}
```

The plugin only exports `HTTPTransportPreAuthHook`; it has no LLM, MCP, post-HTTP,
or stream hooks.

`HTTPTransportPreAuthHook` runs before the transport authenticates the request, which
is what makes the injected key count as a credential. `HTTPTransportPreHook` runs after
authentication, so a virtual key set there is too late to be authenticated against —
use it for everything that is not a credential.
