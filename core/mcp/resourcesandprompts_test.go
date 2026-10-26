package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newResourcesPromptsUpstream is the matrix upstream plus one resource, one resource template and one prompt,
// with list_changed advertised for all three.
func newResourcesPromptsUpstream() *server.MCPServer {
	s := server.NewMCPServer("resources-prompts-upstream", "1.0.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(false, true),
		server.WithPromptCapabilities(true),
		server.WithCompletions(),
		server.WithPromptCompletionProvider(diffCompletions{}),
	)
	s.AddTool(mcpgo.NewTool("echo", mcpgo.WithString("message", mcpgo.Required())),
		func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			msg, _ := req.GetArguments()["message"].(string)
			return mcpgo.NewToolResultText(msg), nil
		})
	s.AddResource(mcpgo.NewResource("file:///handbook.md", "handbook", mcpgo.WithMIMEType("text/markdown")),
		func(context.Context, mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
			return []mcpgo.ResourceContents{mcpgo.TextResourceContents{URI: "file:///handbook.md", Text: "# Handbook"}}, nil
		})
	s.AddResourceTemplate(mcpgo.NewResourceTemplate("repo://{owner}/{name}", "repo"),
		func(_ context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
			return []mcpgo.ResourceContents{mcpgo.TextResourceContents{URI: req.Params.URI, Text: "repo " + req.Params.URI}}, nil
		})
	s.AddPrompt(mcpgo.NewPrompt("review", mcpgo.WithPromptDescription("Review a change"), mcpgo.WithArgument("diff", mcpgo.RequiredArgument())),
		func(_ context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
			return mcpgo.NewGetPromptResult("review", []mcpgo.PromptMessage{
				mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent("Review: "+req.Params.Arguments["diff"])),
			}), nil
		})
	return s
}

// diffCompletions completes the review prompt's diff argument.
type diffCompletions struct{}

func (diffCompletions) CompletePromptArgument(_ context.Context, promptName string, argument mcpgo.CompleteArgument, _ mcpgo.CompleteContext) (*mcpgo.Completion, error) {
	return &mcpgo.Completion{Values: []string{promptName + ":" + argument.Name + ":" + argument.Value + "-done"}}, nil
}

// discoveryRecorder records the discovery each change callback carries.
type discoveryRecorder struct {
	mu    sync.Mutex
	calls []*schemas.MCPDiscovery
}

func (r *discoveryRecorder) record(_, _ string, discovery *schemas.MCPDiscovery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, discovery)
}

func (r *discoveryRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func installedOf(t *testing.T, m *MCPManager, id string) *schemas.MCPDiscovery {
	t.Helper()
	state, ok := snapshotClientState(m, id)
	require.True(t, ok)
	return installedDiscovery(&state)
}

func requireResourcesAndPrompts(t *testing.T, listed *schemas.MCPDiscovery, promptPrefix string) {
	t.Helper()
	require.Len(t, listed.Resources, 1)
	assert.Equal(t, "file:///handbook.md", listed.Resources[0].URI, "resource URIs are the server's own")
	require.Len(t, listed.ResourceTemplates, 1)
	assert.Equal(t, "repo://{owner}/{name}", listed.ResourceTemplates[0].URITemplate.Raw())
	require.Len(t, listed.Prompts, 1)
	assert.Equal(t, promptPrefix+"-review", listed.Prompts[0].Name, "prompts are prefixed like tools")
	require.Len(t, listed.Prompts[0].Arguments, 1)
}

// Resources, resource templates and prompts are listed alongside tools on every protocol revision,
// over a shared connection and per-call discovery alike, and the change callback fires once.
func TestResourcesAndPrompts_Discovery(t *testing.T) {
	for _, version := range []string{mcpgo.ProtocolVersion20250618, mcpgo.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			ts := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream(), server.WithStreamableHTTPProtocolVersions(version)))
			t.Cleanup(ts.Close)

			for _, tc := range []struct {
				name   string
				sticky bool
			}{{"shared", true}, {"per-call", false}} {
				t.Run(tc.name, func(t *testing.T) {
					m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
					recorder := &discoveryRecorder{}
					m.SetDiscoveryChangeCallback(recorder.record)
					config := &schemas.MCPClientConfig{
						ID: "listed-" + tc.name, Name: "docs", AuthType: schemas.MCPAuthTypeHeaders, ConnectionType: schemas.MCPConnectionTypeHTTP,
						ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(tc.sticky),
					}
					require.NoError(t, m.AddClient(context.Background(), config))
					t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

					requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "docs")
					assert.Equal(t, 1, recorder.count(), "the first discovery is a change")

					_, err := m.RefreshClientTools(context.Background(), config.ID)
					require.NoError(t, err)
					requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "docs")
					assert.Equal(t, 1, recorder.count(), "an identical rediscovery fires nothing")
				})
			}
		})
	}
}

