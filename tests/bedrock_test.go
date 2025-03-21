package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost"
	"github.com/maximhq/bifrost/interfaces"
)

// setupBedrockRequests sends multiple test requests to Bedrock
func setupBedrockRequests(bifrost *bifrost.Bifrost) {
	ctx := context.Background()

	maxTokens := 4096

	params := interfaces.ModelParameters{
		MaxTokens: &maxTokens,
	}

	// Text completion request
	go func() {
		text := "\n\nHuman:<prompt>\n\nAssistant:"

		result, err := bifrost.TextCompletionRequest(interfaces.Bedrock, &interfaces.BifrostRequest{
			Model: "anthropic.claude-v2:1",
			Input: interfaces.RequestInput{
				TextInput: &text,
			},
			Params: &params,
		}, ctx)
		if err != nil {
			fmt.Println("Error:", err)
		} else {
			fmt.Println("🤖 Text Completion Result:", *result.Choices[0].Message.Content)
		}
	}()

	// Regular chat completion requests
	bedrockMessages := []string{
		"Hello! How are you today?",
		"Tell me a joke!",
		"What's your favorite programming language?",
	}

	for i, message := range bedrockMessages {
		delay := time.Duration(500+100*i) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Bedrock, &interfaces.BifrostRequest{
				Model: "anthropic.claude-3-sonnet-20240229-v1:0",
				Input: interfaces.RequestInput{
					ChatInput: &messages,
				},
				Params: &params,
			}, ctx)

			if err != nil {
				fmt.Printf("Error in Bedrock request %d: %v\n", index+1, err)
			} else {
				fmt.Printf("🤖 Chat Completion Result %d: %s\n", index+1, *result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}

	// Tool calls test
	setupBedrockToolCalls(bifrost, ctx)
}

// setupBedrockToolCalls tests Bedrock's function calling capability
func setupBedrockToolCalls(bifrost *bifrost.Bifrost, ctx context.Context) {
	bedrockMessages := []string{
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

	for i, message := range bedrockMessages {
		delay := time.Duration(500+100*i) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Bedrock, &interfaces.BifrostRequest{
				Model: "anthropic.claude-3-sonnet-20240229-v1:0",
				Input: interfaces.RequestInput{
					ChatInput: &messages,
				},
				Params: &params,
			}, ctx)

			if err != nil {
				fmt.Printf("Error in Bedrock tool call request %d: %v\n", index+1, err)
			} else {
				if result.Choices[0].Message.ToolCalls != nil && len(*result.Choices[0].Message.ToolCalls) > 0 {
					toolCall := *result.Choices[0].Message.ToolCalls
					fmt.Printf("🤖 Tool Call Result %d: %s\n", index+1, toolCall[0].Function.Arguments)
				} else {
					fmt.Printf("🤖 No tool calls in response %d\n", index+1)
					fmt.Println("Raw JSON Response", result.ExtraFields.RawResponse)
				}
			}
		}(message, delay, i)
	}
}

func TestBedrock(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	setupBedrockRequests(bifrost)

	bifrost.Cleanup()
}
