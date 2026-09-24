package logstore

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Comma-list ranking dimensions: labels stored several to a row in one
// comma-separated text column - the routing engines a request passed through,
// the function names its response called.
//
// The split happens in Go rather than in SQL, which is the precedent this
// package already sets for these two columns (GetDistinctToolCallNames and
// splitCommaListValues read the joined strings and split them here). SQL groups
// by the joined string, of which there are only as many as there are distinct
// combinations, and each combination's totals are then added to every name in
// it. That keeps one dialect-agnostic query where a SQL-side split would need a
// third fan-out style per backend, for columns small enough not to need it.
//
// One request counts under every name on its row, so TotalAttributedRequests
// can exceed TotalActualRequests - the relationship the array-backed
// dimensions (team, fail_reason) already have. Like the JSON-field dimensions
// there is no Unassigned bucket: a request that called no tool is not "an
// unassigned tool call".
var commaListDimensions = map[RankingDimension]string{
	RankingDimensionRoutingEngine: "routing_engines_used",
	RankingDimensionToolCallName:  "tool_call_names",
}

type commaListTotals struct {
	requests int64
	tokens   int64
	cost     float64
}

// commaListDimensionTotals returns per-name totals over filters, along with how
// many name attributions they add up to.
func (s *RDBLogStore) commaListDimensionTotals(ctx context.Context, filters SearchFilters, column string) (map[string]*commaListTotals, error) {
	var combinations []struct {
		Joined        string          `gorm:"column:joined"`
		TotalRequests int64           `gorm:"column:total_requests"`
		TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
		TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
	}
	query := s.ScopedDB(ctx).Model(&Log{})
	query = s.applyFilters(query, filters)
	// column is one of the constants above, never caller input.
	query = query.Where("status IN ?", terminalLogStatuses).
		Where(fmt.Sprintf("%s IS NOT NULL AND %s != ''", column, column))
	if err := query.
		Select(fmt.Sprintf("%s as joined, COUNT(*) as total_requests, SUM(total_tokens) as total_tokens, COALESCE(SUM(cost), 0) as total_cost", column)).
		Group(column).
		Find(&combinations).Error; err != nil {
		return nil, err
	}

	totals := map[string]*commaListTotals{}
	for _, combination := range combinations {
		seen := map[string]struct{}{}
		for name := range strings.SplitSeq(combination.Joined, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			// A name repeated on one row is still one request under that name.
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			entry, ok := totals[name]
			if !ok {
				entry = &commaListTotals{}
				totals[name] = entry
			}
			entry.requests += combination.TotalRequests
			entry.tokens += combination.TotalTokens.Int64
			entry.cost += combination.TotalCost.Float64
		}
	}
	return totals, nil
}

// GetCommaListDimensionRankings ranks a comma-list dimension by requests, with
// the same trend against the preceding period every other ranking carries.
func (s *RDBLogStore) GetCommaListDimensionRankings(ctx context.Context, filters SearchFilters, dimension RankingDimension) (*DimensionRankingResult, error) {
	column, ok := commaListDimensions[dimension]
	if !ok {
		return nil, fmt.Errorf("invalid ranking dimension: %s", dimension)
	}

	current, err := s.commaListDimensionTotals(ctx, filters, column)
	if err != nil {
		return nil, fmt.Errorf("failed to get dimension rankings for %s: %w", dimension, err)
	}

	result := &DimensionRankingResult{Rankings: []DimensionRankingWithTrend{}, Dimension: dimension}
	actualQuery := s.applyFilters(s.ScopedDB(ctx).Model(&Log{}), filters).Where("status IN ?", terminalLogStatuses)
	if err := actualQuery.Count(&result.TotalActualRequests).Error; err != nil {
		return nil, fmt.Errorf("failed to get dimension ranking totals for %s: %w", dimension, err)
	}
	if len(current) == 0 {
		return result, nil
	}

	previous := map[string]*commaListTotals{}
	if filters.StartTime != nil && filters.EndTime != nil {
		duration := filters.EndTime.Sub(*filters.StartTime)
		prevStart := filters.StartTime.Add(-duration)
		prevEnd := filters.StartTime.Add(-time.Nanosecond)
		prevFilters := filters
		prevFilters.StartTime, prevFilters.EndTime = &prevStart, &prevEnd
		if previous, err = s.commaListDimensionTotals(ctx, prevFilters, column); err != nil {
			return nil, fmt.Errorf("failed to get previous period dimension rankings for %s: %w", dimension, err)
		}
	}

	names := make([]string, 0, len(current))
	for name, totals := range current {
		names = append(names, name)
		result.TotalAttributedRequests += totals.requests
	}
	// Most requests first, name as the tiebreak - the order every other ranking
	// uses, applied here because the grouping SQL could not.
	slices.SortFunc(names, func(a, b string) int {
		if byRequests := cmp.Compare(current[b].requests, current[a].requests); byRequests != 0 {
			return byRequests
		}
		return cmp.Compare(a, b)
	})
	// Capped after the split: the cap is on names, and a cap on the raw
	// combinations would drop names arbitrarily.
	if limit := filters.EffectiveRankingLimit(defaultMaxRankingsLimit); limit > 0 && len(names) > limit {
		names = names[:limit]
	}

	for _, name := range names {
		totals := current[name]
		var trend DimensionRankingTrend
		if prev, exists := previous[name]; exists && prev.requests > 0 {
			trend.HasPreviousPeriod = true
			trend.RequestsTrend = pctChange(float64(prev.requests), float64(totals.requests))
			trend.TokensTrend = metricTrend(float64(prev.tokens), float64(totals.tokens))
			trend.CostTrend = metricTrend(prev.cost, totals.cost)
		}
		result.Rankings = append(result.Rankings, DimensionRankingWithTrend{
			DimensionRankingEntry: DimensionRankingEntry{
				ID: name, Name: name, TotalRequests: totals.requests, TotalTokens: totals.tokens, TotalCost: totals.cost,
			},
			Trend: trend,
		})
	}
	return result, nil
}
