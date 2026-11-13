package configstore

import (
	"context"
	"fmt"
	"math"
	"slices"
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

// testModelRouterTarget builds one target; an empty provider, model or key is
// left unset rather than stored as an empty string.
func testModelRouterTarget(tier, provider, model, keyID string, weight float64) tables.TableModelRouterTarget {
	target := tables.TableModelRouterTarget{Tier: tier, Weight: weight}
	if provider != "" {
		target.Provider = bifrost.Ptr(provider)
	}
	if model != "" {
		target.Model = bifrost.Ptr(model)
	}
	if keyID != "" {
		target.KeyID = bifrost.Ptr(keyID)
	}
	return target
}

// describeModelRouterTargets renders targets as "TIER provider/model@key weight"
// lines, so a whole set and its order compare in one assertion.
func describeModelRouterTargets(targets []tables.TableModelRouterTarget) []string {
	lines := make([]string, 0, len(targets))
	for _, target := range targets {
		lines = append(lines, fmt.Sprintf("%s %s/%s@%s %g", target.Tier, derefStr(target.Provider), derefStr(target.Model), derefStr(target.KeyID), target.Weight))
	}
	return lines
}

// storedModelRouterTargets reads a router's targets from the store.
func storedModelRouterTargets(t *testing.T, store *RDBConfigStore, id string) []tables.TableModelRouterTarget {
	t.Helper()
	router, err := store.GetModelRouter(context.Background(), id)
	require.NoError(t, err)
	return router.Targets
}

// TestSetModelRouterTargetsReplacesOnlyItsRouter pins that saving targets is a
// replace scoped to one router: nothing from the previous save survives, a
// refused save keeps what was stored, an empty list clears them, and through
// all of it another router's targets stay exactly as they were.
func TestSetModelRouterTargetsReplacesOnlyItsRouter(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	require.NoError(t, store.SetModelRouterTargets(ctx, "billing", []tables.TableModelRouterTarget{
		testModelRouterTarget(tables.ModelRouterTierMedium, "openai", "gpt-4o", "", 1),
	}))
	billing := []string{"MEDIUM openai/gpt-4o@ 1"}

	require.NoError(t, store.SetModelRouterTargets(ctx, " support ", []tables.TableModelRouterTarget{
		testModelRouterTarget(tables.ModelRouterTierSimple, "anthropic", "claude-haiku", "", 1),
	}), "the router id is trimmed")
	replaced := []string{"SIMPLE anthropic/claude-haiku@ 1"}
	assert.Equal(t, replaced, describeModelRouterTargets(storedModelRouterTargets(t, store, "support")), "none of the first save's targets are left")
	assert.Equal(t, billing, describeModelRouterTargets(storedModelRouterTargets(t, store, "billing")))

	require.Error(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{
		testModelRouterTarget(tables.ModelRouterTierComplex, "openai", "gpt-5", "", 0.5),
	}))
	assert.Equal(t, replaced, describeModelRouterTargets(storedModelRouterTargets(t, store, "support")), "a refused save keeps the stored targets")

	require.NoError(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{}))
	assert.Empty(t, storedModelRouterTargets(t, store, "support"), "an empty list clears them")
	assert.Equal(t, billing, describeModelRouterTargets(storedModelRouterTargets(t, store, "billing")))
}

// TestModelRouterSavesLeaveTargetsToTheirOwnSave pins that targets riding along
// on a router are ignored by create and update: a new router starts with none,
// and a config save cannot change the ones stored.
func TestModelRouterSavesLeaveTargetsToTheirOwnSave(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	router := testModelRouter("support", "Support")
	router.Targets = testModelRouterTargets()
	require.NoError(t, store.CreateModelRouter(ctx, router))
	assert.Empty(t, storedModelRouterTargets(t, store, "support"), "create stores no targets")

	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	stored := describeModelRouterTargets(storedModelRouterTargets(t, store, "support"))
	update := testModelRouter("support", "Support")
	update.Targets = []tables.TableModelRouterTarget{testModelRouterTarget(tables.ModelRouterTierMedium, "openai", "gpt-4o", "", 1)}
	require.NoError(t, store.UpdateModelRouter(ctx, update))
	assert.Equal(t, stored, describeModelRouterTargets(storedModelRouterTargets(t, store, "support")), "update keeps the stored targets")
}

