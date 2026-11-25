package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/internal/mcpvouch"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertMCPToolToBifrostSchema_EmptyParameters tests that tools with no parameters
// get an empty properties map instead of nil, which is required by some providers like OpenAI
func TestConvertMCPToolToBifrostSchema_EmptyParameters(t *testing.T) {
	// Create a tool with no parameters (like return_special_chars or return_null)
	mcpTool := &mcp.Tool{
		Name:        "test_tool_no_params",
		Description: "A test tool with no parameters",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{}, // Empty properties
			Required:   []string{},
		},
	}

	// Convert the tool
	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	// Verify the function was created
	if bifrostTool.Function == nil {
		t.Fatal("Function should not be nil")
	}

	// Verify parameters were created
	if bifrostTool.Function.Parameters == nil {
		t.Fatal("Parameters should not be nil")
	}

	// Verify properties is not nil (this is the key fix)
	if bifrostTool.Function.Parameters.Properties == nil {
		t.Error("Properties should not be nil for object type, even if empty")
	}

	// Verify it's an empty map
	if bifrostTool.Function.Parameters.Properties != nil && bifrostTool.Function.Parameters.Properties.Len() != 0 {
		t.Errorf("Expected empty properties map, got %d properties", bifrostTool.Function.Parameters.Properties.Len())
	}

	// Verify the type is preserved
	if bifrostTool.Function.Parameters.Type != "object" {
		t.Errorf("Expected type 'object', got '%s'", bifrostTool.Function.Parameters.Type)
	}
}

// TestConvertMCPToolToBifrostSchema_WithAnnotations tests that MCP tool annotations
// are preserved on ChatTool.Annotations (not ChatToolFunction) and are absent from JSON.
func TestConvertMCPToolToBifrostSchema_WithAnnotations(t *testing.T) {
	readOnly := true
	destructive := false

	mcpTool := &mcp.Tool{
		Name:        "read_resource",
		Description: "Reads a resource",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
		Annotations: mcp.ToolAnnotation{
			Title:           "Resource Reader",
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &destructive,
			IdempotentHint:  schemas.Ptr(true),
		},
	}

	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	// Annotations must be on ChatTool, not buried in Function
	require.NotNil(t, bifrostTool.Annotations, "Annotations should be set on ChatTool")
	assert.Equal(t, "Resource Reader", bifrostTool.Annotations.Title)
	require.NotNil(t, bifrostTool.Annotations.ReadOnlyHint)
	assert.True(t, *bifrostTool.Annotations.ReadOnlyHint)
	require.NotNil(t, bifrostTool.Annotations.DestructiveHint)
	assert.False(t, *bifrostTool.Annotations.DestructiveHint)
	require.NotNil(t, bifrostTool.Annotations.IdempotentHint)
	assert.True(t, *bifrostTool.Annotations.IdempotentHint)
	assert.Nil(t, bifrostTool.Annotations.OpenWorldHint)

	// The JSON sent to providers must not contain annotations
	toolJSON, err := json.Marshal(bifrostTool)
	require.NoError(t, err)
	s := string(toolJSON)
	assert.NotContains(t, s, "annotations", "annotations must be absent from provider JSON")
	assert.NotContains(t, s, "readOnlyHint", "readOnlyHint must be absent from provider JSON")
	assert.NotContains(t, s, "Resource Reader", "annotation title must be absent from provider JSON")
}

// TestConvertMCPToolToBifrostSchema_NilAnnotationsWhenAllZero verifies the nil guard:
// when all annotation fields are zero-valued, ChatTool.Annotations must remain nil.
func TestConvertMCPToolToBifrostSchema_NilAnnotationsWhenAllZero(t *testing.T) {
	mcpTool := &mcp.Tool{
		Name:        "no_hints_tool",
		Description: "A tool with no annotation hints",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
		Annotations: mcp.ToolAnnotation{}, // All zero values — Title empty, all hints nil
	}

	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	assert.Nil(t, bifrostTool.Annotations,
		"Annotations should be nil when all MCP annotation fields are zero")
}

// TestConvertMCPToolToBifrostSchema_WithParameters tests the normal case with parameters
func TestConvertMCPToolToBifrostSchema_WithParameters(t *testing.T) {
	// Create a tool with parameters
	mcpTool := &mcp.Tool{
		Name:        "test_tool_with_params",
		Description: "A test tool with parameters",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"param1": map[string]interface{}{
					"type":        "string",
					"description": "A string parameter",
				},
				"param2": map[string]interface{}{
					"type":        "number",
					"description": "A number parameter",
				},
			},
			Required: []string{"param1"},
		},
	}

	// Convert the tool
	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)

	// Verify the function was created
	if bifrostTool.Function == nil {
		t.Fatal("Function should not be nil")
	}

	// Verify parameters were created
	if bifrostTool.Function.Parameters == nil {
		t.Fatal("Parameters should not be nil")
	}

	// Verify properties is not nil
	if bifrostTool.Function.Parameters.Properties == nil {
		t.Fatal("Properties should not be nil")
	}

	// Verify the correct number of properties
	if bifrostTool.Function.Parameters.Properties.Len() != 2 {
		t.Errorf("Expected 2 properties, got %d", bifrostTool.Function.Parameters.Properties.Len())
	}

	// Verify required fields
	if len(bifrostTool.Function.Parameters.Required) != 1 {
		t.Errorf("Expected 1 required field, got %d", len(bifrostTool.Function.Parameters.Required))
	}

	if bifrostTool.Function.Parameters.Required[0] != "param1" {
		t.Errorf("Expected required field 'param1', got '%s'", bifrostTool.Function.Parameters.Required[0])
	}
}

