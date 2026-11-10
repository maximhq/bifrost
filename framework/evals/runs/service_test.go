package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// judgeFunc is a stand-in AI evaluator.
type judgeFunc func(ctx context.Context, in evals.Input) (evals.Result, error)

func (f judgeFunc) Evaluate(ctx context.Context, in evals.Input) (evals.Result, error) {
	return f(ctx, in)
}

// harness is a test runs service on a real SQLite store and an in-memory queue, with
// a stub model under test and a stub judge.
type harness struct {
	svc        *Service
	engine     *evals.Engine
	datasets   *datasets.Service
	evaluators *evals.Service
	store      configstore.ConfigStore
	keys       *atomic.Pointer[[]evals.EvalKey]

	mu     sync.Mutex
	model  evals.ModelCaller // the model under test; replaced per test
	judge  judgeFunc
	called []evals.ModelCallRequest
}

func newHarness(t testing.TB) *harness {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "runs.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)

	h := &harness{store: store, keys: &atomic.Pointer[[]evals.EvalKey]{}}
	h.keys.Store(&[]evals.EvalKey{{ID: "k1", Name: "eval key", Provider: "openai", Concurrency: 4}})
	h.model = func(_ context.Context, req evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		last := req.Messages[len(req.Messages)-1]
		return evals.ModelCallResponse{Text: "echo: " + *last.Content.ContentStr, Usage: &evals.Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}, Cost: new(0.001)}, nil
	}
	h.judge = func(_ context.Context, in evals.Input) (evals.Result, error) {
		return evals.Result{Score: evals.BoolScore(strings.Contains(in.Variables["output"], "echo")), Reasoning: "judged", Usage: &evals.Usage{TotalTokens: 5}}, nil
	}

	engine := evals.NewEngine(evals.EngineOptions{})
	evals.RegisterProgrammaticEvaluator(engine, evals.ProgrammaticOptions{})
	engine.Register(evals.EvaluatorTypeAI, "", func(evals.Definition, evals.Config) (evals.Evaluator, error) {
		return judgeFunc(func(ctx context.Context, in evals.Input) (evals.Result, error) {
			h.mu.Lock()
			judge := h.judge
			h.mu.Unlock()
			return judge(ctx, in)
		}), nil
	})

	h.engine = engine
	h.datasets = datasets.NewService(store, nil)
	h.evaluators = evals.NewService(store, engine, nil)
	h.svc = NewService(Options{
		Store: store, Datasets: h.datasets, Evaluators: h.evaluators, Engine: engine,
		Model: func(ctx context.Context, req evals.ModelCallRequest) (evals.ModelCallResponse, error) {
			h.mu.Lock()
			model := h.model
			h.called = append(h.called, req)
			h.mu.Unlock()
			return model(ctx, req)
		},
	})

	q, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	runner := evals.NewRunner(q, engine, func(context.Context) ([]evals.EvalKey, error) { return *h.keys.Load(), nil },
		NewJobStore(evals.NewExecutionJobStore(store), h.svc), evals.RunnerOptions{PollInterval: 2 * time.Millisecond})
	require.NoError(t, runner.Start(ctx))
	h.svc.SetRunner(runner)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(closeCtx)
		_ = q.Close(closeCtx)
	})
	return h
}

func (h *harness) setModel(model evals.ModelCaller) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.model = model
}

// dataset makes a dataset with input, expected_output and output columns and one row
// per triple.
func (h *harness) dataset(t testing.TB, name string, rows ...[3]string) *datasets.DatasetView {
	t.Helper()
	ctx := context.Background()
	view, err := h.datasets.Create(ctx, datasets.DatasetInput{Name: name, Template: "qa", Columns: []datasets.ColumnSpec{{Name: "output", Type: datasets.ColumnOutput}}})
	require.NoError(t, err)
	cells := make([]map[string]string, len(rows))
	for i, row := range rows {
		cells[i] = map[string]string{"input": row[0], "expected_output": row[1], "output": row[2]}
	}
	if len(cells) > 0 {
		_, err = h.datasets.AddRows(ctx, view.ID, cells, "", "tester")
		require.NoError(t, err)
	}
	return view
}

func (h *harness) evaluator(t testing.TB, name string, evaluatorType evals.EvaluatorType, config string) EvaluatorRef {
	t.Helper()
	view, err := h.evaluators.Create(context.Background(), evals.EvaluatorInput{Name: name, Type: evaluatorType, Config: json.RawMessage(config)})
	require.NoError(t, err)
	return EvaluatorRef{ID: view.ID}
}

