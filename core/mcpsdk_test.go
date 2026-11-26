package bifrost

// These tests use core the way a Go SDK caller does: Init, then the MCP client and tool-execution
// methods on *Bifrost. The upstream MCP servers are started by the tests themselves:
// alpha, a streamable-HTTP server with echo_tool and add_tool that counts tool calls and sessions;
// in-process mcp-go servers; OpenAPI clients served by a test factory; and a STDIO server that is this
// test binary run again. Each case is a subtest named by its test ID.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
)

// stdioServerEnv names the variable that turns this test binary into the STDIO MCP server; its value
// is the file each server process appends its pid to.
const stdioServerEnv = "BIFROST_MCP_SDK_STDIO_PID_FILE"

// TestMCPSDK runs each case of core used as a Go SDK with MCP.
func TestMCPSDK(t *testing.T) {
	t.Run("GO-19 STDIO respawn", testSDKSTDIORespawn)
	t.Run("GO-20 connect gating", testSDKConnectGating)
	t.Run("GO-21 chat and Responses execution", testSDKExecution)
	t.Run("GO-22 allow-list default", testSDKAllowListDefault)
	t.Run("GO-23 without MCP config", testSDKWithoutMCPConfig)
	t.Run("GO-24 RegisterMCPTool", testSDKRegisterMCPTool)
	t.Run("GO-25 plugin short-circuit", testSDKPluginShortCircuit)
	t.Run("GO-26 RemoveMCPClient", testSDKRemoveMCPClient)
	t.Run("GO-27 in-process client", testSDKInProcessClient)
	t.Run("GO-28 in-process client with no server", testSDKInProcessNoServer)
	t.Run("GO-29 OpenAPI client without a factory", testSDKOpenAPINoFactory)
	t.Run("GO-30 OpenAPI client with a factory", testSDKOpenAPIFactory)
	t.Run("GO-31 factory returns nil", testSDKOpenAPIFactoryNil)
	t.Run("GO-32 factory set after Init", testSDKOpenAPIFactoryAfterInit)
	t.Run("GO-37 concurrency on a sticky client", testSDKConcurrency)
	t.Run("CONN-26 in-process result types", testSDKInProcessResults)
}

// TestMCPSDKSTDIOServer is not a test on its own: the STDIO case runs this binary again with
// stdioServerEnv set, and this function then serves alpha's tools over stdin and stdout after
// appending its pid to the named file, until the parent closes the pipe or kills the process.
func TestMCPSDKSTDIOServer(t *testing.T) {
	pidFile := os.Getenv(stdioServerEnv)
	if pidFile == "" {
		t.Skip("runs only as the STDIO server for TestMCPSDK")
	}
	f, err := os.OpenFile(pidFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	_ = f.Close()
	_ = server.ServeStdio(newAlphaMCPServer(nil))
	os.Exit(0)
}

// alphaServer is the alpha upstream over streamable HTTP, with what it saw.
type alphaServer struct {
	url         string
	mu          sync.Mutex
	calls       map[string]int
	handshakes  atomic.Int64 // connection handshakes: initialize, or server/discover on the modern protocol
}

// newAlphaServer starts alpha on a local port for the length of the test.
func newAlphaServer(t *testing.T) *alphaServer {
	t.Helper()
	a := &alphaServer{calls: map[string]int{}}
	streamable := server.NewStreamableHTTPServer(newAlphaMCPServer(a))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &msg) == nil && (msg.Method == "initialize" || msg.Method == "server/discover") {
				a.handshakes.Add(1)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		streamable.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	a.url = ts.URL + "/mcp"
	return a
}

// callCount reports how many times alpha ran tool.
func (a *alphaServer) callCount(tool string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[tool]
}

// newAlphaMCPServer builds alpha's tools: echo_tool returns its text argument and add_tool returns
// a+b. Calls are counted on a when it is not nil.
func newAlphaMCPServer(a *alphaServer) *server.MCPServer {
	s := server.NewMCPServer("alpha", "1.0.0", server.WithToolCapabilities(true))
	count := func(tool string) {
		if a != nil {
			a.mu.Lock()
			a.calls[tool]++
			a.mu.Unlock()
		}
	}
	s.AddTool(mcpgo.NewTool("echo_tool", mcpgo.WithString("text", mcpgo.Required())), func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		count("echo_tool")
		text, _ := req.GetArguments()["text"].(string)
		return mcpgo.NewToolResultText(text), nil
	})
	s.AddTool(mcpgo.NewTool("add_tool", mcpgo.WithNumber("a"), mcpgo.WithNumber("b")), func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		count("add_tool")
		args := req.GetArguments()
		x, _ := args["a"].(float64)
		y, _ := args["b"].(float64)
		return mcpgo.NewToolResultText(strconv.FormatFloat(x+y, 'f', -1, 64)), nil
	})
	return s
}

// alphaClient is an HTTP client named alpha on url, auth none, per call, allowed every tool.
func alphaClient(url string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:               "alpha-id",
		Name:             "alpha",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(url),
		AuthType:         schemas.MCPAuthTypeNone,
		ToolsToExecute:   schemas.WhiteList{"*"},
	}
}

