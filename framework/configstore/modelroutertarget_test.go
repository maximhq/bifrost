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

// TestValidateModelRouterTargets pins what a classifier router's targets are
// checked for before they are stored.
func TestValidateModelRouterTargets(t *testing.T) {
	cases := map[string]func(targets []tables.TableModelRouterTarget){
		"unknown tier": func(ts []tables.TableModelRouterTarget) { ts[0].Tier = "HARD" },
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
			require.Error(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, targets))
		})
	}
	assert.NoError(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, nil), "no targets is valid")
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
			err := ValidateModelRouterTargets(tables.ModelRouterModeClassifier, tc.targets)
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
			err := ValidateModelRouterTargets(tables.ModelRouterModeClassifier, tc.targets)
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
// without a pinned key) is a target, a blank string counts as unset, and a
// target that names neither a provider nor a model keeps the request's own.
func TestValidateModelRouterTargetsShapes(t *testing.T) {
	t.Run("a padded or mixed-case tier is upper-cased in place", func(t *testing.T) {
		targets := []tables.TableModelRouterTarget{
			testModelRouterTarget(" Complex ", "openai", "gpt-5", "", 1),
			testModelRouterTarget("\tsimple\n", "openai", "gpt-4o-mini", "", 1),
		}
		require.NoError(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, targets))
		assert.Equal(t, tables.ModelRouterTierComplex, targets[0].Tier)
		assert.Equal(t, tables.ModelRouterTierSimple, targets[1].Tier)
	})

	valid := map[string]tables.TableModelRouterTarget{
		"provider only":              testModelRouterTarget("SIMPLE", "openai", "", "", 1),
		"provider with a pinned key": testModelRouterTarget("SIMPLE", "openai", "", "key-1", 1),
		"a key-pinned fallback": {Tier: "SIMPLE", Model: bifrost.Ptr("gpt-4o"), Weight: 1,
			ParsedFallbacks: []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet", KeyID: "key-1"}}}},
		"neither provider nor model": testModelRouterTarget("SIMPLE", "", "", "", 1),
		"a blank provider and model": {Tier: "SIMPLE", Provider: bifrost.Ptr("  "), Model: bifrost.Ptr(""), Weight: 1},
		"keeping the model, with a fallback": {Tier: "SIMPLE", Weight: 1,
			ParsedFallbacks: []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet"}}}},
	}
	for name, target := range valid {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, []tables.TableModelRouterTarget{target}))
		})
	}

	invalid := map[string]tables.TableModelRouterTarget{
		"key with a blank provider":           {Tier: "SIMPLE", Provider: bifrost.Ptr(" "), Model: bifrost.Ptr("gpt-4o"), KeyID: bifrost.Ptr("key-1"), Weight: 1},
		"key with neither provider nor model": {Tier: "SIMPLE", KeyID: bifrost.Ptr("key-1"), Weight: 1},
		"blank tier":                          testModelRouterTarget("  ", "openai", "gpt-4o", "", 1),
		"fallback without a provider": {Tier: "SIMPLE", Model: bifrost.Ptr("gpt-4o"), Weight: 1,
			ParsedFallbacks: []tables.RoutingFallback{{Fallback: schemas.Fallback{Model: "gpt-4o"}}}},
	}
	for name, target := range invalid {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, []tables.TableModelRouterTarget{target}))
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

