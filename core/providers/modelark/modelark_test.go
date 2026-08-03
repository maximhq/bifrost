package modelark_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestModelArk(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MODELARK_API_KEY")) == "" {
		t.Skip("Skipping ModelArk tests because MODELARK_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.ModelArk,
		VideoGenerationModel: "seedance-1-0-pro-fast-251015",
		Scenarios: llmtests.TestScenarios{
			VideoGeneration: true,
			VideoRetrieve:   true,
			VideoDownload:   false, // disabled for now because of long running operations
			VideoRemix:      false,
			VideoList:       false,
			VideoDelete:     false,
		},
	}

	t.Run("ModelArkTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
