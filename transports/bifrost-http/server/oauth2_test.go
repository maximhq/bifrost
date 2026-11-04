package server

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sweepRecordingStore counts sweep invocations. It embeds the ConfigStore
// interface so only the three methods sweep() touches need real bodies; any
// other call would panic, proving the gate short-circuits before store access.
type sweepRecordingStore struct {
	configstore.ConfigStore
	calls int
}

func (s *sweepRecordingStore) SweepExpiredOAuth2AuthorizeRequests(ctx context.Context) error {
	s.calls++
	return nil
}

func (s *sweepRecordingStore) SweepOAuth2RefreshTokens(ctx context.Context, revokedOlderThan time.Duration) (int64, error) {
	s.calls++
	return 0, nil
}

func (s *sweepRecordingStore) SweepOrphanedOAuth2Clients(ctx context.Context, registeredOlderThan time.Duration) (int64, error) {
	s.calls++
	return 0, nil
}

func TestOAuth2SweepWorkerShouldSweepGate(t *testing.T) {
	tests := []struct {
		name        string
		shouldSweep func() bool
		wantCalls   int
	}{
		{"nil gate always sweeps", nil, 3},
		{"true gate sweeps", func() bool { return true }, 3},
		{"false gate skips the pass entirely", func() bool { return false }, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &sweepRecordingStore{}
			w := newOAuth2SweepWorker(store, tt.shouldSweep)
			w.sweep(context.Background())
			if store.calls != tt.wantCalls {
				t.Errorf("sweep() made %d store calls, want %d", store.calls, tt.wantCalls)
			}
		})
	}
}

func TestOAuth2SweepWorkerGateReevaluatedPerPass(t *testing.T) {
	store := &sweepRecordingStore{}
	allowed := false
	w := newOAuth2SweepWorker(store, func() bool { return allowed })

	w.sweep(context.Background())
	if store.calls != 0 {
		t.Fatalf("gated-off pass made %d store calls, want 0", store.calls)
	}

	// Flipping the gate must take effect on the next pass — the decision is
	// per-pass, not latched at construction.
	allowed = true
	w.sweep(context.Background())
	if store.calls != 3 {
		t.Fatalf("gated-on pass made %d store calls, want 3", store.calls)
	}

	allowed = false
	w.sweep(context.Background())
	if store.calls != 3 {
		t.Fatalf("re-gated-off pass made %d store calls, want 3 total", store.calls)
	}
}

// staleOAuthRecorder records what dropStaleMCPOAuthState asks the server to drop; release, when set,
// holds the token-cache drop until it is closed.
type staleOAuthRecorder struct {
	evicted, closed, discoveriesEvicted []string
	closeErr                            error
	release                             chan struct{}
}

func (r *staleOAuthRecorder) EvictOauthTokenCacheByMCPClient(_ context.Context, mcpClientID string) {
	if r.release != nil {
		<-r.release
	}
	r.evicted = append(r.evicted, mcpClientID)
}

func (r *staleOAuthRecorder) EvictMCPPerUserDiscoveryCacheByMCPClient(_ context.Context, mcpClientID string) {
	r.discoveriesEvicted = append(r.discoveriesEvicted, mcpClientID)
}

func (r *staleOAuthRecorder) CloseAndMarkNeedsReauth(_ context.Context, id string) error {
	r.closed = append(r.closed, id)
	return r.closeErr
}

// A rotation (a metadata-document move, or a sign-in moving to a new authorization server) drops the
// client's cached tokens and shared connection before the hook returns, so the sign-in it rotated for
// cannot reconnect first and have its new connection closed by a late drop.
func TestDropStaleMCPOAuthState(t *testing.T) {
	recorder := &staleOAuthRecorder{release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a caller that hung up must not cut the drop short
	done := make(chan struct{})
	go func() {
		dropStaleMCPOAuthState(recorder)(ctx, "linear")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the hook returned before the stale tokens were dropped")
	case <-time.After(50 * time.Millisecond):
	}
	close(recorder.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the hook did not return once the drop finished")
	}
	assert.Equal(t, []string{"linear"}, recorder.evicted)
	assert.Equal(t, []string{"linear"}, recorder.closed)
	// The rotation deleted every caller's discovery of the client with their tokens; the cached
	// copies go too, so no caller is served the deleted tools until the store next reloads.
	assert.Equal(t, []string{"linear"}, recorder.discoveriesEvicted)

	// A per-user client has no shared connection to close.
	perUser := &staleOAuthRecorder{closeErr: schemas.ErrMCPReconnectNotApplicable}
	dropStaleMCPOAuthState(perUser)(context.Background(), "linear")
	require.Equal(t, []string{"linear"}, perUser.evicted)
	assert.Equal(t, []string{"linear"}, perUser.closed)
}
