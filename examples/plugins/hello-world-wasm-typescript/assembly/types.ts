/**
 * Type definitions for Bifrost WASM plugins.
 * 
 * Uses json-as library with @json decorators for safe JSON parsing.
 * These types mirror the Go SDK types for interoperability.
 */

import { JSON } from 'json-as'

// =============================================================================
// HTTP Transport Input/Output Types
// =============================================================================

/**
 * BifrostContext holds request-scoped values passed between hooks.
 * Common keys include:
 * - request_id: Unique identifier for the request
 * - Custom plugin values can be added and will be persisted across hooks
 */
@json
export class BifrostContext {
  request_id: string = ''

  // Custom values for plugin use (add more as needed)
  plugin_processed: string = ''
  plugin_name: string = ''
  post_hook_completed: string = ''
}

// =============================================================================
// HTTP Transport Structures
// =============================================================================

/**
 * HTTPRequest represents an incoming HTTP request at the transport layer.
 * Body is base64-encoded.
 */
@json
export class HTTPRequest {
  method: string = ''
  path: string = ''
  body: string = '' // base64 encoded
  headers: Map<string, string> = new Map<string, string>()
  query: Map<string, string> = new Map<string, string>()
}

/**
 * HTTPResponse represents an HTTP response to return.
 */
@json
export class HTTPResponse {
  status_code: i32 = 200
  body: string = '' // base64 encoded
  headers: Map<string, string> = new Map<string, string>()
}

/**
 * HTTPInterceptInput is the input for http_intercept hook.
 * Context is a dynamic object (JSON.Obj) since Go sends map[string]interface{}.
 * Request is kept as JSON.Raw to pass through without full parsing.
 */
@json
export class HTTPInterceptInput {
  context: JSON.Obj = new JSON.Obj()
  request: JSON.Raw = new JSON.Raw('null')
}

/**
 * HTTPInterceptOutput is the output for http_intercept hook.
 */
@json
export class HTTPInterceptOutput {
  context: JSON.Obj = new JSON.Obj()
  request: JSON.Raw = new JSON.Raw('null')
  response: JSON.Raw = new JSON.Raw('null')
  has_response: bool = false
  error: string = ''
}

// =============================================================================
// Pre-Hook Input/Output Types
// =============================================================================

/**
 * PreHookInput is the input for pre_hook.
 */
@json
export class PreHookInput {
  context: JSON.Obj = new JSON.Obj()
  request: JSON.Raw = new JSON.Raw('null')
}

/**
 * PreHookOutput is the output for pre_hook.
 */
@json
export class PreHookOutput {
  context: JSON.Obj = new JSON.Obj()
  request: JSON.Raw = new JSON.Raw('null')
  short_circuit: JSON.Raw = new JSON.Raw('null')
  has_short_circuit: bool = false
  error: string = ''
}

// =============================================================================
// Post-Hook Input/Output Types
// =============================================================================

/**
 * PostHookInput is the input for post_hook.
 */
@json
export class PostHookInput {
  context: JSON.Obj = new JSON.Obj()
  response: JSON.Raw = new JSON.Raw('null')
  error: JSON.Raw = new JSON.Raw('null')
  has_error: bool = false
}

/**
 * PostHookOutput is the output for post_hook.
 */
@json
export class PostHookOutput {
  context: JSON.Obj = new JSON.Obj()
  response: JSON.Raw = new JSON.Raw('null')
  error: JSON.Raw = new JSON.Raw('null')
  has_error: bool = false
  hook_error: string = ''
}

// =============================================================================
// HTTP Stream Chunk Hook Input/Output Types
// =============================================================================

/**
 * HTTPStreamChunkHookInput is the input for http_stream_chunk_hook.
 * Called for each chunk during streaming responses.
 */
@json
export class HTTPStreamChunkHookInput {
  context: JSON.Obj = new JSON.Obj()
  request: JSON.Raw = new JSON.Raw('null')
  chunk: JSON.Raw = new JSON.Raw('null') // BifrostStreamChunk as JSON
}

/**
 * HTTPStreamChunkHookOutput is the output for http_stream_chunk_hook.
 */
@json
export class HTTPStreamChunkHookOutput {
  context: JSON.Obj = new JSON.Obj()
  chunk: JSON.Raw = new JSON.Raw('null') // BifrostStreamChunk as JSON, or null to skip
  has_chunk: bool = false
  skip: bool = false
  error: string = ''
}
