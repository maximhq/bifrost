package openai

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestNormalizeRealtimeClientSecretRequest(t *testing.T) {
	t.Parallel()

	body, model, bifrostErr := normalizeRealtimeClientSecretRequest(
		json.RawMessage(`{"model":"openai/gpt-4o-realtime-preview","voice":"alloy"}`),
		schemas.OpenAI,
		schemas.RealtimeSessionEndpointClientSecrets,
	)
	if bifrostErr != nil {
		t.Fatalf("normalizeRealtimeClientSecretRequest() error = %v", bifrostErr)
	}
	if model != "gpt-4o-realtime-preview" {
		t.Fatalf("model = %q, want %q", model, "gpt-4o-realtime-preview")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to unmarshal normalized body: %v", err)
	}
	if _, ok := payload["model"]; ok {
		t.Fatal("top-level model should be removed after normalization")
	}

	var session map[string]any
	if err := json.Unmarshal(payload["session"], &session); err != nil {
		t.Fatalf("failed to unmarshal session: %v", err)
	}
	if session["model"] != "gpt-4o-realtime-preview" {
		t.Fatalf("session.model = %v, want %q", session["model"], "gpt-4o-realtime-preview")
	}
	if session["type"] != "realtime" {
		t.Fatalf("session.type = %v, want %q", session["type"], "realtime")
	}
}

func TestNormalizeRealtimeClientSecretRequestUsesDefaultProvider(t *testing.T) {
	t.Parallel()

	body, model, bifrostErr := normalizeRealtimeClientSecretRequest(
		json.RawMessage(`{"session":{"model":"gpt-4o-realtime-preview"}}`),
		schemas.OpenAI,
		schemas.RealtimeSessionEndpointClientSecrets,
	)
	if bifrostErr != nil {
		t.Fatalf("normalizeRealtimeClientSecretRequest() error = %v", bifrostErr)
	}
	if model != "gpt-4o-realtime-preview" {
		t.Fatalf("model = %q, want %q", model, "gpt-4o-realtime-preview")
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to unmarshal normalized body: %v", err)
	}

	var session map[string]any
	if err := json.Unmarshal(payload["session"], &session); err != nil {
		t.Fatalf("failed to unmarshal session: %v", err)
	}
	if session["model"] != "gpt-4o-realtime-preview" {
		t.Fatalf("session.model = %v, want %q", session["model"], "gpt-4o-realtime-preview")
	}
	if session["type"] != "realtime" {
		t.Fatalf("session.type = %v, want %q", session["type"], "realtime")
	}
}

func TestNormalizeRealtimeSessionsRequest(t *testing.T) {
	t.Parallel()

	body, model, bifrostErr := normalizeRealtimeClientSecretRequest(
		json.RawMessage(`{"session":{"model":"openai/gpt-4o-realtime-preview","voice":"alloy"}}`),
		schemas.OpenAI,
		schemas.RealtimeSessionEndpointSessions,
	)
	if bifrostErr != nil {
		t.Fatalf("normalizeRealtimeClientSecretRequest() error = %v", bifrostErr)
	}
	if model != "gpt-4o-realtime-preview" {
		t.Fatalf("model = %q, want %q", model, "gpt-4o-realtime-preview")
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to unmarshal normalized body: %v", err)
	}
	if _, ok := payload["session"]; ok {
		t.Fatal("legacy sessions endpoint should not forward nested session object")
	}
	if payload["model"] != "gpt-4o-realtime-preview" {
		t.Fatalf("model = %v, want %q", payload["model"], "gpt-4o-realtime-preview")
	}
	if payload["voice"] != "alloy" {
		t.Fatalf("voice = %v, want %q", payload["voice"], "alloy")
	}
}

