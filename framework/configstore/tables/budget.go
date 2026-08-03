package tables

import (
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
)

// BudgetOverrideMode identifies how long a budget override remains active.
type BudgetOverrideMode string

const (
	// BudgetOverrideModeNone means no override is active (default/zero value).
	BudgetOverrideModeNone BudgetOverrideMode = ""
	// BudgetOverrideModeCycles keeps an override active for a finite number of reset cycles.
	BudgetOverrideModeCycles BudgetOverrideMode = "cycles"
	// BudgetOverrideModeForever keeps an override active until it is explicitly removed.
	BudgetOverrideModeForever BudgetOverrideMode = "forever"
)

// TableBudget defines spending limits with configurable reset periods
type TableBudget struct {
	ID            string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	MaxLimit      float64   `gorm:"not null" json:"max_limit"`                       // Maximum budget in dollars
	ResetDuration string    `gorm:"type:varchar(50);not null" json:"reset_duration"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	LastReset     time.Time `gorm:"index" json:"last_reset"`                         // Last time budget was reset
	CurrentUsage  float64   `gorm:"default:0" json:"current_usage"`                  // Current usage in dollars

	// OverrideAmount is added to MaxLimit while the override is active.
	OverrideAmount float64 `gorm:"not null;default:0" json:"override_amount,omitempty"`
	// OverrideMode distinguishes finite cycle-based overrides from permanent overrides.
	OverrideMode BudgetOverrideMode `gorm:"type:varchar(20);not null;default:''" json:"override_mode,omitempty"`
	// OverrideCyclesRemaining includes the current cycle and is positive only for cycle-based overrides.
	//
	// This is a materialized view of the immutable grant below, not independent
	// state: RefreshOverrideCyclesRemaining recomputes it from
	// (OverrideAnchorReset, OverrideCyclesTotal, LastReset) on every write that
	// advances LastReset. It stays persisted and exposed so the API, the UI,
	// HasActiveOverride and EffectiveMaxLimit are unchanged, but it must never be
	// treated as the source of truth. Doing so is what let a config reload hand
	// back a cycle the reset path had already spent.
	OverrideCyclesRemaining int `gorm:"not null;default:0" json:"override_cycles_remaining,omitempty"`

	// OverrideCyclesTotal is the number of budget windows the current finite
	// override was granted for. Immutable for the life of a grant.
	OverrideCyclesTotal int `gorm:"not null;default:0" json:"override_cycles_total,omitempty"`

	// OverrideAnchorReset is the window boundary at which the current finite
	// override was granted. Immutable for the life of a grant.
	//
	// Together with OverrideCyclesTotal it makes override consumption a pure
	// function of persisted state, so every cluster node derives the same
	// remaining count without coordinating, a reload that replays the grant
	// cannot resurrect a spent cycle, and a node that was down for many windows
	// lands on the correct count instead of catching up one window at a time.
	//
	// Nil only for a budget with no finite override. validateOverride rejects a
	// cycles override without an anchor, so an unanchored grant cannot be persisted,
	// and the backfill migration adopts any row written before these columns existed.
	//
	// Deliberately not indexed. Nothing filters, joins or orders by this column: it is
	// written with the rest of the override state and read back as part of the row. The
	// only predicate on it is the one-time backfill's IS NULL, which scans regardless.
	// An index here would add write cost to every budget update, and DumpBudgets
	// rewrites the whole table on its dump tick.
	OverrideAnchorReset *time.Time `json:"override_anchor_reset,omitempty"`

	// Owner FKs: a budget belongs to at most one Team, VK, ProviderConfig, ModelConfig, or Customer
	TeamID           *string `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	VirtualKeyID     *string `gorm:"type:varchar(255);index" json:"virtual_key_id,omitempty"`
	ProviderConfigID *uint   `gorm:"index" json:"provider_config_id,omitempty"`
	ModelConfigID    *string `gorm:"type:varchar(255);index" json:"model_config_id,omitempty"`
	CustomerID       *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`

	// Deprecated: set calendar_aligned on the parent access profile / VK / team
	// instead. Kept for backward compatibility with older config.json files;
	// the OSS applyV1Compat path and the enterprise access-profile reconciler
	// promote any true value here to the owner's top-level CalendarAligned at
	// load time.
	CalendarAlignedInput *bool `gorm:"-" json:"calendar_aligned,omitempty"`

	// Derived from the owning entity (VK / PC's parent VK / Team). Populated by
	// the owner's AfterFind hook on cold load and by the governance store's
	// Create/Update *InMemory methods on write. Never persisted; consumed by
	// the reset path to decide rolling vs. calendar-aligned window.
	IsCalendarAligned bool `gorm:"-" json:"-"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableBudget) TableName() string { return "governance_budgets" }

