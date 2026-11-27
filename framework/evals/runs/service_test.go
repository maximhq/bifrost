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
	"github.com/maximhq/bifrost/framework/livestore/livestoretest"
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
	queue      queue.Queue   // the runner's queue
	runner     *evals.Runner // the runner newHarness started

	mu     sync.Mutex
	model  evals.ModelCaller // the model under test; replaced per test
	judge  judgeFunc
	called []evals.ModelCallRequest

	bare string // the ID of the empty dataset runs built by hand point at (see bareDataset)
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
		LiveStore: livestoretest.New(t),
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
	h.runner = runner
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(closeCtx)
		_ = q.Close(closeCtx)
	})
	return h
}

// bareDataset returns the ID of an empty dataset, created on first use, for a run a test
// stores by hand: a run is only stored on a live dataset.
func (h *harness) bareDataset(t testing.TB) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bare == "" {
		view, err := h.datasets.Create(context.Background(), datasets.DatasetInput{Name: "bare", Template: "qa"})
		require.NoError(t, err)
		// A run is only stored on a version that exists, so the dataset gets a version 1.
		added, err := h.datasets.AddRows(context.Background(), view.ID, []map[string]string{{"input": "q"}}, "", "tester")
		require.NoError(t, err)
		if added.Version == nil {
			_, err = h.datasets.SaveVersion(context.Background(), view.ID, "", "", "tester")
			require.NoError(t, err)
		}
		h.bare = view.ID
	}
	return h.bare
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
	assert.Equal(t, 58.0, passRate(29, 50), "a whole percentage is exact")
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
	// The in-process queue can finish the rerun before Rerun reads the run back.
	assert.Contains(t, []string{tables.EvalRunRunning, tables.EvalRunCompleted}, rerun.Status)
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

// A review is judged the way the engine judges a machine score: a yes or no evaluator whose
// normalizer maps Yes to 1 passes a "Yes" review against an "= Yes" criterion.
func TestReviewJudgesAMappedScoreInTheMappedSpace(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "mapped", [3]string{"a", "same", "same"})
	human := h.evaluator(t, "mapped tone", evals.EvaluatorTypeHuman,
		`{"evaluation_type":"yes_no","score_normalizer":{"action":"mapping","mapping":{"Yes":1,"No":0}},"pass_criteria":{"entry_level":{"name":"score","operator":"=","value":"Yes"}}}`)
	created, err := h.svc.Create(ctx, RunInput{Name: "mapped", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{human}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entry := h.entries(t, run.ID, EntryQuery{}).Entries[0]

	reviewed, err := h.svc.Review(ctx, run.ID, entry.ID, human.ID, ReviewInput{Score: true}, "reviewer")
	require.NoError(t, err)
	require.NotNil(t, reviewed.Pass)
	assert.True(t, *reviewed.Pass, "a mapped Yes meets = Yes")
	got := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	require.NotNil(t, got.Passed)
	assert.True(t, *got.Passed, "the entry follows the review")
}

// A normalizer can push a valid review score out of the finite range. That score cannot be
// stored, so the review is refused as invalid.
func TestReviewWithANonFiniteNormalizedScoreIsInvalid(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "huge", [3]string{"a", "same", "same"})
	human := h.evaluator(t, "huge scale", evals.EvaluatorTypeHuman,
		`{"evaluation_type":"scale","scale":{"min":1,"max":5},"score_normalizer":{"action":"multiply","value":1e308}}`)
	created, err := h.svc.Create(ctx, RunInput{Name: "huge", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{human}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entry := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	_, err = h.svc.Review(ctx, run.ID, entry.ID, human.ID, ReviewInput{Score: 5}, "reviewer")
	require.True(t, evals.IsInvalid(err), "5 x 1e308 is not finite: %v", err)
}

// A result removed between the review's read and its write (a rerun reset it) is not
// found, and the review is not counted on the run.
func TestReviewOfAResultGoneBeforeTheWriteIsNotFound(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "gone", [3]string{"a", "same", "same"})
	human := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	created, err := h.svc.Create(ctx, RunInput{Name: "gone", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{human}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	entry := h.entries(t, run.ID, EntryQuery{}).Entries[0]
	real := h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, transitionResult: func(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
			if _, review := fields["reviewed_by"]; review {
				return false, nil
			}
			return real.TransitionEvalRunResult(ctx, id, from, fields)
		}}
	})
	before, err := real.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	_, err = h.svc.Review(ctx, run.ID, entry.ID, human.ID, ReviewInput{Score: true}, "reviewer")
	require.ErrorIs(t, err, configstore.ErrNotFound)
	after, err := real.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, before.ReviewSeq, after.ReviewSeq, "a review that wrote nothing is not counted")
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

// A compared row that no run returned an entry for (a run deleted between the row listing
// and the entry reads) still carries an object of variables, never null.
func TestCompareRowWithoutEntriesHasEmptyVariables(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "compare empty", [3]string{"a", "a", "a"})
	exact := h.exactMatch(t)
	first, err := h.svc.Create(ctx, RunInput{Name: "one", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	second, err := h.svc.Create(ctx, RunInput{Name: "two", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, first.ID, tables.EvalRunCompleted)
	h.await(t, second.ID, tables.EvalRunCompleted)
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, listEntries: func(context.Context, configstore.EvalRunEntryListParams) ([]tables.TableEvalRunEntry, int64, error) {
			return nil, 0, nil
		}}
	})
	comparison, err := h.svc.Compare(ctx, []string{first.ID, second.ID}, 0, 10)
	require.NoError(t, err)
	require.Len(t, comparison.Rows, 1)
	assert.NotNil(t, comparison.Rows[0].Variables)
	raw, err := json.Marshal(comparison.Rows[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"variables":{}`)
}

// The run IDs a comparison names are bounded before they are deduplicated, so a long list
// of distinct IDs is refused at once rather than compared pairwise.
func TestCompareBoundsTheIDList(t *testing.T) {
	h := newHarness(t)
	ids := make([]string, 0, 100_000)
	for i := range 100_000 {
		ids = append(ids, fmt.Sprintf("run-%d", i))
	}
	start := time.Now()
	_, err := h.svc.Compare(context.Background(), ids, 0, 10)
	require.True(t, evals.IsInvalid(err), "six or more distinct runs: %v", err)
	assert.Less(t, time.Since(start), 500*time.Millisecond, "the list is refused at the sixth distinct ID")
	repeated := []string{"a", "b", "c", "d", "e", "f"}
	for range 10_000 {
		repeated = append(repeated, "a")
	}
	_, err = h.svc.Compare(context.Background(), repeated, 0, 10)
	require.True(t, evals.IsInvalid(err))
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

// Two deliveries with different delivery IDs can both load a queued entry. The second
// misses the claim and is admitted by the fallback once the first stored the output: it
// must not call the target again, only finish what the first left.
func TestSecondDeliveryOfAQueuedTargetDoesNotCallItAgain(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useRunner(t, stubRunner{h: h})
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryQueued)
	var called atomic.Int32
	h.setModel(func(context.Context, evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		called.Add(1)
		return evals.ModelCallResponse{Text: "answer"}, nil
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := targetJobID(entries[0].ID, 1)
	first, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	second, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)

	claimed, err := jobs.StartJob(ctx, id, first.KeyID, evals.JobClaim{DeliveryID: "d1"})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, first.Complete(ctx, first.Exec(ctx)))
	claimed, err = jobs.StartJob(ctx, id, second.KeyID, evals.JobClaim{DeliveryID: "d2"})
	require.NoError(t, err)
	require.True(t, claimed, "the fallback admits the second delivery")
	require.NoError(t, second.Complete(ctx, second.Exec(ctx)))

	assert.EqualValues(t, 1, called.Load(), "the target is called once")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryEvaluating, entry.Status)
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID})
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

// The same race for a result: a second delivery admitted after the first stored the result
// must not call the evaluator again.
func TestSecondDeliveryOfAQueuedResultDoesNotRunItAgain(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useRunner(t, stubRunner{h: h})
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1, "output": "answer"})
	require.NoError(t, err)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultQueued}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	var called atomic.Int32
	h.mu.Lock()
	h.judge = func(context.Context, evals.Input) (evals.Result, error) {
		called.Add(1)
		return evals.Result{Score: evals.BoolScore(true)}, nil
	}
	h.mu.Unlock()
	// runJob is what Runner.handler does with a claimed job.
	runJob := func(job *evals.Job) error {
		var result evals.Result
		if job.Exec != nil {
			result = job.Exec(ctx)
		} else {
			result = h.engine.Run(ctx, job.Definition, job.Input)
		}
		return job.Complete(ctx, result)
	}
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := resultJobID(results[0].ID, 1)
	first, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	second, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)

	claimed, err := jobs.StartJob(ctx, id, first.KeyID, evals.JobClaim{DeliveryID: "d1"})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, runJob(first))
	claimed, err = jobs.StartJob(ctx, id, second.KeyID, evals.JobClaim{DeliveryID: "d2"})
	require.NoError(t, err)
	require.True(t, claimed, "the fallback admits the second delivery")
	require.NoError(t, runJob(second))
	assert.EqualValues(t, 1, called.Load(), "the evaluator is called once")
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
		Name: "big", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: h.bareDataset(t), DatasetVersion: 1,
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
	stale, _, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, []string{"k1"}, evals.JobCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "only the entry on the key nothing consumes")
	assert.Equal(t, targetJobID(entries[29].ID, 1), stale[0].ID)
	assert.Equal(t, "key-gone", stale[0].KeyID)
	require.NotNil(t, stale[0].Model)
	assert.Equal(t, "openai", stale[0].Model.Provider)
}

// backlogJobStore is an execution store whose stale backlog never runs out: every page is
// full and has a next page, as when API executions the sweep cannot act on arrive as fast
// as it reads them. It records the cursors it was asked for.
type backlogJobStore struct {
	evals.JobStore
	asked []evals.JobCursor
}

func (s *backlogJobStore) StaleJobs(_ context.Context, _, _ time.Time, _ []string, after evals.JobCursor, limit int) ([]*evals.Job, evals.JobCursor, error) {
	s.asked = append(s.asked, after)
	start := after.CreatedAt
	if start.IsZero() {
		start = time.Unix(0, 0).UTC()
	}
	jobs := make([]*evals.Job, 0, limit)
	var last evals.JobCursor
	for i := range limit {
		last = evals.JobCursor{CreatedAt: start.Add(time.Duration(i+1) * time.Second), ID: fmt.Sprintf("exec-%d", start.Unix()+int64(i+1))}
		jobs = append(jobs, &evals.Job{ID: last.ID, Exec: noop, CreatedAt: last.CreatedAt})
	}
	return jobs, last, nil
}

// An entry or result held under the sweep's own recovery claim after a failed write is
// listed again even on a key that is being consumed: no delivery holds that claim, so
// nothing else would end it.
func TestStaleJobsListsTheSweepsOwnClaimOnAConsumedKey(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryRunning, tables.EvalRunEntryEvaluating)
	old, now := time.Now().Add(-2*time.Hour), time.Now()
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{
		"attempt": 1, "key_id": "k1", "claim_id": configstore.EvalEndLostClaim, "started_at": old, "routed_at": now,
	})
	require.NoError(t, err)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[1].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 1, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	_, err = h.store.TransitionEvalRunResult(ctx, results[0].ID, nil, map[string]any{"claim_id": configstore.EvalEndLostClaim, "started_at": old, "routed_at": now})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	stale, _, err := jobs.StaleJobs(ctx, time.Now(), time.Now().Add(-time.Hour), []string{"k1"}, evals.JobCursor{}, 10)
	require.NoError(t, err)
	ids := make([]string, 0, len(stale))
	for _, job := range stale {
		ids = append(ids, job.ID)
	}
	assert.Contains(t, ids, targetJobID(entries[0].ID, 1), "the entry under the sweep's claim is listed")
	assert.Contains(t, ids, resultJobID(results[0].ID, 1), "the result under the sweep's claim is listed")
}

// lastPageJobStore is an execution store with one stale execution, on its last page.
type lastPageJobStore struct{ evals.JobStore }

func (lastPageJobStore) StaleJobs(context.Context, time.Time, time.Time, []string, evals.JobCursor, int) ([]*evals.Job, evals.JobCursor, error) {
	return []*evals.Job{{ID: "exec-stranded", Exec: noop}}, evals.JobCursor{}, nil
}

// When the run passes fail on the execution walk's last page, the executions already read
// still come back, with the error and a cursor that retries the run passes first.
func TestStaleJobsKeepsTheExecutionsWhenTheRunPassFails(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, listEntryRefs: func(context.Context, configstore.EvalRunEntryListParams) ([]configstore.EvalRunEntryRef, error) {
			return nil, errors.New("database is locked")
		}}
	})
	jobs := NewJobStore(lastPageJobStore{}, h.svc)
	stale, next, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, nil, evals.JobCursor{}, 10)
	require.Error(t, err)
	require.Len(t, stale, 1, "the execution read before the failure is returned")
	assert.Equal(t, "exec-stranded", stale[0].ID)
	assert.Equal(t, evals.JobCursor{ID: runPassCursor}, next, "the next sweep retries the run passes, then starts the executions over")
}

// twoPageJobStore has a full first page of stale executions and a last page holding one
// stranded execution.
type twoPageJobStore struct{ evals.JobStore }

func (twoPageJobStore) StaleJobs(_ context.Context, _, _ time.Time, _ []string, after evals.JobCursor, limit int) ([]*evals.Job, evals.JobCursor, error) {
	if after.ID == "" {
		jobs := make([]*evals.Job, 0, limit)
		for i := range limit {
			jobs = append(jobs, &evals.Job{ID: fmt.Sprintf("exec-%d", i), Exec: noop})
		}
		return jobs, evals.JobCursor{CreatedAt: time.Unix(1, 0), ID: fmt.Sprintf("exec-%d", limit-1)}, nil
	}
	return []*evals.Job{{ID: "exec-stranded", Exec: noop}}, evals.JobCursor{}, nil
}

// A run pass that keeps failing mid-walk must not hold the execution walk on its first
// page: the cursor it returns with the error goes on to the next execution page, and the
// run passes are tried again after that page.
func TestFailingRunPassStillAdvancesTheExecutionWalk(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, listEntryRefs: func(context.Context, configstore.EvalRunEntryListParams) ([]configstore.EvalRunEntryRef, error) {
			return nil, errors.New("database is locked")
		}}
	})
	jobs := NewJobStore(twoPageJobStore{}, h.svc)
	var after evals.JobCursor
	reached := false
	for range 4 {
		stale, next, _ := jobs.StaleJobs(ctx, time.Now(), time.Time{}, nil, after, 5)
		for _, job := range stale {
			reached = reached || job.ID == "exec-stranded"
		}
		after = next // the sweep stores the cursor it gets back, with or without an error
	}
	assert.True(t, reached, "the execution behind the first page is reached while the run pass fails")
}

// failingStaleJobStore is an execution store whose stale listing always fails.
type failingStaleJobStore struct{ evals.JobStore }

func (failingStaleJobStore) StaleJobs(context.Context, time.Time, time.Time, []string, evals.JobCursor, int) ([]*evals.Job, evals.JobCursor, error) {
	return nil, evals.JobCursor{}, errors.New("database is locked")
}

// An execution listing that keeps failing must not hold the run passes back: a run entry
// stranded on a removed key is still returned, with the error and a cursor that retries
// the executions from the same place.
func TestFailingExecutionListingStillRunsTheRunPasses(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run := &tables.TableEvalRun{
		Name: "stranded", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: h.bareDataset(t), DatasetVersion: 1,
		Target: json.RawMessage(modelTarget), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	entries := []tables.TableEvalRunEntry{{RunID: run.ID, Seq: 1, DatasetRowID: "row", Status: tables.EvalRunEntryQueued, KeyID: "key-gone", QueuedAt: time.Now().Add(-time.Hour)}}
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))

	jobs := NewJobStore(failingStaleJobStore{}, h.svc)
	stale, next, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, []string{"k1"}, evals.JobCursor{}, 10)
	require.Error(t, err)
	ids := make([]string, 0, len(stale))
	for _, job := range stale {
		ids = append(ids, job.ID)
	}
	assert.Contains(t, ids, targetJobID(entries[0].ID, 1), "the stranded entry is found while the execution listing fails")
	assert.Equal(t, evals.JobCursor{ID: executionStartCursor}, next, "the next sweep retries the executions from the start")

	mid := evals.JobCursor{CreatedAt: time.Unix(1, 0), ID: "exec-9"}
	_, next, err = jobs.StaleJobs(ctx, time.Now(), time.Time{}, []string{"k1"}, mid, 10)
	require.Error(t, err)
	assert.Equal(t, mid, next, "a mid-walk failure retries the same execution page")
}

// The sweep keeps its place in the execution backlog from one sweep to the next. A run
// entry stranded on a removed key must still be reached while execution rows fill every
// page, or a busy API would hide a test run's stuck work for as long as it stays busy.
// The execution walk must not lose its place to the run passes either.
func TestSweepReachesStrandedRunEntriesBehindAFullExecutionBacklog(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run := &tables.TableEvalRun{
		Name: "stranded", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: h.bareDataset(t), DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"model","provider":"openai","model":"gpt-4o"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	entries := []tables.TableEvalRunEntry{{RunID: run.ID, Seq: 1, DatasetRowID: "row", Status: tables.EvalRunEntryQueued, KeyID: "key-gone", QueuedAt: time.Now().Add(-time.Hour)}}
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))

	inner := &backlogJobStore{}
	jobs := NewJobStore(inner, h.svc)
	const limit, sweeps = 5, 6
	var after evals.JobCursor
	var execPages []evals.JobCursor // the cursor each execution page ended on
	reached := 0
	for range sweeps {
		stale, next, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, []string{"k1"}, after, limit)
		require.NoError(t, err)
		require.NotEqual(t, evals.JobCursor{}, next, "the backlog never runs out, so the lap never wraps")
		for _, job := range stale {
			if job.ID == targetJobID(entries[0].ID, 1) {
				reached++
			} else if strings.HasPrefix(job.ID, "exec-") {
				execPages = append(execPages, evals.JobCursor{CreatedAt: job.CreatedAt, ID: job.ID})
			}
		}
		after = next
	}
	assert.Positive(t, reached, "the stranded run entry is reached behind a full execution backlog")
	// Every cursor the execution store was asked for is one it returned, in order: the run
	// passes neither restart the execution walk nor hand it a cursor of their own.
	require.NotEmpty(t, inner.asked)
	assert.Equal(t, evals.JobCursor{}, inner.asked[0])
	for i := 1; i < len(inner.asked); i++ {
		assert.Equal(t, execPages[i*limit-1], inner.asked[i], "execution page %d continues after page %d", i+1, i)
	}
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
	assert.EqualValues(t, planPageSize, view.Total, "the total counts the entries the plan stored")

	_, err = h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)
	view = h.await(t, run.ID, tables.EvalRunCompleted)
	assert.EqualValues(t, planPageSize, view.Counts[tables.EvalRunEntryCompleted])
	assert.EqualValues(t, planPageSize, view.Total)
}

// A stop whose cleanup ran just before the plan stored its last page is still seen by the
// plan: the stop check follows every stored page, the last one too. The late page is
// stopped and the run gets its total, so its report counts every entry it stores.
func TestStopBeforeThePlansLastPageIsSeen(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "stopped at the end", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	saved, err := h.datasets.SaveVersion(ctx, dataset.ID, "", "", "tester")
	require.NoError(t, err)
	exact := h.definition(t, h.exactMatch(t))
	evaluatorsJSON, err := json.Marshal([]evals.Definition{exact})
	require.NoError(t, err)
	run := &tables.TableEvalRun{
		Name: "stopped at the end", Status: tables.EvalRunQueued, TargetType: string(TargetDataset), DatasetID: dataset.ID, DatasetVersion: saved.Number,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: evaluatorsJSON,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	h.useRunner(t, stubRunner{h: h})
	var once sync.Once
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, createEntries: func(ctx context.Context, entries []tables.TableEvalRunEntry) error {
			// The stop and its cleanup land between the plan's page read and its insert.
			once.Do(func() {
				_, err := h.svc.Stop(ctx, run.ID)
				require.NoError(t, err)
			})
			return real.CreateEvalRunEntries(ctx, entries)
		}}
	})

	require.NoError(t, h.svc.plan(ctx, run.ID))

	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryStopped: 2}, counts, "the late page is stopped")
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, stored.Status)
	assert.EqualValues(t, 2, stored.Total, "the total counts the entries the plan stored")
}

// A stop that commits after the plan's last stop check stops every stored entry, so the
// plan's running write matches nothing. The plan must then end as a stopped plan and
// write the run's total, not go on and leave the stopped run with a total of 0.
func TestStopAfterThePlansLastCheckKeepsTheTotal(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "stopped before counting", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	saved, err := h.datasets.SaveVersion(ctx, dataset.ID, "", "", "tester")
	require.NoError(t, err)
	exact := h.definition(t, h.exactMatch(t))
	evaluatorsJSON, err := json.Marshal([]evals.Definition{exact})
	require.NoError(t, err)
	run := &tables.TableEvalRun{
		Name: "stopped before counting", Status: tables.EvalRunQueued, TargetType: string(TargetDataset), DatasetID: dataset.ID, DatasetVersion: saved.Number,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: evaluatorsJSON,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	h.useRunner(t, stubRunner{h: h})
	var stopped atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, countEntries: func(ctx context.Context, runID string) (map[string]int64, error) {
			// The stop and its cleanup land after the last stop check, before the count.
			// Stop may count entries itself, so the flag is set before it runs.
			if stopped.CompareAndSwap(false, true) {
				_, err := h.svc.Stop(ctx, run.ID)
				require.NoError(t, err)
			}
			return real.CountEvalRunEntries(ctx, runID)
		}}
	})

	require.NoError(t, h.svc.plan(ctx, run.ID))

	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryStopped: 2}, counts, "every entry is stopped")
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, stored.Status)
	assert.EqualValues(t, 2, stored.Total, "the stopped run counts the entries the plan stored")
	assert.NotNil(t, stored.StartedAt, "and counts as planned")
}

// A plan can store a page after Stop's cleanup and die before its own stop check. The
// redelivered plan job then finds the run stopped with no start time, and must end the
// plan itself: stop the page it stored and give the run its start time, or those entries
// wait forever and the run can never be rerun.
func TestRedeliveredPlanOfAStoppedRunEndsThePlan(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued)
	_, err := h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped, "started_at": nil})
	require.NoError(t, err)
	stopped, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	require.Nil(t, stopped.StartedAt)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, planJobID(run.ID))
	require.NoError(t, err)
	require.False(t, job.Done, "the plan of a stopped run that was never planned is not done")
	require.NoError(t, finish(ctx, jobs, job))

	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryStopped: 2}, counts, "the stored page is stopped")
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.NotNil(t, stored.StartedAt, "the run counts as planned")
	assert.EqualValues(t, 2, stored.Total)
}

// A claim of a run stopped mid-plan between the plan's stop check and its cleanup owns the
// run's entries: the stale cleanup does not stop them. Rerun refuses that run until its plan
// has ended, so the claim is made on the store directly to pin the cleanup's fence.
func TestStoppedPlanCleanupLeavesARerunsEntries(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	rows := make([][3]string, planPageSize+20)
	for i := range rows {
		rows[i] = [3]string{fmt.Sprintf("q%d", i), "same", "same"}
	}
	dataset := h.dataset(t, "stopped mid plan", rows...)
	saved, err := h.datasets.Get(ctx, dataset.ID)
	require.NoError(t, err)
	exact := h.definition(t, h.exactMatch(t))
	evaluatorsJSON, err := json.Marshal([]evals.Definition{exact})
	require.NoError(t, err)
	run := &tables.TableEvalRun{
		Name: "stopped mid plan", Status: tables.EvalRunStopped, TargetType: string(TargetDataset), DatasetID: dataset.ID, DatasetVersion: saved.HeadVersion,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: evaluatorsJSON,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	h.useRunner(t, stubRunner{h: h})
	var first atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, runState: func(ctx context.Context, id string) (*configstore.EvalRunState, error) {
			state, err := real.GetEvalRunState(ctx, id)
			if err == nil && first.CompareAndSwap(false, true) {
				_, rerunErr := h.svc.Rerun(ctx, id, RerunInput{Scope: RerunAll})
				require.True(t, evals.IsInvalid(rerunErr), "a rerun waits for the plan to end: %v", rerunErr)
				claimed, claimErr := real.ClaimEvalRunWithEntries(ctx, id, []string{tables.EvalRunStopped}, state.Generation,
					map[string]any{"status": tables.EvalRunRunning, "generation": configstore.EvalRunNextGeneration}, nil)
				require.NoError(t, claimErr)
				require.True(t, claimed)
			}
			return state, err
		}}
	})
	require.NoError(t, h.svc.plan(ctx, run.ID))
	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Zero(t, counts[tables.EvalRunEntryStopped], "the rerun's entries are not stopped: %v", counts)
	assert.EqualValues(t, planPageSize, counts[tables.EvalRunEntryQueued])
}

// A rerun that claims a failed run between failRun's run write and its cleanup owns the
// entries it reset: the stale cleanup does not stop them.
func TestFailRunCleanupLeavesARerunsEntries(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued)
	h.useRunner(t, stubRunner{h: h})
	var first atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, entriesAtRun: func(ctx context.Context, runID, status string, generation int64, from []string, fields map[string]any) (int64, error) {
			if fields["status"] == tables.EvalRunEntryStopped && first.CompareAndSwap(false, true) {
				_, rerunErr := h.svc.Rerun(ctx, runID, RerunInput{Scope: RerunAll})
				require.NoError(t, rerunErr)
			}
			return real.TransitionEvalRunEntriesAtRun(ctx, runID, status, generation, from, fields)
		}}
	})
	require.NoError(t, h.svc.failRun(ctx, run.ID, run.Generation, errors.New("walk failed")))
	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryQueued: 2}, counts, "the rerun's entries stay queued")
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
	// As the sweep's recovery claim leaves it, which CompleteJob follows.
	_, err = h.store.TransitionEvalRunEntry(ctx, entry.ID, nil, map[string]any{"status": tables.EvalRunEntryRunning, "passed": nil, "ended_at": nil, "claim_id": configstore.EvalEndLostClaim})
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
	got, _, ok := runOfJob(all[0].ID, resumeJobPrefix)
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
	got, _, ok := runOfJob(ids[3], resumeJobPrefix)
	require.True(t, ok)
	assert.Equal(t, run.ID, got)
}

// A gateway on a queue that forgets puts every unfinished job back on Start. With a
// backlog of executions longer than one page, and an active run, Start returns: the walk
// through the runs job store reaches the zero cursor.
func TestVolatileStartEndsWithABacklogOverOnePage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
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
	// Registered after the runner's cleanup, so it runs first: a Start that never ends is
	// canceled before the runner and the store close under it.
	startCtx, cancelStart := context.WithCancel(context.Background())
	t.Cleanup(cancelStart)
	started := make(chan error, 1)
	go func() { started <- runner.Start(startCtx) }()
	select {
	case err := <-started:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("a volatile Start never finished putting the unfinished jobs back")
	}
}

// Two finite scores near the float64 limit overflow a plain sum. The run's summary still
// holds a finite mean and median, so the finalize write (MarshalSorted refuses Inf) goes
// through and the run ends.
func TestSummaryOfScoresNearTheFloatLimitIsStored(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	scale := h.definition(t, h.evaluator(t, "huge scale", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(output):\n    return 1\n","response_type":"scale"}`))
	run, entries := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{scale}, tables.EvalRunEntryCompleted, tables.EvalRunEntryCompleted)
	score, err := json.Marshal(evals.NumberScore(1e308))
	require.NoError(t, err)
	results := make([]tables.TableEvalRunResult, len(entries))
	for i := range entries {
		results[i] = tables.TableEvalRunResult{RunID: run.ID, EntryID: entries[i].ID, EvaluatorID: scale.ID, Status: tables.EvalRunResultCompleted,
			Score: score, ScoreNumber: new(1e308)}
	}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))

	wrote, err := h.svc.writeSummary(ctx, run.ID, []evals.Definition{scale}, nil, run.Generation, nil, map[string]any{"status": tables.EvalRunCompleted})
	require.NoError(t, err, "the summary is finite, so it marshals")
	require.True(t, wrote)
	view, err := h.svc.Get(ctx, run.ID)
	require.NoError(t, err)
	summary := evaluatorSummary(t, view, scale.ID)
	require.NotNil(t, summary.Mean)
	require.NotNil(t, summary.Median)
	assert.InDelta(t, 1e308, *summary.Mean, 1e293)
	assert.InDelta(t, 1e308, *summary.Median, 1e293)
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
	// The runner is stopped before the swap: a worker can still be finishing the run that
	// await saw completed, and it reads the store field. The calls below are refusals on the
	// stored run state and need no worker; the cleanup's Close is then a no-op.
	h.stopRunner(t)
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

