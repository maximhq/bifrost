package openai

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
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

// codexAdditionalToolsInput is the shape codex sends on its first request for
// code-mode models: every tool it owns is declared inside the input array.
const codexAdditionalToolsInput = `[
	{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec","description":"Run JavaScript","format":{"type":"grammar","syntax":"lark","definition":"start: pragma_source"}},{"type":"function","name":"wait","description":"Waits on a cell","parameters":{"type":"object","properties":{"cell_id":{"type":"string"}},"required":["cell_id"],"additionalProperties":false}},{"type":"namespace","name":"collaboration","description":"Sub-agent tools","tools":[{"type":"function","name":"spawn_agent","description":"Spawn an agent","parameters":{"type":"object","properties":{"task_name":{"type":"string"}},"required":["task_name"]}}]}]},
	{"role":"user","content":[{"type":"input_text","text":"Reply exactly with OK."}]}
]`

func codexAdditionalToolsRequest(t *testing.T, provider schemas.ModelProvider) []byte {
	t.Helper()

	var input OpenAIResponsesRequestInput
	if err := input.UnmarshalJSON([]byte(codexAdditionalToolsInput)); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	req := ToOpenAIResponsesRequest(nil, &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    "gpt-5.6-sol",
		Input:    input.OpenAIResponsesRequestInputArray,
	})
	if req == nil {
		t.Fatal("ToOpenAIResponsesRequest returned nil")
	}
	out, err := req.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return out
}

// TestBedrockMantleHoistsAdditionalTools verifies that codex `additional_tools`
// items are lifted into the top-level tools param for Bedrock Mantle, which
// rejects the item type with "Invalid 'input': value did not match any expected
// variant", and that the nested tool shapes survive the hoist intact.
func TestBedrockMantleHoistsAdditionalTools(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Bedrock, schemas.BedrockMantle} {
		t.Run(string(provider), func(t *testing.T) {
			out := codexAdditionalToolsRequest(t, provider)

			// The rejected item must be gone, leaving only the user message.
			if got := gjson.GetBytes(out, "input.#").Int(); got != 1 {
				t.Fatalf("expected 1 input item after hoist, got %d: %s", got, gjson.GetBytes(out, "input").Raw)
			}
			if got := gjson.GetBytes(out, "input.0.role").String(); got != "user" {
				t.Fatalf("wrong item survived: %s", gjson.GetBytes(out, "input.0").Raw)
			}

			// All three tools must land at the top level with their discriminators.
			if got := gjson.GetBytes(out, "tools.#").Int(); got != 3 {
				t.Fatalf("expected 3 hoisted tools, got %d: %s", got, gjson.GetBytes(out, "tools").Raw)
			}
			for i, want := range []string{"custom", "function", "namespace"} {
				if got := gjson.GetBytes(out, fmt.Sprintf("tools.%d.type", i)).String(); got != want {
					t.Fatalf("tools[%d].type = %q, want %q (full: %s)", i, got, want, gjson.GetBytes(out, "tools").Raw)
				}
			}
			// The custom tool's lark grammar must survive.
			if got := gjson.GetBytes(out, "tools.0.format.syntax").String(); got != "lark" {
				t.Fatalf("custom tool grammar lost: %s", gjson.GetBytes(out, "tools.0").Raw)
			}
			// Function parameters must survive, and strict must be false, not null —
			// strict-pydantic upstreams reject an explicit null.
			if !gjson.GetBytes(out, "tools.1.parameters").IsObject() {
				t.Fatalf("function parameters lost: %s", gjson.GetBytes(out, "tools.1").Raw)
			}
			if got := gjson.GetBytes(out, "tools.1.strict"); got.Type != gjson.False {
				t.Fatalf("hoisted function strict = %s, want false", got.Raw)
			}
			// The namespace's nested tool list must survive.
			if got := gjson.GetBytes(out, "tools.2.tools.#").Int(); got != 1 {
				t.Fatalf("namespace nested tools lost: %s", gjson.GetBytes(out, "tools.2").Raw)
			}
		})
	}
}

// TestOpenAIKeepsAdditionalToolsItem guards the native path: OpenAI accepts the
// item, so it must stay in input verbatim and must not be hoisted.
func TestOpenAIKeepsAdditionalToolsItem(t *testing.T) {
	out := codexAdditionalToolsRequest(t, schemas.OpenAI)

	if got := gjson.GetBytes(out, "input.#").Int(); got != 2 {
		t.Fatalf("expected 2 input items, got %d: %s", got, gjson.GetBytes(out, "input").Raw)
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "additional_tools" {
		t.Fatalf("additional_tools item not preserved: %s", gjson.GetBytes(out, "input.0").Raw)
	}
	if got := gjson.GetBytes(out, "input.0.tools.#").Int(); got != 3 {
		t.Fatalf("nested tools not preserved verbatim: %s", gjson.GetBytes(out, "input.0").Raw)
	}
	if gjson.GetBytes(out, "tools").Exists() {
		t.Fatalf("tools must not be hoisted for OpenAI: %s", gjson.GetBytes(out, "tools").Raw)
	}
}

// TestUnlistedProviderKeepsAdditionalToolsItem verifies the fail-open default:
// providers absent from ProviderFeatures are left untouched.
func TestUnlistedProviderKeepsAdditionalToolsItem(t *testing.T) {
	out := codexAdditionalToolsRequest(t, schemas.Groq)

	if got := gjson.GetBytes(out, "input.#").Int(); got != 2 {
		t.Fatalf("expected 2 input items for unlisted provider, got %d", got)
	}
	if gjson.GetBytes(out, "tools").Exists() {
		t.Fatalf("unlisted provider must not hoist: %s", gjson.GetBytes(out, "tools").Raw)
	}
}
