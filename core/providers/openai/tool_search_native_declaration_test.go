package openai

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToolSearchNativeDeclaration_StripsNameAndDedupesForOpenAI reproduces a
// live failure: a caller declaring tool_search the way Anthropic's own
// tool_search collapses to on ingest -- two neutral entries,
// {"type":"tool_search","name":"tool_search_tool_bm25"} and
// {"type":"tool_search","name":"tool_search_tool_regex"} (see
// core/providers/anthropic/responses.go:6685-6690) -- must not forward the
// "name" field to a real OpenAI-compatible backend, which rejects it
// ("Unknown parameter: 'tools[N].name'"), and must not emit two duplicate
// {"type":"tool_search"} declarations.
func TestToolSearchNativeDeclaration_StripsNameAndDedupesForOpenAI(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4-mini",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather")},
				{Type: schemas.ResponsesToolTypeToolSearch, Name: schemas.Ptr("tool_search_tool_bm25")},
				{Type: schemas.ResponsesToolTypeToolSearch, Name: schemas.Ptr("tool_search_tool_regex")},
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

	if strings.Contains(raw, "tool_search_tool_bm25") || strings.Contains(raw, "tool_search_tool_regex") {
		t.Fatalf("Anthropic sub-tool name leaked into the OpenAI-bound tool_search declaration: %s", raw)
	}

	toolSearchCount := 0
	for _, tool := range gjson.Get(raw, "tools").Array() {
		if tool.Get("type").String() == "tool_search" {
			toolSearchCount++
			if tool.Get("name").Exists() {
				t.Errorf("tool_search declaration must not carry a name field for OpenAI, got: %s", tool.Raw)
			}
		}
	}
	if toolSearchCount != 1 {
		t.Fatalf("expected exactly 1 collapsed tool_search declaration, got %d: %s", toolSearchCount, gjson.Get(raw, "tools").Raw)
	}

	if !strings.Contains(raw, `"name":"get_weather"`) {
		t.Errorf("unrelated function tool must survive untouched, got: %s", raw)
	}
}
