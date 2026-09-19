package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AggregationInfo describes retained-history coverage and effective inclusive bounds.
type AggregationInfo struct {
	EffectiveStartTime        *time.Time `json:"effective_start_time,omitempty"`
	EffectiveEndTime          *time.Time `json:"effective_end_time,omitempty"`
	ArchivedResolutionSeconds int64      `json:"archived_resolution_seconds,omitempty"`
	Coverage                  string     `json:"coverage"`
}

// AggregateMetadata adds optional archive coverage to existing response objects.
type AggregateMetadata struct {
	AggregationInfo *AggregationInfo `json:"aggregation_info,omitempty" gorm:"-"`
}

// setAggregationInfo attaches coverage without changing existing metric fields.
func (m *AggregateMetadata) setAggregationInfo(info *AggregationInfo) { m.AggregationInfo = info }

// hourlyReadEnabled recognizes persisted archives even when maintenance is paused.
func (s *RDBLogStore) hourlyReadEnabled(ctx context.Context) (bool, error) {
	if s.db.Dialector.Name() != "postgres" {
		return false, nil
	}
	if s.hourlyArchiveKnown.Load() {
		return true, nil
	}
	if !s.hourlyArchiveRequested {
		return false, nil
	}
	db, err := s.db.DB()
	if err != nil {
		return false, err
	}
	exists, err := hourlyStateExists(ctx, db)
	if err == nil && exists {
		s.hourlyArchiveKnown.Store(true)
	}
	return exists, err
}

// prepareHourlyQuery expands only archived boundary hours. Unsupported filters
// remain raw-only and explicitly report that retained history is incomplete.
func (s *RDBLogStore) prepareHourlyQuery(ctx context.Context, filters SearchFilters, allowArchive bool) (SearchFilters, *AggregationInfo, error) {
	enabled, err := s.hourlyReadEnabled(ctx)
	if err != nil || !enabled {
		return filters, nil, err
	}
	var bounds struct{ First, Last sql.NullTime }
	err = s.db.WithContext(ctx).Raw(`SELECT MIN(hour) AS first, MAX(hour)+interval '1 hour'-interval '1 microsecond' AS last
	 FROM bifrost_hourly_hours WHERE frozen AND (?::timestamptz IS NULL OR hour+interval '1 hour'>?)
	 AND (?::timestamptz IS NULL OR hour<=?)`, filters.StartTime, filters.StartTime, filters.EndTime, filters.EndTime).Scan(&bounds).Error
	if err != nil || !bounds.First.Valid {
		return filters, nil, err
	}
	info := &AggregationInfo{EffectiveStartTime: filters.StartTime, EffectiveEndTime: filters.EndTime, Coverage: "retained_only"}
	if !allowArchive || !canUseMatViewFilters(filters) {
		filters.hourlyRawOnly = true
		return filters, info, nil
	}
	if !s.matViewsReady.Load() {
		db, err := s.db.DB()
		if err != nil {
			return filters, nil, err
		}
		if err := validateHourlyArchive(ctx, db); err != nil {
			s.triggerMatViewSelfHeal()
			return filters, nil, fmt.Errorf("archived aggregates unavailable: %w", err)
		}
		s.matViewsReady.Store(true)
	}
	filters.hourlyArchive = true
	if filters.StartTime != nil && bounds.First.Time.Before(*filters.StartTime) {
		filters.StartTime = &bounds.First.Time
	}
	if filters.EndTime != nil && bounds.Last.Time.After(*filters.EndTime) {
		filters.EndTime = &bounds.Last.Time
	}
	info.Coverage = "complete"
	info.ArchivedResolutionSeconds = 3600
	info.EffectiveStartTime, info.EffectiveEndTime = filters.StartTime, filters.EndTime
	return filters, info, nil
}

// readHourlyAggregate applies the same archive contract to every aggregate endpoint.
func readHourlyAggregate[T interface{ setAggregationInfo(*AggregationInfo) }](ctx context.Context, s *RDBLogStore, filters SearchFilters, bucket int64, allowArchive bool, read func(SearchFilters, int64) (T, error)) (T, error) {
	var zero T
	filters, info, err := s.prepareHourlyQuery(ctx, filters, allowArchive)
	if err != nil {
		return zero, err
	}
	if filters.hourlyArchive && bucket != 0 {
		// Whole-hour multiples avoid representing archived counts as sub-hour data.
		if bucket < 3600 {
			bucket = 3600
		} else if bucket%3600 != 0 {
			bucket = ((bucket / 3600) + 1) * 3600
		}
	}
	result, err := read(filters, bucket)
	if err == nil {
		result.setAggregationInfo(info)
	}
	return result, err
}

