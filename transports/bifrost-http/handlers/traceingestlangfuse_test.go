package handlers

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// langfuseAuth is the header a Langfuse SDK sends: Basic public:secret, set to the dashboard
// username and password.
func langfuseAuth() map[string]string {
	return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(traceTestUser+":"+traceTestPassword)), "Content-Type": "application/json"}
}

// TestLangfuseLegacyBatchAnswers207PerEvent pins POST /api/public/ingestion: each event gets its
// own result, a malformed event does not fail the good ones, and the trace, its observation, the
// update and the score all land.
func TestLangfuseLegacyBatchAnswers207PerEvent(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	batch := `{"batch":[
		{"id":"e1","type":"trace-create","timestamp":"2026-11-12T10:00:00Z","body":{"id":"tr-1","name":"chat-app","sessionId":"s1","userId":"u1","tags":["prod"]}},
		{"id":"e2","type":"generation-create","timestamp":"2026-11-12T10:00:00Z","body":{"id":"gen-1","traceId":"tr-1","name":"llm","model":"gpt-4o","startTime":"2026-11-12T10:00:00.100Z","input":[{"role":"user","content":"hi"}]}},
		{"id":"e3","type":"generation-update","timestamp":"2026-11-12T10:00:02Z","body":{"id":"gen-1","traceId":"tr-1","endTime":"2026-11-12T10:00:01.100Z","output":"hello","usage":{"input":9,"output":3,"total":12}}},
		{"id":"e4","type":"score-create","timestamp":"2026-11-12T10:00:03Z","body":{"traceId":"tr-1","name":"helpful","value":0.8}},
		{"id":"e5","type":"span-create","timestamp":"2026-11-12T10:00:03Z","body":{"name":"no ids"}},
		{"id":"e6","type":"sdk-log","timestamp":"2026-11-12T10:00:03Z","body":{"log":"debug"}}
	],"metadata":{"sdk_name":"python"}}`
	resp := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/ingestion", langfuseAuth(), []byte(batch))
	require.Equal(t, fasthttp.StatusMultiStatus, resp.Response.StatusCode(), string(resp.Response.Body()))
	var result struct {
		Successes []struct {
			ID     string `json:"id"`
			Status int    `json:"status"`
		} `json:"successes"`
		Errors []struct {
			ID     string `json:"id"`
			Status int    `json:"status"`
		} `json:"errors"`
	}
	require.NoError(t, sonic.Unmarshal(resp.Response.Body(), &result))
	assert.Len(t, result.Successes, 5)
	assert.Equal(t, 201, result.Successes[0].Status)
	require.Len(t, result.Errors, 1)
	assert.Equal(t, "e5", result.Errors[0].ID)
	assert.Equal(t, 400, result.Errors[0].Status)

	spans, err := env.store.ListTraceSpans(ctx, "p1", "tr-1")
	require.NoError(t, err)
	require.Len(t, spans, 2)
	byID := map[string]*logstore.TraceSpan{}
	for _, span := range spans {
		byID[span.SpanID] = span
	}
	generation := byID["gen-1"]
	assert.Equal(t, "tr-1", generation.ParentSpanID)
	assert.Equal(t, "hello", generation.Output)
	assert.Equal(t, 12, generation.TotalTokens)
	assert.False(t, generation.Partial)

	require.NoError(t, env.service.FlushRollups(ctx))
	trace, err := env.store.FindTrace(ctx, "p1", "tr-1")
	require.NoError(t, err)
	assert.Equal(t, "chat-app", trace.Name)
	assert.Equal(t, "s1", trace.SessionID)
	assert.Equal(t, map[string]float64{"helpful": 0.8}, trace.ScoresParsed)

	assert.Equal(t, fasthttp.StatusUnauthorized, env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/ingestion", map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("pk:sk-wrong"))}, []byte(batch)).Response.StatusCode())
}

// TestLangfuseOTLPScoresMediaAndTraceURL pins the rest of the Langfuse surface an SDK touches: the
// OTLP route with Basic auth, the auth check, a score posted on its own, media upload with reuse
// of a file already held, trace deletion, and the redirect of the URL get_trace_url() prints.
func TestLangfuseOTLPScoresMediaAndTraceURL(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	auth := langfuseAuth()

	otlp := map[string]string{"Authorization": auth["Authorization"], "Content-Type": "application/x-protobuf"}
	resp := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/otel/v1/traces", otlp, otlpTestExport(t, otlpString("service.name", "would-be-a-project")))
	require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
	spans, err := env.store.ListTraceSpans(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	require.Len(t, spans, 3, "the key decides the project: a resource attribute does not")
	assert.Equal(t, logstore.TraceSourceLangfuse, spans[0].Source)

	projects := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/projects", auth, nil)
	require.Equal(t, fasthttp.StatusOK, projects.Response.StatusCode())
	assert.JSONEq(t, `{"data":[{"id":"p1","name":"agent","metadata":{}}]}`, string(projects.Response.Body()))
	assert.Equal(t, fasthttp.StatusUnauthorized, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/projects", nil, nil).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/health", nil, nil).Response.StatusCode())

	score := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/scores", auth, []byte(`{"id":"sc-1","traceId":"`+otlpTestTrace+`","observationId":"`+otlpTestLLM+`","name":"quality","value":"good","dataType":"CATEGORICAL"}`))
	require.Equal(t, fasthttp.StatusOK, score.Response.StatusCode(), string(score.Response.Body()))
	assert.JSONEq(t, `{"id":"sc-1"}`, string(score.Response.Body()))
	scores, err := env.store.ListTraceScores(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.Equal(t, "good", scores[0].StringValue)
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/scores", auth, []byte(`{"name":"quality"}`)).Response.StatusCode())

	// Langfuse 5 posts scores as an array: 202 when every one is taken, 207 with counts otherwise.
	batch := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/scores", auth, []byte(`[{"traceId":"`+otlpTestTrace+`","name":"helpful","value":1,"dataType":"BOOLEAN"},{"traceId":"`+otlpTestTrace+`","name":"tone","value":"formal"}]`))
	require.Equal(t, fasthttp.StatusAccepted, batch.Response.StatusCode(), string(batch.Response.Body()))
	assert.JSONEq(t, `{"message":"Accepted"}`, string(batch.Response.Body()))
	partial := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/scores", auth, []byte(`[{"traceId":"`+otlpTestTrace+`","name":"speed","value":0.5},{"name":"no trace"}]`))
	require.Equal(t, fasthttp.StatusMultiStatus, partial.Response.StatusCode())
	var counts struct {
		Accepted int              `json:"accepted"`
		Rejected int              `json:"rejected"`
		Errors   []map[string]any `json:"errors"`
	}
	require.NoError(t, sonic.Unmarshal(partial.Response.Body(), &counts))
	assert.Equal(t, 1, counts.Accepted)
	assert.Equal(t, 1, counts.Rejected)
	require.Len(t, counts.Errors, 1)
	assert.NotEmpty(t, counts.Errors[0]["message"])
	scores, err = env.store.ListTraceScores(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, scores, 4, "the single score, both batch scores and the accepted half of the partial batch")

	mediaBody := []byte(`{"traceId":"` + otlpTestTrace + `","contentType":"image/png","contentLength":9,"sha256Hash":"n4bQgYhMfWWaL+qgxVrQFaO/TxsrC4Is0V1sFbDwCgg=","field":"input"}`)
	media := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/media", auth, mediaBody)
	require.Equal(t, fasthttp.StatusCreated, media.Response.StatusCode(), string(media.Response.Body()))
	var created struct {
		MediaID   string  `json:"mediaId"`
		UploadURL *string `json:"uploadUrl"`
	}
	require.NoError(t, sonic.Unmarshal(media.Response.Body(), &created))
	require.NotNil(t, created.UploadURL)
	assert.Equal(t, "http://bifrost.test/tracing/uploads/"+created.MediaID, *created.UploadURL)
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPut, "/tracing/uploads/"+created.MediaID, map[string]string{"Content-Type": "image/png"}, []byte("png-bytes")).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusNoContent, env.do(fasthttp.MethodPatch, "/tracing/langfuse/api/public/media/"+created.MediaID, auth, []byte(`{"uploadedAt":"2026-11-12T10:00:00Z","uploadHttpStatus":200}`)).Response.StatusCode())
	again := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/media", auth, mediaBody)
	require.Equal(t, fasthttp.StatusCreated, again.Response.StatusCode())
	assert.JSONEq(t, `{"mediaId":"`+created.MediaID+`","uploadUrl":null}`, string(again.Response.Body()), "a file the project already holds is not uploaded again")
	assert.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/media/"+created.MediaID, auth, nil).Response.StatusCode())

	// The dashboard serves the file back, never as a page that could run script.
	NewTracesHandler(env.store, nil, env.handler, logger).RegisterRoutes(env.router)
	download := env.do(fasthttp.MethodGet, "/api/traces/uploads/"+created.MediaID, nil, nil)
	require.Equal(t, fasthttp.StatusOK, download.Response.StatusCode())
	assert.Equal(t, "png-bytes", string(download.Response.Body()))
	assert.Equal(t, "nosniff", string(download.Response.Header.Peek("X-Content-Type-Options")))

	redirect := env.do(fasthttp.MethodGet, "/tracing/langfuse/project/p1/traces/"+otlpTestTrace, nil, nil)
	assert.Equal(t, fasthttp.StatusFound, redirect.Response.StatusCode())
	assert.Contains(t, string(redirect.Response.Header.Peek("Location")), "/workspace/tracing?project=p1&trace="+otlpTestTrace)

	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodDelete, "/tracing/langfuse/api/public/traces/"+otlpTestTrace, auth, nil).Response.StatusCode())
	spans, err = env.store.ListTraceSpans(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Empty(t, spans)
}

