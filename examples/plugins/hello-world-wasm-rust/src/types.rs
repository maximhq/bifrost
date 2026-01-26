//! Type definitions for Bifrost WASM plugins.
//! These structures mirror the Go SDK types for interoperability.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;

// =============================================================================
// Nullable Deserializers
// =============================================================================

/// Helper module for deserializing fields that may be null in JSON.
/// Go's JSON encoder outputs `null` for nil slices/maps, but Rust's serde
/// with `#[serde(default)]` only handles missing fields, not explicit nulls.
mod nullable {
    use serde::{Deserialize, Deserializer};
    use std::collections::HashMap;

    /// Deserialize a string that may be null, converting null to empty string.
    pub fn string<'de, D>(deserializer: D) -> Result<String, D::Error>
    where
        D: Deserializer<'de>,
    {
        Option::<String>::deserialize(deserializer).map(|opt| opt.unwrap_or_default())
    }

    /// Deserialize a HashMap<String, String> that may be null or contain null values.
    /// Handles both `null` (entire map is null) and `{"key": null}` (value is null).
    pub fn string_map<'de, D>(deserializer: D) -> Result<HashMap<String, String>, D::Error>
    where
        D: Deserializer<'de>,
    {
        // First deserialize as Option<HashMap<String, Option<String>>> to handle null values
        let opt_map: Option<HashMap<String, Option<String>>> = Option::deserialize(deserializer)?;
        
        match opt_map {
            None => Ok(HashMap::new()),
            Some(map) => {
                // Filter out null values and unwrap the rest
                Ok(map
                    .into_iter()
                    .filter_map(|(k, v)| v.map(|val| (k, val)))
                    .collect())
            }
        }
    }

    /// Deserialize an i32 that may be null, converting null to 0.
    pub fn i32_field<'de, D>(deserializer: D) -> Result<i32, D::Error>
    where
        D: Deserializer<'de>,
    {
        Option::<i32>::deserialize(deserializer).map(|opt| opt.unwrap_or_default())
    }

    /// Deserialize an HTTPRequest that may be null, converting null to default.
    pub fn http_request<'de, D>(deserializer: D) -> Result<super::HTTPRequest, D::Error>
    where
        D: Deserializer<'de>,
    {
        Option::<super::HTTPRequest>::deserialize(deserializer).map(|opt| opt.unwrap_or_default())
    }

    /// Deserialize a BifrostContext that may be null, converting null to default.
    pub fn context<'de, D>(deserializer: D) -> Result<super::BifrostContext, D::Error>
    where
        D: Deserializer<'de>,
    {
        Option::<super::BifrostContext>::deserialize(deserializer).map(|opt| opt.unwrap_or_default())
    }
}

// =============================================================================
// Context Structure
// =============================================================================

/// BifrostContext holds request-scoped values passed between hooks.
/// This is a dynamic map (map[string]any in Go) that can hold any JSON values.
/// Common keys include:
/// - request_id: Unique identifier for the request
/// - Custom plugin values can be added and will be persisted across hooks
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(transparent)]
pub struct BifrostContext(pub HashMap<String, serde_json::Value>);

impl BifrostContext {
    pub fn new() -> Self {
        Self(HashMap::new())
    }

    /// Set a custom value in the context
    pub fn set_value(&mut self, key: &str, value: impl Into<serde_json::Value>) {
        self.0.insert(key.to_string(), value.into());
    }

    /// Get a value from the context
    pub fn get(&self, key: &str) -> Option<&serde_json::Value> {
        self.0.get(key)
    }

    /// Get a string value from the context
    pub fn get_string(&self, key: &str) -> Option<&str> {
        self.0.get(key).and_then(|v| v.as_str())
    }

    /// Get a boolean value from the context
    pub fn get_bool(&self, key: &str) -> Option<bool> {
        self.0.get(key).and_then(|v| v.as_bool())
    }

    /// Get an i64 value from the context
    pub fn get_i64(&self, key: &str) -> Option<i64> {
        self.0.get(key).and_then(|v| v.as_i64())
    }

