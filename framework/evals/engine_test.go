package evals

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEvaluator is a scripted evaluator for engine tests.
type fakeEvaluator struct {
	required []string
	fn       func(ctx context.Context, in Input) (Result, error)
}

func (f *fakeEvaluator) RequiredVariables() []string { return f.required }

func (f *fakeEvaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	return f.fn(ctx, in)
}

func scoring(score Score) func(context.Context, Input) (Result, error) {
	return func(context.Context, Input) (Result, error) {
		return Result{Score: score, Reasoning: "because"}, nil
	}
}

const statisticalConfig = `{"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":0.8},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`

// newTestEngine registers fn as the statistical metric "fake".
func newTestEngine(t *testing.T, opts EngineOptions, evaluator Evaluator) *Engine {
	t.Helper()
	engine := NewEngine(opts)
	engine.Register(EvaluatorTypeStatistical, "fake", func(Definition, Config) (Evaluator, error) {
		return evaluator, nil
	})
	return engine
}

func fakeDefinition(config string) Definition {
	return Definition{ID: "ev-1", Name: "fake", Type: EvaluatorTypeStatistical, BuiltinKey: "fake", Version: 1, Config: []byte(config)}
}

func TestEngineRun_ScoresAndJudges(t *testing.T) {
	engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: scoring(NumberScore(0.9))})

	result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{})

	assert.Equal(t, NumberScore(0.9), result.Score)
	assert.Equal(t, "because", result.Reasoning)
	require.NotNil(t, result.Pass)
	assert.True(t, *result.Pass)
	assert.Equal(t, "ev-1", result.EvaluatorID)
	assert.Equal(t, 1, result.EvaluatorVersion)
}

func TestEngineRun_FailsBelowThreshold(t *testing.T) {
	engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: scoring(NumberScore(0.5))})

	result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{})

	require.NotNil(t, result.Pass)
	assert.False(t, *result.Pass)
}

// The normalizer runs before the pass criterion, so a 1 to 5 score can be judged on a
// 0 to 1 threshold.
func TestEngineRun_NormalizesBeforeJudging(t *testing.T) {
	engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: scoring(NumberScore(4))})
	config := `{"score_normalizer":{"action":"divide","value":5},"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":0.8},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`

	result := engine.Run(context.Background(), fakeDefinition(config), Input{})

	assert.Equal(t, NumberScore(0.8), result.Score)
	require.NotNil(t, result.Pass)
	assert.True(t, *result.Pass)
}

func TestEngineRun_MissingVariableIsNA(t *testing.T) {
	called := false
	evaluator := &fakeEvaluator{required: []string{"expected_output"}, fn: func(context.Context, Input) (Result, error) {
		called = true
		return Result{Score: NumberScore(1)}, nil
	}}
	engine := newTestEngine(t, EngineOptions{}, evaluator)
	config := `{"variables":{"output":"the model reply"},"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":0.8},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`

	result := engine.Run(context.Background(), fakeDefinition(config), Input{Variables: map[string]string{"input": "hi"}})

	assert.Equal(t, ScoreStatusNA, result.Score.Status)
	assert.Contains(t, result.Reasoning, "expected_output")
	assert.Contains(t, result.Reasoning, "output")
	assert.Nil(t, result.Pass, "a result that was not scored has no verdict")
	assert.False(t, called, "the evaluator never runs without its inputs")
}

// An empty value is still a value: an empty reply is something an evaluator can score.
func TestEngineRun_EmptyVariableIsPresent(t *testing.T) {
	evaluator := &fakeEvaluator{required: []string{"output"}, fn: scoring(NumberScore(0))}
	engine := newTestEngine(t, EngineOptions{}, evaluator)

	result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{Variables: map[string]string{"output": ""}})

	assert.Equal(t, ScoreStatusOK, result.Score.Status)
}

func TestEngineRun_FailuresBecomeErrorResults(t *testing.T) {
	t.Run("evaluator returns an error", func(t *testing.T) {
		engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: func(context.Context, Input) (Result, error) {
			return Result{}, errors.New("judge unavailable")
		}})
		result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{})
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "judge unavailable")
		assert.Nil(t, result.Pass)
	})

	t.Run("evaluator panics", func(t *testing.T) {
		engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: func(context.Context, Input) (Result, error) {
			panic("boom")
		}})
		result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{})
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "boom")
	})

	t.Run("evaluator exceeds its timeout", func(t *testing.T) {
		engine := newTestEngine(t, EngineOptions{Timeout: 20 * time.Millisecond}, &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		}})
		result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{})
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "timed out")
	})

	t.Run("invalid config", func(t *testing.T) {
		engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: scoring(NumberScore(1))})
		result := engine.Run(context.Background(), fakeDefinition(`{"pass_criteria":{"entry_level":{"name":"score","operator":"~","value":1}}}`), Input{})
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "operator")
	})

	t.Run("unregistered evaluator", func(t *testing.T) {
		engine := NewEngine(EngineOptions{})
		result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{})
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "fake")
	})
}

func TestEngineBuild_HumanIsNotExecutable(t *testing.T) {
	engine := NewEngine(EngineOptions{})
	_, err := engine.Build(Definition{Type: EvaluatorTypeHuman, Config: []byte(`{"evaluation_type":"yes_no"}`)})
	require.ErrorIs(t, err, ErrNotExecutable)
}

