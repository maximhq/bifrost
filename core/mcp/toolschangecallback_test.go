package mcp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolsChangeCall records one invocation of a registered SetDiscoveryChangeCallback.
type toolsChangeCall struct {
	clientID, name  string
	tools           map[string]schemas.ChatTool
	toolNameMapping map[string]string
}

// TestSetClientDiscovery_FiresToolsChangeCallback pins the funnel's simplest
// entry point: SetClientDiscovery (used by per-call clients' own AddClient/
// UpdateClientCredentials discovery, and directly by admin-verify HTTP
// handlers) must notify a registered callback with the fresh result so the
// transport layer can persist it — core/mcp has no DB access of its own.
func TestSetClientDiscovery_FiresToolsChangeCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "client-one"}
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config}
	m.mu.Unlock()

	var calls []toolsChangeCall
	m.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		tools, toolNameMapping := discovery.Tools, discovery.ToolNameMapping
		calls = append(calls, toolsChangeCall{clientID, name, tools, toolNameMapping})
	})

	tools := map[string]schemas.ChatTool{"echo": {Type: "function"}}
	mapping := map[string]string{"echo": "echo-server"}
	m.SetClientDiscovery(config.ID, &schemas.MCPDiscovery{Tools: tools, ToolNameMapping: mapping})

	require.Len(t, calls, 1)
	assert.Equal(t, config.ID, calls[0].clientID)
	assert.Equal(t, config.Name, calls[0].name)
	assert.Equal(t, tools, calls[0].tools)
	assert.Equal(t, mapping, calls[0].toolNameMapping)
}

// TestSetClientDiscovery_UnknownClient_DoesNotFireCallback confirms the callback
// only fires for a real mutation — calling SetClientDiscovery for a client that
// isn't (or is no longer) registered must not notify a stale/nonexistent
// entry.
// Callbacks fire after the manager's lock is released, so two changes can fire out of order. Each
// delivers the client's discovery as installed when it fires, so the last one delivered is current.
func TestDiscoveryChanged_LateCallbackDeliversTheCurrentDiscovery(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	var delivered []string
	m.SetDiscoveryChangeCallback(func(_, _ string, discovery *schemas.MCPDiscovery) {
		delivered = append(delivered, discovery.Instructions)
	})
	state := &schemas.MCPClientState{Name: "docs", ExecutionConfig: &schemas.MCPClientConfig{ID: "docs-id", Name: "docs"}}
	m.clientMap["docs-id"] = state

	m.mu.Lock()
	m.discoveryStore.SetDiscovery(m.ctx, state.ExecutionConfig, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, Instructions: "first"})
	older := m.discoveryChanged(state, "docs-id")
	m.discoveryStore.SetDiscovery(m.ctx, state.ExecutionConfig, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, Instructions: "second"})
	newer := m.discoveryChanged(state, "docs-id")
	m.mu.Unlock()
	require.NotNil(t, older)
	require.NotNil(t, newer)

	newer()
	older() // fires last
	assert.Equal(t, []string{"second", "second"}, delivered, "a late callback must not deliver a superseded discovery")
}

func TestSetClientDiscovery_UnknownClient_DoesNotFireCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)

	fired := false
	m.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		fired = true
	})

	m.SetClientDiscovery("does-not-exist", &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, ToolNameMapping: map[string]string{}})

	assert.False(t, fired)
}

// TestConnectToMCPClient_SuccessfulDial_FiresToolsChangeCallback covers the
// sticky-client funnel entry point: a live connect/reconnect that discovers
// tools via list_tools must also notify, exactly like the per-call path —
// sticky clients have the identical "freshly discovered tools never reach
// the DB" gap per-call ones do, so both must go through the same seam.
func TestConnectToMCPClient_SuccessfulDial_FiresToolsChangeCallback(t *testing.T) {
	ts := buildGatedMCPServer(t, nil)

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	config := newMakeBeforeBreakConfig("sticky-dial-client", ts.URL)
	toolName := config.Name + "-echo"

	var calls []toolsChangeCall
	m.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		tools, toolNameMapping := discovery.Tools, discovery.ToolNameMapping
		calls = append(calls, toolsChangeCall{clientID, name, tools, toolNameMapping})
	})

	require.NoError(t, m.connectToMCPClient(context.Background(), config))

	require.Len(t, calls, 1)
	assert.Equal(t, config.ID, calls[0].clientID)
	assert.Contains(t, calls[0].tools, toolName)
}

// TestPerformCheck_PerCall_SuccessfulDiscovery_FiresToolsChangeCallback
// covers the periodic checker's own refresh (checkPerCall -> writeBackTools)
// — the path that, before this funnel existed, was the one place a client's
// tools could silently drift out of sync with the DB indefinitely, since
// nothing else revisits a per-call client after its first discovery.
func TestPerformCheck_PerCall_SuccessfulDiscovery_FiresToolsChangeCallback(t *testing.T) {
	ts, _ := buildAdminDiscoveryHTTPServer(t)

	cred := &fakeAdminCredStore{headers: http.Header{"Authorization": []string{"Bearer shared-token"}}}
	manager := &MCPManager{
		discoveryStore: NewDiscoveryStore(nil, nil),
		credStore:      cred,
		logger:         &MockLogger{},
		clientMap:      map[string]*schemas.MCPClientState{},
	}

	state, config := newSyncTestClientState("client-checker-percall", "checker-client", schemas.MCPAuthTypeHeaders)
	config.ConnectionType = schemas.MCPConnectionTypeHTTP
	config.ConnectionString = schemas.NewSecretVar(ts.URL)
	manager.clientMap[config.ID] = state

	var calls []toolsChangeCall
	manager.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		tools, toolNameMapping := discovery.Tools, discovery.ToolNameMapping
		calls = append(calls, toolsChangeCall{clientID, name, tools, toolNameMapping})
	})

	checker := NewClientConnectionChecker(manager, config.ID, time.Minute, false, &MockLogger{})
	checker.performCheck()

	require.Len(t, calls, 1)
	assert.Equal(t, config.ID, calls[0].clientID)
	assert.Contains(t, calls[0].tools, "checker-client-echo")
}

// TestAddClient_RestoreFromConfig_DoesNotFireToolsChangeCallback pins the
// deliberate exception: AddClient restoring an already-known tool set read
// back out of the config it was just given (the normal boot path for a
// per-call client whose tools are already in the DB) must NOT notify —
// nothing changed, so there is nothing new to persist. Only a genuine fresh
// discovery result should reach the callback.
func TestAddClient_RestoreFromConfig_DoesNotFireToolsChangeCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	fired := false
	m.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		fired = true
	})

	config := &schemas.MCPClientConfig{
		ID:                "restore-client",
		Name:              "restore_client",
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		ConnectionString:  schemas.NewSecretVar("https://example.invalid/mcp"),
		AuthType:          schemas.MCPAuthTypePerUserHeaders,
		PerUserHeaderKeys: []string{"X-User-Token"},
		DiscoveredTools:   map[string]schemas.ChatTool{"tool-a": {}},
	}

	require.NoError(t, m.AddClient(context.Background(), config))

	assert.False(t, fired, "restoring already-known tools from the config passed in must not re-trigger persistence")
}
