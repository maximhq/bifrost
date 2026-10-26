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
		func(context.Context, mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) { return nil, nil })
	s.AddPrompt(mcpgo.NewPrompt("review", mcpgo.WithPromptDescription("Review a change"), mcpgo.WithArgument("diff", mcpgo.RequiredArgument())),
		func(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
			return mcpgo.NewGetPromptResult("review", nil), nil
		})
	return s
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

// listGatePipeline records the request types its pre-hooks see and short-circuits list_prompts.
type listGatePipeline struct {
	mu   sync.Mutex
	seen []schemas.MCPRequestType
}

func (p *listGatePipeline) RunMCPPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	p.mu.Lock()
	p.seen = append(p.seen, req.RequestType)
	p.mu.Unlock()
	if req.RequestType == schemas.MCPRequestTypeListPrompts {
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
	pipeline := &listGatePipeline{}
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
