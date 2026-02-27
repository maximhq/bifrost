// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
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

// Config for OTEL plugin
type Config struct {
	ServiceName  string            `json:"service_name"`
	CollectorURL string            `json:"collector_url"`
	Headers      map[string]string `json:"headers"`
	TraceType    TraceType         `json:"trace_type"`
	Protocol     Protocol          `json:"protocol"`
	TLSCACert    string            `json:"tls_ca_cert"`
	Insecure     bool              `json:"insecure"` // Skip TLS when true; ignored if TLSCACert is set

	// Metrics push configuration
	MetricsEnabled         bool              `json:"metrics_enabled"`
	MetricsEndpoint        string            `json:"metrics_endpoint"`
	MetricsProtocol        Protocol          `json:"metrics_protocol"`
	MetricsTLSCACert       string            `json:"metrics_tls_ca_cert"`
	MetricsInsecure        bool              `json:"metrics_insecure"`
	MetricsPushInterval    int               `json:"metrics_push_interval"`    // seconds, default 15s
	MetricsExporterTimeout int               `json:"metrics_exporter_timeout"` // seconds, default 10s
	MetricsExtraLabels     map[string]string `json:"metrics_extra_labels"`     // optional
	MetricsHeaders         map[string]string `json:"metrics_headers"`

	// To be populated with the prometheus registry from telemetry plugin.
	PrometheusRegistry *prometheus.Registry `json:"-"`
}

// OtelPlugin is the plugin for OpenTelemetry.
// It implements the ObservabilityPlugin interface to receive completed traces
// from the tracing middleware and forward them to an OTEL collector.
// Also set up pushing metrics to OTEL collector if configured.
type OtelPlugin struct {
	ctx    context.Context
	cancel context.CancelFunc

	serviceName string
	headers     map[string]string
	traceType   TraceType
	protocol    Protocol

	bifrostVersion string

	attributesFromEnvironment []*commonpb.KeyValue

	client OtelClient

	pricingManager *modelcatalog.ModelCatalog

	meterProvider *metric.MeterProvider
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
	// Trace client is mandatory when the plugin is enabled.
	// The config should already be validated with defaults set.
	traceClient, err := initOTELTraceExportClient(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init OTEL trace export client")
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
	// Preparing the plugin
	p := &OtelPlugin{
		serviceName:               config.ServiceName,
		pricingManager:            pricingManager,
		bifrostVersion:            bifrostVersion,
		attributesFromEnvironment: attributesFromEnvironment,
		client:                    traceClient,
	}
	p.ctx, p.cancel = context.WithCancel(ctx)

	// Initialize metrics exporter if enabled
	if config.MetricsEnabled {
		provider, err := initOTELMeterProvider(p.ctx, config.ServiceName, config)
		if err != nil {
			// Clean up trace client if metrics exporter fails
			if p.client != nil {
				p.client.Close()
			}
			return nil, fmt.Errorf("failed to initialize metrics exporter: %w", err)
		}
		p.meterProvider = provider
		logger.Info("OTEL metrics push enabled, pushing to %s every %d seconds", config.MetricsEndpoint, config.MetricsPushInterval)
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

// ValidateConfig validates values of PluginConfig and set up defaults where needed.
func ValidateConfig(config *Config) (*Config, error) {
	// Validating fields
	if config.ServiceName == "" {
		config.ServiceName = "bifrost"
	}
	if config.CollectorURL == "" {
		return nil, fmt.Errorf("trace collector url is required")
	}
	if config.TraceType == "" {
		return nil, fmt.Errorf("trace type is required")
	}
	if config.Protocol == "" {
		return nil, fmt.Errorf("trace export protocol is required")
	}
	if config.MetricsEnabled {
		// Prometheus registry should have already been created by telemetry plugin
		if config.PrometheusRegistry == nil {
			return nil, fmt.Errorf("prometheus registry is not provided")
		}
		if config.MetricsEndpoint == "" {
			return nil, fmt.Errorf("OTEL metrics collector endpoint is required")
		}
		// Some defaults
		if config.MetricsPushInterval == 0 {
			config.MetricsPushInterval = 15 // default 15 seconds
		} else if config.MetricsPushInterval > 300 {
			return nil, fmt.Errorf("metrics_push_interval must be between 1 and 300 seconds, got %d", config.MetricsPushInterval)
		}
		if config.MetricsExporterTimeout == 0 {
			config.MetricsExporterTimeout = 10 // default 10 seconds
		} else if config.MetricsExporterTimeout > 60 {
			return nil, fmt.Errorf("metrics_exporter_timeout must be between 1 and 60 seconds, got %d", config.MetricsExporterTimeout)
		}
	}
	return config, nil
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

	return nil
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	if p.cancel != nil {
		p.cancel()
	}

	// Shutdown metrics exporter first
	if p.meterProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.meterProvider.Shutdown(ctx); err != nil {
			logger.Error("failed to shutdown metrics exporter: %v", err)
		}
	}
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// Compile-time check that OtelPlugin implements ObservabilityPlugin
var _ schemas.ObservabilityPlugin = (*OtelPlugin)(nil)
