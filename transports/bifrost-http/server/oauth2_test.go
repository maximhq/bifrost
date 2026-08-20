package server

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
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
