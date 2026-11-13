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

func braintrustAuth() map[string]string {
	return map[string]string{"Authorization": "Bearer " + traceTestKey}
}

func decodeJSON(t *testing.T, ctx *fasthttp.RequestCtx) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &body), string(ctx.Response.Body()))
	return body
}

// TestBraintrustSDKFlow walks the calls a Braintrust SDK makes with BRAINTRUST_APP_URL pointed at
// Bifrost: login, project register, version, a log flush whose merge row arrives in its own
// request, then feedback and a comment addressed by row id.
func TestBraintrustSDKFlow(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	auth := braintrustAuth()

	assert.Equal(t, fasthttp.StatusUnauthorized, env.do(fasthttp.MethodPost, "/tracing/braintrust/api/apikey/login", nil, nil).Response.StatusCode())
	login := env.do(fasthttp.MethodPost, "/tracing/braintrust/api/apikey/login", auth, nil)
	require.Equal(t, fasthttp.StatusOK, login.Response.StatusCode(), string(login.Response.Body()))
	org := decodeJSON(t, login)["org_info"].([]any)[0].(map[string]any)
	assert.Equal(t, "http://bifrost.test/tracing/braintrust", org["api_url"], "every later SDK call comes back to this adapter")

	register := env.do(fasthttp.MethodPost, "/tracing/braintrust/api/project/register", auth, []byte(`{"project_name":"My first project","org_id":"bifrost"}`))
	require.Equal(t, fasthttp.StatusOK, register.Response.StatusCode(), string(register.Response.Body()))
	projectID := decodeJSON(t, register)["project"].(map[string]any)["id"].(string)
	require.NotEmpty(t, projectID)
	again := env.do(fasthttp.MethodPost, "/tracing/braintrust/v1/project", auth, []byte(`{"name":"My first project"}`))
	assert.Equal(t, projectID, decodeJSON(t, again)["id"], "registering the same name returns the same project")
	assert.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodGet, "/tracing/braintrust/v1/project/"+projectID, auth, nil).Response.StatusCode())

	version := decodeJSON(t, env.do(fasthttp.MethodGet, "/tracing/braintrust/version", nil, nil))
	assert.EqualValues(t, braintrustMaxPayloadBytes, version["logs3_payload_max_bytes"])

	// The SDK flushes the span start and its end as separate rows, and here the end lands first.
	end := `{"rows":[{"id":"row-llm","span_id":"span-llm","root_span_id":"span-root","project_id":"` + projectID + `","log_id":"g","_is_merge":true,
		"output":[{"message":{"role":"assistant","content":"Paris"}}],"metrics":{"end":1700000001.5,"prompt_tokens":12,"completion_tokens":5,"tokens":17}}],"api_version":2}`
	flushed := env.do(fasthttp.MethodPost, "/tracing/braintrust/logs3", auth, []byte(end))
	require.Equal(t, fasthttp.StatusOK, flushed.Response.StatusCode(), string(flushed.Response.Body()))
	assert.Equal(t, []any{"row-llm"}, decodeJSON(t, flushed)["row_ids"])
	start := `{"rows":[
		{"id":"row-root","span_id":"span-root","root_span_id":"span-root","project_id":"` + projectID + `","log_id":"g","span_attributes":{"name":"Bifrost agent","type":"task"},"input":"capital of France?","output":"Paris","metrics":{"start":1700000000,"end":1700000002},"tags":["demo"]},
		{"id":"row-llm","span_id":"span-llm","root_span_id":"span-root","span_parents":["span-root"],"project_id":"` + projectID + `","log_id":"g","span_attributes":{"name":"Chat Completion","type":"llm"},
		 "input":[{"role":"user","content":"capital of France?"}],"metadata":{"model":"gpt-4o"},"metrics":{"start":1700000000.5}}
	],"api_version":2}`
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPost, "/tracing/braintrust/logs3", auth, []byte(start)).Response.StatusCode())

	spans, err := env.store.ListTraceSpans(ctx, projectID, "span-root")
	require.NoError(t, err)
	require.Len(t, spans, 2)
	byID := map[string]*logstore.TraceSpan{}
	for _, span := range spans {
		byID[span.SpanID] = span
	}
	llm := byID["span-llm"]
	assert.False(t, llm.Partial, "the base row completed the span whose merge row came first")
	assert.Equal(t, "Chat Completion", llm.Name)
	assert.Equal(t, logstore.TraceSpanKindLLM, llm.Kind)
	assert.Equal(t, 17, llm.TotalTokens)
	assert.InDelta(t, 1000, *llm.DurationMs, 1e-6)
	assert.Equal(t, "gpt-4o", llm.Model)

	feedback := `{"rows":[
		{"id":"row-llm","project_id":"` + projectID + `","log_id":"g","_is_merge":true,"scores":{"correctness":1},"expected":"Paris"},
		{"id":"comment-1","project_id":"` + projectID + `","log_id":"g","origin":{"id":"row-llm"},"comment":{"text":"good answer"}},
		{"id":"row-unknown","project_id":"` + projectID + `","_is_merge":true,"scores":{"x":1}}
	],"api_version":2}`
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPost, "/tracing/braintrust/logs3", auth, []byte(feedback)).Response.StatusCode())
	require.NoError(t, env.service.FlushRollups(ctx))
	trace, err := env.store.FindTrace(ctx, projectID, "span-root")
	require.NoError(t, err)
	assert.Equal(t, "Bifrost agent", trace.Name)
	assert.Equal(t, map[string]float64{"correctness": 1}, trace.ScoresParsed, "feedback addressed by row id reached its span")
	assert.Equal(t, []string{"demo"}, trace.TagsParsed)
	comments, err := env.store.ListTraceComments(ctx, projectID, "span-root")
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "span-llm", comments[0].SpanID)
	llmAfter, err := env.store.FindTraceSpansByRowIDs(ctx, projectID, []string{"row-llm"})
	require.NoError(t, err)
	assert.Equal(t, "Paris", llmAfter[0].Expected)

	// span.link() and the project URL land on the Bifrost dashboard.
	link := env.do(fasthttp.MethodGet, "/tracing/braintrust/app/bifrost/object?object_type=project_logs&object_id="+projectID+"&id=row-llm", nil, nil)
	assert.Equal(t, fasthttp.StatusFound, link.Response.StatusCode())
	assert.Contains(t, string(link.Response.Header.Peek("Location")), "trace=span-root&span=span-llm")
	projectLink := env.do(fasthttp.MethodGet, "/tracing/braintrust/app/bifrost/p/My%20first%20project/logs", nil, nil)
	assert.Equal(t, fasthttp.StatusFound, projectLink.Response.StatusCode())
	assert.Contains(t, string(projectLink.Response.Header.Peek("Location")), "project=My+first+project")

	btql := env.do(fasthttp.MethodPost, "/tracing/braintrust/btql", auth, []byte(`{"query":"select: *"}`))
	assert.Equal(t, fasthttp.StatusNotImplemented, btql.Response.StatusCode())
	assert.Contains(t, string(btql.Response.Body()), "BTQL is not supported")
}

