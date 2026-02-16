// Package tracing provides distributed tracing infrastructure for Bifrost
package tracing

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// DeferredSpanInfo stores information about a deferred span for streaming requests
type DeferredSpanInfo struct {
	SpanID         string
	StartTime      time.Time
	Tracer         schemas.Tracer // Reference to tracer for completing the span
	RequestID      string         // Request ID for accumulator lookup
	FirstChunkTime time.Time      // Timestamp of first chunk (for TTFT calculation)
	ChunkCount     int            // Count of received streaming chunks (for AttrTotalChunks)
	mu             sync.Mutex     // Mutex for thread-safe chunk accumulation
}

// TraceStore manages traces with thread-safe access and object pooling
type TraceStore struct {
	traces        sync.Map  // map[traceID]*schemas.Trace - thread-safe concurrent access
	deferredSpans sync.Map  // map[traceID]*DeferredSpanInfo - deferred spans for streaming requests
	tracePool     sync.Pool // Reuse Trace objects to reduce allocations
	spanPool      sync.Pool // Reuse Span objects to reduce allocations
	logger        schemas.Logger

	ttl           time.Duration
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
	cleanupWg     sync.WaitGroup
	stopOnce      sync.Once // Ensures Stop() cleanup runs only once
}

// NewTraceStore creates a new TraceStore with the given TTL for cleanup
func NewTraceStore(ttl time.Duration, logger schemas.Logger) *TraceStore {
	store := &TraceStore{
		ttl:    ttl,
		logger: logger,
		tracePool: sync.Pool{
			New: func() any {
				return &schemas.Trace{
					Spans:      make([]*schemas.Span, 0, 16), // Pre-allocate capacity
					Attributes: make(map[string]any),
				}
			},
		},
		spanPool: sync.Pool{
			New: func() any {
				return &schemas.Span{
					Attributes: make(map[string]any),
					Events:     make([]schemas.SpanEvent, 0, 4), // Pre-allocate capacity
				}
			},
		},
		stopCleanup: make(chan struct{}),
	}

	// Start background cleanup goroutine
	store.startCleanup()

	return store
}

// CreateTrace creates a new trace and stores it, returns trace ID only.
// The inheritedTraceID parameter is the trace ID from an incoming W3C traceparent header.
// If provided, this trace will use that ID to continue the distributed trace.
// If empty, a new trace ID will be generated.
// Note: The parent span ID (for linking to upstream spans) is handled separately
// via context in StartSpan, not stored on the trace itself.
func (s *TraceStore) CreateTrace(inheritedTraceID string) string {
	trace := s.tracePool.Get().(*schemas.Trace)
	// Reset and initialize the trace
	if inheritedTraceID != "" {
		trace.TraceID = inheritedTraceID
	} else {
		trace.TraceID = generateTraceID()
	}
	// Note: trace.ParentID is intentionally not set here.
	// Parent-child relationships are between spans, not traces.
	// The root span's ParentID is set in StartSpan from context.
	trace.ParentID = ""
	trace.StartTime = time.Now()
	trace.EndTime = time.Time{}
	trace.RootSpan = nil

	// Reset slices but keep capacity
	if trace.Spans != nil {
		trace.Spans = trace.Spans[:0]
	} else {
		trace.Spans = make([]*schemas.Span, 0, 16)
	}

	// Reset attributes
	if trace.Attributes == nil {
		trace.Attributes = make(map[string]any)
	} else {
		clear(trace.Attributes)
	}

	s.traces.Store(trace.TraceID, trace)
	return trace.TraceID
}

// GetTrace retrieves a trace by ID
func (s *TraceStore) GetTrace(traceID string) *schemas.Trace {
	if val, ok := s.traces.Load(traceID); ok {
		return val.(*schemas.Trace)
	}
	return nil
}

// CompleteTrace marks the trace as complete, removes it from store, and returns it for flushing
func (s *TraceStore) CompleteTrace(traceID string) *schemas.Trace {
	// Clear any deferred span for this trace
	s.deferredSpans.Delete(traceID)

	if val, ok := s.traces.LoadAndDelete(traceID); ok {
		trace := val.(*schemas.Trace)
		trace.EndTime = time.Now()
		return trace
	}
	return nil
}

