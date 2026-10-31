//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/internal/mcpvouch"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const connectSignInMessage = "Authentication required for notes. Visit https://idp.example/authorize to connect your account."

// connectCredStore resolves a per-call credential per caller: a caller listed in
// tokens connects with it as a bearer token, any other is asked to sign in.
type connectCredStore struct {
	mu     sync.Mutex
	tokens map[string]string // by user or virtual-key identity
	calls  int
}

func (s *connectCredStore) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	token, ok := s.tokens[ctx.MCPIdentity(ctx.MCPAuthMode())]
	if !ok {
		return nil, &schemas.MCPAuthRequiredError{
			Kind:          schemas.MCPAuthRequiredKindOAuth,
			MCPClientID:   config.ID,
			MCPClientName: config.Name,
			AuthorizeURL:  "https://idp.example/authorize",
			Message:       connectSignInMessage,
		}
	}
	return http.Header{"Authorization": []string{"Bearer " + token}}, nil
}

func (s *connectCredStore) RequestHeaders(*schemas.BifrostContext, *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (s *connectCredStore) RequiresPerCallConnection(*schemas.MCPClientConfig) bool { return true }

func (s *connectCredStore) ForceRefresh(*schemas.BifrostContext, *schemas.MCPClientConfig) error {
	return nil
}

func (s *connectCredStore) AdminConnectionHeaders(context.Context, *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (s *connectCredStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// buildConnectUpstream starts an upstream MCP server advertising instructions
// and listing tools, each of which answers with its own name; it records every
// request's headers.
func buildConnectUpstream(t *testing.T, tools ...string) (*httptest.Server, *adminDiscoveryRecorder) {
	t.Helper()
	s := server.NewMCPServer("connect-upstream", "1.0.0", server.WithToolCapabilities(true), server.WithInstructions("Search before you write."))
	for _, name := range tools {
		answer := "upstream " + name
		s.AddTool(mcpgo.NewTool(name, mcpgo.WithDescription(name)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText(answer), nil
		})
	}
	streamable := server.NewStreamableHTTPServer(s)
	rec := &adminDiscoveryRecorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.Method, r.Header)
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, rec
}

// newConnectTestManager returns a manager holding one client with
// per_user_discovery on, "notes", served by upstreamURL, and the store keeping
// its callers' discoveries.
func newConnectTestManager(t *testing.T, creds *connectCredStore, upstreamURL string, mcpConfig schemas.MCPConfig) (*MCPManager, *fakePerUserDiscoveryStore, *schemas.MCPClientConfig) {
	t.Helper()
	perUser := newFakePerUserDiscoveryStore()
	mcpConfig.PerUserDiscoveryStore = perUser
	m := NewMCPManager(context.Background(), mcpConfig, creds, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID:               "notes-id",
		Name:             "notes",
		AuthType:         schemas.MCPAuthTypePerUserOauth,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(upstreamURL),
		ToolsToExecute:   []string{"*"},
		PerUserDiscovery: schemas.Ptr(true),
	}
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateHealthy}
	m.mu.Unlock()
	return m, perUser, config
}

func connectToolCall(name string) *schemas.ChatAssistantMessageToolCall {
	return &schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("call-" + name),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(name), Arguments: "{}"},
	}
}

func toolNames(tools []schemas.ChatTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Function != nil {
			names = append(names, tool.Function.Name)
		}
	}
	return names
}

func TestGetConnectTools_StandsInForTheClientUntilTheCallerConnects(t *testing.T) {
	m, perUser, notes := newConnectTestManager(t, &connectCredStore{}, "http://upstream.invalid", schemas.MCPConfig{})
	shared := sharedClient()
	m.mu.Lock()
	m.clientMap[shared.ID] = &schemas.MCPClientState{Name: shared.Name, ExecutionConfig: shared, State: schemas.MCPConnectionStateHealthy}
	m.mu.Unlock()

	connect := m.GetConnectTools(userContext("alice"))
	require.Len(t, connect, 1, "only the per-user discovery client stands in with connect")
	assert.Equal(t, "notes-connect", connect[0].Function.Name)
	assert.Contains(t, *connect[0].Function.Description, "notes")
	assert.NotContains(t, m.GetToolPerClient(userContext("alice")), "notes", "connect never joins the per-client tool lists code mode builds bindings from")

	require.NoError(t, perUser.SetDiscovery(userContext("alice"), notes, discoveryOf("notes", "search")))
	assert.Empty(t, m.GetConnectTools(userContext("alice")), "a caller with a discovery is offered the client's tools instead")
	assert.Equal(t, []string{"notes-connect"}, toolNames(m.GetConnectTools(vkContext("vk-1"))), "another caller still has to connect")
}