func TestToProviderRealtimeEventSerializesTopLevelClientFields(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	contentIndex, err := json.Marshal(0)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	audioEndMS, err := json.Marshal(640)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RealtimeEventType("conversation.item.truncate"),
		ExtraParams: map[string]json.RawMessage{
			"item_id":       json.RawMessage(`"item_123"`),
			"content_index": contentIndex,
			"audio_end_ms":  audioEndMS,
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["type"] != "conversation.item.truncate" {
		t.Fatalf("type = %v, want %q", payload["type"], "conversation.item.truncate")
	}
	if payload["item_id"] != "item_123" {
		t.Fatalf("item_id = %v, want %q", payload["item_id"], "item_123")
	}
	if payload["content_index"] != float64(0) {
		t.Fatalf("content_index = %v, want 0", payload["content_index"])
	}
	if payload["audio_end_ms"] != float64(640) {
		t.Fatalf("audio_end_ms = %v, want 640", payload["audio_end_ms"])
	}
}

func TestToBifrostRealtimeEventParsesTopLevelClientFields(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{"type":"conversation.item.truncate","item_id":"item_123","content_index":0,"audio_end_ms":640}`))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	var itemID string
	if err := json.Unmarshal(event.ExtraParams["item_id"], &itemID); err != nil {
		t.Fatalf("json.Unmarshal(item_id) error = %v", err)
	}
	if itemID != "item_123" {
		t.Fatalf("item_id = %q, want %q", itemID, "item_123")
	}
	var contentIndex int
	if err := json.Unmarshal(event.ExtraParams["content_index"], &contentIndex); err != nil {
		t.Fatalf("json.Unmarshal(content_index) error = %v", err)
	}
	if contentIndex != 0 {
		t.Fatalf("content_index = %d, want 0", contentIndex)
	}
	var audioEndMS int
	if err := json.Unmarshal(event.ExtraParams["audio_end_ms"], &audioEndMS); err != nil {
		t.Fatalf("json.Unmarshal(audio_end_ms) error = %v", err)
	}
	if audioEndMS != 640 {
		t.Fatalf("audio_end_ms = %d, want 640", audioEndMS)
	}
}

func TestToBifrostRealtimeEventParsesCompletedInputAudioTranscript(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{"type":"conversation.item.input_audio_transcription.completed","event_id":"evt_123","item_id":"item_123","content_index":0,"transcript":"Who are you?"}`))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}

	var transcript string
	if err := json.Unmarshal(event.ExtraParams["transcript"], &transcript); err != nil {
		t.Fatalf("json.Unmarshal(transcript) error = %v", err)
	}
	if transcript != "Who are you?" {
		t.Fatalf("transcript = %q, want %q", transcript, "Who are you?")
	}
}

func TestToBifrostRealtimeEventParsesModernOutputTextDelta(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{
		"type":"response.output_text.delta",
		"event_id":"evt_123",
		"item_id":"item_123",
		"output_index":0,
		"content_index":0,
		"response_id":"resp_123",
		"delta":"hello"
	}`))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Delta == nil || event.Delta.Text != "hello" {
		t.Fatalf("Delta = %+v, want text=hello", event.Delta)
	}
}

func TestShouldStartRealtimeTurn(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	tests := []struct {
		name  string
		event *schemas.BifrostRealtimeEvent
		want  bool
	}{
		{
			name:  "response create starts turn",
			event: &schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseCreate},
			want:  true,
		},
		{
			name:  "audio buffer committed starts turn",
			event: &schemas.BifrostRealtimeEvent{Type: schemas.RTEventInputAudioBufferCommitted},
			want:  true,
		},
		{
			name:  "response done does not start turn",
			event: &schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseDone},
			want:  false,
		},
		{
			name:  "nil event does not start turn",
			event: nil,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := provider.ShouldStartRealtimeTurn(tt.event); got != tt.want {
				t.Fatalf("ShouldStartRealtimeTurn() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToProviderRealtimeEventSerializesModernOutputTextDelta(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	outputIndex := 0
	contentIndex := 0
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RealtimeEventType("response.output_text.delta"),
		Delta: &schemas.RealtimeDelta{
			Text:       "hello",
			ItemID:     "item_123",
			OutputIdx:  &outputIndex,
			ContentIdx: &contentIndex,
			ResponseID: "resp_123",
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["type"] != "response.output_text.delta" {
		t.Fatalf("type = %v, want response.output_text.delta", payload["type"])
	}
	if payload["delta"] != "hello" {
		t.Fatalf("delta = %v, want hello", payload["delta"])
	}
}

func TestToProviderRealtimeEventSerializesSessionID(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventSessionCreated,
		Session: &schemas.RealtimeSession{
			ID:    "sess_123",
			Model: "gpt-realtime",
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	session, ok := payload["session"].(map[string]any)
	if !ok {
		t.Fatalf("session = %T, want object", payload["session"])
	}
	if session["id"] != "sess_123" {
		t.Fatalf("session.id = %v, want sess_123", session["id"])
	}
}

func TestToProviderRealtimeEventSerializesMessageItemStatus(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	content := json.RawMessage(`[{"type":"input_audio","transcript":"hello"}]`)
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RealtimeEventType("conversation.item.retrieved"),
		Item: &schemas.RealtimeItem{
			ID:      "item_123",
			Type:    "message",
			Role:    "user",
			Status:  "completed",
			Content: content,
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	item, ok := payload["item"].(map[string]any)
	if !ok {
		t.Fatalf("item = %T, want object", payload["item"])
	}
	if item["status"] != "completed" {
		t.Fatalf("item.status = %v, want completed", item["status"])
	}
}

func TestToBifrostRealtimeEventPreservesTopLevelResponsePayload(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{
		"type":"response.done",
		"event_id":"evt_123",
		"response":{
			"id":"resp_123",
			"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]
		}
	}`))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(event.ExtraParams["response"], &response); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	if response["id"] != "resp_123" {
		t.Fatalf("response.id = %v, want resp_123", response["id"])
	}
}