// exactMatch passes when the output equals the expected output.
func (h *harness) exactMatch(t testing.TB) EvaluatorRef {
	return h.evaluator(t, "exact match", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(output, expected_output):\n    return output == expected_output\n","response_type":"yes_no"}`)
}

func (h *harness) aiJudge(t testing.TB) EvaluatorRef {
	return h.evaluator(t, "judge", evals.EvaluatorTypeAI, `{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"gpt-4o"}`)
}

// await waits until the run reaches one of the statuses and returns it.
func (h *harness) await(t testing.TB, id string, statuses ...string) *RunView {
	t.Helper()
	var view *RunView
	require.Eventually(t, func() bool {
		got, err := h.svc.Get(context.Background(), id)
		if err != nil {
			return false
		}
		view = got
		for _, status := range statuses {
			if got.Status == status {
				return true
			}
		}
		return false
	}, 15*time.Second, 5*time.Millisecond, "run %s did not reach %v", id, statuses)
	return view
}

func (h *harness) entries(t testing.TB, id string, query EntryQuery) *EntriesPage {
	t.Helper()
	page, err := h.svc.Entries(context.Background(), id, query)
	require.NoError(t, err)
	return page
}

func evaluatorSummary(t testing.TB, view *RunView, evaluatorID string) EvaluatorSummary {
	t.Helper()
	require.NotNil(t, view.Summary)
	for _, summary := range view.Summary.Evaluators {
		if summary.EvaluatorID == evaluatorID {
			return summary
		}
	}
	t.Fatalf("no summary for evaluator %s", evaluatorID)
	return EvaluatorSummary{}
}

func TestRunOnDatasetOutputs(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "golden",
		[3]string{"2+2", "4", "4"},
		[3]string{"capital of France", "Paris", "Lyon"},
		[3]string{"no answer yet", "x", ""},
	)
	exact := h.exactMatch(t)

	_, err := h.svc.Create(ctx, RunInput{Name: " ", Target: Target{Type: TargetDataset}, DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}})
	require.True(t, evals.IsInvalid(err), "a run needs a name")
	_, err = h.svc.Create(ctx, RunInput{Name: "x", Target: Target{Type: TargetDataset}, DatasetID: dataset.ID})
	require.True(t, evals.IsInvalid(err), "a run needs an evaluator")
	_, err = h.svc.Create(ctx, RunInput{Name: "x", Target: Target{Type: "nope"}, DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}})
	require.True(t, evals.IsInvalid(err))

	created, err := h.svc.Create(ctx, RunInput{Name: "baseline", Tags: []string{"nightly"}, Target: Target{Type: TargetDataset}, DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, CreatedBy: "tester"})
	require.NoError(t, err)
	assert.Equal(t, 1, created.DatasetVersion, "starting a run saves the dataset's draft as a version and pins it")

	// Later edits to the dataset do not reach the run: it reads version 1.
	_, err = h.datasets.AddRows(ctx, dataset.ID, []map[string]string{{"input": "late", "expected_output": "y", "output": "y"}}, "", "tester")
	require.NoError(t, err)

	run := h.await(t, created.ID, tables.EvalRunCompleted)
	assert.EqualValues(t, 3, run.Total)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 2, tables.EvalRunEntryFailed: 1}, run.Counts)
	assert.EqualValues(t, 1, run.Passed)
	require.NotNil(t, run.EndedAt)

	summary := evaluatorSummary(t, run, exact.ID)
	assert.Equal(t, "exact match", summary.Name)
	assert.Equal(t, 2, summary.Scored)
	assert.Equal(t, 1, summary.Passed)
	assert.InDelta(t, 50, summary.PassRate, 1e-9)
	require.NotNil(t, summary.Mean)
	assert.InDelta(t, 0.5, *summary.Mean, 1e-9)

	page := h.entries(t, run.ID, EntryQuery{})
	require.Len(t, page.Entries, 3)
	assert.EqualValues(t, 3, page.Total)
	first := page.Entries[0]
	assert.Equal(t, "4", first.Output)
	assert.Equal(t, "2+2", first.Variables["input"])
	require.Len(t, first.Results, 1)
	assert.Equal(t, tables.EvalRunResultCompleted, first.Results[0].Status)
	require.NotNil(t, first.Passed)
	assert.True(t, *first.Passed)
	assert.Equal(t, tables.EvalRunEntryFailed, page.Entries[2].Status, "a row with no output cannot be scored")
	assert.NotEmpty(t, page.Entries[2].Error)

	failed := h.entries(t, run.ID, EntryQuery{Passed: new(false)})
	require.Len(t, failed.Entries, 1)
	assert.Equal(t, "Lyon", failed.Entries[0].Output)
	one, err := h.svc.Entry(ctx, run.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, one.ID)

	// The pinned version is protected while the run exists.
	_, err = h.datasets.SaveVersion(ctx, dataset.ID, "", "", "tester")
	require.NoError(t, err)
	require.ErrorIs(t, h.datasets.DeleteVersion(ctx, dataset.ID, 1), configstore.ErrEvalDatasetVersionInUse)

	list, total, err := h.svc.List(ctx, configstore.EvalRunListParams{Tag: "nightly"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, run.ID, list[0].ID)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 2, tables.EvalRunEntryFailed: 1}, list[0].Counts)

	renamed, err := h.svc.Update(ctx, run.ID, UpdateInput{Name: "baseline v1", Tags: []string{"kept"}})
	require.NoError(t, err)
	assert.Equal(t, "baseline v1", renamed.Name)

	var csv bytes.Buffer
	require.NoError(t, h.svc.Export(ctx, run.ID, &csv))
	lines := strings.Split(strings.TrimSpace(csv.String()), "\n")
	require.Len(t, lines, 4)
	assert.Contains(t, lines[0], "exact match: score")
	assert.Contains(t, lines[1], "2+2")

	require.NoError(t, h.svc.Delete(ctx, run.ID))
	_, err = h.svc.Get(ctx, run.ID)
	require.ErrorIs(t, err, configstore.ErrNotFound)
	require.NoError(t, h.datasets.DeleteVersion(ctx, dataset.ID, 1))
}

