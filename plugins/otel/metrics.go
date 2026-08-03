package otel

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// MetricsConfig holds configuration for the OTEL metrics exporter
type MetricsConfig struct {
	ServiceName  string
	Endpoint     string
	Headers      map[string]string
	Protocol     Protocol
	TLSCACert    string
	Insecure     bool // Skip TLS when true; ignored if TLSCACert is set
	PushInterval int  // in seconds
}

// MetricsExporter handles OTEL metrics export
type MetricsExporter struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter

	// Bifrost metrics - counters
	upstreamRequestsTotal *syncInt64Counter
	successRequestsTotal  *syncInt64Counter
	errorRequestsTotal    *syncInt64Counter
	inputTokensTotal      *syncInt64Counter
	outputTokensTotal     *syncInt64Counter
	cacheHitsTotal        *syncInt64Counter

	// Provider-side prompt cache token counters (distinct from cacheHitsTotal, which
	// counts Bifrost's own semantic-cache hits).
	cacheReadInputTokensTotal    *syncInt64Counter
	cacheWriteInputTokensTotal   *syncInt64Counter
	cacheWriteInputTokens5mTotal *syncInt64Counter
	cacheWriteInputTokens1hTotal *syncInt64Counter

	// Bifrost metrics - float counters (for cost)
	costTotal *syncFloat64Counter

	// Bifrost metrics - histograms
	upstreamLatencySeconds         *syncFloat64Histogram
	streamFirstTokenLatencySeconds *syncFloat64Histogram
	streamInterTokenLatencySeconds *syncFloat64Histogram
	requestRetries                 *syncFloat64Histogram

	// OTel MCP semconv duration histogram. _count gives call volume and error.type gives
	// the error rate, so no separate MCP counters are needed.
	mcpClientOperationDuration *syncFloat64Histogram

	// HTTP metrics
	httpRequestsTotal     *syncInt64Counter
	httpRequestDuration   *syncFloat64Histogram
	httpRequestSizeBytes  *syncFloat64Histogram
	httpResponseSizeBytes *syncFloat64Histogram
}

// syncInt64Counter wraps metric.Int64Counter with thread-safe lazy initialization
type syncInt64Counter struct {
	counter metric.Int64Counter
	once    sync.Once
	name    string
	desc    string
	unit    string
	meter   metric.Meter
}

func (c *syncInt64Counter) Add(ctx context.Context, value int64, opts ...metric.AddOption) {
	c.once.Do(func() {
		var err error
		c.counter, err = c.meter.Int64Counter(c.name,
			metric.WithDescription(c.desc),
			metric.WithUnit(c.unit),
		)
		if err != nil {
			logger.Error("failed to create counter %s: %v", c.name, err)
		}
	})
	if c.counter != nil {
		c.counter.Add(ctx, value, opts...)
	}
}

// syncFloat64Counter wraps metric.Float64Counter with thread-safe lazy initialization
type syncFloat64Counter struct {
	counter metric.Float64Counter
	once    sync.Once
	name    string
	desc    string
	unit    string
	meter   metric.Meter
}

func (c *syncFloat64Counter) Add(ctx context.Context, value float64, opts ...metric.AddOption) {
	c.once.Do(func() {
		var err error
		c.counter, err = c.meter.Float64Counter(c.name,
			metric.WithDescription(c.desc),
			metric.WithUnit(c.unit),
		)
		if err != nil {
			logger.Error("failed to create float counter %s: %v", c.name, err)
		}
	})
	if c.counter != nil {
		c.counter.Add(ctx, value, opts...)
	}
}

