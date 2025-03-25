package tests

import (
	"bifrost"
	"bifrost/interfaces"
	"context"
	"fmt"
	"testing"
	"time"
)

// setupBedrockRequests sends multiple test requests to Bedrock
func setupBedrockRequests(bifrost *bifrost.Bifrost) {
	bedrockMessages := []string{
		"What's your favorite programming language?",
		"Can you help me write a Go function?",
		"What's the best way to learn programming?",
		"Tell me about artificial intelligence.",
	}

	ctx := context.Background()

	go func() {
		params := interfaces.ModelParameters{
			ExtraParams: map[string]interface{}{
				"max_tokens_to_sample": 4096,
			},
		}
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
			fmt.Println("🤖 Text Completion Result:", result.Choices[0].Message.Content)
		}
	}()

	params := interfaces.ModelParameters{
		ExtraParams: map[string]interface{}{
			"max_tokens": 4096,
		},
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
				fmt.Printf("🤖 Chat Completion Result %d: %s\n", index+1, result.Choices[0].Message.Content)
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
