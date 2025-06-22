package main

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/sdk"
)

// ExamplePlugin is a simple plugin implementation
type ExamplePlugin struct{}

// GetName returns the plugin name
func (p *ExamplePlugin) GetName() string {
	return "example-plugin"
}

// PreHook modifies the request before it goes to the provider
func (p *ExamplePlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	fmt.Printf("[%s] PreHook: Processing request for model %s from provider %s\n", p.GetName(), req.Model, req.Provider)

	// Example: Modify temperature if not set
	if req.Params == nil {
		req.Params = &schemas.ModelParameters{}
	}
	if req.Params.Temperature == nil {
		temp := 0.7
		req.Params.Temperature = &temp
		fmt.Printf("[%s] PreHook: Set default temperature to %f\n", p.GetName(), temp)
	}

	return req, nil, nil
}

// PostHook modifies the response after it comes from the provider
func (p *ExamplePlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if result != nil {
		fmt.Printf("[%s] PostHook: Processing response with %d choices\n", p.GetName(), len(result.Choices))
	}
	if err != nil {
		fmt.Printf("[%s] PostHook: Processing error: %s\n", p.GetName(), err.Error.Message)
	}

	return result, err, nil
}

// Cleanup performs any necessary cleanup
func (p *ExamplePlugin) Cleanup() error {
	fmt.Printf("[%s] Cleanup: Plugin shutting down\n", p.GetName())
	return nil
}

func main() {
	// Create the plugin implementation
	plugin := &ExamplePlugin{}

	// Serve the plugin using the SDK
	sdk.ServePlugin(plugin)
}

func (p *ExamplePlugin) SetLogger(logger schemas.Logger) {
	// no-op
}