// TestConvertMCPToolToBifrostSchema_PreservesDefs verifies that top-level JSON
// Schema definitions ($defs) on an MCP tool's input schema survive conversion.
// Without this, a $ref inside a property (which rides along in Properties) would
// be left dangling once the definitions it targets are dropped — the cause of
// Vertex Gemini rejecting such tools with INVALID_ARGUMENT.
func TestConvertMCPToolToBifrostSchema_PreservesDefs(t *testing.T) {
	mcpTool := &mcp.Tool{
		Name:        "suggest_time",
		Description: "Suggests time periods",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"preferences": map[string]interface{}{
					"$ref": "#/$defs/Preferences",
				},
			},
			Required: []string{"preferences"},
			Defs: map[string]interface{}{
				"Preferences": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"startHour": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}

	bifrostTool := convertMCPToolToBifrostSchema(mcpTool, defaultLogger)
	require.NotNil(t, bifrostTool.Function)
	require.NotNil(t, bifrostTool.Function.Parameters)
	require.NotNil(t, bifrostTool.Function.Parameters.Defs, "$defs must be preserved on conversion")

	data, err := json.Marshal(bifrostTool.Function.Parameters)
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"$defs"`, "marshalled schema must carry $defs")
	assert.Contains(t, s, "Preferences", "definition name must be present")
	assert.Contains(t, s, `"$ref"`, "the property $ref must still be present (resolution happens per-provider)")
}

// fetchReportBranches is a tagged union a schema generator emits for one tool's arguments.
const fetchReportBranches = `[
	{"type": "object", "properties": {"kind": {"type": "string", "const": "sales"}, "region": {"type": "string"}}, "required": ["kind"]},
	{"type": "object", "properties": {"kind": {"type": "string", "const": "inventory"}, "warehouse": {"type": "string"}}, "required": ["kind"]}
]`

// A root-level oneOf/anyOf/allOf (allowed by the 2026-07-28 spec) must survive discovery, or the
// tool is left with no parameters at all.
func TestConvertMCPToolToBifrostSchema_PreservesRootComposition(t *testing.T) {
	var want []any
	require.NoError(t, json.Unmarshal([]byte(fetchReportBranches), &want))

	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		t.Run(keyword, func(t *testing.T) {
			var mcpTool mcp.Tool
			require.NoError(t, json.Unmarshal([]byte(`{"name": "fetch_report", "description": "Fetch a report.",
				"inputSchema": {"type": "object", "`+keyword+`": `+fetchReportBranches+`}}`), &mcpTool))

			params := convertMCPToolToBifrostSchema(&mcpTool, defaultLogger).Function.Parameters
			require.NotNil(t, params)

			data, err := json.Marshal(params)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, want, got[keyword], "the %s branches must reach consumers unchanged", keyword)
			assert.Equal(t, "object", got["type"])
		})
	}

	t.Run("boolean members keep their meaning", func(t *testing.T) {
		var mcpTool mcp.Tool
		require.NoError(t, json.Unmarshal([]byte(`{"name": "rec", "inputSchema": {"type": "object",
			"allOf": [true, {"type": "object", "properties": {"id": {"type": "integer"}}, "required": ["id"]}],
			"oneOf": [false, {"type": "object"}]}}`), &mcpTool))
		data, err := json.Marshal(convertMCPToolToBifrostSchema(&mcpTool, defaultLogger).Function.Parameters)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(data, &got))
		require.Len(t, got["allOf"], 2, "a true member must not drop the allOf: %s", data)
		assert.Equal(t, map[string]any{}, got["allOf"].([]any)[0], "true is kept as {}")
		assert.Equal(t, []any{"id"}, got["allOf"].([]any)[1].(map[string]any)["required"])
		require.Len(t, got["oneOf"], 2, "a false member must not drop the oneOf: %s", data)
		assert.Equal(t, map[string]any{"not": map[string]any{}}, got["oneOf"].([]any)[0], "false is kept as {\"not\": {}}")
	})

	t.Run("arrays inside a branch's own composition get items", func(t *testing.T) {
		var mcpTool mcp.Tool
		require.NoError(t, json.Unmarshal([]byte(`{"name": "tagged", "inputSchema": {"type": "object", "oneOf": [
			{"allOf": [{"type": "object", "properties": {"tags": {"type": "array"}}}]}]}}`), &mcpTool))
		data, err := json.Marshal(convertMCPToolToBifrostSchema(&mcpTool, defaultLogger).Function.Parameters)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"tags":{"items":{},"type":"array"}`)
	})

	t.Run("a malformed composition is skipped", func(t *testing.T) {
		var mcpTool mcp.Tool
		require.NoError(t, json.Unmarshal([]byte(`{"name": "odd", "inputSchema": {"type": "object", "oneOf": "not-a-list"}}`), &mcpTool))
		params := convertMCPToolToBifrostSchema(&mcpTool, defaultLogger).Function.Parameters
		require.NotNil(t, params)
		assert.Empty(t, params.OneOf)
	})
}

// TestCanAutoExecuteTool covers the allow-list check that is now the sole enforcement
// point for whether Code Mode may run a tool without human approval (see the call site
// in codemode/starlark/executecode.go's callMCPTool). A tool must be on BOTH
// ToolsToExecute (executable at all) AND ToolsToAutoExecute (exempt from approval) -
// missing from either denies auto-execute.
func TestCanAutoExecuteTool(t *testing.T) {
	cfg := func(toolsToExecute, toolsToAutoExecute schemas.WhiteList) *schemas.MCPClientConfig {
		return &schemas.MCPClientConfig{
			Name:               "video",
			ToolsToExecute:     toolsToExecute,
			ToolsToAutoExecute: toolsToAutoExecute,
		}
	}

	tests := []struct {
		name   string
		tool   string
		config *schemas.MCPClientConfig
		want   bool
	}{
		{
			name:   "tool on both allow-lists auto-executes",
			tool:   "video-echo_message",
			config: cfg(schemas.WhiteList{"echo_message", "delete_video"}, schemas.WhiteList{"echo_message"}),
			want:   true,
		},
		{
			name:   "tool executable but not on the auto-execute allow-list is denied - the delete_video case",
			tool:   "video-delete_video",
			config: cfg(schemas.WhiteList{"echo_message", "delete_video"}, schemas.WhiteList{"echo_message"}),
			want:   false,
		},
		{
			name:   "tool not executable at all is denied regardless of auto-execute list",
			tool:   "video-delete_video",
			config: cfg(schemas.WhiteList{"echo_message"}, schemas.WhiteList{"echo_message", "delete_video"}),
			want:   false,
		},
		{
			name:   "nil ToolsToAutoExecute denies everything",
			tool:   "video-echo_message",
			config: cfg(schemas.WhiteList{"*"}, nil),
			want:   false,
		},
		{
			name:   "empty ToolsToAutoExecute denies everything",
			tool:   "video-echo_message",
			config: cfg(schemas.WhiteList{"*"}, schemas.WhiteList{}),
			want:   false,
		},
		{
			name:   "wildcard ToolsToAutoExecute allows any executable tool",
			tool:   "video-delete_video",
			config: cfg(schemas.WhiteList{"*"}, schemas.WhiteList{"*"}),
			want:   true,
		},
		{
			name:   "nil config denies everything",
			tool:   "video-echo_message",
			config: nil,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CanAutoExecuteTool(tt.tool, tt.config))
		})
	}
}

