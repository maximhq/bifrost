package schemas

import "testing"

// reasoning.mode ("standard" | "pro") is an OpenAI Responses-only knob that chat
// callers send inside the reasoning object, next to effort.
func TestChatParametersReasoningMode(t *testing.T) {
	var cp ChatParameters
	if err := Unmarshal([]byte(`{"reasoning":{"effort":"high","mode":"pro"}}`), &cp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cp.Reasoning == nil || cp.Reasoning.Mode == nil || *cp.Reasoning.Mode != "pro" {
		t.Fatalf("reasoning.mode should decode, got %+v", cp.Reasoning)
	}
	if cp.Reasoning.Effort == nil || *cp.Reasoning.Effort != "high" {
		t.Fatalf("reasoning.effort should decode alongside mode, got %+v", cp.Reasoning)
	}
}
