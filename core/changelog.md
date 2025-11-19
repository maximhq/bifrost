- feat: added code mode to mcp
- feat: added health monitoring to mcp
- feat: added responses format tool execution support to mcp
- feat: adds central tracer for e2e tracing

### BREAKING CHANGES

- **Plugin Interface: TransportInterceptor removed, replaced with HTTPTransportMiddleware**

  The `TransportInterceptor` method has been removed from the `Plugin` interface in `schemas/plugin.go`. All plugins must now implement `HTTPTransportMiddleware()` instead.

  **Old API (removed in core v1.3.0):**
  ```go
  TransportInterceptor(ctx *BifrostContext, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error)
  ```

  **New API (core v1.3.0+):**
  ```go
  HTTPTransportMiddleware() BifrostHTTPMiddleware
  // where BifrostHTTPMiddleware = func(next fasthttp.RequestHandler) fasthttp.RequestHandler
  ```

  **Key changes:**
  - Method renamed: `TransportInterceptor` -> `HTTPTransportMiddleware`
  - Return type changed: Now returns a middleware function instead of modified headers/body
  - New import required: `github.com/valyala/fasthttp`
  - Flow control: Must call `next(ctx)` explicitly to continue the middleware chain
  - New capability: Can now intercept and modify responses (not just requests)

  **Migration for plugin consumers:**
  1. Update your plugin to implement `HTTPTransportMiddleware()` instead of `TransportInterceptor()`
  2. If your plugin doesn't need HTTP transport interception, return `nil` from `HTTPTransportMiddleware()`
  3. Update tests to verify the new middleware signature

  See [Plugin Migration Guide](/docs/plugins/migration-guide) for complete instructions and code examples.