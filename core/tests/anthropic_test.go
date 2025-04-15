// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of schemas, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestAnthropic(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	maxTokens := 4096

	config := TestConfig{
		Provider:       schemas.Anthropic,
		TextModel:      "claude-2.1",
		ChatModel:      "claude-3-5-sonnet-20240620",
		SetupText:      true,
		SetupToolCalls: false, // available in 3.7 sonnet
		SetupImage:     true,
		SetupBaseImage: true,
		CustomParams: &schemas.ModelParameters{
			MaxTokens: &maxTokens,
		},
	}

	SetupAllRequests(bifrost, config)

	bifrost.Cleanup()
}
