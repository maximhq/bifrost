package streaming

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// gateReplayBufMaxBytes caps the per-stream paused replay buffer at 100 MB.
// On overflow, the gate is force-ended with a synthetic error chunk so the
// consumer is notified and memory is released.
const gateReplayBufMaxBytes int64 = 100 * 1024 * 1024

// chunkBytes returns the approximate wire size of a BifrostStreamChunk via
// MarshalJSON. Used only on the paused replay path to bound buffer growth.
func chunkBytes(chunk *schemas.BifrostStreamChunk) int64 {
	if chunk == nil {
		return 0
	}
	b, err := chunk.MarshalJSON()
	if err != nil {
		return 0
	}
	return int64(len(b))
}

// PauseStream is the Tracer-level entry point for pausing a stream.
// Forwards to the per-stream accumulator entry keyed by traceID.
func (a *Accumulator) PauseStream(traceID string) {
	if traceID == "" {
		return
	}
	sa := a.getOrCreateStreamAccumulator(traceID)
	sa.Pause()
}

// ResumeStream is the Tracer-level entry point for resuming a paused stream.
func (a *Accumulator) ResumeStream(traceID string) {
	if traceID == "" {
		return
	}
	sa := a.getOrCreateStreamAccumulator(traceID)
	sa.Resume()
}

// EndStream is the Tracer-level entry point for terminating a stream.
// Any buffered chunks are flushed first; then if err is non-nil it is delivered
// as a terminal error chunk. After EndStream, further provider chunks are dropped.
func (a *Accumulator) EndStream(traceID string, err *schemas.BifrostError) {
	if traceID == "" {
		return
	}
	sa := a.getOrCreateStreamAccumulator(traceID)
	sa.End(err)
}

// WaitForFlusher is the Tracer-level entry point for blocking until the gate
// flusher for traceID has fully drained and exited. Read-only: does NOT
// create an accumulator if one doesn't exist (no flusher = nothing to wait for).
func (a *Accumulator) WaitForFlusher(traceID string) {
	if traceID == "" {
		return
	}
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		return
	}
	v.(*StreamAccumulator).WaitForFlusher()
}

// IsStreamEnded reports whether the gate for traceID is in the Ended state.
// Read-only: does NOT create an accumulator if one doesn't exist.
func (a *Accumulator) IsStreamEnded(traceID string) bool {
	if traceID == "" {
		return false
	}
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		return false
	}
	sa := v.(*StreamAccumulator)
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.gateState == StreamStateEnded
}

// IsStreamPaused reports whether the gate for traceID is currently Paused.
// Read-only: does NOT create an accumulator if one doesn't exist.
func (a *Accumulator) IsStreamPaused(traceID string) bool {
	if traceID == "" {
		return false
	}
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		return false
	}
	sa := v.(*StreamAccumulator)
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.gateState == StreamStatePaused
}

