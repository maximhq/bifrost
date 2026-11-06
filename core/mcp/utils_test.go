package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
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
