package mcp

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

// startNotifyTestStubServer serves a minimal JSON-RPC MCP endpoint whose tools/list
// response is controlled by the caller via toolCount (read atomically on every request,
// so a test can flip it between calls to simulate upstream drift).
func startNotifyTestStubServer(t *testing.T, toolCount *int32) *httptest.Server {
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
			n := atomic.LoadInt32(toolCount)
			tools := make([]map[string]any, 0, n)
			for i := int32(0); i < n; i++ {
				tools = append(tools, map[string]any{
					"name":        "tool" + string(rune('a'+i)),
					"description": "a test tool",
					"inputSchema": map[string]any{"type": "object"},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"tools": tools},
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

// TestPerformSyncOnlyNotifiesOnActualChange verifies performSync's diff-gating: it must
// skip notifyToolsUpdated when a sync tick's tool set is unchanged from before (the
// common steady-state case), and must fire it when the tool set genuinely changes.
// Notifying unconditionally on every tick — the prior behavior — invalidates the entire
// per-VK gateway-server cache on every tool-sync interval across every configured
// client regardless of whether anything changed, which is the cost this test guards
// against regressing back to.
func TestPerformSyncOnlyNotifiesOnActualChange(t *testing.T) {
	var toolCount int32 = 2
	stub := startNotifyTestStubServer(t, &toolCount)

	manager := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	t.Cleanup(func() { _ = manager.Cleanup() })

	var notifyCalls int32
	manager.SetOnToolsUpdated(func() {
		atomic.AddInt32(&notifyCalls, 1)
	})

	clientID := "stub-1"
	err := manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:               clientID,
		Name:             "stub",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(stub.URL + "/mcp"),
		AuthType:         schemas.MCPAuthTypeNone,
	})
	if err != nil {
		t.Fatalf("AddClient failed: %v", err)
	}

	// The initial connect itself fires notifyToolsUpdated once (connectToMCPClient's own
	// hook call, covered by a separate test) — drain that before measuring performSync's
	// behavior in isolation.
	waitForNotifyCount(t, &notifyCalls, 1, time.Second)

	syncer := NewClientToolSyncer(manager, clientID, "stub", time.Hour, nil)

	// Same tool count as connect (2): performSync must find no change and must NOT
	// notify again.
	syncer.performSync()
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&notifyCalls); got != 1 {
		t.Fatalf("expected no additional notify after an unchanged sync tick, notifyCalls=%d", got)
	}

	// Now change the upstream tool count and sync again: performSync must detect the
	// diff and notify.
	atomic.StoreInt32(&toolCount, 3)
	syncer.performSync()
	waitForNotifyCount(t, &notifyCalls, 2, time.Second)
}

func waitForNotifyCount(t *testing.T, counter *int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(counter) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for notifyCalls >= %d, got %d", want, atomic.LoadInt32(counter))
}
