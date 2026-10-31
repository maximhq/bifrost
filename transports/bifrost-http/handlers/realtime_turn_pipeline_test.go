package handlers

import (
	"context"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"github.com/stretchr/testify/assert"
)

// TestRealtimeRejectedCancelPreservesInput keeps pending messages available to the next response.
func TestRealtimeRejectedCancelPreservesInput(t *testing.T) {
	session := bfws.NewSession(nil)
	session.RecordRealtimeInput("item", "Hello.", `{"type":"conversation.item.create"}`)
	err := newBifrostErrorFromRealtimeError(schemas.OpenAI, "realtime", nil, &schemas.RealtimeError{Type: "invalid_request_error", Code: "response_cancel_not_active", Message: "Cancellation failed: no active response found"})
	assert.Nil(t, finalizeRealtimeTurnHooksWithError(nil, nil, session, schemas.OpenAI, "realtime", nil, schemas.RTEventError, nil, err))
	assert.Len(t, session.PeekRealtimeTurnInputs(), 1)
}

// BenchmarkRealtimeRejectedCancel measures finalization without consuming a pending turn.
func BenchmarkRealtimeRejectedCancel(b *testing.B) {
	session := bfws.NewSession(nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	err := newBifrostErrorFromRealtimeError(schemas.OpenAI, "realtime", nil, &schemas.RealtimeError{Type: "invalid_request_error", Code: "response_cancel_not_active"})
	b.ReportAllocs()
	for b.Loop() {
		_ = finalizeRealtimeTurnHooksWithError(nil, ctx, session, schemas.OpenAI, "realtime", nil, schemas.RTEventError, nil, err)
	}
}

// transcriptSink records only events handed to the logging queue.
type transcriptSink struct {
	schemas.LLMPlugin
	connectionID     string
	sessionStartedAt time.Time
	itemID           string
	transcript       string
	calls            int
}

// EnqueueRealtimeTranscript records the handoff without a provider or database.
func (s *transcriptSink) EnqueueRealtimeTranscript(connectionID string, sessionStartedAt time.Time, itemID, transcript, _ string) {
	s.sessionStartedAt = sessionStartedAt
	s.connectionID, s.itemID, s.transcript = connectionID, itemID, transcript
	s.calls++
}

// TestRecordRealtimeProviderInput updates pending inputs or delegates to persisted lookup.
func TestRecordRealtimeProviderInput(t *testing.T) {
	sink := &transcriptSink{}
	config := &lib.Config{}
	plugins := []schemas.LLMPlugin{sink}
	config.LLMPlugins.Store(&plugins)
	session := bfws.NewSession(nil)
	raw := `{"type":"conversation.item.input_audio_transcription.completed","item_id":"old","transcript":"late words"}`
	event, err := schemas.ParseRealtimeEvent([]byte(raw))
	assert.NoError(t, err)
	session.RecordRealtimeInput("old", realtimeMissingTranscriptText, "old")
	recordRealtimeProviderInput(config, session, event, raw)
	assert.Zero(t, sink.calls)
	assert.Equal(t, "late words", session.PeekRealtimeTurnInputs()[0].Summary)
	session.ConsumeRealtimeTurnInputs()
	session.RecordRealtimeInput("next", "next turn", "next")
	recordRealtimeProviderInput(config, session, event, raw)
	assert.Equal(t, 1, sink.calls)
	assert.Equal(t, session.ID(), sink.connectionID)
	assert.False(t, sink.sessionStartedAt.IsZero())
	assert.Equal(t, session.StartedAt(), sink.sessionStartedAt)
	assert.Equal(t, "old", sink.itemID)
	assert.Equal(t, "late words", sink.transcript)
	assert.Equal(t, "next", session.PeekRealtimeTurnInputs()[0].ItemID)
	// A transcript need not have a remembered tombstone to reach persisted lookup.
	unknown, err := schemas.ParseRealtimeEvent([]byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"not-in-memory","transcript":"persisted words"}`))
	assert.NoError(t, err)
	recordRealtimeProviderInput(config, session, unknown, string(unknown.RawData))
	assert.Equal(t, 2, sink.calls)
	assert.Equal(t, "not-in-memory", sink.itemID)
	session.Close()
	recordRealtimeProviderInput(config, session, event, raw)
	assert.Equal(t, 2, sink.calls)
}

// TestRealtimeProviderInputLifecycle distinguishes creation from late completion echoes.
func TestRealtimeProviderInputLifecycle(t *testing.T) {
	session := bfws.NewSession(nil)
	record := func(raw string) {
		t.Helper()
		event, err := schemas.ParseRealtimeEvent([]byte(raw))
		assert.NoError(t, err)
		recordRealtimeProviderInput(nil, session, event, raw)
	}
	record(`{"type":"conversation.item.added","item":{"id":"audio","type":"message","role":"user","content":[{"type":"input_audio","transcript":null}]}}`)
	assert.Equal(t, realtimeMissingTranscriptText, session.PeekRealtimeTurnInputs()[0].Summary)
	record(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"audio","transcript":"early words"}`)
	record(`{"type":"conversation.item.done","item":{"id":"audio","type":"message","role":"user","content":[{"type":"input_audio","transcript":null}]}}`)
	inputs := session.ConsumeRealtimeTurnInputs()
	assert.Len(t, inputs, 1)
	assert.Equal(t, "early words", inputs[0].Summary)
	assert.Contains(t, inputs[0].Raw, "early words")

	session.RecordRealtimeToolOutput("tool", "result", "client result")
	record(`{"type":"conversation.item.created","item":{"id":"tool","type":"function_call_output","output":"result"}}`)
	record(`{"type":"conversation.item.done","item":{"id":"tool","type":"function_call_output","output":"result"}}`)
	assert.Len(t, session.ConsumeRealtimeTurnInputs(), 1)

	record(`{"type":"conversation.item.added","item":{"id":"next","type":"message","role":"user","content":[{"type":"input_text","text":"next turn"}]}}`)
	// Completion, retrieved-item and tool-result echoes cannot introduce old inputs.
	for _, raw := range []string{
		`{"type":"conversation.item.done","item":{"id":"audio","type":"message","role":"user","content":[{"type":"input_audio","transcript":null}]}}`,
		`{"type":"conversation.item.retrieved","item":{"id":"audio","type":"message","role":"user","content":[{"type":"input_text","text":"old words"}]}}`,
		`{"type":"conversation.item.created","item":{"id":"tool","type":"function_call_output","output":"result"}}`,
		`{"type":"conversation.item.done","item":{"id":"tool","type":"function_call_output","output":"result"}}`,
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"audio","transcript":"late words"}`,
	} {
		record(raw)
	}
	inputs = session.ConsumeRealtimeTurnInputs()
	assert.Len(t, inputs, 1)
	assert.Equal(t, "next", inputs[0].ItemID)
}

// BenchmarkRealtimeProviderInputDelta measures the common streaming path without input work.
func BenchmarkRealtimeProviderInputDelta(b *testing.B) {
	session := bfws.NewSession(nil)
	event := &schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseTextDelta, Delta: &schemas.RealtimeDelta{Text: "word"}}
	b.ReportAllocs()
	for b.Loop() {
		recordRealtimeProviderInput(nil, session, event, "delta")
	}
}

// TestRealtimeInvalidRequestErrorFinalization distinguishes a propagated provider error from a hook rejection.
func TestRealtimeInvalidRequestErrorFinalization(t *testing.T) {
	for _, active := range []bool{false, true} {
		session := bfws.NewSession(nil)
		if active {
			session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{RequestID: "active", StartedAt: time.Now(), PostHookRunner: func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return result, err
			}})
		}
		inputErr := newRealtimeWireBifrostError(400, "invalid_request_error", "unsupported modality")
		got := finalizeRealtimeTurnHooksWithError(nil, nil, session, schemas.OpenAI, "realtime", nil, schemas.RTEventError, nil, inputErr)
		assert.Nil(t, got)
	}
	session := bfws.NewSession(nil)
	policyErr := newRealtimeWireBifrostError(403, "policy_rejection", "blocked by post-hook")
	session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{PostHookRunner: func(_ *schemas.BifrostContext, _ *schemas.BifrostResponse, _ *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return nil, policyErr
	}})
	assert.Same(t, policyErr, finalizeRealtimeTurnHooksWithError(nil, nil, session, schemas.OpenAI, "realtime", nil, schemas.RTEventError, nil, newRealtimeWireBifrostError(400, "invalid_request_error", "unsupported modality")))
}

func TestMapRealtimeWireErrorFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      *schemas.BifrostError
		wantType string
		wantCode string
	}{
		{name: "an operation the provider does not allow is the caller's mistake, not the server's",
			err: providerUtils.NewUnsupportedOperationError(schemas.LiveRequest, "openai-nolive"), wantType: "invalid_request_error", wantCode: "unsupported_operation"},
		{name: "a wire error keeps its own type",
			err: newRealtimeWireBifrostError(400, "invalid_request_error", "bad frame"), wantType: "invalid_request_error", wantCode: "invalid_request_error"},
		{name: "a spent budget is a quota error",
			err: newRealtimeWireBifrostError(402, "budget_exceeded", "Budget exceeded"), wantType: "insufficient_quota", wantCode: "insufficient_quota"},
		{name: "anything unclassified is a server error",
			err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "boom"}}, wantType: "server_error", wantCode: "server_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errType, code, message, _ := mapRealtimeWireErrorFields(tc.err)
			assert.Equal(t, tc.wantType, errType)
			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, tc.err.Error.Message, message)
		})
	}
}

// TestUpdateRealtimeSessionTracksVoiceType pins that the session keeps the voice's type beside its
// name, so Voice Live pricing can tell an Azure voice from the model's own.
func TestUpdateRealtimeSessionTracksVoiceType(t *testing.T) {
	session := bfws.NewSession(nil)
	for _, tc := range []struct {
		name, raw, wantVoice, wantType string
	}{
		{"object voice", `{"type":"session.update","session":{"voice":{"name":"en-US-AvaNeural","type":"azure-standard"}}}`, "en-US-AvaNeural", "azure-standard"},
		{"nested object voice", `{"type":"session.updated","session":{"audio":{"output":{"voice":{"name":"en-US-Ava","type":"azure-custom"}}}}}`, "en-US-Ava", "azure-custom"},
		{"string voice clears the type", `{"type":"session.update","session":{"voice":"alloy"}}`, "alloy", ""},
	} {
		event, err := schemas.ParseRealtimeEvent([]byte(tc.raw))
		assert.NoError(t, err, tc.name)
		updateRealtimeSessionFromEvent(session, event)
		assert.Equal(t, tc.wantVoice, session.RealtimeVoice(), tc.name)
		assert.Equal(t, tc.wantType, session.RealtimeVoiceType(), tc.name)
	}
}

// TestApplyVoiceLivePricing pins how a Voice Live turn's audio is split for pricing: output audio from
// an Azure standard voice is Standard Speech, from a custom or personal voice Custom Speech, and from
// the model's own voice LLM audio. Input audio is Standard Speech for a model that does not hear
// audio natively (Azure speech to text transcribes it) and LLM audio for a realtime model.
func TestApplyVoiceLivePricing(t *testing.T) {
	for _, tc := range []struct {
		name, model, voiceType         string
		wantInputStandard              int
		wantOutputStandard, wantCustom int
	}{
		{name: "realtime model with an Azure standard voice", model: "gpt-realtime", voiceType: "azure-standard", wantOutputStandard: 52},
		{name: "realtime model with its own voice", model: "gpt-realtime-mini", voiceType: "openai"},
		{name: "realtime model with a custom voice", model: "gpt-realtime", voiceType: "azure-custom", wantCustom: 52},
		{name: "realtime model with a personal voice", model: "gpt-realtime", voiceType: "azure-personal", wantCustom: 52},
		{name: "azure-realtime native voice", model: "azure-realtime", voiceType: "azure-realtime-native"},
		{name: "text model with an Azure standard voice", model: "gpt-4.1", voiceType: "azure-standard", wantInputStandard: 50, wantOutputStandard: 52},
		{name: "text model without a voice type speaks through Azure", model: "gpt-5", wantInputStandard: 50, wantOutputStandard: 52},
	} {
		resp := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Usage: &schemas.ResponsesResponseUsage{
			InputTokens:         70,
			OutputTokens:        66,
			InputTokensDetails:  &schemas.ResponsesResponseInputTokens{TextTokens: 20, AudioTokens: 50},
			OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{TextTokens: 14, AudioTokens: 52},
		}}}
		applyVoiceLivePricing(resp, tc.voiceType, tc.model)
		r := resp.ResponsesResponse
		assert.Equal(t, schemas.PricingSurfaceVoiceLive, r.ExtraFields.PricingSurface, tc.name)
		assert.Equal(t, tc.wantInputStandard, r.Usage.InputTokensDetails.StandardSpeechAudioTokens, tc.name)
		assert.Zero(t, r.Usage.InputTokensDetails.CustomSpeechAudioTokens, tc.name)
		assert.Equal(t, tc.wantOutputStandard, r.Usage.OutputTokensDetails.StandardSpeechAudioTokens, tc.name)
		assert.Equal(t, tc.wantCustom, r.Usage.OutputTokensDetails.CustomSpeechAudioTokens, tc.name)
	}
}
