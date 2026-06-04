package lib

import (
	"io"
	"sync"
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

// Done closes the event channel, signaling to Read that the stream is finished.
// Must be called exactly once by the producer goroutine when streaming is complete.
func (r *SSEStreamReader) Done() {
	close(r.eventCh)
}
