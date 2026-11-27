package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
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
	service := traceingest.NewService(store, config, traceingest.Config{}, logger)
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

// TestTracingKeysAndProjectsAPI pins the management API: a key's value is returned once and never
// again, a key must be bound to real projects, an edit reaches the ingest path at once, and
// deleting a project removes its traces and the bindings keys held to it.
func TestTracingKeysAndProjectsAPI(t *testing.T) {
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

	assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "POST", "/api/traces/keys", map[string]any{"name": "ci"}, nil), "a key with no project and no create permission can write nowhere")
	assert.Equal(t, fasthttp.StatusBadRequest, env.call(t, "POST", "/api/traces/keys", map[string]any{"name": "ci", "project_ids": []string{"missing"}}, nil))
	var keyResponse struct {
		Key   map[string]any `json:"key"`
		Value string         `json:"value"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/keys", map[string]any{"name": "ci", "project_ids": []string{projectID}, "default_project_id": projectID}, &keyResponse))
	require.Contains(t, keyResponse.Value, tables.TracingKeyPrefix)
	assert.NotContains(t, keyResponse.Key, "key_hash", "the hash is never sent to the dashboard")
	keyID, _ := keyResponse.Key["id"].(string)

	var listed struct {
		Keys []map[string]any `json:"keys"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces/keys", nil, &listed))
	require.Len(t, listed.Keys, 1)
	raw, err := schemas.MarshalSorted(listed)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), keyResponse.Value, "the value is shown once, at creation")
	assert.Equal(t, true, listed.Keys[0]["log_content"], "content is stored unless the key says otherwise")

	principal, err := env.service.Authenticate(ctx, keyResponse.Value)
	require.NoError(t, err)
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
		Endpoints map[string]string `json:"endpoints"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "GET", "/api/traces/connect", nil, &connect))
	assert.Equal(t, "http://bifrost.test/tracing/braintrust", connect.Endpoints["braintrust"])

	// Disabling the key in the dashboard stops ingest on the next request.
	require.Equal(t, fasthttp.StatusOK, env.call(t, "PUT", "/api/traces/keys/"+keyID, map[string]any{"name": "ci", "project_ids": []string{projectID}, "enabled": false}, nil))
	_, err = env.service.Authenticate(ctx, keyResponse.Value)
	assert.ErrorIs(t, err, traceingest.ErrUnauthorized)

	require.Equal(t, fasthttp.StatusOK, env.call(t, "DELETE", "/api/traces/projects/"+projectID, nil, nil))
	spans, err := env.store.ListTraceSpans(ctx, projectID, "t1")
	require.NoError(t, err)
	assert.Empty(t, spans, "the project's traces go with it")
	key, err := env.config.GetTracingKey(ctx, keyID)
	require.NoError(t, err)
	assert.Empty(t, key.ProjectIDs, "the key no longer points at the deleted project")
	assert.Equal(t, fasthttp.StatusOK, env.call(t, "DELETE", "/api/traces/keys/"+keyID, nil, nil))
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "DELETE", "/api/traces/keys/"+keyID, nil, nil))
}

// failingDeleteStore fails the first DeleteTraces, as a database that drops mid-delete would.
type failingDeleteStore struct {
	logstore.LogStore
	failed *atomic.Bool
}

func (s failingDeleteStore) DeleteTraces(ctx context.Context, keys []logstore.TraceKey) error {
	if s.failed.CompareAndSwap(false, true) {
		return errors.New("database went away")
	}
	return s.LogStore.DeleteTraces(ctx, keys)
}

// A project delete that fails partway can be retried. The project row is gone after the first
// attempt, so the retry finds the leftover traces instead of answering 404 and leaving them.
func TestDeleteProjectCanBeRetriedAfterAFailure(t *testing.T) {
	env := newTracesAPITestEnv(t)
	ctx := context.Background()
	project := &tables.TableTraceProject{Name: "retried"}
	require.NoError(t, env.config.CreateTraceProject(ctx, project))
	require.NoError(t, env.store.UpsertTraceSpans(ctx, []*logstore.TraceSpan{{ProjectID: project.ID, TraceID: "t1", SpanID: "s", Source: logstore.TraceSourceOTel, StartTime: time.Now()}}))
	_, err := env.store.RecomputeTraceRollup(ctx, project.ID, "t1")
	require.NoError(t, err)

	rt := router.New()
	NewTracesHandler(failingDeleteStore{LogStore: env.store, failed: &atomic.Bool{}}, env.config, NewTraceIngestHandler(env.service, nil, logger), logger).RegisterRoutes(rt)
	retry := &tracesAPITestEnv{router: rt, store: env.store, config: env.config, service: env.service}
	assert.Equal(t, fasthttp.StatusInternalServerError, retry.call(t, "DELETE", "/api/traces/projects/"+project.ID, nil, nil))
	assert.Equal(t, fasthttp.StatusOK, retry.call(t, "DELETE", "/api/traces/projects/"+project.ID, nil, nil), "the retry finishes the delete")
	keys, err := env.store.ListProjectTraceKeys(ctx, project.ID, 10)
	require.NoError(t, err)
	assert.Empty(t, keys)
	assert.Equal(t, fasthttp.StatusNotFound, retry.call(t, "DELETE", "/api/traces/projects/"+project.ID, nil, nil), "with nothing left it is not found")
}

// overflowingHistogramStore answers every histogram the way a store does when the bucket size splits
// unbounded data into too many buckets.
type overflowingHistogramStore struct {
	logstore.LogStore
}

func (overflowingHistogramStore) GetTraceHistogram(context.Context, logstore.TraceFilters, int64) (*logstore.TraceHistogramResult, error) {
	return nil, logstore.ErrTooManyHistogramBuckets
}

// A histogram with no time range that would return too many buckets is the caller's mistake, the
// same 400 a bounded range gets, not a server error.
func TestHistogramWithTooManyBucketsIsABadRequest(t *testing.T) {
	env := newTracesAPITestEnv(t)
	rt := router.New()
	NewTracesHandler(overflowingHistogramStore{LogStore: env.store}, env.config, NewTraceIngestHandler(env.service, nil, logger), logger).RegisterRoutes(rt)
	overflow := &tracesAPITestEnv{router: rt, store: env.store, config: env.config, service: env.service}
	assert.Equal(t, fasthttp.StatusBadRequest, overflow.call(t, "GET", "/api/traces/histogram?bucket_size_seconds=1", nil, nil))
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

	// Review deletes only what a reviewer wrote: SDK and evaluator rows are the client's record.
	require.NoError(t, env.store.CreateTraceComment(ctx, &logstore.TraceComment{ID: "sdk-comment", ProjectID: "p1", TraceID: "t1", Text: "from the SDK", Source: logstore.TraceAuthorSDK}))
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "DELETE", base+"/comments/sdk-comment", nil, nil), "an SDK's comment is not deletable through review")
	assert.Equal(t, fasthttp.StatusNotFound, env.call(t, "DELETE", base+"/scores/sdk-score", nil, nil), "an SDK's score is not deletable through review")
	scores, err = env.store.ListTraceScores(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.True(t, slices.ContainsFunc(scores, func(s *logstore.TraceScore) bool { return s.ID == "sdk-score" }), "the SDK score is still stored")

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

	var key struct {
		Value string `json:"value"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/keys", map[string]any{"name": "ci", "project_ids": []string{projectID}, "default_project_id": projectID}, &key))
	forwarder := traceingest.NewForwarder(nil, logger)
	defer forwarder.Close()
	env.ingest.SetForwarder(forwarder)
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetMethod(fasthttp.MethodPost)
	reqCtx.Request.SetRequestURI("/tracing/otel/v1/traces")
	reqCtx.Request.Header.Set("Authorization", key.Value)
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

