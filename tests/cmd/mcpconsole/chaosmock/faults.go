package chaosmock

import (
	"math/rand"
	"sync"
	"time"
)

// Faults is the behaviour a virtual server is currently exhibiting. Every field
// is independently settable so a scenario can combine them: a server can be
// slow AND intermittently 429 AND refuse ping, which is what a degrading
// upstream actually looks like.
//
// The zero value is a healthy server. Fields are grouped by the layer they
// break at, because that grouping is exactly what a client should attribute a
// failure to, and attribution is one of the things under test.
type Faults struct {
	// Transport layer: the connection itself never carries a usable response.
	Refuse  bool `json:"refuse,omitempty"`   // accept then immediately close, so a dial fails
	Reset   bool `json:"reset,omitempty"`    // hijack and close mid-response, producing a truncated read
	DropSSE bool `json:"drop_sse,omitempty"` // close any open SSE stream at the next write

	// HTTP layer: a well-formed HTTP response that is not a JSON-RPC one.
	Status     int    `json:"status,omitempty"`      // answer every request with this status
	StatusOnce bool   `json:"status_once,omitempty"` // clear Status after it fires once
	RetryAfter string `json:"retry_after,omitempty"` // Retry-After header, paired with 429/503
	WWWAuth    string `json:"www_auth,omitempty"`    // WWW-Authenticate header, paired with 401

	// Protocol layer: valid HTTP, but the MCP exchange is wrong.
	InitFail    bool   `json:"init_fail,omitempty"`    // initialize returns a JSON-RPC error
	BadVersion  string `json:"bad_version,omitempty"`  // advertise this protocolVersion instead
	NoPing      bool   `json:"no_ping,omitempty"`      // ping answers method-not-found
	PingFail    bool   `json:"ping_fail,omitempty"`    // ping answers an internal error
	ListFail    bool   `json:"list_fail,omitempty"`    // tools/list answers an error
	KillSession bool   `json:"kill_session,omitempty"` // every known session is invalid, so requests 404

	// Timing. Hang is applied before answering and is what a client's own
	// timeout should cut short; Latency plus Jitter model an upstream that is
	// merely slow. Kept separate because "slow" and "stuck" must be
	// distinguishable in the results.
	Hang    Duration `json:"hang,omitempty"`
	Latency Duration `json:"latency,omitempty"`
	Jitter  Duration `json:"jitter,omitempty"`

	// Call layer: tools/call specifically.
	ErrRate   float64 `json:"err_rate,omitempty"`  // probability in [0,1] that a call fails
	ErrKind   ErrKind `json:"err_kind,omitempty"`  // how it fails when it does
	Malformed bool    `json:"malformed,omitempty"` // answer with a body that is not valid JSON-RPC
}

// ErrKind selects the shape of an injected tool-call failure. These are not
// interchangeable: a tool-level error is a successful MCP exchange reporting a
// failed tool, while a protocol error is a failed exchange. A client that
// conflates them will look correct under one and wrong under the other.
type ErrKind string

const (
	// ErrKindTool returns a normal result with isError true. The MCP call
	// succeeded; the tool did not.
	ErrKindTool ErrKind = "tool"
	// ErrKindProtocol returns a JSON-RPC error object.
	ErrKindProtocol ErrKind = "protocol"
	// ErrKindHTTP returns a bare HTTP 500 with no JSON-RPC body at all.
	ErrKindHTTP ErrKind = "http"
)

// Duration is a time.Duration that round-trips through JSON as a string, so
// control-API payloads read as "1500ms" rather than a nanosecond count.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" || s == "null" || s == "0" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// faultState holds a server's live fault set behind a lock. Faults are read on
// every request and written by the control API at arbitrary times, so the
// snapshot a request works from must be a copy: a scenario that flips a fault
// halfway through a slow request should not change how that request finishes.
type faultState struct {
	mu sync.Mutex
	f  Faults
	// rng is per-server and seeded once, so an ErrRate run is reproducible
	// within a process rather than depending on global rand scheduling.
	rng *rand.Rand
}

func newFaultState() *faultState {
	return &faultState{rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// snapshot returns a copy of the current faults, and clears any one-shot
// status so the next request sees a healthy server again.
func (fs *faultState) snapshot() Faults {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f := fs.f
	if fs.f.StatusOnce && fs.f.Status != 0 {
		fs.f.Status = 0
		fs.f.StatusOnce = false
	}
	// DropSSE is edge-triggered: it closes the streams open when it was set,
	// not every stream opened forever after.
	if fs.f.DropSSE {
		fs.f.DropSSE = false
	}
	return f
}

// peek returns the current faults without consuming one-shot state. Used by
// the control API's readback, which must not itself be a request.
func (fs *faultState) peek() Faults {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.f
}

// set replaces the fault set wholesale.
func (fs *faultState) set(f Faults) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.f = f
}

// update applies a mutation under the lock, so read-modify-write from the
// control API cannot interleave with a request's snapshot.
func (fs *faultState) update(fn func(*Faults)) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fn(&fs.f)
}

// clear returns the server to healthy.
func (fs *faultState) clear() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.f = Faults{}
}

// shouldFail rolls against ErrRate.
func (fs *faultState) shouldFail(rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.rng.Float64() < rate
}

// delay returns the pause to apply before answering: the base latency plus a
// symmetric jitter, so the mean stays at Latency and a scenario that sets only
// jitter does not also slow the server down.
func (fs *faultState) delay(f Faults) time.Duration {
	base := time.Duration(f.Latency)
	jit := time.Duration(f.Jitter)
	if jit <= 0 {
		return base
	}
	fs.mu.Lock()
	offset := time.Duration(fs.rng.Int63n(int64(jit))) - jit/2
	fs.mu.Unlock()
	d := base + offset
	if d < 0 {
		return 0
	}
	return d
}