    /// Check if a key exists in the context
    pub fn contains_key(&self, key: &str) -> bool {
        self.0.contains_key(key)
    }

    /// Remove a value from the context
    pub fn remove(&mut self, key: &str) -> Option<serde_json::Value> {
        self.0.remove(key)
    }

    /// Get the underlying HashMap for iteration
    pub fn inner(&self) -> &HashMap<String, serde_json::Value> {
        &self.0
    }

    /// Get mutable access to the underlying HashMap
    pub fn inner_mut(&mut self) -> &mut HashMap<String, serde_json::Value> {
        &mut self.0
    }
}

// =============================================================================
// HTTP Transport Structures
// =============================================================================

/// HTTPRequest represents an incoming HTTP request at the transport layer.
/// Body is base64-encoded.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPRequest {
    #[serde(default, deserialize_with = "nullable::string")]
    pub method: String,

    #[serde(default, deserialize_with = "nullable::string")]
    pub path: String,

    #[serde(default, deserialize_with = "nullable::string_map")]
    pub headers: HashMap<String, String>,

    #[serde(default, deserialize_with = "nullable::string_map")]
    pub query: HashMap<String, String>,

    /// Base64-encoded request body
    #[serde(default, deserialize_with = "nullable::string")]
    pub body: String,
}

/// HTTPResponse represents an HTTP response to return.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPResponse {
    #[serde(default, deserialize_with = "nullable::i32_field")]
    pub status_code: i32,

    #[serde(default, deserialize_with = "nullable::string_map")]
    pub headers: HashMap<String, String>,

    /// Base64-encoded response body
    #[serde(default, deserialize_with = "nullable::string")]
    pub body: String,
}

/// HTTPInterceptInput is the input for http_intercept hook.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPInterceptInput {
    #[serde(default, deserialize_with = "nullable::context")]
    pub context: BifrostContext,

    #[serde(default, deserialize_with = "nullable::http_request")]
    pub request: HTTPRequest,
}

/// HTTPInterceptOutput is the output for http_intercept hook.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPInterceptOutput {
    pub context: BifrostContext,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub request: Option<serde_json::Value>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub response: Option<HTTPResponse>,

    #[serde(default)]
    pub has_response: bool,

    #[serde(default)]
    pub error: String,
}

// =============================================================================
// Chat Completion Structures (BifrostRequest)
// =============================================================================

/// ChatMessageRole represents the role of a message sender.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "lowercase")]
pub enum ChatMessageRole {
    User,
    Assistant,
    System,
    Tool,
    Developer,
}

impl Default for ChatMessageRole {
    fn default() -> Self {
        ChatMessageRole::User
    }
}

/// ChatMessageContent can be either a string or an array of content blocks.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum ChatMessageContent {
    Text(String),
    Blocks(Vec<ChatContentBlock>),
}

impl Default for ChatMessageContent {
    fn default() -> Self {
        ChatMessageContent::Text(String::new())
    }
}

/// ChatContentBlock represents a content block in a message.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatContentBlock {
    #[serde(rename = "type")]
    pub block_type: String,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub image_url: Option<ImageUrl>,
}

/// ImageUrl represents an image URL in a content block.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ImageUrl {
    pub url: String,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// ChatMessage represents a message in the conversation.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatMessage {
    #[serde(default)]
    pub role: ChatMessageRole,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<ChatMessageContent>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_calls: Option<Vec<ToolCall>>,
}

/// ToolCall represents a tool call made by the assistant.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ToolCall {
    #[serde(default)]
    pub id: Option<String>,

    #[serde(rename = "type", default)]
    pub call_type: Option<String>,

    #[serde(default)]
    pub function: ToolCallFunction,
}

/// ToolCallFunction represents the function being called.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ToolCallFunction {
    #[serde(default)]
    pub name: Option<String>,

    #[serde(default)]
    pub arguments: String,
}

/// ChatParameters contains optional parameters for chat completion.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatParameters {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f64>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_completion_tokens: Option<i32>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub top_p: Option<f64>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub frequency_penalty: Option<f64>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub presence_penalty: Option<f64>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub stop: Option<Vec<String>>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub tools: Option<Vec<ChatTool>>,

    /// Catch-all for additional parameters
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

