// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

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

// ProfileConfig represents the configuration for a single OTEL collector profile
type ProfileConfig struct {
	Name         string            `json:"name"`
	Enabled      *bool             `json:"enabled,omitempty"` // nil defaults to true
	ServiceName  string            `json:"service_name"`
	CollectorURL string            `json:"collector_url"`
	Headers      map[string]string `json:"headers"`
	TraceType    TraceType         `json:"trace_type"`
	Protocol     Protocol          `json:"protocol"`
	TLSCACert    string            `json:"tls_ca_cert"`
	Insecure     bool              `json:"insecure"` // Skip TLS when true; ignored if TLSCACert is set

	// Metrics push configuration
	MetricsEnabled      bool   `json:"metrics_enabled"`
	MetricsEndpoint     string `json:"metrics_endpoint"`
	MetricsPushInterval int    `json:"metrics_push_interval"` // in seconds, default 15
}

// Config represents the configuration for the OTEL plugin with multiple profiles
type Config struct {
	Profiles []ProfileConfig `json:"profiles"`
}

// otelProfile holds runtime state for a single OTEL collector profile
type otelProfile struct {
	name        string
	serviceName string
	traceType   TraceType

	bifrostVersion string

	attributesFromEnvironment []*commonpb.KeyValue

	client OtelClient

	pricingManager *modelcatalog.ModelCatalog

	// Metrics push support
	metricsExporter *MetricsExporter
}

// OtelPlugin is the plugin for OpenTelemetry.
// It implements the ObservabilityPlugin interface to receive completed traces
// from the tracing middleware and forward them to OTEL collectors.
type OtelPlugin struct {
	ctx    context.Context
	cancel context.CancelFunc

	profiles []*otelProfile
}

// initProfile initializes a single otelProfile from a ProfileConfig
func initProfile(ctx context.Context, config *ProfileConfig, pricingManager *modelcatalog.ModelCatalog, bifrostVersion string) (*otelProfile, error) {
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
		for attribute := range strings.SplitSeq(attributes, ",") {
			attributeParts := strings.Split(strings.TrimSpace(attribute), "=")
			if len(attributeParts) == 2 {
				attributesFromEnvironment = append(attributesFromEnvironment, kvStr(strings.TrimSpace(attributeParts[0]), strings.TrimSpace(attributeParts[1])))
			}
		}
	}

	profile := &otelProfile{
		name:                      config.Name,
		serviceName:               config.ServiceName,
		traceType:                 config.TraceType,
		pricingManager:            pricingManager,
		bifrostVersion:            bifrostVersion,
		attributesFromEnvironment: attributesFromEnvironment,
	}

	var err error
	if config.Protocol == ProtocolGRPC {
		profile.client, err = NewOtelClientGRPC(config.CollectorURL, config.Headers, config.TLSCACert, config.Insecure)
		if err != nil {
			return nil, fmt.Errorf("profile %q: failed to create gRPC client: %w", config.Name, err)
		}
	}
	if config.Protocol == ProtocolHTTP {
		profile.client, err = NewOtelClientHTTP(config.CollectorURL, config.Headers, config.TLSCACert, config.Insecure)
		if err != nil {
			return nil, fmt.Errorf("profile %q: failed to create HTTP client: %w", config.Name, err)
		}
	}
	if profile.client == nil {
		return nil, fmt.Errorf("profile %q: otel client is not initialized. invalid protocol type", config.Name)
	}

	// Initialize metrics exporter if enabled
	if config.MetricsEnabled {
		if config.MetricsEndpoint == "" {
			profile.client.Close()
			return nil, fmt.Errorf("profile %q: metrics_endpoint is required when metrics_enabled is true", config.Name)
		}
		pushInterval := config.MetricsPushInterval
		if pushInterval <= 0 {
			pushInterval = 15 // default 15 seconds
		} else if pushInterval > 300 {
			profile.client.Close()
			return nil, fmt.Errorf("profile %q: metrics_push_interval must be between 1 and 300 seconds, got %d", config.Name, pushInterval)
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
		profile.metricsExporter, err = NewMetricsExporter(ctx, metricsConfig)
		if err != nil {
			profile.client.Close()
			return nil, fmt.Errorf("profile %q: failed to initialize metrics exporter: %w", config.Name, err)
		}
		logger.Info("OTEL metrics push enabled for profile %q, pushing to %s every %d seconds", config.Name, config.MetricsEndpoint, pushInterval)
	}

	return profile, nil
}

