package schemas

import (
	"testing"
	"time"
)

func TestStreamTimingSnapshot(t *testing.T) {
	start := time.Unix(100, 0)
	timing := NewStreamTiming(start, 300*time.Second)
	timing.RecordUpstreamBytes(start.Add(2 * time.Second))
	timing.RecordUpstreamBytes(start.Add(7 * time.Second))
	timing.RecordUpstreamBytes(start.Add(10 * time.Second))
	timing.MarkIdleTimeout()

	snapshot := timing.Snapshot()
	if snapshot.FirstByteLatency != 2*time.Second {
		t.Fatalf("FirstByteLatency = %v, want 2s", snapshot.FirstByteLatency)
	}
	if snapshot.MaxUpstreamGap != 5*time.Second {
		t.Fatalf("MaxUpstreamGap = %v, want 5s", snapshot.MaxUpstreamGap)
	}
	if snapshot.IdleTimeout != 300*time.Second {
		t.Fatalf("IdleTimeout = %v, want 300s", snapshot.IdleTimeout)
	}
	if !snapshot.IdleTimeoutFired {
		t.Fatal("IdleTimeoutFired = false, want true")
	}
}
