package heimdall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

var (
	errCancelled   = errors.New("cancelled by request")
	errNotHermetic = errors.New("the replay client could run a tool")
)

// activeRun is the run being worked on in this process.
type activeRun struct {
	id     string
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// runFailure ends a run as failed, with a code a client can switch on.
type runFailure struct{ code, message string }

func (f *runFailure) Error() string { return f.code + ": " + f.message }

func failure(code, format string, args ...any) *runFailure {
	return &runFailure{code: code, message: fmt.Sprintf(format, args...)}
}

// launchLocked starts a run in the background. The caller holds p.mu.
func (p *Plugin) launchLocked(runID string) {
	ctx, cancel := context.WithCancelCause(p.ctx)
	ar := &activeRun{id: runID, cancel: cancel, done: make(chan struct{})}
	p.active = ar
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			p.mu.Lock()
			if p.active == ar {
				p.active = nil
			}
			p.mu.Unlock()
			cancel(nil)
			close(ar.done)
		}()
		p.execute(ctx, runID)
	}()
}

// execute works one run to its end and records how it ended. A run that is cancelled or stopped by a shutdown
// keeps the replays it finished and has no verdict.
func (p *Plugin) execute(ctx context.Context, runID string) {
	// The final write must go through even when ctx has been cancelled.
	final := context.WithoutCancel(ctx)

	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				p.logger.Error("heimdall: run %s panicked: %v\n%s", runID, r, debug.Stack())
				err = failure("internal", "the run panicked: %v", r)
			}
		}()
		err = p.pipeline(ctx, runID)
	}()
	if err == nil {
		return
	}

	status, code, message := StatusFailed, "internal", err.Error()
	var rf *runFailure
	switch {
	case errors.As(err, &rf):
		code, message = rf.code, rf.message
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), errCancelled):
		status, code, message = StatusCancelled, "cancelled", "the run was cancelled"
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), errShutdown):
		status, code, message = StatusInterrupted, "interrupted", "the plugin stopped before the run finished"
	}
	if status == StatusFailed {
		p.logger.Error("heimdall: run %s failed: %s: %s", runID, code, message)
	}
	werr := p.retry(final, func() error {
		return p.store.updateRun(final, runID, map[string]any{"status": status, "stage": StageDone, "finished_at": p.now(), "error_code": code, "error_message": message})
	})
	if werr != nil {
		p.logger.Error("heimdall: could not record how run %s ended: %v", runID, werr)
	}
}

// retry runs a store write once more if it fails. A verdict needs complete data, so a write that fails twice
// fails the run.
func (p *Plugin) retry(ctx context.Context, write func() error) error {
	if err := write(); err != nil {
		if ctx.Err() != nil {
			return err
		}
		p.logger.Warn("heimdall: store write failed, retrying: %v", err)
		return write()
	}
	return nil
}

// pipeline runs the stages: planning, replaying, evaluating. It writes the completed run itself.
func (p *Plugin) pipeline(ctx context.Context, runID string) error {
	run, err := p.store.getRun(ctx, runID)
	if err != nil {
		return failure("internal", "load the run: %v", err)
	}
	var params RunParams
	var rules ToolRules
	var selection Selection
	for _, d := range []struct {
		name string
		raw  string
		into any
	}{{"params", run.ParamsJSON, &params}, {"tool rules", run.RulesJSON, &rules}, {"selection", run.SelectionJSON, &selection}} {
		if err := json.Unmarshal([]byte(d.raw), d.into); err != nil {
			return failure("internal", "read the run's %s: %v", d.name, err)
		}
	}
	baseline := Route{Provider: schemas.ModelProvider(run.BaselineProvider), Model: run.BaselineModel}
	candidate := Route{Provider: schemas.ModelProvider(run.CandidateProvider), Model: run.CandidateModel}

	if err := p.update(ctx, runID, map[string]any{"status": StatusRunning, "stage": StagePlanning, "started_at": p.now()}); err != nil {
		return err
	}

	// ---- planning ----
	traces, err := p.plan(ctx, run, selection, baseline)
	if err != nil {
		return err
	}
	perSession := params.BaselineRuns + params.CandidateRuns
	if err := p.update(ctx, runID, map[string]any{"stage": StageReplaying, "replays_total": len(traces) * perSession}); err != nil {
		return err
	}

	// ---- replaying ----
	results, budget, err := p.replay(ctx, run, traces, params, rules, baseline, candidate)
	spend, finished := 0.0, 0
	if budget != nil {
		spend = budget.Spent()
	}
	if results != nil {
		finished = len(results.results)
	}
	// The counts written as replays ended are progress; these are the final numbers.
	if uerr := p.update(context.WithoutCancel(ctx), runID, map[string]any{"spend_usd": spend, "replays_done": finished}); err == nil {
		err = uerr
	}
	if err != nil {
		return err
	}

	// ---- evaluating ----
	if err := p.update(ctx, runID, map[string]any{"stage": StageEvaluating}); err != nil {
		return err
	}
	return p.evaluate(ctx, run, traces, results, params, rules, budget, spend)
}

