package streaming

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// gateReplayBufMaxBytes caps the per-stream paused replay buffer at 100 MB.
// On overflow, the gate is force-ended with a synthetic error chunk so the
// consumer is notified and memory is released.
const gateReplayBufMaxBytes int64 = 100 * 1024 * 1024

// gateChunkSizeFloor is the per-chunk baseline for replay accounting: struct
// overhead plus the small scalar fields the estimator does not walk. Measured
// heap overhead for a passthrough chunk is ~500B; 512 keeps the floor honest
// without tripping the 100MB cap on large-but-legitimate streams (a 60MB
// stream in 1.4KB events carries ~43k chunks — a 1KB floor alone would have
// added 43MB to the estimate).
const gateChunkSizeFloor int64 = 512

// gateReplayEntry pairs a buffered chunk with its size estimate, computed once
// at append so cap accounting never re-serializes held chunks.
//
// Ownership contract: a chunk handed to GateSend belongs to the delivery
// pipeline from that point on. Producers must hand over freshly built,
// right-sized chunks (no shared scratch buffers, no sub-slices of larger
// arrays) and must not retain or mutate them afterward — the same invariant
// the non-gated channel send already requires, since the chunk outlives the
// producer's loop either way. The only sanctioned post-append modification is
// TransformPausedBuffer, whose copy-on-write replacements are re-estimated at
// install. Violating this contract cannot corrupt delivery or bypass guardrail
// inspection; it can only make the cached size (and thus the
// gateReplayBufMaxBytes accounting) under- or over-read.
type gateReplayEntry struct {
	chunk *schemas.BifrostStreamChunk
	size  int64
}

// gateRawBytes sizes the raw request/response carriers without serializing:
// they typically hold json.RawMessage, []byte, or string. An atypical carrier
// (e.g. a pre-parsed map) is sonic-sized rather than riding the buffer
// uncounted; that path only exists for providers that deviate from
// compactRawJSON's json.RawMessage contract.
func gateRawBytes(v interface{}) int64 {
	switch b := v.(type) {
	case nil:
		return 0
	case *interface{}:
		// Unwrap a pointer-boxed value so its payload hits the fast len()
		// cases below instead of the sonic fallback.
		if b == nil {
			return 0
		}
		return gateRawBytes(*b)
	case json.RawMessage:
		return int64(len(b))
	case []byte:
		return int64(len(b))
	case string:
		return int64(len(b))
	}
	return gateCompositeBytes(v)
}

func gateStrPtrBytes(s *string) int64 {
	if s == nil {
		return 0
	}
	return int64(len(*s))
}

// gateCompositeBytes sizes a composite carrier (full item, part, or terminal
// message copy) with a plain sonic marshal. Composites ride only item-boundary
// events, never per-delta chunks, so this stays off the hot append path.
func gateCompositeBytes(v interface{}) int64 {
	b, err := sonic.Marshal(v)
	if err != nil {
		return 0
	}
	return int64(len(b))
}

