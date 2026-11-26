package governance

import (
	"math"
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
)

// LimitState is one budget's or rate limit's live usage, kept apart from its settings. The store
// holds it behind a pointer and never changes a stored state: every change stores a new one with a
// compare-and-swap, so a reader holding a state sees one consistent moment of it, its usage paired
// with the baseline that usage is counted from.
//
// A budget uses slot 0 of each pair. A rate limit uses slot 0 for its token counter and slot 1 for
// its request counter.
//
// LastDBUsage is that baseline: only usage added after it is unsaved, and the dump writes Usage
// minus LastDBUsage, so every reset clears it with the usage. ResetGen moves whenever a budget's
// usage is cleared, and a dump reads it with the usage and compares it again before writing, so a
// row read before a reset is never written after it. ResetEpoch only moves forward, and a calendar
// adoption that moves LastReset leaves it alone, so the dump never writes a row read before a newer
// clear over it.
//
// Override is state rather than a setting: it is taken from the budget's row whenever the row is
// read, a state update (UpdateBudgetState) can replace it, the reset path spends its cycles, and
// the dump writes it back.
type LimitState struct {
	Usage       [2]Count          // dollars for a budget; tokens and requests for a rate limit
	LastReset   [2]int64          // each counter's window start in Unix nanoseconds, 0 for the zero time
	ResetLoc    [2]*time.Location // each LastReset's location, nil for local; a budget's slot 1 is anchorLocSlot
	LastDBUsage [2]Count          // usage last persisted or applied as a baseline
	ResetGen    uint64            // moves whenever a budget's usage is cleared
	ResetEpoch  [2]int64          // each counter's last window-reset clear in Unix nanoseconds, 0 when unknown
	Override    BudgetOverride    // a budget's override grant; zero for a rate limit
}

// Count is one usage counter of a LimitState: a budget's dollars as the bits of a float64, a rate
// limit's tokens or requests as an int64. A rate-limit count stays exact past 2^53, where a float64
// would round it, and a pair of counters takes 16 bytes either way. It is a struct so a counter is
// only read and written through its accessors: arithmetic or ++ on the raw bits does not compile.
// The zero Count is zero in both readings.
type Count struct{ bits uint64 }

// DollarCount is a budget counter holding v dollars.
func DollarCount(v float64) Count { return Count{math.Float64bits(v)} }

// WholeCount is a rate-limit counter holding n tokens or requests.
func WholeCount(n int64) Count { return Count{uint64(n)} }

// Dollars reads a budget counter.
func (c Count) Dollars() float64 { return math.Float64frombits(c.bits) }

// Whole reads a rate-limit counter.
func (c Count) Whole() int64 { return int64(c.bits) }

// BudgetOverride is a budget's override grant: an amount added to its limit, forever or for a
// number of reset windows counted from an anchor reset. CyclesRemaining is derived from the others
// and the budget's last reset whenever the budget is read.
type BudgetOverride struct {
	Amount          float64
	Mode            configstoreTables.BudgetOverrideMode
	CyclesRemaining int
	CyclesTotal     int
	AnchorReset     int64 // Unix nanoseconds, 0 for none; its location is the state's ResetLoc[anchorLocSlot]
}

// setOverride takes a budget's override grant from its row, keeping the anchor reset's location.
func (st *LimitState) setOverride(row *configstoreTables.TableBudget) {
	st.Override = BudgetOverride{
		Amount: row.OverrideAmount, Mode: row.OverrideMode, CyclesRemaining: row.OverrideCyclesRemaining,
		CyclesTotal: row.OverrideCyclesTotal, AnchorReset: anchorNanos(row.OverrideAnchorReset),
	}
	st.ResetLoc[anchorLocSlot] = nil
	if row.OverrideAnchorReset != nil {
		st.ResetLoc[anchorLocSlot] = locationOf(*row.OverrideAnchorReset)
	}
}

// anchorReset is a budget's override anchor reset as callers read it: nil for none.
func (st *LimitState) anchorReset() *time.Time {
	if st.Override.AnchorReset == 0 {
		return nil
	}
	t := timeOfNanos(st.Override.AnchorReset, st.ResetLoc[anchorLocSlot])
	return &t
}

// anchorNanos encodes an override's anchor reset for BudgetOverride: 0 when there is none.
func anchorNanos(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return unixNanos(*t)
}