func TestToProviderRealtimeEventSerializesTopLevelResponsePayload(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventResponseDone,
		ExtraParams: map[string]json.RawMessage{
			"response": json.RawMessage(`{"id":"resp_123","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`),
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	response, ok := payload["response"].(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want object", payload["response"])
	}
	if response["id"] != "resp_123" {
		t.Fatalf("response.id = %v, want resp_123", response["id"])
	}
}

func TestToBifrostRealtimeEventPreservesTopLevelPartPayload(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(`{
		"type":"response.content_part.added",
		"event_id":"evt_123",
		"item_id":"item_123",
		"output_index":0,
		"content_index":0,
		"part":{
			"type":"text",
			"text":"hello"
		}
	}`))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}

	var part map[string]any
	if err := json.Unmarshal(event.ExtraParams["part"], &part); err != nil {
		t.Fatalf("json.Unmarshal(part) error = %v", err)
	}
	if part["type"] != "text" {
		t.Fatalf("part.type = %v, want text", part["type"])
	}
}

func TestToProviderRealtimeEventSerializesTopLevelPartPayload(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventResponseContentPartAdded,
		ExtraParams: map[string]json.RawMessage{
			"part": json.RawMessage(`{"type":"text","text":"hello"}`),
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	part, ok := payload["part"].(map[string]any)
	if !ok {
		t.Fatalf("part = %T, want object", payload["part"])
	}
	if part["type"] != "text" {
		t.Fatalf("part.type = %v, want text", part["type"])
	}
}

func TestParseRealtimeEventPreservesNestedSessionExtraParams(t *testing.T) {
	t.Parallel()

	event, err := schemas.ParseRealtimeEvent([]byte(`{
		"type":"session.update",
		"session":{
			"type":"realtime",
			"model":"gpt-4o-realtime-preview",
			"output_modalities":["text"]
		}
	}`))
	if err != nil {
		t.Fatalf("ParseRealtimeEvent() error = %v", err)
	}
	if event.Session == nil {
		t.Fatal("expected session to be parsed")
	}
	var outputModalities []string
	if err := json.Unmarshal(event.Session.ExtraParams["output_modalities"], &outputModalities); err != nil {
		t.Fatalf("json.Unmarshal(output_modalities) error = %v", err)
	}
	if len(outputModalities) != 1 || outputModalities[0] != "text" {
		t.Fatalf("output_modalities = %v, want [text]", outputModalities)
	}
}

func TestToProviderRealtimeEventSerializesNestedSessionExtraParams(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventSessionUpdate,
		Session: &schemas.RealtimeSession{
			Model: "gpt-4o-realtime-preview",
			ExtraParams: map[string]json.RawMessage{
				"type":              json.RawMessage(`"realtime"`),
				"output_modalities": json.RawMessage(`["text"]`),
			},
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload struct {
		Type    string         `json:"type"`
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload.Type != "session.update" {
		t.Fatalf("type = %q, want %q", payload.Type, "session.update")
	}
	if payload.Session["type"] != "realtime" {
		t.Fatalf("session.type = %v, want realtime", payload.Session["type"])
	}
	outputModalities, ok := payload.Session["output_modalities"].([]any)
	if !ok || len(outputModalities) != 1 || outputModalities[0] != "text" {
		t.Fatalf("session.output_modalities = %v, want [text]", payload.Session["output_modalities"])
	}
}

func TestToProviderRealtimeEventOmitsReadOnlySessionFieldsOnSessionUpdate(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{}
	out, err := provider.ToProviderRealtimeEvent(&schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventSessionUpdate,
		Session: &schemas.RealtimeSession{
			ID:    "sess_123",
			Model: "gpt-realtime",
			ExtraParams: map[string]json.RawMessage{
				"type":          json.RawMessage(`"realtime"`),
				"object":        json.RawMessage(`"realtime.session"`),
				"expires_at":    json.RawMessage(`1774614381`),
				"client_secret": json.RawMessage(`{"value":"secret"}`),
				"modalities":    json.RawMessage(`["text","audio"]`),
			},
		},
	})
	if err != nil {
		t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
	}

	var payload struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{"id", "object", "expires_at", "client_secret"} {
		if _, ok := payload.Session[key]; ok {
			t.Fatalf("session.%s unexpectedly present in session.update payload", key)
		}
	}
	if payload.Session["type"] != "realtime" {
		t.Fatalf("session.type = %v, want realtime", payload.Session["type"])
	}
	if payload.Session["model"] != "gpt-realtime" {
		t.Fatalf("session.model = %v, want gpt-realtime", payload.Session["model"])
	}
}