// A tools-only server costs nothing extra: no resources/ or prompts/ request, no lists.
func TestResourcesAndPrompts_ToolsOnlyServerSendsNoRequests(t *testing.T) {
	counter := newWireCounter()
	ts := httptest.NewServer(counter.wrap(server.NewStreamableHTTPServer(newMatrixUpstream())))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "tools-only", Name: "matrix", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	_, err := m.RefreshClientTools(context.Background(), config.ID)
	require.NoError(t, err)

	installed := installedOf(t, m, config.ID)
	assert.Nil(t, installed.Resources)
	assert.Nil(t, installed.ResourceTemplates)
	assert.Nil(t, installed.Prompts)
	_, methods := counter.discovery()
	for method := range methods {
		assert.False(t, strings.HasPrefix(method, "resources/") || strings.HasPrefix(method, "prompts/"), "unexpected %s", method)
	}
}

// A resources or prompts list that fails keeps what was listed before instead of emptying it.
func TestResourcesAndPrompts_FailedListKeepsPrevious(t *testing.T) {
	var failResources atomic.Bool
	ts := newFailingResourcesUpstream(t, &failResources)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "flaky", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "docs")

	failResources.Store(true)
	_, err := m.RefreshClientTools(context.Background(), config.ID)
	require.NoError(t, err, "a resources failure never fails tool discovery")
	requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "docs")
}

// After a restart, a persistent client's first connect that fails a list keeps the saved list.
func TestResourcesAndPrompts_FailedFirstListKeepsSaved(t *testing.T) {
	var failResources atomic.Bool
	failResources.Store(true)
	ts := newFailingResourcesUpstream(t, &failResources)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	saved := []mcpgo.Resource{mcpgo.NewResource("file:///saved.md", "saved")}
	config := &schemas.MCPClientConfig{
		ID: "restarted", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
		DiscoveredResources: saved,
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	assert.Equal(t, saved, installedOf(t, m, config.ID).Resources)
}

// newFailingResourcesUpstream serves the resources-and-prompts upstream, failing resources/list while fail is set.
func newFailingResourcesUpstream(t *testing.T, fail *atomic.Bool) *httptest.Server {
	t.Helper()
	upstream := server.NewStreamableHTTPServer(newResourcesPromptsUpstream())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(body, &msg)
			if msg.Method == "resources/list" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(msg.ID) + `,"error":{"code":-32603,"message":"backend down"}}`))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// An upstream that adds a prompt or a resource tells listening clients, and the lists follow
// without waiting for the checker's interval.
func TestResourcesAndPrompts_ListChangedRefreshes(t *testing.T) {
	for _, version := range []string{mcpgo.ProtocolVersion20250618, mcpgo.ProtocolVersion20260728} {
		t.Run(version, func(t *testing.T) {
			upstream := newResourcesPromptsUpstream()
			ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream, server.WithStreamableHTTPProtocolVersions(version)))
			t.Cleanup(ts.Close)
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			config := &schemas.MCPClientConfig{
				ID: "live", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
			}
			require.NoError(t, m.AddClient(context.Background(), config))
			t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
			requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "docs")

			upstream.AddPrompt(mcpgo.NewPrompt("summarize"), func(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
				return mcpgo.NewGetPromptResult("summarize", nil), nil
			})
			assert.Eventually(t, func() bool { return len(installedOf(t, m, config.ID).Prompts) == 2 }, 5*time.Second, 20*time.Millisecond, "prompts/list_changed")

			upstream.AddResource(mcpgo.NewResource("file:///faq.md", "faq"), func(context.Context, mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
				return nil, nil
			})
			assert.Eventually(t, func() bool { return len(installedOf(t, m, config.ID).Resources) == 2 }, 5*time.Second, 20*time.Millisecond, "resources/list_changed")
		})
	}
}

// Renaming a client re-prefixes its prompts; resource URIs are untouched.
func TestResourcesAndPrompts_RenameReprefixesPrompts(t *testing.T) {
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream()))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "renamed", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	renamed := *config
	renamed.Name = "handbook"
	require.NoError(t, m.UpdateClient(config.ID, &renamed))
	requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "handbook")
}

