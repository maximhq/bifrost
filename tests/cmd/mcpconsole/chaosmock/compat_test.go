package chaosmock_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// These tests point a real mcp-go client, the same library and version the
// system under test uses, at the hand-rolled mock. The mock deliberately
// implements the protocol itself so it can violate it on demand, and that
// freedom is only safe if a genuine client accepts it when it is behaving.
// Everything else in the harness rests on that, so it is checked first.

func newFleetWithTools(t *testing.T, tools ...chaosmock.Tool) (*chaosmock.Fleet, *chaosmock.Server) {
	t.Helper()
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set(tools)
	fleet.Start()
	t.Cleanup(fleet.Stop)
	return fleet, srv
}

func connect(t *testing.T, url string) (*client.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	c, err := client.NewStreamableHttpClient(url)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-compat", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, req); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	return c, ctx
}

func TestRealClientCompletesHandshakeAndListsTools(t *testing.T) {
	fleet, srv := newFleetWithTools(t,
		chaosmock.Tool{Name: "alpha_one"},
		chaosmock.Tool{Name: "alpha_two"},
	)

	c, ctx := connect(t, fleet.ServerURL("alpha"))

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(res.Tools))
	}
	// Sorted by name, so the order is part of the contract rather than an
	// accident of map iteration.
	if res.Tools[0].Name != "alpha_one" || res.Tools[1].Name != "alpha_two" {
		t.Fatalf("unexpected tool names: %q, %q", res.Tools[0].Name, res.Tools[1].Name)
	}

	counters := srv.Counters().Snapshot()
	if counters.Initializes != 1 {
		t.Errorf("expected 1 initialize, got %d", counters.Initializes)
	}
	if counters.ToolsList != 1 {
		t.Errorf("expected 1 tools/list, got %d", counters.ToolsList)
	}
	if counters.Sessions != 1 {
		t.Errorf("expected 1 session, got %d", counters.Sessions)
	}
	// The handshake's own notification has to have landed, otherwise the
	// client is not as far along as it appears.
	if counters.Methods["notifications/initialized"] != 1 {
		t.Errorf("expected the initialized notification, got methods %v", counters.Methods)
	}
}

func TestRealClientCallsToolAndCounterRecordsDispatch(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	var req mcp.CallToolRequest
	req.Params.Name = "echo_tool"
	req.Params.Arguments = map[string]any{"value": "hello"}

	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected a successful call, got an error result")
	}
	if len(res.Content) == 0 {
		t.Fatalf("expected content in the result")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	// The default reply names the tool that ran, so a misdispatch is visible
	// in the response body before the counters are even consulted.
	if want := "echo_tool:"; len(text.Text) < len(want) || text.Text[:len(want)] != want {
		t.Errorf("expected the reply to name the tool that ran, got %q", text.Text)
	}

	counters := srv.Counters().Snapshot()
	if counters.Calls["echo_tool"] != 1 {
		t.Errorf("expected 1 call to echo_tool, got %v", counters.Calls)
	}
}

func TestRealClientPings(t *testing.T) {
	fleet, srv := newFleetWithTools(t)
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if got := srv.Counters().Snapshot().Pings; got != 1 {
		t.Errorf("expected 1 ping, got %d", got)
	}
}

func TestToolMutationIsVisibleToTheNextList(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "before_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	first, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("first tools/list failed: %v", err)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "before_tool" {
		t.Fatalf("unexpected initial tools: %+v", first.Tools)
	}

	// Mutate underneath the live session. The connection is not disturbed, so
	// this isolates "did the tool set change" from "did the client reconnect".
	srv.Tools().Add(chaosmock.Tool{Name: "after_tool"})
	srv.Tools().Remove("before_tool")

	second, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("second tools/list failed: %v", err)
	}
	if len(second.Tools) != 1 || second.Tools[0].Name != "after_tool" {
		t.Fatalf("expected only after_tool, got %+v", second.Tools)
	}

	if got := srv.Counters().Snapshot().Sessions; got != 1 {
		t.Errorf("mutating tools must not force a new session, got %d sessions", got)
	}
}

