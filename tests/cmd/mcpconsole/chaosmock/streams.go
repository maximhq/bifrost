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
// Streams come in two shapes because the two transports need different things.
// The streamable HTTP listening stream is anonymous: it carries only
// server-initiated messages, and nothing needs to address it individually. An
// SSE stream is keyed by session, because under that transport a request's
// response comes back out-of-band on the stream rather than in the reply to
// the POST that carried it, so the server has to be able to reach exactly one.
type streamRegistry struct {
	mu sync.Mutex
	// anon holds streams nothing addresses individually.
	anon map[chan []byte]struct{}
	// keyed holds streams addressable by session id.
	keyed map[string]chan []byte
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{
		anon:  map[chan []byte]struct{}{},
		keyed: map[string]chan []byte{},
	}
}

// buffer is deliberately small but non-zero: a slow reader should not block a
// broadcast, but a stream that has fallen far behind is better dropped than
// grown without bound, and the senders below do exactly that.
const streamBuffer = 8

// add registers an anonymous stream and returns the channel writes arrive on.
func (r *streamRegistry) add() chan []byte {
	ch := make(chan []byte, streamBuffer)
	r.mu.Lock()
	r.anon[ch] = struct{}{}
	r.mu.Unlock()
	return ch
}

// addKeyed registers a stream addressable by id, replacing any stream already
// held under that id. Replacing rather than refusing matches what a client
// reconnecting with the same session would expect, and the displaced stream is
// closed so its serving goroutine unwinds instead of leaking.
func (r *streamRegistry) addKeyed(id string) chan []byte {
	ch := make(chan []byte, streamBuffer)
	r.mu.Lock()
	if existing, ok := r.keyed[id]; ok {
		close(existing)
	}
	r.keyed[id] = ch
	r.mu.Unlock()
	return ch
}

// remove deregisters a stream, anonymous or keyed. It does not close the
// channel: the goroutine serving the stream owns that, and closing from here
// would race it.
func (r *streamRegistry) remove(ch chan []byte) {
	r.mu.Lock()
	delete(r.anon, ch)
	for id, held := range r.keyed {
		if held == ch {
			delete(r.keyed, id)
		}
	}
	r.mu.Unlock()
}

// send delivers payload to one keyed stream. It reports false when no such
// stream is held or its buffer is full, which the caller surfaces rather than
// swallowing: under SSE that means the response had nowhere to go.
func (r *streamRegistry) send(id string, payload []byte) bool {
	r.mu.Lock()
	ch, ok := r.keyed[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- payload:
		return true
	default:
		return false
	}
}

// broadcast delivers payload to every open stream of either shape, skipping
// any whose buffer is full. Skipping rather than blocking is the right call
// for a mock: a test that wedges one reader should still be able to observe
// the others, and a dropped notification to a stuck client is what a real
// server would do too. It returns how many streams accepted the payload.
func (r *streamRegistry) broadcast(payload []byte) int {
	r.mu.Lock()
	targets := make([]chan []byte, 0, len(r.anon)+len(r.keyed))
	for ch := range r.anon {
		targets = append(targets, ch)
	}
	for _, ch := range r.keyed {
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
	n := len(r.anon) + len(r.keyed)
	for ch := range r.anon {
		close(ch)
	}
	for _, ch := range r.keyed {
		close(ch)
	}
	r.anon = map[chan []byte]struct{}{}
	r.keyed = map[string]chan []byte{}
	return n
}

func (r *streamRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.anon) + len(r.keyed)
}