// listGatePipeline records the request types its pre-hooks see and, when set, short-circuits list_prompts.
type listGatePipeline struct {
	mu                sync.Mutex
	seen              []schemas.MCPRequestType
	answerListPrompts bool
}

func (p *listGatePipeline) RunMCPPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	p.mu.Lock()
	p.seen = append(p.seen, req.RequestType)
	p.mu.Unlock()
	if p.answerListPrompts && req.RequestType == schemas.MCPRequestTypeListPrompts {
		return req, &schemas.MCPPluginShortCircuit{Response: &schemas.BifrostMCPResponse{
			BifrostMCPListPromptsResponse: &schemas.BifrostMCPListPromptsResponse{Prompts: []mcpgo.Prompt{mcpgo.NewPrompt("docs-synthetic")}},
		}}, 0
	}
	return req, nil, 0
}

func (p *listGatePipeline) RunMCPPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, err *schemas.BifrostError, _ int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	return resp, err
}

func (p *listGatePipeline) RunMCPPreConnectionHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, int) {
	return req, nil, 0
}

func (p *listGatePipeline) RunMCPPostConnectionHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPConnectResponse, err *schemas.BifrostError, _ int) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError) {
	return resp, err
}

// Each resources and prompts list runs through the MCP plugin gate under its own request type, so a plugin can
// observe it or answer it without the upstream.
func TestResourcesAndPrompts_ListsRunThroughPluginGate(t *testing.T) {
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream()))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	pipeline := &listGatePipeline{answerListPrompts: true}
	m.pluginPipelineProvider = func() PluginPipeline { return pipeline }
	m.releasePluginPipeline = func(PluginPipeline) {}
	config := &schemas.MCPClientConfig{
		ID: "gated", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	pipeline.mu.Lock()
	seen := slices.Clone(pipeline.seen)
	pipeline.mu.Unlock()
	assert.Subset(t, seen, []schemas.MCPRequestType{schemas.MCPRequestTypeListTools, schemas.MCPRequestTypeListResources, schemas.MCPRequestTypeListResourceTemplates, schemas.MCPRequestTypeListPrompts})
	installed := installedOf(t, m, config.ID)
	require.Len(t, installed.Prompts, 1)
	assert.Equal(t, "docs-synthetic", installed.Prompts[0].Name, "the plugin's answer is used")
	assert.Len(t, installed.Resources, 1, "an un-short-circuited list still reaches the upstream")
}

// With a per-list budget, the lists do not inherit a caller deadline that tools/list already spent.
func TestResourcesAndPrompts_PerListBudgetOutlivesSpentDeadline(t *testing.T) {
	upstream := server.NewStreamableHTTPServer(newResourcesPromptsUpstream())
	var slow atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if bytes.Contains(body, []byte(`"resources/list"`)) || bytes.Contains(body, []byte(`"prompts/list"`)) {
				time.Sleep(150 * time.Millisecond)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		upstream.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "slow", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	require.NotNil(t, state.Conn)

	slow.Store(true)
	spent, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	discovery := &schemas.MCPDiscovery{}
	m.listResourcesAndPrompts(spent, 2*time.Second, state.Conn, config.Name, state.Conn.GetServerCapabilities(), discovery, nil)
	requireResourcesAndPrompts(t, discovery, "docs")
}

// A server offering only prompts has no tools/list to probe, so its prompts list is the liveness check:
// once the upstream dies, the checker marks the client unstable instead of keeping it healthy.
func TestResourcesAndPrompts_PromptsOnlyServerLivenessFollowsPromptsList(t *testing.T) {
	upstream := server.NewMCPServer("prompts-only", "1.0.0", server.WithPromptCapabilities(true))
	upstream.AddPrompt(mcpgo.NewPrompt("review"), func(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
		return mcpgo.NewGetPromptResult("review", nil), nil
	})
	handler := server.NewStreamableHTTPServer(upstream)
	var dead atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dead.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID: "prompts-only", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	require.Len(t, installedOf(t, m, config.ID).Prompts, 1)

	dead.Store(true)
	NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{}).performCheck()
	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, schemas.MCPConnectionStateUnstable, state.State, "a dead prompts-only upstream must not stay healthy")
}