/// ChatTool represents a tool definition.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatTool {
    #[serde(rename = "type")]
    pub tool_type: String,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub function: Option<ChatToolFunction>,
}

/// ChatToolFunction represents a function definition.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ChatToolFunction {
    pub name: String,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub parameters: Option<serde_json::Value>,
}

/// BifrostChatRequest represents a chat completion request.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostChatRequest {
    #[serde(default)]
    pub provider: String,

    #[serde(default)]
    pub model: String,

    #[serde(default)]
    pub input: Vec<ChatMessage>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<ChatParameters>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub fallbacks: Option<Vec<Fallback>>,
}

/// Fallback represents a fallback provider/model.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct Fallback {
    pub provider: String,
    pub model: String,
}

/// BifrostRequest is the unified request structure.
/// Only one of the request types should be present.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostRequest {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub chat_request: Option<BifrostChatRequest>,

    // Add other request types as needed
    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

impl BifrostRequest {
    /// Get provider and model from the request
    pub fn get_provider_model(&self) -> (String, String) {
        if let Some(ref chat) = self.chat_request {
            return (chat.provider.clone(), chat.model.clone());
        }
        (String::new(), String::new())
    }
}

// =============================================================================
// Response Structures (BifrostResponse)
// =============================================================================

/// LLMUsage contains token usage information.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct LLMUsage {
    #[serde(default)]
    pub prompt_tokens: i32,

    #[serde(default)]
    pub completion_tokens: i32,

    #[serde(default)]
    pub total_tokens: i32,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub prompt_tokens_details: Option<serde_json::Value>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub completion_tokens_details: Option<serde_json::Value>,
}

/// ResponseChoice represents a single completion choice.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ResponseChoice {
    #[serde(default)]
    pub index: i32,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<ChatMessage>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub delta: Option<ChatMessage>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub finish_reason: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub logprobs: Option<serde_json::Value>,
}

/// BifrostChatResponse represents a chat completion response.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostChatResponse {
    #[serde(default)]
    pub id: String,

    #[serde(default)]
    pub model: String,

    #[serde(default)]
    pub choices: Vec<ResponseChoice>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<LLMUsage>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub created: Option<i64>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub object: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub system_fingerprint: Option<String>,
}

/// BifrostResponse is the unified response structure.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub chat_response: Option<BifrostChatResponse>,

    #[serde(flatten)]
    pub extra: HashMap<String, serde_json::Value>,
}

// =============================================================================
// Error Structure
// =============================================================================

/// ErrorField contains the error details.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ErrorField {
    #[serde(default)]
    pub message: String,

    #[serde(skip_serializing_if = "Option::is_none", rename = "type")]
    pub error_type: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub code: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub param: Option<String>,
}

/// BifrostError represents an error response.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BifrostError {
    #[serde(default)]
    pub error: ErrorField,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub status_code: Option<i32>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub allow_fallbacks: Option<bool>,
}

impl BifrostError {
    /// Create a new error with a message
    pub fn new(message: &str) -> Self {
        Self {
            error: ErrorField {
                message: message.to_string(),
                ..Default::default()
            },
            ..Default::default()
        }
    }

    /// Set the error type
    pub fn with_type(mut self, error_type: &str) -> Self {
        self.error.error_type = Some(error_type.to_string());
        self
    }

    /// Set the error code
    pub fn with_code(mut self, code: &str) -> Self {
        self.error.code = Some(code.to_string());
        self
    }

    /// Set the status code
    pub fn with_status(mut self, status: i32) -> Self {
        self.status_code = Some(status);
        self
    }
}

// =============================================================================
// Short Circuit Structure
// =============================================================================

/// PluginShortCircuit allows plugins to short-circuit the request flow.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PluginShortCircuit {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub response: Option<BifrostResponse>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<BifrostError>,
}

// =============================================================================
// Hook Input/Output Structures
// =============================================================================

/// PreHookInput is the input for pre_hook.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PreHookInput {
    #[serde(default)]
    pub context: BifrostContext,

    #[serde(default)]
    pub request: serde_json::Value,
}

