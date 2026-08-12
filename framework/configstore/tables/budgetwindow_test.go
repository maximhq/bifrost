package tables

import (
	"fmt"
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
			assert.Equal(t, testCase.want, CountCalendarPeriods(testCase.duration, testCase.from, testCase.to, QuarterStartNotApplicable))
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

	assert.Equal(t, 3, CountCalendarPeriods("7d", from, to, QuarterStartNotApplicable),
		`"7d" must count days, matching GetCalendarPeriodStart which snaps "7d" to midnight today`)
	assert.Equal(t, CountCalendarPeriods("1d", from, to, QuarterStartNotApplicable), CountCalendarPeriods("7d", from, to, QuarterStartNotApplicable),
		`"1d" and "7d" must agree because GetCalendarPeriodStart treats them identically`)

	// The reset path's view, asserted directly so the two cannot drift apart.
	assert.True(t, GetCalendarPeriodStart("7d", to, QuarterStartNotApplicable).Equal(GetCalendarPeriodStart("1d", to, QuarterStartNotApplicable)),
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

// TestIsCalendarAlignableDurationIncludesQuarter verifies a quarter has a
// calendar boundary, alongside the day/week/month/year windows.
func TestIsCalendarAlignableDurationIncludesQuarter(t *testing.T) {
	assert.True(t, IsCalendarAlignableDuration("1Q"))
	assert.True(t, IsCalendarAlignableDuration("2Q"))
	assert.False(t, IsCalendarAlignableDuration("1q"))
	assert.False(t, IsCalendarAlignableDuration("1h"))
	assert.False(t, IsCalendarAlignableDuration("30m"))
}

// TestGetCalendarPeriodStartQuarter walks every month of the year against three
// fiscal starts and asserts the snapped boundary.
//
// A quarter is not a new kind of boundary, only a different choice of which
// month-start to land on, so every result must be the 1st at midnight UTC -
// exactly what the monthly case guarantees.
//
// The January, April and October rows are deliberately identical, not a
// copy-paste slip. Quarter boundaries repeat every three months, so a start
// month only changes them modulo 3: January, April, July and October all snap
// to Jan/Apr/Jul/Oct. The common fiscal years - April in the UK and India,
// October for the US federal year, July in Australia - therefore reset on the
// same dates as calendar quarters and differ only in which quarter is labelled
// Q1. February is included because it is one of the eight start months that
// genuinely move the boundaries.
func TestGetCalendarPeriodStartQuarter(t *testing.T) {
	testCases := []struct {
		quarterStart time.Month
		// wantMonth[i] is the quarter-start month for a probe date in month i+1.
		wantMonth [12]time.Month
	}{
		{
			// Calendar quarters: Jan-Mar, Apr-Jun, Jul-Sep, Oct-Dec.
			quarterStart: time.January,
			wantMonth: [12]time.Month{
				time.January, time.January, time.January,
				time.April, time.April, time.April,
				time.July, time.July, time.July,
				time.October, time.October, time.October,
			},
		},
		{
			// April fiscal year: Apr-Jun, Jul-Sep, Oct-Dec, Jan-Mar.
			quarterStart: time.April,
			wantMonth: [12]time.Month{
				time.January, time.January, time.January,
				time.April, time.April, time.April,
				time.July, time.July, time.July,
				time.October, time.October, time.October,
			},
		},
		{
			// US federal fiscal year: Oct-Dec, Jan-Mar, Apr-Jun, Jul-Sep.
			quarterStart: time.October,
			wantMonth: [12]time.Month{
				time.January, time.January, time.January,
				time.April, time.April, time.April,
				time.July, time.July, time.July,
				time.October, time.October, time.October,
			},
		},
		{
			// A start month that is not itself on a calendar-quarter boundary
			// shifts every boundary by one month: Feb-Apr, May-Jul, Aug-Oct, Nov-Jan.
			quarterStart: time.February,
			wantMonth: [12]time.Month{
				time.November, time.February, time.February,
				time.February, time.May, time.May,
				time.May, time.August, time.August,
				time.August, time.November, time.November,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.quarterStart.String(), func(t *testing.T) {
			for month := 1; month <= 12; month++ {
				// Probe mid-month so the result cannot accidentally match the input.
				probe := time.Date(2026, time.Month(month), 17, 13, 45, 30, 500, time.UTC)
				got := GetCalendarPeriodStart("1Q", probe, testCase.quarterStart)

				assert.Equal(t, testCase.wantMonth[month-1], got.Month(),
					"probe %s with quarter start %s", probe.Format("2006-01"), testCase.quarterStart)
				assert.Equal(t, 1, got.Day(), "a quarter boundary is always the 1st")
				assert.Equal(t, 0, got.Hour())
				assert.Equal(t, 0, got.Minute())
				assert.Equal(t, 0, got.Second())
				assert.Equal(t, 0, got.Nanosecond())
				assert.Equal(t, time.UTC, got.Location())
				assert.False(t, got.After(probe), "the period start cannot be in the future")
			}
		})
	}
}

// TestGetCalendarPeriodStartQuarterWrapsAcrossTheYear pins the year boundary.
//
// Under an April fiscal start the final quarter is Jan-Mar of the *following*
// calendar year, so the snapped boundary has to move back into January of the
// probe's own year rather than forward into the next one. This is the case an
// implementation built on "months since January" gets wrong.
func TestGetCalendarPeriodStartQuarterWrapsAcrossTheYear(t *testing.T) {
	// February 2027 under an April start belongs to the Jan-Mar 2027 quarter.
	feb2027 := time.Date(2027, time.February, 10, 0, 0, 0, 0, time.UTC)
	assert.Equal(t,
		time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		GetCalendarPeriodStart("1Q", feb2027, time.April))

	// December 2026 under an April start belongs to the Oct-Dec 2026 quarter.
	dec2026 := time.Date(2026, time.December, 31, 23, 59, 59, 0, time.UTC)
	assert.Equal(t,
		time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC),
		GetCalendarPeriodStart("1Q", dec2026, time.April))

	// January 2026 under a November start belongs to the Nov 2025 - Jan 2026 quarter,
	// so the boundary is in the previous calendar year.
	jan2026 := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	assert.Equal(t,
		time.Date(2025, time.November, 1, 0, 0, 0, 0, time.UTC),
		GetCalendarPeriodStart("1Q", jan2026, time.November))
}

// TestGetCalendarPeriodStartQuarterIgnoresMultiplier pins the inherited
// convention that calendar snapping ignores the numeric prefix, matching how
// "7d" snaps to midnight today rather than to a seven-day block. It is the
// reason a quarter needs its own suffix instead of reusing "3M".
func TestGetCalendarPeriodStartQuarterIgnoresMultiplier(t *testing.T) {
	probe := time.Date(2026, time.May, 20, 0, 0, 0, 0, time.UTC)
	assert.Equal(t,
		GetCalendarPeriodStart("1Q", probe, time.January),
		GetCalendarPeriodStart("2Q", probe, time.January))

	// "3M" is a month window, not a quarter: it snaps to the 1st of May.
	assert.Equal(t,
		time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
		GetCalendarPeriodStart("3M", probe, time.January))
}

// TestCountCalendarPeriodsQuarter verifies the override-cycle counter counts
// quarter boundaries.
func TestCountCalendarPeriodsQuarter(t *testing.T) {
	testCases := []struct {
		name         string
		quarterStart time.Month
		from, to     time.Time
		want         int
	}{
		{
			name:         "same quarter crosses nothing",
			quarterStart: time.January,
			from:         time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC),
			to:           time.Date(2026, time.March, 31, 23, 0, 0, 0, time.UTC),
			want:         0,
		},
		{
			name:         "one boundary",
			quarterStart: time.January,
			from:         time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC),
			to:           time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
			want:         1,
		},
		{
			name:         "a full year is four quarters",
			quarterStart: time.January,
			from:         time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			to:           time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:         4,
		},
		{
			// Feb-Apr / May-Jul / Aug-Oct / Nov-Jan: Mar 1 to Apr 1 stays inside
			// Feb-Apr, where a January start would have crossed into Apr-Jun.
			name:         "february start moves the boundary off the calendar quarter",
			quarterStart: time.February,
			from:         time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC),
			to:           time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
			want:         0,
		},
		{
			// The same interval under a January start does cross a boundary,
			// which is what makes the case above meaningful rather than incidental.
			name:         "january start crosses where february start does not",
			quarterStart: time.January,
			from:         time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC),
			to:           time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
			want:         1,
		},
		{
			// Under a February start the Nov-Jan quarter spans the new year, so
			// December to December crosses nothing despite the calendar rollover
			// being one day away.
			name:         "quarter spanning the new year does not cross in december",
			quarterStart: time.February,
			from:         time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC),
			to:           time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC),
			want:         0,
		},
		{
			name:         "to before from counts nothing",
			quarterStart: time.January,
			from:         time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
			to:           time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:         0,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want,
				CountCalendarPeriods("1Q", testCase.from, testCase.to, testCase.quarterStart))
		})
	}
}