// TestSDKPromptFetchesAreServedFromThePromptRepository pins prompt fetches: a key reads prompts only
// when it was given that permission, Langfuse's get_prompt and Braintrust's load_prompt both get
// the prompt repository's messages and model settings, a version can be pinned, and a tracing key
// never creates a prompt.
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
	newKey := func(canRead bool) map[string]string {
		var key struct {
			Value string `json:"value"`
		}
		require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/keys", map[string]any{"name": "k", "project_ids": []string{project.Project.ID}, "can_read_prompts": canRead}, &key))
		return map[string]string{"Authorization": "Bearer " + key.Value}
	}
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

	status, _ := get("/tracing/langfuse/api/public/v2/prompts/Support%20Reply", newKey(false))
	assert.Equal(t, fasthttp.StatusForbidden, status, "a tracing key reads prompts only when given that permission")
	status, _ = get("/tracing/langfuse/api/public/v2/prompts/Support%20Reply", nil)
	assert.Equal(t, fasthttp.StatusUnauthorized, status)

	reader := newKey(true)
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
	assert.Equal(t, fasthttp.StatusForbidden, write.Response.StatusCode(), "a tracing key never writes a prompt, even one that may read them")
}

// sdkPromptReader returns the headers of a caller allowed to read prompts through the SDK routes.
func sdkPromptReader(t *testing.T, env *tracesAPITestEnv) map[string]string {
	var project struct {
		Project tables.TableTraceProject `json:"project"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/projects", map[string]any{"name": "prompt-readers"}, &project))
	var key struct {
		Value string `json:"value"`
	}
	require.Equal(t, fasthttp.StatusOK, env.call(t, "POST", "/api/traces/keys", map[string]any{"name": "reader", "project_ids": []string{project.Project.ID}, "can_read_prompts": true}, &key))
	return map[string]string{"Authorization": "Bearer " + key.Value}
}

// sdkPromptGet calls an SDK prompt route and decodes the JSON answer.
func sdkPromptGet(env *tracesAPITestEnv, uri string, headers map[string]string) (int, map[string]any) {
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

// storedPromptMessage is a prompt message in the dashboard's serialized form.
func storedPromptMessage(t *testing.T, role string, content any) tables.TablePromptVersionMessage {
	raw, err := schemas.MarshalSorted(map[string]any{"id": role, "index": 0, "originalType": "completion_request", "currentType": "completion_request", "payload": map[string]any{"role": role, "content": content}})
	require.NoError(t, err)
	return tables.TablePromptVersionMessage{Message: raw}
}

// countingPromptStore counts the whole-repository reads a prompt fetch must not need.
type countingPromptStore struct {
	*configstore.RDBConfigStore
	allPrompts, allVersions int
}

func (c *countingPromptStore) GetPrompts(ctx context.Context, folderID *string) ([]tables.TablePrompt, error) {
	c.allPrompts++
	return c.RDBConfigStore.GetPrompts(ctx, folderID)
}

func (c *countingPromptStore) GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error) {
	c.allVersions++
	return c.RDBConfigStore.GetPromptVersions(ctx, promptID)
}

// An exact name wins over a slug alias, an alias two prompts share is refused, image parts are
// kept or refused (never dropped), and a fetch reads one version instead of the whole repository.
func TestSDKPromptLookupNamesImagesAndBoundedReads(t *testing.T) {
	ctx := context.Background()
	env := newTracesAPITestEnv(t)
	counting := &countingPromptStore{RDBConfigStore: env.config.(*configstore.RDBConfigStore)}
	env.ingest.SetPromptStore(counting)
	reader := sdkPromptReader(t, env)

	exact := &tables.TablePrompt{ID: "exact", Name: "Support Reply"}
	require.NoError(t, env.config.CreatePrompt(ctx, exact))
	require.NoError(t, env.config.CreatePromptVersion(ctx, &tables.TablePromptVersion{PromptID: exact.ID, Model: "gpt-4o", Messages: []tables.TablePromptVersionMessage{storedPromptMessage(t, "system", "exact one")}}))
	// Created later, so it is listed first; its slug is the same.
	alias := &tables.TablePrompt{ID: "alias", Name: "Support-Reply"}
	require.NoError(t, env.config.CreatePrompt(ctx, alias))
	require.NoError(t, env.config.CreatePromptVersion(ctx, &tables.TablePromptVersion{PromptID: alias.ID, Model: "gpt-4o", Messages: []tables.TablePromptVersionMessage{storedPromptMessage(t, "system", "alias one")}}))

	status, got := sdkPromptGet(env, "/tracing/langfuse/api/public/v2/prompts/Support%20Reply", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "exact one", got["prompt"].([]any)[0].(map[string]any)["content"], "the exact name wins over a slug alias")
	status, _ = sdkPromptGet(env, "/tracing/braintrust/v1/prompt?slug=support-reply", reader)
	assert.Equal(t, fasthttp.StatusConflict, status, "a slug two prompts share is ambiguous")
	assert.Zero(t, counting.allPrompts, "a fetch does not load every prompt's latest version")
	assert.Zero(t, counting.allVersions, "a fetch does not load every version's messages")

	status, list := sdkPromptGet(env, "/tracing/langfuse/api/public/v2/prompts", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Len(t, list["data"].([]any), 2)
	assert.Zero(t, counting.allPrompts+counting.allVersions, "the list reads metadata only")

	image := &tables.TablePrompt{ID: "image", Name: "Describe"}
	require.NoError(t, env.config.CreatePrompt(ctx, image))
	parts := []any{map[string]any{"type": "text", "text": "What is this?"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png"}}}
	require.NoError(t, env.config.CreatePromptVersion(ctx, &tables.TablePromptVersion{PromptID: image.ID, Model: "gpt-4o", Messages: []tables.TablePromptVersionMessage{storedPromptMessage(t, "user", parts)}}))
	status, _ = sdkPromptGet(env, "/tracing/langfuse/api/public/v2/prompts/Describe", reader)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, status, "a Langfuse chat message holds text only, so an image prompt is refused, not flattened")
	status, loaded := sdkPromptGet(env, "/tracing/braintrust/v1/prompt?slug=describe", reader)
	require.Equal(t, fasthttp.StatusOK, status)
	messages := loaded["objects"].([]any)[0].(map[string]any)["prompt_data"].(map[string]any)["prompt"].(map[string]any)["messages"].([]any)
	assert.Equal(t, parts, messages[0].(map[string]any)["content"], "Braintrust gets the content parts, image included")
}