// newSDKClient initializes Bifrost with cfg, filling in an account that configures mcpClients and
// a logger, and shuts it down when the test ends.
func newSDKClient(t *testing.T, cfg schemas.BifrostConfig, mcpClients ...*schemas.MCPClientConfig) *Bifrost {
	t.Helper()
	if cfg.Account == nil {
		account := NewMockAccount()
		account.AddMCPClients(mcpClients...)
		cfg.Account = account
	} else if len(mcpClients) > 0 {
		t.Fatal("newSDKClient: MCP clients come from the default account; add them to cfg.Account instead")
	}
	if cfg.Logger == nil {
		cfg.Logger = NewNoOpLogger()
	}
	b, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b
}

// connectedAlpha initializes Bifrost with alpha as its one configured client, connected.
func connectedAlpha(t *testing.T, a *alphaServer, edit func(*schemas.MCPClientConfig)) *Bifrost {
	t.Helper()
	client := alphaClient(a.url)
	if edit != nil {
		edit(client)
	}
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}}, client)
	b.ConnectConfiguredMCPClients(context.Background(), nil)
	return b
}

// newCtx is a request context with no deadline.
func newCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

// execChat runs tool with JSON arguments args through ExecuteChatMCPTool.
func execChat(b *Bifrost, tool, args string) (*schemas.ChatMessage, *schemas.BifrostError) {
	return b.ExecuteChatMCPTool(newCtx(), &schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("c1"),
		Type:     schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(tool), Arguments: args},
	})
}

// contentOf returns a chat message's text content, or "" when it has none.
func contentOf(m *schemas.ChatMessage) string {
	if m == nil || m.Content == nil || m.Content.ContentStr == nil {
		return ""
	}
	return *m.Content.ContentStr
}

// errorText returns a Bifrost error's message, or "" for nil.
func errorText(err *schemas.BifrostError) string {
	if err == nil || err.Error == nil {
		return ""
	}
	return err.Error.Message
}

// clientNamed returns the client called name from GetMCPClients.
func clientNamed(t *testing.T, b *Bifrost, name string) schemas.MCPClient {
	t.Helper()
	clients, err := b.GetMCPClients()
	if err != nil {
		t.Fatalf("GetMCPClients: %v", err)
	}
	for _, c := range clients {
		if c.Config != nil && c.Config.Name == name {
			return c
		}
	}
	t.Fatalf("no client named %q among %d clients", name, len(clients))
	return schemas.MCPClient{}
}

// toolNames returns a client's tool names in the order GetMCPClients lists them (sorted).
func toolNames(c schemas.MCPClient) []string {
	names := make([]string, 0, len(c.Tools))
	for _, tool := range c.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// eventually polls cond every 50ms until it holds or within passes, and reports whether it held.
func eventually(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// lastPid reads the last pid written to path, or 0.
func lastPid(path string) int {
	data, _ := os.ReadFile(path)
	lines := strings.Fields(string(data))
	if len(lines) == 0 {
		return 0
	}
	pid, _ := strconv.Atoi(lines[len(lines)-1])
	return pid
}

// testSDKSTDIORespawn: a STDIO client with a 2s sync interval gets a new child when its child is
// killed, and is healthy again within 30s with no admin action.
func testSDKSTDIORespawn(t *testing.T) {
	pidFile := t.TempDir() + "/pids"
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}}, &schemas.MCPClientConfig{
		ID:               "stdio-id",
		Name:             "stdioalpha",
		ConnectionType:   schemas.MCPConnectionTypeSTDIO,
		StdioConfig:      &schemas.MCPStdioConfig{Command: os.Args[0], Args: []string{"-test.run=^TestMCPSDKSTDIOServer$"}, Envs: []string{stdioServerEnv + "=" + pidFile}},
		AuthType:         schemas.MCPAuthTypeNone,
		ToolsToExecute:   schemas.WhiteList{"*"},
		ToolSyncInterval: 2 * time.Second,
	})
	b.ConnectConfiguredMCPClients(context.Background(), nil)
	if got := clientNamed(t, b, "stdioalpha").State; got != schemas.MCPConnectionStateHealthy {
		t.Fatalf("state after connect = %q, want healthy", got)
	}
	first := lastPid(pidFile)
	if first == 0 {
		t.Fatal("the STDIO child recorded no pid")
	}
	child, err := os.FindProcess(first)
	if err != nil {
		t.Fatalf("find the child: %v", err)
	}
	if err := child.Kill(); err != nil {
		t.Fatalf("kill the child: %v", err)
	}
	respawned := eventually(30*time.Second, func() bool {
		pid := lastPid(pidFile)
		return pid != 0 && pid != first && clientNamed(t, b, "stdioalpha").State == schemas.MCPConnectionStateHealthy
	})
	if !respawned {
		t.Fatalf("no healthy client on a new child within 30s (state %q, pid %d, first pid %d)", clientNamed(t, b, "stdioalpha").State, lastPid(pidFile), first)
	}
	if msg, bErr := execChat(b, "stdioalpha-echo_tool", `{"text":"after the respawn"}`); bErr != nil || contentOf(msg) != "after the respawn" {
		t.Fatalf("a call on the new child = %q, %q", contentOf(msg), errorText(bErr))
	}
}

// testSDKConnectGating: Init does not dial the configured clients; ConnectConfiguredMCPClients
// does, after which alpha is healthy with its two tools.
func testSDKConnectGating(t *testing.T) {
	a := newAlphaServer(t)
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}}, alphaClient(a.url))
	clients, err := b.GetMCPClients()
	if err != nil {
		t.Fatalf("GetMCPClients before connect: %v", err)
	}
	if len(clients) != 0 {
		t.Fatalf("before connect: %d clients, want none", len(clients))
	}
	b.ConnectConfiguredMCPClients(context.Background(), nil)
	alpha := clientNamed(t, b, "alpha")
	if alpha.State != schemas.MCPConnectionStateHealthy {
		t.Fatalf("state = %q, want healthy", alpha.State)
	}
	if got := strings.Join(toolNames(alpha), ","); got != "add_tool,echo_tool" {
		t.Fatalf("tools = %s, want add_tool,echo_tool", got)
	}
}

