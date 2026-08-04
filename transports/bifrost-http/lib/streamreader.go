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
}

// NewSSEStreamReader creates a new SSEStreamReader with a buffered event channel.
// Channel capacity of 1 allows one event of pipeline parallelism between
// the producer goroutine and fasthttp's writeBodyChunked loop.
func NewSSEStreamReader() *SSEStreamReader {
	return &SSEStreamReader{
		eventCh: make(chan []byte, 1),
		closeCh: make(chan struct{}),
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
func (r *SSEStreamReader) Send(event []byte) bool {
	// Check closeCh first (non-blocking) to avoid sending after Close
	select {
	case <-r.closeCh:
		return false
	default:
	}
	select {
	case r.eventCh <- event:
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

// sseHeartbeatFrame is an SSE comment line -- per the WHATWG SSE spec, a line
// beginning with ':' is a comment that every compliant client ignores. It never
// surfaces to application code as a real event.
var sseHeartbeatFrame = []byte(": heartbeat\n\n")

// SendHeartbeat sends a no-op SSE comment line purely to force an additional
// downstream write attempt. Client-disconnect detection in this reader is
// reactive: fasthttp only calls Close() when a write actually fails, and a
// write only happens when the producer calls Send/SendEvent/SendError/SendDone.
// If the upstream provider finishes fast enough (few large chunks) that the
// producer never attempts another write during a disconnect window, the
// disconnect goes undetected and the stream completes as if nothing happened.
// A caller sending this periodically (independent of real data) closes that
// window. Returns false if the reader has been closed (client disconnected).
func (r *SSEStreamReader) SendHeartbeat() bool {
	return r.Send(sseHeartbeatFrame)
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
