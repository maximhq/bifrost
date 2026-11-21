package evals

import (
	"context"
	"testing"
)

// These benchmarks cover what a test run repeats for every entry: build the evaluator,
// render its request and judge the score. The model call itself is stubbed out.

func BenchmarkEngineRun_AIJudge(b *testing.B) {
	judge := func(context.Context, JudgeRequest) (JudgeResponse, error) {
		return JudgeResponse{Text: `{"reasoning":"It states the window.","score":true}`}, nil
	}
	engine := NewEngine(EngineOptions{})
	RegisterAIEvaluator(engine, judge)
	def := Definition{ID: "ai-bench", Type: EvaluatorTypeAI, Version: 1, Config: []byte(aiYesNoConfig)}
	in := Input{Variables: map[string]string{"input": "What is the refund window?", "output": "30 days from delivery.", "context": "Refunds are accepted within 30 days."}}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if result := engine.Run(ctx, def, in); result.Score.Status != ScoreStatusOK {
			b.Fatal(result.Reasoning)
		}
	}
}

func BenchmarkEngineRun_Programmatic(b *testing.B) {
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	def := Definition{ID: "prog-bench", Type: EvaluatorTypeProgrammatic, Version: 1,
		Config: []byte(`{"code":"def validate(output, expected_output):\n    return output.strip().lower() == expected_output.strip().lower()\n","response_type":"yes_no"}`)}
	in := Input{Variables: map[string]string{"output": " Paris ", "expected_output": "paris"}}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if result := engine.Run(ctx, def, in); result.Score.Status != ScoreStatusOK {
			b.Fatal(result.Reasoning)
		}
	}
}

// An unsaved draft has no version to key on, so it is rebuilt every time. This is the
// cost the cache removes for saved evaluators.
func BenchmarkEngineRun_AIJudgeUncached(b *testing.B) {
	judge := func(context.Context, JudgeRequest) (JudgeResponse, error) {
		return JudgeResponse{Text: `{"reasoning":"It states the window.","score":true}`}, nil
	}
	engine := NewEngine(EngineOptions{})
	RegisterAIEvaluator(engine, judge)
	def := Definition{Type: EvaluatorTypeAI, Config: []byte(aiYesNoConfig)}
	in := Input{Variables: map[string]string{"input": "What is the refund window?", "output": "30 days from delivery."}}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		engine.Run(ctx, def, in)
	}
}
