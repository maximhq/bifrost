package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Fixtures below are trimmed captures from a live Gemini Live
// (BidiGenerateContent) session against models/gemini-3.1-flash-live-preview.

func TestToBifrostRealtimeEvent_SetupComplete(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{"setupComplete": {}}`))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != schemas.RTEventSessionCreated {
		t.Fatalf("Type = %q, want %q", event.Type, schemas.RTEventSessionCreated)
	}
}

func TestToBifrostRealtimeEvent_AudioDeltaOnly(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := json.RawMessage(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQADAAQAEgA="}}]}}}`)
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != schemas.RTEventResponseAudioDelta {
		t.Fatalf("Type = %q, want %q", event.Type, schemas.RTEventResponseAudioDelta)
	}
	if event.Delta == nil || event.Delta.Audio != "AQADAAQAEgA=" {
		t.Fatalf("Delta.Audio = %+v, want AQADAAQAEgA=", event.Delta)
	}
	if event.Delta.Transcript != "" {
		t.Fatalf("Delta.Transcript = %q, want empty (no transcription in this fixture)", event.Delta.Transcript)
	}
}

// Regression test (found in Greptile PR review): a single modelTurn can carry
// more than one audio inlineData part — the BidiGenerateContent protobuf schema
// allows it. An earlier implementation kept only the first part and silently
// dropped the rest; parts must be decoded and concatenated instead.
func TestToBifrostRealtimeEvent_MultipleAudioPartsConcatenated(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	chunk1 := []byte{0x01, 0x02, 0x03}
	chunk2 := []byte{0x04, 0x05, 0x06}
	raw := json.RawMessage(fmt.Sprintf(
		`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":%q}},{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":%q}}]}}}`,
		base64.StdEncoding.EncodeToString(chunk1), base64.StdEncoding.EncodeToString(chunk2),
	))
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Delta == nil {
		t.Fatal("Delta = nil, want combined audio from both parts")
	}
	got, err := base64.StdEncoding.DecodeString(event.Delta.Audio)
	if err != nil {
		t.Fatalf("Delta.Audio is not valid base64: %v", err)
	}
	want := append(append([]byte{}, chunk1...), chunk2...)
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded audio = %v, want %v (both parts concatenated, not just the first)", got, want)
	}
}

