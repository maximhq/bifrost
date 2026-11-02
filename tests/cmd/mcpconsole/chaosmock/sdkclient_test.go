package chaosmock_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
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
