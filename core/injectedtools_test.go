package bifrost

import (
	"context"
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
	return schemas.DeepCopyChatTool(tool), nil // the MCP manager's contract: the caller owns the copy
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
	return &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "web_search", MCPClientName: "tavily", ToolName: "search"},
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
		assert.True(t, set.isInjected("web_search"), "calls are classified by the configured name")
		assert.False(t, set.isInjected("tavily-search"), "the prefixed MCP name is never shown to the model")
		assert.Equal(t, 4, set.maxDepth)
	}

	assert.Nil(t, resolveInjectedTools(src, webSearchConfig(), schemas.EmbeddingRequest, nil), "only chat and responses carry tools")
	assert.Nil(t, resolveInjectedTools(src, &schemas.ProviderConfig{}, schemas.ChatCompletionRequest, nil))
	assert.Nil(t, resolveInjectedTools(src, nil, schemas.ChatCompletionRequest, nil))
	assert.Nil(t, resolveInjectedTools(nil, webSearchConfig(), schemas.ChatCompletionRequest, nil), "no MCP manager, nothing to inject")

	missing := webSearchConfig()
	missing.InjectedTools[0].ToolName = "crawl"
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
	assert.Equal(t, "web_search", got.Params.Tools[1].Function.Name)
	assert.Nil(t, got.Params.WebSearchOptions, "web_search_options would turn native search back on")
	assert.Equal(t, "web_search", got.Params.ToolChoice.ChatToolChoiceStruct.Function.Name,
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
	assert.Equal(t, "web_search", *got.Params.Tools[1].Name)
	choice := got.Params.ToolChoice.ResponsesToolChoiceStruct
	assert.Equal(t, schemas.ResponsesToolChoiceTypeFunction, choice.Type)
	assert.Equal(t, "web_search", *choice.Name)

	assert.Len(t, original.Params.Tools, 3, "the shared request keeps the caller's tools for a fallback")
	assert.Equal(t, schemas.ResponsesToolChoiceTypeWebSearchPreview, original.Params.ToolChoice.ResponsesToolChoiceStruct.Type)
}

// The name decides replacement: a caller's function with the injected tool's name is
// replaced by it, and a choice pinning that name now pins the injected tool.
func TestApplyInjectedTools_ClientToolWithInjectedNameIsReplaced(t *testing.T) {
	set := testInjectedSet(t)
	own := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "web_search"}}
	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools: []schemas.ChatTool{{Type: "web_search"}, own},
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "web_search"},
		}},
	}}, set)
	require.Len(t, chat.Params.Tools, 1, "a model cannot be offered two tools with one name")
	assert.Equal(t, "Search the web", *chat.Params.Tools[0].Function.Description, "the injected tool won")
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
	require.Len(t, responses.Params.Tools, 1)
	assert.Equal(t, "Search the web", *responses.Params.Tools[0].Description)
	assert.Equal(t, "web_search", *responses.Params.ToolChoice.ResponsesToolChoiceStruct.Name)
}

// A native server tool is replaced by name too, not only a function: Anthropic names
// its server tools, and a request cannot declare two tools with one name.
func TestApplyInjectedToolsChat_NativeToolWithInjectedNameIsReplaced(t *testing.T) {
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool()}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "code_execution", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, set)
	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools: []schemas.ChatTool{{Type: "code_execution_20250522", Name: "code_execution"}},
	}}, set)
	require.Len(t, chat.Params.Tools, 1)
	assert.Equal(t, "code_execution", chat.Params.Tools[0].Function.Name)
}

// Only the tool named web_search replaces native web search. Any other injected tool
// is added next to whatever the caller sent, native web search and its choice included.
func TestApplyInjectedTools_OtherNamesLeaveNativeWebSearch(t *testing.T) {
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool()}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "lookup", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, set)
	assert.Empty(t, set.webSearchName)

	pinNative := &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "web_search"},
	}}
	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools:            []schemas.ChatTool{{Type: "web_search_20250305", Name: "web_search"}},
		WebSearchOptions: &schemas.ChatWebSearchOptions{},
		ToolChoice:       pinNative,
	}}, set)
	require.Len(t, chat.Params.Tools, 2)
	assert.Equal(t, "web_search_20250305", string(chat.Params.Tools[0].Type))
	assert.Nil(t, chat.Params.Tools[0].Function)
	assert.Equal(t, "lookup", chat.Params.Tools[1].Function.Name)
	assert.NotNil(t, chat.Params.WebSearchOptions)
	assert.Same(t, pinNative, chat.Params.ToolChoice)

	responses := applyInjectedToolsResponses(&schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
	}}, set)
	require.Len(t, responses.Params.Tools, 2)
	assert.Equal(t, schemas.ResponsesToolTypeWebSearch, responses.Params.Tools[0].Type)
	assert.Equal(t, "lookup", *responses.Params.Tools[1].Name)
}

