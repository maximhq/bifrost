package logstore

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Model-less rows (list_models fan-out, file/batch/video ops) must not surface
// as an empty-named series, matching what GetModelRankings already excludes.
func TestGetModelHistogramExcludesEmptyModel(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now().UTC()
	seed := []Log{
		{ID: "chat-1", Timestamp: now, Object: "chat_completion", Provider: "openai", Model: "gpt-4o", Status: "success"},
		{ID: "chat-2", Timestamp: now, Object: "chat_completion", Provider: "openai", Model: "gpt-4o", Status: "error"},
		{ID: "list-1", Timestamp: now, Object: "list_models", Provider: "openai", Model: "", Status: "success"},
		{ID: "list-2", Timestamp: now, Object: "list_models", Provider: "anthropic", Model: "", Status: "success"},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}
	s := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}

	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	result, err := s.GetModelHistogram(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, 3600)
	require.NoError(t, err)

	require.Equal(t, []string{"gpt-4o"}, result.Models)
	var total int64
	for _, b := range result.Buckets {
		require.NotContains(t, b.ByModel, "")
		for _, stats := range b.ByModel {
			total += stats.Total
		}
	}
	require.EqualValues(t, 2, total)
}
