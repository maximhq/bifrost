package bifrost

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeInjectedToolSource struct {
	tools    map[string]schemas.ChatTool
	maxDepth int
}

func (f fakeInjectedToolSource) GetInjectedTool(clientName, toolName string) (schemas.ChatTool, error) {
	tool, ok := f.tools[clientName+"-"+toolName]
	if !ok {
		return schemas.ChatTool{}, errors.New("not found")
	}
	return tool, nil
}

func (f fakeInjectedToolSource) GetMaxAgentDepth() int { return f.maxDepth }

func tavilySearchTool() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "tavily-search",
			Description: schemas.Ptr("Search the web"),
			Parameters:  &schemas.ToolFunctionParameters{Type: "object"},
		},
	}
}

func webSearchConfig() *schemas.ProviderConfig {
	return &schemas.ProviderConfig{InjectedTools: &schemas.InjectedToolsConfig{
		WebSearch: &schemas.InjectedToolRef{MCPClientName: "tavily", ToolName: "search"},
	}}
}

func testInjectedSet(t *testing.T) *injectedToolSet {
	t.Helper()
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool()}, maxDepth: 4}
	set := resolveInjectedTools(src, webSearchConfig(), schemas.ChatCompletionRequest, nil)
	require.NotNil(t, set)
	return set
}

func TestResolveInjectedTools(t *testing.T) {
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool()}, maxDepth: 4}

	for _, rt := range []schemas.RequestType{schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest, schemas.ResponsesRequest, schemas.ResponsesStreamRequest} {
		set := resolveInjectedTools(src, webSearchConfig(), rt, nil)
		require.NotNil(t, set, "%s requests get the injected tool", rt)
		assert.True(t, set.isInjected("tavily-search"))
		assert.False(t, set.isInjected("tavily-crawl"))
		assert.Equal(t, 4, set.maxDepth)
	}

	assert.Nil(t, resolveInjectedTools(src, webSearchConfig(), schemas.EmbeddingRequest, nil), "only chat and responses carry tools")
	assert.Nil(t, resolveInjectedTools(src, &schemas.ProviderConfig{}, schemas.ChatCompletionRequest, nil))
	assert.Nil(t, resolveInjectedTools(src, nil, schemas.ChatCompletionRequest, nil))
	assert.Nil(t, resolveInjectedTools(nil, webSearchConfig(), schemas.ChatCompletionRequest, nil), "no MCP manager, nothing to inject")

	missing := webSearchConfig()
	missing.InjectedTools.WebSearch.ToolName = "crawl"
	assert.Nil(t, resolveInjectedTools(src, missing, schemas.ChatCompletionRequest, nil), "an unresolvable tool fails open")

	noDepth := fakeInjectedToolSource{tools: src.tools}
	assert.Equal(t, schemas.DefaultMaxAgentDepth, resolveInjectedTools(noDepth, webSearchConfig(), schemas.ChatCompletionRequest, nil).maxDepth)
}

func TestApplyInjectedToolsChat_ReplacesNativeWebSearch(t *testing.T) {
	set := testInjectedSet(t)
	weather := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "get_weather"}}
	original := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude",
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				weather,
				{Type: "web_search_20250305", Name: "web_search", MaxUses: schemas.Ptr(3)},
				{Type: "openrouter:web_search"},
			},
			WebSearchOptions: &schemas.ChatWebSearchOptions{},
			ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "web_search"},
			}},
		},
	}

	got := applyInjectedToolsChat(original, set)

	require.NotSame(t, original, got)
	require.Len(t, got.Params.Tools, 2)
	assert.Equal(t, "get_weather", got.Params.Tools[0].Function.Name)
	assert.Equal(t, "tavily-search", got.Params.Tools[1].Function.Name)
	assert.Nil(t, got.Params.WebSearchOptions, "web_search_options would turn native search back on")
	assert.Equal(t, "tavily-search", got.Params.ToolChoice.ChatToolChoiceStruct.Function.Name,
		"a choice pinned to the native tool now pins the tool that replaced it")

	// Copy-on-write: a fallback to another provider reuses the original request.
	assert.Len(t, original.Params.Tools, 3)
	assert.NotNil(t, original.Params.WebSearchOptions)
	assert.Equal(t, "web_search", original.Params.ToolChoice.ChatToolChoiceStruct.Function.Name)
}

