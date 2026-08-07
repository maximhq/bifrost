package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingToolsConfigStore captures every UpdateMCPClientTools call for
// assertion, and can be made to fail once to exercise the error-logging path.
type recordingToolsConfigStore struct {
	configstore.ConfigStore
	calls   []toolsUpdateCall
	failErr error
}

type toolsUpdateCall struct {
	clientID string
	tools    map[string]schemas.ChatTool
	mapping  map[string]string
}

func (s *recordingToolsConfigStore) UpdateMCPClientTools(ctx context.Context, clientID string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) error {
	if s.failErr != nil {
		return s.failErr
	}
	s.calls = append(s.calls, toolsUpdateCall{clientID: clientID, tools: tools, mapping: toolNameMapping})
	return nil
}

// TestPersistMCPClientTools_WritesThroughConfigStore pins the OSS side of the
// funnel: the tools-change callback registered on the MCP manager must
// persist the fresh result via ConfigStore.UpdateMCPClientTools — the
// targeted column update, not a full-row overwrite.
func TestPersistMCPClientTools_WritesThroughConfigStore(t *testing.T) {
	store := &recordingToolsConfigStore{}
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: store}}

	tools := map[string]schemas.ChatTool{"echo": {Type: "function"}}
	mapping := map[string]string{"echo": "echo-server"}
	s.PersistMCPClientTools(context.Background(), "client-1", tools, mapping)

	require.Len(t, store.calls, 1)
	assert.Equal(t, "client-1", store.calls[0].clientID)
	assert.Equal(t, tools, store.calls[0].tools)
	assert.Equal(t, mapping, store.calls[0].mapping)
}

// TestPersistMCPClientTools_NilConfigStore_NoOp confirms the nil-guard
// convention this package uses elsewhere (Config or ConfigStore not yet
// wired) — must not panic.
func TestPersistMCPClientTools_NilConfigStore_NoOp(t *testing.T) {
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: nil}}
	assert.NotPanics(t, func() {
		s.PersistMCPClientTools(context.Background(), "client-1", map[string]schemas.ChatTool{}, map[string]string{})
	})
}

// TestPersistMCPClientTools_StoreErrorLogsAndDoesNotPanic confirms a DB
// failure is logged (best-effort — the periodic checker's own retry, or the
// next discovery event, will try again) rather than propagated, since this
// runs from a callback with no caller to return an error to.
func TestPersistMCPClientTools_StoreErrorLogsAndDoesNotPanic(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()

	store := &recordingToolsConfigStore{failErr: fmt.Errorf("db unavailable")}
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: store}}

	assert.NotPanics(t, func() {
		s.PersistMCPClientTools(context.Background(), "client-1", map[string]schemas.ChatTool{}, map[string]string{})
	})
	assert.Empty(t, store.calls)
}

// TestSyncMCPServersAfterToolsChange_NilHandler_NoOp confirms the nil-guard
// convention this package uses elsewhere — MCPServerHandler not yet wired
// (e.g. in a partially-constructed test server) must not panic. The
// underlying SyncAllMCPServers call itself is exercised the same way the
// pre-existing SetClientTools wrapper's identical call already is: only via
// full integration, since MCPServerHandler has no fake-able seam from this
// package.
func TestSyncMCPServersAfterToolsChange_NilHandler_NoOp(t *testing.T) {
	s := &BifrostHTTPServer{}
	assert.NotPanics(t, func() {
		s.SyncMCPServersAfterToolsChange(context.Background(), "client-1")
	})
}