// TestCountCalendarPeriodsQuarterAgreesWithGetCalendarPeriodStart is the
// invariant that keeps finite overrides honest.
//
// A budget's override consumption is derived by counting periods, while its
// resets are driven by snapping to period starts. If the two disagree by even
// one, a finite override expires a whole quarter early or lives a quarter too
// long. Rather than restate the arithmetic, this walks four years a day at a
// time and asserts the count equals the number of distinct boundaries actually
// produced.
func TestCountCalendarPeriodsQuarterAgreesWithGetCalendarPeriodStart(t *testing.T) {
	for _, quarterStart := range []time.Month{time.January, time.February, time.April, time.October, time.December} {
		t.Run(quarterStart.String(), func(t *testing.T) {
			from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			previous := GetCalendarPeriodStart("1Q", from, quarterStart)
			boundaries := 0

			for day := 1; day <= 4*365; day++ {
				to := from.AddDate(0, 0, day)
				current := GetCalendarPeriodStart("1Q", to, quarterStart)
				if !current.Equal(previous) {
					boundaries++
					previous = current
				}
				require.Equal(t, boundaries, CountCalendarPeriods("1Q", from, to, quarterStart),
					"day %d (%s)", day, to.Format("2006-01-02"))
			}
		})
	}
}

// TestWindowStartCalendarAlignedQuarterUsesBudgetQuarterStart verifies the
// budget's own quarter definition drives its window, not a global default.
func TestWindowStartCalendarAlignedQuarterUsesBudgetQuarterStart(t *testing.T) {
	now := time.Date(2026, time.May, 15, 8, 0, 0, 0, time.UTC)

	januaryBudget := &TableBudget{
		ResetDuration:     "1Q",
		IsCalendarAligned: true,
		CreatedAt:         time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
	}
	assert.Equal(t,
		time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
		januaryBudget.WindowStart(now))

	aprilBudget := &TableBudget{
		ResetDuration:     "1Q",
		IsCalendarAligned: true,
		CreatedAt:         time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
		ResetConfig:       &BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	assert.Equal(t,
		time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC),
		aprilBudget.WindowStart(now))

	// February start puts 15 May inside the May-Jul quarter, which is where the
	// two definitions visibly diverge.
	februaryBudget := &TableBudget{
		ResetDuration:     "1Q",
		IsCalendarAligned: true,
		CreatedAt:         time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
		ResetConfig:       &BudgetResetConfig{QuarterStartMonth: int(time.February)},
	}
	assert.Equal(t,
		time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
		februaryBudget.WindowStart(now))
}

