/**
 * Bifrost WASM Plugin for TypeScript/AssemblyScript
 *
 * This plugin uses json-as for safe JSON parsing with @json decorators.
 *
 * Build with: npm run build
 */

import { JSON } from 'json-as'
import { free as _free, malloc as _malloc, readString, writeString } from './memory'
import {
  HTTPInterceptInput,
  HTTPInterceptOutput,
  PreHookInput,
  PreHookOutput,
  PostHookInput,
  PostHookOutput,
  HTTPStreamChunkHookInput,
  HTTPStreamChunkHookOutput
} from './types'

// =============================================================================
// Re-export memory functions for WASM host
// =============================================================================

export function malloc(size: u32): u32 {
  return _malloc(size)
}

export function free(ptr: u32): void {
  _free(ptr)
}

// =============================================================================
// Plugin Configuration
// =============================================================================

let pluginConfig: string = ''

// =============================================================================
// Exported Plugin Functions
// =============================================================================

export function get_name(): u64 {
  return writeString('hello-world-wasm-typescript')
}

export function init(configPtr: u32, configLen: u32): i32 {
  pluginConfig = readString(configPtr, configLen)
  return 0
}

/**
 * HTTP transport intercept
 * Pass through the request with added context value
 */
export function http_intercept(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<HTTPInterceptInput>(inputJson)

  const output = new HTTPInterceptOutput()
  output.context = input.context
  output.context.set<string>('from-http', '123')
  output.request = input.request

  return writeString(JSON.stringify(output))
}

/**
 * Pre-request hook
 * Pass through the request with added context value
 */
export function pre_hook(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<PreHookInput>(inputJson)

  const output = new PreHookOutput()
  output.context = input.context
  output.context.set<string>('from-pre-hook', '789')
  output.request = input.request

  return writeString(JSON.stringify(output))
}

/**
 * Post-response hook
 * Pass through the response/error with added context value
 */
export function post_hook(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<PostHookInput>(inputJson)

  const output = new PostHookOutput()
  output.context = input.context
  output.context.set<string>('from-post-hook', '456')
  output.response = input.response
  output.error = input.error
  output.has_error = input.has_error

  return writeString(JSON.stringify(output))
}

/**
 * HTTP stream chunk hook
 * Called for each chunk during streaming responses.
 * Pass through the chunk with added context value.
 */
export function http_stream_chunk_hook(inputPtr: u32, inputLen: u32): u64 {
  const inputJson = readString(inputPtr, inputLen)
  const input = JSON.parse<HTTPStreamChunkHookInput>(inputJson)

  const output = new HTTPStreamChunkHookOutput()
  output.context = input.context
  output.context.set<string>('from-stream-chunk', 'wasm-plugin')
  output.chunk = input.chunk
  output.has_chunk = true
  output.skip = false

  return writeString(JSON.stringify(output))
}

/**
 * Cleanup resources
 */
export function cleanup(): i32 {
  pluginConfig = ''
  return 0
}
