package tests

import (
	"bifrost"
	"bifrost/interfaces"
	"fmt"
	"testing"
	"time"
)

// setupOpenAIRequests sends multiple test requests to OpenAI
func setupOpenAIRequests(bifrost *bifrost.Bifrost) {
	// Text completion request
	go func() {
		result, err := bifrost.TextCompletionRequest(interfaces.OpenAI, "gpt-4o-mini", "Hello world!", nil)
		if err != nil {
			fmt.Println("Error:", err)
		} else {
			fmt.Println("🐒 Text Completion Result:", result.Choices[0].Message.Content)
		}
	}()

	// Chat completion requests with different messages and delays
	openAIMessages := []string{
		"Hello! How are you today?",
		"What's the weather like?",
		"Tell me a joke!",
		"What's your favorite programming language?",
	}

	for i, message := range openAIMessages {
		delay := time.Duration(100*(i+1)) * time.Millisecond
		go func(msg string, delay time.Duration, index int) {
			time.Sleep(delay)
			messages := []interfaces.Message{
				{
					Role:    interfaces.UserRole,
					Content: &msg,
				},
			}
			result, err := bifrost.ChatCompletionRequest(interfaces.OpenAI, "gpt-4o-mini", messages, nil)
			if err != nil {
				fmt.Printf("Error in OpenAI request %d: %v\n", index+1, err)
			} else {
				fmt.Printf("🐒 Chat Completion Result %d: %s\n", index+1, result.Choices[0].Message.Content)
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
