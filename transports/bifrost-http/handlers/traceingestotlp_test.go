package handlers

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func otlpString(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func otlpInt(key string, value int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}}
}

func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

const (
	otlpTestTrace = "0af7651916cd43dd8448eb211c80319c"
	otlpTestRoot  = "00f067aa0ba902b7"
	otlpTestLLM   = "b7ad6b7169203331"
)

// otlpTestExport is what an OpenInference-instrumented app exports: an agent span, a model call
// under it, and a span no LLM convention explains.
func otlpTestExport(t *testing.T, resourceAttributes ...*commonpb.KeyValue) []byte {
	t.Helper()
	request := &collectortrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: resourceAttributes},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &commonpb.InstrumentationScope{Name: "openinference.instrumentation.openai"},
			Spans: []*tracepb.Span{
				{TraceId: hexBytes(t, otlpTestTrace), SpanId: hexBytes(t, otlpTestRoot), Name: "agent", StartTimeUnixNano: 1_700_000_000_000_000_000, EndTimeUnixNano: 1_700_000_002_000_000_000,
					Attributes: []*commonpb.KeyValue{otlpString("openinference.span.kind", "AGENT"), otlpString("input.value", "hi"), otlpString("output.value", "bye")}},
				{TraceId: hexBytes(t, otlpTestTrace), SpanId: hexBytes(t, otlpTestLLM), ParentSpanId: hexBytes(t, otlpTestRoot), Name: "ChatCompletion", Kind: tracepb.Span_SPAN_KIND_CLIENT,
					StartTimeUnixNano: 1_700_000_000_500_000_000, EndTimeUnixNano: 1_700_000_001_500_000_000,
					Attributes: []*commonpb.KeyValue{otlpString("openinference.span.kind", "LLM"), otlpString("llm.model_name", "gpt-4o"), otlpInt("llm.token_count.prompt", 12), otlpInt("llm.token_count.completion", 5)},
					Status:     &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "rate limited"}},
				{TraceId: hexBytes(t, otlpTestTrace), SpanId: hexBytes(t, "1111111111111111"), ParentSpanId: hexBytes(t, otlpTestRoot), Name: "SELECT users", StartTimeUnixNano: 1_700_000_000_100_000_000, EndTimeUnixNano: 1_700_000_000_200_000_000,
					Attributes: []*commonpb.KeyValue{otlpString("db.system", "postgresql")}},
			},
		}},
	}}}
	body, err := proto.Marshal(request)
	require.NoError(t, err)
	return body
}

