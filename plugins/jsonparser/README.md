# Streaming JSON Parser Plugin for Bifrost

A simple Bifrost plugin that handles partial JSON chunks in streaming responses by making them valid JSON objects.

## Overview

When using AI providers that stream JSON responses, the individual chunks often contain incomplete JSON that cannot be parsed directly. This plugin automatically detects and fixes partial JSON chunks by adding the necessary closing braces, brackets, and quotes to make them valid JSON.

## Features

- **Automatic JSON Completion**: Detects partial JSON and adds missing closing characters
- **Streaming Only**: Processes only streaming responses (non-streaming responses are ignored)
- **Flexible Usage Modes**: Supports two usage types for different deployment scenarios
- **Safe Fallback**: Returns original content if JSON cannot be fixed
- **Memory Leak Prevention**: Automatic cleanup of stale accumulated content with configurable intervals
- **Zero Dependencies**: Only depends on Go's standard library

## Usage

### Usage Types

The plugin supports two usage types:

1. **AllRequests**: Processes all streaming responses automatically
2. **PerRequest**: Processes only when explicitly enabled via request context


```go
package main

import (
    "time"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
    "github.com/maximhq/bifrost/plugins/jsonparser"
)

func main() {
    // Create the JSON parser plugin for all requests
    jsonPlugin := jsonparser.NewJsonParserPlugin(jsonparser.PluginConfig{
        Usage:           jsonparser.AllRequests,
        CleanupInterval: 2 * time.Minute,  // Cleanup every 2 minutes
        MaxAge:          10 * time.Minute,  // Remove entries older than 10 minutes
    })
    
    // Initialize Bifrost with the plugin
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: &MyAccount{},
        Plugins: []schemas.Plugin{
            jsonPlugin,
        },
    })
    
    if err != nil {
        panic(err)
    }
    
    // Use the client normally - JSON parsing happens automatically
    // in the PostHook for all streaming responses
}
```

### PerRequest Mode

```go
package main

import (
    "context"
    "time"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
    "github.com/maximhq/bifrost/plugins/jsonparser"
)

func main() {
    // Create the JSON parser plugin for per-request control
    jsonPlugin := jsonparser.NewJsonParserPlugin(jsonparser.PluginConfig{
        Usage:           jsonparser.PerRequest,
        CleanupInterval: 2 * time.Minute,  // Cleanup every 2 minutes
        MaxAge:          10 * time.Minute,  // Remove entries older than 10 minutes
    })
    
    // Initialize Bifrost with the plugin
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: &MyAccount{},
        Plugins: []schemas.Plugin{
            jsonPlugin,
        },
    })
    
    if err != nil {
        panic(err)
    }

    ctx := context.WithValue(context.Background(), jsonparser.EnableStreamingJSONParser, true)
    
    // Enable JSON parsing for specific requests
    stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, request)
    if bifrostErr != nil {
            // handle error
    }
    for chunk := range stream {
        _ = chunk // handle each streaming chunk
    }
}
```

### Configuration

```go
// Custom cleanup configuration
plugin := jsonparser.NewJsonParserPlugin(jsonparser.PluginConfig{
    Usage:           jsonparser.AllRequests,
    CleanupInterval: 2 * time.Minute,  // Cleanup every 2 minutes
    MaxAge:          10 * time.Minute,  // Remove entries older than 10 minutes
})
```

#### Default Values

- **CleanupInterval**: 5 minutes (how often to run cleanup)
- **MaxAge**: 30 minutes (how old entries can be before cleanup)
- **Usage**: Must be specified (AllRequests or PerRequest)

### Context Key for PerRequest Mode

When using `PerRequest` mode, the plugin checks for the context key `jsonparser.EnableStreamingJSONParser` with a boolean value:

- `true`: Enable JSON parsing for this request
- `false`: Disable JSON parsing for this request
- Key not present: Disable JSON parsing for this request

**Example:**

```go
import (
    "context"

    "github.com/maximhq/bifrost/plugins/jsonparser"
)

// Enable JSON parsing for this request
ctx := context.WithValue(context.Background(), jsonparser.EnableStreamingJSONParser, true)

// Disable JSON parsing for this request
ctx := context.WithValue(context.Background(), jsonparser.EnableStreamingJSONParser, false)

// No context key - JSON parsing disabled (default behavior)
ctx := context.Background()
```

## How It Works

The plugin implements an optimized `parsePartialJSON` function with the following steps:

1. **Usage Check**: Determines if processing should occur based on usage type and context
2. **Validates Input**: First tries to parse the string as valid JSON
3. **Character Analysis**: If invalid, processes the string character-by-character to track:
   - String boundaries (inside/outside quotes)
   - Escape sequences
   - Opening/closing braces and brackets
4. **Auto-Completion**: Adds missing closing characters in the correct order
5. **Validation**: Verifies the completed JSON is valid
6. **Fallback**: Returns original content if completion fails

### Memory Management

The plugin automatically manages memory by:

1. **Accumulating Content**: Stores partial JSON chunks with timestamps for each request
2. **Periodic Cleanup**: Runs a background goroutine that removes stale entries based on `MaxAge`
3. **Request Completion**: Automatically clears accumulated content when requests complete successfully
4. **Configurable Intervals**: Allows customization of cleanup frequency and retention periods

### Example Transformations

| Input | Output |
|-------|--------|
| `{"name": "John"` | `{"name": "John"}` |
| `["apple", "banana"` | `["apple", "banana"]` |
| `{"user": {"name": "John"` | `{"user": {"name": "John"}}` |
| `{"message": "Hello\nWorld"` | `{"message": "Hello\nWorld"}` |
| `""` (empty string) | `{}` |
| `"   "` (whitespace only) | `{}` |

## Testing

Run the test suite:

```bash
cd plugins/jsonparser
go test -v
```

The tests cover:
- Plugin interface compliance
- Both usage types (AllRequests and PerRequest)
- Context-based enabling/disabling
- Streaming responses only (non-streaming responses are ignored)
- Various JSON completion scenarios
- Edge cases and error conditions
- Memory cleanup functionality with real and simulated requests
- Configuration options and default values

## License

This plugin is part of the Bifrost project and follows the same license terms. 