func TestKilledSessionForcesReinitialization(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "some_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list before session kill failed: %v", err)
	}

	// The transport stays up; only the session goes away. A client that only
	// watches for transport errors should not notice until its next request.
	srv.KillSessions()

	_, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err == nil {
		t.Fatalf("expected tools/list to fail after the session was killed")
	}
	if srv.SessionCount() != 0 {
		t.Errorf("expected no live sessions, got %d", srv.SessionCount())
	}
}

func TestInjectedStatusReachesTheClient(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "some_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	srv.SetFaults(chaosmock.Faults{Status: 429, RetryAfter: "3"})

	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err == nil {
		t.Fatalf("expected tools/list to fail while a 429 is injected")
	}

	// Clearing the fault has to restore service without any client-side action,
	// which is what makes a transient upstream failure testable at all.
	srv.ClearFaults()
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list should succeed once the fault is cleared: %v", err)
	}
}

func TestPerIdentityOverlayServesDifferentTools(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "shared_tool"}})
	srv.SetAuth(chaosmock.Auth{Bearer: map[string]string{
		"token-a": "user-a",
		"token-b": "user-b",
	}})
	// Only user-a sees a private tool. Two callers against one server seeing
	// two tool lists is the shape per-user auth has to reproduce.
	srv.Tools().SetOverlay("user-a", []chaosmock.Tool{
		{Name: "shared_tool"}, {Name: "private_tool"},
	})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	for _, tc := range []struct {
		token string
		want  int
	}{{"token-a", 2}, {"token-b", 1}} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"),
			transportHeaders(map[string]string{"Authorization": "Bearer " + tc.token}))
		if err != nil {
			t.Fatalf("failed to create client: %v", err)
		}
		if err := c.Start(ctx); err != nil {
			t.Fatalf("failed to start client: %v", err)
		}
		var req mcp.InitializeRequest
		req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-compat", Version: "1.0.0"}
		if _, err := c.Initialize(ctx, req); err != nil {
			t.Fatalf("initialize failed for %s: %v", tc.token, err)
		}
		res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			t.Fatalf("tools/list failed for %s: %v", tc.token, err)
		}
		if len(res.Tools) != tc.want {
			t.Errorf("token %s: expected %d tools, got %d", tc.token, tc.want, len(res.Tools))
		}
		_ = c.Close()
		cancel()
	}

	// Revoking one token must leave the other working. This asymmetry is the
	// whole point of per-identity credentials, and a shared-state
	// implementation fails exactly here.
	if !srv.RevokeToken("token-a") {
		t.Fatalf("expected token-a to be revokable")
	}
	if _, ok := srv.Auth().Bearer["token-b"]; !ok {
		t.Errorf("revoking token-a must not remove token-b")
	}
}

// transportHeaders is a thin alias for the mcp-go option that bakes static
// headers onto a client, named locally so the tests above read as being about
// identity rather than about transport plumbing.
func transportHeaders(h map[string]string) transport.StreamableHTTPCOption {
	return transport.WithHTTPHeaders(h)
}

func TestRealClientReadsInstructionsAndAnnotations(t *testing.T) {
	fleet, srv := newFleetWithTools(t,
		chaosmock.Tool{Name: "echo_ro", Annotations: json.RawMessage(`{"readOnlyHint":true,"title":"Echo"}`)},
		chaosmock.Tool{Name: "plain_tool"},
	)
	srv.SetInstructions("Call echo_ro first")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start client: %v", err)
	}
	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-compat", Version: "1.0.0"}
	initRes, err := c.Initialize(ctx, req)
	if err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	if initRes.Instructions != "Call echo_ro first" {
		t.Errorf("expected the instructions in the initialize result, got %q", initRes.Instructions)
	}

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	ro := res.Tools[0].Annotations
	if ro.ReadOnlyHint == nil || !*ro.ReadOnlyHint || ro.Title != "Echo" {
		t.Errorf("expected echo_ro to arrive read-only and titled, got %+v", ro)
	}
	if plain := res.Tools[1].Annotations; plain.ReadOnlyHint != nil || plain.Title != "" {
		t.Errorf("a tool without annotations must arrive without them, got %+v", plain)
	}
}

