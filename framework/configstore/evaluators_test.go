package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupEvaluatorTestStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableEvaluator{}, &tables.TableEvaluatorVersion{}))
	return store
}

func testEvaluator(name string) (*tables.TableEvaluator, *tables.TableEvaluatorVersion) {
	return &tables.TableEvaluator{
		Name:        name,
		Description: "checks the reply answers the question",
		Type:        "ai",
		Tags:        []string{"support", "quality"},
		CreatedBy:   "tester",
	}, &tables.TableEvaluatorVersion{
		Config:        json.RawMessage(`{"grading_style":"yes_no","instructions":"v1"}`),
		CommitMessage: "initial",
		CreatedBy:     "tester",
	}
}

func TestEvaluatorCreateAndGet(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	evaluator, version := testEvaluator("answers-question")
	require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))
	require.NotEmpty(t, evaluator.ID, "an id is assigned")
	require.NotEmpty(t, version.ID)
	assert.Equal(t, 1, evaluator.CurrentVersion)
	assert.Equal(t, 1, version.Version)
	assert.Equal(t, evaluator.ID, version.EvaluatorID)

	got, err := store.GetEvaluator(ctx, evaluator.ID)
	require.NoError(t, err)
	assert.Equal(t, "answers-question", got.Name)
	assert.Equal(t, "ai", got.Type)
	assert.Equal(t, []string{"support", "quality"}, got.Tags, "tags round-trip through the JSON column")
	assert.Nil(t, got.BuiltinKey)

	byName, err := store.GetEvaluatorByName(ctx, "answers-question")
	require.NoError(t, err)
	assert.Equal(t, evaluator.ID, byName.ID)

	gotVersion, err := store.GetEvaluatorVersion(ctx, evaluator.ID, 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"grading_style":"yes_no","instructions":"v1"}`, string(gotVersion.Config))
	assert.Equal(t, "initial", gotVersion.CommitMessage)

	_, err = store.GetEvaluator(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetEvaluatorByName(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.GetEvaluatorVersion(ctx, evaluator.ID, 9)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestEvaluatorNameIsUniqueAmongLiveRows(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	first, firstVersion := testEvaluator("toxicity")
	require.NoError(t, store.CreateEvaluator(ctx, first, firstVersion))

	dup, dupVersion := testEvaluator("toxicity")
	err := store.CreateEvaluator(ctx, dup, dupVersion)
	require.ErrorIs(t, err, ErrEvaluatorNameExists)

	var versions int64
	require.NoError(t, store.DB().Model(&tables.TableEvaluatorVersion{}).Count(&versions).Error)
	assert.EqualValues(t, 1, versions, "a rejected create leaves no orphan version behind")

	// Renaming onto a taken name is rejected the same way.
	other, otherVersion := testEvaluator("bias")
	require.NoError(t, store.CreateEvaluator(ctx, other, otherVersion))
	other.Name = "toxicity"
	require.ErrorIs(t, store.UpdateEvaluator(ctx, other), ErrEvaluatorNameExists)
}

func TestEvaluatorUpdateChangesMetadataOnly(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	evaluator, version := testEvaluator("clarity")
	require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))

	evaluator.Name = "clarity-v2"
	evaluator.Description = "edited"
	evaluator.Tags = []string{"writing"}
	evaluator.Type = "programmatic" // the type of an evaluator can never change
	evaluator.CurrentVersion = 7    // nor can a caller move the version pointer
	require.NoError(t, store.UpdateEvaluator(ctx, evaluator))

	got, err := store.GetEvaluator(ctx, evaluator.ID)
	require.NoError(t, err)
	assert.Equal(t, "clarity-v2", got.Name)
	assert.Equal(t, "edited", got.Description)
	assert.Equal(t, []string{"writing"}, got.Tags)
	assert.Equal(t, "ai", got.Type)
	assert.Equal(t, 1, got.CurrentVersion)

	missing, _ := testEvaluator("ghost")
	missing.ID = "does-not-exist"
	require.ErrorIs(t, store.UpdateEvaluator(ctx, missing), ErrNotFound)
}

func TestEvaluatorVersionsAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	evaluator, version := testEvaluator("faithfulness")
	require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))

	second := &tables.TableEvaluatorVersion{
		EvaluatorID:   evaluator.ID,
		Config:        json.RawMessage(`{"grading_style":"yes_no","instructions":"v2"}`),
		CommitMessage: "tighten wording",
	}
	require.NoError(t, store.CreateEvaluatorVersion(ctx, second))
	assert.Equal(t, 2, second.Version)

	got, err := store.GetEvaluator(ctx, evaluator.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.CurrentVersion, "the evaluator points at the newest version")

	versions, err := store.ListEvaluatorVersions(ctx, evaluator.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, 2, versions[0].Version, "newest first")
	assert.Equal(t, 1, versions[1].Version)

	old, err := store.GetEvaluatorVersion(ctx, evaluator.ID, 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"grading_style":"yes_no","instructions":"v1"}`, string(old.Config), "earlier versions are untouched")

	orphan := &tables.TableEvaluatorVersion{EvaluatorID: "missing", Config: json.RawMessage(`{}`)}
	require.ErrorIs(t, store.CreateEvaluatorVersion(ctx, orphan), ErrNotFound)
}

