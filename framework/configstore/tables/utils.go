package tables

import (
	"fmt"
	"time"
)

// IsCalendarAlignableDuration reports whether the given duration string supports calendar-aligned resets.
// Only day ("d"), week ("w"), month ("M"), and year ("Y") suffixes have natural calendar boundaries.
// Sub-day durations like "1h", "30m" are not alignable.
func IsCalendarAlignableDuration(duration string) bool {
	if duration == "" {
		return false
	}
	switch duration[len(duration)-1] {
	case 'd', 'w', 'M', 'Y':
		return true
	default:
		return false
	}
}

// GetCalendarPeriodStart returns the start of the current calendar period for the given duration and time.
// For calendar-scale durations (daily, weekly, monthly, yearly) it snaps to clean boundaries in UTC:
//   - "Nd"  → midnight UTC on the current day
//   - "Nw"  → midnight UTC on the most recent Monday
//   - "NM"  → midnight UTC on the 1st of the current month
//   - "NY"  → midnight UTC on Jan 1 of the current year
//
// For all other durations (e.g. "1h", "30m") the original time t is returned unchanged,
// since sub-day periods don't have a natural calendar boundary.
func GetCalendarPeriodStart(duration string, t time.Time) time.Time {
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
	case "Y":
		return time.Date(t.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	default:
		return t
	}
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
func CountCalendarPeriods(duration string, from, to time.Time) int {
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
		fromWeek := GetCalendarPeriodStart("1w", from)
		toWeek := GetCalendarPeriodStart("1w", to)
		return int(toWeek.Sub(fromWeek) / (7 * 24 * time.Hour))
	case "M":
		return (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
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