// MCP auto-injection, or a client, can declare the injected tool under its prefixed MCP
// name and pin it. That declaration is dropped as a duplicate of the alias, so a choice
// naming it must follow to the alias, or the provider gets a choice for an undeclared tool.
func TestApplyInjectedTools_ChoicePinningPrefixedMCPNameFollowsAlias(t *testing.T) {
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool()}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "lookup", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, set)

	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools: []schemas.ChatTool{tavilySearchTool()},
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "tavily-search"},
		}},
	}}, set)
	require.Len(t, chat.Params.Tools, 1)
	assert.Equal(t, "lookup", chat.Params.Tools[0].Function.Name)
	assert.Equal(t, "lookup", chat.Params.ToolChoice.ChatToolChoiceStruct.Function.Name, "the pinned prefixed name now pins the alias")

	responses := applyInjectedToolsResponses(&schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("tavily-search")}},
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
			Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("tavily-search"),
		}},
	}}, set)
	require.Len(t, responses.Params.Tools, 1)
	assert.Equal(t, "lookup", *responses.Params.Tools[0].Name)
	assert.Equal(t, "lookup", *responses.Params.ToolChoice.ResponsesToolChoiceStruct.Name, "the pinned prefixed name now pins the alias")
}

// A choice that already names a declared injected tool is kept, even when that name is
// also another injected tool's prefixed MCP name: the alias the operator configured wins.
func TestApplyInjectedTools_ChoiceNamingAnAliasIsKept(t *testing.T) {
	fetch := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "fetcher-fetch"}}
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool(), "fetcher-fetch": fetch}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "lookup", MCPClientName: "tavily", ToolName: "search"},
		{Name: "tavily-search", MCPClientName: "fetcher", ToolName: "fetch"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, set)

	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "tavily-search"},
		}},
	}}, set)
	assert.Equal(t, "tavily-search", chat.Params.ToolChoice.ChatToolChoiceStruct.Function.Name, "the fetch alias stays pinned")

	responses := applyInjectedToolsResponses(&schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
			Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("tavily-search"),
		}},
	}}, set)
	assert.Equal(t, "tavily-search", *responses.Params.ToolChoice.ResponsesToolChoiceStruct.Name, "the fetch alias stays pinned")
}

// A custom tool sharing an injected tool's name is replaced by it, so a custom choice
// for that name must become a function choice for the injected tool.
func TestApplyInjectedTools_CustomChoiceForReplacedToolPinsInjectedFunction(t *testing.T) {
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool()}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "apply_patch", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, set)

	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools: []schemas.ChatTool{{Type: schemas.ChatToolTypeCustom, Name: "apply_patch", Custom: &schemas.ChatToolCustom{}}},
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type: schemas.ChatToolChoiceTypeCustom, Custom: &schemas.ChatToolChoiceCustom{Name: "apply_patch"},
		}},
	}}, set)
	require.Len(t, chat.Params.Tools, 1)
	assert.Equal(t, schemas.ChatToolTypeFunction, chat.Params.Tools[0].Type, "the custom declaration was replaced")
	choice := chat.Params.ToolChoice.ChatToolChoiceStruct
	assert.Equal(t, schemas.ChatToolChoiceTypeFunction, choice.Type)
	require.NotNil(t, choice.Function)
	assert.Equal(t, "apply_patch", choice.Function.Name)

	responses := applyInjectedToolsResponses(&schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCustom, Name: schemas.Ptr("apply_patch")}},
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
			Type: schemas.ResponsesToolChoiceTypeCustom, Name: schemas.Ptr("apply_patch"),
		}},
	}}, set)
	require.Len(t, responses.Params.Tools, 1)
	assert.Equal(t, schemas.ResponsesToolTypeFunction, responses.Params.Tools[0].Type, "the custom declaration was replaced")
	rchoice := responses.Params.ToolChoice.ResponsesToolChoiceStruct
	assert.Equal(t, schemas.ResponsesToolChoiceTypeFunction, rchoice.Type)
	assert.Equal(t, "apply_patch", *rchoice.Name)
}

// An allowed_tools choice lists tools by name, so an entry for a declaration an injected
// tool dropped must name the injected tool instead, or the provider gets an allowed tool
// that was never declared. The caller's choice is never written.
func TestApplyInjectedToolsChat_AllowedToolsEntriesFollowInjectedTools(t *testing.T) {
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool()}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "lookup", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, set)
	original := &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type: schemas.ChatToolChoiceTypeAllowedTools,
		AllowedTools: &schemas.ChatToolChoiceAllowedTools{Mode: "required", Tools: []schemas.ChatToolChoiceAllowedToolsTool{
			{Type: "function", Function: schemas.ChatToolChoiceFunction{Name: "tavily-search"}},
			{Type: "function", Function: schemas.ChatToolChoiceFunction{Name: "get_weather"}},
		}},
	}}
	weather := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "get_weather"}}

	chat := applyInjectedToolsChat(&schemas.BifrostChatRequest{Params: &schemas.ChatParameters{
		Tools:      []schemas.ChatTool{tavilySearchTool(), weather},
		ToolChoice: original,
	}}, set)

	allowed := chat.Params.ToolChoice.ChatToolChoiceStruct.AllowedTools
	require.NotNil(t, allowed)
	assert.Equal(t, "required", allowed.Mode)
	var names []string
	for _, tool := range allowed.Tools {
		names = append(names, tool.Function.Name)
	}
	assert.Equal(t, []string{"lookup", "get_weather"}, names, "the dropped prefixed declaration is allowed as its alias")
	assert.Equal(t, "tavily-search", original.ChatToolChoiceStruct.AllowedTools.Tools[0].Function.Name, "the caller's choice is untouched")
}

