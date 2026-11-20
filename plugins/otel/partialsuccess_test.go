package otel

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type partialSuccessLogger struct {
	testLogger
	warnings []string
}

func (l *partialSuccessLogger) Warn(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

type partialSuccessCollector struct {
	collectorpb.UnimplementedTraceServiceServer
	calls    atomic.Int64
	response *collectorpb.ExportTraceServiceResponse
}

func (c *partialSuccessCollector) Export(context.Context, *collectorpb.ExportTraceServiceRequest) (*collectorpb.ExportTraceServiceResponse, error) {
	c.calls.Add(1)
	return c.response, nil
}

func TestInjectObservesPartialSuccess(t *testing.T) {
	const diagnostic = "collector span limit"
	response := &collectorpb.ExportTraceServiceResponse{
		PartialSuccess: &collectorpb.ExportTracePartialSuccess{
			RejectedSpans: 1,
			ErrorMessage:  diagnostic,
		},
	}
	for _, protocol := range []Protocol{ProtocolHTTP, ProtocolGRPC} {
		t.Run(string(protocol), func(t *testing.T) {
			log := &partialSuccessLogger{}
			oldLogger := logger
			logger = log
			t.Cleanup(func() { logger = oldLogger })
			var client traceExporter
			var calls *atomic.Int64
			switch protocol {
			case ProtocolHTTP:
				payload, err := proto.Marshal(response)
				require.NoError(t, err)
				httpCalls := &atomic.Int64{}
				calls = httpCalls
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					httpCalls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/x-protobuf")
					_, _ = w.Write(payload)
				}))
				t.Cleanup(server.Close)
				client, err = NewOtelClientHTTP(server.URL, nil, "", false, time.Second)
				require.NoError(t, err)
			case ProtocolGRPC:
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				receiver := &partialSuccessCollector{response: response}
				calls = &receiver.calls
				server := grpc.NewServer()
				collectorpb.RegisterTraceServiceServer(server, receiver)
				go func() { _ = server.Serve(listener) }()
				t.Cleanup(server.Stop)
				client, err = NewOtelClientGRPC(listener.Addr().String(), nil, "", true)
				require.NoError(t, err)
			}
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			target := testTarget(t, client, time.Second)
			target.profileIndex = 3
			target.consecutiveFailures.Store(2)
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
			target.metricsExporter = &MetricsExporter{meter: provider.Meter("test")}
			target.metricsExporter.initMetrics()

			plugin := &OtelPlugin{targets: []*otelTarget{target}}
			trace := &schemas.Trace{
				TraceID: "0af7651916cd43dd8448eb211c80319c",
				Spans: []*schemas.Span{
					{SpanID: "b7ad6b7169203331", Name: "first"},
					{SpanID: "b7ad6b7169203332", Name: "second"},
				},
			}
			for range breakerFailureThreshold + 1 {
				require.NoError(t, plugin.Inject(t.Context(), trace))
			}
			require.EqualValues(t, breakerFailureThreshold+1, calls.Load())
			require.Zero(t, plugin.ExportStats()[target.url].Failed)
			require.Zero(t, plugin.ExportStats()[target.url].Suppressed)
			require.Zero(t, target.consecutiveFailures.Load())
			require.EqualValues(t, breakerFailureThreshold+1, plugin.ExportRejectionStats()[target.profileIndex].RejectedSpans)
			require.EqualValues(t, breakerFailureThreshold+1, plugin.ExportRejectionStats()[target.profileIndex].PartialExports)
			require.Len(t, log.warnings, 1)
			require.Contains(t, log.warnings[0], diagnostic)
			var collected metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(t.Context(), &collected))
			counters := map[string]int64{}
			for _, scope := range collected.ScopeMetrics {
				for _, instrument := range scope.Metrics {
					if sum, ok := instrument.Data.(metricdata.Sum[int64]); ok {
						for _, point := range sum.DataPoints {
							if index, ok := point.Attributes.Value(attribute.Key("bifrost.otel.profile_index")); ok {
								require.EqualValues(t, 3, index.AsInt64())
								counters[instrument.Name] = point.Value
							}
						}
					}
				}
			}
			require.Equal(t, map[string]int64{
				"bifrost_otel_trace_rejected_spans_total":  breakerFailureThreshold + 1,
				"bifrost_otel_trace_partial_exports_total": breakerFailureThreshold + 1,
			}, counters)
		})
	}
}

