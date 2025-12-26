- feat: adds new tracing framework for allowing plugins to enable e2e tracing

### BREAKING CHANGES

- **DynamicPlugin: TransportInterceptor replaced with HTTPTransportMiddleware**

  The `DynamicPlugin` loader now expects plugins to export `HTTPTransportMiddleware` instead of `TransportInterceptor`.

  **Old symbol lookup (removed in framework v1.2.0):**
  ```go
  plugin.Lookup("TransportInterceptor")
  // Expected: func(ctx *BifrostContext, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error)
  ```

  **New symbol lookup (framework v1.2.0+):**
  ```go
  plugin.Lookup("HTTPTransportMiddleware")
  // Expected: func() BifrostHTTPMiddleware
  ```

  **Impact on dynamic plugins (.so files):**
  - Plugins compiled for core v1.2.x will fail to load with error: `plugin: symbol HTTPTransportMiddleware not found`
  - Recompile all dynamic plugins against core v1.3.0+ and framework v1.2.0+

  See [Plugin Migration Guide](/docs/plugins/migration-guide) for migration instructions.