impl PreHookInput {
    /// Parse the request as a BifrostRequest
    pub fn parse_request(&self) -> Option<BifrostRequest> {
        serde_json::from_value(self.request.clone()).ok()
    }

    /// Get provider and model from the request
    pub fn get_provider_model(&self) -> (String, String) {
        if let Some(req) = self.parse_request() {
            return req.get_provider_model();
        }
        // Try direct access for simpler structures
        let provider = self.request.get("provider")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let model = self.request.get("model")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        (provider, model)
    }
}

/// PreHookOutput is the output for pre_hook.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PreHookOutput {
    pub context: BifrostContext,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub request: Option<serde_json::Value>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub short_circuit: Option<PluginShortCircuit>,

    #[serde(default)]
    pub has_short_circuit: bool,

    #[serde(default)]
    pub error: String,
}

/// PostHookInput is the input for post_hook.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PostHookInput {
    #[serde(default)]
    pub context: BifrostContext,

    #[serde(default)]
    pub response: serde_json::Value,

    #[serde(default)]
    pub error: serde_json::Value,

    #[serde(default)]
    pub has_error: bool,
}

impl PostHookInput {
    /// Parse the response as a BifrostResponse
    pub fn parse_response(&self) -> Option<BifrostResponse> {
        serde_json::from_value(self.response.clone()).ok()
    }

    /// Parse the error as a BifrostError
    pub fn parse_error(&self) -> Option<BifrostError> {
        if self.has_error {
            serde_json::from_value(self.error.clone()).ok()
        } else {
            None
        }
    }
}

/// PostHookOutput is the output for post_hook.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PostHookOutput {
    pub context: BifrostContext,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub response: Option<serde_json::Value>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<serde_json::Value>,

    #[serde(default)]
    pub has_error: bool,

    #[serde(default)]
    pub hook_error: String,
}

// =============================================================================
// HTTP Stream Chunk Hook Input/Output Structures
// =============================================================================

/// HTTPStreamChunkHookInput is the input for http_stream_chunk_hook.
/// Called for each chunk during streaming responses.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPStreamChunkHookInput {
    #[serde(default)]
    pub context: BifrostContext,

    #[serde(default)]
    pub request: serde_json::Value,

    #[serde(default)]
    pub chunk: serde_json::Value, // BifrostStreamChunk as JSON
}

/// HTTPStreamChunkHookOutput is the output for http_stream_chunk_hook.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct HTTPStreamChunkHookOutput {
    pub context: BifrostContext,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub chunk: Option<serde_json::Value>, // BifrostStreamChunk as JSON, None to skip

    #[serde(default)]
    pub has_chunk: bool,

    #[serde(default)]
    pub skip: bool,

    #[serde(default)]
    pub error: String,
}

// =============================================================================
// Plugin Configuration
// =============================================================================

/// Plugin configuration (customize as needed)
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PluginConfig {
    #[serde(flatten)]
    pub values: HashMap<String, serde_json::Value>,
}

