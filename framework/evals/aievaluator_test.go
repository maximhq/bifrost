package evals

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedJudge replies with the next response on each call and records the requests.
type scriptedJudge struct {
	replies  []JudgeResponse
	errs     []error
	requests []JudgeRequest
}

func (j *scriptedJudge) call(_ context.Context, req JudgeRequest) (JudgeResponse, error) {
	i := len(j.requests)
	j.requests = append(j.requests, req)
	var err error
	if i < len(j.errs) {
		err = j.errs[i]
	}
	if err != nil {
		return JudgeResponse{}, err
	}
	if i >= len(j.replies) {
		return JudgeResponse{}, errors.New("scripted judge ran out of replies")
	}
	return j.replies[i], nil
}

const aiYesNoConfig = `{"instructions":"Does {{output}} answer {{input}}? Consider {{context}} if given.","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini","optional_variables":{"context":"retrieved documents"},"model_params":{"max_completion_tokens":300}}`

func newAIEngine(judge *scriptedJudge) *Engine {
	engine := NewEngine(EngineOptions{})
	RegisterAIEvaluator(engine, judge.call)
	return engine
}

func aiDefinition(config string) Definition {
	return Definition{ID: "ai-1", Type: EvaluatorTypeAI, Version: 3, Config: []byte(config)}
}

func TestAIEvaluator_BuildsMaximPromptContract(t *testing.T) {
	judge := &scriptedJudge{replies: []JudgeResponse{{
		Text:  `{"reasoning":"It states the window.","score":true}`,
		Usage: &Usage{PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220},
		Cost:  new(0.0001),
	}}}
	engine := newAIEngine(judge)

	result := engine.Run(context.Background(), aiDefinition(aiYesNoConfig), Input{Variables: map[string]string{
		"input":  "What is the refund window?",
		"output": "30 days from delivery.",
		"unused": "never sent to the judge",
	}})

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, BoolScore(true), result.Score)
	assert.Equal(t, "It states the window.", result.Reasoning)
	require.NotNil(t, result.Pass)
	assert.True(t, *result.Pass, "a yes_no judge passes on Yes by default")
	assert.Equal(t, &Usage{PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220}, result.Usage)
	require.NotNil(t, result.Cost)
	assert.InDelta(t, 0.0001, *result.Cost, 1e-12)

	require.Len(t, judge.requests, 1)
	req := judge.requests[0]
	assert.Equal(t, "openai", req.Provider)
	assert.Equal(t, "gpt-4o-mini", req.Model)
	assert.Equal(t, map[string]any{"max_completion_tokens": float64(300)}, req.Params)

	// The instructions go out untouched: placeholders are not substituted, so the system
	// message is identical for every entry and can be prompt-cached.
	assert.True(t, strings.HasPrefix(req.System, "Does {{output}} answer {{input}}? Consider {{context}} if given."))
	assert.Contains(t, req.System, "{{output}}, {{input}}, {{context}}", "the judge is told which variables to expect")
	assert.Contains(t, req.System, "true")
	assert.Contains(t, req.System, `"score"`)
	assert.Contains(t, req.System, `"reasoning"`)

	// Values travel in the user message, one {{name}} = "value" line per variable, in the
	// order the instructions use them. A variable the evaluator never names is not sent,
	// and an optional one with no value is skipped.
	assert.Equal(t, "{{output}} = \"30 days from delivery.\"\n\n{{input}} = \"What is the refund window?\"", req.User)

	require.NotNil(t, req.Schema, "structured output is requested")
	properties, ok := req.Schema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"type": "boolean"}, properties["score"])
}

func TestAIEvaluator_RequiresVariablesNamedInInstructions(t *testing.T) {
	judge := &scriptedJudge{}
	engine := newAIEngine(judge)

	result := engine.Run(context.Background(), aiDefinition(aiYesNoConfig), Input{Variables: map[string]string{"output": "x"}})

	assert.Equal(t, ScoreStatusNA, result.Score.Status)
	assert.Equal(t, "Missing variables: input", result.Reasoning, "context is optional, input is not")
	assert.Empty(t, judge.requests, "no model call is paid for")
}

