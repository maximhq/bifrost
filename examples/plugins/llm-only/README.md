# LLM-Only Plugin Example

This example demonstrates a plugin that only implements the `LLMPlugin` interface.

## Features

- **PreLLMHook**: Intercepts requests before they reach the LLM provider
  - Logs request details
  - Modifies requests (adds system message)
  - Stores metadata in context
  
- **PostLLMHook**: Intercepts responses after the LLM provider responds
  - Logs response details
  - Accesses context metadata
  - Handles errors

## Use Cases

- Request/response logging
- Adding default system messages
- Request validation
- Response filtering
- Token counting
- Cost tracking

## Building

```bash
make build
```

This creates `build/llm-only.so`

## Configuration

Add to your Bifrost config:

```json
{
  "plugins": [
    {
      "path": "/path/to/llm-only.so",
      "name": "llm-only",
      "display_name": "LLM Request Logger",
      "enabled": true,
      "type": "llm",
      "config": {
        "inject_system_message": true,
        "system_message_text": "You are a helpful assistant.",
        "enable_logging": true,
        "log_requests": true,
        "log_responses": true
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
| `inject_system_message` | boolean | `true` | Enable/disable automatic system message injection |
| `system_message_text` | string | `"You are a helpful assistant..."` | Custom system message to inject |
| `enable_logging` | boolean | `true` | Enable/disable detailed logging |
| `log_requests` | boolean | `true` | Log request details (provider, model) |
| `log_responses` | boolean | `true` | Log response details (ID, choices) |

### Example Configurations

**Minimal logging:**
```json
{
  "config": {
    "enable_logging": false,
    "log_requests": false,
    "log_responses": false
  }
}
```

**Custom system message:**
```json
{
  "config": {
    "inject_system_message": true,
    "system_message_text": "You are a technical expert. Provide detailed, accurate answers."
  }
}
```

**No system message injection:**
```json
{
  "config": {
    "inject_system_message": false
  }
}
```