func TestGetConnectTools_OnlyCallersWhoCanReachTheClientSeeIt(t *testing.T) {
	cases := []struct {
		name    string
		ctx     func() context.Context
		edit    func(*schemas.MCPClientState)
		offered bool
	}{
		{name: "user", ctx: func() context.Context { return userContext("alice") }, offered: true},
		{name: "virtual key", ctx: func() context.Context { return vkContext("vk-1") }, offered: true},
		{name: "session-only caller", ctx: func() context.Context { return callerContext(schemas.BifrostContextKeyMCPSessionID, "sess-1") }},
		{name: "anonymous caller", ctx: func() context.Context { return context.Background() }},
		{name: "disabled client", ctx: func() context.Context { return userContext("alice") }, edit: func(s *schemas.MCPClientState) { s.State = schemas.MCPConnectionStateDisabled }},
		{name: "client executing no tool", ctx: func() context.Context { return userContext("alice") }, edit: func(s *schemas.MCPClientState) { s.ExecutionConfig.ToolsToExecute = []string{} }},
		{name: "client left out of the request's clients", ctx: func() context.Context {
			ctx := userContext("alice")
			ctx.SetValue(schemas.MCPContextKeyIncludeClients, []string{"wiki"})
			return ctx
		}},
		{name: "request tool list naming one of its tools", ctx: func() context.Context {
			ctx := userContext("alice")
			ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"notes-search"})
			return ctx
		}, offered: true},
		{name: "request tool list naming only another client", ctx: func() context.Context {
			ctx := userContext("alice")
			ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"wiki-*"})
			return ctx
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, notes := newConnectTestManager(t, &connectCredStore{}, "http://upstream.invalid", schemas.MCPConfig{})
			if tc.edit != nil {
				m.mu.Lock()
				tc.edit(m.clientMap[notes.ID])
				m.mu.Unlock()
			}
			got := toolNames(m.GetConnectTools(tc.ctx()))
			if tc.offered {
				assert.Equal(t, []string{"notes-connect"}, got)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

func TestGetAvailableTools_OffersConnectDirectlyOnACodeModeClient(t *testing.T) {
	m, _, notes := newConnectTestManager(t, &connectCredStore{}, "http://upstream.invalid", schemas.MCPConfig{})
	m.mu.Lock()
	notes.IsCodeModeClient = true
	m.mu.Unlock()

	assert.Contains(t, toolNames(m.GetAvailableTools(userContext("alice"))), "notes-connect", "connect is answered by Bifrost, so it is never left to a code-mode binding")
}

func TestConnect_WithoutACredentialReturnsTheSignInLinkAsAResult(t *testing.T) {
	upstream, rec := buildConnectUpstream(t, "search")
	m, perUser, notes := newConnectTestManager(t, &connectCredStore{}, upstream.URL, schemas.MCPConfig{})

	result, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.Nil(t, bErr, "the sign-in link is a result the model relays, not an error that stops the request")
	require.NotNil(t, result)
	assert.Equal(t, connectSignInMessage, *result.Content.ContentStr)
	assert.Nil(t, result.ChatToolMessage.IsError)
	assert.Nil(t, perUser.GetDiscovery(userContext("alice"), notes))
	assert.Empty(t, rec.headerValues("Authorization"), "nothing is dialed without a credential")
}

func TestConnect_WithACredentialStoresTheCallersDiscovery(t *testing.T) {
	upstream, rec := buildConnectUpstream(t, "search", "create_page")
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, perUser, notes := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{})

	result, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.Nil(t, bErr)
	assert.Equal(t, "Connected to notes: 2 tools are available from your next turn.", *result.Content.ContentStr)

	discovery := perUser.GetDiscovery(userContext("alice"), notes)
	require.NotNil(t, discovery, "the discovery is stored under the caller whose credential made it")
	assert.Contains(t, discovery.Tools, "notes-search")
	assert.Contains(t, discovery.Tools, "notes-create_page")
	assert.Equal(t, "Search before you write.", discovery.Instructions)
	assert.False(t, discovery.DiscoveredAt.IsZero())
	assert.Nil(t, perUser.GetDiscovery(userContext("bob"), notes))
	for _, header := range rec.headerValues("Authorization") {
		assert.Equal(t, "Bearer alice-token", header, "the upstream is reached with the caller's own credential")
	}

	assert.Equal(t, []string{"notes-create_page", "notes-search"}, toolNames(m.GetToolPerClient(userContext("alice"))["notes"]))
	assert.Empty(t, m.GetConnectTools(userContext("alice")))
}