func TestRunOnAModel(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "questions", [3]string{"hello", "echo: hello", ""}, [3]string{"bye", "nope", ""})
	exact, judge := h.exactMatch(t), h.aiJudge(t)
	// Reads tool_calls, which a run always provides: "[]" when the model made none.
	noTools := h.evaluator(t, "no tools", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(tool_calls):\n    return tool_calls == \"[]\"\n","response_type":"yes_no"}`)

	var keysSeen sync.Map
	h.setModel(func(ctx context.Context, req evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		keysSeen.Store(evals.KeyIDFromContext(ctx), true)
		last := req.Messages[len(req.Messages)-1]
		return evals.ModelCallResponse{Text: "echo: " + *last.Content.ContentStr, Usage: &evals.Usage{TotalTokens: 10}, Cost: new(0.001)}, nil
	})

	created, err := h.svc.Create(ctx, RunInput{
		Name: "model run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact, judge, noTools},
		Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o", System: "be brief"},
	})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)

	_, onEvalKey := keysSeen.Load("k1")
	assert.True(t, onEvalKey, "the model under test is called on the eval key")
	h.mu.Lock()
	require.NotEmpty(t, h.called)
	request := h.called[0]
	h.mu.Unlock()
	assert.Equal(t, "gpt-4o", request.Model)
	require.Len(t, request.Messages, 2)
	assert.Equal(t, schemas.ChatMessageRoleSystem, request.Messages[0].Role)
	assert.Equal(t, "be brief", *request.Messages[0].Content.ContentStr)

	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 2}, run.Counts)
	assert.EqualValues(t, 1, run.Passed, "one row matches its expected output")
	assert.Equal(t, 1, evaluatorSummary(t, run, exact.ID).Passed)
	assert.Equal(t, 2, evaluatorSummary(t, run, judge.ID).Passed)
	assert.Equal(t, 2, evaluatorSummary(t, run, noTools.ID).Passed)
	require.NotNil(t, run.Summary.Latency)
	assert.Equal(t, 2, run.Summary.Latency.Count)
	assert.EqualValues(t, 20, run.Summary.Totals.TargetTokens)
	assert.InDelta(t, 0.002, run.Summary.Totals.TargetCost, 1e-9)
	assert.EqualValues(t, 10, run.Summary.Totals.EvaluatorTokens)

	entry := h.entries(t, run.ID, EntryQuery{Search: "hello"}).Entries[0]
	assert.Equal(t, "echo: hello", entry.Output)
	assert.EqualValues(t, 10, entry.TargetTotalTokens)
	assert.Len(t, entry.Results, 3)

	// A run with a model target needs an eval key for the model's provider.
	_, err = h.svc.Create(ctx, RunInput{
		Name: "no key", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact},
		Target: Target{Type: TargetModel, Provider: "anthropic", Model: "claude"},
	})
	require.True(t, evals.IsInvalid(err))
	assert.Contains(t, err.Error(), "anthropic")

	// An evaluator that needs a variable the run cannot provide is reported up front.
	needsContext := h.evaluator(t, "grounded", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(output, retrieved_docs):\n    return True\n","response_type":"yes_no"}`)
	check, err := h.svc.Validate(ctx, RunInput{
		Name: "check", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{needsContext},
		Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"},
	})
	require.NoError(t, err)
	require.Len(t, check.Problems, 1)
	assert.Contains(t, check.Problems[0], "retrieved_docs")
}