// A plan that stored a page and then failed ends the run failed with no start time. A
// rerun or an addition of that run would test a partial row set under a total of 0, and
// finalize refuses to end a running run that was never planned, so both are refused.
func TestRerunAndAddEvaluatorsRefuseARunThatFailedWhilePlanning(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryQueued)
	_, err := h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunQueued, "started_at": nil})
	require.NoError(t, err)
	require.NoError(t, h.svc.failRun(ctx, run.ID, run.Generation, errors.New("the dataset read failed")))
	h.useRunner(t, stubRunner{h: h})

	_, err = h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunAll})
	assert.True(t, evals.IsInvalid(err), "a rerun of a run that failed before it was planned is refused: %v", err)
	_, err = h.svc.AddEvaluators(ctx, run.ID, []EvaluatorRef{h.aiJudge(t)})
	assert.True(t, evals.IsInvalid(err), "and so is an addition: %v", err)
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, stored.Status, "the run stays failed")
	assert.Nil(t, stored.StartedAt)
}

// An AddEvaluators whose claim loses to another AddEvaluators that claimed and finished in
// between retries with the run read again, so the other request's evaluator is kept.
func TestAddEvaluatorsRetryKeepsTheEvaluatorAnotherAdded(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "two adds", [3]string{"a", "same", "same"})
	exact := h.exactMatch(t)
	created, err := h.svc.Create(ctx, RunInput{Name: "two adds", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	other := h.evaluator(t, "other", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(output, expected_output):\n    return output == expected_output\n","response_type":"yes_no"}`)
	mine := h.evaluator(t, "mine", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(output, expected_output):\n    return output == expected_output\n","response_type":"yes_no"}`)
	var first atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, claimRun: func(ctx context.Context, id string, runFrom []string, generation int64, runFields map[string]any, entries *configstore.EvalRunEntryUpdate) (bool, error) {
			if first.CompareAndSwap(false, true) {
				_, addErr := h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{other})
				require.NoError(t, addErr)
				h.await(t, created.ID, tables.EvalRunCompleted)
			}
			return real.ClaimEvalRunWithEntries(ctx, id, runFrom, generation, runFields, entries)
		}}
	})
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{mine})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	stored, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	definitions, err := definitionsOf(stored)
	require.NoError(t, err)
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		ids = append(ids, definition.ID)
	}
	assert.Equal(t, []string{exact.ID, other.ID, mine.ID}, ids, "the evaluator the other request added is kept")
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: created.ID, EvaluatorID: other.ID})
	require.NoError(t, err)
	assert.Len(t, results, 1, "its results are kept")
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

// A split with no rows in the run's dataset version is refused when the run is checked,
// not found by planning after the run was stored.
func TestValidateRefusesAnEmptySplit(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "empty split", [3]string{"a", "same", "same"})
	split, err := h.datasets.CreateSplit(ctx, dataset.ID, datasets.SplitInput{Name: "holdout"})
	require.NoError(t, err)
	in := RunInput{Name: "empty split", DatasetID: dataset.ID, SplitID: split.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}}
	check, err := h.svc.Validate(ctx, in)
	require.NoError(t, err)
	assert.False(t, check.Valid)
	assert.Contains(t, check.Problems, "the split has no rows in this version of the dataset")
	_, err = h.svc.Create(ctx, in)
	require.True(t, evals.IsInvalid(err), "%v", err)
	runs, _, err := h.store.ListEvalRuns(ctx, configstore.EvalRunListParams{})
	require.NoError(t, err)
	assert.Empty(t, runs, "no run is stored")
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
		ID: "00000000-0000-0000-0000-000000000000", Name: "local", Status: tables.EvalRunRunning, TargetType: string(TargetDataset), DatasetID: h.bareDataset(t), DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, local))
	model := &tables.TableEvalRun{
		ID: "ffffffff-ffff-ffff-ffff-ffffffffffff", Name: "model", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: h.bareDataset(t), DatasetVersion: 1,
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
	stale, _, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, nil, evals.JobCursor{}, 10)
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
		Name: "judged", Status: tables.EvalRunRunning, TargetType: string(TargetDataset), DatasetID: h.bareDataset(t), DatasetVersion: 1,
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
	stale, _, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, nil, evals.JobCursor{}, 10)
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

// A rerun whose queue walk could not be queued has already reset its entries and deleted
// their results, so it goes ahead: the caller gets the running run, and the sweep queues
// the reset entries without the walk.
func TestRerunWhoseQueueWalkFailsStillGoesAhead(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "walkless", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "walkless", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	h.useRunner(t, stubRunner{h: h, submit: func(_ context.Context, job *evals.Job) error {
		if strings.HasPrefix(job.ID, queueJobPrefix) {
			return errors.New("queue is down")
		}
		return nil
	}})
	view, err := h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err, "the reset cannot be undone, so the rerun is reported as started")
	require.NotNil(t, view)
	assert.Equal(t, tables.EvalRunRunning, view.Status)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryQueued: 2}, view.Counts)

	future := time.Now().Add(time.Hour)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	stale, _, err := jobs.StaleJobs(ctx, future, future, nil, evals.JobCursor{}, 10)
	require.NoError(t, err)
	refs, err := h.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{RunID: created.ID})
	require.NoError(t, err)
	ids := make([]string, 0, len(stale))
	for _, job := range stale {
		ids = append(ids, job.ID)
	}
	for _, ref := range refs {
		assert.Contains(t, ids, targetJobID(ref.ID, 2), "the sweep queues the reset entry")
	}
}

