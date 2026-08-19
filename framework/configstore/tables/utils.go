package tables

import (
	"fmt"
	"time"
)

// QuarterStartNotApplicable is the quarter start to pass for windows that carry
// no quarter definition of their own, most notably rate limits: quarters are a
// budget-only concept. January is not a fallback here but the correct answer,
// since a window with no fiscal calendar snaps to plain calendar quarters.
const QuarterStartNotApplicable = time.January

// IsCalendarAlignableDuration reports whether the given duration string supports calendar-aligned resets.
// Only day ("d"), week ("w"), month ("M"), quarter ("Q") and year ("Y") suffixes have natural
// calendar boundaries. Sub-day durations like "1h", "30m" are not alignable.
func IsCalendarAlignableDuration(duration string) bool {
	if duration == "" {
		return false
	}
	switch duration[len(duration)-1] {
	case 'd', 'w', 'M', 'Q', 'Y':
		return true
	default:
		return false
	}
}

// GetCalendarPeriodStart returns the start of the current calendar period for the given duration and time.
// For calendar-scale durations (daily, weekly, monthly, quarterly, yearly) it snaps to clean boundaries in UTC:
//   - "Nd"  → midnight UTC on the current day
//   - "Nw"  → midnight UTC on the most recent Monday
//   - "NM"  → midnight UTC on the 1st of the current month
//   - "NQ"  → midnight UTC on the 1st of the current fiscal quarter's opening month
//   - "NY"  → midnight UTC on Jan 1 of the current year
//
// For all other durations (e.g. "1h", "30m") the original time t is returned unchanged,
// since sub-day periods don't have a natural calendar boundary.
//
// quarterStart is the first month of Q1 and is consulted only for "Q". Callers with
// no fiscal calendar - rate limits, which cannot be quarterly - pass
// QuarterStartNotApplicable. It is a required parameter rather than an optional one
// so that adding a budget call site is a compile error until the budget's own
// definition is threaded through: a silent January default would produce the wrong
// boundary for eight of the twelve possible fiscal starts, and only for part of
// the year, which is exactly the kind of drift that hides in production.
func GetCalendarPeriodStart(duration string, t time.Time, quarterStart time.Month) time.Time {
	if duration == "" {
		return t
	}
	t = t.UTC()
	suffix := duration[len(duration)-1:]
	switch suffix {
	case "d":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	case "w":
		weekday := int(t.Weekday())
		// Sunday = 0, so shift to Monday = 0
		daysFromMonday := (weekday + 6) % 7
		monday := t.AddDate(0, 0, -daysFromMonday)
		return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, time.UTC)
	case "M":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "Q":
		return quarterStartAt(t, quarterStart)
	case "Y":
		return time.Date(t.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	default:
		return t
	}
}

// quarterStartAt returns midnight UTC on the 1st of the fiscal quarter containing t.
//
// Works in absolute months (year*12 + month) so the year boundary needs no special
// case: a fiscal year opening in November puts January in a quarter that began the
// previous calendar year, and month arithmetic that reasons per-year gets that wrong.
//
// Note that only quarterStart modulo 3 affects the result, since quarters repeat
// every three months. January, April, July and October are therefore equivalent
// here, which is why the common April and October fiscal years reset on the same
// dates as calendar quarters.
func quarterStartAt(t time.Time, quarterStart time.Month) time.Time {
	if quarterStart < time.January || quarterStart > time.December {
		quarterStart = time.January
	}
	absoluteMonth := t.Year()*12 + int(t.Month()) - 1
	monthsIntoQuarter := ((absoluteMonth-(int(quarterStart)-1))%3 + 3) % 3
	start := absoluteMonth - monthsIntoQuarter
	return time.Date(start/12, time.Month(start%12+1), 1, 0, 0, 0, 0, time.UTC)
}

// RollingWindowStart returns the most recent rolling-window boundary at or
// before now for a window of the given duration anchored at anchor. The result
// is always a lattice point anchor + k*duration for some integer k >= 0.
//
// This is what makes rolling resets agree across a cluster. anchor is a
// persisted, immutable value (a row's CreatedAt), so the returned boundary is a
// pure function of (anchor, duration, now) and every node computes the identical
// instant no matter when its own reset ticker happened to fire. Stamping
// time.Now() instead makes each node's boundary a function of its ticker phase,
// and because that stamp becomes the origin of the node's next window the error
// compounds every cycle and no two nodes ever agree on when a window closed.
//
// Quantizing now also bounds clock skew: a node whose clock runs fast notices
// the boundary early but still writes the same lattice point, so skew has to
// exceed a full duration before it can shift which boundary gets written.
//
// A now before anchor (clock skew or a rollback) clamps to anchor, and a
// non-positive duration returns anchor unchanged, so a caller can never build a
// perpetually-due target (issue #4851 class).
func RollingWindowStart(anchor time.Time, duration time.Duration, now time.Time) time.Time {
	if duration <= 0 {
		return anchor
	}
	elapsed := now.Sub(anchor)
	if elapsed < 0 {
		return anchor
	}
	return anchor.Add((elapsed / duration) * duration)
}