func TestApplyInjectedToolsResponses_AllowedToolsEntriesFollowInjectedTools(t *testing.T) {
	fetch := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "fetcher-fetch"}}
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool(), "fetcher-fetch": fetch}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "lookup", MCPClientName: "tavily", ToolName: "search"},
		{Name: "web_search", MCPClientName: "fetcher", ToolName: "fetch"},
	}}, schemas.ResponsesRequest, nil)
	require.NotNil(t, set)
	original := &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeAllowedTools,
		Mode: schemas.Ptr("auto"),
		Tools: []schemas.ResponsesToolChoiceAllowedToolDef{
			{Type: "function", Name: schemas.Ptr("tavily-search")},
			{Type: "web_search"},
			{Type: "function", Name: schemas.Ptr("lookup")},
			{Type: "function", Name: schemas.Ptr("get_weather")},
		},
	}}

	responses := applyInjectedToolsResponses(&schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("tavily-search")},
			{Type: schemas.ResponsesToolTypeWebSearch},
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather")},
		},
		ToolChoice: original,
	}}, set)

	choice := responses.Params.ToolChoice.ResponsesToolChoiceStruct
	assert.Equal(t, schemas.ResponsesToolChoiceTypeAllowedTools, choice.Type)
	assert.Equal(t, "auto", *choice.Mode)
	var entries []string
	for _, tool := range choice.Tools {
		name := ""
		if tool.Name != nil {
			name = *tool.Name
		}
		entries = append(entries, tool.Type+":"+name)
	}
	assert.Equal(t, []string{"function:lookup", "function:web_search", "function:get_weather"}, entries,
		"prefixed and native entries name the injected tools, once each")
	assert.Equal(t, "tavily-search", *original.ResponsesToolChoiceStruct.Tools[0].Name, "the caller's choice is untouched")
	assert.Len(t, original.ResponsesToolChoiceStruct.Tools, 4)
}

// Every configured row is injected under its own name. A row that does not resolve,
// or repeats a name, is skipped without taking the others down.
func TestResolveInjectedTools_MultipleRows(t *testing.T) {
	fetch := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "fetcher-fetch"}}
	src := fakeInjectedToolSource{tools: map[string]schemas.ChatTool{"tavily-search": tavilySearchTool(), "fetcher-fetch": fetch}, maxDepth: 4}
	set := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "web_search", MCPClientName: "tavily", ToolName: "search"},
		{Name: "gone", MCPClientName: "tavily", ToolName: "crawl"},
		{Name: "fetch_page", MCPClientName: "fetcher", ToolName: "fetch"},
		{Name: "fetch_page", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ResponsesRequest, nil)
	require.NotNil(t, set)
	require.Len(t, set.tools, 2)
	assert.Equal(t, "web_search", set.tools[0].Function.Name)
	assert.Equal(t, "fetch_page", set.tools[1].Function.Name)
	assert.Equal(t, "web_search", set.webSearchName)
	assert.Equal(t, injectedTarget{clientName: "fetcher", toolName: "fetcher-fetch"}, set.targets["fetch_page"], "the first row with a name wins")
	assert.False(t, set.isInjected("gone"))
	assert.Equal(t, "fetcher-fetch", fetch.Function.Name, "renaming never reaches the source schema")

	onlyBroken := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "web_search", MCPClientName: "tavily", ToolName: "crawl"},
	}}, schemas.ChatCompletionRequest, nil)
	assert.Nil(t, onlyBroken, "with nothing resolved the request goes out as sent")

	// The first row with a name wins even when it cannot be resolved: a later row with the
	// same name must not swap in another backend, and native web search stays as sent.
	brokenFirst := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "web_search", MCPClientName: "tavily", ToolName: "crawl"},
		{Name: "web_search", MCPClientName: "fetcher", ToolName: "fetch"},
		{Name: "lookup", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, brokenFirst)
	assert.False(t, brokenFirst.isInjected("web_search"), "the later web_search row must not stand in for the unavailable first one")
	assert.Empty(t, brokenFirst.webSearchName, "native web search is left alone")
	assert.True(t, brokenFirst.isInjected("lookup"))

	// Two rows for the same MCP tool: the first row owns it, and the later row is not
	// injected at all, as the management API would have rejected it.
	sameTool := resolveInjectedTools(src, &schemas.ProviderConfig{InjectedTools: []schemas.InjectedTool{
		{Name: "web_search", MCPClientName: "tavily", ToolName: "search"},
		{Name: "lookup", MCPClientName: "tavily", ToolName: "search"},
	}}, schemas.ChatCompletionRequest, nil)
	require.NotNil(t, sameTool)
	assert.Len(t, sameTool.tools, 1)
	assert.True(t, sameTool.isInjected("web_search"))
	assert.False(t, sameTool.isInjected("lookup"), "a later row for an already injected MCP tool is skipped")
	assert.Equal(t, "web_search", sameTool.mcpNames["tavily-search"], "the prefixed MCP name maps to the first row")
}

// The model calls the configured name; the MCP manager and the governance approval
// get the prefixed tool the operator picked.
func TestMCPInjectedCall_MapsNameToMCPTool(t *testing.T) {
	set := testInjectedSet(t)
	call := chatToolCall("c1", "web_search")

	mcpCall, target := mcpInjectedCall(set, call)

	assert.Equal(t, "tavily-search", *mcpCall.Function.Name)
	assert.Equal(t, call.Function.Arguments, mcpCall.Function.Arguments)
	assert.Equal(t, injectedTarget{clientName: "tavily", toolName: "tavily-search"}, target)
	assert.Equal(t, "web_search", *call.Function.Name, "the caller's call keeps the model-facing name")

	other, none := mcpInjectedCall(set, chatToolCall("c2", "get_weather"))
	assert.Equal(t, "get_weather", *other.Function.Name)
	assert.Equal(t, injectedTarget{}, none)
}