// The slots of a LimitState pair: a budget's dollars, and a rate limit's tokens and requests.
const (
	BudgetSlot  = 0
	TokenSlot   = 0
	RequestSlot = 1
	// anchorLocSlot is the ResetLoc slot of a budget's override anchor: a budget leaves slot 1 of
	// its counters unused.
	anchorLocSlot = 1
)

// lastResetAt is a counter's window start as callers read it: the stored instant in the location
// it was given in.
func (st *LimitState) lastResetAt(slot int) time.Time {
	return timeOfNanos(st.LastReset[slot], st.ResetLoc[slot])
}

// setLastReset stores t as a counter's window start, keeping t's location, so the start reads back
// exactly as it was set: a calendar boundary in UTC stays UTC and a row's time keeps the zone the
// database gave it.
func (st *LimitState) setLastReset(slot int, t time.Time) {
	st.LastReset[slot] = unixNanos(t)
	st.ResetLoc[slot] = locationOf(t)
}

// locationOf is the location a ResetLoc slot keeps for t: nil for the zero time, which reads back
// as the zero time whatever its location.
func locationOf(t time.Time) *time.Location {
	if t.IsZero() {
		return nil
	}
	return t.Location()
}

// unixNanos encodes t for LimitState.LastReset. The zero time is 0: time.Time{}.UnixNano() is
// outside int64 and would read back as a different instant.
func unixNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// timeOfNanos decodes a LimitState.LastReset value: 0 is the zero time, anything else that instant
// in loc, or in the local zone, as time.Unix reads it, when loc is nil.
func timeOfNanos(n int64, loc *time.Location) time.Time {
	if n == 0 {
		return time.Time{}
	}
	t := time.Unix(0, n)
	if loc != nil {
		t = t.In(loc)
	}
	return t
}

// budgetView is a budget as callers read it: its settings with the usage, last reset and override
// of st, and its override count derived for that last reset. It is a value, so a caller on the
// request path keeps it on its stack.
func budgetView(settings *configstoreTables.TableBudget, st *LimitState) configstoreTables.TableBudget {
	v := *settings
	v.CurrentUsage = st.Usage[BudgetSlot].Dollars()
	v.LastReset = st.lastResetAt(BudgetSlot)
	v.UsageResetEpoch = st.ResetEpoch[BudgetSlot]
	v.OverrideAmount, v.OverrideMode = st.Override.Amount, st.Override.Mode
	v.OverrideCyclesRemaining, v.OverrideCyclesTotal = st.Override.CyclesRemaining, st.Override.CyclesTotal
	v.OverrideAnchorReset = st.anchorReset()
	v.RefreshOverrideCyclesRemaining()
	return v
}

// rateLimitView is a rate limit as callers read it: its settings with the token and request usage
// and last resets of st.
func rateLimitView(settings *configstoreTables.TableRateLimit, st *LimitState) configstoreTables.TableRateLimit {
	v := *settings
	v.TokenCurrentUsage = st.Usage[TokenSlot].Whole()
	v.TokenLastReset = st.lastResetAt(TokenSlot)
	v.RequestCurrentUsage = st.Usage[RequestSlot].Whole()
	v.RequestLastReset = st.lastResetAt(RequestSlot)
	v.TokenUsageResetEpoch = st.ResetEpoch[TokenSlot]
	v.RequestUsageResetEpoch = st.ResetEpoch[RequestSlot]
	return v
}

// budgetSettings returns a budget's settings, its record's row in the live store. The pointer is
// shared, so a caller copies it before changing it.
func (gs *LocalGovernanceStore) budgetSettings(id string) (*configstoreTables.TableBudget, bool) {
	rec, ok := gs.live.Collections.Budgets.Get(id)
	if !ok {
		return nil, false
	}
	return &rec.TableBudget, true
}

// rateLimitSettings is budgetSettings for rate limits.
func (gs *LocalGovernanceStore) rateLimitSettings(id string) (*configstoreTables.TableRateLimit, bool) {
	rec, ok := gs.live.Collections.RateLimits.Get(id)
	if !ok {
		return nil, false
	}
	return &rec.TableRateLimit, true
}

// budgetAt reads a budget's settings and state. ok is false when either is missing: a budget is
// held once it has both, since a state alone has nothing to be checked against and settings alone
// have no usage to check. Checks and charges both read a budget through it, so a budget whose record
// is published but whose state is not seeded yet applies to neither until the seed.
func (gs *LocalGovernanceStore) budgetAt(id string) (*configstoreTables.TableBudget, *LimitState, bool) {
	st, ok := gs.budgetUsage.Get(id)
	if !ok {
		return nil, nil, false
	}
	settings, ok := gs.budgetSettings(id)
	if !ok {
		return nil, nil, false
	}
	return settings, st, true
}