// WindowStart returns the budget's current reset-window boundary at or before
// now: the calendar period for a calendar-aligned budget whose duration has a
// calendar boundary, and the rolling lattice anchored on CreatedAt otherwise.
//
// This is the single definition of "when did the current window open", shared by
// the reset path, the enforcement path and the override-grant anchor. Every node
// derives the same instant from the same persisted row, which is what lets
// budget resets and finite-override accounting agree across a cluster without
// any coordination.
//
// Falls back to LastReset as the rolling anchor when CreatedAt is zero, which
// happens for budgets that were loaded straight from a config file and never
// round-tripped through the database. Such a budget still resets on a stable
// cadence; it just anchors on its own last reset rather than its creation.
// Returns LastReset unchanged when the duration is missing, unparseable or
// non-positive, so callers comparing against LastReset see "not due".
func (b *TableBudget) WindowStart(now time.Time) time.Time {
	if b == nil {
		return time.Time{}
	}
	if b.ResetDuration == "" {
		return b.LastReset
	}
	if b.IsCalendarAligned && IsCalendarAlignableDuration(b.ResetDuration) {
		return GetCalendarPeriodStart(b.ResetDuration, now)
	}
	duration, err := ParseDuration(b.ResetDuration)
	if err != nil || duration <= 0 {
		return b.LastReset
	}
	anchor := b.CreatedAt
	if anchor.IsZero() {
		anchor = b.LastReset
	}
	return RollingWindowStart(anchor, duration, now)
}

// StampCalendarAlignment copies an owner's calendar-alignment flag onto the
// budgets and rate limit it owns.
//
// IsCalendarAligned is derived, never persisted on the budget or rate-limit row,
// so every owner has to stamp it on load. Miss it and the governance reset path
// silently takes the rolling branch, so a calendar-aligned owner's "1d" budget
// resets on a rolling 24h clock instead of at midnight UTC. Every owner type
// (virtual key, team, customer, model config, and the enterprise access profiles)
// funnels through here so a new owner cannot reintroduce that bug by forgetting
// one of the two field sets.
func StampCalendarAlignment(calendarAligned bool, budgets []TableBudget, rateLimit *TableRateLimit) {
	for i := range budgets {
		budgets[i].IsCalendarAligned = calendarAligned
	}
	if rateLimit != nil {
		rateLimit.IsCalendarAligned = calendarAligned
	}
}

// HasActiveOverride reports whether the budget currently has a valid configured override.
func (b *TableBudget) HasActiveOverride() bool {
	if b == nil || b.OverrideAmount <= 0 {
		return false
	}
	return b.OverrideMode == BudgetOverrideModeForever ||
		(b.OverrideMode == BudgetOverrideModeCycles && b.OverrideCyclesRemaining > 0)
}

// EffectiveMaxLimit returns the base limit plus any active override amount.
func (b *TableBudget) EffectiveMaxLimit() float64 {
	if b == nil {
		return 0
	}
	if !b.HasActiveOverride() {
		return b.MaxLimit
	}
	return b.MaxLimit + b.OverrideAmount
}

// SetOverride replaces the budget's current override after validating the
// complete state, anchoring a finite grant at the budget's current window
// boundary (LastReset).
//
// LastReset is the right anchor for a clock-free caller because it already is
// the current window's boundary. Callers that hold a clock and may be looking at
// a row whose boundary has not been flushed yet should use SetOverrideAt with
// WindowStart(now) instead, so the grant is not anchored one window behind.
func (b *TableBudget) SetOverride(amount float64, mode BudgetOverrideMode, cyclesRemaining int) error {
	if b == nil {
		return fmt.Errorf("budget is required")
	}
	return b.SetOverrideAt(amount, mode, cyclesRemaining, b.LastReset)
}

