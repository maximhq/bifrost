package governance

import (
	"context"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	routingutil "github.com/maximhq/bifrost/framework/routing"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCELExpressionReferencesComplexityTierIdentifierOnly(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		expected   bool
	}{
		{
			name:       "direct identifier",
			expression: `complexity_tier == "SIMPLE"`,
			expected:   true,
		},
		{
			name:       "identifier in in-list",
			expression: `complexity_tier in ["COMPLEX", "REASONING"]`,
			expected:   true,
		},
		{
			name:       "string literal only",
			expression: `model == "complexity_tier"`,
			expected:   false,
		},
		{
			name:       "unrelated identifier containing name",
			expression: `my_complexity_tier == true`,
			expected:   false,
		},
		{
			name:       "map key string",
			expression: `headers["complexity_tier"] == "SIMPLE"`,
			expected:   false,
		},
		{
			name:       "field selection",
			expression: `metadata.complexity_tier == "SIMPLE"`,
			expected:   false,
		},
		{
			name:       "comprehension local shadows identifier",
			expression: `["SIMPLE"].exists(complexity_tier, complexity_tier == "SIMPLE")`,
			expected:   false,
		},
		{
			name:       "comprehension references outer identifier",
			expression: `["SIMPLE"].exists(tier, complexity_tier == tier)`,
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, routingutil.CELExpressionReferencesIdentifier(tt.expression, "complexity_tier"))
		})
	}
}

// TestCELComplexityTierVariable proves that CEL supports the flat complexity_tier string variable.
// This is the foundation for expressions like complexity_tier == "COMPLEX".
func TestCELComplexityTierVariable(t *testing.T) {
	env, err := cel.NewEnv(
		cel.Variable("complexity_tier", cel.StringType),
	)
	require.NoError(t, err, "failed to create CEL environment")

	tests := []struct {
		name       string
		expression string
		variables  map[string]interface{}
		expected   bool
	}{
		{
			name:       "tier equals COMPLEX",
			expression: `complexity_tier == "COMPLEX"`,
			variables: map[string]interface{}{
				"complexity_tier": "COMPLEX",
			},
			expected: true,
		},
		{
			name:       "tier equals SIMPLE",
			expression: `complexity_tier == "SIMPLE"`,
			variables: map[string]interface{}{
				"complexity_tier": "SIMPLE",
			},
			expected: true,
		},
		{
			name:       "tier equals REASONING",
			expression: `complexity_tier == "REASONING"`,
			variables: map[string]interface{}{
				"complexity_tier": "REASONING",
			},
			expected: true,
		},
		{
			name:       "tier mismatch",
			expression: `complexity_tier == "COMPLEX"`,
			variables: map[string]interface{}{
				"complexity_tier": "MEDIUM",
			},
			expected: false,
		},
		{
			name:       "tier not equals",
			expression: `complexity_tier != "SIMPLE"`,
			variables: map[string]interface{}{
				"complexity_tier": "COMPLEX",
			},
			expected: true,
		},
		{
			name:       "tier in list",
			expression: `complexity_tier in ["COMPLEX", "REASONING"]`,
			variables: map[string]interface{}{
				"complexity_tier": "COMPLEX",
			},
			expected: true,
		},
		{
			name:       "tier not in list",
			expression: `!(complexity_tier in ["SIMPLE", "MEDIUM"])`,
			variables: map[string]interface{}{
				"complexity_tier": "COMPLEX",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast, issues := env.Compile(tt.expression)
			require.NoError(t, issues.Err(), "compilation failed for: %s", tt.expression)

			program, err := env.Program(ast)
			require.NoError(t, err, "program creation failed for: %s", tt.expression)

			out, _, err := program.Eval(tt.variables)
			require.NoError(t, err, "evaluation failed for: %s", tt.expression)

			result, ok := out.Value().(bool)
			assert.True(t, ok, "expected boolean result")
			assert.Equal(t, tt.expected, result, "unexpected result for: %s", tt.expression)
		})
	}
}

// TestCELComplexityWithFullEnvironment tests complexity_tier alongside all existing CEL variables.
func TestCELComplexityWithFullEnvironment(t *testing.T) {
	env, err := createCELEnvironment()
	require.NoError(t, err, "failed to create full CEL environment")

	expression := `complexity_tier == "SIMPLE" && budget_used > 60.0`
	ast, issues := env.Compile(expression)
	require.NoError(t, issues.Err(), "compilation failed")

	program, err := env.Program(ast)
	require.NoError(t, err, "program creation failed")

	variables := map[string]interface{}{
		"model":            "gpt-4o",
		"provider":         "openai",
		"request_type":     "chat_completion",
		"headers":          map[string]string{},
		"params":           map[string]string{},
		"virtual_key_id":   "",
		"virtual_key_name": "",
		"team_id":          "",
		"team_name":        "",
		"customer_id":      "",
		"customer_name":    "",
		"tokens_used":      0.0,
		"request":          0.0,
		"budget_used":      75.0,
		"complexity_tier":  "SIMPLE",
	}

	out, _, err := program.Eval(variables)
	require.NoError(t, err, "evaluation failed")

	result, ok := out.Value().(bool)
	assert.True(t, ok, "expected boolean result")
	assert.True(t, result, "expected complexity_tier == SIMPLE && budget_used > 60 to match")
}

