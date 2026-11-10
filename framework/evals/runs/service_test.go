package runs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
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
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	queue      queue.Queue // the runner's queue

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
	evals.RegisterStatisticalEvaluators(engine, func(context.Context, string, string, []string) (evals.EmbeddingResponse, error) {
		return evals.EmbeddingResponse{}, errors.New("no embeddings in these tests")
	})
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
	h.queue = q
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
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	slow, err := h.svc.Create(ctx, RunInput{Name: "slow run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: target})
	require.NoError(t, err)
	waitFor(t, started, "the target call to start")
	stopped, err := h.svc.Stop(ctx, slow.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, stopped.Status)
	releaseOnce.Do(func() { close(release) })
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
	}, 0))
	_, err = h.store.TransitionEvalRunEntries(ctx, run.ID, nil, nil, map[string]any{"status": tables.EvalRunEntryEvaluating, "passed": nil, "ended_at": nil})
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	for _, entry := range entries {
		job, err := jobs.LoadJob(ctx, targetJobID(entry.ID, entry.Attempt))
		require.NoError(t, err)
		require.False(t, job.Done, "the redelivered message still has work to do")
		// The runner claims a job before it runs it, as Runner.handler does.
		claimed, err := jobs.StartJob(ctx, job.ID, job.KeyID, evals.JobClaim{DeliveryID: "redelivery"})
		require.NoError(t, err)
		require.True(t, claimed, "the redelivery of an entry left evaluating is claimed")
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
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	created, err := h.svc.Create(ctx, RunInput{Name: "orphan run", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	waitFor(t, started, "the target call to start")
	h.keys.Store(&[]evals.EvalKey{})
	releaseOnce.Do(func() { close(release) })

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
	stale, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, []string{"k1"}, 10)
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
		// The claim of the first pass is cleared: this runner is not on a forgetting queue,
		// so it cannot take over by start time, and a row with no claim is anyone's.
		_, err := h.store.TransitionEvalRunEntry(ctx, entryID, nil, map[string]any{"status": status, "attempt": 7, "claim_id": "", "passed": nil, "ended_at": nil})
		require.NoError(t, err)
	}
	set(entries[0].ID, tables.EvalRunEntryQueued)
	set(entries[1].ID, tables.EvalRunEntryRunning)
	set(entries[2].ID, tables.EvalRunEntryEvaluating)
	set(entries[3].ID, tables.EvalRunEntryEvaluating)
	require.NoError(t, h.store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[2].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultQueued, Attempt: 7},
		{RunID: run.ID, EntryID: entries[3].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultRunning, Attempt: 7},
	}, 0))
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	unfinished, _, err := jobs.UnfinishedJobs(ctx, evals.JobCursor{}, 0)
	require.NoError(t, err)
	require.Len(t, unfinished, 1, "one job for the one active run; finished runs have none")
	require.NoError(t, h.svc.runner.Submit(ctx, unfinished[0]))

	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 4}, view.Counts)
	assert.EqualValues(t, 4, view.Passed)
}

// walkUnfinished follows the cursor of UnfinishedJobs until it is the zero cursor, as
// Runner.resume does, and fails the test when the walk takes more than maxCalls pages.
func walkUnfinished(t *testing.T, jobs evals.JobResumer, limit, maxCalls int) []*evals.Job {
	t.Helper()
	var all []*evals.Job
	var after evals.JobCursor
	for range maxCalls {
		page, next, err := jobs.UnfinishedJobs(context.Background(), after, limit)
		require.NoError(t, err)
		all = append(all, page...)
		if next == (evals.JobCursor{}) {
			return all
		}
		after = next
	}
	t.Fatalf("the walk of unfinished jobs did not end within %d pages", maxCalls)
	return nil
}

// The walk of unfinished jobs ends on the zero cursor, so the runner's startup recovery,
// which stops only there, ends too: on an empty store, and with an active run, whose
// resume job is listed once.
func TestUnfinishedJobsWalkEnds(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	page, next, err := jobs.UnfinishedJobs(ctx, evals.JobCursor{}, 10)
	require.NoError(t, err)
	assert.Empty(t, page)
	assert.Equal(t, evals.JobCursor{}, next, "an empty store has one page, the last")

	run, _ := h.manualRun(t, `{"type":"dataset"}`, nil, tables.EvalRunEntryQueued)
	all := walkUnfinished(t, jobs, 10, 5)
	require.Len(t, all, 1)
	got, ok := runOfJob(all[0].ID, resumeJobPrefix)
	require.True(t, ok)
	assert.Equal(t, run.ID, got)
}

// scriptedResumer answers UnfinishedJobs with fixed pages, keyed by the cursor asked for.
type scriptedResumer struct {
	evals.JobStore
	pages map[string]scriptedPage
}

type scriptedPage struct {
	jobs []*evals.Job
	next evals.JobCursor
}

func (s scriptedResumer) UnfinishedJobs(_ context.Context, after evals.JobCursor, _ int) ([]*evals.Job, evals.JobCursor, error) {
	page := s.pages[after.ID]
	return page.jobs, page.next, nil
}

// A page of the wrapped store can be short without being the last: the store leaves out
// a row it cannot decode. The wrapper follows the wrapped store's cursor, not the page
// length, and lists the run jobs only after the wrapped store's last page.
func TestUnfinishedJobsFollowTheWrappedCursor(t *testing.T) {
	h := newHarness(t)
	run, _ := h.manualRun(t, `{"type":"dataset"}`, nil, tables.EvalRunEntryQueued)
	inner := scriptedResumer{JobStore: evals.NewExecutionJobStore(h.store), pages: map[string]scriptedPage{
		"":   {jobs: []*evals.Job{{ID: "exec-1"}}, next: evals.JobCursor{CreatedAt: time.Now(), ID: "p2"}},
		"p2": {jobs: []*evals.Job{{ID: "exec-2"}, {ID: "exec-3"}}},
	}}
	all := walkUnfinished(t, NewJobStore(inner, h.svc), 2, 5)
	ids := make([]string, len(all))
	for i := range all {
		ids[i] = all[i].ID
	}
	require.Len(t, ids, 4, "%v", ids)
	assert.Equal(t, []string{"exec-1", "exec-2", "exec-3"}, ids[:3], "every wrapped job comes before the run jobs")
	got, ok := runOfJob(ids[3], resumeJobPrefix)
	require.True(t, ok)
	assert.Equal(t, run.ID, got)
}