func chatToolCall(id, name string) schemas.ChatAssistantMessageToolCall {
	return schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr(id),
		Type:     schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(name), Arguments: `{"query":"q"}`},
	}
}

func chatTurnResponse(text string, finish string, promptTokens int, calls ...schemas.ChatAssistantMessageToolCall) *schemas.BifrostChatResponse {
	msg := &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant}
	if text != "" {
		msg.Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}
	}
	if len(calls) > 0 {
		msg.ChatAssistantMessage = &schemas.ChatAssistantMessage{ToolCalls: calls}
	}
	return &schemas.BifrostChatResponse{
		ID: "resp",
		Choices: []schemas.BifrostResponseChoice{{
			FinishReason:                schemas.Ptr(finish),
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: msg},
		}},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: promptTokens, CompletionTokens: 1, TotalTokens: promptTokens + 1},
	}
}

// scriptedChatTurns answers each dispatch with the next scripted response and records
// every request the loop sent.
type scriptedChatTurns struct {
	responses []*schemas.BifrostChatResponse
	sent      []*schemas.BifrostChatRequest
}

func (s *scriptedChatTurns) dispatch(req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	s.sent = append(s.sent, req)
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func searchResult(call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
	return &schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("results for " + *call.ID)},
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: call.ID},
	}
}

func TestRunInjectedChatLoop_ExecutesInjectedCallsAndReturnsFinalAnswer(t *testing.T) {
	set := testInjectedSet(t)
	turns := &scriptedChatTurns{responses: []*schemas.BifrostChatResponse{
		chatTurnResponse("Let me search.", "tool_calls", 10, chatToolCall("c1", "web_search")),
		chatTurnResponse("It is sunny.", "stop", 20),
	}}
	var executed []string
	exec := func(call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
		executed = append(executed, *call.ID)
		return searchResult(call)
	}
	original := &schemas.BifrostChatRequest{Model: "m", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("weather?")}}}}

	resp, err := runInjectedChatLoop(original, set, turns.dispatch, exec)

	require.Nil(t, err)
	assert.Equal(t, []string{"c1"}, executed)
	require.Len(t, turns.sent, 2)
	second := turns.sent[1]
	require.Len(t, second.Input, 3, "user, assistant turn with the injected call, tool result")
	assert.Equal(t, "c1", *second.Input[1].ToolCalls[0].ID)
	assert.Equal(t, "c1", *second.Input[2].ToolCallID)
	assert.Equal(t, "web_search", second.Params.Tools[0].Function.Name, "every turn still declares the injected tool")

	msg := resp.Choices[0].Message
	assert.Nil(t, msg.ChatAssistantMessage, "the client never sees the injected call")
	assert.Equal(t, "Let me search.\n\nIt is sunny.", *msg.Content.ContentStr)
	assert.Equal(t, "stop", *resp.Choices[0].FinishReason)
	assert.Equal(t, 30, resp.Usage.PromptTokens, "usage covers every model turn")
	assert.Equal(t, 32, resp.Usage.TotalTokens)
	assert.Len(t, original.Input, 1, "the caller's request is never appended to")
}

func TestRunInjectedChatLoop_MixedTurnDropsClientCallAndReasks(t *testing.T) {
	set := testInjectedSet(t)
	turns := &scriptedChatTurns{responses: []*schemas.BifrostChatResponse{
		chatTurnResponse("", "tool_calls", 10, chatToolCall("c1", "get_weather"), chatToolCall("c2", "web_search")),
		chatTurnResponse("", "tool_calls", 10, chatToolCall("c3", "get_weather")),
	}}
	resp, err := runInjectedChatLoop(&schemas.BifrostChatRequest{Model: "m"}, set, turns.dispatch, searchResult)

	require.Nil(t, err)
	second := turns.sent[1]
	assistant := second.Input[0]
	require.Len(t, assistant.ToolCalls, 1, "the client call is dropped: it has no result to pair with")
	assert.Equal(t, "c2", *assistant.ToolCalls[0].ID)

	calls := resp.Choices[0].Message.ToolCalls
	require.Len(t, calls, 1, "the re-issued client call comes back untouched")
	assert.Equal(t, "c3", *calls[0].ID)
	assert.Equal(t, "tool_calls", *resp.Choices[0].FinishReason)
}

func TestRunInjectedChatLoop_StopsAtMaxDepth(t *testing.T) {
	set := testInjectedSet(t)
	set.maxDepth = 2
	turns := &scriptedChatTurns{responses: []*schemas.BifrostChatResponse{
		chatTurnResponse("", "tool_calls", 1, chatToolCall("c1", "web_search")),
		chatTurnResponse("Still searching.", "tool_calls", 1, chatToolCall("c2", "web_search")),
	}}
	resp, err := runInjectedChatLoop(&schemas.BifrostChatRequest{Model: "m"}, set, turns.dispatch, searchResult)

	require.Nil(t, err)
	assert.Len(t, turns.sent, 2)
	assert.Nil(t, resp.Choices[0].Message.ChatAssistantMessage, "an unexecuted injected call never reaches the client")
	assert.Equal(t, "stop", *resp.Choices[0].FinishReason)
}