// A rerun's queue walk that is still submitting when a Stop and a second rerun reset the
// entry does not fail the second rerun's entry with its stale Submit error: the failure
// write is fenced on the walk's generation, and the newer rerun owns the entry.
func TestStaleQueueWalkDoesNotFailANewerRerunsEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "stale walk", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "stale walk", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)

	var mu sync.Mutex
	var walks []string
	entered := make(chan struct{})
	release := make(chan struct{})
	var blocked atomic.Bool
	h.useRunner(t, stubRunner{h: h, submit: func(_ context.Context, job *evals.Job) error {
		if strings.HasPrefix(job.ID, queueJobPrefix) {
			mu.Lock()
			walks = append(walks, job.ID)
			mu.Unlock()
			return nil
		}
		if blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
			return fmt.Errorf("%w: no key for this provider", evals.ErrNoEvalKey)
		}
		return nil
	}})
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)
	mu.Lock()
	require.Len(t, walks, 1)
	first := walks[0]
	mu.Unlock()

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	walk, err := jobs.LoadJob(ctx, first)
	require.NoError(t, err)
	walked := make(chan error, 1)
	go func() { walked <- finish(ctx, jobs, walk) }()
	<-entered
	_, err = h.svc.Stop(ctx, created.ID)
	require.NoError(t, err)
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)
	close(release)
	require.NoError(t, <-walked)

	entry := h.entries(t, created.ID, EntryQuery{}).Entries[0]
	assert.Contains(t, []string{tables.EvalRunEntryQueued, tables.EvalRunEntryRunning}, entry.Status,
		"the second rerun's entry is not failed by the first walk's stale error: %s", entry.Error)
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
	// The runner is stopped before the swap: a worker can still be finishing the run that
	// await saw completed, and it reads the store field. The calls below are refusals on the
	// stored run state and need no worker; the cleanup's Close is then a no-op.
	h.stopRunner(t)
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
		Name: "routed", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: h.bareDataset(t), DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"model","provider":"openai","model":"gpt-4o"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	entries := []tables.TableEvalRunEntry{{RunID: run.ID, Seq: 1, DatasetRowID: "row", Status: tables.EvalRunEntryQueued, QueuedAt: time.Now().Add(-time.Hour)}}
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, entries))
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	moved, err := jobs.RouteJob(ctx, targetJobID(entries[0].ID, 1), "", "k", time.Now())
	require.NoError(t, err)
	require.True(t, moved)

	future := time.Now().Add(time.Minute)
	stale, _, err := jobs.StaleJobs(ctx, future, future, []string{"k"}, evals.JobCursor{}, 10)
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

// A sweep pass whose result query fails keeps the entries its entry query already read:
// the entry cursor has moved past them, so dropping them would skip them for a lap, and a
// result query that keeps failing would never hand them to the runner. The sweep reports
// the error and still republishes the entry.
func TestSweepKeepsEntriesWhenTheResultQueryFails(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{h.definition(t, h.exactMatch(t))}, tables.EvalRunEntryQueued)
	routedAt := time.Now().Add(-2 * time.Hour)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"key_id": "k1", "routed_at": routedAt, "queued_at": routedAt})
	require.NoError(t, err)
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, listResultRefs: func(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error) {
			if !params.RoutedBefore.IsZero() || !params.StartedBefore.IsZero() {
				return nil, errors.New("result query failed")
			}
			return real.ListEvalRunResultRefs(ctx, params)
		}}
	})

	err = h.svc.runner.(*evals.Runner).Sweep(ctx)
	require.Error(t, err, "the failed result query is reported")
	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 1}, view.Counts, "the entry read before the failure was republished")
}

// cursorRecorder is an execution job store that records the cursors StaleJobs is asked for.
type cursorRecorder struct {
	evals.JobStore
	mu    sync.Mutex
	asked []evals.JobCursor
}

func (c *cursorRecorder) StaleJobs(ctx context.Context, queuedBefore, routedBefore time.Time, skipKeys []string, after evals.JobCursor, limit int) ([]*evals.Job, evals.JobCursor, error) {
	c.mu.Lock()
	c.asked = append(c.asked, after)
	c.mu.Unlock()
	return c.JobStore.StaleJobs(ctx, queuedBefore, routedBefore, skipKeys, after, limit)
}

// While the run queries keep failing, the lap-end retry of the run passes must still hand
// the sweep a cursor that is not the zero one: with no jobs and a zero cursor, Sweep returns
// before the purge and keeps the old cursor, so the execution walk and the purge would
// never run again. The sweep after the retry reads the executions from the start.
func TestStaleJobsKeepsTheExecutionWalkWhileTheRunPassesFail(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useStore(t, func(real Store) Store {
		return hookStore{
			Store: real,
			listEntryRefs: func(context.Context, configstore.EvalRunEntryListParams) ([]configstore.EvalRunEntryRef, error) {
				return nil, errors.New("entry query failed")
			},
			listResultRefs: func(context.Context, configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error) {
				return nil, errors.New("result query failed")
			},
		}
	})
	inner := &cursorRecorder{JobStore: evals.NewExecutionJobStore(h.store)}
	jobs := NewJobStore(inner, h.svc)
	future := time.Now().Add(time.Minute)

	_, first, err := jobs.StaleJobs(ctx, future, future, nil, evals.JobCursor{}, 10)
	require.Error(t, err, "the lap end reports the failed run passes")
	require.NotEqual(t, evals.JobCursor{}, first, "the lap end has the next sweep retry the run passes")

	stale, second, err := jobs.StaleJobs(ctx, future, future, nil, first, 10)
	require.Error(t, err, "the retry reports the failed run passes")
	require.Empty(t, stale)
	require.NotEqual(t, evals.JobCursor{}, second, "a failed retry must not end the lap with nothing read: the sweep would stop before its purge")

	_, _, _ = jobs.StaleJobs(ctx, future, future, nil, second, 10)
	inner.mu.Lock()
	defer inner.mu.Unlock()
	require.Len(t, inner.asked, 2, "the sweep after the retry reads the executions again")
	assert.Equal(t, evals.JobCursor{}, inner.asked[1], "from the start, with a cursor the execution store understands")
}

// A second message for the same entry is a different delivery and is refused, so the
// target is called once. A redelivery of the first message is claimed again.
func TestStartJobHonoursTheDeliveryClaim(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run := &tables.TableEvalRun{
		Name: "claimed", Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: h.bareDataset(t), DatasetVersion: 1,
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
	resetEntries           func(ctx context.Context, runID string, generation int64, ids []string, fields map[string]any) (int64, error)
	transitionResult       func(ctx context.Context, id string, from []string, fields map[string]any) (bool, error)
	runState               func(ctx context.Context, id string) (*configstore.EvalRunState, error)
	hasOpen                func(ctx context.Context, runID string) (bool, error)
	getRun                 func(ctx context.Context, id string) (*tables.TableEvalRun, error)
	fencedRunWrite         func() // called before a write fenced on an earlier read of the run
	entriesAtGeneration    func(ctx context.Context, runID string, generation int64, runFields map[string]any, from []string, entryFields map[string]any) (int64, error)
	listResultRefs         func(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error)
	stoppedEntries         func(ctx context.Context, runID string, generation int64, from []string, idleEvaluating bool, entryFields map[string]any) (int64, error)
	afterRunClaim          func(ctx context.Context, fields map[string]any) // called after a fenced run write that wrote
	claimRun               func(ctx context.Context, id string, runFrom []string, generation int64, runFields map[string]any, entries *configstore.EvalRunEntryUpdate) (bool, error)
	runAtGeneration        func(ctx context.Context, id string, from []string, generation int64, fields map[string]any) (bool, error)
	entryAtRun             func(ctx context.Context, runID, status string, generation int64, id string, from []string, fields map[string]any) (bool, error)
	entryAtReview          func(ctx context.Context, runID string, reviewSeq, generation int64, id string, attempt int, from []string, fields map[string]any) (bool, error)
	listEntries            func(ctx context.Context, params configstore.EvalRunEntryListParams) ([]tables.TableEvalRunEntry, int64, error)
	listEntryRefs          func(ctx context.Context, params configstore.EvalRunEntryListParams) ([]configstore.EvalRunEntryRef, error)
	entriesAtRun           func(ctx context.Context, runID, status string, generation int64, from []string, fields map[string]any) (int64, error)
	createResults          func(ctx context.Context, results []tables.TableEvalRunResult, entryAttempt int) error
	createEntries          func(ctx context.Context, entries []tables.TableEvalRunEntry) error
	countEntries           func(ctx context.Context, runID string) (map[string]int64, error)
	resultUnstarted        func(ctx context.Context, id string, attempt int, fields map[string]any) (bool, error)
	runAtReview            func(ctx context.Context, id string, from []string, generation, reviewSeq int64, fields map[string]any) (bool, error)
}

func (s hookStore) TransitionEvalRunResultUnstarted(ctx context.Context, id string, attempt int, fields map[string]any) (bool, error) {
	if s.resultUnstarted != nil {
		return s.resultUnstarted(ctx, id, attempt, fields)
	}
	return s.Store.TransitionEvalRunResultUnstarted(ctx, id, attempt, fields)
}

func (s hookStore) CountEvalRunEntries(ctx context.Context, runID string) (map[string]int64, error) {
	if s.countEntries != nil {
		return s.countEntries(ctx, runID)
	}
	return s.Store.CountEvalRunEntries(ctx, runID)
}

func (s hookStore) CreateEvalRunEntries(ctx context.Context, entries []tables.TableEvalRunEntry) error {
	if s.createEntries != nil {
		return s.createEntries(ctx, entries)
	}
	return s.Store.CreateEvalRunEntries(ctx, entries)
}

func (s hookStore) CreateEvalRunResults(ctx context.Context, results []tables.TableEvalRunResult, entryAttempt int) error {
	if s.createResults != nil {
		return s.createResults(ctx, results, entryAttempt)
	}
	return s.Store.CreateEvalRunResults(ctx, results, entryAttempt)
}

func (s hookStore) TransitionEvalRunEntriesAtRun(ctx context.Context, runID, status string, generation int64, from []string, fields map[string]any) (int64, error) {
	if s.entriesAtRun != nil {
		return s.entriesAtRun(ctx, runID, status, generation, from, fields)
	}
	return s.Store.TransitionEvalRunEntriesAtRun(ctx, runID, status, generation, from, fields)
}

func (s hookStore) ListEvalRunEntryRefs(ctx context.Context, params configstore.EvalRunEntryListParams) ([]configstore.EvalRunEntryRef, error) {
	if s.listEntryRefs != nil {
		return s.listEntryRefs(ctx, params)
	}
	return s.Store.ListEvalRunEntryRefs(ctx, params)
}

func (s hookStore) ListEvalRunEntries(ctx context.Context, params configstore.EvalRunEntryListParams) ([]tables.TableEvalRunEntry, int64, error) {
	if s.listEntries != nil {
		return s.listEntries(ctx, params)
	}
	return s.Store.ListEvalRunEntries(ctx, params)
}

func (s hookStore) TransitionEvalRunEntryAtRun(ctx context.Context, runID, status string, generation int64, id string, from []string, fields map[string]any) (bool, error) {
	if s.entryAtRun != nil {
		return s.entryAtRun(ctx, runID, status, generation, id, from, fields)
	}
	return s.Store.TransitionEvalRunEntryAtRun(ctx, runID, status, generation, id, from, fields)
}

func (s hookStore) TransitionEvalRunEntryAtReview(ctx context.Context, runID string, reviewSeq, generation int64, id string, attempt int, from []string, fields map[string]any) (bool, error) {
	if s.entryAtReview != nil {
		return s.entryAtReview(ctx, runID, reviewSeq, generation, id, attempt, from, fields)
	}
	return s.Store.TransitionEvalRunEntryAtReview(ctx, runID, reviewSeq, generation, id, attempt, from, fields)
}

func (s hookStore) ClaimEvalRunWithEntries(ctx context.Context, id string, runFrom []string, generation int64, runFields map[string]any, entries *configstore.EvalRunEntryUpdate) (bool, error) {
	if s.fencedRunWrite != nil {
		s.fencedRunWrite()
	}
	claim := s.Store.ClaimEvalRunWithEntries
	if s.claimRun != nil {
		claim = s.claimRun
	}
	claimed, err := claim(ctx, id, runFrom, generation, runFields, entries)
	if err == nil && claimed && s.afterRunClaim != nil {
		s.afterRunClaim(ctx, runFields)
	}
	return claimed, err
}

func (s hookStore) TransitionStoppedEvalRunEntries(ctx context.Context, runID string, generation int64, from []string, idleEvaluating bool, entryFields map[string]any) (int64, error) {
	if s.stoppedEntries != nil {
		return s.stoppedEntries(ctx, runID, generation, from, idleEvaluating, entryFields)
	}
	return s.Store.TransitionStoppedEvalRunEntries(ctx, runID, generation, from, idleEvaluating, entryFields)
}

func (s hookStore) ListEvalRunResultRefs(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error) {
	if s.listResultRefs != nil {
		return s.listResultRefs(ctx, params)
	}
	return s.Store.ListEvalRunResultRefs(ctx, params)
}

func (s hookStore) TransitionEvalRunAtGeneration(ctx context.Context, id string, from []string, generation int64, fields map[string]any) (bool, error) {
	if s.fencedRunWrite != nil {
		s.fencedRunWrite()
	}
	write := s.Store.TransitionEvalRunAtGeneration
	if s.runAtGeneration != nil {
		write = s.runAtGeneration
	}
	wrote, err := write(ctx, id, from, generation, fields)
	if err == nil && wrote && s.afterRunClaim != nil {
		s.afterRunClaim(ctx, fields)
	}
	return wrote, err
}

func (s hookStore) TransitionEvalRunAtReview(ctx context.Context, id string, from []string, generation, reviewSeq int64, fields map[string]any) (bool, error) {
	if s.fencedRunWrite != nil {
		s.fencedRunWrite()
	}
	if s.runAtReview != nil {
		return s.runAtReview(ctx, id, from, generation, reviewSeq, fields)
	}
	return s.Store.TransitionEvalRunAtReview(ctx, id, from, generation, reviewSeq, fields)
}

func (s hookStore) TransitionEvalRunEntriesAtGeneration(ctx context.Context, runID string, generation int64, runFields map[string]any, from []string, entryFields map[string]any) (int64, error) {
	if s.entriesAtGeneration != nil {
		return s.entriesAtGeneration(ctx, runID, generation, runFields, from, entryFields)
	}
	return s.Store.TransitionEvalRunEntriesAtGeneration(ctx, runID, generation, runFields, from, entryFields)
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

func (s hookStore) ResetEvalRunEntries(ctx context.Context, runID string, generation int64, ids []string, fields map[string]any) (int64, error) {
	if s.resetEntries != nil {
		return s.resetEntries(ctx, runID, generation, ids, fields)
	}
	return s.Store.ResetEvalRunEntries(ctx, runID, generation, ids, fields)
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

func (r stubRunner) Run(ctx context.Context, def evals.Definition, input evals.Input) evals.Result {
	return r.h.engine.Run(ctx, def, input)
}

func (r stubRunner) Keys(context.Context) ([]evals.EvalKey, error) { return *r.h.keys.Load(), nil }

// useStore swaps the service's store for the test. The field is read without a lock by
// every worker, so the swap first waits for the runner to go quiet: a run that reached its
// final status can still have a worker finishing the same run (a second entry's
// maybeFinalize, the summary), and a swap under it is a data race. The queue acknowledges a
// delivery only after its handler returned, under the queue's lock, so no message leased
// means no handler is reading the store. A test that swaps while its own work is still
// waiting gives up the wait after a short bound. The swap is not undone: the harness is the
// test's own, and a write at cleanup would race the workers that Close is still stopping.
func (h *harness) useStore(t testing.TB, wrap func(real Store) Store) Store {
	t.Helper()
	h.quiesce(t)
	real := h.svc.store
	h.svc.store = wrap(real)
	return real
}

// quiesce waits, for at most a short bound, until no message of the runner's topics is
// waiting or held by a worker.
func (h *harness) quiesce(t testing.TB) {
	t.Helper()
	topics := []string{"evals.local", "evals.dead"}
	for _, key := range *h.keys.Load() {
		topics = append(topics, evalKeyTopic(key.ID))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		busy := false
		for _, topic := range topics {
			stats, err := h.queue.Stats(context.Background(), topic, "evals")
			if err == nil && (stats.Leased > 0 || stats.Pending > 0) {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// stopRunner closes the harness's runner, so no worker reads the service's fields after it.
func (h *harness) stopRunner(t testing.TB) {
	t.Helper()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, h.runner.Close(closeCtx))
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
		Name: "manual", Status: tables.EvalRunRunning, TargetType: string(parsed.Type), DatasetID: h.bareDataset(t), DatasetVersion: 1,
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

// An entry whose output is stored but whose results could not be created stays evaluating
// and the delivery is not acknowledged: the redelivered target job finishes its results
// without calling the target again. Failing the entry instead would let results queued
// meanwhile finish against a failed entry, and a rerun call the target for a stored output.
func TestEntryWhoseResultsFailToStartStaysEvaluating(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	_, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryQueued)
	var createFailed, listFailed atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			createResults: func(ctx context.Context, results []tables.TableEvalRunResult, entryAttempt int) error {
				if createFailed.CompareAndSwap(false, true) {
					return errors.New("database is locked")
				}
				return real.CreateEvalRunResults(ctx, results, entryAttempt)
			},
			listResultRefs: func(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error) {
				if listFailed.CompareAndSwap(false, true) {
					return nil, errors.New("database is locked")
				}
				return real.ListEvalRunResultRefs(ctx, params)
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
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryEvaluating, entry.Status, "the entry keeps waiting for its results")
	assert.NotEmpty(t, entry.Output, "with its output")

	job, err = jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	require.False(t, job.Done)
	require.NoError(t, finish(ctx, jobs, job))
	require.Eventually(t, func() bool {
		entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
		return err == nil && entry.Status == tables.EvalRunEntryCompleted
	}, 10*time.Second, 5*time.Millisecond, "the redelivery completes the entry")
	h.mu.Lock()
	defer h.mu.Unlock()
	assert.Len(t, h.called, 1, "the target was called once")
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
		return hookStore{Store: real, resultUnstarted: func(context.Context, string, int, map[string]any) (bool, error) {
			return false, errors.New("database is locked")
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, runJobID(scoreJobPrefix, run.ID, run.Generation))
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

// The finalizer reads the run's state before it checks for open entries. A finalize and
// a rerun claim that land between the two reads must fence its final write out, instead
// of an open check from the old operation and a fence from the new one.
func TestFinalizeReadsTheRunBeforeItsOpenCheck(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "fenced order", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "fenced order", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	// The run as it is just before it is finalized: running, with nothing open.
	_, err = h.store.TransitionEvalRun(ctx, created.ID, nil, map[string]any{"status": tables.EvalRunRunning})
	require.NoError(t, err)
	h.useRunner(t, stubRunner{h: h})

	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, hasOpen: func(ctx context.Context, runID string) (bool, error) {
			if once.CompareAndSwap(false, true) {
				// Another worker finalizes the run, and a rerun claims it.
				_, finishErr := real.TransitionEvalRun(ctx, runID, nil, map[string]any{"status": tables.EvalRunCompleted})
				require.NoError(t, finishErr)
				_, rerunErr := h.svc.Rerun(ctx, runID, RerunInput{Scope: RerunAll})
				require.NoError(t, rerunErr)
				return false, nil
			}
			return real.HasOpenEvalRunEntries(ctx, runID)
		}}
	})
	require.NoError(t, h.svc.maybeFinalize(ctx, created.ID))
	state, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, state.Status, "the rerun is still running")
	counts, err := h.store.CountEvalRunEntries(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryQueued: 1}, counts)
}

// The dead letter of a walk that failed every delivery fails the run, through the
// runner's dead topic: the walk keeps no cursor, so the run must not stay running.
func TestDeadLetterOfAWalkThroughTheQueueFailsTheRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	payload, err := json.Marshal(map[string]string{"job_id": runJobID(scoreJobPrefix, run.ID, run.Generation)})
	require.NoError(t, err)
	require.NoError(t, h.queue.Publish(ctx, &queue.Message{ID: "dead-walk", Topic: "evals.dead", Payload: payload}))
	h.await(t, run.ID, tables.EvalRunFailed)
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Error, "evaluation failed after")
}

// deadLetter publishes a job's dead letter on the dead topic, as the queue does for a
// message that failed every attempt.
func (h *harness) deadLetter(t testing.TB, id, jobID string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"job_id": jobID})
	require.NoError(t, err)
	require.NoError(t, h.queue.Publish(context.Background(), &queue.Message{ID: id, Topic: "evals.dead", Payload: payload}))
}

