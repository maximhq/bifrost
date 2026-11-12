package rules

import (
	"context"
	"fmt"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stickyPickRepeats is how many times a test asks for the same session's target.
const stickyPickRepeats = 200

// stickyTarget builds a target that is identified by provider and model alone.
func stickyTarget(provider, model string, weight float64) configstoreTables.TableRoutingTarget {
	return configstoreTables.TableRoutingTarget{Provider: bifrost.Ptr(provider), Model: bifrost.Ptr(model), Weight: weight}
}

// countStickyPicks picks a target for n distinct session keys and counts the picks by provider.
func countStickyPicks(t *testing.T, targets []configstoreTables.TableRoutingTarget, n int) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for i := range n {
		target, ok := selectStickyWeightedTarget(targets, fmt.Sprintf("session-%d\x00rule-1", i))
		require.True(t, ok)
		counts[*target.Provider]++
	}
	return counts
}

func TestSelectStickyWeightedTargetSameKeySameTarget(t *testing.T) {
	targets := []configstoreTables.TableRoutingTarget{
		stickyTarget("openai", "gpt-4o", 0.5),
		stickyTarget("anthropic", "claude-sonnet", 0.5),
	}
	for i := range 50 {
		key := fmt.Sprintf("session-%d\x00rule-1", i)
		first, ok := selectStickyWeightedTarget(targets, key)
		require.True(t, ok)
		for range stickyPickRepeats {
			again, ok := selectStickyWeightedTarget(targets, key)
			require.True(t, ok)
			require.Equal(t, *first.Provider, *again.Provider, "key %q moved between targets", key)
		}
	}
}

func TestSelectStickyWeightedTargetSplitFollowsTheWeights(t *testing.T) {
	for _, tc := range []struct {
		name      string
		providers []string
		weights   []float64
	}{
		{name: "70/30", providers: []string{"openai", "anthropic"}, weights: []float64{0.7, 0.3}},
		{name: "50/30/20", providers: []string{"openai", "anthropic", "groq"}, weights: []float64{0.5, 0.3, 0.2}},
		{name: "all zero is uniform", providers: []string{"openai", "anthropic", "groq"}, weights: []float64{0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts := countStickyPicks(t, weightedTargets(tc.providers, tc.weights), targetDraws)
			expected := tc.weights
			if tc.weights[0] == 0 {
				expected = []float64{1, 1, 1}
			}
			requireTargetSplit(t, tc.providers, counts, expected)
		})
	}
}

func TestSelectStickyWeightedTargetNeverPicksZeroWeight(t *testing.T) {
	targets := []configstoreTables.TableRoutingTarget{
		stickyTarget("openai", "gpt-4o", 1),
		stickyTarget("anthropic", "claude-sonnet", 0),
	}
	counts := countStickyPicks(t, targets, 2000)
	assert.Equal(t, 0, counts["anthropic"])
}

func TestSelectStickyWeightedTargetInvalidInput(t *testing.T) {
	_, ok := selectStickyWeightedTarget(nil, "session-1")
	assert.False(t, ok, "no targets")

	_, ok = selectStickyWeightedTarget([]configstoreTables.TableRoutingTarget{stickyTarget("openai", "gpt-4o", -1)}, "session-1")
	assert.False(t, ok, "only negative weights")

	target, ok := selectStickyWeightedTarget([]configstoreTables.TableRoutingTarget{
		stickyTarget("openai", "gpt-4o", -1),
		stickyTarget("anthropic", "claude-sonnet", 0.5),
	}, "session-1")
	require.True(t, ok)
	assert.Equal(t, "anthropic", *target.Provider, "negative weights are filtered out")
}

// The rule store may hand targets back in any order, so the order must not move a session.
func TestSelectStickyWeightedTargetIgnoresTargetOrder(t *testing.T) {
	forward := []configstoreTables.TableRoutingTarget{
		stickyTarget("openai", "gpt-4o", 0.5),
		stickyTarget("anthropic", "claude-sonnet", 0.3),
		stickyTarget("groq", "llama", 0.2),
	}
	reversed := []configstoreTables.TableRoutingTarget{forward[2], forward[1], forward[0]}
	for i := range 500 {
		key := fmt.Sprintf("session-%d\x00rule-1", i)
		a, _ := selectStickyWeightedTarget(forward, key)
		b, _ := selectStickyWeightedTarget(reversed, key)
		require.Equal(t, *a.Provider, *b.Provider, "key %q depends on target order", key)
	}
}

