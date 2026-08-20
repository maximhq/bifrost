package lib

import (
	"io"
	"sync"
	"time"
)

// SSEStreamReader is an io.ReadCloser that delivers one event per Read call,
// bypassing fasthttp's internal pipe mechanism (fasthttputil.PipeConns) which
// batches multiple events into single TCP segments.
//
// Usage:
//  1. Create with NewSSEStreamReader()
//  2. Pass to ctx.Response.SetBodyStream(reader, -1)
//  3. Start a producer goroutine that calls Send()/SendEvent()/SendError() for each event
//  4. Producer calls Done() when finished (closes the event channel)
//  5. fasthttp calls Close() on write errors (signals producer to stop)
type SSEStreamReader struct {
	eventCh   chan []byte
	closeCh   chan struct{}
	closeOnce sync.Once
	current   []byte // remaining bytes from a partial read

	// mu guards atLineBoundary and makes "check the write position, then write" atomic against
	// a concurrent producer. Heartbeats are written by an independent goroutine (see
	// StartSSEHeartbeat), so an unsynchronized check could go stale between the check and the
	// write: the producer can enqueue a partial line into that gap, which is exactly #5905.
	// An atomic flag is NOT sufficient for the same reason -- the pair must be atomic, not the
	// flag.
	mu sync.Mutex
	// atLineBoundary reports whether the last bytes handed to Send terminated a line, i.e.
	// whether the next byte written starts a fresh SSE line. True initially: the start of a
	// stream is a line boundary.
	atLineBoundary bool
}

// NewSSEStreamReader creates a new SSEStreamReader with a buffered event channel.
// Channel capacity of 1 allows one event of pipeline parallelism between
// the producer goroutine and fasthttp's writeBodyChunked loop.
func NewSSEStreamReader() *SSEStreamReader {
	return &SSEStreamReader{
		eventCh:        make(chan []byte, 1),
		closeCh:        make(chan struct{}),
		atLineBoundary: true,
	}
}

// Read implements io.Reader. It blocks until an event is available, then returns
// that event's bytes. If the caller's buffer is smaller than the event, remaining
// bytes are stored and returned on subsequent calls. Returns io.EOF when Done()
// has been called and all events have been consumed.
func (r *SSEStreamReader) Read(p []byte) (int, error) {
	if len(r.current) == 0 {
		event, ok := <-r.eventCh
		if !ok {
			return 0, io.EOF
		}
		r.current = event
	}
	n := copy(p, r.current)
	r.current = r.current[n:]
	return n, nil
}

// Close implements io.Closer. Called by fasthttp when writeBodyChunked encounters
// a write error (client disconnect). Signals the producer goroutine to stop via closeCh.
// Safe to call multiple times.
func (r *SSEStreamReader) Close() error {
	r.closeOnce.Do(func() {
		close(r.closeCh)
	})
	return nil
}

// Send delivers a pre-formatted event to the reader. Returns false if the reader
// has been closed (client disconnected), in which case the producer should stop.
//
// event need not be a complete SSE frame. The raw passthrough path forwards arbitrary
// upstream network chunks (core's StreamPassthrough reads into a fixed 4096-byte buffer), so
// a chunk can end anywhere, including mid-line. Send therefore records where it left off so
// SendHeartbeat can tell whether injecting a comment line there is safe.
func (r *SSEStreamReader) Send(event []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sendLocked(event)
}

// sendLocked is Send's body; callers must hold r.mu. Split out so SendHeartbeat can check the
// write position and send without releasing the lock in between -- otherwise a producer could
// write a partial line into the gap and the safety check would already be stale.
//
// Blocking on eventCh while holding r.mu is safe: Close() closes closeCh, which the inner
// select watches, so a stalled send is always unblockable without acquiring r.mu.
func (r *SSEStreamReader) sendLocked(event []byte) bool {
	// Check closeCh first (non-blocking) to avoid sending after Close
	select {
	case <-r.closeCh:
		return false
	default:
	}
	select {
	case r.eventCh <- event:
		// A zero-length send writes nothing, so it cannot move the write position.
		if len(event) > 0 {
			r.atLineBoundary = event[len(event)-1] == '\n'
		}
		return true
	case <-r.closeCh:
		return false
	}
}

// SendEvent sends an SSE-framed event. If eventType is empty, it sends "data: <data>\n\n".
// If eventType is non-empty, it sends "event: <eventType>\ndata: <data>\n\n".
// Returns false if the reader has been closed (client disconnected).
func (r *SSEStreamReader) SendEvent(eventType string, data []byte) bool {
	var buf []byte
	if eventType != "" {
		buf = make([]byte, 0, 7+len(eventType)+7+len(data)+2)
		buf = append(buf, "event: "...)
		buf = append(buf, eventType...)
		buf = append(buf, "\ndata: "...)
	} else {
		buf = make([]byte, 0, 6+len(data)+2)
		buf = append(buf, "data: "...)
	}
	buf = append(buf, data...)
	buf = append(buf, '\n', '\n')
	return r.Send(buf)
}

// SendError sends an SSE error event: "event: error\ndata: <data>\n\n".
// Returns false if the reader has been closed (client disconnected).
func (r *SSEStreamReader) SendError(data []byte) bool {
	return r.SendEvent("error", data)
}

// SendDone sends the standard SSE done marker: "data: [DONE]\n\n".
// Returns false if the reader has been closed (client disconnected).
func (r *SSEStreamReader) SendDone() bool {
	return r.Send([]byte("data: [DONE]\n\n"))
}

