package chaosmock_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// allVersions is a server speaking every revision, newest first.
var allVersions = []string{chaosmock.ProtocolVersion20260728, chaosmock.ProtocolVersion20251125, chaosmock.ProtocolVersion20250618}

// connectAt connects a real client asking for version, "" for the client's own default.
func connectAt(t *testing.T, url, version string) (*client.Client, context.Context) {
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
	req.Params.ProtocolVersion = version
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-versions", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, req); err != nil {
		t.Fatalf("initialize at %q failed: %v", version, err)
	}
	return c, ctx
}

// TestDefaultServerNegotiatesThroughInitialize: a server left on its default versions answers
// server/discover with method-not-found, so a current client falls back to initialize, asks for
// 2025-11-25 and gets the newest version the server speaks.
func TestDefaultServerNegotiatesThroughInitialize(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo"})
	c, _ := connectAt(t, fleet.ServerURL("alpha"), "")

	if got := c.ProtocolVersion(); got != chaosmock.ProtocolVersion20250618 {
		t.Fatalf("negotiated %q, want 2025-06-18", got)
	}
	counters := srv.Counters().Snapshot()
	want := []chaosmock.Handshake{{Method: chaosmock.MethodInitialize, Requested: chaosmock.ProtocolVersion20251125, Negotiated: chaosmock.ProtocolVersion20250618}}
	if !reflect.DeepEqual(counters.Handshakes, want) {
		t.Fatalf("handshakes = %+v, want %+v", counters.Handshakes, want)
	}
	if counters.Methods[chaosmock.MethodServerDiscover] != 1 || counters.Sessions != 1 {
		t.Fatalf("want one refused server/discover and one session, got methods %v sessions %d", counters.Methods, counters.Sessions)
	}
}

// TestModernServerServesTheSessionlessCore: a server set to speak 2026-07-28 answers
// server/discover, serves calls with no session, and delivers list_changed on a listen stream.
func TestModernServerServesTheSessionlessCore(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo"})
	if err := srv.SetProtocolVersions(allVersions); err != nil {
		t.Fatal(err)
	}
	c, ctx := connectAt(t, fleet.ServerURL("alpha"), "")
	if got := c.ProtocolVersion(); got != chaosmock.ProtocolVersion20260728 {
		t.Fatalf("negotiated %q, want 2026-07-28", got)
	}

	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("tools/list = %v, %v", tools, err)
	}
	call := mcp.CallToolRequest{}
	call.Params.Name = "echo"
	if _, err := c.CallTool(ctx, call); err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}

	var changed atomic.Bool
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == chaosmock.NotificationToolListChanged {
			changed.Store(true)
		}
	})
	stop, err := c.ListenAsync(ctx, mcp.SubscriptionFilter{ToolsListChanged: true}, nil)
	if err != nil {
		t.Fatalf("subscriptions/listen failed: %v", err)
	}
	t.Cleanup(stop)
	if !waitFor(t, 5*time.Second, func() bool { return srv.OpenStreams() == 1 }) {
		t.Fatal("the listen stream never opened")
	}
	srv.NotifyToolListChanged()
	if !waitFor(t, 5*time.Second, changed.Load) {
		t.Fatal("list_changed never reached the listen stream")
	}

	counters := srv.Counters().Snapshot()
	want := []chaosmock.Handshake{{Method: chaosmock.MethodServerDiscover, Requested: chaosmock.ProtocolVersion20260728, Negotiated: chaosmock.ProtocolVersion20260728}}
	if !reflect.DeepEqual(counters.Handshakes, want) {
		t.Fatalf("handshakes = %+v, want %+v", counters.Handshakes, want)
	}
	if counters.Sessions != 0 || counters.Initializes != 0 || counters.Calls["echo"] != 1 {
		t.Fatalf("want no session, no initialize and one call, got sessions %d initializes %d calls %v", counters.Sessions, counters.Initializes, counters.Calls)
	}
}

