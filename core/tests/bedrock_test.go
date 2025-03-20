// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of schemas, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestBedrock(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	maxTokens := 4096
	textCompletion := "\n\nHuman:<prompt>\n\nAssistant:"

	config := TestConfig{
		Provider:       schemas.Bedrock,
		TextModel:      "anthropic.claude-v2:1",
		ChatModel:      "anthropic.claude-3-sonnet-20240229-v1:0",
		SetupText:      true,
		SetupToolCalls: true,
		SetupImage:     true,
		SetupBaseImage: false,
		CustomParams: &schemas.ModelParameters{
			MaxTokens: &maxTokens,
		},
		CustomTextCompletion: &textCompletion,
	}

	SetupAllRequests(bifrost, config)
	bifrost.Cleanup()
}