// estimateChunkBytes estimates the heap held by one buffered chunk without
// serializing it. It feeds only the gateReplayBufMaxBytes safety cap, so
// exactness is not required: it counts the carriers that grow with response
// size — passthrough bodies, raw request/response bytes, delta/text strings,
// tool-call arguments, and composite item copies — and covers everything else
// with a fixed per-chunk floor.
func estimateChunkBytes(chunk *schemas.BifrostStreamChunk) int64 {
	if chunk == nil {
		return 0
	}
	size := gateChunkSizeFloor
	switch {
	case chunk.BifrostPassthroughResponse != nil:
		r := chunk.BifrostPassthroughResponse
		size += int64(len(r.Body))
		size += gateRawBytes(r.ExtraFields.RawRequest) + gateRawBytes(r.ExtraFields.RawResponse)
	case chunk.BifrostChatResponse != nil:
		r := chunk.BifrostChatResponse
		size += int64(len(r.ID)) + gateChoiceBytes(r.Choices)
		size += gateRawBytes(r.ExtraFields.RawRequest) + gateRawBytes(r.ExtraFields.RawResponse)
	case chunk.BifrostTextCompletionResponse != nil:
		r := chunk.BifrostTextCompletionResponse
		size += int64(len(r.ID)) + gateChoiceBytes(r.Choices)
		size += gateRawBytes(r.ExtraFields.RawRequest) + gateRawBytes(r.ExtraFields.RawResponse)
	case chunk.BifrostResponsesStreamResponse != nil:
		r := chunk.BifrostResponsesStreamResponse
		size += gateStrPtrBytes(r.Delta) + gateStrPtrBytes(r.Text) + gateStrPtrBytes(r.Arguments) +
			gateStrPtrBytes(r.Input) + gateStrPtrBytes(r.Refusal) + gateStrPtrBytes(r.Obfuscation) +
			gateStrPtrBytes(r.PartialImageB64) + gateStrPtrBytes(r.Signature)
		if r.Item != nil {
			size += gateCompositeBytes(r.Item)
		}
		if r.Part != nil {
			size += gateCompositeBytes(r.Part)
		}
		if r.Response != nil {
			size += gateCompositeBytes(r.Response)
		}
		if len(r.LogProbs) > 0 {
			// Same treatment as chat choice logprobs: only present when the
			// client requested them, several KB per chunk at high top_logprobs.
			size += gateCompositeBytes(r.LogProbs)
		}
		size += gateRawBytes(r.ExtraFields.RawRequest) + gateRawBytes(r.ExtraFields.RawResponse)
	case chunk.BifrostSpeechStreamResponse != nil:
		r := chunk.BifrostSpeechStreamResponse
		size += int64(len(r.Audio))
		size += gateRawBytes(r.ExtraFields.RawRequest) + gateRawBytes(r.ExtraFields.RawResponse)
	case chunk.BifrostTranscriptionStreamResponse != nil:
		r := chunk.BifrostTranscriptionStreamResponse
		size += gateStrPtrBytes(r.Delta) + int64(len(r.Text))
		size += gateRawBytes(r.ExtraFields.RawRequest) + gateRawBytes(r.ExtraFields.RawResponse)
	case chunk.BifrostImageGenerationStreamResponse != nil:
		r := chunk.BifrostImageGenerationStreamResponse
		size += int64(len(r.B64JSON)) + int64(len(r.URL))
		// The image variant carries direct raw strings separate from ExtraFields.
		size += int64(len(r.RawRequest)) + int64(len(r.RawResponse))
		size += gateRawBytes(r.ExtraFields.RawRequest) + gateRawBytes(r.ExtraFields.RawResponse)
	case chunk.BifrostError != nil:
		e := chunk.BifrostError
		if e.Error != nil {
			size += int64(len(e.Error.Message))
		}
		size += gateRawBytes(e.ExtraFields.RawRequest) + gateRawBytes(e.ExtraFields.RawResponse)
	}
	return size
}

// gateChoiceBytes sizes the text carriers of chat/text-completion choices:
// stream deltas (content, reasoning, refusal, tool-call arguments), terminal
// message copies, and text-completion text.
func gateChoiceBytes(choices []schemas.BifrostResponseChoice) int64 {
	var size int64
	for i := range choices {
		c := &choices[i]
		if c.ChatStreamResponseChoice != nil && c.ChatStreamResponseChoice.Delta != nil {
			d := c.ChatStreamResponseChoice.Delta
			size += gateStrPtrBytes(d.Content) + gateStrPtrBytes(d.Reasoning) + gateStrPtrBytes(d.Refusal)
			size += int64(len(d.ExtraContent))
			for j := range d.ToolCalls {
				size += int64(len(d.ToolCalls[j].Function.Arguments))
			}
			if d.Audio != nil {
				size += int64(len(d.Audio.Data)) + int64(len(d.Audio.Transcript))
			}
			for j := range d.ReasoningDetails {
				rd := &d.ReasoningDetails[j]
				size += gateStrPtrBytes(rd.Text) + gateStrPtrBytes(rd.Data) +
					gateStrPtrBytes(rd.Signature) + gateStrPtrBytes(rd.Summary)
			}
		}
		if c.LogProbs != nil {
			// Only present when the client requested logprobs; several KB per
			// chunk at top_logprobs=20, far past the floor.
			size += gateCompositeBytes(c.LogProbs)
		}
		if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil {
			size += gateCompositeBytes(c.ChatNonStreamResponseChoice.Message)
		}
		if c.TextCompletionResponseChoice != nil {
			size += gateStrPtrBytes(c.TextCompletionResponseChoice.Text)
		}
	}
	return size
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

// ResumeStreamWithReplayInterval arms a paced resume that starts after the next chunk joins the paused buffer.
func (a *Accumulator) ResumeStreamWithReplayInterval(traceID string, eventInterval time.Duration) bool {
	if traceID == "" || eventInterval <= 0 {
		return false
	}
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		return false
	}
	return v.(*StreamAccumulator).ResumeWithReplayInterval(eventInterval)
}

