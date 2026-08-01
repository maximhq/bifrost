package tables

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// utcDate builds a UTC instant for the calendar-period tables below.
func utcDate(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
}

// TestCountCalendarPeriods verifies the override-cycle counter counts exactly the
// boundaries GetCalendarPeriodStart produces.
//
// The two must agree or a finite override on a calendar-aligned budget drifts away
// from the cadence the budget actually resets on: WindowsSinceAnchor uses this to
// decide how many granted windows have closed, while the reset path uses
// GetCalendarPeriodStart to decide when a window closes.
func TestCountCalendarPeriods(t *testing.T) {
	tests := []struct {
		name     string
		duration string
		from     time.Time
		to       time.Time
		want     int
	}{
		{name: "same day is zero", duration: "1d", from: utcDate(2026, time.March, 10, 1), to: utcDate(2026, time.March, 10, 23), want: 0},
		{name: "one day boundary", duration: "1d", from: utcDate(2026, time.March, 10, 23), to: utcDate(2026, time.March, 11, 1), want: 1},
		{name: "three day boundaries", duration: "1d", from: utcDate(2026, time.March, 10, 0), to: utcDate(2026, time.March, 13, 0), want: 3},
		{name: "day across month end", duration: "1d", from: utcDate(2026, time.March, 31, 12), to: utcDate(2026, time.April, 2, 1), want: 2},
		{name: "day across year end", duration: "1d", from: utcDate(2025, time.December, 31, 12), to: utcDate(2026, time.January, 1, 1), want: 1},

		// Weeks are counted from Monday, matching GetCalendarPeriodStart's snap.
		{name: "same week is zero", duration: "1w", from: utcDate(2026, time.March, 10, 0), to: utcDate(2026, time.March, 14, 0), want: 0},
		{name: "one week boundary", duration: "1w", from: utcDate(2026, time.March, 10, 0), to: utcDate(2026, time.March, 17, 0), want: 1},

		{name: "same month is zero", duration: "1M", from: utcDate(2026, time.March, 1, 0), to: utcDate(2026, time.March, 31, 23), want: 0},
		{name: "one month boundary", duration: "1M", from: utcDate(2026, time.March, 31, 23), to: utcDate(2026, time.April, 1, 0), want: 1},
		{name: "months across year end", duration: "1M", from: utcDate(2025, time.November, 15, 0), to: utcDate(2026, time.February, 3, 0), want: 3},

		{name: "same year is zero", duration: "1Y", from: utcDate(2026, time.January, 1, 0), to: utcDate(2026, time.December, 31, 23), want: 0},
		{name: "one year boundary", duration: "1Y", from: utcDate(2025, time.December, 31, 23), to: utcDate(2026, time.January, 1, 0), want: 1},

		// Not calendar suffixes, so there is no calendar boundary to count.
		{name: "hours are not a calendar period", duration: "1h", from: utcDate(2026, time.March, 10, 1), to: utcDate(2026, time.March, 12, 1), want: 0},
		{name: "reversed range is zero", duration: "1d", from: utcDate(2026, time.March, 12, 0), to: utcDate(2026, time.March, 10, 0), want: 0},
		{name: "empty duration is zero", duration: "", from: utcDate(2026, time.March, 10, 0), to: utcDate(2026, time.March, 12, 0), want: 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, CountCalendarPeriods(testCase.duration, testCase.from, testCase.to))
		})
	}
}

// TestCountCalendarPeriodsIgnoresDurationMultiplier pins a surprising inherited
// behaviour rather than silently diverging from it.
//
// GetCalendarPeriodStart ignores the numeric prefix: "7d" snaps to midnight today,
// so a calendar-aligned "7d" budget actually resets daily, not weekly. This counter
// must reproduce that exactly, because a mismatch would make a finite override count
// windows at a different rate than the budget resets at. Changing either one without
// the other is the bug this test exists to catch, so a deliberate fix to the
// multiplier must update both and edit this test.
func TestCountCalendarPeriodsIgnoresDurationMultiplier(t *testing.T) {
	from := utcDate(2026, time.March, 10, 0)
	to := utcDate(2026, time.March, 13, 0)

	assert.Equal(t, 3, CountCalendarPeriods("7d", from, to),
		`"7d" must count days, matching GetCalendarPeriodStart which snaps "7d" to midnight today`)
	assert.Equal(t, CountCalendarPeriods("1d", from, to), CountCalendarPeriods("7d", from, to),
		`"1d" and "7d" must agree because GetCalendarPeriodStart treats them identically`)

	// The reset path's view, asserted directly so the two cannot drift apart.
	assert.True(t, GetCalendarPeriodStart("7d", to).Equal(GetCalendarPeriodStart("1d", to)),
		`GetCalendarPeriodStart must treat "7d" and "1d" identically for this counter to be correct`)
}