// TestAuthorizeCodeModeToolCall covers the invocation-time check callMCPTool runs for
// every tool Code Mode calls. tools_to_execute always applies; tools_to_auto_execute
// applies only when the agent loop marked the call unattended. An approved (or
// application-driven) run must not be denied by the auto-execute list.
func TestAuthorizeCodeModeToolCall(t *testing.T) {
	cfg := &schemas.MCPClientConfig{
		Name:               "video",
		ToolsToExecute:     schemas.WhiteList{"echo_message", "delete_video"},
		ToolsToAutoExecute: schemas.WhiteList{"echo_message"},
	}

	tests := []struct {
		name       string
		tool       string
		unattended bool
		wantErr    string
	}{
		{name: "unattended auto-executable tool runs", tool: "video-echo_message", unattended: true},
		{name: "unattended tool needing approval is refused", tool: "video-delete_video", unattended: true, wantErr: "requires approval"},
		{name: "approved tool needing approval runs", tool: "video-delete_video", unattended: false},
		{name: "unattended non-executable tool is refused", tool: "video-drop_all", unattended: true, wantErr: "not in tools_to_execute"},
		{name: "approved non-executable tool is refused", tool: "video-drop_all", unattended: false, wantErr: "not in tools_to_execute"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.unattended {
				ctx.SetValue(schemas.BifrostContextKeyMCPUnattendedExecution, true)
			}
			err := AuthorizeCodeModeToolCall(ctx, tt.tool, cfg)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func injectedToolTestManager(state schemas.MCPConnectionState) *MCPManager {
	m := &MCPManager{
		logger:         &MockLogger{},
		discoveryStore: NewDiscoveryStore(nil, nil),
		clientMap: map[string]*schemas.MCPClientState{
			"id-tavily": {
				State: state,
				ExecutionConfig: &schemas.MCPClientConfig{
					ID:   "id-tavily",
					Name: "tavily",
					// The operator exposes nothing from this client to callers. The
					// provider's injected web search must still resolve and run.
					ToolsToExecute: schemas.WhiteList{},
				},
			},
		},
	}
	m.discoveryStore.SetDiscovery(context.Background(), &schemas.MCPClientConfig{ID: "id-tavily"}, &schemas.MCPDiscovery{
		Tools: map[string]schemas.ChatTool{
			"tavily-search": {
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        "tavily-search",
					Description: schemas.Ptr("Search the web"),
					Parameters:  &schemas.ToolFunctionParameters{Type: "object", Required: []string{"query"}},
				},
			},
		},
	})
	return m
}

func TestGetInjectedTool(t *testing.T) {
	m := injectedToolTestManager(schemas.MCPConnectionStateHealthy)

	tool, err := m.GetInjectedTool(context.Background(), "tavily", "search")
	require.NoError(t, err, "injected tools ignore tools_to_execute: the provider config is the authorization")
	require.NotNil(t, tool.Function)
	assert.Equal(t, "tavily-search", tool.Function.Name)

	tool.Function.Name = "mutated"
	tool.Function.Parameters.Required[0] = "mutated"
	tool.Function.Parameters.Type = "mutated"
	again, err := m.GetInjectedTool(context.Background(), "tavily", "search")
	require.NoError(t, err)
	assert.Equal(t, "tavily-search", again.Function.Name, "the returned tool must be a copy, never the stored discovery's entry")
	assert.Equal(t, "object", again.Function.Parameters.Type, "the parameter schema is copied too")
	assert.Equal(t, []string{"query"}, again.Function.Parameters.Required, "nested schema slices are copied too")

	_, err = m.GetInjectedTool(context.Background(), "exa", "search")
	assert.ErrorContains(t, err, "not found")
	_, err = m.GetInjectedTool(context.Background(), "tavily", "crawl")
	assert.ErrorContains(t, err, "does not expose")

	_, err = injectedToolTestManager(schemas.MCPConnectionStateDisabled).GetInjectedTool(context.Background(), "tavily", "search")
	assert.ErrorContains(t, err, "disabled")
}

// fakeCodeMode serves one code-mode tool, executeToolCode.
type fakeCodeMode struct{ CodeMode }

func (fakeCodeMode) IsCodeModeTool(toolName string) bool { return toolName == "executeToolCode" }

func (fakeCodeMode) GetTools() []schemas.ChatTool {
	return []schemas.ChatTool{{Type: "function", Function: &schemas.ChatToolFunction{Name: "executeToolCode"}}}
}

// onClientSlug is a request on name's /mcp/<slug>, as admitBySlug stamps it.
func onClientSlug(name string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{name + "-*"})
	ctx.SetValue(schemas.BifrostContextKeyMCPGatewayClient, name)
	return ctx
}

// setServerTypeOnSlug sets name's server_type_on_slug override.
func setServerTypeOnSlug(m *MCPManager, name string, mode schemas.MCPServerTypeOnSlug) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clientMap[name+"-id"].ExecutionConfig.ServerTypeOnSlug = mode
}

// listedToolNames is what GetAvailableTools lists on ctx, sorted.
func listedToolNames(m *MCPManager, ctx *schemas.BifrostContext) []string {
	names := []string{}
	for _, tool := range m.GetAvailableTools(ctx) {
		names = append(names, tool.Function.Name)
	}
	slices.Sort(names)
	return names
}

// The slug override decides the listing only on the client's own /mcp/<slug>; the gateway's install
// lists what either endpoint may serve.
func TestGetAvailableTools_FollowsServerTypeOnSlug(t *testing.T) {
	plain := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	install := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(WithGatewayTools(context.Background()), schemas.NoDeadline)
	}

	t.Run("code mode client with slug disabled", func(t *testing.T) {
		m := newManagerWithInstructions(t)
		addCodeModeClient(t, m, "codey")
		m.toolsManager.SetCodeMode(fakeCodeMode{})
		setServerTypeOnSlug(m, "codey", schemas.MCPServerTypeOnSlugDisabled)

		assert.Equal(t, []string{"alpha-echo", "beta-echo", "executeToolCode"}, listedToolNames(m, plain()))
		assert.Equal(t, []string{"codey-run"}, listedToolNames(m, onClientSlug("codey")))
		assert.Equal(t, []string{"alpha-echo"}, listedToolNames(m, onClientSlug("alpha")))
		assert.Equal(t, []string{"alpha-echo", "beta-echo", "codey-run", "executeToolCode"}, listedToolNames(m, install()))
	})

	t.Run("direct client with slug enabled", func(t *testing.T) {
		m := newManagerWithInstructions(t)
		m.toolsManager.SetCodeMode(fakeCodeMode{})
		setServerTypeOnSlug(m, "alpha", schemas.MCPServerTypeOnSlugEnabled)

		assert.Equal(t, []string{"alpha-echo", "beta-echo"}, listedToolNames(m, plain()))
		assert.Equal(t, []string{"executeToolCode"}, listedToolNames(m, onClientSlug("alpha")))
		assert.Equal(t, []string{"beta-echo"}, listedToolNames(m, onClientSlug("beta")))
		assert.Equal(t, []string{"alpha-echo", "beta-echo", "executeToolCode"}, listedToolNames(m, install()))
	})

	t.Run("inherit keeps code mode on the slug", func(t *testing.T) {
		m := newManagerWithInstructions(t)
		addCodeModeClient(t, m, "codey")
		m.toolsManager.SetCodeMode(fakeCodeMode{})

		assert.Equal(t, []string{"executeToolCode"}, listedToolNames(m, onClientSlug("codey")))
		assert.Equal(t, []string{"alpha-echo", "beta-echo", "executeToolCode"}, listedToolNames(m, install()))
	})
}

// executeToolCode is callable on a client's slug exactly when that slug lists it.
func TestPrepareToolExecution_CodeModeToolFollowsServerTypeOnSlug(t *testing.T) {
	m := newManagerWithInstructions(t)
	addCodeModeClient(t, m, "codey")
	m.toolsManager.SetCodeMode(fakeCodeMode{})
	name := "executeToolCode"
	request := &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name}},
	}

	setServerTypeOnSlug(m, "codey", schemas.MCPServerTypeOnSlugDisabled)
	_, _, _, err := m.prepareToolExecution(onClientSlug("codey"), request)
	assert.ErrorContains(t, err, "not permitted", "slug disabled: the code-mode tools are not served there")

	setServerTypeOnSlug(m, "alpha", schemas.MCPServerTypeOnSlugEnabled)
	_, _, release, err := m.prepareToolExecution(onClientSlug("alpha"), request)
	require.NoError(t, err, "slug enabled: the code-mode tools are served there")
	release()
}

// A code-mode tool is listed only when the request can reach a code-mode client, so calling it
// by name must be refused under the same rule.
func TestPrepareToolExecution_CodeModeToolNeedsAReachableCodeModeClient(t *testing.T) {
	m := newManagerWithInstructions(t)
	addCodeModeClient(t, m, "codey")
	m.toolsManager.SetCodeMode(fakeCodeMode{})
	name := "executeToolCode"
	request := &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name}},
	}
	for _, tc := range []struct {
		name    string
		include []string
		wantErr bool
	}{
		{"not narrowed", nil, false},
		{"narrowed to the code-mode client", []string{"codey"}, false},
		{"narrowed away from the code-mode client", []string{"alpha"}, true},
		{"no code-mode client at all, not narrowed", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tc.include != nil {
				ctx.SetValue(schemas.MCPContextKeyIncludeClients, tc.include)
			}
			if tc.name == "no code-mode client at all, not narrowed" {
				m.mu.Lock()
				delete(m.clientMap, "codey-id")
				m.mu.Unlock()
			}
			_, _, release, err := m.prepareToolExecution(ctx, request)
			if tc.wantErr {
				assert.ErrorContains(t, err, "not permitted")
				return
			}
			require.NoError(t, err)
			release()
		})
	}
}