// TestLangfuseReadAPI pins the read routes of Langfuse's current API, in the shapes its SDK models
// require: every required ObservationV2 field is present (as null when Bifrost holds no value),
// paging is by cursor, and a score carries its data type with a value of the matching JSON type.
func TestLangfuseReadAPI(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	auth := langfuseAuth()
	batch := `{"batch":[
		{"id":"e1","type":"trace-create","timestamp":"2026-11-12T10:00:00Z","body":{"id":"tr-1","name":"chat-app"}},
		{"id":"e2","type":"generation-create","timestamp":"2026-11-12T10:00:00Z","body":{"id":"gen-1","traceId":"tr-1","name":"llm","model":"gpt-4o","startTime":"2026-11-12T10:00:00.100Z","endTime":"2026-11-12T10:00:01.100Z","input":[{"role":"user","content":"hi"}],"output":"hello","usage":{"input":9,"output":3,"total":12},"costDetails":{"total":0.0004}}},
		{"id":"e3","type":"span-create","timestamp":"2026-11-12T10:00:00Z","body":{"id":"sp-1","traceId":"tr-1","name":"retrieve","parentObservationId":"gen-1","startTime":"2026-11-12T10:00:00.200Z","level":"ERROR","statusMessage":"timeout"}},
		{"id":"e4","type":"score-create","timestamp":"2026-11-12T10:00:03Z","body":{"id":"sc-num","traceId":"tr-1","observationId":"gen-1","name":"helpful","value":0.8}},
		{"id":"e5","type":"score-create","timestamp":"2026-11-12T10:00:04Z","body":{"id":"sc-cat","traceId":"tr-1","name":"tone","value":"formal"}}
	]}`
	require.Equal(t, fasthttp.StatusMultiStatus, env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/ingestion", auth, []byte(batch)).Response.StatusCode())

	type observationPage struct {
		Data []map[string]any `json:"data"`
		Meta map[string]any   `json:"meta"`
	}
	var observations observationPage
	resp := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?traceId=tr-1&type=GENERATION", auth, nil)
	require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
	require.NoError(t, sonic.Unmarshal(resp.Response.Body(), &observations))
	require.Len(t, observations.Data, 1)
	generation := observations.Data[0]
	for _, required := range []string{"id", "traceId", "startTime", "endTime", "projectId", "parentObservationId", "type", "modelId", "inputPrice", "outputPrice", "totalPrice"} {
		assert.Contains(t, generation, required, "ObservationV2 requires %s", required)
	}
	assert.Equal(t, "GENERATION", generation["type"])
	assert.Nil(t, generation["parentObservationId"], "an observation under the trace root has no parent observation")
	assert.Equal(t, "2026-11-12T10:00:00.100Z", generation["startTime"])
	assert.Equal(t, map[string]any{"input": 9.0, "output": 3.0, "total": 12.0}, generation["usageDetails"])
	assert.InDelta(t, 1.0, generation["latency"], 1e-9, "latency is in seconds")
	assert.IsType(t, []any{}, generation["input"], "content is returned as the JSON it was sent as")

	all := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?traceId=tr-1", auth, nil)
	observations = observationPage{}
	require.NoError(t, sonic.Unmarshal(all.Response.Body(), &observations))
	assert.Len(t, observations.Data, 2, "the span that stands for the trace object is not an observation")
	paged := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?traceId=tr-1&limit=1", auth, nil)
	observations = observationPage{}
	require.NoError(t, sonic.Unmarshal(paged.Response.Body(), &observations))
	require.Len(t, observations.Data, 1)
	require.Equal(t, "1", observations.Meta["cursor"])
	errors := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?level=ERROR", auth, nil)
	observations = observationPage{}
	require.NoError(t, sonic.Unmarshal(errors.Response.Body(), &observations))
	require.Len(t, observations.Data, 1)
	assert.Equal(t, "gen-1", observations.Data[0]["parentObservationId"])
	assert.Equal(t, "timeout", observations.Data[0]["statusMessage"])
	assert.NotContains(t, observations.Meta, "cursor", "the last page carries no cursor")
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?limit=5000", auth, nil).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusUnauthorized, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations", nil, nil).Response.StatusCode())

	var scores struct {
		Data []map[string]any `json:"data"`
		Meta map[string]any   `json:"meta"`
	}
	listed := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v3/scores?traceId=tr-1", auth, nil)
	require.Equal(t, fasthttp.StatusOK, listed.Response.StatusCode(), string(listed.Response.Body()))
	require.NoError(t, sonic.Unmarshal(listed.Response.Body(), &scores))
	require.Len(t, scores.Data, 2)
	assert.EqualValues(t, 50, scores.Meta["limit"])
	byID := map[string]map[string]any{}
	for _, score := range scores.Data {
		byID[score["id"].(string)] = score
		for _, required := range []string{"id", "projectId", "name", "source", "timestamp", "environment", "createdAt", "updatedAt", "dataType", "value"} {
			assert.Contains(t, score, required)
		}
	}
	assert.Equal(t, "NUMERIC", byID["sc-num"]["dataType"])
	assert.Equal(t, 0.8, byID["sc-num"]["value"])
	assert.Equal(t, "API", byID["sc-num"]["source"])
	assert.Equal(t, "CATEGORICAL", byID["sc-cat"]["dataType"])
	assert.Equal(t, "formal", byID["sc-cat"]["value"])

	require.Equal(t, fasthttp.StatusNoContent, env.do(fasthttp.MethodDelete, "/tracing/langfuse/api/public/scores/sc-cat", auth, nil).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusNotFound, env.do(fasthttp.MethodDelete, "/tracing/langfuse/api/public/scores/sc-cat", auth, nil).Response.StatusCode())
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodDelete, "/tracing/langfuse/api/public/traces", auth, []byte(`{"traceIds":["tr-1"]}`)).Response.StatusCode())
	spans, err := env.store.ListTraceSpans(ctx, "p1", "tr-1")
	require.NoError(t, err)
	assert.Empty(t, spans)
}