// TestValidateModelRouterTargetsDuplicates pins what counts as the same target
// twice in a tier: provider and model compare without regard to letter case or
// padding, while a different key, or a different tier, makes a target of its
// own.
func TestValidateModelRouterTargetsDuplicates(t *testing.T) {
	half := func(provider, model, keyID string) tables.TableModelRouterTarget {
		return testModelRouterTarget(tables.ModelRouterTierSimple, provider, model, keyID, 0.5)
	}
	cases := []struct {
		name      string
		targets   []tables.TableModelRouterTarget
		duplicate bool
	}{
		{"provider differing only by letter case", []tables.TableModelRouterTarget{half("openai", "gpt-4o", ""), half("OpenAI", "gpt-4o", "")}, true},
		{"model differing only by letter case", []tables.TableModelRouterTarget{half("openai", "gpt-4o", ""), half("openai", "GPT-4o", "")}, true},
		{"provider and model differing only by padding", []tables.TableModelRouterTarget{half("openai", "gpt-4o", ""), half(" openai ", "gpt-4o ", "")}, true},
		{"the same pinned key twice", []tables.TableModelRouterTarget{half("openai", "gpt-4o", "key-a"), half("openai", "gpt-4o", "key-a")}, true},
		{"the same provider and model on two keys", []tables.TableModelRouterTarget{half("openai", "gpt-4o", "key-a"), half("openai", "gpt-4o", "key-b")}, false},
		{"a pinned and an unpinned copy", []tables.TableModelRouterTarget{half("openai", "gpt-4o", ""), half("openai", "gpt-4o", "key-a")}, false},
		{"the same provider and model in two tiers", []tables.TableModelRouterTarget{
			testModelRouterTarget(tables.ModelRouterTierSimple, "openai", "gpt-4o", "", 1),
			testModelRouterTarget(tables.ModelRouterTierMedium, "openai", "gpt-4o", "", 1),
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelRouterTargets(tc.targets)
			if tc.duplicate {
				require.ErrorContains(t, err, "duplicate target for tier SIMPLE")
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestValidateModelRouterTargetsWeights pins the weight rules: every weight is
// positive, each tier's weights sum to 1 on their own, never pooled with
// another tier's, and a sum within 0.001 of 1 is accepted so a split typed
// with rounded figures still saves.
func TestValidateModelRouterTargetsWeights(t *testing.T) {
	// tier gives one tier a target per weight, each a different model.
	tier := func(name string, weights ...float64) []tables.TableModelRouterTarget {
		targets := make([]tables.TableModelRouterTarget, 0, len(weights))
		for i, weight := range weights {
			targets = append(targets, testModelRouterTarget(name, "openai", fmt.Sprintf("model-%d", i), "", weight))
		}
		return targets
	}
	cases := []struct {
		name    string
		targets []tables.TableModelRouterTarget
		wantErr string
	}{
		{"a sum of 0.9995 is within tolerance", tier("SIMPLE", 0.9995), ""},
		{"a sum of 0.99 is not", tier("SIMPLE", 0.99), "target weights for tier SIMPLE must sum to 1"},
		{"a sum of 1.0005 is within tolerance", tier("SIMPLE", 1.0005), ""},
		{"a sum of 1.01 is not", tier("SIMPLE", 1.01), "target weights for tier SIMPLE must sum to 1"},
		{"a split that lands within tolerance", tier("SIMPLE", 0.5, 0.4995), ""},
		{"three thirds", tier("SIMPLE", 1.0/3, 1.0/3, 1.0/3), ""},
		{"a negative weight in a tier that still sums to 1", tier("SIMPLE", 1.5, -0.5), "weight must be greater than 0"},
		{"a weight that is not a number", tier("SIMPLE", math.NaN()), "weight must be greater than 0"},
		{"every tier sums to 1 on its own", slices.Concat(tier("SIMPLE", 1), tier("MEDIUM", 1), tier("COMPLEX", 0.5, 0.5)), ""},
		{"two tiers that only sum to 1 together", slices.Concat(tier("SIMPLE", 0.5), tier("MEDIUM", 0.5)), "target weights for tier MEDIUM must sum to 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelRouterTargets(tc.targets)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestValidateModelRouterTargetsShapes pins which targets are well formed
// beyond the cases TestValidateModelRouterTargets refuses: a tier typed in any
// letter case or with padding is upper-cased, a provider on its own (with or
// without a pinned key) is a target, and a blank string counts as unset.
func TestValidateModelRouterTargetsShapes(t *testing.T) {
	t.Run("a padded or mixed-case tier is upper-cased in place", func(t *testing.T) {
		targets := []tables.TableModelRouterTarget{
			testModelRouterTarget(" Complex ", "openai", "gpt-5", "", 1),
			testModelRouterTarget("\tsimple\n", "openai", "gpt-4o-mini", "", 1),
		}
		require.NoError(t, ValidateModelRouterTargets(targets))
		assert.Equal(t, tables.ModelRouterTierComplex, targets[0].Tier)
		assert.Equal(t, tables.ModelRouterTierSimple, targets[1].Tier)
	})

	valid := map[string]tables.TableModelRouterTarget{
		"provider only":              testModelRouterTarget("SIMPLE", "openai", "", "", 1),
		"provider with a pinned key": testModelRouterTarget("SIMPLE", "openai", "", "key-1", 1),
		"a key-pinned fallback": {Tier: "SIMPLE", Model: bifrost.Ptr("gpt-4o"), Weight: 1,
			ParsedFallbacks: []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet", KeyID: "key-1"}}}},
	}
	for name, target := range valid {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidateModelRouterTargets([]tables.TableModelRouterTarget{target}))
		})
	}

	invalid := map[string]tables.TableModelRouterTarget{
		"blank provider and model":  {Tier: "SIMPLE", Provider: bifrost.Ptr("  "), Model: bifrost.Ptr(""), Weight: 1},
		"key with a blank provider": {Tier: "SIMPLE", Provider: bifrost.Ptr(" "), Model: bifrost.Ptr("gpt-4o"), KeyID: bifrost.Ptr("key-1"), Weight: 1},
		"blank tier":                testModelRouterTarget("  ", "openai", "gpt-4o", "", 1),
		"fallback without a provider": {Tier: "SIMPLE", Model: bifrost.Ptr("gpt-4o"), Weight: 1,
			ParsedFallbacks: []tables.RoutingFallback{{Fallback: schemas.Fallback{Model: "gpt-4o"}}}},
	}
	for name, target := range invalid {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidateModelRouterTargets([]tables.TableModelRouterTarget{target}))
		})
	}

	t.Run("a half that is unset is stored unset", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{
			testModelRouterTarget("MEDIUM", "openai", "", "", 1),
			testModelRouterTarget("SIMPLE", "", "gpt-4o-mini", "", 1),
		}))
		got := storedModelRouterTargets(t, store, "support")
		require.Len(t, got, 2)
		assert.Nil(t, got[0].Model, "a provider-only target keeps the request's model")
		assert.Nil(t, got[0].KeyID)
		assert.Nil(t, got[1].Provider, "a model-only target keeps the request's provider")
	})
}

