package anthropic

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restrictionRequest builds a two-tool chat request so that a restriction has
// something to remove. The model is a current one so that no capability
// override drops the tool choice before these paths run.
func restrictionRequest(tc *schemas.ChatToolChoice, parallel *bool) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is the weather in Tokyo?")},
		}},
		Params: &schemas.ChatParameters{
			ToolChoice:        tc,
			ParallelToolCalls: parallel,
			Tools: []schemas.ChatTool{
				{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "tool_a"}},
				{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "tool_b"}},
			},
		},
	}
}

func allowedTools(mode string, names ...string) *schemas.ChatToolChoice {
	tools := make([]schemas.ChatToolChoiceAllowedToolsTool, 0, len(names))
	for _, n := range names {
		tools = append(tools, schemas.ChatToolChoiceAllowedToolsTool{
			Type:     "function",
			Function: schemas.ChatToolChoiceFunction{Name: n},
		})
	}
	return &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type:         schemas.ChatToolChoiceTypeAllowedTools,
		AllowedTools: &schemas.ChatToolChoiceAllowedTools{Mode: mode, Tools: tools},
	}}
}

func toolNames(req *AnthropicMessageRequest) []string {
	names := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		names = append(names, t.Name)
	}
	return names
}

// A caller that allows only tool_a must not have tool_b forwarded. Anthropic
// has no subset form on tool_choice, so the restriction has to be carried by
// the declaration list; mapping to "any" and forwarding both tools told the
// upstream it could call either one.
func TestToAnthropicChatRequest_AllowedToolsRestrictsDeclarations(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	req, err := ToAnthropicChatRequest(ctx, restrictionRequest(allowedTools("required", "tool_a"), nil))
	require.NoError(t, err)

	assert.Equal(t, []string{"tool_a"}, toolNames(req), "tool_b was not allowed and must not be forwarded")
	require.NotNil(t, req.ToolChoice)
	assert.Equal(t, "any", req.ToolChoice.Type, `mode "required" means a tool must be called`)
}

// mode "auto" means the model may call one of the allowed tools or none at all.
// Forcing "any" changed an optional call into a mandatory one.
func TestToAnthropicChatRequest_AllowedToolsAutoModeDoesNotForceACall(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	req, err := ToAnthropicChatRequest(ctx, restrictionRequest(allowedTools("auto", "tool_a"), nil))
	require.NoError(t, err)

	require.NotNil(t, req.ToolChoice)
	assert.Equal(t, "auto", req.ToolChoice.Type, `mode "auto" must not force a tool call`)
	assert.Equal(t, []string{"tool_a"}, toolNames(req))
}

// An allowed list naming only tools this request does not declare permits
// nothing. Forwarding the declarations anyway would hand the model every tool
// the caller just excluded - the same failure this conversion exists to stop,
// arrived at from the other direction.
func TestToAnthropicChatRequest_AllowedToolsUnknownNamesPermitNothing(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	req, err := ToAnthropicChatRequest(ctx, restrictionRequest(allowedTools("required", "not_declared"), nil))
	require.NoError(t, err)

	assert.Empty(t, toolNames(req), "no declared tool was allowed, so none may be forwarded")
	require.NotNil(t, req.ToolChoice)
	assert.Equal(t, "none", req.ToolChoice.Type,
		`"any" with an empty tool list is a request Anthropic rejects`)
}

// parallel_tool_calls: false has a direct Anthropic equivalent that was never
// populated, so the instruction was dropped and calls stayed parallel.
func TestToAnthropicChatRequest_ParallelToolCallsFalseDisablesParallelUse(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	t.Run("with an explicit tool choice", func(t *testing.T) {
		req, err := ToAnthropicChatRequest(ctx, restrictionRequest(
			&schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("auto")}, schemas.Ptr(false)))
		require.NoError(t, err)
		require.NotNil(t, req.ToolChoice)
		require.NotNil(t, req.ToolChoice.DisableParallelToolUse)
		assert.True(t, *req.ToolChoice.DisableParallelToolUse)
	})

	t.Run("with no tool choice of its own", func(t *testing.T) {
		req, err := ToAnthropicChatRequest(ctx, restrictionRequest(nil, schemas.Ptr(false)))
		require.NoError(t, err)
		require.NotNil(t, req.ToolChoice, "the flag has nowhere to live without a tool_choice")
		assert.Equal(t, "auto", req.ToolChoice.Type, "auto is Anthropic's default, so this adds no restriction")
		require.NotNil(t, req.ToolChoice.DisableParallelToolUse)
		assert.True(t, *req.ToolChoice.DisableParallelToolUse)
	})
}

// parallel_tool_calls: true is Anthropic's default, so nothing should be sent.
func TestToAnthropicChatRequest_ParallelToolCallsTrueSendsNothing(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	req, err := ToAnthropicChatRequest(ctx, restrictionRequest(nil, schemas.Ptr(true)))
	require.NoError(t, err)
	if req.ToolChoice != nil {
		assert.Nil(t, req.ToolChoice.DisableParallelToolUse)
	}
}

// A server tool - MCP toolset, web search, computer use - cannot be named in
// an OpenAI allowed_tools list, which describes function tools only. Filtering
// it on a name it cannot have would switch off a capability the caller enabled
// elsewhere in the same request.
func TestToAnthropicChatRequest_AllowedToolsLeavesServerToolsAlone(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	req := restrictionRequest(allowedTools("required", "tool_a"), nil)
	// Stand in for a converted server tool: it carries a Type, which a
	// converted function tool never does.
	converted, err := ToAnthropicChatRequest(ctx, req)
	require.NoError(t, err)

	serverType := AnthropicToolType("mcp_toolset")
	withServer := append([]AnthropicTool{{Name: "mcp-docs", Type: &serverType}}, converted.Tools...)
	filtered := filterToolsByAllowed(withServer, []schemas.ChatToolChoiceAllowedToolsTool{
		{Type: "function", Function: schemas.ChatToolChoiceFunction{Name: "tool_a"}},
	})

	names := make([]string, 0, len(filtered))
	for _, tl := range filtered {
		names = append(names, tl.Name)
	}
	assert.Contains(t, names, "mcp-docs", "a server tool must survive an allowed_tools filter")
	assert.Contains(t, names, "tool_a")
	assert.NotContains(t, names, "tool_b")
}

// With every nameable tool excluded, the choice must still be "none" even
// though server tools remain in the list.
func TestToAnthropicChatRequest_AllowedToolsNoneNameableStillMeansNone(t *testing.T) {
	serverType := AnthropicToolType("mcp_toolset")
	tools := []AnthropicTool{
		{Name: "mcp-docs", Type: &serverType},
		{Name: "tool_a"},
	}
	assert.Equal(t, 1, countFunctionTools(tools), "only the function tool is nameable")

	filtered := filterToolsByAllowed(tools, []schemas.ChatToolChoiceAllowedToolsTool{
		{Type: "function", Function: schemas.ChatToolChoiceFunction{Name: "not_declared"}},
	})
	assert.Equal(t, 0, countFunctionTools(filtered), "no function tool was allowed")
	assert.Len(t, filtered, 1, "the server tool is still there")
}
