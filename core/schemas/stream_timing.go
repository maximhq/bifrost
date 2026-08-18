package schemas

import (
	"sync"
	"time"
)

// StreamTiming tracks per-request raw upstream stream timing without retaining payload data.
type StreamTiming struct {
	mu               sync.Mutex
	startedAt        time.Time
	lastUpstreamByte time.Time
	firstByteLatency time.Duration
	maxUpstreamGap   time.Duration
	idleTimeout      time.Duration
	idleTimeoutFired bool
}

// StreamTimingSnapshot is the immutable metric view of StreamTiming.
type StreamTimingSnapshot struct {
	FirstByteLatency time.Duration
	MaxUpstreamGap   time.Duration
	IdleTimeout      time.Duration
	IdleTimeoutFired bool
}

// NewStreamTiming creates timing state for one upstream stream.
func NewStreamTiming(startedAt time.Time, idleTimeout time.Duration) *StreamTiming {
	return &StreamTiming{startedAt: startedAt, idleTimeout: idleTimeout}
}

// RecordUpstreamBytes records a successful raw upstream read.
func (s *StreamTiming) RecordUpstreamBytes(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastUpstreamByte.IsZero() {
		s.firstByteLatency = now.Sub(s.startedAt)
		s.lastUpstreamByte = now
		return
	}
	gap := now.Sub(s.lastUpstreamByte)
	if gap > s.maxUpstreamGap {
		s.maxUpstreamGap = gap
	}
	s.lastUpstreamByte = now
}

// MarkIdleTimeout records that the configured raw upstream idle timeout fired.
func (s *StreamTiming) MarkIdleTimeout() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.idleTimeoutFired = true
	s.mu.Unlock()
}

// Snapshot returns the current immutable timing values.
func (s *StreamTiming) Snapshot() StreamTimingSnapshot {
	if s == nil {
		return StreamTimingSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return StreamTimingSnapshot{
		FirstByteLatency: s.firstByteLatency,
		MaxUpstreamGap:   s.maxUpstreamGap,
		IdleTimeout:      s.idleTimeout,
		IdleTimeoutFired: s.idleTimeoutFired,
	}
}