// TestRollingWindowStart verifies the rolling lattice is a pure function of
// (anchor, duration, now) and always lands on a lattice point.
func TestRollingWindowStart(t *testing.T) {
	anchor := utcDate(2026, time.March, 10, 0)
	hour := time.Hour

	assert.True(t, RollingWindowStart(anchor, hour, anchor).Equal(anchor), "now at the anchor stays on the anchor")
	assert.True(t, RollingWindowStart(anchor, hour, anchor.Add(59*time.Minute)).Equal(anchor), "inside the first window stays on the anchor")
	assert.True(t, RollingWindowStart(anchor, hour, anchor.Add(hour)).Equal(anchor.Add(hour)), "exactly on a boundary lands on it")
	assert.True(t, RollingWindowStart(anchor, hour, anchor.Add(90*time.Minute)).Equal(anchor.Add(hour)), "mid second window snaps back")

	// A long gap collapses to the current lattice point in one step rather than
	// requiring one call per elapsed window.
	assert.True(t, RollingWindowStart(anchor, hour, anchor.Add(240*hour+17*time.Minute)).Equal(anchor.Add(240*hour)),
		"a ten day gap lands on the current window boundary")

	// Clock skew and misconfiguration must never produce a target before the anchor
	// or a perpetually-due target.
	assert.True(t, RollingWindowStart(anchor, hour, anchor.Add(-time.Hour)).Equal(anchor), "now before the anchor clamps to the anchor")
	assert.True(t, RollingWindowStart(anchor, 0, anchor.Add(time.Hour)).Equal(anchor), "zero duration returns the anchor")
	assert.True(t, RollingWindowStart(anchor, -time.Hour, anchor.Add(time.Hour)).Equal(anchor), "negative duration returns the anchor")
}

// TestWindowStartCalendarAlignedSubDayUsesRollingLattice pins the case that was
// silently broken: a calendar-aligned budget whose duration is shorter than a day.
//
// IsCalendarAlignableDuration only accepts d/w/M/Y, so a calendar-aligned "1h" budget
// falls through to the rolling branch. Before the lattice that branch stamped each
// node's own clock, so these budgets diverged across a cluster exactly like plain
// rolling ones, despite the operator having asked for alignment.
func TestWindowStartCalendarAlignedSubDayUsesRollingLattice(t *testing.T) {
	created := utcDate(2026, time.March, 10, 0)
	budget := &TableBudget{
		ResetDuration:     "1h",
		CreatedAt:         created,
		LastReset:         created,
		IsCalendarAligned: true,
	}

	// Two different observation instants inside the same window must agree, which is
	// what makes two nodes with out-of-phase tickers agree.
	first := budget.WindowStart(created.Add(90 * time.Minute))
	second := budget.WindowStart(created.Add(119 * time.Minute))
	assert.True(t, first.Equal(second), "two instants in one window must give the same boundary: %s vs %s", first, second)
	assert.True(t, first.Equal(created.Add(time.Hour)), "boundary must be a lattice point past CreatedAt, got %s", first)
}

// TestWindowStartCalendarAlignedDayUsesCalendarBoundary verifies a calendar-aligned
// budget with a calendar-scale duration snaps to midnight UTC rather than to the
// CreatedAt lattice, which is what the flag is for.
func TestWindowStartCalendarAlignedDayUsesCalendarBoundary(t *testing.T) {
	// Deliberately created mid-afternoon so the CreatedAt lattice and the calendar
	// boundary cannot coincide by accident.
	created := utcDate(2026, time.March, 10, 15)
	budget := &TableBudget{
		ResetDuration:     "1d",
		CreatedAt:         created,
		LastReset:         created,
		IsCalendarAligned: true,
	}

	now := utcDate(2026, time.March, 12, 9)
	got := budget.WindowStart(now)
	assert.True(t, got.Equal(utcDate(2026, time.March, 12, 0)),
		"calendar-aligned daily budget should snap to midnight UTC, got %s", got)
}