// StoreDeferredSpan stores a span ID for later completion (used for streaming requests)
func (s *TraceStore) StoreDeferredSpan(traceID, spanID string) {
	s.deferredSpans.Store(traceID, &DeferredSpanInfo{
		SpanID:    spanID,
		StartTime: time.Now(),
	})
}

// GetDeferredSpan retrieves the deferred span info for a trace ID
func (s *TraceStore) GetDeferredSpan(traceID string) *DeferredSpanInfo {
	if val, ok := s.deferredSpans.Load(traceID); ok {
		return val.(*DeferredSpanInfo)
	}
	return nil
}

// ClearDeferredSpan removes the deferred span info for a trace ID
func (s *TraceStore) ClearDeferredSpan(traceID string) {
	s.deferredSpans.Delete(traceID)
}

// AppendStreamingChunk tracks TTFT and chunk count for the deferred span.
// Chunks are no longer stored — the new streaming.Accumulator handles full content
// accumulation for plugins (logging, maxim). This eliminates storing 1M+ BifrostResponse
// objects in the old accumulator at high concurrency.
func (s *TraceStore) AppendStreamingChunk(traceID string, chunk *schemas.BifrostResponse) {
	if chunk == nil {
		return
	}
	info := s.GetDeferredSpan(traceID)
	if info == nil {
		return
	}
	info.mu.Lock()
	defer info.mu.Unlock()

	// Track first chunk time for TTFT calculation
	if info.FirstChunkTime.IsZero() {
		info.FirstChunkTime = time.Now()
	}

	info.ChunkCount++
}

// GetAccumulatedData returns TTFT and chunk count for a deferred span.
// Chunks are no longer stored; full content is available via the streaming.Accumulator.
func (s *TraceStore) GetAccumulatedData(traceID string) (ttftMs int64, chunkCount int) {
	info := s.GetDeferredSpan(traceID)
	if info == nil {
		return 0, 0
	}
	info.mu.Lock()
	defer info.mu.Unlock()

	// Calculate TTFT in milliseconds
	if !info.StartTime.IsZero() && !info.FirstChunkTime.IsZero() {
		ttftMs = info.FirstChunkTime.Sub(info.StartTime).Milliseconds()
	}

	return ttftMs, info.ChunkCount
}

// ReleaseTrace returns the trace and its spans to the pools for reuse
func (s *TraceStore) ReleaseTrace(trace *schemas.Trace) {
	if trace == nil {
		return
	}

	// Return all spans to the pool
	for _, span := range trace.Spans {
		s.releaseSpan(span)
	}

	// Reset the trace
	trace.Reset()

	// Return trace to pool
	s.tracePool.Put(trace)
}

// StartSpan creates a new span and adds it to the trace
func (s *TraceStore) StartSpan(traceID, name string, kind schemas.SpanKind) *schemas.Span {
	trace := s.GetTrace(traceID)
	if trace == nil {
		return nil
	}

	span := s.spanPool.Get().(*schemas.Span)

	// Reset and initialize the span
	span.SpanID = generateSpanID()
	span.TraceID = traceID
	span.Name = name
	span.Kind = kind
	span.StartTime = time.Now()
	span.EndTime = time.Time{}
	span.Status = schemas.SpanStatusUnset
	span.StatusMsg = ""

	// Reset slices but keep capacity
	if span.Events != nil {
		span.Events = span.Events[:0]
	} else {
		span.Events = make([]schemas.SpanEvent, 0, 4)
	}

	// Reset attributes
	if span.Attributes == nil {
		span.Attributes = make(map[string]any)
	} else {
		clear(span.Attributes)
	}

	// Set parent ID to root span if it exists, otherwise this is root
	if trace.RootSpan != nil {
		span.ParentID = trace.RootSpan.SpanID
	} else {
		span.ParentID = ""
		trace.RootSpan = span
	}

	// Add span to trace
	trace.AddSpan(span)

	return span
}