// The dead letter of a target job whose entry already ended at its attempt finishes the
// run: the job only finishes the run, and nothing else lists a run whose entries all ended.
func TestDeadLetterOfAFinalizeJobFinishesTheRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryCompleted)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	h.deadLetter(t, "dead-finalize", targetJobID(entries[0].ID, 1))
	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.NotNil(t, view.EndedAt)
}

// The dead letter of a queued target job whose end fails after CompleteQueuedJob claimed
// the entry is retried, and the retry ends it: the entry is running under that claim, which
// is not a start any delivery owns, so the retry must take the queued path again and not
// be acknowledged as the dead letter of someone else's start.
func TestDeadLetterRetryEndsAQueuedTargetAfterAFailedWrite(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryQueued)
	resultFails, entryFails := 0, 1
	h.useStore(t, func(real Store) Store {
		return failOnceStore{Store: real, resultFails: &resultFails, entryFails: &entryFails}
	})
	h.deadLetter(t, "dead-queued-target", targetJobID(entries[0].ID, 1))
	require.Eventually(t, func() bool {
		entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
		return err == nil && entry.Status == tables.EvalRunEntryFailed
	}, 5*time.Second, 5*time.Millisecond, "the retried dead letter ends the entry")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Contains(t, entry.Error, "evaluation failed after")
	view := h.await(t, run.ID, tables.EvalRunFailed) // its only entry failed, so the run finishes failed
	assert.NotNil(t, view.EndedAt)
}

// The result twin of TestDeadLetterRetryEndsAQueuedTargetAfterAFailedWrite.
func TestDeadLetterRetryEndsAQueuedResultAfterAFailedWrite(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	installed, err := h.evaluators.Install(ctx, evals.BuiltinSemanticSimilarity, evals.InstallInput{Provider: "openai", Model: "text-embedding-3-small"})
	require.NoError(t, err)
	similarity := h.definition(t, EvaluatorRef{ID: installed.ID})
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{similarity}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: similarity.ID, Status: tables.EvalRunResultQueued}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	resultFails, entryFails := 1, 0
	h.useStore(t, func(real Store) Store {
		return failOnceStore{Store: real, resultFails: &resultFails, entryFails: &entryFails}
	})
	h.deadLetter(t, "dead-queued-result", resultJobID(results[0].ID, 1))
	require.Eventually(t, func() bool {
		result, err := h.store.GetEvalRunResult(ctx, results[0].ID)
		return err == nil && result.Status == tables.EvalRunResultFailed
	}, 5*time.Second, 5*time.Millisecond, "the retried dead letter ends the result")
	result, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "evaluation failed after")
	h.await(t, run.ID, tables.EvalRunCompleted)
}

// The dead letter of a plan job ends the run failed with the reason: a queue that keeps
// its messages has nothing else that would plan the run or fail it.
func TestDeadLetterOfAPlanJobFailsTheRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	evaluatorsJSON, err := json.Marshal([]evals.Definition{exact})
	require.NoError(t, err)
	run := &tables.TableEvalRun{
		Name: "dead plan", Status: tables.EvalRunQueued, TargetType: string(TargetDataset), DatasetID: h.bareDataset(t), DatasetVersion: 1,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: evaluatorsJSON,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	h.deadLetter(t, "dead-plan", planJobID(run.ID))
	h.await(t, run.ID, tables.EvalRunFailed)
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Error, "evaluation failed after")
}

// The dead letter of a walk job of a stopped run whose cleanup never finished ends the
// stop: the run gets its end time and summary.
func TestDeadLetterOfAWalkOfAStoppedRunFinishesTheStop(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryStopped)
	_, err := h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped})
	require.NoError(t, err)
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	h.deadLetter(t, "dead-stopped-walk", runJobID(scoreJobPrefix, run.ID, state.Generation))
	require.Eventually(t, func() bool {
		stored, err := h.store.GetEvalRun(ctx, run.ID)
		return err == nil && stored.EndedAt != nil && len(stored.Summary) > 0
	}, 3*time.Second, 5*time.Millisecond, "the stopped run gets its end time and summary")
}

// A plan job redelivered after the run's start time was written, by a worker that died
// before the run was finished, finishes the run: the plan's last finish is the only one a
// run whose entries all ended before planning did gets.
func TestPlanRedeliveryAfterTheStartTimeFinishesTheRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryCompleted)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, planJobID(run.ID))
	require.NoError(t, err)
	require.False(t, job.Done, "the run is not finished, so its plan job has work left")
	require.NoError(t, finish(ctx, jobs, job))
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status)
}

// A plan whose last finish fails is delivered again, not failed: its entries all ended,
// and one failed write must not fail the run.
func TestPlanFinishFailureIsRetriedNotFailed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "finish fails", [3]string{"a", "same", "same"})
	saved, err := h.datasets.SaveVersion(ctx, dataset.ID, "", "", "tester")
	require.NoError(t, err)
	exact := h.definition(t, h.exactMatch(t))
	evaluatorsJSON, err := json.Marshal([]evals.Definition{exact})
	require.NoError(t, err)
	run := &tables.TableEvalRun{
		Name: "finish fails", Status: tables.EvalRunQueued, TargetType: string(TargetDataset), DatasetID: dataset.ID, DatasetVersion: saved.Number,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: evaluatorsJSON,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	h.useRunner(t, stubRunner{h: h})
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			// The entries end before planning does, as fast workers can make them.
			createEntries: func(ctx context.Context, entries []tables.TableEvalRunEntry) error {
				for i := range entries {
					entries[i].Status = tables.EvalRunEntryCompleted
					entries[i].Output = "same"
				}
				return real.CreateEvalRunEntries(ctx, entries)
			},
			runAtReview: func(ctx context.Context, id string, from []string, generation, reviewSeq int64, fields map[string]any) (bool, error) {
				if failedOnce.CompareAndSwap(false, true) {
					return false, errors.New("database is locked")
				}
				return real.TransitionEvalRunAtReview(ctx, id, from, generation, reviewSeq, fields)
			},
		}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, planJobID(run.ID))
	require.NoError(t, err)
	require.Error(t, finish(ctx, jobs, job), "the failed finish has the delivery retried")
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunRunning, state.Status, "one failed finish does not fail the run")

	job, err = jobs.LoadJob(ctx, planJobID(run.ID))
	require.NoError(t, err)
	require.False(t, job.Done)
	require.NoError(t, finish(ctx, jobs, job))
	state, err = h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status)
}

// A resume of a queued run whose plan's last finish fails is delivered again, not failed,
// as a plan job is (TestPlanFinishFailureIsRetriedNotFailed): planning finished, and one
// failed write must not fail the run. The redelivery finds the start time, skips planning
// and finishes the run.
func TestResumePlanFinishFailureIsRetriedNotFailed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "resume finish fails", [3]string{"a", "same", "same"})
	saved, err := h.datasets.SaveVersion(ctx, dataset.ID, "", "", "tester")
	require.NoError(t, err)
	exact := h.definition(t, h.exactMatch(t))
	evaluatorsJSON, err := json.Marshal([]evals.Definition{exact})
	require.NoError(t, err)
	run := &tables.TableEvalRun{
		Name: "resume finish fails", Status: tables.EvalRunQueued, TargetType: string(TargetDataset), DatasetID: dataset.ID, DatasetVersion: saved.Number,
		Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: evaluatorsJSON,
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	h.useRunner(t, stubRunner{h: h})
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			createEntries: func(ctx context.Context, entries []tables.TableEvalRunEntry) error {
				for i := range entries {
					entries[i].Status = tables.EvalRunEntryCompleted
					entries[i].Output = "same"
				}
				return real.CreateEvalRunEntries(ctx, entries)
			},
			runAtReview: func(ctx context.Context, id string, from []string, generation, reviewSeq int64, fields map[string]any) (bool, error) {
				if failedOnce.CompareAndSwap(false, true) {
					return false, errors.New("database is locked")
				}
				return real.TransitionEvalRunAtReview(ctx, id, from, generation, reviewSeq, fields)
			},
		}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, resumeJobID(run.ID, run.Generation))
	require.NoError(t, err)
	require.Error(t, finish(ctx, jobs, job), "the failed finish has the delivery retried")
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunRunning, state.Status, "one failed finish does not fail the run")

	job, err = jobs.LoadJob(ctx, resumeJobID(run.ID, run.Generation))
	require.NoError(t, err)
	require.False(t, job.Done)
	require.NoError(t, finish(ctx, jobs, job))
	state, err = h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status)
}

// A walk job of an older operation that arrives after a stop and a rerun does nothing: it
// would otherwise walk the rerun with the rerun's spec.
func TestOldWalkJobAfterAStopAndRerunIsDone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "old walk", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "old walk", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	h.useRunner(t, stubRunner{h: h})
	added := h.evaluator(t, "added", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(output, expected_output):\n    return output == expected_output\n","response_type":"yes_no"}`)
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{added})
	require.NoError(t, err)
	state, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	oldJob := runJobID(scoreJobPrefix, created.ID, state.Generation)
	_, err = h.svc.Stop(ctx, created.ID)
	require.NoError(t, err)
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, oldJob)
	require.NoError(t, err)
	assert.True(t, job.Done, "the old operation's walk has nothing to do")
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: created.ID, EvaluatorID: added.ID})
	require.NoError(t, err)
	assert.Empty(t, results)
}

// The dead letter of a whole-run walk ends the run failed with the reason: a walk keeps
// no cursor to resume from, and the run would otherwise stay running forever.
func TestDeadRunWalkFailsTheRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, runJobID(scoreJobPrefix, run.ID, run.Generation))
	require.NoError(t, err)
	require.NotNil(t, job.Complete)
	require.NoError(t, job.Complete(ctx, evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "evaluation failed after 3 attempts"}))
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, state.Status)
	stored, err := h.store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Error, "evaluation failed after 3 attempts")

	// The dead letter of a walk from before a stop and a rerun does not fail the rerun.
	h.useRunner(t, stubRunner{h: h})
	other, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	stale, err := jobs.LoadJob(ctx, runJobID(scoreJobPrefix, other.ID, other.Generation))
	require.NoError(t, err)
	require.NotNil(t, stale.Complete)
	_, err = h.svc.Stop(ctx, other.ID)
	require.NoError(t, err)
	_, err = h.svc.Rerun(ctx, other.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)
	require.NoError(t, stale.Complete(ctx, evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "an old walk failed"}))
	state, err = h.store.GetEvalRunState(ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, state.Status, "the rerun is not failed by an earlier walk's dead letter")

	// The rerun's operation is the run's current generation (the stop finalized the run,
	// its only entry being idle, so it is not a fixed offset from the start).
	rerunState, err := h.store.GetEvalRunState(ctx, other.ID)
	require.NoError(t, err)
	current, err := jobs.LoadJob(ctx, runJobID(scoreJobPrefix, other.ID, rerunState.Generation))
	require.NoError(t, err)
	require.NotNil(t, current.Complete)
	require.NoError(t, current.Complete(ctx, evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "the rerun's walk failed"}))
	state, err = h.store.GetEvalRunState(ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, state.Status, "the dead letter of the current walk still fails the run")

	// A dead letter whose failure could not be stored is not acknowledged: the dead topic
	// delivers it again until storage recovers.
	dead := evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "the walk failed every delivery"}
	var runWriteFailed, entriesFailed atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			runAtGeneration: func(ctx context.Context, id string, from []string, generation int64, fields map[string]any) (bool, error) {
				if fields["status"] == tables.EvalRunFailed && runWriteFailed.CompareAndSwap(false, true) {
					return false, errors.New("database is locked")
				}
				return real.TransitionEvalRunAtGeneration(ctx, id, from, generation, fields)
			},
		}
	})
	unwritten, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryQueued)
	job, err = jobs.LoadJob(ctx, runJobID(scoreJobPrefix, unwritten.ID, unwritten.Generation))
	require.NoError(t, err)
	require.Error(t, job.Complete(ctx, dead), "a failure that was not stored is retried")
	require.NoError(t, job.Complete(ctx, dead))
	state, err = h.store.GetEvalRunState(ctx, unwritten.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, state.Status, "the retry fails the run")

	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			entriesAtRun: func(ctx context.Context, runID, status string, generation int64, from []string, fields map[string]any) (int64, error) {
				if fields["status"] == tables.EvalRunEntryStopped && entriesFailed.CompareAndSwap(false, true) {
					return 0, errors.New("database is locked")
				}
				return real.TransitionEvalRunEntriesAtRun(ctx, runID, status, generation, from, fields)
			},
		}
	})
	halfWritten, _ := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryQueued)
	job, err = jobs.LoadJob(ctx, runJobID(scoreJobPrefix, halfWritten.ID, halfWritten.Generation))
	require.NoError(t, err)
	require.Error(t, job.Complete(ctx, dead), "entries left waiting have the dead letter retried")
	require.NoError(t, job.Complete(ctx, dead))
	counts, err := h.store.CountEvalRunEntries(ctx, halfWritten.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryStopped: 1}, counts, "the retry stops the waiting entries of the failed run")
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