// A gateway on a queue that forgets puts every unfinished job back on Start. With a
// backlog of executions longer than one page, and an active run, Start returns: the walk
// through the runs job store reaches the zero cursor.
func TestVolatileStartEndsWithABacklogOverOnePage(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	definition, err := json.Marshal(evals.Definition{ID: "ev", Type: evals.EvaluatorTypeProgrammatic, Version: 1,
		Config: json.RawMessage(`{"code":"def validate(output):\n    return len(output) > 0\n","response_type":"yes_no"}`)})
	require.NoError(t, err)
	const backlog = 205 // more than the runner's page of 200
	for range backlog {
		require.NoError(t, h.store.CreateEvalExecution(ctx, &tables.TableEvalExecution{Definition: definition, Variables: json.RawMessage(`{"output":"x"}`)}))
	}
	h.manualRun(t, `{"type":"dataset"}`, nil, tables.EvalRunEntryQueued)

	q, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	runner := evals.NewRunner(q, h.engine, func(context.Context) ([]evals.EvalKey, error) { return *h.keys.Load(), nil },
		NewJobStore(evals.NewExecutionJobStore(h.store), h.svc), evals.RunnerOptions{PollInterval: 2 * time.Millisecond, Volatile: true, SyncInterval: time.Hour})
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(closeCtx)
		_ = q.Close(closeCtx)
	})
	started := make(chan error, 1)
	go func() { started <- runner.Start(ctx) }()
	select {
	case err := <-started:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("a volatile Start never finished putting the unfinished jobs back")
	}
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

// staleRunStore hands out a run as it was before another request changed it, which is
// what a request sees when it reads the run just before a concurrent one writes it.
type staleRunStore struct {
	Store
	stale *tables.TableEvalRun
}

func (s staleRunStore) GetEvalRun(_ context.Context, _ string) (*tables.TableEvalRun, error) {
	copied := *s.stale
	return &copied, nil
}

// Rerun and AddEvaluators check that the run is idle and then mark it running. The
// mark is conditional on the run still being finished, so of two requests that both saw
// it idle only one goes ahead.
func TestRerunAndAddEvaluatorsClaimTheFinishedRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "race", [3]string{"a", "echo: a", ""})
	exact := h.exactMatch(t)
	created, err := h.svc.Create(ctx, RunInput{Name: "race", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	finished, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)

	// Another request has just marked the run running; this one read it a moment before.
	_, err = h.store.TransitionEvalRun(ctx, created.ID, nil, map[string]any{"status": tables.EvalRunRunning})
	require.NoError(t, err)
	real := h.svc.store
	h.svc.store = staleRunStore{Store: real, stale: finished}
	defer func() { h.svc.store = real }()

	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{h.aiJudge(t)})
	assert.True(t, evals.IsInvalid(err), "the second request must not mark the run again: %v", err)
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	assert.True(t, evals.IsInvalid(err), "nor may a rerun: %v", err)
	stored, err := real.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, finished.EvaluatorsJSON, stored.EvaluatorsJSON, "the evaluators were not changed")
}

// A spreadsheet runs a cell that starts with =, +, - or @ as a formula. Exported cells
// that come from dataset rows, model output or evaluator text are neutralised with a
// leading quote, so opening the export cannot run what a row or a model wrote.
func TestExportNeutralisesFormulaCells(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	payload := `=HYPERLINK("https://attacker.example","click")`
	dataset := h.dataset(t, "formulas", [3]string{payload, "", ""})
	created, err := h.svc.Create(ctx, RunInput{Name: "formulas", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)

	var out strings.Builder
	require.NoError(t, h.svc.Export(ctx, run.ID, &out))
	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	for i, cell := range records[1] {
		assert.False(t, cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])), "column %q starts a formula: %q", records[0][i], cell)
	}
	assert.Contains(t, records[1], "'"+payload, "the row's value is kept, behind a quote")
}

// An embedding metric calls its embedding model through an eval key, like a judge. A
// run that uses one on a provider with no eval key is refused before it starts.
func TestValidateNeedsAKeyForAnEmbeddingEvaluator(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "embeddings", [3]string{"a", "a", ""})
	similarity, err := h.evaluators.Install(ctx, evals.BuiltinSemanticSimilarity, evals.InstallInput{Provider: "cohere", Model: "embed-v4"})
	require.NoError(t, err)

	check, err := h.svc.Validate(ctx, RunInput{Name: "embeddings", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{{ID: similarity.ID}}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	assert.False(t, check.Valid)
	assert.Contains(t, strings.Join(check.Problems, "\n"), `provider "cohere"`)
}

// AddEvaluators runs the checks Create runs: an evaluator that needs a variable the run
// does not have, or a judge on a provider with no eval key, is refused instead of
// failing on every entry.
func TestAddEvaluatorsChecksVariablesAndKeys(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "added", [3]string{"a", "echo: a", ""})
	created, err := h.svc.Create(ctx, RunInput{Name: "added", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)

	needsDocs := h.evaluator(t, "needs docs", evals.EvaluatorTypeProgrammatic, `{"code":"def validate(output, retrieved_docs):\n    return True\n","response_type":"yes_no"}`)
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{needsDocs})
	require.True(t, evals.IsInvalid(err), "a missing variable is refused: %v", err)
	assert.Contains(t, err.Error(), "retrieved_docs")

	noKey := h.evaluator(t, "anthropic judge", evals.EvaluatorTypeAI, `{"instructions":"grade","grading_style":"yes_no","provider":"anthropic","model":"claude-sonnet-5-5"}`)
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{noKey})
	require.True(t, evals.IsInvalid(err), "a judge without an eval key is refused: %v", err)
	assert.Contains(t, err.Error(), `provider "anthropic"`)
}

// A model run's waiting work may already be routed to an eval key when the gateway
// stops: an entry queued on k1, an entry whose call was running on k1, a judge result
// running on k1. On a queue that forgets, resume puts each back on its key, so the run
// still ends.
func TestActiveModelRunIsResumedWithRoutedWork(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "routed", [3]string{"a", "echo: a", ""}, [3]string{"b", "echo: b", ""}, [3]string{"c", "echo: c", ""})
	judge := h.aiJudge(t)
	created, err := h.svc.Create(ctx, RunInput{Name: "routed", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{judge}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entries := h.entries(t, run.ID, EntryQuery{}).Entries
	require.Len(t, entries, 3)

	_, err = h.store.DeleteEvalRunResults(ctx, run.ID, nil, "")
	require.NoError(t, err)
	set := func(entryID, status string) {
		// See TestActiveRunIsResumedOnAQueueThatForgets for why the claim is cleared.
		_, err := h.store.TransitionEvalRunEntry(ctx, entryID, nil, map[string]any{"status": status, "attempt": 7, "key_id": "k1", "claim_id": "", "passed": nil, "ended_at": nil})
		require.NoError(t, err)
	}
	set(entries[0].ID, tables.EvalRunEntryQueued)
	set(entries[1].ID, tables.EvalRunEntryRunning)
	set(entries[2].ID, tables.EvalRunEntryEvaluating)
	require.NoError(t, h.store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[2].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 7, KeyID: "k1"},
	}, 0))
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	unfinished, _, err := jobs.UnfinishedJobs(ctx, evals.JobCursor{}, 0)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	require.NoError(t, h.svc.runner.Submit(ctx, unfinished[0]))

	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 3}, view.Counts)
}