// testSDKExecution: a chat-format and a Responses-format call both reach alpha's echo_tool and
// return its text.
func testSDKExecution(t *testing.T) {
	a := newAlphaServer(t)
	b := connectedAlpha(t, a, nil)
	msg, bErr := execChat(b, "alpha-echo_tool", `{"text":"go21 chat"}`)
	if bErr != nil {
		t.Fatalf("chat execute: %s", errorText(bErr))
	}
	if !strings.Contains(contentOf(msg), "go21 chat") {
		t.Fatalf("chat content = %q, want it to contain the echo", contentOf(msg))
	}
	resp, bErr := b.ExecuteResponsesMCPTool(newCtx(), &schemas.ResponsesToolMessage{CallID: schemas.Ptr("r1"), Name: schemas.Ptr("alpha-echo_tool"), Arguments: schemas.Ptr(`{"text":"go21 responses"}`)})
	if bErr != nil {
		t.Fatalf("Responses execute: %s", errorText(bErr))
	}
	if resp == nil || resp.ResponsesToolMessage == nil || resp.ResponsesToolMessage.Output == nil || resp.ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil {
		t.Fatalf("Responses execute returned no output: %+v", resp)
	}
	if got := *resp.ResponsesToolMessage.Output.ResponsesToolCallOutputStr; got != "go21 responses" {
		t.Fatalf("Responses output = %q, want the echo", got)
	}
	if got := a.callCount("echo_tool"); got != 2 {
		t.Fatalf("alpha ran echo_tool %d times, want 2", got)
	}
}

// testSDKAllowListDefault: a client with no ToolsToExecute exposes no tools, and executing one is
// refused by the allow-list.
func testSDKAllowListDefault(t *testing.T) {
	a := newAlphaServer(t)
	b := connectedAlpha(t, a, func(c *schemas.MCPClientConfig) { c.ToolsToExecute = nil })
	if tools := b.GetAvailableMCPTools(newCtx()); len(tools) != 0 {
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			if tool.Function != nil {
				names = append(names, tool.Function.Name)
			}
		}
		t.Fatalf("available tools = %v, want none", names)
	}
	_, bErr := execChat(b, "alpha-echo_tool", `{"text":"x"}`)
	if !strings.Contains(errorText(bErr), "is not permitted (not in client's ToolsToExecute allow-list)") {
		t.Fatalf("execute error = %q, want the allow-list refusal", errorText(bErr))
	}
	if got := a.callCount("echo_tool"); got != 0 {
		t.Fatalf("alpha ran echo_tool %d times, want 0", got)
	}
}

// testSDKWithoutMCPConfig: with no MCP config, execution reports MCP as unconfigured until
// AddMCPClient creates the manager, after which calls work.
func testSDKWithoutMCPConfig(t *testing.T) {
	a := newAlphaServer(t)
	b := newSDKClient(t, schemas.BifrostConfig{})
	_, bErr := execChat(b, "alpha-echo_tool", `{"text":"x"}`)
	if got := errorText(bErr); got != "mcp is not configured in this bifrost instance" {
		t.Fatalf("execute error = %q, want \"mcp is not configured in this bifrost instance\"", got)
	}
	if err := addMCPClient(context.Background(), b, alphaClient(a.url)); err != nil {
		t.Fatalf("AddMCPClient: %v", err)
	}
	msg, bErr := execChat(b, "alpha-echo_tool", `{"text":"go23"}`)
	if bErr != nil || contentOf(msg) != "go23" {
		t.Fatalf("execute after AddMCPClient = %q, %q; want \"go23\"", contentOf(msg), errorText(bErr))
	}
}

// textTool is a function tool schema with one string argument, text.
func textTool(name string) schemas.ChatTool {
	return schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
		Name:       name,
		Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("text", map[string]interface{}{"type": "string"}))},
	}}
}

// testSDKRegisterMCPTool: a registered tool runs under the bifrostInternal prefix, a handler
// error comes back as "Error: <msg>", and duplicate or hyphenated names are refused.
func testSDKRegisterMCPTool(t *testing.T) {
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}})
	echo := func(args any) (string, error) {
		m, _ := args.(map[string]interface{})
		text, _ := m["text"].(string)
		return "local " + text, nil
	}
	if err := b.RegisterMCPTool("echo_local", "Echoes text", echo, textTool("echo_local")); err != nil {
		t.Fatalf("RegisterMCPTool echo_local: %v", err)
	}
	msg, bErr := execChat(b, "bifrostInternal-echo_local", `{"text":"go24"}`)
	if bErr != nil || contentOf(msg) != "local go24" {
		t.Fatalf("bifrostInternal-echo_local = %q, %q; want \"local go24\"", contentOf(msg), errorText(bErr))
	}
	fail := func(any) (string, error) { return "", errors.New("handler broke") }
	if err := b.RegisterMCPTool("fail_local", "Always fails", fail, textTool("fail_local")); err != nil {
		t.Fatalf("RegisterMCPTool fail_local: %v", err)
	}
	msg, bErr = execChat(b, "bifrostInternal-fail_local", `{"text":"x"}`)
	if bErr != nil || contentOf(msg) != "Error: handler broke" {
		t.Fatalf("handler error came back as %q, %q; want the message \"Error: handler broke\"", contentOf(msg), errorText(bErr))
	}
	if err := b.RegisterMCPTool("echo_local", "again", echo, textTool("echo_local")); err == nil || err.Error() != "tool 'echo_local' is already registered" {
		t.Fatalf("duplicate registration error = %v, want \"tool 'echo_local' is already registered\"", err)
	}
	if err := b.RegisterMCPTool("a-b", "hyphen", echo, textTool("a-b")); err == nil || err.Error() != "tool name cannot contain hyphens" {
		t.Fatalf("hyphenated name error = %v, want \"tool name cannot contain hyphens\"", err)
	}
}

