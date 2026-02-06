# Bifrost WASM Plugin (TypeScript/AssemblyScript)

A comprehensive example of a Bifrost plugin written in TypeScript and compiled to WebAssembly using AssemblyScript. This plugin demonstrates proper structure definitions, JSON parsing, context handling, and request/response modification patterns.

## Prerequisites

### Node.js Installation

Node.js is required to run AssemblyScript:

**macOS:**
```bash
brew install node
```

**Linux:**
```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs
```

**Other platforms:**
See [Node.js Downloads](https://nodejs.org/en/download/)

## Building

```bash
# Install dependencies and build
make build

# Build with debug info
make build-debug

# Clean build artifacts
make clean
```

The compiled plugin will be at `build/hello-world.wasm`.

## File Structure

```
assembly/
├── index.ts      # Plugin implementation (hooks)
├── memory.ts     # Memory management utilities
├── types.ts      # Type definitions (mirrors Go SDK)
└── tsconfig.json # AssemblyScript config
```

## Plugin Structure

WASM plugins must export the following functions:

| Export | Signature | Description |
|--------|-----------|-------------|
| `malloc` | `(size: u32) -> u32` | Allocate memory for host to write data |
| `free` | `(ptr: u32)` | Free allocated memory |
| `get_name` | `() -> u64` | Returns packed ptr+len of plugin name |
| `init` | `(config_ptr, config_len: u32) -> i32` | Initialize with config (optional) |
| `http_intercept` | `(input_ptr, input_len: u32) -> u64` | HTTP transport intercept |
| `pre_hook` | `(input_ptr, input_len: u32) -> u64` | Pre-request hook |
| `post_hook` | `(input_ptr, input_len: u32) -> u64` | Post-response hook |
| `cleanup` | `() -> i32` | Cleanup resources (0 = success) |

### Return Value Format

Functions returning data use a packed `u64` format:
- Upper 32 bits: pointer to data in WASM memory
- Lower 32 bits: length of data

## Data Structures

This plugin uses `json-as` with `@json` decorators for automatic JSON serialization. All structures mirror the Go SDK types:

### Context

```typescript
@json
class BifrostContext {
  request_id: string = ''        // Unique request identifier
  plugin_processed: string = ''  // Custom plugin values
  plugin_name: string = ''
}
```

### HTTP Transport Types

```typescript
@json
class HTTPRequest {
  method: string = ''      // GET, POST, etc.
  path: string = ''        // /v1/chat/completions
  body: string = ''        // base64 encoded
}

@json
class HTTPResponse {
  status_code: i32 = 200   // HTTP status code
  body: string = ''        // base64 encoded
}
```

### Chat Completion Types

```typescript
@json
class ChatMessage {
  role: string = ''        // "user", "assistant", "system", "tool"
  content: string = ''
  name: string = ''
  tool_call_id: string = ''
}

@json
class ChatParameters {
  temperature: f64 = 0
  max_completion_tokens: i32 = 0
  top_p: f64 = 0
}

@json
class BifrostChatRequest {
  provider: string = ''    // "openai", "anthropic", etc.
  model: string = ''       // "gpt-4", "claude-3", etc.
  input: ChatMessage[] = []
  params: ChatParameters = new ChatParameters()
}
```

### Response Types

```typescript
@json
class LLMUsage {
  prompt_tokens: i32 = 0
  completion_tokens: i32 = 0
  total_tokens: i32 = 0
}

@json
class ResponseChoice {
  index: i32 = 0
  message: ChatMessage = new ChatMessage()
  finish_reason: string = 'stop'  // "stop", "length", "tool_calls"
}

@json
class BifrostChatResponse {
  id: string = ''
  model: string = ''
  choices: ResponseChoice[] = []
  usage: LLMUsage = new LLMUsage()
}
```

### Error Types

```typescript
@json
class ErrorField {
  message: string = ''
  type: string = ''        // "rate_limit", "auth_error", etc.
  code: string = ''        // "429", "401", etc.
}

@json
class BifrostError {
  error: ErrorField = new ErrorField()
  status_code: i32 = 0
}
```

### Short Circuit

```typescript
@json
class LLMPluginShortCircuit {
  response: BifrostResponse | null = null  // Success short-circuit
  error: BifrostError | null = null        // Error short-circuit
}
```

## Hook Input/Output Structures

### http_intercept

**Input:**
```json
{
  "context": { "request_id": "abc-123" },
  "request": {
    "method": "POST",
    "path": "/v1/chat/completions",
    "headers": { "Content-Type": "application/json" },
    "query": {},
    "body": "<base64-encoded>"
  }
}
```

**Output:**
```json
{
  "context": { "request_id": "abc-123", "custom_key": "value" },
  "request": {},
  "response": { "status_code": 200, "headers": {}, "body": "<base64>" },
  "has_response": false,
  "error": ""
}
```

### pre_hook

**Input:**
```json
{
  "context": { "request_id": "abc-123" },
  "request": {
    "provider": "openai",
    "model": "gpt-4",
    "input": [{ "role": "user", "content": "Hello" }],
    "params": { "temperature": 0.7 }
  }
}
```

**Output:**
```json
{
  "context": { "request_id": "abc-123", "plugin_processed": "true" },
  "request": {},
  "short_circuit": {
    "response": { "chat_response": { ... } }
  },
  "has_short_circuit": false,
  "error": ""
}
```

### post_hook

**Input:**
```json
{
  "context": { "request_id": "abc-123", "plugin_processed": "true" },
  "response": {
    "chat_response": {
      "id": "chatcmpl-123",
      "model": "gpt-4",
      "choices": [{ "index": 0, "message": { "role": "assistant", "content": "Hi!" } }],
      "usage": { "prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15 }
    }
  },
  "error": {},
  "has_error": false
}
```

**Output:**
```json
{
  "context": { "request_id": "abc-123", "post_hook_completed": "true" },
  "response": {},
  "error": {},
  "has_error": false,
  "hook_error": ""
}
```

## Usage Examples

### Modifying Context

```typescript
import { JSON } from 'json-as'

export function pre_hook(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<PreHookInput>(inputJson)
  
  const output = new PreHookOutput()
  output.context = input.context
  
  // Add custom values to context
  output.context.plugin_processed = 'true'
  output.context.plugin_name = 'my-plugin'
  
  return writeString(JSON.stringify(output))
}
```

### Short-Circuit with Mock Response

```typescript
import { JSON } from 'json-as'

export function pre_hook(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<PreHookInput>(inputJson)
  
  // Check if this should be mocked
  const model = input.request.model
  if (model === 'mock-model') {
    const output = new PreHookOutput()
    output.context = input.context
    output.has_short_circuit = true
    output.short_circuit = new LLMPluginShortCircuit()
    
    // Build mock response
    const mockResponse = new BifrostResponse()
    mockResponse.chat_response = new BifrostChatResponse()
    mockResponse.chat_response!.id = 'mock-' + input.context.request_id
    mockResponse.chat_response!.model = 'mock-model'
    
    const choice = new ResponseChoice()
    choice.message.role = 'assistant'
    choice.message.content = 'This is a mock response!'
    mockResponse.chat_response!.choices.push(choice)
    
    mockResponse.chat_response!.usage.prompt_tokens = 10
    mockResponse.chat_response!.usage.completion_tokens = 15
    mockResponse.chat_response!.usage.total_tokens = 25
    
    output.short_circuit!.response = mockResponse
    return writeString(JSON.stringify(output))
  }
  
  // Pass through
  const output = new PreHookOutput()
  output.context = input.context
  return writeString(JSON.stringify(output))
}
```

### Short-Circuit with Error

```typescript
import { JSON } from 'json-as'

export function pre_hook(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<PreHookInput>(inputJson)
  
  // Check rate limit (example)
  if (shouldRateLimit(input.context.request_id)) {
    const output = new PreHookOutput()
    output.context = input.context
    output.has_short_circuit = true
    output.short_circuit = new LLMPluginShortCircuit()
    
    const error = new BifrostError()
    error.error.message = 'Rate limit exceeded'
    error.error.type = 'rate_limit'
    error.error.code = '429'
    error.status_code = 429
    
    output.short_circuit!.error = error
    return writeString(JSON.stringify(output))
  }
  
  // Pass through
  const output = new PreHookOutput()
  output.context = input.context
  return writeString(JSON.stringify(output))
}
```

### Modifying Responses in post_hook

```typescript
import { JSON } from 'json-as'

export function post_hook(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<PostHookInput>(inputJson)
  
  const output = new PostHookOutput()
  output.context = input.context
  
  // Handle errors
  if (input.has_error && input.error !== null) {
    output.has_error = true
    output.error = input.error
    // Could modify error here if needed
    return writeString(JSON.stringify(output))
  }
  
  // Modify response
  if (input.response !== null && input.response!.chat_response !== null) {
    output.response = input.response
    // Could add logging, metrics, or modify response here
  }
  
  return writeString(JSON.stringify(output))
}
```

## Usage with Bifrost

Configure the plugin in your Bifrost config:

```json
{
  "plugins": [
    {
      "path": "/path/to/hello-world.wasm",
      "name": "hello-world-wasm-typescript",
      "enabled": true,
      "config": {
        "custom_option": "value"
      }
    }
  ]
}
```

## AssemblyScript Notes

AssemblyScript is similar to TypeScript but with some differences:

1. **Types are required**: All variables must have explicit types
2. **No closures**: Functions cannot capture variables from outer scope
3. **Limited stdlib**: Not all JavaScript/TypeScript features are available
4. **Strict null handling**: Null checks are required
5. **JSON via json-as**: Uses the `json-as` package with `@json` decorators for serialization

This plugin uses `json-as` for JSON parsing/serialization:

```typescript
import { JSON } from 'json-as'

@json
class MyClass {
  name: string = ''
  value: i32 = 0
}

// Parse JSON
const obj = JSON.parse<MyClass>('{"name":"test","value":42}')

// Stringify to JSON
const json = JSON.stringify(obj)
```

See [AssemblyScript Documentation](https://www.assemblyscript.org/introduction.html) and [json-as Documentation](https://github.com/JairusSW/as-json) for more details.

## Benefits

1. **Familiar syntax**: TypeScript-like syntax for JS/TS developers
2. **Cross-platform**: Single `.wasm` binary runs on any OS/architecture
3. **Security**: WASM provides sandboxed execution
4. **Type Safety**: Strongly typed structures catch errors at compile time
5. **npm ecosystem**: Can use npm for dependency management
