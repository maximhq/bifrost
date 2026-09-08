package logstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newToolCallNamesStore seeds an in-memory sqlite store with rows that called
// different tool sets. See newRootsOnlyStore for why the DSN is a named shared
// cache and closed on cleanup.
func newToolCallNamesStore(t *testing.T) *RDBLogStore {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now()
	seed := []Log{
		{ID: "both", Timestamp: now, Status: "success", Provider: "openai", ToolCallNames: []string{"get_weather", "search"}},
		{ID: "search-only", Timestamp: now, Status: "success", Provider: "openai", ToolCallNames: []string{"search"}},
		{ID: "no-tools", Timestamp: now, Status: "success", Provider: "openai"},
		// Names that differ from the seeds above only where LIKE treats a
		// character as a wildcard: "_" matches any single char, "%" any run.
		{ID: "dash", Timestamp: now, Status: "success", Provider: "openai", ToolCallNames: []string{"get-weather"}},
		{ID: "percent-literal", Timestamp: now, Status: "success", Provider: "openai", ToolCallNames: []string{"a%b"}},
		{ID: "percent-expanded", Timestamp: now, Status: "success", Provider: "openai", ToolCallNames: []string{"axxb"}},
	}
	for i := range seed {
		require.NoError(t, seed[i].SerializeFields())
		require.NoError(t, db.Create(&seed[i]).Error)
	}
	return &RDBLogStore{db: db}
}

func searchIDs(t *testing.T, store *RDBLogStore, filters SearchFilters) []string {
	t.Helper()
	res, err := store.SearchLogs(context.Background(), filters, PaginationOptions{Limit: 50, SortBy: "timestamp", Order: "desc"})
	require.NoError(t, err)
	ids := make([]string, 0, len(res.Logs))
	for _, l := range res.Logs {
		ids = append(ids, l.ID)
	}
	return ids
}

func TestSearchLogsFiltersByToolCallNames(t *testing.T) {
	store := newToolCallNamesStore(t)

	require.ElementsMatch(t, []string{"both"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"get_weather"}}))
	require.ElementsMatch(t, []string{"both", "search-only"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"search"}}))
	// ANY semantics across the requested names.
	require.ElementsMatch(t, []string{"both", "search-only"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"get_weather", "search"}}))
	// Delimiter-aware: a substring of a name must not match.
	require.Empty(t, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"search_only"}}))
	require.Empty(t, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"get"}}))
	// Blank entries are ignored rather than matching everything.
	require.ElementsMatch(t, []string{"both", "search-only", "no-tools", "dash", "percent-literal", "percent-expanded"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{" "}}))
}

func TestSearchLogsToolCallNamesTreatsLikeMetacharactersLiterally(t *testing.T) {
	store := newToolCallNamesStore(t)

	// "_" must not act as a single-character wildcard: get_weather is not get-weather.
	require.ElementsMatch(t, []string{"both"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"get_weather"}}))
	require.ElementsMatch(t, []string{"dash"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"get-weather"}}))
	// "%" must not act as a run wildcard: a%b is not axxb.
	require.ElementsMatch(t, []string{"percent-literal"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"a%b"}}))
	require.ElementsMatch(t, []string{"percent-expanded"}, searchIDs(t, store, SearchFilters{ToolCallNames: []string{"axxb"}}))
	// A backslash in the value is a literal too, not the escape character.
	require.Empty(t, searchIDs(t, store, SearchFilters{ToolCallNames: []string{`get\_weather`}}))
}

func TestSearchLogsListSelectsToolCallNames(t *testing.T) {
	store := newToolCallNamesStore(t)
	res, err := store.SearchLogs(context.Background(), SearchFilters{ToolCallNames: []string{"get_weather"}}, PaginationOptions{Limit: 50, SortBy: "timestamp", Order: "desc"})
	require.NoError(t, err)
	require.Len(t, res.Logs, 1)
	require.Equal(t, []string{"get_weather", "search"}, res.Logs[0].ToolCallNames)
}

func TestGetDistinctToolCallNames(t *testing.T) {
	store := newToolCallNamesStore(t)
	ctx := context.Background()

	names, err := store.GetDistinctToolCallNames(ctx, 50, "")
	require.NoError(t, err)
	require.Equal(t, []string{"a%b", "axxb", "get-weather", "get_weather", "search"}, names)

	// The LIKE ran on the joined column; sibling names sharing a row (search)
	// are dropped by the Go-side re-filter.
	names, err = store.GetDistinctToolCallNames(ctx, 50, "weather")
	require.NoError(t, err)
	require.Equal(t, []string{"get-weather", "get_weather"}, names)

	names, err = store.GetDistinctToolCallNames(ctx, 1, "")
	require.NoError(t, err)
	require.Equal(t, []string{"a%b"}, names)
}

func TestCanUseMatViewFiltersRejectsToolCallNames(t *testing.T) {
	require.True(t, canUseMatViewFilters(SearchFilters{}))
	require.False(t, canUseMatViewFilters(SearchFilters{ToolCallNames: []string{"search"}}))
}

func TestLogToolCallNamesRoundTrip(t *testing.T) {
	l := &Log{ToolCallNames: []string{" search ", "search", "get_weather", "", "bad,name"}}
	require.NoError(t, l.SerializeFields())
	require.NotNil(t, l.ToolCallNamesStr)
	require.Equal(t, "search,get_weather", *l.ToolCallNamesStr)

	l.ToolCallNames = nil
	require.NoError(t, l.DeserializeFields())
	require.Equal(t, []string{"search", "get_weather"}, l.ToolCallNames)

	empty := &Log{ToolCallNames: []string{}}
	require.NoError(t, empty.SerializeFields())
	require.Nil(t, empty.ToolCallNamesStr)
	require.NoError(t, empty.DeserializeFields())
	require.Nil(t, empty.ToolCallNames)
}

func TestJoinToolCallNamesCapKeepsFirstSeenPrefix(t *testing.T) {
	long := strings.Repeat("x", maxToolCallNamesBytes-5)
	// long (2043) + ",abcdefgh" (9) overflows, so the join stops there even
	// though ",ab" alone would still fit: the stored list is always a prefix of
	// first-seen order, and a name is never split.
	joined := joinToolCallNames([]string{long, "abcdefgh", "ab"})
	require.Equal(t, long, joined)
	require.LessOrEqual(t, len(joined), maxToolCallNamesBytes)

	exact := strings.Repeat("y", maxToolCallNamesBytes-3)
	require.Equal(t, exact+",ab", joinToolCallNames([]string{exact, "ab"}))
}
