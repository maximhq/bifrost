// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of schemas, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestCohere(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	config := TestConfig{
		Provider:       schemas.Cohere,
		ChatModel:      "command-a-03-2025",
		SetupText:      false, // Cohere does not support text completion
		SetupToolCalls: true,
		SetupImage:     false,
		SetupBaseImage: false,
	}

	SetupAllRequests(bifrost, config)

	bifrost.Cleanup()
}
