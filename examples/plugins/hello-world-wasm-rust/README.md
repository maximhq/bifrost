# Bifrost WASM Plugin (Rust)

A comprehensive example of a Bifrost plugin written in Rust and compiled to WebAssembly. This plugin demonstrates proper structure definitions with serde, JSON parsing, context handling, and request/response modification patterns.

## Prerequisites

### Rust Installation

Install Rust from [rustup.rs](https://rustup.rs/) and add the WASM target:

```bash
# Install Rust (if not already installed)
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh

# Add WASM target
rustup target add wasm32-unknown-unknown
```

### Optional: wasm-opt

For smaller binaries, install `wasm-opt` from [binaryen](https://github.com/WebAssembly/binaryen):

```bash
# macOS
brew install binaryen

# Linux
apt install binaryen
```

## Building

```bash
# Build the WASM plugin
make build

# Build with wasm-opt optimization
make build-optimized

# Clean build artifacts
make clean
```

The compiled plugin will be at `build/hello-world.wasm`.

## File Structure

```
src/
├── lib.rs      # Plugin implementation (hooks)
├── memory.rs   # Memory management utilities
└── types.rs    # Type definitions (mirrors Go SDK)
```

## Plugin Structure

WASM plugins must export the following functions:

| Export | Signature | Description |
|--------|-----------|-------------|
| `malloc` | `(size: u32) -> u32` | Allocate memory for host to write data |
| `free` | `(ptr: u32, size: u32)` | Free allocated memory |
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

This plugin uses `serde` with derive macros for JSON serialization. All structures mirror the Go SDK types:

### Context

```rust
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostContext {
    pub request_id: Option<String>,
    
    // Custom values via HashMap
    #[serde(flatten)]
    pub values: HashMap<String, serde_json::Value>,
}

impl BifrostContext {
    pub fn set_value(&mut self, key: &str, value: impl Into<serde_json::Value>);
    pub fn get_string(&self, key: &str) -> Option<&str>;
    pub fn get_bool(&self, key: &str) -> Option<bool>;
}
```

### HTTP Transport Types

```rust
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPRequest {
    pub method: String,
    pub path: String,
    pub headers: HashMap<String, String>,
    pub query: HashMap<String, String>,
    pub body: String,  // base64 encoded
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPResponse {
    pub status_code: i32,
    pub headers: HashMap<String, String>,
    pub body: String,  // base64 encoded
}
```

### Chat Completion Types

```rust
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum ChatMessageRole {
    User,
    Assistant,
    System,
    Tool,
    Developer,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum ChatMessageContent {
    Text(String),
    Blocks(Vec<ChatContentBlock>),
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatMessage {
    pub role: ChatMessageRole,
    pub content: Option<ChatMessageContent>,
    pub name: Option<String>,
    pub tool_call_id: Option<String>,
    pub tool_calls: Option<Vec<ToolCall>>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatParameters {
    pub temperature: Option<f64>,
    pub max_completion_tokens: Option<i32>,
    pub top_p: Option<f64>,
    pub frequency_penalty: Option<f64>,
    pub presence_penalty: Option<f64>,
    pub stop: Option<Vec<String>>,
    pub tools: Option<Vec<ChatTool>>,
    
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostChatRequest {
    pub provider: String,
    pub model: String,
    pub input: Vec<ChatMessage>,
    pub params: Option<ChatParameters>,
    pub fallbacks: Option<Vec<Fallback>>,
}
```

### Response Types

```rust
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct LLMUsage {
    pub prompt_tokens: i32,
    pub completion_tokens: i32,
    pub total_tokens: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ResponseChoice {
    pub index: i32,
    pub message: Option<ChatMessage>,
    pub delta: Option<ChatMessage>,
    pub finish_reason: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostChatResponse {
    pub id: String,
    pub model: String,
    pub choices: Vec<ResponseChoice>,
    pub usage: Option<LLMUsage>,
    pub created: Option<i64>,
    pub object: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostResponse {
    pub chat_response: Option<BifrostChatResponse>,
}
```

### Error Types

```rust
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ErrorField {
    pub message: String,
    #[serde(rename = "type")]
    pub error_type: Option<String>,
    pub code: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostError {
    pub error: ErrorField,
    pub status_code: Option<i32>,
    pub allow_fallbacks: Option<bool>,
}

impl BifrostError {
    pub fn new(message: &str) -> Self;
    pub fn with_type(self, error_type: &str) -> Self;
    pub fn with_code(self, code: &str) -> Self;
    pub fn with_status(self, status: i32) -> Self;
}
```

### Short Circuit

```rust
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct LLMPluginShortCircuit {
    pub response: Option<BifrostResponse>,
    pub error: Option<BifrostError>,
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
  "context": { "request_id": "abc-123" },
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
  "context": { "request_id": "abc-123", "plugin_processed": true },
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
  "context": { "request_id": "abc-123", "plugin_processed": true },
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
  "context": { "request_id": "abc-123", "post_hook_completed": true },
  "response": {},
  "error": {},
  "has_error": false,
  "hook_error": ""
}
```

## Usage Examples

### Modifying Context

```rust
#[no_mangle]
pub extern "C" fn pre_hook(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    let input: PreHookInput = serde_json::from_str(&input_str).unwrap();
    
    let mut output = PreHookOutput {
        context: input.context.clone(),
        ..Default::default()
    };
    
    // Add custom values to context
    output.context.set_value("plugin_processed", serde_json::json!(true));
    output.context.set_value("plugin_name", serde_json::json!("my-rust-plugin"));
    
    write_string(&serde_json::to_string(&output).unwrap())
}
```

### Short-Circuit with Mock Response

```rust
#[no_mangle]
pub extern "C" fn pre_hook(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    let input: PreHookInput = serde_json::from_str(&input_str).unwrap();
    
    let (provider, model) = input.get_provider_model();
    
    // Check if this should be mocked
    if model == "mock-model" {
        let mut output = PreHookOutput {
            context: input.context.clone(),
            has_short_circuit: true,
            ..Default::default()
        };
        
        // Build mock response
        let mock_response = BifrostResponse {
            chat_response: Some(BifrostChatResponse {
                id: format!("mock-{}", input.context.request_id.unwrap_or_default()),
                model: "mock-model".to_string(),
                choices: vec![ResponseChoice {
                    index: 0,
                    message: Some(ChatMessage {
                        role: ChatMessageRole::Assistant,
                        content: Some(ChatMessageContent::Text(
                            "This is a mock response!".to_string()
                        )),
                        ..Default::default()
                    }),
                    finish_reason: Some("stop".to_string()),
                    ..Default::default()
                }],
                usage: Some(LLMUsage {
                    prompt_tokens: 10,
                    completion_tokens: 15,
                    total_tokens: 25,
                    ..Default::default()
                }),
                ..Default::default()
            }),
            ..Default::default()
        };
        
        output.short_circuit = Some(LLMPluginShortCircuit {
            response: Some(mock_response),
            error: None,
        });
        
        return write_string(&serde_json::to_string(&output).unwrap());
    }
    
    // Pass through
    let output = PreHookOutput {
        context: input.context,
        ..Default::default()
    };
    write_string(&serde_json::to_string(&output).unwrap())
}
```

### Short-Circuit with Error

```rust
#[no_mangle]
pub extern "C" fn pre_hook(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    let input: PreHookInput = serde_json::from_str(&input_str).unwrap();
    
    // Check rate limit (example)
    if should_rate_limit(&input.context) {
        let mut output = PreHookOutput {
            context: input.context.clone(),
            has_short_circuit: true,
            ..Default::default()
        };
        
        output.short_circuit = Some(LLMPluginShortCircuit {
            response: None,
            error: Some(
                BifrostError::new("Rate limit exceeded")
                    .with_type("rate_limit")
                    .with_code("429")
                    .with_status(429)
            ),
        });
        
        return write_string(&serde_json::to_string(&output).unwrap());
    }
    
    // Pass through
    let output = PreHookOutput {
        context: input.context,
        ..Default::default()
    };
    write_string(&serde_json::to_string(&output).unwrap())
}
```

### Modifying Responses in post_hook

```rust
#[no_mangle]
pub extern "C" fn post_hook(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    let input: PostHookInput = serde_json::from_str(&input_str).unwrap();
    
    let mut output = PostHookOutput {
        context: input.context.clone(),
        ..Default::default()
    };
    
    // Handle errors
    if input.has_error {
        output.has_error = true;
        output.error = input.error.clone();
        
        // Optionally modify the error
        if let Some(mut error) = input.parse_error() {
            error.error.message = format!("{} (via rust plugin)", error.error.message);
            output.error = serde_json::to_value(&error).unwrap_or_default();
        }
        
        return write_string(&serde_json::to_string(&output).unwrap());
    }
    
    // Pass through or modify response
    if let Some(mut response) = input.parse_response() {
        if let Some(ref mut chat) = response.chat_response {
            // Add a marker to the model name
            chat.model = format!("{} (via rust-wasm)", chat.model);
        }
        output.response = serde_json::to_value(&response).unwrap_or_default();
    }
    
    write_string(&serde_json::to_string(&output).unwrap())
}
```

## Usage with Bifrost

Configure the plugin in your Bifrost config:

```json
{
  "plugins": [
    {
      "path": "/path/to/hello-world.wasm",
      "name": "hello-world-wasm-rust",
      "enabled": true,
      "config": {
        "custom_option": "value"
      }
    }
  ]
}
```

## Testing

The plugin includes unit tests that can be run with:

```bash
cargo test
```

## Benefits

1. **Performance**: Rust compiles to highly optimized WASM
2. **Safety**: Memory safety without garbage collection
3. **Small binaries**: Rust WASM binaries are typically very small
4. **Cross-platform**: Single `.wasm` binary runs on any OS/architecture
5. **Security**: WASM provides sandboxed execution
6. **Type Safety**: Strongly typed structures with serde derive macros
7. **Excellent JSON**: serde_json provides robust JSON handling
