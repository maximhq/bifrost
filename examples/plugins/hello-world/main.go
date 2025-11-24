package main

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func Init(config any) error {
	fmt.Println("Init called")
	return nil
}

func GetName() string {
	return "Hello World Plugin"
}

func HTTPTransportMiddleware() schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			fmt.Println("HTTPTransportMiddleware called")
			next(ctx)
		}
	}
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
