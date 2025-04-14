// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of interfaces, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	"github.com/maximhq/bifrost/interfaces"
)

func TestAzure(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	config := TestConfig{
		Provider:       interfaces.Azure,
		ChatModel:      "gpt-4o",
		SetupText:      false, // gpt-4o does not support text completion
		SetupToolCalls: true,
		SetupImage:     true,
		SetupBaseImage: false,
	}

	SetupAllRequests(bifrost, config)
	bifrost.Cleanup()
}
