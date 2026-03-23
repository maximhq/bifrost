package main

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	transportPreHookKey  schemas.BifrostContextKey = "hello-world-plugin-transport-pre-hook"
	transportPostHookKey schemas.BifrostContextKey = "hello-world-plugin-transport-post-hook"
	preHookKey           schemas.BifrostContextKey = "hello-world-plugin-pre-hook"
)

func Init(config any) error {
	fmt.Println("Init called")
	return nil
}

// GetName returns the name of the plugin (required)
// This is the system identifier - not editable by users
// Users can set a custom display_name in the config for the UI
func GetName() string {
	return "hello-world"
}

func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	fmt.Println("HTTPTransportPreHook called")
	// Modify request in-place
	req.Headers["x-hello-world-plugin"] = "transport-pre-hook-value"
	// Store value in context for PreLLMHook/PostLLMHook
	ctx.SetValue(transportPreHookKey, "transport-pre-hook-value")
	// Return nil to continue processing, or return &schemas.HTTPResponse{} to short-circuit
	ctx.Log(schemas.LogLevelInfo, "HTTPTransportPreHook called")
	return nil, nil
}

func HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	fmt.Println("HTTPTransportPostHook called")
	// Modify response in-place
	resp.Headers["x-hello-world-plugin"] = "transport-post-hook-value"
	// Store value in context
	ctx.Log(schemas.LogLevelInfo, "HTTPTransportPostHook called")
	ctx.SetValue(transportPostHookKey, "transport-post-hook-value")
	// Return nil to continue processing
	return nil
}

func HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	fmt.Println("HTTPTransportStreamChunkHook called")
	// Modify chunk in-place
	ctx.Log(schemas.LogLevelInfo, "HTTPTransportStreamChunkHook called")
	if chunk.BifrostChatResponse != nil && chunk.BifrostChatResponse.Choices != nil && len(chunk.BifrostChatResponse.Choices) > 0 && chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice != nil && chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice.Delta != nil && chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice.Delta.Content != nil {
		*chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice.Delta.Content += " - modified by hello-world-plugin"
	}
	// Return the modified chunk
	return chunk, nil
}

func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	value1 := ctx.Value(transportPreHookKey)
	fmt.Println("value1:", value1)
	ctx.SetValue(preHookKey, "pre-hook-value")
	ctx.Log(schemas.LogLevelInfo, "PreLLMHook called")
	fmt.Println("PreLLMHook called")
	return req, nil, nil
}

func PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	fmt.Println("PostLLMHook called")
	value1 := ctx.Value(transportPreHookKey)
	fmt.Println("value1:", value1)
	value2 := ctx.Value(preHookKey)
	fmt.Println("value2:", value2)
	ctx.Log(schemas.LogLevelInfo, "PostLLMHook called")
	return resp, bifrostErr, nil
}

func Cleanup() error {
	fmt.Println("Cleanup called")
	return nil
}
