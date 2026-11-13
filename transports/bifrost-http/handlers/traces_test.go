package handlers

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type tracesAPITestEnv struct {
	ingest  *TraceIngestHandler
	router  *router.Router
	store   logstore.LogStore
	config  configstore.ConfigStore
	service *traceingest.Service
}

// newTracesAPITestEnv builds the dashboard API and the ingest handler over real SQLite stores, the
// way the server wires them.
func newTracesAPITestEnv(t *testing.T) *tracesAPITestEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(dir, "logs.db")}}, logger)
	require.NoError(t, err)
	config, err := configstore.NewConfigStore(ctx, &configstore.Config{Enabled: true, Type: configstore.ConfigStoreTypeSQLite, Config: &configstore.SQLiteConfig{Path: filepath.Join(dir, "config.db")}}, logger)
	require.NoError(t, err)
	service := traceingest.NewService(store, config, traceingest.Config{DefaultProjectName: "agent"}, logger)
	t.Cleanup(func() {
		service.Close()
		_ = store.Close(ctx)
		_ = config.Close(ctx)
	})
	ingest := NewTraceIngestHandler(service, nil, logger)
	rt := router.New()
	ingest.RegisterRoutes(rt)
	NewTracesHandler(store, config, ingest, logger).RegisterRoutes(rt)
	return &tracesAPITestEnv{ingest: ingest, router: rt, store: store, config: config, service: service}
}

func (e *tracesAPITestEnv) call(t *testing.T, method, uri string, body any, out any) int {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(uri)
	ctx.Request.Header.SetHost("bifrost.test")
	if body != nil {
		data, err := schemas.MarshalSorted(body)
		require.NoError(t, err)
		ctx.Request.SetBody(data)
	}
	e.router.Handler(ctx)
	if out != nil && ctx.Response.StatusCode() == fasthttp.StatusOK {
		require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), out), string(ctx.Response.Body()))
	}
	return ctx.Response.StatusCode()
}

// TestTracingProjectsAndReadAPI pins the dashboard API: projects are created and validated, a trace
// opens before its first rollup, the lists and overview read it, the connect info names each SDK's
// URL and whether credentials are needed, and deleting a project removes its traces.
func TestTracingProjectsAndReadAPI(t *testing.T) {
	ctx := context.Background()
	env := newTracesAPITestEnv(t)

	var created struct {
		Project tables.TableTraceProject `json:"project"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": "agent", "retention_days": 30}, &created))
	projectID := created.Project.ID
	require.NotEmpty(t, projectID)
	assert.Equal(t, fasthttp.StatusConflict, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": "agent"}, nil))
	assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": " "}, nil))
	assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": "x", "retention_days": 0}, nil))

	principal := traceingest.AdminPrincipal()
	start := time.Now().UTC().Add(-time.Minute)
	end := start.Add(time.Second)
	require.NoError(t, env.service.Ingest(ctx, principal, &created.Project, logstore.TraceSourceOTel, []*traceingest.SpanPart{
		{TraceID: "t1", SpanID: "root", Name: "agent run", Kind: logstore.TraceSpanKindTask, StartTime: &start, EndTime: &end, SessionID: "s1"},
		{TraceID: "t1", SpanID: "llm", ParentSpanIDs: []string{"root"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: &start, EndTime: &end, PromptTokens: 5, CompletionTokens: 2},
	}))

	// The trace opens before its first rollup has run.
	var detail struct {
		Trace logstore.Trace        `json:"trace"`
		Spans []*logstore.TraceSpan `json:"spans"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces/trace/"+projectID+"/t1", nil, &detail))
	assert.Equal(t, "agent run", detail.Trace.Name)
	assert.Len(t, detail.Spans, 2)
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "GET", "/api/traces/trace/"+projectID+"/absent", nil, nil))

	require.NoError(t, env.service.FlushRollups(ctx))
	var page logstore.TraceSearchResult
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces?project_ids="+projectID+"&search=agent&limit=10", nil, &page))
	require.Len(t, page.Traces, 1)
	assert.Equal(t, 7, page.Traces[0].TotalTokens)
	assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "GET", "/api/traces?start_time=yesterday", nil, nil), "a time bound that does not parse is refused, not dropped")
	assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "GET", "/api/traces?limit=100000", nil, nil))
	var overview struct {
		Stats     logstore.TraceStats          `json:"stats"`
		ByProject []logstore.TraceBreakdownRow `json:"by_project"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces/stats", nil, &overview))
	assert.Equal(t, int64(1), overview.Stats.TraceCount)
	require.Len(t, overview.ByProject, 1)
	for _, uri := range []string{"/api/traces/spans?kinds=llm", "/api/traces/histogram?bucket_size_seconds=60", "/api/traces/sessions", "/api/traces/filterdata", "/api/traces/connect", "/api/traces/projects"} {
		assert.Equal(t, fasthttp.StatusOK, env.call(t, "GET", uri, nil, nil), uri)
	}
	var connect struct {
		Endpoints    map[string]string `json:"endpoints"`
		AuthRequired bool              `json:"auth_required"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces/connect", nil, &connect))
	assert.Equal(t, "http://bifrost.test/tracing/braintrust", connect.Endpoints["braintrust"])
	assert.False(t, connect.AuthRequired, "with no dashboard auth, SDKs send no credential")
	env.ingest.SetCredentials(&fakeDashboardAuth{active: true})
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces/connect", nil, &connect))
	assert.True(t, connect.AuthRequired)

	require.Equal(t, fasthttp.StatusOK, env.call(t, "DELETE", "/api/traces/projects/"+projectID, nil, nil))
	spans, err := env.store.ListTraceSpans(ctx, projectID, "t1")
	require.NoError(t, err)
	assert.Empty(t, spans, "the project's traces go with it")
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "DELETE", "/api/traces/projects/"+projectID, nil, nil))
}

