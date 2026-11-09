package evals

import (
	"context"
	"errors"
	"math"
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

// panickingRequired panics when the engine asks for its variables.
type panickingRequired struct{}

func (panickingRequired) RequiredVariables() []string { panic("required boom") }

func (panickingRequired) Evaluate(context.Context, Input) (Result, error) {
	return Result{Score: NumberScore(1)}, nil
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

// A normalizer can push a valid score past the float range. Such a score cannot be
// stored, so the run ends as an error score and keeps what the evaluator spent.
func TestEngineRun_RejectsNonFiniteNormalizedScore(t *testing.T) {
	cost := 0.25
	engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: func(context.Context, Input) (Result, error) {
		return Result{Score: NumberScore(2), Usage: &Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}, Cost: &cost}, nil
	}})
	config := `{"score_normalizer":{"action":"multiply","value":1e308},"pass_criteria":{"entry_level":{"name":"score","operator":">=","value":0.8},"run_level":{"name":"queries_passed","operator":">=","value":80}}}`

	result := engine.Run(context.Background(), fakeDefinition(config), Input{})

	assert.Equal(t, ScoreStatusError, result.Score.Status, "a score of %v is not stored as ok", result.Score.Number)
	assert.Contains(t, result.Reasoning, "finite")
	require.NotNil(t, result.Usage, "the usage is kept")
	assert.Equal(t, 7, result.Usage.TotalTokens)
	require.NotNil(t, result.Cost, "the cost is kept")
	assert.Nil(t, result.Pass)
}

// An evaluator that answers a non-finite number is refused before it is normalized.
func TestEngineRun_RejectsNonFiniteScore(t *testing.T) {
	engine := newTestEngine(t, EngineOptions{}, &fakeEvaluator{fn: scoring(NumberScore(math.Inf(1)))})
	result := engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "finite")
}