// Entries never routed to a key wait on the local topic by design when their run calls
// no model. The sweep's listing leaves those runs out before its limit, so a long line
// of them cannot hide a model entry that was stored and never published.
func TestSweepFindsUnroutedModelEntriesBehindLocalOnes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	local := &tables.TableEvalRun{
		ID: "00000000-0000-0000-0000-000000000000", Name: "local", Status: tables.EvalRunRunning, TargetType: string(TargetDataset), DatasetID: "ds", DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, local))
	model := &tables.TableEvalRun{
		ID: "ffffffff-ffff-ffff-ffff-ffffffffffff", Name: "model", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: "ds", DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"model","provider":"openai","model":"gpt-4o"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, model))
	older, old := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	entries := make([]tables.TableEvalRunEntry, 30)
	for i := range entries {
		entries[i] = tables.TableEvalRunEntry{RunID: local.ID, Seq: int64(i + 1), DatasetRowID: fmt.Sprintf("row-%d", i), Status: tables.EvalRunEntryQueued, QueuedAt: older}
	}
	// The entry query orders by run_id, then seq: the local run must sort first for its
	// entries to stand in front of the model run's, which is what the test is about.
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))
	stranded := []tables.TableEvalRunEntry{{RunID: model.ID, Seq: 1, DatasetRowID: "row-m", Status: tables.EvalRunEntryQueued, QueuedAt: old}}
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, stranded))

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	stale, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, nil, 10)
	require.NoError(t, err)
	ids := make([]string, 0, len(stale))
	for _, job := range stale {
		ids = append(ids, job.ID)
	}
	assert.Contains(t, ids, targetJobID(stranded[0].ID, 1), "the model entry is found behind the local ones")
}

// Results that call no model wait on the local topic by design. Leaving them out of the
// unrouted query keeps a long line of them from hiding a stuck judge result behind the limit.
func TestSweepFindsUnroutedModelResultsBehindLocalOnes(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	for _, ev := range []struct{ id, typ, provider string }{{"ev-exact", "statistical", ""}, {"ev-judge", "ai", "openai"}} {
		require.NoError(t, h.store.CreateEvaluator(ctx, &tables.TableEvaluator{ID: ev.id, Name: ev.id, Type: ev.typ},
			&tables.TableEvaluatorVersion{Config: json.RawMessage(`{}`), ModelProvider: ev.provider}))
	}
	run := &tables.TableEvalRun{
		Name: "judged", Status: tables.EvalRunRunning, TargetType: string(TargetDataset), DatasetID: "ds", DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"dataset"}`),
		Evaluators: json.RawMessage(`[{"id":"ev-exact","type":"statistical","builtin_key":"exact_match","version":1,"config":{}},` +
			`{"id":"ev-judge","type":"ai","version":1,"config":{"instructions":"x","grading_style":"yes_no","provider":"openai","model":"gpt-4o"}}]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	older, old := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	// A result is stored only for an entry of the run.
	stored := make([]tables.TableEvalRunEntry, 0, 31)
	for i := range 30 {
		stored = append(stored, tables.TableEvalRunEntry{ID: fmt.Sprintf("entry-%d", i), RunID: run.ID, Seq: int64(i + 1), DatasetRowID: fmt.Sprintf("row-%d", i), Status: tables.EvalRunEntryEvaluating})
	}
	stored = append(stored, tables.TableEvalRunEntry{ID: "entry-j", RunID: run.ID, Seq: 31, DatasetRowID: "row-j", Status: tables.EvalRunEntryEvaluating})
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, stored))
	results := make([]tables.TableEvalRunResult, 0, 31)
	for i := range 30 {
		results = append(results, tables.TableEvalRunResult{ID: fmt.Sprintf("a-%02d", i), RunID: run.ID, EntryID: fmt.Sprintf("entry-%d", i), EvaluatorID: "ev-exact", Status: tables.EvalRunResultQueued, QueuedAt: older})
	}
	results = append(results, tables.TableEvalRunResult{ID: "zz-judge", RunID: run.ID, EntryID: "entry-j", EvaluatorID: "ev-judge", Status: tables.EvalRunResultQueued, QueuedAt: old})
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	stale, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, nil, 10)
	require.NoError(t, err)
	ids := make([]string, 0, len(stale))
	for _, job := range stale {
		ids = append(ids, job.ID)
	}
	assert.Contains(t, ids, resultJobID("zz-judge", 1), "the judge result is found behind the local ones")
}

// A rerun pages the selected entries writeBatch at a time. When exactly writeBatch
// entries are selected, the next page is empty, and an empty ID list means "every
// entry" to the bulk writes: the page must be skipped, not applied.
func TestRerunOfExactlyOneBatchLeavesOtherEntriesAlone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	rows := make([][3]string, writeBatch+1)
	for i := range rows {
		rows[i] = [3]string{fmt.Sprintf("q%d", i), "same", "same"}
	}
	dataset := h.dataset(t, "one batch", rows...)
	created, err := h.svc.Create(ctx, RunInput{Name: "one batch", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	refs, err := h.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{RunID: run.ID})
	require.NoError(t, err)
	require.Len(t, refs, writeBatch+1)
	failed := make([]string, 0, writeBatch)
	for _, ref := range refs[:writeBatch] {
		failed = append(failed, ref.ID)
	}
	kept := refs[writeBatch]
	_, err = h.store.TransitionEvalRunEntries(ctx, run.ID, nil, failed, map[string]any{"status": tables.EvalRunEntryFailed})
	require.NoError(t, err)
	before, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{kept.ID}})
	require.NoError(t, err)
	require.Len(t, before, 1)

	_, err = h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunFailed})
	require.NoError(t, err)
	h.await(t, run.ID, tables.EvalRunCompleted)
	entry, err := h.store.GetEvalRunEntry(ctx, kept.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status)
	assert.Equal(t, 1, entry.Attempt, "the entry that was not selected is not run again")
	after, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{kept.ID}})
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, before[0].ID, after[0].ID, "its result is kept")
}

