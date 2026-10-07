package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestHandshakeRetryFixture(t *testing.T) {
	s := httptest.NewServer(handshakeRetryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	t.Cleanup(s.Close)
	open := func() *client.Client {
		tr, err := transport.NewStreamableHTTP(s.URL + "/handshake-retry/run-one/mcp")
		if err != nil {
			t.Fatal(err)
		}
		c := client.NewClient(tr)
		t.Cleanup(func() { _ = c.Close() })
		if err := c.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		return c
	}
	initialize := func(c *client.Client) error {
		_, err := c.Initialize(context.Background(), mcp.InitializeRequest{Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ClientInfo: mcp.Implementation{Name: "fixture-test", Version: "1"},
		}})
		return err
	}
	c := open()
	if err := initialize(c); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected transient initialized failure, got %v", err)
	}
	if err := initialize(c); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected same-session rejection, got %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := open()
	if err := initialize(fresh); err != nil {
		t.Fatal(err)
	}
	tools, err := fresh.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("tools=%+v error=%v", tools, err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(s.URL + "/api/handshake-retry/run-one")
	if err != nil {
		t.Fatal(err)
	}
	var state handshakeRetryState
	err = json.NewDecoder(resp.Body).Decode(&state)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("state read: %v status=%d", err, resp.StatusCode)
	}
	want := []handshakeRetryRequest{
		{"initialize", "", 200}, {"notifications/initialized", "session-1", 503}, {"initialize", "session-1", 400},
		{"initialize", "", 200}, {"notifications/initialized", "session-2", 202}, {"tools/list", "session-2", 200},
	}
	if state.Sessions != 2 || !reflect.DeepEqual(state.Requests, want) || !reflect.DeepEqual(state.ClosedSessions, []string{"session-1", "session-2"}) {
		t.Fatalf("unexpected witness: %+v", state)
	}
	for _, url := range []string{"/api/handshake-retry/run-two", "/handshake-retry//mcp"} {
		resp, err := http.Get(s.URL + url)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("%s status=%d", url, resp.StatusCode)
		}
	}
	request, _ := http.NewRequest(http.MethodDelete, s.URL+"/api/handshake-retry/run-one", nil)
	resp, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("fixture cleanup status=%d", resp.StatusCode)
	}
	resp, err = http.Get(s.URL + "/api/handshake-retry/run-one")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("fixture remains after cleanup: %d", resp.StatusCode)
	}
	resp, err = http.Get(s.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ordinary endpoint changed: %d", resp.StatusCode)
	}
}