// TestTraceReviewScoresAndComments pins human review: a reviewer's score lands on the trace and in
// its rollup at once, an edit replaces it, a score from an SDK cannot be overwritten through the
// review route, the author is never taken from the request body, and comments round-trip.
func TestTraceReviewScoresAndComments(t *testing.T) {
	ctx := context.Background()
	env := newTracesAPITestEnv(t)
	start := time.Now().UTC().Add(-time.Minute)
	end := start.Add(time.Second)
	require.NoError(t, env.store.UpsertTraceSpans(ctx, []*logstore.TraceSpan{{ProjectID: "p1", TraceID: "t1", SpanID: "root", Source: logstore.TraceSourceOTel, Name: "run", Kind: logstore.TraceSpanKindTask, StartTime: start, EndTime: &end}}))
	require.NoError(t, env.store.UpsertTraceScores(ctx, []*logstore.TraceScore{{ID: "sdk-score", ProjectID: "p1", TraceID: "t1", Name: "auto", Value: new(0.1), Source: logstore.TraceAuthorSDK}}))
	base := "/api/traces/trace/p1/t1"

	var created struct {
		Score logstore.TraceScore `json:"score"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", base+"/scores", map[string]any{"name": "correctness", "value": 0.5, "span_id": "root", "author": "someone-else"}, &created))
	assert.Equal(t, logstore.TraceAuthorHuman, created.Score.Source)
	assert.Empty(t, created.Score.Author, "the author comes from the signed-in user, never from the body")
	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, 0.5, trace.ScoresParsed["correctness"], "the rollup is rebuilt with the review")

	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", base+"/scores", map[string]any{"id": created.Score.ID, "name": "correctness", "value": 1}, nil))
	scores, err := env.store.ListTraceScores(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, scores, 2, "an edit replaces the score")
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "POST", base+"/scores", map[string]any{"id": "sdk-score", "name": "auto", "value": 1}, nil), "an SDK's score is not editable through review")

	for name, body := range map[string]map[string]any{
		"no name":      {"value": 1},
		"no value":     {"name": "x"},
		"foreign span": {"name": "x", "value": 1, "span_id": "not-in-this-trace"},
	} {
		assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "POST", base+"/scores", body, nil), name)
	}
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "POST", "/api/traces/trace/p1/absent/scores", map[string]any{"name": "x", "value": 1}, nil))
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", base+"/scores", map[string]any{"name": "tone", "string_value": "formal"}, nil))

	var comment struct {
		Comment logstore.TraceComment `json:"comment"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", base+"/comments", map[string]any{"text": "  check the tool call  ", "span_id": "root"}, &comment))
	assert.Equal(t, "check the tool call", comment.Comment.Text)
	assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "POST", base+"/comments", map[string]any{"text": " "}, nil))
	require.Equal(t, fasthttp.StatusOK, env.call(t, "DELETE", base+"/comments/"+comment.Comment.ID, nil, nil))
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "DELETE", base+"/comments/"+comment.Comment.ID, nil, nil))

	require.Equal(t, fasthttp.StatusOK, env.call(t, "DELETE", base+"/scores/"+created.Score.ID, nil, nil))
	trace, err = env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.NotContains(t, trace.ScoresParsed, "correctness")
}

