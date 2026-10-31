package otel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/stretchr/testify/require"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// a2aExportTrace passes a stream through the actual gateway gate and framework
// tracer. Raw-policy conversion uses a synthetic capture already tested at the gate.
func a2aExportTrace(t *testing.T) *schemas.Trace {
	t.Helper()
	log := bifrost.NewDefaultLogger(schemas.LogLevelError)
	tracer := tracing.NewTracer(tracing.NewTraceStore(time.Minute, log), nil, log)
	t.Cleanup(tracer.Stop)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	id := tracer.CreateTrace("")
	ctx.SetValue(schemas.BifrostContextKeyTraceID, id)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	_, root := tracer.StartSpan(ctx, "request", schemas.SpanKindHTTPRequest)
	rootSpan := tracer.SpanFromHandle(root)
	ctx.SetValue(schemas.BifrostContextKeySpanID, rootSpan.SpanID)
	request := `{"message":{"role":"user","parts":[{"text":"synthetic input"}]}}`
	_, err := (&agent.Manager{}).RunWithPluginPipeline(ctx, &schemas.BifrostA2ARequest{RequestType: schemas.A2ARequestTypeSendStreamingMessage, AgentName: "fixture", RequestBody: &request}, func(_ *schemas.BifrostA2ARequest, observe func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		body := `{"taskId":"created-task","artifact":{"parts":[{"text":"synthetic output"}]}}`
		observe(&schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeArtifactUpdate, TaskID: "created-task", ContextID: "created-context", Body: &body})
		return &schemas.BifrostA2AResponse{}, nil
	})
	require.Nil(t, err)
	tracer.EndSpan(root, schemas.SpanStatusOk, "")
	trace := tracer.EndTrace(id)
	require.NotNil(t, trace)
	t.Cleanup(func() { tracer.ReleaseTrace(trace) })
	trace.Attributes = map[string]any{schemas.TraceAttrSessionID: "synthetic-session"}
	for _, span := range trace.Spans {
		if span.Kind == schemas.SpanKindA2AOperation {
			span.SetA2ARawPayloads(span.SpanID, &schemas.A2ASpanData{RawRequest: request, RawResponse: `{"text":"synthetic raw output"}`})
		}
	}
	return trace
}

// TestA2AExportContentPolicy pins structured content, raw opt-in, root-only
// suppression, virtual-key policy, exclusions, and event content independently.
func TestA2AExportContentPolicy(t *testing.T) {
	trace := a2aExportTrace(t)
	p := &OtelPlugin{}
	for _, tc := range []struct {
		name                    string
		disabled, raw, root, vk bool
		excluded                []string
	}{
		{name: "structured"}, {name: "raw", raw: true}, {name: "disabled", disabled: true, raw: true},
		{name: "root suppressed", raw: true, root: true}, {name: "virtual key suppressed", raw: true, vk: true},
		{name: "raw request excluded", raw: true, excluded: []string{schemas.AttrBifrostRawRequest}},
		{name: "structured excluded", raw: true, excluded: []string{schemas.AttrInputMessages, schemas.AttrOutputMessages}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace.RootSpan.SetAttribute(schemas.AttrBifrostContentLoggingDisabled, tc.vk)
			filtered := schemas.StripTraceAttributes(trace, tc.excluded)
			rs := p.convertTraceToResourceSpan("fixture", filtered, nil, tc.disabled, tc.raw, false, tc.root)
			stripExcludedFromResourceSpan(rs, tc.excluded)
			for _, span := range rs.ScopeSpans[0].Spans {
				attrs := kvMap(span.Attributes)
				root := span.Name == "request"
				wantContent := !tc.disabled && !tc.vk && !(tc.root && root)
				_, input := attrs[schemas.AttrInputMessages]
				require.Equal(t, wantContent && !slices.Contains(tc.excluded, schemas.AttrInputMessages), input)
				_, output := attrs[schemas.AttrOutputMessages]
				require.Equal(t, wantContent && !slices.Contains(tc.excluded, schemas.AttrOutputMessages), output)
				_, rawRequest := attrs[schemas.AttrBifrostRawRequest]
				_, rawResponse := attrs[schemas.AttrBifrostRawResponse]
				require.Equal(t, tc.raw && wantContent && !root && !slices.Contains(tc.excluded, schemas.AttrBifrostRawRequest), rawRequest)
				require.Equal(t, tc.raw && wantContent && !root, rawResponse)
				if !root {
					require.Equal(t, "created-task", attrs[schemas.AttrA2ATaskID].GetStringValue())
					require.Len(t, span.Events, 1)
					_, eventContent := kvMap(span.Events[0].Attributes)[schemas.AttrOutputMessages]
					require.Equal(t, wantContent && !slices.Contains(tc.excluded, schemas.AttrOutputMessages), eventContent)
				}
			}
		})
	}
}

