package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/evals/runs"
	"github.com/maximhq/bifrost/framework/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// newEvalsRunsTestAPI serves the run, dataset and evaluator routes over a real SQLite
// config store and an in-memory queue, so a run goes through the migration, the
// store, the runner, the service and the handler together.
func newEvalsRunsTestAPI(t *testing.T) *evalsTestAPI {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "runs.db")},
	}, &mockLogger{})
	require.NoError(t, err)

	engine := evals.NewEngine(evals.EngineOptions{})
	evals.RegisterProgrammaticEvaluator(engine, evals.ProgrammaticOptions{})
	datasetService := datasets.NewService(store, nil)
	evaluatorService := evals.NewService(store, engine, nil)
	runService := runs.NewService(runs.Options{Store: store, Datasets: datasetService, Evaluators: evaluatorService, Engine: engine})

	q, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	runner := evals.NewRunner(q, engine, func(context.Context) ([]evals.EvalKey, error) { return nil, nil },
		runs.NewJobStore(evals.NewExecutionJobStore(store), runService), evals.RunnerOptions{PollInterval: 2 * time.Millisecond})
	require.NoError(t, runner.Start(ctx))
	runService.SetRunner(runner)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(closeCtx)
		_ = q.Close(closeCtx)
	})

	r := router.New()
	NewEvalsHandler(evaluatorService).RegisterRoutes(r)
	NewEvalsDatasetsHandler(datasetService).RegisterRoutes(r)
	NewEvalsRunsHandler(runService).RegisterRoutes(r)
	return &evalsTestAPI{t: t, router: r}
}

// awaitRun polls a run until it reaches the status and returns its body.
func (a *evalsTestAPI) awaitRun(id, status string) []byte {
	a.t.Helper()
	var body []byte
	require.Eventually(a.t, func() bool {
		var code int
		code, body = a.do("GET", "/api/evals/runs/"+id, "")
		return code == fasthttp.StatusOK && gjson.GetBytes(body, "run.status").String() == status
	}, 15*time.Second, 5*time.Millisecond, "run %s did not reach %s: %s", id, status, body)
	return body
}

