package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
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

// Forwarding upstream MCP `instructions` on the gateway's own initialize response.

// fakeInstructionsToolManager reports a fixed aggregate and records the context it was asked
// with, so a test can assert the hook resolves the text per request rather than once at build.
type fakeInstructionsToolManager struct {
	instructions string
	seenCtx      context.Context
}

func (f *fakeInstructionsToolManager) GetAvailableMCPTools(context.Context) []schemas.ChatTool {
	return nil
}

func (f *fakeInstructionsToolManager) GetMCPServerInstructions(ctx context.Context) string {
	f.seenCtx = ctx
	return f.instructions
}

func (f *fakeInstructionsToolManager) ExecuteChatMCPTool(context.Context, *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return nil, nil
}

func (f *fakeInstructionsToolManager) ExecuteResponsesMCPTool(context.Context, *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return nil, nil
}

// initializeResult drives a real initialize through the built server and returns the decoded
// result, which is what a connecting MCP client would actually receive.
func initializeResult(t *testing.T, tm MCPToolManager, ctx context.Context) map[string]any {
	t.Helper()
	h := &MCPServerHandler{
		toolManager: tm,
		config:      &lib.Config{ClientConfig: &configstore.ClientConfig{}},
	}
	h.mcpServer.Store(h.buildServer(nil))

	raw := h.server().HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0.0.1"}}}`))
	require.NotNil(t, raw)

	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	var envelope struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	return envelope.Result
}

func TestInitializeForwardsInstructionsOnTheHandshake(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"github\">\nAlways call get_me first.\n</mcp_server>"}

	result := initializeResult(t, tm, context.Background())

	assert.Equal(t, "<mcp_server name=\"github\">\nAlways call get_me first.\n</mcp_server>", result["instructions"])
}

// Nothing to say means no field, rather than an empty one a client would have to special-case.
func TestInitializeOmitsInstructionsWhenNoUpstreamAdvertisesAny(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: ""}

	result := initializeResult(t, tm, context.Background())

	_, present := result["instructions"]
	assert.False(t, present)
}

// The hook must resolve against the per-request context — that is the whole reason it is a
// hook rather than server.WithInstructions, which is fixed at construction. If the request's
// narrowing did not reach the resolver, a caller scoped to one upstream would be handed every
// upstream's text.
func TestInitializeResolvesInstructionsFromTheRequestContext(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.MCPContextKeyIncludeClients, []string{"alpha"})

	initializeResult(t, tm, ctx)

	require.NotNil(t, tm.seenCtx)
	assert.Equal(t, []string{"alpha"}, tm.seenCtx.Value(schemas.MCPContextKeyIncludeClients))
}

// A /mcp/<slug> request is narrowed by admitBySlug, which stamps the served tool list on
// MCPContextKeyIncludeTools rather than include-clients. The resolver has to see that narrowing
// too, or a Virtual MCP endpoint would answer initialize with every upstream's instructions.
func TestInitializeResolvesInstructionsForSlugNarrowedRequests(t *testing.T) {
	tm := &fakeInstructionsToolManager{instructions: "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>"}
	ctx := context.WithValue(context.Background(), schemas.MCPContextKeyIncludeTools, []string{"alpha-echo"})

	result := initializeResult(t, tm, ctx)

	assert.Equal(t, "<mcp_server name=\"alpha\">\nscoped\n</mcp_server>", result["instructions"])
	require.NotNil(t, tm.seenCtx)
	assert.Equal(t, []string{"alpha-echo"}, tm.seenCtx.Value(schemas.MCPContextKeyIncludeTools),
		"the slug's served tool list must reach the instructions resolver")
}

// schemaGatewayManager adapts the actual discovery/cache manager to the transport seam.
// Execution deliberately panics: this regression only sends initialize/tools/list.
type schemaGatewayManager struct{ manager *coremcp.MCPManager }

func (g *schemaGatewayManager) GetMCPServerInstructions(ctx context.Context) string { return "" }

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

func (*schemaGatewayAdmitter) VirtualMCPToolAccess(*schemas.BifrostContext, string, schemas.Access) ([]string, bool) {
	return nil, false
}

