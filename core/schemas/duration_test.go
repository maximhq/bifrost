package schemas

import (
	"math"
	"testing"
	"time"
)

func TestDurationFromUnits(t *testing.T) {
	tests := []struct {
		name    string
		n       int64
		unit    time.Duration
		want    time.Duration
		wantErr bool
	}{
		{name: "ten minutes", n: 10, unit: time.Minute, want: 10 * time.Minute},
		{name: "zero", n: 0, unit: time.Minute, want: 0},
		{name: "negative minutes", n: -5, unit: time.Minute, want: -5 * time.Minute},
		{name: "thirty seconds", n: 30, unit: time.Second, want: 30 * time.Second},
		{
			// This is the exact corruption from the original bug report: a raw
			// nanoseconds value (600000000000 for 10 minutes) resent into a field
			// interpreted as minutes overflows int64 when multiplied by time.Minute.
			name:    "raw nanoseconds value overflows when treated as minutes",
			n:       600000000000,
			unit:    time.Minute,
			wantErr: true,
		},
		{
			name:    "max int64 minutes overflows",
			n:       math.MaxInt64,
			unit:    time.Minute,
			wantErr: true,
		},
		{
			name:    "min int64 minutes overflows",
			n:       math.MinInt64,
			unit:    time.Minute,
			wantErr: true,
		},
		{
			name: "largest safe minute value does not overflow",
			n:    int64(math.MaxInt64) / int64(time.Minute),
			unit: time.Minute,
			want: time.Duration(int64(math.MaxInt64)/int64(time.Minute)) * time.Minute,
		},
		{
			name:    "one past the largest safe minute value overflows",
			n:       int64(math.MaxInt64)/int64(time.Minute) + 1,
			unit:    time.Minute,
			wantErr: true,
		},
		{
			name: "smallest safe negative minute value does not overflow",
			n:    int64(math.MinInt64) / int64(time.Minute),
			unit: time.Minute,
			want: time.Duration(int64(math.MinInt64)/int64(time.Minute)) * time.Minute,
		},
		{
			name:    "one past the smallest safe negative minute value overflows",
			n:       int64(math.MinInt64)/int64(time.Minute) - 1,
			unit:    time.Minute,
			wantErr: true,
		},
		{
			name:    "invalid zero unit",
			n:       10,
			unit:    0,
			wantErr: true,
		},
		{
			name:    "invalid negative unit",
			n:       10,
			unit:    -time.Second,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DurationFromUnits(tt.n, tt.unit, "tool_sync_interval")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got duration %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != time.Duration(tt.want) {
				t.Fatalf("got %v, want %v", got, time.Duration(tt.want))
			}
		})
	}
}
