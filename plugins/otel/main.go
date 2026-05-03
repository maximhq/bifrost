// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"go.opentelemetry.io/otel/attribute"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// logger is the logger for the OTEL plugin
var logger schemas.Logger

// OTELResponseAttributesEnvKey is the environment variable key for the OTEL resource attributes
// We check if this is present in the environment variables and if so, we will use it to set the attributes for all spans at the resource level
const OTELResponseAttributesEnvKey = "OTEL_RESOURCE_ATTRIBUTES"

const PluginName = "otel"

// TraceType is the type of trace to use for the OTEL collector
type TraceType string

// TraceTypeGenAIExtension is the type of trace to use for the OTEL collector
const TraceTypeGenAIExtension TraceType = "genai_extension"

// TraceTypeVercel is the type of trace to use for the OTEL collector
const TraceTypeVercel TraceType = "vercel"

// TraceTypeOpenInference is the type of trace to use for the OTEL collector
const TraceTypeOpenInference TraceType = "open_inference"

// Protocol is the protocol to use for the OTEL collector
type Protocol string

// ProtocolHTTP is the default protocol
const ProtocolHTTP Protocol = "http"

// ProtocolGRPC is the second protocol
const ProtocolGRPC Protocol = "grpc"

type Config struct {
	ServiceName  string            `json:"service_name"`
	CollectorURL string            `json:"collector_url"`
	Headers      map[string]string `json:"headers"`
	TraceType    TraceType         `json:"trace_type"`
	Protocol     Protocol          `json:"protocol"`
	TLSCACert    string            `json:"tls_ca_cert"`
	Insecure     bool              `json:"insecure"` // Skip TLS when true; ignored if TLSCACert is set. Defaults to true when omitted.

	// Metrics push configuration
	MetricsEnabled      bool   `json:"metrics_enabled"`
	MetricsEndpoint     string `json:"metrics_endpoint"`
	MetricsPushInterval int    `json:"metrics_push_interval"` // in seconds, default 15
}

// UnmarshalJSON applies field defaults that the zero-value wouldn't capture.
// Specifically, Insecure defaults to true when the key is omitted so http://
// collectors work out-of-the-box without forcing users to set it explicitly.
func (c *Config) UnmarshalJSON(data []byte) error {
	type alias Config
	aux := struct {
		Insecure *bool `json:"insecure"`
		*alias
	}{
		alias: (*alias)(c),
	}
	if err := sonic.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Insecure == nil {
		c.Insecure = true
	} else {
		c.Insecure = *aux.Insecure
	}
	return nil
}

// OtelPlugin is the plugin for OpenTelemetry.
// It implements the ObservabilityPlugin interface to receive completed traces
// from the tracing middleware and forward them to an OTEL collector.
type OtelPlugin struct {
	ctx    context.Context
	cancel context.CancelFunc

	serviceName string
	url         string
	headers     map[string]string
	traceType   TraceType
	protocol    Protocol

	bifrostVersion string

	attributesFromEnvironment []*commonpb.KeyValue
	instanceAttrs             []*commonpb.KeyValue // machine ID + pod labels, added only to root spans

	client OtelClient

	pricingManager *modelcatalog.ModelCatalog

	// Metrics push support
	metricsExporter *MetricsExporter
}

