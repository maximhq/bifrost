package configstore

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ComplexitySuggestionsSchedule is when phrase-suggestion runs start.
//
// Every is a count and a unit, in the budget reset format: hours ("6h", run at
// midnight and every six hours after, so the hours must divide a day), days
// ("1d", "2d", at At) or weeks ("1w", on Weekday at At). At and Weekday are
// wall-clock settings in Timezone, so a daily 03:00 run stays at 03:00 across
// a daylight-saving change; a time the change skips runs at the next valid
// time instead.
//
// Slots are aligned to the calendar, not to when the schedule was saved: a
// "2d" schedule runs on days whose number since 1970-01-01 is even in
// Timezone, and a "2w" one on alternate weeks counted the same way. That makes
// every node compute the same slot without sharing state.
type ComplexitySuggestionsSchedule struct {
	Every    string `json:"every,omitempty"`    // "6h", "1d", "1w"; default 1d
	At       string `json:"at,omitempty"`       // "HH:MM"; days and weeks only; default 03:00
	Weekday  string `json:"weekday,omitempty"`  // weeks only; default monday
	Timezone string `json:"timezone,omitempty"` // IANA name; default UTC
}

const (
	// DefaultComplexitySuggestionsEvery runs once a day.
	DefaultComplexitySuggestionsEvery = "1d"
	// DefaultComplexitySuggestionsAt runs at a quiet hour.
	DefaultComplexitySuggestionsAt = "03:00"
	// DefaultComplexitySuggestionsWeekday is the day weekly runs take.
	DefaultComplexitySuggestionsWeekday = "monday"
	// DefaultComplexitySuggestionsTimezone is the zone a schedule without one
	// is read in.
	DefaultComplexitySuggestionsTimezone = "UTC"
	// maxComplexitySuggestionsEveryDays and ...Weeks bound the interval: a
	// run further apart than the longest lookback would skip traffic.
	maxComplexitySuggestionsEveryDays  = 30
	maxComplexitySuggestionsEveryWeeks = 4
)

// complexitySuggestionsWeekdays maps the accepted weekday names.
var complexitySuggestionsWeekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

// UnmarshalJSON rejects unknown fields.
func (s *ComplexitySuggestionsSchedule) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for field := range fields {
		switch field {
		case "every", "at", "weekday", "timezone":
		default:
			return fmt.Errorf("unknown complexity suggestions schedule field %q", field)
		}
	}
	type plain ComplexitySuggestionsSchedule
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = ComplexitySuggestionsSchedule(decoded)
	return nil
}

// parseComplexitySuggestionsEvery splits Every into its count and unit.
func parseComplexitySuggestionsEvery(every string) (int, byte, error) {
	if len(every) < 2 {
		return 0, 0, fmt.Errorf("suggestions schedule every must look like 6h, 1d or 1w, got %q", every)
	}
	unit := every[len(every)-1]
	n, err := strconv.Atoi(every[:len(every)-1])
	if err != nil || n < 1 {
		return 0, 0, fmt.Errorf("suggestions schedule every must look like 6h, 1d or 1w, got %q", every)
	}
	switch unit {
	case 'h':
		if n >= 24 || 24%n != 0 {
			return 0, 0, fmt.Errorf("suggestions schedule every in hours must divide a day (1, 2, 3, 4, 6, 8 or 12h; use 1d for daily), got %q", every)
		}
	case 'd':
		if n > maxComplexitySuggestionsEveryDays {
			return 0, 0, fmt.Errorf("suggestions schedule every must be at most %dd, got %q", maxComplexitySuggestionsEveryDays, every)
		}
	case 'w':
		if n > maxComplexitySuggestionsEveryWeeks {
			return 0, 0, fmt.Errorf("suggestions schedule every must be at most %dw, got %q", maxComplexitySuggestionsEveryWeeks, every)
		}
	default:
		return 0, 0, fmt.Errorf("suggestions schedule every must end in h, d or w, got %q", every)
	}
	return n, unit, nil
}

// parseComplexitySuggestionsAt reads "HH:MM".
func parseComplexitySuggestionsAt(at string) (int, int, error) {
	hh, mm, ok := strings.Cut(at, ":")
	hour, errH := strconv.Atoi(hh)
	minute, errM := strconv.Atoi(mm)
	if !ok || len(hh) != 2 || len(mm) != 2 || errH != nil || errM != nil || hour > 23 || minute > 59 || hour < 0 || minute < 0 {
		return 0, 0, fmt.Errorf("suggestions schedule at must be HH:MM in 24-hour time, got %q", at)
	}
	return hour, minute, nil
}