// shortCircuitPlugin answers every MCP tool call itself and counts the post hooks that see its answer.
type shortCircuitPlugin struct {
	posts atomic.Int64
}

// GetName names the plugin.
func (p *shortCircuitPlugin) GetName() string { return "mcp-short-circuit" }

// Cleanup has nothing to release.
func (p *shortCircuitPlugin) Cleanup() error { return nil }

// PreMCPHook short-circuits tool calls with a fixed tool message and lets every other request through.
func (p *shortCircuitPlugin) PreMCPHook(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if req == nil || !req.RequestType.IsExecuteTool() {
		return req, nil, nil
	}
	return req, &schemas.MCPPluginShortCircuit{Response: &schemas.BifrostMCPResponse{ChatMessage: &schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("answered by the plugin")},
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("c1")},
	}}}, nil
}

// PostMCPHook counts the responses carrying the plugin's answer and passes everything through.
func (p *shortCircuitPlugin) PostMCPHook(_ *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if resp != nil && contentOf(resp.ChatMessage) == "answered by the plugin" {
		p.posts.Add(1)
	}
	return resp, bErr, nil
}

// testSDKPluginShortCircuit: a PreMCPHook answer reaches the caller, alpha is never called, and
// PostMCPHook runs once for it.
func testSDKPluginShortCircuit(t *testing.T) {
	a := newAlphaServer(t)
	plugin := &shortCircuitPlugin{}
	b := newSDKClient(t, schemas.BifrostConfig{
		MCPPlugins: []schemas.MCPPlugin{plugin},
		MCPConfig:  &schemas.MCPConfig{},
	}, alphaClient(a.url))
	b.ConnectConfiguredMCPClients(context.Background(), nil)
	if alpha := clientNamed(t, b, "alpha"); alpha.State != schemas.MCPConnectionStateHealthy || !slices.Contains(toolNames(alpha), "echo_tool") {
		t.Fatalf("control: alpha is %q with tools %v, want healthy with echo_tool", alpha.State, toolNames(alpha))
	}
	msg, bErr := execChat(b, "alpha-echo_tool", `{"text":"x"}`)
	if bErr != nil || contentOf(msg) != "answered by the plugin" {
		t.Fatalf("caller got %q, %q; want the plugin's answer", contentOf(msg), errorText(bErr))
	}
	if got := a.callCount("echo_tool"); got != 0 {
		t.Fatalf("alpha ran echo_tool %d times, want 0", got)
	}
	if got := plugin.posts.Load(); got != 1 {
		t.Fatalf("PostMCPHook saw the answer %d times, want 1", got)
	}
}

// testSDKRemoveMCPClient: after RemoveMCPClient, alpha's tool is no longer available.
func testSDKRemoveMCPClient(t *testing.T) {
	a := newAlphaServer(t)
	b := connectedAlpha(t, a, nil)
	if msg, bErr := execChat(b, "alpha-echo_tool", `{"text":"before"}`); bErr != nil || contentOf(msg) != "before" {
		t.Fatalf("control call before the removal = %q, %q", contentOf(msg), errorText(bErr))
	}
	if err := b.RemoveMCPClient("alpha-id"); err != nil {
		t.Fatalf("RemoveMCPClient: %v", err)
	}
	_, bErr := execChat(b, "alpha-echo_tool", `{"text":"after"}`)
	if errorText(bErr) != "tool 'alpha-echo_tool' is not available or not permitted" {
		t.Fatalf("execute after the removal = %q, want \"tool 'alpha-echo_tool' is not available or not permitted\"", errorText(bErr))
	}
	if got := a.callCount("echo_tool"); got != 1 {
		t.Fatalf("alpha ran echo_tool %d times, want only the control call", got)
	}
}

// spanTracer records the attributes set on each span, by span name.
type spanTracer struct {
	*schemas.NoOpTracer
	mu    sync.Mutex
	attrs map[string]map[string]any
}

// StartSpan opens a span whose handle is its name, so attributes can be read back by name.
func (s *spanTracer) StartSpan(ctx context.Context, name string, _ schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs[name] == nil {
		s.attrs[name] = map[string]any{}
	}
	return ctx, name
}

