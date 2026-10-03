package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Required: []string{"preferences"},
		Defs: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"startHour": map[string]any{"type": "string"},
				},
			}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
	assert.Contains(t, string(data), `"$ref":"#/$defs/Preferences"`)
}

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesLegacyDefinitionsAsDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Definitions: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{"type": "object"}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
}

// schemaGatewayManager adapts the actual discovery/cache manager to the transport seam.
// Execution deliberately panics: this regression only sends initialize/tools/list.
type schemaGatewayManager struct{ manager *coremcp.MCPManager }

func (g *schemaGatewayManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	return g.manager.GetAvailableTools(schemas.NewBifrostContext(ctx, schemas.NoDeadline))
}
func (*schemaGatewayManager) ExecuteChatMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	panic("tools/call prohibited")
}
func (*schemaGatewayManager) ExecuteResponsesMCPTool(context.Context, *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	panic("tools/call prohibited")
}

type schemaGatewayAdmitter struct{ grants map[string]schemas.Access }

func (a *schemaGatewayAdmitter) AdmitMCPGatewayRequest(ctx *schemas.BifrostContext) (schemas.Access, *schemas.BifrostError) {
	access, ok := a.grants[stringFromCtx(ctx, schemas.BifrostContextKeyVirtualKey)]
	if !ok {
		return nil, &schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "unknown test VK"}}
	}
	return access, nil
}

func TestMCPSchemaPreservationEndToEnd(t *testing.T) {
	testMCPSchemaPreservationEndToEnd(t, "")
}

func TestMCPSchemaPreservationPersistenceEndToEnd(t *testing.T) {
	for _, mode := range []string{"create", "config-update", "tools-update"} {
		t.Run(mode, func(t *testing.T) { testMCPSchemaPreservationEndToEnd(t, mode) })
	}
}