func TestCheckToolExecutionPermitted_InjectedToolBypassesFilters(t *testing.T) {
	m := injectedToolTestManager(schemas.MCPConnectionStateHealthy)
	state := m.clientMap["id-tavily"]

	plain := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	assert.ErrorContains(t, checkToolExecutionPermitted(plain, state, "tavily-search", m.logger), "ToolsToExecute",
		"without the injected marker the client's allow-list still applies")

	injected := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	mcpvouch.Set(injected, schemas.MCPToolCallKindInjected, "tavily", "tavily-search")
	injected.SetValue(schemas.MCPContextKeyIncludeClients, []string{"other"})
	injected.SetValue(schemas.MCPContextKeyIncludeTools, []string{"other-tool"})
	assert.NoError(t, checkToolExecutionPermitted(injected, state, "tavily-search", m.logger),
		"the injected tool runs whatever the caller's include lists say")

	other := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	mcpvouch.Set(other, schemas.MCPToolCallKindInjected, "tavily", "tavily-crawl")
	assert.Error(t, checkToolExecutionPermitted(other, state, "tavily-search", m.logger),
		"the marker authorizes exactly one tool, never its siblings")

	wrongClient := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	mcpvouch.Set(wrongClient, schemas.MCPToolCallKindInjected, "exa", "tavily-search")
	assert.Error(t, checkToolExecutionPermitted(wrongClient, state, "tavily-search", m.logger),
		"the marker authorizes one client's tool; the executing client must be that client")

	disabled := *state
	disabled.State = schemas.MCPConnectionStateDisabled
	assert.ErrorContains(t, checkToolExecutionPermitted(injected, &disabled, "tavily-search", m.logger), "disabled",
		"a disabled client stays off even for injected tools")
}

// A plugin holds the request context and can lift BlockRestrictedWrites, so a marker
// reachable through public SetValue is not a trusted boundary. Only core may plant it.
func TestCheckToolExecutionPermitted_PluginCannotForgeInjectedMarker(t *testing.T) {
	m := injectedToolTestManager(schemas.MCPConnectionStateHealthy)
	state := m.clientMap["id-tavily"]

	forged := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	forged.BlockRestrictedWrites()
	forged.Root().UnblockRestrictedWrites()
	forged.SetValue(schemas.BifrostContextKey("bifrost-injected-tool-execution"), struct{ ClientName, ToolName string }{ClientName: "tavily", ToolName: "tavily-search"})
	assert.ErrorContains(t, checkToolExecutionPermitted(forged, state, "tavily-search", m.logger), "ToolsToExecute",
		"a marker written through public SetValue must not bypass the client's allow-list")
}

// A plugin running inside a genuine injected execution finds no marker among the user
// values, so writing a look-alike under any of them never retargets it to another tool.
func TestCheckToolExecutionPermitted_PluginCannotRetargetInjectedMarker(t *testing.T) {
	m := injectedToolTestManager(schemas.MCPConnectionStateHealthy)
	state := m.clientMap["id-tavily"]

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	mcpvouch.Set(ctx, schemas.MCPToolCallKindInjected, "tavily", "tavily-crawl")
	for key := range ctx.GetUserValues() {
		if _, isBifrostKey := key.(schemas.BifrostContextKey); !isBifrostKey {
			ctx.SetValue(key, struct{ clientName, toolName string }{"tavily", "tavily-search"})
		}
	}
	assert.ErrorContains(t, checkToolExecutionPermitted(ctx, state, "tavily-search", m.logger), "ToolsToExecute",
		"a look-alike value under the real key must not authorize another tool")
}

// A plugin handling a genuine injected call can copy every user value into another
// context. The copy must not carry the approval to that context's tool call.
func TestCheckToolExecutionPermitted_PluginCannotCopyInjectedMarker(t *testing.T) {
	m := injectedToolTestManager(schemas.MCPConnectionStateHealthy)
	state := m.clientMap["id-tavily"]

	genuine := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	mcpvouch.Set(genuine, schemas.MCPToolCallKindInjected, "tavily", "tavily-search")
	other := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for key, value := range genuine.GetUserValues() {
		other.SetValue(key, value)
	}
	assert.ErrorContains(t, checkToolExecutionPermitted(other, state, "tavily-search", m.logger), "ToolsToExecute",
		"values copied out of an injected execution must not authorize another context")
}

