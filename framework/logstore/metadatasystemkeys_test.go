package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestGetDistinctMetadataKeysHidesLoadBalancerKeys checks that no key under
// schemas.LoadBalancerMetadataPrefix reaches the metadata filter or the metadata columns, whatever
// the key: they are the router's own record of an attempt, not caller metadata. Ordinary caller
// keys stay.
func TestGetDistinctMetadataKeysHidesLoadBalancerKeys(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	prefix := schemas.LoadBalancerMetadataPrefix
	now := time.Now().UTC()
	insertLogWithMetadata(t, store, "lb-keys",
		`{"tenant":"acme","`+prefix+`decision":"pinned_rerouted","`+prefix+`route_state":"healthy","`+prefix+`v":"1","`+prefix+`excluded_providers":"groq:failed","`+prefix+`excluded_keys":"k1:rate_limit"}`,
		now.Add(-time.Second))

	keys, err := store.GetDistinctMetadataKeys(context.Background(), 100, "", nil)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"tenant": {"acme"}}, keys)
}

// Filters must be applied before the bounded scan; each active key excludes only itself.
func TestGetDistinctMetadataKeysFacets(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	now := time.Now().UTC()
	for _, row := range []struct {
		id, vk, metadata string
		ts               time.Time
	}{
		{"a", "prod", `{"tenant":"bank","user":"alice","op":"chat"}`, now},
		{"b", "prod", `{"tenant":"bank","user":"bob","op":"chat"}`, now},
		{"c", "prod", `{"tenant":"shop","user":"carol","op":"chat"}`, now},
		{"d", "spark", `{"tenant":"bank","user":"dave","op":"chat"}`, now},
		{"old", "prod", `{"tenant":"bank","user":"old"}`, now.AddDate(0, 0, -90)},
	} {
		insertLogWithMetadata(t, store, row.id, row.metadata, row.ts)
		require.NoError(t, store.db.Model(&Log{}).Where("id = ?", row.id).Update("virtual_key_id", row.vk).Error)
	}
	t.Run("scope and self exclusion", func(t *testing.T) {
		start := now.Add(-time.Hour)
		filters := SearchFilters{VirtualKeyIDs: []string{"prod"}, StartTime: &start, MetadataFilters: map[string][]string{"tenant": {"bank"}}}
		got, err := store.GetDistinctMetadataKeys(context.Background(), 100, "", &filters)
		require.NoError(t, err)
		require.Equal(t, []string{"alice", "bob"}, got["user"])
		require.Equal(t, []string{"bank", "shop"}, got["tenant"])
		filters.MetadataFilters["user"] = []string{"alice"}
		got, err = store.GetDistinctMetadataKeys(context.Background(), 100, "", &filters)
		require.NoError(t, err)
		require.Equal(t, []string{"alice", "bob"}, got["user"])
		require.Equal(t, []string{"bank"}, got["tenant"])
		require.Equal(t, []string{"chat"}, got["op"])
		require.Equal(t, []string{"alice"}, filters.MetadataFilters["user"], "must not mutate caller filters")
		filters.MetadataFilters["user"] = []string{"alice", "bob"}
		got, err = store.GetDistinctMetadataKeys(context.Background(), 100, "bob", &filters)
		require.NoError(t, err)
		require.Equal(t, map[string][]string{"user": {"bob"}}, got)
	})
	t.Run("explicit historical window", func(t *testing.T) {
		oldStart, oldEnd := now.AddDate(0, 0, -91), now.AddDate(0, 0, -89)
		got, err := store.GetDistinctMetadataKeys(context.Background(), 100, "", &SearchFilters{StartTime: &oldStart, EndTime: &oldEnd})
		require.NoError(t, err)
		require.Equal(t, []string{"old"}, got["user"])
	})
}

// TestGetDistinctMetadataKeysFiltersBeforeLimit preserves sparse matches and access scopes.
func TestGetDistinctMetadataKeysFiltersBeforeLimit(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	now := time.Now().UTC()
	insertLogWithMetadata(t, store, "wanted", `{"tenant":"bank","user":"alice"}`, now.Add(-time.Hour))
	for i := 0; i < maxMetadataRows+1; i++ {
		insertLogWithMetadata(t, store, fmt.Sprintf("noise-%d", i), `{"tenant":"shop","user":"carol"}`, now)
	}
	scope := SearchFilters{MetadataFilters: map[string][]string{"tenant": {"bank"}}}
	got, err := store.GetDistinctMetadataKeys(context.Background(), 100, "", &scope)
	require.NoError(t, err)
	require.Equal(t, []string{"alice"}, got["user"], "matching rows must be found before limiting")
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB { return db.Where("id = ?", "wanted") })
	scope.MetadataFilters["user"] = []string{"missing"}
	got, err = store.GetDistinctMetadataKeys(ctx, 100, "", &scope)
	require.NoError(t, err)
	require.Equal(t, []string{"alice"}, got["user"])
	require.Empty(t, got["tenant"])
}

// TestMetadataFacetRescanBudget pins a deterministic query budget even when many keys are selected.
func TestMetadataFacetRescanBudget(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	selected := MetadataFilterMap{}
	row := map[string]string{}
	for i := 0; i < 18; i++ {
		key := fmt.Sprintf("key%02d", i)
		selected[key] = []string{"selected"}
		row[key] = "selected"
	}
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	insertLogWithMetadata(t, store, "base", string(raw), time.Now())
	for key := range row {
		row[key] = "alternative"
		raw, err = json.Marshal(row)
		require.NoError(t, err)
		insertLogWithMetadata(t, store, key, string(raw), time.Now())
		row[key] = "selected"
	}
	scans := 0
	require.NoError(t, store.db.Callback().Query().After("gorm:query").Register("test:count_facets", func(db *gorm.DB) {
		if db.Statement.Table == "logs" {
			scans++
		}
	}))
	got, err := store.GetDistinctMetadataKeys(context.Background(), 100, "", &SearchFilters{MetadataFilters: selected})
	require.NoError(t, err)
	require.Equal(t, 17, scans, "one base query plus at most sixteen facet rescans")
	for i := 0; i < 18; i++ {
		key := fmt.Sprintf("key%02d", i)
		if i < 16 {
			require.Equal(t, []string{"alternative", "selected"}, got[key])
		} else {
			require.Equal(t, []string{"selected"}, got[key])
		}
	}
}