// A plan that fails and cannot store the failure returns the error from Complete, so the
// message is delivered again: on a queue that keeps its messages nothing else would plan
// or fail the run. The redelivery stores the failure.
func TestPlanFailureThatCannotBeStoredIsRedelivered(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run := &tables.TableEvalRun{
		Name: "unplannable", Status: tables.EvalRunQueued, TargetType: string(TargetDataset), DatasetID: h.bareDataset(t), DatasetVersion: 1,
		SplitID: new("no-such-split"), Target: json.RawMessage(`{"type":"dataset"}`), Evaluators: json.RawMessage(`[]`),
	}
	require.NoError(t, h.store.CreateEvalRun(ctx, run))
	var failures atomic.Int32
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, runAtGeneration: func(ctx context.Context, id string, from []string, generation int64, fields map[string]any) (bool, error) {
			if fields["status"] == tables.EvalRunFailed && failures.Add(1) == 1 {
				return false, errors.New("store is down")
			}
			return real.TransitionEvalRunAtGeneration(ctx, id, from, generation, fields)
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, planJobID(run.ID))
	require.NoError(t, err)
	require.NotNil(t, job.Exec)
	result := job.Exec(ctx)
	require.NotNil(t, job.Complete, "a plan job reports a failure it could not store")
	require.Error(t, job.Complete(ctx, result))
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunQueued, state.Status)

	again, err := jobs.LoadJob(ctx, planJobID(run.ID))
	require.NoError(t, err)
	require.NotNil(t, again.Exec)
	result = again.Exec(ctx)
	require.NoError(t, again.Complete(ctx, result))
	state, err = h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, state.Status)
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

	// A negative prompt version is refused too, not read as the latest.
	prompt := &tables.TablePrompt{ID: "prompt-neg", Name: "neg"}
	require.NoError(t, h.store.CreatePrompt(ctx, prompt))
	require.NoError(t, h.store.CreatePromptVersion(ctx, &tables.TablePromptVersion{
		PromptID: prompt.ID, CommitMessage: "v1", Provider: "openai", Model: "gpt-4o-mini",
		Messages: []tables.TablePromptVersionMessage{{PromptID: prompt.ID, OrderIndex: 0, Message: json.RawMessage(`{"role":"user","content":"{{input}}"}`)}},
	}))
	check, err = h.svc.Validate(ctx, RunInput{Name: "neg", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact},
		Target: Target{Type: TargetPrompt, PromptID: prompt.ID, PromptVersion: -1}})
	require.NoError(t, err)
	assert.False(t, check.Valid, "a negative prompt version is refused")
	assert.Contains(t, check.Problems, "prompt_version must be 0 or more")
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

	// A rename between the claim and the failed queueing is not a new operation: the undo
	// still applies, and the new name is kept.
	h.useRunner(t, stubRunner{h: h, submit: func(ctx context.Context, _ *evals.Job) error {
		_, renameErr := h.svc.Update(ctx, created.ID, UpdateInput{Name: "renamed"})
		require.NoError(t, renameErr)
		return errors.New("queue is down")
	}})
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{h.evaluator(t, "second judge", evals.EvaluatorTypeAI, `{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"gpt-4o"}`)})
	require.Error(t, err)
	after, err = h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, after.Status, "a rename does not block the undo")
	assert.Equal(t, "renamed", after.Name)
	counts, err = h.store.CountEvalRunEntries(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 2}, counts)

	// An undo that cannot be written is reported, with the way out, next to the publish error.
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, entriesAtGeneration: func(context.Context, string, int64, map[string]any, []string, map[string]any) (int64, error) {
			return 0, errors.New("store is down")
		}}
	})
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error { return errors.New("queue is down") }})
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{h.evaluator(t, "third judge", evals.EvaluatorTypeAI, `{"instructions":"grade","grading_style":"yes_no","provider":"openai","model":"gpt-4o"}`)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue is down")
	assert.Contains(t, err.Error(), "could not be put back as it was; stop it to end it")
	assert.Contains(t, err.Error(), "store is down")
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

// A review rebuilds a finished run's summary with a write fenced on the generation it read
// with the run. When another summary write moved the generation since that read, with the
// run still finished, the review's write must match nothing and keep the newer summary: it
// then reads the run again and rebuilds on top of it. A rerun that claimed the run since
// the read is running with no summary, and the same fence keeps the finished summary off it.
func TestReviewDoesNotSummarizeARerunItDidNotSee(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "review race", [3]string{"a", "same", "same"})
	human := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	created, err := h.svc.Create(ctx, RunInput{Name: "review race", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t), human}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	entry := h.entries(t, created.ID, EntryQuery{}).Entries[0]
	finished, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)

	const newer = `{"newer":true}`
	var once atomic.Bool
	var staleWrote bool
	var afterStale *string
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, runAtGeneration: func(ctx context.Context, id string, from []string, generation int64, fields map[string]any) (bool, error) {
			if _, summary := fields["summary_json"]; summary && generation == finished.Generation && once.CompareAndSwap(false, true) {
				// Another summary write lands first: the run stays completed at the next generation.
				_, moveErr := real.TransitionEvalRun(ctx, id, nil, map[string]any{"generation": generation + 1, "summary_json": newer})
				require.NoError(t, moveErr)
				wrote, err := real.TransitionEvalRunAtGeneration(ctx, id, from, generation, fields)
				staleWrote = wrote
				if stored, getErr := real.GetEvalRun(ctx, id); getErr == nil {
					afterStale = stored.SummaryJSON
				}
				return wrote, err
			}
			return real.TransitionEvalRunAtGeneration(ctx, id, from, generation, fields)
		}}
	})
	_, err = h.svc.Review(ctx, created.ID, entry.ID, human.ID, ReviewInput{Score: true}, "reviewer")
	require.NoError(t, err)
	require.True(t, once.Load(), "the review wrote the summary at the generation it read")
	assert.False(t, staleWrote, "the write at the old generation matches nothing")
	require.NotNil(t, afterStale)
	assert.Equal(t, newer, *afterStale, "the newer summary is kept by the stale write")
	stored, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, stored.Status)
	assert.Equal(t, finished.Generation+2, stored.Generation, "the review rebuilt the summary on top of the newer write")

	// A rerun that claimed the run right after the review read it gets no finished summary.
	entry = h.entries(t, created.ID, EntryQuery{}).Entries[0]
	var claimed atomic.Bool
	h.useStore(t, func(Store) Store {
		real := h.store
		return hookStore{Store: real, getRun: func(ctx context.Context, id string) (*tables.TableEvalRun, error) {
			run, err := real.GetEvalRun(ctx, id)
			if err == nil && claimed.CompareAndSwap(false, true) {
				_, claimErr := real.TransitionEvalRun(ctx, id, finishedRunStatuses, map[string]any{
					"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil, "error": "", "updated_at": time.Now(),
				})
				require.NoError(t, claimErr)
			}
			return run, err
		}}
	})
	_, err = h.svc.Review(ctx, created.ID, entry.ID, human.ID, ReviewInput{Score: false}, "reviewer")
	require.NoError(t, err)
	stored, err = h.store.GetEvalRun(ctx, created.ID)
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

// A late definite error from a duplicate submission of attempt N (a redelivered queueing
// job) must not fail a row that a retry has already queued for attempt N+1: the row is
// stored queued at attempt N, so only submissions above its stored attempt may fail it.
func TestLateSubmitFailureLeavesTheRetriedResultQueued(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	// Attempt 1 failed and was requeued: the row is queued at attempt 1, waiting for attempt 2.
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultQueued, Attempt: 1}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error { return evals.ErrNoEvalKey }})

	queued, err := h.svc.submitResult(ctx, exact, results[0].ID, 1)
	require.NoError(t, err)
	assert.False(t, queued)

	stored, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultQueued, stored.Status, "the late failure of attempt 1 leaves the row attempt 2 owns")
	assert.Equal(t, 1, stored.Attempt)
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

// The runner ends a job that never started (its key is gone, or its message went to the
// dead letter topic) only while it is still queued on the key it read. A result that
// another instance moved to a new key, or that a delivery started, is left to that route
// or claim, and so is a queued result of a later attempt. On the key it read, the result
// ends as CompleteJob ends it, and its entry completes.
func TestCompleteQueuedJobLeavesAMovedOrStartedResult(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	installed, err := h.evaluators.Install(ctx, evals.BuiltinSemanticSimilarity, evals.InstallInput{Provider: "openai", Model: "text-embedding-3-small"})
	require.NoError(t, err)
	similarity := h.definition(t, EvaluatorRef{ID: installed.ID})
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{similarity},
		tables.EvalRunEntryEvaluating, tables.EvalRunEntryEvaluating, tables.EvalRunEntryEvaluating, tables.EvalRunEntryEvaluating)
	results := make([]tables.TableEvalRunResult, len(entries))
	for i := range entries {
		results[i] = tables.TableEvalRunResult{RunID: run.ID, EntryID: entries[i].ID, EvaluatorID: similarity.ID, Status: tables.EvalRunResultQueued, KeyID: "k1"}
	}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	moved, started, later, current := results[0].ID, results[1].ID, results[2].ID, results[3].ID

	routed, err := h.store.RouteEvalRunResult(ctx, moved, "k1", "k2", time.Now())
	require.NoError(t, err)
	require.True(t, routed)
	claimed, err := h.store.ClaimEvalRunResult(ctx, started, "k1", "d1", 1, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = h.store.TransitionEvalRunResult(ctx, later, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	gone := evals.Result{Score: evals.ErrorScore(), Reasoning: "the key is gone"}
	for _, id := range []string{moved, started, later} {
		completed, err := jobs.CompleteQueuedJob(ctx, resultJobID(id, 1), "k1", gone)
		require.NoError(t, err)
		assert.False(t, completed, "result %s moved on and is left alone", id)
	}
	stored, err := h.store.GetEvalRunResult(ctx, moved)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultQueued, stored.Status, "the moved result waits on its new key")
	assert.Equal(t, "k2", stored.KeyID)
	stored, err = h.store.GetEvalRunResult(ctx, started)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultRunning, stored.Status, "the started result is left to its claim")
	assert.Equal(t, "d1", stored.ClaimID)
	assert.Empty(t, stored.Error)
	stored, err = h.store.GetEvalRunResult(ctx, later)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultQueued, stored.Status, "the queued result of a later attempt is left for its own job")

	completed, err := jobs.CompleteQueuedJob(ctx, resultJobID(current, 1), "k1", gone)
	require.NoError(t, err)
	assert.True(t, completed)
	stored, err = h.store.GetEvalRunResult(ctx, current)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultFailed, stored.Status)
	assert.Equal(t, "the key is gone", stored.Error)
	assert.Equal(t, 1, stored.Attempt, "the end is stored at the job's attempt")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[3].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status, "the entry has no result left open")

	again, err := jobs.CompleteQueuedJob(ctx, resultJobID(current, 1), "k1", gone)
	require.NoError(t, err)
	assert.False(t, again, "a result that already ended is not ended again")
}

// CompleteQueuedJob for a target job fails the waiting entry only while it is still
// queued on the key the runner read, at an attempt below the job's. An entry another
// instance moved to a new key, or that a delivery started, keeps its route or claim.
func TestCompleteQueuedJobLeavesAMovedOrStartedEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, nil,
		tables.EvalRunEntryQueued, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued)
	for i := range entries {
		routed, err := h.store.RouteEvalRunEntry(ctx, entries[i].ID, "", "k1", time.Now())
		require.NoError(t, err)
		require.True(t, routed)
	}
	moved, started, later, current := entries[0].ID, entries[1].ID, entries[2].ID, entries[3].ID
	routed, err := h.store.RouteEvalRunEntry(ctx, moved, "k1", "k2", time.Now())
	require.NoError(t, err)
	require.True(t, routed)
	claimed, err := h.store.ClaimEvalRunEntry(ctx, started, "k1", "d1", 1, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = h.store.TransitionEvalRunEntry(ctx, later, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	gone := evals.Result{Score: evals.ErrorScore(), Reasoning: "the key is gone"}
	for _, id := range []string{moved, started, later} {
		completed, err := jobs.CompleteQueuedJob(ctx, targetJobID(id, 1), "k1", gone)
		require.NoError(t, err)
		assert.False(t, completed, "entry %s moved on and is left alone", id)
	}
	stored, err := h.store.GetEvalRunEntry(ctx, moved)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryQueued, stored.Status, "the moved entry waits on its new key")
	assert.Equal(t, "k2", stored.KeyID)
	stored, err = h.store.GetEvalRunEntry(ctx, started)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryRunning, stored.Status, "the started entry is left to its claim")
	assert.Empty(t, stored.Error)
	stored, err = h.store.GetEvalRunEntry(ctx, later)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryQueued, stored.Status, "the queued entry of a later attempt is left for its own job")

	completed, err := jobs.CompleteQueuedJob(ctx, targetJobID(current, 1), "k1", gone)
	require.NoError(t, err)
	assert.True(t, completed)
	stored, err = h.store.GetEvalRunEntry(ctx, current)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryFailed, stored.Status)
	assert.Equal(t, "the key is gone", stored.Error)
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, state.Status, "the run waits for its other entries")

	again, err := jobs.CompleteQueuedJob(ctx, targetJobID(current, 1), "k1", gone)
	require.NoError(t, err)
	assert.False(t, again, "an entry that already failed is not failed again")
}

// failOnceStore fails the first write that ends a result (completeResult's attempt-fenced
// write) and the first write that fails an entry (failWaitingEntry's write).
type failOnceStore struct {
	Store
	resultFails, entryFails *int
}

func (s failOnceStore) TransitionEvalRunResultAttempt(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error) {
	if *s.resultFails > 0 {
		*s.resultFails--
		return false, errors.New("the store is unavailable")
	}
	return s.Store.TransitionEvalRunResultAttempt(ctx, id, attempt, from, fields)
}

func (s failOnceStore) TransitionEvalRunEntry(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
	if *s.entryFails > 0 {
		*s.entryFails--
		return false, errors.New("the store is unavailable")
	}
	return s.Store.TransitionEvalRunEntry(ctx, id, from, fields)
}

func (s failOnceStore) TransitionEvalRunEntryClaimed(ctx context.Context, id string, attempt int, claimID string, from []string, fields map[string]any) (bool, error) {
	if *s.entryFails > 0 {
		*s.entryFails--
		return false, errors.New("the store is unavailable")
	}
	return s.Store.TransitionEvalRunEntryClaimed(ctx, id, attempt, claimID, from, fields)
}

// When the write that ends a job fails after CompleteQueuedJob claimed it, the error has
// the runner try again (the dead letter is redelivered, the sweep reroutes again). That
// retry must finish the job: the row is running under CompleteQueuedJob's own claim at the
// job's attempt, which only that retry matches, so it is not stuck for ever on a key no
// lost-claim sweep looks at.
func TestCompleteQueuedJobRetryFinishesAfterAFailedWrite(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	installed, err := h.evaluators.Install(ctx, evals.BuiltinSemanticSimilarity, evals.InstallInput{Provider: "openai", Model: "text-embedding-3-small"})
	require.NoError(t, err)
	similarity := h.definition(t, EvaluatorRef{ID: installed.ID})
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{similarity}, tables.EvalRunEntryEvaluating, tables.EvalRunEntryQueued)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: similarity.ID, Status: tables.EvalRunResultQueued}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))

	resultFails, entryFails := 1, 1
	h.useStore(t, func(real Store) Store {
		return failOnceStore{Store: real, resultFails: &resultFails, entryFails: &entryFails}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	gone := evals.Result{Score: evals.ErrorScore(), Reasoning: "the key is gone"}

	for _, id := range []string{resultJobID(results[0].ID, 1), targetJobID(entries[1].ID, 1)} {
		_, err := jobs.CompleteQueuedJob(ctx, id, "", gone)
		require.Error(t, err, "%s: the failed write is reported", id)
		completed, err := jobs.CompleteQueuedJob(ctx, id, "", gone)
		require.NoError(t, err)
		assert.True(t, completed, "%s: the retry finishes the job", id)
	}
	result, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultFailed, result.Status)
	assert.Equal(t, "the key is gone", result.Error)
	entry, err := h.store.GetEvalRunEntry(ctx, entries[1].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryFailed, entry.Status)
	assert.Equal(t, "the key is gone", entry.Error)
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status, "with every entry ended, the run finishes: one entry completed, with a failed result")
}

// The sweep ends an unrouted model entry with an error when no eval key is left for it.
// When the write after CompleteQueuedJob's claim fails there, no delivery retries it: the
// entry is running under that claim on key "", which no lost-start pass ends. A later
// sweep must still end it, or the run never finishes.
func TestSweepEndsAnUnroutedEntryWhoseEndFailedAfterTheClaim(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.keys.Store(&[]evals.EvalKey{})
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{h.definition(t, h.exactMatch(t))}, tables.EvalRunEntryQueued)
	old := time.Now().Add(-2 * time.Hour)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"queued_at": old})
	require.NoError(t, err)
	resultFails, entryFails := 0, 1
	h.useStore(t, func(real Store) Store {
		return failOnceStore{Store: real, resultFails: &resultFails, entryFails: &entryFails}
	})
	sweeper := h.svc.runner.(*evals.Runner)
	require.Error(t, sweeper.Sweep(ctx), "the failed end is reported")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunEntryRunning, entry.Status, "the claim landed and the end did not")

	// Long after: the claim is far older than every sweep cutoff.
	_, err = h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"started_at": old})
	require.NoError(t, err)
	for range 3 {
		_ = sweeper.Sweep(ctx)
	}
	entry, err = h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryFailed, entry.Status, "a later sweep ends the entry (claim %q)", entry.ClaimID)
	h.await(t, run.ID, tables.EvalRunFailed)
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
	// The key is unmarked without a Sync: a Sync would close k-gone's subscription, its
	// topic being drained, and the message below is the one a durable queue still holds.
	h.keys.Store(&[]evals.EvalKey{{ID: "k1", Provider: "openai", Concurrency: 4}})
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

// A target job whose results could not be created on any delivery is dead-lettered, and
// the dead letter ends nothing for an entry that is not queued. An entry left evaluating
// with no open result, on the local topic or on a key that is still consumed, must still be
// found by the sweep and repaired from its stored output, or its run never finishes.
func TestSweepRepairsAnEntryLeftEvaluatingWithNoOpenResults(t *testing.T) {
	for _, keyID := range []string{"", "k1"} {
		t.Run("key "+keyID, func(t *testing.T) {
			ctx := context.Background()
			h := newHarness(t)
			run, _ := h.leftEvaluatingOn(t, keyID, time.Now().Add(-2*time.Hour))
			require.NoError(t, h.svc.runner.(*evals.Runner).Sweep(ctx))
			view := h.await(t, run.ID, tables.EvalRunCompleted)
			assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 1}, view.Counts)
		})
	}
}

// A rerun resets a page of entries in one write: when the reset fails, the results of the
// page are still there, and no entry is left completed with its results gone. The first
// page is reset in the claim's transaction, so the claim is undone with it.
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
			claimRun: func(ctx context.Context, id string, runFrom []string, generation int64, runFields map[string]any, entries *configstore.EvalRunEntryUpdate) (bool, error) {
				broken := *entries
				broken.Fields = maps.Clone(entries.Fields)
				broken.Fields["no_such_column"] = 1
				return real.ClaimEvalRunWithEntries(ctx, id, runFrom, generation, runFields, &broken)
			},
		}
	})
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.Error(t, err)
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: created.ID})
	require.NoError(t, err)
	assert.Len(t, results, 2, "every result is kept")
	state, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status, "the claim is undone with the reset")
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
	stale, _, err := jobs.StaleJobs(ctx, time.Now(), time.Now().Add(-time.Hour), []string{"k1"}, evals.JobCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "only the job on the removed key")
	assert.Equal(t, targetJobID(entries[0].ID, 1), stale[0].ID)
	assert.True(t, stale[0].Started)
	assert.Equal(t, "c", stale[0].ClaimID)
	assert.Equal(t, "key-gone", stale[0].KeyID)
	assert.WithinDuration(t, started, stale[0].StartedAt, time.Second)
}

// Once the sweep has claimed a lost entry, a redelivery of the message that started it
// cannot restart it before the sweep's failure write: the recovery claim moved the entry
// to the sweep's own claim ID, and the entry ends failed with the key-gone error.
func TestSweepClaimFencesARedeliveryOfALostEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	_, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued)
	started := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"status": tables.EvalRunEntryRunning, "attempt": 1, "key_id": "key-gone", "claim_id": "D", "started_at": started})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := targetJobID(entries[0].ID, 1)
	claimed, err := jobs.StartJob(ctx, id, "key-gone", evals.JobClaim{DeliveryID: "D", RecoverStartedAt: started})
	require.NoError(t, err)
	require.True(t, claimed, "the sweep claims the lost entry")
	restarted, err := jobs.StartJob(ctx, id, "key-gone", evals.JobClaim{DeliveryID: "D"})
	require.NoError(t, err)
	assert.False(t, restarted, "the redelivery of D is refused once the sweep owns the entry")

	require.NoError(t, jobs.CompleteJob(ctx, id, evals.Result{Score: evals.ErrorScore(), Reasoning: "the key is no longer enabled for evaluations"}))
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryFailed, entry.Status)
	assert.Contains(t, entry.Error, "no longer enabled for evaluations")
}

