package main

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

func Init(config any) error {
	fmt.Println("Init called")
	return nil
}

func GetName() string {
	return "Hello World Plugin"
}

func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	fmt.Println("HTTPTransportPreHook called")
	// Modify request in-place
	req.Headers["x-hello-world-plugin"] = "transport-pre-hook-value"
	// Store value in context for PreHook/PostHook
	ctx.SetValue(schemas.BifrostContextKey("hello-world-plugin-transport-pre-hook"), "transport-pre-hook-value")
	// Return nil to continue processing, or return &schemas.HTTPResponse{} to short-circuit
	return nil, nil
}

func HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	fmt.Println("HTTPTransportPostHook called")
	// Modify response in-place
	resp.Headers["x-hello-world-plugin"] = "transport-post-hook-value"
	// Store value in context
	ctx.SetValue(schemas.BifrostContextKey("hello-world-plugin-transport-post-hook"), "transport-post-hook-value")
	// Return nil to continue processing
	return nil
}

func HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	fmt.Println("HTTPTransportStreamChunkHook called")
	// Modify chunk in-place
	if chunk.BifrostChatResponse != nil && chunk.BifrostChatResponse.Choices != nil && len(chunk.BifrostChatResponse.Choices) > 0 && chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice != nil && chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice.Delta != nil && chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice.Delta.Content != nil {
		*chunk.BifrostChatResponse.Choices[0].ChatStreamResponseChoice.Delta.Content += " - modified by hello-world-plugin"
	}
	// Return the modified chunk
	return chunk, nil
}

func PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	value1 := ctx.Value(schemas.BifrostContextKey("hello-world-plugin-transport-pre-hook"))
	fmt.Println("value1:", value1)
	ctx.SetValue(schemas.BifrostContextKey("hello-world-plugin-pre-hook"), "pre-hook-value")
	fmt.Println("PreHook called")
	return req, nil, nil
}

func PostHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	fmt.Println("PostHook called")
	value1 := ctx.Value(schemas.BifrostContextKey("hello-world-plugin-transport-pre-hook"))
	fmt.Println("value1:", value1)
	value2 := ctx.Value(schemas.BifrostContextKey("hello-world-plugin-pre-hook"))
	fmt.Println("value2:", value2)
	return resp, bifrostErr, nil
}

func Cleanup() error {
	fmt.Println("Cleanup called")
	return nil
}
