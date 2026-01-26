//! Bifrost WASM Plugin for Rust
//!
//! This plugin demonstrates the proper structure for parsing inputs,
//! building responses, and handling context - similar to Go plugin patterns.
//!
//! Build with: cargo build --release --target wasm32-unknown-unknown

mod memory;
mod types;

use memory::{read_string, write_string};
use types::*;

// Global configuration storage
static mut PLUGIN_CONFIG: Option<PluginConfig> = None;

// =============================================================================
// Exported Plugin Functions
// =============================================================================

/// Return the plugin name
#[no_mangle]
pub extern "C" fn get_name() -> u64 {
    write_string("hello-world-wasm-rust")
}

/// Initialize the plugin with config
/// Returns 0 on success, non-zero on error
#[no_mangle]
pub extern "C" fn init(config_ptr: u32, config_len: u32) -> i32 {
    let config_str = read_string(config_ptr, config_len);
    
    // Parse configuration
    let config: PluginConfig = if config_str.is_empty() {
        PluginConfig::default()
    } else {
        match serde_json::from_str(&config_str) {
            Ok(c) => c,
            Err(_) => return 1, // Config parse error
        }
    };
    
    // Store configuration
    unsafe {
        PLUGIN_CONFIG = Some(config);
    }
    
    0 // Success
}

/// HTTP transport intercept
/// Called at the HTTP layer before request enters Bifrost core.
/// Can modify headers, query params, or short-circuit with a response.
#[no_mangle]
pub extern "C" fn http_intercept(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    
    // Parse input
    let input: HTTPInterceptInput = match serde_json::from_str(&input_str) {
        Ok(i) => i,
        Err(e) => {
            // Include context around the error position for debugging
            let error_context = if let Some(col) = extract_column(&e.to_string()) {
                let start = col.saturating_sub(50);
                let end = (col + 50).min(input_str.len());
                format!(" | context: ...{}...", &input_str[start..end])
            } else {
                String::new()
            };
            let output = HTTPInterceptOutput {
                error: format!("Failed to parse input: {}{}", e, error_context),
                ..Default::default()
            };
            return write_string(&serde_json::to_string(&output).unwrap_or_default());
        }
    };
    

    // Add context value like Go plugin does
    let mut context = input.context;
    context.set_value("from-http", serde_json::json!("123"));
    
    // Create output with context and request preserved (pass-through)
    // Serialize request to Value to ensure proper JSON structure
    let request_value = serde_json::to_value(&input.request).ok();
    
    let output = HTTPInterceptOutput {
        context: input.context,
        request: input.request,
        has_response: false,
        ..Default::default()
    };
    
    // Pass through
    write_string(&serde_json::to_string(&output).unwrap_or_default())
}

/// Pre-request hook
/// Called before request is sent to the provider.
/// Can modify the request or short-circuit with a response/error.
#[no_mangle]
pub extern "C" fn pre_hook(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    
    // Parse input
    let input: PreHookInput = match serde_json::from_str(&input_str) {
        Ok(i) => i,
        Err(e) => {
            let output = PreHookOutput {
                error: format!("Failed to parse input: {}", e),
                ..Default::default()
            };
            return write_string(&serde_json::to_string(&output).unwrap_or_default());
        }
    };
    
    // Create output with context preserved
    let mut output = PreHookOutput {
        context: input.context.clone(),
        request: input.request.clone(),
        has_short_circuit: false,
        ..Default::default()
    };
    
    // Get provider and model for potential modifications
    let (_provider, model) = input.get_provider_model();
    
    // Example: Short-circuit with mock response for specific model
    // Uncomment to test:
    /*
    if model == "mock-model" {
        output.has_short_circuit = true;
        
        let mock_response = BifrostResponse {
            chat_response: Some(BifrostChatResponse {
                id: format!("mock-{}", input.context.request_id.unwrap_or_default()),
                model: "mock-model".to_string(),
                choices: vec![ResponseChoice {
                    index: 0,
                    message: Some(ChatMessage {
                        role: ChatMessageRole::Assistant,
                        content: Some(ChatMessageContent::Text(
                            "This is a mock response from the Rust WASM plugin!".to_string()
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
        
        output.short_circuit = Some(PluginShortCircuit {
            response: Some(mock_response),
            error: None,
        });
        
        return write_string(&serde_json::to_string(&output).unwrap_or_default());
    }
    */
    
    // Example: Short-circuit with rate limit error
    // Uncomment to test:
    /*
    if should_rate_limit(&input.context) {
        output.has_short_circuit = true;
        output.short_circuit = Some(PluginShortCircuit {
            response: None,
            error: Some(
                BifrostError::new("Rate limit exceeded")
                    .with_type("rate_limit")
                    .with_code("429")
                    .with_status(429)
            ),
        });
        return write_string(&serde_json::to_string(&output).unwrap_or_default());
    }
    */

    // Silence unused variable warning in example code
    let _ = model;
    
    // Pass through - empty request means use original
    write_string(&serde_json::to_string(&output).unwrap_or_default())
}