// newToollessUpstream serves a server without tools, offering a resource and a template, a prompt, or
// both. A method fail reports answers with a JSON-RPC error.
func newToollessUpstream(t *testing.T, resources, prompts bool, fail func(method string) bool) *httptest.Server {
	t.Helper()
	var opts []server.ServerOption
	if resources {
		opts = append(opts, server.WithResourceCapabilities(false, false))
	}
	if prompts {
		opts = append(opts, server.WithPromptCapabilities(false))
	}
	upstream := server.NewMCPServer("toolless", "1.0.0", opts...)
	if resources {
		upstream.AddResource(mcpgo.NewResource("file:///handbook.md", "handbook"), func(context.Context, mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
			return []mcpgo.ResourceContents{mcpgo.TextResourceContents{URI: "file:///handbook.md", Text: "# Handbook"}}, nil
		})
		upstream.AddResourceTemplate(mcpgo.NewResourceTemplate("repo://{name}", "repo"), func(_ context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
			return []mcpgo.ResourceContents{mcpgo.TextResourceContents{URI: req.Params.URI, Text: "repo"}}, nil
		})
	}
	if prompts {
		upstream.AddPrompt(mcpgo.NewPrompt("review"), func(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
			return mcpgo.NewGetPromptResult("review", nil), nil
		})
	}
	handler := server.NewStreamableHTTPServer(upstream)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(body, &msg)
			if fail(msg.Method) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(msg.ID) + `,"error":{"code":-32603,"message":"backend down"}}`))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// A server without tools has no tools/list to prove it works, so when every one of its lists fails it
// neither verifies nor connects, as a server whose tools/list fails does not.
func TestResourcesAndPrompts_ToollessServerWithFailingListsIsNotHealthy(t *testing.T) {
	ts := newToollessUpstream(t, false, true, func(method string) bool { return method == "prompts/list" })
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID: "toolless", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP, AuthType: schemas.MCPAuthTypeHeaders,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	_, err := m.VerifyHeadersConnection(context.Background(), config, nil)
	require.Error(t, err, "verification must fail when nothing the server offers can be listed")

	require.Error(t, m.connectToMCPClient(context.Background(), config), "connecting must fail too")
	state, ok := snapshotClientState(m, config.ID)
	if ok {
		assert.NotEqual(t, schemas.MCPConnectionStateHealthy, state.State)
	}
}

// A resources-only server whose resources/list fails but whose templates list answers is alive: the
// check keeps it connected instead of replacing a working connection.
func TestResourcesAndPrompts_TemplatesAnsweringKeepsServerHealthy(t *testing.T) {
	var failResources atomic.Bool
	ts := newToollessUpstream(t, true, false, func(method string) bool { return failResources.Load() && method == "resources/list" })
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := &schemas.MCPClientConfig{
		ID: "resources-only", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.connectToMCPClient(context.Background(), config))

	failResources.Store(true)
	NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{}).performCheck()
	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, schemas.MCPConnectionStateHealthy, state.State, "the templates list answered, so the server is alive")
}

// swallowingPipeline's post-hooks drop a failed call's error without supplying a response.
type swallowingPipeline struct{ listGatePipeline }

func (p *swallowingPipeline) RunMCPPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	return req, nil, 0
}

func (p *swallowingPipeline) RunMCPPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, err *schemas.BifrostError, _ int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	if err != nil {
		return nil, nil
	}
	return resp, nil
}

// A post-hook that drops a failed list's error without a response does not empty the kept list.
func TestResourcesAndPrompts_SwallowedFailureKeepsPrevious(t *testing.T) {
	var failResources atomic.Bool
	ts := newFailingResourcesUpstream(t, &failResources)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	pipeline := &swallowingPipeline{}
	m.pluginPipelineProvider = func() PluginPipeline { return pipeline }
	m.releasePluginPipeline = func(PluginPipeline) {}
	config := &schemas.MCPClientConfig{
		ID: "swallowed", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "docs")

	failResources.Store(true)
	_, err := m.RefreshClientTools(context.Background(), config.ID)
	require.NoError(t, err)
	requireResourcesAndPrompts(t, installedOf(t, m, config.ID), "docs")
}

// A discovery installed from outside (a cluster peer's stored result) carries its resources and
// prompts to the client's state and config.
func TestResourcesAndPrompts_SetClientDiscoveryCarriesLists(t *testing.T) {
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newMatrixUpstream()))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "replayed", Name: "docs", AuthType: schemas.MCPAuthTypeHeaders, ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"},
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	recorder := &discoveryRecorder{}
	m.SetDiscoveryChangeCallback(recorder.record)

	stored := []mcpgo.Prompt{mcpgo.NewPrompt("docs-review")}
	m.SetClientDiscovery(config.ID, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, Prompts: stored})
	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, stored, state.Prompts)
	assert.Equal(t, stored, state.ExecutionConfig.DiscoveredPrompts)
	require.Equal(t, 1, recorder.count())
	assert.Equal(t, stored, recorder.calls[0].Prompts)

	m.SetClientDiscovery("missing", &schemas.MCPDiscovery{Prompts: stored})
}