// SetAttribute records key on the named span.
func (s *spanTracer) SetAttribute(handle schemas.SpanHandle, key string, value any) {
	name, _ := handle.(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs[name] == nil {
		s.attrs[name] = map[string]any{}
	}
	s.attrs[name][key] = value
}

// spanFor returns the attributes of client's execute-tool span (a span named "...<client>" whose
// operation is execute_tool), and whether there is one.
func (s *spanTracer) spanFor(client string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, attrs := range s.attrs {
		if strings.HasSuffix(name, "."+client) && attrs[schemas.AttrOperationName] == schemas.OTelOperationNameExecuteTool {
			return attrs, true
		}
	}
	return nil, false
}

// testSDKInProcessClient: an in-process client is healthy, its tool returns the handler's text,
// and its span carries no network.transport, while an HTTP client's span says "tcp".
func testSDKInProcessClient(t *testing.T) {
	a := newAlphaServer(t)
	inproc := server.NewMCPServer("inproc", "1.0.0", server.WithToolCapabilities(true))
	inproc.AddTool(mcpgo.NewTool("ping_tool"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("pong from inproc"), nil
	})
	tracer := &spanTracer{NoOpTracer: &schemas.NoOpTracer{}, attrs: map[string]map[string]any{}}
	b := newSDKClient(t, schemas.BifrostConfig{Tracer: tracer, MCPConfig: &schemas.MCPConfig{}},
		alphaClient(a.url),
		&schemas.MCPClientConfig{ID: "inproc-id", Name: "inproc", ConnectionType: schemas.MCPConnectionTypeInProcess, InProcessServer: inproc, ToolsToExecute: schemas.WhiteList{"*"}},
	)
	b.ConnectConfiguredMCPClients(context.Background(), nil)
	if got := clientNamed(t, b, "inproc").State; got != schemas.MCPConnectionStateHealthy {
		t.Fatalf("in-process state = %q, want healthy", got)
	}
	msg, bErr := execChat(b, "inproc-ping_tool", `{}`)
	if bErr != nil || contentOf(msg) != "pong from inproc" {
		t.Fatalf("inproc-ping_tool = %q, %q; want \"pong from inproc\"", contentOf(msg), errorText(bErr))
	}
	if _, bErr := execChat(b, "alpha-echo_tool", `{"text":"x"}`); bErr != nil {
		t.Fatalf("control call on alpha: %s", errorText(bErr))
	}
	alphaSpan, ok := tracer.spanFor("alpha")
	if !ok || alphaSpan[schemas.AttrNetworkTransport] != "tcp" {
		t.Fatalf("control: alpha's execute span = %v, want network.transport \"tcp\"", alphaSpan)
	}
	inprocSpan, ok := tracer.spanFor("inproc")
	if !ok {
		t.Fatalf("no execute span for inproc among %v", tracer.attrs)
	}
	if v, has := inprocSpan[schemas.AttrNetworkTransport]; has {
		t.Fatalf("inproc's execute span has network.transport %v, want none", v)
	}
}

// testSDKInProcessNoServer: an in-process client with no server is refused at once. The error is a
// misconfiguration, so the connect retries, about 31s for a network error, do not run.
func testSDKInProcessNoServer(t *testing.T) {
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}})
	start := time.Now()
	err := addMCPClient(context.Background(), b, &schemas.MCPClientConfig{ID: "inproc-id", Name: "inproc", ConnectionType: schemas.MCPConnectionTypeInProcess, ToolsToExecute: schemas.WhiteList{"*"}})
	if err == nil || !strings.Contains(err.Error(), "InProcess connection requires a server instance") {
		t.Fatalf("AddMCPClient error = %v, want \"InProcess connection requires a server instance\"", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("AddMCPClient took %v, want the refusal without retries", elapsed)
	}
}

// openAPIClient is an OpenAPI client called name whose server comes from the in-process factory.
func openAPIClient(name string) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:             name + "-id",
		Name:           name,
		ConnectionType: schemas.MCPConnectionTypeOpenAPI,
		ToolsToExecute: schemas.WhiteList{"*"},
		OpenAPIConfig:  &schemas.MCPOpenAPIConfig{Spec: "served by the test factory"},
	}
}

// countingFactory returns an in-process server factory that counts its calls and serves one tool,
// spec_tool. With noServer it returns (nil, nil) instead.
func countingFactory(calls *atomic.Int64, noServer bool) schemas.InProcessServerFactory {
	return func(context.Context, *schemas.MCPClientConfig, schemas.InProcessServerDeps) (*server.MCPServer, error) {
		calls.Add(1)
		if noServer {
			return nil, nil
		}
		s := server.NewMCPServer("openapi", "1.0.0", server.WithToolCapabilities(true))
		s.AddTool(mcpgo.NewTool("spec_tool"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("from the factory"), nil
		})
		return s, nil
	}
}

// testSDKOpenAPINoFactory: an OpenAPI client with no factory fails at once, without retries.
func testSDKOpenAPINoFactory(t *testing.T) {
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}})
	start := time.Now()
	err := addMCPClient(context.Background(), b, openAPIClient("spec"))
	if err == nil || !strings.Contains(err.Error(), "openapi connection type requires MCPConfig.InProcessServerFactory") {
		t.Fatalf("AddMCPClient error = %v, want the missing factory", err)
	}
	if took := time.Since(start); took > 900*time.Millisecond {
		t.Fatalf("the refusal took %s; the first retry waits 1s, want under 900ms", took)
	}
}

