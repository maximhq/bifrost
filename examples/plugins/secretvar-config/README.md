# SecretVar Config Plugin

This native Go plugin shows how to accept a secret in a custom plugin's config using `schemas.SecretVar` instead of a plain `string`. The `api_key` config field can be set to a literal value, an `env.VAR_NAME` reference, or (with enterprise vault wired up) a `vault.path/to/secret` reference - resolution happens automatically when the config is unmarshaled, no extra code required.

The plugin adds the resolved key as the `x-secretvar-plugin-key` header on every request via `HTTPTransportPreHook`, and implements `MarshalConfigForStorage`/`RedactConfig` so the plugins API never stores or returns the resolved secret:

- `MarshalConfigForStorage` writes `api_key` back as its `env.X`/`vault.X` reference, or the plain literal value if one was configured directly - never the resolved secret.
- `RedactConfig` fully masks the value (`FullyRedacted`, not `Redacted` - `Redacted` would leak the first/last 4 characters of a literal value, which is acceptable for a non-secret field but not for a credential) while keeping the reference visible, e.g. `env.MY_PLUGIN_API_KEY`.

## Build and test

```bash
make test
make build
```

The build produces `build/secretvar-config.so`.

## Configure

```json
{
  "plugins": [
    {
      "enabled": true,
      "name": "secretvar-config",
      "path": "/absolute/path/to/secretvar-config.so",
      "config": {
        "api_key": "env.MY_PLUGIN_API_KEY"
      }
    }
  ]
}
```