func TestApplyInjectedToolsChat_NoToolsAndDuplicates(t *testing.T) {
	set := testInjectedSet(t)

	bare := applyInjectedToolsChat(&schemas.BifrostChatRequest{Model: "m"}, set)
	require.NotNil(t, bare.Params)
	require.Len(t, bare.Params.Tools, 1, "every request gets the tool, even one that sent none")

	// The regular MCP auto-injection may already have added the same client tool.
	dup := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{tavilySearchTool()}}}, set)
	assert.Len(t, dup.Params.Tools, 1, "the injected tool is never declared twice")

	assert.Nil(t, applyInjectedToolsChat(nil, set))
	plain := &schemas.BifrostChatRequest{Model: "m"}
	assert.Same(t, plain, applyInjectedToolsChat(plain, nil))
}

func TestApplyInjectedToolsResponses_ReplacesNativeWebSearch(t *testing.T) {
	set := testInjectedSet(t)
	original := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather")},
				{Type: schemas.ResponsesToolTypeWebSearch},
				{Type: schemas.ResponsesToolTypeWebSearchPreview},
			},
			ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeWebSearchPreview,
			}},
		},
	}

	got := applyInjectedToolsResponses(original, set)

	require.Len(t, got.Params.Tools, 2)
	assert.Equal(t, "get_weather", *got.Params.Tools[0].Name)
	assert.Equal(t, schemas.ResponsesToolTypeFunction, got.Params.Tools[1].Type)
	assert.Equal(t, "tavily-search", *got.Params.Tools[1].Name)
	choice := got.Params.ToolChoice.ResponsesToolChoiceStruct
	assert.Equal(t, schemas.ResponsesToolChoiceTypeFunction, choice.Type)
	assert.Equal(t, "tavily-search", *choice.Name)

	assert.Len(t, original.Params.Tools, 3, "the shared request keeps the caller's tools for a fallback")
	assert.Equal(t, schemas.ResponsesToolChoiceTypeWebSearchPreview, original.Params.ToolChoice.ResponsesToolChoiceStruct.Type)
}

// A caller's own function that happens to be called web_search is not a native tool:
// it stays declared and a choice pinning it is left alone.
func TestApplyInjectedTools_ClientFunctionNamedWebSearchIsUntouched(t *testing.T) {
	set := testInjectedSet(t)
	own := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "web_search"}}
	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools: []schemas.ChatTool{own},
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "web_search"},
		}},
	}}, set)
	require.Len(t, chat.Params.Tools, 2)
	assert.Equal(t, "web_search", chat.Params.ToolChoice.ChatToolChoiceStruct.Function.Name)

	responses := applyInjectedToolsResponses(&schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("web_search")}},
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
			Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("web_search"),
		}},
	}}, set)
	require.Len(t, responses.Params.Tools, 2)
	assert.Equal(t, "web_search", *responses.Params.ToolChoice.ResponsesToolChoiceStruct.Name)
}

// An unnamed native web search (OpenAI's hosted tool) is pinned by the conventional
// name web_search. When the caller also declares its own function web_search, a choice
// naming it targets the caller's retained function, not the removed native tool.
func TestApplyInjectedTools_ChoiceForRetainedFunctionSurvivesNativeRemoval(t *testing.T) {
	set := testInjectedSet(t)
	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{Type: "web_search"},
			{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "web_search"}},
		},
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "web_search"},
		}},
	}}, set)
	assert.Equal(t, "web_search", chat.Params.ToolChoice.ChatToolChoiceStruct.Function.Name)

	responses := applyInjectedToolsResponses(&schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeWebSearch},
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("web_search")},
		},
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
			Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("web_search"),
		}},
	}}, set)
	assert.Equal(t, "web_search", *responses.Params.ToolChoice.ResponsesToolChoiceStruct.Name)
}
