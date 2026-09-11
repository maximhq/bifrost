package compat

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// TestCloneBifrostReq_OriginalUntouched guards the copy-on-write clone: core
// rebuilds fallback attempts from the caller's request, so every write the
// plugin makes must land on the clone. The clone shares tool schemas and
// messages with the original, so a new in-place write into those shows up here.
func TestCloneBifrostReq_OriginalUntouched(t *testing.T) {
	p := newTestPlugin(t, map[string][]string{"model-router": {"tools"}})

	tools := []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("lookup"), CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
		{Type: schemas.ResponsesToolTypeWebSearch},
	}
	req := newResponsesRequest(schemas.Azure, "model-router", &schemas.ResponsesParameters{
		Temperature:     schemas.Ptr(0.3),
		TopP:            schemas.Ptr(0.9),
		MaxOutputTokens: schemas.Ptr(100),
		Metadata:        &map[string]any{"k": "v"},
		Reasoning:       &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high"), Summary: schemas.Ptr("detailed")},
		ToolChoice:      &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("auto")},
		Tools:           tools,
	})
	req.ResponsesRequest.Input = []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser)}}

	before, err := schemas.MarshalSorted(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	modified, _, err := p.PreLLMHook(newTestContext(), req)
	if err != nil {
		t.Fatalf("PreLLMHook: %v", err)
	}
	after, err := schemas.MarshalSorted(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("original request mutated by PreLLMHook\nbefore: %s\nafter:  %s", before, after)
	}
	got, err := schemas.MarshalSorted(modified)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) == string(before) {
		t.Fatalf("PreLLMHook dropped nothing, so the guard proved nothing: %s", got)
	}
	if n := len(modified.ResponsesRequest.Params.Tools); n != 1 {
		t.Errorf("clone tools = %d, want web_search dropped", n)
	}
	if len(tools) != 2 || tools[0].CacheControl == nil {
		t.Errorf("original tools slice mutated: %+v", tools)
	}
}

// TestCloneBifrostReq_FallbackSeesOriginalTools mirrors core's fallback path:
// prepareFallbackRequest shallow-copies the caller's request, so a fallback
// attempt runs PreLLMHook against the same Params the primary attempt saw. The
// tool rewrites made for the primary provider must not reach the fallback.
func TestCloneBifrostReq_FallbackSeesOriginalTools(t *testing.T) {
	ds := datasheet.NewTestStore(nil)
	ds.SetSupportedParamsForTest(map[string][]string{
		"primary-model":  {"tools"},
		"fallback-model": {"tools", "web_search", "cache_control", "tool_choice"},
	})
	p, err := Init(Config{ShouldDropParams: true, ShouldConvertParams: true}, bifrost.NewNoOpLogger(), modelcatalog.NewTestCatalogWithDatasheet(ds))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	original := newResponsesRequest(schemas.Bedrock, "primary-model", &schemas.ResponsesParameters{
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("required")},
		Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("lookup"), CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
			{Type: schemas.ResponsesToolTypeWebSearch},
			{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("github"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
				Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("create_issue")}},
			}},
		},
	})

	ctx := newTestContext()
	primary, _, err := p.PreLLMHook(ctx, original)
	if err != nil {
		t.Fatalf("PreLLMHook (primary): %v", err)
	}
	// Primary attempt: web_search dropped, cache_control stripped, namespace flattened, tool_choice dropped.
	if got := toolTypes(primary.ResponsesRequest.Params.Tools); len(got) != 2 || got[0] != schemas.ResponsesToolTypeFunction || got[1] != schemas.ResponsesToolTypeFunction {
		t.Fatalf("primary tools = %v, want [function function]", got)
	}
	if primary.ResponsesRequest.Params.Tools[0].CacheControl != nil || primary.ResponsesRequest.Params.ToolChoice != nil {
		t.Fatalf("primary attempt kept cache_control/tool_choice, so the fallback check below proves nothing")
	}

	// Build the fallback request the way core's prepareFallbackRequest does.
	fallbackReq := *original
	tmp := *original.ResponsesRequest
	tmp.Provider = schemas.OpenAI
	tmp.Model = "fallback-model"
	fallbackReq.ResponsesRequest = &tmp

	fallback, _, err := p.PreLLMHook(ctx, &fallbackReq)
	if err != nil {
		t.Fatalf("PreLLMHook (fallback): %v", err)
	}
	tools := fallback.ResponsesRequest.Params.Tools
	if got := toolTypes(tools); len(got) != 3 || got[1] != schemas.ResponsesToolTypeWebSearch || got[2] != schemas.ResponsesToolTypeNamespace {
		t.Errorf("fallback tools = %v, want the caller's [function web_search namespace]", got)
	}
	if len(tools) > 0 && tools[0].CacheControl == nil {
		t.Errorf("fallback lost cache_control on tools[0]")
	}
	if fallback.ResponsesRequest.Params.ToolChoice == nil {
		t.Errorf("fallback lost tool_choice")
	}
	if len(tools) > 2 && tools[2].ResponsesToolNamespace != nil && len(tools[2].ResponsesToolNamespace.Tools) != 1 {
		t.Errorf("fallback namespace tool lost its nested tools")
	}
}

func toolTypes(tools []schemas.ResponsesTool) []schemas.ResponsesToolType {
	types := make([]schemas.ResponsesToolType, len(tools))
	for i, tool := range tools {
		types[i] = tool.Type
	}
	return types
}