// TestValidateMCPClientConfig_OpenAPI pins the config rules for the openapi
// connection type: a spec source is mandatory, connection_string is not the way
// to name the upstream, base_url must be absolute http(s), and the auth types
// that drive an MCP handshake against a remote server are refused because the
// synthesized server has no such remote.
func TestValidateMCPClientConfig_OpenAPI(t *testing.T) {
	str := func(s string) *string { return &s }
	base := func(mutate func(c *schemas.MCPClientConfig)) *schemas.MCPClientConfig {
		c := &schemas.MCPClientConfig{
			ID:             "openapi-1",
			Name:           "petstore",
			ConnectionType: schemas.MCPConnectionTypeOpenAPI,
			AuthType:       schemas.MCPAuthTypeNone,
			OpenAPIConfig:  &schemas.MCPOpenAPIConfig{Spec: `{"openapi":"3.0.3","info":{"title":"p","version":"1"},"paths":{}}`},
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	for _, tc := range []struct {
		name    string
		cfg     *schemas.MCPClientConfig
		wantErr string
	}{
		{name: "inline spec, no auth", cfg: base(nil)},
		{name: "spec_url only", cfg: base(func(c *schemas.MCPClientConfig) {
			c.OpenAPIConfig = &schemas.MCPOpenAPIConfig{SpecURL: str("https://api.example.com/openapi.json")}
		})},
		{name: "spec_file only", cfg: base(func(c *schemas.MCPClientConfig) {
			c.OpenAPIConfig = &schemas.MCPOpenAPIConfig{SpecFile: str("specs/petstore.yaml")}
		})},
		{name: "headers auth", cfg: base(func(c *schemas.MCPClientConfig) { c.AuthType = schemas.MCPAuthTypeHeaders })},
		{name: "empty auth type", cfg: base(func(c *schemas.MCPClientConfig) { c.AuthType = "" })},
		{name: "per-user headers", cfg: base(func(c *schemas.MCPClientConfig) {
			c.AuthType = schemas.MCPAuthTypePerUserHeaders
			c.PerUserHeaderKeys = []string{"X-User-Token"}
		})},
		{name: "absolute base url", cfg: base(func(c *schemas.MCPClientConfig) { c.OpenAPIConfig.BaseURL = str("https://api.example.com/v1") })},
		{name: "missing openapi_config", cfg: base(func(c *schemas.MCPClientConfig) { c.OpenAPIConfig = nil }), wantErr: "openapi_config is required"},
		{name: "no spec source", cfg: base(func(c *schemas.MCPClientConfig) { c.OpenAPIConfig = &schemas.MCPOpenAPIConfig{} }), wantErr: "one of spec, spec_file or spec_url"},
		{name: "connection_string set", cfg: base(func(c *schemas.MCPClientConfig) { c.ConnectionString = schemas.NewSecretVar("https://api.example.com") }), wantErr: "connection_string must not be set"},
		{name: "relative base url", cfg: base(func(c *schemas.MCPClientConfig) { c.OpenAPIConfig.BaseURL = str("/v1") }), wantErr: "base_url must be an absolute http(s) URL"},
		{name: "non-http base url", cfg: base(func(c *schemas.MCPClientConfig) { c.OpenAPIConfig.BaseURL = str("ftp://api.example.com") }), wantErr: "base_url must be an absolute http(s) URL"},
		{name: "negative response cap", cfg: base(func(c *schemas.MCPClientConfig) { c.OpenAPIConfig.MaxResponseBytes = -1 }), wantErr: "max_response_bytes must not be negative"},
		{name: "oauth refused", cfg: base(func(c *schemas.MCPClientConfig) { c.AuthType = schemas.MCPAuthTypeOauth }), wantErr: `auth_type "oauth" is not supported for openapi`},
		{name: "per-user oauth refused", cfg: base(func(c *schemas.MCPClientConfig) { c.AuthType = schemas.MCPAuthTypePerUserOauth }), wantErr: "not supported for openapi"},
		{name: "token exchange refused", cfg: base(func(c *schemas.MCPClientConfig) {
			c.AuthType = schemas.MCPAuthTypeTokenExchange
			c.TokenExchange = &schemas.MCPTokenExchangeConfig{Audience: "aud", UseIdPCredentials: true}
		}), wantErr: "not supported for openapi"},
		{name: "client credentials refused", cfg: base(func(c *schemas.MCPClientConfig) {
			c.AuthType = schemas.MCPAuthTypeClientCredentials
			c.ClientCredentials = &schemas.MCPClientCredentialsConfig{ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("s")}
		}), wantErr: "not supported for openapi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMCPClientConfig(tc.cfg)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestClientCapabilitiesFor: a client_credentials client declares everything
// every client does plus the OAuth Client Credentials extension, and the shared
// base is left unchanged.
func TestClientCapabilitiesFor(t *testing.T) {
	cc := clientCapabilitiesFor(&schemas.MCPClientConfig{AuthType: schemas.MCPAuthTypeClientCredentials})
	for name, settings := range bifrostClientCapabilities.Extensions {
		require.Equal(t, settings, cc.Extensions[name], "extension %s", name)
	}
	require.Contains(t, cc.Extensions, schemas.MCPOAuthClientCredentialsExtension)
	require.Len(t, cc.Extensions, len(bifrostClientCapabilities.Extensions)+1)
	require.NotContains(t, bifrostClientCapabilities.Extensions, schemas.MCPOAuthClientCredentialsExtension)
	require.Equal(t, bifrostClientCapabilities, clientCapabilitiesFor(&schemas.MCPClientConfig{AuthType: schemas.MCPAuthTypeOauth}))
}

// TestValidateMCPClientConfig_ClientCredentials: the block is required and checked, and STDIO is refused.
func TestValidateMCPClientConfig_ClientCredentials(t *testing.T) {
	config := func(cc *schemas.MCPClientCredentialsConfig) *schemas.MCPClientConfig {
		return &schemas.MCPClientConfig{
			ID: "cc", Name: "cc_client", ConnectionType: schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.NewSecretVar("https://mcp.example.com/mcp"),
			AuthType:         schemas.MCPAuthTypeClientCredentials, ClientCredentials: cc,
		}
	}
	require.NoError(t, validateMCPClientConfig(config(&schemas.MCPClientCredentialsConfig{ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("s")})))
	require.ErrorContains(t, validateMCPClientConfig(config(nil)), "client_credentials is required")
	require.ErrorContains(t, validateMCPClientConfig(config(&schemas.MCPClientCredentialsConfig{ClientID: schemas.NewSecretVar("id")})), "set client_secret or private_key")

	stdio := config(&schemas.MCPClientCredentialsConfig{ClientID: schemas.NewSecretVar("id"), ClientSecret: schemas.NewSecretVar("s")})
	stdio.ConnectionType, stdio.ConnectionString, stdio.StdioConfig = schemas.MCPConnectionTypeSTDIO, nil, &schemas.MCPStdioConfig{Command: "server"}
	require.ErrorContains(t, validateMCPClientConfig(stdio), "not supported for STDIO")
}

// TestValidateMCPClientConfig_AWSSigV4: the block is required and checked, the
// server must be reachable over HTTP, and no header the signer owns may be
// configured or forwarded.
func TestValidateMCPClientConfig_AWSSigV4(t *testing.T) {
	config := func(mutate func(*schemas.MCPClientConfig)) *schemas.MCPClientConfig {
		c := &schemas.MCPClientConfig{
			ID: "sigv4", Name: "sigv4_client", ConnectionType: schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.NewSecretVar("https://gw.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp"),
			AuthType:         schemas.MCPAuthTypeAWSSigV4, AWSSigV4: &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar("us-east-1")},
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	require.NoError(t, validateMCPClientConfig(config(nil)))
	require.NoError(t, validateMCPClientConfig(config(func(c *schemas.MCPClientConfig) { c.ConnectionType = schemas.MCPConnectionTypeSSE })))
	require.NoError(t, validateMCPClientConfig(config(func(c *schemas.MCPClientConfig) {
		c.Headers = map[string]schemas.SecretVar{"X-Api-Version": *schemas.NewSecretVar("2")}
		c.AllowedExtraHeaders = schemas.WhiteList{"x-tenant-id"}
	})))

	for name, tc := range map[string]struct {
		mutate func(*schemas.MCPClientConfig)
		want   string
	}{
		"missing block": {func(c *schemas.MCPClientConfig) { c.AWSSigV4 = nil }, "aws_sigv4 is required for aws_sigv4 auth type in client 'sigv4_client'"},
		"invalid block": {func(c *schemas.MCPClientConfig) { c.AWSSigV4.Service = "s3" }, "aws_sigv4.service must be one of"},
		"stdio": {func(c *schemas.MCPClientConfig) {
			c.ConnectionType, c.ConnectionString, c.StdioConfig = schemas.MCPConnectionTypeSTDIO, nil, &schemas.MCPStdioConfig{Command: "server"}
		}, "aws_sigv4 auth type needs connection_type http, sse or openapi"},
		"oauth config id": {func(c *schemas.MCPClientConfig) { c.OauthConfigID = schemas.Ptr("oauth-1") }, "oauth_config_id must not be set for aws_sigv4 auth type"},
		"authorization header": {func(c *schemas.MCPClientConfig) {
			c.Headers = map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("Bearer x")}
		}, `header "Authorization" is set by the SigV4 signer and cannot be configured in client 'sigv4_client'`},
		"x-amz header": {func(c *schemas.MCPClientConfig) {
			c.Headers = map[string]schemas.SecretVar{"X-Amz-Target": *schemas.NewSecretVar("t")}
		}, `header "X-Amz-Target" is set by the SigV4 signer`},
		"forwarded x-amz header":  {func(c *schemas.MCPClientConfig) { c.AllowedExtraHeaders = schemas.WhiteList{"x-amz-security-token"} }, `allowed_extra_headers cannot include "x-amz-security-token": it is set by the SigV4 signer in client 'sigv4_client'`},
		"forwarded authorization": {func(c *schemas.MCPClientConfig) { c.AllowedExtraHeaders = schemas.WhiteList{"authorization"} }, `allowed_extra_headers cannot include "authorization"`},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, validateMCPClientConfig(config(tc.mutate)), tc.want)
		})
	}
}

// TestValidateMCPClientConfig_STDIONeedsACommand: a STDIO client with an empty or blank command is
// refused. mcp-go starts no process for one and leaves its stdout nil, and its reader goroutine then
// panics on it, which took the whole process down.
func TestValidateMCPClientConfig_STDIONeedsACommand(t *testing.T) {
	for _, command := range []string{"", "   "} {
		cfg := &schemas.MCPClientConfig{
			ID: "stdio", Name: "stdio_client", ConnectionType: schemas.MCPConnectionTypeSTDIO,
			StdioConfig: &schemas.MCPStdioConfig{Command: command},
		}
		require.ErrorContains(t, validateMCPClientConfig(cfg), "command is required", "command %q", command)
	}
}

// TestValidateMCPClientConfig_PerUserDiscovery pins where the flag is valid: on
// a per-user auth type, which has a credential per caller to discover with,
// and not on an openapi client, whose tools come from its document.
func TestValidateMCPClientConfig_PerUserDiscovery(t *testing.T) {
	base := func(authType schemas.MCPAuthType) *schemas.MCPClientConfig {
		config := &schemas.MCPClientConfig{
			ID:               "pudtest",
			Name:             "pudtest",
			ConnectionType:   schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
			AuthType:         authType,
			PerUserDiscovery: schemas.Ptr(true),
		}
		if authType == schemas.MCPAuthTypePerUserHeaders {
			config.PerUserHeaderKeys = []string{"x-api-key"}
		}
		return config
	}

	t.Run("rejected_for_shared_auth_types", func(t *testing.T) {
		for _, authType := range []schemas.MCPAuthType{
			schemas.MCPAuthTypeNone,
			schemas.MCPAuthTypeHeaders,
			schemas.MCPAuthTypeOauth,
		} {
			err := validateMCPClientConfig(base(authType))
			if err == nil {
				t.Errorf("per_user_discovery with auth type %q must be rejected", authType)
			}
		}
	})

	t.Run("rejected_for_openapi", func(t *testing.T) {
		config := base(schemas.MCPAuthTypePerUserHeaders)
		config.ConnectionType = schemas.MCPConnectionTypeOpenAPI
		config.OpenAPIConfig = &schemas.MCPOpenAPIConfig{Spec: `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{}}`}
		assert.ErrorContains(t, ValidatePerUserDiscovery(config), "openapi")
	})

	t.Run("accepted_for_per_user_auth_types", func(t *testing.T) {
		for _, authType := range []schemas.MCPAuthType{
			schemas.MCPAuthTypePerUserOauth,
			schemas.MCPAuthTypePerUserHeaders,
			schemas.MCPAuthTypeTokenExchange,
		} {
			config := base(authType)
			if authType == schemas.MCPAuthTypeTokenExchange {
				config.TokenExchange = &schemas.MCPTokenExchangeConfig{Audience: "api://pudtest", ClientID: schemas.NewSecretVar("exchange-app")}
			}
			assert.NoError(t, validateMCPClientConfig(config), "auth type %q", authType)
		}
	})

	t.Run("flag_off_stays_valid", func(t *testing.T) {
		for _, off := range []*bool{nil, schemas.Ptr(false)} {
			config := base(schemas.MCPAuthTypePerUserHeaders)
			config.PerUserDiscovery = off
			if err := validateMCPClientConfig(config); err != nil {
				t.Errorf("per_user_discovery unset or false must not affect validation, got: %v", err)
			}
		}
	})
}

// benchmarkSharedManager returns a manager with clients shared clients of tools tools each, all
// open to every tool, and a caller context carrying a virtual key the way governance stamps it.
func benchmarkSharedManager(clients, tools int) (*MCPManager, *schemas.BifrostContext) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	for c := 0; c < clients; c++ {
		name := fmt.Sprintf("client%d", c)
		config := &schemas.MCPClientConfig{ID: name + "-id", Name: name, ToolsToExecute: []string{"*"}}
		discovery := &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}}
		for t := 0; t < tools; t++ {
			tool := fmt.Sprintf("tool%d", t)
			prefixed := name + "-" + tool
			discovery.Tools[prefixed] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: prefixed}}
		}
		m.clientMap[config.ID] = &schemas.MCPClientState{Name: name, ExecutionConfig: config, State: schemas.MCPConnectionStateHealthy}
		m.discoveryStore.SetDiscovery(context.Background(), config, discovery)
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-bench")
	return m, ctx
}