// TestOTLPProtobufExportIsStoredAndAcknowledged pins the OTLP wire for Arize, Phoenix and generic
// exporters: a protobuf export answers with an empty protobuf ExportTraceServiceResponse, every
// span is stored, including one no LLM convention explains, and the trace rolls up.
func TestOTLPProtobufExportIsStoredAndAcknowledged(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	headers := map[string]string{"Content-Type": "application/x-protobuf", "Authorization": traceTestKey}
	for _, path := range []string{"/tracing/arize/v1/traces", "/tracing/arize", "/tracing/otel/v1/traces", "/tracing/otel"} {
		resp := env.do(fasthttp.MethodPost, path, headers, otlpTestExport(t))
		require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), "%s: %s", path, resp.Response.Body())
		assert.Equal(t, "application/x-protobuf", string(resp.Response.Header.ContentType()))
		var ack collectortrace.ExportTraceServiceResponse
		require.NoError(t, proto.Unmarshal(resp.Response.Body(), &ack))
		assert.Nil(t, ack.PartialSuccess, "nothing decodable is ever rejected")
	}

	spans, err := env.store.ListTraceSpans(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	require.Len(t, spans, 3, "a repeated export stores each span once")
	byID := map[string]*logstore.TraceSpan{}
	for _, span := range spans {
		byID[span.SpanID] = span
	}
	llm := byID[otlpTestLLM]
	assert.Equal(t, logstore.TraceSpanKindLLM, llm.Kind)
	assert.Equal(t, otlpTestRoot, llm.ParentSpanID)
	assert.Equal(t, 17, llm.TotalTokens)
	assert.Equal(t, logstore.TraceSpanStatusError, llm.Status)
	assert.Equal(t, "rate limited", llm.Error)
	assert.Equal(t, "client", llm.AttributesParsed["otel.span.kind"])
	assert.Equal(t, logstore.TraceSourceArize, llm.Source, "the first route a span arrived on names its source")
	unknown := byID["1111111111111111"]
	assert.Equal(t, logstore.TraceSpanKindUnknown, unknown.Kind, "a span no convention explains is stored, not dropped")
	assert.Equal(t, "postgresql", unknown.AttributesParsed["db.system"])

	require.NoError(t, env.service.FlushRollups(ctx))
	trace, err := env.store.FindTrace(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Equal(t, "agent", trace.Name)
	assert.False(t, trace.InProgress)
	assert.Equal(t, 1, trace.ErrorCount)
}

// TestOTLPJSONExportUsesHexIDs pins OTLP/JSON, which is not plain protobuf JSON: ids are hex,
// 64-bit integers are strings, enums may be names. The answer is JSON.
func TestOTLPJSONExportUsesHexIDs(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"scope":{"name":"ai"},"spans":[
		{"traceId":"` + otlpTestTrace + `","spanId":"` + otlpTestLLM + `","name":"ai.generateText.doGenerate","kind":"SPAN_KIND_CLIENT",
		 "startTimeUnixNano":"1700000000000000000","endTimeUnixNano":1700000001000000000,
		 "attributes":[{"key":"ai.operationId","value":{"stringValue":"ai.generateText.doGenerate"}},{"key":"ai.model.id","value":{"stringValue":"gpt-4o"}},
		   {"key":"ai.usage.promptTokens","value":{"intValue":"9"}},{"key":"ai.usage.completionTokens","value":{"intValue":3}},
		   {"key":"ai.response.msToFirstChunk","value":{"doubleValue":120.5}},{"key":"tags","value":{"arrayValue":{"values":[{"stringValue":"a"}]}}}],
		 "status":{"code":"STATUS_CODE_ERROR","message":"boom"},
		 "events":[{"name":"exception","timeUnixNano":"1700000000500000000","attributes":[{"key":"exception.message","value":{"stringValue":"boom"}}]}]}
	]}]}]}`
	resp := env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + traceTestKey}, []byte(body))
	require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
	assert.Equal(t, "{}", string(resp.Response.Body()))
	assert.Equal(t, "application/json", string(resp.Response.Header.ContentType()))

	// The test key may create projects, so the resource's service.name becomes the project.
	project, err := env.config.GetTraceProjectByName(context.Background(), "api")
	require.NoError(t, err)
	spans, err := env.store.ListTraceSpans(context.Background(), project.ID, otlpTestTrace)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, otlpTestLLM, span.SpanID)
	assert.Equal(t, logstore.TraceSpanKindLLM, span.Kind)
	assert.Equal(t, 12, span.TotalTokens)
	assert.InDelta(t, 1000, *span.DurationMs, 1e-6)
	assert.InDelta(t, 120.5, *span.TimeToFirstTokenMs, 1e-9)
	assert.Equal(t, logstore.TraceSpanStatusError, span.Status)
	assert.Equal(t, logstore.TraceSourceOTel, span.Source)
}

// TestOTLPAuthAndProjectRouting pins who may write where: no key is a 401, a key in any header
// form SDKs use is accepted, a malformed body is a 400, an explicit project header must resolve,
// and a project named by a resource attribute the key cannot use falls back to the key's default.
func TestOTLPAuthAndProjectRouting(t *testing.T) {
	ctx := context.Background()
	env := newTraceIngestTestEnv(t)
	proto := map[string]string{"Content-Type": "application/x-protobuf"}

	assert.Equal(t, fasthttp.StatusUnauthorized, env.do(fasthttp.MethodPost, "/tracing/arize/v1/traces", proto, otlpTestExport(t)).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusUnauthorized, env.do(fasthttp.MethodPost, "/tracing/arize/v1/traces", map[string]string{"Authorization": "bf-tr-wrong"}, otlpTestExport(t)).Response.StatusCode())
	for _, header := range []string{"api_key", "arize-api-key", "x-api-key"} {
		resp := env.do(fasthttp.MethodPost, "/tracing/arize/v1/traces", map[string]string{"Content-Type": "application/x-protobuf", header: traceTestKey}, otlpTestExport(t))
		assert.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), header)
	}
	withKey := map[string]string{"Content-Type": "application/x-protobuf", "Authorization": traceTestKey}
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", withKey, []byte{0xff, 0xff, 0xff}).Response.StatusCode())
	assert.Equal(t, fasthttp.StatusBadRequest, env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", map[string]string{"Content-Type": "application/json", "Authorization": traceTestKey}, []byte("{not json")).Response.StatusCode())

	// The test key may create projects, so a project named by the SDK is made on first use.
	named := env.do(fasthttp.MethodPost, "/tracing/arize/v1/traces", withKey, otlpTestExport(t, otlpString("openinference.project.name", "support-bot"), otlpString("service.name", "api")))
	require.Equal(t, fasthttp.StatusOK, named.Response.StatusCode())
	project, err := env.config.GetTraceProjectByName(ctx, "support-bot")
	require.NoError(t, err)
	spans, err := env.store.ListTraceSpans(ctx, project.ID, otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, spans, 3)

	// A key that may not create projects: a resource attribute is a hint, an explicit header is not.
	fixed := &tables.TableTracingKey{ID: "k-fixed", Name: "fixed", ProjectIDs: []string{"p1"}, DefaultProjectID: "p1", LogContent: true, Enabled: true}
	fixed.SetValue("bf-tr-fixed")
	env.config.keys[fixed.KeyHash] = fixed
	fixedKey := map[string]string{"Content-Type": "application/x-protobuf", "Authorization": "bf-tr-fixed"}
	hinted := env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", fixedKey, otlpTestExport(t, otlpString("service.name", "some-service")))
	assert.Equal(t, fasthttp.StatusOK, hinted.Response.StatusCode(), "an unusable service.name falls back to the key's default project")
	fixedKey["x-project-name"] = "not-mine"
	explicit := env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", fixedKey, otlpTestExport(t))
	assert.Equal(t, fasthttp.StatusForbidden, explicit.Response.StatusCode(), "an explicit project the key cannot use is refused")
}
