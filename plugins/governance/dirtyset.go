package governance

import "sync"

// FNV-1a constants for the stripe hash below.
const (
	dirtySetFNVOffset uint64 = 14695981039346656037
	dirtySetFNVPrime  uint64 = 1099511628211
)

// dirtySetShards is how many independently locked stripes a dirty set is split
// across. Marking happens on the request path, once per governed budget or rate
// limit per request, so the stripes exist to keep that off a single lock.
const dirtySetShards = 32

// dirtyKeySet records which governance IDs may have changed since the last
// drain, so that periodic work can look at those instead of walking every key
// in memory.
//
// "May have changed" is deliberate. A false positive costs one map lookup in
// the drain; a false negative would mean usage that never reaches other nodes
// until the next full sweep. So every path that mutates or removes an entry
// marks it, and nothing tries to be clever about whether the value really moved.
type dirtyKeySet struct {
	shards [dirtySetShards]struct {
		mu  sync.Mutex
		ids map[string]struct{}
	}
}

// shardFor returns the stripe owning id, using the same cheap FNV-1a mix the
// usage hashes use rather than pulling in a hashing dependency.
func (d *dirtyKeySet) shardFor(id string) int {
	h := dirtySetFNVOffset
	for i := range len(id) {
		h ^= uint64(id[i])
		h *= dirtySetFNVPrime
	}
	return int(h % dirtySetShards)
}

// mark records that id may have changed. Safe to call from many goroutines and
// cheap enough for the request path: one lock acquire and one map write.
func (d *dirtyKeySet) mark(id string) {
	if id == "" {
		return
	}
	shard := &d.shards[d.shardFor(id)]
	shard.mu.Lock()
	if shard.ids == nil {
		shard.ids = make(map[string]struct{})
	}
	shard.ids[id] = struct{}{}
	shard.mu.Unlock()
}

// markAll records several IDs at once.
func (d *dirtyKeySet) markAll(ids []string) {
	for _, id := range ids {
		d.mark(id)
	}
}

// drain returns everything marked since the last drain and clears the set.
//
// The caller owns what it takes. If it fails to act on the IDs it must mark
// them again, otherwise that change is invisible until the next full sweep.
// Each stripe's map is replaced rather than cleared so a burst of activity does
// not leave every stripe holding a permanently oversized map.
func (d *dirtyKeySet) drain() []string {
	var ids []string
	for i := range d.shards {
		shard := &d.shards[i]
		shard.mu.Lock()
		for id := range shard.ids {
			ids = append(ids, id)
		}
		shard.ids = nil
		shard.mu.Unlock()
	}
	return ids
}

// MarkBudgetDirty records that a budget's usage may have changed, so the next
// usage sync includes it. Exported for callers outside this package that mutate
// usage through their own paths.
func (gs *LocalGovernanceStore) MarkBudgetDirty(budgetID string) {
	gs.dirtyBudgets.mark(budgetID)
}

// MarkRateLimitDirty records that a rate limit's usage may have changed.
func (gs *LocalGovernanceStore) MarkRateLimitDirty(rateLimitID string) {
	gs.dirtyRateLimits.mark(rateLimitID)
}

// DrainDirtyBudgetIDs returns the budgets marked since the last drain and
// clears the set. An ID that is no longer in memory means the budget was
// deleted, which the caller should treat as a removal rather than skip.
func (gs *LocalGovernanceStore) DrainDirtyBudgetIDs() []string {
	return gs.dirtyBudgets.drain()
}

// DrainDirtyRateLimitIDs mirrors DrainDirtyBudgetIDs for rate limits.
func (gs *LocalGovernanceStore) DrainDirtyRateLimitIDs() []string {
	return gs.dirtyRateLimits.drain()
}

// RemarkDirtyBudgetIDs puts drained budget IDs back, for a caller whose send
// failed. Without this a failed sync would silently drop those changes.
func (gs *LocalGovernanceStore) RemarkDirtyBudgetIDs(ids []string) {
	gs.dirtyBudgets.markAll(ids)
}

// RemarkDirtyRateLimitIDs mirrors RemarkDirtyBudgetIDs for rate limits.
func (gs *LocalGovernanceStore) RemarkDirtyRateLimitIDs(ids []string) {
	gs.dirtyRateLimits.markAll(ids)
}