// Removing a target moves only the sessions that were on it.
func TestSelectStickyWeightedTargetRemovingATargetMovesOnlyItsSessions(t *testing.T) {
	all := []configstoreTables.TableRoutingTarget{
		stickyTarget("openai", "gpt-4o", 0.4),
		stickyTarget("anthropic", "claude-sonnet", 0.3),
		stickyTarget("groq", "llama", 0.3),
	}
	withoutGroq := all[:2]
	moved := 0
	for i := range 2000 {
		key := fmt.Sprintf("session-%d\x00rule-1", i)
		before, _ := selectStickyWeightedTarget(all, key)
		after, _ := selectStickyWeightedTarget(withoutGroq, key)
		if *before.Provider == "groq" {
			moved++
			continue
		}
		require.Equal(t, *before.Provider, *after.Provider, "key %q moved although its target stayed", key)
	}
	assert.Positive(t, moved, "some sessions should have been on the removed target")
}

// Targets that differ only by key pin are different targets.
func TestSelectStickyWeightedTargetDistinguishesKeyPins(t *testing.T) {
	a := stickyTarget("openai", "gpt-4o", 0.5)
	a.KeyID = bifrost.Ptr("key-a")
	b := stickyTarget("openai", "gpt-4o", 0.5)
	b.KeyID = bifrost.Ptr("key-b")
	counts := map[string]int{}
	for i := range 2000 {
		target, ok := selectStickyWeightedTarget([]configstoreTables.TableRoutingTarget{a, b}, fmt.Sprintf("session-%d", i))
		require.True(t, ok)
		counts[*target.KeyID]++
	}
	assert.Positive(t, counts["key-a"])
	assert.Positive(t, counts["key-b"])
}

// stickyRule is a 50/50 rule across two providers.
func stickyRule(sticky bool) *configstoreTables.TableRoutingRule {
	return &configstoreTables.TableRoutingRule{
		ID:            "sticky-1",
		Name:          "Sticky Rule",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			stickyTarget("openai", "gpt-4o", 0.5),
			stickyTarget("anthropic", "claude-sonnet", 0.5),
		},
		StickySessions: sticky,
		Enabled:        bifrost.Ptr(true),
		Scope:          "global",
	}
}

// evaluateStickyRule evaluates the rule once for a request with the given session id ("" = none).
func evaluateStickyRule(t *testing.T, engine *Engine, sessionID string) string {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now())
	if sessionID != "" {
		ctx.SetValue(schemas.BifrostContextKeySessionID, sessionID)
	}
	decision, err := engine.EvaluateRoutingRules(ctx, &EvaluationContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	return decision.Provider
}

func TestEvaluateRoutingRulesStickyRuleKeepsSessionOnOneTarget(t *testing.T) {
	store, engine, err := newTestEngine(DefaultChainMaxDepth)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), stickyRule(true)))

	seen := map[string]bool{}
	for i := range 40 {
		session := fmt.Sprintf("conversation-%d", i)
		first := evaluateStickyRule(t, engine, session)
		seen[first] = true
		for range 25 {
			require.Equal(t, first, evaluateStickyRule(t, engine, session), "session %q switched provider", session)
		}
	}
	assert.Len(t, seen, 2, "different sessions should still spread across both targets")
}

func TestEvaluateRoutingRulesStickyRuleWithoutSessionStaysRandom(t *testing.T) {
	store, engine, err := newTestEngine(DefaultChainMaxDepth)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), stickyRule(true)))

	seen := map[string]bool{}
	for range stickyPickRepeats {
		seen[evaluateStickyRule(t, engine, "")] = true
	}
	assert.Len(t, seen, 2, "a request with no session id keeps weighted random selection")
}

func TestEvaluateRoutingRulesNonStickyRuleIgnoresSession(t *testing.T) {
	store, engine, err := newTestEngine(DefaultChainMaxDepth)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRule(context.Background(), stickyRule(false)))

	seen := map[string]bool{}
	for range stickyPickRepeats {
		seen[evaluateStickyRule(t, engine, "conversation-1")] = true
	}
	assert.Len(t, seen, 2, "sticky_sessions=false keeps the existing per-request random draw")
}