// TestBraintrustExperimentOverflowAndAttachments pins the rest: an experiment's rows land in its
// project, a payload too large to post inline goes through the overflow upload, an attachment is
// uploaded to the URL the SDK is handed, and the eval summary calls end cleanly.
func TestBraintrustExperimentOverflowAndAttachments(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	auth := braintrustAuth()

	registered := decodeJSON(t, env.do(fasthttp.MethodPost, "/tracing/braintrust/api/experiment/register", auth, []byte(`{"project_name":"agent","experiment_name":"baseline"}`)))
	experimentID := registered["experiment"].(map[string]any)["id"].(string)
	assert.Equal(t, "p1", registered["project"].(map[string]any)["id"])

	overflow := decodeJSON(t, env.do(fasthttp.MethodPost, "/tracing/braintrust/logs3/overflow", auth, []byte(`{"content_type":"application/json","size_bytes":9000000}`)))
	key, signedURL := overflow["key"].(string), overflow["signedUrl"].(string)
	assert.Equal(t, "http://bifrost.test/tracing/uploads/"+key, signedURL)
	payload := `{"rows":[{"id":"row-e","span_id":"case-1","root_span_id":"case-1","experiment_id":"` + experimentID + `","span_attributes":{"name":"eval","type":"eval"},"input":"2+2","output":"4","expected":"4","scores":{"exact":1},"metrics":{"start":1700000000,"end":1700000001}}],"api_version":2}`
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPut, "/tracing/uploads/"+key, nil, []byte(payload)).Response.StatusCode())
	reference := env.do(fasthttp.MethodPost, "/tracing/braintrust/logs3", auth, []byte(`{"rows":{"type":"logs3_overflow","key":"`+key+`"},"api_version":2}`))
	require.Equal(t, fasthttp.StatusOK, reference.Response.StatusCode(), string(reference.Response.Body()))
	spans, err := env.store.ListTraceSpans(ctx, "p1", "case-1")
	require.NoError(t, err)
	require.Len(t, spans, 1, "an experiment's rows are spans of its project")
	assert.Equal(t, experimentID, spans[0].ExperimentID)
	assert.Equal(t, logstore.TraceSpanKindEval, spans[0].Kind)
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodPost, "/tracing/braintrust/logs3", auth, []byte(`{"rows":{"type":"logs3_overflow","key":"nope"}}`)).Response.StatusCode())

	attachment := decodeJSON(t, env.do(fasthttp.MethodPost, "/tracing/braintrust/attachment", auth, []byte(`{"key":"7c9e6679-7425-40de-944b-e07fc1f90ae7","filename":"shot.png","content_type":"image/png","org_id":"bifrost"}`)))
	uploadURL := attachment["signedUrl"].(string)
	uploadID := uploadURL[len("http://bifrost.test/tracing/uploads/"):]
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPut, "/tracing/uploads/"+uploadID, map[string]string{"Content-Type": "image/png"}, []byte("png")).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPost, "/tracing/braintrust/attachment/status", auth, []byte(`{"key":"7c9e6679-7425-40de-944b-e07fc1f90ae7","status":{"upload_status":"done"}}`)).Response.StatusCode())
	stored, data, err := env.store.ReadTraceUploadBytes(ctx, uploadID)
	require.NoError(t, err)
	assert.Equal(t, []byte("png"), data)
	assert.Equal(t, "shot.png", stored.Filename)

	summary := env.do(fasthttp.MethodGet, "/tracing/braintrust/v1/experiment/"+experimentID+"/summarize?summarize_scores=true", auth, nil)
	require.Equal(t, fasthttp.StatusOK, summary.Response.StatusCode())
	summarized := decodeJSON(t, summary)
	assert.Equal(t, "baseline", summarized["experiment_name"], "the summary names the experiment as it was registered")
	exact := summarized["scores"].(map[string]any)["exact"].(map[string]any)
	assert.Equal(t, 1.0, exact["score"], "the summary is the mean of the experiment's own scores")
	assert.Nil(t, exact["diff"], "there is no base experiment to compare against")
	assert.InDelta(t, 1.0, summarized["metrics"].(map[string]any)["duration"].(map[string]any)["metric"], 1e-9, "duration is reported in seconds")
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodPost, "/tracing/braintrust/api/base_experiment/get_id", auth, []byte(`{"id":"`+experimentID+`"}`)).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodGet, "/tracing/braintrust/experiment-comparison2?experiment_id="+experimentID, auth, nil).Response.StatusCode())

	// OTLP through Braintrust's endpoint: x-bt-parent names the project.
	headers := map[string]string{"Authorization": auth["Authorization"], "Content-Type": "application/x-protobuf", "x-bt-parent": "project_name:agent"}
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPost, "/tracing/braintrust/otel/v1/traces", headers, otlpTestExport(t)).Response.StatusCode())
	otlpSpans, err := env.store.ListTraceSpans(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, otlpSpans, 3)
	exported := base64.StdEncoding.EncodeToString(append([]byte{4, 2, 0}, `{"object_id":"p1"}`...))
	headers["x-bt-parent"] = exported
	assert.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPost, "/tracing/braintrust/otel/v1/traces", headers, otlpTestExport(t)).Response.StatusCode(), "an exported span names its project too")
}
