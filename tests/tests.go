package tests

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost"
	"github.com/maximhq/bifrost/interfaces"
	"github.com/maximhq/maxim-go"
)

// TestConfig holds configuration for test requests
type TestConfig struct {
	Provider             interfaces.SupportedModelProvider
	ChatModel            string
	TextModel            string
	Messages             []string
	SetupText            bool
	SetupToolCalls       bool
	SetupImage           bool
	SetupBaseImage       bool
	CustomTextCompletion *string
	CustomParams         *interfaces.ModelParameters
}

// Common test messages used across providers
var CommonTestMessages = []string{
	"Hello! How are you today?",
	"Tell me a joke!",
	"What's your favorite programming language?",
}

// Common weather tool parameters
var WeatherToolParams = interfaces.ModelParameters{
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

// setupTextCompletionRequest sets up a text completion request
func setupTextCompletionRequest(bifrost *bifrost.Bifrost, config TestConfig, ctx context.Context) {
	text := "Hello world!"
	if config.CustomTextCompletion != nil {
		text = *config.CustomTextCompletion
	}

	params := interfaces.ModelParameters{}
	if config.CustomParams != nil {
		params = *config.CustomParams
	}

	go func() {
		result, err := bifrost.TextCompletionRequest(config.Provider, &interfaces.BifrostRequest{
			Model: config.TextModel,
			Input: interfaces.RequestInput{
				TextCompletionInput: &text,
			},
			Params: &params,
		}, ctx)
		if err != nil {
			fmt.Printf("\nError in %s text completion: %v\n", config.Provider, err.Error.Message)
		} else {
			fmt.Printf("\n🐒 %s Text Completion Result: %s\n", config.Provider, *result.Choices[0].Message.Content)
		}
	}()
}

// setupChatCompletionRequests sets up multiple chat completion requests
func setupChatCompletionRequests(bifrost *bifrost.Bifrost, config TestConfig, ctx context.Context) {
	messages := config.Messages
	if len(messages) == 0 {
		messages = CommonTestMessages
	}

	params := interfaces.ModelParameters{}
	if config.CustomParams != nil {
		params = *config.CustomParams
	}

	for i, message := range messages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(config.Provider, &interfaces.BifrostRequest{
				Model: config.ChatModel,
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: &params,
			}, ctx)
			if err != nil {
				fmt.Printf("\nError in %s request %d: %v\n", config.Provider, index+1, err.Error.Message)
			} else {
				fmt.Printf("\n🐒 %s Chat Completion Result %d: %s\n", config.Provider, index+1, *result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}
}

// setupImageTests sets up image input tests
func setupImageTests(bifrost *bifrost.Bifrost, config TestConfig, ctx context.Context) {
	params := interfaces.ModelParameters{}
	if config.CustomParams != nil {
		params = *config.CustomParams
	}

	// URL image test
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

	if config.Provider == interfaces.Anthropic {
		urlImageMessages[0].ImageContent.Type = maxim.StrPtr("url")
	}

	go func() {
		result, err := bifrost.ChatCompletionRequest(config.Provider, &interfaces.BifrostRequest{
			Model: config.ChatModel,
			Input: interfaces.RequestInput{
				ChatCompletionInput: &urlImageMessages,
			},
			Params: &params,
		}, ctx)
		if err != nil {
			fmt.Printf("\nError in %s URL image request: %v\n", config.Provider, err.Error.Message)
		} else {
			fmt.Printf("\n🐒 %s URL Image Result: %s\n", config.Provider, *result.Choices[0].Message.Content)
		}
	}()

	// Base64 image test (only for providers that support it)
	if config.SetupBaseImage {
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
			result, err := bifrost.ChatCompletionRequest(config.Provider, &interfaces.BifrostRequest{
				Model: config.ChatModel,
				Input: interfaces.RequestInput{
					ChatCompletionInput: &base64ImageMessages,
				},
				Params: &params,
			}, ctx)
			if err != nil {
				fmt.Printf("\nError in %s base64 image request: %v\n", config.Provider, err.Error.Message)
			} else {
				fmt.Printf("\n🐒 %s Base64 Image Result: %s\n", config.Provider, *result.Choices[0].Message.Content)
			}
		}()
	}
}

// setupToolCalls sets up function calling tests
func setupToolCalls(bifrost *bifrost.Bifrost, config TestConfig, ctx context.Context) {
	messages := []string{"What's the weather like in Mumbai?"}

	params := WeatherToolParams
	if config.CustomParams != nil {
		customParams := *config.CustomParams
		if customParams.Tools != nil {
			params.Tools = customParams.Tools
		}
		if customParams.MaxTokens != nil {
			params.MaxTokens = customParams.MaxTokens
		}
	}

	for i, message := range messages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(config.Provider, &interfaces.BifrostRequest{
				Model: config.ChatModel,
				Input: interfaces.RequestInput{
					ChatCompletionInput: &messages,
				},
				Params: &params,
			}, ctx)
			if err != nil {
				fmt.Printf("\nError in %s tool call request %d: %v\n", config.Provider, index+1, err.Error.Message)
			} else {
				if result.Choices[0].Message.ToolCalls != nil && len(*result.Choices[0].Message.ToolCalls) > 0 {
					toolCall := *result.Choices[0].Message.ToolCalls
					fmt.Printf("\n🐒 %s Tool Call Result %d: %s\n", config.Provider, index+1, toolCall[0].Function.Arguments)
				} else {
					fmt.Printf("\n🐒 %s No tool calls in response %d\n", config.Provider, index+1)
					if result.ExtraFields.RawResponse != nil {
						fmt.Println("\nRaw JSON Response", result.ExtraFields.RawResponse)
					}
				}
			}
		}(message, delay, i)
	}
}

// setupAllRequests sets up all test requests for a provider
func SetupAllRequests(bifrost *bifrost.Bifrost, config TestConfig) {
	ctx := context.Background()

	if config.SetupText {
		setupTextCompletionRequest(bifrost, config, ctx)
	}

	setupChatCompletionRequests(bifrost, config, ctx)

	if config.SetupImage {
		setupImageTests(bifrost, config, ctx)
	}

	if config.SetupToolCalls {
		setupToolCalls(bifrost, config, ctx)
	}
}
