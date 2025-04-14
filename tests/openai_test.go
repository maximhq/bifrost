// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of interfaces, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	"github.com/maximhq/bifrost/interfaces"
)

func TestOpenAI(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	config := TestConfig{
		Provider:       interfaces.OpenAI,
		ChatModel:      "gpt-4o-mini",
		SetupText:      false, // OpenAI does not support text completion
		SetupToolCalls: true,
		SetupImage:     true,
		SetupBaseImage: false,
	}

	SetupAllRequests(bifrost, config)
	bifrost.Cleanup()
}
