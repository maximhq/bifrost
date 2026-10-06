package warp

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

const warpSemanticCandidateLimit = 100

// MaxSemanticQueryChars bounds the natural-language query, in characters. The
// query becomes an embedding request, so an unbounded tool argument is
// provider capacity and usage budget spent on one call. The tool schema
// advertises the same figure as maxLength, so the model can stay inside it
// instead of learning the bound from a refusal.
const MaxSemanticQueryChars = 2000

// SemanticSearcher joins the vector index back to the authoritative log store.
// Vector metadata is only a coarse prefilter: every candidate is reloaded using
// the caller's context so queryscope remains the access-control boundary.
type SemanticSearcher struct {
	store   configstore.WarpStore
	vectors vectorstore.VectorStore
	embed   EmbeddingExecutor
	// logs is the hydration half only. Narrower than LogReader on purpose: this
	// is the sole reason GetLogsByIDs would otherwise have to sit on the exported
	// reader interface, where adding it breaks every implementation outside this
	// repo - including ones with semantic search switched off.
	logs SemanticHydrator
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

func NewSemanticSearcher(store configstore.WarpStore, vectors vectorstore.VectorStore, embed EmbeddingExecutor, logs SemanticHydrator) *SemanticSearcher {
	return &SemanticSearcher{store: store, vectors: vectors, embed: embed, logs: logs}
}

// Search returns meaning-similar conversations in vector score order.
func (s *SemanticSearcher) Search(ctx context.Context, query string, filters *logstore.SearchFilters, requestedLimit int) (SemanticSearchResult, error) {
	return s.SearchVisible(ctx, query, filters, nil, requestedLimit)
}

// SearchVisible is Search for a caller whose reads row-level access control
// narrows to visible.
//
// visible changes which candidates the index is asked for, never which rows
// come back: every candidate is still reloaded through the caller's context,
// and the store decides. What it buys is recall. Unfiltered, the candidate cap
// is spent on the deployment's nearest rows, and for a caller who may see a
// small slice of them nearly all are discarded after hydration - a genuine
// match of theirs at rank 500 is never reached. A nil visible searches exactly
// as Search always has.
func (s *SemanticSearcher) SearchVisible(ctx context.Context, query string, filters *logstore.SearchFilters, visible *LogVisibility, requestedLimit int) (SemanticSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SemanticSearchResult{}, fmt.Errorf("query is required")
	}
	// Refused before the embedding request is paid for. Runes, not bytes, to
	// match the schema's advertised maxLength.
	if count := utf8.RuneCountInString(query); count > MaxSemanticQueryChars {
		return SemanticSearchResult{}, fmt.Errorf("query is %d characters; at most %d are accepted", count, MaxSemanticQueryChars)
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
	embedding, _, err := generateWarpEmbedding(ctx, s.embed, config, query)
	if err != nil {
		return SemanticSearchResult{}, fmt.Errorf("embed semantic query: %w", err)
	}
	// The vector store only knows the metadata it was indexed with. Scope,
	// content-hiding and ContentSearch are decided here, after hydration, so a
	// single top-K page can be spent entirely on rows this loop discards and
	// leave a genuine match at rank K+1 unseen. Widen the page and ask again
	// until the limit is met or the index is exhausted; already-hydrated rows
	// are reused so a refill only fetches what it has not seen.
	candidateLimit := min(max(limit*5, limit), warpSemanticCandidateLimit)
	hydrated := make(map[string]*logstore.Log, candidateLimit)
	scores := make(map[string]float64, candidateLimit)
	result := SemanticSearchResult{Rows: make([]SemanticSearchRow, 0, limit), Threshold: threshold}
	prefilters := semanticPrefilters(filters, visible)
	for {
		pages, err := s.nearestPages(ctx, config.EffectiveLogVectorStoreNamespace(), embedding, prefilters, threshold, candidateLimit)
		if err != nil {
			return SemanticSearchResult{}, fmt.Errorf("search log embeddings: %w", err)
		}
		atCap := candidateLimit >= warpSemanticCandidateLimit
		ids, exhausted := mergeSemanticPages(pages, candidateLimit, scores, atCap)
		pending := make([]string, 0, len(ids))
		for _, id := range ids {
			if _, seen := hydrated[id]; !seen {
				pending = append(pending, id)
			}
		}
		if len(pending) > 0 {
			logs, err := s.logs.GetLogsByIDs(ctx, pending)
			if err != nil {
				return SemanticSearchResult{}, fmt.Errorf("hydrate semantic log matches: %w", err)
			}
			for _, id := range pending {
				// A miss is recorded too, so a refill never re-asks for a row
				// the log store has already said it does not have.
				hydrated[id] = nil
			}
			for index := range logs {
				hydrated[logs[index].ID] = &logs[index]
			}
		}
		result.Rows = result.Rows[:0]
		for _, id := range ids {
			entry := hydrated[id]
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
		// A short page means the index had nothing more to give at this
		// threshold, so widening it again would return the same rows. So would
		// widening past a full merged set: it is already the best the cap allows.
		if len(result.Rows) == limit || exhausted || atCap || len(ids) >= warpSemanticCandidateLimit {
			break
		}
		if err := ctx.Err(); err != nil {
			return SemanticSearchResult{}, err
		}
		candidateLimit = min(candidateLimit*2, warpSemanticCandidateLimit)
	}
	result.Returned = len(result.Rows)
	return result, nil
}

// LogVisibility is the row ownership row-level access control grants a
// restricted caller: a log is theirs to read when it matches any one of these
// sets. It mirrors the store's own predicate, and it only has to be no
// narrower than it - a set that is too wide costs candidates, one that is too
// narrow hides rows the caller may see.
type LogVisibility struct {
	UserIDs         []string
	VirtualKeyIDs   []string
	TeamIDs         []string
	BusinessUnitIDs []string
	CustomerIDs     []string
}

// warpVisibilityPrefilterMaxIDs bounds each visibility set pushed into the
// vector filter. A caller who may see thousands of users sees a large share of
// the index anyway, so the unfiltered search serves them; a filter that size
// is what some vector stores refuse.
const warpVisibilityPrefilterMaxIDs = 500

// semanticPrefilters returns the vector filters to search with: the question's
// own, once per visibility dimension.
//
// Vector filters only AND, and visibility is an OR across fields - the
// caller's user ids, or their teams, or their keys. So each dimension is its
// own query, and the pages are merged by score. One filter set, the question's
// own, is returned when there is nothing to narrow by: no visibility, a set
// too large to send, or a question that already names ids inside one of the
// sets - its candidates are the caller's own already.
func semanticPrefilters(filters *logstore.SearchFilters, visible *LogVisibility) [][]vectorstore.Query {
	base := semanticVectorFilters(filters)
	if visible == nil || filtersWithinVisibility(filters, visible) {
		return [][]vectorstore.Query{base}
	}
	dimensions := []struct {
		field  string
		ids    []string
		scalar bool
	}{
		{"user_id", visible.UserIDs, true},
		{"virtual_key_id", visible.VirtualKeyIDs, true},
		{"team_ids", visible.TeamIDs, false},
		{"business_unit_ids", visible.BusinessUnitIDs, false},
		{"customer_ids", visible.CustomerIDs, false},
	}
	prefilters := make([][]vectorstore.Query, 0, len(dimensions))
	for _, dimension := range dimensions {
		if len(dimension.ids) > warpVisibilityPrefilterMaxIDs {
			return [][]vectorstore.Query{base}
		}
		if len(dimension.ids) == 0 {
			continue
		}
		queries := slices.Clone(base)
		if dimension.scalar {
			queries = appendScalarQuery(queries, dimension.field, dimension.ids)
		} else {
			queries = appendContainsAnyQuery(queries, dimension.field, dimension.ids)
		}
		prefilters = append(prefilters, queries)
	}
	if len(prefilters) == 0 {
		return [][]vectorstore.Query{base}
	}
	return prefilters
}

// filtersWithinVisibility reports whether the question already names ids that
// all sit inside one visibility set, so its own filter narrows the index to
// rows the caller may see.
func filtersWithinVisibility(filters *logstore.SearchFilters, visible *LogVisibility) bool {
	if filters == nil {
		return false
	}
	within := func(named, allowed []string) bool {
		if len(named) == 0 {
			return false
		}
		for _, id := range named {
			if !matchesString(id, allowed) {
				return false
			}
		}
		return true
	}
	return within(filters.UserIDs, visible.UserIDs) ||
		within(filters.VirtualKeyIDs, visible.VirtualKeyIDs) ||
		within(filters.TeamIDs, visible.TeamIDs) ||
		within(filters.BusinessUnitIDs, visible.BusinessUnitIDs) ||
		within(filters.CustomerIDs, visible.CustomerIDs)
}

// nearestPages runs one nearest-neighbour query per prefilter and returns the
// pages in prefilter order. More than one runs together: they are independent
// reads, and a restricted caller should not wait five times as long.
func (s *SemanticSearcher) nearestPages(ctx context.Context, namespace string, embedding []float32, prefilters [][]vectorstore.Query, threshold float64, limit int) ([][]vectorstore.SearchResult, error) {
	pages := make([][]vectorstore.SearchResult, len(prefilters))
	errs := make([]error, len(prefilters))
	var wg sync.WaitGroup
	for index, queries := range prefilters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pages[index], errs[index] = s.vectors.GetNearest(
				vectorstore.WithDisableScanFallback(ctx),
				namespace,
				embedding,
				queries,
				[]string{"log_id"},
				threshold,
				int64(limit),
			)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return pages, nil
}

// mergeSemanticPages folds the pages into one candidate list, best first,
// recording each score. exhausted reports that no page was full, so the index
// has nothing more at this threshold.
//
// One page passes through in the store's own order. Several are merged by
// score, and then only as far as the merge can be trusted: a full page stopped
// at its last score, so rows it would have returned next may outrank another
// page's lower ones. Candidates below the highest such stopping score are held
// back for the wider round, unless final says there will not be one. The result
// is capped at the candidate limit, which is also all hydration accepts.
func mergeSemanticPages(pages [][]vectorstore.SearchResult, pageLimit int, scores map[string]float64, final bool) (ids []string, exhausted bool) {
	exhausted = true
	seen := map[string]struct{}{}
	frontier, hasFrontier := 0.0, false
	for _, page := range pages {
		if len(page) >= pageLimit {
			exhausted = false
			if last := page[len(page)-1].Score; last != nil && (!hasFrontier || *last > frontier) {
				frontier, hasFrontier = *last, true
			}
		}
		for _, candidate := range page {
			id := semanticCandidateID(candidate)
			if id == "" {
				continue
			}
			if candidate.Score != nil {
				if known, ok := scores[id]; !ok || *candidate.Score > known {
					scores[id] = *candidate.Score
				}
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(pages) < 2 {
		return ids, exhausted
	}
	slices.SortStableFunc(ids, func(a, b string) int {
		switch {
		case scores[a] > scores[b]:
			return -1
		case scores[a] < scores[b]:
			return 1
		}
		return 0
	})
	if hasFrontier && !final {
		ids = slices.DeleteFunc(ids, func(id string) bool { return scores[id] < frontier })
	}
	if len(ids) > warpSemanticCandidateLimit {
		ids = ids[:warpSemanticCandidateLimit]
	}
	return ids, exhausted
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
	queries = appendScalarQuery(queries, "provider", filters.Providers)
	queries = appendScalarQuery(queries, "model", filters.Models)
	queries = appendScalarQuery(queries, "status", filters.Status)
	queries = appendScalarQuery(queries, "virtual_key_id", filters.VirtualKeyIDs)
	queries = appendScalarQuery(queries, "user_id", filters.UserIDs)
	queries = appendScalarQuery(queries, "app", filters.Apps)
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

// appendScalarQuery filters a scalar metadata field by one or more values.
//
// Two or more used to produce no query at all, which did not narrow the search
// - it widened it. The candidate cap was then spent on rows the post-filter
// would discard, so an ordinary "openai or anthropic" question came back thin
// or empty. ContainsAny compares against a scalar stored value as happily as an
// array one, so the multi-value case is expressible; equality is kept for one
// value because it is the cheaper predicate.
func appendScalarQuery(queries []vectorstore.Query, field string, values []string) []vectorstore.Query {
	// Lowered to meet the index. The post-filter compares with EqualFold, but
	// the vector store's equality is exact - so "OpenAI" as a filter excluded an
	// indexed "openai" before hydration ever ran. buildLogIndexItem lowers the
	// same scalar fields on the way in, which is the other half of the contract.
	lowered := make([]string, len(values))
	for index, value := range values {
		lowered[index] = strings.ToLower(value)
	}
	switch len(lowered) {
	case 0:
		return queries
	case 1:
		return append(queries, vectorstore.Query{Field: field, Operator: vectorstore.QueryOperatorEqual, Value: lowered[0]})
	default:
		return append(queries, vectorstore.Query{Field: field, Operator: vectorstore.QueryOperatorContainsAny, Value: lowered})
	}
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
	// A metric that was never recorded fails any bound on it, rather than being
	// read as zero. Treating nil as 0 let "under $0.01" quietly include every
	// request whose cost was never measured - and the logstore's own range
	// filters exclude NULL, so the same filter asked through query_logs and
	// through semantic search returned different sets.
	if !withinOptionalBound(entry.Latency, filters.MinLatency, filters.MaxLatency) ||
		!withinOptionalBound(entry.Cost, filters.MinCost, filters.MaxCost) {
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

// withinOptionalBound reports whether a nullable metric satisfies the bounds
// that are set. A nil value satisfies no bound: there is nothing to compare.
func withinOptionalBound(value, minimum, maximum *float64) bool {
	if minimum == nil && maximum == nil {
		return true
	}
	if value == nil {
		return false
	}
	if minimum != nil && *value < *minimum {
		return false
	}
	return maximum == nil || *value <= *maximum
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