// TestWindowStartRollingQuarterUsesCreatedAtLattice verifies a quarterly budget
// that is not calendar-aligned keeps the existing rolling behaviour: a 90-day
// lattice anchored on CreatedAt, ignoring the quarter definition entirely.
func TestWindowStartRollingQuarterUsesCreatedAtLattice(t *testing.T) {
	createdAt := time.Date(2026, time.January, 17, 9, 30, 0, 0, time.UTC)
	budget := &TableBudget{
		ResetDuration: "1Q",
		CreatedAt:     createdAt,
		ResetConfig:   &BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}

	// Two days in: still inside the first window.
	assert.Equal(t, createdAt, budget.WindowStart(createdAt.AddDate(0, 0, 2)))

	// 91 days in: exactly one 90-day window has closed.
	assert.Equal(t, createdAt.Add(90*24*time.Hour), budget.WindowStart(createdAt.AddDate(0, 0, 91)))

	// The boundary is a lattice point, never the calendar quarter start.
	assert.NotEqual(t, 1, budget.WindowStart(createdAt.AddDate(0, 0, 91)).Day())
}

// TestWindowsSinceAnchorQuarterSpansGrantedQuarters verifies a finite override on
// a calendar-aligned quarterly budget is consumed one quarter at a time, using
// the budget's own fiscal start.
func TestWindowsSinceAnchorQuarterSpansGrantedQuarters(t *testing.T) {
	// April fiscal start: boundaries at Apr 1, Jul 1, Oct 1, Jan 1.
	anchor := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	budget := &TableBudget{
		MaxLimit:          100,
		ResetDuration:     "1Q",
		IsCalendarAligned: true,
		LastReset:         anchor,
		ResetConfig:       &BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	require.NoError(t, budget.SetOverrideAt(50, BudgetOverrideModeCycles, 2, anchor))
	require.Equal(t, 2, budget.OverrideCyclesRemaining)

	// One quarter closes.
	budget.LastReset = time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, 1, budget.WindowsSinceAnchor())
	budget.RefreshOverrideCyclesRemaining()
	assert.Equal(t, 1, budget.OverrideCyclesRemaining)
	assert.Equal(t, 150.0, budget.EffectiveMaxLimit())

	// The second quarter closes and the grant is spent.
	budget.LastReset = time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, 2, budget.WindowsSinceAnchor())
	budget.RefreshOverrideCyclesRemaining()
	assert.Equal(t, 0, budget.OverrideCyclesRemaining)
	assert.Equal(t, 100.0, budget.EffectiveMaxLimit())
}

