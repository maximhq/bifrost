package schemas

import (
	"strings"
	"testing"
)

// TestChatMessageIsErrorRoundTrip verifies is_error on a tool message survives
// the ChatMessage JSON round trip — ChatMessage has a custom UnmarshalJSON
// that reattaches ChatToolMessage, so a new field silently dropping there
// would lose the marker for every HTTP-transport caller.
func TestChatMessageIsErrorRoundTrip(t *testing.T) {
	in := `{"role":"tool","tool_call_id":"call_1","content":"command exited with code 1","is_error":true}`

	var msg ChatMessage
	if err := Unmarshal([]byte(in), &msg); err != nil {
		t.Fatalf("unmarshal tool message: %v", err)
	}
	if msg.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if msg.ChatToolMessage.IsError == nil || !*msg.ChatToolMessage.IsError {
		t.Fatal("is_error must survive unmarshal")
	}

	out, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal tool message: %v", err)
	}
	if !strings.Contains(string(out), `"is_error":true`) {
		t.Fatalf("is_error must survive marshal, got: %s", out)
	}

	// Absent is_error must stay absent — omitempty, never a false literal.
	var clean ChatMessage
	if err := Unmarshal([]byte(`{"role":"tool","tool_call_id":"call_2","content":"ok"}`), &clean); err != nil {
		t.Fatalf("unmarshal clean tool message: %v", err)
	}
	out, err = MarshalSorted(clean)
	if err != nil {
		t.Fatalf("marshal clean tool message: %v", err)
	}
	if strings.Contains(string(out), "is_error") {
		t.Fatalf("absent is_error must not serialize, got: %s", out)
	}

	// Explicit false is distinct from absent: a caller that marks a tool result
	// as succeeded must not have that collapse into "unspecified", which the
	// Bedrock converter would read as the same thing but Anthropic would not.
	var notError ChatMessage
	if err := Unmarshal([]byte(`{"role":"tool","tool_call_id":"call_3","content":"ok","is_error":false}`), &notError); err != nil {
		t.Fatalf("unmarshal false tool message: %v", err)
	}
	if notError.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if notError.ChatToolMessage.IsError == nil || *notError.ChatToolMessage.IsError {
		t.Fatal("explicit is_error:false must survive unmarshal as a non-nil false")
	}
	out, err = MarshalSorted(notError)
	if err != nil {
		t.Fatalf("marshal false tool message: %v", err)
	}
	if !strings.Contains(string(out), `"is_error":false`) {
		t.Fatalf("explicit is_error:false must survive marshal, got: %s", out)
	}
}