// GetAccumulatedResponse returns a snapshot *schemas.BifrostResponse built
// from chunks accumulated so far for traceID. Built on demand each call; no
// caching. Detects stream type by which per-type chunk slice is populated.
// Returns nil if:
//   - traceID is empty
//   - no accumulator exists for traceID (never started or already cleaned up)
//   - no chunks have been accumulated yet
//   - the populated slices are ambiguous (more than one type — should not
//     happen in normal flow, but defensive)
//   - the per-type build returns no data
//
// Note: ExtraFields.Provider / OriginalModelRequested / ResolvedModelUsed are
// not preserved on the StreamAccumulator (they're per-chunk, not per-stream),
// so the returned response will have empty values for those fields. The body
// of the response (Choices/Message/etc.) is fully populated.
func (a *Accumulator) GetAccumulatedResponse(traceID string) *schemas.BifrostResponse {
	if traceID == "" {
		return nil
	}
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		return nil
	}
	sa := v.(*StreamAccumulator)

	// Detect stream type by which slice has data. Brief lock just to read
	// slice lengths; the per-type build below re-locks internally.
	sa.mu.Lock()
	var streamType StreamType
	populated := 0
	if len(sa.ChatStreamChunks) > 0 {
		streamType = StreamTypeChat
		populated++
	}
	if len(sa.ResponsesStreamChunks) > 0 {
		streamType = StreamTypeResponses
		populated++
	}
	if len(sa.AudioStreamChunks) > 0 {
		streamType = StreamTypeAudio
		populated++
	}
	if len(sa.TranscriptionStreamChunks) > 0 {
		streamType = StreamTypeTranscription
		populated++
	}
	if len(sa.ImageStreamChunks) > 0 {
		streamType = StreamTypeImage
		populated++
	}
	requestID := sa.RequestID
	sa.mu.Unlock()

	if populated != 1 {
		return nil // no data, or ambiguous (multiple types populated)
	}

	var data *AccumulatedData
	var err error
	switch streamType {
	case StreamTypeChat:
		data, err = a.processAccumulatedChatStreamingChunks(requestID, nil, false)
	case StreamTypeResponses:
		data, err = a.processAccumulatedResponsesStreamingChunks(requestID, nil, false)
	case StreamTypeAudio:
		data, err = a.processAccumulatedAudioStreamingChunks(requestID, nil, false)
	case StreamTypeTranscription:
		data, err = a.processAccumulatedTranscriptionStreamingChunks(requestID, nil, false)
	case StreamTypeImage:
		data, err = a.processAccumulatedImageStreamingChunks(requestID, nil, false)
	default:
		return nil
	}
	if err != nil || data == nil {
		return nil
	}

	psr := &ProcessedStreamResponse{
		RequestID:  requestID,
		StreamType: streamType,
		Data:       data,
	}
	return psr.ToBifrostResponse()
}

// GateSend is the Tracer-level entry point for delivering a stream chunk
// through the pause/resume/end gate. See Tracer.GateSend in core/schemas for
// behavior. Returns true if the chunk was handled (delivered or buffered),
// false if the caller should stop sending.
func (a *Accumulator) GateSend(traceID string, chunk *schemas.BifrostStreamChunk, isFinal, isHardErr bool, ch chan *schemas.BifrostStreamChunk, ctx *schemas.BifrostContext) bool {
	sa := a.getOrCreateStreamAccumulator(traceID)
	return sa.GateSend(chunk, isFinal, isHardErr, ch, ctx)
}

// Pause transitions the gate from Active to Paused. Idempotent.
func (sa *StreamAccumulator) Pause() {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState != StreamStateActive {
		return
	}
	sa.gateState = StreamStatePaused
	sa.gatePausedAt = sa.gateSeq
}

// Resume transitions the gate from Paused back to Active and wakes the flusher
// to drain buffered chunks. Idempotent.
func (sa *StreamAccumulator) Resume() {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState != StreamStatePaused {
		return
	}
	sa.gateState = StreamStateActive
	if sa.gateCond != nil {
		sa.gateCond.Broadcast()
	}
}

// End transitions the gate to Ended. Any buffered chunks are flushed by the
// flusher (if running) before exit; if err is non-nil it is delivered as a
// terminal error chunk after the flush. Idempotent.
//
// If no flusher is running but the gate has a cached delivery target (i.e.
// a prior GateSend captured ch+ctx) and there is work to do — buffered
// chunks or a synthetic terminal error from err — a flusher is started so
// the terminal chunk reaches the client. When ch+ctx were never cached
// (no chunks ever sent), the error is dropped: there is no consumer to
// deliver it to.
func (sa *StreamAccumulator) End(err *schemas.BifrostError) {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState == StreamStateEnded {
		return
	}
	sa.gateState = StreamStateEnded
	if err != nil {
		sa.gateEndError = err
	}
	if !sa.gateFlusherOn && sa.gateFlusherCh != nil &&
		(len(sa.gateReplayBuf) > 0 || sa.gateEndError != nil) {
		sa.gateFlusherOn = true
		sa.gateFlusherDone = make(chan struct{})
		go sa.gateFlusher()
	}
	if sa.gateCond != nil {
		sa.gateCond.Broadcast()
	}
}