// StartChildSpan creates a new span as a child of the specified parent span
func (s *TraceStore) StartChildSpan(traceID, parentSpanID, name string, kind schemas.SpanKind) *schemas.Span {
	trace := s.GetTrace(traceID)
	if trace == nil {
		return nil
	}

	span := s.spanPool.Get().(*schemas.Span)

	// Reset and initialize the span
	span.SpanID = generateSpanID()
	span.ParentID = parentSpanID
	span.TraceID = traceID
	span.Name = name
	span.Kind = kind
	span.StartTime = time.Now()
	span.EndTime = time.Time{}
	span.Status = schemas.SpanStatusUnset
	span.StatusMsg = ""

	// Reset slices but keep capacity
	if span.Events != nil {
		span.Events = span.Events[:0]
	} else {
		span.Events = make([]schemas.SpanEvent, 0, 4)
	}

	// Reset attributes
	if span.Attributes == nil {
		span.Attributes = make(map[string]any)
	} else {
		clear(span.Attributes)
	}

	// Set as root span if this is the first span in the trace.
	// This can happen when the span has an external parent (from W3C traceparent)
	// but is the first span within this service's trace.
	if trace.RootSpan == nil {
		trace.RootSpan = span
	}

	// Add span to trace
	trace.AddSpan(span)

	return span
}

// EndSpan marks a span as complete with the given status and attributes
func (s *TraceStore) EndSpan(traceID, spanID string, status schemas.SpanStatus, statusMsg string, attrs map[string]any) {
	trace := s.GetTrace(traceID)
	if trace == nil {
		return
	}

	span := trace.GetSpan(spanID)
	if span == nil {
		return
	}

	span.End(status, statusMsg)

	// Add any final attributes
	for k, v := range attrs {
		span.SetAttribute(k, v)
	}
}

// releaseSpan returns a span to the pool
func (s *TraceStore) releaseSpan(span *schemas.Span) {
	if span == nil {
		return
	}
	span.Reset()
	s.spanPool.Put(span)
}

// startCleanup starts the background cleanup goroutine
func (s *TraceStore) startCleanup() {
	if s.ttl <= 0 {
		return
	}

	// Cleanup interval is TTL / 2
	cleanupInterval := s.ttl / 2
	if cleanupInterval < time.Minute {
		cleanupInterval = time.Minute
	}

	s.cleanupTicker = time.NewTicker(cleanupInterval)
	s.cleanupWg.Add(1)

	go func() {
		defer s.cleanupWg.Done()
		for {
			select {
			case <-s.cleanupTicker.C:
				s.cleanupOldTraces()
			case <-s.stopCleanup:
				return
			}
		}
	}()
}

// cleanupOldTraces removes traces that have exceeded the TTL
func (s *TraceStore) cleanupOldTraces() {
	cutoff := time.Now().Add(-s.ttl)
	count := 0

	s.traces.Range(func(key, value any) bool {
		trace := value.(*schemas.Trace)
		if trace.StartTime.Before(cutoff) {
			if deleted, ok := s.traces.LoadAndDelete(key); ok {
				s.ReleaseTrace(deleted.(*schemas.Trace))
				count++
			}
		}
		return true
	})

	if count > 0 && s.logger != nil {
		s.logger.Debug("tracing: cleaned up %d orphaned traces", count)
	}
}

// Stop stops the cleanup goroutine and releases resources
func (s *TraceStore) Stop() {
	s.stopOnce.Do(func() {
		if s.cleanupTicker != nil {
			s.cleanupTicker.Stop()
		}
		close(s.stopCleanup)
		s.cleanupWg.Wait()
	})
}

// generateTraceID generates a W3C-compliant trace ID.
// Returns 32 lowercase hex characters (128-bit UUID without hyphens).
func generateTraceID() string {
	u := uuid.New()
	return hex.EncodeToString(u[:])
}

// generateSpanID generates a W3C-compliant span ID.
// Returns 16 lowercase hex characters (first 64 bits of a UUID).
func generateSpanID() string {
	u := uuid.New()
	return hex.EncodeToString(u[:8])
}
