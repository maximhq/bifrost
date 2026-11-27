package handlers

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/traceingest"
)

// traceEvaluatorWaitMs is how long one evaluation of a trace may take before it is given up on.
const traceEvaluatorWaitMs = 120_000

// evalsTraceEvaluator runs a Tracing project's evaluator rules through the evals service. Every
// run goes through the evals queue, so a model-calling evaluator runs on an eval key within that
// key's concurrency, and its model calls are ordinary gateway requests.
type evalsTraceEvaluator struct {
	service *evals.Service
}

// NewTraceEvaluator adapts the evals service to what trace ingest calls.
func NewTraceEvaluator(service *evals.Service) traceingest.TraceEvaluator {
	return &evalsTraceEvaluator{service: service}
}

func (e *evalsTraceEvaluator) EvaluateTrace(ctx context.Context, evaluatorID string, variables map[string]string) (*traceingest.TraceEvaluation, error) {
	wait := traceEvaluatorWaitMs
	view, err := e.service.Execute(ctx, evals.ExecuteInput{EvaluatorID: evaluatorID, Variables: variables, WaitMs: &wait, CreatedBy: "tracing"})
	if err != nil {
		return nil, err
	}
	if view == nil || view.Result == nil {
		return nil, fmt.Errorf("the evaluation did not finish in time")
	}
	result := view.Result
	evaluation := &traceingest.TraceEvaluation{Reasoning: result.Reasoning}
	switch result.Score.Status {
	case evals.ScoreStatusNA:
		evaluation.NotApplicable = true
	case evals.ScoreStatusOK:
		switch {
		case result.Score.Number != nil:
			evaluation.Value = result.Score.Number
		case result.Score.Bool != nil:
			evaluation.Value = new(0.0)
			if *result.Score.Bool {
				evaluation.Value = new(1.0)
			}
		case result.Score.String != nil:
			evaluation.Label = *result.Score.String
		default:
			evaluation.NotApplicable = true
		}
	default:
		return nil, fmt.Errorf("the evaluator failed: %s", result.Reasoning)
	}
	return evaluation, nil
}