func TestRunStopRerunAndAddEvaluators(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "flaky", [3]string{"a", "echo: a", ""}, [3]string{"b", "echo: b", ""}, [3]string{"c", "echo: c", ""})
	exact := h.exactMatch(t)

	// The model fails on "b" until it is told to recover.
	var recovered atomic.Bool
	h.setModel(func(_ context.Context, req evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		input := *req.Messages[len(req.Messages)-1].Content.ContentStr
		if input == "b" && !recovered.Load() {
			return evals.ModelCallResponse{}, errors.New("upstream timed out")
		}
		return evals.ModelCallResponse{Text: "echo: " + input}, nil
	})
	target := Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}
	created, err := h.svc.Create(ctx, RunInput{Name: "flaky run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: target})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 2, tables.EvalRunEntryFailed: 1}, run.Counts)
	failed := h.entries(t, run.ID, EntryQuery{Statuses: []string{tables.EvalRunEntryFailed}}).Entries
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error, "upstream timed out")

	// Rerunning the failed rows tries only those, on the same pinned dataset version.
	recovered.Store(true)
	_, err = h.svc.Rerun(ctx, run.ID, RerunInput{Scope: "nope"})
	require.True(t, evals.IsInvalid(err))
	rerun, err := h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunFailed})
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, rerun.Status)
	run = h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 3}, run.Counts)
	assert.EqualValues(t, 3, run.Passed)
	again := h.entries(t, run.ID, EntryQuery{Search: "\"b\""}).Entries
	require.Len(t, again, 1)
	assert.Equal(t, 2, again[0].Attempt)

	// Adding an evaluator scores the stored outputs. The model is not called again.
	h.mu.Lock()
	callsBefore := len(h.called)
	h.mu.Unlock()
	judge := h.aiJudge(t)
	_, err = h.svc.AddEvaluators(ctx, run.ID, []EvaluatorRef{exact})
	require.True(t, evals.IsInvalid(err), "an evaluator the run already has is refused")
	_, err = h.svc.AddEvaluators(ctx, run.ID, []EvaluatorRef{judge})
	require.NoError(t, err)
	run = h.await(t, run.ID, tables.EvalRunCompleted)
	h.mu.Lock()
	assert.Len(t, h.called, callsBefore)
	h.mu.Unlock()
	assert.Equal(t, 3, evaluatorSummary(t, run, judge.ID).Passed)
	assert.Len(t, h.entries(t, run.ID, EntryQuery{}).Entries[0].Results, 2)

	// Rerunning one row leaves the others alone.
	one := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	_, err = h.svc.Rerun(ctx, run.ID, RerunInput{EntryIDs: []string{one.ID}})
	require.NoError(t, err)
	run = h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, 2, h.entries(t, run.ID, EntryQuery{}).Entries[0].Attempt)
	assert.Equal(t, 1, h.entries(t, run.ID, EntryQuery{}).Entries[2].Attempt)

	// A stop lets the call in flight finish and stops everything still waiting.
	h.keys.Store(&[]evals.EvalKey{{ID: "k1", Provider: "openai", Concurrency: 1}})
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	h.setModel(func(_ context.Context, req evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		started <- struct{}{}
		<-release
		return evals.ModelCallResponse{Text: "late"}, nil
	})
	slow, err := h.svc.Create(ctx, RunInput{Name: "slow run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: target})
	require.NoError(t, err)
	<-started
	stopped, err := h.svc.Stop(ctx, slow.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, stopped.Status)
	close(release)
	require.Eventually(t, func() bool {
		view, err := h.svc.Get(ctx, slow.ID)
		return err == nil && view.Counts[tables.EvalRunEntryStopped] == 3 && view.EndedAt != nil
	}, 10*time.Second, 5*time.Millisecond, "every entry of a stopped run ends stopped")
	final, err := h.svc.Get(ctx, slow.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, final.Status)
	_, err = h.svc.Stop(ctx, slow.ID)
	require.True(t, evals.IsInvalid(err), "a run that is not active cannot be stopped")
}

func TestRunPromptAndHumanEvaluator(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "prompted", [3]string{"refunds", "x", ""})
	human := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	exact := h.exactMatch(t)

	prompt := &tables.TablePrompt{ID: "prompt-1", Name: "support"}
	require.NoError(t, h.store.CreatePrompt(ctx, prompt))
	version := &tables.TablePromptVersion{
		PromptID: prompt.ID, CommitMessage: "v1", Provider: "openai", Model: "gpt-4o-mini", ModelParams: tables.ModelParams{"temperature": 0.2},
		Messages: []tables.TablePromptVersionMessage{
			{PromptID: prompt.ID, OrderIndex: 0, Message: json.RawMessage(`{"role":"system","content":"You answer questions about {{input}}."}`)},
			{PromptID: prompt.ID, OrderIndex: 1, Message: json.RawMessage(`{"role":"user","content":"Tell me about {{input}}"}`)},
		},
	}
	require.NoError(t, h.store.CreatePromptVersion(ctx, version))

	created, err := h.svc.Create(ctx, RunInput{
		Name: "prompt run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact, human},
		Target: Target{Type: TargetPrompt, PromptID: prompt.ID},
	})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)

	h.mu.Lock()
	request := h.called[0]
	h.mu.Unlock()
	assert.Equal(t, "gpt-4o-mini", request.Model)
	assert.EqualValues(t, 0.2, request.Params["temperature"])
	require.Len(t, request.Messages, 2, "the prompt uses {{input}}, so the input is not added again")
	assert.Equal(t, "You answer questions about refunds.", *request.Messages[0].Content.ContentStr)
	assert.Equal(t, "Tell me about refunds", *request.Messages[1].Content.ContentStr)

	// The run keeps a copy of the prompt: it does not follow later versions.
	var target Target
	require.NoError(t, json.Unmarshal(run.Target, &target))
	assert.Equal(t, 1, target.PromptVersion)
	assert.Len(t, target.Messages, 2)

	// A human evaluator's result waits for a reviewer, without holding the run open.
	entry := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	var waiting *tables.TableEvalRunResult
	for i := range entry.Results {
		if entry.Results[i].EvaluatorID == human.ID {
			waiting = &entry.Results[i]
		}
	}
	require.NotNil(t, waiting)
	assert.Equal(t, tables.EvalRunResultAwaitingReview, waiting.Status)
	assert.Zero(t, evaluatorSummary(t, run, human.ID).Scored)

	_, err = h.svc.Review(ctx, run.ID, entry.ID, exact.ID, ReviewInput{Score: true}, "reviewer")
	require.True(t, evals.IsInvalid(err), "only a human evaluator's result is scored by hand")
	_, err = h.svc.Review(ctx, run.ID, entry.ID, human.ID, ReviewInput{Score: "maybe"}, "reviewer")
	require.True(t, evals.IsInvalid(err), "a yes or no evaluator takes true or false")
	reviewed, err := h.svc.Review(ctx, run.ID, entry.ID, human.ID, ReviewInput{Score: true, Reasoning: "polite"}, "reviewer")
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultCompleted, reviewed.Status)
	assert.Equal(t, "reviewer", reviewed.ReviewedBy)
	require.NotNil(t, reviewed.Pass)
	assert.True(t, *reviewed.Pass)
	run, err = h.svc.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, evaluatorSummary(t, run, human.ID).Passed, "the summary follows the review")
}