// Prompts, resources and completions are served from the client that owns them, over a shared
// connection and per-call alike, and each call runs through the plugin gate under its own type.
func TestResourcesAndPrompts_ServedFromOwner(t *testing.T) {
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream()))
	t.Cleanup(ts.Close)
	for _, sticky := range []bool{true, false} {
		t.Run(map[bool]string{true: "shared", false: "per-call"}[sticky], func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			pipeline := &listGatePipeline{}
			m.pluginPipelineProvider = func() PluginPipeline { return pipeline }
			m.releasePluginPipeline = func(PluginPipeline) {}
			config := &schemas.MCPClientConfig{
				ID: "served", Name: "docs", AuthType: schemas.MCPAuthTypeHeaders, ConnectionType: schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(sticky),
			}
			require.NoError(t, m.AddClient(context.Background(), config))
			t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

			require.Len(t, m.GetAvailablePrompts(ctx), 1)
			require.Len(t, m.GetAvailableResources(ctx), 1)
			require.Len(t, m.GetAvailableResourceTemplates(ctx), 1)

			prompt, bErr := m.GetPrompt(ctx, "docs-review", map[string]string{"diff": "+1"})
			require.Nil(t, bErr)
			require.Len(t, prompt.Messages, 1)
			assert.Equal(t, "Review: +1", prompt.Messages[0].Content.(mcpgo.TextContent).Text, "the upstream sees its own prompt name")

			resource, bErr := m.ReadResource(ctx, "file:///handbook.md")
			require.Nil(t, bErr)
			assert.Equal(t, "# Handbook", resource.Contents[0].(mcpgo.TextResourceContents).Text)

			fromTemplate, bErr := m.ReadResource(ctx, "repo://acme/widgets")
			require.Nil(t, bErr)
			assert.Equal(t, "repo repo://acme/widgets", fromTemplate.Contents[0].(mcpgo.TextResourceContents).Text)

			completion, bErr := m.Complete(ctx, mcpgo.CompleteParams{
				Ref:      mcpgo.PromptReference{Type: "ref/prompt", Name: "docs-review"},
				Argument: mcpgo.CompleteArgument{Name: "diff", Value: "+"},
			})
			require.Nil(t, bErr)
			assert.Equal(t, []string{"review:diff:+-done"}, completion.Completion.Values)

			_, bErr = m.GetPrompt(ctx, "docs-missing", nil)
			require.NotNil(t, bErr)
			_, bErr = m.ReadResource(ctx, "file:///missing.md")
			require.NotNil(t, bErr)

			pipeline.mu.Lock()
			seen := slices.Clone(pipeline.seen)
			pipeline.mu.Unlock()
			assert.Subset(t, seen, []schemas.MCPRequestType{schemas.MCPRequestTypeGetPrompt, schemas.MCPRequestTypeReadResource, schemas.MCPRequestTypeComplete})
		})
	}
}

// A caller narrowed away from a client sees none of its prompts or resources and cannot fetch them.
func TestResourcesAndPrompts_NarrowedCallerCannotReach(t *testing.T) {
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream()))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "narrowed", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	for name, ctx := range map[string]*schemas.BifrostContext{
		"other client": schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(schemas.MCPContextKeyIncludeClients, []string{"other"}),
		"other tools":  schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(schemas.MCPContextKeyIncludeTools, []string{"other-*"}),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, m.GetAvailablePrompts(ctx))
			assert.Empty(t, m.GetAvailableResources(ctx))
			assert.Empty(t, m.GetAvailableResourceTemplates(ctx))
			_, bErr := m.GetPrompt(ctx, "docs-review", nil)
			assert.NotNil(t, bErr)
			_, bErr = m.ReadResource(ctx, "file:///handbook.md")
			assert.NotNil(t, bErr)
			completion, bErr := m.Complete(ctx, mcpgo.CompleteParams{Ref: mcpgo.PromptReference{Type: "ref/prompt", Name: "docs-review"}})
			require.Nil(t, bErr)
			assert.Empty(t, completion.Completion.Values)
		})
	}
}