func TestRealClientParsesScriptedReplies(t *testing.T) {
	fleet, _ := newFleetWithTools(t,
		chaosmock.Tool{Name: "mixed_tool", Reply: json.RawMessage(`{"content":[` +
			`{"type":"text","text":"a"},{"type":"text","text":"b"},` +
			`{"type":"image","data":"QUJD","mimeType":"image/png"}],"structuredContent":{"k":1}}`)},
		chaosmock.Tool{Name: "empty_tool", Reply: json.RawMessage(`{"content":[]}`)},
		chaosmock.Tool{Name: "failing_tool", Reply: json.RawMessage(`{"content":[{"type":"text","text":"no"}],"isError":true}`)},
	)
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	call := func(name string) *mcp.CallToolResult {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = name
		res, err := c.CallTool(ctx, req)
		if err != nil {
			t.Fatalf("tools/call %s failed: %v", name, err)
		}
		return res
	}

	mixed := call("mixed_tool")
	if len(mixed.Content) != 3 {
		t.Fatalf("expected three content blocks, got %d", len(mixed.Content))
	}
	if img, ok := mcp.AsImageContent(mixed.Content[2]); !ok || img.Data != "QUJD" || img.MIMEType != "image/png" {
		t.Errorf("expected the image block to survive, got %+v", mixed.Content[2])
	}
	if sc, ok := mixed.StructuredContent.(map[string]any); !ok || sc["k"] != float64(1) {
		t.Errorf("expected structuredContent {k:1}, got %#v", mixed.StructuredContent)
	}

	// An empty result is "content":[], which a client must accept as a
	// success with nothing to show.
	if empty := call("empty_tool"); len(empty.Content) != 0 || empty.IsError {
		t.Errorf("expected an empty successful result, got %+v", empty)
	}
	if failing := call("failing_tool"); !failing.IsError {
		t.Errorf("expected the scripted isError to reach the client")
	}
}

// TestRealClientReadsAScriptedJSONRPCError checks that reply_error reaches a real client as the
// JSON-RPC error it names, data included, which is how mcp-go recognises codes such as -32021.
func TestRealClientReadsAScriptedJSONRPCError(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "needs_capability",
		ReplyError: json.RawMessage(`{"code":-32021,"message":"missing required client capability","data":{"capability":"elicitation"}}`)})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	var req mcp.CallToolRequest
	req.Params.Name = "needs_capability"
	_, err := c.CallTool(ctx, req)
	var missing mcp.MissingRequiredClientCapabilityError
	if !errors.As(err, &missing) || missing.Capability != "elicitation" {
		t.Fatalf("expected a missing capability error naming elicitation, got %v", err)
	}
	if got := srv.Counters().Snapshot().Calls["needs_capability"]; got != 1 {
		t.Errorf("expected the call to be counted once, got %d", got)
	}
}