func TestRunOnAnHTTPEndpointAndCompare(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "compare", [3]string{"a", "echo: a", "echo: a"}, [3]string{"b", "echo: b", "wrong"})
	exact := h.exactMatch(t)

	_, err := h.svc.Create(ctx, RunInput{Name: "bad http", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetHTTP, HTTP: json.RawMessage(`{"method":"POST"}`)}})
	require.True(t, evals.IsInvalid(err), "an HTTP target needs a URL and an output path")

	onDataset, err := h.svc.Create(ctx, RunInput{Name: "from dataset", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	onModel, err := h.svc.Create(ctx, RunInput{Name: "from model", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	h.await(t, onDataset.ID, tables.EvalRunCompleted)
	h.await(t, onModel.ID, tables.EvalRunCompleted)

	_, err = h.svc.Compare(ctx, []string{onDataset.ID}, 0, 10)
	require.True(t, evals.IsInvalid(err), "a comparison needs at least two runs")
	comparison, err := h.svc.Compare(ctx, []string{onDataset.ID, onModel.ID}, 0, 10)
	require.NoError(t, err)
	require.Len(t, comparison.Runs, 2)
	assert.EqualValues(t, 2, comparison.Total)
	require.Len(t, comparison.Rows, 2)
	second := comparison.Rows[1]
	assert.Equal(t, "b", second.Variables["input"])
	require.Len(t, second.Entries, 2)
	require.NotNil(t, second.Entries[0])
	require.NotNil(t, second.Entries[1])
	assert.Equal(t, "wrong", second.Entries[0].Output)
	assert.Equal(t, "echo: b", second.Entries[1].Output)
	assert.False(t, *second.Entries[0].Passed)
	assert.True(t, *second.Entries[1].Passed)

	// Against the first run, the model run scored one row higher and none lower.
	change := comparison.Changes[onModel.ID][exact.ID]
	assert.Equal(t, ScoreChange{Improved: 1, Regressed: 0, Compared: 2}, change)
	_, baselineHasChanges := comparison.Changes[onDataset.ID]
	assert.False(t, baselineHasChanges, "the baseline is not compared with itself")

	other := h.dataset(t, "other", [3]string{"z", "z", "z"})
	onOther, err := h.svc.Create(ctx, RunInput{Name: "elsewhere", DatasetID: other.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, onOther.ID, tables.EvalRunCompleted)
	_, err = h.svc.Compare(ctx, []string{onDataset.ID, onOther.ID}, 0, 10)
	require.True(t, evals.IsInvalid(err), "runs on different datasets cannot be compared")
}

// An HTTP target's concurrency is the most calls in flight to the endpoint. The
// target's request is also built once for the run, not once for every row.
func TestHTTPTargetHonorsItsConcurrency(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var inFlight, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = w.Write([]byte(`{"answer":"same"}`))
	}))
	t.Cleanup(server.Close)
	evals.RegisterAPIEvaluator(h.engine, evals.APIOptions{Client: server.Client()})

	rows := make([][3]string, 8)
	for i := range rows {
		rows[i] = [3]string{fmt.Sprintf("q%d", i), "same", ""}
	}
	dataset := h.dataset(t, "http", rows...)
	target := Target{Type: TargetHTTP, HTTP: json.RawMessage(fmt.Sprintf(`{"method":"POST","url":%q,"payload":"{\"q\":\"{{input}}\"}","output_mapping":{"score":"answer"}}`, server.URL))}
	created, err := h.svc.Create(ctx, RunInput{Name: "http run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: target, TargetConcurrency: 2})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted, tables.EvalRunFailed)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 8}, run.Counts)
	assert.EqualValues(t, 8, run.Passed)
	assert.LessOrEqual(t, peak.Load(), int32(2), "no more than the target's concurrency ran at once")
}

// A gateway can die between storing an entry's output and creating its results, or
// between creating the results and queueing them. The entry's message is delivered
// again, and that delivery must finish the entry instead of dropping it.
func TestEntryLeftEvaluatingIsRecovered(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "crash", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	exact := h.exactMatch(t)
	created, err := h.svc.Create(ctx, RunInput{Name: "crash run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entries := h.entries(t, run.ID, EntryQuery{}).Entries
	require.Len(t, entries, 2)

	// Put the run back to where a crash would leave it: both entries evaluating, the
	// first with no results at all, the second with a result that was never queued.
	_, err = h.store.DeleteEvalRunResults(ctx, run.ID, nil, "")
	require.NoError(t, err)
	require.NoError(t, h.store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[1].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultQueued},
	}))
	_, err = h.store.TransitionEvalRunEntries(ctx, run.ID, nil, nil, map[string]any{"status": tables.EvalRunEntryEvaluating, "passed": nil, "ended_at": nil})
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	for _, entry := range entries {
		job, err := jobs.LoadJob(ctx, targetJobID(entry.ID, entry.Attempt))
		require.NoError(t, err)
		require.False(t, job.Done, "the redelivered message still has work to do")
		job.Exec(ctx)
		if job.Complete != nil {
			require.NoError(t, job.Complete(ctx, evals.Result{Score: evals.NAScore()}))
		}
	}
	run = h.await(t, run.ID, tables.EvalRunCompleted)
	assert.EqualValues(t, 2, run.Passed)
	for _, entry := range h.entries(t, run.ID, EntryQuery{}).Entries {
		require.Len(t, entry.Results, 1)
		assert.Equal(t, tables.EvalRunResultCompleted, entry.Results[0].Status)
	}
}

// When the last eval key of a provider goes away, the target calls still waiting on
// it cannot run. Their entries fail with the reason, and the run ends.
func TestWaitingTargetsFailWhenNoEvalKeyRemains(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	// One call at a time, so two entries are still waiting when the key goes away.
	h.keys.Store(&[]evals.EvalKey{{ID: "k1", Provider: "openai", Concurrency: 1}})
	require.NoError(t, h.svc.runner.(*evals.Runner).Sync(ctx))
	dataset := h.dataset(t, "orphans", [3]string{"a", "echo: a", ""}, [3]string{"b", "echo: b", ""}, [3]string{"c", "echo: c", ""})
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	h.setModel(func(_ context.Context, req evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		started <- struct{}{}
		<-release
		return evals.ModelCallResponse{Text: "echo: " + *req.Messages[len(req.Messages)-1].Content.ContentStr}, nil
	})
	created, err := h.svc.Create(ctx, RunInput{Name: "orphan run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	<-started
	h.keys.Store(&[]evals.EvalKey{})
	close(release)

	run := h.await(t, created.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 1, tables.EvalRunEntryFailed: 2}, run.Counts)
	failed := h.entries(t, run.ID, EntryQuery{Statuses: []string{tables.EvalRunEntryFailed}}).Entries
	require.Len(t, failed, 2)
	assert.Contains(t, failed[0].Error, "enabled for evaluations")
}

// The sweep looks for entries stranded on a key nothing consumes. A large run has
// hundreds of entries waiting their turn on a healthy key, and they must not fill the
// sweep's page and hide the stranded ones behind them.
func TestSweepFindsStrandedEntriesBehindWaitingOnes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run := &tables.TableEvalRun{
		Name: "big", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: "ds", DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"model","provider":"openai","model":"gpt-4o"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	old := time.Now().Add(-time.Hour)
	entries := make([]tables.TableEvalRunEntry, 30)
	for i := range entries {
		entries[i] = tables.TableEvalRunEntry{RunID: run.ID, Seq: int64(i + 1), DatasetRowID: fmt.Sprintf("row-%d", i), Status: tables.EvalRunEntryQueued, KeyID: "k1", QueuedAt: old}
	}
	entries[29].KeyID = "key-gone"
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	stale, err := jobs.StaleJobs(ctx, time.Now(), []string{"k1"}, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "only the entry on the key nothing consumes")
	assert.Equal(t, targetJobID(entries[29].ID, 1), stale[0].ID)
	assert.Equal(t, "key-gone", stale[0].KeyID)
	require.NotNil(t, stale[0].Model)
	assert.Equal(t, "openai", stale[0].Model.Provider)
}

// A stop can land while a run is still being planned. Planning then stops, and must
// leave nothing behind: the entries it had just stored are stopped, the run ends, and
// the run counts as planned, so a later rerun can finish.
func TestStopDuringPlanningLeavesNothingWaiting(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	rows := make([][3]string, planPageSize+20)
	for i := range rows {
		rows[i] = [3]string{fmt.Sprintf("q%d", i), "same", "same"}
	}
	dataset := h.dataset(t, "stopped early", rows...)
	// Adding this many rows saved a version already.
	saved, err := h.datasets.Get(ctx, dataset.ID)
	require.NoError(t, err)
	require.Positive(t, saved.HeadVersion)
	exact := h.exactMatch(t)
	definition, err := h.evaluators.Definition(ctx, exact.ID, 0)
	require.NoError(t, err)
	evaluatorsJSON, err := json.Marshal([]evals.Definition{definition})
	require.NoError(t, err)
	// The run as Stop leaves it when planning has not stored anything yet.
	run := &tables.TableEvalRun{
		Name: "stopped early", Status: tables.EvalRunStopped, TargetType: string(TargetDataset), DatasetID: dataset.ID, DatasetVersion: saved.HeadVersion,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: evaluatorsJSON,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))

	require.NoError(t, h.svc.plan(ctx, run.ID))

	view, err := h.svc.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Zero(t, view.Counts[tables.EvalRunEntryQueued], "no entry is left waiting on a stopped run")
	assert.EqualValues(t, planPageSize, view.Counts[tables.EvalRunEntryStopped])
	assert.NotNil(t, view.EndedAt, "the stopped run has ended")
	assert.NotNil(t, view.StartedAt, "and counts as planned, so a rerun of it can finish")

	_, err = h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)
	view = h.await(t, run.ID, tables.EvalRunCompleted)
	assert.EqualValues(t, planPageSize, view.Counts[tables.EvalRunEntryCompleted])
}

// A worker can die while an entry's target is being called. If the entry's key is
// then unmarked, the runner cannot move the entry and ends its job with an error. The
// entry fails with the reason, although it had started.
func TestStartedTargetFailsWhenItsKeyIsGone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "gone", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "gone run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entry := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	_, err = h.store.DeleteEvalRunResults(ctx, run.ID, nil, "")
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRunEntry(ctx, entry.ID, nil, map[string]any{"status": tables.EvalRunEntryRunning, "passed": nil, "ended_at": nil})
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	loaded, err := jobs.LoadJob(ctx, targetJobID(entry.ID, entry.Attempt))
	require.NoError(t, err)
	assert.True(t, loaded.Started, "the store says the job had started")
	require.NoError(t, jobs.CompleteJob(ctx, targetJobID(entry.ID, entry.Attempt), evals.Result{Score: evals.ErrorScore(), Reasoning: "the key is gone"}))

	view := h.await(t, run.ID, tables.EvalRunFailed)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryFailed: 1}, view.Counts)
	assert.Equal(t, "the key is gone", h.entries(t, run.ID, EntryQuery{}).Entries[0].Error)
}

