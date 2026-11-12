package evals

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_Valid(t *testing.T) {
	cases := []struct {
		name string
		typ  EvaluatorType
		raw  string
	}{
		{
			name: "ai yes_no",
			typ:  EvaluatorTypeAI,
			raw:  `{"instructions":"Does {{output}} answer {{input}}?","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini","pass_criteria":{"entry_level":{"name":"score","operator":"=","value":"Yes"},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`,
		},
		{
			name: "ai scale with explicit range",
			typ:  EvaluatorTypeAI,
			raw:  `{"instructions":"Rate {{output}}","grading_style":"scale","scale":{"min":1,"max":10},"provider":"anthropic","model":"claude-sonnet-5-5","pass_criteria":{"entry_level":{"name":"score","operator":">=","value":7},"run_level":{"name":"mean_score","operator":">=","value":7}}}`,
		},
		{
			name: "ai multiselect",
			typ:  EvaluatorTypeAI,
			raw:  `{"instructions":"Classify {{output}}","grading_style":"multiselect","provider":"openai","model":"gpt-4o-mini","pass_criteria":{"entry_level":{"name":"score","operator":"is one of","value":["good","great"],"options":["bad","good","great"]},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`,
		},
		{
			name: "programmatic",
			typ:  EvaluatorTypeProgrammatic,
			raw:  `{"code":"def validate(output):\n    return len(output) > 0\n","response_type":"yes_no","pass_criteria":{"entry_level":{"name":"score","operator":"=","value":"Yes"},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`,
		},
		{
			name: "api",
			typ:  EvaluatorTypeAPI,
			raw:  `{"method":"POST","url":"https://scorer.example.com/score","headers":[{"key":"Authorization","value":"Bearer scorer-token"}],"payload":"{\"text\":\"{{output}}\"}","output_mapping":{"score":"data.score","reasoning":"data.why"},"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":0.8},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`,
		},
		{
			name: "statistical with reversed score and normalizer",
			typ:  EvaluatorTypeStatistical,
			raw:  `{"reversed":true,"score_normalizer":{"action":"divide","value":100},"pass_criteria":{"entry_level":{"name":"score","operator":"<=","value":0.2},"run_level":{"name":"median_score","operator":"<=","value":0.2}}}`,
		},
		{
			name: "human scale",
			typ:  EvaluatorTypeHuman,
			raw:  `{"instructions":"Rate helpfulness","evaluation_type":"scale","scale":{"min":1,"max":5},"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":4},"run_level":{"name":"minimum_score","operator":">=","value":3}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig(tc.typ, []byte(tc.raw))
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, tc.typ, cfg.Type())
		})
	}
}

func TestParseConfig_Invalid(t *testing.T) {
	const criteria = `"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":0.8},"run_level":{"name":"queries_passed","operator":">=","value":80}}`
	cases := []struct {
		name    string
		typ     EvaluatorType
		raw     string
		wantErr string
	}{
		{"unknown type", EvaluatorType("vibes"), `{}`, "unknown evaluator type"},
		{"malformed json", EvaluatorTypeAI, `{"instructions":`, "invalid evaluator config"},
		{"ai without instructions", EvaluatorTypeAI, `{"grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}`, "instructions"},
		{"ai without model", EvaluatorTypeAI, `{"instructions":"x","grading_style":"yes_no","provider":"openai"}`, "model"},
		{"ai without provider", EvaluatorTypeAI, `{"instructions":"x","grading_style":"yes_no","model":"gpt-4o-mini"}`, "provider"},
		{"ai unknown grading style", EvaluatorTypeAI, `{"instructions":"x","grading_style":"stars","provider":"openai","model":"gpt-4o-mini"}`, "grading_style"},
		{"ai scale min above max", EvaluatorTypeAI, `{"instructions":"x","grading_style":"scale","scale":{"min":5,"max":1},"provider":"openai","model":"gpt-4o-mini"}`, "scale"},
		{"ai multiselect without options", EvaluatorTypeAI, `{"instructions":"x","grading_style":"multiselect","provider":"openai","model":"gpt-4o-mini","pass_criteria":{"entry_level":{"name":"score","operator":"is one of","value":["good"]},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`, "options"},
		{"programmatic without code", EvaluatorTypeProgrammatic, `{"response_type":"yes_no"}`, "code"},
		{"programmatic unknown response type", EvaluatorTypeProgrammatic, `{"code":"def validate(output):\n    return True\n","response_type":"maybe"}`, "response_type"},
		{"api without url", EvaluatorTypeAPI, `{"method":"POST","output_mapping":{"score":"score"}}`, "url"},
		{"api non-http url", EvaluatorTypeAPI, `{"method":"POST","url":"file:///etc/passwd","output_mapping":{"score":"score"}}`, "url"},
		{"api unknown method", EvaluatorTypeAPI, `{"method":"TRACE","url":"https://x.example.com","output_mapping":{"score":"score"}}`, "method"},
		{"api without score mapping", EvaluatorTypeAPI, `{"method":"POST","url":"https://x.example.com","output_mapping":{}}`, "output_mapping.score"},
		{"api negative concurrency", EvaluatorTypeAPI, `{"method":"POST","url":"https://x.example.com","output_mapping":{"score":"score"},"concurrency":-1}`, "concurrency"},
		{"human unknown evaluation type", EvaluatorTypeHuman, `{"evaluation_type":"thumbs"}`, "evaluation_type"},
		{"bad operator", EvaluatorTypeStatistical, `{"pass_criteria":{"entry_level":{"name":"score","operator":"~=","value":1},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`, "operator"},
		{"bad run level name", EvaluatorTypeStatistical, `{"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":1},"run_level":{"name":"mode_score","operator":">=","value":80}}}`, "run_level"},
		{"ordering operator with string value", EvaluatorTypeStatistical, `{"pass_criteria":{"entry_level":{"name":"score","operator":">","value":"high"},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`, "number"},
		{"is one of without a list", EvaluatorTypeStatistical, `{"pass_criteria":{"entry_level":{"name":"score","operator":"is one of","value":"good"},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`, "list"},
		{"normalizer unknown action", EvaluatorTypeStatistical, `{"score_normalizer":{"action":"square","value":2},` + criteria + `}`, "score_normalizer"},
		{"normalizer divide by zero", EvaluatorTypeStatistical, `{"score_normalizer":{"action":"divide","value":0},` + criteria + `}`, "score_normalizer"},
		{"normalizer mapping without entries", EvaluatorTypeStatistical, `{"score_normalizer":{"action":"mapping"},` + criteria + `}`, "score_normalizer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig(tc.typ, []byte(tc.raw))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// Maxim's defaults: an AI yes/no judge passes on "Yes", every other evaluator passes at
// 0.8, and a run passes when 80% of its entries pass.
func TestParseConfig_AppliesDefaults(t *testing.T) {
	t.Run("ai yes_no", func(t *testing.T) {
		cfg, err := ParseConfig(EvaluatorTypeAI, []byte(`{"instructions":"x","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}`))
		require.NoError(t, err)
		pc := cfg.Common().PassCriteria
		assert.Equal(t, Criterion{Name: CriterionScore, Operator: OperatorEqual, Value: "Yes"}, pc.EntryLevel)
		assert.Equal(t, Criterion{Name: RunCriterionQueriesPassed, Operator: OperatorGreaterOrEqual, Value: float64(80)}, pc.RunLevel)
	})
	t.Run("ai scale defaults to 1 through 5", func(t *testing.T) {
		cfg, err := ParseConfig(EvaluatorTypeAI, []byte(`{"instructions":"x","grading_style":"scale","provider":"openai","model":"gpt-4o-mini"}`))
		require.NoError(t, err)
		ai, ok := cfg.(*AIConfig)
		require.True(t, ok)
		require.NotNil(t, ai.Scale)
		assert.Equal(t, Scale{Min: 1, Max: 5}, *ai.Scale)
	})
	t.Run("programmatic", func(t *testing.T) {
		cfg, err := ParseConfig(EvaluatorTypeProgrammatic, []byte(`{"code":"def validate(output):\n    return 1\n","response_type":"number"}`))
		require.NoError(t, err)
		assert.Equal(t, Criterion{Name: CriterionScore, Operator: OperatorGreaterOrEqual, Value: 0.8}, cfg.Common().PassCriteria.EntryLevel)
	})
	t.Run("api defaults concurrency to 1", func(t *testing.T) {
		cfg, err := ParseConfig(EvaluatorTypeAPI, []byte(`{"method":"post","url":"https://x.example.com","output_mapping":{"score":"score"}}`))
		require.NoError(t, err)
		api, ok := cfg.(*APIConfig)
		require.True(t, ok)
		assert.Equal(t, 1, api.Concurrency)
		assert.Equal(t, "POST", api.Method, "the method is normalised to upper case")
	})
}

// A stored config must produce the same bytes every time it is marshalled, so an
// unchanged save can be detected by comparing bytes.
func TestMarshalConfig_Deterministic(t *testing.T) {
	raw := `{"model_params":{"temperature":0,"top_p":1,"max_tokens":256},"instructions":"x {{b}} {{a}}","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini","variables":{"b":"second","a":"first"}}`
	cfg, err := ParseConfig(EvaluatorTypeAI, []byte(raw))
	require.NoError(t, err)

	first, err := MarshalConfig(cfg)
	require.NoError(t, err)
	for range 20 {
		again, err := MarshalConfig(cfg)
		require.NoError(t, err)
		require.Equal(t, string(first), string(again))
	}

	reparsed, err := ParseConfig(EvaluatorTypeAI, first)
	require.NoError(t, err)
	roundTrip, err := MarshalConfig(reparsed)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(roundTrip), "parse then marshal is a fixed point")
}
