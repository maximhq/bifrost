package chaosmock

import "sync"

// streamRegistry tracks the open server-to-client channels a virtual server
// holds. It exists for two reasons that pull in opposite directions and both
// matter:
//
//   - It is how the mock pushes an unsolicited notification, which is the only
//     way to find out whether a client listens on a channel it opened.
//   - It is how the mock drops a stream out from under a client without
//     closing anything else, which is a different failure from the process
//     going away and should be distinguishable as such.
//
// A stream is identified by the channel it is closed through, so a caller that
// registered one can always deregister exactly that one.
type streamRegistry struct {
	mu      sync.Mutex
	streams map[chan []byte]struct{}
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{streams: map[chan []byte]struct{}{}}
}

// buffer is deliberately small but non-zero: a slow reader should not block a
// broadcast, but a stream that has fallen far behind is better dropped than
// grown without bound, and the senders below do exactly that.
const streamBuffer = 8

// add registers a stream and returns the channel writes arrive on.
func (r *streamRegistry) add() chan []byte {
	ch := make(chan []byte, streamBuffer)
	r.mu.Lock()
	r.streams[ch] = struct{}{}
	r.mu.Unlock()
	return ch
}

// remove deregisters a stream. It does not close the channel: the goroutine
// serving the stream owns that, and closing from here would race it.
func (r *streamRegistry) remove(ch chan []byte) {
	r.mu.Lock()
	delete(r.streams, ch)
	r.mu.Unlock()
}

// broadcast delivers payload to every open stream, skipping any whose buffer
// is full. Skipping rather than blocking is the right call
// for a mock: a test that wedges one reader should still be able to observe
// the others, and a dropped notification to a stuck client is what a real
// server would do too. It returns how many streams accepted the payload.
func (r *streamRegistry) broadcast(payload []byte) int {
	r.mu.Lock()
	targets := make([]chan []byte, 0, len(r.streams))
	for ch := range r.streams {
		targets = append(targets, ch)
	}
	r.mu.Unlock()

	delivered := 0
	for _, ch := range targets {
		select {
		case ch <- payload:
			delivered++
		default:
		}
	}
	return delivered
}

// closeAll signals every open stream to end by closing its channel, and
// forgets them. The serving goroutines observe the close and return, which
// unwinds the HTTP handler and drops the connection.
func (r *streamRegistry) closeAll() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.streams)
	for ch := range r.streams {
		close(ch)
	}
	r.streams = map[chan []byte]struct{}{}
	return n
}

func (r *streamRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams)
}