// The in-memory queue forgets its messages on a restart, while a run's entries and
// results stay stored where they were. The job store lists a job for every active run
// that puts all of it back: waiting entries, entries whose target was being called,
// and results that were waiting or being scored.
func TestActiveRunIsResumedOnAQueueThatForgets(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "restart", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"}, [3]string{"c", "same", "same"}, [3]string{"d", "same", "same"})
	exact := h.exactMatch(t)
	created, err := h.svc.Create(ctx, RunInput{Name: "restart run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entries := h.entries(t, run.ID, EntryQuery{}).Entries
	require.Len(t, entries, 4)

	// Put the run where a restart would find it, with nothing in the queue for it:
	// one entry waiting, one with its target in flight, one whose result was waiting,
	// one whose result was being scored.
	_, err = h.store.DeleteEvalRunResults(ctx, run.ID, nil, "")
	require.NoError(t, err)
	// This test's queue did not restart, and still holds the messages of the first
	// pass. A queue keeps the first message of an ID, so the attempts are moved on to
	// numbers it has not seen, as every ID is to a queue that starts empty.
	set := func(entryID, status string) {
		_, err := h.store.TransitionEvalRunEntry(ctx, entryID, nil, map[string]any{"status": status, "attempt": 7, "passed": nil, "ended_at": nil})
		require.NoError(t, err)
	}
	set(entries[0].ID, tables.EvalRunEntryQueued)
	set(entries[1].ID, tables.EvalRunEntryRunning)
	set(entries[2].ID, tables.EvalRunEntryEvaluating)
	set(entries[3].ID, tables.EvalRunEntryEvaluating)
	require.NoError(t, h.store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[2].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultQueued, Attempt: 7},
		{RunID: run.ID, EntryID: entries[3].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultRunning, Attempt: 7},
	}))
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	unfinished, err := jobs.UnfinishedJobs(ctx)
	require.NoError(t, err)
	require.Len(t, unfinished, 1, "one job for the one active run; finished runs have none")
	require.NoError(t, h.svc.runner.Submit(ctx, unfinished[0]))

	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 4}, view.Counts)
	assert.EqualValues(t, 4, view.Passed)
}