// A stop that runTarget read during the call is stale once a rerun has claimed the
// stopped run and reset the entry: the late stop must not end the rerun's entry, which
// would then never run.
func TestLateStopOfATargetCallLeavesTheRerunsEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryRunning)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped, "ended_at": time.Now()})
	require.NoError(t, err)
	h.useRunner(t, stubRunner{h: h})
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	spec, err := h.svc.spec(ctx, run.ID, state.UpdatedAt)
	require.NoError(t, err)
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)

	var reran atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, runState: func(ctx context.Context, id string) (*configstore.EvalRunState, error) {
			read, err := real.GetEvalRunState(ctx, id)
			if err != nil || read.Status != tables.EvalRunStopped || !reran.CompareAndSwap(false, true) {
				return read, err
			}
			// runTarget's stop check reads the stopped run; a rerun claims it right after.
			_, rerunErr := h.svc.Rerun(ctx, id, RerunInput{Scope: RerunAll})
			require.NoError(t, rerunErr)
			return read, nil
		}}
	})
	require.NoError(t, h.svc.runTarget(ctx, spec, entry, 1))
	require.True(t, reran.Load(), "the rerun ran during the stop check")

	got, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryQueued, got.Status, "the rerun's entry is left queued")
	after, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, after.Status, "the rerun is running")
}

// stoppedRunWithQueuedResult stores a stopped run whose one entry is evaluating with an AI
// judge's result queued at attempt 1.
func stoppedRunWithQueuedResult(t *testing.T, h *harness) (*tables.TableEvalRun, tables.TableEvalRunEntry, tables.TableEvalRunResult) {
	t.Helper()
	ctx := context.Background()
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultQueued, Attempt: 1, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	_, err = h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped})
	require.NoError(t, err)
	return run, entries[0], results[0]
}

// A result of a stopped run whose failure write fails is not acknowledged: the load
// returns the error, and the redelivery fails the result and stops the entry.
func TestStoppedResultWriteFailureIsRedelivered(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useRunner(t, stubRunner{h: h})
	_, entry, result := stoppedRunWithQueuedResult(t, h)
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, transitionResult: func(ctx context.Context, id string, from []string, fields map[string]any) (bool, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.TransitionEvalRunResult(ctx, id, from, fields)
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	_, err := jobs.LoadJob(ctx, resultJobID(result.ID, 1))
	require.Error(t, err, "the failed write is not acknowledged")
	stored, err := h.store.GetEvalRunResult(ctx, result.ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunResultQueued, stored.Status)

	_, err = jobs.LoadJob(ctx, resultJobID(result.ID, 1))
	require.NoError(t, err)
	stored, err = h.store.GetEvalRunResult(ctx, result.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultFailed, stored.Status)
	got, err := h.store.GetEvalRunEntry(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryStopped, got.Status)
}

// A result of a stopped run whose failure landed and whose entry stop failed is delivered
// again. That redelivery finds a terminal result at its attempt, and must stop the entry
// rather than complete it under the stopped run.
func TestRedeliveredStoppedResultStopsTheEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useRunner(t, stubRunner{h: h})
	_, entry, result := stoppedRunWithQueuedResult(t, h)
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, entryAtRun: func(ctx context.Context, runID, status string, generation int64, id string, from []string, fields map[string]any) (bool, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.TransitionEvalRunEntryAtRun(ctx, runID, status, generation, id, from, fields)
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	_, err := jobs.LoadJob(ctx, resultJobID(result.ID, 1))
	require.Error(t, err, "the failed entry stop is not acknowledged")
	stored, err := h.store.GetEvalRunResult(ctx, result.ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunResultFailed, stored.Status, "the result write landed")

	job, err := jobs.LoadJob(ctx, resultJobID(result.ID, 1))
	require.NoError(t, err)
	require.NoError(t, finish(ctx, jobs, job))
	got, err := h.store.GetEvalRunEntry(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryStopped, got.Status, "the entry of a stopped run is stopped, not completed")
}

// A result still stored at attempt 0 met by a message of attempt 1 under a stopped run:
// the result write stores the message's attempt, so the redelivery after a failed entry
// stop finds the result terminal at its attempt and stops the entry.
func TestRedeliveredStoppedResultAtAnOlderAttemptStopsTheEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.useRunner(t, stubRunner{h: h})
	_, entry, result := stoppedRunWithQueuedResult(t, h)
	_, err := h.store.TransitionEvalRunResult(ctx, result.ID, nil, map[string]any{"attempt": 0})
	require.NoError(t, err)
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, entryAtRun: func(ctx context.Context, runID, status string, generation int64, id string, from []string, fields map[string]any) (bool, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.TransitionEvalRunEntryAtRun(ctx, runID, status, generation, id, from, fields)
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	_, err = jobs.LoadJob(ctx, resultJobID(result.ID, 1))
	require.Error(t, err, "the failed entry stop is not acknowledged")
	job, err := jobs.LoadJob(ctx, resultJobID(result.ID, 1))
	require.NoError(t, err)
	require.False(t, job.Done, "the redelivery still has the entry to stop")
	require.NoError(t, finish(ctx, jobs, job))
	got, err := h.store.GetEvalRunEntry(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryStopped, got.Status)
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

// A stop can land between two pages of a rerun's reset. The rerun must stop there: the
// next page must not delete completed scores or put entries back to waiting under a
// stopped run.
func TestStopBetweenRerunPagesLeavesTheRestAlone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	rows := make([][3]string, writeBatch+1)
	for i := range rows {
		rows[i] = [3]string{fmt.Sprintf("q%d", i), "same", "same"}
	}
	dataset := h.dataset(t, "stopped rerun", rows...)
	created, err := h.svc.Create(ctx, RunInput{Name: "stopped rerun", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	run := h.await(t, created.ID, tables.EvalRunCompleted)
	refs, err := h.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{RunID: run.ID})
	require.NoError(t, err)
	require.Len(t, refs, writeBatch+1)
	last := refs[writeBatch]

	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		// The claim resets the first page; the stop lands before the second.
		return hookStore{Store: real, afterRunClaim: func(ctx context.Context, fields map[string]any) {
			if fields["status"] == tables.EvalRunRunning && once.CompareAndSwap(false, true) {
				_, stopErr := h.svc.Stop(ctx, run.ID)
				require.NoError(t, stopErr)
			}
		}}
	})
	_, rerunErr := h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunAll})

	entry, err := h.store.GetEvalRunEntry(ctx, last.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status, "the entry past the first page is not reset under a stopped run")
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{last.ID}})
	require.NoError(t, err)
	assert.Len(t, results, 1, "its score is kept")
	assert.True(t, evals.IsInvalid(rerunErr), "the rerun reports the stop: %v", rerunErr)
}

// Two reviews of a finished run each rebuild its summary. The one that computed first can
// write last; its write must not drop the other review's score.
// A review that lands while the last machine result completes the entry is in the entry's
// verdict: the worker read the results before the review's score, and the review read the
// entry before the worker completed it, so neither alone would see both. The review count
// fences the worker's completion, which is then computed again with the score.
func TestReviewDuringTheEntrysCompletionIsInItsVerdict(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	humanRef := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	human := h.definition(t, humanRef)
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge, human}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	results := []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 1, KeyID: "k1"},
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: human.ID, Status: tables.EvalRunResultAwaitingReview, Attempt: 1},
	}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	h.useRunner(t, stubRunner{h: h})
	var reviewed atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, listResultRefs: func(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error) {
			refs, err := real.ListEvalRunResultRefs(ctx, params)
			if err != nil || !reviewed.CompareAndSwap(false, true) {
				return refs, err
			}
			// The worker read the results; the review lands before its completion write.
			_, reviewErr := h.svc.Review(ctx, run.ID, entries[0].ID, human.ID, ReviewInput{Score: false}, "reviewer")
			require.NoError(t, reviewErr)
			return refs, nil
		}}
	})

	ref := resultRef{id: results[0].ID, runID: run.ID, entryID: entries[0].ID, evaluatorID: judge.ID}
	require.NoError(t, h.svc.completeResult(ctx, ref, 1, evals.Result{Score: evals.BoolScore(true), Pass: new(true)}))
	require.True(t, reviewed.Load(), "the review ran during the completion")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status)
	require.NotNil(t, entry.Passed)
	assert.False(t, *entry.Passed, "the failing review is in the entry's verdict")
}

func TestConcurrentReviewsBothReachTheSummary(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "two reviews", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	human := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	created, err := h.svc.Create(ctx, RunInput{Name: "two reviews", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{human}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	entries := h.entries(t, created.ID, EntryQuery{}).Entries
	require.Len(t, entries, 2)

	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, fencedRunWrite: func() {
			if once.CompareAndSwap(false, true) {
				// The first review has computed its summary; the second runs to completion.
				_, reviewErr := h.svc.Review(ctx, created.ID, entries[1].ID, human.ID, ReviewInput{Score: true}, "second")
				require.NoError(t, reviewErr)
			}
		}}
	})
	_, err = h.svc.Review(ctx, created.ID, entries[0].ID, human.ID, ReviewInput{Score: true}, "first")
	require.NoError(t, err)
	view, err := h.svc.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, evaluatorSummary(t, view, human.ID).Scored, "the summary counts both reviews")
}

// The runner retries a result's completion in-process. When the first try stored the
// result and then failed to complete the entry, the retry finds the result stored; it
// must still complete the entry and finish the run, not return as a duplicate.
func TestRetriedResultCompletionFinishesItsEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultRunning, Attempt: 1}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	var failedOnce atomic.Bool
	fail := func(fields map[string]any) bool {
		return fields["status"] == tables.EvalRunEntryCompleted && failedOnce.CompareAndSwap(false, true)
	}
	h.useStore(t, func(real Store) Store {
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
			entryAtReview: func(ctx context.Context, runID string, reviewSeq, generation int64, id string, attempt int, from []string, fields map[string]any) (bool, error) {
				if fail(fields) {
					return false, errors.New("database is locked")
				}
				return real.TransitionEvalRunEntryAtReview(ctx, runID, reviewSeq, generation, id, attempt, from, fields)
			},
		}
	})
	ref := resultRef{id: results[0].ID, runID: run.ID, entryID: entries[0].ID, evaluatorID: exact.ID}
	outcome := evals.Result{Score: evals.BoolScore(true), Pass: new(true)}
	require.Error(t, h.svc.completeResult(ctx, ref, 1, outcome), "the first try fails to complete the entry")
	require.NoError(t, h.svc.completeResult(ctx, ref, 1, outcome))

	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status, "the retry completes the entry")
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status, "and finishes the run")
}

