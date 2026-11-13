package configstore

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testModelRouterTargets sends SIMPLE to a small model and splits COMPLEX
// between two large ones, the first with a fallback.
func testModelRouterTargets() []tables.TableModelRouterTarget {
	return []tables.TableModelRouterTarget{
		{Tier: tables.ModelRouterTierSimple, Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4o-mini"), Weight: 1},
		{Tier: tables.ModelRouterTierComplex, Provider: bifrost.Ptr("anthropic"), Model: bifrost.Ptr("claude-opus"), Weight: 0.7,
			ParsedFallbacks: []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o"}}}},
		{Tier: tables.ModelRouterTierComplex, Provider: bifrost.Ptr("anthropic"), Model: bifrost.Ptr("claude-sonnet"), Weight: 0.3},
	}
}

// TestModelRouterTargetsStore covers a router's targets: written, read back
// with the router in a fixed order, replaced, cleared, and removed with the
// router.
func TestModelRouterTargetsStore(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))

	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	require.Len(t, got.Targets, 3)
	// Written SIMPLE first, read back grouped by tier and heaviest first, as
	// routing rule targets are.
	assert.Equal(t, []string{"claude-opus", "claude-sonnet", "gpt-4o-mini"},
		[]string{derefStr(got.Targets[0].Model), derefStr(got.Targets[1].Model), derefStr(got.Targets[2].Model)})
	require.Len(t, got.Targets[0].ParsedFallbacks, 1)
	assert.Equal(t, "gpt-4o", got.Targets[0].ParsedFallbacks[0].Model)

	listed, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	for _, router := range listed {
		if router.ID == "support" {
			assert.Len(t, router.Targets, 3, "the list carries each router's targets")
		}
	}

	// Saving the router's config leaves its targets alone.
	update := testModelRouter("support", "Support desk")
	require.NoError(t, store.UpdateModelRouter(ctx, update))
	got, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Len(t, got.Targets, 3)

	require.NoError(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{
		{Tier: "medium", Model: bifrost.Ptr("gpt-4o"), Weight: 1},
	}))
	got, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	require.Len(t, got.Targets, 1)
	assert.Equal(t, tables.ModelRouterTierMedium, got.Targets[0].Tier, "a lower-case tier is normalized")

	require.NoError(t, store.SetModelRouterTargets(ctx, "support", nil))
	got, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Empty(t, got.Targets, "no targets clears them")

	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	require.NoError(t, store.DeleteModelRouter(ctx, "support"))
	var left int64
	require.NoError(t, store.DB().Model(&tables.TableModelRouterTarget{}).Count(&left).Error)
	assert.Zero(t, left, "a router's targets go with it")
}

// TestModelRouterTargetsNeedASavedRouter pins that targets are only written
// for a router that has been saved, and never create one.
// TestModelRouterTargetsUnreadableRow pins that a target row whose fallback
// chain cannot be decoded makes only its own router unreadable: the list
// leaves that router out and still serves the others with their targets, and
// saving the router's targets again repairs it.
func TestModelRouterTargetsUnreadableRow(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	require.NoError(t, store.SetModelRouterTargets(ctx, "billing", testModelRouterTargets()))
	require.NoError(t, store.DB().Model(&tables.TableModelRouterTarget{}).
		Where("router_id = ? AND fallbacks IS NOT NULL", "support").
		Update("fallbacks", `["unterminated`).Error)

	_, err := store.GetModelRouter(ctx, "support")
	require.ErrorIs(t, err, ErrConfigUnreadable)

	routers, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	require.Len(t, routers, 1)
	assert.Equal(t, "billing", routers[0].ID)
	assert.Len(t, routers[0].Targets, 3, "the readable router keeps its targets")

	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Len(t, got.Targets, 3)
}

func TestModelRouterTargetsNeedASavedRouter(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.ErrorIs(t, store.SetModelRouterTargets(ctx, "missing", testModelRouterTargets()), ErrNotFound)
	var rows int64
	require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Count(&rows).Error)
	assert.Zero(t, rows, "no router row is created")
}

// TestValidateModelRouterTargets pins what targets are checked for before
// they are stored.
func TestValidateModelRouterTargets(t *testing.T) {
	cases := map[string]func(targets []tables.TableModelRouterTarget){
		"unknown tier":           func(ts []tables.TableModelRouterTarget) { ts[0].Tier = "HARD" },
		"target changes nothing": func(ts []tables.TableModelRouterTarget) { ts[0].Provider, ts[0].Model = nil, nil },
		"key without provider": func(ts []tables.TableModelRouterTarget) {
			ts[0].Provider, ts[0].KeyID = nil, bifrost.Ptr("key-1")
		},
		"zero weight":                func(ts []tables.TableModelRouterTarget) { ts[0].Weight = 0 },
		"tier weights do not sum":    func(ts []tables.TableModelRouterTarget) { ts[2].Weight = 0.5 },
		"duplicate target in a tier": func(ts []tables.TableModelRouterTarget) { ts[2].Model = bifrost.Ptr("claude-opus") },
		"unknown fallback provider": func(ts []tables.TableModelRouterTarget) {
			ts[0].ParsedFallbacks = []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "nowhere", Model: "x"}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			targets := testModelRouterTargets()
			mutate(targets)
			require.Error(t, ValidateModelRouterTargets(targets))
		})
	}
	assert.NoError(t, ValidateModelRouterTargets(nil), "no targets is valid")
}

// TestGenerateModelRouterHashTargets pins that a config.json router entry's
// hash covers its targets only when it declares them, and ignores their order.
func TestGenerateModelRouterHashTargets(t *testing.T) {
	bare := *testModelRouter("support", "Support")
	withTargets := bare
	withTargets.Targets = testModelRouterTargets()
	assert.NotEqual(t, GenerateModelRouterHash(bare), GenerateModelRouterHash(withTargets))

	empty := bare
	empty.Targets = []tables.TableModelRouterTarget{}
	assert.NotEqual(t, GenerateModelRouterHash(bare), GenerateModelRouterHash(empty),
		"declaring no targets differs from not declaring them")

	reordered := bare
	reordered.Targets = testModelRouterTargets()
	reordered.Targets[0], reordered.Targets[2] = reordered.Targets[2], reordered.Targets[0]
	assert.Equal(t, GenerateModelRouterHash(withTargets), GenerateModelRouterHash(reordered))

	reweighted := bare
	reweighted.Targets = testModelRouterTargets()
	reweighted.Targets[1].Weight, reweighted.Targets[2].Weight = 0.5, 0.5
	assert.NotEqual(t, GenerateModelRouterHash(withTargets), GenerateModelRouterHash(reweighted))
}