// A caller who connects gets the server's resources, resource templates and prompts with its
// tools, as one discovery.
func TestConnect_StoresTheCallersResourcesAndPromptsWithTheTools(t *testing.T) {
	upstream := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream()))
	t.Cleanup(upstream.Close)
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, perUser, notes := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{})

	_, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.Nil(t, bErr)

	discovery := perUser.GetDiscovery(userContext("alice"), notes)
	require.NotNil(t, discovery)
	requireResourcesAndPrompts(t, discovery, "notes")
}

func TestConnect_WhenAlreadyConnectedSaysSo(t *testing.T) {
	upstream, rec := buildConnectUpstream(t, "search")
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, perUser, notes := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{})
	require.NoError(t, perUser.SetDiscovery(userContext("alice"), notes, discoveryOf("notes", "search")))

	result, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.Nil(t, bErr)
	assert.Equal(t, "Already connected to notes; its tools are in your tool list.", *result.Content.ContentStr)
	assert.Empty(t, rec.headerValues("Authorization"))
	assert.Zero(t, creds.callCount())
}

func TestConnect_NameGoesToAnUpstreamConnectToolOnceDiscovered(t *testing.T) {
	upstream, _ := buildConnectUpstream(t, "connect")
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, _, _ := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{})

	first, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.Nil(t, bErr)
	require.Equal(t, "Connected to notes: 1 tools are available from your next turn.", *first.Content.ContentStr)
	assert.Nil(t, m.ConnectToolClient(userContext("alice"), "notes-connect"))

	second, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.Nil(t, bErr)
	assert.Equal(t, "upstream connect", *second.Content.ContentStr, "the upstream's own connect tool owns the name once the caller has it")
}

// An operator can inject a caller's upstream tool that happens to be named connect. It is the
// caller's own tool, never Bifrost's connect, so it keeps the injected mark and runs past the
// request's tool filters like any injected tool.
func TestConnect_AnInjectedUpstreamConnectToolKeepsItsInjectedMark(t *testing.T) {
	upstream, _ := buildConnectUpstream(t, "connect")
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, _, _ := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{})
	_, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.Nil(t, bErr)

	ctx := userContext("alice")
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"notes-search"})
	mcpvouch.Set(ctx, schemas.MCPToolCallKindInjected, "notes", "notes-connect")
	result, bErr := m.ExecuteChatTool(ctx, connectToolCall("notes-connect"))
	if bErr != nil {
		t.Fatalf("an injected tool runs past the request's tool filters, got: %s", bErr.GetErrorString())
	}
	assert.Equal(t, "upstream connect", *result.Content.ContentStr)
}

