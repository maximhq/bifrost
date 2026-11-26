package chaosmock

import (
	"net"
	"net/http"
	"sync"
)

// connTracker counts TCP connections against a fleet's listener.
//
// This is deliberately fleet-level rather than per-server. One listener serves
// every virtual server, so a connection cannot be attributed to one of them,
// and pretending otherwise would produce a number that looks precise and is
// wrong. A scenario that needs connection-level attribution runs its own fleet
// with a single server, which is cheap because a fleet binds an ephemeral port.
//
// What this exists for is the leak assertion: a client that reconnects N times
// should leave the open count where it started. A climbing Opened count against
// a flat Open count is a reconnect storm, which is a different fault from a
// leak and should not be mistaken for one.
type connTracker struct {
	mu     sync.Mutex
	opened int64
	closed int64
	open   int64
	maxOpn int64
	// active guards against double-counting: net/http reports StateClosed for
	// connections it has already reported as hijacked, and a naive counter
	// would go negative on those.
	active map[net.Conn]struct{}
}

func newConnTracker() *connTracker {
	return &connTracker{active: map[net.Conn]struct{}{}}
}

// ConnectionStats is a point-in-time view of a fleet's connection accounting.
type ConnectionStats struct {
	Opened int64 `json:"opened"` // connections accepted over the fleet's life
	Closed int64 `json:"closed"` // connections that have gone away
	Open   int64 `json:"open"`   // connections currently held
	Max    int64 `json:"max"`    // high-water mark of Open
}

// hook returns the ConnState callback to install on an http.Server. Only the
// New and Closed transitions are counted; the states in between describe what a
// connection is doing rather than whether it exists.
func (t *connTracker) hook() func(net.Conn, http.ConnState) {
	return func(c net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			t.mu.Lock()
			if _, seen := t.active[c]; !seen {
				t.active[c] = struct{}{}
				t.opened++
				t.open++
				if t.open > t.maxOpn {
					t.maxOpn = t.open
				}
			}
			t.mu.Unlock()
		case http.StateHijacked, http.StateClosed:
			t.mu.Lock()
			if _, seen := t.active[c]; seen {
				delete(t.active, c)
				t.closed++
				t.open--
			}
			t.mu.Unlock()
		}
	}
}

func (t *connTracker) stats() ConnectionStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return ConnectionStats{Opened: t.opened, Closed: t.closed, Open: t.open, Max: t.maxOpn}
}

func (t *connTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Open is a live fact rather than a tally, so it survives a reset and
	// seeds the new high-water mark.
	t.opened = t.open
	t.closed = 0
	t.maxOpn = t.open
}
