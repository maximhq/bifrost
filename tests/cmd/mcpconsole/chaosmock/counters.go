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
type CountersSnapshot struct {
	Requests    int64 `json:"requests"`    // MCP requests served, including ones later faulted
	Initializes int64 `json:"initializes"` // successful initialize handshakes
	Sessions    int64 `json:"sessions"`    // distinct session ids issued
	ToolsList   int64 `json:"tools_list"`  // tools/list requests answered, faulted or not
	Pings       int64 `json:"pings"`

	// Calls counts tools/call by resolved tool name.
	Calls map[string]int64 `json:"calls"`
	// Methods counts every JSON-RPC method seen, including ones this mock does
	// not implement, so an unexpected method surfaces rather than vanishing.
	Methods map[string]int64 `json:"methods"`
	// SessionRequests counts requests per session id, which distinguishes one
	// long-lived connection from many short ones without inspecting sockets.
	SessionRequests map[string]int64 `json:"session_requests"`
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

func newCounters() *Counters {
	return &Counters{d: newSnapshot()}
}

func newSnapshot() CountersSnapshot {
	return CountersSnapshot{
		Calls:           map[string]int64{},
		Methods:         map[string]int64{},
		SessionRequests: map[string]int64{},
	}
}

func (c *Counters) request() {
	c.mu.Lock()
	c.d.Requests++
	c.mu.Unlock()
}

func (c *Counters) initialize() {
	c.mu.Lock()
	c.d.Initializes++
	c.mu.Unlock()
}

func (c *Counters) session() {
	c.mu.Lock()
	c.d.Sessions++
	c.mu.Unlock()
}

func (c *Counters) method(m, sessionID string) {
	c.mu.Lock()
	c.d.Methods[m]++
	if sessionID != "" {
		c.d.SessionRequests[sessionID]++
	}
	c.mu.Unlock()
}

func (c *Counters) toolsList() {
	c.mu.Lock()
	c.d.ToolsList++
	c.mu.Unlock()
}

func (c *Counters) ping() {
	c.mu.Lock()
	c.d.Pings++
	c.mu.Unlock()
}

func (c *Counters) call(tool string) {
	c.mu.Lock()
	c.d.Calls[tool]++
	c.mu.Unlock()
}

// Snapshot returns a deep copy safe to hold while the server keeps running.
func (c *Counters) Snapshot() CountersSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.d
	out.Calls = copyCounts(c.d.Calls)
	out.Methods = copyCounts(c.d.Methods)
	out.SessionRequests = copyCounts(c.d.SessionRequests)
	return out
}

// Reset zeroes every tally. Scenarios call this at a phase boundary so an
// assertion reads "calls during this phase" rather than "calls ever".
func (c *Counters) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.d = newSnapshot()
}

func copyCounts(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