func TestConnect_WithoutAUserOrVirtualKeyIsAToolError(t *testing.T) {
	upstream, rec := buildConnectUpstream(t, "search")
	creds := &connectCredStore{}
	m, _, _ := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{})

	_, bErr := m.ExecuteChatTool(callerContext(schemas.BifrostContextKeyMCPSessionID, "sess-1"), connectToolCall("notes-connect"))
	require.NotNil(t, bErr)
	assert.Contains(t, bErr.GetErrorString(), "needs a signed-in user or a virtual key")
	assert.Nil(t, bErr.ExtraFields.MCPAuthRequired, "never a sign-in link for a caller a discovery cannot be kept for")
	assert.Zero(t, creds.callCount())
	assert.Empty(t, rec.headerValues("Authorization"))
}

// connectMarkPipeline records, for each tool call it gates, whether the call
// carried the connect mark, and can retarget the call as a plugin might. With
// user set it first names the caller, as governance does on a direct tool
// execution, where the request carries only a credential until its pre-hook
// resolves it.
type connectMarkPipeline struct {
	headerRewritingPipeline
	mu       sync.Mutex
	marked   map[string]bool
	retarget string
	user     string
}

func (p *connectMarkPipeline) RunMCPPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	if req.RequestType != schemas.MCPRequestTypeChatToolCall {
		return req, nil, 0
	}
	if p.user != "" {
		ctx.SetValue(schemas.BifrostContextKeyUserID, p.user)
	}
	name := req.GetToolName()
	p.mu.Lock()
	p.marked[name] = mcpvouch.Kind(ctx, req.ClientName, name) == schemas.MCPToolCallKindConnect
	p.mu.Unlock()
	if p.retarget != "" {
		retargeted := *req
		toolCall := *req.ChatAssistantMessageToolCall
		toolCall.Function.Name = schemas.Ptr(p.retarget)
		retargeted.ChatAssistantMessageToolCall = &toolCall
		return &retargeted, nil, 0
	}
	return req, nil, 0
}

func TestConnect_IsMarkedForPolicyPlugins(t *testing.T) {
	upstream, _ := buildConnectUpstream(t, "search")
	pipeline := &connectMarkPipeline{marked: map[string]bool{}}
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, _, _ := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{
		PluginPipelineProvider: func() interface{} { return pipeline },
		ReleasePluginPipeline:  func(interface{}) {},
	})

	ctx := userContext("alice")
	_, bErr := m.ExecuteChatTool(ctx, connectToolCall("notes-connect"))
	require.Nil(t, bErr)
	_, bErr = m.ExecuteChatTool(ctx, connectToolCall("notes-search"))
	require.Nil(t, bErr)

	assert.Equal(t, map[string]bool{"notes-connect": true, "notes-search": false}, pipeline.marked)
	assert.Equal(t, schemas.MCPToolCallKindNone, mcpvouch.Kind(ctx, "notes", "notes-connect"), "the mark covers the connect call only, not the request around it")
}

// A direct tool execution names its caller only once a policy plugin resolves the credential, so
// whether "notes-connect" is Bifrost's connect or the upstream's own tool is settled then: the
// plugin is asked about the upstream tool under its own permit, and the upstream tool runs.
func TestConnect_NameGoesToAnUpstreamConnectToolWhenAPolicyPluginNamesTheCaller(t *testing.T) {
	upstream, _ := buildConnectUpstream(t, "connect")
	pipeline := &connectMarkPipeline{marked: map[string]bool{}, user: "alice"}
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, perUser, notes := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{
		PluginPipelineProvider: func() interface{} { return pipeline },
		ReleasePluginPipeline:  func(interface{}) {},
	})
	require.NoError(t, perUser.SetDiscovery(userContext("alice"), notes, discoveryOf("notes", "connect")))

	result, bErr := m.ExecuteChatTool(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), connectToolCall("notes-connect"))
	require.Nil(t, bErr)
	assert.Equal(t, "upstream connect", *result.Content.ContentStr)
	assert.False(t, pipeline.marked["notes-connect"], "the plugin authorizes the upstream tool by its own permit, not as a connect")
}