func TestRunInjectedChatLoop_PropagatesTurnError(t *testing.T) {
	set := testInjectedSet(t)
	calls := 0
	dispatch := func(*schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		calls++
		if calls == 1 {
			return chatTurnResponse("", "tool_calls", 1, chatToolCall("c1", "web_search")), nil
		}
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream down"}}
	}
	resp, err := runInjectedChatLoop(&schemas.BifrostChatRequest{Model: "m"}, set, dispatch, searchResult)
	assert.Nil(t, resp)
	require.NotNil(t, err)
	assert.Equal(t, "upstream down", err.Error.Message)
}

func responsesFunctionCall(callID, name string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		ID:   schemas.Ptr("fc_" + callID),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr(callID), Name: schemas.Ptr(name), Arguments: schemas.Ptr(`{"query":"q"}`),
		},
	}
}

func responsesText(text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(text)},
	}
}

func TestRunInjectedResponsesLoop_ExecutesInjectedCalls(t *testing.T) {
	set := testInjectedSet(t)
	var sent []*schemas.BifrostResponsesRequest
	scripted := []*schemas.BifrostResponsesResponse{
		{Output: []schemas.ResponsesMessage{responsesText("Searching."), responsesFunctionCall("c1", "web_search"), responsesFunctionCall("c2", "get_weather")},
			Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 1, TotalTokens: 11}},
		{Output: []schemas.ResponsesMessage{responsesText("Sunny.")},
			Usage: &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 2, TotalTokens: 22}},
	}
	dispatch := func(req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		sent = append(sent, req)
		resp := scripted[0]
		scripted = scripted[1:]
		return resp, nil
	}

	resp, err := runInjectedResponsesLoop(&schemas.BifrostResponsesRequest{Model: "m"}, set, dispatch, searchResult)

	require.Nil(t, err)
	require.Len(t, sent, 2)
	next := sent[1].Input
	require.Len(t, next, 3, "prior text, the injected call and its output; the client call is dropped")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *next[1].Type)
	assert.Equal(t, "c1", *next[1].CallID)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *next[2].Type)
	assert.Equal(t, "c1", *next[2].CallID)

	require.Len(t, resp.Output, 2, "the client sees both turns' text and no function calls")
	assert.Equal(t, "Searching.", *resp.Output[0].Content.ContentStr)
	assert.Equal(t, "Sunny.", *resp.Output[1].Content.ContentStr)
	assert.Equal(t, 30, resp.Usage.InputTokens)
	assert.Equal(t, 33, resp.Usage.TotalTokens)
}

// A client that forces web search gets the choice remapped to the injected tool. Kept on
// every turn, it would force another search after each result until maxDepth, so the
// loop relaxes a forced choice to auto once an injected call has run.
func TestRunInjectedChatLoop_ForcedChoiceRelaxedAfterInjectedTurn(t *testing.T) {
	set := testInjectedSet(t)
	turns := &scriptedChatTurns{responses: []*schemas.BifrostChatResponse{
		chatTurnResponse("", "tool_calls", 10, chatToolCall("c1", "web_search")),
		chatTurnResponse("Sunny.", "stop", 20),
	}}
	original := &schemas.BifrostChatRequest{Model: "m", Params: &schemas.ChatParameters{
		ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")},
	}}
	_, err := runInjectedChatLoop(original, set, turns.dispatch, searchResult)
	require.Nil(t, err)
	assert.Equal(t, "required", *turns.sent[0].Params.ToolChoice.ChatToolChoiceStr, "the first turn keeps the caller's choice")
	assert.Equal(t, "auto", *turns.sent[1].Params.ToolChoice.ChatToolChoiceStr, "after a search the model must be free to answer")
	assert.Equal(t, "required", *original.Params.ToolChoice.ChatToolChoiceStr, "the caller's request is untouched")
}

func TestRunInjectedResponsesLoop_ForcedChoiceRelaxedAfterInjectedTurn(t *testing.T) {
	set := testInjectedSet(t)
	var sent []*schemas.BifrostResponsesRequest
	scripted := []*schemas.BifrostResponsesResponse{
		{Output: []schemas.ResponsesMessage{responsesFunctionCall("c1", "web_search")}},
		{Output: []schemas.ResponsesMessage{responsesText("Sunny.")}},
	}
	dispatch := func(req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		sent = append(sent, req)
		resp := scripted[0]
		scripted = scripted[1:]
		return resp, nil
	}
	original := &schemas.BifrostResponsesRequest{Model: "m", Params: &schemas.ResponsesParameters{
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeWebSearchPreview}},
	}}
	_, err := runInjectedResponsesLoop(original, set, dispatch, searchResult)
	require.Nil(t, err)
	assert.Equal(t, schemas.ResponsesToolChoiceTypeFunction, sent[0].Params.ToolChoice.ResponsesToolChoiceStruct.Type, "the first turn forces the injected tool")
	require.NotNil(t, sent[1].Params.ToolChoice.ResponsesToolChoiceStr)
	assert.Equal(t, "auto", *sent[1].Params.ToolChoice.ResponsesToolChoiceStr)
}

