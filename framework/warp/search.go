package warp

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

const warpSemanticCandidateLimit = 100

// SemanticSearcher joins the vector index back to the authoritative log store.
// Vector metadata is only a coarse prefilter: every candidate is reloaded using
// the caller's context so queryscope remains the access-control boundary.
type SemanticSearcher struct {
	store   configstore.WarpStore
	vectors vectorstore.VectorStore
	embed   EmbeddingExecutor
	logs    LogReader
}

type SemanticSearchRow struct {
	Score float64 `json:"score"`
	logRow
}

type SemanticSearchResult struct {
	Rows      []SemanticSearchRow `json:"rows"`
	Returned  int                 `json:"returned"`
	Threshold float64             `json:"threshold"`
}

func NewSemanticSearcher(store configstore.WarpStore, vectors vectorstore.VectorStore, embed EmbeddingExecutor, logs LogReader) *SemanticSearcher {
	return &SemanticSearcher{store: store, vectors: vectors, embed: embed, logs: logs}
}

// Search returns meaning-similar conversations in vector score order.
func (s *SemanticSearcher) Search(ctx context.Context, query string, filters *logstore.SearchFilters, requestedLimit int) (SemanticSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SemanticSearchResult{}, fmt.Errorf("query is required")
	}
	if s == nil || s.store == nil || s.vectors == nil || s.embed == nil || s.logs == nil {
		return SemanticSearchResult{}, ErrUnavailable
	}
	row, err := s.store.GetWarpConfig(ctx)
	if err != nil {
		return SemanticSearchResult{}, fmt.Errorf("read Warp configuration: %w", err)
	}
	config := configFromRow(row)
	if !config.IsConfigured() {
		return SemanticSearchResult{}, ErrUnavailable
	}
	limit := requestedLimit
	if limit < 1 {
		limit = config.EffectiveSemanticSearchLimit()
	}
	limit = min(limit, config.EffectiveSemanticSearchLimit(), warpMaxSemanticLimit())
	threshold := config.EffectiveSemanticSearchThreshold()
	embedding, err := generateWarpEmbedding(ctx, s.embed, config, query)
	if err != nil {
		return SemanticSearchResult{}, fmt.Errorf("embed semantic query: %w", err)
	}
	candidateLimit := min(max(limit*5, limit), warpSemanticCandidateLimit)
	nearest, err := s.vectors.GetNearest(
		vectorstore.WithDisableScanFallback(ctx),
		config.EffectiveLogVectorStoreNamespace(),
		embedding,
		semanticVectorFilters(filters),
		[]string{"log_id"},
		threshold,
		int64(candidateLimit),
	)
	if err != nil {
		return SemanticSearchResult{}, fmt.Errorf("search log embeddings: %w", err)
	}

	ids := make([]string, 0, len(nearest))
	scores := make(map[string]float64, len(nearest))
	for _, candidate := range nearest {
		id := semanticCandidateID(candidate)
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if candidate.Score != nil {
			scores[id] = *candidate.Score
		}
	}
	logs, err := s.logs.GetLogsByIDs(ctx, ids)
	if err != nil {
		return SemanticSearchResult{}, fmt.Errorf("hydrate semantic log matches: %w", err)
	}
	byID := make(map[string]*logstore.Log, len(logs))
	for index := range logs {
		byID[logs[index].ID] = &logs[index]
	}
	result := SemanticSearchResult{Rows: make([]SemanticSearchRow, 0, limit), Threshold: threshold}
	for _, id := range ids {
		entry := byID[id]
		if entry == nil || entry.ContentHidden || !terminalWarpLogStatus(entry.Status) || !conversationalWarpObject(entry.Object) || !matchesSemanticFilters(entry, filters) {
			continue
		}
		result.Rows = append(result.Rows, SemanticSearchRow{
			Score:  scores[id],
			logRow: projectLog(entry, true, LogContentChars),
		})
		if len(result.Rows) == limit {
			break
		}
	}
	result.Returned = len(result.Rows)
	return result, nil
}

func warpMaxSemanticLimit() int {
	// Keep the vector and model-context caps aligned even if the config ceiling
	// grows independently later.
	return min(warpSemanticCandidateLimit, MaxLogRows)
}

func semanticCandidateID(candidate vectorstore.SearchResult) string {
	if value, ok := candidate.Properties["log_id"].(string); ok && value != "" {
		return value
	}
	return candidate.ID
}

