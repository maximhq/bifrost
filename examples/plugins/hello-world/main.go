package main

import (
	"context"
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

func TransportInterceptor(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	fmt.Println("TransportInterceptor called")
	return headers, body, nil
}

func PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	fmt.Println("PreHook called")
	return req, nil, nil
}

func PostHook(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	fmt.Println("PostHook called")
	return resp, bifrostErr, nil
}

func Cleanup() error {
	fmt.Println("Cleanup called")
	return nil
}
