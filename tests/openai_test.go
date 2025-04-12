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
		SetupToolCalls: true,
		SetupImage:     true,
		SetupBaseImage: false,
	}

	SetupAllRequests(bifrost, config)
	bifrost.Cleanup()
}