// TestTraceProjectSettingsForwardAndDelete pins the project settings and what they drive: a
// masking rule that does not compile is refused, the forward target's credential is never sent
// back to the dashboard and survives an edit that leaves it masked, a stored request is replayed
// to the upstream with that credential, and a deleted trace is gone.
func TestTraceProjectSettingsForwardAndDelete(t *testing.T) {
	ctx := context.Background()
	env := newTracesAPITestEnv(t)
	var mu sync.Mutex
	var upstreamAuth, upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamAuth, upstreamPath = r.Header.Get("Authorization"), r.URL.Path
		mu.Unlock()
	}))
	defer upstream.Close()

	var created struct {
		Project tables.TableTraceProject `json:"project"`
	}
	body := map[string]any{
		"name": "agent", "retention_days": 14,
		"masking_rules": []map[string]any{{"name": "email", "pattern": `[\w.]+@[\w.]+`, "replacement": "[email]"}},
		"forward":       map[string]any{"vendor": "otel", "base_url": upstream.URL, "headers": map[string]string{"Authorization": "Bearer upstream-secret"}, "enabled": true},
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/projects", body, &created))
	projectID := created.Project.ID
	assert.Equal(t, traceForwardSecretMask, created.Project.Forward.Headers["Authorization"], "the upstream credential is never sent back")

	for name, bad := range map[string]map[string]any{
		"pattern does not compile": {"name": "agent", "masking_rules": []map[string]any{{"pattern": "("}}},
		"pattern matches nothing":  {"name": "agent", "masking_rules": []map[string]any{{"pattern": "a*"}}},
		"unknown vendor":           {"name": "agent", "forward": map[string]any{"vendor": "datadog", "base_url": "https://x.test"}},
		"relative url":             {"name": "agent", "forward": map[string]any{"vendor": "otel", "base_url": "/v1/traces"}},
	} {
		assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "PUT", "/api/traces/projects/"+projectID, bad, nil), name)
	}

	// Evaluator rules: refused when malformed, stored when valid, left alone by an edit that omits them.
	for name, bad := range map[string][]map[string]any{
		"no evaluator":   {{"sample_rate": 1}},
		"rate above one": {{"evaluator_id": "ev-1", "sample_rate": 1.5}},
		"listed twice":   {{"evaluator_id": "ev-1", "sample_rate": 1}, {"evaluator_id": "ev-1", "sample_rate": 0.5}},
		"negative cap":   {{"evaluator_id": "ev-1", "sample_rate": 1, "daily_cap": -1}},
	} {
		assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "PUT", "/api/traces/projects/"+projectID, map[string]any{"name": "agent", "evaluators": bad}, nil), name)
	}
	body["evaluators"] = []map[string]any{{"evaluator_id": "ev-1", "name": "faithfulness", "sample_rate": 0.25, "daily_cap": 100, "enabled": true}}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "PUT", "/api/traces/projects/"+projectID, body, nil))
	delete(body, "evaluators")

	// An edit that sends the mask back keeps the stored credential.
	body["description"] = "edited"
	body["forward"].(map[string]any)["headers"] = map[string]string{"Authorization": traceForwardSecretMask}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "PUT", "/api/traces/projects/"+projectID, body, nil))
	stored, err := env.config.GetTraceProject(ctx, projectID)
	require.NoError(t, err)
	assert.Equal(t, "Bearer upstream-secret", stored.Forward.Headers["Authorization"])
	assert.Equal(t, "edited", stored.Description)
	require.Len(t, stored.Evaluators, 1, "an edit that does not send evaluators leaves the rules in place")
	assert.Equal(t, 0.25, stored.Evaluators[0].SampleRate)

	forwarder := traceingest.NewForwarder(nil, logger)
	defer forwarder.Close()
	env.ingest.SetForwarder(forwarder)
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetMethod(fasthttp.MethodPost)
	reqCtx.Request.SetRequestURI("/tracing/otel/v1/traces")
	reqCtx.Request.Header.Set("Authorization", traceTestKey)
	reqCtx.Request.Header.SetContentType("application/x-protobuf")
	reqCtx.Request.SetBody(otlpTestExport(t))
	env.router.Handler(reqCtx)
	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return upstreamPath == "/v1/traces"
	}, 3*time.Second, 10*time.Millisecond, "the stored request is replayed to the project's upstream")
	mu.Lock()
	assert.Equal(t, "Bearer upstream-secret", upstreamAuth, "with the upstream's credential, not the Bifrost tracing key")
	mu.Unlock()

	spans, err := env.store.ListTraceSpans(ctx, projectID, otlpTestTrace)
	require.NoError(t, err)
	require.Len(t, spans, 3)
	require.NotNil(t, spans[0].ExpiresAt)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 14), *spans[0].ExpiresAt, time.Minute, "the project's retention sets each span's expiry")

	require.Equal(t, fasthttp.StatusOK, env.call(t, "DELETE", "/api/traces/trace/"+projectID+"/"+otlpTestTrace, nil, nil))
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "DELETE", "/api/traces/trace/"+projectID+"/"+otlpTestTrace, nil, nil))
	spans, err = env.store.ListTraceSpans(ctx, projectID, otlpTestTrace)
	require.NoError(t, err)
	assert.Empty(t, spans)
	parts, err := env.store.ListTraceSpanParts(ctx, projectID, otlpTestTrace, otlpTestLLM)
	require.NoError(t, err)
	assert.Empty(t, parts, "the received parts go with the trace")
}