// rawPost sends one JSON-RPC message to a streamable HTTP endpoint and returns
// the status, the session id and the body, for assertions about bytes on the
// wire that a client library would normalize away.
func rawPost(t *testing.T, url, sessionID, body string) (int, string, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set(chaosmock.HeaderSessionID, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	return resp.StatusCode, resp.Header.Get(chaosmock.HeaderSessionID), out
}

func TestToolsListSendsOnlyWireFields(t *testing.T) {
	fleet, _ := newFleetWithTools(t, chaosmock.Tool{
		Name:        "echo_ro",
		Annotations: json.RawMessage(`{"readOnlyHint":true}`),
		Reply:       json.RawMessage(`{"content":[]}`),
		Fault:       &chaosmock.Faults{Latency: chaosmock.Duration(time.Millisecond)},
	})
	url := fleet.ServerURL("alpha")

	_, session, _ := rawPost(t, url, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	status, _, body := rawPost(t, url, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if status != http.StatusOK {
		t.Fatalf("tools/list returned %d: %s", status, body)
	}
	var res struct {
		Result struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil || len(res.Result.Tools) != 1 {
		t.Fatalf("unexpected tools/list body: %s", body)
	}
	var keys []string
	for k := range res.Result.Tools[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// The reply and the fault are instructions to the mock. A client that saw
	// them would be looking at fields no real server sends.
	if got := strings.Join(keys, ","); got != "annotations,description,inputSchema,name" {
		t.Errorf("expected only wire fields, got %s", got)
	}
}

func TestScopedFaultSparesTheHandshakeAndIsCounted(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	srv.SetFaults(chaosmock.Faults{Status: 503, Methods: []string{"tools/call"}})

	// The handshake and the listing are out of scope, so they succeed.
	c, ctx := connect(t, fleet.ServerURL("alpha"))
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list is out of the fault's scope and should succeed: %v", err)
	}

	var req mcp.CallToolRequest
	req.Params.Name = "echo_tool"
	if _, err := c.CallTool(ctx, req); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected the call to fail with 503, got %v", err)
	}

	// The refused call is counted as a request for its method, but it never
	// reached the tool.
	counters := srv.Counters().Snapshot()
	if counters.Methods["tools/call"] != 1 {
		t.Errorf("expected the faulted tools/call to be counted, got methods %v", counters.Methods)
	}
	if counters.Calls["echo_tool"] != 0 {
		t.Errorf("a call answered by an injected status must not count as reaching the tool, got %v", counters.Calls)
	}
	if counters.Initializes != 1 {
		t.Errorf("expected one successful handshake, got %d", counters.Initializes)
	}
}

func TestPerToolFaultBreaksOneToolOnce(t *testing.T) {
	fleet, srv := newFleetWithTools(t,
		chaosmock.Tool{Name: "flaky_tool", Fault: &chaosmock.Faults{Status: 503, StatusOnce: true}},
		chaosmock.Tool{Name: "steady_tool"},
	)
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	call := func(name string) error {
		var req mcp.CallToolRequest
		req.Params.Name = name
		_, err := c.CallTool(ctx, req)
		return err
	}

	if err := call("steady_tool"); err != nil {
		t.Fatalf("a tool without a fault must be unaffected: %v", err)
	}
	if err := call("flaky_tool"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected the first flaky_tool call to fail with 503, got %v", err)
	}
	if err := call("flaky_tool"); err != nil {
		t.Fatalf("the one-shot fault should be spent after one call: %v", err)
	}

	counters := srv.Counters().Snapshot()
	if counters.Methods["tools/call"] != 3 {
		t.Errorf("expected three tools/call requests, got %v", counters.Methods)
	}
	if counters.Calls["flaky_tool"] != 1 {
		t.Errorf("expected flaky_tool to be reached once, got %v", counters.Calls)
	}
	// Spending the fault is not a change to the tool set.
	if rev := srv.Tools().Revision(); rev != 1 {
		t.Errorf("expected the revision to stay at 1, got %d", rev)
	}
}

func TestInjectedFailuresCarryTheConfiguredMessage(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	var req mcp.CallToolRequest
	req.Params.Name = "echo_tool"

	srv.SetFaults(chaosmock.Faults{Status: 503, Message: "upstream overloaded", Methods: []string{"tools/call"}})
	if _, err := c.CallTool(ctx, req); err == nil || !strings.Contains(err.Error(), "upstream overloaded") {
		t.Errorf("expected the status body to carry the message, got %v", err)
	}

	srv.SetFaults(chaosmock.Faults{ErrRate: 1, ErrKind: chaosmock.ErrKindProtocol, Message: "access forbidden by tool policy"})
	if _, err := c.CallTool(ctx, req); err == nil || !strings.Contains(err.Error(), "access forbidden by tool policy") {
		t.Errorf("expected the JSON-RPC error to carry the message, got %v", err)
	}

	srv.SetFaults(chaosmock.Faults{ErrRate: 1, ErrKind: chaosmock.ErrKindTool, Message: "quota exceeded"})
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("a tool-level error is a successful exchange: %v", err)
	}
	if text, ok := mcp.AsTextContent(res.Content[0]); !res.IsError || !ok || text.Text != "quota exceeded" {
		t.Errorf("expected an isError result reading %q, got %+v", "quota exceeded", res)
	}
}

// connectAs connects a real client with a bearer token, so the mock resolves
// the caller to whichever identity that token maps to, and returns the
// initialize result alongside the client.
func connectAs(t *testing.T, url, token string) (*client.Client, context.Context, *mcp.InitializeResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	c, err := client.NewStreamableHttpClient(url, transportHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-compat", Version: "1.0.0"}
	res, err := c.Initialize(ctx, req)
	if err != nil {
		t.Fatalf("initialize as %s failed: %v", token, err)
	}
	return c, ctx, res
}

// identityFleet seeds alpha with two bearer identities, alice and bob, plus
// whatever else seed declares.
func identityFleet(t *testing.T, seed chaosmock.Seed) (*chaosmock.Fleet, *chaosmock.Server) {
	t.Helper()
	seed.Auth = &chaosmock.Auth{Bearer: map[string]string{"token-a": "alice", "token-b": "bob"}}
	if err := seed.Validate(); err != nil {
		t.Fatalf("invalid seed: %v", err)
	}
	fleet := chaosmock.NewFleet()
	srv := fleet.AddSeeded("alpha", seed)
	fleet.Start()
	t.Cleanup(fleet.Stop)
	return fleet, srv
}

func promptNames(prompts []mcp.Prompt) string {
	names := make([]string, len(prompts))
	for i, p := range prompts {
		names[i] = p.Name
	}
	return strings.Join(names, ",")
}

func readText(t *testing.T, res *mcp.ReadResourceResult) string {
	t.Helper()
	if len(res.Contents) != 1 {
		t.Fatalf("expected one content block, got %d", len(res.Contents))
	}
	text, ok := res.Contents[0].(mcp.TextResourceContents)
	if !ok {
		t.Fatalf("expected text contents, got %T", res.Contents[0])
	}
	return text.Text
}

func TestRealClientDiscoversPromptsAndResourcesPerIdentity(t *testing.T) {
	fleet, _ := identityFleet(t, chaosmock.Seed{
		Tools: []chaosmock.Tool{{Name: "echo_tool"}},
		Prompts: []chaosmock.Prompt{{Name: "summarize", Description: "shared", Text: "Summarize {topic}",
			Arguments: []chaosmock.PromptArgument{{Name: "topic", Required: true}}}},
		PromptViews: map[string][]chaosmock.Prompt{"alice": {
			{Name: "summarize", Description: "alice's", Text: "alice summarizes {topic}"}, {Name: "alice_only"},
		}},
		Resources:     []chaosmock.Resource{{URI: "file:///readme.md", Name: "readme", MimeType: "text/markdown", Text: "readme"}},
		ResourceViews: map[string][]chaosmock.Resource{"alice": {{URI: "file:///alice/notes.txt", Name: "notes", Text: "alice's notes"}}},
		Templates:     []chaosmock.ResourceTemplate{{URITemplate: "notes://{id}", Name: "note", Text: "note {uri}"}},
		TemplateViews: map[string][]chaosmock.ResourceTemplate{"alice": {{URITemplate: "docs://{name}", Name: "doc", Text: "alice's doc {uri}"}}},
	})

	alice, ctx, initRes := connectAs(t, fleet.ServerURL("alpha"), "token-a")
	if initRes.Capabilities.Prompts == nil || initRes.Capabilities.Resources == nil || initRes.Capabilities.Tools == nil {
		t.Fatalf("expected tools, prompts and resources advertised, got %+v", initRes.Capabilities)
	}

	prompts, err := alice.ListPrompts(ctx, mcp.ListPromptsRequest{})
	if err != nil {
		t.Fatalf("prompts/list as alice failed: %v", err)
	}
	if got := promptNames(prompts.Prompts); got != "summarize,alice_only" {
		t.Errorf("expected alice's own prompts, got %s", got)
	}
	var get mcp.GetPromptRequest
	get.Params.Name = "summarize"
	get.Params.Arguments = map[string]string{"topic": "rivers"}
	got, err := alice.GetPrompt(ctx, get)
	if err != nil {
		t.Fatalf("prompts/get as alice failed: %v", err)
	}
	if text, ok := mcp.AsTextContent(got.Messages[0].Content); got.Description != "alice's" || !ok ||
		text.Text != "alice summarizes rivers" || got.Messages[0].Role != mcp.RoleUser {
		t.Errorf("expected alice's own summarize, got %+v", got)
	}
	get.Params.Name = "nobody"
	if _, err := alice.GetPrompt(ctx, get); !errors.Is(err, mcp.ErrInvalidParams) {
		t.Errorf("expected an unknown prompt to be invalid params, got %v", err)
	}

	resources, err := alice.ListResources(ctx, mcp.ListResourcesRequest{})
	if err != nil || len(resources.Resources) != 1 || resources.Resources[0].URI != "file:///alice/notes.txt" {
		t.Fatalf("expected alice's one resource, got %+v, %v", resources, err)
	}
	templates, err := alice.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
	if err != nil || len(templates.ResourceTemplates) != 1 {
		t.Fatalf("expected alice's one template, got %+v, %v", templates, err)
	}
	docs := templates.ResourceTemplates[0].URITemplate
	if docs == nil || docs.Raw() != "docs://{name}" {
		t.Fatalf("expected the client to parse docs://{name}, got %+v", templates.ResourceTemplates[0])
	}

	read := func(c *client.Client, uri string) (*mcp.ReadResourceResult, error) {
		var req mcp.ReadResourceRequest
		req.Params.URI = uri
		return c.ReadResource(ctx, req)
	}
	res, err := read(alice, "docs://setup.md")
	if err != nil || readText(t, res) != "alice's doc docs://setup.md" {
		t.Errorf("expected a read through alice's template, got %+v, %v", res, err)
	}
	res, err = read(alice, "file:///alice/notes.txt")
	if err != nil || readText(t, res) != "alice's notes" {
		t.Errorf("expected alice's own resource, got %+v, %v", res, err)
	}
	// alice's view replaces the base list, so the shared readme is not hers.
	if _, err := read(alice, "file:///readme.md"); !errors.Is(err, mcp.ErrResourceNotFound) {
		t.Errorf("expected the base readme to be not found for alice, got %v", err)
	}

	bob, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-b")
	prompts, err = bob.ListPrompts(ctx, mcp.ListPromptsRequest{})
	if err != nil || promptNames(prompts.Prompts) != "summarize" || prompts.Prompts[0].Description != "shared" ||
		len(prompts.Prompts[0].Arguments) != 1 || !prompts.Prompts[0].Arguments[0].Required {
		t.Errorf("expected bob to see the base summarize with its argument, got %+v, %v", prompts, err)
	}
	res, err = read(bob, "file:///readme.md")
	if err != nil {
		t.Fatalf("resources/read as bob failed: %v", err)
	}
	if text := res.Contents[0].(mcp.TextResourceContents); text.Text != "readme" || text.MIMEType != "text/markdown" {
		t.Errorf("expected the base readme with its mime type, got %+v", text)
	}
	res, err = read(bob, "notes://42")
	if err != nil || readText(t, res) != "note notes://42" {
		t.Errorf("expected bob's template read, got %+v, %v", res, err)
	}
	// A template variable does not cross '/', on the mock or in the client.
	var notes mcp.URITemplate
	if err := json.Unmarshal([]byte(`"notes://{id}"`), &notes); err != nil {
		t.Fatalf("the client cannot parse notes://{id}: %v", err)
	}
	if _, err := read(bob, "notes://a/b"); !errors.Is(err, mcp.ErrResourceNotFound) {
		t.Errorf("expected notes://a/b to be not found, got %v", err)
	}
	if notes.Regexp().MatchString("notes://a/b") {
		t.Errorf("the client's matcher disagrees with the mock about notes://a/b")
	}
	if _, err := read(bob, "docs://setup.md"); !errors.Is(err, mcp.ErrResourceNotFound) {
		t.Errorf("expected alice's template to be not found for bob, got %v", err)
	}
}

func TestCapabilitiesFollowWhatTheServerHoldsNotTheCaller(t *testing.T) {
	// Only alice holds a prompt, yet bob is told prompts exist too: the
	// capability describes the server, and his own list is simply empty.
	fleet, _ := identityFleet(t, chaosmock.Seed{
		PromptViews: map[string][]chaosmock.Prompt{"alice": {{Name: "alice_only"}}},
	})
	bob, ctx, initRes := connectAs(t, fleet.ServerURL("alpha"), "token-b")
	if initRes.Capabilities.Prompts == nil {
		t.Fatalf("expected prompts advertised to bob, got %+v", initRes.Capabilities)
	}
	if initRes.Capabilities.Resources != nil {
		t.Errorf("expected no resources capability without resources or templates, got %+v", initRes.Capabilities)
	}
	prompts, err := bob.ListPrompts(ctx, mcp.ListPromptsRequest{})
	if err != nil || len(prompts.Prompts) != 0 {
		t.Errorf("expected bob's prompt list to be empty, got %+v, %v", prompts, err)
	}
}

func TestRealClientReadsOutputSchemaAndStructuredContent(t *testing.T) {
	fleet, _ := newFleetWithTools(t, chaosmock.Tool{
		Name:         "sum_tool",
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"tool":{"type":"string"},"arguments":{"type":"object"}}}`),
	})
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	if out := listed.Tools[0].OutputSchema; out.Type != "object" || len(out.Properties) != 2 {
		t.Errorf("expected the output schema to reach the client, got %+v", out)
	}

	var req mcp.CallToolRequest
	req.Params.Name = "sum_tool"
	req.Params.Arguments = map[string]any{"a": 1}
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok || sc["tool"] != "sum_tool" || at(sc, "arguments", "a") != float64(1) {
		t.Fatalf("expected structuredContent naming the tool and its arguments, got %#v", res.StructuredContent)
	}
	// The text block mirrors the structured result, as MCP asks of a server
	// for clients that do not read structuredContent.
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok || text.Text != `{"tool":"sum_tool","arguments":{"a":1}}` {
		t.Errorf("expected the text block to be the structured result's JSON, got %+v", res.Content)
	}
}

func TestListFaultsFailEveryListMethod(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{
		Tools:     []chaosmock.Tool{{Name: "echo_tool"}},
		Prompts:   []chaosmock.Prompt{{Name: "summarize"}},
		Resources: []chaosmock.Resource{{URI: "file:///readme.md"}},
		Templates: []chaosmock.ResourceTemplate{{URITemplate: "notes://{id}"}},
	})
	c, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")

	lists := map[string]func() error{
		"tools/list":   func() error { _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); return err },
		"prompts/list": func() error { _, err := c.ListPrompts(ctx, mcp.ListPromptsRequest{}); return err },
		"resources/list": func() error {
			_, err := c.ListResources(ctx, mcp.ListResourcesRequest{})
			return err
		},
		"resources/templates/list": func() error {
			_, err := c.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
			return err
		},
	}

	srv.SetFaults(chaosmock.Faults{ListFail: true, Message: "listing is down"})
	for method, list := range lists {
		if err := list(); err == nil || !strings.Contains(err.Error(), "listing is down") {
			t.Errorf("%s: expected list_fail to answer with its message, got %v", method, err)
		}
	}
	srv.SetFaults(chaosmock.Faults{Malformed: true})
	for method, list := range lists {
		if err := list(); err == nil {
			t.Errorf("%s: expected malformed to break the listing", method)
		}
	}
	srv.ClearFaults()
	for method, list := range lists {
		if err := list(); err != nil {
			t.Errorf("%s: expected the listing to recover, got %v", method, err)
		}
	}
	if got := srv.Counters().Snapshot().Faulted; got != 8 {
		t.Errorf("expected eight injected failures, got %d", got)
	}
}

func TestIdentityFaultsReplaceTheServerWideSetForThatCaller(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{
		Tools:   []chaosmock.Tool{{Name: "echo_tool"}},
		Prompts: []chaosmock.Prompt{{Name: "summarize"}},
	})
	base := fleet.URL() + "/control/alpha"
	alice, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")
	bob, _, _ := connectAs(t, fleet.ServerURL("alpha"), "token-b")

	// alice's listings fail while bob's do not, on every list method.
	post(t, base+"/faults/identity", map[string]any{"identity": "alice", "faults": map[string]any{"list_fail": true}})
	if _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); err == nil {
		t.Errorf("expected alice's tools/list to fail")
	}
	if _, err := alice.ListPrompts(ctx, mcp.ListPromptsRequest{}); err == nil {
		t.Errorf("expected alice's prompts/list to fail")
	}
	if _, err := bob.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Errorf("bob's tools/list must be unaffected: %v", err)
	}

	// alice's set replaces the server-wide one: a server-wide 503 does not
	// reach her, while it does reach bob.
	srv.SetFaults(chaosmock.Faults{Status: 503, Methods: []string{"tools/call"}})
	var call mcp.CallToolRequest
	call.Params.Name = "echo_tool"
	if _, err := alice.CallTool(ctx, call); err != nil {
		t.Errorf("alice's own fault set has no 503, so her call should work: %v", err)
	}
	if _, err := bob.CallTool(ctx, call); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("expected bob's call to see the server-wide 503, got %v", err)
	}

	// A one-shot status in alice's set fires once, for her only.
	post(t, base+"/faults/identity", map[string]any{"identity": "alice", "faults": map[string]any{"status": 429, "status_once": true}})
	if _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("expected alice's one-shot 429, got %v", err)
	}
	if _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Errorf("alice's one-shot status should be spent: %v", err)
	}

	post(t, base+"/faults/identity", map[string]any{"identity": "alice", "clear": true})
	if _, err := alice.CallTool(ctx, call); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("with her set cleared, alice should see the server-wide 503, got %v", err)
	}
}

func TestInstructionsPerIdentityFallBackToTheServerWideText(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{
		Tools:                []chaosmock.Tool{{Name: "echo_tool"}},
		Instructions:         "everyone reads this",
		IdentityInstructions: map[string]string{"alice": "alice reads this"},
	})
	_, _, aliceInit := connectAs(t, fleet.ServerURL("alpha"), "token-a")
	_, _, bobInit := connectAs(t, fleet.ServerURL("alpha"), "token-b")
	if aliceInit.Instructions != "alice reads this" {
		t.Errorf("expected alice's own instructions, got %q", aliceInit.Instructions)
	}
	if bobInit.Instructions != "everyone reads this" {
		t.Errorf("expected bob to fall back to the server-wide instructions, got %q", bobInit.Instructions)
	}

	// With the server-wide text gone, alice still has hers and bob has none.
	srv.SetInstructions("")
	_, _, aliceInit = connectAs(t, fleet.ServerURL("alpha"), "token-a")
	_, _, bobInit = connectAs(t, fleet.ServerURL("alpha"), "token-b")
	if aliceInit.Instructions != "alice reads this" || bobInit.Instructions != "" {
		t.Errorf("expected only alice to have instructions, got %q and %q", aliceInit.Instructions, bobInit.Instructions)
	}
}

func TestMethodsAreCountedByTheIdentityThatSentThem(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{
		Tools:     []chaosmock.Tool{{Name: "echo_tool"}},
		Prompts:   []chaosmock.Prompt{{Name: "summarize"}},
		Resources: []chaosmock.Resource{{URI: "file:///readme.md"}},
	})
	alice, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")
	bob, _, _ := connectAs(t, fleet.ServerURL("alpha"), "token-b")
	anonymous, _ := connect(t, fleet.ServerURL("alpha"))

	if _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list as alice failed: %v", err)
	}
	if _, err := alice.ListPrompts(ctx, mcp.ListPromptsRequest{}); err != nil {
		t.Fatalf("prompts/list as alice failed: %v", err)
	}
	if _, err := bob.ListResources(ctx, mcp.ListResourcesRequest{}); err != nil {
		t.Fatalf("resources/list as bob failed: %v", err)
	}
	if _, err := anonymous.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("anonymous tools/list failed: %v", err)
	}

	byIdentity := srv.Counters().Snapshot().MethodsByIdentity
	// mcp-go's Initialize probes server/discover before it initializes, under the
	// same credential, so each identity is attributed that probe too.
	want := map[string]map[string]int64{
		"alice": {"server/discover": 1, "initialize": 1, "notifications/initialized": 1, "tools/list": 1, "prompts/list": 1},
		"bob":   {"server/discover": 1, "initialize": 1, "notifications/initialized": 1, "resources/list": 1},
	}
	if len(byIdentity) != len(want) {
		t.Errorf("expected only alice and bob to be attributed, got %v", byIdentity)
	}
	for identity, methods := range want {
		for method, n := range methods {
			if got := byIdentity[identity][method]; got != n {
				t.Errorf("%s %s: expected %d, got %d (all: %v)", identity, method, n, got, byIdentity)
			}
		}
		if len(byIdentity[identity]) != len(methods) {
			t.Errorf("%s: expected exactly %v, got %v", identity, methods, byIdentity[identity])
		}
	}
	if srv.Counters().Snapshot().Methods["tools/list"] != 2 {
		t.Errorf("the per-method tally still counts everyone, got %v", srv.Counters().Snapshot().Methods)
	}
}
