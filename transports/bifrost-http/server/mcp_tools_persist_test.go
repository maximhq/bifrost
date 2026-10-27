package server

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingToolsConfigStore captures every UpdateMCPClientDiscovery call for assertion. failErr is
// returned for the first failTimes calls (every call when failTimes is 0); hold, when set, blocks the
// first call until it is closed, after signalling started.
type recordingToolsConfigStore struct {
	configstore.ConfigStore
	mu        sync.Mutex
	calls     []toolsUpdateCall
	attempts  int
	failErr   error
	failTimes int
	hold      chan struct{}
	started   chan struct{}
}

type toolsUpdateCall struct {
	clientID     string
	tools        map[string]schemas.ChatTool
	instructions string
	prompts      []mcp.Prompt
}

func (s *recordingToolsConfigStore) UpdateMCPClientDiscovery(ctx context.Context, clientID string, discovery *schemas.MCPDiscovery) error {
	s.mu.Lock()
	s.attempts++
	first := s.attempts == 1
	s.mu.Unlock()
	if first && s.hold != nil {
		close(s.started)
		<-s.hold
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil && (s.failTimes == 0 || s.attempts <= s.failTimes) {
		return s.failErr
	}
	s.calls = append(s.calls, toolsUpdateCall{clientID: clientID, tools: discovery.Tools, instructions: discovery.Instructions, prompts: discovery.Prompts})
	return nil
}

func (s *recordingToolsConfigStore) discoveryCalls() []toolsUpdateCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *recordingToolsConfigStore) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

// fastDiscoveryRetries makes a failed discovery write retry within milliseconds for the test.
func fastDiscoveryRetries(t *testing.T) {
	t.Helper()
	prev := mcpDiscoveryRetryBackoff
	mcpDiscoveryRetryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { mcpDiscoveryRetryBackoff = prev })
}

// TestPersistMCPClientDiscovery_WritesThroughConfigStore pins the OSS side of the
// funnel: the tools-change callback registered on the MCP manager must
// persist the fresh result via ConfigStore.UpdateMCPClientDiscovery — the
// targeted column update, not a full-row overwrite.
func TestPersistMCPClientDiscovery_WritesThroughConfigStore(t *testing.T) {
	store := &recordingToolsConfigStore{}
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: store}}

	tools := map[string]schemas.ChatTool{"echo": {Type: "function"}}
	prompts := []mcp.Prompt{mcp.NewPrompt("client-review")}
	s.PersistMCPClientDiscovery(context.Background(), "client-1", &schemas.MCPDiscovery{Tools: tools, Instructions: "Use me carefully.", Prompts: prompts})

	require.Eventually(t, func() bool { return len(store.discoveryCalls()) == 1 }, 2*time.Second, time.Millisecond)
	calls := store.discoveryCalls()
	assert.Equal(t, "client-1", calls[0].clientID)
	assert.Equal(t, tools, calls[0].tools)
	// The instructions must ride the same write: a per-call client holds no connection to
	// re-read them from, so if they are dropped here they are lost across a restart.
	assert.Equal(t, "Use me carefully.", calls[0].instructions)
	assert.Equal(t, prompts, calls[0].prompts, "prompts ride the same write")
}

// TestPersistMCPClientDiscovery_NilConfigStore_NoOp confirms the nil-guard
// convention this package uses elsewhere (Config or ConfigStore not yet
// wired) — must not panic.
func TestPersistMCPClientDiscovery_NilConfigStore_NoOp(t *testing.T) {
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: nil}}
	assert.NotPanics(t, func() {
		s.PersistMCPClientDiscovery(context.Background(), "client-1", &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}})
	})
}

// TestPersistMCPClientDiscovery_RetriesUntilWritten: the manager records a discovery's hash before it
// is saved, so an identical later discovery never asks again. A failed write is retried until it lands.
func TestPersistMCPClientDiscovery_RetriesUntilWritten(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()
	fastDiscoveryRetries(t)

	store := &recordingToolsConfigStore{failErr: fmt.Errorf("db unavailable"), failTimes: 2}
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: store}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.PersistMCPClientDiscovery(ctx, "client-1", &schemas.MCPDiscovery{Instructions: "v1"})
	require.Eventually(t, func() bool { return len(store.discoveryCalls()) == 1 }, 2*time.Second, time.Millisecond, "the failed write was not retried")
	assert.Equal(t, 3, store.attemptCount())
	assert.Equal(t, "v1", store.discoveryCalls()[0].instructions)
}

// TestPersistMCPClientDiscovery_NewestDiscoveryWins: changes that arrive while a write is in flight
// are written after it, and only the newest, so an older discovery never lands last.
func TestPersistMCPClientDiscovery_NewestDiscoveryWins(t *testing.T) {
	store := &recordingToolsConfigStore{hold: make(chan struct{}), started: make(chan struct{})}
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: store}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.PersistMCPClientDiscovery(ctx, "client-1", &schemas.MCPDiscovery{Instructions: "v1"})
	<-store.started
	var wg sync.WaitGroup
	for _, version := range []string{"v2", "v3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.PersistMCPClientDiscovery(ctx, "client-1", &schemas.MCPDiscovery{Instructions: version})
		}()
		time.Sleep(20 * time.Millisecond) // v3 is queued after v2
	}
	close(store.hold)
	wg.Wait()

	require.Eventually(t, func() bool {
		calls := store.discoveryCalls()
		return len(calls) > 0 && calls[len(calls)-1].instructions == "v3"
	}, 2*time.Second, time.Millisecond, "the newest discovery did not land last: %+v", store.discoveryCalls())
	time.Sleep(50 * time.Millisecond)
	calls := store.discoveryCalls()
	assert.Equal(t, "v3", calls[len(calls)-1].instructions, "an older discovery landed after the newest: %+v", calls)
	assert.Equal(t, []string{"v1", "v3"}, []string{calls[0].instructions, calls[len(calls)-1].instructions})
	assert.Len(t, calls, 2, "v2 was superseded before it was written")
}

// TestPersistMCPClientDiscovery_DeletedClientIsNotRetried: a client removed before its discovery was
// saved has no row to write, so the write is dropped instead of retried.
func TestPersistMCPClientDiscovery_DeletedClientIsNotRetried(t *testing.T) {
	prevLogger := logger
	logger = noopTestLogger{}
	defer func() { logger = prevLogger }()
	fastDiscoveryRetries(t)

	store := &recordingToolsConfigStore{failErr: configstore.ErrNotFound}
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: store}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.PersistMCPClientDiscovery(ctx, "client-1", &schemas.MCPDiscovery{})
	require.Eventually(t, func() bool { return store.attemptCount() == 1 }, 2*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, store.attemptCount())
}

// TestSyncMCPServersAfterToolsChange_NilHandler_NoOp confirms the nil-guard
// convention this package uses elsewhere — MCPServerHandler not yet wired
// (e.g. in a partially-constructed test server) must not panic. The
// underlying SyncMCPServer call itself is exercised the same way the
// pre-existing SetClientDiscovery wrapper's identical call already is: only via
// full integration, since MCPServerHandler has no fake-able seam from this
// package.
func TestSyncMCPServersAfterToolsChange_NilHandler_NoOp(t *testing.T) {
	s := &BifrostHTTPServer{}
	assert.NotPanics(t, func() {
		s.SyncMCPServersAfterToolsChange(context.Background(), "client-1")
	})
}