// update writes columns of the run, once more if the first write fails.
func (p *Plugin) update(ctx context.Context, runID string, fields map[string]any) error {
	if err := p.retry(ctx, func() error { return p.store.updateRun(ctx, runID, fields) }); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return failure("store", "write the run: %v", err)
	}
	return nil
}

// plan rebuilds the selected sessions, stores who is eligible and why not, and returns the traces to replay.
func (p *Plugin) plan(ctx context.Context, run *tables.TableHeimdallRun, selection Selection, baseline Route) ([]*SessionTrace, error) {
	summaries, err := p.selectSessions(ctx, selection)
	if err != nil {
		return nil, failure("log_store", "select sessions: %v", err)
	}
	recon := NewReconstructor(p.reader, ReconOptions{MaxSteps: p.config.MaxSteps, Baseline: &baseline, InjectedTools: p.hasInjectedTools})

	var traces []*SessionTrace
	rows := make([]tables.TableHeimdallRunSession, 0, len(summaries))
	reasons := map[string]int{}
	for _, s := range summaries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		trace, elig, err := recon.Build(ctx, s.SessionID)
		if err != nil {
			return nil, failure("log_store", "rebuild session %s: %v", s.SessionID, err)
		}
		row := tables.TableHeimdallRunSession{RunID: run.ID, SessionID: s.SessionID, Eligibility: elig.Code, EncryptionStatus: "unencrypted", FirstDivergence: -1}
		if elig.Eligible() {
			snap, err := trace.Snapshot()
			if err != nil {
				return nil, failure("internal", "snapshot session %s: %v", s.SessionID, err)
			}
			text := string(snap)
			row.SnapshotJSON = &text
			traces = append(traces, trace)
		} else {
			reasons[elig.Code]++
		}
		rows = append(rows, row)
	}

	reasonsJSON, _ := json.Marshal(reasons)
	if err := p.retry(ctx, func() error { return p.store.createRunSessions(ctx, rows) }); err != nil {
		return nil, failure("store", "write the sessions: %v", err)
	}
	if err := p.update(ctx, run.ID, map[string]any{"sessions_selected": len(summaries), "sessions_eligible": len(traces), "ineligible_json": string(reasonsJSON)}); err != nil {
		return nil, err
	}
	return traces, nil
}

// job is one replay to run.
type job struct {
	trace   *SessionTrace
	arm     Arm
	route   Route
	attempt int
}

// replayKey names a replay within a run.
type replayKey struct {
	session string
	arm     Arm
	attempt int
}

// replayed is what the workers hand back.
type replayed struct {
	results []*ReplayResult
	rowIDs  map[replayKey]string
}

