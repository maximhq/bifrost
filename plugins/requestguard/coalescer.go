package requestguard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Coalescer shares completed unary responses and live stream chunks for
// requests with the same eligible fingerprint. Its retained state is bounded
// by MaxInFlight, MaxBufferedChunks, and MaxBufferedBytes.
type Coalescer struct {
	mu      sync.Mutex
	cfg     CoalescerConfig
	flights map[string]*coalescedFlight
	closed  bool
}

type coalescedFlight struct {
	key            string
	streaming      bool
	done           chan struct{}
	complete       bool
	waiters        int
	response       *schemas.BifrostResponse
	err            *schemas.BifrostError
	streamErr      *schemas.BifrostError
	buffer         []*schemas.BifrostStreamChunk
	bufferBytes    int
	bufferOverflow bool
	subscribers    map[*FlightHandle]*streamSubscriber
}

type streamSubscriber struct {
	handle      *FlightHandle
	queue       chan queuedChunk
	done        chan struct{}
	active      bool
	queuedBytes int
	stop        func() bool
}

type queuedChunk struct {
	chunk *schemas.BifrostStreamChunk
	bytes int
}

// FlightHandle is the caller's view of a coalesced request. The leader passes
// its handle to CompleteUnary, PublishChunk, FinishStream, or Abort. Followers
// read Response/Err after a unary Acquire returns, or range Chunks for streams.
type FlightHandle struct {
	Response   *schemas.BifrostResponse
	Err        *schemas.BifrostError
	Chunks     chan *schemas.BifrostStreamChunk
	ContextErr error

	coalescer *Coalescer
	flight    *coalescedFlight
	leader    bool
	streaming bool
}

// NewCoalescer creates a coalescer from config. Zero capacity fields use the
// package defaults, matching Config.NormalizeAndValidate.
func NewCoalescer(cfg CoalescerConfig) *Coalescer {
	defaults := DefaultConfig().Coalescer
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = defaults.MaxInFlight
	}
	if cfg.WaitTimeout <= 0 {
		cfg.WaitTimeout = defaults.WaitTimeout
	}
	if cfg.MaxBufferedChunks <= 0 {
		cfg.MaxBufferedChunks = defaults.MaxBufferedChunks
	}
	if cfg.MaxBufferedBytes <= 0 {
		cfg.MaxBufferedBytes = defaults.MaxBufferedBytes
	}
	return &Coalescer{cfg: cfg, flights: make(map[string]*coalescedFlight)}
}