// TestQuarterlyBudgetResetIsNotPerpetuallyDue is the regression guard for the
// ordering hazard that split this work across two branches.
//
// Marking "Q" calendar-alignable without teaching GetCalendarPeriodStart about
// it sends WindowStart into its default branch, which returns now. Because now
// is always after LastReset, the reset path would find the budget due on every
// single ticker tick and zero its usage continuously. The assertion is simply
// that a freshly reset budget is not immediately due again.
func TestQuarterlyBudgetResetIsNotPerpetuallyDue(t *testing.T) {
	now := time.Date(2026, time.May, 15, 8, 0, 0, 0, time.UTC)

	for _, quarterStart := range []time.Month{time.January, time.April, time.October} {
		budget := &TableBudget{
			ResetDuration:     "1Q",
			IsCalendarAligned: true,
			CreatedAt:         time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			ResetConfig:       &BudgetResetConfig{QuarterStartMonth: int(quarterStart)},
		}
		// Simulate the reset path having just run.
		budget.LastReset = budget.WindowStart(now)

		assert.False(t, budget.WindowStart(now).After(budget.LastReset),
			fmt.Sprintf("quarter start %s: budget is due again immediately after resetting", quarterStart))

		// Still not due an hour later, well inside the quarter.
		later := now.Add(time.Hour)
		assert.False(t, budget.WindowStart(later).After(budget.LastReset),
			fmt.Sprintf("quarter start %s: budget became due an hour into its quarter", quarterStart))
	}
}

// TestAdoptCalendarAlignmentPreservesTheOpenWindow pins the switch-over contract:
// turning calendar alignment on must not cost the owner its accumulated usage.
//
// Without adoption, a window opened before the current boundary is already due
// the moment the flag flips, because budgetResetTarget resets whenever
// WindowStart(now) is after LastReset. Adoption moves LastReset forward onto the
// boundary instead, which the forward-only guard permits, so the window becomes
// current rather than overdue and its usage survives to the next real boundary.
func TestAdoptCalendarAlignmentPreservesTheOpenWindow(t *testing.T) {
	now := time.Date(2026, time.February, 5, 12, 0, 0, 0, time.UTC)
	monthStart := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)

	newBudget := func(duration string, lastReset time.Time) *TableBudget {
		return &TableBudget{
			ID:                "adopt-budget",
			MaxLimit:          100,
			CurrentUsage:      42,
			ResetDuration:     duration,
			IsCalendarAligned: true,
			CreatedAt:         lastReset,
			LastReset:         lastReset,
		}
	}

	t.Run("a window opened before the boundary is adopted onto it", func(t *testing.T) {
		budget := newBudget("1M", time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC))

		assert.True(t, budget.AdoptCalendarAlignment(now), "the window moved, so adoption reports a change")
		assert.True(t, budget.LastReset.Equal(monthStart), "the window is re-anchored on the boundary it now follows")
		assert.Equal(t, 42.0, budget.CurrentUsage, "adoption must never spend the operator's usage")
		assert.False(t, budget.WindowStart(now).After(budget.LastReset),
			"and the budget must no longer read as due, which is the whole point")
	})

	t.Run("a window opened after the boundary is left alone", func(t *testing.T) {
		lastReset := time.Date(2026, time.February, 3, 9, 0, 0, 0, time.UTC)
		budget := newBudget("1M", lastReset)

		assert.False(t, budget.AdoptCalendarAlignment(now), "nothing to move")
		assert.True(t, budget.LastReset.Equal(lastReset), "an already-current window keeps its own start")
	})

	t.Run("the boundary is never moved backwards", func(t *testing.T) {
		lastReset := time.Date(2026, time.February, 20, 9, 0, 0, 0, time.UTC)
		budget := newBudget("1M", lastReset)

		assert.False(t, budget.AdoptCalendarAlignment(now))
		assert.True(t, budget.LastReset.Equal(lastReset),
			"rewinding would re-open a window the cluster already agreed was spent")
	})

	t.Run("a sub-day window is not adopted", func(t *testing.T) {
		lastReset := time.Date(2026, time.February, 5, 9, 30, 0, 0, time.UTC)
		budget := newBudget("1h", lastReset)

		assert.False(t, budget.AdoptCalendarAlignment(now),
			"an hourly window has no calendar boundary, so alignment leaves it rolling")
		assert.True(t, budget.LastReset.Equal(lastReset))
	})

	t.Run("an unaligned budget is not adopted", func(t *testing.T) {
		lastReset := time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC)
		budget := newBudget("1M", lastReset)
		budget.IsCalendarAligned = false

		assert.False(t, budget.AdoptCalendarAlignment(now),
			"adoption belongs to the switch-over, not to every config write")
		assert.True(t, budget.LastReset.Equal(lastReset))
	})

	t.Run("a quarterly budget adopts its fiscal boundary", func(t *testing.T) {
		budget := newBudget("1Q", time.Date(2025, time.December, 10, 9, 0, 0, 0, time.UTC))
		budget.ResetConfig = &BudgetResetConfig{QuarterStartMonth: int(time.February)}

		assert.True(t, budget.AdoptCalendarAlignment(now))
		assert.True(t, budget.LastReset.Equal(time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)),
			"a February fiscal year opens Q1 on Feb 1, not on the calendar quarter")
	})
}

