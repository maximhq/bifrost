package schemas

import "testing"

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