// Keep in sync with plugins/telemetry/main.go's identical arrays so the Prometheus
// and OTel exporters report the same quantile estimates for the same metric.
var (
	// upstreamLatencyBuckets: end-to-end / upstream LLM call latency. Top end (900s)
	// covers reasoning-model and long-context outliers; without these buckets p99
	// collapses to the highest finite bucket boundary.
	upstreamLatencyBuckets = []float64{
		.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5,
		10, 15, 30, 45, 60, 90, 120, 180, 300, 600, 900,
	}

	// firstTokenLatencyBuckets: TTFT. Bimodal - sub-second for fast streaming
	// providers, tens to hundreds of seconds for reasoning models. Purely additive
	// over the prior SDK-default fallback so historical queries remain valid.
	firstTokenLatencyBuckets = []float64{
		.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5,
		10, 20, 30, 60, 120, 300,
	}

	// interTokenLatencyBuckets: inter-token latency. Typically single-digit ms to ~1s.
	// Adds .001 for fast models (Haiku) and keeps 10 at the top so the array is
	// purely additive over the prior SDK-default fallback.
	interTokenLatencyBuckets = []float64{
		.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10,
	}

	// httpBodySizeBuckets: HTTP request/response body sizes, 100B to 1GB
	// (matches prometheus.ExponentialBuckets(100, 10, 8) on the Prometheus side).
	// The SDK default boundaries top out at 10,000, which would collapse any
	// payload over 10KB into +Inf.
	httpBodySizeBuckets = []float64{
		100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000, 100_000_000, 1_000_000_000,
	}

	// mcpOperationDurationBuckets: boundaries recommended by the MCP semconv.
	mcpOperationDurationBuckets = []float64{
		0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300,
	}
)

// syncFloat64Histogram wraps metric.Float64Histogram with thread-safe lazy initialization
type syncFloat64Histogram struct {
	histogram  metric.Float64Histogram
	once       sync.Once
	name       string
	desc       string
	unit       string
	meter      metric.Meter
	boundaries []float64
}

func (h *syncFloat64Histogram) Record(ctx context.Context, value float64, opts ...metric.RecordOption) {
	h.once.Do(func() {
		// Explicit boundaries must be set at histogram-creation time; the SDK
		// default is calibrated for milliseconds and silently collapses our
		// seconds-valued latencies into +Inf above ~10s without this.
		histOpts := []metric.Float64HistogramOption{
			metric.WithDescription(h.desc),
			metric.WithUnit(h.unit),
		}
		if len(h.boundaries) > 0 {
			histOpts = append(histOpts, metric.WithExplicitBucketBoundaries(h.boundaries...))
		}
		var err error
		h.histogram, err = h.meter.Float64Histogram(h.name, histOpts...)
		if err != nil {
			logger.Error("failed to create histogram %s: %v", h.name, err)
		}
	})
	if h.histogram != nil {
		h.histogram.Record(ctx, value, opts...)
	}
}

// NewMetricsExporter creates a new OTEL metrics exporter
func NewMetricsExporter(ctx context.Context, config *MetricsConfig) (*MetricsExporter, error) {
	// Generate a unique instance ID for this node
	instanceID, err := os.Hostname()
	if err != nil {
		instanceID = fmt.Sprintf("bifrost-%d", time.Now().UnixNano())
	}

	// Create resource with service info
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(config.ServiceName),
			semconv.ServiceInstanceID(instanceID),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create exporter based on protocol
	var exporter sdkmetric.Exporter
	if config.Protocol == ProtocolGRPC {
		exporter, err = createGRPCExporter(ctx, config)
	} else {
		exporter, err = createHTTPExporter(ctx, config)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create exporter: %w", err)
	}

	// Create meter provider with periodic reader
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(
				exporter,
				sdkmetric.WithInterval(time.Duration(config.PushInterval)*time.Second),
			),
		),
	)

	// Set as global provider
	otel.SetMeterProvider(provider)

	// Create meter
	meter := provider.Meter("bifrost",
		metric.WithInstrumentationVersion("1.0.0"),
	)

	// Create metrics exporter
	m := &MetricsExporter{
		provider: provider,
		meter:    meter,
	}

	// Initialize metrics with lazy loading wrappers
	m.initMetrics()

	return m, nil
}

func createHTTPExporter(ctx context.Context, config *MetricsConfig) (sdkmetric.Exporter, error) {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpointURL(config.Endpoint),
	}

	if len(config.Headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(config.Headers))
	}

	// HTTP metrics insecure mode disables TLS entirely (unlike the trace HTTP client
	// which uses InsecureSkipVerify). buildTLSConfig is bypassed for that case.
	if config.TLSCACert == "" && config.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	} else {
		tlsConfig, err := buildTLSConfig(config.TLSCACert, false)
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlpmetrichttp.WithTLSClientConfig(tlsConfig))
	}

	return otlpmetrichttp.New(ctx, opts...)
}

