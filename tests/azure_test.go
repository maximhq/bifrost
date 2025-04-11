package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost"
	"github.com/maximhq/bifrost/interfaces"

	"github.com/maximhq/maxim-go"
)

// setupAzureRequests sends multiple test requests to Azure
func setupAzureRequests(bifrost *bifrost.Bifrost) {
	text := "Hello world!"
	ctx := context.Background()

	// Text completion request
	go func() {
		result, err := bifrost.TextCompletionRequest(interfaces.Azure, &interfaces.BifrostRequest{
			Model: "gpt-4o",
			Input: interfaces.RequestInput{
				TextCompletionInput: &text,
			},
			Params: nil,
		}, ctx)
		if err != nil {
			fmt.Println("Error:", err.Error.Message)
		} else {
			fmt.Println("🐒 Azure Text Completion Result:", result.Choices[0].Message.Content)
		}
	}()

	// Regular chat completion requests
	azureMessages := []string{
		"Hello! How are you today?",
		"Tell me a joke!",
		"What's your favorite programming language?",
	}

	for i, message := range azureMessages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Azure, &interfaces.BifrostRequest{
				Model: "gpt-4o",
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: nil,
			}, ctx)
			if err != nil {
				fmt.Printf("Error in Azure request %d: %v\n", index+1, err.Error.Message)
			} else {
				fmt.Printf("🐒 Azure Chat Completion Result %d: %s\n", index+1, *result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}

	// Image input tests
	setupAzureImageTests(bifrost, ctx)

	// Tool calls test
	setupAzureToolCalls(bifrost, ctx)
}

// setupAzureImageTests tests Azure's image input capabilities
func setupAzureImageTests(bifrost *bifrost.Bifrost, ctx context.Context) {
	// Test with URL image
	urlImageMessages := []interfaces.Message{
		{
			Role:    interfaces.RoleUser,
			Content: maxim.StrPtr("What is Happening in this picture?"),
			ImageContent: &interfaces.ImageContent{
				URL: "https://upload.wikimedia.org/wikipedia/commons/a/a7/Camponotus_flavomarginatus_ant.jpg",
			},
		},
	}

	go func() {
		result, err := bifrost.ChatCompletionRequest(interfaces.Azure, &interfaces.BifrostRequest{
			Model: "gpt-4o",
			Input: interfaces.RequestInput{
				ChatCompletionInput: &urlImageMessages,
			},
			Params: nil,
		}, ctx)
		if err != nil {
			fmt.Printf("Error in Azure URL image request: %v\n", err.Error.Message)
		} else {
			fmt.Printf("🐒 Azure URL Image Result: %s\n", *result.Choices[0].Message.Content)
		}
	}()
}

// setupAzureToolCalls tests Azure's function calling capability
func setupAzureToolCalls(bifrost *bifrost.Bifrost, ctx context.Context) {
	azureMessages := []string{
		"What's the weather like in Mumbai?",
	}

	params := interfaces.ModelParameters{
		Tools: &[]interfaces.Tool{{
			Type: "function",
			Function: interfaces.Function{
				Name:        "get_weather",
				Description: "Get the current weather in a given location",
				Parameters: interfaces.FunctionParameters{
					Type: "object",
					Properties: map[string]interface{}{
						"location": map[string]interface{}{
							"type":        "string",
							"description": "The city and state, e.g. San Francisco, CA",
						},
						"unit": map[string]interface{}{
							"type": "string",
							"enum": []string{"celsius", "fahrenheit"},
						},
					},
					Required: []string{"location"},
				},
			},
		}},
	}

	for i, message := range azureMessages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Azure, &interfaces.BifrostRequest{
				Model: "gpt-4o",
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: &params,
			}, ctx)
			if err != nil {
				fmt.Printf("Error in Azure tool call request %d: %v\n", index+1, err.Error.Message)
			} else {
				toolCall := *result.Choices[0].Message.ToolCalls
				fmt.Printf("🐒 Azure Tool Call Result %d: %s\n", index+1, toolCall[0].Function.Arguments)
			}
		}(message, delay, i)
	}
}

func TestAzure(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	setupAzureRequests(bifrost)

	bifrost.Cleanup()
}