// A mapping normalizer turns a yes/no answer into a number before it is judged, so the
// default criterion `= "Yes"` is compared with the number "Yes" maps to: a true answer
// passes and a false one fails.
func TestEngineRun_JudgesAMappedYesNoAnswerInTheMappedSpace(t *testing.T) {
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	config := func(answer string) []byte {
		return []byte(`{"code":"def validate(output):\n    return ` + answer + `\n","response_type":"yes_no","score_normalizer":{"action":"mapping","mapping":{"Yes":1,"No":0}}}`)
	}
	run := func(answer string) Result {
		return engine.Run(context.Background(), Definition{Type: EvaluatorTypeProgrammatic, Config: config(answer)}, Input{Variables: map[string]string{"output": "x"}})
	}

	yes := run("True")
	require.Equal(t, ScoreStatusOK, yes.Score.Status, yes.Reasoning)
	assert.Equal(t, NumberScore(1), yes.Score)
	require.NotNil(t, yes.Pass)
	assert.True(t, *yes.Pass, "a Yes mapped to 1 meets the criterion = Yes")

	no := run("False")
	require.Equal(t, ScoreStatusOK, no.Score.Status, no.Reasoning)
	assert.Equal(t, NumberScore(0), no.Score)
	require.NotNil(t, no.Pass)
	assert.False(t, *no.Pass, "a No mapped to 0 does not meet the criterion = Yes")
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

	t.Run("factory panics while building", func(t *testing.T) {
		engine := NewEngine(EngineOptions{})
		engine.Register(EvaluatorTypeStatistical, "fake", func(Definition, Config) (Evaluator, error) {
			panic("factory boom")
		})
		for _, def := range []Definition{fakeDefinition(statisticalConfig), {Type: EvaluatorTypeStatistical, BuiltinKey: "fake", Config: []byte(statisticalConfig)}} {
			var result Result
			require.NotPanics(t, func() { result = engine.Run(context.Background(), def, Input{}) })
			assert.Equal(t, ScoreStatusError, result.Score.Status)
			assert.Contains(t, result.Reasoning, "factory boom")
		}
	})

	t.Run("required variables panic", func(t *testing.T) {
		engine := NewEngine(EngineOptions{})
		engine.Register(EvaluatorTypeStatistical, "fake", func(Definition, Config) (Evaluator, error) {
			return panickingRequired{}, nil
		})
		var result Result
		require.NotPanics(t, func() { result = engine.Run(context.Background(), fakeDefinition(statisticalConfig), Input{}) })
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "required boom")
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
	// Every evaluator signals entry and holds until released, so the test waits for the
	// pool to fill instead of hoping a second evaluator enters during a short sleep.
	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	entered := make(chan struct{}, 12)
	release := make(chan struct{})
	evaluator := &fakeEvaluator{fn: func(_ context.Context, in Input) (Result, error) {
		now := inFlight.Add(1)
		mu.Lock()
		if now > peak.Load() {
			peak.Store(now)
		}
		mu.Unlock()
		entered <- struct{}{}
		<-release
		inFlight.Add(-1)
		return Result{Score: NumberScore(1)}, nil
	}}
	engine := newTestEngine(t, EngineOptions{StatisticalConcurrency: 3}, evaluator)

	defs := make([]Definition, 12)
	for i := range defs {
		defs[i] = fakeDefinition(statisticalConfig)
		defs[i].ID = string(rune('a' + i))
	}
	done := make(chan []Result, 1)
	go func() { done <- engine.RunMany(context.Background(), defs, Input{}) }()

	timeout := time.After(2 * time.Second)
	for range 3 {
		select {
		case <-entered:
		case <-timeout:
			close(release)
			t.Fatalf("the pool of 3 did not fill: %d evaluators entered", inFlight.Load())
		}
	}
	assert.Equal(t, int32(3), inFlight.Load(), "three evaluators run at once")
	select {
	case <-entered:
		t.Error("a fourth evaluator entered while three held the pool")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)

	var results []Result
	select {
	case results = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunMany did not finish after release")
	}
	require.Len(t, results, len(defs))
	for i, result := range results {
		assert.Equal(t, defs[i].ID, result.EvaluatorID, "results come back in the order they were asked for")
		assert.Equal(t, ScoreStatusOK, result.Score.Status)
	}
	assert.LessOrEqual(t, peak.Load(), int32(3), "no more than the pool size run at once")
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
	// The fake records its context's absolute deadline and returns at once, so no timer
	// races the deadline. The test brackets each Run with wall-clock reads, so a pause
	// between the engine setting the limit and the fake reading it cannot fail the check.
	var mu sync.Mutex
	var seen time.Time
	recording := func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return Result{}, errors.New("no deadline")
			}
			mu.Lock()
			seen = deadline
			mu.Unlock()
			return Result{Score: BoolScore(true)}, nil
		}}, nil
	}
	run := func(t *testing.T, engine *Engine, def Definition, in Input, want time.Duration, what string) {
		t.Helper()
		before := time.Now()
		result := engine.Run(context.Background(), def, in)
		after := time.Now()
		require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
		mu.Lock()
		deadline := seen
		mu.Unlock()
		assert.False(t, deadline.Before(before.Add(want)), "%s: the deadline %s is no earlier than the limit after the call started (%s)", what, deadline, before.Add(want))
		assert.False(t, deadline.After(after.Add(want)), "%s: the deadline %s is no later than the limit after the call ended (%s)", what, deadline, after.Add(want))
	}
	aiDef := Definition{Type: EvaluatorTypeAI, Config: []byte(`{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"m"}`)}
	localDef := Definition{Type: EvaluatorTypeProgrammatic, Config: []byte(`{"code":"def validate(output):\n    return True\n","response_type":"yes_no"}`)}
	localIn := Input{Variables: map[string]string{"output": "x"}}

	engine := NewEngine(EngineOptions{Timeout: 20 * time.Millisecond})
	engine.Register(EvaluatorTypeAI, "", recording)
	engine.Register(EvaluatorTypeProgrammatic, "", recording)

	run(t, engine, aiDef, Input{}, defaultAIEvaluatorTimeout, "AI default")
	run(t, engine, localDef, localIn, 20*time.Millisecond, "local")

	short := NewEngine(EngineOptions{Timeout: 20 * time.Millisecond, AITimeout: 30 * time.Millisecond})
	short.Register(EvaluatorTypeAI, "", recording)
	run(t, short, aiDef, Input{}, 30*time.Millisecond, "AI override")

	// A run cut by its deadline says so. This fake only waits on the context.
	blocking := func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		}}, nil
	}
	cutEngine := NewEngine(EngineOptions{Timeout: 20 * time.Millisecond, AITimeout: 30 * time.Millisecond})
	cutEngine.Register(EvaluatorTypeAI, "", blocking)
	cutEngine.Register(EvaluatorTypeProgrammatic, "", blocking)
	cutLocal := cutEngine.Run(context.Background(), localDef, localIn)
	assert.Equal(t, ScoreStatusError, cutLocal.Score.Status)
	assert.Contains(t, cutLocal.Reasoning, "timed out")
	cut := cutEngine.Run(context.Background(), aiDef, Input{})
	assert.Equal(t, ScoreStatusError, cut.Score.Status)
	assert.Contains(t, cut.Reasoning, "30ms")
}