// Delete checks that the run is idle and then deletes it. A run that another request
// marked running after that check is kept: the store refuses it in the delete itself.
func TestDeleteKeepsARunThatWentActive(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "delete race", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "delete race", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	finished, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRun(ctx, created.ID, nil, map[string]any{"status": tables.EvalRunRunning})
	require.NoError(t, err)
	real := h.svc.store
	h.svc.store = staleRunStore{Store: real, stale: finished}
	defer func() { h.svc.store = real }()

	err = h.svc.Delete(ctx, created.ID)
	assert.True(t, evals.IsInvalid(err), "a run that went active is not deleted: %v", err)
	_, err = real.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	refs, err := real.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{RunID: created.ID})
	require.NoError(t, err)
	assert.Len(t, refs, 1, "its entries are kept")
}

// Entry applies the run's read scope: an entry of a run the caller cannot see is not found.
func TestEntryAppliesTheRunsScope(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "scoped", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "scoped", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entry := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	_, err = h.svc.Entry(scoped, run.ID, entry.ID)
	require.ErrorIs(t, err, configstore.ErrNotFound)
}

// Update applies the run's read scope before it writes: a run the caller cannot see is
// not found, and keeps its name.
func TestUpdateAppliesTheRunsScope(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, _ := h.manualRun(t, `{"type":"dataset"}`, nil)
	scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	_, err := h.svc.Update(scoped, run.ID, UpdateInput{Name: "renamed"})
	require.ErrorIs(t, err, configstore.ErrNotFound)
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "manual", stored.Name, "the run the caller cannot see is not renamed")
}

// A routed job whose message was lost between routing and publishing is found by the
// sweep once it was routed before the cutoff, even on a key that is being consumed.
func TestSweepFindsRoutedEntriesWhoseMessageWasLost(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run := &tables.TableEvalRun{
		Name: "routed", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: "ds", DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"model","provider":"openai","model":"gpt-4o"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	entries := []tables.TableEvalRunEntry{{RunID: run.ID, Seq: 1, DatasetRowID: "row", Status: tables.EvalRunEntryQueued, QueuedAt: time.Now().Add(-time.Hour)}}
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	moved, err := jobs.RouteJob(ctx, targetJobID(entries[0].ID, 1), "", "k")
	require.NoError(t, err)
	require.True(t, moved)

	future := time.Now().Add(time.Minute)
	stale, err := jobs.StaleJobs(ctx, future, future, []string{"k"}, 10)
	require.NoError(t, err)
	var found *evals.Job
	for _, job := range stale {
		if job.ID == targetJobID(entries[0].ID, 1) {
			found = job
		}
	}
	require.NotNil(t, found, "the routed entry is returned once its routing is older than the cutoff")
	assert.False(t, found.RoutedAt.IsZero(), "with the time it was routed")
}

// A second message for the same entry is a different delivery and is refused, so the
// target is called once. A redelivery of the first message is claimed again.
func TestStartJobHonoursTheDeliveryClaim(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run := &tables.TableEvalRun{
		Name: "claimed", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: "ds", DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"model","provider":"openai","model":"gpt-4o"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	entries := []tables.TableEvalRunEntry{{RunID: run.ID, Seq: 1, DatasetRowID: "row", Status: tables.EvalRunEntryQueued}}
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := targetJobID(entries[0].ID, 1)

	claimed, err := jobs.StartJob(ctx, id, "", evals.JobClaim{DeliveryID: "a"})
	require.NoError(t, err)
	assert.True(t, claimed, "the first delivery claims the entry")
	claimed, err = jobs.StartJob(ctx, id, "", evals.JobClaim{DeliveryID: "b"})
	require.NoError(t, err)
	assert.False(t, claimed, "another delivery of the running entry is refused")
	claimed, err = jobs.StartJob(ctx, id, "", evals.JobClaim{DeliveryID: "a"})
	require.NoError(t, err)
	assert.True(t, claimed, "a redelivery of the first message is claimed again")
	job, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "a", job.ClaimID, "the loaded job carries its claim")
	assert.False(t, job.StartedAt.IsZero())
}

// hookStore lets a test make one store method fail or answer differently. A nil hook
// passes the call through.
type hookStore struct {
	Store
	transitionEntry        func(ctx context.Context, id string, from []string, fields map[string]any) (bool, error)
	transitionEntryAttempt func(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error)
	transitionEntries      func(ctx context.Context, runID string, from, ids []string, fields map[string]any) (int64, error)
	resetEntries           func(ctx context.Context, runID string, ids []string, fields map[string]any) (int64, error)
	transitionResult       func(ctx context.Context, id string, from []string, fields map[string]any) (bool, error)
	runState               func(ctx context.Context, id string) (*configstore.EvalRunState, error)
	hasOpen                func(ctx context.Context, runID string) (bool, error)
	getRun                 func(ctx context.Context, id string) (*tables.TableEvalRun, error)
}

func (s hookStore) GetEvalRun(ctx context.Context, id string) (*tables.TableEvalRun, error) {
	if s.getRun != nil {
		return s.getRun(ctx, id)
	}
	return s.Store.GetEvalRun(ctx, id)
}

func (s hookStore) TransitionEvalRunEntry(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
	if s.transitionEntry != nil {
		return s.transitionEntry(ctx, id, from, fields)
	}
	return s.Store.TransitionEvalRunEntry(ctx, id, from, fields)
}

func (s hookStore) TransitionEvalRunEntryAttempt(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error) {
	if s.transitionEntryAttempt != nil {
		return s.transitionEntryAttempt(ctx, id, attempt, from, fields)
	}
	return s.Store.TransitionEvalRunEntryAttempt(ctx, id, attempt, from, fields)
}

func (s hookStore) TransitionEvalRunEntries(ctx context.Context, runID string, from, ids []string, fields map[string]any) (int64, error) {
	if s.transitionEntries != nil {
		return s.transitionEntries(ctx, runID, from, ids, fields)
	}
	return s.Store.TransitionEvalRunEntries(ctx, runID, from, ids, fields)
}

func (s hookStore) ResetEvalRunEntries(ctx context.Context, runID string, ids []string, fields map[string]any) (int64, error) {
	if s.resetEntries != nil {
		return s.resetEntries(ctx, runID, ids, fields)
	}
	return s.Store.ResetEvalRunEntries(ctx, runID, ids, fields)
}

func (s hookStore) TransitionEvalRunResult(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
	if s.transitionResult != nil {
		return s.transitionResult(ctx, id, from, fields)
	}
	return s.Store.TransitionEvalRunResult(ctx, id, from, fields)
}

func (s hookStore) GetEvalRunState(ctx context.Context, id string) (*configstore.EvalRunState, error) {
	if s.runState != nil {
		return s.runState(ctx, id)
	}
	return s.Store.GetEvalRunState(ctx, id)
}

func (s hookStore) HasOpenEvalRunEntries(ctx context.Context, runID string) (bool, error) {
	if s.hasOpen != nil {
		return s.hasOpen(ctx, runID)
	}
	return s.Store.HasOpenEvalRunEntries(ctx, runID)
}

// stubRunner queues nothing: Submit calls submit when it is set, and otherwise accepts
// the job and drops it. Keys come from the harness.
type stubRunner struct {
	h      *harness
	submit func(ctx context.Context, job *evals.Job) error
}

func (r stubRunner) Submit(ctx context.Context, job *evals.Job) error {
	if r.submit != nil {
		return r.submit(ctx, job)
	}
	return nil
}

func (r stubRunner) Resume(context.Context, *evals.Job) error { return nil }

func (r stubRunner) Keys(context.Context) ([]evals.EvalKey, error) { return *r.h.keys.Load(), nil }

// useStore swaps the service's store for the test.
func (h *harness) useStore(t testing.TB, wrap func(real Store) Store) Store {
	t.Helper()
	real := h.svc.store
	h.svc.store = wrap(real)
	t.Cleanup(func() { h.svc.store = real })
	return real
}

// useRunner swaps the service's runner for the test.
func (h *harness) useRunner(t testing.TB, runner Runner) {
	t.Helper()
	real := h.svc.runner
	h.svc.SetRunner(runner)
	t.Cleanup(func() { h.svc.SetRunner(real) })
}

// manualRun stores a running, planned run with the given target and evaluators, and one
// entry per status, without queueing anything.
func (h *harness) manualRun(t testing.TB, target string, definitions []evals.Definition, statuses ...string) (*tables.TableEvalRun, []tables.TableEvalRunEntry) {
	t.Helper()
	ctx := context.Background()
	evaluatorsJSON, err := json.Marshal(definitions)
	require.NoError(t, err)
	var parsed Target
	require.NoError(t, json.Unmarshal([]byte(target), &parsed))
	now := time.Now()
	run := &tables.TableEvalRun{
		Name: "manual", Status: tables.EvalRunRunning, TargetType: string(parsed.Type), DatasetID: "ds", DatasetVersion: 1,
		Target: json.RawMessage(target), Evaluators: evaluatorsJSON, StartedAt: &now,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	entries := make([]tables.TableEvalRunEntry, len(statuses))
	for i, status := range statuses {
		entries[i] = tables.TableEvalRunEntry{RunID: run.ID, Seq: int64(i + 1), DatasetRowID: fmt.Sprintf("row-%d", i), Status: status,
			Variables: map[string]string{"input": fmt.Sprintf("q%d", i), "expected_output": "same"}, Output: "same"}
	}
	if len(entries) > 0 {
		require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))
	}
	return run, entries
}

