package elevenlabs

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestRealtimeWebSocketURLRejectsTranscription(t *testing.T) {
	provider := &ElevenlabsProvider{}

	url, err := provider.RealtimeWebSocketURL(schemas.Key{}, "agent-id", "transcription")
	if err == nil {
		t.Fatal("RealtimeWebSocketURL() error = nil, want unsupported operation")
	}
	if url != "" {
		t.Fatalf("RealtimeWebSocketURL() URL = %q, want empty", url)
	}
	if err.Error == nil || err.Error.Code == nil || *err.Error.Code != "unsupported_operation" {
		t.Fatalf("RealtimeWebSocketURL() error = %+v, want unsupported_operation", err)
	}
}