// Acquire joins a matching flight or creates a new leader. A nil handle with a
// true leader flag means fail open: the caller should execute this request as
// an uncoalesced leader. Unary followers wait for the leader; streaming
// followers receive a chunk channel immediately.
func (c *Coalescer) Acquire(ctx context.Context, key string, streaming bool) (*FlightHandle, bool) {
	if c == nil || key == "" {
		return nil, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.closed || !c.cfg.Enabled {
		c.mu.Unlock()
		return nil, true
	}
	if flight := c.flights[key]; flight != nil {
		if flight.complete || flight.streaming != streaming {
			c.mu.Unlock()
			return nil, true
		}
		if streaming {
			handle := c.acquireStreamFollowerLocked(ctx, flight)
			c.mu.Unlock()
			if handle == nil {
				return nil, true
			}
			return handle, false
		}
		if flight.waiters >= c.cfg.MaxInFlight {
			c.mu.Unlock()
			return nil, true
		}
		flight.waiters++
		handle := &FlightHandle{coalescer: c, flight: flight, streaming: false}
		c.mu.Unlock()
		if !c.waitForUnary(ctx, flight) {
			c.decrementWaiter(flight)
			return nil, true
		}
		c.decrementWaiter(flight)
		response, err := cloneResponse(flight.response)
		clonedErr, errClone := cloneBifrostError(flight.err)
		if err != nil {
			handle.Err = coalescerError("could not clone the coalesced response")
		} else if errClone != nil {
			handle.Err = coalescerError("could not clone the coalesced error")
		} else {
			handle.Response = response
			handle.Err = clonedErr
		}
		return handle, false
	}
	if len(c.flights) >= c.cfg.MaxInFlight {
		c.mu.Unlock()
		return nil, true
	}
	flight := &coalescedFlight{
		key:         key,
		streaming:   streaming,
		done:        make(chan struct{}),
		subscribers: make(map[*FlightHandle]*streamSubscriber),
	}
	c.flights[key] = flight
	handle := &FlightHandle{coalescer: c, flight: flight, leader: true, streaming: streaming}
	c.mu.Unlock()
	return handle, true
}

func (c *Coalescer) acquireStreamFollowerLocked(ctx context.Context, flight *coalescedFlight) *FlightHandle {
	if flight.bufferOverflow || len(flight.subscribers) >= c.cfg.MaxInFlight {
		return nil
	}
	maxInt := int(^uint(0) >> 1)
	if c.cfg.MaxBufferedChunks >= maxInt {
		return nil
	}
	queueCapacity := c.cfg.MaxBufferedChunks + 1
	handle := &FlightHandle{
		coalescer: c,
		flight:    flight,
		streaming: true,
		Chunks:    make(chan *schemas.BifrostStreamChunk),
	}
	subscriber := &streamSubscriber{
		handle: handle,
		queue:  make(chan queuedChunk, queueCapacity),
		done:   make(chan struct{}),
		active: true,
	}
	for _, bufferedChunk := range flight.buffer {
		cloned, size, err := cloneChunkAndSize(bufferedChunk)
		if err != nil || subscriber.queuedBytes > c.cfg.MaxBufferedBytes-size {
			return nil
		}
		subscriber.queue <- queuedChunk{chunk: cloned, bytes: size}
		subscriber.queuedBytes += size
	}
	flight.subscribers[handle] = subscriber
	if ctx.Done() != nil {
		subscriber.stop = context.AfterFunc(ctx, func() {
			c.cancelStreamFollower(flight, handle, ctx.Err())
		})
	}
	go c.forwardStreamFollower(subscriber)
	return handle
}

func (c *Coalescer) waitForUnary(ctx context.Context, flight *coalescedFlight) bool {
	timer := time.NewTimer(c.cfg.WaitTimeout)
	defer timer.Stop()
	select {
	case <-flight.done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (c *Coalescer) decrementWaiter(flight *coalescedFlight) {
	c.mu.Lock()
	if flight.waiters > 0 {
		flight.waiters--
	}
	c.mu.Unlock()
}

// CompleteUnary completes a leader's unary request and wakes all waiting
// followers. Each follower receives independent deep copies of the response or
// error through Acquire's returned handle.
func (c *Coalescer) CompleteUnary(handle *FlightHandle, response *schemas.BifrostResponse, err *schemas.BifrostError) {
	if c == nil || !c.isLeaderHandle(handle, false) {
		return
	}
	clonedResponse, cloneErr := cloneResponse(response)
	clonedError, errorCloneErr := cloneBifrostError(err)
	if cloneErr != nil {
		clonedResponse = nil
		clonedError = coalescerError("could not clone the leader response")
	} else if errorCloneErr != nil {
		clonedError = coalescerError("could not clone the leader error")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	flight := handle.flight
	if flight.complete {
		return
	}
	flight.response = clonedResponse
	flight.err = clonedError
	flight.complete = true
	c.removeFlightLocked(flight)
	close(flight.done)
}

// PublishChunk appends a bounded replay copy and broadcasts independent clones
// to active followers. It never blocks on a slow follower.
func (c *Coalescer) PublishChunk(handle *FlightHandle, chunk *schemas.BifrostStreamChunk) bool {
	if c == nil || chunk == nil || !c.isLeaderHandle(handle, true) {
		return false
	}
	if schemas.IsStreamCommitMarker(chunk) {
		c.mu.Lock()
		defer c.mu.Unlock()
		flight := handle.flight
		if flight.complete {
			return false
		}
		for _, subscriber := range flight.subscribers {
			if !subscriber.active {
				continue
			}
			if !c.enqueueSubscriberLocked(flight, subscriber, chunk, 0) {
				c.abortSubscriberLocked(flight, subscriber, coalescerError("stream follower could not keep up"), nil)
			}
		}
		return true
	}
	bufferClone, size, cloneErr := cloneChunkAndSize(chunk)
	if cloneErr != nil {
		c.FinishStream(handle, coalescerError("could not clone a stream chunk"))
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	flight := handle.flight
	if flight.complete {
		return false
	}
	if !flight.bufferOverflow {
		if len(flight.buffer) >= c.cfg.MaxBufferedChunks || size > c.cfg.MaxBufferedBytes-flight.bufferBytes {
			flight.bufferOverflow = true
			flight.buffer = nil
			flight.bufferBytes = 0
		} else {
			flight.buffer = append(flight.buffer, bufferClone)
			flight.bufferBytes += size
		}
	}
	for _, subscriber := range flight.subscribers {
		if !subscriber.active {
			continue
		}
		if !c.enqueueSubscriberLocked(flight, subscriber, chunk, size) {
			c.abortSubscriberLocked(flight, subscriber, coalescerError("stream follower could not keep up"), nil)
		}
	}
	return true
}

func (c *Coalescer) enqueueSubscriberLocked(flight *coalescedFlight, subscriber *streamSubscriber, chunk *schemas.BifrostStreamChunk, size int) bool {
	var cloned *schemas.BifrostStreamChunk
	var err error
	if schemas.IsStreamCommitMarker(chunk) {
		cloned = schemas.StreamCommitMarker()
	} else {
		cloned, _, err = cloneChunkAndSize(chunk)
	}
	if err != nil || size > c.cfg.MaxBufferedBytes-subscriber.queuedBytes {
		return false
	}
	select {
	case subscriber.queue <- queuedChunk{chunk: cloned, bytes: size}:
		subscriber.queuedBytes += size
		return true
	default:
		return false
	}
}

// FinishStream marks a streaming leader complete, closes each follower's
// bounded queue after its buffered chunks, and removes the fingerprint.
func (c *Coalescer) FinishStream(handle *FlightHandle, errors ...*schemas.BifrostError) {
	if c == nil || !c.isLeaderHandle(handle, true) {
		return
	}
	var err *schemas.BifrostError
	if len(errors) > 0 {
		err = errors[0]
	}
	clonedErr, cloneErr := cloneBifrostError(err)
	if cloneErr != nil {
		clonedErr = coalescerError("could not clone the stream error")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	flight := handle.flight
	if flight.complete {
		return
	}
	flight.complete = true
	flight.streamErr = clonedErr
	c.removeFlightLocked(flight)
	close(flight.done)
	for follower, subscriber := range flight.subscribers {
		follower.Err, _ = cloneBifrostError(clonedErr)
		c.stopSubscriberContextLocked(subscriber)
		subscriber.active = false
		delete(flight.subscribers, follower)
		close(subscriber.queue)
	}
}

// Abort completes a flight with an error. Streaming followers receive the
// error on their handle and observe a closed chunk channel.
func (c *Coalescer) Abort(handle *FlightHandle, err *schemas.BifrostError) {
	if handle == nil {
		return
	}
	if err == nil {
		err = coalescerError("coalesced request was aborted")
	}
	if handle.streaming {
		c.FinishStream(handle, err)
		return
	}
	c.CompleteUnary(handle, nil, err)
}

// Close releases every active flight and wakes all followers. It is safe to
// call more than once.
func (c *Coalescer) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	closeErr := coalescerError("request coalescer closed")
	for _, flight := range c.flights {
		if flight.complete {
			continue
		}
		flight.complete = true
		if flight.streaming {
			flight.streamErr, _ = cloneBifrostError(closeErr)
			for follower, subscriber := range flight.subscribers {
				follower.Err, _ = cloneBifrostError(closeErr)
				c.abortSubscriberLocked(flight, subscriber, closeErr, nil)
			}
		} else {
			flight.err, _ = cloneBifrostError(closeErr)
		}
		close(flight.done)
	}
	c.flights = make(map[string]*coalescedFlight)
}

func (c *Coalescer) isLeaderHandle(handle *FlightHandle, streaming bool) bool {
	return handle != nil && handle.coalescer == c && handle.leader && handle.streaming == streaming && handle.flight != nil
}

func (c *Coalescer) removeFlightLocked(flight *coalescedFlight) {
	if c.flights[flight.key] == flight {
		delete(c.flights, flight.key)
	}
}

func (c *Coalescer) cancelStreamFollower(flight *coalescedFlight, handle *FlightHandle, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if subscriber := flight.subscribers[handle]; subscriber != nil {
		c.abortSubscriberLocked(flight, subscriber, nil, err)
	}
}

func (c *Coalescer) abortSubscriberLocked(flight *coalescedFlight, subscriber *streamSubscriber, err *schemas.BifrostError, contextErr error) {
	if !subscriber.active {
		return
	}
	subscriber.active = false
	delete(flight.subscribers, subscriber.handle)
	subscriber.handle.Err, _ = cloneBifrostError(err)
	subscriber.handle.ContextErr = contextErr
	c.stopSubscriberContextLocked(subscriber)
	close(subscriber.done)
	close(subscriber.queue)
}

func (c *Coalescer) stopSubscriberContextLocked(subscriber *streamSubscriber) {
	if subscriber.stop != nil {
		subscriber.stop()
		subscriber.stop = nil
	}
}

func (c *Coalescer) forwardStreamFollower(subscriber *streamSubscriber) {
	defer close(subscriber.handle.Chunks)
	for {
		select {
		case <-subscriber.done:
			return
		case item, open := <-subscriber.queue:
			if !open {
				return
			}
			select {
			case subscriber.handle.Chunks <- item.chunk:
				c.mu.Lock()
				if subscriber.queuedBytes >= item.bytes {
					subscriber.queuedBytes -= item.bytes
				} else {
					subscriber.queuedBytes = 0
				}
				c.mu.Unlock()
			case <-subscriber.done:
				return
			}
		}
	}
}

func cloneResponse(response *schemas.BifrostResponse) (*schemas.BifrostResponse, error) {
	cloned, err := cloneJSONValue(response)
	if err != nil || response == nil || cloned == nil {
		return cloned, err
	}
	sourceExtra, targetExtra := response.GetExtraFields(), cloned.GetExtraFields()
	if sourceExtra != nil && targetExtra != nil {
		if sourceExtra.OverheadLatency != nil {
			value := *sourceExtra.OverheadLatency
			targetExtra.OverheadLatency = &value
		}
		targetExtra.RawResponseFormat = sourceExtra.RawResponseFormat
	}
	return cloned, nil
}

func cloneChunkAndSize(chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, int, error) {
	if chunk == nil {
		return nil, 0, fmt.Errorf("nil stream chunk")
	}
	if schemas.IsStreamCommitMarker(chunk) {
		return schemas.StreamCommitMarker(), 0, nil
	}
	var cloned schemas.BifrostStreamChunk
	switch {
	case chunk.BifrostTextCompletionResponse != nil:
		value, err := cloneJSONValue(chunk.BifrostTextCompletionResponse)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostTextCompletionResponse = value
	case chunk.BifrostChatResponse != nil:
		value, err := cloneJSONValue(chunk.BifrostChatResponse)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostChatResponse = value
	case chunk.BifrostResponsesStreamResponse != nil:
		value, err := cloneJSONValue(chunk.BifrostResponsesStreamResponse)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostResponsesStreamResponse = value
	case chunk.BifrostSpeechStreamResponse != nil:
		value, err := cloneJSONValue(chunk.BifrostSpeechStreamResponse)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostSpeechStreamResponse = value
	case chunk.BifrostTranscriptionStreamResponse != nil:
		value, err := cloneJSONValue(chunk.BifrostTranscriptionStreamResponse)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostTranscriptionStreamResponse = value
	case chunk.BifrostImageGenerationStreamResponse != nil:
		value, err := cloneJSONValue(chunk.BifrostImageGenerationStreamResponse)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostImageGenerationStreamResponse = value
	case chunk.BifrostPassthroughResponse != nil:
		value, err := cloneJSONValue(chunk.BifrostPassthroughResponse)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostPassthroughResponse = value
	case chunk.BifrostError != nil:
		value, err := cloneBifrostError(chunk.BifrostError)
		if err != nil {
			return nil, 0, err
		}
		cloned.BifrostError = value
	default:
		return nil, 0, fmt.Errorf("stream chunk has no response variant")
	}
	encoded, err := schemas.MarshalSorted(&cloned)
	if err != nil {
		return nil, 0, err
	}
	return &cloned, len(encoded), nil
}

func cloneBifrostError(value *schemas.BifrostError) (*schemas.BifrostError, error) {
	cloned, err := cloneJSONValue(value)
	if err != nil || value == nil || cloned == nil {
		return cloned, err
	}
	if value.AllowFallbacks != nil {
		allow := *value.AllowFallbacks
		cloned.AllowFallbacks = &allow
	}
	if value.StreamControl != nil {
		control := *value.StreamControl
		if control.LogError != nil {
			v := *control.LogError
			control.LogError = &v
		}
		if control.SkipStream != nil {
			v := *control.SkipStream
			control.SkipStream = &v
		}
		cloned.StreamControl = &control
	}
	if value.Error != nil && value.Error.Error != nil && cloned.Error != nil {
		cloned.Error.Error = value.Error.Error
	}
	return cloned, nil
}

func cloneJSONValue[T any](value *T) (*T, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := schemas.MarshalSorted(value)
	if err != nil {
		return nil, err
	}
	var cloned T
	if err := schemas.Unmarshal(encoded, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func coalescerError(message string) *schemas.BifrostError {
	typeName := "request_guard_coalescer"
	status := 500
	allowFallbacks := false
	return &schemas.BifrostError{
		Type:           &typeName,
		IsBifrostError: true,
		StatusCode:     &status,
		Error:          &schemas.ErrorField{Message: message},
		AllowFallbacks: &allowFallbacks,
	}
}
