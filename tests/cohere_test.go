package tests

import (
	"bifrost"
	"bifrost/interfaces"
	"context"
	"fmt"
	"testing"
	"time"
)

// setupCohereRequests sends multiple test requests to Cohere
func setupCohereRequests(bifrost *bifrost.Bifrost) {
	text := "Hello world!"

	ctx := context.Background()

	// Text completion request
	go func() {
		result, err := bifrost.TextCompletionRequest(interfaces.Cohere, &interfaces.BifrostRequest{
			Model: "command-a-03-2025",
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

	// Chat completion requests with different messages and delays
	CohereMessages := []string{
		"Hello! How are you today?",
		"What's the weather like?",
		"Tell me a joke!",
		"What's your favorite programming language?",
	}

	for i, message := range CohereMessages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.RoleUser,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.Cohere, &interfaces.BifrostRequest{
				Model: "command-a-03-2025",
				Input: interfaces.RequestInput{
					ChatInput: &messages,
				},
				Params: nil,
			}, ctx)
			if err != nil {
				fmt.Printf("Error in Cohere request %d: %v\n", index+1, err)
			} else {
				fmt.Printf("🐒 Chat Completion Result %d: %s\n", index+1, result.Choices[0].Message.Content)
			}
		}(message, delay, i)
	}
}

func TestCohere(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	setupCohereRequests(bifrost)

	bifrost.Cleanup()
}