func (h *harness) definition(t testing.TB, ref EvaluatorRef) evals.Definition {
	t.Helper()
	definition, err := h.evaluators.Definition(context.Background(), ref.ID, 0)
	require.NoError(t, err)
	return definition
}

const modelTarget = `{"type":"model","provider":"openai","model":"gpt-4o"}`

// finish runs a loaded job the way the runner does once it holds the claim, and returns
// what completing it returned.
func finish(ctx context.Context, jobs *JobStore, job *evals.Job) error {
	result := evals.Result{Score: evals.NAScore()}
	if job.Exec != nil {
		result = job.Exec(ctx)
	}
	if job.Complete != nil {
		return job.Complete(ctx, result)
	}
	return jobs.CompleteJob(ctx, job.ID, result)
}

// A target call can outlive its attempt: a stop and a rerun reset the entry, and the next
// delivery starts it again at the next attempt. The late call of the first attempt must
// not store its output over the second.
func TestLateTargetCallOfAnEarlierAttemptIsDropped(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued)
	release, started := make(chan struct{}), make(chan struct{}, 1)
	h.setModel(func(context.Context, evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		started <- struct{}{}
		<-release
		return evals.ModelCallResponse{Text: "late"}, nil
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := targetJobID(entries[0].ID, 1)
	job, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	claimed, err := jobs.StartJob(ctx, id, job.KeyID, evals.JobClaim{DeliveryID: "first"})
	require.NoError(t, err)
	require.True(t, claimed)
	done := make(chan error, 1)
	go func() { done <- finish(ctx, jobs, job) }()
	waitFor(t, started, "the target call to start")

	// What Stop, Rerun and the next start leave on the entry.
	_, err = h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"status": tables.EvalRunEntryRunning, "attempt": 2, "claim_id": "second"})
	require.NoError(t, err)
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the late call did not return")
	}
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryRunning, entry.Status, "the second attempt still owns the entry")
	assert.Equal(t, 2, entry.Attempt)
	assert.Equal(t, "same", entry.Output, "the late output is not stored")
	_ = run
}

// waitFor fails the test when nothing arrives on ch in time.
func waitFor(t testing.TB, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A target delivery is acknowledged only once its output is stored. A failed write has
// the delivery come back instead of leaving the entry running with nothing queued.
func TestTargetDeliveryFailsWhenItsOutputIsNotStored(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	_, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued)
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		fail := func(fields map[string]any) bool {
			return fields["status"] == tables.EvalRunEntryEvaluating && failedOnce.CompareAndSwap(false, true)
		}
		return hookStore{Store: real,
			transitionEntry: func(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
				if fail(fields) {
					return false, errors.New("database is locked")
				}
				return real.TransitionEvalRunEntry(ctx, id, from, fields)
			},
			transitionEntryAttempt: func(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error) {
				if fail(fields) {
					return false, errors.New("database is locked")
				}
				return real.TransitionEvalRunEntryAttempt(ctx, id, attempt, from, fields)
			},
		}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := targetJobID(entries[0].ID, 1)
	job, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	claimed, err := jobs.StartJob(ctx, id, job.KeyID, evals.JobClaim{DeliveryID: "d"})
	require.NoError(t, err)
	require.True(t, claimed)
	require.Error(t, finish(ctx, jobs, job), "the delivery is not acknowledged")
}