// Concurrent saves must each get their own version number, with no gaps and no
// duplicates, and the pointer must land on the highest one.
func TestEvaluatorVersionsConcurrentSaves(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)
	// A shared in-memory SQLite database exists per connection, so pin the pool to one.
	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	evaluator, version := testEvaluator("conciseness")
	require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = store.CreateEvaluatorVersion(ctx, &tables.TableEvaluatorVersion{
				EvaluatorID: evaluator.ID,
				Config:      json.RawMessage(fmt.Sprintf(`{"instructions":"writer %d"}`, i)),
			})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	versions, err := store.ListEvaluatorVersions(ctx, evaluator.ID)
	require.NoError(t, err)
	require.Len(t, versions, writers+1)
	for i, v := range versions {
		assert.Equal(t, writers+1-i, v.Version)
	}
	got, err := store.GetEvaluator(ctx, evaluator.ID)
	require.NoError(t, err)
	assert.Equal(t, writers+1, got.CurrentVersion)
}

func TestEvaluatorSoftDelete(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	evaluator, version := testEvaluator("bleu")
	require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))
	require.NoError(t, store.DeleteEvaluator(ctx, evaluator.ID))

	_, err := store.GetEvaluator(ctx, evaluator.ID)
	require.ErrorIs(t, err, ErrNotFound, "a deleted evaluator reads as absent")
	_, err = store.GetEvaluatorByName(ctx, "bleu")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, store.DeleteEvaluator(ctx, evaluator.ID), ErrNotFound, "deleting twice is not found")
	require.ErrorIs(t, store.UpdateEvaluator(ctx, evaluator), ErrNotFound)
	late := &tables.TableEvaluatorVersion{EvaluatorID: evaluator.ID, Config: json.RawMessage(`{}`)}
	require.ErrorIs(t, store.CreateEvaluatorVersion(ctx, late), ErrNotFound, "a deleted evaluator takes no new versions")

	// The versions stay, so results recorded against them can still be explained.
	kept, err := store.GetEvaluatorVersion(ctx, evaluator.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, kept.Version)

	// The name is free again, any number of times.
	for range 2 {
		again, againVersion := testEvaluator("bleu")
		require.NoError(t, store.CreateEvaluator(ctx, again, againVersion))
		require.NotEqual(t, evaluator.ID, again.ID)
		require.NoError(t, store.DeleteEvaluator(ctx, again.ID))
	}
}