// replay runs every session on the original model first, then on the candidate. Baseline first means a run whose
// baseline cannot be replayed stops before it spends money on the candidate.
func (p *Plugin) replay(ctx context.Context, run *tables.TableHeimdallRun, traces []*SessionTrace, params RunParams, rules ToolRules, baseline, candidate Route) (*replayed, *Budget, error) {
	out := &replayed{rowIDs: map[replayKey]string{}}
	if len(traces) == 0 {
		return out, nil, nil
	}
	client, err := newReplayClient(ctx, p.deps.Account, []Route{baseline, candidate}, p.config.MaxConcurrency, p.pricing, p.logger)
	if err != nil {
		var denied *UnsupportedCandidateError
		if errors.As(err, &denied) {
			return nil, nil, failure("unsupported_candidate", "%v", err)
		}
		return nil, nil, failure("replay_client", "%v", err)
	}
	defer client.Shutdown()
	if p.testHookClient != nil {
		p.testHookClient(client)
	}

	callTimeout, _ := p.config.callTimeout()
	budget := NewBudget(run.MaxUSD, run.MaxCalls)
	engine := NewEngine(client, EngineOptions{CallTimeout: callTimeout, MaxSteps: p.config.MaxSteps, Rules: rules, Budget: budget, Pricing: p.pricing, Params: p.params})

	var baselineJobs, candidateJobs []job
	for _, t := range traces {
		for a := 1; a <= params.BaselineRuns; a++ {
			baselineJobs = append(baselineJobs, job{t, ArmBaseline, baseline, a})
		}
		for a := 1; a <= params.CandidateRuns; a++ {
			candidateJobs = append(candidateJobs, job{t, ArmCandidate, candidate, a})
		}
	}

	var done int
	pool := &workerPool{plugin: p, engine: engine, runID: run.ID, out: out, done: &done, budget: budget}
	if err := pool.run(ctx, baselineJobs); err != nil {
		return out, budget, err
	}
	if !anySessionUsable(out.results, traces, ArmBaseline) {
		p.logger.Warn("heimdall: run %s: no session has two usable baseline replays; the candidate is not replayed", run.ID)
		return out, budget, nil
	}
	return out, budget, pool.run(ctx, candidateJobs)
}

// anySessionUsable reports whether any session has enough usable replays in an arm to be scored.
func anySessionUsable(results []*ReplayResult, traces []*SessionTrace, arm Arm) bool {
	usable := map[string]int{}
	for _, r := range results {
		if r.Arm == arm && r.Outcome.Usable() {
			usable[r.SessionID]++
		}
	}
	for _, t := range traces {
		if usable[t.SessionID] >= minUsablePerArm {
			return true
		}
	}
	return false
}

// workerPool runs replays with a bounded number of workers and stores each as it ends.
type workerPool struct {
	plugin *Plugin
	engine *Engine
	runID  string
	out    *replayed
	budget *Budget

	mu   sync.Mutex
	done *int
}

func (w *workerPool) run(ctx context.Context, jobs []job) error {
	if len(jobs) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	queue := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < w.plugin.config.MaxConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				w.one(ctx, cancel, j)
			}
		}()
	}
feed:
	for _, j := range jobs {
		select {
		case queue <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()

	// A failure inside a worker is the cause; otherwise it is the run's own cancellation.
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		var rf *runFailure
		if errors.As(cause, &rf) {
			return rf
		}
		if errors.Is(cause, errNotHermetic) {
			return failure("not_hermetic", "%v", cause)
		}
		return cause
	}
	return ctx.Err()
}

// one replays one session and stores the result.
func (w *workerPool) one(ctx context.Context, stop context.CancelCauseFunc, j job) {
	if ctx.Err() != nil {
		return
	}
	res := w.engine.ReplayOnce(ctx, j.trace, j.route, j.arm, j.attempt)
	if res.Outcome == ReplayNotHermetic {
		stop(fmt.Errorf("%w: %s", errNotHermetic, res.Error))
		return
	}

	actions, _ := json.Marshal(res.Actions)
	text := string(actions)
	row := &tables.TableHeimdallReplay{
		ID: newID(), RunID: w.runID, SessionID: res.SessionID, Arm: string(res.Arm), Attempt: res.Attempt, Outcome: string(res.Outcome),
		ActionsJSON: &text, PromptTokens: res.Usage.PromptTokens, CompletionTokens: res.Usage.CompletionTokens,
		CostUSD: res.Usage.CostUSD, LatencyMS: res.Usage.LatencyMS, ErrorMessage: res.Error, CreatedAt: w.plugin.now(),
	}
	bg := context.WithoutCancel(ctx)
	if err := w.plugin.retry(bg, func() error { return w.plugin.store.createReplay(bg, row) }); err != nil {
		stop(failure("store", "write a replay: %v", err))
		return
	}

	// Progress is written while the lock is held, so two workers cannot write their counts out of order.
	w.mu.Lock()
	defer w.mu.Unlock()
	w.out.results = append(w.out.results, res)
	w.out.rowIDs[replayKey{res.SessionID, res.Arm, res.Attempt}] = row.ID
	*w.done++
	_ = w.plugin.store.updateRun(bg, w.runID, map[string]any{"replays_done": *w.done, "spend_usd": w.budget.Spent()})
}