// TestModelRouterTargetsReadOrder pins that targets always read back in the
// same order, whatever order they were saved in: by tier, heaviest first
// within a tier, and ties broken by provider, then model, then key. Both the
// single read and the list give that order.
func TestModelRouterTargetsReadOrder(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))

	targets := []tables.TableModelRouterTarget{
		testModelRouterTarget("SIMPLE", "openai", "gpt-4o-mini", "", 1),
		testModelRouterTarget("MEDIUM", "anthropic", "claude-sonnet", "key-a", 0.25),
		testModelRouterTarget("MEDIUM", "anthropic", "claude-sonnet", "", 0.25),
		testModelRouterTarget("MEDIUM", "", "gpt-4o", "", 0.25),
		testModelRouterTarget("MEDIUM", "anthropic", "claude-opus", "", 0.25),
		testModelRouterTarget("COMPLEX", "anthropic", "claude-opus", "", 0.25),
		testModelRouterTarget("COMPLEX", "openai", "gpt-5", "", 0.75),
	}
	want := []string{
		"COMPLEX openai/gpt-5@ 0.75", // the heavier target leads, though anthropic sorts first
		"COMPLEX anthropic/claude-opus@ 0.25",
		"MEDIUM /gpt-4o@ 0.25", // no provider sorts before any provider
		"MEDIUM anthropic/claude-opus@ 0.25",
		"MEDIUM anthropic/claude-sonnet@ 0.25", // no key sorts before a pinned key
		"MEDIUM anthropic/claude-sonnet@key-a 0.25",
		"SIMPLE openai/gpt-4o-mini@ 1",
	}
	reversed := slices.Clone(targets)
	slices.Reverse(reversed)
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", targets))
	require.NoError(t, store.SetModelRouterTargets(ctx, "billing", reversed))

	assert.Equal(t, want, describeModelRouterTargets(storedModelRouterTargets(t, store, "support")))
	assert.Equal(t, want, describeModelRouterTargets(storedModelRouterTargets(t, store, "billing")))

	listed, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	for _, router := range listed {
		assert.Equal(t, want, describeModelRouterTargets(router.Targets), router.ID)
	}
}