// testSDKOpenAPIFactory: the factory runs once on connect and again on reconnect, and the
// client's tool is callable after each.
func testSDKOpenAPIFactory(t *testing.T) {
	var calls atomic.Int64
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{InProcessServerFactory: countingFactory(&calls, false)}})
	if err := addMCPClient(context.Background(), b, openAPIClient("spec")); err != nil {
		t.Fatalf("AddMCPClient: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory calls after connect = %d, want 1", got)
	}
	if msg, bErr := execChat(b, "spec-spec_tool", `{}`); bErr != nil || contentOf(msg) != "from the factory" {
		t.Fatalf("spec-spec_tool after connect = %q, %q", contentOf(msg), errorText(bErr))
	}
	if err := b.ReconnectMCPClient("spec-id"); err != nil {
		t.Fatalf("ReconnectMCPClient: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("factory calls after reconnect = %d, want 2", got)
	}
	if msg, bErr := execChat(b, "spec-spec_tool", `{}`); bErr != nil || contentOf(msg) != "from the factory" {
		t.Fatalf("spec-spec_tool after reconnect = %q, %q", contentOf(msg), errorText(bErr))
	}
}

// testSDKOpenAPIFactoryNil: a factory that returns no server fails the connect after one call.
func testSDKOpenAPIFactoryNil(t *testing.T) {
	var calls atomic.Int64
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{InProcessServerFactory: countingFactory(&calls, true)}})
	err := addMCPClient(context.Background(), b, openAPIClient("spec"))
	if err == nil || !strings.Contains(err.Error(), "in-process server factory returned no server") {
		t.Fatalf("AddMCPClient error = %v, want \"in-process server factory returned no server\"", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
}

// testSDKOpenAPIFactoryAfterInit: a factory registered after Init serves an OpenAPI client added
// afterwards.
func testSDKOpenAPIFactoryAfterInit(t *testing.T) {
	var calls atomic.Int64
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}})
	b.SetMCPInProcessServerFactory(countingFactory(&calls, false))
	if err := addMCPClient(context.Background(), b, openAPIClient("spec")); err != nil {
		t.Fatalf("AddMCPClient: %v", err)
	}
	if got := clientNamed(t, b, "spec").State; got != schemas.MCPConnectionStateHealthy {
		t.Fatalf("state = %q, want healthy", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
	if msg, bErr := execChat(b, "spec-spec_tool", `{}`); bErr != nil || contentOf(msg) != "from the factory" {
		t.Fatalf("spec-spec_tool = %q, %q", contentOf(msg), errorText(bErr))
	}
}

// testSDKConcurrency: 100 concurrent calls on one sticky client all succeed over its one upstream
// connection, with no handshake after the first.
func testSDKConcurrency(t *testing.T) {
	a := newAlphaServer(t)
	b := connectedAlpha(t, a, func(c *schemas.MCPClientConfig) { c.NeedsSessionStickiness = schemas.Ptr(true) })
	if got := a.handshakes.Load(); got != 1 {
		t.Fatalf("handshakes after connect = %d, want 1", got)
	}
	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := "call " + strconv.Itoa(i)
			msg, bErr := execChat(b, "alpha-echo_tool", `{"text":"`+want+`"}`)
			if bErr != nil || contentOf(msg) != want {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := failures.Load(); got != 0 {
		t.Fatalf("%d of 100 calls failed or returned the wrong text", got)
	}
	if got := a.callCount("echo_tool"); got != 100 {
		t.Fatalf("alpha ran echo_tool %d times, want 100", got)
	}
	if got := a.handshakes.Load(); got != 1 {
		t.Fatalf("handshakes after the calls = %d, want 1: the sticky client reconnected", got)
	}
}

// testSDKInProcessResults: on an in-process client, text round-trips, an image becomes an image
// placeholder, a tool error sets IsError, a slow tool hits the 1s timeout (a known issue today, so
// that check is skipped), and a reconnect keeps the tools.
func testSDKInProcessResults(t *testing.T) {
	s := server.NewMCPServer("results", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcpgo.NewTool("echo", mcpgo.WithString("text", mcpgo.Required())), func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		text, _ := req.GetArguments()["text"].(string)
		return mcpgo.NewToolResultText(text), nil
	})
	s.AddTool(mcpgo.NewTool("image"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultImage("", "QUJD", "image/png"), nil
	})
	s.AddTool(mcpgo.NewTool("error"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultError("the tool failed"), nil
	})
	s.AddTool(mcpgo.NewTool("slow"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		time.Sleep(3 * time.Second)
		return mcpgo.NewToolResultText("late"), nil
	})
	s.AddTool(mcpgo.NewTool("stops"), func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		<-ctx.Done()
		return mcpgo.NewToolResultText("stopped"), nil
	})
	b := newSDKClient(t, schemas.BifrostConfig{MCPConfig: &schemas.MCPConfig{}}, &schemas.MCPClientConfig{
		ID: "results-id", Name: "results", ConnectionType: schemas.MCPConnectionTypeInProcess, InProcessServer: s,
		ToolsToExecute: schemas.WhiteList{"*"}, ToolExecutionTimeout: time.Second,
	})
	b.ConnectConfiguredMCPClients(context.Background(), nil)
	if msg, bErr := execChat(b, "results-echo", `{"text":"round trip"}`); bErr != nil || contentOf(msg) != "round trip" {
		t.Fatalf("results-echo = %q, %q; want \"round trip\"", contentOf(msg), errorText(bErr))
	}
	// The image comes back as an image content block, beside the empty text item the tool sends with it.
	msg, bErr := execChat(b, "results-image", `{}`)
	hasImage := false
	if bErr == nil && msg != nil && msg.Content != nil {
		for _, block := range msg.Content.ContentBlocks {
			hasImage = hasImage || (block.ImageURLStruct != nil && block.ImageURLStruct.URL == "data:image/png;base64,QUJD")
		}
	}
	if !hasImage {
		raw, _ := json.Marshal(msg)
		t.Fatalf("results-image = %s, %q; want an image block data:image/png;base64,QUJD", raw, errorText(bErr))
	}
	msg, bErr = execChat(b, "results-error", `{}`)
	if bErr != nil || msg == nil || msg.ChatToolMessage == nil || msg.ChatToolMessage.IsError == nil || !*msg.ChatToolMessage.IsError {
		t.Fatalf("results-error = %+v, %q; want IsError set", msg, errorText(bErr))
	}
	// A tool that ignores its context and one that returns once it ends both time out at 1s:
	// an in-process handler runs on the call's own goroutine, so neither stops the call itself.
	for _, tool := range []string{"results-slow", "results-stops"} {
		t.Run(tool+" times out at 1s", func(t *testing.T) {
			start := time.Now()
			_, bErr := execChat(b, tool, `{}`)
			if !strings.Contains(errorText(bErr), "timed out after 1s") {
				t.Fatalf("%s error = %q, want the 1s timeout", tool, errorText(bErr))
			}
			if took := time.Since(start); took > 2500*time.Millisecond {
				t.Fatalf("%s took %s, want about 1s", tool, took)
			}
		})
	}
	before := strings.Join(toolNames(clientNamed(t, b, "results")), ",")
	if err := b.ReconnectMCPClient("results-id"); err != nil {
		t.Fatalf("ReconnectMCPClient: %v", err)
	}
	if after := strings.Join(toolNames(clientNamed(t, b, "results")), ","); after != before || after != "echo,error,image,slow,stops" {
		t.Fatalf("tools after reconnect = %s (before %s), want echo,error,image,slow,stops", after, before)
	}
}