// evaluate scores the sessions, asks the gate, and writes the completed run.
func (p *Plugin) evaluate(ctx context.Context, run *tables.TableHeimdallRun, traces []*SessionTrace, rep *replayed, params RunParams, rules ToolRules, budget *Budget, spend float64) error {
	evals := Evaluate(traces, rep.results, rules)

	input := GateInput{Params: params, Selected: run.SessionsSelected}
	// SessionsSelected was written during planning; the run row passed in predates it.
	if fresh, err := p.store.getRun(ctx, run.ID); err == nil {
		input.Selected = fresh.SessionsSelected
	}
	for _, ev := range evals {
		if err := ctx.Err(); err != nil {
			return err
		}
		input.Sessions = append(input.Sessions, ev.Score)
		if err := p.storeEvaluation(ctx, run.ID, ev, rep); err != nil {
			return err
		}
	}
	for _, r := range rep.results {
		if r.Outcome == ReplayBudgetStopped {
			input.BudgetStopped = true
			break
		}
	}

	decision := Decide(input)
	decisionJSON, _ := json.Marshal(decision)
	metrics, _ := json.Marshal(metricsOf(rep.results))
	verdict := string(decision.Verdict)
	return p.update(context.WithoutCancel(ctx), run.ID, map[string]any{
		"status": StatusComplete, "stage": StageDone, "finished_at": p.now(), "verdict": verdict, "decision_json": string(decisionJSON),
		"metrics_json": string(metrics), "sessions_scored": decision.ScoredSessions, "spend_usd": spend,
	})
}

// storeEvaluation writes a session's score and each of its replays' comparison.
func (p *Plugin) storeEvaluation(ctx context.Context, runID string, ev SessionEvaluation, rep *replayed) error {
	for _, rc := range ev.Replays {
		id, ok := rep.rowIDs[replayKey{rc.Result.SessionID, rc.Result.Arm, rc.Result.Attempt}]
		if !ok {
			continue
		}
		comparison, _ := json.Marshal(rc.Comparison)
		fields := map[string]any{"comparison_json": string(comparison)}
		if rc.Comparison.Usable {
			fields["divergence"] = rc.Comparison.Divergence
		}
		if err := p.retry(ctx, func() error { return p.store.updateReplay(ctx, id, fields) }); err != nil {
			return failure("store", "write a comparison: %v", err)
		}
	}
	s := ev.Score
	scoreJSON, _ := json.Marshal(s)
	fields := map[string]any{"critical_count": s.CriticalCount(), "first_divergence": s.FirstDivergence, "score_json": string(scoreJSON)}
	if s.BaselineUsable > 0 {
		fields["baseline_divergence"] = s.DBase
	}
	if s.CandidateUsable > 0 {
		fields["candidate_divergence"] = s.DCand
	}
	if s.Scored {
		fields["delta"] = s.Delta
	}
	if err := p.retry(ctx, func() error { return p.store.updateRunSession(ctx, runID, s.SessionID, fields) }); err != nil {
		return failure("store", "write a session score: %v", err)
	}
	return nil
}

// metricsOf averages latency, cost and tokens per replay for each arm, and counts how the cassette answered.
func metricsOf(results []*ReplayResult) storedMetrics {
	var m storedMetrics
	type sums struct {
		n                int
		latency, cost, t float64
	}
	arms := map[Arm]*sums{ArmBaseline: {}, ArmCandidate: {}}
	for _, r := range results {
		if s := arms[r.Arm]; s != nil {
			s.n++
			s.latency += r.Usage.LatencyMS
			s.cost += r.Usage.CostUSD
			s.t += float64(r.Usage.TotalTokens)
		}
		for _, a := range r.Actions {
			for _, c := range a.Calls {
				switch c.Outcome {
				case "":
				case OutcomeHit:
					m.ToolActivity.CassetteHits++
				default:
					m.ToolActivity.StoppedCalls++
				}
			}
		}
	}
	mean := func(s *sums) ArmMetrics {
		if s.n == 0 {
			return ArmMetrics{}
		}
		n := float64(s.n)
		return ArmMetrics{MeanLatencyMS: s.latency / n, MeanCostUSD: s.cost / n, MeanTokens: s.t / n}
	}
	m.Baseline, m.Candidate = mean(arms[ArmBaseline]), mean(arms[ArmCandidate])
	return m
}
