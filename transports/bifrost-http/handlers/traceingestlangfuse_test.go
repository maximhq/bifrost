package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// langfuseAuth is the header a Langfuse SDK sends: Basic public:secret, the secret being the key.
func langfuseAuth() map[string]string {
	return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("pk-lf-anything:"+traceTestKey)), "Content-Type": "application/json"}
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

	mediaBody := []byte(`{"traceId":"` + otlpTestTrace + `","contentType":"image/png","contentLength":9,"sha256Hash":"6oAzQ2Pu0UXf7uUeuufcPxzX0MeHn4v9IHDAYdPDP1Y=","field":"input"}`)
	media := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/media", auth, mediaBody)
	require.Equal(t, fasthttp.StatusCreated, media.Response.StatusCode(), string(media.Response.Body()))
	var created struct {
		MediaID   string  `json:"mediaId"`
		UploadURL *string `json:"uploadUrl"`
	}
	require.NoError(t, sonic.Unmarshal(media.Response.Body(), &created))
	require.NotNil(t, created.UploadURL)
	assert.True(t, strings.HasPrefix(*created.UploadURL, "http://bifrost.test/tracing/uploads/"+created.MediaID+"?token="), *created.UploadURL)
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPut, strings.TrimPrefix(*created.UploadURL, "http://bifrost.test"), map[string]string{"Content-Type": "image/png"}, []byte("png-bytes")).Response.StatusCode())
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

