package elevenlabs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestElevenlabsListModelsWithoutCatalog(t *testing.T) {
	for _, tc := range []struct {
		status int
		model  string
	}{
		{http.StatusOK, "eleven_turbo_v2_5"},
		{http.StatusOK, "scribe_v2"},
		{http.StatusUnauthorized, ""},
	} {
		t.Run(http.StatusText(tc.status)+"/"+tc.model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = w.Write([]byte(`[{"model_id":"` + tc.model + `","name":"Upstream Model"}]`))
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
			if tc.status != http.StatusOK {
				if failure == nil || failure.StatusCode == nil || *failure.StatusCode != tc.status {
					t.Fatalf("upstream status must be preserved, got %+v", failure)
				}
				return
			}
			if failure != nil {
				t.Fatal(failure.GetErrorString())
			}
			if len(response.Data) != 1 || response.Data[0].ID != "elevenlabs/"+tc.model {
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

// The with-timestamps endpoint reports character timing in seconds as
// character_start_times_seconds / character_end_times_seconds. Speech must pass both
// alignments through to the client under the same names and units.
func TestElevenlabsSpeechWithTimestampsAlignment(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"audio_base64": "AAAA",
			"alignment": {"characters": ["H", "i"], "character_start_times_seconds": [0.0, 0.18], "character_end_times_seconds": [0.18, 0.8]},
			"normalized_alignment": {"characters": ["H", "i"], "character_start_times_seconds": [0.0, 0.18], "character_end_times_seconds": [0.18, 0.8]}
		}`))
	}))
	defer server.Close()

	provider := elevenlabs.NewElevenlabsProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("xi-test")}

	response, bifrostErr := provider.Speech(ctx, key, &schemas.BifrostSpeechRequest{
		Provider: schemas.Elevenlabs,
		Model:    "eleven_v4",
		Input:    &schemas.SpeechInput{Input: "Hi"},
		Params: &schemas.SpeechParameters{
			VoiceConfig:    &schemas.SpeechVoiceInput{Voice: schemas.Ptr("voice-id")},
			WithTimestamps: schemas.Ptr(true),
		},
	})
	require.Nil(t, bifrostErr)
	require.Equal(t, "/v1/text-to-speech/voice-id/with-timestamps", gotPath)
	require.NotNil(t, response.AudioBase64)
	require.Equal(t, "AAAA", *response.AudioBase64)

	// Assert on the JSON a client receives, field names included.
	body, err := sonic.Marshal(response)
	require.NoError(t, err)
	var got map[string]interface{}
	require.NoError(t, sonic.Unmarshal(body, &got))

	want := map[string]interface{}{
		"characters":                    []interface{}{"H", "i"},
		"character_start_times_seconds": []interface{}{0.0, 0.18},
		"character_end_times_seconds":   []interface{}{0.18, 0.8},
	}
	for _, field := range []string{"alignment", "normalized_alignment"} {
		t.Run(field, func(t *testing.T) {
			require.Equal(t, want, got[field])
		})
	}
}
