package rules

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateTestRule builds a rule whose two equal-weight targets make sticky and
// random selection distinguishable: a sticky run collapses to one provider,
// a random one visits both within a handful of draws.
func gateTestRule(id, expression string, chain bool, priority int) *configstoreTables.TableRoutingRule {
	return &configstoreTables.TableRoutingRule{
		ID:            id,
		Name:          id,
		CelExpression: expression,
		Enabled:       bifrost.Ptr(true),
		Scope:         "global",
		Priority:      priority,
		ChainRule:     chain,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("anthropic"), Model: bifrost.Ptr("claude-3-5-sonnet"), Weight: 1.0},
			{Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4o-mini"), Weight: 1.0},
		},
	}
}

// distinctTargetsOverRuns evaluates the same routing context repeatedly and
// reports how many distinct provider/model pairs came back. One means the
// selection was sticky; two means it was still random.
func distinctTargetsOverRuns(t *testing.T, engine *Engine, build func() *EvaluationContext, runs int) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for i := 0; i < runs; i++ {
		decision, err := engine.EvaluateRoutingRules(
			schemas.NewBifrostContext(context.Background(), time.Now()), build())
		require.NoError(t, err)
		require.NotNil(t, decision)
		seen[decision.Provider+"/"+decision.Model]++
	}
	return seen
}

func gateTestEngine(t *testing.T, rules ...*configstoreTables.TableRoutingRule) *Engine {
	t.Helper()
	ctx := context.Background()
	store, err := newTestRuleStore()
	require.NoError(t, err)
	for _, rule := range rules {
		require.NoError(t, store.UpsertRule(ctx, rule))
	}
	engine, err := NewEngine(store, NewMockGovernanceStore(), NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)
	return engine
}

// The x-bf-session-id header predates session complexity and is already used for
// unrelated purposes such as API-key stickiness. A deployment that sends it with
// session mode off must keep the random weighted routing it has today.
func TestSessionStickinessRequiresSessionModeEnabled(t *testing.T) {
	engine := gateTestEngine(t, gateTestRule("gate-complexity", `complexity_tier == "COMPLEX"`, false, 1))

	seen := distinctTargetsOverRuns(t, engine, func() *EvaluationContext {
		return &EvaluationContext{
			Provider:                 schemas.OpenAI,
			Model:                    "gpt-4o",
			RequestType:              "chat_completion",
			SessionID:                "session-abc",
			SessionComplexityEnabled: false,
			ComputeComplexity: func() *complexity.ComplexityResult {
				return &complexity.ComplexityResult{Tier: "COMPLEX"}
			},
		}
	}, 100)

	assert.Len(t, seen, 2, "session mode is off, so selection must stay random: %v", seen)
}

// Stickiness is a complexity-routing behaviour. A rule that never consults
// complexity_tier is outside the feature's scope even when session mode is on.
func TestSessionStickinessRequiresComplexityRelevance(t *testing.T) {
	engine := gateTestEngine(t, gateTestRule("gate-no-complexity", `model == "gpt-4o"`, false, 1))

	seen := distinctTargetsOverRuns(t, engine, func() *EvaluationContext {
		return &EvaluationContext{
			Provider:                 schemas.OpenAI,
			Model:                    "gpt-4o",
			RequestType:              "chat_completion",
			SessionID:                "session-abc",
			SessionComplexityEnabled: true,
			ComputeComplexity: func() *complexity.ComplexityResult {
				return &complexity.ComplexityResult{Tier: "COMPLEX"}
			},
		}
	}, 100)

	assert.Len(t, seen, 2, "rule never references complexity_tier, so selection must stay random: %v", seen)
}