// failRun leaves a run failed. Its waiting target jobs must not call the model.
func TestTargetJobOfAFailedRunIsDropped(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued)
	_, err := h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunFailed})
	require.NoError(t, err)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, targetJobID(entries[0].ID, 1))
	require.NoError(t, err)
	assert.True(t, job.Done, "a failed run's target job has no work")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryStopped, entry.Status)
}

// A result can be stored while the step after it, completing its entry, fails. The
// redelivered job finishes that step instead of finding the result done and leaving
// the entry evaluating.
func TestStoredResultStillCompletesItsEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultRunning, Attempt: 1}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	_, err := h.store.TransitionEvalRunResult(ctx, results[0].ID, nil, map[string]any{"status": tables.EvalRunResultCompleted, "pass": true})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, resultJobID(results[0].ID, 1))
	require.NoError(t, err)
	if !job.Done {
		require.NoError(t, finish(ctx, jobs, job))
	}
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status)
}

// A result that could not be queued is marked failed. When that write fails as well, the
// walk that created it must be delivered again, not acknowledged.
func TestFailureToRecordAnUnqueuedResultIsReturned(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryCompleted)
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error { return errors.New("queue is full") }})
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, transitionResult: func(context.Context, string, []string, map[string]any) (bool, error) {
			return false, errors.New("database is locked")
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, runJobID(scoreJobPrefix, run.ID))
	require.NoError(t, err)
	require.Error(t, finish(ctx, jobs, job))
}

// A worker can read the run as running, then another worker finalizes it and a rerun
// marks it running again. The first worker's final write must not end the rerun.
func TestFinalizeDoesNotEndARerunItDidNotSee(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "fenced", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "fenced", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	// The run as the first worker read it, while it was still running.
	_, err = h.store.TransitionEvalRun(ctx, created.ID, nil, map[string]any{"status": tables.EvalRunRunning, "updated_at": time.Now().Add(-time.Minute)})
	require.NoError(t, err)
	captured, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRun(ctx, created.ID, nil, map[string]any{"status": tables.EvalRunCompleted})
	require.NoError(t, err)
	h.useRunner(t, stubRunner{h: h})
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)

	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			runState: func(context.Context, string) (*configstore.EvalRunState, error) {
				copied := *captured
				return &copied, nil
			},
			hasOpen: func(ctx context.Context, runID string) (bool, error) {
				if once.CompareAndSwap(false, true) {
					return false, nil
				}
				return real.HasOpenEvalRunEntries(ctx, runID)
			},
		}
	})
	require.NoError(t, h.svc.maybeFinalize(ctx, created.ID))
	state, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, state.Status, "the rerun is still running")
}

// The dead letter of a whole-run walk ends the run failed with the reason: a walk keeps
// no cursor to resume from, and the run would otherwise stay running forever.
func TestDeadRunWalkFailsTheRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, runJobID(scoreJobPrefix, run.ID))
	require.NoError(t, err)
	require.NotNil(t, job.Complete)
	require.NoError(t, job.Complete(ctx, evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "evaluation failed after 3 attempts"}))
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, state.Status)
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Error, "evaluation failed after 3 attempts")
}

// A run whose planning job could not be queued is failed even when the request that
// created it was canceled in between.
func TestCreateFailsTheRunWithACanceledRequest(t *testing.T) {
	h := newHarness(t)
	dataset := h.dataset(t, "canceled", [3]string{"a", "same", "same"})
	exact := h.exactMatch(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error {
		cancel()
		return errors.New("queue is down")
	}})
	_, err := h.svc.Create(ctx, RunInput{Name: "canceled", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.Error(t, err)
	runs, _, err := h.store.ListEvalRuns(context.Background(), configstore.EvalRunListParams{})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, tables.EvalRunFailed, runs[0].Status)
}

// The pooled planning batch is cleared over its whole capacity: a short page must not
// leave the variables of a longer earlier page reachable from the pool.
func TestResetEntryBatchClearsEverySlot(t *testing.T) {
	batch := make([]tables.TableEvalRunEntry, 0, 200)
	for range 200 {
		batch = append(batch, tables.TableEvalRunEntry{Variables: map[string]string{"input": "x"}})
	}
	batch = batch[:3]
	resetEntryBatch(&batch)
	assert.Empty(t, batch)
	for i, entry := range batch[:cap(batch)] {
		assert.Nil(t, entry.Variables, "slot %d", i)
	}
}

// Negative versions are refused, not read as "the current one".
func TestNegativeVersionsAreRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "negative", [3]string{"a", "same", "same"})
	exact := h.exactMatch(t)
	_, err := h.svc.Validate(ctx, RunInput{Name: "neg", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{{ID: exact.ID, Version: -1}}, Target: Target{Type: TargetDataset}})
	assert.True(t, evals.IsInvalid(err), "a negative evaluator version is refused: %v", err)

	in := RunInput{Name: "neg", DatasetID: dataset.ID, DatasetVersion: -1, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}}
	check, err := h.svc.Validate(ctx, in)
	require.NoError(t, err)
	assert.False(t, check.Valid)
	assert.Contains(t, strings.Join(check.Problems, "\n"), "dataset_version")
	_, err = h.svc.Create(ctx, in)
	assert.True(t, evals.IsInvalid(err))
	runs, _, err := h.store.ListEvalRuns(ctx, configstore.EvalRunListParams{})
	require.NoError(t, err)
	assert.Empty(t, runs, "no run is stored")
}

// An eval key serves a model only when it supports it. A run whose target, or an added
// judge, calls a model no key supports is refused up front.
func TestKeysMustSupportTheModel(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.keys.Store(&[]evals.EvalKey{{ID: "k1", Provider: "openai", Concurrency: 4, Supports: func(m string) bool { return m == "gpt-a" || m == "gpt-4o" }}})
	dataset := h.dataset(t, "models", [3]string{"a", "same", "same"})
	exact := h.exactMatch(t)
	check, err := h.svc.Validate(ctx, RunInput{Name: "m", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-b"}})
	require.NoError(t, err)
	assert.False(t, check.Valid)
	assert.Contains(t, strings.Join(check.Problems, "\n"), "gpt-b")
	check, err = h.svc.Validate(ctx, RunInput{Name: "m", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-a"}})
	require.NoError(t, err)
	assert.True(t, check.Valid, "%v", check.Problems)

	created, err := h.svc.Create(ctx, RunInput{Name: "m", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	judge := h.evaluator(t, "gpt-b judge", evals.EvaluatorTypeAI, `{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"gpt-b"}`)
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{judge})
	assert.True(t, evals.IsInvalid(err), "a judge on a model no key supports is refused: %v", err)
	state, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status, "the run is unchanged")
}

// AddEvaluators claims the run and then queues the scoring. When queueing fails, the
// claim is undone: the run is finished again with its evaluators, and its entries keep
// their outputs as completed, so a later rerun does not call the target again.
func TestAddEvaluatorsUndoesItsClaimWhenQueueingFails(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "undo", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "undo", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	before, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error { return errors.New("queue is down") }})
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{h.aiJudge(t)})
	require.Error(t, err)
	after, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, after.Status)
	assert.Equal(t, before.EvaluatorsJSON, after.EvaluatorsJSON)
	counts, err := h.store.CountEvalRunEntries(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 2}, counts)
}

