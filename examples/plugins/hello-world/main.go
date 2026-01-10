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

func HTTPTransportIntercept(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	fmt.Println("HTTPTransportIntercept called")
	// Modify request in-place
	req.Headers["x-hello-world-plugin"] = "transport-interceptor-value"
	// Store value in context for PreHook/PostHook
	ctx.SetValue(schemas.BifrostContextKey("hello-world-plugin-transport-interceptor"), "transport-interceptor-value")
	// Return nil to continue processing, or return &schemas.HTTPResponse{} to short-circuit
	return nil, nil
}

func PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	value1 := ctx.Value(schemas.BifrostContextKey("hello-world-plugin-transport-interceptor"))
	fmt.Println("value1:", value1)
	ctx.SetValue(schemas.BifrostContextKey("hello-world-plugin-pre-hook"), "pre-hook-value")
	fmt.Println("PreHook called")
	return req, nil, nil
}

func PostHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	fmt.Println("PostHook called")
	value1 := ctx.Value(schemas.BifrostContextKey("hello-world-plugin-transport-interceptor"))
	fmt.Println("value1:", value1)
	value2 := ctx.Value(schemas.BifrostContextKey("hello-world-plugin-pre-hook"))
	fmt.Println("value2:", value2)
	return resp, bifrostErr, nil
}

func Cleanup() error {
	fmt.Println("Cleanup called")
	return nil
}
