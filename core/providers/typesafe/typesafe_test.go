package typesafe_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestTypesafe(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")) == "" {
		t.Skip("Skipping Typesafe tests because TYPESAFE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:      schemas.Typesafe,
		DecisionModel: "jev-1.13.0",
		Scenarios: llmtests.TestScenarios{
			// Typesafe serves the decision operation only; every other operation
			// returns the standard unsupported-operation error. ListModels is
			// served from the static catalog without an upstream call.
			Decision:   true,
			ListModels: true,
		},
	}

	t.Run("TypesafeTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