// warnRecorder is a no-op logger that keeps the warnings.
type warnRecorder struct {
	schemas.Logger
	mu    sync.Mutex
	warns []string
}

func (l *warnRecorder) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(msg, args...))
}

func (l *warnRecorder) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.warns, "\n")
}

// TestLazyMCPManagerWiresDecisionRanker starts without MCPConfig, so the MCP
// manager is created lazily, then installs a decision-model discovery ranker:
// a ranked listToolFiles query must reach the decision API.
func TestLazyMCPManagerWiresDecisionRanker(t *testing.T) {
	a := newAlphaServer(t)
	logger := &warnRecorder{Logger: NewNoOpLogger()}
	b := newSDKClient(t, schemas.BifrostConfig{Logger: logger})
	client := alphaClient(a.url)
	client.IsCodeModeClient = true
	if err := addMCPClient(context.Background(), b, client); err != nil {
		t.Fatalf("AddMCPClient: %v", err)
	}
	b.MCPManager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		CodeModeDiscoveryRanker: &schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "ranker-test-missing/ranker"},
	})

	msg, bErr := execChat(b, "listToolFiles", `{"query":"echo this text back"}`)
	if bErr != nil {
		t.Fatalf("listToolFiles: %s", errorText(bErr))
	}
	if !strings.Contains(contentOf(msg), "discovery ranker was not used") {
		t.Fatalf("expected the lexical fallback after the failing decision call:\n%s", contentOf(msg))
	}
	warns := logger.joined()
	if strings.Contains(warns, "decision API is not available") || !strings.Contains(warns, "decision request to ranker-test-missing/ranker failed") {
		t.Fatalf("the decision API was not called, warnings:\n%s", warns)
	}
}

// A ranked listToolFiles run by the MCP agent loop carries the route that
// served the conversation's first turn. The decision ranker call must still
// go to the configured ranker model, not be pinned to the conversation's.
func TestDecisionRankerIgnoresAgentServedRoute(t *testing.T) {
	a := newAlphaServer(t)
	logger := &warnRecorder{Logger: NewNoOpLogger()}
	b := newSDKClient(t, schemas.BifrostConfig{Logger: logger})
	client := alphaClient(a.url)
	client.IsCodeModeClient = true
	if err := addMCPClient(context.Background(), b, client); err != nil {
		t.Fatalf("AddMCPClient: %v", err)
	}
	b.MCPManager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		CodeModeDiscoveryRanker: &schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "ranker-test-missing/ranker"},
	})

	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyMCPAgentServedRoute, &schemas.RoutingInfo{Provider: "conversation-test-missing", Model: "conversation"})
	msg, bErr := b.ExecuteChatMCPTool(ctx, &schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("c1"),
		Type:     schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("listToolFiles"), Arguments: `{"query":"echo this text back"}`},
	})
	if bErr != nil {
		t.Fatalf("listToolFiles: %s", errorText(bErr))
	}
	if !strings.Contains(contentOf(msg), "discovery ranker was not used") {
		t.Fatalf("expected the lexical fallback after the failing decision call:\n%s", contentOf(msg))
	}
	warns := logger.joined()
	if !strings.Contains(warns, "decision request to ranker-test-missing/ranker failed") || strings.Contains(warns, "conversation-test-missing") {
		t.Fatalf("the decision call did not go to the configured ranker model, warnings:\n%s", warns)
	}
	if route, _ := ctx.Value(schemas.BifrostContextKeyMCPAgentServedRoute).(*schemas.RoutingInfo); route == nil {
		t.Fatal("the ranker call cleared the agent route on the tool call's own context")
	}
}

