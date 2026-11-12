package heimdall

import "sync"

// Budget caps what a run may spend. A cap of zero or less is off. Calls are counted when they are
// reserved, so the call cap is exact however many workers share the budget; the dollar cap is checked
// before a call and charged after it, so it can be passed by the calls already in flight.
type Budget struct {
	mu       sync.Mutex
	maxUSD   float64
	maxCalls int
	spent    float64
	calls    int
}

// NewBudget caps spend in US dollars and the number of model calls.
func NewBudget(maxUSD float64, maxCalls int) *Budget {
	return &Budget{maxUSD: maxUSD, maxCalls: maxCalls}
}

// Reserve takes one call from the budget. It reports false when either cap is reached.
func (b *Budget) Reserve() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxCalls > 0 && b.calls >= b.maxCalls {
		return false
	}
	if b.maxUSD > 0 && b.spent >= b.maxUSD {
		return false
	}
	b.calls++
	return true
}

// Add charges the cost of a finished call. A call that was never priced adds nothing, which is why the
// call cap exists.
func (b *Budget) Add(costUSD float64) {
	if costUSD <= 0 {
		return
	}
	b.mu.Lock()
	b.spent += costUSD
	b.mu.Unlock()
}

// Spent is the money charged so far.
func (b *Budget) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// Calls is the number of calls reserved so far.
func (b *Budget) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}