// An allowed_tools choice in required mode is relaxed by mode only: its set is the
// client's constraint on which tools may run, and must survive into later turns.
func TestRelaxForcedChoice_AllowedToolsKeepsItsSet(t *testing.T) {
	chatSet := []schemas.ChatToolChoiceAllowedToolsTool{{Type: "function", Function: schemas.ChatToolChoiceFunction{Name: "get_weather"}}}
	chatIn := &schemas.ChatParameters{ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type:         schemas.ChatToolChoiceTypeAllowedTools,
		AllowedTools: &schemas.ChatToolChoiceAllowedTools{Mode: "required", Tools: chatSet},
	}}}
	chat := relaxForcedChatChoice(chatIn)
	require.NotNil(t, chat.ToolChoice.ChatToolChoiceStruct, "the allowed set must not collapse to a bare auto")
	allowed := chat.ToolChoice.ChatToolChoiceStruct.AllowedTools
	require.NotNil(t, allowed)
	assert.Equal(t, "auto", allowed.Mode)
	assert.Equal(t, chatSet, allowed.Tools)
	assert.Equal(t, "required", chatIn.ToolChoice.ChatToolChoiceStruct.AllowedTools.Mode, "the turn's own params are not written")

	respSet := []schemas.ResponsesToolChoiceAllowedToolDef{{Type: "function", Name: schemas.Ptr("get_weather")}}
	respIn := &schemas.ResponsesParameters{ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type:  schemas.ResponsesToolChoiceTypeAllowedTools,
		Mode:  schemas.Ptr("required"),
		Tools: respSet,
	}}}
	resp := relaxForcedResponsesChoice(respIn)
	require.NotNil(t, resp.ToolChoice.ResponsesToolChoiceStruct, "the allowed set must not collapse to a bare auto")
	assert.Equal(t, schemas.ResponsesToolChoiceTypeAllowedTools, resp.ToolChoice.ResponsesToolChoiceStruct.Type)
	assert.Equal(t, "auto", *resp.ToolChoice.ResponsesToolChoiceStruct.Mode)
	assert.Equal(t, respSet, resp.ToolChoice.ResponsesToolChoiceStruct.Tools)
	assert.Equal(t, "required", *respIn.ToolChoice.ResponsesToolChoiceStruct.Mode, "the turn's own params are not written")
}

// With n>1 the loop is driven by the first choice, but no other choice may carry an
// injected call the client cannot run.
func TestRunInjectedChatLoop_OtherChoicesNeverCarryInjectedCalls(t *testing.T) {
	set := testInjectedSet(t)
	resp := chatTurnResponse("Sunny.", "stop", 10)
	second := chatTurnResponse("", "tool_calls", 0, chatToolCall("c9", "web_search"), chatToolCall("c8", "get_weather"))
	second.Choices[0].Index = 1
	resp.Choices = append(resp.Choices, second.Choices[0])
	turns := &scriptedChatTurns{responses: []*schemas.BifrostChatResponse{resp}}

	out, err := runInjectedChatLoop(&schemas.BifrostChatRequest{Model: "m"}, set, turns.dispatch, searchResult)

	require.Nil(t, err)
	require.Len(t, out.Choices, 2)
	calls := out.Choices[1].Message.ToolCalls
	require.Len(t, calls, 1, "the injected call is removed, the client call stays")
	assert.Equal(t, "get_weather", *calls[0].Function.Name)
}

// Earlier turns' text goes in front of a final answer made of content blocks without
// flattening the blocks.
func TestRunInjectedChatLoop_PrependKeepsContentBlocks(t *testing.T) {
	set := testInjectedSet(t)
	final := chatTurnResponse("", "stop", 20)
	final.Choices[0].Message.Content = &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("Sunny."), CacheControl: &schemas.CacheControl{Type: "ephemeral"}},
	}}
	turns := &scriptedChatTurns{responses: []*schemas.BifrostChatResponse{
		chatTurnResponse("Let me search.", "tool_calls", 10, chatToolCall("c1", "web_search")),
		final,
	}}

	out, err := runInjectedChatLoop(&schemas.BifrostChatRequest{Model: "m"}, set, turns.dispatch, searchResult)

	require.Nil(t, err)
	blocks := out.Choices[0].Message.Content.ContentBlocks
	require.Len(t, blocks, 2, "earlier text becomes a leading block; the answer's own blocks survive")
	assert.Equal(t, "Let me search.", *blocks[0].Text)
	assert.Equal(t, "Sunny.", *blocks[1].Text)
	assert.NotNil(t, blocks[1].CacheControl, "block details survive")
}

// A later turn's failure must still bill the turns that completed before it.
func TestRunInjectedLoops_ErrorBillsEarlierTurns(t *testing.T) {
	set := testInjectedSet(t)
	calls := 0
	chatDispatch := func(*schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		calls++
		if calls == 1 {
			return chatTurnResponse("", "tool_calls", 10, chatToolCall("c1", "web_search")), nil
		}
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream down"}}
	}
	_, err := runInjectedChatLoop(&schemas.BifrostChatRequest{Model: "m"}, set, chatDispatch, searchResult)
	require.NotNil(t, err)
	require.NotNil(t, err.ExtraFields.BilledUsage, "chat: the completed first turn is billed")
	assert.Equal(t, 10, err.ExtraFields.BilledUsage.PromptTokens)

	rcalls := 0
	responsesDispatch := func(*schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		rcalls++
		if rcalls == 1 {
			return &schemas.BifrostResponsesResponse{
				Output: []schemas.ResponsesMessage{responsesFunctionCall("c1", "web_search")},
				Usage:  &schemas.ResponsesResponseUsage{InputTokens: 7, OutputTokens: 1, TotalTokens: 8},
			}, nil
		}
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream down"}}
	}
	_, err = runInjectedResponsesLoop(&schemas.BifrostResponsesRequest{Model: "m"}, set, responsesDispatch, searchResult)
	require.NotNil(t, err)
	require.NotNil(t, err.ExtraFields.BilledUsage, "responses: the completed first turn is billed")
	assert.Equal(t, 7, err.ExtraFields.BilledUsage.PromptTokens)
}

