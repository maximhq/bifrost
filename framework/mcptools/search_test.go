package mcptools

import (
	"context"
	"fmt"
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
	sawQueries []string
	sawFilters *logstore.SearchFilters
	sawVisible *LogVisibility
	sawLimit   int
	result     SemanticSearchResult
}

func (f *fakeSemanticSearcher) SearchVisible(_ context.Context, queries []string, filters *logstore.SearchFilters, visible *LogVisibility, requestedLimit int) (SemanticSearchResult, error) {
	f.sawQueries, f.sawFilters, f.sawVisible, f.sawLimit = queries, filters, visible, requestedLimit
	return f.result, nil
}

func TestSemanticSearchToolReportsUnavailableWithoutASearcher(t *testing.T) {
	_, err := runTool(t, "semantic_search_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"queries": []any{"payment failures"}, "filters": map[string]any{},
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
		"queries": []any{"payment failures", "card declined"}, "filters": map[string]any{}, "limit": float64(9999),
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	require.Equal(t, "self", response["scope"])
	require.Equal(t, []string{"payment failures", "card declined"}, searcher.sawQueries, "every phrasing reaches the searcher")
	require.Nil(t, searcher.sawVisible, "a caller view with no visibility searches unfiltered")
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
		"queries": []any{"refund requests"}, "filters": map[string]any{},
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	require.Equal(t, 0, response["returned"])
	hint, _ := response["hint"].(string)
	require.Contains(t, hint, "threshold")
	require.Contains(t, hint, "do not fall back to count_logs or query_logs")
	// A survey question ("what kinds of tasks was Rohan doing") has no meaning
	// to embed, so an empty result is the expected outcome of the wrong first
	// step, not a finding. Forbidding the fallback outright here had the model
	// tell a person with 42k requests in the window that nothing matched.
	require.Contains(t, hint, "survey question")
	require.Contains(t, hint, "query_logs using include_content and limit 25")
	require.Contains(t, hint, "not representative of the entire traffic")
}

// The tool hands the searcher the visibility the caller view carries, so the
// index is asked for the caller's rows rather than the deployment's.
func TestSemanticSearchToolPrefiltersByCallerVisibility(t *testing.T) {
	visible := &LogVisibility{UserIDs: []string{"user-7"}, TeamIDs: []string{"team-1"}}
	ctx := WithCallerView(context.Background(), CallerView{DefaultUserID: "user-7", Visibility: "their teams' traffic", Visible: visible})
	searcher := &fakeSemanticSearcher{result: SemanticSearchResult{Threshold: 0.8}}
	// scope "all": the one question a restricted caller's default does not
	// already narrow to their own user id.
	result, err := runToolCtx(t, ctx, "semantic_search_logs", &Deps{LogManager: &fakeLogReader{}, Semantic: searcher}, map[string]any{
		"queries": []any{"payment failures"}, "filters": map[string]any{"scope": "all"},
	})
	require.NoError(t, err)
	require.Same(t, visible, searcher.sawVisible)
	require.Empty(t, searcher.sawFilters.UserIDs)
	require.Equal(t, "all", result.(map[string]any)["scope"])
}

// queries is a required list of strings: a call without it, or with a
// non-string phrasing, is refused rather than searched as something narrower.
// Its count and lengths are the searcher's to bound.
func TestSemanticSearchToolValidatesQueries(t *testing.T) {
	deps := &Deps{LogManager: &fakeLogReader{}, Semantic: &fakeSemanticSearcher{}}
	for _, bad := range []map[string]any{
		{"filters": map[string]any{}},
		{"queries": "payment failures", "filters": map[string]any{}},
		{"queries": []any{"payment failures", 42.0}, "filters": map[string]any{}},
	} {
		_, err := runTool(t, "semantic_search_logs", deps, bad)
		require.ErrorContains(t, err, "queries", "%v", bad)
	}
}

// Scores cannot tell a topic match from an unrelated row, so the model is told
// to judge each row by its content. Without it the model counted every
// returned row as a match, or - with nothing above the old threshold - said a
// topic with nineteen conversations had none.
func TestSemanticSearchToldToJudgeRowsByContent(t *testing.T) {
	tool := semanticSearchLogsTool()
	require.Contains(t, tool.description, "Read each row's content and keep only the rows actually about what was asked")
	require.Contains(t, tool.description, "several phrasings")
	require.Contains(t, tool.schemaJSON, `"queries"`)
	require.Contains(t, tool.schemaJSON, fmt.Sprintf(`"maxItems": %d`, MaxSemanticQueries))
}