func testMCPSchemaPreservationEndToEnd(t *testing.T, persistenceMode string) {
	SetLogger(&mockLogger{})
	var err error
	fixture := struct{ Tools []map[string]any }{Tools: []map[string]any{
		{"name": "lookup", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"query": map[string]any{"type": "string", "pattern": "^[a-z]+$", "minLength": 1}}, "required": []string{"query"}, "x-contract": "synthetic"}, "outputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}, "annotations": map[string]any{"title": "Lookup", "readOnlyHint": true, "destructiveHint": false}},
	}}
	// Synthetic siblings pin composition, refs, large numeric bounds and absent vs {}.
	var edge map[string]any
	// Decode numbers exactly for the numeric precision assertion below.
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"name":"schema_edge","inputSchema":{"type":"object","additionalProperties":false,"maxProperties":2,"$defs":{"N":{"type":"integer","maximum":9007199254740993}},"properties":{"n":{"$ref":"#/$defs/N"},"a":{"type":"array"}},"anyOf":[{"required":["n"]},{"required":["a"]}],"oneOf":[{"maxProperties":1},{"minProperties":2}],"x-extension":{"one":1}},"outputSchema":{},"annotations":{"readOnlyHint":true}}`)))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&edge))
	fixture.Tools = append(fixture.Tools, map[string]any{"name": "boolean_true", "inputSchema": true, "outputSchema": false, "annotations": map[string]any{"readOnlyHint": true}}, map[string]any{"name": "boolean_false", "inputSchema": false, "outputSchema": true, "annotations": map[string]any{"readOnlyHint": true}}, edge, map[string]any{"name": "schema_absent", "inputSchema": map[string]any{}, "annotations": map[string]any{"readOnlyHint": true}})
	var mu sync.Mutex
	calls := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(405)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		defer mu.Unlock()
		calls[req.Method]++
		if len(req.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "offline-fixture", "version": "1"}}
		case "tools/list":
			if req.Params.Cursor == "" {
				result = map[string]any{"tools": fixture.Tools[:1], "nextCursor": "page2"}
			} else {
				require.Equal(t, "page2", req.Params.Cursor)
				result = map[string]any{"tools": fixture.Tools[1:]}
			}
		default:
			t.Errorf("unexpected upstream method %s", req.Method)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}))
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager := coremcp.NewMCPManager(ctx, schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, &mockLogger{}, nil)
	defer manager.Cleanup()
	config := &schemas.MCPClientConfig{ID: "fixture-id", Name: "fixture", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar(upstream.URL), AuthType: schemas.MCPAuthTypePerUserHeaders, PerUserHeaderKeys: []string{"X-Fixture"}, ToolsToExecute: schemas.WhiteList{"*"}, ToolSyncInterval: time.Hour}
	discover := func() (map[string]schemas.ChatTool, map[string]string) {
		tools, mapping, err := manager.VerifyHeadersConnection(ctx, config, map[string]string{"X-Fixture": "offline"})
		require.NoError(t, err)
		return tools, mapping
	}
	tools, mapping := discover()
	require.Len(t, tools, 5)
	config.DiscoveredTools = tools
	config.DiscoveredToolNameMapping = mapping
	if persistenceMode != "" {
		path := filepath.Join(t.TempDir(), "mcp-schema.db")
		openStore := func() configstore.ConfigStore {
			store, err := configstore.NewConfigStore(ctx, &configstore.Config{Enabled: true, Type: configstore.ConfigStoreTypeSQLite, Config: &configstore.SQLiteConfig{Path: path}}, &mockLogger{})
			require.NoError(t, err)
			return store
		}
		store := openStore()
		originalTools, originalMapping := config.DiscoveredTools, config.DiscoveredToolNameMapping
		if persistenceMode != "create" {
			config.DiscoveredTools = nil
			config.DiscoveredToolNameMapping = nil
		}
		require.NoError(t, store.CreateMCPClientConfig(ctx, config))
		switch persistenceMode {
		case "config-update":
			require.NoError(t, store.UpdateMCPClientConfig(ctx, config.ID, &tables.TableMCPClient{
				Name: config.Name, AuthType: string(config.AuthType), PerUserHeaderKeys: config.PerUserHeaderKeys,
				ToolsToExecute: config.ToolsToExecute, DiscoveredTools: originalTools, DiscoveredToolNameMapping: originalMapping,
			}))
		case "tools-update":
			require.NoError(t, store.UpdateMCPClientTools(ctx, config.ID, originalTools, originalMapping))
		}
		require.NoError(t, store.Close(ctx))
		manager.Cleanup()
		tools, mapping, config = nil, nil, nil
		originalTools, originalMapping = nil, nil
		// The upstream is gone before the fresh manager exists: only the database
		// contract can satisfy downstream tools/list. No rediscovery is possible.
		upstream.Close()
		store = openStore()
		config, err = store.GetMCPClientConfigByID(ctx, "fixture-id")
		require.NoError(t, err)
		require.NoError(t, store.Close(ctx))
		manager = coremcp.NewMCPManager(ctx, schemas.MCPConfig{ToolSyncInterval: time.Hour}, nil, &mockLogger{}, nil)
		defer manager.Cleanup()
		tools = config.DiscoveredTools
		for _, tool := range fixture.Tools {
			name := tool["name"].(string)
			require.Equal(t, name, config.DiscoveredToolNameMapping[name])
		}
	}
	require.NoError(t, manager.AddClient(ctx, config))
	allNames := make([]string, 0, len(tools))
	for _, tool := range fixture.Tools {
		allNames = append(allNames, tool["name"].(string))
	}
	slices.Sort(allNames)
	h := &MCPServerHandler{toolManager: &schemaGatewayManager{manager}, config: &lib.Config{ClientConfig: &configstore.ClientConfig{}}, admitter: &schemaGatewayAdmitter{grants: map[string]schemas.Access{
		"sk-bf-all": restrictedAccess("fixture", allNames...), "sk-bf-one": restrictedAccess("fixture", "schema_absent"), "sk-bf-none": restrictedAccess("fixture"),
	}}}
	require.NoError(t, h.SyncMCPServer(ctx))
	changes := 0
	manager.SetToolsChangeCallback(func(string, string, map[string]schemas.ChatTool, map[string]string) {
		changes++
		require.NoError(t, h.SyncMCPServer(ctx))
	})
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		fc := &fasthttp.RequestCtx{}
		fc.Request.Header.SetMethod("POST")
		fc.Request.Header.Set("x-bf-vk", r.Header.Get("x-bf-vk"))
		fc.Request.Header.Set("Content-Type", "application/json")
		fc.Request.SetBody(body)
		h.handleMCPServer(fc)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fc.Response.StatusCode())
		_, err = w.Write(fc.Response.Body())
		require.NoError(t, err)
	}))
	defer downstream.Close()
	list := func(vk string) map[string]map[string]json.RawMessage {
		req, err := http.NewRequestWithContext(ctx, "POST", downstream.URL, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		require.NoError(t, err)
		req.Header.Set("x-bf-vk", vk)
		resp, err := downstream.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		var result struct {
			Result struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		require.Empty(t, result.Error)
		out := map[string]map[string]json.RawMessage{}
		for _, tool := range result.Result.Tools {
			var name string
			require.NoError(t, json.Unmarshal(tool["name"], &name))
			require.NotContains(t, out, name)
			out[name] = tool
		}
		return out
	}
	check := func() {
		got := list("sk-bf-all")
		require.Len(t, got, len(fixture.Tools))
		for _, up := range fixture.Tools {
			name := "fixture-" + up["name"].(string)
			down, ok := got[name]
			require.True(t, ok, name)
			for _, field := range []string{"inputSchema", "outputSchema", "annotations"} {
				value, present := up[field]
				actual, exists := down[field]
				if !present {
					require.False(t, exists, "%s %s must stay absent", name, field)
					continue
				}
				require.True(t, exists, "%s %s missing", name, field)
				expected, err := json.Marshal(value)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(actual), "%s %s changed", name, field)
				if name == "fixture-schema_edge" && field == "inputSchema" {
					require.Contains(t, string(actual), "9007199254740993")
				}
			}
		}
		require.Len(t, list("sk-bf-one"), 1)
		require.Contains(t, list("sk-bf-one"), "fixture-schema_absent")
		require.Empty(t, list("sk-bf-none"))
	}
	check()
	if persistenceMode != "" {
		mu.Lock()
		require.Zero(t, calls["tools/call"])
		require.Equal(t, 2, calls["tools/list"], "only the initial discovery ran")
		mu.Unlock()
		return
	}
	tools, mapping = discover()
	manager.SetClientTools(config.ID, tools, mapping)
	require.Equal(t, 0, changes, "duplicate discovery must not resync")
	// Output-only change is invisible to provider JSON but must refresh the gateway cache.
	mu.Lock()
	edge["outputSchema"] = map[string]any{"type": "object", "additionalProperties": false, "x-revision": 2}
	mu.Unlock()
	tools, mapping = discover()
	manager.SetClientTools(config.ID, tools, mapping)
	require.Equal(t, 1, changes)
	check()
	mu.Lock()
	edge["inputSchema"].(map[string]any)["maxProperties"] = 3
	mu.Unlock()
	tools, mapping = discover()
	manager.SetClientTools(config.ID, tools, mapping)
	require.Equal(t, 2, changes)
	check()
	mu.Lock()
	require.Zero(t, calls["tools/call"])
	require.Equal(t, 8, calls["tools/list"])
	mu.Unlock()
}
func TestMCPServerSchemaSnapshotOwnsMetadata(t *testing.T) {
	h := &MCPServerHandler{}
	tool := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "snapshot"}, MCPToolSchema: &schemas.MCPToolSchema{InputSchema: []byte(`{}`), OutputSchema: []byte(`{}`)}, Annotations: &schemas.MCPToolAnnotations{ReadOnlyHint: schemas.Ptr(true)}}
	snapshot := h.buildServer([]schemas.ChatTool{tool})
	tool.MCPToolSchema.InputSchema[0] = '['
	tool.MCPToolSchema.OutputSchema[0] = '['
	*tool.Annotations.ReadOnlyHint = false
	result := snapshot.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	wire, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(wire), `"inputSchema":{}`)
	require.Contains(t, string(wire), `"outputSchema":{}`)
	require.Contains(t, string(wire), `"readOnlyHint":true`)
}
