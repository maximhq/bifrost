package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestGetModelConfigsByScopeAndScopeIDsStampsRateLimit covers the batched loader
// the virtual key quota endpoint reads through. loadModelConfigRelations attaches
// the rate limit after the configs are scanned, so AfterFind never sees it; the
// loader has to stamp the owner's calendar alignment onto it itself, or a
// calendar-aligned config reports a rolling next reset for its rate limit.
func TestGetModelConfigsByScopeAndScopeIDsStampsRateLimit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&tables.TableRateLimit{}, &tables.TableBudget{}, &tables.TableModelConfig{}))

	str := func(v string) *string { return &v }
	i64 := func(v int64) *int64 { return &v }
	for _, id := range []string{"rl-aligned", "rl-rolling"} {
		rl := tables.TableRateLimit{ID: id, TokenMaxLimit: i64(1000), TokenResetDuration: str("1d")}
		require.NoError(t, db.Create(&rl).Error)
	}
	configs := []tables.TableModelConfig{
		{ID: "mc-aligned", Scope: "virtual_key", ScopeID: str("vk1"), ModelName: "gpt-4o", CalendarAligned: true, RateLimitID: str("rl-aligned")},
		{ID: "mc-rolling", Scope: "virtual_key", ScopeID: str("vk1"), ModelName: "claude", CalendarAligned: false, RateLimitID: str("rl-rolling")},
	}
	for i := range configs {
		require.NoError(t, db.Create(&configs[i]).Error)
	}

	store := &RDBConfigStore{}
	store.db.Store(db)

	got, err := store.GetModelConfigsByScopeAndScopeIDs(context.Background(), "virtual_key", []string{"vk1"})
	require.NoError(t, err)
	require.Len(t, got, 2)

	byID := make(map[string]tables.TableModelConfig, len(got))
	for _, mc := range got {
		byID[mc.ID] = mc
	}

	aligned := byID["mc-aligned"]
	require.NotNil(t, aligned.RateLimit, "the loader must attach the rate limit")
	require.True(t, aligned.RateLimit.IsCalendarAligned, "a calendar-aligned config must stamp its rate limit")

	rolling := byID["mc-rolling"]
	require.NotNil(t, rolling.RateLimit, "the loader must attach the rate limit")
	require.False(t, rolling.RateLimit.IsCalendarAligned, "a rolling config must leave its rate limit rolling")
}
