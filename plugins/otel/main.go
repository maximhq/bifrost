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

	client OtelClient

	pricingManager *modelcatalog.ModelCatalog
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
	}
	p.ctx, p.cancel = context.WithCancel(ctx)
	if config.Protocol == ProtocolGRPC {
		p.client, err = NewOtelClientGRPC(config.CollectorURL, config.Headers, config.TLSCACert)
		if err != nil {
			return nil, err
		}
	}
	if config.Protocol == ProtocolHTTP {
		p.client, err = NewOtelClientHTTP(config.CollectorURL, config.Headers, config.TLSCACert)
		if err != nil {
			return nil, err
		}
	}
	if p.client == nil {
		return nil, fmt.Errorf("otel client is not initialized. invalid protocol type")
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

// PreHook is a no-op - tracing is handled via the Inject method.
// The OTEL plugin receives completed traces from TracingMiddleware.
func (p *OtelPlugin) PreHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return req, nil, nil
}

// PostHook is a no-op - tracing is handled via the Inject method.
// The OTEL plugin receives completed traces from TracingMiddleware.
func (p *OtelPlugin) PostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
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
	if p.client == nil {
		logger.Warn("otel client is not initialized")
		return nil
	}

	// Convert schemas.Trace to OTEL ResourceSpan
	resourceSpan := p.convertTraceToResourceSpan(trace)

	// Emit to collector
	if err := p.client.Emit(ctx, []*ResourceSpan{resourceSpan}); err != nil {
		logger.Error("failed to emit trace %s: %v", trace.TraceID, err)
		return err
	}

	return nil
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// Compile-time check that OtelPlugin implements ObservabilityPlugin
var _ schemas.ObservabilityPlugin = (*OtelPlugin)(nil)
