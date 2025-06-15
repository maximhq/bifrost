// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of schemas, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestMistral(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	config := TestConfig{
		Provider:       schemas.Mistral,
		TextModel:      "ministral-3b-2410",
		ChatModel:      "pixtral-12b-latest",
		SetupText:      false, // Mistral does not support text completion
		SetupToolCalls: true,
		SetupImage:     true,
		SetupBaseImage: true,
		Fallbacks: []schemas.Fallback{
			{
				Provider: schemas.Anthropic,
				Model:    "claude-3-7-sonnet-20250219",
			},
		},
	}

	SetupAllRequests(bifrost, config)
}