// A ranker decision call skips the plugin pipeline, so the compat plugin never
// marks it for emulation. A general model configured as the ranker's decision
// model must still be answered, through its Responses API, and a native
// decisions model must still go to the decisions endpoint even when the tool
// call's own request was marked for conversion.
func TestDecisionRankerChoosesDecisionPath(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"native decisions unavailable","type":"server_error"}}`))
			return
		}
		// Answer the emit_decision tool over the offered choices: the first
		// one is the relevant tool.
		var body struct {
			Tools []struct {
				Parameters struct {
					Properties map[string]struct {
						Properties struct {
							Choice struct {
								Enum []string `json:"enum"`
							} `json:"choice"`
						} `json:"properties"`
					} `json:"properties"`
				} `json:"parameters"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Tools) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		options := body.Tools[0].Parameters.Properties["relevant_tool"].Properties.Choice.Enum
		if len(options) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		probabilities := map[string]float64{}
		for i, option := range options {
			probabilities[option] = 0
			if i == 0 {
				probabilities[option] = 1
			}
		}
		args, _ := json.Marshal(map[string]any{
			"relevant_tool":       map[string]any{"choice": options[0], "confidence": 0.9, "probabilities": probabilities},
			"tools_cover_request": map[string]any{"value": 0.9},
		})
		out, _ := json.Marshal(map[string]any{
			"id": "resp_1", "object": "response", "created_at": 1, "model": "gpt-5-nano", "status": "completed",
			"output": []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "emit_decision", "arguments": string(args), "status": "completed"}},
			"usage":  map[string]any{"input_tokens": 5, "output_tokens": 5, "total_tokens": 10},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(upstream.Close)

	for _, tc := range []struct {
		name, model string
		// provider defaults to OpenAI.
		provider schemas.ModelProvider
		// aliases are the key's model aliases.
		aliases schemas.KeyAliases
		// callerMarked marks the tool call's own request for conversion to Responses.
		callerMarked bool
		wantPath     string
		wantRanked   bool
	}{
		{name: "general model is emulated", model: "gpt-5-nano", wantPath: "/v1/responses", wantRanked: true},
		{name: "general model is emulated under a marked caller", model: "gpt-5-nano", callerMarked: true, wantPath: "/v1/responses", wantRanked: true},
		{name: "native decisions model stays native", model: "gpt-6-luna", wantPath: "/v1/decisions"},
		{name: "native decisions model stays native under a marked caller", model: "gpt-6-luna", callerMarked: true, wantPath: "/v1/decisions"},
		// The path follows the model the key's alias resolves to, not the alias name.
		{name: "alias of a native decisions model stays native", model: "ranker", aliases: schemas.KeyAliases{"ranker": {ModelID: "gpt-6-luna"}}, wantPath: "/v1/decisions"},
		{name: "alias of a general model is emulated", model: "luna", aliases: schemas.KeyAliases{"luna": {ModelID: "gpt-5-nano"}}, wantPath: "/v1/responses", wantRanked: true},
		{name: "sarvam prism stays native", provider: schemas.Sarvam, model: "sarvam-prism", wantPath: "/v2/systemone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			paths = nil
			mu.Unlock()
			provider := tc.provider
			if provider == "" {
				provider = schemas.OpenAI
			}
			account := NewMockAccount()
			account.AddProviderWithBaseURL(provider, 1, 10, upstream.URL)
			account.configs[provider].NetworkConfig.AllowPrivateNetwork = true
			account.configs[provider].NetworkConfig.MaxRetries = 0
			account.SetKeysForProvider(provider, []schemas.Key{{ID: "k1", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 1, Aliases: tc.aliases}})
			a := newAlphaServer(t)
			logger := &warnRecorder{Logger: NewNoOpLogger()}
			b := newSDKClient(t, schemas.BifrostConfig{Account: account, Logger: logger})
			client := alphaClient(a.url)
			client.IsCodeModeClient = true
			if err := addMCPClient(context.Background(), b, client); err != nil {
				t.Fatalf("AddMCPClient: %v", err)
			}
			b.MCPManager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
				CodeModeDiscoveryRanker: &schemas.MCPCodeModeDiscoveryRanker{DecisionModel: string(provider) + "/" + tc.model},
			})

			ctx := newCtx()
			if tc.callerMarked {
				ctx.SetValue(schemas.BifrostContextKeyChangeRequestType, schemas.ResponsesRequest)
			}
			msg, bErr := b.ExecuteChatMCPTool(ctx, &schemas.ChatAssistantMessageToolCall{
				ID:       schemas.Ptr("c1"),
				Type:     schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("listToolFiles"), Arguments: `{"query":"echo this text back"}`},
			})
			if bErr != nil {
				t.Fatalf("listToolFiles: %s", errorText(bErr))
			}
			mu.Lock()
			got := slices.Clone(paths)
			mu.Unlock()
			if len(got) != 1 || got[0] != tc.wantPath {
				t.Fatalf("upstream paths = %v, want one call to %s; warnings:\n%s", got, tc.wantPath, logger.joined())
			}
			if ranked := strings.Contains(contentOf(msg), "(discovery ranker)"); ranked != tc.wantRanked {
				t.Fatalf("ranked by the decision model = %t, want %t:\n%s\nwarnings:\n%s", ranked, tc.wantRanked, contentOf(msg), logger.joined())
			}
			if changeType, _ := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType); (changeType == schemas.ResponsesRequest) != tc.callerMarked {
				t.Fatalf("the ranker call changed the tool call's own request type mark to %q", changeType)
			}
		})
	}
}