// Both gates open: this is the case the feature exists for.
func TestSessionStickinessAppliesWhenEnabledAndComplexityRelevant(t *testing.T) {
	engine := gateTestEngine(t, gateTestRule("gate-both", `complexity_tier == "COMPLEX"`, false, 1))

	seen := distinctTargetsOverRuns(t, engine, func() *EvaluationContext {
		return &EvaluationContext{
			Provider:                 schemas.OpenAI,
			Model:                    "gpt-4o",
			RequestType:              "chat_completion",
			SessionID:                "session-abc",
			SessionComplexityEnabled: true,
			ComputeComplexity: func() *complexity.ComplexityResult {
				return &complexity.ComplexityResult{Tier: "COMPLEX"}
			},
		}
	}, 100)

	assert.Len(t, seen, 1, "session should have pinned a single target: %v", seen)
}

// Different sessions must still spread across the targets; a gate that pinned
// every session to the same target would look sticky while destroying the
// weighting.
func TestSessionStickinessStillSpreadsAcrossSessions(t *testing.T) {
	engine := gateTestEngine(t, gateTestRule("gate-spread", `complexity_tier == "COMPLEX"`, false, 1))

	session := 0
	seen := distinctTargetsOverRuns(t, engine, func() *EvaluationContext {
		session++
		return &EvaluationContext{
			Provider:                 schemas.OpenAI,
			Model:                    "gpt-4o",
			RequestType:              "chat_completion",
			SessionID:                "session-" + string(rune('a'+session%26)) + string(rune('a'+session/26)),
			SessionComplexityEnabled: true,
			ComputeComplexity: func() *complexity.ComplexityResult {
				return &complexity.ComplexityResult{Tier: "COMPLEX"}
			},
		}
	}, 100)

	assert.Len(t, seen, 2, "distinct sessions must still spread across targets: %v", seen)
}

// The tier is resolved by one rule and the target is often chosen by a later one
// in the same chain. If relevance were scoped to the rule that names
// complexity_tier, the terminal rule would re-roll every turn and discard the
// prompt cache the pinned tier exists to protect.
func TestSessionStickinessLatchesAcrossChain(t *testing.T) {
	// Rule A consults complexity and chains; rule B is terminal, carries the
	// weighted targets, and never mentions complexity_tier.
	ruleA := gateTestRule("chain-complexity", `complexity_tier == "COMPLEX"`, true, 0)
	ruleA.Targets = []configstoreTables.TableRoutingTarget{
		{Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4-turbo"), Weight: 1.0},
	}
	ruleB := gateTestRule("chain-terminal", `model == "gpt-4-turbo"`, false, 1)

	engine := gateTestEngine(t, ruleA, ruleB)

	seen := distinctTargetsOverRuns(t, engine, func() *EvaluationContext {
		return &EvaluationContext{
			Provider:                 schemas.OpenAI,
			Model:                    "gpt-4o",
			RequestType:              "chat_completion",
			SessionID:                "session-abc",
			SessionComplexityEnabled: true,
			ComputeComplexity: func() *complexity.ComplexityResult {
				return &complexity.ComplexityResult{Tier: "COMPLEX"}
			},
		}
	}, 100)

	assert.Len(t, seen, 1, "terminal rule in a complexity chain should be sticky: %v", seen)
}

// A chain that never touches complexity stays random even with session mode on,
// confirming the latch starts closed rather than defaulting open.
func TestSessionStickinessLatchStartsClosed(t *testing.T) {
	ruleA := gateTestRule("plain-chain", `model == "gpt-4o"`, true, 0)
	ruleA.Targets = []configstoreTables.TableRoutingTarget{
		{Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4-turbo"), Weight: 1.0},
	}
	ruleB := gateTestRule("plain-terminal", `model == "gpt-4-turbo"`, false, 1)

	engine := gateTestEngine(t, ruleA, ruleB)

	seen := distinctTargetsOverRuns(t, engine, func() *EvaluationContext {
		return &EvaluationContext{
			Provider:                 schemas.OpenAI,
			Model:                    "gpt-4o",
			RequestType:              "chat_completion",
			SessionID:                "session-abc",
			SessionComplexityEnabled: true,
		}
	}, 100)

	assert.Len(t, seen, 2, "no rule consulted complexity, so selection must stay random: %v", seen)
}
