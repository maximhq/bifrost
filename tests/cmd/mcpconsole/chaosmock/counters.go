package chaosmock

import (
	"sort"
	"sync"
)

// CountersSnapshot is a point-in-time copy of a server's counters, safe to
// serialize or compare while the server keeps running. It is a separate type
// from the live Counters rather than a copy of it because the live one carries
// a lock, and handing a copied lock to a caller is a bug waiting for a
// concurrent reader.
//
// Two of these fields are load-bearing in ways the others are not:
//
//   - Calls is keyed by the tool name the server resolved the request to, not
//     by the name the caller sent. A client that maps a caller-facing name back
//     to the wrong upstream tool produces a correct-looking response and a
//     wrong counter, and nothing else catches that.
//   - OpenStreams is the stream-level leak signal. TCP-level leak detection
//     lives on the Fleet instead (see Fleet.Connections), because one listener
//     serves every virtual server and a connection cannot be attributed to one
//     of them.
type CountersSnapshot struct {
	Requests    int64 `json:"requests"`    // MCP requests served, including ones later faulted
	Initializes int64 `json:"initializes"` // successful initialize handshakes
	Sessions    int64 `json:"sessions"`    // distinct session ids issued
	ToolsList   int64 `json:"tools_list"`  // tools/list requests answered, faulted or not
	Pings       int64 `json:"pings"`
	OpenStreams int64 `json:"open_streams"` // server-to-client streams currently held open
	MaxStreams  int64 `json:"max_streams"`  // high-water mark of the above
	Faulted     int64 `json:"faulted"`      // requests that returned an injected failure

	// Calls counts tools/call by resolved tool name.
	Calls map[string]int64 `json:"calls"`
	// CallsByIdentity counts tools/call by the identity that made them, which
	// is how "revoking user A did not affect user B" becomes checkable.
	CallsByIdentity map[string]int64 `json:"calls_by_identity"`
	// Methods counts every JSON-RPC method seen, including ones this mock does
	// not implement, so an unexpected method surfaces rather than vanishing.
	Methods map[string]int64 `json:"methods"`
	// SessionRequests counts requests per session id, which distinguishes one
	// long-lived connection from many short ones without inspecting sockets.
	SessionRequests map[string]int64 `json:"session_requests"`
	// Handshakes lists every negotiation the server answered, in order, with the version the
	// client asked for and the one it got, which is how a version cap becomes checkable.
	Handshakes []Handshake `json:"handshakes"`
}

// TotalCalls sums Calls across every tool.
func (c CountersSnapshot) TotalCalls() int64 {
	var n int64
	for _, v := range c.Calls {
		n += v
	}
	return n
}

// CalledTools lists the tools called at least once, sorted, which is the
// readable form of a dispatch assertion.
func (c CountersSnapshot) CalledTools() []string {
	out := make([]string, 0, len(c.Calls))
	for k := range c.Calls {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Counters is the live, concurrently-updated counter set for one server.
type Counters struct {
	mu sync.Mutex
	d  CountersSnapshot
}

// newCounters starts a server's counters at zero.
func newCounters() *Counters {
	return &Counters{d: newSnapshot()}
}

// newSnapshot is an all-zero snapshot with its maps and lists allocated.
func newSnapshot() CountersSnapshot {
	return CountersSnapshot{
		Calls:           map[string]int64{},
		CallsByIdentity: map[string]int64{},
		Methods:         map[string]int64{},
		SessionRequests: map[string]int64{},
		Handshakes:      []Handshake{},
	}
}

// request counts one request received.
func (c *Counters) request() {
	c.mu.Lock()
	c.d.Requests++
	c.mu.Unlock()
}

// initialize counts one successful initialize.
func (c *Counters) initialize() {
	c.mu.Lock()
	c.d.Initializes++
	c.mu.Unlock()
}

// session counts one session issued.
func (c *Counters) session() {
	c.mu.Lock()
	c.d.Sessions++
	c.mu.Unlock()
}

// method counts one JSON-RPC method, and the request against its session when it has one.
func (c *Counters) method(m, sessionID string) {
	c.mu.Lock()
	c.d.Methods[m]++
	if sessionID != "" {
		c.d.SessionRequests[sessionID]++
	}
	c.mu.Unlock()
}

// handshake records one answered negotiation.
func (c *Counters) handshake(h Handshake) {
	c.mu.Lock()
	c.d.Handshakes = append(c.d.Handshakes, h)
	c.mu.Unlock()
}

// toolsList counts one tools/list answered.
func (c *Counters) toolsList() {
	c.mu.Lock()
	c.d.ToolsList++
	c.mu.Unlock()
}

// ping counts one ping answered.
func (c *Counters) ping() {
	c.mu.Lock()
	c.d.Pings++
	c.mu.Unlock()
}

// call counts one tools/call by the tool it resolved to and the identity that made it.
func (c *Counters) call(tool, identity string) {
	c.mu.Lock()
	c.d.Calls[tool]++
	if identity != "" {
		c.d.CallsByIdentity[identity]++
	}
	c.mu.Unlock()
}

// faulted counts one injected failure.
func (c *Counters) faulted() {
	c.mu.Lock()
	c.d.Faulted++
	c.mu.Unlock()
}

// streamOpened counts a server-to-client stream opening and raises the high-water mark.
func (c *Counters) streamOpened() {
	c.mu.Lock()
	c.d.OpenStreams++
	if c.d.OpenStreams > c.d.MaxStreams {
		c.d.MaxStreams = c.d.OpenStreams
	}
	c.mu.Unlock()
}

// streamClosed counts a server-to-client stream closing.
func (c *Counters) streamClosed() {
	c.mu.Lock()
	if c.d.OpenStreams > 0 {
		c.d.OpenStreams--
	}
	c.mu.Unlock()
}

// Snapshot returns a deep copy safe to hold while the server keeps running.
func (c *Counters) Snapshot() CountersSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.d
	out.Calls = copyCounts(c.d.Calls)
	out.CallsByIdentity = copyCounts(c.d.CallsByIdentity)
	out.Methods = copyCounts(c.d.Methods)
	out.SessionRequests = copyCounts(c.d.SessionRequests)
	out.Handshakes = append([]Handshake{}, c.d.Handshakes...)
	return out
}

// Reset zeroes every tally. Scenarios call this at a phase boundary so an
// assertion reads "calls during this phase" rather than "calls ever".
func (c *Counters) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	open := c.d.OpenStreams
	c.d = newSnapshot()
	// Open streams are a live fact about the world rather than a tally, so
	// resetting counts must not claim the streams went away.
	c.d.OpenStreams = open
	c.d.MaxStreams = open
}

// copyCounts copies a tally map.
func copyCounts(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
