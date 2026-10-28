package schemas

import "testing"

// TestIsRealtimeConversationItemEventType verifies realtime schema behavior.
func TestIsRealtimeConversationItemEventType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType RealtimeEventType
		want      bool
	}{
		{name: "create", eventType: RTEventConversationItemCreate, want: true},
		{name: "added", eventType: RTEventConversationItemAdded, want: true},
		{name: "created", eventType: RTEventConversationItemCreated, want: true},
		{name: "retrieved", eventType: RTEventConversationItemRetrieved, want: true},
		{name: "done", eventType: RTEventConversationItemDone, want: true},
		{name: "response done", eventType: RTEventResponseDone, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsRealtimeConversationItemEventType(tt.eventType); got != tt.want {
				t.Fatalf("IsRealtimeConversationItemEventType(%q) = %v, want %v", tt.eventType, got, tt.want)
			}
		})
	}
}

// TestRealtimeCanonicalEventClassifiers verifies realtime schema behavior.
func TestRealtimeCanonicalEventClassifiers(t *testing.T) {
	t.Parallel()

	userEvent := &BifrostRealtimeEvent{
		Type: RTEventConversationItemAdded,
		Item: &RealtimeItem{
			Role: "user",
			Type: "message",
		},
	}
	if !IsRealtimeUserInputEvent(userEvent) {
		t.Fatal("expected conversation.item.added user event to be classified as realtime user input")
	}
	if IsRealtimeToolOutputEvent(userEvent) {
		t.Fatal("did not expect conversation.item.added user event to be classified as realtime tool output")
	}

	toolEvent := &BifrostRealtimeEvent{
		Type: RTEventConversationItemRetrieved,
		Item: &RealtimeItem{
			Type: "function_call_output",
		},
	}
	if !IsRealtimeToolOutputEvent(toolEvent) {
		t.Fatal("expected function_call_output item to be classified as realtime tool output")
	}
	if IsRealtimeUserInputEvent(toolEvent) {
		t.Fatal("did not expect function_call_output item to be classified as realtime user input")
	}

	transcriptEvent := &BifrostRealtimeEvent{Type: RTEventInputAudioTransCompleted}
	if !IsRealtimeInputTranscriptEvent(transcriptEvent) {
		t.Fatal("expected input audio transcription completion to be classified as transcript event")
	}
	if IsRealtimeInputTranscriptEvent(&BifrostRealtimeEvent{Type: RTEventInputAudioTransDelta}) {
		t.Fatal("did not expect input audio transcription delta to be classified as transcript event")
	}
}

// TestParseRealtimeEventOwnsNativeBytes preserves field presence and precision independently of a reused receive buffer.
func TestParseRealtimeEventOwnsNativeBytes(t *testing.T) {
	const input = `{"type":"session.update","session":{"instructions":"","tools":[],"future":9007199254740993},"raw_data":{"type":"response.done"}}`
	raw := []byte(input)
	event, err := ParseRealtimeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		raw[i] = ' '
	}
	if string(event.RawData) != input {
		t.Fatal("native event not independently preserved")
	}
	if event.Type != RTEventSessionUpdate {
		t.Fatal("client raw_data changed canonical event type")
	}
}

// TestParseRealtimeEventAcceptsObjectVoice pins that a session.update setting an Azure Voice Live
// voice object parses, so the gateway forwards it instead of answering 400.
func TestParseRealtimeEventAcceptsObjectVoice(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"session.update","session":{"voice":{"name":"en-US-Ava:DragonHDLatestNeural","type":"azure-standard","temperature":0.8,"pitch":null},"instructions":"Be brief."}}`)
	event, err := ParseRealtimeEvent(raw)
	if err != nil {
		t.Fatalf("ParseRealtimeEvent: %v", err)
	}
	if event.Session == nil || event.Session.Instructions != "Be brief." {
		t.Fatalf("session = %+v", event.Session)
	}
	voice := event.Session.Voice
	if voice == nil || voice.VoiceObject == nil || voice.GetName() != "en-US-Ava:DragonHDLatestNeural" || voice.VoiceObject.Type != "azure-standard" {
		t.Fatalf("voice = %+v", voice)
	}
	encoded, err := Marshal(voice)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"name":"en-US-Ava:DragonHDLatestNeural","pitch":null,"temperature":0.8,"type":"azure-standard"}`; string(encoded) != want {
		t.Fatalf("voice = %s, want %s", encoded, want)
	}
}

// TestRealtimeVoiceRoundTrip pins both wire forms: a name stays a string, and an object keeps every
// field, including a null or non-string name that is not lifted into Name.
func TestRealtimeVoiceRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		wantName string
	}{
		{"name", `"alloy"`, "alloy"},
		{"openai custom voice", `{"id":"voice_123"}`, "voice_123"},
		{"azure voice", `{"endpoint_id":"ep-1","name":"my-voice","type":"azure-custom"}`, "my-voice"},
		{"null name stays as sent", `{"name":null,"type":"openai"}`, ""},
	}
	for _, tc := range cases {
		var voice RealtimeVoice
		if err := Unmarshal([]byte(tc.in), &voice); err != nil {
			t.Fatalf("%s: Unmarshal: %v", tc.name, err)
		}
		if got := voice.GetName(); got != tc.wantName {
			t.Errorf("%s: GetName = %q, want %q", tc.name, got, tc.wantName)
		}
		out, err := Marshal(voice)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", tc.name, err)
		}
		if string(out) != tc.in {
			t.Errorf("%s: round trip = %s, want %s", tc.name, out, tc.in)
		}
	}

	var unset RealtimeVoice
	if err := Unmarshal([]byte(`null`), &unset); err != nil || unset.VoiceStr != nil || unset.VoiceObject != nil {
		t.Fatalf("null voice = %+v, %v", unset, err)
	}
	name := "alloy"
	if _, err := Marshal(RealtimeVoice{VoiceStr: &name, VoiceObject: &RealtimeVoiceObject{Name: name}}); err == nil {
		t.Fatal("Marshal accepted a voice with both forms set")
	}
}

// BenchmarkParseRealtimeEvent covers the client events every realtime session parses: audio
// appends dominate, and session.update carries the voice.
func BenchmarkParseRealtimeEvent(b *testing.B) {
	events := map[string][]byte{
		"audio_append":   []byte(`{"type":"input_audio_buffer.append","audio":"UklGRiQAAABXQVZFZm10IBAAAAABAAEAgD4AAAB9AAACABAAZGF0YQAAAAA="}`),
		"session_update": []byte(`{"type":"session.update","session":{"voice":"alloy","instructions":"Be brief.","turn_detection":{"type":"server_vad"}}}`),
	}
	for name, raw := range events {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ParseRealtimeEvent(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
