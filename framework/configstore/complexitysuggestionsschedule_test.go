package configstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func utc(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse("2006-01-02 15:04", value)
	require.NoError(t, err)
	return at
}

// Runs fall on StartAt + k*Every: every node computes the same ones, and the
// time of day comes from the start.
func TestComplexitySuggestionsScheduleSlots(t *testing.T) {
	start := utc(t, "2026-11-01 04:30")
	cases := []struct {
		name       string
		schedule   ComplexitySuggestionsSchedule
		now        string
		last, next string
	}{
		{"default daily before 03:00 UTC", ComplexitySuggestionsSchedule{}, "2026-11-04 02:59", "2026-11-03 03:00", "2026-11-04 03:00"},
		{"default daily at 03:00 UTC", ComplexitySuggestionsSchedule{}, "2026-11-04 03:00", "2026-11-04 03:00", "2026-11-05 03:00"},
		{"daily keeps the start's time of day", ComplexitySuggestionsSchedule{Every: "1d", StartAt: &start}, "2026-11-04 12:00", "2026-11-04 04:30", "2026-11-05 04:30"},
		{"six hourly from the start", ComplexitySuggestionsSchedule{Every: "6h", StartAt: &start}, "2026-11-04 23:10", "2026-11-04 22:30", "2026-11-05 04:30"},
		{"weekly on the start's weekday", ComplexitySuggestionsSchedule{Every: "1w", StartAt: &start}, "2026-11-10 09:00", "2026-11-08 04:30", "2026-11-15 04:30"},
		{"thirty days", ComplexitySuggestionsSchedule{Every: "30d", StartAt: &start}, "2026-12-15 00:00", "2026-12-01 04:30", "2026-12-31 04:30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := utc(t, tc.now)
			last, ok := tc.schedule.LastSlot(now)
			require.True(t, ok)
			assert.True(t, last.Equal(utc(t, tc.last)), "last: got %s", last)
			next := tc.schedule.NextSlot(now)
			assert.True(t, next.Equal(utc(t, tc.next)), "next: got %s", next)
		})
	}
}

// A first run still ahead has no slot before it, and is the next one.
func TestComplexitySuggestionsScheduleFutureStart(t *testing.T) {
	start := utc(t, "2026-12-01 09:00")
	schedule := ComplexitySuggestionsSchedule{Every: "1d", StartAt: &start}
	_, ok := schedule.LastSlot(utc(t, "2026-11-20 09:00"))
	assert.False(t, ok)
	assert.True(t, schedule.NextSlot(utc(t, "2026-11-20 09:00")).Equal(start))
	last, ok := schedule.LastSlot(start)
	require.True(t, ok, "the start itself is a slot")
	assert.True(t, last.Equal(start))
}

// Every is a fixed duration: a daily run keeps its UTC time, so it moves by an
// hour of local time across a daylight-saving change.
func TestComplexitySuggestionsScheduleIsFixedDuration(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	start := time.Date(2026, time.October, 30, 3, 0, 0, 0, newYork)
	schedule := ComplexitySuggestionsSchedule{Every: "1d", StartAt: &start}
	// US daylight saving ended on 2026-11-01 at 02:00.
	next := schedule.NextSlot(time.Date(2026, time.November, 2, 0, 0, 0, 0, newYork))
	assert.Equal(t, 2, next.In(newYork).Hour(), "03:00 EDT is 02:00 EST")
	assert.Equal(t, 0, int(next.Sub(start)%(24*time.Hour)))
}

func TestComplexitySuggestionsScheduleDecodeAndValidate(t *testing.T) {
	var s ComplexitySuggestionsSchedule
	require.NoError(t, json.Unmarshal([]byte(`{"every":"12h","start_at":"2026-11-01T09:30:00+05:30"}`), &s))
	n := s.normalized()
	require.NoError(t, n.Validate())
	assert.Equal(t, "2026-11-01T04:00:00Z", n.StartAt.Format(time.RFC3339), "kept in UTC")
	assert.Equal(t, 12*time.Hour, n.Interval())

	require.ErrorContains(t, json.Unmarshal([]byte(`{"every":"1d","at":"03:00"}`), &s), `unknown complexity suggestions schedule field "at"`)
	require.Error(t, json.Unmarshal([]byte(`{"start_at":"tomorrow"}`), &s))

	assert.Nil(t, (&ComplexitySuggestionsSchedule{}).normalized().StartAt, "an unset start stays unset")
	for _, every := range []string{"1h", "23h", "1d", "30d", "1w", "4w"} {
		assert.NoError(t, (&ComplexitySuggestionsSchedule{Every: every}).Validate(), every)
	}
	for _, every := range []string{"0h", "24h", "31d", "5w", "1m", "d", "-1d"} {
		assert.Error(t, (&ComplexitySuggestionsSchedule{Every: every}).Validate(), every)
	}
	zero := time.Time{}
	assert.Error(t, (&ComplexitySuggestionsSchedule{Every: "1d", StartAt: &zero}).Validate())
}
