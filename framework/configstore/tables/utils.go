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
