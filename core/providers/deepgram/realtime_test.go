package deepgram_test

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/deepgram"
	"github.com/maximhq/bifrost/core/schemas"
)

// testLogger is a minimal no-op logger implementing schemas.Logger for tests.
type testLogger struct{}

func (testLogger) Debug(string, ...any)      {}
func (testLogger) Info(string, ...any)       {}
func (testLogger) Warn(string, ...any)       {}
func (testLogger) Error(string, ...any)      {}
func (testLogger) Fatal(string, ...any)      {}
func (testLogger) SetLevel(schemas.LogLevel) {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {
}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestDeepgramRealtimeWebSocketURL(t *testing.T) {
	t.Parallel()

	t.Run("default BaseURL redirects to agent.deepgram.com", func(t *testing.T) {
		provider := deepgram.NewDeepgramProvider(&schemas.ProviderConfig{}, testLogger{})
		// Verified live: Deepgram's Voice Agent API is served from agent.deepgram.com,
		// not api.deepgram.com (which 404s for /v1/agent/converse).
		want := "wss://agent.deepgram.com/v1/agent/converse"
		if got := provider.RealtimeWebSocketURL(schemas.Key{}, "unused"); got != want {
			t.Errorf("RealtimeWebSocketURL() = %q, want %q", got, want)
		}
	})

	t.Run("custom BaseURL (dedicated/self-hosted deployment) is respected as-is", func(t *testing.T) {
		provider := deepgram.NewDeepgramProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: "https://my-dedicated.deepgram.example"},
		}, testLogger{})
		want := "wss://my-dedicated.deepgram.example/v1/agent/converse"
		if got := provider.RealtimeWebSocketURL(schemas.Key{}, "unused"); got != want {
			t.Errorf("RealtimeWebSocketURL() = %q, want %q", got, want)
		}
	})

	t.Run("custom BaseURL with a trailing slash does not produce a double slash", func(t *testing.T) {
		provider := deepgram.NewDeepgramProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: "https://my-dedicated.deepgram.example/"},
		}, testLogger{})
		want := "wss://my-dedicated.deepgram.example/v1/agent/converse"
		if got := provider.RealtimeWebSocketURL(schemas.Key{}, "unused"); got != want {
			t.Errorf("RealtimeWebSocketURL() = %q, want %q", got, want)
		}
	})
}

func TestDeepgramRealtimeCapabilities(t *testing.T) {
	t.Parallel()
	provider := &deepgram.DeepgramProvider{}

	if !provider.SupportsRealtimeAPI() {
		t.Error("SupportsRealtimeAPI() = false, want true")
	}
	if provider.SupportsRealtimeWebRTC() {
		t.Error("SupportsRealtimeWebRTC() = true, want false")
	}
	if !provider.SupportsRealtimeBinaryAudioInput() {
		t.Error("SupportsRealtimeBinaryAudioInput() = false, want true")
	}
	if provider.RealtimeTurnFinalEvent() != schemas.RTEventResponseAudioDone {
		t.Errorf("RealtimeTurnFinalEvent() = %v, want %v", provider.RealtimeTurnFinalEvent(), schemas.RTEventResponseAudioDone)
	}
}

func TestDeepgramToBifrostRealtimeEvent(t *testing.T) {
	t.Parallel()
	provider := &deepgram.DeepgramProvider{}

	cases := []struct {
		name           string
		raw            string
		wantType       schemas.RealtimeEventType
		wantStartsTurn bool
	}{
		{
			name:     "Welcome",
			raw:      `{"type":"Welcome","request_id":"req-1"}`,
			wantType: schemas.RTEventSessionCreated,
		},
		{
			name:     "SettingsApplied",
			raw:      `{"type":"SettingsApplied"}`,
			wantType: schemas.RTEventSessionUpdated,
		},
		{
			name:           "UserStartedSpeaking starts a turn",
			raw:            `{"type":"UserStartedSpeaking"}`,
			wantType:       schemas.RTEventInputAudioSpeechStarted,
			wantStartsTurn: true,
		},
		{
			name:     "ConversationText user role",
			raw:      `{"type":"ConversationText","role":"user","content":"hello"}`,
			wantType: schemas.RTEventInputAudioTransCompleted,
		},
		{
			name:     "ConversationText assistant role",
			raw:      `{"type":"ConversationText","role":"assistant","content":"hi there"}`,
			wantType: schemas.RTEventResponseAudioTransDelta,
		},
		{
			name:     "AgentAudioDone is the turn-final event",
			raw:      `{"type":"AgentAudioDone"}`,
			wantType: schemas.RTEventResponseAudioDone,
		},
		{
			name:     "Error",
			raw:      `{"type":"Error","code":"BAD","description":"bad request"}`,
			wantType: schemas.RTEventError,
		},
		{
			name:     "Unknown type passes through as literal",
			raw:      `{"type":"SomeFutureEvent"}`,
			wantType: schemas.RealtimeEventType("SomeFutureEvent"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
			}
			if event.Type != tc.wantType {
				t.Errorf("Type = %v, want %v", event.Type, tc.wantType)
			}
			if got := provider.ShouldStartRealtimeTurn(event); got != tc.wantStartsTurn {
				t.Errorf("ShouldStartRealtimeTurn() = %v, want %v", got, tc.wantStartsTurn)
			}
		})
	}
}