// Confirmed live: Gemini bundles the audio parts AND the outputTranscription of
// that same audio into ONE serverContent message. An earlier implementation
// treated these as mutually exclusive and silently dropped the audio whenever a
// transcript was present — this test guards that regression.
func TestToBifrostRealtimeEvent_AudioDeltaWithTranscriptBundled(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := json.RawMessage(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQADAAQAEgA="}}]},"outputTranscription":{"text":"One, two,"}}}`)
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != schemas.RTEventResponseAudioDelta {
		t.Fatalf("Type = %q, want %q", event.Type, schemas.RTEventResponseAudioDelta)
	}
	if event.Delta == nil || event.Delta.Audio != "AQADAAQAEgA=" {
		t.Fatalf("Delta.Audio = %+v, want AQADAAQAEgA=", event.Delta)
	}
	if event.Delta.Transcript != "One, two," {
		t.Fatalf("Delta.Transcript = %q, want %q", event.Delta.Transcript, "One, two,")
	}
	if !provider.ShouldAccumulateRealtimeOutput(event.Type) {
		t.Fatal("ShouldAccumulateRealtimeOutput() = false, want true so the transcript still gets logged")
	}
}

func TestToBifrostRealtimeEvent_InputTranscriptionOnly(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := json.RawMessage(`{"serverContent":{"inputTranscription":{"text":"hello there"}}}`)
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != schemas.RTEventInputAudioTransCompleted {
		t.Fatalf("Type = %q, want %q", event.Type, schemas.RTEventInputAudioTransCompleted)
	}
	if event.Delta == nil || event.Delta.Transcript != "hello there" {
		t.Fatalf("Delta.Transcript = %+v, want %q", event.Delta, "hello there")
	}
}

func TestToBifrostRealtimeEvent_TurnComplete(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := json.RawMessage(`{"serverContent":{"turnComplete":true},"usageMetadata":{"promptTokenCount":145,"responseTokenCount":45,"totalTokenCount":190}}`)
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != schemas.RTEventResponseDone {
		t.Fatalf("Type = %q, want %q", event.Type, schemas.RTEventResponseDone)
	}
	if event.Type != provider.RealtimeTurnFinalEvent() {
		t.Fatalf("turnComplete event type must equal RealtimeTurnFinalEvent()")
	}
}

// Regression test for a bug found via codex review: the terminal serverContent
// message can carry turnComplete AND the final modelTurn chunk together in the
// SAME frame. An earlier implementation checked turnComplete first and never
// built a Delta in that case, silently dropping the last chunk.
func TestToBifrostRealtimeEvent_TurnCompleteWithBundledContent(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := json.RawMessage(`{"serverContent":{"turnComplete":true,"modelTurn":{"parts":[{"text":"final words"}]},"outputTranscription":{"text":"final words"}}}`)
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != schemas.RTEventResponseDone {
		t.Fatalf("Type = %q, want %q", event.Type, schemas.RTEventResponseDone)
	}
	if event.Delta == nil || event.Delta.Text != "final words" {
		t.Fatalf("Delta = %+v, want Text=%q (must not be dropped just because turnComplete is also set)", event.Delta, "final words")
	}
	if !provider.ShouldAccumulateRealtimeOutput(event.Type) {
		t.Fatal("ShouldAccumulateRealtimeOutput(RTEventResponseDone) = false, want true so this bundled final chunk still gets logged")
	}
}

// Regression test: Gemini supports parallel function calling (multiple entries
// in a single toolCall.functionCalls array) — an earlier implementation only
// ever read index 0 and silently dropped the rest.
func TestToBifrostRealtimeEvent_MultipleToolCalls(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := json.RawMessage(`{"toolCall":{"functionCalls":[{"id":"call-1","name":"get_weather","args":{"city":"SF"}},{"id":"call-2","name":"get_time","args":{"tz":"UTC"}}]}}`)
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Item == nil || event.Item.Name != "get_weather" {
		t.Fatalf("Item = %+v, want first call get_weather preserved", event.Item)
	}
	allCalls, ok := event.Item.ExtraParams["function_calls"]
	if !ok {
		t.Fatal("expected ExtraParams[\"function_calls\"] to preserve all calls, not just the first")
	}
	if !strings.Contains(string(allCalls), "get_time") {
		t.Fatalf("ExtraParams[\"function_calls\"] = %s, want it to contain the second call get_time", allCalls)
	}
}

func TestToBifrostRealtimeEvent_ToolCall(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := json.RawMessage(`{"toolCall":{"functionCalls":[{"id":"call-1","name":"get_weather","args":{"city":"SF"}}]}}`)
	event, err := provider.ToBifrostRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != geminiEventToolCall {
		t.Fatalf("Type = %q, want %q", event.Type, geminiEventToolCall)
	}
	if event.Item == nil || event.Item.Name != "get_weather" || event.Item.CallID != "call-1" {
		t.Fatalf("Item = %+v, want function_call for get_weather/call-1", event.Item)
	}
}

func TestExtractRealtimeTurnUsage(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := []byte(`{"serverContent":{"turnComplete":true},"usageMetadata":{"promptTokenCount":145,"responseTokenCount":45,"totalTokenCount":190}}`)
	usage := provider.ExtractRealtimeTurnUsage(raw)
	if usage == nil {
		t.Fatal("ExtractRealtimeTurnUsage() = nil")
	}
	if usage.PromptTokens != 145 || usage.CompletionTokens != 45 || usage.TotalTokens != 190 {
		t.Fatalf("usage = %+v, want {145 45 190}", usage)
	}
}

func TestExtractRealtimeTurnOutput(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw := []byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"Hi there, hello!"}]}}}`)
	msg := provider.ExtractRealtimeTurnOutput(raw)
	if msg == nil {
		t.Fatal("ExtractRealtimeTurnOutput() = nil")
	}
	if msg.Content == nil || msg.Content.ContentStr == nil || *msg.Content.ContentStr != "Hi there, hello!" {
		t.Fatalf("Content = %+v, want %q", msg.Content, "Hi there, hello!")
	}
}

func TestToProviderRealtimeEvent_SessionUpdate(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	event := &schemas.BifrostRealtimeEvent{
		Type:    schemas.RTEventSessionUpdate,
		Session: &schemas.RealtimeSession{Model: "gemini-3.1-flash-live-preview", Instructions: "be terse"},
	}
	raw, err := provider.ToProviderRealtimeEvent(event)
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var msg geminiSetupMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("failed to unmarshal setup message: %v", err)
	}
	if msg.Setup == nil {
		t.Fatal("Setup is nil")
	}
	// Model must carry the "models/" resource-name prefix Gemini requires —
	// missing this causes a live "model not found" close (1008) at connect time.
	if msg.Setup.Model != "models/gemini-3.1-flash-live-preview" {
		t.Fatalf("Setup.Model = %q, want %q", msg.Setup.Model, "models/gemini-3.1-flash-live-preview")
	}
	if len(msg.Setup.OutputAudioTranscription) == 0 || len(msg.Setup.InputAudioTranscription) == 0 {
		t.Fatal("expected transcription toggles to be enabled by default (this is what makes T->T/S->T achievable)")
	}

	var genCfg geminiGenerationConfig
	if err := json.Unmarshal(msg.Setup.GenerationConfig, &genCfg); err != nil {
		t.Fatalf("failed to unmarshal generationConfig: %v", err)
	}
	if len(genCfg.ResponseModalities) != 1 || genCfg.ResponseModalities[0] != "AUDIO" {
		t.Fatalf("ResponseModalities = %v, want [AUDIO] (current Gemini Live models reject TEXT)", genCfg.ResponseModalities)
	}
}

// Regression test (found in CodeRabbit PR review): Session.Tools carries the
// client's canonical OpenAI-shaped tool array as raw JSON. An earlier
// implementation forwarded it to Gemini verbatim, but Gemini expects
// tools: [{functionDeclarations: [...]}] — a completely different shape — so
// realtime tool-calling would have silently failed to register any tools.
func TestToProviderRealtimeEvent_SessionUpdateConvertsTools(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	toolsJSON := json.RawMessage(`[{"type":"function","function":{"name":"get_weather","description":"Get the weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`)
	event := &schemas.BifrostRealtimeEvent{
		Type:    schemas.RTEventSessionUpdate,
		Session: &schemas.RealtimeSession{Model: "gemini-3.1-flash-live-preview", Tools: toolsJSON},
	}
	raw, err := provider.ToProviderRealtimeEvent(event)
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var msg geminiSetupMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("failed to unmarshal setup message: %v", err)
	}
	if msg.Setup == nil || len(msg.Setup.Tools) == 0 {
		t.Fatal("Setup.Tools is empty, want converted Gemini tool declarations")
	}

	var geminiTools []Tool
	if err := json.Unmarshal(msg.Setup.Tools, &geminiTools); err != nil {
		t.Fatalf("Setup.Tools is not valid Gemini tool JSON: %v", err)
	}
	if len(geminiTools) != 1 || len(geminiTools[0].FunctionDeclarations) != 1 {
		t.Fatalf("geminiTools = %+v, want exactly 1 tool with 1 functionDeclaration", geminiTools)
	}
	if geminiTools[0].FunctionDeclarations[0].Name != "get_weather" {
		t.Fatalf("FunctionDeclarations[0].Name = %q, want %q", geminiTools[0].FunctionDeclarations[0].Name, "get_weather")
	}
}

func TestToProviderRealtimeEvent_ResponseCreate(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseCreate})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}
	var msg geminiClientContentMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("failed to unmarshal clientContent message: %v", err)
	}
	if msg.ClientContent == nil || !msg.ClientContent.TurnComplete {
		t.Fatalf("ClientContent = %+v, want TurnComplete=true", msg.ClientContent)
	}
}

