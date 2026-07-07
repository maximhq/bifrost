package deepgram_test

import (
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/deepgram"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestBuildTranscriptionQueryParams(t *testing.T) {
	t.Parallel()

	language := "en"
	req := &schemas.BifrostTranscriptionRequest{
		Model: "nova-2-general",
		Params: &schemas.TranscriptionParameters{
			Language: &language,
			ExtraParams: map[string]interface{}{
				"punctuate":       true,
				"diarize_model":   "latest",
				"callback":        "https://example.com/hook",
				"callback_method": "POST",
			},
		},
	}

	q := deepgram.BuildTranscriptionQueryParams(req)

	if got := q.Get("model"); got != "nova-2-general" {
		t.Errorf("model = %q, want nova-2-general", got)
	}
	if got := q.Get("language"); got != "en" {
		t.Errorf("language = %q, want en", got)
	}
	if got := q.Get("punctuate"); got != "true" {
		t.Errorf("punctuate = %q, want true", got)
	}
	if got := q.Get("diarize_model"); got != "latest" {
		t.Errorf("diarize_model = %q, want latest", got)
	}
	if q.Has("callback") || q.Has("callback_method") {
		t.Errorf("callback/callback_method must never be forwarded (async webhook mode is unsupported), got query=%v", q)
	}
}

// TestBuildTranscriptionQueryParams_URLNotMutatedOrForwarded guards against a
// regression where Transcription() used to delete "url" from the caller's
// ExtraParams map in-place to keep it off the query string. Since Bifrost's
// retry loop reuses the same *BifrostTranscriptionRequest pointer across
// attempts, that mutation silently broke every retry after the first (the
// second attempt would see an empty ExtraParams and fail the
// file-or-url-required guard). "url" must instead be filtered by
// BuildTranscriptionQueryParams without touching the input map.
func TestBuildTranscriptionQueryParams_URLNotMutatedOrForwarded(t *testing.T) {
	t.Parallel()

	extraParams := map[string]interface{}{
		"url": "https://example.com/audio.wav",
	}
	req := &schemas.BifrostTranscriptionRequest{
		Model: "nova-2-general",
		Params: &schemas.TranscriptionParameters{
			ExtraParams: extraParams,
		},
	}

	q := deepgram.BuildTranscriptionQueryParams(req)
	if q.Has("url") {
		t.Errorf("url must never be forwarded as a query param, got query=%v", q)
	}

	// Simulate a second retry attempt reusing the same request/map pointer.
	if _, ok := extraParams["url"]; !ok {
		t.Fatal("ExtraParams[\"url\"] was mutated/removed by BuildTranscriptionQueryParams; a retry reusing this request would fail the file-or-url-required guard")
	}
	q2 := deepgram.BuildTranscriptionQueryParams(req)
	if q2.Has("url") {
		t.Errorf("url must never be forwarded as a query param on a repeated call either, got query=%v", q2)
	}
}

// TestBuildTranscriptionQueryParams_MultiValuedArraysUseRepeatedKeys guards
// against comma-joining array ExtraParams: Deepgram's keywords/replace/search
// params are documented as multi-valued via key repetition
// (?keywords=a&keywords=b), and comma-joining silently produces a malformed
// request for those.
func TestBuildTranscriptionQueryParams_MultiValuedArraysUseRepeatedKeys(t *testing.T) {
	t.Parallel()

	req := &schemas.BifrostTranscriptionRequest{
		Model: "nova-2-general",
		Params: &schemas.TranscriptionParameters{
			ExtraParams: map[string]interface{}{
				"keywords": []string{"foo:5", "bar:3"},
			},
		},
	}

	q := deepgram.BuildTranscriptionQueryParams(req)
	got := q["keywords"]
	want := []string{"foo:5", "bar:3"}
	if len(got) != len(want) {
		t.Fatalf("keywords = %v, want repeated keys %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("keywords[%d] = %q, want %q", i, got[i], v)
		}
	}
}

