package requestguard

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCoalescerUnaryCancellationIsNotATimeoutBypass(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{Enabled: true, MaxInFlight: 4, WaitTimeout: time.Second})
	defer c.Close()
	leader, isLeader := c.Acquire(context.Background(), "same-request", false)
	if !isLeader || leader == nil {
		t.Fatal("first Acquire() did not create a leader")
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		handle *FlightHandle
		leader bool
	}, 1)
	go func() {
		handle, isLeader := c.Acquire(ctx, "same-request", false)
		result <- struct {
			handle *FlightHandle
			leader bool
		}{handle, isLeader}
	}()
	waitForRequestGuard(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return leader.flight.waiters == 1
	})
	cancel()

	select {
	case got := <-result:
		if got.leader || got.handle == nil || got.handle.ContextErr != context.Canceled {
			t.Fatalf("canceled Acquire() = (%+v, %v), want follower cancellation", got.handle, got.leader)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled unary follower remained blocked")
	}
}

func TestCoalescerTimeoutStillFailsOpen(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{Enabled: true, MaxInFlight: 4, WaitTimeout: 5 * time.Millisecond})
	defer c.Close()
	if _, leader := c.Acquire(context.Background(), "same-request", false); !leader {
		t.Fatal("first Acquire() did not create a leader")
	}
	handle, leader := c.Acquire(context.Background(), "same-request", false)
	if handle != nil || !leader {
		t.Fatalf("timed-out Acquire() = (%+v, %v), want fail-open leader", handle, leader)
	}
}

func TestCoalescerStreamFollowerCancellationAfterFinishDrains(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{Enabled: true, EnableStreaming: true, MaxInFlight: 4, MaxBufferedChunks: 4, MaxBufferedBytes: 1 << 16})
	defer c.Close()
	leader, _ := c.Acquire(context.Background(), "same-request", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	follower, isLeader := c.Acquire(ctx, "same-request", true)
	if isLeader || follower == nil || follower.Chunks == nil {
		t.Fatalf("stream follower Acquire() = (%+v, %v)", follower, isLeader)
	}
	chunk := &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{ID: "queued"}}
	if !c.PublishChunk(leader, chunk) {
		t.Fatal("PublishChunk() = false, want published")
	}
	c.FinishStream(leader)
	cancel()

	select {
	case _, open := <-follower.Chunks:
		if open {
			for range follower.Chunks {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("stream follower channel stayed open after cancellation")
	}
	waitForRequestGuard(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.streamSubscribers) == 0
	})
}

func TestCoalescerStoresTerminalErrorForStreamFollower(t *testing.T) {
	c := NewCoalescer(CoalescerConfig{Enabled: true, EnableStreaming: true, MaxInFlight: 4, MaxBufferedChunks: 4, MaxBufferedBytes: 1 << 16})
	defer c.Close()
	leader, _ := c.Acquire(context.Background(), "same-request", true)
	follower, isLeader := c.Acquire(context.Background(), "same-request", true)
	if isLeader || follower == nil {
		t.Fatalf("stream follower Acquire() = (%+v, %v)", follower, isLeader)
	}
	want := coalescerError("leader stream failed")
	c.FinishStream(leader, want)

	for range follower.Chunks {
	}
	if follower.Err == nil || follower.Err.Error.Message != want.Error.Message {
		t.Fatalf("stream follower error = %+v, want leader terminal error", follower.Err)
	}
}