// The undo of a failed addition is fenced on that addition's own claim. A stop and a
// second addition can land while the first one's queueing hangs; when it then fails, its
// undo must not put back the evaluator list from before it over the second addition.
func TestAddEvaluatorsUndoLeavesALaterAddition(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "undo race", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "undo race", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)

	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return errors.New("queue is down")
		}
		return nil
	}})
	first, second := h.aiJudge(t), h.evaluator(t, "second judge", evals.EvaluatorTypeAI, `{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"gpt-4o"}`)
	failed := make(chan error, 1)
	go func() {
		_, err := h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{first})
		failed <- err
	}()
	waitFor(t, entered, "the first addition to queue its scoring")
	_, err = h.svc.Stop(ctx, created.ID)
	require.NoError(t, err)
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{second})
	require.NoError(t, err)
	close(release)
	require.Error(t, <-failed)

	stored, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, stored.Status, "the second addition still runs")
	assert.Contains(t, stored.EvaluatorsJSON, second.ID, "the second addition's evaluator list is kept")
}

// An addition of only human evaluators still marks the entries as being scored, the
// durable sign that their results are still to be created. If the scoring message is
// then lost, a restart's resume of the run creates the awaiting_review results, instead
// of finishing the run with none.
func TestHumanOnlyAdditionSurvivesALostScoringMessage(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "human added", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "human added", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	human := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	h.useRunner(t, stubRunner{h: h}) // the scoring message is accepted and then lost
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{human})
	require.NoError(t, err)

	require.NoError(t, h.svc.resumeRun(ctx, created.ID))
	for _, entry := range h.entries(t, created.ID, EntryQuery{}).Entries {
		var statuses []string
		for _, result := range entry.Results {
			if result.EvaluatorID == human.ID {
				statuses = append(statuses, result.Status)
			}
		}
		assert.Equal(t, []string{tables.EvalRunResultAwaitingReview}, statuses, "entry %s", entry.ID)
	}
	state, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status)
}

// A review rebuilds a finished run's summary. A rerun that claimed the run after the
// review read it is running with no summary; the review's write must not give it the
// finished summary, which Get would then show.
func TestReviewDoesNotSummarizeARerunItDidNotSee(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "review race", [3]string{"a", "same", "same"})
	human := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	created, err := h.svc.Create(ctx, RunInput{Name: "review race", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t), human}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	entry := h.entries(t, created.ID, EntryQuery{}).Entries[0]

	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, getRun: func(ctx context.Context, id string) (*tables.TableEvalRun, error) {
			run, err := real.GetEvalRun(ctx, id)
			if err == nil && once.CompareAndSwap(false, true) {
				// A rerun claims the run right after the review read it.
				_, claimErr := real.TransitionEvalRun(ctx, id, finishedRunStatuses, map[string]any{
					"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil, "error": "", "updated_at": time.Now(),
				})
				require.NoError(t, claimErr)
			}
			return run, err
		}}
	})
	_, err = h.svc.Review(ctx, created.ID, entry.ID, human.ID, ReviewInput{Score: true}, "reviewer")
	require.NoError(t, err)
	stored, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, stored.Status)
	assert.Nil(t, stored.SummaryJSON, "the rerun has no summary")
}

// A rename moves the run's updated_at, which fences the final write. A rename that lands
// between the finalizing worker's read and its write must not leave the run running with
// nothing left to do: the worker reads the run again and finishes it.
func TestFinalizeFinishesARunRenamedMeanwhile(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryCompleted)
	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, runState: func(ctx context.Context, id string) (*configstore.EvalRunState, error) {
			state, err := real.GetEvalRunState(ctx, id)
			if err == nil && once.CompareAndSwap(false, true) {
				time.Sleep(2 * time.Millisecond)
				_, renameErr := h.svc.Update(ctx, id, UpdateInput{Name: "renamed"})
				require.NoError(t, renameErr)
			}
			return state, err
		}}
	})
	require.NoError(t, h.svc.maybeFinalize(ctx, run.ID))
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, stored.Status)
	assert.NotNil(t, stored.SummaryJSON, "the finished run has its summary")
	assert.Equal(t, "renamed", stored.Name)
}

// A result whose eval key goes away before it starts is ended by the runner with the
// reason, without a claim, so the stored attempt is still the one before the message's.
// That end must still be stored: an evaluator that is not retried (here an embedding
// metric) would otherwise stay queued, and its entry and run would never finish.
func TestUnstartedResultEndsWhenItsKeyIsGone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	installed, err := h.evaluators.Install(ctx, evals.BuiltinSemanticSimilarity, evals.InstallInput{Provider: "openai", Model: "text-embedding-3-small"})
	require.NoError(t, err)
	similarity := h.definition(t, EvaluatorRef{ID: installed.ID})
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{similarity}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: similarity.ID, Status: tables.EvalRunResultQueued, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))

	h.keys.Store(&[]evals.EvalKey{})
	runner := h.svc.runner.(*evals.Runner)
	require.NoError(t, runner.Sync(ctx))
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, resultJobID(results[0].ID, 1))
	require.NoError(t, err)
	require.False(t, job.Done)
	require.NoError(t, runner.Resume(ctx, job)) // the message on k1, which is no longer an eval key

	stored, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultFailed, stored.Status)
	assert.Contains(t, stored.Error, "enabled for evaluations")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status)
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status)
}

// evalKeyTopic is the runner's topic for an eval key (evals.keyTopic).
func evalKeyTopic(keyID string) string {
	sum := sha256.Sum256([]byte(keyID))
	return "evals.key." + hex.EncodeToString(sum[:16])
}

