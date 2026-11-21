package evals

import (
	"context"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const builtinAIConfig = `{"instructions":"built-in","grading_style":"number","provider":"openai","model":"gpt-4o-mini"}`

func newBuiltinEngine(judge *scriptedJudge) *Engine {
	engine := NewEngine(EngineOptions{})
	RegisterBuiltinAIEvaluators(engine, judge.call)
	return engine
}

func builtinDefinition(key string) Definition {
	return Definition{ID: "builtin-" + key, Type: EvaluatorTypeAI, BuiltinKey: key, Version: 1, Config: []byte(builtinAIConfig)}
}

// runBuiltin runs a built-in against scripted judge replies and returns the result
// with the requests the judge received.
func runBuiltin(t *testing.T, key string, variables map[string]string, replies ...string) (Result, []JudgeRequest) {
	t.Helper()
	judge := &scriptedJudge{}
	for _, reply := range replies {
		judge.replies = append(judge.replies, JudgeResponse{Text: reply, Usage: &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}})
	}
	result := newBuiltinEngine(judge).Run(context.Background(), builtinDefinition(key), Input{Variables: variables})
	return result, judge.requests
}

// Every registered built-in must have its prompts on disk and compile them.
func TestBuiltinPromptsAreEmbedded(t *testing.T) {
	entries, err := promptFiles.ReadDir("prompts")
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".txt")
		compiled := prompt(name)
		assert.NotEmpty(t, compiled.names, "prompt %s has no placeholders, so it would ignore its input", name)
	}
}

func TestBuiltin_RubricOnOutput(t *testing.T) {
	for _, key := range []string{BuiltinClarity, BuiltinConciseness} {
		t.Run(key, func(t *testing.T) {
			result, requests := runBuiltin(t, key, map[string]string{"output": "The refund window is 30 days."},
				`{"score": 0.75, "reasoning": "Mostly clear."}`)

			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, NumberScore(0.75), result.Score)
			assert.Equal(t, "Mostly clear.", result.Reasoning)
			require.NotNil(t, result.Pass)
			assert.False(t, *result.Pass, "0.75 is below the default 0.8 threshold")
			assert.Equal(t, &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, result.Usage)

			require.Len(t, requests, 1)
			assert.Empty(t, requests[0].System, "Maxim sends the whole prompt as one user message")
			assert.Contains(t, requests[0].User, "The refund window is 30 days.")
			assert.Contains(t, requests[0].User, "SCORING RUBRIC")
			assert.NotContains(t, requests[0].User, "{{", "every placeholder is filled")
			assert.NotNil(t, requests[0].Schema)
			assert.Equal(t, "openai", requests[0].Provider)
			assert.Equal(t, "gpt-4o-mini", requests[0].Model)
		})
	}
}

func TestBuiltin_EmptyOutputScoresZeroWithoutACall(t *testing.T) {
	for _, key := range []string{BuiltinClarity, BuiltinConciseness} {
		result, requests := runBuiltin(t, key, map[string]string{"output": ""})
		require.Equal(t, ScoreStatusOK, result.Score.Status)
		assert.Equal(t, NumberScore(0), result.Score)
		assert.Equal(t, emptyOutputReason, result.Reasoning)
		assert.Empty(t, requests)
	}
}

func TestBuiltin_MissingVariableIsNA(t *testing.T) {
	result, requests := runBuiltin(t, BuiltinSQLCorrectness, map[string]string{"input": "q", "output": "SELECT 1"})
	assert.Equal(t, ScoreStatusNA, result.Score.Status)
	assert.Equal(t, "Missing variables: db_schema", result.Reasoning)
	assert.Empty(t, requests)
}

func TestBuiltin_SQLCorrectness(t *testing.T) {
	result, requests := runBuiltin(t, BuiltinSQLCorrectness,
		map[string]string{"input": "Total per customer", "output": "SELECT customer_id, SUM(total) FROM orders GROUP BY 1", "db_schema": "CREATE TABLE orders (customer_id INT, total REAL)"},
		`{"score": 1, "reasoning": "Correct aggregation."}`)
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, NumberScore(1), result.Score)
	assert.Contains(t, requests[0].User, "Total per customer")
	assert.Contains(t, requests[0].User, "GROUP BY 1")
	assert.Contains(t, requests[0].User, "CREATE TABLE orders")
}

