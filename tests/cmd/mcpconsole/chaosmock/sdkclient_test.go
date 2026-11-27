package chaosmock_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// TestDrivenSDKClientCompletesOAuthAndRefreshes drives the fleet's SDK client against one of the
// fleet's own OAuth servers: connect discovers, registers and stops at the authorization URL, the
// test approves it, authorized exchanges the code and lists the tools, and a call after the access
// token expired refreshes it first. The request log shows each OAuth step in order, with the grant
// type of each token request, and no MCP request before the client holds a token.
func TestDrivenSDKClientCompletesOAuthAndRefreshes(t *testing.T) {
	fleet, _ := oauthFleet(t, map[string]any{"access_ttl": "1s"})
	clientURL := fleet.URL() + "/client/sdk"
	decoded := func(resp *http.Response) map[string]any {
		t.Helper()
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %v", resp.StatusCode, out)
		}
		return out
	}

	connectBody, _ := json.Marshal(map[string]string{"url": fleet.ServerURL("alpha"), "redirect_uri": testRedirect, "client_name": "driven"})
	connected := decoded(mustPost(t, clientURL+"/connect", connectBody))
	authURL, _ := connected["authorize_url"].(string)
	if authURL == "" || connected["client_id"] == "" || connected["client_id"] == nil {
		t.Fatalf("connect answered %v", connected)
	}

	back := authorize(t, authURL+"&login_hint=alice")
	body, _ := json.Marshal(map[string]string{"code": back.Get("code"), "state": back.Get("state")})
	authorized := decoded(mustPost(t, clientURL+"/authorized", body))
	if tools, _ := authorized["tools"].([]any); len(tools) != 1 || tools[0] != "echo_tool" {
		t.Errorf("tools = %v, want [echo_tool]", authorized["tools"])
	}

	time.Sleep(1500 * time.Millisecond)
	called := decoded(mustPost(t, clientURL+"/call", []byte(`{"tool":"echo_tool","arguments":{"text":"hi"}}`)))
	if called["is_error"] != false {
		t.Errorf("call answered %v", called)
	}

	res, err := http.Get(clientURL)
	if err != nil {
		t.Fatal(err)
	}
	var log struct {
		Requests []struct {
			Method, Path string
			Status       int
			GrantType    string `json:"grant_type"`
		} `json:"requests"`
	}
	if err := json.NewDecoder(res.Body).Decode(&log); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var steps []string
	for _, r := range log.Requests {
		if r.Path == "/s/alpha/mcp" {
			continue
		}
		if r.Status >= 300 {
			t.Errorf("request %+v failed", r)
		}
		steps = append(steps, r.Method+" "+r.Path+" "+r.GrantType)
	}
	want := []string{
		"GET /.well-known/oauth-protected-resource/s/alpha/mcp ",
		"GET /.well-known/oauth-authorization-server/s/alpha/as ",
		"POST /s/alpha/as/register ",
		"POST /s/alpha/as/token authorization_code",
		"POST /s/alpha/as/token refresh_token",
	}
	if len(steps) != len(want) {
		t.Fatalf("OAuth requests %q, want %q", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("OAuth request %d is %q, want %q", i, steps[i], want[i])
		}
	}
	for _, r := range log.Requests {
		if r.GrantType == "authorization_code" {
			break
		}
		if r.Path == "/s/alpha/mcp" {
			t.Errorf("an MCP request went out before the client held a token: %+v", r)
		}
	}

	req, _ := http.NewRequest(http.MethodDelete, clientURL, nil)
	if closed := decoded(mustDo(t, req)); closed["closed"] != true {
		t.Errorf("delete answered %v", closed)
	}
	if res, err := http.Get(clientURL); err != nil || res.StatusCode != http.StatusNotFound {
		t.Errorf("a closed client must be gone, got %v %v", res, err)
	} else {
		res.Body.Close()
	}
}

// mustPost posts a JSON body and fails the test on a transport error.
func mustPost(t *testing.T, target string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return mustDo(t, req)
}

// mustDo sends req and fails the test on a transport error.
func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", req.Method, req.URL, err)
	}
	return resp
}

