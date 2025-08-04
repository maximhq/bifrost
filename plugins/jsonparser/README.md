# Streaming JSON Parser Plugin for Bifrost

A simple Bifrost plugin that handles partial JSON chunks in streaming responses by making them valid JSON objects.

## Overview

When using AI providers that stream JSON responses, the individual chunks often contain incomplete JSON that cannot be parsed directly. This plugin automatically detects and fixes partial JSON chunks by adding the necessary closing braces, brackets, and quotes to make them valid JSON.

## Features

- **Automatic JSON Completion**: Detects partial JSON and adds missing closing characters
- **Streaming Only**: Processes only streaming responses (non-streaming responses are ignored)
- **Safe Fallback**: Returns original content if JSON cannot be fixed
- **Zero Dependencies**: Only depends on Go's standard library

## Usage

### Basic Usage

```go
package main

import (
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
    "github.com/maximhq/bifrost/plugins/jsonparser"
)

func main() {
    // Create the JSON parser plugin
    jsonPlugin := jsonparser.NewJsonParserPlugin(true)
    
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
    // in the PostHook for streaming responses only
}
```

### Configuration

The plugin accepts a simple boolean parameter to enable/disable it:

```go
// Enable the plugin
plugin := jsonparser.NewJsonParserPlugin(true)

// Disable the plugin
plugin := jsonparser.NewJsonParserPlugin(false)
```

## How It Works

The plugin implements an optimized `parsePartialJSON` function with the following steps:

1. **Validates Input**: First tries to parse the string as valid JSON
2. **Character Analysis**: If invalid, processes the string character-by-character to track:
   - String boundaries (inside/outside quotes)
   - Escape sequences
   - Opening/closing braces and brackets
3. **Auto-Completion**: Adds missing closing characters in the correct order
4. **Validation**: Verifies the completed JSON is valid
5. **Fallback**: Returns original content if completion fails

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
- Streaming responses only (non-streaming responses are ignored)
- Various JSON completion scenarios
- Edge cases and error conditions

## License

This plugin is part of the Bifrost project and follows the same license terms. 