// applyHourlyInterior includes complete mutable buckets and whole archived boundaries.
func applyHourlyInterior(q *gorm.DB, start, end *time.Time, archived bool) *gorm.DB {
	if !archived {
		return applyInteriorBucketWindow(q, start, end)
	}
	var conditions []string
	var args []any
	if start != nil {
		conditions = append(conditions, "hour >= ?")
		args = append(args, *start)
		q = q.Where("hour + interval '1 hour' > ?", *start)
	}
	if end != nil {
		conditions = append(conditions, "hour < bifrost_hourly_bucket(?)")
		args = append(args, *end)
		q = q.Where("hour <= ?", *end)
	}
	if len(conditions) != 0 {
		q = q.Where("("+strings.Join(conditions, " AND ")+`) OR EXISTS
		 (SELECT 1 FROM bifrost_hourly_hours h WHERE h.hour=mv_logs_hourly.hour AND h.frozen)`, args...)
	}
	return q
}

// hourlyBoundarySlivers uses the archive's pinned grid for exact mutable boundaries.
func hourlyBoundarySlivers(start, end *time.Time, archived bool) (string, []any) {
	query, args := boundarySliverWhere(start, end)
	if archived {
		query = strings.ReplaceAll(query, "date_trunc('hour', timestamp)", "bifrost_hourly_bucket(timestamp)")
		query = strings.ReplaceAll(query, "date_trunc('hour', ?::timestamptz)", "bifrost_hourly_bucket(?::timestamptz)")
	}
	return query, args
}

// hourlyAggregateSource supplies exact mutable boundary aggregates to histogram
// and ranking readers while retaining complete frozen hours. Unreferenced metric
// expressions can be pruned by PostgreSQL for readers that only need sums.
func (s *RDBLogStore) hourlyAggregateSource(ctx context.Context, filters SearchFilters) *gorm.DB {
	base := s.scopedLogsDB(ctx)
	if !filters.hourlyArchive || (filters.StartTime == nil && filters.EndTime == nil) {
		return base.Table("mv_logs_hourly")
	}
	var boundaries []string
	var args []any
	if filters.StartTime != nil {
		boundaries = append(boundaries, "(v.hour=bifrost_hourly_bucket(?::timestamptz) AND v.hour<?)")
		args = append(args, *filters.StartTime, *filters.StartTime)
	}
	if filters.EndTime != nil {
		boundaries = append(boundaries, "v.hour=bifrost_hourly_bucket(?::timestamptz)")
		args = append(args, *filters.EndTime)
	}
	slivers, sliverArgs := hourlyBoundarySlivers(filters.StartTime, filters.EndTime, true)
	// A short mutable range can have overlapping boundary predicates; OR keeps
	// each raw row unique and the exact range below clips both ends.
	if filters.StartTime != nil {
		slivers = "(" + slivers + ") AND timestamp>=?"
		sliverArgs = append(sliverArgs, *filters.StartTime)
	}
	if filters.EndTime != nil {
		slivers = "(" + slivers + ") AND timestamp<=?"
		sliverArgs = append(sliverArgs, *filters.EndTime)
	}
	slivers = "(" + slivers + ") AND NOT EXISTS (SELECT 1 FROM bifrost_hourly_hours h WHERE h.frozen AND h.hour=bifrost_hourly_bucket(timestamp))"
	query := `SELECT v.* FROM mv_logs_hourly v WHERE EXISTS
	 (SELECT 1 FROM bifrost_hourly_hours h WHERE h.hour=v.hour AND h.frozen)
	 OR NOT (` + strings.Join(boundaries, " OR ") + ") UNION ALL " + hourlyRawSelect(slivers)
	args = append(args, sliverArgs...)
	return base.Table("("+query+") AS mv_logs_hourly", args...)
}