// TestValidateModelRouterTargetsConditional pins a conditional router's
// targets: they name no tier and are one group, so their weights sum to 1
// together and a provider and model may appear once, and a target may keep
// the request's model as a routing rule's can.
func TestValidateModelRouterTargetsConditional(t *testing.T) {
	conditional := tables.ModelRouterModeConditional
	valid := map[string][]tables.TableModelRouterTarget{
		"one target":          {testModelRouterTarget("", "openai", "gpt-4o", "", 1)},
		"a weighted split":    {testModelRouterTarget("", "openai", "gpt-4o", "", 0.7), testModelRouterTarget("", "anthropic", "claude-sonnet", "", 0.3)},
		"keeping the model":   {testModelRouterTarget("", "", "", "", 1)},
		"a padded blank tier": {testModelRouterTarget("  ", "openai", "gpt-4o", "", 1)},
		"no targets":          nil,
	}
	for name, targets := range valid {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidateModelRouterTargets(conditional, targets))
		})
	}

	invalid := map[string]struct {
		targets []tables.TableModelRouterTarget
		wantErr string
	}{
		"a tier": {[]tables.TableModelRouterTarget{testModelRouterTarget("SIMPLE", "openai", "gpt-4o", "", 1)},
			"a conditional router's targets take no tier"},
		"weights that do not sum": {[]tables.TableModelRouterTarget{testModelRouterTarget("", "openai", "gpt-4o", "", 0.5)},
			"target weights must sum to 1"},
		"the same target twice": {[]tables.TableModelRouterTarget{
			testModelRouterTarget("", "openai", "gpt-4o", "", 0.5), testModelRouterTarget("", "OpenAI", "gpt-4o", "", 0.5)},
			"duplicate target: provider"},
		"keeping the model twice": {[]tables.TableModelRouterTarget{
			testModelRouterTarget("", "", "", "", 0.5), testModelRouterTarget("", "", "", "", 0.5)},
			"duplicate target: provider"},
		"fallbacks on a target": {[]tables.TableModelRouterTarget{func() tables.TableModelRouterTarget {
			target := testModelRouterTarget("", "openai", "gpt-4o", "", 1)
			target.ParsedFallbacks = []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet"}}}
			return target
		}()}, "a conditional router takes its fallbacks on the router, not on a target"},
	}
	for name, tc := range invalid {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, ValidateModelRouterTargets(conditional, tc.targets), tc.wantErr)
		})
	}

	t.Run("a classifier router refuses a target without a tier", func(t *testing.T) {
		err := ValidateModelRouterTargets(tables.ModelRouterModeClassifier, []tables.TableModelRouterTarget{testModelRouterTarget("", "openai", "gpt-4o", "", 1)})
		require.ErrorContains(t, err, "tier must be SIMPLE, MEDIUM or COMPLEX")
	})
	t.Run("an empty mode is a classifier's", func(t *testing.T) {
		require.Error(t, ValidateModelRouterTargets("", []tables.TableModelRouterTarget{testModelRouterTarget("", "openai", "gpt-4o", "", 1)}))
		require.NoError(t, ValidateModelRouterTargets("", []tables.TableModelRouterTarget{testModelRouterTarget("SIMPLE", "openai", "gpt-4o", "", 1)}))
	})

	t.Run("the store checks targets against the stored router's mode", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		router := testModelRouter("fast-lane", "Fast lane")
		router.Mode = conditional
		require.NoError(t, store.CreateModelRouter(ctx, router))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))

		require.Error(t, store.SetModelRouterTargets(ctx, "fast-lane", testModelRouterTargets()), "tiered targets do not fit a conditional router")
		require.NoError(t, store.SetModelRouterTargets(ctx, "fast-lane", []tables.TableModelRouterTarget{
			testModelRouterTarget("", "openai", "gpt-4o", "", 0.6),
			testModelRouterTarget("", "", "", "", 0.4),
		}))
		assert.Equal(t, []string{" openai/gpt-4o@ 0.6", " /@ 0.4"}, describeModelRouterTargets(storedModelRouterTargets(t, store, "fast-lane")))
		require.Error(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{testModelRouterTarget("", "openai", "gpt-4o", "", 1)}),
			"a target without a tier does not fit a classifier router")
	})
}

