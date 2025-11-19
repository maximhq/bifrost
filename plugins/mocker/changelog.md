- chore: upgraded versions of core to 1.3.0 and framework to 1.2.0

### BREAKING CHANGES

- **Plugin Interface: TransportInterceptor replaced with HTTPTransportMiddleware**

  This plugin now implements `HTTPTransportMiddleware()` instead of `TransportInterceptor()` to comply with core v1.3.0.

  **What changed:**
  - Old: `TransportInterceptor(ctx, url, headers, body) (headers, body, error)`
  - New: `HTTPTransportMiddleware() BifrostHTTPMiddleware`

  **For plugin consumers:**
  - If you import this plugin directly, no code changes are required
  - If you extend this plugin, update your implementation to use `HTTPTransportMiddleware()`
  - Recompile any code that depends on this plugin against core v1.3.0+ and framework v1.2.0+

  See [Plugin Migration Guide](/docs/plugins/migration-guide) for details.