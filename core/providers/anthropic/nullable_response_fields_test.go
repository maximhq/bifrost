package anthropic

import (
	"encoding/json"
	"testing"
)

func TestAnthropicMessageResponseRequiredNullableFieldsMarshalAsNull(t *testing.T) {
	response := AnthropicMessageResponse{
		ID:      "msg_1",
		Type:    "message",
		Role:    "assistant",
		Content: []AnthropicContentBlock{},
		Model:   "claude-sonnet-4-20250514",
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if _, ok := got["stop_reason"]; !ok {
		t.Fatalf("stop_reason key missing from response: %#v", got)
	}
	if got["stop_reason"] != nil {
		t.Fatalf("stop_reason = %#v, want nil", got["stop_reason"])
	}
	if _, ok := got["stop_sequence"]; !ok {
		t.Fatalf("stop_sequence key missing from response: %#v", got)
	}
	if got["stop_sequence"] != nil {
		t.Fatalf("stop_sequence = %#v, want nil", got["stop_sequence"])
	}
}

func TestAnthropicStopReasonMarshalPreservesNonEmptyValue(t *testing.T) {
	data, err := json.Marshal(AnthropicStopReasonEndTurn)
	if err != nil {
		t.Fatalf("marshal stop reason: %v", err)
	}
	if string(data) != `"end_turn"` {
		t.Fatalf("stop reason JSON = %s, want %q", data, `"end_turn"`)
	}
}