// TestAdoptCalendarAlignmentOnRateLimits mirrors the budget contract for the two
// counters a rate limit carries.
//
// Token and request counters keep independent durations and independent
// LastReset values, so adoption is decided per counter. rateLimitResetTarget
// applies the same "boundary after last reset" rule as budgets, which means the
// same switch-over would otherwise clear a counter the operator was still using.
func TestAdoptCalendarAlignmentOnRateLimits(t *testing.T) {
	now := time.Date(2026, time.February, 5, 12, 0, 0, 0, time.UTC)
	monthStart := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	dayStart := time.Date(2026, time.February, 5, 0, 0, 0, 0, time.UTC)
	stale := time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC)

	newRateLimit := func(tokenDuration, requestDuration string) *TableRateLimit {
		return &TableRateLimit{
			ID:                   "adopt-rate-limit",
			TokenResetDuration:   &tokenDuration,
			TokenCurrentUsage:    900,
			TokenLastReset:       stale,
			RequestResetDuration: &requestDuration,
			RequestCurrentUsage:  17,
			RequestLastReset:     stale,
			IsCalendarAligned:    true,
		}
	}

	t.Run("each counter adopts its own boundary", func(t *testing.T) {
		rl := newRateLimit("1M", "1d")

		assert.True(t, rl.AdoptCalendarAlignment(now), "both counters moved")
		assert.True(t, rl.TokenLastReset.Equal(monthStart), "the monthly counter adopts the month boundary")
		assert.True(t, rl.RequestLastReset.Equal(dayStart), "the daily counter adopts midnight, not the month boundary")
		assert.Equal(t, int64(900), rl.TokenCurrentUsage, "adoption must not clear a counter")
		assert.Equal(t, int64(17), rl.RequestCurrentUsage)
	})

	t.Run("a sub-day counter is left rolling while its sibling adopts", func(t *testing.T) {
		rl := newRateLimit("1M", "1h")

		assert.True(t, rl.AdoptCalendarAlignment(now), "the monthly counter still moves")
		assert.True(t, rl.TokenLastReset.Equal(monthStart))
		assert.True(t, rl.RequestLastReset.Equal(stale),
			"an hourly counter has no calendar boundary, so it keeps its rolling anchor")
	})

	t.Run("an unaligned rate limit is not adopted", func(t *testing.T) {
		rl := newRateLimit("1M", "1d")
		rl.IsCalendarAligned = false

		assert.False(t, rl.AdoptCalendarAlignment(now))
		assert.True(t, rl.TokenLastReset.Equal(stale))
		assert.True(t, rl.RequestLastReset.Equal(stale))
	})

	t.Run("counters with no duration are skipped", func(t *testing.T) {
		rl := &TableRateLimit{ID: "no-durations", IsCalendarAligned: true, TokenLastReset: stale, RequestLastReset: stale}

		assert.False(t, rl.AdoptCalendarAlignment(now))
		assert.True(t, rl.TokenLastReset.Equal(stale))
	})
}
