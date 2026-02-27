package otel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
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

	// Bifrost metrics - float counters (for cost)
	costTotal *syncFloat64Counter

	// Bifrost metrics - histograms
	upstreamLatencySeconds         *syncFloat64Histogram
	streamFirstTokenLatencySeconds *syncFloat64Histogram
	streamInterTokenLatencySeconds *syncFloat64Histogram

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

// syncFloat64Histogram wraps metric.Float64Histogram with thread-safe lazy initialization
type syncFloat64Histogram struct {
	histogram metric.Float64Histogram
	once      sync.Once
	name      string
	desc      string
	unit      string
	meter     metric.Meter
}

func (h *syncFloat64Histogram) Record(ctx context.Context, value float64, opts ...metric.RecordOption) {
	h.once.Do(func() {
		var err error
		h.histogram, err = h.meter.Float64Histogram(h.name,
			metric.WithDescription(h.desc),
			metric.WithUnit(h.unit),
		)
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
		resource.NewWithAttributes(
			semconv.SchemaURL,
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

// validateCACertPath validates the CA certificate path to prevent path traversal attacks.
// It ensures the path is absolute, cleaned of traversal sequences, and exists as a regular file.
func validateCACertPath(certPath string) error {
	if certPath == "" {
		return nil
	}

	// Clean the path to resolve any .. or . components
	cleanPath := filepath.Clean(certPath)

	// Require absolute paths to prevent relative path attacks
	if !filepath.IsAbs(cleanPath) {
		return fmt.Errorf("TLS CA cert path must be absolute: %s", certPath)
	}

	// Check that the cleaned path doesn't differ significantly from input
	// (indicates attempted traversal)
	if cleanPath != filepath.Clean(filepath.FromSlash(certPath)) {
		return fmt.Errorf("invalid TLS CA cert path: %s", certPath)
	}

	// Verify the file exists and is not a symlink
	info, err := os.Lstat(cleanPath)
	if err != nil {
		return fmt.Errorf("TLS CA cert path not accessible: %w", err)
	}
	// Reject symlinks to prevent symlink-based path traversal
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("TLS CA cert path cannot be a symlink: %s", certPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("TLS CA cert path is not a regular file: %s", certPath)
	}

	return nil
}

func createHTTPExporter(ctx context.Context, config *MetricsConfig) (sdkmetric.Exporter, error) {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpointURL(config.Endpoint),
	}

	if len(config.Headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(config.Headers))
	}

	// TLS priority: custom CA > system roots > insecure
	if config.TLSCACert != "" {
		// Validate the CA cert path to prevent path traversal attacks
		if err := validateCACertPath(config.TLSCACert); err != nil {
			return nil, err
		}
		// Use custom CA certificate
		caCert, err := os.ReadFile(config.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA cert: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA cert")
		}
		tlsConfig := &tls.Config{
			RootCAs:    caCertPool,
			MinVersion: tls.VersionTLS12,
		}
		opts = append(opts, otlpmetrichttp.WithTLSClientConfig(tlsConfig))
	} else if config.Insecure {
		// Skip TLS entirely
		opts = append(opts, otlpmetrichttp.WithInsecure())
	} else {
		// Use system root CAs (empty tls.Config uses system roots)
		opts = append(opts, otlpmetrichttp.WithTLSClientConfig(&tls.Config{
			MinVersion: tls.VersionTLS12,
		}))
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

	// TLS priority: custom CA > system roots > insecure
	if config.TLSCACert != "" {
		// Validate the CA cert path to prevent path traversal attacks
		if err := validateCACertPath(config.TLSCACert); err != nil {
			return nil, err
		}
		// Use custom CA certificate with MinVersion
		caCert, err := os.ReadFile(config.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA cert: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA cert")
		}
		tlsConfig := &tls.Config{
			RootCAs:    caCertPool,
			MinVersion: tls.VersionTLS12,
		}
		creds := credentials.NewTLS(tlsConfig)
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(creds))
	} else if config.Insecure {
		// Skip TLS entirely
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(insecure.NewCredentials()))
	} else {
		// Use system root CAs with MinVersion
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		creds := credentials.NewTLS(tlsConfig)
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(creds))
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

	m.costTotal = &syncFloat64Counter{
		name:  "bifrost_cost_total",
		desc:  "Total cost in USD for requests to upstream providers",
		unit:  "USD",
		meter: m.meter,
	}

	m.upstreamLatencySeconds = &syncFloat64Histogram{
		name:  "bifrost_upstream_latency_seconds",
		desc:  "Latency of requests forwarded to upstream providers by Bifrost",
		unit:  "s",
		meter: m.meter,
	}

	m.streamFirstTokenLatencySeconds = &syncFloat64Histogram{
		name:  "bifrost_stream_first_token_latency_seconds",
		desc:  "Latency of the first token of a stream response",
		unit:  "s",
		meter: m.meter,
	}

	m.streamInterTokenLatencySeconds = &syncFloat64Histogram{
		name:  "bifrost_stream_inter_token_latency_seconds",
		desc:  "Latency of the intermediate tokens of a stream response",
		unit:  "s",
		meter: m.meter,
	}

	// HTTP metrics
	m.httpRequestsTotal = &syncInt64Counter{
		name:  "http_requests_total",
		desc:  "Total number of HTTP requests",
		unit:  "{request}",
		meter: m.meter,
	}

	m.httpRequestDuration = &syncFloat64Histogram{
		name:  "http_request_duration_seconds",
		desc:  "Duration of HTTP requests",
		unit:  "s",
		meter: m.meter,
	}

	m.httpRequestSizeBytes = &syncFloat64Histogram{
		name:  "http_request_size_bytes",
		desc:  "Size of HTTP requests",
		unit:  "By",
		meter: m.meter,
	}

	m.httpResponseSizeBytes = &syncFloat64Histogram{
		name:  "http_response_size_bytes",
		desc:  "Size of HTTP responses",
		unit:  "By",
		meter: m.meter,
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

// BuildBifrostAttributes builds common Bifrost metric attributes
func BuildBifrostAttributes(provider, model, method, virtualKeyID, virtualKeyName, selectedKeyID, selectedKeyName string, numberOfRetries, fallbackIndex int, teamID, teamName, customerID, customerName string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("provider", provider),
		attribute.String("model", model),
		attribute.String("method", method),
		attribute.String("virtual_key_id", virtualKeyID),
		attribute.String("virtual_key_name", virtualKeyName),
		attribute.String("selected_key_id", selectedKeyID),
		attribute.String("selected_key_name", selectedKeyName),
		attribute.Int("number_of_retries", numberOfRetries),
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