// An upstream that offers prompts but not completions completes to nothing instead of failing.
func TestResourcesAndPrompts_CompleteWithoutUpstreamCompletions(t *testing.T) {
	upstream := server.NewMCPServer("no-completions", "1.0.0", server.WithToolCapabilities(true), server.WithPromptCapabilities(false))
	upstream.AddTool(mcpgo.NewTool("echo"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText(""), nil
	})
	upstream.AddPrompt(mcpgo.NewPrompt("review"), func(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
		return mcpgo.NewGetPromptResult("review", nil), nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "no-completions", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	completion, bErr := m.Complete(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), mcpgo.CompleteParams{
		Ref:      mcpgo.PromptReference{Type: "ref/prompt", Name: "docs-review"},
		Argument: mcpgo.CompleteArgument{Name: "diff", Value: "+"},
	})
	require.Nil(t, bErr, "%+v", bErr.GetErrorString())
	assert.Empty(t, completion.Completion.Values)
}

// A server with prompts and resources but no tools is served to any caller that may reach it.
func TestResourcesAndPrompts_ServerWithoutToolsIsServed(t *testing.T) {
	upstream := server.NewMCPServer("no-tools", "1.0.0", server.WithPromptCapabilities(false), server.WithResourceCapabilities(false, false))
	upstream.AddPrompt(mcpgo.NewPrompt("review"), func(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
		return mcpgo.NewGetPromptResult("review", nil), nil
	})
	upstream.AddResource(mcpgo.NewResource("file:///handbook.md", "handbook"), func(context.Context, mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		return []mcpgo.ResourceContents{mcpgo.TextResourceContents{URI: "file:///handbook.md", Text: "# Handbook"}}, nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "no-tools", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	for name, tc := range map[string]struct {
		ctx     *schemas.BifrostContext
		reaches bool
	}{
		"unrestricted":       {schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), true},
		"granted the server": {schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(schemas.MCPContextKeyIncludeTools, []string{"docs-*"}), true},
		"granted another":    {schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(schemas.MCPContextKeyIncludeTools, []string{"other-*"}), false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.reaches, len(m.GetAvailablePrompts(tc.ctx)) == 1)
			assert.Equal(t, tc.reaches, len(m.GetAvailableResources(tc.ctx)) == 1)
			_, bErr := m.ReadResource(tc.ctx, "file:///handbook.md")
			assert.Equal(t, tc.reaches, bErr == nil)
		})
	}
}

// The per-server allow-lists decide which prompts, resources and templates are listed and reachable.
func TestResourcesAndPrompts_AllowListsNarrowWhatIsServed(t *testing.T) {
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream()))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "allow-lists", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
		PromptsToExpose: schemas.WhiteList{}, ResourcesToExpose: schemas.WhiteList{"repo://{owner}/{name}"},
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	assert.Empty(t, m.GetAvailablePrompts(ctx), "[] exposes no prompts")
	_, bErr := m.GetPrompt(ctx, "docs-review", nil)
	assert.NotNil(t, bErr)
	assert.Empty(t, m.GetAvailableResources(ctx), "the handbook is not on the list")
	_, bErr = m.ReadResource(ctx, "file:///handbook.md")
	assert.NotNil(t, bErr)
	require.Len(t, m.GetAvailableResourceTemplates(ctx), 1)
	_, bErr = m.ReadResource(ctx, "repo://acme/widgets")
	assert.Nil(t, bErr, "a listed template is readable")
	completion, bErr := m.Complete(ctx, mcpgo.CompleteParams{Ref: mcpgo.PromptReference{Type: "ref/prompt", Name: "docs-review"}, Argument: mcpgo.CompleteArgument{Name: "diff"}})
	require.Nil(t, bErr)
	assert.Empty(t, completion.Completion.Values, "a hidden prompt completes to nothing")
}