// A target job stores the entry as failed and then fails to finish the run. The
// redelivery of the same job must finish the run, not find the entry ended and drop it.
func TestRedeliveredTargetJobFinishesTheRunAfterAFailedFinalize(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued)
	h.setModel(func(context.Context, evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		return evals.ModelCallResponse{}, errors.New("the model is down")
	})
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, hasOpen: func(ctx context.Context, runID string) (bool, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.HasOpenEvalRunEntries(ctx, runID)
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := targetJobID(entries[0].ID, 1)
	job, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	claimed, err := jobs.StartJob(ctx, id, job.KeyID, evals.JobClaim{DeliveryID: "first"})
	require.NoError(t, err)
	require.True(t, claimed)
	require.Error(t, finish(ctx, jobs, job), "the failed finalize has the delivery retried")
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunRunning, state.Status)

	again, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	require.False(t, again.Done, "the redelivery still has the run to finish")
	claimed, err = jobs.StartJob(ctx, id, again.KeyID, evals.JobClaim{DeliveryID: "second"})
	require.NoError(t, err)
	require.True(t, claimed, "the redelivery is claimed without calling the target again")
	require.NoError(t, finish(ctx, jobs, again))
	state, err = h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, state.Status, "no entry completed, so the run ends failed")

	// With only a human evaluator, the target job completes the entry itself (its result
	// waits for a reviewer). A failed finish leaves the entry completed and the run
	// running; the redelivery must still finish the run.
	human := h.definition(t, h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`))
	reviewed, reviewedEntries := h.manualRun(t, modelTarget, []evals.Definition{human}, tables.EvalRunEntryQueued)
	h.setModel(func(context.Context, evals.ModelCallRequest) (evals.ModelCallResponse, error) {
		return evals.ModelCallResponse{Text: "an answer"}, nil
	})
	var humanFailures atomic.Int32
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, hasOpen: func(ctx context.Context, runID string) (bool, error) {
			if runID == reviewed.ID && humanFailures.Add(1) <= 1 {
				return false, errors.New("database is locked")
			}
			return real.HasOpenEvalRunEntries(ctx, runID)
		}}
	})
	id = targetJobID(reviewedEntries[0].ID, 1)
	job, err = jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	claimed, err = jobs.StartJob(ctx, id, job.KeyID, evals.JobClaim{DeliveryID: "first"})
	require.NoError(t, err)
	require.True(t, claimed)
	require.Error(t, finish(ctx, jobs, job), "the failed finalize has the delivery retried")
	entry, err := h.store.GetEvalRunEntry(ctx, reviewedEntries[0].ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunEntryCompleted, entry.Status)

	again, err = jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	require.False(t, again.Done, "the redelivery of a completed entry still has the run to finish")
	claimed, err = jobs.StartJob(ctx, id, again.KeyID, evals.JobClaim{DeliveryID: "second"})
	require.NoError(t, err)
	require.True(t, claimed, "the redelivery is claimed without calling the target again")
	require.NoError(t, finish(ctx, jobs, again))
	state, err = h.store.GetEvalRunState(ctx, reviewed.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, state.Status, "the redelivery finishes the run")
}

// A repair job checks the entry's attempt when it loads, and must keep checking it when
// it creates results: a rerun that reset the entry in between leaves it waiting with its
// output cleared, and no result may be created or queued against it.
func TestRepairAfterARerunResetCreatesNoResults(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	h.useRunner(t, stubRunner{h: h})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, repairJobID(entries[0].ID, 1))
	require.NoError(t, err)
	require.False(t, job.Done)

	_, err = h.store.ResetEvalRunEntries(ctx, run.ID, run.Generation, []string{entries[0].ID}, resetEntry(time.Now()))
	require.NoError(t, err)
	require.NoError(t, finish(ctx, jobs, job))
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{entries[0].ID}})
	require.NoError(t, err)
	assert.Empty(t, results, "no result is created on an entry a rerun reset")
}

// entryProgress reads an entry's verdict and then completes it. A rerun that started the
// entry again in between must not have its new attempt completed with the old verdict.
func TestEntryProgressLeavesALaterAttemptAlone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	entryID := entries[0].ID
	_, err := h.store.TransitionEvalRunEntry(ctx, entryID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	require.NoError(t, h.store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entryID, EvaluatorID: exact.ID, Status: tables.EvalRunResultCompleted, Attempt: 1, Pass: new(true)}}, 0))

	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, listResultRefs: func(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error) {
			refs, err := real.ListEvalRunResultRefs(ctx, params)
			if err == nil && once.CompareAndSwap(false, true) {
				// A rerun resets the entry and starts it again at attempt 2.
				_, resetErr := real.ResetEvalRunEntries(ctx, run.ID, run.Generation, []string{entryID}, resetEntry(time.Now()))
				require.NoError(t, resetErr)
				_, moveErr := real.TransitionEvalRunEntry(ctx, entryID, nil, map[string]any{"status": tables.EvalRunEntryEvaluating, "attempt": 2})
				require.NoError(t, moveErr)
				require.NoError(t, real.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entryID, EvaluatorID: exact.ID, Status: tables.EvalRunResultQueued}}, 2))
			}
			return refs, err
		}}
	})
	require.NoError(t, h.svc.entryProgress(ctx, run.ID, entryID))
	entry, err := h.store.GetEvalRunEntry(ctx, entryID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryEvaluating, entry.Status, "the second attempt is still being scored")
	assert.Equal(t, 2, entry.Attempt)
}

// A run tag with a NUL character is refused when the run is created and when it is
// updated: Postgres reads tags back as JSON to filter by them, and one such tag would
// break every tag-filtered list.
func TestRunTagsWithANULAreRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "tags", [3]string{"a", "same", "same"})
	exact := h.exactMatch(t)
	in := RunInput{Name: "tagged", Tags: []string{"a\x00b"}, DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}}
	_, err := h.svc.Create(ctx, in)
	require.True(t, evals.IsInvalid(err), "a NUL tag is refused on create: %v", err)
	assert.Contains(t, err.Error(), `"a\x00b"`, "the error names the tag")

	in.Tags = []string{"ok"}
	created, err := h.svc.Create(ctx, in)
	require.NoError(t, err)
	_, err = h.svc.Update(ctx, created.ID, UpdateInput{Name: "tagged", Tags: []string{"a\x00b"}})
	require.True(t, evals.IsInvalid(err), "a NUL tag is refused on update: %v", err)
	assert.Contains(t, err.Error(), `"a\x00b"`, "the error names the tag")
	stored, err := h.store.GetEvalRun(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"ok"}, stored.Tags, "the stored tags are unchanged")
}

// A model target with no system message on a dataset that has neither an input nor a
// conversation history column has nothing to send for any row, so it is refused up front.
func TestModelTargetNeedsSomethingToSend(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	bare, err := h.datasets.Create(ctx, datasets.DatasetInput{Name: "expected only", Columns: []datasets.ColumnSpec{{Name: "expected_output", Type: datasets.ColumnExpectedOutput}}})
	require.NoError(t, err)
	_, err = h.datasets.AddRows(ctx, bare.ID, []map[string]string{{"expected_output": "x"}}, "", "tester")
	require.NoError(t, err)
	exact := h.exactMatch(t)
	in := RunInput{Name: "nothing to send", DatasetID: bare.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}}
	check, err := h.svc.Validate(ctx, in)
	require.NoError(t, err)
	assert.False(t, check.Valid, "no system message and no input column: nothing to send")
	assert.Contains(t, strings.Join(check.Problems, "\n"), "nothing to send")

	in.Target.System = "Answer in one word."
	check, err = h.svc.Validate(ctx, in)
	require.NoError(t, err)
	assert.True(t, check.Valid, "a system message is something to send: %v", check.Problems)

	withInput := h.dataset(t, "with input", [3]string{"a", "x", "x"})
	check, err = h.svc.Validate(ctx, RunInput{Name: "input", DatasetID: withInput.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}})
	require.NoError(t, err)
	assert.True(t, check.Valid, "an input column is something to send: %v", check.Problems)
}

// racingSaves saves the draft and then reports that there was nothing to save, as a
// request that lost the race to another request saving the same draft sees it.
type racingSaves struct{ Datasets }

func (d racingSaves) SaveVersion(ctx context.Context, id, name, description, actor string) (*tables.TableEvalDatasetVersion, error) {
	if _, err := d.Datasets.SaveVersion(ctx, id, name, description, actor); err != nil {
		return nil, err
	}
	return nil, configstore.ErrEvalDatasetNoChanges
}

// When another request saves the same draft first, the run reads the version that save
// made, not the older head read before it.
func TestCreateReadsTheVersionAConcurrentSaveMade(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "racing save", [3]string{"a", "same", "same"})
	exact := h.exactMatch(t)
	first, err := h.svc.Create(ctx, RunInput{Name: "first", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	require.Equal(t, 1, first.DatasetVersion)
	// The first run's plan reads the datasets on a runner goroutine: it must be done
	// before they are swapped below.
	h.await(t, first.ID, tables.EvalRunCompleted)
	_, err = h.datasets.AddRows(ctx, dataset.ID, []map[string]string{{"input": "b", "expected_output": "same", "output": "same"}}, "", "tester")
	require.NoError(t, err)

	real := h.svc.datasets
	h.svc.datasets = racingSaves{real}
	t.Cleanup(func() { h.svc.datasets = real })
	second, err := h.svc.Create(ctx, RunInput{Name: "second", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{exact}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	assert.Equal(t, 2, second.DatasetVersion, "the run reads the draft it validated, as the other request saved it")
	// The same holds for the second run's plan and the cleanup's restore.
	h.await(t, second.ID, tables.EvalRunCompleted)
}

// A stop whose run write committed but whose cleanup failed can be repeated: the second
// stop finishes the cleanup instead of reporting that the run is not active.
func TestRepeatedStopFinishesAFailedCleanup(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, _ := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued)
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, stoppedEntries: func(ctx context.Context, runID string, generation int64, from []string, idle bool, fields map[string]any) (int64, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return 0, errors.New("database is locked")
			}
			return real.TransitionStoppedEvalRunEntries(ctx, runID, generation, from, idle, fields)
		}}
	})
	_, err := h.svc.Stop(ctx, run.ID)
	require.Error(t, err, "the first stop fails to stop the waiting entries")
	_, err = h.svc.Stop(ctx, run.ID)
	require.NoError(t, err, "the second stop finishes the cleanup")

	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryStopped: 2}, counts)
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, state.Status)
	assert.NotNil(t, state.EndedAt, "the stopped run is finalized")
}

// A stop whose cleanup failed leaves the run stopped with EndedAt unset and its target
// work still queued. AddEvaluators must refuse it: claiming it would run that queued
// target work under an operation that does not call the target. A repeated stop then
// finishes the cleanup.
func TestAddEvaluatorsRefusesARunStillStopping(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryCompleted, tables.EvalRunEntryQueued)
	var submitted atomic.Int32
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error {
		submitted.Add(1)
		return nil
	}})
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, stoppedEntries: func(ctx context.Context, runID string, generation int64, from []string, idle bool, fields map[string]any) (int64, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return 0, errors.New("database is locked")
			}
			return real.TransitionStoppedEvalRunEntries(ctx, runID, generation, from, idle, fields)
		}}
	})
	_, err := h.svc.Stop(ctx, run.ID)
	require.Error(t, err, "the first stop fails to stop the waiting entries")

	_, err = h.svc.AddEvaluators(ctx, run.ID, []EvaluatorRef{h.aiJudge(t)})
	require.Error(t, err)
	assert.True(t, evals.IsInvalid(err), "a run still stopping is refused: %v", err)
	assert.Zero(t, submitted.Load(), "nothing is queued for the run")
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, state.Status)
	assert.Nil(t, state.EndedAt, "the stop has not finished")
	queued, err := h.store.GetEvalRunEntry(ctx, entries[1].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryQueued, queued.Status, "the queued target entry is left to the stop")

	_, err = h.svc.Stop(ctx, run.ID)
	require.NoError(t, err, "the second stop finishes the cleanup")
	state, err = h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, state.Status)
	assert.NotNil(t, state.EndedAt, "the stopped run is finalized")
}

// A repair job's dead letter completes without its Exec. The entry must still get its
// results then, instead of staying evaluating with nothing queued.
func TestDeadRepairJobStillFinishesTheEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, repairJobID(entries[0].ID, 1))
	require.NoError(t, err)
	require.False(t, job.Done)
	require.NotNil(t, job.Complete)
	require.NoError(t, job.Complete(ctx, evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "the repair failed every delivery"}))

	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{entries[0].ID}})
	require.NoError(t, err)
	assert.Len(t, results, 1, "the dead letter creates the entry's results")
	require.Eventually(t, func() bool {
		entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
		return err == nil && entry.Status == tables.EvalRunEntryCompleted
	}, 10*time.Second, 5*time.Millisecond, "the entry completes when its results do")
}

// A failure write that lands while the run's finish fails is retried by the runner. The
// retry's write matches nothing, since the entry already failed, but it still finishes the
// run: the sweep no longer lists a failed entry, so nothing else would.
func TestRetriedEntryFailureStillFinishesTheRun(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryRunning)
	// Held under the sweep's recovery claim, as endLostJob leaves it before CompleteJob.
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1, "key_id": "key-gone", "claim_id": configstore.EvalEndLostClaim})
	require.NoError(t, err)
	var failed atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, hasOpen: func(ctx context.Context, runID string) (bool, error) {
			if failed.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.HasOpenEvalRunEntries(ctx, runID)
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := targetJobID(entries[0].ID, 1)
	gone := evals.Result{Score: evals.ErrorScore(), Reasoning: "the key is no longer enabled for evaluations"}
	require.Error(t, jobs.CompleteJob(ctx, id, gone), "the failed finish is reported")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunEntryFailed, entry.Status, "the failure write landed")

	require.NoError(t, jobs.CompleteJob(ctx, id, gone))
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunFailed, state.Status, "the retry finishes the run")
}

// A retry that was taken, whose submit failed and marked the result failed, and whose
// entry progress then failed, is retried by the runner in process. The retry's claim
// matches nothing, but the stored failure of this attempt still advances the entry.
func TestRetriedResultWhoseSubmitFailedStillAdvancesTheEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 1, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error {
		return errors.New("no eval key serves openai")
	}})
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, entryAtReview: func(ctx context.Context, runID string, reviewSeq, generation int64, id string, attempt int, from []string, fields map[string]any) (bool, error) {
			if fields["status"] == tables.EvalRunEntryCompleted && failedOnce.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.TransitionEvalRunEntryAtReview(ctx, runID, reviewSeq, generation, id, attempt, from, fields)
		}}
	})

	ref := resultRef{id: results[0].ID, runID: run.ID, entryID: entries[0].ID, evaluatorID: judge.ID}
	timedOut := evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "judge timed out"}
	require.Error(t, h.svc.completeResult(ctx, ref, 1, timedOut), "the failed entry write is reported")
	stored, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunResultFailed, stored.Status, "the submit failure is stored")

	require.NoError(t, h.svc.completeResult(ctx, ref, 1, timedOut))
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryCompleted, entry.Status, "the retry advances the entry")

	// A row a later attempt is running is left alone by the same retry, called twice.
	other, otherEntries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	later := []tables.TableEvalRunResult{{RunID: other.ID, EntryID: otherEntries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 2, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, later, 0))
	laterRef := resultRef{id: later[0].ID, runID: other.ID, entryID: otherEntries[0].ID, evaluatorID: judge.ID}
	for range 2 {
		require.NoError(t, h.svc.completeResult(ctx, laterRef, 1, timedOut))
	}
	kept, err := h.store.GetEvalRunResult(ctx, later[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultRunning, kept.Status, "attempt 2 still runs")
	assert.Equal(t, 2, kept.Attempt)
}

// A retry whose transition committed, and whose submit and failed-row write both failed,
// is retried by the runner in process. The row is queued at the failed attempt with no
// message: the retry dispatches attempt+1 instead of leaving it to the sweep.
func TestRetryCommittedButNotDispatchedIsDispatchedOnTheRetry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 1, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	var submits atomic.Int32
	var submitted sync.Map
	h.useRunner(t, stubRunner{h: h, submit: func(_ context.Context, job *evals.Job) error {
		if submits.Add(1) == 1 {
			return errors.New("no eval key serves openai")
		}
		submitted.Store(job.ID, true)
		return nil
	}})
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, resultUnstarted: func(ctx context.Context, id string, attempt int, fields map[string]any) (bool, error) {
			if fields["status"] == tables.EvalRunResultFailed && failedOnce.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.TransitionEvalRunResultUnstarted(ctx, id, attempt, fields)
		}}
	})
	ref := resultRef{id: results[0].ID, runID: run.ID, entryID: entries[0].ID, evaluatorID: judge.ID}
	timedOut := evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "judge timed out"}
	require.Error(t, h.svc.completeResult(ctx, ref, 1, timedOut))
	require.NoError(t, h.svc.completeResult(ctx, ref, 1, timedOut))
	_, ok := submitted.Load(resultJobID(results[0].ID, 2))
	assert.True(t, ok, "attempt 2 is submitted by the retry")
	stored, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultQueued, stored.Status)
}

// A late error from attempt 1 must not requeue a result attempt 2 is running: that would
// clear its key and submit attempt 2 a second time.
func TestLateErrorOfAnEarlierAttemptDoesNotRequeue(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 2, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	var submitted atomic.Int32
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error {
		submitted.Add(1)
		return nil
	}})

	ref := resultRef{id: results[0].ID, runID: run.ID, entryID: entries[0].ID, evaluatorID: judge.ID}
	require.NoError(t, h.svc.completeResult(ctx, ref, 1, evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "judge timed out"}))
	stored, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultRunning, stored.Status, "attempt 2 still runs")
	assert.Equal(t, 2, stored.Attempt)
	assert.Equal(t, "k1", stored.KeyID, "its key is kept")
	assert.Zero(t, submitted.Load(), "nothing is submitted again")
}

// A transient failure to read the run while deciding on a retry is returned, not taken as
// "no retry": attempt 1 of an AI evaluator is not stored as its final failure, and the
// runner's in-process retry of Complete then requeues it at attempt 2.
func TestRetryDecisionReturnsAFailedRunRead(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultRunning, Attempt: 1, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	var submitted atomic.Int32
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error {
		submitted.Add(1)
		return nil
	}})
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, runState: func(ctx context.Context, id string) (*configstore.EvalRunState, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return nil, errors.New("database is locked")
			}
			return real.GetEvalRunState(ctx, id)
		}}
	})

	ref := resultRef{id: results[0].ID, runID: run.ID, entryID: entries[0].ID, evaluatorID: judge.ID}
	timedOut := evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "judge timed out"}
	require.Error(t, h.svc.completeResult(ctx, ref, 1, timedOut), "the failed run read is returned")
	stored, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultRunning, stored.Status, "attempt 1 is not stored as final")

	require.NoError(t, h.svc.completeResult(ctx, ref, 1, timedOut))
	stored, err = h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultQueued, stored.Status, "the retry requeues it")
	assert.EqualValues(t, 1, submitted.Load(), "attempt 2 is submitted")
}

// A review moves a finished run's generation when it rebuilds the summary. A rerun that
// read the run just before must still claim it: the run is finished, not active.
func TestRerunClaimsARunAReviewJustSummarized(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "reviewed", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "reviewed", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	h.useRunner(t, stubRunner{h: h})
	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, getRun: func(ctx context.Context, id string) (*tables.TableEvalRun, error) {
			run, err := real.GetEvalRun(ctx, id)
			if err == nil && once.CompareAndSwap(false, true) {
				// A review's summary write lands right after the rerun read the run.
				_, bumpErr := real.TransitionEvalRun(ctx, id, nil, map[string]any{"generation": configstore.EvalRunNextGeneration})
				require.NoError(t, bumpErr)
			}
			return run, err
		}}
	})
	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err, "the run is finished, so the rerun claims it")
	state, err := h.store.GetEvalRunState(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, state.Status)
}

// uncertainRunner fails every Submit and Resume as a publish whose acknowledgement was
// lost: the message may have been stored.
type uncertainRunner struct{ stubRunner }

func (r uncertainRunner) Submit(context.Context, *evals.Job) error {
	return fmt.Errorf("%w: ack lost", evals.ErrPlacementUnknown)
}

func (r uncertainRunner) Resume(context.Context, *evals.Job) error {
	return fmt.Errorf("%w: ack lost", evals.ErrPlacementUnknown)
}

// A publish that failed after the message may have been stored must not end the entry or
// the result as failed: the message runs if it exists, and the sweep publishes it again if
// it does not. A definite failure, such as no eval key, still fails them.
func TestUncertainPlacementLeavesWorkQueued(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued, tables.EvalRunEntryEvaluating, tables.EvalRunEntryEvaluating)
	h.useRunner(t, uncertainRunner{stubRunner{h: h}})
	target := Target{Type: TargetModel, Provider: "openai", Model: "gpt-4o"}

	// The plan's (or a rerun's) walk queues a waiting entry.
	require.NoError(t, h.svc.submitWaiting(ctx, run.ID, target, nil))
	// A resume after a restart puts back an entry routed to a key.
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[1].ID, nil, map[string]any{"key_id": "k1"})
	require.NoError(t, err)
	refs, err := h.store.ListEvalRunEntryRefs(ctx, configstore.EvalRunEntryListParams{IDs: []string{entries[1].ID}})
	require.NoError(t, err)
	require.NoError(t, h.svc.resumeTarget(ctx, run.ID, nil, target, refs[0], 1))
	for _, entry := range entries[:2] {
		stored, err := h.store.GetEvalRunEntry(ctx, entry.ID)
		require.NoError(t, err)
		assert.Equal(t, tables.EvalRunEntryQueued, stored.Status, "the entry stays queued")
		assert.Empty(t, stored.Error)
	}

	// An entry's results are created and submitted.
	_, err = h.store.TransitionEvalRunEntry(ctx, entries[2].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	require.NoError(t, h.svc.startResults(ctx, run.ID, entries[2].ID, 1, []evals.Definition{judge}))
	results, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{entries[2].ID}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, tables.EvalRunResultQueued, results[0].Status, "the result stays queued")
	assert.Empty(t, results[0].Error)
	entry, err := h.store.GetEvalRunEntry(ctx, entries[2].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryEvaluating, entry.Status, "the entry waits for its result")

	// A result routed to a key is resumed.
	routed := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[3].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultQueued, KeyID: "k1"}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, routed, 0))
	resultRefs, err := h.store.ListEvalRunResultRefs(ctx, configstore.EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{entries[3].ID}})
	require.NoError(t, err)
	queued, err := h.svc.resumeResult(ctx, judge, resultRefs[0], 1)
	require.NoError(t, err)
	assert.True(t, queued, "it counts as queued")
	stored, err := h.store.GetEvalRunResult(ctx, routed[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultQueued, stored.Status)

	// A definite failure still fails the entry, also through a walk's write fenced on the
	// run's current generation.
	h.useRunner(t, stubRunner{h: h, submit: func(context.Context, *evals.Job) error { return evals.ErrNoEvalKey }})
	require.NoError(t, h.svc.submitTarget(ctx, run.ID, &run.Generation, target, entries[0].ID, 1))
	failed, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryFailed, failed.Status, "no eval key fails the entry")
}

// Entries and results that call no model wait on the local topic, never routed to a key.
// One whose message was lost (the gateway stopped between storing it and publishing it)
// is listed once it has waited past the lost-message cutoff, with CreatedAt its queue
// time, so the sweep publishes it again under Submit's message ID. A younger one is not.
func TestSweepFindsLocalWorkWhoseMessageWasLost(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, entries := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact},
		tables.EvalRunEntryQueued, tables.EvalRunEntryQueued, tables.EvalRunEntryEvaluating, tables.EvalRunEntryEvaluating)
	old, recent := time.Now().Add(-30*time.Minute).Truncate(time.Millisecond), time.Now().Add(-time.Minute)
	for id, at := range map[string]time.Time{entries[0].ID: old, entries[1].ID: recent} {
		_, err := h.store.TransitionEvalRunEntry(ctx, id, nil, map[string]any{"queued_at": at})
		require.NoError(t, err)
	}
	results := []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[2].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultQueued, QueuedAt: old},
		{RunID: run.ID, EntryID: entries[3].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultQueued, QueuedAt: recent},
	}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	stale, _, err := jobs.StaleJobs(ctx, time.Now(), time.Now().Add(-10*time.Minute), nil, evals.JobCursor{}, 10)
	require.NoError(t, err)
	byID := map[string]*evals.Job{}
	for _, job := range stale {
		byID[job.ID] = job
	}
	entryJob := byID[targetJobID(entries[0].ID, 1)]
	require.NotNil(t, entryJob, "the old local entry is listed")
	assert.True(t, entryJob.CreatedAt.Equal(old), "its CreatedAt is its queue time: %v", entryJob.CreatedAt)
	assert.Nil(t, entryJob.Model, "it calls no model")
	assert.Empty(t, entryJob.KeyID)
	resultJob := byID[resultJobID(results[0].ID, 1)]
	require.NotNil(t, resultJob, "the old local result is listed")
	assert.True(t, resultJob.CreatedAt.Equal(old), "its CreatedAt is its queue time: %v", resultJob.CreatedAt)
	assert.NotContains(t, byID, targetJobID(entries[1].ID, 1), "a young local entry is left alone")
	assert.NotContains(t, byID, resultJobID(results[1].ID, 1), "a young local result is left alone")
}

// Each run pass of the sweep keeps its own cursor. Rows the sweep lists and then skips (a
// run whose spec cannot be read) or leaves alone fill a page as much as stranded ones do;
// without a cursor every sweep reads the same first page, and a stranded row behind it is
// never reached.
func TestSweepPassesPageAcrossRowsTheySkip(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	runs := map[string]string{"aaaaaaaa-0000-0000-0000-000000000000": "unreadable", "bbbbbbbb-0000-0000-0000-000000000000": "stranded"}
	for id, name := range runs {
		require.NoError(t, h.store.CreateEvalRun(ctx, &tables.TableEvalRun{
			ID: id, Name: name, Status: tables.EvalRunRunning, TargetType: string(TargetModel), DatasetID: h.bareDataset(t), DatasetVersion: 1,
			Target: json.RawMessage(modelTarget), Evaluators: json.RawMessage(`[]`),
		}))
	}
	old := time.Now().Add(-time.Hour)
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, []tables.TableEvalRunEntry{
		{RunID: "aaaaaaaa-0000-0000-0000-000000000000", Seq: 1, DatasetRowID: "a1", Status: tables.EvalRunEntryQueued, QueuedAt: old},
		{RunID: "aaaaaaaa-0000-0000-0000-000000000000", Seq: 2, DatasetRowID: "a2", Status: tables.EvalRunEntryQueued, QueuedAt: old},
	}))
	stranded := []tables.TableEvalRunEntry{{RunID: "bbbbbbbb-0000-0000-0000-000000000000", Seq: 1, DatasetRowID: "b1", Status: tables.EvalRunEntryQueued, QueuedAt: old}}
	require.NoError(t, h.store.CreateEvalRunEntries(ctx, stranded))
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, runState: func(ctx context.Context, id string) (*configstore.EvalRunState, error) {
			if id == "aaaaaaaa-0000-0000-0000-000000000000" {
				return nil, errors.New("database is locked")
			}
			return real.GetEvalRunState(ctx, id)
		}}
	})

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	var ids []string
	for range 2 {
		stale, _, err := jobs.StaleJobs(ctx, time.Now(), time.Time{}, nil, evals.JobCursor{}, 2)
		require.NoError(t, err)
		for _, job := range stale {
			ids = append(ids, job.ID)
		}
	}
	assert.Contains(t, ids, targetJobID(stranded[0].ID, 1), "the second sweep reads past the rows the first one skipped")
}

// A stopped run that is not finalized is not active, so a rerun can claim it between the
// stop's run write and its cleanup of the waiting entries. The cleanup must then leave the
// rerun's entries alone: they belong to the rerun.
func TestStopCleanupLeavesARerunsEntries(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, _ := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued)
	h.useRunner(t, stubRunner{h: h})
	var once atomic.Bool
	rerunFirst := func(ctx context.Context, fields map[string]any) {
		if fields["status"] == tables.EvalRunEntryStopped && once.CompareAndSwap(false, true) {
			_, rerunErr := h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunAll})
			require.NoError(t, rerunErr)
		}
	}
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real,
			transitionEntries: func(ctx context.Context, runID string, from, ids []string, fields map[string]any) (int64, error) {
				rerunFirst(ctx, fields)
				return real.TransitionEvalRunEntries(ctx, runID, from, ids, fields)
			},
			stoppedEntries: func(ctx context.Context, runID string, generation int64, from []string, idle bool, fields map[string]any) (int64, error) {
				rerunFirst(ctx, fields)
				return real.TransitionStoppedEvalRunEntries(ctx, runID, generation, from, idle, fields)
			},
		}
	})
	_, err := h.svc.Stop(ctx, run.ID)
	require.NoError(t, err)

	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryQueued: 2}, counts, "the rerun's entries stay queued")
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, state.Status, "the rerun keeps the run")
}

// An operation that moves the run's generation and adds an evaluator between an entry's
// verdict read and its completion write fences the write out: the verdict is read again,
// finds the new evaluator's result missing, and leaves the entry evaluating.
func TestEntryCompletionHoldsTheGenerationItRead(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{exact}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1})
	require.NoError(t, err)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: exact.ID, Status: tables.EvalRunResultCompleted, Attempt: 1, Pass: new(true)}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	both, err := json.Marshal([]evals.Definition{exact, judge})
	require.NoError(t, err)
	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, listResultRefs: func(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error) {
			refs, err := real.ListEvalRunResultRefs(ctx, params)
			if err == nil && once.CompareAndSwap(false, true) {
				// What an AddEvaluators claim writes, landing after the verdict read.
				_, moveErr := real.TransitionEvalRun(ctx, run.ID, nil, map[string]any{
					"evaluators_json": string(both), "generation": configstore.EvalRunNextGeneration, "updated_at": time.Now().Add(time.Second),
				})
				require.NoError(t, moveErr)
			}
			return refs, err
		}}
	})
	require.NoError(t, h.svc.entryProgress(ctx, run.ID, entries[0].ID))
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryEvaluating, entry.Status, "the entry waits for the added evaluator's result")
}

// After AddEvaluators reopened a finished run's entries, and before its walk created the
// new results, an old result job's redelivery must not complete the entry.
func TestOldResultRedeliveryAfterAddEvaluatorsLeavesTheEntryOpen(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "reopened", [3]string{"a", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "reopened", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	old, err := h.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: created.ID})
	require.NoError(t, err)
	require.Len(t, old, 1)
	h.useRunner(t, stubRunner{h: h}) // the scoring walk is accepted and never delivered
	added := h.evaluator(t, "added", evals.EvaluatorTypeProgrammatic,
		`{"code":"def validate(output, expected_output):\n    return output == expected_output\n","response_type":"yes_no"}`)
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{added})
	require.NoError(t, err)

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, resultJobID(old[0].ID, old[0].Attempt))
	require.NoError(t, err)
	require.NoError(t, finish(ctx, jobs, job))
	entry, err := h.store.GetEvalRunEntry(ctx, old[0].EntryID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryEvaluating, entry.Status, "the added evaluator has no result yet")
}

// A target job's error completion that lands after a stop and a rerun reset its entry to
// queued at the same attempt does not fail the rerun's entry: the write holds the claim the
// job was loaded under.
func TestStaleTargetErrorLeavesARerunsEntry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	run, entries := h.manualRun(t, modelTarget, nil, tables.EvalRunEntryRunning)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1, "claim_id": "D", "key_id": "k1", "started_at": time.Now()})
	require.NoError(t, err)
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, targetJobID(entries[0].ID, 1))
	require.NoError(t, err)
	require.NotNil(t, job.Complete)

	h.useRunner(t, stubRunner{h: h})
	_, err = h.svc.Stop(ctx, run.ID)
	require.NoError(t, err)
	_, err = h.svc.Rerun(ctx, run.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err)
	require.NoError(t, job.Complete(ctx, evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "no key left"}))
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryQueued, entry.Status, "the rerun's entry stays queued")
}

// A review can store its score after the finalizer computed the run's summary and before
// the finalizer's write commits. The summary that stands must count the score.
func TestReviewDuringFinalizeReachesTheSummary(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	human := h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`)
	run, entries := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{h.definition(t, human)}, tables.EvalRunEntryCompleted)
	require.NoError(t, h.store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: human.ID, Status: tables.EvalRunResultAwaitingReview},
	}, 0))
	var once atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, fencedRunWrite: func() {
			if once.CompareAndSwap(false, true) {
				// The finalizer has computed its summary; the review runs to completion.
				_, reviewErr := h.svc.Review(ctx, run.ID, entries[0].ID, human.ID, ReviewInput{Score: true}, "reviewer")
				require.NoError(t, reviewErr)
			}
		}}
	})
	require.NoError(t, h.svc.maybeFinalize(ctx, run.ID))
	view, err := h.svc.Get(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunCompleted, view.Status)
	assert.Equal(t, 1, evaluatorSummary(t, view, human.ID).Scored, "the summary counts the review")
}