// cleanup shuts down a single profile's resources
func (p *otelProfile) cleanup() error {
	var errs []error
	if p.metricsExporter != nil {
		if err := p.metricsExporter.Shutdown(context.Background()); err != nil {
			errs = append(errs, fmt.Errorf("profile %q: failed to shutdown metrics exporter: %w", p.name, err))
		}
	}
	if p.client != nil {
		if err := p.client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("profile %q: failed to close client: %w", p.name, err))
		}
	}
	return errors.Join(errs...)
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
	if len(config.Profiles) == 0 {
		logger.Warn("otel plugin has no profiles configured")
	}

	p := &OtelPlugin{}
	p.ctx, p.cancel = context.WithCancel(ctx)

	profiles := make([]*otelProfile, 0, len(config.Profiles))
	for i := range config.Profiles {
		// Skip disabled profiles
		if config.Profiles[i].Enabled != nil && !*config.Profiles[i].Enabled {
			logger.Info("OTEL profile %q is disabled, skipping", config.Profiles[i].Name)
			continue
		}
		profile, err := initProfile(p.ctx, &config.Profiles[i], pricingManager, bifrostVersion)
		if err != nil {
			// Clean up already initialized profiles
			for _, initialized := range profiles {
				initialized.cleanup()
			}
			p.cancel()
			return nil, err
		}
		profiles = append(profiles, profile)
		logger.Info("OTEL profile %q initialized (collector: %s, protocol: %s)", config.Profiles[i].Name, config.Profiles[i].CollectorURL, config.Profiles[i].Protocol)
	}
	p.profiles = profiles

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
	// Try to marshal from various input types to Config
	var otelConfig Config
	configBytes, err := marshalToBytes(config)
	if err != nil {
		return nil, err
	}
	if err := unmarshalFromBytes(configBytes, &otelConfig); err != nil {
		return nil, err
	}

	for i, profile := range otelConfig.Profiles {
		// Skip validation for disabled profiles
		if profile.Enabled != nil && !*profile.Enabled {
			continue
		}
		if profile.CollectorURL == "" {
			return nil, fmt.Errorf("profile[%d] (%s): collector url is required", i, profile.Name)
		}
		if profile.TraceType == "" {
			return nil, fmt.Errorf("profile[%d] (%s): trace type is required", i, profile.Name)
		}
		if profile.Protocol == "" {
			return nil, fmt.Errorf("profile[%d] (%s): protocol is required", i, profile.Name)
		}
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

// Inject receives a completed trace and sends it to all OTEL collector profiles.
// Implements schemas.ObservabilityPlugin interface.
// This method is called asynchronously by TracingMiddleware after the response
// has been written to the client.
func (p *OtelPlugin) Inject(ctx context.Context, trace *schemas.Trace) error {
	if trace == nil {
		return nil
	}

	errs := make([]error, len(p.profiles))
	var wg sync.WaitGroup
	for i, profile := range p.profiles {
		if profile.client == nil {
			logger.Warn("otel client is not initialized for profile %q", profile.name)
			continue
		}
		wg.Add(1)
		go func(idx int, prof *otelProfile) {
			defer wg.Done()
			resourceSpan := prof.convertTraceToResourceSpan(trace)
			if err := prof.client.Emit(ctx, []*ResourceSpan{resourceSpan}); err != nil {
				logger.Error("failed to emit trace %s to profile %q: %v", trace.TraceID, prof.name, err)
				errs[idx] = err
			}
		}(i, profile)
	}
	wg.Wait()

	return errors.Join(errs...)
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	if p.cancel != nil {
		p.cancel()
	}
	var errs []error
	for _, profile := range p.profiles {
		if err := profile.cleanup(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GetMetricsExporter returns the first profile's metrics exporter for external use
func (p *OtelPlugin) GetMetricsExporter() *MetricsExporter {
	for _, profile := range p.profiles {
		if profile.metricsExporter != nil {
			return profile.metricsExporter
		}
	}
	return nil
}

// marshalToBytes converts any config input to JSON bytes
func marshalToBytes(config any) ([]byte, error) {
	switch v := config.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	default:
		return sonic.Marshal(v)
	}
}

// unmarshalFromBytes unmarshals JSON bytes into the target
func unmarshalFromBytes(data []byte, target any) error {
	return sonic.Unmarshal(data, target)
}

// Compile-time check that OtelPlugin implements ObservabilityPlugin
var _ schemas.ObservabilityPlugin = (*OtelPlugin)(nil)
