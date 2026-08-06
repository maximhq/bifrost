package bifrost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// startStubMCPServer spins up a minimal in-process JSON-RPC HTTP server that answers
// initialize/tools/list/ping, enough for connectToMCPClient to succeed and populate a
// real ToolMap (which is what triggers notifyToolsUpdated on the success path).
func startStubMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		method, _ := req["method"].(string)
		id := req["id"]

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "stub", "version": "0.1"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "echo", "description": "echo text", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			})
		case "ping":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		default:
			if id == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "nope"}})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestLazyMCPManagerGetsToolsUpdatedHook covers the #4998 fix's lazy-init gap:
// SetOnMCPToolsUpdated, called once at boot when MCPConfig is nil (so no MCPManager
// exists yet), must still reach an MCPManager constructed later by a lazy-init path
// (AddMCPClient here). Without applyMCPToolsUpdatedHook wired into that lazy path, the
// hook would be permanently nil on the manager actually serving traffic, silently
// reintroducing the gateway staleness bug for any deployment that starts with no MCP
// config and adds clients dynamically afterward.
func TestLazyMCPManagerGetsToolsUpdatedHook(t *testing.T) {
	stub := startStubMCPServer(t)

	bf, err := Init(context.Background(), schemas.BifrostConfig{
		Account: NewMockAccount(),
		// MCPConfig intentionally nil: mirrors a process that boots with no MCP
		// configured, so MCPManager is nil at Init time.
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer bf.Shutdown()

	if bf.MCPManager != nil {
		t.Fatalf("expected MCPManager to be nil before any client is added")
	}

	var hookCalls int32
	done := make(chan struct{}, 1)
	bf.SetOnMCPToolsUpdated(func() {
		atomic.AddInt32(&hookCalls, 1)
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// This is the lazy-init path under test: MCPManager is nil, so AddMCPClient
	// constructs a fresh one via mcpInitOnce. Before the fix, that fresh manager's
	// onToolsUpdated hook would be nil regardless of the SetOnMCPToolsUpdated call
	// above, since it ran before this manager existed.
	if err := bf.AddMCPClient(context.Background(), &schemas.MCPClientConfig{
		ID:               "stub-1",
		Name:             "stub",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(stub.URL + "/mcp"),
		AuthType:         schemas.MCPAuthTypeNone,
	}); err != nil {
		t.Fatalf("AddMCPClient failed: %v", err)
	}

	if bf.MCPManager == nil {
		t.Fatalf("expected AddMCPClient to lazily construct MCPManager")
	}

	select {
	case <-done:
		// Hook fired — the lazily-constructed manager was correctly wired.
	case <-time.After(2 * time.Second):
		t.Fatalf("onToolsUpdated hook never fired after AddMCPClient connected successfully; " +
			"lazily-constructed MCPManager did not have the hook wired (hookCalls=%d)", atomic.LoadInt32(&hookCalls))
	}
}
