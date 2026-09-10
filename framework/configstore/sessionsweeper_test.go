package configstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingSessionExpiryStore struct {
	mu    sync.Mutex
	calls int
	seen  chan time.Duration
}

func (s *recordingSessionExpiryStore) DeleteOrphanedSessions(_ context.Context, retention time.Duration) (int64, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	select {
	case s.seen <- retention:
	default:
	}
	return 0, nil
}

func (s *recordingSessionExpiryStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestSessionSweepWorkerRunsImmediatelyAndStops(t *testing.T) {
	store := &recordingSessionExpiryStore{seen: make(chan time.Duration, 2)}
	worker := NewSessionSweepWorker(store, time.Hour, 30*24*time.Hour, nil)
	require.NotNil(t, worker)
	worker.Start(context.Background())

	select {
	case retention := <-store.seen:
		require.Equal(t, 30*24*time.Hour, retention)
	case <-time.After(time.Second):
		t.Fatal("expected an immediate session expiry sweep")
	}

	worker.Stop()
	worker.Stop()
	calls := store.callCount()
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, calls, store.callCount(), "worker must stop after Stop")
}

func TestSessionSweepWorkerRejectsNonPositiveInterval(t *testing.T) {
	worker := NewSessionSweepWorker(&recordingSessionExpiryStore{seen: make(chan time.Duration, 1)}, 0, 30*24*time.Hour, nil)
	require.Nil(t, worker)
}