/// Post-response hook
/// Called after response is received from provider.
/// Can modify the response or error.
#[no_mangle]
pub extern "C" fn post_hook(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    
    // Parse input
    let input: PostHookInput = match serde_json::from_str(&input_str) {
        Ok(i) => i,
        Err(e) => {
            let output = PostHookOutput {
                hook_error: format!("Failed to parse input: {}", e),
                ..Default::default()
            };
            return write_string(&serde_json::to_string(&output).unwrap_or_default());
        }
    };
    
    // Add context value like Go plugin does
    let mut context = input.context.clone();
    context.set_value("from-post-hook", serde_json::json!("456"));
    
    // Create output with context and response/error preserved (pass-through)
    // This matches Go plugin behavior exactly
    let output = PostHookOutput {
        context,
        response: Some(input.response.clone()),
        error: Some(input.error.clone()),
        has_error: input.has_error,
        hook_error: String::new(),
    };
    
    // Example: Modify error message when has_error is true
    // Uncomment to test:
    /*
    if input.has_error {
        if let Some(mut error) = input.parse_error() {
            error.error.message = format!("{} (processed by Rust WASM plugin)", error.error.message);
            let mut output = output;
            output.error = Some(serde_json::to_value(&error).unwrap_or_default());
            return write_string(&serde_json::to_string(&output).unwrap_or_default());
        }
    }
    */
    
    // Example: Modify response
    // Uncomment to test:
    /*
    if let Some(mut response) = input.parse_response() {
        // Add custom metadata, modify model name, etc.
        if let Some(ref mut chat) = response.chat_response {
            // Add a marker to the model name
            chat.model = format!("{} (via rust-wasm)", chat.model);
        }
        let mut output = output;
        output.response = Some(serde_json::to_value(&response).unwrap_or_default());
        return write_string(&serde_json::to_string(&output).unwrap_or_default());
    }
    */
    
    write_string(&serde_json::to_string(&output).unwrap_or_default())
}

/// HTTP stream chunk hook
/// Called for each chunk during streaming responses.
/// Can modify, skip, or stop streaming based on return values.
#[no_mangle]
pub extern "C" fn http_stream_chunk_hook(input_ptr: u32, input_len: u32) -> u64 {
    let input_str = read_string(input_ptr, input_len);
    
    // Parse input
    let input: HTTPStreamChunkHookInput = match serde_json::from_str(&input_str) {
        Ok(i) => i,
        Err(e) => {
            let output = HTTPStreamChunkHookOutput {
                error: format!("Failed to parse input: {}", e),
                ..Default::default()
            };
            return write_string(&serde_json::to_string(&output).unwrap_or_default());
        }
    };
    
    // Add context value like Go plugin does
    let mut context = input.context.clone();
    context.set_value("from-stream-chunk", serde_json::json!("rust-wasm"));
    
    // Pass through chunk unchanged
    let output = HTTPStreamChunkHookOutput {
        context,
        chunk: Some(input.chunk),
        has_chunk: true,
        skip: false,
        error: String::new(),
    };
    
    write_string(&serde_json::to_string(&output).unwrap_or_default())
}

/// Cleanup resources
/// Called when plugin is being unloaded.
/// Returns 0 on success, non-zero on error
#[no_mangle]
pub extern "C" fn cleanup() -> i32 {
    // Clear stored configuration
    unsafe {
        PLUGIN_CONFIG = None;
    }
    
    0 // Success
}

// =============================================================================
// Helper Functions
// =============================================================================

/// Extract column number from serde error message for debugging
fn extract_column(error_msg: &str) -> Option<usize> {
    // Error format: "... at line X column Y"
    if let Some(idx) = error_msg.rfind("column ") {
        let col_str = &error_msg[idx + 7..];
        col_str.split_whitespace().next()?.parse().ok()
    } else {
        None
    }
}

/// Example rate limit check function
#[allow(dead_code)]
fn should_rate_limit(_context: &BifrostContext) -> bool {
    // Implement your rate limiting logic here
    false
}
