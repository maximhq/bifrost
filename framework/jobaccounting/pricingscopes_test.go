package jobaccounting

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
)

// TestPricingScopesForLog_CarriesBilledAt guards the one pricing input a
// reprice cannot re-derive from anywhere else. Without BilledAt the time-of-day
// engine falls back to the wall clock, so a model on a peak/off-peak schedule
// reprices to a different cost on every pass.
func TestPricingScopesForLog_CarriesBilledAt(t *testing.T) {
	vk := "vk-1"
	userID := "user-1"
	ts := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)

	scopes := PricingScopesForLog(&logstore.Log{
		Timestamp:     ts,
		Provider:      "deepseek",
		SelectedKeyID: "key-1",
		VirtualKeyID:  &vk,
		UserID:        &userID,
	})

	assert.Equal(t, ts, scopes.BilledAt)
	assert.Equal(t, "deepseek", scopes.Provider)
	assert.Equal(t, "key-1", scopes.SelectedKeyID)
	assert.Equal(t, vk, scopes.VirtualKeyID)
	assert.Equal(t, userID, scopes.UserID)
}

func TestPricingScopesForLog_NilEntry(t *testing.T) {
	assert.True(t, PricingScopesForLog(nil).BilledAt.IsZero())
}
