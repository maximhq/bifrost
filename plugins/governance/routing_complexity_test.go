package governance

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCELMapDotAccess proves that CEL supports dot-access syntax on MapType(StringType, AnyType).
// This is the foundation for expressions like complexity.score > 0.4 && complexity.tier == "COMPLEX".
func TestCELMapDotAccess(t *testing.T) {
	env, err := cel.NewEnv(
		cel.Variable("complexity", cel.MapType(cel.StringType, cel.AnyType)),
	)
	require.NoError(t, err, "failed to create CEL environment")

	tests := []struct {
		name       string
		expression string
		variables  map[string]interface{}
		expected   bool
	}{
		{
			name:       "dot access on score",
			expression: `complexity.score > 0.4`,
			variables: map[string]interface{}{
				"complexity": map[string]interface{}{"score": 0.5, "tier": "COMPLEX"},
			},
			expected: true,
		},
		{
			name:       "dot access on tier string",
			expression: `complexity.tier == "COMPLEX"`,
			variables: map[string]interface{}{
				"complexity": map[string]interface{}{"score": 0.5, "tier": "COMPLEX"},
			},
			expected: true,
		},
		{
			name:       "combined dot access",
			expression: `complexity.score > 0.4 && complexity.tier == "COMPLEX"`,
			variables: map[string]interface{}{
				"complexity": map[string]interface{}{"score": 0.5, "tier": "COMPLEX"},
			},
			expected: true,
		},
		{
			name:       "score below threshold",
			expression: `complexity.score > 0.7`,
			variables: map[string]interface{}{
				"complexity": map[string]interface{}{"score": 0.3, "tier": "MEDIUM"},
			},
			expected: false,
		},
		{
			name:       "code_presence dimension access",
			expression: `complexity.code_presence > 0.5`,
			variables: map[string]interface{}{
				"complexity": map[string]interface{}{
					"score": 0.6, "tier": "COMPLEX", "code_presence": 0.8,
					"reasoning_markers": 0.0, "technical_terms": 0.0,
					"simple_indicators": 0.0, "token_count": 0.0,
					"conversation_ctx": 0.0, "system_prompt": 0.0,
					"output_complexity": 0.0,
				},
			},
			expected: true,
		},
		{
			name:       "combined with existing variable types",
			expression: `complexity.tier == "SIMPLE" && budget_used > 60`,
			variables: map[string]interface{}{
				"complexity": map[string]interface{}{"score": 0.1, "tier": "SIMPLE"},
			},
			expected: false, // budget_used not declared, but we test expression compilation below
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip the combined test that needs budget_used — test it separately
			if tt.name == "combined with existing variable types" {
				t.Skip("tested separately with full environment")
				return
			}

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

// TestCELComplexityWithFullEnvironment tests complexity variables alongside all existing CEL variables.
func TestCELComplexityWithFullEnvironment(t *testing.T) {
	env, err := createCELEnvironment()
	require.NoError(t, err, "failed to create full CEL environment")

	expression := `complexity.tier == "SIMPLE" && budget_used > 60.0`
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
		"complexity": map[string]interface{}{
			"score": 0.1, "tier": "SIMPLE",
			"code_presence": 0.0, "reasoning_markers": 0.0,
			"technical_terms": 0.0, "simple_indicators": 0.0,
			"token_count": 0.0, "conversation_ctx": 0.0,
			"system_prompt": 0.0, "output_complexity": 0.0,
		},
	}

	out, _, err := program.Eval(variables)
	require.NoError(t, err, "evaluation failed")

	result, ok := out.Value().(bool)
	assert.True(t, ok, "expected boolean result")
	assert.True(t, result, "expected complexity.tier == SIMPLE && budget_used > 60 to match")
}
