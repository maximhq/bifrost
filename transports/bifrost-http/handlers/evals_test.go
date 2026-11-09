package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

type evalsTestAPI struct {
	t      *testing.T
	router *router.Router
}

// newEvalsTestAPI serves the evals routes over a real SQLite config store, so the
// migration, the store, the service and the handler are exercised together.
func newEvalsTestAPI(t *testing.T, keys []evals.EvalKey, judge evals.Judge) *evalsTestAPI {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "evals.db")},
	}, &mockLogger{})
	require.NoError(t, err)

	engine := evals.NewEngine(evals.EngineOptions{})
	evals.RegisterAIEvaluator(engine, judge)
	evals.RegisterBuiltinAIEvaluators(engine, judge)
	evals.RegisterProgrammaticEvaluator(engine, evals.ProgrammaticOptions{})
	evals.RegisterAPIEvaluator(engine, evals.APIOptions{})
	evals.RegisterStatisticalEvaluators(engine, nil)

	q, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	runner := evals.NewRunner(q, engine, func(context.Context) ([]evals.EvalKey, error) { return keys, nil },
		evals.NewExecutionJobStore(store), evals.RunnerOptions{PollInterval: 5 * time.Millisecond})
	require.NoError(t, runner.Start(ctx))
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(closeCtx)
		_ = q.Close(closeCtx)
	})

	r := router.New()
	NewEvalsHandler(evals.NewService(store, engine, runner)).RegisterRoutes(r)
	return &evalsTestAPI{t: t, router: r}
}

// do sends one request and returns the status and body.
func (a *evalsTestAPI) do(method, path, body string) (int, []byte) {
	a.t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(path)
	if body != "" {
		req.SetBodyString(body)
	}
	// Init gives the context a server, which the database driver needs to watch for cancellation.
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, nil, nil)
	a.router.Handler(ctx)
	return ctx.Response.StatusCode(), append([]byte(nil), ctx.Response.Body()...)
}

const evalsLengthEvaluator = `{"name":"Long enough","description":"length check","type":"programmatic","tags":["format"],"config":{"code":"def validate(output):\n    return len(output) > 3\n","response_type":"yes_no"}}`