// GateSend implements the per-chunk delivery gate.
//
//   - Active state: chunk is forwarded to ch (with ctx.Done() guard).
//   - Paused state: chunk is buffered for later replay; flusher started lazily.
//   - Ended state:  chunk is dropped.
//
// Final chunks (isFinal) and hard provider errors (isHardErr) bypass Active
// and force-flush + transition to Ended; if a flusher is running or chunks are
// buffered, the final chunk is appended to the buffer so the flusher delivers
// it in order.
func (sa *StreamAccumulator) GateSend(chunk *schemas.BifrostStreamChunk, isFinal, isHardErr bool, ch chan *schemas.BifrostStreamChunk, ctx *schemas.BifrostContext) bool {
	sa.mu.Lock()
	sa.gateSeq++
	// Cache (ch, ctx) for the flusher. They are stable for the life of the stream.
	if sa.gateFlusherCh == nil {
		sa.gateFlusherCh = ch
	}
	if sa.gateFlusherCtx == nil {
		sa.gateFlusherCtx = ctx
	}

	if sa.gateState == StreamStateEnded {
		sa.mu.Unlock()
		return false
	}

	// Paused: buffer every chunk regardless of isFinal/isHardErr. A terminal
	// chunk arriving while paused is held until Resume drains the buffer
	// (gateFlusher transitions to Ended afterward via gatePendingTerminal),
	// or until EndStream is called explicitly. Enforces a 100 MB cap to
	// prevent unbounded heap growth from a paused-and-forgotten stream.
	if sa.gateState == StreamStatePaused {
		size := chunkBytes(chunk)
		if sa.gateReplayBufBytes+size > gateReplayBufMaxBytes {
			// Overflow: force-end the gate with a synthetic error so the
			// consumer is notified and memory is released. Drops this chunk
			// and any further chunks for this stream.
			sa.gateState = StreamStateEnded
			sa.gateEndError = &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("paused_replay_buffer_overflow"),
					Message: fmt.Sprintf("paused stream replay buffer exceeded %d bytes — ending stream", gateReplayBufMaxBytes),
				},
			}
			if !sa.gateFlusherOn && sa.gateFlusherCh != nil {
				sa.gateFlusherOn = true
				sa.gateFlusherDone = make(chan struct{})
				go sa.gateFlusher()
			}
			if sa.gateCond != nil {
				sa.gateCond.Broadcast()
			}
			sa.mu.Unlock()
			return false
		}
		sa.gateReplayBuf = append(sa.gateReplayBuf, chunk)
		sa.gateReplayBufBytes += size
		if isFinal || isHardErr {
			sa.gatePendingTerminal = true
		}
		if !sa.gateFlusherOn {
			sa.gateFlusherOn = true
			sa.gateFlusherDone = make(chan struct{})
			go sa.gateFlusher()
		}
		sa.mu.Unlock()
		return true
	}

	// Active state from here on.
	if isFinal || isHardErr {
		// If there's already a flusher or a non-empty buffer, append the final
		// chunk so it's delivered after pending chunks. Otherwise fast-path send.
		if sa.gateFlusherOn || len(sa.gateReplayBuf) > 0 {
			sa.gateReplayBuf = append(sa.gateReplayBuf, chunk)
			sa.gateReplayBufBytes += chunkBytes(chunk)
			sa.gateState = StreamStateEnded
			if sa.gateCond != nil {
				sa.gateCond.Broadcast()
			}
			sa.mu.Unlock()
			return true
		}
		sa.gateState = StreamStateEnded
		sa.mu.Unlock()
		return sendOrCancel(ctx, ch, chunk)
	}

	// StreamStateActive: if a flusher is mid-drain (buffer non-empty), append
	// so order is preserved between buffered and live chunks. Otherwise pure
	// passthrough.
	if len(sa.gateReplayBuf) > 0 {
		sa.gateReplayBuf = append(sa.gateReplayBuf, chunk)
		sa.gateReplayBufBytes += chunkBytes(chunk)
		sa.gateCond.Broadcast()
		sa.mu.Unlock()
		return true
	}
	sa.mu.Unlock()
	return sendOrCancel(ctx, ch, chunk)
}