func createGRPCExporter(ctx context.Context, config *MetricsConfig) (sdkmetric.Exporter, error) {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(config.Endpoint),
	}

	if len(config.Headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(config.Headers))
	}

	// gRPC insecure mode uses plaintext (no TLS at all). buildTLSConfig is bypassed for that case.
	if config.TLSCACert == "" && config.Insecure {
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(insecure.NewCredentials()))
	} else {
		tlsConfig, err := buildTLSConfig(config.TLSCACert, false)
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(tlsConfig)))
	}

	return otlpmetricgrpc.New(ctx, opts...)
}

func (m *MetricsExporter) initMetrics() {
	// Bifrost upstream metrics
	m.upstreamRequestsTotal = &syncInt64Counter{
		name:  "bifrost_upstream_requests_total",
		desc:  "Total number of requests forwarded to upstream providers by Bifrost",
		unit:  "{request}",
		meter: m.meter,
	}

	m.successRequestsTotal = &syncInt64Counter{
		name:  "bifrost_success_requests_total",
		desc:  "Total number of successful requests forwarded to upstream providers by Bifrost",
		unit:  "{request}",
		meter: m.meter,
	}

	m.errorRequestsTotal = &syncInt64Counter{
		name:  "bifrost_error_requests_total",
		desc:  "Total number of error requests forwarded to upstream providers by Bifrost",
		unit:  "{request}",
		meter: m.meter,
	}

	m.inputTokensTotal = &syncInt64Counter{
		name:  "bifrost_input_tokens_total",
		desc:  "Total number of input tokens forwarded to upstream providers by Bifrost",
		unit:  "{token}",
		meter: m.meter,
	}

	m.outputTokensTotal = &syncInt64Counter{
		name:  "bifrost_output_tokens_total",
		desc:  "Total number of output tokens forwarded to upstream providers by Bifrost",
		unit:  "{token}",
		meter: m.meter,
	}

	m.cacheHitsTotal = &syncInt64Counter{
		name:  "bifrost_cache_hits_total",
		desc:  "Total number of cache hits forwarded to upstream providers by Bifrost",
		unit:  "{hit}",
		meter: m.meter,
	}

	m.cacheReadInputTokensTotal = &syncInt64Counter{
		name:  "bifrost_cache_read_input_tokens_total",
		desc:  "Total provider-side prompt-cache read (cached) input tokens. Billed at a reduced rate by the provider",
		unit:  "{token}",
		meter: m.meter,
	}

	m.cacheWriteInputTokensTotal = &syncInt64Counter{
		name:  "bifrost_cache_write_input_tokens_total",
		desc:  "Total provider-side prompt-cache creation (write) input tokens",
		unit:  "{token}",
		meter: m.meter,
	}

	m.cacheWriteInputTokens5mTotal = &syncInt64Counter{
		name:  "bifrost_cache_write_input_tokens_5m_total",
		desc:  "Provider-side prompt-cache write input tokens with a 5-minute TTL (Anthropic only). Subset of bifrost_cache_write_input_tokens_total — do not sum with it",
		unit:  "{token}",
		meter: m.meter,
	}

	m.cacheWriteInputTokens1hTotal = &syncInt64Counter{
		name:  "bifrost_cache_write_input_tokens_1h_total",
		desc:  "Provider-side prompt-cache write input tokens with a 1-hour TTL (Anthropic only). Subset of bifrost_cache_write_input_tokens_total — do not sum with it",
		unit:  "{token}",
		meter: m.meter,
	}

	m.costTotal = &syncFloat64Counter{
		name:  "bifrost_cost_total",
		desc:  "Total cost in USD for requests to upstream providers",
		unit:  "USD",
		meter: m.meter,
	}

	m.upstreamLatencySeconds = &syncFloat64Histogram{
		name:       "bifrost_upstream_latency_seconds",
		desc:       "Latency of requests forwarded to upstream providers by Bifrost",
		unit:       "s",
		meter:      m.meter,
		boundaries: upstreamLatencyBuckets,
	}

	m.streamFirstTokenLatencySeconds = &syncFloat64Histogram{
		name:       "bifrost_stream_first_token_latency_seconds",
		desc:       "Latency of the first token of a stream response",
		unit:       "s",
		meter:      m.meter,
		boundaries: firstTokenLatencyBuckets,
	}

	m.streamInterTokenLatencySeconds = &syncFloat64Histogram{
		name:       "bifrost_stream_inter_token_latency_seconds",
		desc:       "Latency of the intermediate tokens of a stream response",
		unit:       "s",
		meter:      m.meter,
		boundaries: interTokenLatencyBuckets,
	}

	m.requestRetries = &syncFloat64Histogram{
		name:       "bifrost_request_retries",
		desc:       "Number of retries used per request (observed once per request)",
		unit:       "{retry}",
		meter:      m.meter,
		boundaries: []float64{0, 1, 2, 3, 5, 10},
	}

	// Dotted name is intentional: the exact semconv metric name, not a bifrost_* metric.
	m.mcpClientOperationDuration = &syncFloat64Histogram{
		name:       "mcp.client.operation.duration",
		desc:       "Duration of an MCP request as observed by the client (Bifrost) from send until the response is received",
		unit:       "s",
		meter:      m.meter,
		boundaries: mcpOperationDurationBuckets,
	}

	// HTTP metrics
	m.httpRequestsTotal = &syncInt64Counter{
		name:  "http_requests_total",
		desc:  "Total number of HTTP requests",
		unit:  "{request}",
		meter: m.meter,
	}

	m.httpRequestDuration = &syncFloat64Histogram{
		name:       "http_request_duration_seconds",
		desc:       "Duration of HTTP requests",
		unit:       "s",
		meter:      m.meter,
		boundaries: upstreamLatencyBuckets,
	}

	m.httpRequestSizeBytes = &syncFloat64Histogram{
		name:       "http_request_size_bytes",
		desc:       "Size of HTTP requests",
		unit:       "By",
		meter:      m.meter,
		boundaries: httpBodySizeBuckets,
	}

	m.httpResponseSizeBytes = &syncFloat64Histogram{
		name:       "http_response_size_bytes",
		desc:       "Size of HTTP responses",
		unit:       "By",
		meter:      m.meter,
		boundaries: httpBodySizeBuckets,
	}
}