func TestEvalsHandler_EvaluatorLifecycle(t *testing.T) {
	api := newEvalsTestAPI(t, nil, nil)

	status, body := api.do("POST", "/api/evals/evaluators", evalsLengthEvaluator)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, "evaluator.id").String()
	require.NotEmpty(t, id)
	assert.Equal(t, "Long enough", gjson.GetBytes(body, "evaluator.name").String())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "evaluator.version").Int())
	assert.Equal(t, "output", gjson.GetBytes(body, "evaluator.variables.0").String())
	assert.True(t, gjson.GetBytes(body, "evaluator.executable").Bool())

	status, body = api.do("POST", "/api/evals/evaluators", evalsLengthEvaluator)
	assert.Equal(t, fasthttp.StatusConflict, status, "a taken name is a conflict: %s", body)

	status, body = api.do("GET", "/api/evals/evaluators?type=programmatic&search=LONG", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int())
	assert.Equal(t, id, gjson.GetBytes(body, "evaluators.0.id").String())

	status, body = api.do("GET", "/api/evals/evaluators?type=ai", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, int64(0), gjson.GetBytes(body, "total").Int())

	status, body = api.do("GET", "/api/evals/evaluators?type=ai,programmatic&source=custom", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int(), "several types are a union")
	assert.Equal(t, "output", gjson.GetBytes(body, "evaluators.0.variables.0").String(), "a listing carries each evaluator's summary")
	assert.Equal(t, "=", gjson.GetBytes(body, "evaluators.0.pass_criterion.operator").String(), "a yes/no evaluator passes on Yes")
	assert.False(t, gjson.GetBytes(body, "evaluators.0.config").Exists(), "but never its config")

	status, body = api.do("GET", "/api/evals/evaluators?source=builtin", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(0), gjson.GetBytes(body, "total").Int())

	status, _ = api.do("GET", "/api/evals/evaluators?source=other", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	status, _ = api.do("GET", "/api/evals/evaluators?limit=zero", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	// A config change makes version 2; history keeps both.
	status, body = api.do("PUT", "/api/evals/evaluators/"+id, `{"name":"Long enough","description":"stricter","config":{"code":"def validate(output):\n    return len(output) > 10\n","response_type":"yes_no"},"commit_message":"raise the bar"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "evaluator.version").Int())

	status, body = api.do("GET", "/api/evals/evaluators/"+id+"/versions", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, int64(2), gjson.GetBytes(body, "versions.#").Int())
	assert.Equal(t, "raise the bar", gjson.GetBytes(body, "versions.0.commit_message").String())

	status, body = api.do("GET", "/api/evals/evaluators/"+id+"/versions/1", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Contains(t, gjson.GetBytes(body, "evaluator.config.code").String(), "> 3")
	status, _ = api.do("GET", "/api/evals/evaluators/"+id+"/versions/nine", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	status, _ = api.do("GET", "/api/evals/evaluators/"+id+"/versions/9", "")
	assert.Equal(t, fasthttp.StatusNotFound, status)

	status, body = api.do("POST", "/api/evals/evaluators/"+id+"/duplicate", `{"name":"Long enough v2"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.NotEqual(t, id, gjson.GetBytes(body, "evaluator.id").String())

	status, _ = api.do("DELETE", "/api/evals/evaluators/"+id, "")
	require.Equal(t, fasthttp.StatusOK, status)
	status, _ = api.do("GET", "/api/evals/evaluators/"+id, "")
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = api.do("DELETE", "/api/evals/evaluators/"+id, "")
	assert.Equal(t, fasthttp.StatusNotFound, status)

	// The name is free again once the evaluator is deleted.
	status, body = api.do("POST", "/api/evals/evaluators", evalsLengthEvaluator)
	assert.Equal(t, fasthttp.StatusOK, status, string(body))
}

func TestEvalsHandler_BadRequests(t *testing.T) {
	api := newEvalsTestAPI(t, nil, nil)
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"malformed json", "POST", "/api/evals/evaluators", `{"name":`, fasthttp.StatusBadRequest},
		{"no name", "POST", "/api/evals/evaluators", `{"type":"programmatic","config":{}}`, fasthttp.StatusBadRequest},
		{"invalid config", "POST", "/api/evals/evaluators", `{"name":"x","type":"ai","config":{"grading_style":"yes_no"}}`, fasthttp.StatusBadRequest},
		{"unknown evaluator", "GET", "/api/evals/evaluators/nope", "", fasthttp.StatusNotFound},
		{"update unknown evaluator", "PUT", "/api/evals/evaluators/nope", `{"name":"x"}`, fasthttp.StatusNotFound},
		{"unknown built-in", "POST", "/api/evals/catalog/vibes/install", `{}`, fasthttp.StatusNotFound},
		{"judged built-in without a model", "POST", "/api/evals/catalog/clarity/install", `{}`, fasthttp.StatusBadRequest},
		{"draft execute without a draft", "POST", "/api/evals/evaluators/execute", `{"variables":{}}`, fasthttp.StatusBadRequest},
		{"unknown execution", "GET", "/api/evals/executions/nope", "", fasthttp.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := api.do(tc.method, tc.path, tc.body)
			assert.Equal(t, tc.want, status, string(body))
		})
	}
}

