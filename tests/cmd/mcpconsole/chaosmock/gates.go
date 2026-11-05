package chaosmock

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// A gate is a barrier for races. It names an MCP method, narrowed to one
// caller or not, or the OAuth token endpoint, and holds every request it
// matches until a scenario opens it. That is how a test puts two requests in a
// known order, such as one caller's discovery landing while another's token
// refresh is still in flight, instead of hoping the scheduler does.
//
// A held request has already arrived: it is counted in methods (or in the
// OAuth token counters) before the gate, and answered after it, against the
// server as it is when the gate lets it go. A credential revoked while a
// request is held is therefore rejected once it is released, which is the
// point of holding it.
//
// Opening a gate releases what it holds and lets every later request straight
// through. Every gate also has a safety timeout, after which it lets a held
// request through and records it as timed out, so a scenario that forgets to
// open a gate fails slowly rather than hanging. A request whose client hangs
// up while held is dropped unanswered and recorded as abandoned.

// DefaultGateTimeout is how long a gate holds a request when its own timeout
// is not set.
const DefaultGateTimeout = 20 * time.Second

// GateEndpointToken is the endpoint name of the OAuth token endpoint, the one
// endpoint a gate can hold.
const GateEndpointToken = "token"

// Gate is what a gate holds. Exactly one of Method and Endpoint is set.
type Gate struct {
	Name     string   `json:"name"`
	Method   string   `json:"method,omitempty"`   // an MCP method, e.g. tools/list
	Identity string   `json:"identity,omitempty"` // narrows a method gate to one caller
	Endpoint string   `json:"endpoint,omitempty"` // "token" for the OAuth token endpoint
	Timeout  Duration `json:"timeout,omitempty"`  // safety timeout, 20s when unset
}

// Validate rejects a gate that could never hold anything, or one with a field
// it would ignore.
func (g Gate) Validate() error {
	switch {
	case g.Name == "":
		return fmt.Errorf("every gate needs a name")
	case g.Method == "" && g.Endpoint == "":
		return fmt.Errorf("gate %q needs a method or an endpoint to hold", g.Name)
	case g.Method != "" && g.Endpoint != "":
		return fmt.Errorf("gate %q holds a method or an endpoint, not both", g.Name)
	case g.Endpoint != "" && g.Endpoint != GateEndpointToken:
		return fmt.Errorf("gate %q: the only endpoint a gate holds is %q", g.Name, GateEndpointToken)
	case g.Endpoint != "" && g.Identity != "":
		return fmt.Errorf("gate %q: identity narrows a method gate only", g.Name)
	case g.Timeout < 0:
		return fmt.Errorf("gate %q: timeout cannot be negative", g.Name)
	}
	return nil
}

// GateState is a gate and what it has held so far. Passed counts requests let
// through because the gate was or became open, TimedOut those let through by
// the safety timeout, and Abandoned those whose client gave up while held.
type GateState struct {
	Gate
	Open      bool  `json:"open"`
	Waiting   int64 `json:"waiting"`
	Passed    int64 `json:"passed"`
	TimedOut  int64 `json:"timed_out"`
	Abandoned int64 `json:"abandoned"`
}

// gate is one live gate. open is closed exactly once, when the gate opens or
// is removed; a gate set again under the same name is a new gate.
type gate struct {
	spec   Gate
	open   chan struct{}
	isOpen bool

	waiting, passed, timedOut, abandoned int64
}

// release opens g. Callers hold the set's lock.
func (g *gate) release() {
	if !g.isOpen {
		g.isOpen = true
		close(g.open)
	}
}

// gateSet is a server's gates, keyed by name.
type gateSet struct {
	mu    sync.Mutex
	gates map[string]*gate
}

func newGateSet() *gateSet {
	return &gateSet{gates: map[string]*gate{}}
}