// A Langfuse upload id is derived from the file's SHA-256, so anyone who knows the file can compute
// the upload URL. The receiver checks the bytes against that hash, so only the file itself lands.
func TestLangfuseUploadAcceptsOnlyTheHashedBytes(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	auth := map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("pk-lf-any:"+traceTestKey))}
	real := []byte("the real image bytes")
	sum := sha256.Sum256(real)
	body := []byte(`{"traceId":"` + otlpTestTrace + `","contentType":"image/png","contentLength":20,"sha256Hash":"` + base64.StdEncoding.EncodeToString(sum[:]) + `","field":"input"}`)
	media := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/media", auth, body)
	require.Equal(t, fasthttp.StatusCreated, media.Response.StatusCode(), string(media.Response.Body()))
	var created struct {
		UploadURL string `json:"uploadUrl"`
	}
	require.NoError(t, sonic.Unmarshal(media.Response.Body(), &created))
	uploadPath := strings.TrimPrefix(created.UploadURL, "http://bifrost.test")

	forged := env.do(fasthttp.MethodPut, uploadPath, map[string]string{"Content-Type": "image/png"}, []byte("something else entirely"))
	assert.Equal(t, fasthttp.StatusBadRequest, forged.Response.StatusCode(), "bytes that do not match the hash are refused")
	ok := env.do(fasthttp.MethodPut, uploadPath, map[string]string{"Content-Type": "image/png"}, real)
	assert.Equal(t, fasthttp.StatusOK, ok.Response.StatusCode(), string(ok.Response.Body()))
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
	resp := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?traceId=tr-1&type=GENERATION&fields=core,basic,io,usage,metrics", auth, nil)
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
	require.IsType(t, "", generation["input"], "IO is always returned as the raw string")
	assert.JSONEq(t, `[{"role":"user","content":"hi"}]`, generation["input"].(string))
	assert.Equal(t, "hello", generation["output"])

	// Field groups: core and basic by default; core (and the schema's always-present nullable
	// fields) whatever is asked for.
	bare := observationPage{}
	require.NoError(t, sonic.Unmarshal(env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?traceId=tr-1&type=GENERATION", auth, nil).Response.Body(), &bare))
	require.Len(t, bare.Data, 1)
	assert.Equal(t, "llm", bare.Data[0]["name"], "basic is a default group")
	for _, absent := range []string{"input", "output", "usageDetails", "latency", "metadata"} {
		assert.NotContains(t, bare.Data[0], absent, "%s is not in the default groups", absent)
	}
	coreOnly := observationPage{}
	require.NoError(t, sonic.Unmarshal(env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?traceId=tr-1&type=GENERATION&fields=core", auth, nil).Response.Body(), &coreOnly))
	require.Len(t, coreOnly.Data, 1)
	assert.NotContains(t, coreOnly.Data[0], "name")
	assert.Contains(t, coreOnly.Data[0], "modelId", "the schema's required nullable fields are always present")
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?parseIoAsJson=true", auth, nil).Response.StatusCode(), "parseIoAsJson=true is refused")
	assert.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?parseIoAsJson=false", auth, nil).Response.StatusCode())

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
	assert.Equal(t, "sp-1", observations.Data[0]["id"])
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

// Observation filters follow Langfuse's definitions: an empty parentObservationId means roots only,
// environment may repeat, and every level filters by the level the observation is rendered with.
func TestLangfuseObservationFilters(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	auth := langfuseAuth()
	start := time.Now().UTC().Add(-time.Minute)
	span := func(id, parent, environment, status string) *logstore.TraceSpan {
		return &logstore.TraceSpan{ProjectID: "p1", TraceID: "tf", SpanID: id, ParentSpanID: parent, Source: logstore.TraceSourceLangfuse, Name: id, Kind: logstore.TraceSpanKindTask, StartTime: start, Environment: environment, Status: status}
	}
	require.NoError(t, env.store.UpsertTraceSpans(ctx, []*logstore.TraceSpan{
		span("a", "", "prod", logstore.TraceSpanStatusOK),
		span("b", "a", "staging", logstore.TraceSpanStatusError),
		span("c", "a", "dev", logstore.TraceSpanStatusOK),
	}))
	ids := func(query string) []string {
		resp := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?traceId=tf&"+query, auth, nil)
		require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), query+": "+string(resp.Response.Body()))
		var page struct {
			Data []map[string]any `json:"data"`
		}
		require.NoError(t, sonic.Unmarshal(resp.Response.Body(), &page))
		found := make([]string, 0, len(page.Data))
		for _, observation := range page.Data {
			found = append(found, observation["id"].(string))
		}
		slices.Sort(found)
		return found
	}
	assert.Equal(t, []string{"a"}, ids("parentObservationId="), "an empty parent means roots only")
	assert.Equal(t, []string{"b", "c"}, ids("parentObservationId=a"))
	assert.Equal(t, []string{"a", "b"}, ids("environment=prod&environment=staging"), "every environment occurrence counts")
	assert.Equal(t, []string{"a", "c"}, ids("environment=prod,dev"))
	assert.Equal(t, []string{"a", "c"}, ids("level=DEFAULT"), "DEFAULT excludes what renders as ERROR")
	assert.Equal(t, []string{"b"}, ids("level=ERROR"))
	assert.Empty(t, ids("level=WARNING"), "Bifrost stores no WARNING or DEBUG observations")
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v2/observations?level=LOUD", auth, nil).Response.StatusCode())
}

// Page sizes up to Langfuse's maximum of 1000 keep every observation reachable through the cursor.
func TestLangfuseObservationPagingAboveFiveHundred(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	auth := langfuseAuth()
	start := time.Now().UTC().Add(-time.Hour)
	const total = 1001
	spans := make([]*logstore.TraceSpan, total)
	for i := range spans {
		spans[i] = &logstore.TraceSpan{ProjectID: "p1", TraceID: "big", SpanID: fmt.Sprintf("s%04d", i), ParentSpanID: "root", Source: logstore.TraceSourceLangfuse, Name: "n", Kind: logstore.TraceSpanKindTask, StartTime: start.Add(time.Duration(i) * time.Millisecond)}
	}
	require.NoError(t, env.store.UpsertTraceSpans(ctx, spans))
	for _, limit := range []int{499, 500, 1000} {
		seen := map[string]struct{}{}
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			url := fmt.Sprintf("/tracing/langfuse/api/public/v2/observations?traceId=big&fields=core&limit=%d", limit)
			if cursor != "" {
				url += "&cursor=" + cursor
			}
			resp := env.do(fasthttp.MethodGet, url, auth, nil)
			require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
			var page struct {
				Data []map[string]any `json:"data"`
				Meta map[string]any   `json:"meta"`
			}
			require.NoError(t, sonic.Unmarshal(resp.Response.Body(), &page))
			assert.LessOrEqual(t, len(page.Data), limit)
			for _, observation := range page.Data {
				seen[observation["id"].(string)] = struct{}{}
			}
			next, _ := page.Meta["cursor"].(string)
			if next == "" {
				break
			}
			cursor = next
		}
		assert.Len(t, seen, total, "limit %d reaches every observation", limit)
	}
}

// Scores v3 filters by data type and value, and refuses the combinations Langfuse refuses.
func TestLangfuseScoreTypeAndValueFilters(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	auth := langfuseAuth()
	require.NoError(t, env.store.UpsertTraceScores(ctx, []*logstore.TraceScore{
		{ID: "low", ProjectID: "p1", TraceID: "t", Name: "n", Value: new(0.1), Source: logstore.TraceAuthorSDK},
		{ID: "high", ProjectID: "p1", TraceID: "t", Name: "n", Value: new(0.5), Source: logstore.TraceAuthorSDK},
		{ID: "yes", ProjectID: "p1", TraceID: "t", Name: "n", Value: new(1.0), DataType: "boolean", Source: logstore.TraceAuthorSDK},
		{ID: "cat", ProjectID: "p1", TraceID: "t", Name: "n", StringValue: "formal", Source: logstore.TraceAuthorSDK},
		{ID: "txt", ProjectID: "p1", TraceID: "t", Name: "n", StringValue: "long note", DataType: "text", Source: logstore.TraceAuthorSDK},
	}))
	ids := func(query string) []string {
		resp := env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v3/scores?"+query, auth, nil)
		require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), query+": "+string(resp.Response.Body()))
		var page struct {
			Data []map[string]any `json:"data"`
		}
		require.NoError(t, sonic.Unmarshal(resp.Response.Body(), &page))
		found := make([]string, 0, len(page.Data))
		for _, score := range page.Data {
			found = append(found, score["id"].(string))
		}
		slices.Sort(found)
		return found
	}
	assert.Equal(t, []string{"low"}, ids("dataType=NUMERIC&valueMax=0.2"))
	assert.Equal(t, []string{"high"}, ids("dataType=numeric&valueMin=0.2"))
	assert.Equal(t, []string{"high", "low"}, ids("dataType=NUMERIC"))
	assert.Equal(t, []string{"yes"}, ids("dataType=BOOLEAN&value=true"))
	assert.Equal(t, []string{"cat"}, ids("dataType=CATEGORICAL&value=formal"))
	assert.Equal(t, []string{"cat", "txt"}, ids("dataType=CATEGORICAL,TEXT"))
	assert.Equal(t, []string{"low"}, ids("dataType=NUMERIC&value=0.1"))
	for _, refused := range []string{
		"valueMax=0.2", "dataType=BOOLEAN&valueMin=0", "dataType=NUMERIC,BOOLEAN&valueMin=0",
		"dataType=TEXT&value=x", "dataType=NUMERIC&value=abc", "dataType=BOOLEAN&value=maybe", "dataType=LOUD", "valueMin=x&dataType=NUMERIC",
	} {
		assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodGet, "/tracing/langfuse/api/public/v3/scores?"+refused, auth, nil).Response.StatusCode(), refused)
	}
}