// ClearPausedStreamBuffer drops chunks buffered while a stream is paused.
func (a *Accumulator) ClearPausedStreamBuffer(traceID string) error {
	if traceID == "" {
		return fmt.Errorf("trace ID is empty")
	}
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		return fmt.Errorf("stream accumulator not found")
	}
	return v.(*StreamAccumulator).ClearPausedBuffer()
}

// TransformPausedStreamBuffer atomically rewrites chunks captured by the latest pause epoch.
func (a *Accumulator) TransformPausedStreamBuffer(traceID string, transform schemas.PausedStreamBufferTransform) error {
	if traceID == "" {
		return fmt.Errorf("trace ID is empty")
	}
	v, ok := a.streamAccumulators.Load(traceID)
	if !ok {
		return fmt.Errorf("stream accumulator not found")
	}
	return v.(*StreamAccumulator).TransformPausedBuffer(transform)
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
		streamType = sa.chatStreamType
		if streamType == "" {
			streamType = StreamTypeChat
		}
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
	case StreamTypeChat, StreamTypeText:
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

// Pause transitions the gate from Active to Paused and revokes buffered replay permission. Idempotent.
func (sa *StreamAccumulator) Pause() {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState != StreamStateActive {
		return
	}
	// A new pause must not inherit pacing from an earlier replay.
	sa.gateReplayEventInterval = 0
	sa.gateState = StreamStatePaused
	sa.gatePausedAt = sa.gateSeq
	sa.gatePauseEpoch++
	// Keep an older replay backlog outside the new transform scope, but do not
	// let it cross the new pause until a transform commits or Resume is called.
	sa.gateTransformStart = len(sa.gateReplayBuf)
	sa.gateApprovedPrefix = 0
}

// Resume transitions the gate from Paused back to Active and wakes the flusher
// to drain buffered chunks. Idempotent.
func (sa *StreamAccumulator) Resume() {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState != StreamStatePaused {
		return
	}
	// Plain resume drains immediately, so discard any armed replay interval.
	sa.gateReplayEventInterval = 0
	sa.gateState = StreamStateActive
	if sa.gateCond != nil {
		sa.gateCond.Broadcast()
	}
}

// ResumeWithReplayInterval records a paced-resume request without waking the flusher;
// the next GateSend activates it after buffering the in-flight chunk.
func (sa *StreamAccumulator) ResumeWithReplayInterval(eventInterval time.Duration) bool {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState != StreamStatePaused || eventInterval <= 0 {
		return false
	}
	sa.gateReplayEventInterval = eventInterval
	return true
}

// waitForReplayIntervalLocked waits between consecutive buffered event deliveries.
// It unlocks while waiting so other stream lifecycle operations can proceed,
// then restores the lock before reporting whether replay should continue.
func (sa *StreamAccumulator) waitForReplayIntervalLocked() bool {
	// Snapshot the value while locked so this wait uses one stable interval.
	interval := sa.gateReplayEventInterval
	if interval <= 0 {
		return true
	}
	ctx := sa.gateFlusherCtx
	timer := time.NewTimer(interval)
	// Do not hold the stream state lock during the configured delay.
	sa.mu.Unlock()
	canceled := false
	if ctx == nil {
		<-timer.C
	} else {
		// Continue when the delay elapses, or stop early if the request is canceled.
		select {
		case <-timer.C:
		case <-ctx.Done():
			canceled = true
		}
	}
	// Stop and drain the timer before restoring the caller's locking invariant.
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	// Callers enter and leave this helper with sa.mu held.
	sa.mu.Lock()
	return !canceled
}

// ClearPausedBuffer removes replay chunks captured while the gate is paused.
// If a terminal chunk was among the dropped chunks, delivering a replacement
// terminal becomes the caller's responsibility.
func (sa *StreamAccumulator) ClearPausedBuffer() error {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState != StreamStatePaused {
		return fmt.Errorf("stream gate is not paused")
	}
	sa.gateReplayBuf = nil
	sa.gateReplayBufBytes = 0
	sa.gateTransformStart = 0
	sa.gateApprovedPrefix = 0
	// Dropping the buffer also discards the pacing armed for that buffer.
	sa.gateReplayEventInterval = 0
	// A terminal chunk buffered while paused no longer exists; keeping the
	// pending-terminal marker would make the flusher end the gate on resume
	// and silently drop the caller's replacement terminal chunk.
	sa.gatePendingTerminal = false
	if sa.gateCond != nil {
		sa.gateCond.Broadcast()
	}
	return nil
}

// TransformPausedBuffer transactionally replaces the current pause epoch's chunk pointers.
// The callback runs without the gate mutex, and the result is installed only when the
// paused epoch and its buffered suffix are unchanged. Callbacks must use copy-on-write.
func (sa *StreamAccumulator) TransformPausedBuffer(transform schemas.PausedStreamBufferTransform) error {
	if transform == nil {
		return fmt.Errorf("paused stream buffer transform is nil")
	}

	sa.mu.Lock()
	if sa.gateState != StreamStatePaused {
		sa.mu.Unlock()
		return fmt.Errorf("stream gate is not paused")
	}
	epoch := sa.gatePauseEpoch
	start := sa.gateTransformStart
	if start < 0 || start > len(sa.gateReplayBuf) || sa.gateApprovedPrefix > start {
		sa.mu.Unlock()
		return fmt.Errorf("paused stream buffer transform scope is invalid")
	}
	original := make([]*schemas.BifrostStreamChunk, len(sa.gateReplayBuf)-start)
	var originalBytes int64
	for i, entry := range sa.gateReplayBuf[start:] {
		original[i] = entry.chunk
		originalBytes += entry.size
	}
	sa.mu.Unlock()

	result, err := transform(original)
	if err != nil {
		return err
	}
	if len(result.Chunks) != len(original) {
		return fmt.Errorf("paused stream buffer transform changed chunk count from %d to %d", len(original), len(result.Chunks))
	}
	if result.ReleaseCount < 0 || result.ReleaseCount > len(original) {
		return fmt.Errorf("paused stream buffer transform release count %d is outside [0,%d]", result.ReleaseCount, len(original))
	}

	// Size the replacements once; cached alongside each installed entry below.
	transformedSizes := make([]int64, len(result.Chunks))
	var transformedBytes int64
	for i := range result.Chunks {
		transformedSizes[i] = estimateChunkBytes(result.Chunks[i])
		transformedBytes += transformedSizes[i]
	}

	sa.mu.Lock()
	defer sa.mu.Unlock()
	if sa.gateState != StreamStatePaused || sa.gatePauseEpoch != epoch {
		return fmt.Errorf("paused stream buffer changed during transformation")
	}
	currentStart := sa.gateTransformStart
	if currentStart < 0 || currentStart > start || sa.gateApprovedPrefix > currentStart || len(sa.gateReplayBuf) != currentStart+len(original) {
		return fmt.Errorf("paused stream buffer changed during transformation")
	}
	for i := range original {
		if sa.gateReplayBuf[currentStart+i].chunk != original[i] {
			return fmt.Errorf("paused stream buffer changed during transformation")
		}
	}
	nextBytes := sa.gateReplayBufBytes - originalBytes + transformedBytes
	if nextBytes < 0 {
		nextBytes = 0
	}
	if nextBytes > gateReplayBufMaxBytes {
		return fmt.Errorf("transformed paused stream buffer exceeds %d bytes", gateReplayBufMaxBytes)
	}
	for i := range result.Chunks {
		sa.gateReplayBuf[currentStart+i] = gateReplayEntry{chunk: result.Chunks[i], size: transformedSizes[i]}
	}
	sa.gateReplayBufBytes = nextBytes
	nextBoundary := currentStart + result.ReleaseCount
	sa.gateTransformStart = nextBoundary
	sa.gateApprovedPrefix = nextBoundary
	if sa.gateApprovedPrefix > 0 && sa.gateCond != nil {
		sa.gateCond.Broadcast()
	}
	return nil
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
	if sa.gateState == StreamStatePaused {
		// Ending before paced replay starts falls back to an immediate terminal drain.
		sa.gateReplayEventInterval = 0
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
		size := estimateChunkBytes(chunk)
		if sa.gateReplayBufBytes+size > gateReplayBufMaxBytes {
			// Overflow: force-end the gate with a synthetic error so the
			// consumer is notified and memory is released. Drops this chunk
			// and any further chunks for this stream.
			sa.gateState = StreamStateEnded
			// Replay cannot continue after overflow forces the gate to end.
			sa.gateReplayEventInterval = 0
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
		sa.gateReplayBuf = append(sa.gateReplayBuf, gateReplayEntry{chunk: chunk, size: size})
		sa.gateReplayBufBytes += size
		if isFinal || isHardErr {
			sa.gatePendingTerminal = true
		}
		if !sa.gateFlusherOn {
			sa.gateFlusherOn = true
			sa.gateFlusherDone = make(chan struct{})
			go sa.gateFlusher()
		}
		if sa.gateReplayEventInterval > 0 {
			sa.gateState = StreamStateActive
			if sa.gateCond != nil {
				sa.gateCond.Broadcast()
			}
		}
		sa.mu.Unlock()
		return true
	}

	// Active state from here on.
	if isFinal || isHardErr {
		// If there's already a flusher or a non-empty buffer, append the final
		// chunk so it's delivered after pending chunks. Otherwise fast-path send.
		if sa.gateFlusherOn || len(sa.gateReplayBuf) > 0 {
			size := estimateChunkBytes(chunk)
			sa.gateReplayBuf = append(sa.gateReplayBuf, gateReplayEntry{chunk: chunk, size: size})
			sa.gateReplayBufBytes += size
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
		size := estimateChunkBytes(chunk)
		sa.gateReplayBuf = append(sa.gateReplayBuf, gateReplayEntry{chunk: chunk, size: size})
		sa.gateReplayBufBytes += size
		sa.gateCond.Broadcast()
		sa.mu.Unlock()
		return true
	}
	sa.mu.Unlock()
	return sendOrCancel(ctx, ch, chunk)
}

// drainBufferLocked drains gateReplayBuf to gateFlusherCh in order. MUST be
// called with sa.mu held. Releases sa.mu while sending; reacquires before
// returning. While paused, it drains only the approved prefix and leaves the
// current unevaluated suffix held. It also stops when ctx is done.
func (sa *StreamAccumulator) drainBufferLocked() {
	for len(sa.gateReplayBuf) > 0 && (sa.gateState != StreamStatePaused || sa.gateApprovedPrefix > 0) {
		entry := sa.gateReplayBuf[0]
		sa.gateReplayBuf = sa.gateReplayBuf[1:]
		if sa.gateTransformStart > 0 {
			sa.gateTransformStart--
		}
		if sa.gateApprovedPrefix > 0 {
			sa.gateApprovedPrefix--
		}
		// Decrement bytes per-chunk so the counter stays accurate even if
		// the loop exits mid-drain (Pause). Sizes are cached at append, so
		// this is exact; the clamp is pure defense.
		sa.gateReplayBufBytes -= entry.size
		if sa.gateReplayBufBytes < 0 {
			sa.gateReplayBufBytes = 0
		}
		ch := sa.gateFlusherCh
		ctx := sa.gateFlusherCtx
		sa.mu.Unlock()
		ok := sendOrCancel(ctx, ch, entry.chunk)
		sa.mu.Lock()
		if ok && len(sa.gateReplayBuf) > 0 {
			ok = sa.waitForReplayIntervalLocked()
		}
		if !ok {
			// Delivery or pacing was canceled; abandon the remaining buffer.
			sa.gateReplayBuf = nil
			sa.gateReplayBufBytes = 0
			sa.gateTransformStart = 0
			sa.gateApprovedPrefix = 0
			// Canceled replay is terminal, so its interval is no longer applicable.
			sa.gateReplayEventInterval = 0
			sa.gateState = StreamStateEnded
			return
		}
	}
	if len(sa.gateReplayBuf) == 0 {
		sa.gateReplayBuf = nil // release backing array
		sa.gateReplayBufBytes = 0
		sa.gateTransformStart = 0
		sa.gateApprovedPrefix = 0
		// Pacing belongs to this buffer and must not survive a completed replay.
		sa.gateReplayEventInterval = 0
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
		for sa.gateState != StreamStateEnded && (len(sa.gateReplayBuf) == 0 || (sa.gateState == StreamStatePaused && sa.gateApprovedPrefix == 0)) {
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
