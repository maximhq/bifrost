# HTTP-Transport-Only Plugin Example

This example demonstrates a plugin that only implements the `HTTPTransportPlugin` interface for HTTP-layer request/response interception.

## Features

- **HTTPTransportPreHook**: Intercepts HTTP requests before they enter Bifrost core
  - Authentication validation
  - Rate limiting (in-memory, per API key)
  - Request validation (size limits)
  - Custom header injection
  - Request short-circuiting for auth failures
  
- **HTTPTransportPostHook**: Intercepts HTTP responses after Bifrost core processing
  - CORS header injection
  - Security headers
  - Request duration tracking
  - Error response enrichment
  - Response logging

## Use Cases

- **Security**
  - Authentication/Authorization
  - API key validation
  - Request sanitization
  
- **Rate Limiting**
  - Per-user limits
  - Per-endpoint limits
  - Burst protection
  
- **Observability**
  - Request/response logging
  - Performance monitoring
  - Access tracking
  
- **Compliance**
  - CORS enforcement
  - Security headers
  - Request/response auditing

## Building

```bash
make build
```

This creates `build/http-transport-only.so`

## Configuration

Add to your Bifrost config:

```json
{
  "plugins": [
    {
      "path": "/path/to/http-transport-only.so",
      "name": "http-transport-only",
      "display_name": "Security & Rate Limiting",
      "enabled": true,
      "type": "http_transport",
      "config": {
        "require_auth": true,
        "rate_limit": 100,
        "rate_window": 60,
        "max_body_size": 1048576
      }
    }
  ]
}
```

**Note:** 
- `name` is the system identifier (from `GetName()`) and is **not editable**
- `display_name` is shown in the UI and is **editable** by users

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `require_auth` | boolean | `true` | Enable/disable authentication header enforcement |
| `rate_limit` | integer | `10` | Maximum requests per window (0 = unlimited) |
| `rate_window` | integer | `60` | Rate limit window in seconds |
| `max_body_size` | integer | `1048576` | Maximum request body size in bytes (0 = unlimited) |

### Example Configurations

**Disable authentication:**
```json
{
  "config": {
    "require_auth": false,
    "rate_limit": 1000
  }
}
```

**Unlimited rate limiting:**
```json
{
  "config": {
    "require_auth": true,
    "rate_limit": 0
  }
}
```

**Strict limits:**
```json
{
  "config": {
    "require_auth": true,
    "rate_limit": 10,
    "rate_window": 60,
    "max_body_size": 512000
  }
}
```

## Notes

- This plugin operates at the HTTP transport layer only
- Works only when using bifrost-http, not when using Bifrost as a Go SDK
- Rate limiter is in-memory (resets on restart)
- For production, consider using Redis for distributed rate limiting
