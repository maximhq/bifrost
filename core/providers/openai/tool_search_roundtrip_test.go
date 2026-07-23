package openai

import (
	"fmt"
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

// TestResponsesInputRoundTripsAdditionalToolsItems reproduces the codex
// first request for code-mode models (e.g. gpt-5.6-sol), whose
// `additional_tools` input item previously had its nested tools re-serialized
// through the mcp_list_tools shape — dropping every `tools[].type`
// discriminator and making OpenAI reject with "Missing required parameter:
// 'input[0].tools[0].type'" — and verifies the item now round-trips unchanged.
func TestResponsesInputRoundTripsAdditionalToolsItems(t *testing.T) {
	input := []byte(`[
		{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"apply_patch","description":"Apply a patch"},{"type":"function","name":"shell","description":"Runs a shell command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},{"type":"namespace","name":"repo_tools","description":"Repository helper tools","tools":[{"type":"function","name":"open_file","description":"Open a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},{"type":"function","name":"list_files","description":"List files","parameters":{"type":"object","properties":{"dir":{"type":"string"}},"required":["dir"]}}]}]},
		{"role":"user","content":[{"type":"input_text","text":"Reply exactly with OK."}]}
	]`)

	var in OpenAIResponsesRequestInput
	if err := in.UnmarshalJSON(input); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got := len(in.OpenAIResponsesRequestInputArray); got != 2 {
		t.Fatalf("expected 2 input items, got %d", got)
	}

	out, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Every nested tools[].type must survive (OpenAI requires them).
	for i, want := range []string{"custom", "function", "namespace"} {
		if got := gjson.GetBytes(out, fmt.Sprintf("0.tools.%d.type", i)).String(); got != want {
			t.Fatalf("additional_tools.tools[%d].type lost: %q (full: %s)", i, got, gjson.GetBytes(out, "0.tools").Raw)
		}
	}
	// Function parameters and the namespace's nested tool list must survive.
	if !gjson.GetBytes(out, "0.tools.1.parameters").IsObject() {
		t.Fatalf("additional_tools function parameters lost: %s", gjson.GetBytes(out, "0.tools.1").Raw)
	}
	if got := gjson.GetBytes(out, "0.tools.2.tools.#").Int(); got != 2 {
		t.Fatalf("additional_tools namespace nested tools lost: %s", gjson.GetBytes(out, "0.tools.2").Raw)
	}
	// The typed mcp_list_tools shape must not bleed in.
	if gjson.GetBytes(out, "0.tools.0.input_schema").Exists() {
		t.Fatalf("mcp_list_tools shape leaked into additional_tools: %s", gjson.GetBytes(out, "0.tools.0").Raw)
	}
	// The ordinary user message must still parse/serialize normally.
	if got := gjson.GetBytes(out, "1.role").String(); got != "user" {
		t.Fatalf("plain user message broke: %q", got)
	}
	t.Logf("round-trip OK:\n%s", out)
}