// AddEvaluators and Rerun claim a finished run and then reopen its entries. A stale
// delivery that finalizes the run in between sees it running with nothing open and would
// complete it at the claim, fencing the operation out. The claim and the reopen commit
// together, so the finalizer sees the reopened entries and the operation goes on.
func TestFinalizeBetweenAClaimAndItsReopenChangesNothing(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	dataset := h.dataset(t, "claimed", [3]string{"a", "same", "same"}, [3]string{"b", "same", "same"})
	created, err := h.svc.Create(ctx, RunInput{Name: "claimed", DatasetID: dataset.ID, Evaluators: []EvaluatorRef{h.exactMatch(t)}, Target: Target{Type: TargetDataset}})
	require.NoError(t, err)
	h.await(t, created.ID, tables.EvalRunCompleted)
	judge := h.aiJudge(t)

	var claims atomic.Int32
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, afterRunClaim: func(ctx context.Context, fields map[string]any) {
			if fields["status"] != tables.EvalRunRunning {
				return
			}
			claims.Add(1)
			require.NoError(t, h.svc.maybeFinalize(ctx, created.ID), "a stale delivery finalizes the run")
		}}
	})
	_, err = h.svc.AddEvaluators(ctx, created.ID, []EvaluatorRef{judge})
	require.NoError(t, err, "the addition goes on")
	require.EqualValues(t, 1, claims.Load())
	view := h.await(t, created.ID, tables.EvalRunCompleted)
	assert.Equal(t, 2, evaluatorSummary(t, view, judge.ID).Scored, "every entry is scored by the added evaluator")

	_, err = h.svc.Rerun(ctx, created.ID, RerunInput{Scope: RerunAll})
	require.NoError(t, err, "the rerun goes on")
	require.EqualValues(t, 2, claims.Load())
	view = h.await(t, created.ID, tables.EvalRunCompleted)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryCompleted: 2}, view.Counts)
	assert.Equal(t, 2, evaluatorSummary(t, view, judge.ID).Scored, "the rerun scored every entry again")
}

// A result that never started (queued below the failed attempt) is requeued for the next
// attempt with that attempt stored, so a second error of the same attempt does not match
// again and move past the next attempt.
func TestDuplicateErrorOfAnUnstartedAttemptRequeuesOnce(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	judge := h.definition(t, h.aiJudge(t))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{judge}, tables.EvalRunEntryEvaluating)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: judge.ID, Status: tables.EvalRunResultQueued, Attempt: 1}}
	require.NoError(t, h.store.CreateEvalRunResults(ctx, results, 0))
	var mu sync.Mutex
	var submitted []string
	h.useRunner(t, stubRunner{h: h, submit: func(_ context.Context, job *evals.Job) error {
		mu.Lock()
		defer mu.Unlock()
		submitted = append(submitted, job.ID)
		return nil
	}})

	ref := resultRef{id: results[0].ID, runID: run.ID, entryID: entries[0].ID, evaluatorID: judge.ID}
	failure := evals.Result{Score: evals.Score{Status: evals.ScoreStatusError}, Reasoning: "no key for the judge"}
	require.NoError(t, h.svc.completeResult(ctx, ref, 2, failure))
	require.NoError(t, h.svc.completeResult(ctx, ref, 2, failure))
	// The duplicate finds the row queued at attempt 2, which may be a retry that was never
	// dispatched, so it submits attempt 3 again under the same deterministic job ID: the
	// queue keeps one message per ID, and no later attempt is submitted.
	require.NotEmpty(t, submitted)
	for _, id := range submitted {
		assert.Equal(t, resultJobID(results[0].ID, 3), id, "only attempt 3 is submitted")
	}
	stored, err := h.store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, 2, stored.Attempt, "the requeued attempt is stored")
}

// A repair job completes the entry and then fails to finish the run. Its redelivery must
// finish the run, not find the entry completed and drop it.
func TestRedeliveredRepairJobFinishesTheRunAfterAFailedFinalize(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	human := h.definition(t, h.evaluator(t, "tone", evals.EvaluatorTypeHuman, `{"evaluation_type":"yes_no"}`))
	run, entries := h.manualRun(t, modelTarget, []evals.Definition{human}, tables.EvalRunEntryEvaluating)
	_, err := h.store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"attempt": 1, "key_id": "k1"})
	require.NoError(t, err)
	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, hasOpen: func(ctx context.Context, runID string) (bool, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return false, errors.New("database is locked")
			}
			return real.HasOpenEvalRunEntries(ctx, runID)
		}}
	})
	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	id := repairJobID(entries[0].ID, 1)
	job, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	require.False(t, job.Done)
	require.Error(t, finish(ctx, jobs, job), "the failed finalize has the delivery retried")
	entry, err := h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	require.Equal(t, tables.EvalRunEntryCompleted, entry.Status)

	again, err := jobs.LoadJob(ctx, id)
	require.NoError(t, err)
	require.False(t, again.Done, "the redelivery still has the run to finish")
	// The redelivery comes through the local topic, where the sweep puts repair messages.
	payload, err := json.Marshal(map[string]string{"job_id": id})
	require.NoError(t, err)
	require.NoError(t, h.queue.Publish(ctx, &queue.Message{ID: "redelivered-repair", Topic: "evals.local", Payload: payload}))
	view := h.await(t, run.ID, tables.EvalRunCompleted)
	assert.Equal(t, tables.EvalRunCompleted, view.Status, "the redelivery finishes the run")
	entry, err = h.store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, 1, entry.Attempt, "the entry is not run again")

	// A repair job of another attempt has nothing to do.
	other, err := jobs.LoadJob(ctx, repairJobID(entries[0].ID, 2))
	require.NoError(t, err)
	assert.True(t, other.Done)
}

// A stop that lands after AddEvaluators reopened the entries, before the score job ran,
// finds them evaluating with no result to wait for. Nothing would ever score them under a
// stopped run, so the stop ends them and the run finalizes; it must not depend on a score
// job that may never be delivered.
func TestStopAfterAddEvaluatorsFinalizesTheRun(t *testing.T) {
	targets := map[string]string{
		"dataset": `{"type":"dataset"}`,
		"http":    `{"type":"http","http":{"method":"POST","url":"http://127.0.0.1:1/never","output_mapping":{"score":"answer"}}}`,
	}
	for name, target := range targets {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h := newHarness(t)
			exact := h.definition(t, h.exactMatch(t))
			run, _ := h.manualRun(t, target, []evals.Definition{exact}, tables.EvalRunEntryCompleted, tables.EvalRunEntryCompleted)
			_, err := h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunCompleted, "ended_at": time.Now()})
			require.NoError(t, err)
			h.useRunner(t, stubRunner{h: h}) // the score job is never delivered
			_, err = h.svc.AddEvaluators(ctx, run.ID, []EvaluatorRef{h.aiJudge(t)})
			require.NoError(t, err)

			_, err = h.svc.Stop(ctx, run.ID)
			require.NoError(t, err)
			counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
			require.NoError(t, err)
			assert.Equal(t, map[string]int64{tables.EvalRunEntryStopped: 2}, counts, "no entry is left being scored under a stopped run")
			state, err := h.store.GetEvalRunState(ctx, run.ID)
			require.NoError(t, err)
			assert.Equal(t, tables.EvalRunStopped, state.Status)
			assert.NotNil(t, state.EndedAt, "the stopped run is finalized")
		})
	}
}

// When the stop's cleanup failed and no one repeats the stop, the score job of the
// operation the stop ended finishes the cleanup when it is delivered.
func TestScoreJobOfAStoppedRunFinishesTheStop(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	exact := h.definition(t, h.exactMatch(t))
	run, _ := h.manualRun(t, `{"type":"dataset"}`, []evals.Definition{exact}, tables.EvalRunEntryCompleted, tables.EvalRunEntryCompleted)
	_, err := h.store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunCompleted, "ended_at": time.Now()})
	require.NoError(t, err)
	var scoreJob string
	h.useRunner(t, stubRunner{h: h, submit: func(_ context.Context, job *evals.Job) error {
		scoreJob = job.ID
		return nil
	}})
	_, err = h.svc.AddEvaluators(ctx, run.ID, []EvaluatorRef{h.aiJudge(t)})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(scoreJob, scoreJobPrefix))

	var failedOnce atomic.Bool
	h.useStore(t, func(real Store) Store {
		return hookStore{Store: real, stoppedEntries: func(ctx context.Context, runID string, generation int64, from []string, idle bool, fields map[string]any) (int64, error) {
			if failedOnce.CompareAndSwap(false, true) {
				return 0, errors.New("database is locked")
			}
			return real.TransitionStoppedEvalRunEntries(ctx, runID, generation, from, idle, fields)
		}}
	})
	_, err = h.svc.Stop(ctx, run.ID)
	require.Error(t, err, "the stop's cleanup fails")

	jobs := NewJobStore(evals.NewExecutionJobStore(h.store), h.svc)
	job, err := jobs.LoadJob(ctx, scoreJob)
	require.NoError(t, err)
	require.NoError(t, finish(ctx, jobs, job))
	counts, err := h.store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryStopped: 2}, counts, "the score job stops the entries")
	state, err := h.store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.NotNil(t, state.EndedAt, "and the run is finalized")
}