func TestEvaluatorList(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	seed := []struct {
		name, typ, description string
		tags                   []string
		builtin                string
	}{
		{"answer relevance", "ai", "is the reply on topic", []string{"rag"}, "output_relevance"},
		{"bleu score", "statistical", "n-gram overlap", []string{"overlap"}, "bleu"},
		{"json shape", "programmatic", "reply parses as JSON", []string{"format", "rag"}, ""},
		{"removed", "ai", "deleted before listing", []string{"rag"}, ""},
	}
	ids := map[string]string{}
	for _, s := range seed {
		evaluator, version := testEvaluator(s.name)
		evaluator.Type, evaluator.Description, evaluator.Tags = s.typ, s.description, s.tags
		if s.builtin != "" {
			evaluator.BuiltinKey = new(s.builtin)
		}
		require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))
		ids[s.name] = evaluator.ID
	}
	require.NoError(t, store.DeleteEvaluator(ctx, ids["removed"]))

	names := func(rows []tables.TableEvaluator) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.Name)
		}
		return out
	}

	all, total, err := store.ListEvaluators(ctx, EvaluatorListParams{})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "deleted evaluators are not listed")
	assert.ElementsMatch(t, []string{"answer relevance", "bleu score", "json shape"}, names(all))

	byType, total, err := store.ListEvaluators(ctx, EvaluatorListParams{Types: []string{"ai"}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, []string{"answer relevance"}, names(byType))
	require.NotNil(t, byType[0].BuiltinKey)
	assert.Equal(t, "output_relevance", *byType[0].BuiltinKey)

	// Several types are a union.
	twoTypes, total, err := store.ListEvaluators(ctx, EvaluatorListParams{Types: []string{"ai", "programmatic"}})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.ElementsMatch(t, []string{"answer relevance", "json shape"}, names(twoTypes))

	builtins, total, err := store.ListEvaluators(ctx, EvaluatorListParams{Source: EvaluatorSourceBuiltin})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.ElementsMatch(t, []string{"answer relevance", "bleu score"}, names(builtins))

	custom, total, err := store.ListEvaluators(ctx, EvaluatorListParams{Source: EvaluatorSourceCustom})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, []string{"json shape"}, names(custom))

	byTag, total, err := store.ListEvaluators(ctx, EvaluatorListParams{Tag: "rag"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.ElementsMatch(t, []string{"answer relevance", "json shape"}, names(byTag))

	// A tag filter matches whole tags, never a fragment of one.
	partial, total, err := store.ListEvaluators(ctx, EvaluatorListParams{Tag: "ra"})
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, partial)

	bySearch, _, err := store.ListEvaluators(ctx, EvaluatorListParams{Search: "OVERLAP"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bleu score"}, names(bySearch), "search covers name and description, ignoring case")

	// LIKE wildcards in a search term are matched literally.
	wild, _, err := store.ListEvaluators(ctx, EvaluatorListParams{Search: "%"})
	require.NoError(t, err)
	assert.Empty(t, wild)

	page, total, err := store.ListEvaluators(ctx, EvaluatorListParams{Limit: 2, Offset: 2})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "the total ignores paging")
	assert.Len(t, page, 1)
}

func setupExecutionTestStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store := setupEvaluatorTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableEvalExecution{}))
	return store
}

func TestEvalExecutionLifecycle(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)

	execution := &tables.TableEvalExecution{
		EvaluatorID:      new("ev-1"),
		EvaluatorVersion: 2,
		Definition:       json.RawMessage(`{"type":"ai","config":{"instructions":"x"}}`),
		Variables:        json.RawMessage(`{"output":"hello"}`),
		CreatedBy:        "tester",
	}
	require.NoError(t, store.CreateEvalExecution(ctx, execution))
	require.NotEmpty(t, execution.ID)

	got, err := store.GetEvalExecution(ctx, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalExecutionQueued, got.Status)
	assert.JSONEq(t, `{"output":"hello"}`, string(got.Variables))
	assert.JSONEq(t, `{"type":"ai","config":{"instructions":"x"}}`, string(got.Definition))
	assert.Nil(t, got.StartedAt)
	assert.Empty(t, got.Result)

	_, err = store.RouteEvalExecution(ctx, execution.ID, "", "key-evals")
	require.NoError(t, err)
	claimed, err := store.StartEvalExecution(ctx, execution.ID, "key-evals", "delivery-1", time.Time{})
	require.NoError(t, err)
	require.True(t, claimed)
	got, err = store.GetEvalExecution(ctx, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalExecutionRunning, got.Status)
	assert.Equal(t, "key-evals", got.KeyID)
	require.NotNil(t, got.StartedAt)

	require.NoError(t, store.CompleteEvalExecution(ctx, execution.ID, json.RawMessage(`{"score":{"status":"ok","bool":true}}`)))
	got, err = store.GetEvalExecution(ctx, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalExecutionCompleted, got.Status)
	assert.JSONEq(t, `{"score":{"status":"ok","bool":true}}`, string(got.Result))
	require.NotNil(t, got.CompletedAt)

	_, err = store.GetEvalExecution(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.StartEvalExecution(ctx, "missing", "", "d", time.Time{})
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, store.CompleteEvalExecution(ctx, "missing", json.RawMessage(`{}`)), ErrNotFound)
}