// set adds a closed gate, replacing one with the same name and releasing
// whatever that one held.
func (gs *gateSet) set(spec Gate) {
	if spec.Timeout == 0 {
		spec.Timeout = Duration(DefaultGateTimeout)
	}
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if old, ok := gs.gates[spec.Name]; ok {
		old.release()
	}
	gs.gates[spec.Name] = &gate{spec: spec, open: make(chan struct{})}
}

// openGate opens the named gate, or every closed gate when name is empty. It
// refuses to do nothing: an unknown name, a gate already open, or no closed
// gate at all is a scenario opening the wrong thing.
func (gs *gateSet) openGate(name string) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if name == "" {
		opened := 0
		for _, g := range gs.gates {
			if !g.isOpen {
				g.release()
				opened++
			}
		}
		if opened == 0 {
			return fmt.Errorf("no gate is closed")
		}
		return nil
	}
	g, ok := gs.gates[name]
	switch {
	case !ok:
		return fmt.Errorf("no gate %q", name)
	case g.isOpen:
		return fmt.Errorf("gate %q is already open", name)
	}
	g.release()
	return nil
}

// remove deletes the named gate, or every gate when name is empty, releasing
// whatever it holds. Removing nothing is refused.
func (gs *gateSet) remove(name string) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if name == "" {
		if len(gs.gates) == 0 {
			return fmt.Errorf("no gates to clear")
		}
		for _, g := range gs.gates {
			g.release()
		}
		gs.gates = map[string]*gate{}
		return nil
	}
	g, ok := gs.gates[name]
	if !ok {
		return fmt.Errorf("no gate %q", name)
	}
	g.release()
	delete(gs.gates, name)
	return nil
}

// holdMethod holds an MCP request for method from identity at every gate that
// matches it, and reports whether the request may go on.
func (gs *gateSet) holdMethod(ctx context.Context, method, identity string) bool {
	return gs.hold(ctx, func(g Gate) bool {
		return g.Method != "" && g.Method == method && (g.Identity == "" || g.Identity == identity)
	})
}

// holdEndpoint holds a request to an OAuth endpoint at every gate that names
// it, and reports whether the request may go on.
func (gs *gateSet) holdEndpoint(ctx context.Context, endpoint string) bool {
	return gs.hold(ctx, func(g Gate) bool { return g.Endpoint == endpoint })
}

// hold waits at each matching gate in name order. False means the request's
// client gave up while held, so nothing should answer it.
func (gs *gateSet) hold(ctx context.Context, matches func(Gate) bool) bool {
	gs.mu.Lock()
	var held []*gate
	for _, g := range gs.gates {
		if matches(g.spec) {
			held = append(held, g)
		}
	}
	gs.mu.Unlock()
	sort.Slice(held, func(i, j int) bool { return held[i].spec.Name < held[j].spec.Name })
	for _, g := range held {
		if !gs.wait(ctx, g) {
			return false
		}
	}
	return true
}

// wait passes one gate: at once when it is open, else when it opens, its
// timeout runs out, or the client gives up, whichever comes first.
func (gs *gateSet) wait(ctx context.Context, g *gate) bool {
	gs.mu.Lock()
	if g.isOpen {
		g.passed++
		gs.mu.Unlock()
		return true
	}
	g.waiting++
	timeout := time.Duration(g.spec.Timeout)
	gs.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var outcome *int64
	proceed := true
	select {
	case <-g.open:
		outcome = &g.passed
	case <-timer.C:
		outcome = &g.timedOut
	case <-ctx.Done():
		outcome, proceed = &g.abandoned, false
	}

	gs.mu.Lock()
	g.waiting--
	*outcome++
	gs.mu.Unlock()
	return proceed
}

// snapshot returns every gate's state sorted by name, nil when there are none.
func (gs *gateSet) snapshot() []GateState {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if len(gs.gates) == 0 {
		return nil
	}
	out := make([]GateState, 0, len(gs.gates))
	for _, g := range gs.gates {
		out = append(out, GateState{
			Gate: g.spec, Open: g.isOpen,
			Waiting: g.waiting, Passed: g.passed, TimedOut: g.timedOut, Abandoned: g.abandoned,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
