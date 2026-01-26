// Package main provides a hello-world WASM plugin example for Bifrost.
// This plugin demonstrates the basic structure and exports required for WASM plugins.
//
// Build with TinyGo:
//
//	tinygo build -o build/hello-world.wasm -target=wasi -scheduler=none main.go
package main

import (
	"encoding/json"
)

// ============================================================================
// Plugin Exports
// ============================================================================

//export get_name
func get_name() uint64 {
	return writeBytes([]byte("Hello World WASM Plugin"))
}

//export init
func init_plugin(configPtr, configLen uint32) int32 {
	println("WASM Plugin: Init called")
	if configLen > 0 {
		configData := readInput(configPtr, configLen)
		println("WASM Plugin: Config received:", string(configData))
	}
	return 0
}

//export http_intercept
func http_intercept(inputPtr, inputLen uint32) uint64 {
	println("WASM Plugin: http_intercept called")

	inputData := readInput(inputPtr, inputLen)
	if inputData == nil {
		return writeError("no input data")
	}

	// Parse input
	var input HTTPInterceptInput
	if err := json.Unmarshal(inputData, &input); err != nil {
		println("WASM Plugin: parse error:", err.Error())
		return writeError("parse error: " + err.Error())
	}

	// Log parsed data
	println("WASM Plugin: HTTP", input.Request.Method, input.Request.Path)
	if ct, ok := input.Request.Headers["content-type"]; ok {
		println("WASM Plugin: Content-Type:", ct)
	}
	input.Context["from-http"] = "123"
	// Return pass-through
	output := HTTPInterceptOutput{
		Context:     input.Context,
		Request:     input.Request,
		HasResponse: false,		
		Error:       "",
	}

	data, _ := json.Marshal(output)
	return writeBytes(data)
}

//export pre_hook
func pre_hook(inputPtr, inputLen uint32) uint64 {
	println("WASM Plugin: pre_hook called")

	inputData := readInput(inputPtr, inputLen)
	if inputData == nil {
		return writePreHookError("no input data")
	}

	println("WASM Plugin: Pre-hook input:", string(inputData))

	// Parse input
	var input PreHookInput
	if err := json.Unmarshal(inputData, &input); err != nil {
		println("WASM Plugin: parse error:", err.Error())
		return writePreHookError("parse error: " + err.Error())
	}

	// Print existing context
	for k, v := range input.Context {
		println("WASM Plugin: Context", k, "=", v)
	}

	input.Context["from-pre-hook"] = "789"

	// Return with custom context value
	output := PreHookOutput{
		Context:         input.Context,
		Request:         input.Request,
		HasShortCircuit: false,
		Error:           "",
	}

	data, _ := json.Marshal(output)
	return writeBytes(data)
}

//export post_hook
func post_hook(inputPtr, inputLen uint32) uint64 {
	println("WASM Plugin: post_hook called")

	inputData := readInput(inputPtr, inputLen)
	if inputData == nil {
		return writePostHookError("no input data")
	}

	// Parse input
	var input PostHookInput
	if err := json.Unmarshal(inputData, &input); err != nil {
		println("WASM Plugin: parse error:", err.Error())
		return writePostHookError("parse error: " + err.Error())
	}

	println("WASM Plugin: Post-hook input:", string(inputData))
	// Print existing context
	for k, v := range input.Context {
		println("WASM Plugin: Context", k, "=", v)
	}

	// Parse response for logging

	if processed, ok := input.Context["wasm_plugin_processed"].(bool); ok && processed {
		println("WASM Plugin: Pre-hook context value present")
	}

	input.Context["from-post-hook"] = "456"
	// Return pass-through
	output := PostHookOutput{
		Context:   input.Context,
		Response:  input.Response,
		Error:     input.Error,
		HasError:  false,
		HookError: "",
	}

	data, _ := json.Marshal(output)
	return writeBytes(data)
}

//export http_stream_chunk_hook
func http_stream_chunk_hook(inputPtr, inputLen uint32) uint64 {
	println("WASM Plugin: http_stream_chunk_hook called")

	inputData := readInput(inputPtr, inputLen)
	if inputData == nil {
		return writeStreamChunkError("no input data")
	}

	// Parse input
	var input HTTPStreamChunkHookInput
	if err := json.Unmarshal(inputData, &input); err != nil {
		println("WASM Plugin: parse error:", err.Error())
		return writeStreamChunkError("parse error: " + err.Error())
	}

	println("WASM Plugin: Stream chunk received")

	// Add context value
	input.Context["from-stream-chunk"] = "wasm-plugin"

	// Pass through chunk unchanged
	output := HTTPStreamChunkHookOutput{
		Context:  input.Context,
		Chunk:    input.Chunk,
		HasChunk: true,
		Skip:     false,
		Error:    "",
	}

	data, _ := json.Marshal(output)
	return writeBytes(data)
}

//export cleanup
func cleanup() int32 {
	println("WASM Plugin: Cleanup called")
	return 0
}

// Helper functions for error responses
func writeError(msg string) uint64 {
	output := HTTPInterceptOutput{HasResponse: false, Error: msg}
	data, _ := json.Marshal(output)
	return writeBytes(data)
}

func writePreHookError(msg string) uint64 {
	output := PreHookOutput{
		Context:         map[string]interface{}{},
		Request:         nil,
		HasShortCircuit: false,
		Error:           msg,
	}
	data, _ := json.Marshal(output)
	return writeBytes(data)
}

func writePostHookError(msg string) uint64 {
	output := PostHookOutput{
		Context:   map[string]interface{}{},
		Response:  nil,
		HasError:  false,
		HookError: msg,
	}
	data, _ := json.Marshal(output)
	return writeBytes(data)
}

func writeStreamChunkError(msg string) uint64 {
	output := HTTPStreamChunkHookOutput{
		Context:  map[string]interface{}{},
		Chunk:    nil,
		HasChunk: false,
		Skip:     false,
		Error:    msg,
	}
	data, _ := json.Marshal(output)
	return writeBytes(data)
}

func main() {}