func TestSummaryLatency(t *testing.T) {
	assert.Nil(t, latencySummary(nil))
	latencies := make([]int64, 100)
	for i := range latencies {
		latencies[i] = int64(100 - i) // 100 down to 1, unsorted on purpose
	}
	got := latencySummary(latencies)
	require.NotNil(t, got)
	assert.Equal(t, 100, got.Count)
	assert.EqualValues(t, 1, got.Min)
	assert.EqualValues(t, 100, got.Max)
	assert.InDelta(t, 50.5, got.Mean, 1e-9)
	assert.EqualValues(t, 50, got.P50)
	assert.EqualValues(t, 90, got.P90)
	assert.EqualValues(t, 95, got.P95)
	assert.EqualValues(t, 99, got.P99)
	assert.EqualValues(t, 7, latencySummary([]int64{7}).P99)
}

func TestRenderTemplate(t *testing.T) {
	variables := map[string]string{"input": "refunds", "user.name": "Ada"}
	assert.Equal(t, "About refunds for Ada.", renderTemplate("About {{input}} for {{ user.name }}.", variables))
	assert.Equal(t, "Missing: .", renderTemplate("Missing: {{nope}}.", variables))
	assert.Equal(t, "No braces", renderTemplate("No braces", variables))
	assert.True(t, usesVariable([]string{"a {{ input }} b"}, "input"))
	assert.False(t, usesVariable([]string{"a {{inputs}} b"}, "input"))
}

