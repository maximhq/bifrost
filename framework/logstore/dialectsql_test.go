package logstore

import (
	"testing"
	"time"
)

func TestTimestampBoundClickHouseRoundsTowardTheRange(t *testing.T) {
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	inside := base.Add(500 * time.Microsecond)

	cases := []struct {
		name   string
		t      time.Time
		side   timeBoundSide
		wantMs int64
	}{
		{"aligned lower stays", base, lowerTimeBound, base.UnixMilli()},
		{"aligned upper stays", base, upperTimeBound, base.UnixMilli()},
		{"lower inside a millisecond rounds up", inside, lowerTimeBound, base.UnixMilli() + 1},
		{"upper inside a millisecond rounds down", inside, upperTimeBound, base.UnixMilli()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			placeholder, arg := timestampBound("clickhouse", tc.t, tc.side)
			if placeholder != "fromUnixTimestamp64Milli(?)" {
				t.Fatalf("placeholder = %q", placeholder)
			}
			if arg != tc.wantMs {
				t.Fatalf("arg = %v, want %d", arg, tc.wantMs)
			}
		})
	}
}

func TestTimestampBoundOtherDialectsBindTheTimeAsIs(t *testing.T) {
	inside := time.Date(2026, 10, 9, 12, 0, 0, 500_000, time.UTC)
	for _, side := range []timeBoundSide{lowerTimeBound, upperTimeBound} {
		placeholder, arg := timestampBound("postgres", inside, side)
		if placeholder != "?" || arg != inside {
			t.Fatalf("side %d: got (%q, %v), want (\"?\", %v)", side, placeholder, arg, inside)
		}
	}
}