// a2aGRPCCollector captures real protobuf export requests from the gRPC client.
type a2aGRPCCollector struct {
	collectorpb.UnimplementedTraceServiceServer
	requests chan *collectorpb.ExportTraceServiceRequest
}

// Export records the wire request before acknowledging collection.
func (c *a2aGRPCCollector) Export(_ context.Context, req *collectorpb.ExportTraceServiceRequest) (*collectorpb.ExportTraceServiceResponse, error) {
	c.requests <- req
	return &collectorpb.ExportTraceServiceResponse{}, nil
}

// TestA2AExportWireProfiles checks actual HTTP and gRPC protobufs after Inject
// fans out to profiles with different content policies and exclusions.
func TestA2AExportWireProfiles(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	httpRequests := make(chan *collectorpb.ExportTraceServiceRequest, 8)
	httpCollector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		req := &collectorpb.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(data, req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		httpRequests <- req
		w.WriteHeader(http.StatusOK)
	}))
	defer httpCollector.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcCollector := &a2aGRPCCollector{requests: make(chan *collectorpb.ExportTraceServiceRequest, 8)}
	server := grpc.NewServer()
	collectorpb.RegisterTraceServiceServer(server, grpcCollector)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	httpClient, err := NewOtelClientHTTP(httpCollector.URL, nil, "", true, time.Second)
	require.NoError(t, err)
	defer httpClient.Close()
	grpcClient, err := NewOtelClientGRPC(listener.Addr().String(), nil, "", true)
	require.NoError(t, err)
	defer grpcClient.Close()
	p := &OtelPlugin{targets: []*otelTarget{
		{client: httpClient, serviceName: "structured", exportTimeout: time.Second},
		{client: httpClient, serviceName: "raw", exportRawPayloads: true, exportTimeout: time.Second},
		{client: grpcClient, serviceName: "disabled", disableContentLogging: true, exportRawPayloads: true, exportTimeout: time.Second},
		{client: grpcClient, serviceName: "root suppressed", disableRootSpanContent: true, exportRawPayloads: true, exportTimeout: time.Second, excludedAttributes: []string{schemas.AttrBifrostRawRequest}},
	}}
	trace := a2aExportTrace(t)
	for _, vkDisabled := range []bool{false, true} {
		trace.RootSpan.SetAttribute(schemas.AttrBifrostContentLoggingDisabled, vkDisabled)
		// A denylist cannot remove the virtual-key policy before it is enforced.
		p.targets[0].excludedAttributes = []string{schemas.AttrBifrostContentLoggingDisabled}
		require.NoError(t, p.Inject(context.Background(), trace))
		for i := 0; i < 4; i++ {
			var request *collectorpb.ExportTraceServiceRequest
			select {
			case request = <-httpRequests:
			case request = <-grpcCollector.requests:
			case <-time.After(3 * time.Second):
				t.Fatal("missing protobuf export")
			}
			require.Len(t, request.ResourceSpans, 1)
			rs := request.ResourceSpans[0]
			service := kvMap(rs.Resource.Attributes)["service.name"].GetStringValue()
			for _, span := range rs.ScopeSpans[0].Spans {
				attrs := kvMap(span.Attributes)
				root := span.Name == "request"
				content := !vkDisabled && service != "disabled" && !(service == "root suppressed" && root)
				_, hasContent := attrs[schemas.AttrOutputMessages]
				require.Equal(t, content, hasContent, service)
				_, raw := attrs[schemas.AttrBifrostRawResponse]
				require.Equal(t, content && !root && service != "structured", raw, service)
				_, rawRequest := attrs[schemas.AttrBifrostRawRequest]
				require.Equal(t, content && !root && service == "raw", rawRequest, service)
			}
		}
	}
}