// rateLimitAt is budgetAt for rate limits.
func (gs *LocalGovernanceStore) rateLimitAt(id string) (*configstoreTables.TableRateLimit, *LimitState, bool) {
	st, ok := gs.rateLimitUsage.Get(id)
	if !ok {
		return nil, nil, false
	}
	settings, ok := gs.rateLimitSettings(id)
	if !ok {
		return nil, nil, false
	}
	return settings, st, true
}

// changeBudget stores fn's next state for a held budget, retrying against whatever another writer
// stored first, and returns the budget's settings with the state it stored. fn sees the settings
// and the current state and may run more than once; returning false stores nothing. ok is false
// when the budget is not held or fn declined.
func (gs *LocalGovernanceStore) changeBudget(id string, fn func(settings *configstoreTables.TableBudget, cur LimitState) (LimitState, bool)) (*configstoreTables.TableBudget, *LimitState, bool) {
	for {
		settings, cur, ok := gs.budgetAt(id)
		if !ok {
			return nil, nil, false
		}
		next, apply := fn(settings, *cur)
		if !apply {
			return nil, nil, false
		}
		if livestore.CompareAndSwap(gs.budgetUsage, id, cur, &next) {
			return settings, &next, true
		}
	}
}

// changeRateLimit is changeBudget for rate limits.
func (gs *LocalGovernanceStore) changeRateLimit(id string, fn func(settings *configstoreTables.TableRateLimit, cur LimitState) (LimitState, bool)) (*configstoreTables.TableRateLimit, *LimitState, bool) {
	for {
		settings, cur, ok := gs.rateLimitAt(id)
		if !ok {
			return nil, nil, false
		}
		next, apply := fn(settings, *cur)
		if !apply {
			return nil, nil, false
		}
		if livestore.CompareAndSwap(gs.rateLimitUsage, id, cur, &next) {
			return settings, &next, true
		}
	}
}

// updateState stores fn's next state for whatever state id holds in t, retrying against whatever
// another writer stored first. It reports whether a state was stored: false when id holds none or
// fn declined.
func updateState(t *livestore.Table[string, *LimitState], id string, fn func(LimitState) (LimitState, bool)) bool {
	for {
		cur, ok := t.Get(id)
		if !ok {
			return false
		}
		next, apply := fn(*cur)
		if !apply {
			return false
		}
		if livestore.CompareAndSwap(t, id, cur, &next) {
			return true
		}
	}
}

// BudgetState returns a copy of a budget's live state, and false when the store holds none.
func (gs *LocalGovernanceStore) BudgetState(id string) (LimitState, bool) {
	st, ok := gs.budgetUsage.Get(id)
	if !ok {
		return LimitState{}, false
	}
	return *st, true
}

// RateLimitState returns a copy of a rate limit's live state, and false when the store holds none.
func (gs *LocalGovernanceStore) RateLimitState(id string) (LimitState, bool) {
	st, ok := gs.rateLimitUsage.Get(id)
	if !ok {
		return LimitState{}, false
	}
	return *st, true
}

// RangeBudgetStates calls fn with every budget the store holds, its settings and its live state,
// until fn returns false. Nothing is copied per budget: settings is the live record's row and st the
// stored state, both shared, so fn must not change them and copies what it keeps. A budget whose
// record is gone is skipped, as GetGovernanceUsageData skips it. Callers that read every limit on a
// timer use it instead of GetGovernanceUsageData, which assembles a row for every limit.
func (gs *LocalGovernanceStore) RangeBudgetStates(fn func(id string, settings *configstoreTables.TableBudget, st *LimitState) bool) {
	gs.budgetUsage.Range(func(id string, st *LimitState) bool {
		settings, ok := gs.budgetSettings(id)
		if !ok {
			return true
		}
		return fn(id, settings, st)
	})
}

// RangeRateLimitStates is RangeBudgetStates for rate limits.
func (gs *LocalGovernanceStore) RangeRateLimitStates(fn func(id string, settings *configstoreTables.TableRateLimit, st *LimitState) bool) {
	gs.rateLimitUsage.Range(func(id string, st *LimitState) bool {
		settings, ok := gs.rateLimitSettings(id)
		if !ok {
			return true
		}
		return fn(id, settings, st)
	})
}

