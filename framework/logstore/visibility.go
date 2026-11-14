package logstore

import (
	"context"

	"gorm.io/gorm"
)

type visibilityContextKey string

// HiddenRequestTypesContextKey carries []string request types on dashboard read
// contexts. Writers and background jobs must not set this key.
const HiddenRequestTypesContextKey visibilityContextKey = "logstore-hidden-request-types"

// HiddenRequestTypesFromContext returns the request types excluded from a read.
func HiddenRequestTypesFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	types, _ := ctx.Value(HiddenRequestTypesContextKey).([]string)
	return types
}

// scopedLogsDB composes dashboard visibility with the caller's access scope.
// Only LLM log readers use it: MCP tables do not have an object_type column.
// Filtering here keeps counts, aggregates and pagination on the same row set.
func (s *RDBLogStore) scopedLogsDB(ctx context.Context) *gorm.DB {
	db := s.ScopedDB(ctx)
	if types := HiddenRequestTypesFromContext(ctx); len(types) > 0 {
		db = db.Where("object_type NOT IN ?", types)
	}
	return db
}

func (s *RDBLogStore) canUseFilterMatView(ctx context.Context) bool {
	// Filter-only materialized views omit object_type, unlike mv_logs_hourly.
	return s.db.Dialector.Name() == "postgres" && s.matViewsReady.Load() && len(HiddenRequestTypesFromContext(ctx)) == 0
}