// The last upstream reply's raw bytes stay on the assembled response: logging
// (store_raw_request_response) and send_back_raw_response clients read them there. The
// Anthropic converter, not the loop, decides not to return them as the whole answer.
func TestRunInjectedLoops_KeepLastTurnRawResponse(t *testing.T) {
	set := testInjectedSet(t)
	final := chatTurnResponse("Sunny.", "stop", 20)
	final.ExtraFields.RawResponse = `{"raw":"turn 2"}`
	turns := &scriptedChatTurns{responses: []*schemas.BifrostChatResponse{
		chatTurnResponse("", "tool_calls", 10, chatToolCall("c1", "web_search")),
		final,
	}}
	chat, err := runInjectedChatLoop(&schemas.BifrostChatRequest{Model: "m"}, set, turns.dispatch, searchResult)
	require.Nil(t, err)
	assert.Equal(t, `{"raw":"turn 2"}`, chat.ExtraFields.RawResponse, "chat: logging still gets the provider reply")

	scripted := []*schemas.BifrostResponsesResponse{
		{Output: []schemas.ResponsesMessage{responsesFunctionCall("c1", "web_search")}},
		{Output: []schemas.ResponsesMessage{responsesText("Sunny.")}, ExtraFields: schemas.BifrostResponseExtraFields{RawResponse: `{"raw":"turn 2"}`}},
	}
	dispatch := func(*schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		resp := scripted[0]
		scripted = scripted[1:]
		return resp, nil
	}
	responses, err := runInjectedResponsesLoop(&schemas.BifrostResponsesRequest{Model: "m"}, set, dispatch, searchResult)
	require.Nil(t, err)
	assert.Equal(t, `{"raw":"turn 2"}`, responses.ExtraFields.RawResponse, "responses: logging still gets the provider reply")
}

// Claude Code reaches Anthropic through raw-body passthrough, which forwards the
// caller's bytes and would carry the native web search tool past the rewrite. An
// attempt with injected tools takes the typed path instead, the same switch a
// non-Anthropic provider gets.
func TestClearAnthropicPassthroughForInjectedTools(t *testing.T) {
	passthrough := func() *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
		ctx.SetValue(schemas.BifrostContextKeyURLPath, "/v1/messages")
		return ctx
	}
	typed := &schemas.BifrostRequest{ResponsesRequest: &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser)}},
	}}

	ctx := passthrough()
	set := clearAnthropicPassthroughForInjectedTools(ctx, testInjectedSet(t), typed)
	require.NotNil(t, set)
	assert.Equal(t, false, ctx.Value(schemas.BifrostContextKeyUseRawRequestBody))
	assert.Equal(t, false, ctx.Value(schemas.BifrostContextKeySendBackRawResponse), "raw upstream events would leak the injected tool_use blocks")
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyURLPath))

	untouched := passthrough()
	assert.Nil(t, clearAnthropicPassthroughForInjectedTools(untouched, nil, typed))
	assert.Equal(t, true, untouched.Value(schemas.BifrostContextKeyUseRawRequestBody), "a provider without injected tools keeps passthrough")
}

// A request whose messages live only in the raw body (a direct SDK call with an empty
// typed input) has nothing for the typed path to send. It keeps passthrough and goes
// out without the injected tool rather than losing its conversation.
func TestClearAnthropicPassthroughForInjectedTools_RawOnlyRequestKeepsPassthrough(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	rawOnly := &schemas.BifrostRequest{ResponsesRequest: &schemas.BifrostResponsesRequest{
		Input:          []schemas.ResponsesMessage{},
		RawRequestBody: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}}

	assert.Nil(t, clearAnthropicPassthroughForInjectedTools(ctx, testInjectedSet(t), rawOnly), "injection is skipped")
	assert.Equal(t, true, ctx.Value(schemas.BifrostContextKeyUseRawRequestBody), "passthrough is kept")
}

// When an attempt with injected tools fails over to one without them, the later
// attempt gets back the raw-body passthrough the injected attempt switched off.
func TestRestoreAnthropicPassthroughAfterInjectedTools(t *testing.T) {
	codec := &struct{ name string }{"codec"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, "/v1/messages")
	ctx.SetValue(schemas.BifrostContextKeyRawStreamTextCodec, codec)
	typed := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}}}}

	require.NotNil(t, clearAnthropicPassthroughForInjectedTools(ctx, testInjectedSet(t), typed))
	require.Equal(t, false, ctx.Value(schemas.BifrostContextKeyUseRawRequestBody))

	restoreAnthropicPassthroughAfterInjectedTools(ctx)
	assert.Equal(t, true, ctx.Value(schemas.BifrostContextKeyUseRawRequestBody))
	assert.Equal(t, true, ctx.Value(schemas.BifrostContextKeySendBackRawResponse))
	assert.Equal(t, "/v1/messages", ctx.Value(schemas.BifrostContextKeyURLPath))
	assert.Same(t, codec, ctx.Value(schemas.BifrostContextKeyRawStreamTextCodec))

	again := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	again.SetValue(schemas.BifrostContextKeyUseRawRequestBody, false)
	restoreAnthropicPassthroughAfterInjectedTools(again)
	assert.Equal(t, false, again.Value(schemas.BifrostContextKeyUseRawRequestBody), "nothing to restore when injected tools never switched it off")
}

