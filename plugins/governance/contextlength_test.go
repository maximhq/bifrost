package governance

import (
	"context"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCELExpressionReferencesContextLengthIdentifierOnly(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		expected   bool
	}{
		{
			name:       "direct identifier",
			expression: `context_length >= 8192`,
			expected:   true,
		},
		{
			name:       "identifier in compound expression",
			expression: `context_length > 32000 && team_name == "research"`,
			expected:   true,
		},
		{
			name:       "string literal only",
			expression: `model == "context_length"`,
			expected:   false,
		},
		{
			name:       "unrelated identifier containing name",
			expression: `max_context_length == 128000`,
			expected:   false,
		},
		{
			name:       "map key string",
			expression: `headers["context_length"] == "large"`,
			expected:   false,
		},
		{
			name:       "field selection",
			expression: `metadata.context_length == 8192`,
			expected:   false,
		},
		{
			name:       "comprehension local shadows identifier",
			expression: `[100].exists(context_length, context_length > 10)`,
			expected:   false,
		},
		{
			name:       "comprehension references outer identifier",
			expression: `[100].exists(limit, context_length > limit)`,
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, celExpressionReferencesIdentifier(tt.expression, "context_length"))
		})
	}
}

func TestEvaluateCELExpression_ContextLengthUnknown(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		budgetUsed float64
		expected   bool
	}{
		{
			name:       "less than depends on unavailable context length",
			expression: `context_length < 2048`,
			expected:   false,
		},
		{
			name:       "not equals depends on unavailable context length",
			expression: `context_length != 2048`,
			expected:   false,
		},
		{
			name:       "or short-circuits when non-context side is true",
			expression: `budget_used > 90.0 || context_length < 2048`,
			budgetUsed: 95.0,
			expected:   true,
		},
		{
			name:       "or is no match when only unavailable context can decide",
			expression: `budget_used > 90.0 || context_length < 2048`,
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

			variables := contextLengthRoutingVariables()
			variables["budget_used"] = tt.budgetUsed
			delete(variables, "context_length")

			matched, err := evaluateCELExpression(program, variables, cel.AttributePattern("context_length"))
			require.NoError(t, err)
			assert.Equal(t, tt.expected, matched)
		})
	}
}

func TestEvaluateRoutingRules_ContextLengthMatches(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := contextLengthRoutingRule("context-length-large", `context_length >= 8192`)
	require.NoError(t, store.UpdateRoutingRuleInMemory(ctx, rule))

	engine, err := NewRoutingEngine(store, NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	computeCalls := 0
	decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, schemas.NoDeadline), &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		RequestType: "chat_completion",
		computeContextLength: func() (int64, bool) {
			computeCalls++
			return 9000, true
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)

	assert.Equal(t, 1, computeCalls)
	assert.Equal(t, "anthropic", decision.Provider)
	assert.Equal(t, "claude-3-5-sonnet", decision.Model)
}

func TestEvaluateRoutingRules_ContextLengthUnavailableDoesNotMatch(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := contextLengthRoutingRule("context-length-unavailable", `context_length < 2048`)
	require.NoError(t, store.UpdateRoutingRuleInMemory(ctx, rule))

	engine, err := NewRoutingEngine(store, NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	computeCalls := 0
	decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, schemas.NoDeadline), &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		RequestType: "chat_completion",
		computeContextLength: func() (int64, bool) {
			computeCalls++
			return 0, false
		},
	})
	require.NoError(t, err)

	assert.Nil(t, decision)
	assert.Equal(t, 1, computeCalls)
}

func TestEvaluateRoutingRules_ContextLengthLiteralDoesNotCompute(t *testing.T) {
	ctx := context.Background()
	store, err := NewLocalGovernanceStore(ctx, NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := contextLengthRoutingRule("context-length-literal", `model == "context_length"`)
	require.NoError(t, store.UpdateRoutingRuleInMemory(ctx, rule))

	engine, err := NewRoutingEngine(store, NewMockLogger(), schemas.Ptr(10))
	require.NoError(t, err)

	computeCalls := 0
	decision, err := engine.EvaluateRoutingRules(schemas.NewBifrostContext(ctx, schemas.NoDeadline), &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "context_length",
		RequestType: "chat_completion",
		computeContextLength: func() (int64, bool) {
			computeCalls++
			return 9000, true
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)

	assert.Equal(t, 0, computeCalls)
	assert.Equal(t, "anthropic", decision.Provider)
	assert.Equal(t, "claude-3-5-sonnet", decision.Model)
}

func TestComputeContextLengthUsesExecutorWithChildContext(t *testing.T) {
	parentCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	prompt := "Explain vector clocks in one paragraph."
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &prompt},
				},
			},
			Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-3-5-sonnet"}},
		},
	}

	plugin := &GovernancePlugin{logger: NewMockLogger()}
	plugin.SetCountTokensRequestExecutor(func(ctx *schemas.BifrostContext, countReq *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
		skipPipeline, _ := ctx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool)
		assert.True(t, skipPipeline)
		assert.Equal(t, schemas.OpenAI, countReq.Provider)
		assert.Equal(t, "gpt-4o", countReq.Model)
		assert.Len(t, countReq.Input, 1)
		assert.Empty(t, countReq.Fallbacks)
		return &schemas.BifrostCountTokensResponse{InputTokens: 123}, nil
	})

	contextLength, ok := plugin.computeContextLength(parentCtx, req)
	require.True(t, ok)
	assert.Equal(t, int64(123), contextLength)

	parentSkipPipeline, _ := parentCtx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool)
	assert.False(t, parentSkipPipeline)
}

func TestComputeContextLengthFallsBackToByteEstimate(t *testing.T) {
	parentCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	prompt := "Summarize the current routing plan."
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &prompt},
				},
			},
		},
	}

	_, estimateBytes, ok := buildContextLengthCountRequest(req)
	require.True(t, ok)

	plugin := &GovernancePlugin{logger: NewMockLogger()}
	contextLength, ok := plugin.computeContextLength(parentCtx, req)
	require.True(t, ok)
	assert.Equal(t, estimateContextTokens(estimateBytes), contextLength)
	assert.Greater(t, contextLength, int64(0))
}

func contextLengthRoutingVariables() map[string]interface{} {
	variables := complexityRoutingVariables()
	variables["context_length"] = int64(0)
	return variables
}

func contextLengthRoutingRule(id string, expression string) *configstoreTables.TableRoutingRule {
	provider := "anthropic"
	model := "claude-3-5-sonnet"
	return &configstoreTables.TableRoutingRule{
		ID:            id,
		Name:          id,
		Enabled:       boolPtr(true),
		CelExpression: expression,
		Scope:         "global",
		Priority:      1,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &model, Weight: 1.0},
		},
	}
}