// TestModelRouterTargetFallbacksRoundTrip pins that a target's fallback chain
// reads back as it was saved: in order, with a fallback pinned to a key still
// pinned to it, and a target saved without a chain still without one.
func TestModelRouterTargetFallbacksRoundTrip(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))

	unpinned := schemas.Fallback{Provider: "openai", Model: "gpt-4o"}
	pinned := schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet", KeyID: "key-backup"}
	withChain := testModelRouterTarget("COMPLEX", "anthropic", "claude-opus", "", 1)
	withChain.ParsedFallbacks = []tables.RoutingFallback{{Fallback: unpinned}, {Fallback: pinned}}
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{
		withChain,
		testModelRouterTarget("SIMPLE", "openai", "gpt-4o-mini", "", 1),
	}))

	check := func(t *testing.T, targets []tables.TableModelRouterTarget) {
		t.Helper()
		require.Len(t, targets, 2)
		chain := targets[0].ParsedFallbacks
		require.Len(t, chain, 2)
		assert.Equal(t, unpinned, chain[0].Resolved())
		assert.False(t, chain[0].IsKeyPinned())
		assert.Equal(t, pinned, chain[1].Resolved())
		assert.True(t, chain[1].IsKeyPinned())
		assert.Empty(t, targets[1].ParsedFallbacks)
		assert.Nil(t, targets[1].Fallbacks, "no chain is stored as NULL, not as an empty list")
	}
	check(t, storedModelRouterTargets(t, store, "support"))
	listed, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	check(t, listed[0].Targets)
}

// TestModelRouterResetAndEditKeepTargets pins that the two config writes that
// start from the stored router, restoring the default phrases and appending a
// phrase, leave the model each tier gets exactly as it was.
func TestModelRouterResetAndEditKeepTargets(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	stored := storedModelRouterTargets(t, store, "support")
	require.Len(t, stored, 3)

	_, err := store.ResetModelRouter(ctx, "support", testComplexityAnalyzerConfig())
	require.NoError(t, err)
	assert.Equal(t, stored, storedModelRouterTargets(t, store, "support"), "a reset keeps the targets")

	_, err = store.EditModelRouter(ctx, "support", func(router *ModelRouter) (bool, error) {
		router.Keywords.SimpleKeywords = append(router.Keywords.SimpleKeywords, "added phrase")
		return true, nil
	})
	require.NoError(t, err)
	assert.Equal(t, stored, storedModelRouterTargets(t, store, "support"), "an edit keeps the targets")
}
