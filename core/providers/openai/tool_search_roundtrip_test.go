package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

// TestResponsesInputRoundTripsToolSearchItems reproduces the codex deferral
// follow-up request that previously made Bifrost reject the whole input array
// with "openai responses request input is neither a string nor an array of
// responses messages" (tool_search_call.arguments is an OBJECT, not the string
// function_call uses), and verifies the items now round-trip unchanged.
func TestResponsesInputRoundTripsToolSearchItems(t *testing.T) {
	input := []byte(`[
		{"role":"user","content":[{"type":"input_text","text":"Read REQ-1"}]},
		{"type":"tool_search_call","call_id":"s1","execution":"client","arguments":{"query":"ticket","limit":1}},
		{"type":"tool_search_output","call_id":"s1","status":"completed","execution":"client","tools":[{"type":"function","name":"get_request","description":"x","defer_loading":true,"parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}}]}
	]`)

	var in OpenAIResponsesRequestInput
	if err := in.UnmarshalJSON(input); err != nil {
		t.Fatalf("unmarshal failed (the original bug): %v", err)
	}
	if got := len(in.OpenAIResponsesRequestInputArray); got != 3 {
		t.Fatalf("expected 3 input items, got %d", got)
	}

	out, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// tool_search_call.arguments must stay a JSON OBJECT (OpenAI 400s a string).
	if !gjson.GetBytes(out, "1.arguments").IsObject() {
		t.Fatalf("tool_search_call.arguments is not an object: %s", gjson.GetBytes(out, "1.arguments").Raw)
	}
	if got := gjson.GetBytes(out, "1.arguments.query").String(); got != "ticket" {
		t.Fatalf("tool_search_call.arguments.query lost: %q", got)
	}
	// tool_search_output.tools[0].type must survive (OpenAI requires it).
	if got := gjson.GetBytes(out, "2.tools.0.type").String(); got != "function" {
		t.Fatalf("tool_search_output.tools[0].type lost: %q (full: %s)", got, gjson.GetBytes(out, "2.tools").Raw)
	}
	if got := gjson.GetBytes(out, "2.tools.0.name").String(); got != "get_request" {
		t.Fatalf("tool_search_output.tools[0].name lost: %q", got)
	}
	// The ordinary user message must still parse/serialize normally.
	if got := gjson.GetBytes(out, "0.role").String(); got != "user" {
		t.Fatalf("plain user message broke: %q", got)
	}
	t.Logf("round-trip OK:\n%s", out)
}
