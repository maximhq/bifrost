package openai

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToolSearchNamespaceBridge_AnthropicOriginReplayedToOpenAI is the case-#4
// regression test: an Anthropic-origin, already-completed tool_search_tool_call
// history item (produced by a prior turn against Anthropic) must convert to
// OpenAI's own native tool_search_call/tool_search_output shape when the
// conversation's backend switches to OpenAI -- reproducing the live failure
// (400 invalid_value: 'tool_search_tool_call') found by hand against a real
// OpenAI-compatible backend, and proving the fix resolves it.
func TestToolSearchNamespaceBridge_AnthropicOriginReplayedToOpenAI(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4-mini",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("find weather tool")},
			},
			{
				ID:     schemas.Ptr("srvtoolu_1"),
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeAnthropicToolSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("srvtoolu_1"),
					Name:      schemas.Ptr("tool_search_tool_bm25"),
					Arguments: schemas.Ptr(`{"query":"weather lookup"}`),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: schemas.Ptr(`["get_weather"]`),
					},
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type:         schemas.ResponsesToolTypeFunction,
					Name:         schemas.Ptr("get_weather"),
					Description:  schemas.Ptr("Get the current weather for a city"),
					DeferLoading: schemas.Ptr(true),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: &schemas.ToolFunctionParameters{
							Type: "object",
							Properties: schemas.NewOrderedMapFromPairs(
								schemas.KV("city", map[string]interface{}{"type": "string"}),
							),
							Required: []string{"city"},
						},
					},
				},
			},
		},
	}

	openAIReq := ToOpenAIResponsesRequest(nil, bifrostReq)
	if openAIReq == nil {
		t.Fatal("ToOpenAIResponsesRequest returned nil")
	}

	wire, err := openAIReq.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(wire)

	// The Anthropic-flavored type must never reach the OpenAI-bound wire --
	// this is exactly what the real backend rejected with a 400.
	if strings.Contains(raw, "tool_search_tool_call") {
		t.Fatalf("Anthropic-native tool_search_tool_call leaked into the OpenAI-bound request: %s", raw)
	}

	if got := gjson.Get(raw, "input.1.type").String(); got != "tool_search_call" {
		t.Fatalf("input[1].type = %q, want tool_search_call; raw=%s", got, raw)
	}
	if !gjson.Get(raw, "input.1.arguments").IsObject() {
		t.Fatalf("tool_search_call.arguments must be a JSON object, got: %s", gjson.Get(raw, "input.1.arguments").Raw)
	}
	if got := gjson.Get(raw, "input.1.arguments.query").String(); got != "weather lookup" {
		t.Errorf("arguments.query = %q, want %q", got, "weather lookup")
	}
	if got := gjson.Get(raw, "input.1.execution").String(); got != "client" {
		t.Errorf("tool_search_call.execution = %q, want client", got)
	}

	if got := gjson.Get(raw, "input.2.type").String(); got != "tool_search_output" {
		t.Fatalf("input[2].type = %q, want tool_search_output; raw=%s", got, raw)
	}
	if got := gjson.Get(raw, "input.2.call_id").String(); got != "srvtoolu_1" {
		t.Errorf("tool_search_output.call_id = %q, want srvtoolu_1", got)
	}
	toolsArr := gjson.Get(raw, "input.2.tools")
	if !toolsArr.IsArray() || len(toolsArr.Array()) != 1 {
		t.Fatalf("expected exactly 1 discovered tool in tools[], got: %s", toolsArr.Raw)
	}
	discovered := toolsArr.Array()[0]
	if got := discovered.Get("name").String(); got != "get_weather" {
		t.Errorf("discovered tool name = %q, want get_weather", got)
	}
	// L2 backfill: description/parameters/defer_loading must be recovered
	// from the request's own tools[] declaration, not left name-only.
	if got := discovered.Get("description").String(); got != "Get the current weather for a city" {
		t.Errorf("discovered tool description not backfilled from request tools[], got %q", got)
	}
	if !discovered.Get("parameters").Exists() {
		t.Error("discovered tool parameters not backfilled from request tools[]")
	}
	if !discovered.Get("defer_loading").Bool() {
		t.Error("discovered tool defer_loading not backfilled as true from request tools[]")
	}
}

// TestToolSearchNamespaceBridge_AnthropicOriginReplay_UnknownToolDegradesNameOnly
// verifies a discovered tool name that ISN'T found in the current request's
// tools[] degrades to a name-only definition instead of erroring or
// fabricating fields (defensive case per the original design doc; shouldn't
// happen under Anthropic's own contract, but must not crash if it does).
func TestToolSearchNamespaceBridge_AnthropicOriginReplay_UnknownToolDegradesNameOnly(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4-mini",
		Input: []schemas.ResponsesMessage{
			{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeAnthropicToolSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("srvtoolu_2"),
					Name:      schemas.Ptr("tool_search_tool_regex"),
					Arguments: schemas.Ptr(`{"pattern":".*weather.*"}`),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: schemas.Ptr(`["mystery_tool"]`),
					},
				},
			},
		},
		Params: &schemas.ResponsesParameters{Tools: nil},
	}

	openAIReq := ToOpenAIResponsesRequest(nil, bifrostReq)
	wire, err := openAIReq.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(wire)

	discovered := gjson.Get(raw, "input.1.tools.0")
	if got := discovered.Get("name").String(); got != "mystery_tool" {
		t.Fatalf("discovered tool name = %q, want mystery_tool; raw=%s", got, raw)
	}
	if discovered.Get("description").Exists() {
		t.Error("unknown tool must not fabricate a description")
	}
	if discovered.Get("parameters").Exists() {
		t.Error("unknown tool must not fabricate parameters")
	}
}

// TestToolSearchNamespaceBridge_AnthropicOriginReplay_InProgressOmitsOutput
// guards against fabricating a completed, empty-result tool_search_output
// for a search that is still running on the Anthropic side (no Output yet).
// Replaying it to OpenAI must emit only the call half -- inventing a
// completed output here would hide the real result once it arrives,
// corrupting conversation state on a backend switch mid-search. Found by an
// automated codex review pass.
func TestToolSearchNamespaceBridge_AnthropicOriginReplay_InProgressOmitsOutput(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4-mini",
		Input: []schemas.ResponsesMessage{
			{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeAnthropicToolSearchCall),
				Status: schemas.Ptr("in_progress"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("srvtoolu_3"),
					Name:      schemas.Ptr("tool_search_tool_bm25"),
					Arguments: schemas.Ptr(`{"query":"weather lookup"}`),
					// No Output -- the search hasn't completed yet.
				},
			},
		},
		Params: &schemas.ResponsesParameters{Tools: nil},
	}

	openAIReq := ToOpenAIResponsesRequest(nil, bifrostReq)
	wire, err := openAIReq.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(wire)

	if strings.Contains(raw, "tool_search_output") {
		t.Fatalf("must not fabricate a tool_search_output for an unfinished search, got: %s", raw)
	}
	if got := gjson.Get(raw, "input.0.type").String(); got != "tool_search_call" {
		t.Fatalf("expected only the tool_search_call half, got input[0].type=%q; raw=%s", got, raw)
	}
}