func (*schemaGatewayAdmitter) MCPClientToolAccess(*schemas.BifrostContext, string, schemas.Access) ([]string, bool) {
	return nil, false
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
		{"name": "lookup", "title": "Lookup display title", "_meta": map[string]any{"fixture": "synthetic", "presentNull": nil}, "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"query": map[string]any{"type": "string", "pattern": "^[a-z]+$", "minLength": 1}}, "required": []string{"query"}, "x-contract": "synthetic"}, "outputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}, "annotations": map[string]any{"title": "Lookup", "readOnlyHint": true, "destructiveHint": false}},
	}}
	// Synthetic siblings pin composition, refs, large numeric bounds and absent vs {}.
	var edge map[string]any
	// Decode numbers exactly for the numeric precision assertion below.
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"name":"schema_edge","title":null,"_meta":{"nested":{"items":[null,{"large":9007199254740993}]},"presentNull":null},"inputSchema":{"type":"object","additionalProperties":false,"maxProperties":2,"$defs":{"N":{"type":"integer","maximum":9007199254740993}},"properties":{"n":{"$ref":"#/$defs/N"},"a":{"type":"array"}},"anyOf":[{"required":["n"]},{"required":["a"]}],"oneOf":[{"maxProperties":1},{"minProperties":2}],"x-extension":{"one":1}},"outputSchema":{},"annotations":{"readOnlyHint":true,"x-unknown":{"nested":{"null":null,"large":9007199254740993}}}}`)))
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
		tools, mapping, _, err := manager.VerifyHeadersConnection(ctx, config, map[string]string{"X-Fixture": "offline"})
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
			require.NoError(t, store.UpdateMCPClientTools(ctx, config.ID, originalTools, originalMapping, ""))
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
	manager.SetToolsChangeCallback(func(string, string, map[string]schemas.ChatTool, map[string]string, string) {
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
			for _, field := range []string{"inputSchema", "outputSchema", "annotations", "title", "_meta"} {
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
				if name == "fixture-schema_edge" && (field == "_meta" || field == "annotations") {
					require.Contains(t, string(actual), "9007199254740993")
					if field == "_meta" {
						require.Contains(t, string(actual), `"presentNull":null`)
					} else {
						require.Contains(t, string(actual), `"null":null`)
					}
				}
				if name == "fixture-schema_edge" && field == "title" {
					require.Equal(t, "null", string(actual), "explicit null title must remain distinct from an absent title")
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
	manager.SetClientTools(config.ID, tools, mapping, "")
	require.Equal(t, 0, changes, "duplicate discovery must not resync")
	// Output-only change is invisible to provider JSON but must refresh the gateway cache.
	mu.Lock()
	edge["outputSchema"] = map[string]any{"type": "object", "additionalProperties": false, "x-revision": 2}
	mu.Unlock()
	tools, mapping = discover()
	manager.SetClientTools(config.ID, tools, mapping, "")
	require.Equal(t, 1, changes)
	check()
	mu.Lock()
	edge["inputSchema"].(map[string]any)["maxProperties"] = 3
	mu.Unlock()
	tools, mapping = discover()
	manager.SetClientTools(config.ID, tools, mapping, "")
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

func TestMCPRawToolTypedHookEditFailsClosedButNameOnlyRewriteWorks(t *testing.T) {
	newTool := func() schemas.ChatTool {
		description := "original description"
		tool := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        "upstream-name",
				Description: &description,
				Parameters:  &schemas.ToolFunctionParameters{Type: "object"},
			},
			MCPToolSchema: &schemas.MCPToolSchema{InputSchema: []byte(`{"type":"object"}`)},
			Annotations:   &schemas.MCPToolAnnotations{ReadOnlyHint: schemas.Ptr(true)},
			SDKRawToolJSON: json.RawMessage(
				`{"name":"upstream-name","description":"original description","title":"Native title","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true,"x-unknown":{"null":null}}}`,
			),
		}
		tool.SDKRawToolProjectionFingerprint = schemas.SDKRawToolProjectionFingerprint(tool)
		return tool
	}
	list := func(t *testing.T, tool schemas.ChatTool) ([]byte, error) {
		t.Helper()
		response := (&MCPServerHandler{}).buildServer([]schemas.ChatTool{tool}).HandleMessage(
			context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`),
		)
		return sonic.Marshal(response)
	}

	t.Run("typed description edit is rejected", func(t *testing.T) {
		tool := newTool()
		*tool.Function.Description = "edited by hook"
		_, err := list(t, tool)
		require.ErrorContains(t, err, "raw MCP tool projection conflict")
	})

	t.Run("name rewrite retains raw metadata", func(t *testing.T) {
		tool := newTool()
		tool.Function.Name = "gateway-renamed"
		wire, err := list(t, tool)
		require.NoError(t, err)
		var response struct {
			Result struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(wire, &response))
		require.Len(t, response.Result.Tools, 1)
		var name string
		require.NoError(t, json.Unmarshal(response.Result.Tools[0]["name"], &name))
		require.Equal(t, "gateway-renamed", name)
		require.JSONEq(t, `"Native title"`, string(response.Result.Tools[0]["title"]))
		annotations := response.Result.Tools[0]["annotations"]
		require.Contains(t, string(annotations), `"x-unknown":{"null":null}`)
	})
}

func BenchmarkMCPNativeMetadata(b *testing.B) {
	description := "synthetic benchmark tool"
	tool := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "benchmark",
			Description: &description,
			Parameters:  &schemas.ToolFunctionParameters{Type: "object"},
		},
	}
	benchmarkMCPToolsList(b, "typed_fallback", tool)

	rawTool := tool
	rawTool.MCPToolSchema = &schemas.MCPToolSchema{InputSchema: []byte(`{"type":"object"}`)}
	rawTool.SDKRawToolJSON = json.RawMessage(`{"name":"benchmark","description":"synthetic benchmark tool","title":"Benchmark","inputSchema":{"type":"object"}}`)
	rawTool.SDKRawToolProjectionFingerprint = schemas.SDKRawToolProjectionFingerprint(rawTool)
	benchmarkMCPToolsList(b, "raw", rawTool)
}

func benchmarkMCPToolsList(b *testing.B, name string, tool schemas.ChatTool) {
	b.Run(name, func(b *testing.B) {
		request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		mcpServer := (&MCPServerHandler{}).buildServer([]schemas.ChatTool{tool})
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			response := mcpServer.HandleMessage(context.Background(), request)
			if _, err := json.Marshal(response); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// slugAdmitter admits everything and resolves slugs from fixed maps.
type slugAdmitter struct {
	vmcps   map[string][]string
	clients map[string][]string
}

func (slugAdmitter) AdmitMCPGatewayRequest(*schemas.BifrostContext) (schemas.Access, *schemas.BifrostError) {
	return nil, nil
}

func (a slugAdmitter) VirtualMCPToolAccess(_ *schemas.BifrostContext, slug string, _ schemas.Access) ([]string, bool) {
	served, ok := a.vmcps[slug]
	return served, ok
}

func (a slugAdmitter) MCPClientToolAccess(_ *schemas.BifrostContext, slug string, _ schemas.Access) ([]string, bool) {
	served, ok := a.clients[slug]
	return served, ok
}

// admitBySlug answers 403 for a real refusal, stamps the served tools when the slug names a granted
// Virtual MCP or client, and without an admitter serves a configured client whole, as /mcp does.
func TestAdmitBySlug(t *testing.T) {
	resolver := slugAdmitter{
		vmcps:   map[string][]string{"research-tools": {"alpha-search"}},
		clients: map[string][]string{"context7": {"context7-*"}},
	}
	cases := []struct {
		name       string
		admitter   MCPGatewayAdmitter
		slug       string
		wantStatus int
		wantTools  []string
	}{
		{name: "nil admitter serves a configured client unrestricted", slug: "context7", wantTools: []string{"context7-*"}},
		{name: "nil admitter refuses an unknown slug", slug: "definitely-does-not-exist", wantStatus: 403},
		{name: "granted virtual mcp", admitter: resolver, slug: "research-tools", wantTools: []string{"alpha-search"}},
		{name: "granted mcp client", admitter: resolver, slug: "context7", wantTools: []string{"context7-*"}},
		{name: "unknown slug", admitter: resolver, slug: "definitely-does-not-exist", wantStatus: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &lib.Config{MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{
				{ID: "c7", Name: "context7", EndpointSlug: "context7"},
			}}}
			h := &MCPServerHandler{admitter: tc.admitter, config: cfg}
			bctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			refusal := h.admitBySlug(bctx, tc.slug, nil)
			if tc.wantStatus != 0 {
				require.NotNil(t, refusal)
				assert.Equal(t, tc.wantStatus, refusal.status)
				if tc.wantStatus == 403 {
					assert.Equal(t, "access_denied", refusal.message)
				}
				return
			}
			require.Nil(t, refusal)
			assert.Equal(t, tc.wantTools, bctx.Value(schemas.MCPContextKeyIncludeTools))
		})
	}
}
