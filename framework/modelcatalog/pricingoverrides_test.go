package modelcatalog

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
)

// TestPricingOverridesFollowTheLiveStore checks that a catalog following the live store, as one
// from Init does, prices with the overrides the store loads.
func TestPricingOverridesFollowTheLiveStore(t *testing.T) {
	store, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(store.Close)
	mc := catalogForTest(t, nil)
	mc.store = store
	mc.cancelOverrides = store.Collections.PricingOverrides.Subscribe(func(context.Context, []livestore.Change[registry.PricingOverride]) {
		mc.applyPricingOverrides()
	})
	t.Cleanup(mc.cancelOverrides)

	providerID := "openai"
	override := func(id string, created time.Time) *registry.PricingOverride {
		return &registry.PricingOverride{TablePricingOverride: configstoreTables.TablePricingOverride{
			ID: id, Name: id, ScopeKind: "provider", ProviderID: &providerID, MatchType: "exact", Pattern: "gpt-4o",
			RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest}, PricingPatchJSON: `{"input_cost_per_token":11}`,
			CreatedAt: created,
		}}
	}
	now := time.Now()
	recs := store.Collections.FileRecords(&registry.FileRecords{PricingOverrides: []*registry.PricingOverride{
		override("b-newer", now), override("a-older", now.Add(-time.Hour)),
	}})
	require.NoError(t, store.LoadRecords(context.Background(), recs))

	got := mc.GetCatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	require.Len(t, got.Matching, 2, "the catalog prices with the overrides the store loaded")
	require.NotEmpty(t, got.AppliedID)
}

// overrideApplyLogger counts the pricing-override apply failures a catalog logs.
type overrideApplyLogger struct {
	schemas.Logger
	applyFailures atomic.Int32
}

// Error counts a failed pricing-override apply; every other message is dropped.
func (l *overrideApplyLogger) Error(msg string, _ ...any) {
	if strings.Contains(msg, "pricing overrides") {
		l.applyFailures.Add(1)
	}
}

// TestFailedInitStopsFollowingPricingOverrides checks that an Init that fails
// leaves nothing subscribed to the live store's pricing overrides: the caller
// never gets the catalog, so a later override change must not reach it.
func TestFailedInitStopsFollowingPricingOverrides(t *testing.T) {
	store, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(store.Close)
	logger := &overrideApplyLogger{Logger: bifrost.NewNoOpLogger()}
	// A cancelled context fails the first pricing load at once, after Init has subscribed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := "file://" + filepath.Join(t.TempDir(), "missing.json")
	_, err = Init(ctx, &Config{PricingURL: &missing}, nil, store, logger)
	require.Error(t, err, "Init fails when the first pricing load fails")

	// A patch that does not parse makes any catalog still following the store log an apply failure.
	recs := store.Collections.FileRecords(&registry.FileRecords{PricingOverrides: []*registry.PricingOverride{{
		TablePricingOverride: configstoreTables.TablePricingOverride{
			ID: "unparsable", Name: "unparsable", ScopeKind: "global", MatchType: "exact", Pattern: "gpt-4o",
			PricingPatchJSON: "not json",
		},
	}}})
	require.NoError(t, store.LoadRecords(context.Background(), recs))
	require.Zero(t, logger.applyFailures.Load(), "the catalog a failed Init discarded still follows pricing overrides")
}
