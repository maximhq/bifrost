package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputeToolsHash_StableAcrossMapIterationOrder pins the core property
// the gating logic depends on: hashing the same content twice (built via
// different map literals, so iteration order differs) must produce the same
// hash — Go's json.Marshal already sorts map string keys, but this asserts
// that invariant directly rather than trusting it implicitly.
func TestComputeToolsHash_StableAcrossMapIterationOrder(t *testing.T) {
	toolsA := map[string]schemas.ChatTool{"b": {Type: "function"}, "a": {Type: "function"}}
	toolsB := map[string]schemas.ChatTool{"a": {Type: "function"}, "b": {Type: "function"}}

	assert.Equal(t, computeDiscoveryHash(&schemas.MCPDiscovery{Tools: toolsA}), computeDiscoveryHash(&schemas.MCPDiscovery{Tools: toolsB}))
}

// TestComputeToolsHash_DiffersOnContentChange confirms the hash actually
// reflects content, not just key sets — a change to a tool's own fields
// (not just which names are present) must produce a different hash.
func TestComputeToolsHash_DiffersOnContentChange(t *testing.T) {
	base := computeDiscoveryHash(&schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"echo": {Type: "function"}}})
	changedType := computeDiscoveryHash(&schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"echo": {Type: "custom"}}})

	assert.NotEqual(t, base, changedType)
}

// A tool's annotations and metadata are left out of ChatTool's JSON, so the hash covers them on its own:
// an upstream that only retitles a tool still counts as a change.
func TestComputeDiscoveryHash_CoversToolMetadata(t *testing.T) {
	hash := func(tool schemas.ChatTool) string {
		return computeDiscoveryHash(&schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"echo": tool}})
	}
	base := hash(schemas.ChatTool{Type: "function"})
	assert.NotEqual(t, base, hash(schemas.ChatTool{Type: "function", MCP: &schemas.MCPToolMetadata{Title: "Echo"}}))
	assert.NotEqual(t, base, hash(schemas.ChatTool{Type: "function", Annotations: &schemas.MCPToolAnnotations{ReadOnlyHint: schemas.Ptr(true)}}))
}

// TestSetClientDiscovery_UnchangedTools_DoesNotFireCallback pins the funnel's
// gating: rediscovering byte-identical tools must not re-notify — the
// registered callback's whole purpose (persist to DB, resync the local MCP
// server) is wasted work when nothing actually changed.
func TestSetClientDiscovery_UnchangedTools_DoesNotFireCallback(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "client-one"}
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, ToolMap: map[string]schemas.ChatTool{}}
	m.mu.Unlock()

	tools := map[string]schemas.ChatTool{"echo": {Type: "function"}}

	callCount := 0
	m.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		callCount++
	})

	m.SetClientDiscovery(config.ID, &schemas.MCPDiscovery{Tools: tools})
	require.Equal(t, 1, callCount, "the first discovery must always fire")

	// Re-set with the exact same content (a fresh map literal, so this isn't
	// just testing pointer equality).
	m.SetClientDiscovery(config.ID, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"echo": {Type: "function"}}})
	assert.Equal(t, 1, callCount, "an unchanged rediscovery must not re-fire the callback")

	// A genuine change must still fire.
	m.SetClientDiscovery(config.ID, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"echo": {Type: "function"}, "new-tool": {Type: "function"}}})
	assert.Equal(t, 2, callCount, "a genuine content change must fire")
}

// TestWriteBackTools_UnchangedTools_DoesNotFireCallback covers the periodic
// checker's own refresh path — the highest-frequency firing point, and the
// one most likely to rediscover identical tools tick after tick.
func TestWriteBackTools_UnchangedTools_DoesNotFireCallback(t *testing.T) {
	manager := &MCPManager{
		logger:    &MockLogger{},
		clientMap: map[string]*schemas.MCPClientState{},
	}
	state, config := newSyncTestClientState("client-checker", "checker-client", schemas.MCPAuthTypeHeaders, map[string]schemas.ChatTool{})
	manager.clientMap[config.ID] = state

	callCount := 0
	manager.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		callCount++
	})

	tools := map[string]schemas.ChatTool{"echo": {Type: "function"}}

	manager.writeBackDiscovery(config.ID, 0, &schemas.MCPDiscovery{Tools: tools}, false)
	require.Equal(t, 1, callCount)

	manager.writeBackDiscovery(config.ID, 0, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"echo": {Type: "function"}}}, false)
	assert.Equal(t, 1, callCount, "an unchanged tick must not re-fire")

	manager.writeBackDiscovery(config.ID, 0, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}}, false)
	assert.Equal(t, 2, callCount, "the server legitimately losing all its tools is still a genuine change")
}

// TestAddClient_RestoreFromConfig_SeedsHashSoSubsequentIdenticalDiscoveryNoOps
// pins that restoring already-known tools from DB (which deliberately
// doesn't fire the callback, per TestAddClient_RestoreFromConfig_
// DoesNotFireToolsChangeCallback in toolschangecallback_test.go) still seeds
// LastDiscoveryHash — otherwise the first real discovery after a restart would
// look like a "change" even when it rediscovers the exact same tools,
// causing a needless persist/resync immediately after every boot.
func TestAddClient_RestoreFromConfig_SeedsHashSoSubsequentIdenticalDiscoveryNoOps(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	callCount := 0
	m.SetDiscoveryChangeCallback(func(clientID, name string, discovery *schemas.MCPDiscovery) {
		callCount++
	})

	restoredTools := map[string]schemas.ChatTool{"tool-a": {Type: "function"}}
	config := &schemas.MCPClientConfig{
		ID:                "restore-seed-client",
		Name:              "restore_seed_client",
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		ConnectionString:  schemas.NewSecretVar("https://example.invalid/mcp"),
		AuthType:          schemas.MCPAuthTypePerUserHeaders,
		PerUserHeaderKeys: []string{"X-User-Token"},
		DiscoveredTools:   restoredTools,
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	require.Equal(t, 0, callCount, "restoring from config must not itself fire")

	// Simulate the periodic checker rediscovering the exact same tools right
	// after boot — must not fire, since nothing actually changed from what
	// was restored.
	m.SetClientDiscovery(config.ID, &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{"tool-a": {Type: "function"}}})
	assert.Equal(t, 0, callCount, "rediscovering the exact tools that were just restored must not fire")
}
