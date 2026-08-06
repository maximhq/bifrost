package governance

import (
	"fmt"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stickyTestTargets(weights ...float64) []configstoreTables.TableRoutingTarget {
	targets := make([]configstoreTables.TableRoutingTarget, 0, len(weights))
	for i, weight := range weights {
		targets = append(targets, configstoreTables.TableRoutingTarget{
			RuleID:   "rule-1",
			Provider: bifrost.Ptr(fmt.Sprintf("provider-%d", i)),
			Model:    bifrost.Ptr(fmt.Sprintf("model-%d", i)),
			Weight:   weight,
		})
	}
	return targets
}

// Stickiness within a session is the whole point: a tier that re-resolves to a
// different target each turn discards the provider-side cache anyway.
func TestSelectWeightedTargetStickyWithinSession(t *testing.T) {
	targets := stickyTestTargets(0.5, 0.3, 0.2)

	first, ok := selectWeightedTarget(targets, "session-abc", "rule-1")
	require.True(t, ok)
	for i := 0; i < 200; i++ {
		got, ok := selectWeightedTarget(targets, "session-abc", "rule-1")
		require.True(t, ok)
		require.Equal(t, *first.Provider, *got.Provider, "target changed on call %d", i)
	}
}

// Targets carry no ID column and arrive in whatever order the DB returned. If
// the walk order were not normalised, the same session would land on different
// targets across queries — stickiness that silently degrades to random.
func TestSelectWeightedTargetStickyIndependentOfTargetOrder(t *testing.T) {
	targets := stickyTestTargets(0.5, 0.3, 0.2)
	reversed := make([]configstoreTables.TableRoutingTarget, 0, len(targets))
	for i := len(targets) - 1; i >= 0; i-- {
		reversed = append(reversed, targets[i])
	}

	for _, session := range []string{"s-1", "s-2", "s-3", "s-4", "s-5"} {
		forward, ok := selectWeightedTarget(targets, session, "rule-1")
		require.True(t, ok)
		backward, ok := selectWeightedTarget(reversed, session, "rule-1")
		require.True(t, ok)
		assert.Equal(t, *forward.Provider, *backward.Provider, "session %s moved when target order changed", session)
	}
}

// Sorting for the sticky walk must not mutate the caller's slice: rule.Targets
// is shared cached config, and reordering it under concurrent readers would be
// a data race.
func TestSelectWeightedTargetDoesNotMutateCallerSlice(t *testing.T) {
	targets := stickyTestTargets(0.2, 0.5, 0.3)
	before := make([]string, len(targets))
	for i, target := range targets {
		before[i] = *target.Provider
	}

	_, ok := selectWeightedTarget(targets, "session-abc", "rule-1")
	require.True(t, ok)

	after := make([]string, len(targets))
	for i, target := range targets {
		after[i] = *target.Provider
	}
	assert.Equal(t, before, after)
}

// Stickiness must not cost proportionality: across many sessions the hash has to
// spread like the RNG it replaced.
func TestSelectWeightedTargetStickyDistributionMatchesWeights(t *testing.T) {
	targets := stickyTestTargets(0.5, 0.3, 0.2)
	const sessions = 20000

	counts := map[string]int{}
	for i := 0; i < sessions; i++ {
		target, ok := selectWeightedTarget(targets, fmt.Sprintf("session-%d", i), "rule-1")
		require.True(t, ok)
		counts[*target.Provider]++
	}

	for i, want := range []float64{0.5, 0.3, 0.2} {
		provider := fmt.Sprintf("provider-%d", i)
		got := float64(counts[provider]) / float64(sessions)
		assert.InDelta(t, want, got, 0.02, "provider %s share", provider)
	}
}

// A session matching two rules must draw independently for each. Keying the
// hash on the session alone would put it at the same slice position in both,
// correlating the choices and skewing the aggregate away from the weights.
func TestSelectWeightedTargetStickyDecorrelatesAcrossRules(t *testing.T) {
	targets := stickyTestTargets(0.5, 0.5)
	const sessions = 2000

	same := 0
	for i := 0; i < sessions; i++ {
		session := fmt.Sprintf("session-%d", i)
		a, ok := selectWeightedTarget(targets, session, "rule-a")
		require.True(t, ok)
		b, ok := selectWeightedTarget(targets, session, "rule-b")
		require.True(t, ok)
		if *a.Provider == *b.Provider {
			same++
		}
	}

	// Independent draws over two equal-weight targets agree about half the time.
	agreement := float64(same) / float64(sessions)
	assert.InDelta(t, 0.5, agreement, 0.05)
}

// Weight edits must move roughly the affected share of sessions, not reshuffle
// everyone — otherwise a routine weight tweak invalidates every live cache.
func TestSelectWeightedTargetStickyProportionalUnderWeightChange(t *testing.T) {
	before := stickyTestTargets(0.5, 0.5)
	after := stickyTestTargets(0.6, 0.4)
	const sessions = 20000

	moved := 0
	for i := 0; i < sessions; i++ {
		session := fmt.Sprintf("session-%d", i)
		a, ok := selectWeightedTarget(before, session, "rule-1")
		require.True(t, ok)
		b, ok := selectWeightedTarget(after, session, "rule-1")
		require.True(t, ok)
		if *a.Provider != *b.Provider {
			moved++
		}
	}

	// A 0.1 shift in the cumulative boundary should relocate ~10% of sessions.
	churn := float64(moved) / float64(sessions)
	assert.InDelta(t, 0.10, churn, 0.02, "weight change churned %.1f%% of sessions", churn*100)
}

// All-zero weights take a separate uniform branch that must be sticky too.
func TestSelectWeightedTargetStickyAllZeroWeights(t *testing.T) {
	targets := stickyTestTargets(0, 0, 0)

	first, ok := selectWeightedTarget(targets, "session-abc", "rule-1")
	require.True(t, ok)
	for i := 0; i < 100; i++ {
		got, ok := selectWeightedTarget(targets, "session-abc", "rule-1")
		require.True(t, ok)
		require.Equal(t, *first.Provider, *got.Provider)
	}

	const sessions = 9000
	counts := map[string]int{}
	for i := 0; i < sessions; i++ {
		target, ok := selectWeightedTarget(targets, fmt.Sprintf("session-%d", i), "rule-1")
		require.True(t, ok)
		counts[*target.Provider]++
	}
	for i := 0; i < 3; i++ {
		share := float64(counts[fmt.Sprintf("provider-%d", i)]) / float64(sessions)
		assert.InDelta(t, 1.0/3.0, share, 0.03)
	}
}

// No session ID is the normal case and must behave exactly as before.
func TestSelectWeightedTargetWithoutSessionStaysRandom(t *testing.T) {
	targets := stickyTestTargets(0.5, 0.5)
	const draws = 20000

	counts := map[string]int{}
	for i := 0; i < draws; i++ {
		target, ok := selectWeightedTarget(targets, "", "rule-1")
		require.True(t, ok)
		counts[*target.Provider]++
	}

	require.Len(t, counts, 2, "random selection collapsed to a single target")
	share := float64(counts["provider-0"]) / float64(draws)
	assert.InDelta(t, 0.5, share, 0.02)
}

// The existing contract: empty and all-negative slices report ok=false, a lone
// target always wins, and a zero-weight target alongside a positive one is
// never selected.
func TestSelectWeightedTargetPreservesExistingContract(t *testing.T) {
	for _, sessionID := range []string{"", "session-abc"} {
		t.Run("session="+sessionID, func(t *testing.T) {
			_, ok := selectWeightedTarget(nil, sessionID, "rule-1")
			assert.False(t, ok)

			_, ok = selectWeightedTarget(stickyTestTargets(-1, -2), sessionID, "rule-1")
			assert.False(t, ok)

			single, ok := selectWeightedTarget(stickyTestTargets(0.25), sessionID, "rule-1")
			require.True(t, ok)
			assert.Equal(t, "provider-0", *single.Provider)

			for i := 0; i < 100; i++ {
				got, ok := selectWeightedTarget(stickyTestTargets(1.0, 0.0), fmt.Sprintf("s-%d", i), "rule-1")
				require.True(t, ok)
				require.Equal(t, "provider-0", *got.Provider)
			}
		})
	}
}

// The selection point must be a uniform [0,1) — never 1.0, which would walk off
// the end of the cumulative range.
func TestSessionSelectionPointRange(t *testing.T) {
	const samples = 50000
	buckets := make([]int, 10)
	for i := 0; i < samples; i++ {
		point := sessionSelectionPoint(fmt.Sprintf("session-%d", i), "rule-1")
		require.GreaterOrEqual(t, point, 0.0)
		require.Less(t, point, 1.0)
		buckets[int(point*10)]++
	}
	for i, count := range buckets {
		share := float64(count) / float64(samples)
		assert.InDelta(t, 0.1, share, 0.01, "decile %d", i)
	}
}

// Targets that route differently must get different identities, or the sort
// would treat them as interchangeable and stickiness would depend on input
// order again. In particular the field separator must not let a value from one
// column bleed into the next.
func TestRoutingTargetIdentitySeparatesFields(t *testing.T) {
	identities := map[string]bool{}
	for _, target := range []configstoreTables.TableRoutingTarget{
		{Provider: bifrost.Ptr("a"), Model: bifrost.Ptr("b")},
		{Provider: bifrost.Ptr("a"), Model: bifrost.Ptr("b"), KeyID: bifrost.Ptr("k")},
		{Provider: bifrost.Ptr("ab")},
		{Model: bifrost.Ptr("ab")},
		{Provider: bifrost.Ptr("a")},
	} {
		identity := routingTargetIdentity(target)
		require.False(t, identities[identity], "identity collision: %q", identity)
		identities[identity] = true
	}
	assert.Len(t, identities, 5)
}

// A nil field and an empty one mean the same thing at selection time ("inherit
// from the request"), so they must collapse rather than sort as two distinct
// positions.
func TestRoutingTargetIdentityTreatsNilAndEmptyAlike(t *testing.T) {
	assert.Equal(t,
		routingTargetIdentity(configstoreTables.TableRoutingTarget{Provider: bifrost.Ptr("a")}),
		routingTargetIdentity(configstoreTables.TableRoutingTarget{Provider: bifrost.Ptr("a"), Model: bifrost.Ptr("")}),
	)
}