// The same late-named caller with no discovery yet is offered Bifrost's connect, marked as one.
func TestConnect_IsBifrostsWhenAPolicyPluginNamesACallerWithoutADiscovery(t *testing.T) {
	upstream, _ := buildConnectUpstream(t, "search")
	pipeline := &connectMarkPipeline{marked: map[string]bool{}, user: "alice"}
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, perUser, notes := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{
		PluginPipelineProvider: func() interface{} { return pipeline },
		ReleasePluginPipeline:  func(interface{}) {},
	})

	result, bErr := m.ExecuteChatTool(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), connectToolCall("notes-connect"))
	require.Nil(t, bErr)
	assert.Equal(t, "Connected to notes: 1 tools are available from your next turn.", *result.Content.ContentStr)
	assert.True(t, pipeline.marked["notes-connect"])
	assert.NotNil(t, perUser.GetDiscovery(userContext("alice"), notes))
}

func TestConnect_RetargetedByAPluginDoesNotRun(t *testing.T) {
	upstream, rec := buildConnectUpstream(t, "search")
	pipeline := &connectMarkPipeline{marked: map[string]bool{}, retarget: "notes-search"}
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, perUser, notes := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{
		PluginPipelineProvider: func() interface{} { return pipeline },
		ReleasePluginPipeline:  func(interface{}) {},
	})

	_, bErr := m.ExecuteChatTool(userContext("alice"), connectToolCall("notes-connect"))
	require.NotNil(t, bErr, "the retargeted call is resolved as the tool it now names, which alice has not discovered")
	assert.Nil(t, perUser.GetDiscovery(userContext("alice"), notes))
	assert.Empty(t, rec.headerValues("Authorization"))
}

func TestParseAndAddToolsToRequest_TakesConnectBackOnceTheCallerConnects(t *testing.T) {
	m, perUser, notes := newConnectTestManager(t, &connectCredStore{}, "http://upstream.invalid", schemas.MCPConfig{})
	ctx := userContext("alice")
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{
			{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "get_weather"}},
		}}},
	}

	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	require.Equal(t, []string{"get_weather", "notes-connect"}, toolNames(req.ChatRequest.Params.Tools))

	// The agent loop's next turn reuses the same params against the same context.
	require.NoError(t, perUser.SetDiscovery(ctx, notes, discoveryOf("notes", "search")))
	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	assert.Equal(t, []string{"get_weather", "notes-search"}, toolNames(req.ChatRequest.Params.Tools), "connect leaves once its client's tools take its place; the caller's own tool stays")
}

// A connect tool is not one of the client's tools, so it stays off the list
// policy plugins check each added tool of against the caller's grant: a grant
// naming only notes-search must not refuse a request offering notes-connect.
// The connect call itself is checked against the client when it runs.
func TestParseAndAddToolsToRequest_LeavesConnectOffTheAddedToolsList(t *testing.T) {
	m, perUser, notes := newConnectTestManager(t, &connectCredStore{}, "http://upstream.invalid", schemas.MCPConfig{})
	ctx := userContext("alice")
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"notes-search"})
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Params: &schemas.ChatParameters{}},
	}

	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	require.Equal(t, []string{"notes-connect"}, toolNames(req.ChatRequest.Params.Tools))
	added, _ := ctx.Value(schemas.BifrostContextKeyMCPAddedTools).([]string)
	assert.NotContains(t, added, "notes-connect")

	require.NoError(t, perUser.SetDiscovery(ctx, notes, discoveryOf("notes", "search")))
	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	added, _ = ctx.Value(schemas.BifrostContextKeyMCPAddedTools).([]string)
	assert.Contains(t, added, "notes-search", "the client's own tools are listed once the caller connects")
	assert.NotContains(t, added, "notes-connect")
}

func TestParseAndAddToolsToRequest_KeepsACallersOwnToolNamedLikeConnect(t *testing.T) {
	m, perUser, notes := newConnectTestManager(t, &connectCredStore{}, "http://upstream.invalid", schemas.MCPConfig{})
	ctx := userContext("alice")
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{
			{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "notes-connect"}},
		}}},
	}

	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	require.NoError(t, perUser.SetDiscovery(ctx, notes, discoveryOf("notes", "search")))
	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	assert.Equal(t, []string{"notes-connect", "notes-search"}, toolNames(req.ChatRequest.Params.Tools), "only a connect tool Bifrost added is taken back")
}

