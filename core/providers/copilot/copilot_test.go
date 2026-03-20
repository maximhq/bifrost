package copilot_test

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
)

const tokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"

// validateCopilotToken does a quick pre-flight HTTP call to check the token is
// valid before spinning up the full test suite, avoiding long retry-induced waits.
func validateCopilotToken(token string) error {
	req, err := http.NewRequest(http.MethodGet, tokenExchangeURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")
	req.Header.Set("editor-version", "vscode/1.111.0")
	req.Header.Set("editor-plugin-version", "copilot-chat/0.40.0")
	req.Header.Set("user-agent", "GitHubCopilotChat/0.40.0")
	req.Header.Set("copilot-integration-id", "vscode-chat")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token exchange returned %d — ensure the token is a GitHub OAuth token (ghu_/ghp_) with an active Copilot subscription", resp.StatusCode)
	}
	return nil
}

func TestCopilot(t *testing.T) {
	t.Parallel()

	token := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_TOKEN"))
	if token == "" {
		t.Skip("Skipping Copilot tests because GITHUB_COPILOT_TOKEN is not set")
	}

	if err := validateCopilotToken(token); err != nil {
		t.Skipf("Skipping Copilot tests: %v", err)
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:    schemas.Copilot,
		ChatModel:   "gpt-4o",
		VisionModel: "gpt-4o",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Copilot, Model: "gpt-4o-mini"},
		},
		Scenarios: llmtests.TestScenarios{
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false, // Copilot API does not support external image URLs
			ImageBase64:           true,
			MultipleImages:        false, // Copilot API does not support external image URLs
			FileBase64:            false, // Copilot API does not support inline document inputs
			FileURL:               false, // Copilot API does not support inline document inputs
			CompleteEnd2End:       false, // CompleteEnd2End uses external image URLs
			StructuredOutputs:     true,
			Embedding:             false, // Not supported
			TextCompletion:        false, // Not supported
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			ImageGeneration:       false, // Not supported
			ImageGenerationStream: false, // Not supported
			ImageEdit:             false, // Not supported
			ImageEditStream:       false, // Not supported
			ImageVariation:        false, // Not supported
			BatchCreate:           false, // Not supported
			BatchList:             false, // Not supported
			BatchRetrieve:         false, // Not supported
			BatchCancel:           false, // Not supported
			BatchResults:          false, // Not supported
			FileUpload:            false, // Not supported
			FileList:              false, // Not supported
			FileRetrieve:          false, // Not supported
			FileDelete:            false, // Not supported
			FileContent:           false, // Not supported
			ListModels:            true,
		},
	}

	t.Run("CopilotTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})

	client.Shutdown()
}
