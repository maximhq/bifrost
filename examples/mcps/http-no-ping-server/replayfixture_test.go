package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

func replayFixtureRequest(t *testing.T, method, url, body, session string) (int, []byte, http.Header, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp.Header, readErr
}

func TestReplaySafetyFixture_PostCommitFaultsAndControls(t *testing.T) {
	for _, tool := range []string{"replay_append", "replay_additive", "replay_destructive", "replay_read", "replay_upsert"} {
		for _, fault := range []string{"503", "401", "reset"} {
			t.Run(tool+"/"+fault, func(t *testing.T) {
				t.Setenv("MCP_REPLAY_SAFETY_FIXTURE", "1")
				s := server.NewMCPServer("fixture-test", "1", server.WithToolCapabilities(true))
				ts := httptest.NewServer(withReplaySafetyFixture(s, server.NewStreamableHTTPServer(s)))
				defer ts.Close()
				status, body, _, err := replayFixtureRequest(t, "POST", ts.URL+"/_replay/scenarios", fmt.Sprintf(`{"id":"one","tool":%q,"fault":%q}`, tool, fault), "")
				if err != nil || status != 200 {
					t.Fatalf("seed: status=%d body=%s err=%v", status, body, err)
				}
				status, body, headers, err := replayFixtureRequest(t, "POST", ts.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture-test","version":"1"}}}`, "")
				if err != nil || status != 200 {
					t.Fatalf("initialize: status=%d body=%s err=%v", status, body, err)
				}
				session := headers.Get("Mcp-Session-Id")
				call := func(id int) (int, []byte, error) {
					status, data, _, err := replayFixtureRequest(t, "POST", ts.URL, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{"scenario":"one"}}}`, id, tool), session)
					return status, data, err
				}
				status, body, err = call(2)
				if fault == "reset" {
					if err == nil {
						t.Fatal("expected response read failure after commit")
					}
				} else {
					want := 503
					if fault == "401" {
						want = 401
					}
					if err != nil || status != want {
						t.Fatalf("fault: status=%d body=%s err=%v", status, body, err)
					}
				}
				status, body, _, err = replayFixtureRequest(t, "GET", ts.URL+"/_replay/scenarios/one", "", "")
				var first replayScenario
				if err != nil || status != 200 || json.Unmarshal(body, &first) != nil || first.WireCalls != 1 || first.Invocations != 1 {
					t.Fatalf("first invocation missing: %s err=%v", body, err)
				}
				if tool == "replay_read" {
					if first.Commits != 0 {
						t.Fatal("read-only tool mutated")
					}
				} else if first.Commits != 1 {
					t.Fatal("fault occurred before the mutation committed")
				}
				status, body, err = call(3)
				if err != nil || status != 200 {
					t.Fatalf("second call: status=%d body=%s err=%v", status, body, err)
				}
				_, body, _, err = replayFixtureRequest(t, "GET", ts.URL+"/_replay/scenarios/one", "", "")
				var second replayScenario
				if err != nil || json.Unmarshal(body, &second) != nil || second.WireCalls != 2 || second.Invocations != 2 {
					t.Fatalf("second invocation missing: %s err=%v", body, err)
				}
				want := 2
				if tool == "replay_read" {
					want = 0
				}
				if tool == "replay_upsert" {
					want = 1
				}
				if second.Commits != want {
					t.Fatalf("wrong effect count: got %d want %d", second.Commits, want)
				}
				status, _, _, err = replayFixtureRequest(t, "DELETE", ts.URL+"/_replay/scenarios/one", "", "")
				if err != nil || status != 200 {
					t.Fatal("scenario cleanup failed")
				}
			})
		}
	}
}

func TestReplaySafetyFixture_OptInAndBounds(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	t.Setenv("MCP_REPLAY_SAFETY_FIXTURE", "0")
	next := withReplaySafetyFixture(server.NewMCPServer("off", "1"), base)
	response := httptest.NewRecorder()
	next.ServeHTTP(response, httptest.NewRequest("GET", "/_replay/scenarios/one", nil))
	if response.Code != 204 {
		t.Fatal("fixture exposed without opt-in")
	}
	t.Setenv("MCP_REPLAY_SAFETY_FIXTURE", "1")
	f := withReplaySafetyFixture(server.NewMCPServer("on", "1"), base)
	post := func(body string) int {
		w := httptest.NewRecorder()
		f.ServeHTTP(w, httptest.NewRequest("POST", "/_replay/scenarios", bytes.NewBufferString(body)))
		return w.Code
	}
	for i := 0; i < replayScenarioLimit; i++ {
		if post(fmt.Sprintf(`{"id":"case_%d","tool":"replay_append","fault":"503"}`, i)) != 200 {
			t.Fatal("failed to fill bounded fixture")
		}
	}
	if post(`{"id":"overflow","tool":"replay_append","fault":"503"}`) != 429 {
		t.Fatal("unbounded scenario retention")
	}
	if post(`{"id":"case_0","tool":"replay_append","fault":"503"}`) != 409 {
		t.Fatal("duplicate scenario was overwritten")
	}
	if post(`{"id":"bad/id","tool":"replay_append","fault":"503"}`) != 400 {
		t.Fatal("invalid identifier accepted")
	}
	if post(`{"id":"bad","tool":"other","fault":"503"}`) != 400 {
		t.Fatal("unknown tool accepted")
	}
}
