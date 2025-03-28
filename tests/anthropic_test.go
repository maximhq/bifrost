package tests

import (
	"bifrost"
	"bifrost/interfaces"
	"context"
	"fmt"
	"testing"
	"time"
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
				TextInput: &text,
			},
			Params: &params,
		}, ctx)
		if err != nil {
			fmt.Println("Error:", err)
		} else {
			fmt.Println("🤖 Text Completion Result:", result.Choices[0].Message.Content)
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
					ChatInput: &messages,
				},
				Params: &params,
			}, ctx)

			if err != nil {
				fmt.Printf("Error in Anthropic request %d: %v\n", index+1, err)
			} else {
				fmt.Printf("🤖 Chat Completion Result %d: %s\n", index+1, result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}

	// Tool calls test
	setupAnthropicToolCalls(bifrost, ctx)
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
					ChatInput: &messages,
				},
				Params: &params,
			}, ctx)

			if err != nil {
				fmt.Printf("Error in Anthropic tool call request %d: %v\n", index+1, err)
			} else {
				toolCall := *result.Choices[1].Message.ToolCalls
				fmt.Printf("🤖 Tool Call Result %d: %s\n", index+1, toolCall[0].Arguments)
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