func deferredToolsResponsesRequest(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
	tool := func(name string, deferred bool) schemas.ResponsesTool {
		t := schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(name), Description: schemas.Ptr(name + " tool"),
			ResponsesToolFunction: &schemas.ResponsesToolFunction{},
		}
		if deferred {
			t.DeferLoading = schemas.Ptr(true)
		}
		return t
	}
	return &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: provider,
			Model:    model,
			Input: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("weather in Tokyo?")},
			}},
			Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{tool("lookup_city", false), tool("get_weather", true)}},
		},
	}
}

// The gateway tool search joins an attempt only when it defers tools on a provider
// with no tool search of its own, and rides alongside any configured MCP tools.
func TestWithGatewayToolSearch(t *testing.T) {
	gemini := deferredToolsResponsesRequest(schemas.Gemini, "gemini-3.8-flash")
	set := withGatewayToolSearch(nil, gemini, schemas.Gemini, "gemini-3.8-flash")
	require.NotNil(t, set)
	require.NotNil(t, set.search)
	assert.True(t, set.isInjected("bifrost_tool_search"))
	assert.Equal(t, 3, set.maxDepth)

	assert.Nil(t, withGatewayToolSearch(nil, deferredToolsResponsesRequest(schemas.OpenAI, "gpt-6-sol"), schemas.OpenAI, "gpt-6-sol"),
		"gpt-6-sol searches natively")
	assert.Nil(t, withGatewayToolSearch(nil, deferredToolsResponsesRequest(schemas.Anthropic, "claude-sonnet-4-6"), schemas.Anthropic, "claude-sonnet-4-6"),
		"Claude searches natively")

	stream := deferredToolsResponsesRequest(schemas.Gemini, "gemini-3.8-flash")
	stream.RequestType = schemas.ResponsesStreamRequest
	assert.NotNil(t, withGatewayToolSearch(nil, stream, schemas.Gemini, "gemini-3.8-flash"), "streaming attempts search too")
	chat := deferredToolsResponsesRequest(schemas.Gemini, "gemini-3.8-flash")
	chat.RequestType = schemas.ChatCompletionRequest
	assert.Nil(t, withGatewayToolSearch(nil, chat, schemas.Gemini, "gemini-3.8-flash"), "chat requests carry no deferred tools")

	eager := deferredToolsResponsesRequest(schemas.Gemini, "gemini-3.8-flash")
	eager.ResponsesRequest.Params.Tools[1].DeferLoading = nil
	assert.Nil(t, withGatewayToolSearch(nil, eager, schemas.Gemini, "gemini-3.8-flash"), "nothing deferred")

	mcp := &injectedToolSet{tools: []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "web_search"}}}, maxDepth: 7}
	combined := withGatewayToolSearch(mcp, gemini, schemas.Gemini, "gemini-3.8-flash")
	assert.Same(t, mcp, combined)
	assert.Len(t, combined.tools, 2)
	assert.Equal(t, 7, combined.maxDepth, "the configured agent depth is kept")
}

// The loop loads what the search found into the next turn, and the caller sees one
// response without the search call.
func TestRunInjectedResponsesLoop_GatewayToolSearchLoadsTools(t *testing.T) {
	req := deferredToolsResponsesRequest(schemas.Gemini, "gemini-3.8-flash")
	set := withGatewayToolSearch(nil, req, schemas.Gemini, "gemini-3.8-flash")
	require.NotNil(t, set)

	call := func(callID, name, args string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr(callID), Name: schemas.Ptr(name), Arguments: schemas.Ptr(args)},
		}
	}
	var turns []*schemas.BifrostResponsesRequest
	resp, bifrostErr := runInjectedResponsesLoop(req.ResponsesRequest, set, func(turn *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		turns = append(turns, turn)
		if len(turns) == 1 {
			return &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{call("call_s1", "bifrost_tool_search", `{"query":"weather"}`)}}, nil
		}
		return &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{call("call_w1", "get_weather", `{"city":"Tokyo"}`)}}, nil
	}, func(c schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
		return set.search.Execute(c)
	})
	require.Nil(t, bifrostErr)
	require.Len(t, turns, 2)
	toolNames := func(r *schemas.BifrostResponsesRequest) []string {
		var names []string
		for _, tool := range r.Params.Tools {
			names = append(names, *tool.Name)
		}
		return names
	}
	assert.Equal(t, []string{"lookup_city", "bifrost_tool_search"}, toolNames(turns[0]))
	assert.Equal(t, []string{"lookup_city", "get_weather", "bifrost_tool_search"}, toolNames(turns[1]))
	require.Len(t, resp.Output, 1)
	assert.Equal(t, "get_weather", *resp.Output[0].Name)
	assert.True(t, *req.ResponsesRequest.Params.Tools[1].DeferLoading, "the caller's request is untouched")
}

// The per-attempt check every Responses request pays when no tool is deferred.
func BenchmarkWithGatewayToolSearch_NoDeferredTools(b *testing.B) {
	req := deferredToolsResponsesRequest(schemas.Gemini, "gemini-3.8-flash")
	req.ResponsesRequest.Params.Tools[1].DeferLoading = nil
	b.ReportAllocs()
	for b.Loop() {
		if withGatewayToolSearch(nil, req, schemas.Gemini, "gemini-3.8-flash") != nil {
			b.Fatal("unexpected tool search")
		}
	}
}