func TestEvalsHandler_Validate(t *testing.T) {
	api := newEvalsTestAPI(t, nil, nil)

	status, body := api.do("POST", "/api/evals/evaluators/validate", `{"type":"programmatic","config":{"code":"def validate(output, expected_output):\n    return output == expected_output\n","response_type":"yes_no"}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.True(t, gjson.GetBytes(body, "valid").Bool())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "variables.#").Int())

	// An invalid config is still a 200: the verdict is the answer.
	status, body = api.do("POST", "/api/evals/evaluators/validate", `{"type":"programmatic","config":{"code":"def validate(","response_type":"yes_no"}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.False(t, gjson.GetBytes(body, "valid").Bool())
	assert.Contains(t, gjson.GetBytes(body, "error").String(), "syntax")
}

func TestEvalsHandler_Catalog(t *testing.T) {
	api := newEvalsTestAPI(t, nil, nil)

	status, body := api.do("GET", "/api/evals/catalog", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, int64(len(evals.Catalog())), gjson.GetBytes(body, "catalog.#").Int())
	assert.NotEmpty(t, gjson.GetBytes(body, "catalog.0.variables").Array())

	status, body = api.do("POST", "/api/evals/catalog/bleu/install", `{"name":"BLEU","tags":["overlap"]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, "evaluator.id").String()
	assert.Equal(t, "bleu", gjson.GetBytes(body, "evaluator.builtin_key").String())
	assert.Equal(t, "statistical", gjson.GetBytes(body, "evaluator.type").String())

	// A statistical built-in runs with no key and no model.
	status, body = api.do("POST", "/api/evals/evaluators/"+id+"/execute", `{"variables":{"output":"on the mat the cat sat quietly today","expected_output":"the cat sat quietly on the mat today"}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "completed", gjson.GetBytes(body, "execution.status").String())
	assert.InDelta(t, 0.517, gjson.GetBytes(body, "execution.result.score.number").Float(), 1e-12)
	assert.True(t, gjson.GetBytes(body, "execution.result.pass").Bool(), "0.517 clears BLEU's default threshold of 0.5")
}

func TestEvalsHandler_Execute(t *testing.T) {
	api := newEvalsTestAPI(t, nil, nil)
	_, body := api.do("POST", "/api/evals/evaluators", evalsLengthEvaluator)
	id := gjson.GetBytes(body, "evaluator.id").String()
	require.NotEmpty(t, id)

	status, body := api.do("POST", "/api/evals/evaluators/"+id+"/execute", `{"variables":{"output":"hello there"}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "completed", gjson.GetBytes(body, "execution.status").String())
	assert.Equal(t, "ok", gjson.GetBytes(body, "execution.result.score.status").String())
	assert.True(t, gjson.GetBytes(body, "execution.result.score.bool").Bool())
	assert.True(t, gjson.GetBytes(body, "execution.result.pass").Bool())
	executionID := gjson.GetBytes(body, "execution.id").String()

	status, body = api.do("GET", "/api/evals/executions/"+executionID, "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, id, gjson.GetBytes(body, "execution.evaluator_id").String())

	// A missing variable is a completed execution with an "na" score, not an HTTP error.
	status, body = api.do("POST", "/api/evals/evaluators/"+id+"/execute", `{"variables":{}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "na", gjson.GetBytes(body, "execution.result.score.status").String())

	// A draft in the body cannot hijack an execute on a saved evaluator.
	status, body = api.do("POST", "/api/evals/evaluators/"+id+"/execute", `{"variables":{"output":"hello there"},"draft":{"type":"programmatic","config":{"code":"def validate(output):\n    return False\n","response_type":"yes_no"}}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.True(t, gjson.GetBytes(body, "execution.result.score.bool").Bool(), "the saved evaluator ran, not the draft")

	status, body = api.do("POST", "/api/evals/evaluators/execute", `{"draft":{"type":"programmatic","config":{"code":"def validate(output):\n    return len(output)\n","response_type":"number"}},"variables":{"output":"four"}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, float64(4), gjson.GetBytes(body, "execution.result.score.number").Float())

	// With no wait the call answers 202 and the client polls.
	status, body = api.do("POST", "/api/evals/evaluators/"+id+"/execute", `{"variables":{"output":"hello there"},"wait_ms":0}`)
	if status == fasthttp.StatusAccepted {
		pending := gjson.GetBytes(body, "execution.id").String()
		require.Eventually(t, func() bool {
			_, polled := api.do("GET", "/api/evals/executions/"+pending, "")
			return gjson.GetBytes(polled, "execution.status").String() == "completed"
		}, 10*time.Second, 10*time.Millisecond)
	} else {
		// The job can finish before the first read, which is also correct.
		assert.Equal(t, fasthttp.StatusOK, status, string(body))
	}
}

func TestEvalsHandler_AIEvaluatorNeedsAnEvalKey(t *testing.T) {
	judge := func(ctx context.Context, _ evals.JudgeRequest) (evals.JudgeResponse, error) {
		return evals.JudgeResponse{Text: `{"reasoning":"polite, on key ` + evals.KeyIDFromContext(ctx) + `","score":true}`}, nil
	}
	const draft = `{"draft":{"type":"ai","config":{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}},"variables":{"output":"thank you"}}`

	without := newEvalsTestAPI(t, nil, judge)
	status, body := without.do("POST", "/api/evals/evaluators/execute", draft)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, status, string(body))
	assert.Contains(t, string(body), "use for evaluations")

	with := newEvalsTestAPI(t, []evals.EvalKey{{ID: "key-evals", Provider: "openai", Concurrency: 2}}, judge)
	status, body = with.do("POST", "/api/evals/evaluators/execute", draft)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.True(t, gjson.GetBytes(body, "execution.result.score.bool").Bool())
	assert.Equal(t, "key-evals", gjson.GetBytes(body, "execution.key_id").String())
	assert.Contains(t, gjson.GetBytes(body, "execution.result.reasoning").String(), "on key key-evals")
}

func TestEvalsHandler_HumanEvaluatorIsDefinitionOnly(t *testing.T) {
	api := newEvalsTestAPI(t, nil, nil)
	status, body := api.do("POST", "/api/evals/evaluators", `{"name":"Reviewer rating","type":"human","config":{"instructions":"Rate helpfulness","evaluation_type":"scale","scale":{"min":1,"max":5}}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.False(t, gjson.GetBytes(body, "evaluator.executable").Bool())

	status, body = api.do("POST", "/api/evals/evaluators/"+gjson.GetBytes(body, "evaluator.id").String()+"/execute", `{"variables":{}}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, string(body), "human evaluator")
}

// The UI needs to know which providers can run a judge, without reading every key.
func TestEvalsHandler_ListsEvalKeys(t *testing.T) {
	api := newEvalsTestAPI(t, []evals.EvalKey{
		{ID: "key-2", Name: "evals-key", Provider: "openai", Concurrency: 8},
		{ID: "key-1", Name: "judge", Provider: "anthropic", Concurrency: 4, Supports: func(string) bool { return true }},
	}, nil)

	status, body := api.do("GET", "/api/evals/keys", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "keys.#").Int())
	assert.Equal(t, "anthropic", gjson.GetBytes(body, "keys.0.provider").String(), "sorted by provider, then name")
	assert.Equal(t, "openai", gjson.GetBytes(body, "keys.1.provider").String())
	assert.Equal(t, "key-2", gjson.GetBytes(body, "keys.1.key_id").String())
	assert.Equal(t, "evals-key", gjson.GetBytes(body, "keys.1.name").String())
	assert.Equal(t, int64(8), gjson.GetBytes(body, "keys.1.concurrency").Int())

	none := newEvalsTestAPI(t, nil, nil)
	status, body = none.do("GET", "/api/evals/keys", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "[]", gjson.GetBytes(body, "keys").Raw, "no eval key is an empty list, not null")
}

// The list can be narrowed to evaluators that cannot run because their provider has no
// key marked for evaluations.
func TestEvalsHandler_FiltersByKeyStatus(t *testing.T) {
	judge := func(context.Context, evals.JudgeRequest) (evals.JudgeResponse, error) {
		return evals.JudgeResponse{Text: `{"score":"Yes","reasoning":"ok"}`}, nil
	}
	api := newEvalsTestAPI(t, []evals.EvalKey{{ID: "key-1", Name: "evals", Provider: "openai", Concurrency: 2}}, judge)

	create := func(name, provider string) {
		body := `{"name":"` + name + `","type":"ai","config":{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"` + provider + `","model":"m"}}`
		status, resp := api.do("POST", "/api/evals/evaluators", body)
		require.Equal(t, fasthttp.StatusOK, status, string(resp))
	}
	create("has key", "openai")
	create("no key", "anthropic")
	status, body := api.do("POST", "/api/evals/evaluators", evalsLengthEvaluator)
	require.Equal(t, fasthttp.StatusOK, status, string(body))

	status, body = api.do("GET", "/api/evals/evaluators?status=no_key", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int())
	assert.Equal(t, "no key", gjson.GetBytes(body, "evaluators.0.name").String())

	status, body = api.do("GET", "/api/evals/evaluators?status=ready", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "total").Int(), "the keyed judge and the code evaluator")

	status, _ = api.do("GET", "/api/evals/evaluators?status=broken", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}
