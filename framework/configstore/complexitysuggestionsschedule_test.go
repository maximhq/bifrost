package configstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scheduleTime(t *testing.T, zone, value string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	require.NoError(t, err)
	at, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	require.NoError(t, err)
	return at
}

func TestComplexitySuggestionsScheduleSlots(t *testing.T) {
	cases := []struct {
		name       string
		schedule   ComplexitySuggestionsSchedule
		zone, now  string
		last, next string
	}{
		{"default daily before the hour", ComplexitySuggestionsSchedule{}, "UTC", "2026-11-04 02:59", "2026-11-03 03:00", "2026-11-04 03:00"},
		{"default daily at the hour", ComplexitySuggestionsSchedule{}, "UTC", "2026-11-04 03:00", "2026-11-04 03:00", "2026-11-05 03:00"},
		{"six hourly", ComplexitySuggestionsSchedule{Every: "6h"}, "UTC", "2026-11-04 13:10", "2026-11-04 12:00", "2026-11-04 18:00"},
		{"six hourly crosses midnight", ComplexitySuggestionsSchedule{Every: "6h"}, "UTC", "2026-11-04 23:10", "2026-11-04 18:00", "2026-11-05 00:00"},
		// 2026-11-05 is day 20762 since the epoch, which is even.
		{"every other day", ComplexitySuggestionsSchedule{Every: "2d", At: "01:00"}, "UTC", "2026-11-06 09:00", "2026-11-05 01:00", "2026-11-07 01:00"},
		{"weekly on a weekday", ComplexitySuggestionsSchedule{Every: "1w", At: "08:00", Weekday: "friday"}, "UTC", "2026-11-04 12:00", "2026-10-30 08:00", "2026-11-06 08:00"},
		{"in the schedule's zone", ComplexitySuggestionsSchedule{Every: "1d", At: "03:00", Timezone: "Asia/Kolkata"}, "Asia/Kolkata", "2026-11-04 10:00", "2026-11-04 03:00", "2026-11-05 03:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := scheduleTime(t, tc.zone, tc.now)
			last, err := tc.schedule.LastSlot(now)
			require.NoError(t, err)
			next, err := tc.schedule.NextSlot(now)
			require.NoError(t, err)
			assert.True(t, last.Equal(scheduleTime(t, tc.zone, tc.last)), "last: got %s", last)
			assert.True(t, next.Equal(scheduleTime(t, tc.zone, tc.next)), "next: got %s", next)
		})
	}
}

// A daily run keeps its wall-clock time across a daylight-saving change, and
// alternate weeks stay alternate across a year boundary.
func TestComplexitySuggestionsScheduleCalendar(t *testing.T) {
	daily := ComplexitySuggestionsSchedule{Every: "1d", At: "03:00", Timezone: "America/New_York"}
	// US daylight saving ended on 2026-11-01 at 02:00.
	before, err := daily.LastSlot(scheduleTime(t, "America/New_York", "2026-10-31 12:00"))
	require.NoError(t, err)
	after, err := daily.NextSlot(before)
	require.NoError(t, err)
	assert.Equal(t, 3, after.In(before.Location()).Hour(), "still 03:00 local after the change")
	assert.Equal(t, 25*time.Hour, after.Sub(before), "the day of the change is 25 hours long")

	fortnightly := ComplexitySuggestionsSchedule{Every: "2w", At: "00:00", Weekday: "monday"}
	slot := scheduleTime(t, "UTC", "2026-12-20 00:00")
	for range 4 {
		next, err := fortnightly.NextSlot(slot)
		require.NoError(t, err)
		if !slot.Equal(scheduleTime(t, "UTC", "2026-12-20 00:00")) {
			assert.Equal(t, 14*24*time.Hour, next.Sub(slot))
		}
		assert.Equal(t, time.Monday, next.Weekday())
		slot = next
	}

	_, err = ComplexitySuggestionsSchedule{Every: "7h"}.LastSlot(time.Now())
	require.Error(t, err, "an invalid schedule has no slots")
}
