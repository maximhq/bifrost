package runware_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestRunware(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("RUNWARE_API_KEY")) == "" {
		t.Skip("Skipping Runware tests because RUNWARE_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Runware,
		ImageGenerationModel: "runware:101@1",
		ImageEditModel:       "runware:400@1",
		Scenarios: llmtests.TestScenarios{
			ImageGeneration: true,
			ImageEdit:       true,
		},
	}

	t.Run("RunwareTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
