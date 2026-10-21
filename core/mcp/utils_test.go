package mcp

import (
	"context"
	"encoding/json"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/internal/injectedauth"
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

func TestConvertMCPToolToBifrostSchema_MetadataIsolation(t *testing.T) {
	var upstream discoveredMCPTool
	require.NoError(t, json.Unmarshal([]byte(`{"name":"edge","inputSchema":{"type":"object","additionalProperties":false,"properties":{"a":{"type":"array"}},"x-input":true},"outputSchema":{},"annotations":{"readOnlyHint":true}}`), &upstream))
	tool := convertMCPToolToBifrostSchema(&upstream.Tool, defaultLogger)
	require.JSONEq(t, `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"array"}},"x-input":true}`, string(tool.MCPToolSchema.InputSchema))
	require.Equal(t, "{}", string(tool.MCPToolSchema.OutputSchema))
	require.NotContains(t, upstream.InputSchema.Properties["a"].(map[string]any), "items", "provider normalization must not mutate upstream")
	*upstream.Annotations.ReadOnlyHint = false
	upstream.RawInputSchema[0] = '['
	require.True(t, *tool.Annotations.ReadOnlyHint)
	require.Equal(t, byte('{'), tool.MCPToolSchema.InputSchema[0])
	wire, err := json.Marshal(tool)
	require.NoError(t, err)
	require.NotContains(t, string(wire), "MCPToolSchema")
	require.NotContains(t, string(wire), "outputSchema")
	require.NotContains(t, string(wire), "x-input")
	require.NotContains(t, string(wire), "annotations")
	// The provider-facing contract still uses the old projection and array fix.
	require.JSONEq(t, `{"type":"object","properties":{"a":{"type":"array","items":{}}}}`, mustJSONParams(t, tool.Function.Parameters))
}
func mustJSONParams(t *testing.T, p *schemas.ToolFunctionParameters) string {
	t.Helper()
	b, err := json.Marshal(p)
	require.NoError(t, err)
	return string(b)
}

// Scripted transport preserves the SDK connection seam without any network.
type schemaListTransport struct {
	fakeCallToolTransport
	results []json.RawMessage
	ids     map[string]bool
}

func (s *schemaListTransport) SendRequest(_ context.Context, r transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	if r.Method != "tools/list" {
		panic("unexpected request")
	}
	if s.ids == nil {
		s.ids = map[string]bool{}
	}
	id := r.ID.String()
	if s.ids[id] {
		panic("duplicate request id")
	}
	s.ids[id] = true
	data := s.results[0]
	if len(s.results) > 1 {
		s.results = s.results[1:]
	}
	return &transport.JSONRPCResponse{JSONRPC: "2.0", ID: r.ID, Result: data}, nil
}
func TestMCPSchemaPreservationRejectsMalformedPages(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []json.RawMessage
		want    string
	}{
		{"null_schema", []json.RawMessage{[]byte(`{"tools":[{"name":"t","inputSchema":null}]}`)}, "schema must be an object"},
		{"malformed_json", []json.RawMessage{[]byte(`{"tools":`)}, "failed to unmarshal"},
		{"cursor_loop", []json.RawMessage{[]byte(`{"tools":[],"nextCursor":"again"}`)}, "repeated tools/list cursor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &schemaListTransport{results: tc.results}
			c := client.NewClient(tr, client.WithSession())
			_, err := listToolsPreservingSchemas(context.Background(), c, mcp.ListToolsRequest{})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestMCPSchemaPreservationBooleanSchemas(t *testing.T) {
	for _, raw := range []string{
		`{"name":"boolean","inputSchema":true,"outputSchema":false}`,
		`{"name":"boolean","inputSchema":false,"outputSchema":true}`,
		`{"name":"union","inputSchema":{"type":["object","null"],"if":{"properties":{"x":false}},"then":{"unevaluatedProperties":false},"else":true,"dependentSchemas":{"x":{"required":["y"]}}},"outputSchema":false}`,
		`{"name":"boolean","inputSchema":{"type":"object","properties":{"enabled":false},"additionalProperties":true},"outputSchema":{}}`,
	} {
		var tool discoveredMCPTool
		require.NoError(t, json.Unmarshal([]byte(raw), &tool))
		var wire map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(raw), &wire))
		got := convertMCPToolToBifrostSchema(&tool.Tool, defaultLogger)
		require.JSONEq(t, string(wire["inputSchema"]), string(got.MCPToolSchema.InputSchema))
		require.JSONEq(t, string(wire["outputSchema"]), string(got.MCPToolSchema.OutputSchema))
		b, err := json.Marshal(got)
		require.NoError(t, err)
		require.NotContains(t, string(b), "outputSchema")
	}
}

// TestMCPSchemaProviderProjectionDefaults checks that richer raw schemas never
// produce an empty provider type, without changing the downstream MCP contract.
func TestMCPSchemaProviderProjectionDefaults(t *testing.T) {
	for _, input := range []string{`true`, `false`, `{}`, `{"properties":{"x":{"type":"string"}}}`, `{"type":["object","null"]}`, `{"type":"object","required":false}`} {
		t.Run(input, func(t *testing.T) {
			var tool discoveredMCPTool
			require.NoError(t, json.Unmarshal([]byte(`{"name":"projection","inputSchema":`+input+`}`), &tool))
			got := convertMCPToolToBifrostSchema(&tool.Tool, defaultLogger)
			require.Equal(t, "object", got.Function.Parameters.Type)
			require.JSONEq(t, input, string(got.MCPToolSchema.InputSchema))
			wire, err := json.Marshal(got)
			require.NoError(t, err)
			require.Contains(t, string(wire), `"type":"object"`)
			require.NotContains(t, string(wire), `"type":""`)
		})
	}
	var missing discoveredMCPTool
	require.NoError(t, json.Unmarshal([]byte(`{"name":"missing"}`), &missing))
	require.Empty(t, missing.InputSchema.Type)
	require.Nil(t, missing.RawInputSchema)
}

func injectedToolTestManager(state schemas.MCPConnectionState) *MCPManager {
	return &MCPManager{
		logger: &MockLogger{},
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
				ToolMap: map[string]schemas.ChatTool{
					"tavily-search": {
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:        "tavily-search",
							Description: schemas.Ptr("Search the web"),
							Parameters:  &schemas.ToolFunctionParameters{Type: "object", Required: []string{"query"}},
						},
					},
				},
			},
		},
	}
}