// =============================================================================
// Tests
// =============================================================================

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_context_serialization() {
        let mut ctx = BifrostContext::new();
        ctx.set_value("request_id", "test-123");
        ctx.set_value("custom_key", "custom_value");
        ctx.set_value("is_enabled", true);
        ctx.set_value("count", 42);
        
        let json = serde_json::to_string(&ctx).unwrap();
        assert!(json.contains("request_id"));
        assert!(json.contains("custom_key"));
        assert!(json.contains("is_enabled"));
        assert!(json.contains("count"));
    }

    #[test]
    fn test_context_deserialization() {
        let json = r#"{"request_id": "test-123", "custom_key": "custom_value", "is_enabled": true}"#;
        let ctx: BifrostContext = serde_json::from_str(json).unwrap();
        
        assert_eq!(ctx.get_string("request_id"), Some("test-123"));
        assert_eq!(ctx.get_string("custom_key"), Some("custom_value"));
        assert_eq!(ctx.get_bool("is_enabled"), Some(true));
    }

    #[test]
    fn test_context_methods() {
        let mut ctx = BifrostContext::new();
        ctx.set_value("key1", "value1");
        ctx.set_value("enabled", true);
        ctx.set_value("count", 42);
        
        assert_eq!(ctx.get_string("key1"), Some("value1"));
        assert_eq!(ctx.get_bool("enabled"), Some(true));
        assert_eq!(ctx.get_i64("count"), Some(42));
        assert!(ctx.contains_key("key1"));
        assert!(!ctx.contains_key("nonexistent"));
        
        ctx.remove("key1");
        assert!(!ctx.contains_key("key1"));
    }

    #[test]
    fn test_chat_message() {
        let msg = ChatMessage {
            role: ChatMessageRole::User,
            content: Some(ChatMessageContent::Text("Hello!".to_string())),
            ..Default::default()
        };
        
        let json = serde_json::to_string(&msg).unwrap();
        assert!(json.contains("user"));
        assert!(json.contains("Hello!"));
    }

    #[test]
    fn test_bifrost_error() {
        let error = BifrostError::new("Test error")
            .with_type("test_type")
            .with_code("500")
            .with_status(500);
        
        let json = serde_json::to_string(&error).unwrap();
        assert!(json.contains("Test error"));
        assert!(json.contains("test_type"));
    }

    #[test]
    fn test_pre_hook_input_parsing() {
        let json = r#"{
            "context": {"request_id": "test-123", "custom": "value"},
            "request": {"provider": "openai", "model": "gpt-4"}
        }"#;
        
        let input: PreHookInput = serde_json::from_str(json).unwrap();
        assert_eq!(input.context.get_string("request_id"), Some("test-123"));
        assert_eq!(input.context.get_string("custom"), Some("value"));
        
        let (provider, model) = input.get_provider_model();
        assert_eq!(provider, "openai");
        assert_eq!(model, "gpt-4");
    }

    #[test]
    fn test_http_request_with_null_fields() {
        // Simulates Go sending null for nil []byte and nil maps
        let json = r#"{
            "method": "POST",
            "path": "/v1/chat/completions",
            "headers": null,
            "query": null,
            "body": null
        }"#;
        
        let req: HTTPRequest = serde_json::from_str(json).unwrap();
        assert_eq!(req.method, "POST");
        assert_eq!(req.path, "/v1/chat/completions");
        assert!(req.headers.is_empty());
        assert!(req.query.is_empty());
        assert_eq!(req.body, "");
    }

    #[test]
    fn test_http_request_with_missing_fields() {
        // Test that missing fields also work (default behavior)
        let json = r#"{
            "method": "GET",
            "path": "/health"
        }"#;
        
        let req: HTTPRequest = serde_json::from_str(json).unwrap();
        assert_eq!(req.method, "GET");
        assert_eq!(req.path, "/health");
        assert!(req.headers.is_empty());
        assert!(req.query.is_empty());
        assert_eq!(req.body, "");
    }

    #[test]
    fn test_http_intercept_input_with_nulls() {
        // Simulates a full HTTP intercept input with null body from Go
        let json = r#"{
            "context": {"request_id": "abc-123"},
            "request": {
                "method": "POST",
                "path": "/v1/chat/completions",
                "headers": {"content-type": "application/json"},
                "query": {},
                "body": null
            }
        }"#;
        
        let input: HTTPInterceptInput = serde_json::from_str(json).unwrap();
        assert_eq!(input.context.get_string("request_id"), Some("abc-123"));
        assert_eq!(input.request.method, "POST");
        assert_eq!(input.request.path, "/v1/chat/completions");
        assert_eq!(input.request.headers.get("content-type"), Some(&"application/json".to_string()));
        assert_eq!(input.request.body, "");
    }

    #[test]
    fn test_http_response_with_null_fields() {
        let json = r#"{
            "status_code": null,
            "headers": null,
            "body": null
        }"#;
        
        let resp: HTTPResponse = serde_json::from_str(json).unwrap();
        assert_eq!(resp.status_code, 0);
        assert!(resp.headers.is_empty());
        assert_eq!(resp.body, "");
    }
}