// normalized returns a copy with defaults filled in for the unit Every names;
// a nil schedule is the default one. Settings that do not apply to the unit
// are left for Validate to refuse rather than dropped.
func (s *ComplexitySuggestionsSchedule) normalized() *ComplexitySuggestionsSchedule {
	out := ComplexitySuggestionsSchedule{}
	if s != nil {
		out = *s
	}
	out.Every = strings.ToLower(strings.TrimSpace(out.Every))
	out.At = strings.TrimSpace(out.At)
	out.Weekday = strings.ToLower(strings.TrimSpace(out.Weekday))
	out.Timezone = strings.TrimSpace(out.Timezone)
	if out.Every == "" {
		out.Every = DefaultComplexitySuggestionsEvery
	}
	if out.Timezone == "" {
		out.Timezone = DefaultComplexitySuggestionsTimezone
	}
	if _, unit, err := parseComplexitySuggestionsEvery(out.Every); err == nil && unit != 'h' {
		if out.At == "" {
			out.At = DefaultComplexitySuggestionsAt
		}
		if unit == 'w' && out.Weekday == "" {
			out.Weekday = DefaultComplexitySuggestionsWeekday
		}
	}
	return &out
}

// Validate checks a normalized schedule.
func (s *ComplexitySuggestionsSchedule) Validate() error {
	if s == nil {
		return nil
	}
	_, unit, err := parseComplexitySuggestionsEvery(s.Every)
	if err != nil {
		return err
	}
	if unit == 'h' && s.At != "" {
		return fmt.Errorf("suggestions schedule at applies to daily and weekly schedules, not %q", s.Every)
	}
	if s.At != "" {
		if _, _, err := parseComplexitySuggestionsAt(s.At); err != nil {
			return err
		}
	}
	if unit != 'w' && s.Weekday != "" {
		return fmt.Errorf("suggestions schedule weekday applies to weekly schedules, not %q", s.Every)
	}
	if s.Weekday != "" {
		if _, ok := complexitySuggestionsWeekdays[s.Weekday]; !ok {
			return fmt.Errorf("suggestions schedule weekday must be a day name such as monday, got %q", s.Weekday)
		}
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("suggestions schedule timezone %q is not a known IANA time zone", s.Timezone)
	}
	return nil
}

// LastSlot returns the most recent scheduled start at or before now.
func (s ComplexitySuggestionsSchedule) LastSlot(now time.Time) (time.Time, error) {
	return s.findSlot(now, -1)
}

// NextSlot returns the first scheduled start after now.
func (s ComplexitySuggestionsSchedule) NextSlot(now time.Time) (time.Time, error) {
	return s.findSlot(now, 1)
}

// findSlot walks calendar days from now's in the schedule's zone, backwards
// (dir -1, the latest slot at or before now) or forwards (dir 1, the first
// slot after now). The longest interval is four weeks, so the walk is bounded.
func (s ComplexitySuggestionsSchedule) findSlot(now time.Time, dir int) (time.Time, error) {
	norm := s.normalized()
	if err := norm.Validate(); err != nil {
		return time.Time{}, err
	}
	n, unit, _ := parseComplexitySuggestionsEvery(norm.Every)
	loc, _ := time.LoadLocation(norm.Timezone)
	hour, minute := 0, 0
	if norm.At != "" {
		hour, minute, _ = parseComplexitySuggestionsAt(norm.At)
	}
	local := now.In(loc)
	for step := 0; step <= 7*maxComplexitySuggestionsEveryWeeks+1; step++ {
		y, m, d := local.Date()
		day := time.Date(y, m, d+dir*step, 0, 0, 0, 0, loc)
		var candidates []time.Time
		switch unit {
		case 'h':
			for h := 0; h < 24; h += n {
				candidates = append(candidates, time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, loc))
			}
		case 'd':
			if complexitySuggestionsDayNumber(day)%n == 0 {
				candidates = append(candidates, time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc))
			}
		case 'w':
			// Weeks are counted from the Monday of 1970-01-01's week.
			if day.Weekday() == complexitySuggestionsWeekdays[norm.Weekday] && ((complexitySuggestionsDayNumber(day)+3)/7)%n == 0 {
				candidates = append(candidates, time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc))
			}
		}
		if dir < 0 {
			for i := len(candidates) - 1; i >= 0; i-- {
				if !candidates[i].After(now) {
					return candidates[i], nil
				}
			}
		} else {
			for _, c := range candidates {
				if c.After(now) {
					return c, nil
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("suggestions schedule %q has no slot within %d weeks", norm.Every, maxComplexitySuggestionsEveryWeeks)
}

// complexitySuggestionsDayNumber is the calendar date's day count since
// 1970-01-01, independent of the zone it was read in.
func complexitySuggestionsDayNumber(day time.Time) int {
	y, m, d := day.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}