// A score that does not match the configured grading style is an evaluator fault, not a
// low score.
func TestEngineRun_ChecksScoreAgainstGradingStyle(t *testing.T) {
	const aiBase = `"instructions":"x","provider":"openai","model":"gpt-4o-mini"`
	cases := []struct {
		name       string
		config     string
		score      Score
		wantStatus ScoreStatus
	}{
		{"yes_no takes a boolean", `{` + aiBase + `,"grading_style":"yes_no"}`, BoolScore(true), ScoreStatusOK},
		{"yes_no rejects a number", `{` + aiBase + `,"grading_style":"yes_no"}`, NumberScore(1), ScoreStatusError},
		{"scale takes a number in range", `{` + aiBase + `,"grading_style":"scale","scale":{"min":1,"max":5}}`, NumberScore(5), ScoreStatusOK},
		{"scale rejects a number out of range", `{` + aiBase + `,"grading_style":"scale","scale":{"min":1,"max":5}}`, NumberScore(7), ScoreStatusError},
		{"number takes any number", `{` + aiBase + `,"grading_style":"number"}`, NumberScore(-3.5), ScoreStatusOK},
		{"number rejects a label", `{` + aiBase + `,"grading_style":"number"}`, StringScore("high"), ScoreStatusError},
		{"multiselect takes a listed option", `{` + aiBase + `,"grading_style":"multiselect","pass_criteria":{"entry_level":{"name":"score","operator":"is one of","value":["good"],"options":["good","bad"]},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`, StringScore("bad"), ScoreStatusOK},
		{"multiselect rejects an unlisted option", `{` + aiBase + `,"grading_style":"multiselect","pass_criteria":{"entry_level":{"name":"score","operator":"is one of","value":["good"],"options":["good","bad"]},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`, StringScore("meh"), ScoreStatusError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine(EngineOptions{})
			engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
				return &fakeEvaluator{fn: scoring(tc.score)}, nil
			})
			result := engine.Run(context.Background(), Definition{Type: EvaluatorTypeAI, Config: []byte(tc.config)}, Input{})
			assert.Equal(t, tc.wantStatus, result.Score.Status, result.Reasoning)
		})
	}
}

func TestEngineRunMany_KeepsOrderAndBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	evaluator := &fakeEvaluator{fn: func(_ context.Context, in Input) (Result, error) {
		now := inFlight.Add(1)
		mu.Lock()
		if now > peak.Load() {
			peak.Store(now)
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return Result{Score: NumberScore(1)}, nil
	}}
	engine := newTestEngine(t, EngineOptions{StatisticalConcurrency: 3}, evaluator)

	defs := make([]Definition, 12)
	for i := range defs {
		defs[i] = fakeDefinition(statisticalConfig)
		defs[i].ID = string(rune('a' + i))
	}
	results := engine.RunMany(context.Background(), defs, Input{})

	require.Len(t, results, len(defs))
	for i, result := range results {
		assert.Equal(t, defs[i].ID, result.EvaluatorID, "results come back in the order they were asked for")
		assert.Equal(t, ScoreStatusOK, result.Score.Status)
	}
	assert.LessOrEqual(t, peak.Load(), int32(3), "no more than the pool size run at once")
	assert.Greater(t, peak.Load(), int32(1), "evaluators do run concurrently")
}

func TestExtractVariables(t *testing.T) {
	assert.Equal(t,
		[]string{"output", "input", "expected_output"},
		ExtractVariables("Does {{ output }} answer {{input}}? Compare with {{expected_output}} and {{output}} again."),
		"names come back in order of first use, trimmed and without repeats",
	)
	assert.Empty(t, ExtractVariables("no placeholders, {single} braces, {{}} empty"))
}

// A built-in judge makes several model calls in a row, each with its own time limit.
// The limit on the whole evaluation has to leave room for all of them, so an AI
// evaluator gets a longer one than an evaluator that calls nothing.
func TestEngine_AIEvaluatorsGetALongerTimeout(t *testing.T) {
	slow := func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			select {
			case <-time.After(80 * time.Millisecond):
				return Result{Score: BoolScore(true)}, nil
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}}, nil
	}
	engine := NewEngine(EngineOptions{Timeout: 20 * time.Millisecond})
	engine.Register(EvaluatorTypeAI, "", slow)
	engine.Register(EvaluatorTypeProgrammatic, "", slow)

	ai := engine.Run(context.Background(), Definition{Type: EvaluatorTypeAI, Config: []byte(`{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"m"}`)}, Input{})
	assert.Equal(t, ScoreStatusOK, ai.Score.Status, ai.Reasoning)
	local := engine.Run(context.Background(), Definition{Type: EvaluatorTypeProgrammatic, Config: []byte(`{"code":"def validate(output):\n    return True\n","response_type":"yes_no"}`)}, Input{Variables: map[string]string{"output": "x"}})
	assert.Equal(t, ScoreStatusError, local.Score.Status)
	assert.Contains(t, local.Reasoning, "timed out")

	short := NewEngine(EngineOptions{Timeout: 20 * time.Millisecond, AITimeout: 30 * time.Millisecond})
	short.Register(EvaluatorTypeAI, "", slow)
	cut := short.Run(context.Background(), Definition{Type: EvaluatorTypeAI, Config: []byte(`{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"m"}`)}, Input{})
	assert.Equal(t, ScoreStatusError, cut.Score.Status)
	assert.Contains(t, cut.Reasoning, "30ms")
}