// TestDrivenPlainClientOnEachProtocolEra drives a plain client, with a header and no OAuth, against
// one of the fleet's servers on 2025-11-25 and on 2026-07-28: the handshake, tools/list, a call, and
// a tools/list_changed notification on the listening stream each era uses.
func TestDrivenPlainClientOnEachProtocolEra(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	if err := srv.SetProtocolVersions([]string{"2026-07-28", "2025-11-25"}); err != nil {
		t.Fatal(err)
	}
	decoded := func(resp *http.Response) map[string]any {
		t.Helper()
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %v", resp.StatusCode, out)
		}
		return out
	}
	for _, tc := range []struct{ name, version, wantVersion string }{
		{name: "legacy", version: "2025-11-25", wantVersion: "2025-11-25"},
		{name: "modern", version: "", wantVersion: "2026-07-28"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientURL := fleet.URL() + "/client/" + tc.name
			body, _ := json.Marshal(map[string]any{"url": fleet.ServerURL("alpha"), "headers": map[string]string{"X-Probe": "1"}, "protocol_version": tc.version, "listen": true})
			opened := decoded(mustPost(t, clientURL+"/open", body))
			if opened["protocol_version"] != tc.wantVersion || opened["server_name"] != "alpha" {
				t.Fatalf("open answered %v", opened)
			}
			if hasSession := opened["session_id"] != ""; hasSession != (tc.name == "legacy") {
				t.Errorf("session_id %q on %s", opened["session_id"], tc.name)
			}

			res, err := http.Get(clientURL + "/tools")
			if err != nil {
				t.Fatal(err)
			}
			tools, _ := decoded(res)["tools"].([]any)
			if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo_tool" || tools[0].(map[string]any)["input_schema_type"] != "object" {
				t.Fatalf("tools = %v", tools)
			}
			called := decoded(mustPost(t, clientURL+"/call", []byte(`{"tool":"echo_tool","arguments":{"text":"hi"}}`)))
			if called["is_error"] != false || called["text"] != `echo_tool:{"text":"hi"}` {
				t.Errorf("call answered %v", called)
			}

			deadline := time.Now().Add(5 * time.Second)
			for {
				srv.NotifyToolListChanged()
				res, err := http.Get(clientURL + "/notifications")
				if err != nil {
					t.Fatal(err)
				}
				got, _ := decoded(res)["notifications"].([]any)
				if len(got) > 0 && got[len(got)-1] == "notifications/tools/list_changed" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no tools/list_changed within 5s, got %v", got)
				}
				time.Sleep(100 * time.Millisecond)
			}
			req, _ := http.NewRequest(http.MethodDelete, clientURL, nil)
			if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("close: %v %v", res, err)
			}
		})
	}
}

// TestStreamProbeReportsAnOpenAndARefusedStream checks the raw stream probe: a GET stream the server
// holds open reads as open with its event-stream headers, and a refused one as closed with its status.
func TestStreamProbeReportsAnOpenAndARefusedStream(t *testing.T) {
	fleet, _ := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	probe := func(target string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"url": target, "hold": "300ms"})
		resp := mustPost(t, fleet.URL()+"/client/probe/stream", body)
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("probe answered %d: %v %v", resp.StatusCode, out, err)
		}
		return out
	}
	open := probe(fleet.ServerURL("alpha"))
	if open["status"] != float64(http.StatusOK) || open["open"] != true || !strings.HasPrefix(open["headers"].(map[string]any)["Content-Type"].(string), "text/event-stream") {
		t.Errorf("open stream probed as %v", open)
	}
	refused := probe(fleet.URL() + "/s/nosuch/mcp")
	if refused["status"] != float64(http.StatusNotFound) || refused["open"] != false {
		t.Errorf("refused stream probed as %v", refused)
	}
}

// TestDrivenPlainClientReportsAFailedHandshake checks that a handshake the server refuses answers
// 502 with the SDK's error and every request the client made, each with the status it got.
func TestDrivenPlainClientReportsAFailedHandshake(t *testing.T) {
	fleet, _ := newFleetWithTools(t, chaosmock.Tool{Name: "echo_tool"})
	body, _ := json.Marshal(map[string]any{"url": fleet.URL() + "/s/nosuch/mcp"})
	resp := mustPost(t, fleet.URL()+"/client/refused/open", body)
	defer resp.Body.Close()
	var out struct {
		Error    string `json:"error"`
		Requests []struct {
			Method string `json:"method"`
			Status int    `json:"status"`
		} `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway || !strings.HasPrefix(out.Error, "handshake: ") || len(out.Requests) == 0 {
		t.Fatalf("open answered %d: %+v", resp.StatusCode, out)
	}
	for _, r := range out.Requests {
		if r.Status != http.StatusNotFound {
			t.Errorf("request %s answered %d, want 404", r.Method, r.Status)
		}
	}
}