// GetStats returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetStats(ctx context.Context, filters SearchFilters) (*SearchStats, error) {
	return readHourlyAggregate(ctx, s, filters, 0, true, func(f SearchFilters, b int64) (*SearchStats, error) {
		return s.getStats(ctx, f)
	})
}

// GetHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*HistogramResult, error) {
		return s.getHistogram(ctx, f, b)
	})
}

// GetTokenHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*TokenHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*TokenHistogramResult, error) {
		return s.getTokenHistogram(ctx, f, b)
	})
}

// GetThroughputHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetThroughputHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ThroughputHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*ThroughputHistogramResult, error) {
		return s.getThroughputHistogram(ctx, f, b)
	})
}

// GetProviderThroughputHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetProviderThroughputHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderThroughputHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*ProviderThroughputHistogramResult, error) {
		return s.getProviderThroughputHistogram(ctx, f, b)
	})
}

// GetCostHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*CostHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*CostHistogramResult, error) {
		return s.getCostHistogram(ctx, f, b)
	})
}

// GetModelHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetModelHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*ModelHistogramResult, error) {
		return s.getModelHistogram(ctx, f, b)
	})
}

// GetLatencyHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*LatencyHistogramResult, error) {
		return s.getLatencyHistogram(ctx, f, b)
	})
}

// GetModelRankings returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetModelRankings(ctx context.Context, filters SearchFilters) (*ModelRankingResult, error) {
	return readHourlyAggregate(ctx, s, filters, 0, true, func(f SearchFilters, b int64) (*ModelRankingResult, error) {
		return s.getModelRankings(ctx, f)
	})
}

// GetUserRankings returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetUserRankings(ctx context.Context, filters SearchFilters) (*UserRankingResult, error) {
	return readHourlyAggregate(ctx, s, filters, 0, true, func(f SearchFilters, b int64) (*UserRankingResult, error) {
		return s.getUserRankings(ctx, f)
	})
}

// GetDimensionRankings returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetDimensionRankings(ctx context.Context, filters SearchFilters, dimension RankingDimension) (*DimensionRankingResult, error) {
	column, name, _ := DimensionColumnDef(dimension)
	return readHourlyAggregate(ctx, s, filters, 0, !s.dimensionReadSourceFor(column, name).Bucketed, func(f SearchFilters, b int64) (*DimensionRankingResult, error) {
		return s.getDimensionRankings(ctx, f, dimension)
	})
}

// GetProviderCostHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetProviderCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderCostHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*ProviderCostHistogramResult, error) {
		return s.getProviderCostHistogram(ctx, f, b)
	})
}

// GetProviderTokenHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetProviderTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderTokenHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*ProviderTokenHistogramResult, error) {
		return s.getProviderTokenHistogram(ctx, f, b)
	})
}

// GetProviderLatencyHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetProviderLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, true, func(f SearchFilters, b int64) (*ProviderLatencyHistogramResult, error) {
		return s.getProviderLatencyHistogram(ctx, f, b)
	})
}

// GetDimensionCostHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetDimensionCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionCostHistogramResult, error) {
	column, _ := histogramDimensionColumn(dimension)
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, !s.dimensionReadSourceFor(column, "").Bucketed, func(f SearchFilters, b int64) (*DimensionCostHistogramResult, error) {
		return s.getDimensionCostHistogram(ctx, f, b, dimension)
	})
}

// GetDimensionTokenHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetDimensionTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionTokenHistogramResult, error) {
	column, _ := histogramDimensionColumn(dimension)
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, !s.dimensionReadSourceFor(column, "").Bucketed, func(f SearchFilters, b int64) (*DimensionTokenHistogramResult, error) {
		return s.getDimensionTokenHistogram(ctx, f, b, dimension)
	})
}

// GetDimensionLatencyHistogram returns aggregates with explicit archived-history coverage.
func (s *RDBLogStore) GetDimensionLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionLatencyHistogramResult, error) {
	column, _ := histogramDimensionColumn(dimension)
	return readHourlyAggregate(ctx, s, filters, bucketSizeSeconds, !s.dimensionReadSourceFor(column, "").Bucketed, func(f SearchFilters, b int64) (*DimensionLatencyHistogramResult, error) {
		return s.getDimensionLatencyHistogram(ctx, f, b, dimension)
	})
}
