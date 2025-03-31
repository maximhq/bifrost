package tests

import (
	"bifrost"
	"bifrost/interfaces"
	"context"
	"fmt"
	"testing"
	"time"
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
				TextInput: &text,
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
					ChatInput: &messages,
				},
				Params: nil,
			}, ctx)
			if err != nil {
				fmt.Printf("Error in OpenAI request %d: %v\n", index+1, err)
			} else {
				fmt.Printf("🐒 Chat Completion Result %d: %s\n", index+1, result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}

	// Tool calls test
	setupOpenAIToolCalls(bifrost, ctx)
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
					ChatInput: &messages,
				},
				Params: &params,
			}, ctx)
			if err != nil {
				fmt.Printf("Error in OpenAI tool call request %d: %v\n", index+1, err)
			} else {
				toolCall := *result.Choices[0].Message.ToolCalls
				fmt.Printf("🐒 Tool Call Result %d: %s\n", index+1, toolCall[0].Arguments)
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
