package handlers

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

	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// attrMap flattens string attributes for assertions.
func attrMap(attrs []*commonpb.KeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range attrs {
		out[kv.Key] = kv.GetValue().GetStringValue()
	}
	return out
}

// spoofedResource is what a client might send: its own service name, plus
// identity attributes and a prompt it should not be able to keep.
func spoofedResource() *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		telemetryStringAttr("service.name", "claude-code"),
		telemetryStringAttr(telemetryAttrVirtualKeyID, "vk-someone-else"),
		telemetryStringAttr(telemetryAttrEndUserID, "someone-else"),
	}}
}

func sampleMetricsExport() *colmetricspb.ExportMetricsServiceRequest {
	return &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: spoofedResource(),
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: "claude_code.token.usage",
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{{
				Attributes: []*commonpb.KeyValue{telemetryStringAttr("model", "claude-sonnet-5-5"), telemetryStringAttr("prompt", "secret prompt"), telemetryStringAttr("bifrost.team.id", "spoofed")},
				Value:      &metricspb.NumberDataPoint_AsInt{AsInt: 42},
			}}}},
		}}}},
	}}}
}

var testTelemetryIdentity = &clientGatewayIdentity{Mode: "vk", Subject: "vk-1", VKID: "vk-1", VKName: "platform-team", TeamID: "team-7"}