// TestListenStreamIsRegisteredByItsAcknowledgement: once a client has read
// subscriptions/acknowledged, its stream is already on the broadcast list, so a change announced
// straight after the acknowledgement reaches it. The window is narrow, so it is opened many times.
func TestListenStreamIsRegisteredByItsAcknowledgement(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo"})
	if err := srv.SetProtocolVersions(allVersions); err != nil {
		t.Fatal(err)
	}
	listen := `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fleet.ServerURL("alpha"), strings.NewReader(listen))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(chaosmock.HeaderProtocolVersion, chaosmock.ProtocolVersion20260728)
		req.Header.Set(chaosmock.HeaderMcpMethod, chaosmock.MethodSubscriptionsListen)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("subscriptions/listen failed: %v", err)
		}
		acknowledged := false
		for scanner := bufio.NewScanner(resp.Body); !acknowledged && scanner.Scan(); {
			acknowledged = strings.HasPrefix(scanner.Text(), "data:") && strings.Contains(scanner.Text(), chaosmock.NotificationSubscriptionsAcknowledged)
		}
		open := srv.OpenStreams()
		cancel()
		resp.Body.Close()
		if !acknowledged {
			t.Fatalf("attempt %d: the stream ended without an acknowledgement", i)
		}
		if open != 1 {
			t.Fatalf("attempt %d: the acknowledgement arrived with %d open streams, want 1", i, open)
		}
		if !waitFor(t, 5*time.Second, func() bool { return srv.OpenStreams() == 0 }) {
			t.Fatalf("attempt %d: the stream never closed", i)
		}
	}
}

// TestClientsNegotiateTheVersionTheyAskFor: on a server speaking every revision, a client pinned to
// an earlier one gets exactly that one through initialize, and a server without 2026-07-28 hands a
// current client the newest initialize revision it speaks.
func TestClientsNegotiateTheVersionTheyAskFor(t *testing.T) {
	cases := []struct {
		name, ask, want string
		server          []string
	}{
		{"pinned to 2025-11-25", chaosmock.ProtocolVersion20251125, chaosmock.ProtocolVersion20251125, allVersions},
		{"pinned to 2025-06-18", chaosmock.ProtocolVersion20250618, chaosmock.ProtocolVersion20250618, allVersions},
		{"no 2026-07-28 on the server", "", chaosmock.ProtocolVersion20251125, []string{chaosmock.ProtocolVersion20251125, chaosmock.ProtocolVersion20250618}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo"})
			if err := srv.SetProtocolVersions(tc.server); err != nil {
				t.Fatal(err)
			}
			c, _ := connectAt(t, fleet.ServerURL("alpha"), tc.ask)
			if got := c.ProtocolVersion(); got != tc.want {
				t.Fatalf("negotiated %q, want %q", got, tc.want)
			}
			handshakes := srv.Counters().Snapshot().Handshakes
			if len(handshakes) != 1 || handshakes[0].Method != chaosmock.MethodInitialize || handshakes[0].Negotiated != tc.want {
				t.Fatalf("handshakes = %+v", handshakes)
			}
		})
	}
}

// postModern sends one sessionless request with the given headers and returns the status and the
// JSON-RPC error it carried.
func postModern(t *testing.T, url string, headers map[string]string, body string) (int, *struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Error *struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Error
}

// TestSessionlessRequestsAreChecked: a sessionless request whose headers disagree with its body,
// or that asks for a version the server does not speak, is refused the way 2026-07-28 prescribes,
// and a bridged stdio request is never treated as one.
func TestSessionlessRequestsAreChecked(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo"})
	if err := srv.SetProtocolVersions(allVersions); err != nil {
		t.Fatal(err)
	}
	url := fleet.ServerURL("alpha")
	meta := func(version string) string {
		return `"_meta":{"io.modelcontextprotocol/protocolVersion":"` + version + `"}`
	}
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + meta("2026-07-28") + `}}`
	headers := func(version, method, name string) map[string]string {
		h := map[string]string{chaosmock.HeaderProtocolVersion: version, chaosmock.HeaderMcpMethod: method}
		if name != "" {
			h[chaosmock.HeaderMcpName] = name
		}
		return h
	}

	if status, rpcErr := postModern(t, url, headers("2026-07-28", "tools/list", ""), list); status != http.StatusOK || rpcErr != nil {
		t.Fatalf("a well-formed request: %d %+v", status, rpcErr)
	}
	if status, rpcErr := postModern(t, url, headers("2026-07-28", "tools/call", ""), list); status != http.StatusBadRequest || rpcErr == nil || rpcErr.Code != chaosmock.CodeHeaderMismatch {
		t.Fatalf("Mcp-Method disagreeing with the body: %d %+v", status, rpcErr)
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo",` + meta("2026-07-28") + `}}`
	if status, rpcErr := postModern(t, url, headers("2026-07-28", "tools/call", "other"), call); status != http.StatusBadRequest || rpcErr == nil || rpcErr.Code != chaosmock.CodeHeaderMismatch {
		t.Fatalf("Mcp-Name disagreeing with the body: %d %+v", status, rpcErr)
	}
	future := `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{` + meta("2099-01-01") + `}}`
	status, rpcErr := postModern(t, url, headers("2099-01-01", "tools/list", ""), future)
	if status != http.StatusBadRequest || rpcErr == nil || rpcErr.Code != chaosmock.CodeUnsupportedProtocolVersion {
		t.Fatalf("an unsupported version: %d %+v", status, rpcErr)
	}
	var data struct {
		Supported []string `json:"supported"`
	}
	if err := json.Unmarshal(rpcErr.Data, &data); err != nil || !reflect.DeepEqual(data.Supported, allVersions) {
		t.Fatalf("the refusal lists %s, want %v", rpcErr.Data, allVersions)
	}
	discover := `{"jsonrpc":"2.0","id":4,"method":"server/discover","params":{` + meta("2026-07-28") + `}}`
	bridged := headers("2026-07-28", "server/discover", "")
	bridged[chaosmock.HeaderStdioBridge] = "1"
	if _, rpcErr := postModern(t, url, bridged, discover); rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("a bridged server/discover must answer method-not-found, got %+v", rpcErr)
	}
}

// TestVersionsVerb: the control verb refuses an empty list or an unknown version and keeps a
// valid list newest first.
func TestVersionsVerb(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	post := func(body string) (int, string) {
		resp, err := http.Post(fleet.URL()+"/control/alpha/versions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	if status, body := post(`{"versions":[]}`); status != http.StatusBadRequest || !strings.Contains(body, "at least one") {
		t.Fatalf("empty list: %d %s", status, body)
	}
	if status, body := post(`{"versions":["1999-01-01"]}`); status != http.StatusBadRequest || !strings.Contains(body, `unknown protocol version "1999-01-01"`) {
		t.Fatalf("unknown version: %d %s", status, body)
	}
	status, body := post(`{"versions":["2025-06-18","2026-07-28"]}`)
	if status != http.StatusOK {
		t.Fatalf("valid list: %d %s", status, body)
	}
	var state chaosmock.State
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		t.Fatal(err)
	}
	if want := []string{"2026-07-28", "2025-06-18"}; !reflect.DeepEqual(state.ProtocolVersions, want) {
		t.Fatalf("protocol_versions = %v, want %v", state.ProtocolVersions, want)
	}
}