// drainBufferLocked drains gateReplayBuf to gateFlusherCh in order. MUST be
// called with sa.mu held. Releases sa.mu while sending; reacquires before
// returning. Stops draining if state transitions to Paused or ctx is done.
func (sa *StreamAccumulator) drainBufferLocked() {
	for sa.gateState != StreamStatePaused && len(sa.gateReplayBuf) > 0 {
		chunk := sa.gateReplayBuf[0]
		sa.gateReplayBuf = sa.gateReplayBuf[1:]
		// Decrement bytes per-chunk so the counter stays accurate even if
		// the loop exits mid-drain (Pause). Clamp at zero to absorb any
		// rounding drift between MarshalJSON sizes captured on append vs.
		// re-computed here.
		sa.gateReplayBufBytes -= chunkBytes(chunk)
		if sa.gateReplayBufBytes < 0 {
			sa.gateReplayBufBytes = 0
		}
		ch := sa.gateFlusherCh
		ctx := sa.gateFlusherCtx
		sa.mu.Unlock()
		ok := sendOrCancel(ctx, ch, chunk)
		sa.mu.Lock()
		if !ok {
			// ctx done; abandon remaining buffer and end the gate.
			sa.gateReplayBuf = nil
			sa.gateReplayBufBytes = 0
			sa.gateState = StreamStateEnded
			return
		}
	}
	if len(sa.gateReplayBuf) == 0 {
		sa.gateReplayBuf = nil // release backing array
		sa.gateReplayBufBytes = 0
	}
}

// gateFlusher is started lazily on first pause. It drains gateReplayBuf to the
// client channel whenever the gate is Active (or finalizing on Ended), and
// exits when state is Ended and the buffer is fully drained.
func (sa *StreamAccumulator) gateFlusher() {
	sa.mu.Lock()
	defer func() {
		sa.gateFlusherOn = false
		done := sa.gateFlusherDone
		sa.gateFlusherDone = nil
		// If a teardown was requested while we were still draining, run it
		// here while we still hold sa.mu (which is the lock cleanup expects).
		// MUST happen before close(done) so WaitForFlusher returning implies
		// the cleanup has also completed.
		if sa.gatePendingCleanup && sa.parent != nil {
			sa.parent.cleanupStreamAccumulator(sa.RequestID, false)
		}
		sa.mu.Unlock()
		if done != nil {
			close(done)
		}
	}()
	for {
		// Wait while paused (chunks may continue to arrive), or while active
		// with empty buffer (no work). Wake on Ended or buffered-while-active.
		for sa.gateState != StreamStateEnded && (sa.gateState == StreamStatePaused || len(sa.gateReplayBuf) == 0) {
			sa.gateCond.Wait()
		}
		// Drain whatever is buffered (state is Active or Ended here).
		sa.drainBufferLocked()
		// If a terminal chunk arrived while paused, Resume put us back in
		// Active and we just drained it. Now transition to Ended so the
		// terminal-error delivery path below runs (if any) and the loop exits.
		if sa.gatePendingTerminal && sa.gateState == StreamStateActive {
			sa.gatePendingTerminal = false
			sa.gateState = StreamStateEnded
		}
		if sa.gateState == StreamStateEnded {
			// Send synthetic terminal error chunk if EndStream(err) supplied one.
			if sa.gateEndError != nil {
				ch := sa.gateFlusherCh
				ctx := sa.gateFlusherCtx
				errChunk := &schemas.BifrostStreamChunk{BifrostError: sa.gateEndError}
				sa.gateEndError = nil
				sa.mu.Unlock()
				_ = sendOrCancel(ctx, ch, errChunk)
				sa.mu.Lock()
			}
			return
		}
	}
}

// WaitForFlusher blocks until the currently-running flusher goroutine, if any,
// has fully drained and exited. Returns immediately if no flusher is active.
// Useful before closing the response channel so the gate can finalize ordered
// delivery without racing the producer's close.
func (sa *StreamAccumulator) WaitForFlusher() {
	sa.mu.Lock()
	done := sa.gateFlusherDone
	sa.mu.Unlock()
	if done != nil {
		<-done
	}
}

// sendOrCancel forwards chunk to ch with ctx cancellation support.
// Returns true if delivered, false if ctx is done or the channel was closed.
// Recovers from "send on closed channel" panics so a consumer that has gone
// away does not take down the flusher goroutine; the caller treats this as
// equivalent to ctx-done and finalizes the gate.
func sendOrCancel(ctx *schemas.BifrostContext, ch chan *schemas.BifrostStreamChunk, chunk *schemas.BifrostStreamChunk) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	if ctx == nil {
		ch <- chunk
		return true
	}
	select {
	case ch <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}