func TestToProviderRealtimeEvent_InputAudioAppend(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	// Client audio arrives on the top-level Audio field, not Delta.Audio — this
	// was a real bug caught live: Delta-only reads silently rejected every
	// input_audio_buffer.append event with "audio must be set".
	event := &schemas.BifrostRealtimeEvent{Type: schemas.RTEventInputAudioAppend, Audio: []byte{0x00, 0x01, 0x02}}
	raw, err := provider.ToProviderRealtimeEvent(event)
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}
	var msg geminiRealtimeInputMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("failed to unmarshal realtimeInput message: %v", err)
	}
	if msg.RealtimeInput == nil || msg.RealtimeInput.Audio == nil || msg.RealtimeInput.Audio.Data == "" {
		t.Fatalf("RealtimeInput = %+v, want non-empty audio data", msg.RealtimeInput)
	}
}

func TestToProviderRealtimeEvent_InputAudioAppendRequiresAudio(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	_, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{Type: schemas.RTEventInputAudioAppend})
	if err == nil {
		t.Fatal("expected an error when no audio is set")
	}
}

// Regression test: a client submitting a tool result via the canonical
// conversation.item.create + item.type="function_call_output" shape (see
// schemas.IsRealtimeToolOutputEvent) must translate to Gemini's toolResponse
// message. An earlier implementation only handled Gemini's own private
// "tool_response" event type, which no standard client ever produces — so
// tool-calling round trips were silently broken.
func TestToProviderRealtimeEvent_ConversationItemCreateToolOutput(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	event := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Type:   "function_call_output",
			CallID: "call-1",
			Name:   "get_weather",
			Output: `{"tempF":72}`,
		},
	}
	raw, err := provider.ToProviderRealtimeEvent(event)
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var msg struct {
		ToolResponse struct {
			FunctionResponses []struct {
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				Response json.RawMessage `json:"response"`
			} `json:"functionResponses"`
		} `json:"toolResponse"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("failed to unmarshal toolResponse message: %v", err)
	}
	if len(msg.ToolResponse.FunctionResponses) != 1 {
		t.Fatalf("FunctionResponses = %+v, want exactly 1 entry", msg.ToolResponse.FunctionResponses)
	}
	got := msg.ToolResponse.FunctionResponses[0]
	if got.ID != "call-1" || got.Name != "get_weather" {
		t.Fatalf("FunctionResponses[0] = %+v, want id=call-1 name=get_weather", got)
	}
	if !strings.Contains(string(got.Response), "72") {
		t.Fatalf("Response = %s, want parsed JSON output containing 72", got.Response)
	}
}

// Confirms a plain (non-JSON) tool output string is still wrapped into an object,
// since Gemini requires "response" to be a JSON object, not a bare string.
func TestToProviderRealtimeEvent_ToolOutputPlainStringWrapped(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	event := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{Type: "function_call_output", CallID: "call-1", Name: "echo", Output: "plain text result"},
	}
	raw, err := provider.ToProviderRealtimeEvent(event)
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}
	if !strings.Contains(string(raw), "plain text result") {
		t.Fatalf("raw = %s, want it to contain the wrapped plain-text output", raw)
	}
}

// Regression test (found in round-2 review): a bare JSON value that isn't an
// object (number/bool/array/null) must also be wrapped, not forwarded as-is —
// Gemini requires functionResponse.response to be an object, and an earlier
// version accepted any successfully-parsed JSON value, including non-objects.
func TestToProviderRealtimeEvent_ToolOutputNonObjectJSONWrapped(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	for _, output := range []string{"42", "true", "[1,2,3]", "null", `"a quoted string"`} {
		event := &schemas.BifrostRealtimeEvent{
			Type: schemas.RTEventConversationItemCreate,
			Item: &schemas.RealtimeItem{Type: "function_call_output", CallID: "call-1", Name: "echo", Output: output},
		}
		raw, err := provider.ToProviderRealtimeEvent(event)
		if err != nil {
			t.Fatalf("ToProviderRealtimeEvent() output=%q error = %v", output, err)
		}
		var msg struct {
			ToolResponse struct {
				FunctionResponses []struct {
					Response map[string]interface{} `json:"response"`
				} `json:"functionResponses"`
			} `json:"toolResponse"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("output=%q: failed to unmarshal: %v", output, err)
		}
		if len(msg.ToolResponse.FunctionResponses) != 1 || msg.ToolResponse.FunctionResponses[0].Response == nil {
			t.Fatalf("output=%q: response = %+v, want a non-nil wrapped object (response field must decode as a JSON object)", output, msg.ToolResponse.FunctionResponses)
		}
	}
}

// Regression test: unmapped client event types must produce a nil payload (so
// the transport skips the upstream write entirely), not a literal "{}" — an
// earlier implementation returned an empty-but-real JSON object, which the
// transport wrote straight to Gemini, risking a connection close on an
// unrecognized top-level key.
func TestToProviderRealtimeEvent_UnmappedEventReturnsNil(t *testing.T) {
	t.Parallel()
	provider := &GeminiProvider{}

	raw, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseCancel})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("raw = %s, want nil/empty for an unmapped event type", raw)
	}
}
