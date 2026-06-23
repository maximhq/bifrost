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
		ImageEditModel:       "runware:102@1",             // FLUX Fill: supports seedImage + maskImage (inpainting)
		VideoGenerationModel: "klingai:kling-video@3-pro", // set a video model; flip the scenarios below on to exercise it
		Scenarios: llmtests.TestScenarios{
			ImageGeneration: true,
			ImageEdit:       true,
			VideoGeneration: false,
			VideoRetrieve:   false,
			VideoDownload:   false,
		},
	}

	t.Run("RunwareTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