func semanticVectorFilters(filters *logstore.SearchFilters) []vectorstore.Query {
	queries := []vectorstore.Query{{Field: "warp_log", Operator: vectorstore.QueryOperatorEqual, Value: true}}
	if filters == nil {
		return queries
	}
	if filters.StartTime != nil {
		queries = append(queries, vectorstore.Query{Field: "timestamp", Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: filters.StartTime.Unix()})
	}
	if filters.EndTime != nil {
		queries = append(queries, vectorstore.Query{Field: "timestamp", Operator: vectorstore.QueryOperatorLessThanOrEqual, Value: filters.EndTime.Unix()})
	}
	queries = appendSingleValueQuery(queries, "provider", filters.Providers)
	queries = appendSingleValueQuery(queries, "model", filters.Models)
	queries = appendSingleValueQuery(queries, "status", filters.Status)
	queries = appendSingleValueQuery(queries, "virtual_key_id", filters.VirtualKeyIDs)
	queries = appendSingleValueQuery(queries, "user_id", filters.UserIDs)
	queries = appendSingleValueQuery(queries, "app", filters.Apps)
	queries = appendContainsAnyQuery(queries, "team_ids", filters.TeamIDs)
	queries = appendContainsAnyQuery(queries, "customer_ids", filters.CustomerIDs)
	queries = appendContainsAnyQuery(queries, "business_unit_ids", filters.BusinessUnitIDs)
	if filters.MinLatency != nil {
		queries = append(queries, vectorstore.Query{Field: "latency_ms", Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: int64(math.Floor(*filters.MinLatency))})
	}
	if filters.MaxLatency != nil {
		queries = append(queries, vectorstore.Query{Field: "latency_ms", Operator: vectorstore.QueryOperatorLessThanOrEqual, Value: int64(math.Ceil(*filters.MaxLatency))})
	}
	if filters.MinCost != nil {
		queries = append(queries, vectorstore.Query{Field: "cost_micro_usd", Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: int64(math.Floor(*filters.MinCost * 1_000_000))})
	}
	if filters.MaxCost != nil {
		queries = append(queries, vectorstore.Query{Field: "cost_micro_usd", Operator: vectorstore.QueryOperatorLessThanOrEqual, Value: int64(math.Ceil(*filters.MaxCost * 1_000_000))})
	}
	return queries
}

func appendSingleValueQuery(queries []vectorstore.Query, field string, values []string) []vectorstore.Query {
	if len(values) == 1 {
		return append(queries, vectorstore.Query{Field: field, Operator: vectorstore.QueryOperatorEqual, Value: values[0]})
	}
	return queries
}

func appendContainsAnyQuery(queries []vectorstore.Query, field string, values []string) []vectorstore.Query {
	if len(values) > 0 {
		return append(queries, vectorstore.Query{Field: field, Operator: vectorstore.QueryOperatorContainsAny, Value: values})
	}
	return queries
}

func matchesSemanticFilters(entry *logstore.Log, filters *logstore.SearchFilters) bool {
	if filters == nil {
		return true
	}
	if filters.StartTime != nil && entry.Timestamp.Before(*filters.StartTime) || filters.EndTime != nil && entry.Timestamp.After(*filters.EndTime) {
		return false
	}
	if !matchesString(entry.Provider, filters.Providers) || !matchesString(entry.Model, filters.Models) || !matchesString(entry.Status, filters.Status) || !matchesPointer(entry.VirtualKeyID, filters.VirtualKeyIDs) || !matchesPointer(entry.UserID, filters.UserIDs) || !matchesPointer(entry.App, filters.Apps) {
		return false
	}
	if !intersectsIDs(mergedIDs(entry.TeamID, entry.TeamIDs), filters.TeamIDs) || !intersectsIDs(mergedIDs(entry.CustomerID, entry.CustomerIDs), filters.CustomerIDs) || !intersectsIDs(mergedIDs(entry.BusinessUnitID, entry.BusinessUnitIDs), filters.BusinessUnitIDs) {
		return false
	}
	latency, cost := derefFloat(entry.Latency), derefFloat(entry.Cost)
	if filters.MinLatency != nil && latency < *filters.MinLatency || filters.MaxLatency != nil && latency > *filters.MaxLatency || filters.MinCost != nil && cost < *filters.MinCost || filters.MaxCost != nil && cost > *filters.MaxCost {
		return false
	}
	if search := strings.TrimSpace(filters.ContentSearch); search != "" {
		content := strings.ToLower(buildSemanticLogText(entry))
		if !strings.Contains(content, strings.ToLower(search)) {
			return false
		}
	}
	return true
}

func matchesString(value string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(value, item) {
			return true
		}
	}
	return false
}

func matchesPointer(value *string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	return value != nil && matchesString(*value, allowed)
}

func intersectsIDs(actual, required []string) bool {
	if len(required) == 0 {
		return true
	}
	for _, value := range actual {
		if matchesString(value, required) {
			return true
		}
	}
	return false
}

func buildSemanticLogText(entry *logstore.Log) string {
	item, ok := buildLogIndexItem(entry)
	if !ok {
		return ""
	}
	return item.text
}
