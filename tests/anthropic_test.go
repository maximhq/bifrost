package tests

import (
	"testing"

	"github.com/maximhq/bifrost/interfaces"
)

func TestAnthropic(t *testing.T) {
	bifrost, err := getBifrost()
	if err != nil {
		t.Fatalf("Error initializing bifrost: %v", err)
		return
	}

	maxTokens := 4096

	config := TestConfig{
		Provider:       interfaces.Anthropic,
		TextModel:      "claude-2.1",
		ChatModel:      "claude-3-5-sonnet-20240620",
		SetupText:      true,
		SetupToolCalls: false, // available in 3.7 sonnet
		SetupImage:     true,
		SetupBaseImage: true,
		CustomParams: &interfaces.ModelParameters{
			MaxTokens: &maxTokens,
		},
	}

	SetupAllRequests(bifrost, config)

	bifrost.Cleanup()
}