// SetOverrideAt replaces the budget's current override and anchors a finite grant
// at the given window boundary, so that every node derives the same remaining
// count from the same immutable grant. anchor is ignored for forever-mode and
// cleared overrides, which have no cycle lifecycle. Validation matches
// SetOverride, and a rejected change leaves every override field untouched.
func (b *TableBudget) SetOverrideAt(amount float64, mode BudgetOverrideMode, cyclesTotal int, anchor time.Time) error {
	if b == nil {
		return fmt.Errorf("budget is required")
	}
	previousAmount := b.OverrideAmount
	previousMode := b.OverrideMode
	previousCyclesRemaining := b.OverrideCyclesRemaining
	previousCyclesTotal := b.OverrideCyclesTotal
	previousAnchor := b.OverrideAnchorReset

	b.OverrideAmount = amount
	b.OverrideMode = mode
	b.OverrideCyclesRemaining = cyclesTotal
	if mode == BudgetOverrideModeCycles {
		b.OverrideCyclesTotal = cyclesTotal
		anchoredAt := anchor
		b.OverrideAnchorReset = &anchoredAt
	} else {
		b.OverrideCyclesTotal = 0
		b.OverrideAnchorReset = nil
	}

	if err := b.validateOverride(); err != nil {
		b.OverrideAmount = previousAmount
		b.OverrideMode = previousMode
		b.OverrideCyclesRemaining = previousCyclesRemaining
		b.OverrideCyclesTotal = previousCyclesTotal
		b.OverrideAnchorReset = previousAnchor
		return err
	}
	return nil
}

// ClearOverride removes any finite or permanent override from the budget,
// including the immutable grant, so a cleared override cannot be re-derived.
func (b *TableBudget) ClearOverride() {
	if b == nil {
		return
	}
	b.OverrideAmount = 0
	b.OverrideMode = ""
	b.OverrideCyclesRemaining = 0
	b.OverrideCyclesTotal = 0
	b.OverrideAnchorReset = nil
}

// boundaryJitterTolerance absorbs sub-millisecond noise when counting whole
// windows between two boundaries.
//
// Both the grant anchor and LastReset are lattice points, so their difference is
// an exact multiple of the window and sits exactly on a truncation edge. Any
// positive error on the anchor would therefore truncate to one window fewer and
// silently grant an extra window. Such error is unavoidable: boundaries cross the
// cluster as JSON numbers, and a nanosecond epoch (around 1.8e18) exceeds
// float64's exact-integer range of 2^53, so it round-trips with roughly 256ns of
// error. One millisecond is four orders of magnitude above that noise and three
// below the smallest sensible reset window, so it cannot mask a real window.
const boundaryJitterTolerance = time.Millisecond

// WindowsSinceAnchor returns how many reset windows have closed between the
// current finite override's anchor and LastReset. Returns 0 when there is no
// anchored grant or when LastReset has not advanced past the anchor, so a node
// whose LastReset briefly lags the anchor never reports negative consumption.
func (b *TableBudget) WindowsSinceAnchor() int {
	if b == nil || b.OverrideAnchorReset == nil || b.ResetDuration == "" {
		return 0
	}
	anchor := *b.OverrideAnchorReset
	if !b.LastReset.After(anchor.Add(boundaryJitterTolerance)) {
		return 0
	}
	if b.IsCalendarAligned && IsCalendarAlignableDuration(b.ResetDuration) {
		// Both ends are nudged past the tolerance before the period count, for the
		// same reason the rolling branch adds it below. A grant anchor and a LastReset
		// are both period starts, so each sits exactly on a calendar boundary, and a
		// negative sub-microsecond shift from a JSON round trip drops it into the
		// previous period. That silently changed the count by one whole period, which
		// on a daily budget means the override expires a day early or lives a day too
		// long. Nudging forward keeps both ends inside their true period for any
		// error smaller than the tolerance.
		return CountCalendarPeriods(
			b.ResetDuration,
			anchor.Add(boundaryJitterTolerance),
			b.LastReset.Add(boundaryJitterTolerance),
		)
	}
	duration, err := ParseDuration(b.ResetDuration)
	if err != nil || duration <= 0 {
		return 0
	}
	return int((b.LastReset.Sub(anchor) + boundaryJitterTolerance) / duration)
}