// CountCalendarPeriods returns how many calendar-period boundaries the interval
// (from, to] crosses for the given duration suffix. It counts exactly the
// boundaries GetCalendarPeriodStart would produce, so "d" counts UTC days, "w"
// counts Mondays, "M" counts months and "Y" counts years.
//
// Like GetCalendarPeriodStart this deliberately ignores the numeric multiplier,
// so "7d" counts days rather than seven-day blocks. The two must agree: a
// mismatch would make a finite override's derived cycle count drift away from
// the cadence at which the budget actually resets.
//
// Returns 0 for a non-calendar suffix and whenever to is not after from.
//
// quarterStart carries the same meaning as in GetCalendarPeriodStart and is
// consulted only for "Q". The two must agree exactly: a mismatch of one makes a
// finite override on a quarterly budget expire a whole quarter early or live a
// quarter too long.
func CountCalendarPeriods(duration string, from, to time.Time, quarterStart time.Month) int {
	if duration == "" || !to.After(from) {
		return 0
	}
	from = from.UTC()
	to = to.UTC()
	switch duration[len(duration)-1:] {
	case "d":
		fromDay := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
		toDay := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
		return int(toDay.Sub(fromDay) / (24 * time.Hour))
	case "w":
		fromWeek := GetCalendarPeriodStart("1w", from, QuarterStartNotApplicable)
		toWeek := GetCalendarPeriodStart("1w", to, QuarterStartNotApplicable)
		return int(toWeek.Sub(fromWeek) / (7 * 24 * time.Hour))
	case "M":
		return (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
	case "Q":
		// Snap both ends with the same helper GetCalendarPeriodStart uses, then
		// divide the whole-month gap by three. Deriving the count from the
		// boundaries rather than restating the arithmetic is what guarantees the
		// two functions cannot drift apart.
		fromQuarter := quarterStartAt(from, quarterStart)
		toQuarter := quarterStartAt(to, quarterStart)
		months := (toQuarter.Year()-fromQuarter.Year())*12 + int(toQuarter.Month()) - int(fromQuarter.Month())
		return months / 3
	case "Y":
		return to.Year() - from.Year()
	default:
		return 0
	}
}

// ParseDuration function to parse duration strings
func ParseDuration(duration string) (time.Duration, error) {
	if duration == "" {
		return 0, fmt.Errorf("duration is empty")
	}

	// Handle special cases for days, weeks, months, years
	switch {
	case duration[len(duration)-1:] == "d":
		days := duration[:len(duration)-1]
		if d, err := time.ParseDuration(days + "h"); err == nil {
			return d * 24, nil
		}
		return 0, fmt.Errorf("invalid day duration: %s", duration)
	case duration[len(duration)-1:] == "w":
		weeks := duration[:len(duration)-1]
		if w, err := time.ParseDuration(weeks + "h"); err == nil {
			return w * 24 * 7, nil
		}
		return 0, fmt.Errorf("invalid week duration: %s", duration)
	case duration[len(duration)-1:] == "M":
		months := duration[:len(duration)-1]
		if m, err := time.ParseDuration(months + "h"); err == nil {
			return m * 24 * 30, nil // Approximate month as 30 days
		}
		return 0, fmt.Errorf("invalid month duration: %s", duration)
	case duration[len(duration)-1:] == "Q":
		quarters := duration[:len(duration)-1]
		if q, err := time.ParseDuration(quarters + "h"); err == nil {
			return q * 24 * 90, nil // Approximate quarter as 90 days
		}
		return 0, fmt.Errorf("invalid quarter duration: %s", duration)
	case duration[len(duration)-1:] == "Y":
		years := duration[:len(duration)-1]
		if y, err := time.ParseDuration(years + "h"); err == nil {
			return y * 24 * 365, nil // Approximate year as 365 days
		}
		return 0, fmt.Errorf("invalid year duration: %s", duration)
	default:
		return time.ParseDuration(duration)
	}
}
