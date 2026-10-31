package schemas

import (
	"strings"
	"testing"
)

func TestLiveEventTypeOf(t *testing.T) {
	t.Parallel()

	// type after a large audio payload still resolves without decoding the frame
	frame := `{"audio":"` + strings.Repeat("A", 64*1024) + `","type":"session.input_audio.append"}`
	got := LiveEventTypeOf([]byte(frame))
	if got != LiveEventInputAudioAppend {
		t.Fatalf("LiveEventTypeOf() = %q", got)
	}
	if got := LiveEventTypeOf([]byte(`not json`)); got != "" {
		t.Fatalf("LiveEventTypeOf(invalid) = %q, want empty", got)
	}
}

func TestParseLiveEventSessionStart(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"type": "session.start",
		"event_id": "event_start",
		"session": {
			"model": "gpt-live-1",
			"instructions": "Be concise.",
			"audio": {"format": {"type": "audio/pcm", "rate": 24000}, "output": {"voice": "marin"}},
			"delegation": {
				"type": "responses",
				"responses": {
					"model": "gpt-5.6-terra",
					"service_tier": "priority",
					"reasoning": {"effort": "low"},
					"parallel_tool_calls": false,
					"tool_choice": "auto",
					"tools": [{"type": "web_search"}, {"type": "function", "name": "book_slot", "parameters": {"type": "object"}}]
				}
			},
			"input": [{"type": "message", "role": "assistant", "content": {"type": "output_text", "text": "Order number?"}}],
			"client": {"data_channel": {"allowed_client_events": "all"}},
			"store": true
		}
	}`)
	event, err := ParseLiveEvent(raw)
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.Type != LiveEventSessionStart || event.EventID != "event_start" || string(event.RawData) != string(raw) {
		t.Fatalf("unexpected envelope: %+v", event)
	}
	session := event.Session
	if session == nil || session.Model != "gpt-live-1" || session.Store == nil || !*session.Store {
		t.Fatalf("unexpected session: %+v", session)
	}
	if session.Audio.Format.Type != "audio/pcm" || session.Audio.Format.Rate != 24000 {
		t.Fatalf("unexpected audio format: %+v", session.Audio.Format)
	}
	if voice := session.Audio.Output.Voice; voice == nil || voice.Name == nil || *voice.Name != "marin" {
		t.Fatalf("unexpected voice: %+v", voice)
	}
	delegation := session.Delegation
	if delegation == nil || delegation.Type != LiveDelegationResponses || delegation.Responses == nil {
		t.Fatalf("unexpected delegation: %+v", delegation)
	}
	backend := delegation.Responses
	if backend.Model != "gpt-5.6-terra" || backend.ServiceTier == nil || *backend.ServiceTier != BifrostServiceTierPriority {
		t.Fatalf("unexpected backend: %+v", backend)
	}
	if len(backend.Tools) != 2 || backend.ToolChoice == nil || backend.ParallelToolCalls == nil || *backend.ParallelToolCalls {
		t.Fatalf("unexpected backend tools: %+v", backend)
	}
}

func TestParseLiveEventClientDelegationAndCustomVoice(t *testing.T) {
	t.Parallel()

	event, err := ParseLiveEvent([]byte(`{"type":"session.start","session":{"model":"gpt-live-1","audio":{"output":{"voice":{"id":"voice_abc"}}},"delegation":{"type":"client"}}}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.Session.Delegation.Type != LiveDelegationClient || event.Session.Delegation.Responses != nil {
		t.Fatalf("unexpected delegation: %+v", event.Session.Delegation)
	}
	if voice := event.Session.Audio.Output.Voice; voice.Custom == nil || voice.Custom.ID != "voice_abc" || voice.Name != nil {
		t.Fatalf("unexpected voice: %+v", voice)
	}

	// an unrecognized voice shape must not fail the frame
	event, err = ParseLiveEvent([]byte(`{"type":"session.start","session":{"model":"gpt-live-1","audio":{"output":{"voice":["marin"]}}}}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent(unknown voice shape) error = %v", err)
	}
	if voice := event.Session.Audio.Output.Voice; voice.Name != nil || voice.Custom != nil {
		t.Fatalf("unexpected voice: %+v", voice)
	}
}

func TestParseLiveEventUsageAndClosed(t *testing.T) {
	t.Parallel()

	event, err := ParseLiveEvent([]byte(`{"type":"session.usage.updated","event_id":"event_usage_1","usage":{"seconds":12.5},"context_window":{"usage_ratio":0.42}}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.Usage == nil || event.Usage.Seconds != 12.5 || event.ContextWindow == nil || event.ContextWindow.UsageRatio != 0.42 {
		t.Fatalf("unexpected usage event: %+v", event)
	}

	event, err = ParseLiveEvent([]byte(`{"type":"session.closed","event_id":"e1","client_event_id":"close_1","reason":"expired","usage":{"seconds":90},"session":{"id":"live_123","expires_at":1758000000,"status":"active","model":"gpt-live-1"}}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.Reason != LiveCloseExpired || event.Usage.Seconds != 90 || event.ClientEventID != "close_1" {
		t.Fatalf("unexpected closed event: %+v", event)
	}
	if event.Session.ID != "live_123" || event.Session.ExpiresAt != 1758000000 {
		t.Fatalf("unexpected session snapshot: %+v", event.Session)
	}
}

func TestParseLiveEventDelegationAndResponseEvent(t *testing.T) {
	t.Parallel()

	event, err := ParseLiveEvent([]byte(`{"type":"session.delegation.created","event_id":"event_delegation","offset_ms":1000,"delegation":{"id":"item_9tA2","type":"delegation","target":"responses","response_id":"resp_1"}}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.OffsetMs == nil || *event.OffsetMs != 1000 || event.Delegation.ID != "item_9tA2" || event.Delegation.Target != LiveDelegationResponses {
		t.Fatalf("unexpected delegation event: %+v", event)
	}
	if event.Delegation.ResponseID == nil || *event.Delegation.ResponseID != "resp_1" {
		t.Fatalf("unexpected response id: %+v", event.Delegation)
	}

	event, err = ParseLiveEvent([]byte(`{
		"type": "response.event",
		"event_id": "event_response_9",
		"delegation_id": "item_9tA2",
		"event": {
			"type": "response.completed",
			"sequence_number": 12,
			"response": {
				"id": "resp_1", "object": "response", "model": "gpt-5.6-terra", "status": "completed", "output": [],
				"usage": {"input_tokens": 120, "output_tokens": 30, "total_tokens": 150, "input_tokens_details": {"cached_tokens": 64}}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.DelegationID == nil || *event.DelegationID != "item_9tA2" || event.Event == nil || event.Event.Response == nil {
		t.Fatalf("unexpected response.event envelope: %+v", event)
	}
	nested := event.Event.Response
	if nested.Type != ResponsesStreamResponseTypeCompleted || nested.Response == nil || nested.Response.Usage == nil {
		t.Fatalf("unexpected nested event: %+v", nested)
	}
	if usage := nested.Response.Usage; usage.InputTokens != 120 || usage.OutputTokens != 30 || usage.TotalTokens != 150 {
		t.Fatalf("unexpected nested usage: %+v", usage)
	}
}

func TestParseLiveEventPayloadUnion(t *testing.T) {
	t.Parallel()

	event, err := ParseLiveEvent([]byte(`{"type":"transport.dtmf.received","event_id":"e1","event":"#"}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.Event == nil || event.Event.DTMF == nil || *event.Event.DTMF != "#" || event.Event.Response != nil {
		t.Fatalf("unexpected dtmf payload: %+v", event.Event)
	}

	out, err := Marshal(event.Event)
	if err != nil || string(out) != `"#"` {
		t.Fatalf("Marshal(dtmf payload) = %s, %v", out, err)
	}
	if _, err := Marshal(LiveEventPayload{DTMF: event.Event.DTMF, Response: &BifrostResponsesStreamResponse{}}); err == nil {
		t.Fatal("Marshal() with both variants set should error")
	}
}

func TestParseLiveEventErrorAndAppend(t *testing.T) {
	t.Parallel()

	event, err := ParseLiveEvent([]byte(`{"type":"error","event_id":"event_error","error":{"type":"invalid_request_error","code":null,"message":"The delegation type cannot change after session startup.","param":"session.delegation.type","client_event_id":"event_update"}}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.Error == nil || event.Error.Code != nil || event.Error.Param == nil || *event.Error.ClientEventID != "event_update" {
		t.Fatalf("unexpected error event: %+v", event.Error)
	}

	event, err = ParseLiveEvent([]byte(`{"type":"session.thinking.append","event_id":"context_1","delegation_id":null,"content":"Lookup still running."}`))
	if err != nil {
		t.Fatalf("ParseLiveEvent() error = %v", err)
	}
	if event.DelegationID != nil || event.Content == nil || *event.Content != "Lookup still running." {
		t.Fatalf("unexpected append event: %+v", event)
	}

	if _, err := ParseLiveEvent([]byte(`{"type":`)); err == nil {
		t.Fatal("ParseLiveEvent(truncated) should error")
	}
}

func TestLiveVoiceMarshal(t *testing.T) {
	t.Parallel()

	name := "marin"
	if out, err := Marshal(LiveVoice{Name: &name}); err != nil || string(out) != `"marin"` {
		t.Fatalf("Marshal(name) = %s, %v", out, err)
	}
	if out, err := Marshal(LiveVoice{Custom: &LiveCustomVoice{ID: "voice_abc"}}); err != nil || string(out) != `{"id":"voice_abc"}` {
		t.Fatalf("Marshal(custom) = %s, %v", out, err)
	}
	if _, err := Marshal(LiveVoice{Name: &name, Custom: &LiveCustomVoice{ID: "voice_abc"}}); err == nil {
		t.Fatal("Marshal() with both variants set should error")
	}
}

func TestAllowedRequestsLive(t *testing.T) {
	t.Parallel()

	if !(&AllowedRequests{Live: true}).IsOperationAllowed(LiveRequest) {
		t.Fatal("live should be allowed when set")
	}
	if (&AllowedRequests{Realtime: true}).IsOperationAllowed(LiveRequest) {
		t.Fatal("realtime must not imply live")
	}
	if !(&AllowedRequests{Live: true}).IsOperationAllowed(LiveContentRequest) {
		t.Fatal("a recording download is gated by the live flag")
	}
	if (&AllowedRequests{FileContent: true}).IsOperationAllowed(LiveContentRequest) {
		t.Fatal("file content must not imply live content")
	}
}
