package tables

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRateLimitNextResets verifies each counter reports its own next reset from its
// own duration and last reset, nil when it has no reset duration, and that quarterly
// windows use calendar quarters since rate limits carry no quarter definition.
func TestRateLimitNextResets(t *testing.T) {
	now := utcDate(2026, time.November, 15, 10) // a Sunday
	str := func(s string) *string { return &s }
	at := func(t time.Time) *time.Time { return &t }
	tests := []struct {
		name       string
		rl         TableRateLimit
		token, req *time.Time
	}{
		{name: "no reset durations", rl: TableRateLimit{CreatedAt: utcDate(2026, time.November, 1, 0)}},
		{
			name:  "token only",
			rl:    TableRateLimit{TokenResetDuration: str("1d"), IsCalendarAligned: true},
			token: at(utcDate(2026, time.November, 16, 0)),
		},
		{
			name: "request only",
			rl:   TableRateLimit{RequestResetDuration: str("1d"), CreatedAt: utcDate(2026, time.November, 10, 6)},
			req:  at(utcDate(2026, time.November, 16, 6)),
		},
		{
			name:  "both calendar aligned",
			rl:    TableRateLimit{TokenResetDuration: str("1w"), RequestResetDuration: str("1M"), IsCalendarAligned: true},
			token: at(utcDate(2026, time.November, 16, 0)),
			req:   at(utcDate(2026, time.December, 1, 0)),
		},
		{
			name:  "both rolling from created_at",
			rl:    TableRateLimit{TokenResetDuration: str("1d"), RequestResetDuration: str("1h"), CreatedAt: utcDate(2026, time.November, 10, 6)},
			token: at(utcDate(2026, time.November, 16, 6)),
			req:   at(utcDate(2026, time.November, 15, 11)),
		},
		{
			name: "each counter anchors on its own last reset when created_at is zero",
			rl: TableRateLimit{
				TokenResetDuration: str("1h"), TokenLastReset: time.Date(2026, time.November, 15, 9, 0, 0, 0, time.UTC),
				RequestResetDuration: str("1h"), RequestLastReset: time.Date(2026, time.November, 15, 9, 30, 0, 0, time.UTC),
			},
			token: at(utcDate(2026, time.November, 15, 11)),
			req:   at(time.Date(2026, time.November, 15, 10, 30, 0, 0, time.UTC)),
		},
		{
			name: "quarter uses calendar quarters",
			rl:   TableRateLimit{RequestResetDuration: str("1Q"), IsCalendarAligned: true},
			req:  at(utcDate(2027, time.January, 1, 0)),
		},
		{
			name: "unparseable duration has no next reset",
			rl:   TableRateLimit{TokenResetDuration: str("soon"), RequestResetDuration: str("1d"), IsCalendarAligned: true},
			req:  at(utcDate(2026, time.November, 16, 0)),
		},
	}
	check := func(t *testing.T, label string, got, want *time.Time) {
		t.Helper()
		if want == nil {
			assert.Nil(t, got, label)
			return
		}
		require.NotNil(t, got, label)
		assert.True(t, got.Equal(*want), "%s: got %v, want %v", label, *got, *want)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, req := tt.rl.NextResets(now)
			check(t, "token", token, tt.token)
			check(t, "request", req, tt.req)
		})
	}

	t.Run("nil rate limit", func(t *testing.T) {
		var rl *TableRateLimit
		token, req := rl.NextResets(now)
		assert.Nil(t, token)
		assert.Nil(t, req)
	})
}
