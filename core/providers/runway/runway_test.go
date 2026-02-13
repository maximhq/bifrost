package runway_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestRunway(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("RUNWAY_API_KEY")) == "" {
		t.Skip("Skipping Runway tests because RUNWAY_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Runway,
		VideoGenerationModel: "gen4.5",
		Scenarios: llmtests.TestScenarios{
			VideoGeneration: true,
			VideoRetrieve:   true,
			VideoDownload:   true,
			VideoList:       false,
			VideoDelete:     false,
		},
	}

	t.Run("RunwayTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
	client.Shutdown()
}
