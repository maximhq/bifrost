package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaOrderInputSchema has non-alphabetical property names, nested object
// properties, several $defs entries, and an array property (which
// FixArraySchemas touches). mcp-go decodes "properties" and "$defs" into Go
// maps, so the server's key order is gone by the time Bifrost converts the tool.
const schemaOrderInputSchema = `{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "search query"},
		"numResults": {"type": "number"},
		"objective": {"type": "string"},
		"filters": {
			"type": "object",
			"properties": {"zeta": {"type": "string"}, "alpha": {"type": "string"}, "mid": {"type": "boolean"}}
		},
		"urls": {"type": "array", "items": {"type": "string"}},
		"preferences": {"$ref": "#/$defs/Preferences"}
	},
	"required": ["query"],
	"$defs": {
		"Preferences": {"type": "object", "properties": {"startHour": {"type": "string"}, "endHour": {"type": "string"}}},
		"Actor": {"type": "string"},
		"Mode": {"type": "string", "enum": ["fast", "deep"]}
	}
}`

// schemaOrderWideInputSchema has more than 8 properties. Go iterates small maps
// (up to 8 entries) as a rotation of insertion order, and larger maps in a
// much more scrambled order, so both sizes are covered.
const schemaOrderWideInputSchema = `{
	"type": "object",
	"properties": {
		"p12": {"type": "string"}, "p11": {"type": "string"}, "p10": {"type": "string"},
		"p09": {"type": "string"}, "p08": {"type": "string"}, "p07": {"type": "string"},
		"p06": {"type": "string"}, "p05": {"type": "string"}, "p04": {"type": "string"},
		"p03": {"type": "string"}, "p02": {"type": "string"}, "p01": {"type": "string"}
	}
}`

func decodeSchemaOrderTool(t *testing.T, name, inputSchema string) mcpgo.Tool {
	t.Helper()
	var tool mcpgo.Tool
	raw := `{"name":"` + name + `","description":"d","inputSchema":` + inputSchema + `}`
	require.NoError(t, json.Unmarshal([]byte(raw), &tool))
	return tool
}

// TestConvertMCPToolToBifrostSchema_PropertyOrderIsDeterministic converts a
// freshly decoded tool many times, as the connection checker does on every
// tick. Every conversion must serialize to the same bytes, or providers with
// prefix caching lose their cache whenever the order changes.
func TestConvertMCPToolToBifrostSchema_PropertyOrderIsDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name        string
		inputSchema string
	}{
		{"nested_and_defs", schemaOrderInputSchema},
		{"wide", schemaOrderWideInputSchema},
		{"empty", `{"type":"object","properties":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wantParams, wantNormalized, wantTool []byte
			for i := 0; i < 200; i++ {
				mcpTool := decodeSchemaOrderTool(t, "search", tc.inputSchema)
				chatTool := convertMCPToolToBifrostSchema(&mcpTool, defaultLogger)
				require.NotNil(t, chatTool.Function)
				params := chatTool.Function.Parameters
				require.NotNil(t, params)

				gotParams, err := json.Marshal(params)
				require.NoError(t, err)
				gotNormalized, err := json.Marshal(params.Normalized())
				require.NoError(t, err)
				gotTool, err := json.Marshal(chatTool)
				require.NoError(t, err)

				if i == 0 {
					wantParams, wantNormalized, wantTool = gotParams, gotNormalized, gotTool
					continue
				}
				require.Equal(t, string(wantParams), string(gotParams), "parameters changed on conversion %d", i)
				require.Equal(t, string(wantNormalized), string(gotNormalized), "normalized parameters changed on conversion %d", i)
				require.Equal(t, string(wantTool), string(gotTool), "tool JSON changed on conversion %d", i)
			}
		})
	}
}

// TestConvertMCPToolToBifrostSchema_SortsPropertiesAndDefs pins the canonical
// order: mcp-go does not keep the server's order, so keys are sorted.
func TestConvertMCPToolToBifrostSchema_SortsPropertiesAndDefs(t *testing.T) {
	mcpTool := decodeSchemaOrderTool(t, "search", schemaOrderInputSchema)
	params := convertMCPToolToBifrostSchema(&mcpTool, defaultLogger).Function.Parameters

	propKeys := params.Properties.Keys()
	assert.True(t, slices.IsSorted(propKeys), "properties keys not sorted: %v", propKeys)
	assert.Len(t, propKeys, 6)

	require.NotNil(t, params.Defs)
	defKeys := params.Defs.Keys()
	assert.True(t, slices.IsSorted(defKeys), "$defs keys not sorted: %v", defKeys)
	assert.Equal(t, []string{"Actor", "Mode", "Preferences"}, defKeys)

	data, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"filters":{"properties":{"alpha":{"type":"string"},"mid":{"type":"boolean"},"zeta":{"type":"string"}},"type":"object"}`,
		"nested schema maps serialize with sorted keys")
}