func BenchmarkRunEntries(b *testing.B) {
	h := newHarness(b)
	rows := make([][3]string, 500)
	for i := range rows {
		rows[i] = [3]string{fmt.Sprintf("question %d", i), "same", "same"}
	}
	dataset := h.dataset(b, "bench", rows...)
	exact := h.exactMatch(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		created, err := h.svc.Create(context.Background(), RunInput{Name: fmt.Sprintf("bench %d", i), DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
		require.NoError(b, err)
		h.await(b, created.ID, tables.EvalRunCompleted)
	}
	b.ReportMetric(float64(len(rows)), "entries/op")
}

// A run's report can be read against a baseline run on the same dataset: each entry
// carries the baseline's entry for the same dataset row, and the entries can be
// narrowed to the rows that scored lower or higher than the baseline.
func TestEntriesAgainstABaseline(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "moved", [3]string{"a", "echo: a", "echo: a"}, [3]string{"b", "echo: b", "wrong"}, [3]string{"c", "wrong", "wrong"})
	exact := h.exactMatch(t)
	baseline, err := h.svc.Create(ctx, RunInput{Name: "baseline", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	candidate, err := h.svc.Create(ctx, RunInput{Name: "candidate", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	h.await(t, baseline.ID, tables.EvalRunCompleted)
	h.await(t, candidate.ID, tables.EvalRunCompleted)

	// The baseline scores the stored outputs: a and c match what is expected, b does
	// not. The candidate's model echoes the input: a and b match, c does not. So b got
	// better and c got worse.
	page := h.entries(t, candidate.ID, EntryQuery{Baseline: baseline.ID})
	require.Len(t, page.Entries, 3)
	for _, entry := range page.Entries {
		require.NotNil(t, entry.Baseline, "every row has the baseline's entry")
		assert.Equal(t, entry.DatasetRowID, entry.Baseline.DatasetRowID)
		require.Len(t, entry.Baseline.Results, 1)
	}
	assert.Equal(t, "wrong", page.Entries[1].Baseline.Output)
	assert.Equal(t, "echo: b", page.Entries[1].Output)

	better := h.entries(t, candidate.ID, EntryQuery{Baseline: baseline.ID, Change: configstore.EvalRunChangeBetter})
	require.Len(t, better.Entries, 1)
	assert.EqualValues(t, 1, better.Total)
	assert.Equal(t, "b", better.Entries[0].Variables["input"])
	worse := h.entries(t, candidate.ID, EntryQuery{Baseline: baseline.ID, Change: configstore.EvalRunChangeWorse})
	require.Len(t, worse.Entries, 1)
	assert.Equal(t, "c", worse.Entries[0].Variables["input"], "the baseline's stored output matched; the model's did not")

	// Without a baseline nothing is attached.
	assert.Nil(t, h.entries(t, candidate.ID, EntryQuery{}).Entries[0].Baseline)

	_, err = h.svc.Entries(ctx, candidate.ID, EntryQuery{Change: configstore.EvalRunChangeWorse})
	require.True(t, evals.IsInvalid(err), "a change is measured against a baseline")
	_, err = h.svc.Entries(ctx, candidate.ID, EntryQuery{Baseline: baseline.ID, Change: "sideways"})
	require.True(t, evals.IsInvalid(err))
	_, err = h.svc.Entries(ctx, candidate.ID, EntryQuery{Baseline: candidate.ID})
	require.True(t, evals.IsInvalid(err), "a run is not its own baseline")
	other := h.dataset(t, "elsewhere", [3]string{"z", "z", "z"})
	onOther, err := h.svc.Create(ctx, RunInput{Name: "elsewhere", DatasetID: other.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, onOther.ID, tables.EvalRunCompleted)
	_, err = h.svc.Entries(ctx, candidate.ID, EntryQuery{Baseline: onOther.ID})
	require.True(t, evals.IsInvalid(err), "the baseline reads the same dataset")
}