func TestGetInjectedTool(t *testing.T) {
	m := injectedToolTestManager(schemas.MCPConnectionStateHealthy)

	tool, err := m.GetInjectedTool("tavily", "search")
	require.NoError(t, err, "injected tools ignore tools_to_execute: the provider config is the authorization")
	require.NotNil(t, tool.Function)
	assert.Equal(t, "tavily-search", tool.Function.Name)

	tool.Function.Name = "mutated"
	tool.Function.Parameters.Required[0] = "mutated"
	tool.Function.Parameters.Type = "mutated"
	again, err := m.GetInjectedTool("tavily", "search")
	require.NoError(t, err)
	assert.Equal(t, "tavily-search", again.Function.Name, "the returned tool must be a copy, never the client's ToolMap entry")
	assert.Equal(t, "object", again.Function.Parameters.Type, "the parameter schema is copied too")
	assert.Equal(t, []string{"query"}, again.Function.Parameters.Required, "nested schema slices are copied too")

	_, err = m.GetInjectedTool("exa", "search")
	assert.ErrorContains(t, err, "not found")
	_, err = m.GetInjectedTool("tavily", "crawl")
	assert.ErrorContains(t, err, "does not expose")

	_, err = injectedToolTestManager(schemas.MCPConnectionStateDisabled).GetInjectedTool("tavily", "search")
	assert.ErrorContains(t, err, "disabled")
}

func TestCheckToolExecutionPermitted_InjectedToolBypassesFilters(t *testing.T) {
	m := injectedToolTestManager(schemas.MCPConnectionStateHealthy)
	state := m.clientMap["id-tavily"]

	plain := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	assert.ErrorContains(t, checkToolExecutionPermitted(plain, state, "tavily-search", m.logger), "ToolsToExecute",
		"without the injected marker the client's allow-list still applies")

	injected := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	injectedauth.Set(injected, "tavily", "tavily-search")
	injected.SetValue(schemas.MCPContextKeyIncludeClients, []string{"other"})
	injected.SetValue(schemas.MCPContextKeyIncludeTools, []string{"other-tool"})
	assert.NoError(t, checkToolExecutionPermitted(injected, state, "tavily-search", m.logger),
		"the injected tool runs whatever the caller's include lists say")

	other := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	injectedauth.Set(other, "tavily", "tavily-crawl")
	assert.Error(t, checkToolExecutionPermitted(other, state, "tavily-search", m.logger),
		"the marker authorizes exactly one tool, never its siblings")

	wrongClient := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	injectedauth.Set(wrongClient, "exa", "tavily-search")
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
	injectedauth.Set(ctx, "tavily", "tavily-crawl")
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
	injectedauth.Set(genuine, "tavily", "tavily-search")
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