func TestBuildSpeakQueryParams(t *testing.T) {
	t.Parallel()

	speed := 1.5
	req := &schemas.BifrostSpeechRequest{
		Model: "aura-asteria-en",
		Params: &schemas.SpeechParameters{
			Speed:          &speed,
			ResponseFormat: "wav",
		},
	}

	q := deepgram.BuildSpeakQueryParams(req)

	if got := q.Get("model"); got != "aura-asteria-en" {
		t.Errorf("model = %q, want aura-asteria-en", got)
	}
	if got := q.Get("speed"); got != "1.5" {
		t.Errorf("speed = %q, want 1.5", got)
	}
	if got := q.Get("encoding"); got != "linear16" {
		t.Errorf("encoding = %q, want linear16", got)
	}
	if got := q.Get("container"); got != "wav" {
		t.Errorf("container = %q, want wav", got)
	}
}

func TestToDeepgramSpeakRequest(t *testing.T) {
	t.Parallel()

	req := &schemas.BifrostSpeechRequest{
		Model: "aura-asteria-en",
		Input: &schemas.SpeechInput{Input: "hello world"},
	}

	out := deepgram.ToDeepgramSpeakRequest(req)
	if out == nil || out.Text != "hello world" {
		t.Fatalf("ToDeepgramSpeakRequest() = %+v, want Text=hello world", out)
	}

	if deepgram.ToDeepgramSpeakRequest(&schemas.BifrostSpeechRequest{}) != nil {
		t.Errorf("ToDeepgramSpeakRequest() with nil Input should return nil")
	}
}

func TestToBifrostTranscriptionResponse(t *testing.T) {
	t.Parallel()

	punctuated := "Hello world"
	lang := "en"
	duration := 1.23
	resp := &deepgram.DeepgramTranscriptionResponse{
		Metadata: &deepgram.DeepgramTranscriptionMetadata{Duration: &duration},
		Results: &deepgram.DeepgramTranscriptionResults{
			Channels: []deepgram.DeepgramTranscriptionChannel{
				{
					DetectedLanguage: &lang,
					Alternatives: []deepgram.DeepgramTranscriptionAlternative{
						{
							Transcript: "hello world",
							Confidence: 0.98,
							Words: []deepgram.DeepgramTranscriptionWord{
								{Word: "hello", Start: 0, End: 0.5, Confidence: 0.99, PunctuatedWord: &punctuated},
							},
						},
					},
				},
			},
		},
	}

	out := deepgram.ToBifrostTranscriptionResponse(resp)
	if out.Text != "hello world" {
		t.Errorf("Text = %q, want %q", out.Text, "hello world")
	}
	if out.Language == nil || *out.Language != "en" {
		t.Errorf("Language = %v, want en", out.Language)
	}
	if out.Duration == nil || *out.Duration != 1.23 {
		t.Errorf("Duration = %v, want 1.23", out.Duration)
	}
	if len(out.Words) != 1 || out.Words[0].Word != "Hello world" {
		t.Fatalf("Words = %+v, want punctuated_word to win", out.Words)
	}

	if deepgram.ToBifrostTranscriptionResponse(nil) != nil {
		t.Errorf("ToBifrostTranscriptionResponse(nil) should return nil")
	}
}

// TestDeepgram runs the comprehensive live test suite against Deepgram's real API.
// Uses Deepgram's lowest-cost tiers to minimize spend: "base" for STT (cheaper than
// nova-2/nova-3) and the standard (non-aura-2) "aura-asteria-en" voice for TTS.
func TestDeepgram(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("DEEPGRAM_API_KEY")) == "" {
		t.Skip("Skipping Deepgram tests because DEEPGRAM_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Deepgram,
		SpeechSynthesisModel: "aura-asteria-en",
		TranscriptionModel:   "base",
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
			Realtime:              false,
		},
	}

	t.Run("DeepgramTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}