// TestStampTelemetryExport pins the stamping every forwarded export goes
// through, for each signal and encoding: the Bifrost identity is stamped on the
// resource, any identity attribute the client sent is removed at every level,
// the configured attributes are dropped, and everything else is kept.
func TestStampTelemetryExport(t *testing.T) {
	wantResource := map[string]string{
		"service.name":             "claude-code",
		telemetryAttrGatewayClient: "claude-code",
		telemetryAttrAuthMode:      "vk",
		telemetryAttrVirtualKeyID:  "vk-1",
		telemetryAttrVirtualKey:    "platform-team",
		telemetryAttrTeamID:        "team-7",
	}

	t.Run("metrics protobuf", func(t *testing.T) {
		body, err := proto.Marshal(sampleMetricsExport())
		require.NoError(t, err)
		out, err := stampTelemetryExport("metrics", body, false, "", "claude-code", testTelemetryIdentity, []string{"prompt"})
		require.NoError(t, err)
		var got colmetricspb.ExportMetricsServiceRequest
		require.NoError(t, proto.Unmarshal(out, &got))
		assert.Equal(t, wantResource, attrMap(got.ResourceMetrics[0].Resource.Attributes))
		dp := got.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetSum().DataPoints[0]
		assert.Equal(t, map[string]string{"model": "claude-sonnet-5-5"}, attrMap(dp.Attributes), "prompt and the spoofed team are gone, model stays")
		assert.EqualValues(t, 42, dp.GetAsInt())
	})

	t.Run("metrics gzip", func(t *testing.T) {
		body, err := proto.Marshal(sampleMetricsExport())
		require.NoError(t, err)
		var zipped bytes.Buffer
		zw := gzip.NewWriter(&zipped)
		_, _ = zw.Write(body)
		require.NoError(t, zw.Close())
		out, err := stampTelemetryExport("metrics", zipped.Bytes(), false, "gzip", "claude-code", testTelemetryIdentity, nil)
		require.NoError(t, err)
		var got colmetricspb.ExportMetricsServiceRequest
		require.NoError(t, proto.Unmarshal(out, &got))
		assert.Equal(t, wantResource, attrMap(got.ResourceMetrics[0].Resource.Attributes))
	})

	t.Run("logs json", func(t *testing.T) {
		export := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource: spoofedResource(),
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
				Attributes: []*commonpb.KeyValue{telemetryStringAttr("event.name", "user_prompt"), telemetryStringAttr("prompt", "secret prompt")},
			}}}},
		}}}
		body, err := protojson.Marshal(export)
		require.NoError(t, err)
		out, err := stampTelemetryExport("logs", body, true, "", "claude-code", testTelemetryIdentity, []string{"prompt"})
		require.NoError(t, err)
		var got collogspb.ExportLogsServiceRequest
		require.NoError(t, proto.Unmarshal(out, &got))
		assert.Equal(t, wantResource, attrMap(got.ResourceLogs[0].Resource.Attributes))
		assert.Equal(t, map[string]string{"event.name": "user_prompt"}, attrMap(got.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes))
	})

	t.Run("traces json with hex ids", func(t *testing.T) {
		// OTLP/JSON writes trace and span IDs as hex, not protojson's base64.
		body := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}}]},
			"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174","name":"tool","attributes":[{"key":"enduser.id","value":{"stringValue":"spoofed"}}]}]}]}]}`)
		out, err := stampTelemetryExport("traces", body, true, "", "claude-code", &clientGatewayIdentity{Mode: "user", Subject: "user-9"}, nil)
		require.NoError(t, err)
		var got coltracepb.ExportTraceServiceRequest
		require.NoError(t, proto.Unmarshal(out, &got))
		span := got.ResourceSpans[0].ScopeSpans[0].Spans[0]
		assert.Equal(t, "5b8efff798038103d269b633813fc60c", strings.ToLower(hexString(span.TraceId)))
		assert.Equal(t, "eee19b7ec3c1b174", strings.ToLower(hexString(span.SpanId)))
		assert.Empty(t, attrMap(span.Attributes), "a client-sent enduser.id is removed")
		res := attrMap(got.ResourceSpans[0].Resource.Attributes)
		assert.Equal(t, "user-9", res[telemetryAttrEndUserID])
		assert.Equal(t, "user", res[telemetryAttrAuthMode])
	})

	t.Run("rejections", func(t *testing.T) {
		_, err := stampTelemetryExport("metrics", []byte("not protobuf"), false, "", "claude-code", testTelemetryIdentity, nil)
		assert.Error(t, err)
		_, err = stampTelemetryExport("metrics", []byte("x"), false, "br", "claude-code", testTelemetryIdentity, nil)
		assert.ErrorContains(t, err, "unsupported content encoding")

		var bomb bytes.Buffer
		zw := gzip.NewWriter(&bomb)
		_, _ = zw.Write(make([]byte, clientGatewayTelemetryMaxDecoded+1))
		require.NoError(t, zw.Close())
		_, err = stampTelemetryExport("metrics", bomb.Bytes(), false, "gzip", "claude-code", testTelemetryIdentity, nil)
		assert.ErrorIs(t, err, errTelemetryTooLarge)
	})
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// telemetryCollector is a fake OTLP/HTTP collector recording what it receives.
type telemetryCollector struct {
	server   *httptest.Server
	received chan *http.Request
	bodies   chan []byte
}

func newTelemetryCollector(t *testing.T, status int) *telemetryCollector {
	c := &telemetryCollector{received: make(chan *http.Request, 16), bodies: make(chan []byte, 16)}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.received <- r
		c.bodies <- body
		w.WriteHeader(status)
	}))
	t.Cleanup(c.server.Close)
	return c
}

// enableTelemetry switches telemetry on for the Claude Code gateway, pointed at
// collector with an env-backed authorization header.
func enableTelemetry(t *testing.T, h *clientGatewayEndpoint, collector *telemetryCollector, drop ...string) {
	t.Setenv("BIFROST_TEST_COLLECTOR_TOKEN", "Bearer collector-token")
	h.store.ClientConfig.OAuth2ServerConfig.Gateways.ClaudeCode.Telemetry = &configtables.ClientGatewayTelemetryConfig{
		Enabled: true,
		Destinations: []configtables.ClientGatewayTelemetryDestination{{
			Endpoint: schemas.NewSecretVar(collector.server.URL + "/"),
			Headers:  map[string]*schemas.SecretVar{"Authorization": schemas.NewSecretVar("env.BIFROST_TEST_COLLECTOR_TOKEN")},
		}},
		DropAttributes: drop,
	}
}

// signedInExport runs one export through the bearer check and the telemetry
// handler with a fresh vk-mode gateway token, as Claude Code sends it.
func signedInExport(t *testing.T, h *clientGatewayEndpoint, vk *configtables.TableVirtualKey, signal string, body []byte) *fasthttp.RequestCtx {
	t.Helper()
	signingKey, err := h.store.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	priv, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	require.NoError(t, err)
	token := mintTestToken(t, priv, signingKey.KID, func(c jwt.MapClaims) {
		c["aud"] = jwt.ClaimStrings{testClaudeCodeResource}
		c["sub"] = vk.ID
	})
	ctx := bearerCtx(token)
	ctx.Request.Header.SetContentType("application/x-protobuf")
	ctx.Request.SetBody(body)
	h.BearerMiddleware(h.telemetryHandler(signal))(ctx)
	return ctx
}

// TestClientGateway_TelemetryForwarding pins the end-to-end path: a signed-in
// client's export is acknowledged at once, then reaches the collector at
// {endpoint}/v1/{signal} as protobuf with the configured header, stamped with
// the virtual key it signed in with and stripped of the dropped attributes.
func TestClientGateway_TelemetryForwarding(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, true)
	t.Cleanup(func() { h.Close(context.Background()) })
	vk := seedGatewayVK(t, store, "vk-telemetry", "sk-bf-telemetry")
	collector := newTelemetryCollector(t, http.StatusOK)
	enableTelemetry(t, h, collector, "prompt")

	body, err := proto.Marshal(sampleMetricsExport())
	require.NoError(t, err)
	ctx := signedInExport(t, h, vk, "metrics", body)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Equal(t, "application/x-protobuf", string(ctx.Response.Header.ContentType()))

	select {
	case req := <-collector.received:
		assert.Equal(t, "/v1/metrics", req.URL.Path)
		assert.Equal(t, "application/x-protobuf", req.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer collector-token", req.Header.Get("Authorization"))
	case <-time.After(5 * time.Second):
		t.Fatal("the export never reached the collector")
	}
	var got colmetricspb.ExportMetricsServiceRequest
	require.NoError(t, proto.Unmarshal(<-collector.bodies, &got))
	res := attrMap(got.ResourceMetrics[0].Resource.Attributes)
	assert.Equal(t, vk.ID, res[telemetryAttrVirtualKeyID])
	assert.Equal(t, vk.Name, res[telemetryAttrVirtualKey])
	assert.Equal(t, "claude-code", res[telemetryAttrGatewayClient])
	assert.NotContains(t, attrMap(got.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetSum().DataPoints[0].Attributes), "prompt")

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(h.telemetry.metrics.forwards.WithLabelValues("claude-code", "metrics", "ok")) == 1
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1.0, testutil.ToFloat64(h.telemetry.metrics.exports.WithLabelValues("claude-code", "metrics", "queued")))
}

// TestClientGateway_TelemetryOutcomes pins the answers that do not forward: with
// telemetry off an export is acknowledged and discarded, an oversized export is
// 413, a malformed one is 400, and a full queue drops the export but still
// acknowledges it, so the client never retries into a struggling gateway.
func TestClientGateway_TelemetryOutcomes(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, true)
	t.Cleanup(func() { h.Close(context.Background()) })
	vk := seedGatewayVK(t, store, "vk-telemetry", "sk-bf-telemetry")
	body, err := proto.Marshal(sampleMetricsExport())
	require.NoError(t, err)
	counter := func(outcome string) float64 {
		return testutil.ToFloat64(h.telemetry.metrics.exports.WithLabelValues("claude-code", "metrics", outcome))
	}

	ctx := signedInExport(t, h, vk, "metrics", body)
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	assert.Equal(t, 1.0, counter("discarded"), "telemetry off: acknowledged and discarded")

	collector := newTelemetryCollector(t, http.StatusOK)
	enableTelemetry(t, h, collector)

	ctx = signedInExport(t, h, vk, "metrics", make([]byte, clientGatewayTelemetryMaxBody+1))
	assert.Equal(t, fasthttp.StatusRequestEntityTooLarge, ctx.Response.StatusCode())

	ctx = signedInExport(t, h, vk, "metrics", []byte("not protobuf"))
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.Equal(t, 1.0, counter("invalid"))

	h.telemetry.maxBytes = 1 // every export is now over the queue's byte bound
	ctx = signedInExport(t, h, vk, "metrics", body)
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "a full queue still acknowledges")
	assert.Equal(t, 1.0, counter("dropped"))
}

// TestTelemetryForwarder_CollectorErrorsAreCounted pins that a collector refusing
// an export is counted as a forwarding error and does not stall the queue.
func TestTelemetryForwarder_CollectorErrorsAreCounted(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, true)
	t.Cleanup(func() { h.Close(context.Background()) })
	vk := seedGatewayVK(t, store, "vk-telemetry", "sk-bf-telemetry")
	collector := newTelemetryCollector(t, http.StatusServiceUnavailable)
	enableTelemetry(t, h, collector)
	body, err := proto.Marshal(sampleMetricsExport())
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		require.Equal(t, fasthttp.StatusOK, signedInExport(t, h, vk, "metrics", body).Response.StatusCode())
	}
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(h.telemetry.metrics.forwards.WithLabelValues("claude-code", "metrics", "error")) == 3
	}, 5*time.Second, 10*time.Millisecond)
	// The error counter is bumped before the worker releases the export's bytes, so wait for those too.
	require.Eventually(t, func() bool { return h.telemetry.bytes.Load() == 0 }, 5*time.Second, 10*time.Millisecond,
		"forwarded exports release their queue bytes")
}

// TestTelemetryForwarder_CloseDrainsQueue pins what every shutdown path relies on: Close forwards
// the exports already queued before it returns, and refuses new ones after.
func TestTelemetryForwarder_CloseDrainsQueue(t *testing.T) {
	h, _, store, _ := newClaudeCodeGateway(t, true)
	vk := seedGatewayVK(t, store, "vk-telemetry", "sk-bf-telemetry")
	collector := newTelemetryCollector(t, http.StatusOK)
	enableTelemetry(t, h, collector)
	body, err := proto.Marshal(sampleMetricsExport())
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.Equal(t, fasthttp.StatusOK, signedInExport(t, h, vk, "metrics", body).Response.StatusCode())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.Close(ctx)
	forwarded := func(outcome string) float64 {
		return testutil.ToFloat64(h.telemetry.metrics.forwards.WithLabelValues("claude-code", "metrics", outcome))
	}
	assert.Equal(t, 3.0, forwarded("ok"), "queued exports are forwarded before Close returns")
	assert.Zero(t, h.telemetry.bytes.Load())

	require.Equal(t, fasthttp.StatusOK, signedInExport(t, h, vk, "metrics", body).Response.StatusCode())
	assert.Equal(t, 1.0, testutil.ToFloat64(h.telemetry.metrics.exports.WithLabelValues("claude-code", "metrics", "dropped")),
		"an export after Close is dropped, not queued")
	assert.Equal(t, 3.0, forwarded("ok"))
}

// BenchmarkStampTelemetryExport measures the per-export work on the request
// path before the hand-off to the queue: decode, stamp, filter, re-encode.
func BenchmarkStampTelemetryExport(b *testing.B) {
	body, err := proto.Marshal(sampleMetricsExport())
	require.NoError(b, err)
	drop := []string{"prompt"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := stampTelemetryExport("metrics", body, false, "", "claude-code", testTelemetryIdentity, drop); err != nil {
			b.Fatal(err)
		}
	}
}
