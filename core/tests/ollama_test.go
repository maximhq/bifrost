// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of schemas, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestOllama(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	config := TestConfig{
		Provider:       schemas.Ollama,
		TextModel:      "llama3.2",
		ChatModel:      "llama3.2",
		SetupText:      false, // Ollama does not support text completion
		SetupToolCalls: true,
		SetupImage:     true,
		SetupBaseImage: true,
	}

	SetupAllRequests(bifrost, config)
}