// RefreshOverrideCyclesRemaining recomputes OverrideCyclesRemaining from the
// immutable grant (OverrideAnchorReset, OverrideCyclesTotal) and the current
// LastReset, clearing the override once every granted window has closed.
//
// This is the single point where a finite override advances. Deriving the count
// rather than decrementing it once per observer is what makes the lifecycle
// correct in a cluster:
//   - every node derives the same count without any coordination, where
//     decrementing locally left each node holding its own tally that only the
//     leader ever persisted;
//   - a config reload that replays the grant cannot resurrect a spent cycle,
//     because replaying an immutable value changes nothing;
//   - a node down for many windows lands directly on the correct count instead of
//     needing to replay one decrement per elapsed window.
//
// A cycles-mode budget with no anchor is not a supported state: validateOverride
// rejects it, so it cannot be persisted. Should one be constructed in memory it
// clears here, which is the safe direction (no unearned headroom) and surfaces
// immediately rather than silently granting extra windows.
func (b *TableBudget) RefreshOverrideCyclesRemaining() {
	if b == nil || b.OverrideMode != BudgetOverrideModeCycles {
		return
	}
	remaining := b.OverrideCyclesTotal - b.WindowsSinceAnchor()
	if remaining <= 0 {
		b.ClearOverride()
		return
	}
	b.OverrideCyclesRemaining = remaining
}

// validateOverride checks that the persisted override fields form one unambiguous state.
func (b *TableBudget) validateOverride() error {
	if math.IsNaN(b.OverrideAmount) || math.IsInf(b.OverrideAmount, 0) {
		return fmt.Errorf("budget override_amount must be finite")
	}
	switch b.OverrideMode {
	case "":
		if b.OverrideAmount != 0 || b.OverrideCyclesRemaining != 0 {
			return fmt.Errorf("budget override fields require override_mode")
		}
		if b.OverrideCyclesTotal != 0 || b.OverrideAnchorReset != nil {
			return fmt.Errorf("budget override grant fields require override_mode")
		}
	case BudgetOverrideModeCycles:
		if b.OverrideAmount <= 0 {
			return fmt.Errorf("budget override_amount must be > 0 for cycles mode")
		}
		if b.OverrideCyclesRemaining <= 0 {
			return fmt.Errorf("budget override_cycles_remaining must be > 0 for cycles mode")
		}
		// The grant is what the remaining count is derived from, so a cycles
		// override without one has no lifecycle at all. Requiring it here means
		// an unanchored grant can never reach the database.
		if b.OverrideAnchorReset == nil {
			return fmt.Errorf("budget override_anchor_reset is required for cycles mode")
		}
		if b.OverrideCyclesTotal < b.OverrideCyclesRemaining {
			return fmt.Errorf("budget override_cycles_total must be >= override_cycles_remaining for cycles mode")
		}
	case BudgetOverrideModeForever:
		if b.OverrideAmount <= 0 {
			return fmt.Errorf("budget override_amount must be > 0 for forever mode")
		}
		if b.OverrideCyclesRemaining != 0 {
			return fmt.Errorf("budget override_cycles_remaining must be 0 for forever mode")
		}
		if b.OverrideCyclesTotal != 0 || b.OverrideAnchorReset != nil {
			return fmt.Errorf("budget override grant fields must be empty for forever mode")
		}
	default:
		return fmt.Errorf("invalid budget override_mode: %s", b.OverrideMode)
	}
	return nil
}

// BeforeSave hook for Budget to validate reset duration format and max limit
func (b *TableBudget) BeforeSave(tx *gorm.DB) error {
	// A budget belongs to at most one owner type
	owners := 0
	if b.TeamID != nil {
		owners++
	}
	if b.VirtualKeyID != nil {
		owners++
	}
	if b.ProviderConfigID != nil {
		owners++
	}
	if b.ModelConfigID != nil {
		owners++
	}
	if b.CustomerID != nil {
		owners++
	}
	if owners > 1 {
		return fmt.Errorf("budget cannot have more than one owner (team/virtual key/provider config/model config/customer)")
	}
	// Validate that ResetDuration is in correct format (e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y")
	if d, err := ParseDuration(b.ResetDuration); err != nil {
		return fmt.Errorf("invalid reset duration format: %s", b.ResetDuration)
	} else if d <= 0 {
		return fmt.Errorf("reset duration must be > 0: %s", b.ResetDuration)
	}
	// Validate that MaxLimit is not negative (budgets should be positive)
	if b.MaxLimit < 0 {
		return fmt.Errorf("budget max_limit cannot be negative: %.2f", b.MaxLimit)
	}
	if err := b.validateOverride(); err != nil {
		return err
	}

	return nil
}