// BudgetView is a budget as callers read it, built from the settings and state RangeBudgetStates
// hands over: its settings with st's usage, last reset and override, and the override count derived
// for that last reset. It is a value, so the caller keeps it on its stack.
func BudgetView(settings *configstoreTables.TableBudget, st *LimitState) configstoreTables.TableBudget {
	return budgetView(settings, st)
}

// UpdateBudgetState replaces a budget's state with what fn returns for the current one, retrying
// when another writer changed it first, so no concurrent charge or reset is lost. fn may run more
// than once and must only compute; returning false leaves the state as it is. It reports whether a
// state was stored: false when the budget has none or fn declined.
func (gs *LocalGovernanceStore) UpdateBudgetState(id string, fn func(LimitState) (LimitState, bool)) bool {
	return updateState(gs.budgetUsage, id, fn)
}

// UpdateRateLimitState is UpdateBudgetState for rate limits.
func (gs *LocalGovernanceStore) UpdateRateLimitState(id string, fn func(LimitState) (LimitState, bool)) bool {
	return updateState(gs.rateLimitUsage, id, fn)
}

// applyBudgetRecord brings a budget record, the row as read, into its state: a budget with no state
// yet is seeded from the usage, last reset and override the row carries, its usage counted from the
// row, as only usage added after the read is unsaved; a budget already held keeps
// its usage and takes the row's override, as every install of a budget's row always has. A seeded
// budget starts from a reset generation no earlier state of it held, so a dump row read before a
// delete never matches the budget seeded after it.
func (gs *LocalGovernanceStore) applyBudgetRecord(id string, row *configstoreTables.TableBudget) {
	var granted LimitState
	granted.setOverride(row)
	for {
		cur, ok := gs.budgetUsage.Get(id)
		if !ok {
			st := budgetSeed(row)
			st.ResetGen = gs.budgetIncarnations.Add(1) << 32
			if livestore.CompareAndSwap(gs.budgetUsage, id, nil, &st) {
				return
			}
			continue
		}
		if cur.Override == granted.Override && cur.ResetLoc[anchorLocSlot] == granted.ResetLoc[anchorLocSlot] {
			return
		}
		next := *cur
		next.Override = granted.Override
		next.ResetLoc[anchorLocSlot] = granted.ResetLoc[anchorLocSlot]
		if livestore.CompareAndSwap(gs.budgetUsage, id, cur, &next) {
			return
		}
	}
}

// seedRateLimitState stores a first state for a rate limit from the token and request usage and
// last resets its row carries, counted from the row, leaving a state already held untouched.
func (gs *LocalGovernanceStore) seedRateLimitState(id string, row *configstoreTables.TableRateLimit) {
	if _, ok := gs.rateLimitUsage.Get(id); ok {
		return
	}
	st := rateLimitSeed(row)
	livestore.CompareAndSwap(gs.rateLimitUsage, id, nil, &st)
}

// budgetSeed is the first state a budget's row seeds: the usage, last reset and override it
// carries, its usage counted from the row. The caller sets the reset generation of a state it
// stores.
func budgetSeed(row *configstoreTables.TableBudget) LimitState {
	var st LimitState
	st.setOverride(row)
	st.Usage[BudgetSlot] = DollarCount(row.CurrentUsage)
	st.setLastReset(BudgetSlot, row.LastReset)
	st.LastDBUsage[BudgetSlot] = DollarCount(row.CurrentUsage)
	st.ResetEpoch[BudgetSlot] = row.UsageResetEpoch
	return st
}

// rateLimitSeed is budgetSeed for rate limits: the token and request usage and last resets the row
// carries, counted from the row.
func rateLimitSeed(row *configstoreTables.TableRateLimit) LimitState {
	var st LimitState
	st.Usage[TokenSlot] = WholeCount(row.TokenCurrentUsage)
	st.Usage[RequestSlot] = WholeCount(row.RequestCurrentUsage)
	st.setLastReset(TokenSlot, row.TokenLastReset)
	st.setLastReset(RequestSlot, row.RequestLastReset)
	st.LastDBUsage = st.Usage
	st.ResetEpoch[TokenSlot] = row.TokenUsageResetEpoch
	st.ResetEpoch[RequestSlot] = row.RequestUsageResetEpoch
	return st
}