// TestCalendarAlignedOverrideSpansExactlyGrantedPeriods verifies a finite override on
// a calendar-aligned budget expires after exactly the granted number of calendar
// periods, using the calendar counter rather than rolling arithmetic.
func TestCalendarAlignedOverrideSpansExactlyGrantedPeriods(t *testing.T) {
	// Granted mid-afternoon; the anchor is the calendar period start, as
	// UpdateBudgetOverride computes it via WindowStart.
	grantDay := utcDate(2026, time.March, 10, 0)
	budget := &TableBudget{
		MaxLimit:          100,
		ResetDuration:     "1d",
		CreatedAt:         utcDate(2026, time.March, 1, 15),
		LastReset:         grantDay,
		IsCalendarAligned: true,
	}
	require.NoError(t, budget.SetOverrideAt(25, BudgetOverrideModeCycles, 3, grantDay))
	require.Equal(t, 3, budget.OverrideCyclesRemaining)
	require.Equal(t, 125.0, budget.EffectiveMaxLimit())

	// Each midnight closes one granted period.
	for day, wantRemaining := range map[int]int{11: 2, 12: 1} {
		budget.LastReset = utcDate(2026, time.March, day, 0)
		budget.RefreshOverrideCyclesRemaining()
		assert.Equal(t, wantRemaining, budget.OverrideCyclesRemaining, "after midnight on March %d", day)
		assert.True(t, budget.HasActiveOverride(), "override should still be active on March %d", day)
	}

	// The third midnight exhausts the grant.
	budget.LastReset = utcDate(2026, time.March, 13, 0)
	budget.RefreshOverrideCyclesRemaining()
	assert.False(t, budget.HasActiveOverride(), "a 3 period grant must be spent after 3 midnights")
	assert.Equal(t, 100.0, budget.EffectiveMaxLimit())
}

// TestCalendarAlignedOverrideSurvivesConfigReload verifies a config reload replaying
// the persisted grant cannot hand back a spent period on a calendar-aligned budget.
func TestCalendarAlignedOverrideSurvivesConfigReload(t *testing.T) {
	grantDay := utcDate(2026, time.March, 10, 0)
	budget := &TableBudget{
		MaxLimit:          100,
		ResetDuration:     "1d",
		CreatedAt:         utcDate(2026, time.March, 1, 15),
		LastReset:         grantDay,
		IsCalendarAligned: true,
	}
	require.NoError(t, budget.SetOverrideAt(25, BudgetOverrideModeCycles, 3, grantDay))

	// Snapshot as the database holds it at grant time.
	pristine := *budget

	budget.LastReset = utcDate(2026, time.March, 12, 0)
	budget.RefreshOverrideCyclesRemaining()
	require.Equal(t, 1, budget.OverrideCyclesRemaining, "two midnights closed, so one period should remain")

	// The reload replays the pristine grant onto the advanced boundary, exactly as
	// UpsertBudgetConfig does: grant from config, LastReset preserved, then derive.
	reloaded := pristine
	reloaded.LastReset = budget.LastReset
	reloaded.RefreshOverrideCyclesRemaining()
	assert.Equal(t, 1, reloaded.OverrideCyclesRemaining,
		"reload resurrected a spent calendar period: %d remaining, want 1", reloaded.OverrideCyclesRemaining)
}

// TestCalendarAlignedOverrideToleratesWireJitter verifies a sub-microsecond shift in
// either boundary cannot change the calendar period count.
//
// This is not a theoretical concern. A grant anchor and a LastReset are both period
// starts, so each sits exactly on midnight, and a JSON round trip shifts a nanosecond
// timestamp by a few hundred nanoseconds. A negative shift drops the value into the
// previous day, which changed the count by a whole period: on a daily budget the
// override expired a day early. The calendar path needs the same tolerance the rolling
// path does, for exactly the same edge-of-truncation reason.
func TestCalendarAlignedOverrideToleratesWireJitter(t *testing.T) {
	grantDay := utcDate(2026, time.March, 10, 0)
	resetDay := utcDate(2026, time.March, 12, 0)
	jitters := []time.Duration{-500 * time.Nanosecond, -1 * time.Nanosecond, 0, time.Nanosecond, 500 * time.Nanosecond}

	for _, anchorJitter := range jitters {
		for _, resetJitter := range jitters {
			shiftedAnchor := grantDay.Add(anchorJitter)
			budget := &TableBudget{
				MaxLimit:            100,
				ResetDuration:       "1d",
				CreatedAt:           utcDate(2026, time.March, 1, 15),
				LastReset:           resetDay.Add(resetJitter),
				IsCalendarAligned:   true,
				OverrideAmount:      25,
				OverrideMode:        BudgetOverrideModeCycles,
				OverrideCyclesTotal: 3,
				OverrideAnchorReset: &shiftedAnchor,
			}
			budget.RefreshOverrideCyclesRemaining()
			assert.Equal(t, 1, budget.OverrideCyclesRemaining,
				"anchor %s / reset %s jitter changed the calendar period count", anchorJitter, resetJitter)
		}
	}
}