// leftEvaluatingOn stores a running model run with one entry left evaluating at attempt 1
// on keyID, its output stored and its results never created, as a gateway that died
// between the two leaves it.
func (h *harness) leftEvaluatingOn(t *testing.T, keyID string, startedAt time.Time) (*tables.TableEvalRun, tables.TableEvalRunEntry) {
	t.Helper()
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(context.Background(), entries[0].ID, nil, map[string]any{"attempt": 1, "key_id": keyID, "claim_id": "dead", "started_at": startedAt})
	require.NoError(t, err)
	return run, entries[0]
}

// A target message for an entry left evaluating only finishes the entry's results from
// the stored output; it calls no model. Delivered on the topic of a key that is no longer
// an eval key, it still runs there instead of being moved, which a started entry cannot be.
func TestEntryLeftEvaluatingOnARemovedKeyIsRepairedFromItsTopic(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.keys.Store(&[]evals.EvalKey{{ID: "k1", Provider: "openai", Concurrency: 4}, {ID: "k-gone", Provider: "openai", Concurrency: 4}})
	runner := h.svc.runner.(*evals.Runner)
	require.NoError(t, runner.Sync(ctx)) // this instance consumes k-gone's topic
	run, entry := h.leftEvaluatingOn(t, "k-gone", time.Now())
	h.keys.Store(&[]evals.EvalKey{{ID: "k1", Provider: "openai", Concurrency: 4}})
	require.NoError(t, runner.Sync(ctx))
	var called atomic.Int32
	h.setModel(func(context.Context, evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		called.Add(1)
		return evals.ModelCallResponse{Text: "called again"}, nil
	})

	payload, err := json.Marshal(map[string]string{"job_id": targetJobID(entry.ID, 1)})
	require.NoError(t, err)
	require.NoError(t, h.queue.Publish(ctx, &queue.Message{ID: "redelivered-target", Topic: evalKeyTopic("k-gone"), Payload: payload, Headers: map[string]string{"key_id": "k-gone"}}))

	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 1}, view.Counts)
	stored := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	require.Len(t, stored.Results, 1)
	assert.Equal(t, tables.EvalRunResultCompleted, stored.Results[0].Status)
	assert.Equal(t, "same", stored.Output, "the stored output is what was scored")
	assert.Zero(t, called.Load(), "the target is not called again")
}

// On a queue that keeps its messages, nothing consumes the topic of a key removed while
// every gateway was down. An entry left evaluating on it is found by the sweep once it
// started long enough ago, and repaired on the local topic.
func TestSweepRepairsAnEntryLeftEvaluatingOnARemovedKey(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, _ := h.leftEvaluatingOn(t, "key-gone", time.Now().Add(-2*time.Hour))
	require.NoError(t, h.svc.runner.(*evals.Runner).Sweep(ctx))
	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 1}, view.Counts)
}

// A rerun resets a page of entries in one write: when the reset fails, the results of the
// page are still there, and no entry is left completed with its results gone.
func TestRerunResetIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "atomic", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "atomic", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			transitionEntries: func(context.Context, string, []string, []string, map[string]any) (int64, error) {
				return 0, errors.New("database is locked")
			},
			// The entries' write fails inside the store's own transaction.
			resetEntries: func(ctx context.Context, runID string, ids []string, fields map[string]any) (int64, error) {
				broken := maps.Clone(fields)
				broken["no_such_column"] = 1
				return real.ResetEvalRunEntries(ctx, runID, ids, broken)
			},
		}
	})
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.Error(t, err)
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: created.ID})
	require.NoError(t, err)
	assert.Len(t, results, 2, "every result is kept")
}

// A job that started on a key which was then removed has nothing consuming its topic
// once a durable queue restarts. The sweep lists it, with its claim, once its start is
// older than the cutoff, so the runner can end it.
func TestSweepFindsStartedEntriesOnARemovedKey(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued)
	started := time.Now().Add(-2 * time.Hour)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"status": tables.EvalRunEntryRunning, "attempt": 1, "key_id": "key-gone", "claim_id": "c", "started_at": started})
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRunEntry(ctx, entries[1].ID, nil, map[string]any{"status": tables.EvalRunEntryRunning, "attempt": 1, "key_id": "k1", "claim_id": "d", "started_at": started})
	require.NoError(t, err)
	_ = run

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	stale, err := jobs.StaleJobs(ctx, time.Now(), time.Now().Add(-time.Hour), []string{"k1"}, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "only the job on the removed key")
	assert.Equal(t, targetJobID(entries[0].ID, 1), stale[0].ID)
	assert.True(t, stale[0].Started)
	assert.Equal(t, "c", stale[0].ClaimID)
	assert.Equal(t, "key-gone", stale[0].KeyID)
	assert.WithinDuration(t, started, stale[0].StartedAt, time.Second)
}

// failingVersions makes the dataset version read of an export fail.
type failingVersions struct {
	Datasets
	err error
}

func (d failingVersions) Version(context.Context, string, int) (*tables.TableEvalDatasetVersion, error) {
	return nil, d.err
}

// An export returns a failure to read the pinned dataset version instead of writing a
// CSV without the variable columns. A deleted dataset still exports, without them.
func TestExportReturnsAVersionReadFailure(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "export", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "export", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	real := h.svc.datasets
	t.Cleanup(func() { h.svc.datasets = real })

	h.svc.datasets = failingVersions{Datasets: real, err: errors.New("boom")}
	var out strings.Builder
	require.ErrorContains(t, h.svc.Export(ctx, created.ID, &out), "boom")
	assert.Empty(t, out.String(), "nothing is written")

	h.svc.datasets = failingVersions{Datasets: real, err: configstore.ErrNotFound}
	out.Reset()
	require.NoError(t, h.svc.Export(ctx, created.ID, &out))
	assert.NotEmpty(t, out.String())
}

// A string score is text a model or a reviewer wrote, so it is neutralised like other
// such cells. A number score is left alone.
func TestExportNeutralisesStringScores(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "scores", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "scores", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID})
	require.NoError(t, err)
	require.Len(t, results, 2)
	for i, score := range []evals.Score{evals.StringScore("=HYPERLINK(1)"), evals.NumberScore(-0.5)} {
		data, err := json.Marshal(score)
		require.NoError(t, err)
		_, err = h.store.TransitionEvalRunResult(ctx, results[i].ID, nil, map[string]any{"score_json": string(data)})
		require.NoError(t, err)
	}
	var out strings.Builder
	require.NoError(t, h.svc.Export(ctx, run.ID, &out))
	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	require.NoError(t, err)
	column := slices.Index(records[0], "exact match: score")
	require.Positive(t, column)
	scores := []string{records[1][column], records[2][column]}
	assert.ElementsMatch(t, []string{"'=HYPERLINK(1)", "-0.5"}, scores)
}
