package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/sdk"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: plugin-host <plugin-binary-path>")
		os.Exit(1)
	}

	pluginPath := os.Args[1]

	fmt.Printf("Loading plugin from: %s\n", pluginPath)

	// Load the plugin
	plugin, err := sdk.LoadPlugin(pluginPath)
	if err != nil {
		log.Fatalf("Failed to load plugin: %v", err)
	}

	fmt.Printf("Plugin loaded successfully: %s\n", plugin.GetName())

	// Test the plugin with a sample request
	ctx := context.Background()
	req := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-3.5-turbo",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: schemas.ModelChatMessageRoleUser,
					Content: schemas.MessageContent{
						ContentStr: stringPtr("Hello, world!"),
					},
				},
			},
		},
	}

	fmt.Println("\n--- Testing PreHook ---")
	modifiedReq, shortCircuit, err := plugin.PreHook(&ctx, req)
	if err != nil {
		log.Printf("PreHook error: %v", err)
	} else {
		fmt.Printf("PreHook completed successfully\n")
		fmt.Printf("Short circuit: %v\n", shortCircuit)
		if modifiedReq.Params != nil && modifiedReq.Params.Temperature != nil {
			fmt.Printf("Temperature set to: %f\n", *modifiedReq.Params.Temperature)
		}
	}

	// Test PostHook with a sample response
	fmt.Println("\n--- Testing PostHook ---")
	response := &schemas.BifrostResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				Message: schemas.BifrostMessage{
					Role: schemas.ModelChatMessageRoleAssistant,
					Content: schemas.MessageContent{
						ContentStr: stringPtr("Hello! How can I help you today?"),
					},
				},
			},
		},
	}

	modifiedResp, modifiedErr, err := plugin.PostHook(&ctx, response, nil)
	if err != nil {
		log.Printf("PostHook error: %v", err)
	} else {
		fmt.Printf("PostHook completed successfully\n")
		fmt.Printf("Response choices: %d\n", len(modifiedResp.Choices))
		fmt.Printf("Error: %v\n", modifiedErr)
	}

	// Test cleanup
	fmt.Println("\n--- Testing Cleanup ---")
	err = plugin.Cleanup()
	if err != nil {
		log.Printf("Cleanup error: %v", err)
	} else {
		fmt.Printf("Cleanup completed successfully\n")
	}

	fmt.Println("\nPlugin test completed!")
}

func stringPtr(s string) *string {
	return &s
}
