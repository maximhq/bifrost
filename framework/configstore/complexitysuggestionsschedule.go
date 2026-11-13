package configstore

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ComplexitySuggestionsSchedule is when phrase-suggestion runs start: at
// StartAt and every Every after it. Every is a count and a unit in the budget
// reset format: hours ("6h"), days ("1d") or weeks ("1w"). StartAt is an
// instant, and picking it picks the time of day the runs keep.
//
// The runs are the lattice StartAt + k*Every, the same on every node with no
// shared state. Every is a fixed duration, so a daily run keeps its UTC time
// and moves by an hour of local time across a daylight-saving change.
type ComplexitySuggestionsSchedule struct {
	Every   string     `json:"every,omitempty"`    // "6h", "1d", "1w"; default 1d
	StartAt *time.Time `json:"start_at,omitempty"` // first run; default 1970-01-01T03:00:00Z
}

const (
	// DefaultComplexitySuggestionsEvery runs once a day.
	DefaultComplexitySuggestionsEvery = "1d"
	// maxComplexitySuggestionsEveryHours, ...Days and ...Weeks bound the
	// interval: hours below a day (a longer one is written in days), and no
	// further apart than the longest lookback, or a run would skip traffic.
	maxComplexitySuggestionsEveryHours = 23
	maxComplexitySuggestionsEveryDays  = 30
	maxComplexitySuggestionsEveryWeeks = 4
)

// DefaultComplexitySuggestionsStartAt anchors a schedule without a first run:
// 03:00 UTC, a quiet hour, so the default is daily at 03:00 UTC.
var DefaultComplexitySuggestionsStartAt = time.Date(1970, time.January, 1, 3, 0, 0, 0, time.UTC)

// UnmarshalJSON rejects unknown fields.
func (s *ComplexitySuggestionsSchedule) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for field := range fields {
		switch field {
		case "every", "start_at":
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

// parseComplexitySuggestionsEvery reads Every as a duration.
func parseComplexitySuggestionsEvery(every string) (time.Duration, error) {
	invalid := fmt.Errorf("suggestions schedule every must look like 6h, 1d or 1w, got %q", every)
	if len(every) < 2 {
		return 0, invalid
	}
	n, err := strconv.Atoi(every[:len(every)-1])
	if err != nil || n < 1 {
		return 0, invalid
	}
	switch every[len(every)-1] {
	case 'h':
		if n > maxComplexitySuggestionsEveryHours {
			return 0, fmt.Errorf("suggestions schedule every in hours must be at most %dh (use days for longer), got %q", maxComplexitySuggestionsEveryHours, every)
		}
		return time.Duration(n) * time.Hour, nil
	case 'd':
		if n > maxComplexitySuggestionsEveryDays {
			return 0, fmt.Errorf("suggestions schedule every must be at most %dd, got %q", maxComplexitySuggestionsEveryDays, every)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		if n > maxComplexitySuggestionsEveryWeeks {
			return 0, fmt.Errorf("suggestions schedule every must be at most %dw, got %q", maxComplexitySuggestionsEveryWeeks, every)
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("suggestions schedule every must end in h, d or w, got %q", every)
}

// normalized returns a canonical copy; a nil schedule is the default one.
// StartAt is kept unset when it was, so a saved config does not grow a
// default it never had; EffectiveSchedule fills it in.
func (s *ComplexitySuggestionsSchedule) normalized() *ComplexitySuggestionsSchedule {
	out := ComplexitySuggestionsSchedule{}
	if s != nil {
		out = *s
	}
	out.Every = strings.ToLower(strings.TrimSpace(out.Every))
	if out.Every == "" {
		out.Every = DefaultComplexitySuggestionsEvery
	}
	if out.StartAt != nil {
		start := out.StartAt.UTC()
		out.StartAt = &start
	}
	return &out
}

// Validate checks a normalized schedule.
func (s *ComplexitySuggestionsSchedule) Validate() error {
	if s == nil {
		return nil
	}
	if _, err := parseComplexitySuggestionsEvery(s.Every); err != nil {
		return err
	}
	if s.StartAt != nil && s.StartAt.IsZero() {
		return fmt.Errorf("suggestions schedule start_at must be a time")
	}
	return nil
}

// Interval is Every as a duration. The schedule must be valid.
func (s ComplexitySuggestionsSchedule) Interval() time.Duration {
	d, _ := parseComplexitySuggestionsEvery(s.normalized().Every)
	return d
}

// Start is the first run: StartAt, or the default anchor.
func (s ComplexitySuggestionsSchedule) Start() time.Time {
	if s.StartAt == nil {
		return DefaultComplexitySuggestionsStartAt
	}
	return s.StartAt.UTC()
}

// LastSlot returns the latest run time at or before now, and false when the
// first run is still ahead. The schedule must be valid.
func (s ComplexitySuggestionsSchedule) LastSlot(now time.Time) (time.Time, bool) {
	start, every := s.Start(), s.Interval()
	if every <= 0 || now.Before(start) {
		return time.Time{}, false
	}
	return start.Add(now.Sub(start) / every * every), true
}

// NextSlot returns the first run time after now. The schedule must be valid.
func (s ComplexitySuggestionsSchedule) NextSlot(now time.Time) time.Time {
	last, ok := s.LastSlot(now)
	if !ok {
		return s.Start()
	}
	return last.Add(s.Interval())
}