func TestBuiltin_SessionJudges(t *testing.T) {
	session := `[{"turn":1,"request":"Book a flight","response":"Booked AA100"}]`
	cases := []struct {
		key   string
		reply string
		want  float64
	}{
		{BuiltinAgentTrajectory, `{"thought":"ok","score":1,"reasoning":"On track."}`, 1},
		{BuiltinTaskSuccess, `{"thought":"ok","score":0,"reasoning":"Not done."}`, 0},
		{BuiltinStepUtility, `{"score":[1,0,1,1],"reasoning":"One wasted step."}`, 0.75},
		{BuiltinToolSelection, `{"thought":"ok","score":[1,1,0],"reasoning":"One wrong tool."}`, 2.0 / 3.0},
		{BuiltinStepUtility, `{"score":[],"reasoning":"No steps."}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			result, requests := runBuiltin(t, tc.key, map[string]string{"session": session}, tc.reply)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.InDelta(t, tc.want, *result.Score.Number, 1e-12)
			assert.Contains(t, requests[0].User, "Booked AA100")
			assert.NotContains(t, requests[0].User, "{{")
		})
	}

	t.Run("a per-step judge that returns one number is rejected", func(t *testing.T) {
		result, _ := runBuiltin(t, BuiltinStepUtility, map[string]string{"session": session}, `{"score":1,"reasoning":"x"}`)
		assert.Equal(t, ScoreStatusError, result.Score.Status)
	})
}

func TestBuiltin_StepCompletion(t *testing.T) {
	for _, key := range []string{BuiltinStepCompletionStrictMatch, BuiltinStepCompletionUnorderedMatch} {
		t.Run(key, func(t *testing.T) {
			result, requests := runBuiltin(t, key,
				map[string]string{"session": `[{"turn":1,"request":"deploy","response":"deployed"}]`, "expected_steps": `["Install dependencies","Deploy"]`},
				`{"score": 0, "reasoning": "Dependencies were never installed."}`)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, NumberScore(0), result.Score)
			assert.Equal(t, "Dependencies were never installed.", result.Reasoning)
			assert.Contains(t, requests[0].User, "Install dependencies")
			assert.Contains(t, requests[0].User, `"response":"deployed"`)
		})
	}
}

func TestBuiltin_CodeCompletion(t *testing.T) {
	variables := map[string]string{"input": "Write a CLI", "output": "import sys\nprint(sys.argv)"}
	cases := []struct {
		name       string
		reply      string
		wantScore  float64
		wantReason string
	}{
		{"complete", `{"completeness_score":5,"missing_elements":[],"assumptions_made":[],"reasoning":"Runs as is."}`, 1, "Runs as is."},
		{"partial with gaps", `{"completeness_score":3,"missing_elements":["requirements.txt","tests"],"assumptions_made":["Python 3.11"],"reasoning":"Mostly there."}`,
			0.5, "Mostly there.\n\nMissing: requirements.txt; tests\nAssumptions: Python 3.11"},
		{"score above the scale is clamped", `{"completeness_score":9,"missing_elements":[],"assumptions_made":[],"reasoning":"x"}`, 1, "x"},
		{"score below the scale is clamped", `{"completeness_score":0,"missing_elements":[],"assumptions_made":[],"reasoning":"x"}`, 0, "x"},
		{"missing score counts as the lowest", `{"missing_elements":[],"assumptions_made":[],"reasoning":"x"}`, 0, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, requests := runBuiltin(t, BuiltinCodeCompletion, variables, tc.reply)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.InDelta(t, tc.wantScore, *result.Score.Number, 1e-12)
			assert.Equal(t, tc.wantReason, result.Reasoning)
			assert.Contains(t, requests[0].User, "Write a CLI")
			assert.Contains(t, requests[0].User, "print(sys.argv)")
		})
	}
}

func TestBuiltin_CodeLanguageSelection(t *testing.T) {
	variables := map[string]string{"input": "Write it in Go", "output": "def main(): pass"}
	result, _ := runBuiltin(t, BuiltinCodeLanguageSelection, variables,
		`{"correct_languages":"no","expected_languages":["Go"],"actual_languages":["Python"],"reasoning":"Wrong language."}`)
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, NumberScore(0), result.Score)
	assert.Equal(t, "Wrong language.\n\nExpected languages: Go\n\nActual languages: Python", result.Reasoning)

	result, _ = runBuiltin(t, BuiltinCodeLanguageSelection, variables,
		`{"correct_languages":" YES ","expected_languages":[],"actual_languages":[],"reasoning":"Right."}`)
	assert.Equal(t, NumberScore(1), result.Score)
	assert.Equal(t, "Right.", result.Reasoning)
}

func TestBuiltin_FollowupCheck(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantScore  float64
		wantReason string
	}{
		{"both valid", `{"tool_output_valid":"yes","followup_date_valid":"yes","tool_output_issues":[],"followup_date_issues":[],"medical_appropriateness":"appropriate","reasoning":"Matches."}`,
			1, "Matches.\n\nMedical Appropriateness: appropriate"},
		{"partial tool output and bad date", `{"tool_output_valid":"partial","followup_date_valid":"no","tool_output_issues":["wrong clinic"],"followup_date_issues":["in the past"],"medical_appropriateness":"not_applicable","reasoning":"Off."}`,
			0.25, "Off.\n\nTool Output Issues: wrong clinic\n\nFollowup Date Issues: in the past"},
		{"date not applicable leaves the tool score", `{"tool_output_valid":"partial","followup_date_valid":"not_applicable","tool_output_issues":[],"followup_date_issues":[],"medical_appropriateness":"not_applicable","reasoning":"No date needed."}`,
			0.5, "No date needed."},
		{"missing verdicts count as no", `{"reasoning":"Unclear."}`, 0, "Unclear."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, requests := runBuiltin(t, BuiltinFollowupCheck,
				map[string]string{"transcript": "Patient agreed to return in two weeks.", "scheduled_date": "2026-11-23", "current_date": "2026-11-09"}, tc.reply)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.InDelta(t, tc.wantScore, *result.Score.Number, 1e-12)
			assert.Equal(t, tc.wantReason, result.Reasoning)
			assert.Contains(t, requests[0].User, "return in two weeks")
			assert.Contains(t, requests[0].User, "2026-11-23")
			assert.Contains(t, requests[0].User, "**CURRENT DATE**: 2026-11-09")
		})
	}

	t.Run("no current date is stated in the prompt", func(t *testing.T) {
		_, requests := runBuiltin(t, BuiltinFollowupCheck, map[string]string{"transcript": "x"}, `{"reasoning":"x"}`)
		assert.Contains(t, requests[0].User, "**CURRENT DATE**: "+followupNoCurrentDate)
	})
}

func TestBuiltin_JudgeFailures(t *testing.T) {
	t.Run("reply is not JSON", func(t *testing.T) {
		result, _ := runBuiltin(t, BuiltinClarity, map[string]string{"output": "x"}, "I think it is clear.")
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "invalid JSON")
		require.NotNil(t, result.Usage, "the call was billed even though the reply was unusable")
	})

	t.Run("reply has no score", func(t *testing.T) {
		result, _ := runBuiltin(t, BuiltinClarity, map[string]string{"output": "x"}, `{"reasoning":"clear"}`)
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "score")
	})

	t.Run("judge call fails", func(t *testing.T) {
		judge := &scriptedJudge{errs: []error{errors.New("429 rate limited")}}
		result := newBuiltinEngine(judge).Run(context.Background(), builtinDefinition(BuiltinClarity), Input{Variables: map[string]string{"output": "x"}})
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "429")
	})

	t.Run("structured output rejected, then plain text works", func(t *testing.T) {
		judge := &scriptedJudge{
			errs:    []error{errors.New("response_format json_schema is not supported")},
			replies: []JudgeResponse{{}, {Text: `{"score":1,"reasoning":"ok"}`}},
		}
		result := newBuiltinEngine(judge).Run(context.Background(), builtinDefinition(BuiltinClarity), Input{Variables: map[string]string{"output": "x"}})
		require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
		require.Len(t, judge.requests, 2)
		assert.Nil(t, judge.requests[1].Schema)
	})
}

// When a provider rejects structured output, the judge is asked again with the prompt
// alone. The prompt then has to say everything the schema said, and an example it
// shows has to be JSON a judge can copy.
func TestBuiltinPromptsStandWithoutTheSchema(t *testing.T) {
	t.Run("the statements example is valid JSON", func(t *testing.T) {
		text := renderPrompt("summarization__generate_statements", map[string]string{"text": "x"})
		_, rest, found := strings.Cut(text, "Example JSON:")
		require.True(t, found)
		example, _, found := strings.Cut(rest, "===== END OF EXAMPLE")
		require.True(t, found)
		assert.True(t, gjson.Valid(strings.TrimSpace(example)), "the example a judge copies must parse: %s", example)
	})
	t.Run("language selection is graded yes or no", func(t *testing.T) {
		text := renderPrompt("code_language_selection__evaluate_code_language_selection", map[string]string{})
		assert.NotContains(t, text, `"partial"`, "the schema and the score know only yes and no")
	})
	t.Run("the follow-up prompt lists every key and its values", func(t *testing.T) {
		text := renderPrompt("followup_check__evaluate_followup_check", map[string]string{})
		for _, key := range []string{"tool_output_valid", "followup_date_valid", "tool_output_issues", "followup_date_issues", "medical_appropriateness", "reasoning"} {
			assert.Contains(t, text, `"`+key+`"`)
		}
		assert.Contains(t, text, `"yes", "no" or "partial"`)
		assert.Contains(t, text, `"yes", "no" or "not_applicable"`)
	})
}

// The reason prompt ends with one "Reason:" line for the judge to continue. The
// questions the summary missed come before it, as a block of their own.
func TestSummarizationReasonPrompt(t *testing.T) {
	standalone := func(text string) int {
		count := 0
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) == "Reason:" {
				count++
			}
		}
		return count
	}
	plain := summarizationReasonPrompt(nil, nil, nil, 0.5)
	assert.Equal(t, 1, standalone(plain))
	assert.True(t, strings.HasSuffix(plain, "Reason:"))
	assert.NotContains(t, plain, "Questions the original text can answer")

	withMissed := summarizationReasonPrompt([]string{"c"}, []string{"r"}, []string{"Who founded it?", "When?"}, 0.5)
	assert.Equal(t, 1, standalone(withMissed))
	assert.True(t, strings.HasSuffix(withMissed, "\n\nReason:"), "the list is separated from the marker")
	assert.Less(t, strings.Index(withMissed, "Who founded it?"), strings.LastIndex(withMissed, "Reason:"))
}
