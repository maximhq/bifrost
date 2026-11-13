package traceingest

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// TraceEvaluation is what an evaluator said about one trace.
type TraceEvaluation struct {
	Value     *float64
	Label     string
	Reasoning string
	// NotApplicable is set when the evaluator ran and had nothing to say. No score is stored.
	NotApplicable bool
}

// TraceEvaluator runs one evaluator. The evals service implements it. Its model calls go through
// the gateway as ordinary requests on an eval key: they are never attached to the user's trace.
type TraceEvaluator interface {
	EvaluateTrace(ctx context.Context, evaluatorID string, variables map[string]string) (*TraceEvaluation, error)
}

// evaluationWorkers bounds how many evaluations run at once across all projects.
const evaluationWorkers = 4

type evaluations struct {
	evaluator TraceEvaluator
	slots     chan struct{}
	mu        sync.Mutex
	running   map[string]struct{}
	// counts is how many traces each rule scored today, by rule key. It resets when the day turns.
	counts map[string]int
	day    string
}

// SetTraceEvaluator sets what runs a project's evaluator rules. Without one, rules are ignored.
func (s *Service) SetTraceEvaluator(evaluator TraceEvaluator) {
	s.evals = &evaluations{evaluator: evaluator, slots: make(chan struct{}, evaluationWorkers), running: make(map[string]struct{}), counts: make(map[string]int)}
}

// sampled reports whether a trace falls inside a rule's sample. The choice is a hash of the trace
// and the evaluator, so it is the same every time the trace is rolled up: a trace is in the sample
// or out of it for good.
func sampled(traceID, evaluatorID string, rate float64) bool {
	if rate >= 1 {
		return true
	}
	if rate <= 0 {
		return false
	}
	h := fnv.New64a()
	h.Write([]byte(traceID))
	h.Write([]byte{0})
	h.Write([]byte(evaluatorID))
	return float64(h.Sum64()%1_000_000)/1_000_000 < rate
}

// evaluateTrace runs the project's evaluator rules on a trace that has completed. Each rule scores
// a trace once: the score's id is derived from the trace and the evaluator, and a trace that
// already carries it is left alone. The work runs off the rollup loop.
func (s *Service) evaluateTrace(ctx context.Context, trace *logstore.Trace) {
	if s.evals == nil || trace == nil || trace.InProgress || (trace.Input == "" && trace.Output == "") {
		return
	}
	project, err := s.cachedProject(ctx, trace.ProjectID)
	if err != nil || len(project.Evaluators) == 0 {
		return
	}
	var existing map[string]struct{}
	for _, rule := range project.Evaluators {
		if !rule.Enabled || rule.EvaluatorID == "" || !sampled(trace.TraceID, rule.EvaluatorID, rule.SampleRate) {
			continue
		}
		scoreID := rowID("evaluator", trace.ProjectID, trace.TraceID, rule.EvaluatorID)
		if existing == nil {
			scores, err := s.store.ListTraceScores(ctx, trace.ProjectID, trace.TraceID)
			if err != nil {
				return
			}
			existing = make(map[string]struct{}, len(scores))
			for _, score := range scores {
				existing[score.ID] = struct{}{}
			}
		}
		if _, done := existing[scoreID]; done {
			continue
		}
		if !s.evals.claim(scoreID, project.ID+"\x00"+rule.EvaluatorID, rule.DailyCap) {
			continue
		}
		go s.runEvaluation(*trace, rule, scoreID)
	}
}

// claim reserves one evaluation: it refuses a trace already being scored and a rule over its daily cap.
func (e *evaluations) claim(scoreID, ruleKey string, dailyCap int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if today := time.Now().UTC().Format("2006-01-02"); today != e.day {
		e.day, e.counts = today, make(map[string]int)
	}
	if _, busy := e.running[scoreID]; busy {
		return false
	}
	if dailyCap > 0 && e.counts[ruleKey] >= dailyCap {
		return false
	}
	e.running[scoreID] = struct{}{}
	e.counts[ruleKey]++
	return true
}

func (s *Service) runEvaluation(trace logstore.Trace, rule tables.TraceEvaluatorRule, scoreID string) {
	defer func() {
		s.evals.mu.Lock()
		delete(s.evals.running, scoreID)
		s.evals.mu.Unlock()
	}()
	select {
	case s.evals.slots <- struct{}{}:
		defer func() { <-s.evals.slots }()
	case <-s.done:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	variables := map[string]string{"input": trace.Input, "output": trace.Output}
	if trace.RootSpanID != "" {
		if roots, err := s.store.FindTraceSpans(ctx, trace.ProjectID, []logstore.TraceSpanKey{{TraceID: trace.TraceID, SpanID: trace.RootSpanID}}); err == nil && len(roots) > 0 && roots[0].Expected != "" {
			variables["expected_output"] = roots[0].Expected
		}
	}
	evaluation, err := s.evals.evaluator.EvaluateTrace(ctx, rule.EvaluatorID, variables)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("traceingest: evaluator %s failed on trace %s: %v", rule.EvaluatorID, trace.TraceID, err)
		}
		return
	}
	if evaluation == nil || evaluation.NotApplicable {
		return
	}
	name := rule.Name
	if name == "" {
		name = rule.EvaluatorID
	}
	score := &logstore.TraceScore{
		ID: scoreID, ProjectID: trace.ProjectID, TraceID: trace.TraceID, Name: name, Value: evaluation.Value, StringValue: evaluation.Label,
		Comment: evaluation.Reasoning, Source: logstore.TraceAuthorEvaluator, Author: rule.EvaluatorID, DataType: "numeric", ExpiresAt: trace.ExpiresAt,
	}
	if score.Value == nil {
		score.DataType = "categorical"
	}
	if err := s.store.UpsertTraceScores(ctx, []*logstore.TraceScore{score}); err != nil {
		if s.logger != nil {
			s.logger.Warn("traceingest: failed to store the score of evaluator %s on trace %s: %v", rule.EvaluatorID, trace.TraceID, err)
		}
		return
	}
	// The rollup carries scores, so it is rebuilt. The next pass finds this score and stops there.
	_ = s.store.MarkTracesDirty(ctx, []logstore.TraceKey{{ProjectID: trace.ProjectID, TraceID: trace.TraceID}})
}