// The queue delivers at least once, so a job can finish twice. The first result stands:
// a caller that already read it must never see it change.
func TestEvalExecutionCompletionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)

	execution := &tables.TableEvalExecution{Definition: json.RawMessage(`{}`), Variables: json.RawMessage(`{}`)}
	require.NoError(t, store.CreateEvalExecution(ctx, execution))
	require.NoError(t, store.CompleteEvalExecution(ctx, execution.ID, json.RawMessage(`{"n":1}`)))
	require.NoError(t, store.CompleteEvalExecution(ctx, execution.ID, json.RawMessage(`{"n":2}`)), "a repeat completion is not an error")
	claimed, err := store.StartEvalExecution(ctx, execution.ID, "", "late", time.Time{})
	require.NoError(t, err, "nor is a repeat start")
	assert.False(t, claimed, "a completed execution is not claimed again")

	got, err := store.GetEvalExecution(ctx, execution.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"n":1}`, string(got.Result))
	assert.Equal(t, tables.EvalExecutionCompleted, got.Status, "a late start does not reopen a completed execution")
	assert.Empty(t, got.KeyID)
}

// A listing needs the current config of every row. That is one query, not one per row.
func TestGetEvaluatorVersionsAt(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	first, firstVersion := testEvaluator("one")
	require.NoError(t, store.CreateEvaluator(ctx, first, firstVersion))
	require.NoError(t, store.CreateEvaluatorVersion(ctx, &tables.TableEvaluatorVersion{EvaluatorID: first.ID, Config: json.RawMessage(`{"instructions":"one v2"}`)}))
	second, secondVersion := testEvaluator("two")
	require.NoError(t, store.CreateEvaluator(ctx, second, secondVersion))

	versions, err := store.GetEvaluatorVersionsAt(ctx, map[string]int{first.ID: 2, second.ID: 1, "missing": 1})
	require.NoError(t, err)
	require.Len(t, versions, 2, "an evaluator that does not exist is simply absent")
	assert.JSONEq(t, `{"instructions":"one v2"}`, string(versions[first.ID].Config), "the requested version is returned, not the first")
	assert.Equal(t, 2, versions[first.ID].Version)
	assert.Equal(t, 1, versions[second.ID].Version)

	empty, err := store.GetEvaluatorVersionsAt(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// "No eval key" is a property of the provider the current version calls, so the filter
// follows the version pointer.
func TestEvaluatorListByKeyStatus(t *testing.T) {
	ctx := context.Background()
	store := setupEvaluatorTestStore(t)

	create := func(name, provider string) string {
		evaluator, version := testEvaluator(name)
		version.ModelProvider = provider
		require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))
		return evaluator.ID
	}
	create("on openai", "openai")
	moved := create("on anthropic", "anthropic")
	create("code only", "")

	names := func(params EvaluatorListParams) []string {
		rows, total, err := store.ListEvaluators(ctx, params)
		require.NoError(t, err)
		require.EqualValues(t, len(rows), total)
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.Name)
		}
		return out
	}

	withOpenAI := []string{"openai"}
	assert.ElementsMatch(t, []string{"on anthropic"}, names(EvaluatorListParams{KeyStatus: EvaluatorKeyStatusMissing, KeyProviders: withOpenAI}))
	assert.ElementsMatch(t, []string{"on openai", "code only"}, names(EvaluatorListParams{KeyStatus: EvaluatorKeyStatusReady, KeyProviders: withOpenAI}),
		"an evaluator that calls no model is always ready")

	// With no eval key at all, everything that calls a model is blocked.
	assert.ElementsMatch(t, []string{"on openai", "on anthropic"}, names(EvaluatorListParams{KeyStatus: EvaluatorKeyStatusMissing}))
	assert.ElementsMatch(t, []string{"code only"}, names(EvaluatorListParams{KeyStatus: EvaluatorKeyStatusReady}))

	// A new version that moves to a provider with a key unblocks the evaluator.
	require.NoError(t, store.CreateEvaluatorVersion(ctx, &tables.TableEvaluatorVersion{
		EvaluatorID: moved, Config: json.RawMessage(`{"instructions":"v2"}`), ModelProvider: "openai",
	}))
	assert.Empty(t, names(EvaluatorListParams{KeyStatus: EvaluatorKeyStatusMissing, KeyProviders: withOpenAI}))

	assert.Len(t, names(EvaluatorListParams{}), 3, "no status means no filter")
}

// A job is routed to one key's topic. Only one writer may move it, so two instances
// that both notice a stranded job do not both requeue it.
func TestEvalExecutionRouting(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)

	execution := &tables.TableEvalExecution{Definition: json.RawMessage(`{}`), Variables: json.RawMessage(`{}`)}
	require.NoError(t, store.CreateEvalExecution(ctx, execution))

	moved, err := store.RouteEvalExecution(ctx, execution.ID, "", "key-a")
	require.NoError(t, err)
	assert.True(t, moved)
	moved, err = store.RouteEvalExecution(ctx, execution.ID, "", "key-b")
	require.NoError(t, err)
	assert.False(t, moved, "the job already left the unrouted state")
	moved, err = store.RouteEvalExecution(ctx, execution.ID, "key-a", "key-b")
	require.NoError(t, err)
	assert.True(t, moved)

	got, err := store.GetEvalExecution(ctx, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, "key-b", got.KeyID)
	assert.Equal(t, tables.EvalExecutionQueued, got.Status, "routing does not start a job")

	_, err = store.StartEvalExecution(ctx, execution.ID, "key-b", "d", time.Time{})
	require.NoError(t, err)
	moved, err = store.RouteEvalExecution(ctx, execution.ID, "key-b", "key-c")
	require.NoError(t, err)
	assert.False(t, moved, "a job that started is no longer routable")
}

func TestEvalExecutionSweepQueries(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)

	create := func() string {
		execution := &tables.TableEvalExecution{Definition: json.RawMessage(`{}`), Variables: json.RawMessage(`{}`)}
		require.NoError(t, store.CreateEvalExecution(ctx, execution))
		return execution.ID
	}
	waiting, running, finished := create(), create(), create()
	_, err := store.StartEvalExecution(ctx, running, "", "d", time.Time{})
	require.NoError(t, err)
	require.NoError(t, store.CompleteEvalExecution(ctx, finished, json.RawMessage(`{}`)))

	future := time.Now().Add(time.Hour)
	queued, err := store.ListQueuedEvalExecutions(ctx, future, time.Time{}, nil, 10)
	require.NoError(t, err)
	require.Len(t, queued, 1, "only executions that never started")
	assert.Equal(t, waiting, queued[0].ID)

	// Keys that are being consumed are filtered before the limit. Otherwise a page full
	// of old executions on consumed keys would hide a stranded one behind them forever.
	for range 3 {
		id := create()
		_, err := store.RouteEvalExecution(ctx, id, "", "key-consumed")
		require.NoError(t, err)
	}
	stranded := create()
	_, err = store.RouteEvalExecution(ctx, stranded, "", "key-removed")
	require.NoError(t, err)
	page, err := store.ListQueuedEvalExecutions(ctx, future, time.Time{}, []string{"key-consumed"}, 2)
	require.NoError(t, err)
	ids := make([]string, 0, len(page))
	for _, execution := range page {
		ids = append(ids, execution.ID)
	}
	assert.Equal(t, []string{waiting, stranded}, ids, "the consumed key's executions do not take up the page")

	recent, err := store.ListQueuedEvalExecutions(ctx, time.Now().Add(-time.Hour), time.Time{}, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, recent, "an execution queued a moment ago is not stale")

	deleted, err := store.DeleteCompletedEvalExecutions(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, deleted, "a result is kept for the retention period")

	deleted, err = store.DeleteCompletedEvalExecutions(ctx, future)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
	_, err = store.GetEvalExecution(ctx, finished)
	assert.ErrorIs(t, err, ErrNotFound)
	for _, id := range []string{waiting, running} {
		_, err = store.GetEvalExecution(ctx, id)
		assert.NoError(t, err, "unfinished executions are never purged")
	}
}

// Starting an execution is a claim. A queued execution on the delivery's key is claimed
// once; the same delivery may claim it again (the queue redelivers a message whose
// worker died), another delivery may not. A runner on a queue that forgets may take over
// an execution that started before the runner did.
func TestEvalExecutionStartIsAClaim(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)
	execution := &tables.TableEvalExecution{Definition: json.RawMessage(`{}`), Variables: json.RawMessage(`{}`)}
	require.NoError(t, store.CreateEvalExecution(ctx, execution))
	_, err := store.RouteEvalExecution(ctx, execution.ID, "", "key-a")
	require.NoError(t, err)

	claimed, err := store.StartEvalExecution(ctx, execution.ID, "key-b", "d1", time.Time{})
	require.NoError(t, err)
	assert.False(t, claimed, "a message on another key's topic does not start it")

	claimed, err = store.StartEvalExecution(ctx, execution.ID, "key-a", "d1", time.Time{})
	require.NoError(t, err)
	assert.True(t, claimed)

	claimed, err = store.StartEvalExecution(ctx, execution.ID, "key-a", "d2", time.Time{})
	require.NoError(t, err)
	assert.False(t, claimed, "a second message for the same job is refused")

	claimed, err = store.StartEvalExecution(ctx, execution.ID, "key-a", "d1", time.Time{})
	require.NoError(t, err)
	assert.True(t, claimed, "a redelivery of the claiming message runs it again")

	claimed, err = store.StartEvalExecution(ctx, execution.ID, "key-a", "d3", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, claimed, "a job that started before the runner did is taken over")
	got, err := store.GetEvalExecution(ctx, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, "d3", got.ClaimID)
}

// Routing records when a job was put on a key's queue. The sweep listing brings back a
// job on a consumed key once that routing is older than the cutoff, since its message
// may have been lost between routing and publishing.
func TestEvalExecutionRoutedAtAndListing(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)
	execution := &tables.TableEvalExecution{Definition: json.RawMessage(`{}`), Variables: json.RawMessage(`{}`)}
	require.NoError(t, store.CreateEvalExecution(ctx, execution))
	_, err := store.RouteEvalExecution(ctx, execution.ID, "", "key-a")
	require.NoError(t, err)
	got, err := store.GetEvalExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RoutedAt, "routing records its time")

	future := time.Now().Add(time.Hour)
	page, err := store.ListQueuedEvalExecutions(ctx, future, time.Now().Add(-time.Hour), []string{"key-a"}, 10)
	require.NoError(t, err)
	assert.Empty(t, page, "a job routed a moment ago to a consumed key waits its turn")
	page, err = store.ListQueuedEvalExecutions(ctx, future, future, []string{"key-a"}, 10)
	require.NoError(t, err)
	require.Len(t, page, 1, "a job routed before the cutoff comes back even on a consumed key")
	assert.Equal(t, execution.ID, page[0].ID)
}

// A job that started on a key nothing consumes any more, with its claim older than the
// lost cutoff, is listed for the sweep to end. One on a consumed key, one started after
// the cutoff, and a local one are not.
func TestEvalExecutionListingReturnsLostStartedJobs(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)
	start := func(keyID string) string {
		execution := &tables.TableEvalExecution{Definition: json.RawMessage(`{}`), Variables: json.RawMessage(`{}`)}
		require.NoError(t, store.CreateEvalExecution(ctx, execution))
		if keyID != "" {
			_, err := store.RouteEvalExecution(ctx, execution.ID, "", keyID)
			require.NoError(t, err)
		}
		claimed, err := store.StartEvalExecution(ctx, execution.ID, keyID, "d-"+execution.ID, time.Time{})
		require.NoError(t, err)
		require.True(t, claimed)
		return execution.ID
	}
	lost := start("key-gone")
	start("key-a")
	start("")

	future := time.Now().Add(time.Hour)
	page, err := store.ListQueuedEvalExecutions(ctx, future, time.Now().Add(-time.Hour), []string{"key-a"}, 10)
	require.NoError(t, err)
	assert.Empty(t, page, "a job started after the cutoff still has a live claim")
	page, err = store.ListQueuedEvalExecutions(ctx, future, future, []string{"key-a"}, 10)
	require.NoError(t, err)
	require.Len(t, page, 1, "only the started job on the key nothing consumes is lost")
	assert.Equal(t, lost, page[0].ID)
	assert.Equal(t, tables.EvalExecutionRunning, page[0].Status)
	assert.Equal(t, "d-"+lost, page[0].ClaimID)
	require.NotNil(t, page[0].StartedAt)
}

// Unfinished executions are read in pages, so a large backlog is never loaded at once.
func TestEvalExecutionUnfinishedPages(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)
	var want []string
	for range 5 {
		execution := &tables.TableEvalExecution{Definition: json.RawMessage(`{}`), Variables: json.RawMessage(`{}`)}
		require.NoError(t, store.CreateEvalExecution(ctx, execution))
		want = append(want, execution.ID)
	}
	var got []string
	var afterAt time.Time
	afterID := ""
	for range 4 {
		page, err := store.ListUnfinishedEvalExecutions(ctx, afterAt, afterID, 2)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page), 2, "a page holds at most the limit")
		for _, execution := range page {
			got = append(got, execution.ID)
		}
		if len(page) < 2 {
			break
		}
		afterAt, afterID = page[len(page)-1].CreatedAt, page[len(page)-1].ID
	}
	assert.ElementsMatch(t, want, got)
	assert.Len(t, got, 5, "every execution once")
}

// An execution reads through the caller's scope, as its evaluator does: one whose evaluator the
// scope hides reads as missing.
func TestEvalExecutionReadsThroughTheCallersScope(t *testing.T) {
	ctx := context.Background()
	store := setupExecutionTestStore(t)
	evaluator, version := testEvaluator("scoped")
	require.NoError(t, store.CreateEvaluator(ctx, evaluator, version))
	execution := &tables.TableEvalExecution{EvaluatorID: new(evaluator.ID), EvaluatorVersion: 1, Definition: json.RawMessage(`{"type":"ai"}`), Variables: json.RawMessage(`{}`), CreatedBy: "tester"}
	require.NoError(t, store.CreateEvalExecution(ctx, execution))
	draft := &tables.TableEvalExecution{Definition: json.RawMessage(`{"type":"ai"}`), Variables: json.RawMessage(`{}`), CreatedBy: "tester"}
	require.NoError(t, store.CreateEvalExecution(ctx, draft))

	visible := queryscope.WithQueryScope(ctx, queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { return db.Where("1 = 1") }))
	hidden := queryscope.WithQueryScope(ctx, queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") }))
	read, err := store.GetEvalExecution(visible, execution.ID)
	require.NoError(t, err, "an execution whose evaluator the scope shows is read")
	assert.Equal(t, execution.ID, read.ID)
	_, err = store.GetEvalExecution(hidden, execution.ID)
	assert.ErrorIs(t, err, ErrNotFound, "an execution outside the scope reads as missing")
	_, err = store.GetEvalExecution(ctx, execution.ID)
	assert.NoError(t, err, "with no scope the execution is read as before")
	_, err = store.GetEvalExecution(hidden, draft.ID)
	assert.NoError(t, err, "a draft execution has no evaluator to scope by")
}
