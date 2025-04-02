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

// setupAnthropicRequests sends multiple test requests to Anthropic
func setupAnthropicRequests(bifrost *bifrost.Bifrost) {
	ctx := context.Background()

	maxTokens := 4096

	params := interfaces.ModelParameters{
		MaxTokens: &maxTokens,
	}

	// Text completion request
	go func() {
		text := "Hello world!"

		result, err := bifrost.TextCompletionRequest(interfaces.Anthropic, &interfaces.BifrostRequest{
			Model: "claude-2.1",
			Input: interfaces.RequestInput{
				TextCompletionInput: &text,
			},
			Params: &params,
		}, ctx)
		if err != nil {
			fmt.Println("Error:", err.Error.Message)
		} else {
			fmt.Println("🤖 Text Completion Result:", *result.Choices[0].Message.Content)
		}
	}()

	// Regular chat completion requests
	anthropicMessages := []string{
		"Hello! How are you today?",
		"Tell me a joke!",
		"What's your favorite programming language?",
	}

	for i, message := range anthropicMessages {
		delay := time.Duration(500+100*i) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Anthropic, &interfaces.BifrostRequest{
				Model: "claude-3-7-sonnet-20250219",
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: &params,
			}, ctx)

			if err != nil {
				fmt.Printf("Error in Anthropic request %d: %v\n", index+1, err.Error.Message)
			} else {
				fmt.Printf("🤖 Chat Completion Result %d: %s\n", index+1, *result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}

	// Image input tests
	setupAnthropicImageTests(bifrost, ctx)

	// Tool calls test
	setupAnthropicToolCalls(bifrost, ctx)
}

// setupAnthropicImageTests tests Anthropic's image input capabilities
func setupAnthropicImageTests(bifrost *bifrost.Bifrost, ctx context.Context) {
	// Test with URL image
	urlImageMessages := []interfaces.Message{
		{
			Role:    interfaces.RoleUser,
			Content: maxim.StrPtr("What is Happening in this picture?"),
			ImageContent: &interfaces.ImageContent{
				Type: maxim.StrPtr("url"),
				URL:  "https://upload.wikimedia.org/wikipedia/commons/a/a7/Camponotus_flavomarginatus_ant.jpg",
			},
		},
	}

	maxTokens := 4096

	params := interfaces.ModelParameters{
		MaxTokens: &maxTokens,
	}

	go func() {
		result, err := bifrost.ChatCompletionRequest(interfaces.Anthropic, &interfaces.BifrostRequest{
			Model: "claude-3-7-sonnet-20250219",
			Input: interfaces.RequestInput{
				ChatCompletionInput: &urlImageMessages,
			},
			Params: &params,
		}, ctx)
		if err != nil {
			fmt.Printf("Error in Anthropic URL image request: %v\n", err.Error.Message)
		} else {
			fmt.Printf("🐒 URL Image Result: %s\n", *result.Choices[0].Message.Content)
		}
	}()

	// Test with base64 image
	base64ImageMessages := []interfaces.Message{
		{
			Role:    interfaces.RoleUser,
			Content: maxim.StrPtr("What is this image about?"),
			ImageContent: &interfaces.ImageContent{
				Type:      maxim.StrPtr("base64"),
				URL:       "/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k=",
				MediaType: maxim.StrPtr("image/jpeg"),
			},
		},
	}

	go func() {
		result, err := bifrost.ChatCompletionRequest(interfaces.Anthropic, &interfaces.BifrostRequest{
			Model: "claude-3-7-sonnet-20250219",
			Input: interfaces.RequestInput{
				ChatCompletionInput: &base64ImageMessages,
			},
			Params: &params,
		}, ctx)
		if err != nil {
			fmt.Printf("Error in Anthropic base64 image request: %v\n", err.Error.Message)
		} else {
			fmt.Printf("🐒 Base64 Image Result: %s\n", *result.Choices[0].Message.Content)
		}
	}()
}

// setupAnthropicToolCalls tests Anthropic's function calling capability
func setupAnthropicToolCalls(bifrost *bifrost.Bifrost, ctx context.Context) {
	anthropicMessages := []string{
		"What's the weather like in Mumbai?",
	}

	maxTokens := 4096

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
		MaxTokens: &maxTokens,
	}

	for i, message := range anthropicMessages {
		delay := time.Duration(500+100*i) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Anthropic, &interfaces.BifrostRequest{
				Model: "claude-3-7-sonnet-20250219",
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: &params,
			}, ctx)

			if err != nil {
				fmt.Printf("Error in Anthropic tool call request %d: %v\n", index+1, err.Error.Message)
			} else {
				toolCall := *result.Choices[1].Message.ToolCalls
				fmt.Printf("🤖 Tool Call Result %d: %s\n", index+1, toolCall[0].Function.Arguments)
			}
		}(message, delay, i)
	}
}

func TestAnthropic(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	setupAnthropicRequests(bifrost)

	bifrost.Cleanup()
}
