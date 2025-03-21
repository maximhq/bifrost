package tests

import (
	"bifrost"
	"bifrost/interfaces"
	"fmt"
	"testing"
	"time"
)

// setupAnthropicRequests sends multiple test requests to Anthropic
func setupAnthropicRequests(bifrost *bifrost.Bifrost) {
	anthropicMessages := []string{
		"What's your favorite programming language?",
		"Can you help me write a Go function?",
		"What's the best way to learn programming?",
		"Tell me about artificial intelligence.",
	}

	go func() {
		params := interfaces.ModelParameters{
			ExtraParams: map[string]interface{}{
				"max_tokens_to_sample": 4096,
			},
		}
		text := "Hello world!"

		result, err := bifrost.TextCompletionRequest(interfaces.Anthropic, &interfaces.BifrostRequest{
			Model: "claude-2.1",
			Input: interfaces.RequestInput{
				StringInput: &text,
			},
			Params: &params,
		})
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

	for i, message := range anthropicMessages {
		delay := time.Duration(500+100*i) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.UserRole,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Anthropic, &interfaces.BifrostRequest{
				Model: "claude-3-7-sonnet-20250219",
				Input: interfaces.RequestInput{
					MessageInput: &messages,
				},
				Params: &params,
			})

			if err != nil {
				fmt.Printf("Error in Anthropic request %d: %v\n", index+1, err)
			} else {
				fmt.Printf("🤖 Chat Completion Result %d: %s\n", index+1, result.Choices[0].Message.Content)
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