func TestParseAndAddToolsToRequest_GivesAnUpstreamConnectItsOwnDefinition(t *testing.T) {
	m, perUser, notes := newConnectTestManager(t, &connectCredStore{}, "http://upstream.invalid", schemas.MCPConfig{})
	ctx := userContext("alice")
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{}}

	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	require.Equal(t, []string{"notes-connect"}, toolNames(req.ChatRequest.Params.Tools))

	// Connecting discovered an upstream tool that is itself named "connect".
	upstream := discoveryOf("notes", "connect", "search")
	connect := upstream.Tools["notes-connect"]
	connect.Function.Description = schemas.Ptr("Open a notes session")
	upstream.Tools["notes-connect"] = connect
	require.NoError(t, perUser.SetDiscovery(ctx, notes, upstream))

	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	tools := req.ChatRequest.Params.Tools
	require.Equal(t, []string{"notes-connect", "notes-search"}, toolNames(tools))
	require.NotNil(t, tools[0].Function.Description)
	assert.Equal(t, "Open a notes session", *tools[0].Function.Description, "the upstream tool replaces Bifrost's connect definition rather than hiding behind it")

	turnTwo := slices.Clone(tools)
	m.toolsManager.ParseAndAddToolsToRequest(ctx, req)
	assert.Equal(t, turnTwo, req.ChatRequest.Params.Tools, "a later turn leaves the upstream tool where it is")
}

func TestAgent_ConnectRunsWithoutApproval(t *testing.T) {
	upstream, _ := buildConnectUpstream(t, "search")
	creds := &connectCredStore{tokens: map[string]string{"alice": "alice-token"}}
	m, perUser, notes := newConnectTestManager(t, creds, upstream.URL, schemas.MCPConfig{})

	turnOne := &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{{
		FinishReason: schemas.Ptr("tool_calls"),
		ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{
			Role:                 schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{*connectToolCall("notes-connect")}},
		}},
	}}}
	var turns int
	var toolResult string
	makeReq := func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		turns++
		if last := req.Input[len(req.Input)-1]; last.Content != nil && last.Content.ContentStr != nil {
			toolResult = *last.Content.ContentStr
		}
		return &schemas.BifrostChatResponse{Choices: []schemas.BifrostResponseChoice{{
			FinishReason:                schemas.Ptr("stop"),
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("done")}}},
		}}}, nil
	}
	originalReq := &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4", Input: []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("use notes")}},
	}}

	agent := &AgentModeExecutor{logger: &MockLogger{}}
	_, bErr := agent.ExecuteAgentForChatRequest(userContext("alice"), 10, originalReq, turnOne, makeReq, nil, m.executeToolForAgent, m)
	require.Nil(t, bErr)

	assert.Equal(t, 1, turns, "connect ran without approval and the loop went on")
	assert.Equal(t, "Connected to notes: 1 tools are available from your next turn.", toolResult)
	assert.NotNil(t, perUser.GetDiscovery(userContext("alice"), notes))
}

// The connect tools of a request served by a manager with no per-user discovery
// client: the common case, which must cost a lock and a loop and nothing more.
func BenchmarkGetConnectTools_NoPerUserClient(b *testing.B) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	m.mu.Lock()
	for i := range 10 {
		config := &schemas.MCPClientConfig{ID: fmt.Sprintf("client-%d", i), Name: fmt.Sprintf("client%d", i), ToolsToExecute: []string{"*"}}
		m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateHealthy}
	}
	m.mu.Unlock()
	ctx := userContext("alice")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.GetConnectTools(ctx)
	}
}

// The connect tools of a caller who has not connected to the one per-user
// discovery client.
func BenchmarkGetConnectTools_UnconnectedCaller(b *testing.B) {
	perUser := newFakePerUserDiscoveryStore()
	m := NewMCPManager(context.Background(), schemas.MCPConfig{PerUserDiscoveryStore: perUser}, nil, &MockLogger{}, nil)
	config := perUserClient()
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateHealthy}
	m.mu.Unlock()
	ctx := userContext("alice")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.GetConnectTools(ctx)
	}
}
