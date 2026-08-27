package governance

// Baselines are the usage other cluster nodes have already accumulated but
// have not yet flushed to the database. Every enforcement decision adds them to
// this node's own in-memory counters, so that all nodes converge on the same
// cluster-wide total without any of them reading the others' counters directly.
//
// These are lookup interfaces rather than maps on purpose. The caller holding
// the data is free to keep it in whatever structure it likes, including one
// that is mutated concurrently as peer updates arrive, and callees here can
// only ask about the handful of IDs a request actually involves. Passing a map
// forced the holder to publish a fully materialised copy on every change, which
// costs one full copy per update at cluster scale.
//
// A nil value of either interface is not usable directly. Callers that may pass
// nil should normalise it first with BudgetBaselinesOrEmpty or
// RateLimitBaselinesOrEmpty.

// BudgetBaselines reports remote unflushed spend per budget ID.
type BudgetBaselines interface {
	// BudgetUsage returns the amount other nodes have spent against budgetID
	// since the last database flush, or zero if they have spent nothing.
	BudgetUsage(budgetID string) float64
}

// RateLimitBaselines reports remote unflushed token and request counts per
// rate limit ID. Both dimensions come from one interface because every caller
// that needs one needs the other, and they are always read from the same
// underlying snapshot.
type RateLimitBaselines interface {
	// TokenUsage returns the tokens other nodes have consumed against
	// rateLimitID since the last database flush.
	TokenUsage(rateLimitID string) int64
	// RequestUsage returns the requests other nodes have counted against
	// rateLimitID since the last database flush.
	RequestUsage(rateLimitID string) int64
}

// MapBudgetBaselines adapts a plain map to BudgetBaselines. It is the natural
// choice for tests and for single-node deployments, where the whole set is
// small and already materialised. The zero value (a nil map) reports zero for
// every ID, which is exactly the "no cluster peers" case.
type MapBudgetBaselines map[string]float64

// BudgetUsage implements BudgetBaselines.
func (m MapBudgetBaselines) BudgetUsage(budgetID string) float64 { return m[budgetID] }

// MapRateLimitBaselines adapts a pair of plain maps to RateLimitBaselines. As
// with MapBudgetBaselines, nil maps report zero for every ID.
type MapRateLimitBaselines struct {
	Tokens   map[string]int64
	Requests map[string]int64
}

// TokenUsage implements RateLimitBaselines.
func (m MapRateLimitBaselines) TokenUsage(rateLimitID string) int64 { return m.Tokens[rateLimitID] }

// RequestUsage implements RateLimitBaselines.
func (m MapRateLimitBaselines) RequestUsage(rateLimitID string) int64 {
	return m.Requests[rateLimitID]
}

// BudgetBaselinesOrEmpty returns baselines, or an empty set when it is nil, so
// that callers can be handed nil and still dereference safely. This replaces
// the previous "if baselines == nil { baselines = map[string]float64{} }" guard
// and, unlike it, allocates nothing.
func BudgetBaselinesOrEmpty(baselines BudgetBaselines) BudgetBaselines {
	if baselines == nil {
		return MapBudgetBaselines(nil)
	}
	return baselines
}

// RateLimitBaselinesOrEmpty mirrors BudgetBaselinesOrEmpty for rate limits.
func RateLimitBaselinesOrEmpty(baselines RateLimitBaselines) RateLimitBaselines {
	if baselines == nil {
		return MapRateLimitBaselines{}
	}
	return baselines
}
