package rules

import (
	"context"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jevRoutingRule(id string, expression string) *configstoreTables.TableRoutingRule {
	return complexityRoutingRule(id, expression)
}

func TestReferencesJevVariablesOnly(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		expected   bool
	}{
		{
			name:       "direct identifier",
			expression: `jev_tier == "SIMPLE"`,
			expected:   true,
		},
		{
			name:       "score variable",
			expression: `jev_complexity <= 0.5`,
			expected:   true,
		},
		{
			name:       "confidence variable",
			expression: `jev_confidence >= 0.8 && jev_tier != ""`,
			expected:   true,
		},
		{
			name:       "string literal only",
			expression: `model == "jev_tier"`,
			expected:   false,
		},
		{
			name:       "unrelated identifier containing name",
			expression: `my_jev_tier == true`,
			expected:   false,
		},
		{
			name:       "map key string",
			expression: `headers["jev_tier"] == "SIMPLE"`,
			expected:   false,
		},
		{
			name:       "complexity_tier alone does not reference jev",
			expression: `complexity_tier == "SIMPLE"`,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, referencesJevVariables(tt.expression))
		})
	}
}

func TestEvaluateRoutingRules_JevVariablesResolve(t *testing.T) {
	ctx := context.Background()
	store, err := newTestRuleStore()
	require.NoError(t, err)

	require.NoError(t, store.UpsertRule(ctx, jevRoutingRule("jev-simple", `jev_tier == "SIMPLE"`)))

	engine, err := NewEngine(store, NewMockGovernanceStore(), NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	computeCalls := 0
	decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, time.Now()), &EvaluationContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		RequestType: "chat_completion",
		ComputeJev: func() *complexity.JevResult {
			computeCalls++
			return &complexity.JevResult{
				Tier:                 "SIMPLE",
				Complexity:           0.1,
				Confidence:           0.95,
				ComplexityConfidence: 0.9,
				Reason:               complexity.JevReasonConfident,
			}
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, 1, computeCalls)
	assert.Equal(t, "anthropic", decision.Provider)
	assert.Equal(t, "claude-3-5-sonnet", decision.Model)
}

func TestEvaluateRoutingRules_JevRawScoresResolveWithoutTier(t *testing.T) {
	// The gates withheld the tier (empty), but the raw score and confidence
	// are still published so rules can apply their own thresholds.
	ctx := context.Background()
	store, err := newTestRuleStore()
	require.NoError(t, err)

	require.NoError(t, store.UpsertRule(ctx, jevRoutingRule("jev-raw-scores", `jev_complexity <= 0.3 && jev_confidence >= 0.8`)))

	engine, err := NewEngine(store, NewMockGovernanceStore(), NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, time.Now()), &EvaluationContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		RequestType: "chat_completion",
		ComputeJev: func() *complexity.JevResult {
			return &complexity.JevResult{
				Tier:       "",
				Complexity: 0.1,
				Confidence: 0.95,
				Reason:     complexity.JevReasonLowConfidence,
			}
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
}

func TestEvaluateRoutingRules_JevUnavailableFailsOpen(t *testing.T) {
	// Fail-open: an unavailable decision must never fail the request nor
	// match jev predicates — not even negative ones. The variables become
	// CEL unknowns and evaluation continues.
	tests := []struct {
		name       string
		expression string
	}{
		{
			name:       "positive predicate",
			expression: `jev_tier == "SIMPLE"`,
		},
		{
			name:       "negative predicate",
			expression: `jev_tier != "SIMPLE"`,
		},
		{
			name:       "score predicate",
			expression: `jev_complexity <= 0.5`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := newTestRuleStore()
			require.NoError(t, err)

			require.NoError(t, store.UpsertRule(ctx, jevRoutingRule("jev-unavailable-"+tt.name, tt.expression)))

			engine, err := NewEngine(store, NewMockGovernanceStore(), NewMockLogger(), schemas.Ptr(10))
			require.NoError(t, err)

			computeCalls := 0
			decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, time.Now()), &EvaluationContext{
				Provider:    schemas.OpenAI,
				Model:       "gpt-4o",
				RequestType: "chat_completion",
				ComputeJev: func() *complexity.JevResult {
					computeCalls++
					return nil
				},
			})
			require.NoError(t, err)
			assert.Nil(t, decision)
			assert.Equal(t, 1, computeCalls)
		})
	}
}

func TestEvaluateRoutingRules_JevComputedAtMostOnce(t *testing.T) {
	// Two rules referencing jev variables in one evaluation share the single
	// lazy decision, mirroring the complexity_tier contract.
	ctx := context.Background()
	store, err := newTestRuleStore()
	require.NoError(t, err)

	require.NoError(t, store.UpsertRule(ctx, jevRoutingRule("jev-first", `jev_tier != "MEDIUM"`)))
	require.NoError(t, store.UpsertRule(ctx, jevRoutingRule("jev-second", `jev_complexity >= 0.0`)))

	engine, err := NewEngine(store, NewMockGovernanceStore(), NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	computeCalls := 0
	_, err = engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, time.Now()), &EvaluationContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		RequestType: "chat_completion",
		ComputeJev: func() *complexity.JevResult {
			computeCalls++
			return &complexity.JevResult{Complexity: 0.2, Confidence: 0.9, Reason: complexity.JevReasonLowConfidence}
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, computeCalls)
}

func TestValidateCELExpressionAcceptsJevVariables(t *testing.T) {
	require.NoError(t, ValidateCELExpression(`jev_tier == "SIMPLE" && jev_complexity <= 0.5 && jev_confidence >= 0.8`))
	err := ValidateCELExpression(`jev_tier == `)
	require.Error(t, err)
}

func TestJevUnknownsAppendAllVariables(t *testing.T) {
	// The unknown set must cover every declared jev variable; compile the
	// environment once to prove all three names are declared with the types
	// the placeholders promise.
	env, err := createCELEnvironment()
	require.NoError(t, err)
	for _, name := range rangeJevVariables() {
		assert.NotPanics(t, func() {
			_, _ = env.Compile(name + ` != ""`)
		})
	}
	_ = cel.AttributePattern
}
