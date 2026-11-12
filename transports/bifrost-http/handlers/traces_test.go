package handlers

import (
	"context"
	"path/filepath"
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
	return &tracesAPITestEnv{router: rt, store: store, config: config, service: service}
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
