package otel

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestOtelClientHTTPUsesGlobalProxy pins that the OTLP/HTTP trace exporter honours the
// global proxy when it is enabled for API traffic. It used to follow only the
// environment proxy.
func TestOtelClientHTTPUsesGlobalProxy(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	set := proxytest.NewSet(t)
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(nil) })

	client, err := NewOtelClientHTTP("http://203.0.113.10:4318", nil, "", true, 3*time.Second)
	if err != nil {
		t.Fatalf("NewOtelClientHTTP: %v", err)
	}
	defer client.Close()
	_ = client.Emit(context.Background(), nil)
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "config"}, "203.0.113.10:4318", nil)
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
		name            string
		status          int
		contentEncoding string
		body            []byte
		gzip            bool
		explicitGzip    bool
		wantRejected    int64
		wantAckWarning  string
		wantError       string
	}{
		{name: "empty success"},
		{name: "partial acknowledgement", body: payload, wantRejected: 2},
		{name: "gzip acknowledgement", body: payload, gzip: true, wantRejected: 2},
		{name: "explicit gzip negotiation", body: payload, gzip: true, explicitGzip: true, wantRejected: 2},
		{name: "json empty object 200 succeeds", body: []byte("{}"), wantAckWarning: "non-protobuf"},
		{name: "json partialSuccess object 200 succeeds", body: []byte(`{"partialSuccess":{}}`), wantAckWarning: "non-protobuf"},
		{name: "plain OK 200 succeeds", body: []byte("OK"), wantAckWarning: "non-protobuf"},
		{name: "html 200 succeeds", body: []byte("<html><body>ok</body></html>"), wantAckWarning: "non-protobuf"},
		{name: "unsupported encoding 200 succeeds", contentEncoding: "br", body: []byte("compressed"), wantAckWarning: "unsupported OTLP trace acknowledgement encoding"},
		{name: "oversized unsupported encoding is not reflected", contentEncoding: "br-" + strings.Repeat("s", 8192), body: []byte("compressed"), wantAckWarning: "unsupported OTLP trace acknowledgement encoding"},
		{name: "over 64KiB 200 succeeds without failing export", body: make([]byte, maxTraceResponseBytes+1), gzip: true, wantAckWarning: "exceeds"},
		{name: "non-2xx fails export", status: http.StatusBadGateway, body: []byte("bad gateway"), wantError: "502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-protobuf")
				if tc.contentEncoding != "" {
					w.Header().Set("Content-Encoding", tc.contentEncoding)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
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
				if tc.wantAckWarning != "" {
					require.Contains(t, result.ackWarning, tc.wantAckWarning)
				require.LessOrEqual(t, len(result.ackWarning), maxCollectorDiagnosticBytes)
				} else {
					require.Empty(t, result.ackWarning)
				}
			}
		})
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
			w.(http.Flusher).Flush()
			_, _ = w.Write(encoded)
		}))
		t.Cleanup(receiver.Close)
		client, err := NewOtelClientHTTP(receiver.URL, headers, "", false, 5*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		result, err := client.emit(t.Context(), nil)
		require.NoError(t, err, "oversized compressed 2xx ack must not fail the export")
		require.Contains(t, result.ackWarning, "compressed OTLP trace acknowledgement exceeds")
	}
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