// The tool list of a request when every client is shared: the path every request without
// per_user_discovery takes.
func BenchmarkGetToolPerClient(b *testing.B) {
	m, ctx := benchmarkSharedManager(10, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.GetToolPerClient(ctx)
	}
}

// Resolving a shared client's tool for execution.
func BenchmarkGetClientForTool(b *testing.B) {
	m, ctx := benchmarkSharedManager(10, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.GetClientForTool(ctx, "client7-tool13")
	}
}

// TestValidateMCPClientConfig_TokenExchangeIDJAG checks the id_jag profile needs
// Bifrost's client at the upstream authorization server, at config load too,
// and no exchange client of its own: it always exchanges as the SSO app, so the
// form sends neither use_idp_credentials nor client_id.
func TestValidateMCPClientConfig_TokenExchangeIDJAG(t *testing.T) {
	cfg := &schemas.MCPClientConfig{
		ID:               "client-xaa",
		Name:             "linear",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://mcp.linear.app/mcp"),
		AuthType:         schemas.MCPAuthTypeTokenExchange,
		TokenExchange: &schemas.MCPTokenExchangeConfig{
			Audience: "https://linear.app", UseIdPCredentials: true, Profile: schemas.TokenExchangeProfileIDJAG,
		},
	}
	require.ErrorContains(t, validateMCPClientConfig(cfg), "resource_client_id")
	cfg.TokenExchange.ResourceClientID = schemas.NewSecretVar("bifrost-at-linear")
	require.NoError(t, validateMCPClientConfig(cfg))

	cfg.TokenExchange.UseIdPCredentials = false
	require.NoError(t, validateMCPClientConfig(cfg), "an id_jag client needs no use_idp_credentials or client_id")
}

// mcp-go's error for an input request it had no handler for.
func unfulfilledInputError(kind string) error {
	return fmt.Errorf("multi round-trip: %w", fmt.Errorf("fulfilling input request %q: %w", "req-1", errors.New("no "+kind+" handler configured")))
}

// Only mcp-go's own failure to fulfil an input request is explained; an upstream error that merely
// mentions "multi round-trip:" keeps its own reason.
func TestDescribeUnservableInput_OnlyMcpGoInputFailures(t *testing.T) {
	for kind, want := range map[string]string{"elicitation": "input from the user (elicitation)", "sampling": "an LLM completion (sampling)", "roots": "the client's filesystem roots (roots)"} {
		explained, ok := DescribeUnservableInput(unfulfilledInputError(kind), "docs")
		require.True(t, ok, kind)
		assert.Equal(t, fmt.Sprintf("MCP server %q asked for %s, which Bifrost cannot provide", "docs", want), explained)
	}
	explained, ok := DescribeUnservableInput(unfulfilledInputError("future"), "docs")
	require.True(t, ok, "an unknown kind of input request is still one Bifrost cannot provide")
	assert.Contains(t, explained, "asked for input Bifrost cannot provide")

	for _, upstream := range []string{"multi round-trip: rate limited, retry later", "transport error: multi round-trip: connection reset"} {
		_, ok := DescribeUnservableInput(errors.New(upstream), "docs")
		assert.False(t, ok, "an upstream error keeps its own reason: %s", upstream)
	}
}

// DescribeUnservableInput runs only after a tool call failed; successful calls never reach it.
func BenchmarkDescribeUnservableInput(b *testing.B) {
	for name, err := range map[string]error{
		"url elicitation": mcp.URLElicitationRequiredError{Elicitations: []mcp.ElicitationParams{{Message: "Sign in", URL: "https://example.com/auth"}}},
		"input request":   unfulfilledInputError("elicitation"),
		"ordinary error":  errors.New("upstream returned 500: internal error"),
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = DescribeUnservableInput(err, "docs")
			}
		})
	}
}