// sseHeartbeatFrame is an SSE comment line that every compliant client ignores.
// Deliberately no trailing blank line: that's the event-dispatch signal, and
// some deployed decoders (e.g. openai-go ssestream < v3.43.0) dispatch an empty
// event on it and abort the stream (#5874). A bare comment line is still a real
// socket write, which is all the disconnect probe needs.
var sseHeartbeatFrame = []byte(": heartbeat\n")

// SendHeartbeat sends a no-op SSE comment line purely to force an additional
// downstream write attempt. Client-disconnect detection in this reader is
// reactive: fasthttp only calls Close() when a write actually fails, and a
// write only happens when the producer calls Send/SendEvent/SendError/SendDone.
// If the upstream provider finishes fast enough (few large chunks) that the
// producer never attempts another write during a disconnect window, the
// disconnect goes undetected and the stream completes as if nothing happened.
// A caller sending this periodically (independent of real data) closes that
// window. Returns false if the reader has been closed (client disconnected).
//
// The frame is only emitted when the stream is at a line boundary. An SSE comment is only a
// comment when the colon is the FIRST character of a line; the spec then ignores the line
// without resetting or dispatching the event buffers, so start-of-line is sufficient and a
// full event boundary is not required. Spliced mid-line it is not a comment at all: it is
// appended to the half-written line, its "\n" terminates that line early, and the remainder
// arrives without its "data:" prefix, which decoders silently discard as an unknown field
// (openai-python _streaming.py: `pass  # Field is ignored.`). The client then sees truncated
// JSON and raises a decode error. That is #5905, and no heartbeat frame SHAPE can fix it --
// #5883's trailing-blank-line change is a separate, complementary fix.
//
// Ordering matters: a closed reader reports false (a real disconnect) even when mid-line, so
// StartSSEHeartbeat still discovers the disconnect. Only on a live reader does mid-line mean
// "skip", which returns true -- StartSSEHeartbeat reads false as "client gone" and would
// cancel an otherwise healthy stream. Skipping costs nothing: being mid-line means real bytes
// are actively flowing, so the reactive write-failure detector already has the coverage a
// heartbeat would provide.
//
// On the typed paths (handleStreaming, handleStreamingResponse) Bifrost frames every event
// itself and each Send ends in "\n\n", so the stream is always at a boundary there and
// heartbeats are never skipped.
func (r *SSEStreamReader) SendHeartbeat() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A real disconnect outranks a skip: report it even when mid-line.
	select {
	case <-r.closeCh:
		return false
	default:
	}
	if !r.atLineBoundary {
		return true
	}
	return r.sendLocked(sseHeartbeatFrame)
}

// Done closes the event channel, signaling to Read that the stream is finished.
// Must be called exactly once by the producer goroutine when streaming is complete.
func (r *SSEStreamReader) Done() {
	close(r.eventCh)
}

// DefaultSSEHeartbeatInterval is how often a heartbeat goroutine started with
// StartSSEHeartbeat probes the downstream connection by default. This helper now serves
// every idle inference, routed streaming, and SSE passthrough stream, so the interval
// trades off disconnect-detection latency against downstream write volume: at N
// concurrently idle streams, a shorter interval means up to N/interval extra writes per
// second carrying no provider data. One second keeps that volume low while still closing
// the disconnect-detection gap described in SendHeartbeat's doc; short intervals belong
// only in focused lifecycle tests, not this default.
const DefaultSSEHeartbeatInterval = time.Second

// StartSSEHeartbeat launches a goroutine that calls send on every tick of interval,
// purely to force a downstream write attempt during otherwise-idle gaps. Client-disconnect
// detection is otherwise reactive: it only fires when a producer actually attempts a
// write, which a fast/bursty producer may not do again during the exact window a client
// disconnects (see SendHeartbeat's doc for the full rationale). If send returns false
// (the reader was closed, i.e. a disconnect was discovered), onDisconnect is called once
// and the goroutine exits.
//
// Callers MUST shut this down via StopSSEHeartbeat before calling reader.Done() -- never
// by closing the returned done channel directly. See StopSSEHeartbeat for why.
func StartSSEHeartbeat(interval time.Duration, send func() bool, onDisconnect func()) (done chan struct{}, exited <-chan struct{}) {
	doneCh := make(chan struct{})
	exitedCh := make(chan struct{})
	go func() {
		defer close(exitedCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !send() {
					onDisconnect()
					return
				}
			case <-doneCh:
				return
			}
		}
	}()
	return doneCh, exitedCh
}

// StopSSEHeartbeat performs the safe shutdown sequence for a heartbeat started with
// StartSSEHeartbeat. Closing done alone only unblocks the heartbeat goroutine if it's
// waiting at its outer select; if it's currently blocked inside send (e.g. Send's
// `eventCh <- event` case, buffer full, nothing reading), closing done does nothing --
// that goroutine isn't looking at it. reader.Close() closes closeCh instead, which
// Send's inner select already watches, safely unblocking a pending send without racing
// eventCh's close. Callers must call this BEFORE reader.Done(): closing eventCh while
// the heartbeat goroutine could still be mid-send on it panics ("send on closed channel").
func StopSSEHeartbeat(reader *SSEStreamReader, done chan struct{}, exited <-chan struct{}) {
	close(done)
	reader.Close()
	<-exited
}
