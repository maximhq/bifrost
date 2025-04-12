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
		SetupText:      false,
		SetupToolCalls: true,
		SetupImage:     true,
		SetupBaseImage: false,
	}

	SetupAllRequests(bifrost, config)
	bifrost.Cleanup()
}
