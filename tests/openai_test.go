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

// setupOpenAIRequests sends multiple test requests to OpenAI
func setupOpenAIRequests(bifrost *bifrost.Bifrost) {
	text := "Hello world!"
	ctx := context.Background()

	// Text completion request
	go func() {
		result, err := bifrost.TextCompletionRequest(interfaces.OpenAI, &interfaces.BifrostRequest{
			Model: "gpt-4o-mini",
			Input: interfaces.RequestInput{
				TextCompletionInput: &text,
			},
			Params: nil,
		}, ctx)
		if err != nil {
			fmt.Println("Error:", err)
		} else {
			fmt.Println("🐒 Text Completion Result:", result.Choices[0].Message.Content)
		}
	}()

	// Regular chat completion requests
	openAIMessages := []string{
		"Hello! How are you today?",
		"Tell me a joke!",
		"What's your favorite programming language?",
	}

	for i, message := range openAIMessages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.OpenAI, &interfaces.BifrostRequest{
				Model: "gpt-4o-mini",
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: nil,
			}, ctx)
			if err != nil {
				fmt.Printf("Error in OpenAI request %d: %v\n", index+1, err)
			} else {
				fmt.Printf("🐒 Chat Completion Result %d: %s\n", index+1, *result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}

	// Image input tests
	setupOpenAIImageTests(bifrost, ctx)

	// Tool calls test
	setupOpenAIToolCalls(bifrost, ctx)
}

// setupOpenAIImageTests tests OpenAI's image input capabilities
func setupOpenAIImageTests(bifrost *bifrost.Bifrost, ctx context.Context) {
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
		result, err := bifrost.ChatCompletionRequest(interfaces.OpenAI, &interfaces.BifrostRequest{
			Model: "gpt-4-turbo",
			Input: interfaces.RequestInput{
				ChatCompletionInput: &urlImageMessages,
			},
			Params: nil,
		}, ctx)
		if err != nil {
			fmt.Printf("Error in OpenAI URL image request: %v\n", err)
		} else {
			fmt.Printf("🐒 URL Image Result: %s\n", result.Choices[0].Message.Content)
		}
	}()

	// Test with base64 image
	base64ImageMessages := []interfaces.Message{
		{
			Role:    interfaces.RoleUser,
			Content: maxim.StrPtr("What is this image about?"),
			ImageContent: &interfaces.ImageContent{
				URL: "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k=",
			},
		},
	}

	go func() {
		result, err := bifrost.ChatCompletionRequest(interfaces.OpenAI, &interfaces.BifrostRequest{
			Model: "gpt-4-turbo",
			Input: interfaces.RequestInput{
				ChatCompletionInput: &base64ImageMessages,
			},
			Params: nil,
		}, ctx)
		if err != nil {
			fmt.Printf("Error in OpenAI base64 image request: %v\n", err)
		} else {
			fmt.Printf("🐒 Base64 Image Result: %s\n", result.Choices[0].Message.Content)
		}
	}()
}

// setupOpenAIToolCalls tests OpenAI's function calling capability
func setupOpenAIToolCalls(bifrost *bifrost.Bifrost, ctx context.Context) {
	openAIMessages := []string{
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

	for i, message := range openAIMessages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.OpenAI, &interfaces.BifrostRequest{
				Model: "gpt-4o-mini",
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: &params,
			}, ctx)
			if err != nil {
				fmt.Printf("Error in OpenAI tool call request %d: %v\n", index+1, err)
			} else {
				toolCall := *result.Choices[0].Message.ToolCalls
				fmt.Printf("🐒 Tool Call Result %d: %s\n", index+1, toolCall[0].Function.Arguments)
			}
		}(message, delay, i)
	}
}

func TestOpenAI(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	setupOpenAIRequests(bifrost)

	bifrost.Cleanup()
}
