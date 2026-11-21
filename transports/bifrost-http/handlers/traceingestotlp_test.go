package handlers

import (
	"context"
	"encoding/hex"
	"errors"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/emptypb"
	"net"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

// TestOTLPAuthAndProjectRouting pins auth and project routing: no credential is a 401, the
// dashboard credentials in any header SDKs use are accepted, a malformed body is a 400, and the
// project comes from the request header, then the resource, then the default project.
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

	// A project the SDK names is made on first use.
	named := env.do(fasthttp.MethodPost, "/tracing/arize/v1/traces", withKey, otlpTestExport(t, otlpString("openinference.project.name", "support-bot"), otlpString("service.name", "api")))
	require.Equal(t, fasthttp.StatusOK, named.Response.StatusCode())
	project, err := env.config.GetTraceProjectByName(ctx, "support-bot")
	require.NoError(t, err)
	spans, err := env.store.ListTraceSpans(ctx, project.ID, otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, spans, 3)

	// An explicit project header wins over every resource attribute.
	explicit := map[string]string{"Content-Type": "application/x-protobuf", "Authorization": traceTestKey, "x-project-name": "from-header"}
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", explicit, otlpTestExport(t, otlpString("service.name", "api"))).Response.StatusCode())
	headerProject, err := env.config.GetTraceProjectByName(ctx, "from-header")
	require.NoError(t, err)
	spans, err = env.store.ListTraceSpans(ctx, headerProject.ID, otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, spans, 3)
	// With no project named anywhere, spans go to the default project.
	require.Equal(t, fasthttp.StatusOK, env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", withKey, otlpTestExport(t)).Response.StatusCode())
	spans, err = env.store.ListTraceSpans(ctx, "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, spans, 3)
}

// A project lookup that fails for an operational reason is retried by the exporter, not routed to
// another project: the export answers 503 and nothing lands in the default project.
func TestOTLPProjectLookupFailureIsRetried(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	env.config.lookupErr = map[string]error{"broken": errors.New("database went away")}
	headers := map[string]string{"Content-Type": "application/x-protobuf", "Authorization": traceTestKey}
	resp := env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", headers, otlpTestExport(t, otlpString("openinference.project.name", "broken")))
	assert.Equal(t, fasthttp.StatusServiceUnavailable, resp.Response.StatusCode(), string(resp.Response.Body()))
	spans, err := env.store.ListTraceSpans(context.Background(), "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Empty(t, spans, "the spans did not fall through to the default project")
}

// The span rate is charged once for a whole export. Charged per project group, a retry would store
// the first group again and be refused on the second, every time.
func TestOTLPRateIsChargedOncePerExport(t *testing.T) {
	env := newTraceIngestTestEnvWith(t, traceingest.Config{MaxSpansPerSecond: 1})
	span := func(id string) string {
		return `{"traceId":"` + otlpTestTrace + `","spanId":"` + id + `","name":"step","startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000"}`
	}
	resource := func(project, id string) string {
		return `{"resource":{"attributes":[{"key":"openinference.project.name","value":{"stringValue":"` + project + `"}}]},"scopeSpans":[{"spans":[` + span(id) + `]}]}`
	}
	body := `{"resourceSpans":[` + resource("alpha", "aaaaaaaaaaaaaaaa") + `,` + resource("beta", "bbbbbbbbbbbbbbbb") + `]}`
	resp := env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", map[string]string{"Content-Type": "application/json", "Authorization": traceTestKey}, []byte(body))
	require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
	for _, name := range []string{"alpha", "beta"} {
		project, err := env.config.GetTraceProjectByName(context.Background(), name)
		require.NoError(t, err, name)
		spans, err := env.store.ListTraceSpans(context.Background(), project.ID, otlpTestTrace)
		require.NoError(t, err)
		assert.Len(t, spans, 1, name)
	}
}

// OTLP/JSON may send 64-bit integers as numbers. They are read exactly: a nanosecond timestamp is
// above 2^53, where a float64 would round it.
func TestOTLPJSONReadsLargeNumbersExactly(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"` + otlpTestTrace + `","spanId":"` + otlpTestLLM + `","name":"step",
		"startTimeUnixNano":1700000000000000001,"endTimeUnixNano":1700000001000000003}]}]}]}`
	resp := env.do(fasthttp.MethodPost, "/tracing/otel/v1/traces", map[string]string{"Content-Type": "application/json", "Authorization": traceTestKey}, []byte(body))
	require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
	spans, err := env.store.ListTraceSpans(context.Background(), "p1", otlpTestTrace)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.Equal(t, int64(1700000000000000001), spans[0].StartTime.UnixNano())
	require.NotNil(t, spans[0].EndTime)
	assert.Equal(t, int64(1700000001000000003), spans[0].EndTime.UnixNano())
}

// Every other Langfuse route stores into the key's default project, so the Langfuse OTLP route does
// too: an x-project-name header there would split a trace's spans from its scores.
func TestLangfuseOTLPIgnoresTheProjectHeader(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	headers := langfuseAuth()
	headers["Content-Type"], headers["x-project-name"] = "application/x-protobuf", "elsewhere"
	resp := env.do(fasthttp.MethodPost, "/tracing/langfuse/api/public/otel/v1/traces", headers, otlpTestExport(t))
	require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
	spans, err := env.store.ListTraceSpans(context.Background(), "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, spans, 3, "the spans land in the key's default project")
	_, err = env.config.GetTraceProjectByName(context.Background(), "elsewhere")
	assert.Error(t, err, "no project is made from the header")
}

// TestOTLPOverGRPC pins the gRPC transport: an export with the key in call metadata is stored
// through the same path as HTTP, and a missing key is Unauthenticated, which exporters do not retry.
func TestOTLPOverGRPC(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := NewTraceIngestGRPCServer(env.handler, logger)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	client := collectortrace.NewTraceServiceClient(conn)
	var request collectortrace.ExportTraceServiceRequest
	require.NoError(t, proto.Unmarshal(otlpTestExport(t), &request))

	_, err = client.Export(context.Background(), &request)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// arize-otel sends the key as api_key metadata, with gzip.
	ctx := metadata.AppendToOutgoingContext(context.Background(), "api_key", traceTestKey, "space_id", "bifrost")
	response, err := client.Export(ctx, &request, grpc.UseCompressor("gzip"))
	require.NoError(t, err)
	assert.Nil(t, response.PartialSuccess)
	spans, err := env.store.ListTraceSpans(context.Background(), "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.Len(t, spans, 3)

	bearer := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+traceTestKey, "x-project-name", "grpc-project")
	_, err = client.Export(bearer, &request)
	require.NoError(t, err, "a project named in metadata is made on first use")
}

// Stop waits for in-flight exports only up to its deadline, then closes them, so a slow export
// cannot hold up shutdown.
func TestTraceIngestGRPCStopForcesAfterDeadline(t *testing.T) {
	started := make(chan struct{})
	grpcServer := grpc.NewServer()
	grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Blocking",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName:    "Stream",
			ServerStreams: true,
			Handler: func(_ any, stream grpc.ServerStream) error {
				close(started)
				<-stream.Context().Done()
				return stream.Context().Err()
			},
		}},
	}, struct{}{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	stream, err := conn.NewStream(t.Context(), &grpc.StreamDesc{ServerStreams: true}, "/test.Blocking/Stream")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&emptypb.Empty{}))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("the export never started")
	}

	server := &TraceIngestGRPCServer{server: grpcServer}
	stopped := make(chan struct{})
	go func() {
		server.stop(10 * time.Millisecond)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not close the in-flight export after its deadline")
	}
}

// OTLP exporters retry RESOURCE_EXHAUSTED only when the status carries RetryInfo, so a rate-limited
// export says when to come back instead of being dropped.
func TestGRPCRateLimitCarriesRetryInfo(t *testing.T) {
	st := status.Convert(grpcIngestError(traceingest.ErrRateLimited))
	assert.Equal(t, codes.ResourceExhausted, st.Code())
	var retry *errdetails.RetryInfo
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.RetryInfo); ok {
			retry = info
		}
	}
	require.NotNil(t, retry, "RetryInfo makes the exporter retry")
	assert.Positive(t, retry.GetRetryDelay().AsDuration())
}

// keyedTraceAuthenticator accepts only requests carrying x-bf-api-key: scoped, as a principal
// limited to p1.
type keyedTraceAuthenticator struct{}

func (keyedTraceAuthenticator) AuthenticateTraceIngest(ctx *fasthttp.RequestCtx) (*traceingest.Principal, error) {
	if string(ctx.Request.Header.Peek("x-bf-api-key")) != "scoped" {
		return nil, traceingest.ErrUnauthorized
	}
	return &traceingest.Principal{ProjectIDs: []string{"p1"}, LogContent: true}, nil
}

// An installed authenticator decides gRPC exports too: with dashboard auth off, an export it
// refuses is refused, instead of acting as the admin.
func TestOTLPOverGRPCUsesTheInstalledAuthenticator(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	env.handler.SetCredentials(&fakeDashboardAuth{active: false})
	env.handler.SetAuthenticator(keyedTraceAuthenticator{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := NewTraceIngestGRPCServer(env.handler, logger)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	client := collectortrace.NewTraceServiceClient(conn)
	var request collectortrace.ExportTraceServiceRequest
	require.NoError(t, proto.Unmarshal(otlpTestExport(t), &request))

	_, err = client.Export(context.Background(), &request)
	assert.Equal(t, codes.Unauthenticated, status.Code(err), "the authenticator refuses a call with no key")
	_, err = client.Export(metadata.AppendToOutgoingContext(context.Background(), "x-bf-api-key", "scoped"), &request)
	require.NoError(t, err)
	spans, err := env.store.ListTraceSpans(context.Background(), "p1", otlpTestTrace)
	require.NoError(t, err)
	assert.NotEmpty(t, spans)
}