func TestDeepgramToBifrostRealtimeEvent_FunctionCallRequest(t *testing.T) {
	t.Parallel()
	provider := &deepgram.DeepgramProvider{}

	raw := `{"type":"FunctionCallRequest","functions":[{"id":"call_1","name":"get_weather","arguments":{"city":"NYC"}}]}`
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Type != schemas.RTEventResponseOutputItemDone {
		t.Fatalf("Type = %v, want %v", event.Type, schemas.RTEventResponseOutputItemDone)
	}
	if event.Item == nil || event.Item.Name != "get_weather" || event.Item.CallID != "call_1" {
		t.Fatalf("Item = %+v, want function_call for get_weather/call_1", event.Item)
	}
	if len(event.ExtraParams) != 0 {
		t.Errorf("ExtraParams = %+v, want none for a single function call", event.ExtraParams)
	}
}

func TestDeepgramToBifrostRealtimeEvent_FunctionCallRequestMultiple(t *testing.T) {
	t.Parallel()
	provider := &deepgram.DeepgramProvider{}

	raw := `{"type":"FunctionCallRequest","functions":[
		{"id":"call_1","name":"get_weather","arguments":{"city":"NYC"}},
		{"id":"call_2","name":"get_time","arguments":{"tz":"EST"}}
	]}`
	event, err := provider.ToBifrostRealtimeEvent(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ToBifrostRealtimeEvent() error = %v", err)
	}
	if event.Item == nil || event.Item.Name != "get_weather" || event.Item.CallID != "call_1" {
		t.Fatalf("Item = %+v, want the first function call (get_weather/call_1)", event.Item)
	}
	extra, ok := event.ExtraParams[schemas.RealtimeExtraParamKeyAdditionalItems]
	if !ok {
		t.Fatalf("ExtraParams missing %q for a multi-function request", schemas.RealtimeExtraParamKeyAdditionalItems)
	}
	var additional []schemas.RealtimeItem
	if err := json.Unmarshal(extra, &additional); err != nil {
		t.Fatalf("failed to parse additional items: %v", err)
	}
	if len(additional) != 1 || additional[0].Name != "get_time" || additional[0].CallID != "call_2" {
		t.Fatalf("additional items = %+v, want [get_time/call_2]", additional)
	}
}

func TestDeepgramToProviderRealtimeEvent(t *testing.T) {
	t.Parallel()
	provider := &deepgram.DeepgramProvider{}

	t.Run("session.update becomes a Settings message", func(t *testing.T) {
		event := &schemas.BifrostRealtimeEvent{
			Type: schemas.RTEventSessionUpdate,
			Session: &schemas.RealtimeSession{
				Instructions: "Be helpful",
				Model:        "gpt-4o-mini",
				Voice:        "aura-asteria-en",
			},
		}
		out, err := provider.ToProviderRealtimeEvent(event)
		if err != nil {
			t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}
		if parsed["type"] != "Settings" {
			t.Errorf("type = %v, want Settings", parsed["type"])
		}
		agent, ok := parsed["agent"].(map[string]interface{})
		if !ok {
			t.Fatalf("agent block missing or wrong shape: %+v", parsed)
		}
		think, ok := agent["think"].(map[string]interface{})
		if !ok || think["prompt"] != "Be helpful" {
			t.Errorf("agent.think.prompt = %+v, want 'Be helpful'", agent["think"])
		}
		thinkProvider, ok := think["provider"].(map[string]interface{})
		if !ok || thinkProvider["model"] != "gpt-4o-mini" {
			t.Errorf("agent.think.provider.model = %+v, want gpt-4o-mini", think["provider"])
		}
		speak, ok := agent["speak"].(map[string]interface{})
		if !ok {
			t.Fatalf("agent.speak block missing or wrong shape: %+v", agent)
		}
		speakProvider, ok := speak["provider"].(map[string]interface{})
		if !ok || speakProvider["type"] != "deepgram" || speakProvider["model"] != "aura-asteria-en" {
			t.Errorf("agent.speak.provider = %+v, want type=deepgram model=aura-asteria-en", speak["provider"])
		}
	})

	t.Run("function_call_output becomes FunctionCallResponse", func(t *testing.T) {
		event := &schemas.BifrostRealtimeEvent{
			Type: schemas.RTEventConversationItemCreate,
			Item: &schemas.RealtimeItem{
				Type:   "function_call_output",
				CallID: "call_1",
				Name:   "get_weather",
				Output: "sunny",
			},
		}
		out, err := provider.ToProviderRealtimeEvent(event)
		if err != nil {
			t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}
		if parsed["type"] != "FunctionCallResponse" || parsed["id"] != "call_1" || parsed["content"] != "sunny" {
			t.Errorf("parsed = %+v, want FunctionCallResponse/call_1/sunny", parsed)
		}
	})

	t.Run("input_audio_buffer.append is rejected (binary route expected instead)", func(t *testing.T) {
		event := &schemas.BifrostRealtimeEvent{Type: schemas.RTEventInputAudioAppend}
		if _, err := provider.ToProviderRealtimeEvent(event); err == nil {
			t.Error("expected an error for JSON-wrapped audio append, got nil")
		}
	})

	t.Run("unknown literal type passes through as-is", func(t *testing.T) {
		event := &schemas.BifrostRealtimeEvent{Type: schemas.RealtimeEventType("UpdateListen")}
		out, err := provider.ToProviderRealtimeEvent(event)
		if err != nil {
			t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}
		if parsed["type"] != "UpdateListen" {
			t.Errorf("type = %v, want UpdateListen", parsed["type"])
		}
	})
}