func TestTraceAcknowledgementHTTP(t *testing.T) {
	oldLogger := logger
	logger = testLogger{}
	t.Cleanup(func() { logger = oldLogger })
	payload, err := proto.Marshal(&collectorpb.ExportTraceServiceResponse{
		PartialSuccess: &collectorpb.ExportTracePartialSuccess{RejectedSpans: 2},
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name         string
		body         []byte
		gzip         bool
		explicitGzip bool
		wantRejected int64
		wantError    string
	}{
		{name: "empty success"},
		{name: "partial acknowledgement", body: payload, wantRejected: 2},
		{name: "gzip acknowledgement", body: payload, gzip: true, wantRejected: 2},
		{name: "explicit gzip negotiation", body: payload, gzip: true, explicitGzip: true, wantRejected: 2},
		{name: "malformed payload", body: []byte{0x0a}, wantError: "invalid OTLP trace acknowledgement"},
		{name: "over receive limit", body: make([]byte, maxTraceResponseBytes+1), gzip: true, wantError: "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-protobuf")
				if tc.gzip {
					w.Header().Set("Content-Encoding", "gzip")
					writer := gzip.NewWriter(w)
					_, _ = writer.Write(tc.body)
					_ = writer.Close()
					return
				}
				_, _ = w.Write(tc.body)
			}))
			t.Cleanup(receiver.Close)
			var headers map[string]string
			if tc.explicitGzip {
				headers = map[string]string{"Accept-Encoding": "gzip"}
			}
			client, err := NewOtelClientHTTP(receiver.URL, headers, "", false, time.Second)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			result, err := client.emit(t.Context(), nil)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantRejected, result.partialSuccess.GetRejectedSpans())
			}
		})
	}
}

func TestWarningOnlyAcknowledgement(t *testing.T) {
	log := &partialSuccessLogger{}
	oldLogger := logger
	logger = log
	t.Cleanup(func() { logger = oldLogger })
	target := &otelTarget{}
	message := "collector warning for example-value " + strings.Repeat("界", 100)
	result := traceExportResult{
		partialSuccess: &collectorpb.ExportTracePartialSuccess{ErrorMessage: message},
		headers:        map[string]string{"Authorization": "Bearer example-value"},
	}
	for range exportLogThrottle + 1 {
		target.recordPartialSuccess(t.Context(), result)
	}
	require.Zero(t, target.partialExports.Load())
	require.Zero(t, target.rejectedSpans.Load())
	require.Len(t, log.warnings, 2)
	require.Contains(t, log.warnings[0], "collector warning")
	require.NotContains(t, log.warnings[0], "example-value")
	require.True(t, utf8.ValidString(result.diagnostic()))
	require.LessOrEqual(t, len(result.diagnostic()), maxCollectorDiagnosticBytes+3)
	require.True(t, strings.HasSuffix(result.diagnostic(), "..."))
}

type traceAckTransport struct {
	payload string
}

func (t traceAckTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(t.payload)),
	}, nil
}

func BenchmarkHTTPTraceAcknowledgement(b *testing.B) {
	oldLogger := logger
	logger = testLogger{}
	b.Cleanup(func() { logger = oldLogger })
	client := &OtelClientHTTP{
		client:   &http.Client{Transport: traceAckTransport{}},
		endpoint: "http://collector.test/v1/traces",
	}
	ctx := context.Background()
	for b.Loop() {
		if err := client.Emit(ctx, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDiagnosticRedactsOverlappingValues(t *testing.T) {
	for _, tenant := range []string{"acme", "token acme"} {
		result := traceExportResult{
			partialSuccess: &collectorpb.ExportTracePartialSuccess{ErrorMessage: "invalid token acme-live-example"},
			headers:        map[string]string{"X-Tenant": tenant, "Authorization": "Bearer acme-live-example"},
		}
		for range 100 {
			diagnostic := result.diagnostic()
			require.NotContains(t, diagnostic, "live-example")
			require.NotContains(t, diagnostic, "acme")
			require.Contains(t, diagnostic, "[redacted]")
		}
	}
}

func TestTraceAcknowledgementHTTPBoundsCompressedInput(t *testing.T) {
	oldLogger := logger
	logger = testLogger{}
	t.Cleanup(func() { logger = oldLogger })
	var member bytes.Buffer
	writer := gzip.NewWriter(&member)
	require.NoError(t, writer.Close())
	encoded := bytes.Repeat(member.Bytes(), maxTraceResponseBytes/member.Len()+1)
	for _, headers := range []map[string]string{nil, {"Accept-Encoding": "gzip"}} {
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Encoding", "gzip")
			w.(http.Flusher).Flush() // Unknown content length exercises the streaming bound.
			_, _ = w.Write(encoded)
		}))
		t.Cleanup(receiver.Close)
		client, err := NewOtelClientHTTP(receiver.URL, headers, "", false, 5*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		_, err = client.emit(t.Context(), nil)
		require.Error(t, err, "compressed input must be bounded even when its output is empty")
	}
}