func TestAIEvaluator_GradingStyles(t *testing.T) {
	const base = `"instructions":"Grade {{output}}","provider":"openai","model":"gpt-4o-mini"`
	multiselect := `{` + base + `,"grading_style":"multiselect","pass_criteria":{"entry_level":{"name":"score","operator":"is one of","value":["good"],"options":["good","bad"]},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`
	cases := []struct {
		name       string
		config     string
		reply      string
		wantScore  Score
		wantPass   bool
		wantSchema map[string]any
		wantClause string
	}{
		{"yes_no false", `{` + base + `,"grading_style":"yes_no"}`, `{"score":false,"reasoning":"no"}`, BoolScore(false), false, map[string]any{"type": "boolean"}, "true"},
		{"yes_no as the word Yes", `{` + base + `,"grading_style":"yes_no"}`, `{"score":"Yes","reasoning":"ok"}`, BoolScore(true), true, map[string]any{"type": "boolean"}, "true"},
		{"scale", `{` + base + `,"grading_style":"scale","scale":{"min":1,"max":10},"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":7},"run_level":{"name":"mean_score","operator":">=","value":7}}}`, `{"score":8,"reasoning":"solid"}`, NumberScore(8), true, map[string]any{"type": "number", "minimum": float64(1), "maximum": float64(10)}, "from 1 to 10"},
		{"scale as numeric text", `{` + base + `,"grading_style":"scale","pass_criteria":{"entry_level":{"name":"score","operator":">=","value":4},"run_level":{"name":"mean_score","operator":">=","value":4}}}`, `{"score":"3","reasoning":"meh"}`, NumberScore(3), false, map[string]any{"type": "number", "minimum": float64(1), "maximum": float64(5)}, "from 1 to 5"},
		{"number", `{` + base + `,"grading_style":"number"}`, `{"score":0.85,"reasoning":"close"}`, NumberScore(0.85), true, map[string]any{"type": "number"}, "number"},
		{"multiselect", multiselect, `{"score":"good","reasoning":"fine"}`, StringScore("good"), true, map[string]any{"type": "string", "enum": []any{"good", "bad"}}, `"good", "bad"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			judge := &scriptedJudge{replies: []JudgeResponse{{Text: tc.reply}}}
			result := newAIEngine(judge).Run(context.Background(), aiDefinition(tc.config), Input{Variables: map[string]string{"output": "x"}})

			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, tc.wantScore, result.Score)
			require.NotNil(t, result.Pass)
			assert.Equal(t, tc.wantPass, *result.Pass)
			properties := judge.requests[0].Schema["properties"].(map[string]any)
			assert.Equal(t, tc.wantSchema, properties["score"])
			assert.Contains(t, judge.requests[0].System, tc.wantClause)
		})
	}
}

func TestAIEvaluator_ParsesJSONOutOfChattyReplies(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"fenced", "```json\n{\"score\": true, \"reasoning\": \"ok\"}\n```"},
		{"prose around it", `Sure! Here is my verdict: {"score": true, "reasoning": "ok"} Hope that helps.`},
		{"a brace inside a string", `{"reasoning": "ok", "score": true, "note": "uses } and { freely"}`},
		{"an invalid object first", `{not json} {"score": true, "reasoning": "ok"}`},
		{"escaped quotes", `{"score": true, "reasoning": "ok", "quote": "she said \"}\" loudly"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			judge := &scriptedJudge{replies: []JudgeResponse{{Text: tc.reply}}}
			result := newAIEngine(judge).Run(context.Background(), aiDefinition(aiYesNoConfig), Input{Variables: map[string]string{"input": "q", "output": "a"}})
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, BoolScore(true), result.Score)
			assert.Equal(t, "ok", result.Reasoning)
		})
	}
}

func TestAIEvaluator_NonStringReasoningIsStringified(t *testing.T) {
	judge := &scriptedJudge{replies: []JudgeResponse{{Text: `{"score":true,"reasoning":{"claim":"supported"}}`}}}
	result := newAIEngine(judge).Run(context.Background(), aiDefinition(aiYesNoConfig), Input{Variables: map[string]string{"input": "q", "output": "a"}})
	require.Equal(t, ScoreStatusOK, result.Score.Status)
	assert.JSONEq(t, `{"claim":"supported"}`, result.Reasoning)
}

func TestAIEvaluator_Failures(t *testing.T) {
	vars := Input{Variables: map[string]string{"input": "q", "output": "a"}}
	cases := []struct {
		name    string
		judge   *scriptedJudge
		wantErr string
	}{
		{"judge fails", &scriptedJudge{errs: []error{errors.New("rate limited")}}, "rate limited"},
		{"reply cut off by the token limit", &scriptedJudge{replies: []JudgeResponse{{Text: `{"score": tr`, Truncated: true}}}, "token limit"},
		{"no json in the reply", &scriptedJudge{replies: []JudgeResponse{{Text: "I would say yes."}}}, "valid JSON"},
		{"json without a score", &scriptedJudge{replies: []JudgeResponse{{Text: `{"reasoning":"hm"}`}}}, "score"},
		{"score of the wrong type", &scriptedJudge{replies: []JudgeResponse{{Text: `{"score":"maybe","reasoning":"hm"}`}}}, "boolean"},
		{"empty reply", &scriptedJudge{replies: []JudgeResponse{{Text: "  "}}}, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := newAIEngine(tc.judge).Run(context.Background(), aiDefinition(aiYesNoConfig), vars)
			assert.Equal(t, ScoreStatusError, result.Score.Status)
			assert.Contains(t, result.Reasoning, tc.wantErr)
			assert.Nil(t, result.Pass)
		})
	}
}

// Tokens spent on a reply that could not be used were still billed, so they are reported.
func TestAIEvaluator_ReportsUsageOnUnusableReply(t *testing.T) {
	judge := &scriptedJudge{replies: []JudgeResponse{{Text: "no json here", Usage: &Usage{TotalTokens: 50}}}}
	result := newAIEngine(judge).Run(context.Background(), aiDefinition(aiYesNoConfig), Input{Variables: map[string]string{"input": "q", "output": "a"}})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 50, result.Usage.TotalTokens)
}

// A provider that rejects structured output gets one more try as plain text.
func TestAIEvaluator_RetriesWithoutStructuredOutput(t *testing.T) {
	judge := &scriptedJudge{
		errs:    []error{errors.New("Invalid parameter: 'response_format' of type 'json_schema' is not supported with this model")},
		replies: []JudgeResponse{{}, {Text: `{"score":true,"reasoning":"ok"}`}},
	}
	result := newAIEngine(judge).Run(context.Background(), aiDefinition(aiYesNoConfig), Input{Variables: map[string]string{"input": "q", "output": "a"}})

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	require.Len(t, judge.requests, 2)
	assert.NotNil(t, judge.requests[0].Schema)
	assert.Nil(t, judge.requests[1].Schema, "the retry asks for plain text")
	assert.Equal(t, judge.requests[0].System, judge.requests[1].System)
}

func TestAIEvaluator_DoesNotRetryOtherErrors(t *testing.T) {
	judge := &scriptedJudge{errs: []error{errors.New("401 unauthorized")}}
	result := newAIEngine(judge).Run(context.Background(), aiDefinition(aiYesNoConfig), Input{Variables: map[string]string{"input": "q", "output": "a"}})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Len(t, judge.requests, 1)
}