// A prompt or resource call on a session the upstream abandoned reconnects and is retried, as a tool call is.
func TestResourcesAndPrompts_DeadSessionIsRecovered(t *testing.T) {
	streamable := server.NewStreamableHTTPServer(newResourcesPromptsUpstream(), server.WithStreamableHTTPProtocolVersions(mcpgo.ProtocolVersion20251125))
	invalidator := &sessionInvalidator{dead: map[string]bool{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invalidator.reject(w, r) {
			return
		}
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := newRecoveryClientConfig("resources-dead-session", "docs", ts.URL)
	require.NoError(t, m.connectToMCPClient(context.Background(), config))
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bErr := m.GetPrompt(ctx, "docs-review", map[string]string{"diff": "+1"})
	require.Nil(t, bErr, "sanity: the prompt works before the session dies")

	invalidator.expireCurrentSession()
	_, bErr = m.GetPrompt(ctx, "docs-review", map[string]string{"diff": "+1"})
	require.Nil(t, bErr, "a dead session must be reconnected and the prompt fetched again")

	invalidator.expireCurrentSession()
	_, bErr = m.ReadResource(ctx, "file:///handbook.md")
	require.Nil(t, bErr, "a dead session must be reconnected and the resource read again")
}

// Catalogs split across pages are followed to the end, for all three lists.
func TestResourcesAndPrompts_FollowsPagination(t *testing.T) {
	const total = 5
	s := server.NewMCPServer("paged-upstream", "1.0.0",
		server.WithToolCapabilities(false), server.WithResourceCapabilities(false, false), server.WithPromptCapabilities(false),
		server.WithPaginationLimit(2))
	for i := range total {
		uri := fmt.Sprintf("file:///doc%d.md", i)
		s.AddResource(mcpgo.NewResource(uri, fmt.Sprintf("doc%d", i)),
			func(context.Context, mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) { return nil, nil })
		s.AddResourceTemplate(mcpgo.NewResourceTemplate(fmt.Sprintf("repo%d://{name}", i), fmt.Sprintf("repo%d", i)),
			func(context.Context, mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) { return nil, nil })
		s.AddPrompt(mcpgo.NewPrompt(fmt.Sprintf("prompt%d", i)),
			func(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) { return nil, nil })
	}
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "paged", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	listed := installedOf(t, m, config.ID)
	assert.Len(t, listed.Resources, total)
	assert.Len(t, listed.ResourceTemplates, total)
	assert.Len(t, listed.Prompts, total)
}

// A per-call client with no tools still restores its saved resources and prompts at startup, so an
// unreachable upstream does not wipe them.
func TestResourcesAndPrompts_ZeroToolPerCallClientRestoresSavedLists(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	saved := []mcpgo.Prompt{mcpgo.NewPrompt("docs-review")}
	savedResources := []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")}
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "zero-tools", Name: "docs", AuthType: schemas.MCPAuthTypeHeaders, ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(down.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(false),
		DiscoveredTools: map[string]schemas.ChatTool{}, DiscoveredPrompts: saved, DiscoveredResources: savedResources,
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	listed := installedOf(t, m, config.ID)
	assert.Equal(t, saved, listed.Prompts)
	assert.Equal(t, savedResources, listed.Resources)
}

// An update that re-ran verification replaces the stored resources and prompts along with the tools.
func TestResourcesAndPrompts_VerifiedUpdateReplacesLists(t *testing.T) {
	ts := httptest.NewServer(server.NewStreamableHTTPServer(newResourcesPromptsUpstream()))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "reverified", Name: "docs", AuthType: schemas.MCPAuthTypeHeaders, ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"},
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	verified := *config
	verified.DiscoveredTools = map[string]schemas.ChatTool{}
	verified.DiscoveredPrompts = []mcpgo.Prompt{mcpgo.NewPrompt("docs-fresh")}
	verified.DiscoveredResources = []mcpgo.Resource{mcpgo.NewResource("file:///fresh.md", "fresh")}
	verified.DiscoveredResourceTemplates = []mcpgo.ResourceTemplate{}
	require.NoError(t, m.UpdateClient(config.ID, &verified))

	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	assert.Equal(t, verified.DiscoveredPrompts, state.ExecutionConfig.DiscoveredPrompts)
	assert.Equal(t, verified.DiscoveredResources, state.ExecutionConfig.DiscoveredResources)
	assert.Empty(t, state.ExecutionConfig.DiscoveredResourceTemplates)
}

// Editing a client that never had a discovery fires no discovery change: persisting an empty one
// would mark an unverified per_user_headers client as verified.
func TestResourcesAndPrompts_EditOfUndiscoveredClientFiresNothing(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	recorder := &discoveryRecorder{}
	m.SetDiscoveryChangeCallback(recorder.record)
	config := &schemas.MCPClientConfig{
		ID: "unverified", Name: "docs", AuthType: schemas.MCPAuthTypePerUserHeaders, PerUserHeaderKeys: []string{"X-User-Token"},
		ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"), ToolsToExecute: []string{"*"},
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	renamed := *config
	renamed.Name = "handbook"
	require.NoError(t, m.UpdateClient(config.ID, &renamed))
	assert.Equal(t, 0, recorder.count())
}

// GetClients hands out its own copies of the discovered lists, so a caller that sorts or edits one
// cannot change what the manager serves.
func TestGetClients_ReturnsOwnedDiscoveryLists(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	m.clientMap["docs-id"] = &schemas.MCPClientState{
		Name:              "docs",
		Resources:         []mcpgo.Resource{mcpgo.NewResource("file:///b.md", "b"), mcpgo.NewResource("file:///a.md", "a")},
		ResourceTemplates: []mcpgo.ResourceTemplate{mcpgo.NewResourceTemplate("repo://{name}", "repo")},
		Prompts:           []mcpgo.Prompt{mcpgo.NewPrompt("docs-review")},
	}
	got := m.GetClients()
	require.Len(t, got, 1)
	got[0].Resources[0] = mcpgo.NewResource("file:///edited.md", "edited")
	got[0].ResourceTemplates[0].Name = "edited"
	got[0].Prompts[0].Name = "edited"

	held := m.clientMap["docs-id"]
	assert.Equal(t, "file:///b.md", held.Resources[0].URI)
	assert.Equal(t, "repo", held.ResourceTemplates[0].Name)
	assert.Equal(t, "docs-review", held.Prompts[0].Name)
}

// stampingPipeline's pre-hook narrows the request to stamped tools, as governance does for a read.
type stampingPipeline struct {
	listGatePipeline
	tools []string
}

func (p *stampingPipeline) RunMCPPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, p.tools)
	return req, nil, 0
}

// A read picks its server before the pre-hooks run; a hook that narrows the request to other servers
// (governance, for a Go caller that stamped nothing) must stop it before the server is contacted.
func TestResourcesAndPrompts_ReadRecheckedAfterPreHooksNarrowAccess(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	m.pluginPipelineProvider = func() PluginPipeline { return &stampingPipeline{tools: []string{"allowed-search"}} }
	m.releasePluginPipeline = func(PluginPipeline) {}
	m.clientMap["allowed-id"] = &schemas.MCPClientState{
		Name:            "allowed",
		ExecutionConfig: &schemas.MCPClientConfig{ID: "allowed-id", Name: "allowed"},
		ToolMap:         map[string]schemas.ChatTool{"allowed-search": {}},
	}
	m.clientMap["denied-id"] = &schemas.MCPClientState{
		Name:            "denied",
		ExecutionConfig: &schemas.MCPClientConfig{ID: "denied-id", Name: "denied"},
		ToolMap:         map[string]schemas.ChatTool{"denied-search": {}},
		Prompts:         []mcpgo.Prompt{mcpgo.NewPrompt("denied-review")},
		Resources:       []mcpgo.Resource{mcpgo.NewResource("file:///secret.md", "secret")},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bErr := m.GetPrompt(ctx, "denied-review", nil)
	require.NotNil(t, bErr)
	assert.ErrorIs(t, bErr.Error.Error, ErrNotFound, "prompt: %s", bErr.Error.Message)
	_, bErr = m.ReadResource(ctx, "file:///secret.md")
	require.NotNil(t, bErr)
	assert.ErrorIs(t, bErr.Error.Error, ErrNotFound, "resource: %s", bErr.Error.Message)
}

// servedByPipeline stamps tools in its pre-hook and records which server the response names.
type servedByPipeline struct {
	stampingPipeline
	servedBy string
}

func (p *servedByPipeline) RunMCPPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, err *schemas.BifrostError, _ int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	if resp != nil {
		p.servedBy = resp.ExtraFields.ClientName
	}
	return resp, err
}

// Two servers expose the same URI and the request may reach only the second: the read goes to the
// second instead of failing on the first, and the response names the server that answered.
func TestResourcesAndPrompts_SharedURIReadGoesToTheReachableServer(t *testing.T) {
	ts := newToollessUpstream(t, true, false, func(string) bool { return false })
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	require.NoError(t, m.connectToMCPClient(context.Background(), &schemas.MCPClientConfig{
		ID: "b-id", Name: "b", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}))
	// "a" sorts first and lists the same URI, but the request may not reach it.
	m.clientMap["a-id"] = &schemas.MCPClientState{
		Name:            "a",
		ExecutionConfig: &schemas.MCPClientConfig{ID: "a-id", Name: "a"},
		ToolMap:         map[string]schemas.ChatTool{},
		Resources:       []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")},
	}
	pipeline := &servedByPipeline{stampingPipeline: stampingPipeline{tools: []string{"b-*"}}}
	m.pluginPipelineProvider = func() PluginPipeline { return pipeline }
	m.releasePluginPipeline = func(PluginPipeline) {}

	result, bErr := m.ReadResource(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), "file:///handbook.md")
	require.Nil(t, bErr, "the read the request may make must not fail: %v", bErr)
	require.Len(t, result.Contents, 1)
	assert.Equal(t, "# Handbook", result.Contents[0].(mcpgo.TextResourceContents).Text)
	assert.Equal(t, "b", pipeline.servedBy, "the response names the server that answered")
}
