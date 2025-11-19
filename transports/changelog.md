- feat: added code mode to mcp
- feat: added health monitoring to mcp
- feat: added responses format tool execution support to mcp
- refactor: governance plugin refactored for extensibility and optimization
- feat: new MCP gateway (server including) along with code mode
- feat: new e2e tracing
- fix: gemini thought signature handling in multi-turn conversations

### BREAKING CHANGES

- **Plugin Interface: TransportInterceptor removed, replaced with HTTPTransportMiddleware**

  The `TransportInterceptor` function has been removed from the plugin interface. Plugins using HTTP transport interception must migrate to `HTTPTransportMiddleware`.

  **Migration summary:**
  ```
  // v1.3.x (removed)
  TransportInterceptor(ctx *BifrostContext, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error)

  // v1.4.x+ (new)
  HTTPTransportMiddleware() BifrostHTTPMiddleware
  // where BifrostHTTPMiddleware = func(next fasthttp.RequestHandler) fasthttp.RequestHandler
  ```

  **Key API changes:**
  - Function renamed: `TransportInterceptor` -> `HTTPTransportMiddleware`
  - Signature changed: Now returns a middleware wrapper instead of accepting/returning header/body maps
  - Added dependency: Requires `github.com/valyala/fasthttp` import
  - Flow control: Must explicitly call `next(ctx)` to continue the chain

  See [Plugin Migration Guide](/docs/plugins/migration-guide) for complete migration instructions and code examples.
