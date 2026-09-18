package mcptools

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// fakeSemanticSearcher records the filters semantic_search_logs passed it and
// answers with a scripted result. The searcher's own hydration, ordering and
// hidden-content behaviour are framework/warp's to test; this covers the tool
// wrapped around it.
type fakeSemanticSearcher struct {
	sawQuery   string
	sawFilters *logstore.SearchFilters
	sawLimit   int
	result     SemanticSearchResult
}

func (f *fakeSemanticSearcher) Search(_ context.Context, query string, filters *logstore.SearchFilters, requestedLimit int) (SemanticSearchResult, error) {
	f.sawQuery, f.sawFilters, f.sawLimit = query, filters, requestedLimit
	return f.result, nil
}

func TestSemanticSearchToolReportsUnavailableWithoutASearcher(t *testing.T) {
	_, err := runTool(t, "semantic_search_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"query": "payment failures", "filters": map[string]any{},
	})
	require.ErrorContains(t, err, "not configured")
}

func TestSemanticSearchToolAppliesDefaultCallerScope(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	oldNow := Now
	Now = func() time.Time { return now }
	defer func() { Now = oldNow }()

	searcher := &fakeSemanticSearcher{result: SemanticSearchResult{Threshold: 0.8}}
	result, err := runToolCtx(t, scoped("asking-user"), "semantic_search_logs", &Deps{LogManager: &fakeLogReader{}, Semantic: searcher}, map[string]any{
		"query": "payment failures", "filters": map[string]any{}, "limit": float64(9999),
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	require.Equal(t, "self", response["scope"])
	require.Equal(t, "payment failures", searcher.sawQuery)
	require.Equal(t, []string{"asking-user"}, searcher.sawFilters.UserIDs, "the default scope must reach the searcher's filters")
	require.Equal(t, MaxLogRows, searcher.sawLimit, "the row cap holds regardless of what the caller asks for")
	// The provenance footer needs an absolute window on every result, not
	// just query_metrics's - otherwise the model has to recompute one from
	// the current-time reference, which the prompt separately tells it not to.
	require.Equal(t, map[string]string{"start": "2026-09-03T12:00:00Z", "end": "2026-09-04T12:00:00Z"}, response["window"])
}

// An empty semantic result used to be four bare fields, and the model read it as
// "search is useless here" and went off counting and listing logs instead. The
// hint says what happened (nothing scored above the threshold) and what the
// legitimate next moves are, so a meaning question stays a meaning question.
func TestSemanticSearchToolHintsWhenNothingMatches(t *testing.T) {
	searcher := &fakeSemanticSearcher{result: SemanticSearchResult{Threshold: 0.8}}
	result, err := runTool(t, "semantic_search_logs", &Deps{LogManager: &fakeLogReader{}, Semantic: searcher}, map[string]any{
		"query": "refund requests", "filters": map[string]any{},
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	require.Equal(t, 0, response["returned"])
	hint, _ := response["hint"].(string)
	require.Contains(t, hint, "threshold")
	require.Contains(t, hint, "Do not fall back to count_logs or query_logs")
}