// Init function for the OTEL plugin
func Init(ctx context.Context, config *Config, _logger schemas.Logger, pricingManager *modelcatalog.ModelCatalog, bifrostVersion string) (*OtelPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	logger = _logger
	if pricingManager == nil {
		logger.Warn("otel plugin requires model catalog to calculate cost, all cost calculations will be skipped.")
	}
	var err error
	// If headers are present, and any of them start with env., we will replace the value with the environment variable
	if config.Headers != nil {
		for key, value := range config.Headers {
			if newValue, ok := strings.CutPrefix(value, "env."); ok {
				config.Headers[key] = os.Getenv(newValue)
				if config.Headers[key] == "" {
					logger.Warn("environment variable %s not found", newValue)
					return nil, fmt.Errorf("environment variable %s not found", newValue)
				}
			}
		}
	}
	if config.ServiceName == "" {
		config.ServiceName = "bifrost"
	}
	// Loading attributes from environment
	attributesFromEnvironment := make([]*commonpb.KeyValue, 0)
	if attributes, ok := os.LookupEnv(OTELResponseAttributesEnvKey); ok {
		// We will split the attributes by , and then split each attribute by =
		for attribute := range strings.SplitSeq(attributes, ",") {
			attributeParts := strings.Split(strings.TrimSpace(attribute), "=")
			if len(attributeParts) == 2 {
				attributesFromEnvironment = append(attributesFromEnvironment, kvStr(strings.TrimSpace(attributeParts[0]), strings.TrimSpace(attributeParts[1])))
			}
		}
	}

	// Build instance-level attrs (machine ID + pod labels) — added only to root spans
	instanceAttrs := make([]*commonpb.KeyValue, 0)
	if hostname, herr := os.Hostname(); herr == nil && hostname != "" {
		instanceAttrs = append(instanceAttrs, kvStr("service.instance.id", hostname))
	}
	if podName := firstNonEmpty(os.Getenv("MY_POD_NAME"), os.Getenv("POD_NAME")); podName != "" {
		instanceAttrs = append(instanceAttrs, kvStr("k8s.pod.name", podName))
	}
	if podNamespace := firstNonEmpty(os.Getenv("MY_POD_NAMESPACE"), os.Getenv("POD_NAMESPACE"), os.Getenv("NAMESPACE")); podNamespace != "" {
		instanceAttrs = append(instanceAttrs, kvStr("k8s.namespace.name", podNamespace))
	}
	if nodeName := firstNonEmpty(os.Getenv("MY_NODE_NAME"), os.Getenv("NODE_NAME")); nodeName != "" {
		instanceAttrs = append(instanceAttrs, kvStr("k8s.node.name", nodeName))
	}
	// Preparing the plugin
	p := &OtelPlugin{
		serviceName:               config.ServiceName,
		url:                       config.CollectorURL,
		traceType:                 config.TraceType,
		headers:                   config.Headers,
		protocol:                  config.Protocol,
		pricingManager:            pricingManager,
		bifrostVersion:            bifrostVersion,
		attributesFromEnvironment: attributesFromEnvironment,
		instanceAttrs:             instanceAttrs,
	}
	p.ctx, p.cancel = context.WithCancel(ctx)
	if config.Protocol == ProtocolGRPC {
		p.client, err = NewOtelClientGRPC(config.CollectorURL, config.Headers, config.TLSCACert, config.Insecure)
		if err != nil {
			return nil, err
		}
	}
	if config.Protocol == ProtocolHTTP {
		p.client, err = NewOtelClientHTTP(config.CollectorURL, config.Headers, config.TLSCACert, config.Insecure)
		if err != nil {
			return nil, err
		}
	}
	if p.client == nil {
		return nil, fmt.Errorf("otel client is not initialized. invalid protocol type")
	}

	// Initialize metrics exporter if enabled
	if config.MetricsEnabled {
		if config.MetricsEndpoint == "" {
			return nil, fmt.Errorf("metrics_endpoint is required when metrics_enabled is true")
		}
		pushInterval := config.MetricsPushInterval
		if pushInterval <= 0 {
			pushInterval = 15 // default 15 seconds
		} else if pushInterval > 300 {
			return nil, fmt.Errorf("metrics_push_interval must be between 1 and 300 seconds, got %d", pushInterval)
		}
		metricsConfig := &MetricsConfig{
			ServiceName:  config.ServiceName,
			Endpoint:     config.MetricsEndpoint,
			Headers:      config.Headers,
			Protocol:     config.Protocol,
			TLSCACert:    config.TLSCACert,
			Insecure:     config.Insecure,
			PushInterval: pushInterval,
		}
		p.metricsExporter, err = NewMetricsExporter(p.ctx, metricsConfig)
		if err != nil {
			// Clean up trace client if metrics exporter fails
			if p.client != nil {
				p.client.Close()
			}
			return nil, fmt.Errorf("failed to initialize metrics exporter: %w", err)
		}
		logger.Info("OTEL metrics push enabled, pushing to %s every %d seconds", config.MetricsEndpoint, pushInterval)
	}

	return p, nil
}

