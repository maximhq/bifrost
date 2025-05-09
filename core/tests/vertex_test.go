// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of schemas, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestVertex(t *testing.T) {
	bifrostClient, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	config := TestConfig{
		Provider:       schemas.Vertex,
		ChatModel:      "google/gemini-2.0-flash-001",
		SetupText:      false, // Vertex does not support text completion
		SetupToolCalls: false,
		SetupImage:     false,
		SetupBaseImage: false,
		CustomParams: &schemas.ModelParameters{
			Region: bifrost.Ptr("us-central1"),
		},
	}

	SetupAllRequests(bifrostClient, config)
	bifrostClient.Cleanup()
}
