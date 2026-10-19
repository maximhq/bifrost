package elevenlabs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestElevenlabsListModelsWithoutCatalog(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`[{"model_id":"eleven_turbo_v2_5","name":"Turbo"}]`))
				} else {
					_, _ = w.Write([]byte(`{"detail":{"status":"invalid_api_key","message":"invalid api key"}}`))
				}
			}))
			defer server.Close()
			provider := elevenlabs.NewElevenlabsProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
			}, bifrost.NewNoOpLogger())
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			response, failure := provider.ListModels(ctx, []schemas.Key{{
				ID: "key-1", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("test-key"),
			}}, &schemas.BifrostListModelsRequest{Provider: schemas.Elevenlabs})
			if status != http.StatusOK {
				if failure == nil || failure.StatusCode == nil || *failure.StatusCode != status {
					t.Fatalf("upstream status must be preserved, got %+v", failure)
				}
				return
			}
			if failure != nil {
				t.Fatal(failure.GetErrorString())
			}
			if len(response.Data) != 1 || response.Data[0].ID != "elevenlabs/eleven_turbo_v2_5" {
				t.Fatalf("SDK without a catalog must retain the upstream-only listing, got %+v", response.Data)
			}
		})
	}
}

func TestElevenlabs(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")) == "" {
		t.Skip("Skipping Elevenlabs tests because ELEVENLABS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	realtimeAgentID := strings.TrimSpace(os.Getenv("ELEVENLABS_AGENT_ID"))
	hasRealtimeAgent := false

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Elevenlabs,
		SpeechSynthesisModel: "eleven_turbo_v2_5",
		TranscriptionModel:   "scribe_v1",
		RealtimeModel:        realtimeAgentID,
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            false,
			CompletionStream:      false,
			MultiTurnConversation: false,
			ToolCalls:             false,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   false,
			Embedding:             false,
			Reasoning:             false,
			ListModels:            false,
			Realtime:              hasRealtimeAgent,
		},
	}

	t.Run("ElevenlabsTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