// Shutdown gracefully shuts down the metrics exporter
func (m *MetricsExporter) Shutdown(ctx context.Context) error {
	if m.provider != nil {
		return m.provider.Shutdown(ctx)
	}
	return nil
}

// RecordUpstreamRequest records an upstream request metric
func (m *MetricsExporter) RecordUpstreamRequest(ctx context.Context, attrs ...attribute.KeyValue) {
	m.upstreamRequestsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordSuccessRequest records a successful request metric
func (m *MetricsExporter) RecordSuccessRequest(ctx context.Context, attrs ...attribute.KeyValue) {
	m.successRequestsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordErrorRequest records an error request metric
func (m *MetricsExporter) RecordErrorRequest(ctx context.Context, attrs ...attribute.KeyValue) {
	m.errorRequestsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordInputTokens records input tokens metric
func (m *MetricsExporter) RecordInputTokens(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	m.inputTokensTotal.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordOutputTokens records output tokens metric
func (m *MetricsExporter) RecordOutputTokens(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	m.outputTokensTotal.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordCacheHit records a cache hit metric
func (m *MetricsExporter) RecordCacheHit(ctx context.Context, attrs ...attribute.KeyValue) {
	m.cacheHitsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordCacheReadInputTokens records provider-side prompt-cache read (cached) input tokens.
func (m *MetricsExporter) RecordCacheReadInputTokens(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	m.cacheReadInputTokensTotal.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordCacheWriteInputTokens records provider-side prompt-cache creation (write) input tokens.
func (m *MetricsExporter) RecordCacheWriteInputTokens(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	m.cacheWriteInputTokensTotal.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordCacheWriteInputTokens5m records the 5-minute-TTL subset of cache-write input tokens.
func (m *MetricsExporter) RecordCacheWriteInputTokens5m(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	m.cacheWriteInputTokens5mTotal.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordCacheWriteInputTokens1h records the 1-hour-TTL subset of cache-write input tokens.
func (m *MetricsExporter) RecordCacheWriteInputTokens1h(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	m.cacheWriteInputTokens1hTotal.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordCost records cost metric
func (m *MetricsExporter) RecordCost(ctx context.Context, cost float64, attrs ...attribute.KeyValue) {
	m.costTotal.Add(ctx, cost, metric.WithAttributes(attrs...))
}

// RecordUpstreamLatency records upstream latency metric
func (m *MetricsExporter) RecordUpstreamLatency(ctx context.Context, latencySeconds float64, attrs ...attribute.KeyValue) {
	m.upstreamLatencySeconds.Record(ctx, latencySeconds, metric.WithAttributes(attrs...))
}

// RecordStreamFirstTokenLatency records first token latency metric
func (m *MetricsExporter) RecordStreamFirstTokenLatency(ctx context.Context, latencySeconds float64, attrs ...attribute.KeyValue) {
	m.streamFirstTokenLatencySeconds.Record(ctx, latencySeconds, metric.WithAttributes(attrs...))
}

// RecordStreamInterTokenLatency records inter-token latency metric
func (m *MetricsExporter) RecordStreamInterTokenLatency(ctx context.Context, latencySeconds float64, attrs ...attribute.KeyValue) {
	m.streamInterTokenLatencySeconds.Record(ctx, latencySeconds, metric.WithAttributes(attrs...))
}

// RecordRequestRetries records the number of retries used for a single request.
// Recorded once per request (off the final span), not once per attempt.
func (m *MetricsExporter) RecordRequestRetries(ctx context.Context, retries float64, attrs ...attribute.KeyValue) {
	m.requestRetries.Record(ctx, retries, metric.WithAttributes(attrs...))
}

// RecordMCPOperationDuration records the mcp.client.operation.duration metric for one op.
func (m *MetricsExporter) RecordMCPOperationDuration(ctx context.Context, durationSeconds float64, attrs ...attribute.KeyValue) {
	m.mcpClientOperationDuration.Record(ctx, durationSeconds, metric.WithAttributes(attrs...))
}

// RecordHTTPRequest records an HTTP request metric
func (m *MetricsExporter) RecordHTTPRequest(ctx context.Context, attrs ...attribute.KeyValue) {
	m.httpRequestsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordHTTPRequestDuration records HTTP request duration metric
func (m *MetricsExporter) RecordHTTPRequestDuration(ctx context.Context, durationSeconds float64, attrs ...attribute.KeyValue) {
	m.httpRequestDuration.Record(ctx, durationSeconds, metric.WithAttributes(attrs...))
}

// RecordHTTPRequestSize records HTTP request size metric
func (m *MetricsExporter) RecordHTTPRequestSize(ctx context.Context, sizeBytes float64, attrs ...attribute.KeyValue) {
	m.httpRequestSizeBytes.Record(ctx, sizeBytes, metric.WithAttributes(attrs...))
}

// RecordHTTPResponseSize records HTTP response size metric
func (m *MetricsExporter) RecordHTTPResponseSize(ctx context.Context, sizeBytes float64, attrs ...attribute.KeyValue) {
	m.httpResponseSizeBytes.Record(ctx, sizeBytes, metric.WithAttributes(attrs...))
}

// BuildBifrostAttributes builds common Bifrost metric attributes.
// Retry depth is intentionally NOT included here; it is reported via the dedicated
// bifrost_request_retries histogram (recorded once per request) rather than as a label
// on every per-attempt counter.
func BuildBifrostAttributes(provider, model, method, virtualKeyID, virtualKeyName, selectedKeyID, selectedKeyName string, fallbackIndex int, teamID, teamName, customerID, customerName string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("provider", provider),
		attribute.String("model", model),
		attribute.String("method", method),
		attribute.String("virtual_key_id", virtualKeyID),
		attribute.String("virtual_key_name", virtualKeyName),
		attribute.String("selected_key_id", selectedKeyID),
		attribute.String("selected_key_name", selectedKeyName),
		attribute.Int("fallback_index", fallbackIndex),
		attribute.String("team_id", teamID),
		attribute.String("team_name", teamName),
		attribute.String("customer_id", customerID),
		attribute.String("customer_name", customerName),
	}
}

// BuildHTTPAttributes builds common HTTP metric attributes
func BuildHTTPAttributes(path, method, status string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("path", path),
		attribute.String("method", method),
		attribute.String("status", status),
	}
}