// TestModelRouterTargetFirstTokenDeadline pins a target's first-token
// deadline: optional, at most five minutes, 0 stored as none, kept through a
// save, and part of a config.json entry's hash only when a target sets one.
func TestModelRouterTargetFirstTokenDeadline(t *testing.T) {
	withDeadline := func(ms int) []tables.TableModelRouterTarget {
		target := testModelRouterTarget("SIMPLE", "openai", "gpt-4o", "", 1)
		target.TTFTTimeoutMs = bifrost.Ptr(ms)
		return []tables.TableModelRouterTarget{target}
	}
	for _, ms := range []int{1, 1500, maxModelRouterTTFTTimeoutMs} {
		require.NoError(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, withDeadline(ms)), "%d ms is in range", ms)
	}
	for _, ms := range []int{-1, maxModelRouterTTFTTimeoutMs + 1} {
		require.ErrorContains(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, withDeadline(ms)), "ttft_timeout_ms must be between", "%d ms is out of range", ms)
	}
	zero := withDeadline(0)
	require.NoError(t, ValidateModelRouterTargets(tables.ModelRouterModeClassifier, zero))
	assert.Nil(t, zero[0].TTFTTimeoutMs, "0 means no deadline and is stored as none")

	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", withDeadline(2000)))
	got := storedModelRouterTargets(t, store, "support")
	require.Len(t, got, 1)
	require.NotNil(t, got[0].TTFTTimeoutMs)
	assert.Equal(t, 2000, *got[0].TTFTTimeoutMs)

	bare := *testModelRouter("support", "Support")
	bare.Targets = []tables.TableModelRouterTarget{testModelRouterTarget("SIMPLE", "openai", "gpt-4o", "", 1)}
	unset := bare
	unset.Targets = withDeadline(0)
	set := bare
	set.Targets = withDeadline(2000)
	assert.Equal(t, GenerateModelRouterHash(bare), GenerateModelRouterHash(unset), "a target without a deadline hashes as it did before deadlines existed")
	assert.NotEqual(t, GenerateModelRouterHash(bare), GenerateModelRouterHash(set))
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

// TestModelRouterFallbacksStore pins a conditional router's own fallback chain, the one list a
// routing rule kept for whichever of its targets was chosen. It is saved on its own, survives every
// other save of the router, keeps a key pin and the order it was written in, and an empty list
// clears it. A classifier router has none: its fallbacks belong to each tier's target.
func TestModelRouterFallbacksStore(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	router := testModelRouter("fast-lane", "Fast lane")
	router.Mode = tables.ModelRouterModeConditional
	require.NoError(t, store.CreateModelRouter(ctx, router))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))

	unpinned := schemas.Fallback{Provider: "openai", Model: "gpt-4o"}
	pinned := schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet", KeyID: "key-backup"}
	chain := []tables.RoutingFallback{{Fallback: unpinned}, {Fallback: pinned}}
	stored := func(t *testing.T) []tables.RoutingFallback {
		t.Helper()
		got, err := store.GetModelRouter(ctx, "fast-lane")
		require.NoError(t, err)
		return got.Fallbacks
	}

	require.Empty(t, stored(t), "a new router has no chain")
	require.NoError(t, store.SetModelRouterFallbacks(ctx, "fast-lane", chain))
	got := stored(t)
	require.Len(t, got, 2)
	assert.Equal(t, unpinned, got[0].Resolved())
	assert.Equal(t, pinned, got[1].Resolved())
	assert.True(t, got[1].IsKeyPinned())

	listed, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	for _, r := range listed {
		if r.ID == "fast-lane" {
			assert.Len(t, r.Fallbacks, 2, "the list carries each router's chain")
		}
	}

	t.Run("other saves of the router keep it", func(t *testing.T) {
		update := testModelRouter("fast-lane", "Fast lane, renamed")
		require.NoError(t, store.UpdateModelRouter(ctx, update))
		require.NoError(t, store.SetModelRouterTargets(ctx, "fast-lane", []tables.TableModelRouterTarget{
			testModelRouterTarget("", "openai", "gpt-4o", "", 0.5), testModelRouterTarget("", "anthropic", "claude-sonnet", "", 0.5),
		}))
		require.NoError(t, store.SetModelRouterRouting(ctx, "fast-lane", ModelRouterRouting{Enabled: true, Scope: "global", Priority: 7}))
		assert.Len(t, stored(t), 2)
	})

	t.Run("a target of the router cannot carry its own", func(t *testing.T) {
		target := testModelRouterTarget("", "openai", "gpt-4o", "", 1)
		target.ParsedFallbacks = chain
		err := store.SetModelRouterTargets(ctx, "fast-lane", []tables.TableModelRouterTarget{target})
		require.ErrorIs(t, err, ErrInvalidModelRouterTargets)
		require.ErrorContains(t, err, "on the router, not on a target")
	})

	t.Run("what is refused", func(t *testing.T) {
		err := store.SetModelRouterFallbacks(ctx, "support", chain)
		require.ErrorIs(t, err, ErrInvalidModelRouterTargets, "a classifier router has no chain of its own")
		require.ErrorContains(t, err, "on each target, not on the router")

		err = store.SetModelRouterFallbacks(ctx, "fast-lane", []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "no-such-provider", Model: "m"}}})
		require.ErrorIs(t, err, ErrInvalidModelRouterTargets)
		require.ErrorContains(t, err, "must use a known provider prefix")

		require.ErrorIs(t, store.SetModelRouterFallbacks(ctx, "no-such-router", chain), ErrNotFound)
		assert.Len(t, stored(t), 2, "a refused write leaves the stored chain")
	})

	t.Run("an empty list clears it", func(t *testing.T) {
		require.NoError(t, store.SetModelRouterFallbacks(ctx, "fast-lane", []tables.RoutingFallback{}))
		assert.Empty(t, stored(t))
		var row tables.TableModelRouter
		require.NoError(t, store.DB().WithContext(ctx).First(&row, "id = ?", "fast-lane").Error)
		assert.Nil(t, row.Fallbacks, "no chain is stored as NULL, not as an empty list")
	})

	t.Run("a router created with a chain stores it", func(t *testing.T) {
		withChain := testModelRouter("with-chain", "With chain")
		withChain.Mode = tables.ModelRouterModeConditional
		withChain.Fallbacks = chain
		require.NoError(t, store.CreateModelRouter(ctx, withChain))
		got, err := store.GetModelRouter(ctx, "with-chain")
		require.NoError(t, err)
		assert.Len(t, got.Fallbacks, 2)

		classifier := testModelRouter("classifier-chain", "Classifier chain")
		classifier.Fallbacks = chain
		require.ErrorContains(t, store.CreateModelRouter(ctx, classifier), "on each target, not on the router")
	})
}