// A gateway caller's capabilities are declared upstream only where Bifrost can relay them.
func TestRelayableCapabilities(t *testing.T) {
	const ui = schemas.MCPAppsExtension
	elicitation := &mcp.ElicitationCapability{}
	sampling := &mcp.SamplingCapability{}
	cases := []struct {
		name            string
		caller, relayed mcp.ClientCapabilities
		want            mcp.ClientCapabilities
	}{
		{name: "nothing relayed", caller: mcp.ClientCapabilities{Elicitation: elicitation, Sampling: sampling}, want: mcp.ClientCapabilities{}},
		{name: "relayed and declared",
			caller:  mcp.ClientCapabilities{Elicitation: elicitation, Sampling: sampling},
			relayed: mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}},
			want:    mcp.ClientCapabilities{Elicitation: elicitation}},
		{name: "relayed but not declared", relayed: mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}}, want: mcp.ClientCapabilities{}},
		{name: "extensions by name",
			caller:  mcp.ClientCapabilities{Extensions: map[string]any{ui: map[string]any{"mimeTypes": []any{schemas.MCPAppsMimeType}}, "other": map[string]any{}}},
			relayed: mcp.ClientCapabilities{Extensions: map[string]any{ui: map[string]any{}}},
			want:    mcp.ClientCapabilities{Extensions: map[string]any{ui: map[string]any{"mimeTypes": []any{schemas.MCPAppsMimeType}}}}},
		{name: "experimental and tasks are never relayed",
			caller:  mcp.ClientCapabilities{Experimental: map[string]any{"x": true}, Tasks: &mcp.TasksCapability{}},
			relayed: mcp.ClientCapabilities{Experimental: map[string]any{"x": true}, Tasks: &mcp.TasksCapability{}},
			want:    mcp.ClientCapabilities{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, relayableCapabilities(tc.caller, tc.relayed))
		})
	}
}

// A caller can answer an elicitation only in a mode it declared; an empty capability means form only.
func TestCanAnswer_ElicitationModes(t *testing.T) {
	form := mcp.InputRequests{"q": {Method: mcp.MethodElicitationCreate, Elicitation: &mcp.ElicitationParams{Message: "Which environment?"}}}
	url := mcp.InputRequests{"q": {Method: mcp.MethodElicitationCreate, Elicitation: &mcp.ElicitationParams{Mode: mcp.ElicitationModeURL, URL: "https://example.com/connect"}}}
	cases := []struct {
		name     string
		cap      *mcp.ElicitationCapability
		requests mcp.InputRequests
		want     bool
	}{
		{"empty capability answers form", &mcp.ElicitationCapability{}, form, true},
		{"empty capability cannot answer url", &mcp.ElicitationCapability{}, url, false},
		{"form capability cannot answer url", &mcp.ElicitationCapability{Form: &struct{}{}}, url, false},
		{"url capability answers url", &mcp.ElicitationCapability{URL: &struct{}{}}, url, true},
		{"url-only capability cannot answer form", &mcp.ElicitationCapability{URL: &struct{}{}}, form, false},
		{"no capability", nil, form, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, canAnswer(mcp.ClientCapabilities{Elicitation: tc.cap}, tc.requests))
		})
	}
}

// The explanation of a mixed input_required names the same request every time.
func TestDescribeInputRequests_IsStable(t *testing.T) {
	requests := mcp.InputRequests{
		"a_summary": {Method: mcp.MethodSamplingCreateMessage, Sampling: &mcp.CreateMessageParams{MaxTokens: 10}},
		"b_login":   {Method: mcp.MethodElicitationCreate, Elicitation: &mcp.ElicitationParams{Message: "GitHub username?"}},
		"c_roots":   {Method: mcp.MethodListRoots, Roots: &mcp.ListRootsParams{}},
	}
	want := describeInputRequests(requests, "upstream")
	assert.Contains(t, want, "an LLM completion (sampling)", "the first request by id is named")
	for range 50 {
		require.Equal(t, want, describeInputRequests(requests, "upstream"))
	}
}