// GetName function for the OTEL plugin
func (p *OtelPlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used for this plugin
func (p *OtelPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *OtelPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *OtelPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// ValidateConfig function for the OTEL plugin
func (p *OtelPlugin) ValidateConfig(config any) (*Config, error) {
	var otelConfig Config
	// Checking if its a string, then we will JSON parse and confirm
	if configStr, ok := config.(string); ok {
		if err := sonic.Unmarshal([]byte(configStr), &otelConfig); err != nil {
			return nil, err
		}
	}
	// Checking if its a map[string]any, then we will JSON parse and confirm
	if configMap, ok := config.(map[string]any); ok {
		configString, err := sonic.Marshal(configMap)
		if err != nil {
			return nil, err
		}
		if err := sonic.Unmarshal([]byte(configString), &otelConfig); err != nil {
			return nil, err
		}
	}
	// Checking if its a Config, then we will confirm
	if config, ok := config.(*Config); ok {
		otelConfig = *config
	}
	// Validating fields
	if otelConfig.CollectorURL == "" {
		return nil, fmt.Errorf("collector url is required")
	}
	if otelConfig.TraceType == "" {
		return nil, fmt.Errorf("trace type is required")
	}
	if otelConfig.Protocol == "" {
		return nil, fmt.Errorf("protocol is required")
	}
	return &otelConfig, nil
}

// PreLLMHook is a no-op - tracing is handled via the Inject method.
// The OTEL plugin receives completed traces from TracingMiddleware.
func (p *OtelPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook is a no-op - tracing is handled via the Inject method.
// The OTEL plugin receives completed traces from TracingMiddleware.
func (p *OtelPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// Inject receives a completed trace and sends it to the OTEL collector.
// Implements schemas.ObservabilityPlugin interface.
// This method is called asynchronously by TracingMiddleware after the response
// has been written to the client.
func (p *OtelPlugin) Inject(ctx context.Context, trace *schemas.Trace) error {
	if trace == nil {
		return nil
	}

	// Emit trace to collector if client is initialized
	if p.client != nil {
		// Convert schemas.Trace to OTEL ResourceSpan
		resourceSpan := p.convertTraceToResourceSpan(trace)

		// Emit to collector
		if err := p.client.Emit(ctx, []*ResourceSpan{resourceSpan}); err != nil {
			logger.Error("failed to emit trace %s: %v", trace.TraceID, err)
		}
	}

	// Record metrics if metrics exporter is enabled
	if p.metricsExporter != nil {
		p.recordMetricsFromTrace(ctx, trace)
	}

	return nil
}

// Helper functions for type-safe attribute extraction from trace spans
func getStringAttr(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	if v, ok := attrs[key].(string); ok {
		return v
	}
	return ""
}

func getIntAttr(attrs map[string]any, key string) int {
	if attrs == nil {
		return 0
	}
	switch v := attrs[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func getFloat64Attr(attrs map[string]any, key string) float64 {
	if attrs == nil {
		return 0
	}
	switch v := attrs[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// buildSpanAttrs extracts metric dimension attrs from a single attempt span.
func buildSpanAttrs(span *schemas.Span) []attribute.KeyValue {
	attrs := span.Attributes
	method := getStringAttr(attrs, "request.type")
	if method == "" {
		method = span.Name
	}
	return BuildBifrostAttributes(
		getStringAttr(attrs, schemas.AttrProviderName),
		getStringAttr(attrs, schemas.AttrRequestModel),
		method,
		getStringAttr(attrs, schemas.AttrVirtualKeyID),
		getStringAttr(attrs, schemas.AttrVirtualKeyName),
		getStringAttr(attrs, schemas.AttrSelectedKeyID),
		getStringAttr(attrs, schemas.AttrSelectedKeyName),
		getIntAttr(attrs, schemas.AttrNumberOfRetries),
		getIntAttr(attrs, schemas.AttrFallbackIndex),
		getStringAttr(attrs, schemas.AttrTeamID),
		getStringAttr(attrs, schemas.AttrTeamName),
		getStringAttr(attrs, schemas.AttrCustomerID),
		getStringAttr(attrs, schemas.AttrCustomerName),
	)
}

// recordMetricsFromTrace extracts metrics data from a completed trace and records them
// via the OTEL metrics exporter. This is called from Inject after trace emission.
//
// Per-attempt metrics (upstream_requests, errors, success, latency) are recorded once
// per llm.call/retry span so fallback attempts and failed retries are counted with
// their own provider/model/fallback_index labels. Per-trace metrics (tokens, cost,
// TTFT) are recorded once, keyed off the final (latest) attempt span.
func (p *OtelPlugin) recordMetricsFromTrace(ctx context.Context, trace *schemas.Trace) {
	if trace == nil || p.metricsExporter == nil {
		return
	}

	var finalSpan *schemas.Span
	for _, span := range trace.Spans {
		if span.Kind != schemas.SpanKindLLMCall && span.Kind != schemas.SpanKindRetry {
			continue
		}

		spanAttrs := buildSpanAttrs(span)

		p.metricsExporter.RecordUpstreamRequest(ctx, spanAttrs...)

		if !span.StartTime.IsZero() && !span.EndTime.IsZero() {
			latencySeconds := span.EndTime.Sub(span.StartTime).Seconds()
			p.metricsExporter.RecordUpstreamLatency(ctx, latencySeconds, spanAttrs...)
		}

		if span.Status == schemas.SpanStatusError {
			p.metricsExporter.RecordErrorRequest(ctx, spanAttrs...)
		} else {
			p.metricsExporter.RecordSuccessRequest(ctx, spanAttrs...)
		}

		if finalSpan == nil || span.EndTime.After(finalSpan.EndTime) {
			finalSpan = span
		}
	}

	if finalSpan == nil {
		finalSpan = trace.RootSpan
	}
	if finalSpan == nil {
		return
	}

	attrs := finalSpan.Attributes
	otelAttrs := buildSpanAttrs(finalSpan)

	// Record token usage - try both naming conventions
	inputTokens := getIntAttr(attrs, schemas.AttrPromptTokens)
	if inputTokens == 0 {
		inputTokens = getIntAttr(attrs, schemas.AttrInputTokens)
	}
	if inputTokens > 0 {
		p.metricsExporter.RecordInputTokens(ctx, int64(inputTokens), otelAttrs...)
	}

	outputTokens := getIntAttr(attrs, schemas.AttrCompletionTokens)
	if outputTokens == 0 {
		outputTokens = getIntAttr(attrs, schemas.AttrOutputTokens)
	}
	if outputTokens > 0 {
		p.metricsExporter.RecordOutputTokens(ctx, int64(outputTokens), otelAttrs...)
	}

	// Record cost if available
	cost := getFloat64Attr(attrs, schemas.AttrUsageCost)
	if cost > 0 {
		p.metricsExporter.RecordCost(ctx, cost, otelAttrs...)
	}

	// Record streaming latency metrics if available
	ttft := getFloat64Attr(attrs, schemas.AttrTimeToFirstToken)
	if ttft > 0 {
		// Convert from nanoseconds to seconds if needed (check the unit)
		p.metricsExporter.RecordStreamFirstTokenLatency(ctx, ttft/1e9, otelAttrs...)
	}
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	if p.cancel != nil {
		p.cancel()
	}
	// Shutdown metrics exporter first
	if p.metricsExporter != nil {
		if err := p.metricsExporter.Shutdown(context.Background()); err != nil {
			logger.Error("failed to shutdown metrics exporter: %v", err)
		}
	}
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// GetMetricsExporter returns the metrics exporter for external use (e.g., by telemetry plugin)
func (p *OtelPlugin) GetMetricsExporter() *MetricsExporter {
	return p.metricsExporter
}

// firstNonEmpty returns the first non-empty string from the provided values.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Compile-time check that OtelPlugin implements ObservabilityPlugin
var _ schemas.ObservabilityPlugin = (*OtelPlugin)(nil)