// TestSDKPromptFetchesAreServedFromThePromptRepository pins prompt fetches: they need the dashboard
// credentials when dashboard auth is on, Langfuse's get_prompt and Braintrust's load_prompt both get
// the prompt repository's messages and model settings, a version can be pinned, and an SDK never
// creates a prompt.
func TestSDKPromptFetchesAreServedFromThePromptRepository(t *testing.T) {
	ctx := context.Background()
	env := newTracesAPITestEnv(t)
	env.ingest.SetPromptStore(env.config)

	prompt := &tables.TablePrompt{ID: "prompt-1", Name: "Support Reply"}
	require.NoError(t, env.config.CreatePrompt(ctx, prompt))
	message := func(role, content string) tables.TablePromptVersionMessage {
		raw, err := schemas.MarshalSorted(map[string]any{"id": role, "index": 0, "originalType": "completion_request", "currentType": "completion_request", "payload": map[string]any{"role": role, "content": content}})
		require.NoError(t, err)
		return tables.TablePromptVersionMessage{Message: raw}
	}
	require.NoError(t, env.config.CreatePromptVersion(ctx, &tables.TablePromptVersion{PromptID: prompt.ID, CommitMessage: "first", Model: "gpt-4o", Provider: "openai",
		Messages: []tables.TablePromptVersionMessage{message("system", "Be brief."), message("user", "{{question}}")}}))
	require.NoError(t, env.config.CreatePromptVersion(ctx, &tables.TablePromptVersion{PromptID: prompt.ID, CommitMessage: "second", Model: "gpt-4o-mini", Provider: "openai",
		ModelParams: tables.ModelParams{"temperature": 0.2}, Messages: []tables.TablePromptVersionMessage{message("system", "Be kind."), message("user", "{{question}}")}}))

	var project struct {
		Project tables.TableTraceProject `json:"project"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": "agent"}, &project))
	env.ingest.SetCredentials(&fakeDashboardAuth{active: true})
	get := func(uri string, headers map[string]string) (int, map[string]any) {
		reqCtx := &fasthttp.RequestCtx{}
		reqCtx.Request.Header.SetMethod(fasthttp.MethodGet)
		reqCtx.Request.SetRequestURI(uri)
		for name, value := range headers {
			reqCtx.Request.Header.Set(name, value)
		}
		env.router.Handler(reqCtx)
		var body map[string]any
		_ = sonic.Unmarshal(reqCtx.Response.Body(), &body)
		return reqCtx.Response.StatusCode(), body
	}

	status, _ := get("/tracing/langfuse/api/public/v2/prompts/Support%20Reply", nil)
	assert.Equal(t, fasthttp.StatusUnauthorized, status, "with dashboard auth on, a prompt fetch needs the dashboard credentials")

	reader := map[string]string{"Authorization": "Bearer " + traceTestKey}
	status, latest := get("/tracing/langfuse/api/public/v2/prompts/Support%20Reply", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "chat", latest["type"])
	assert.EqualValues(t, 2, latest["version"])
	assert.Equal(t, []any{"production", "latest"}, latest["labels"], "the newest version is what a fetch with no label gets")
	assert.Equal(t, []any{
		map[string]any{"type": "chatmessage", "role": "system", "content": "Be kind."},
		map[string]any{"type": "chatmessage", "role": "user", "content": "{{question}}"},
	}, latest["prompt"])
	assert.Equal(t, map[string]any{"model": "gpt-4o-mini", "provider": "openai", "temperature": 0.2}, latest["config"])
	for _, required := range []string{"name", "version", "config", "labels", "tags", "prompt", "type"} {
		assert.Contains(t, latest, required)
	}
	status, pinned := get("/tracing/langfuse/api/public/v2/prompts/Support%20Reply?version=1", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []any{}, pinned["labels"])
	assert.Equal(t, "Be brief.", pinned["prompt"].([]any)[0].(map[string]any)["content"])
	status, _ = get("/tracing/langfuse/api/public/v2/prompts/Support%20Reply?version=9", reader)
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = get("/tracing/langfuse/api/public/v2/prompts/absent", reader)
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, list := get("/tracing/langfuse/api/public/v2/prompts", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	require.Len(t, list["data"].([]any), 1)
	assert.Equal(t, []any{2.0, 1.0}, list["data"].([]any)[0].(map[string]any)["versions"])

	status, loaded := get("/tracing/braintrust/v1/prompt?project_name=agent&slug=support-reply", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	objects := loaded["objects"].([]any)
	require.Len(t, objects, 1)
	object := objects[0].(map[string]any)
	assert.Equal(t, "support-reply", object["slug"])
	assert.Equal(t, "2", object["_xact_id"])
	data := object["prompt_data"].(map[string]any)
	assert.Equal(t, "gpt-4o-mini", data["options"].(map[string]any)["model"])
	assert.Equal(t, map[string]any{"role": "system", "content": "Be kind."}, data["prompt"].(map[string]any)["messages"].([]any)[0])
	status, none := get("/tracing/braintrust/v1/prompt?project_name=agent&slug=absent", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Empty(t, none["objects"], "an unknown slug is an empty list, which the SDK reports as not found")

	write := &fasthttp.RequestCtx{}
	write.Request.Header.SetMethod(fasthttp.MethodPost)
	write.Request.SetRequestURI("/tracing/langfuse/api/public/v2/prompts")
	write.Request.Header.Set("Authorization", reader["Authorization"])
	write.Request.SetBody([]byte(`{"name":"x","prompt":"y"}`))
	env.router.Handler(write)
	assert.Equal(t, fasthttp.StatusForbidden, write.Response.StatusCode(), "an SDK never writes a prompt")
}

// TestSDKDatasetCallsFillEvalsDatasets pins the SDK dataset routes: they need the dashboard
// credentials when dashboard auth is on, Braintrust's init_dataset() and its rows land in an evals dataset, and
// Langfuse's create_dataset(), create_dataset_item() and get_dataset() read and write the same kind.
func TestSDKDatasetCallsFillEvalsDatasets(t *testing.T) {
	ctx := context.Background()
	env := newTracesAPITestEnv(t)
	service := datasets.NewService(env.config, nil)
	env.ingest.SetDatasetService(service)
	var project struct {
		Project tables.TableTraceProject `json:"project"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": "agent"}, &project))
	env.ingest.SetCredentials(&fakeDashboardAuth{active: true})
	send := func(method, uri, auth, body string) (int, map[string]any) {
		reqCtx := &fasthttp.RequestCtx{}
		reqCtx.Request.Header.SetMethod(method)
		reqCtx.Request.SetRequestURI(uri)
		reqCtx.Request.Header.Set("Authorization", auth)
		reqCtx.Request.SetBody([]byte(body))
		env.router.Handler(reqCtx)
		var decoded map[string]any
		_ = sonic.Unmarshal(reqCtx.Response.Body(), &decoded)
		return reqCtx.Response.StatusCode(), decoded
	}

	status, _ := send("POST", "/tracing/braintrust/api/dataset/register", "Bearer wrong", `{"project_name":"agent","dataset_name":"golden"}`)
	assert.Equal(t, fasthttp.StatusUnauthorized, status, "with dashboard auth on, dataset calls need the dashboard credentials")

	auth := "Bearer " + traceTestKey
	status, registered := send("POST", "/tracing/braintrust/api/dataset/register", auth, `{"project_name":"agent","dataset_name":"golden","description":"regression set"}`)
	require.Equal(t, fasthttp.StatusOK, status)
	datasetID := registered["dataset"].(map[string]any)["id"].(string)
	_, again := send("POST", "/tracing/braintrust/api/dataset/register", auth, `{"project_name":"agent","dataset_name":"golden"}`)
	assert.Equal(t, datasetID, again["dataset"].(map[string]any)["id"], "registering the same name returns the same dataset")

	flush := `{"rows":[
		{"id":"rec-1","dataset_id":"` + datasetID + `","project_id":"` + project.Project.ID + `","span_id":"rec-1","root_span_id":"rec-1","input":{"question":"2+2"},"expected":"4","metadata":{"difficulty":"easy"},"created":"2026-11-12T10:00:00Z"},
		{"id":"rec-2","dataset_id":"` + datasetID + `","span_id":"rec-2","root_span_id":"rec-2","input":"capital of France?","expected":"Paris"}
	],"api_version":2}`
	status, _ = send("POST", "/tracing/braintrust/logs3", auth, flush)
	require.Equal(t, fasthttp.StatusOK, status)
	page, err := service.Rows(ctx, datasetID, datasets.RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), page.Total)
	spans, err := env.store.ListTraceSpans(ctx, project.Project.ID, "rec-1")
	require.NoError(t, err)
	assert.Empty(t, spans, "a dataset record is not stored as a trace")

	lf := "Basic " + base64.StdEncoding.EncodeToString([]byte(traceTestUser+":"+traceTestPassword))
	status, created := send("POST", "/tracing/langfuse/api/public/v2/datasets", lf, `{"name":"support cases","description":"from tickets"}`)
	require.Equal(t, fasthttp.StatusOK, status)
	for _, required := range []string{"id", "name", "projectId", "createdAt", "updatedAt"} {
		assert.Contains(t, created, required)
	}
	status, item := send("POST", "/tracing/langfuse/api/public/dataset-items", lf, `{"datasetName":"support cases","input":{"ticket":"refund"},"expectedOutput":"Refund issued","metadata":{"tier":"gold"}}`)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.NotEmpty(t, item["id"])
	assert.Equal(t, map[string]any{"ticket": "refund"}, item["input"], "structured input comes back as the JSON it was sent as")
	status, fetched := send("GET", "/tracing/langfuse/api/public/v2/datasets/support%20cases", lf, "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, created["id"], fetched["id"])
	status, items := send("GET", "/tracing/langfuse/api/public/dataset-items?datasetName=support%20cases&page=1&limit=10", lf, "")
	require.Equal(t, fasthttp.StatusOK, status)
	require.Len(t, items["data"].([]any), 1)
	listed := items["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "Refund issued", listed["expectedOutput"])
	assert.Equal(t, map[string]any{"tier": "gold"}, listed["metadata"])
	assert.Equal(t, "ACTIVE", listed["status"])
	assert.EqualValues(t, 1, items["meta"].(map[string]any)["totalItems"])
	status, _ = send("GET", "/tracing/langfuse/api/public/v2/datasets/absent", lf, "")
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, all := send("GET", "/tracing/langfuse/api/public/v2/datasets", lf, "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Len(t, all["data"].([]any), 2)
}

// TestExperimentsAreListedWithTheirSummaries pins experiments end to end: a Langfuse dataset run
// tags its traces and is recorded by name, a repeat run item does not duplicate it, and the
// dashboard lists each experiment with the scores, tokens and cost of its own traces.
func TestExperimentsAreListedWithTheirSummaries(t *testing.T) {
	ctx := context.Background()
	env := newTracesAPITestEnv(t)
	env.ingest.SetDatasetService(datasets.NewService(env.config, nil))
	var project struct {
		Project tables.TableTraceProject `json:"project"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": "agent"}, &project))
	projectID := project.Project.ID
	env.ingest.SetCredentials(&fakeDashboardAuth{active: true})
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(traceTestUser+":"+traceTestPassword))
	send := func(method, uri, body string) (int, map[string]any) {
		reqCtx := &fasthttp.RequestCtx{}
		reqCtx.Request.Header.SetMethod(method)
		reqCtx.Request.SetRequestURI(uri)
		reqCtx.Request.Header.Set("Authorization", auth)
		reqCtx.Request.SetBody([]byte(body))
		env.router.Handler(reqCtx)
		var decoded map[string]any
		_ = sonic.Unmarshal(reqCtx.Response.Body(), &decoded)
		return reqCtx.Response.StatusCode(), decoded
	}

	for i, score := range []string{"1", "0.5"} {
		trace := "tr-" + strconv.Itoa(i)
		batch := `{"batch":[
			{"id":"a` + trace + `","type":"trace-create","timestamp":"2026-11-12T10:00:00Z","body":{"id":"` + trace + `","name":"case","input":"q","output":"a"}},
			{"id":"b` + trace + `","type":"generation-create","timestamp":"2026-11-12T10:00:00Z","body":{"id":"g-` + trace + `","traceId":"` + trace + `","startTime":"2026-11-12T10:00:00Z","endTime":"2026-11-12T10:00:02Z","usage":{"input":8,"output":2,"total":10},"costDetails":{"total":0.001}}},
			{"id":"c` + trace + `","type":"score-create","timestamp":"2026-11-12T10:00:03Z","body":{"traceId":"` + trace + `","name":"exact","value":` + score + `}}
		]}`
		status, _ := send("POST", "/tracing/langfuse/api/public/ingestion", batch)
		require.Equal(t, fasthttp.StatusMultiStatus, status)
		status, item := send("POST", "/tracing/langfuse/api/public/dataset-run-items", `{"runName":"nightly","datasetItemId":"item-`+trace+`","traceId":"`+trace+`","datasetName":"golden"}`)
		require.Equal(t, fasthttp.StatusOK, status)
		assert.Equal(t, "nightly", item["datasetRunName"])
		for _, required := range []string{"id", "datasetRunId", "datasetItemId", "traceId", "createdAt", "updatedAt"} {
			assert.Contains(t, item, required)
		}
	}
	status, _ := send("POST", "/tracing/langfuse/api/public/dataset-run-items", `{"datasetItemId":"x"}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	require.NoError(t, env.service.FlushRollups(ctx))

	tagged, err := env.store.SearchTraces(ctx, logstore.TraceFilters{ProjectIDs: []string{projectID}}, logstore.PaginationOptions{})
	require.NoError(t, err)
	require.Len(t, tagged.Traces, 2)
	assert.NotEmpty(t, tagged.Traces[0].ExperimentID, "the run item tagged its trace with the run")
	spans, err := env.store.ListTraceSpans(ctx, projectID, "tr-0")
	require.NoError(t, err)
	assert.Len(t, spans, 2, "tagging a trace adds no span to it")

	var listed struct {
		Experiments []struct {
			ID          string                 `json:"id"`
			Name        string                 `json:"name"`
			Source      string                 `json:"source"`
			DatasetName string                 `json:"dataset_name"`
			Summary     TraceExperimentSummary `json:"summary"`
		} `json:"experiments"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces/experiments?project_ids="+projectID, nil, &listed))
	require.Len(t, listed.Experiments, 1, "two run items of one run are one experiment")
	experiment := listed.Experiments[0]
	assert.Equal(t, "nightly", experiment.Name)
	assert.Equal(t, logstore.TraceSourceLangfuse, experiment.Source)
	assert.Equal(t, "golden", experiment.DatasetName)
	assert.Equal(t, 2, experiment.Summary.TraceCount)
	assert.InDelta(t, 0.75, experiment.Summary.Scores["exact"], 1e-9)
	assert.Equal(t, 20, experiment.Summary.TotalTokens)
	assert.InDelta(t, 0.002, experiment.Summary.TotalCost, 1e-9)
	assert.Equal(t, tagged.Traces[0].ExperimentID, experiment.ID)
}