// MCP Apps fields are read from the tool's _meta; no visibility means model and app.
func TestToolMetadata_MCPAppsFields(t *testing.T) {
	withMeta := func(fields map[string]any) *mcp.Tool {
		tool := mcp.NewTool("t")
		tool.Meta = &mcp.Meta{AdditionalFields: fields}
		return &tool
	}
	cases := []struct {
		name         string
		tool         *mcp.Tool
		wantURI      string
		wantVisible  []string
		model, app   bool
		wantMetaJSON string
	}{
		{name: "resource and visibility", tool: withMeta(map[string]any{"ui": map[string]any{"resourceUri": "ui://charts/view", "visibility": []any{"app"}}}),
			wantURI: "ui://charts/view", wantVisible: []string{"app"}, model: false, app: true,
			wantMetaJSON: `{"ui":{"resourceUri":"ui://charts/view","visibility":["app"]}}`},
		{name: "deprecated resourceUri key", tool: withMeta(map[string]any{"ui/resourceUri": "ui://charts/view"}),
			wantURI: "ui://charts/view", model: true, app: true, wantMetaJSON: `{"ui/resourceUri":"ui://charts/view"}`},
		{name: "non-ui URI is not an app resource", tool: withMeta(map[string]any{"ui": map[string]any{"resourceUri": "https://example.com/view"}}),
			model: true, app: true, wantMetaJSON: `{"ui":{"resourceUri":"https://example.com/view"}}`},
		{name: "model only", tool: withMeta(map[string]any{"ui": map[string]any{"visibility": []any{"model"}}}),
			wantVisible: []string{"model"}, model: true, app: false, wantMetaJSON: `{"ui":{"visibility":["model"]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolMetadata(tc.tool)
			require.NotNil(t, got)
			assert.Equal(t, tc.wantURI, got.UIResourceURI)
			assert.Equal(t, tc.wantVisible, got.Visibility)
			assert.Equal(t, tc.model, got.ModelVisible())
			assert.Equal(t, tc.app, got.AppVisible())
			assert.JSONEq(t, tc.wantMetaJSON, string(got.Meta))
		})
	}

	plain := mcp.NewTool("plain")
	got := toolMetadata(&plain)
	assert.Nil(t, got, "a tool with no metadata keeps none")
	assert.True(t, got.ModelVisible())
	assert.True(t, got.AppVisible())
}

// A client name with "/", "?" or "#" stays one path segment of a namespaced ui:// URI, so two clients
// never share a prefix and the URI keeps its path.
func TestGatewayUIResourceURI_EscapesClientName(t *testing.T) {
	cases := []struct{ client, uri, want string }{
		{"apps", "ui://dash/view", "ui://apps/dash/view"},
		{"a/b", "ui://x", "ui://a%2Fb/x"},
		{"q?a#b", "ui://x", "ui://q%3Fa%23b/x"},
		{"apps", "https://example.com/x", "https://example.com/x"},
	}
	for _, tc := range cases {
		got := GatewayUIResourceURI(tc.client, tc.uri)
		assert.Equal(t, tc.want, got, tc.client)
		back, ok := upstreamUIResourceURI(tc.client, got)
		assert.Equal(t, tc.uri, back, tc.client)
		assert.Equal(t, got != tc.uri, ok, tc.client)
	}
	_, ok := upstreamUIResourceURI("a", GatewayUIResourceURI("a/b", "ui://x"))
	assert.False(t, ok, "client a must not own client a/b's resources")
}

func TestToolClientName(t *testing.T) {
	assert.Equal(t, "apps", ToolClientName("apps-view"))
	assert.Equal(t, "apps", ToolClientName("apps-my-tool"))
	assert.Equal(t, "", ToolClientName("executeToolCode"), "an unprefixed tool has no client")
	assert.Equal(t, "", ToolClientName("-x"))
}

// TestRoundTripState: Bifrost's wrap of a requestState round-trips, keeps one id across a call's rounds,
// and leaves a state it did not wrap alone.
func TestRoundTripState(t *testing.T) {
	id := RoundTripID("req-1")
	assert.Len(t, id, 16)
	assert.NotEqual(t, id, RoundTripID("req-2"), "separate calls get separate ids")

	for _, upstream := range []string{"busy", "", "a.b.c", "bfrt1.0123456789abcdef.inner"} {
		gotID, gotState := UnwrapRoundTripState(WrapRoundTripState(id, upstream))
		assert.Equal(t, id, gotID)
		assert.Equal(t, upstream, gotState, "the upstream's own state comes back unchanged")
	}
	for _, foreign := range []string{"", "busy", "bfrt1.", "bfrt1.0123456789abcdef", "bfrt1.0123456789ABCDEF.x", "bfrt1.0123456789abcdeg.x", "bfrt1.short.x"} {
		gotID, gotState := UnwrapRoundTripState(foreign)
		assert.Empty(t, gotID, foreign)
		assert.Equal(t, foreign, gotState, foreign)
	}

	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-2")
	assert.Equal(t, RoundTripID("req-2"), CallRoundTripID(ctx), "a first round's id comes from its request")
	assert.Equal(t, id, CallRoundTripID(context.WithValue(ctx, schemas.BifrostContextKeyMCPRoundTripID, id)), "a retry keeps the id it carried")
}

// A call asks the upstream for progress only when ctx carries a reporter, under a fresh token whose
// progress reaches that reporter until the call ends, keeping any other _meta the call had.
func TestWithProgressToken(t *testing.T) {
	call := mcp.CallToolRequest{}
	call.Params.Meta = &mcp.Meta{AdditionalFields: map[string]any{"trace": "t1"}}

	unchanged, stop := withProgressToken(context.Background(), call)
	stop()
	assert.Same(t, call.Params.Meta, unchanged.Params.Meta)

	type step struct {
		progress float64
		total    *float64
		message  string
	}
	var reported []step
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPProgressReporter,
		ProgressReporter(func(_ context.Context, progress float64, total *float64, message string) {
			reported = append(reported, step{progress, total, message})
		}))
	sent, stop := withProgressToken(ctx, call)
	token, ok := sent.Params.Meta.ProgressToken.(string)
	require.True(t, ok)
	assert.NotEmpty(t, token)
	assert.Equal(t, "t1", sent.Params.Meta.AdditionalFields["trace"])
	assert.Nil(t, call.Params.Meta.ProgressToken, "the caller's request is not modified")

	total := float64(2)
	deliverProgress(map[string]any{"progressToken": token, "progress": float64(1), "total": total, "message": "half"})
	deliverProgress(map[string]any{"progressToken": token, "progress": float64(2)})
	deliverProgress(map[string]any{"progressToken": "another-call", "progress": float64(1)})
	deliverProgress(map[string]any{"progressToken": token, "progress": "1"})
	// An in-process server hands its notification over without a JSON round trip, so numbers keep their Go type.
	four := float64(4)
	deliverProgress(map[string]any{"progressToken": token, "progress": 3, "total": int64(4)})
	deliverProgress(map[string]any{"progressToken": token, "progress": json.Number("4")})
	assert.Equal(t, []step{{1, &total, "half"}, {2, nil, ""}, {3, &four, ""}, {4, nil, ""}}, reported, "only this call's well-formed progress is reported")

	stop()
	deliverProgress(map[string]any{"progressToken": token, "progress": float64(5)})
	assert.Len(t, reported, 4, "nothing is reported once the call has ended")
	_, ok = progressRoutes.Load(token)
	assert.False(t, ok)
}

// A reporter stuck on a caller that stopped reading does not hold up the end of the call: stopping
// cancels the delivery in flight and waits only for it to return.
func TestWithProgressToken_StopUnblocksDelivery(t *testing.T) {
	entered := make(chan struct{})
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPProgressReporter,
		ProgressReporter(func(ctx context.Context, _ float64, _ *float64, _ string) {
			close(entered)
			<-ctx.Done()
		}))
	sent, stop := withProgressToken(ctx, mcp.CallToolRequest{})
	token := sent.Params.Meta.ProgressToken.(string)
	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		deliverProgress(map[string]any{"progressToken": token, "progress": float64(1)})
	}()
	<-entered

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop waited on a delivery that cannot finish")
	}
	<-delivered
}

// An in-process upstream's progress reaches the caller: its notifications skip JSON, so progress and
// total arrive as Go ints rather than float64s.
func TestInProcessUpstreamProgressIsRelayed(t *testing.T) {
	upstream := server.NewMCPServer("inproc", "1.0.0", server.WithToolCapabilities(true))
	upstream.AddTool(mcp.NewTool("work"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Params.Meta == nil || req.Params.Meta.ProgressToken == nil {
			return mcp.NewToolResultText("no token"), nil
		}
		params := map[string]any{"progressToken": req.Params.Meta.ProgressToken, "progress": 1, "total": 2}
		if err := server.ServerFromContext(ctx).SendNotificationToClient(ctx, string(mcp.MethodNotificationProgress), params); err != nil {
			return nil, err
		}
		// The in-process transport forwards notifications on their own goroutine, so a result sent at once
		// can overtake them; the work after the step leaves it time to go out first.
		time.Sleep(50 * time.Millisecond)
		return mcp.NewToolResultText("done"), nil
	})
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	require.NoError(t, m.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID: "inproc", Name: "inproc", ConnectionType: schemas.MCPConnectionTypeInProcess, InProcessServer: upstream, ToolsToExecute: []string{"*"},
	}))

	type step struct {
		progress, total float64
	}
	var mu sync.Mutex
	var reported []step
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPProgressReporter,
		ProgressReporter(func(_ context.Context, progress float64, total *float64, _ string) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, step{progress, *total})
		}))
	name, callType, id := "inproc-work", "function", "call-1"
	result, bErr := m.ExecuteChatTool(schemas.NewBifrostContext(ctx, schemas.NoDeadline), &schemas.ChatAssistantMessageToolCall{
		ID: &id, Type: &callType, Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: "{}"},
	})
	require.Nil(t, bErr)
	require.NotNil(t, result)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []step{{1, 2}}, reported)
}

// progressNumber reads every Go number an in-process server may send, named types included, as both
// progress and total, and nothing that is not a number.
func TestProgressNumber(t *testing.T) {
	type percent uint8
	numbers := map[string]any{
		"float64": float64(3), "float32": float32(3), "int": 3, "int8": int8(3), "int16": int16(3), "int32": int32(3),
		"int64": int64(3), "uint": uint(3), "uint8": uint8(3), "uint16": uint16(3), "uint32": uint32(3), "uint64": uint64(3),
		"uintptr": uintptr(3), "named": percent(3), "json.Number": json.Number("3"),
	}
	type step struct{ progress, total float64 }
	var reported []step
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyMCPProgressReporter,
		ProgressReporter(func(_ context.Context, progress float64, total *float64, _ string) {
			require.NotNil(t, total, "total was dropped")
			reported = append(reported, step{progress, *total})
		}))
	sent, stop := withProgressToken(ctx, mcp.CallToolRequest{})
	t.Cleanup(stop)
	token := sent.Params.Meta.ProgressToken.(string)
	for name, v := range numbers {
		t.Run(name, func(t *testing.T) {
			got, ok := progressNumber(v)
			assert.True(t, ok)
			assert.Equal(t, float64(3), got)
			reported = nil
			deliverProgress(map[string]any{"progressToken": token, "progress": v, "total": v})
			assert.Equal(t, []step{{3, 3}}, reported, "as progress and as total")
		})
	}
	for name, v := range map[string]any{"string": "3", "nil": nil, "bool": true, "malformed json.Number": json.Number("x")} {
		t.Run("not a number: "+name, func(t *testing.T) {
			_, ok := progressNumber(v)
			assert.False(t, ok)
		})
	}
}