func TestEvaluateCELExpression_ComplexityTierUnknown(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		budgetUsed float64
		expected   bool
	}{
		{
			name:       "not equals depends on unavailable complexity",
			expression: `complexity_tier != "SIMPLE"`,
			expected:   false,
		},
		{
			name:       "not in depends on unavailable complexity",
			expression: `!(complexity_tier in ["SIMPLE"])`,
			expected:   false,
		},
		{
			name:       "or short-circuits when non-complexity side is true",
			expression: `budget_used > 90.0 || complexity_tier != "SIMPLE"`,
			budgetUsed: 95.0,
			expected:   true,
		},
		{
			name:       "or is no match when only unavailable complexity can decide",
			expression: `budget_used > 90.0 || complexity_tier != "SIMPLE"`,
			budgetUsed: 40.0,
			expected:   false,
		},
	}

	env, err := createCELEnvironment()
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast, issues := env.Compile(tt.expression)
			require.NoError(t, issues.Err())

			program, err := env.Program(ast, cel.EvalOptions(cel.OptPartialEval))
			require.NoError(t, err)

			variables := complexityRoutingVariables()
			variables["budget_used"] = tt.budgetUsed

			matched, err := evaluateCELExpression(program, variables, cel.AttributePattern("complexity_tier"))
			require.NoError(t, err)
			assert.Equal(t, tt.expected, matched)
		})
	}
}

func TestEvaluateRoutingRules_ComplexityUnavailableNegativePredicatesDoNotMatch(t *testing.T) {
	tests := []struct {
		name       string
		expression string
	}{
		{
			name:       "not equals",
			expression: `complexity_tier != "SIMPLE"`,
		},
		{
			name:       "not in",
			expression: `!(complexity_tier in ["SIMPLE"])`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
			require.NoError(t, err)

			rule := complexityRoutingRule("complexity-unavailable-"+tt.name, tt.expression)
			require.NoError(t, store.UpdateRoutingRuleInMemory(ctx, rule))

			engine, err := NewRoutingEngine(store, NewMockLogger(), schemas.Ptr(10))
			require.NoError(t, err)

			computeCalls := 0
			decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, time.Now()), &RoutingContext{
				Provider:    schemas.OpenAI,
				Model:       "gpt-4o",
				RequestType: "chat_completion",
				computeComplexity: func() *complexity.ComplexityResult {
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

func TestEvaluateRoutingRules_ComplexityTierLiteralDoesNotComputeComplexity(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := complexityRoutingRule("complexity-tier-literal", `model == "complexity_tier"`)
	require.NoError(t, store.UpdateRoutingRuleInMemory(ctx, rule))

	engine, err := NewRoutingEngine(store, NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	computeCalls := 0
	decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, time.Now()), &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "complexity_tier",
		RequestType: "chat_completion",
		computeComplexity: func() *complexity.ComplexityResult {
			computeCalls++
			return &complexity.ComplexityResult{Tier: "SIMPLE"}
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)

	assert.Equal(t, 0, computeCalls)
	assert.Equal(t, "anthropic", decision.Provider)
	assert.Equal(t, "claude-3-5-sonnet", decision.Model)
}

func TestEvaluateRoutingRules_ComplexityNegativePredicateMatchesAvailableTier(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := complexityRoutingRule("complexity-available-not-simple", `complexity_tier != "SIMPLE"`)
	require.NoError(t, store.UpdateRoutingRuleInMemory(ctx, rule))

	engine, err := NewRoutingEngine(store, NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, time.Now()), &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		RequestType: "chat_completion",
		Complexity:  &complexity.ComplexityResult{Tier: "COMPLEX"},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "anthropic", decision.Provider)
	assert.Equal(t, "claude-3-5-sonnet", decision.Model)
}

func complexityRoutingVariables() map[string]interface{} {
	return map[string]interface{}{
		"model":            "gpt-4o",
		"provider":         "openai",
		"request_type":     "chat_completion",
		"headers":          map[string]string{},
		"params":           map[string]string{},
		"virtual_key_id":   "",
		"virtual_key_name": "",
		"team_id":          "",
		"team_name":        "",
		"customer_id":      "",
		"customer_name":    "",
		"tokens_used":      0.0,
		"request":          0.0,
		"budget_used":      0.0,
		"complexity_tier":  "",
	}
}

func complexityRoutingRule(id string, expression string) *configstoreTables.TableRoutingRule {
	provider := "anthropic"
	model := "claude-3-5-sonnet"
	return &configstoreTables.TableRoutingRule{
		ID:            id,
		Name:          id,
		Enabled:       true,
		CelExpression: expression,
		Scope:         "global",
		Priority:      1,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &model, Weight: 1.0},
		},
	}
}