// TestGenerateModelRouterHashFallbacks pins the fallback chain's part in a config.json entry's
// hash: an entry that declares none hashes as it did before routers had a chain, and the chain's
// entries, their order and a key pin each change the hash.
func TestGenerateModelRouterHashFallbacks(t *testing.T) {
	entry := func(fallbacks []tables.RoutingFallback) ModelRouter {
		return ModelRouter{ID: "fast-lane", Name: "Fast lane", Mode: tables.ModelRouterModeConditional, Fallbacks: fallbacks}
	}
	a := tables.RoutingFallback{Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o"}}
	b := tables.RoutingFallback{Fallback: schemas.Fallback{Provider: "anthropic", Model: "claude-sonnet"}}
	pinned := tables.RoutingFallback{Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o", KeyID: "key-1"}}

	none := GenerateModelRouterHash(entry(nil))
	withoutField := ModelRouter{ID: "fast-lane", Name: "Fast lane", Mode: tables.ModelRouterModeConditional}
	assert.Equal(t, GenerateModelRouterHash(withoutField), none)
	assert.NotEqual(t, none, GenerateModelRouterHash(entry([]tables.RoutingFallback{})), "an empty list is declared, and clears the stored chain")
	ab := GenerateModelRouterHash(entry([]tables.RoutingFallback{a, b}))
	assert.Equal(t, ab, GenerateModelRouterHash(entry([]tables.RoutingFallback{a, b})))
	assert.NotEqual(t, ab, GenerateModelRouterHash(entry([]tables.RoutingFallback{b, a})), "the order is the order they are tried in")
	assert.NotEqual(t, GenerateModelRouterHash(entry([]tables.RoutingFallback{a})), GenerateModelRouterHash(entry([]tables.RoutingFallback{pinned})))
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