// TestComputeToolsHash_StableAcrossRediscovery: the tools-changed callback
// (DB persist, local MCP server resync) is gated on computeToolsHash, which
// hashes the serialized tools. Rediscovering the same server tools must hash
// the same.
func TestComputeToolsHash_StableAcrossRediscovery(t *testing.T) {
	mapping := map[string]string{"search": "search"}
	var want string
	for i := 0; i < 200; i++ {
		mcpTool := decodeSchemaOrderTool(t, "search", schemaOrderInputSchema)
		tools := map[string]schemas.ChatTool{"search": convertMCPToolToBifrostSchema(&mcpTool, defaultLogger)}
		got := computeToolsHash(tools, mapping, "")
		if i == 0 {
			want = got
			continue
		}
		require.Equal(t, want, got, "tools hash changed on rediscovery %d", i)
	}
}

// TestPerformCheck_RepeatedSyncs_ToolSchemaBytesStable drives the periodic
// checker against a real streamable-HTTP MCP server that returns a fixed,
// non-alphabetical schema. Across many ticks the stored tool must serialize to
// the same bytes and the tools-changed callback must fire only for the first
// discovery.
func TestPerformCheck_RepeatedSyncs_ToolSchemaBytesStable(t *testing.T) {
	s := server.NewMCPServer("test-schema-order", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(
		mcpgo.NewToolWithRawSchema("search", "search tool", json.RawMessage(schemaOrderInputSchema)),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
	)
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)

	manager := &MCPManager{
		credStore: &fakeAdminCredStore{headers: http.Header{"Authorization": []string{"Bearer shared-token"}}},
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}
	state, config := newSyncTestClientState("client-order", "orderclient", schemas.MCPAuthTypeHeaders, map[string]schemas.ChatTool{})
	config.ConnectionType = schemas.MCPConnectionTypeHTTP
	config.ConnectionString = schemas.NewSecretVar(ts.URL)
	manager.clientMap[config.ID] = state

	callCount := 0
	manager.SetToolsChangeCallback(func(string, string, map[string]schemas.ChatTool, map[string]string, string) {
		callCount++
	})

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	var want string
	for i := 0; i < 40; i++ {
		checker.performCheck()
		manager.mu.RLock()
		tool, ok := manager.clientMap[config.ID].ToolMap["orderclient-search"]
		manager.mu.RUnlock()
		require.True(t, ok, "tick %d did not store the tool", i)
		data, err := json.Marshal(tool)
		require.NoError(t, err)
		if i == 0 {
			want = string(data)
			continue
		}
		require.Equal(t, want, string(data), "stored tool JSON changed on tick %d", i)
	}
	assert.Equal(t, 1, callCount, "only the first discovery is a tools change")
}

func TestMCPSchemaPreservationReconnectAndRefresh(t *testing.T) {
	upstream := server.NewMCPServer("schema-refresh", "1", server.WithToolCapabilities(true))
	setTool := func(input, output string, readOnly bool) {
		tool := mcpgo.NewToolWithRawSchema("edge", "edge", json.RawMessage(input))
		if output != "" {
			tool.RawOutputSchema = json.RawMessage(output)
		}
		tool.Annotations.ReadOnlyHint = schemas.Ptr(readOnly)
		upstream.AddTool(tool, func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			t.Fatal("tools/call prohibited")
			return nil, nil
		})
	}
	input := `{"type":"object","additionalProperties":false,"x-input":1}`
	setTool(input, `{"type":"object","additionalProperties":false,"x-output":1}`, true)
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	defer ts.Close()
	manager := NewMCPManager(context.Background(), schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, &MockLogger{}, nil)
	defer manager.Cleanup()
	changes := 0
	manager.SetToolsChangeCallback(func(string, string, map[string]schemas.ChatTool, map[string]string, string) { changes++ })
	config := &schemas.MCPClientConfig{ID: "refresh", Name: "refresh", NeedsSessionStickiness: schemas.Ptr(true), ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar(ts.URL), AuthType: schemas.MCPAuthTypeNone, ToolsToExecute: schemas.WhiteList{"*"}, ToolSyncInterval: time.Hour}
	require.NoError(t, manager.AddClient(context.Background(), config))
	require.Equal(t, 1, changes)
	checker := NewClientConnectionChecker(manager, config.ID, time.Hour, true, &MockLogger{})
	checker.performCheck()
	require.Equal(t, 1, changes)
	// Key reordering is semantically identical and must not change the hash.
	setTool(`{"x-input":1,"additionalProperties":false,"type":"object"}`, `{"x-output":1,"additionalProperties":false,"type":"object"}`, true)
	checker.performCheck()
	require.Equal(t, 1, changes)
	require.NoError(t, manager.ReconnectClient(config.ID))
	require.Equal(t, 1, changes)
	setTool(input, "{}", true)
	checker.performCheck()
	require.Equal(t, 2, changes)
	state := manager.GetClientByName(config.Name)
	require.Equal(t, "{}", string(state.ToolMap["refresh-edge"].MCPToolSchema.OutputSchema))
	require.NoError(t, manager.ReconnectClient(config.ID))
	require.Equal(t, 2, changes)
	setTool(input, "", true)
	checker.performCheck()
	require.Equal(t, 3, changes)
	state = manager.GetClientByName(config.Name)
	require.Nil(t, state.ToolMap["refresh-edge"].MCPToolSchema.OutputSchema)
	// Annotation-only changes also affect the downstream contract.
	setTool(input, "", false)
	checker.performCheck()
	require.Equal(t, 4, changes)
	require.NoError(t, manager.ReconnectClient(config.ID))
	require.Equal(t, 4, changes)
}