func TestEvalsRunsHandler_Lifecycle(t *testing.T) {
	api := newEvalsRunsTestAPI(t)

	status, body := api.do("POST", "/api/evals/datasets", `{"name":"Golden","template":"qa","columns":[{"name":"output","type":"output"}]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	datasetID := gjson.GetBytes(body, "dataset.id").String()
	status, body = api.do("POST", "/api/evals/datasets/"+datasetID+"/rows",
		`{"rows":[{"input":"2+2","expected_output":"4","output":"4"},{"input":"capital","expected_output":"Paris","output":"Lyon"}]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))

	config, err := json.Marshal(map[string]any{"code": "def validate(output, expected_output):\n    return output == expected_output\n", "response_type": "yes_no"})
	require.NoError(t, err)
	status, body = api.do("POST", "/api/evals/evaluators", fmt.Sprintf(`{"name":"exact match","type":"programmatic","config":%s}`, config))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	evaluatorID := gjson.GetBytes(body, "evaluator.id").String()
	require.NotEmpty(t, evaluatorID)

	run := fmt.Sprintf(`{"name":"Baseline","tags":["nightly"],"dataset_id":%q,"target":{"type":"dataset"},"evaluators":[{"id":%q}]}`, datasetID, evaluatorID)

	// A dry check reports what would stop a run, without starting one.
	status, body = api.do("POST", "/api/evals/runs/validate", run)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.True(t, gjson.GetBytes(body, "valid").Bool(), string(body))
	status, body = api.do("POST", "/api/evals/runs/validate", strings.Replace(run, `"type":"dataset"`, `"type":"model","provider":"openai","model":"gpt-4o"`, 1))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.False(t, gjson.GetBytes(body, "valid").Bool())
	assert.Contains(t, gjson.GetBytes(body, "problems.0").String(), "use for evaluations")

	status, body = api.do("POST", "/api/evals/runs", strings.Replace(run, `"Baseline"`, `""`, 1))
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(body))
	status, body = api.do("POST", "/api/evals/runs", run)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, "run.id").String()
	require.NotEmpty(t, id)
	assert.Equal(t, int64(1), gjson.GetBytes(body, "run.dataset_version").Int())

	body = api.awaitRun(id, "completed")
	assert.Equal(t, int64(2), gjson.GetBytes(body, "run.total").Int())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "run.counts.completed").Int())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "run.passed").Int())
	assert.Equal(t, "exact match", gjson.GetBytes(body, "run.summary.evaluators.0.name").String())
	assert.InDelta(t, 50, gjson.GetBytes(body, "run.summary.evaluators.0.pass_rate").Float(), 1e-9)

	status, body = api.do("GET", "/api/evals/runs?search=base&status=completed&tag=nightly&dataset_id="+datasetID, "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int())
	assert.Equal(t, id, gjson.GetBytes(body, "runs.0.id").String())

	status, body = api.do("GET", "/api/evals/runs/"+id+"/entries?passed=false", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int())
	assert.Equal(t, "Lyon", gjson.GetBytes(body, "entries.0.output").String())
	entryID := gjson.GetBytes(body, "entries.0.id").String()
	status, body = api.do("GET", "/api/evals/runs/"+id+"/entries?limit=1&offset=1&status=completed", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "entries.#").Int())
	status, body = api.do("GET", "/api/evals/runs/"+id+"/entries/"+entryID, "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "completed", gjson.GetBytes(body, "entry.results.0.status").String())
	status, _ = api.do("GET", "/api/evals/runs/"+id+"/entries/missing", "")
	assert.Equal(t, fasthttp.StatusNotFound, status)

	// The entries of a run can be read against a baseline run on the same dataset.
	status, body = api.do("POST", "/api/evals/runs", strings.Replace(run, `"Baseline"`, `"Candidate"`, 1))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	candidateID := gjson.GetBytes(body, "run.id").String()
	api.awaitRun(candidateID, "completed")
	status, body = api.do("GET", "/api/evals/runs/"+candidateID+"/entries?baseline="+id, "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, id, gjson.GetBytes(body, "entries.0.baseline.run_id").String())
	assert.True(t, gjson.GetBytes(body, "entries.0.baseline.results.0.id").Exists())
	status, body = api.do("GET", "/api/evals/runs/"+candidateID+"/entries?baseline="+id+"&change=worse", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(0), gjson.GetBytes(body, "total").Int(), "the same target on the same rows scores the same")
	status, _ = api.do("GET", "/api/evals/runs/"+candidateID+"/entries?change=worse", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a change needs a baseline")
	status, _ = api.do("GET", "/api/evals/runs/"+candidateID+"/entries?baseline="+candidateID, "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	status, _ = api.do("DELETE", "/api/evals/runs/"+candidateID, "")
	require.Equal(t, fasthttp.StatusOK, status)

	status, body = api.do("GET", "/api/evals/runs/"+id+"/export", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Contains(t, string(body), "exact match: score")
	assert.Len(t, strings.Split(strings.TrimSpace(string(body)), "\n"), 3)

	status, body = api.do("POST", "/api/evals/runs/"+id+"/stop", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a finished run cannot be stopped: %s", body)
	status, body = api.do("POST", "/api/evals/runs/"+id+"/rerun", `{"scope":"all"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "running", gjson.GetBytes(body, "run.status").String())
	api.awaitRun(id, "completed")
	status, body = api.do("POST", "/api/evals/runs/"+id+"/evaluators", fmt.Sprintf(`{"evaluators":[{"id":%q}]}`, evaluatorID))
	assert.Equal(t, fasthttp.StatusBadRequest, status, "the run already has it: %s", body)
	status, body = api.do("PUT", "/api/evals/runs/"+id+"/entries/"+entryID+"/results/"+evaluatorID, `{"score":true}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "only human evaluators are reviewed: %s", body)

	status, body = api.do("PUT", "/api/evals/runs/"+id, `{"name":"Baseline v2","tags":[]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "Baseline v2", gjson.GetBytes(body, "run.name").String())

	// The dataset and its pinned version are protected while the run exists.
	status, body = api.do("DELETE", "/api/evals/datasets/"+datasetID, "")
	assert.Equal(t, fasthttp.StatusConflict, status, string(body))

	status, body = api.do("POST", "/api/evals/runs", strings.Replace(run, `"Baseline"`, `"Second"`, 1))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	second := gjson.GetBytes(body, "run.id").String()
	api.awaitRun(second, "completed")
	status, body = api.do("GET", "/api/evals/runs/compare?ids="+id, "")
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(body))
	status, body = api.do("GET", "/api/evals/runs/compare?ids="+id+","+second, "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "runs.#").Int())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "rows.#").Int())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "rows.0.entries.#").Int())

	status, _ = api.do("DELETE", "/api/evals/runs/"+id, "")
	require.Equal(t, fasthttp.StatusOK, status)
	status, _ = api.do("GET", "/api/evals/runs/"+id, "")
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = api.do("DELETE", "/api/evals/runs/"+second, "")
	require.Equal(t, fasthttp.StatusOK, status)
	status, body = api.do("DELETE", "/api/evals/datasets/"+datasetID, "")
	assert.Equal(t, fasthttp.StatusOK, status, string(body))
}
