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

// toolsChangeCall records one invocation of a registered SetToolsChangeCallback.
type toolsChangeCall struct {
	clientID, name  string
	tools           map[string]schemas.ChatTool
	toolNameMapping map[string]string
}

// TestSetClientTools_FiresToolsChangeCallback pins the funnel's simplest
// entry point: SetClientTools (used by per-call clients' own AddClient/
// UpdateClientCredentials discovery, and directly by admin-verify HTTP
// handlers) must notify a registered callback with the fresh result so the
// transport layer can persist it — core/mcp has no DB access of its own.
func TestSetClientTools_FiresToolsChangeCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "client-one"}
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, ToolMap: map[string]schemas.ChatTool{}}
	m.mu.Unlock()

	var calls []toolsChangeCall
	m.SetToolsChangeCallback(func(clientID, name string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) {
		calls = append(calls, toolsChangeCall{clientID, name, tools, toolNameMapping})
	})

	tools := map[string]schemas.ChatTool{"echo": {Type: "function"}}
	mapping := map[string]string{"echo": "echo-server"}
	m.SetClientTools(config.ID, tools, mapping)

	require.Len(t, calls, 1)
	assert.Equal(t, config.ID, calls[0].clientID)
	assert.Equal(t, config.Name, calls[0].name)
	assert.Equal(t, tools, calls[0].tools)
	assert.Equal(t, mapping, calls[0].toolNameMapping)
}

// TestSetClientTools_UnknownClient_DoesNotFireCallback confirms the callback
// only fires for a real mutation — calling SetClientTools for a client that
// isn't (or is no longer) registered must not notify a stale/nonexistent
// entry.
func TestSetClientTools_UnknownClient_DoesNotFireCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)

	fired := false
	m.SetToolsChangeCallback(func(clientID, name string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) {
		fired = true
	})

	m.SetClientTools("does-not-exist", map[string]schemas.ChatTool{}, map[string]string{})

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
	config := newMakeBeforeBreakConfig("sticky-dial-client", ts.URL)
	toolName := config.Name + "-echo"

	var calls []toolsChangeCall
	m.SetToolsChangeCallback(func(clientID, name string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) {
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
		credStore: cred,
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}

	state, config := newSyncTestClientState("client-checker-percall", "checker-client", schemas.MCPAuthTypeHeaders, map[string]schemas.ChatTool{})
	config.ConnectionType = schemas.MCPConnectionTypeHTTP
	config.ConnectionString = schemas.NewSecretVar(ts.URL)
	manager.clientMap[config.ID] = state

	var calls []toolsChangeCall
	manager.SetToolsChangeCallback(func(clientID, name string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) {
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

	fired := false
	m.SetToolsChangeCallback(func(clientID, name string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) {
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
