// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// logger is the logger for the OTEL plugin
var logger schemas.Logger

// ContextKey is a custom type for context keys to prevent collisions
type ContextKey string

// objectPool is the pool for the OTEL plugin
var objectPool *ObjectPool

// Context keys for otel plugin
const (
	TraceIDKey ContextKey = "plugin-otel-trace-id"
	SpanIDKey  ContextKey = "plugin-otel-span-id"
)

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
	CollectorURL string    `json:"collector_url"`
	TraceType    TraceType `json:"trace_type"`
	Protocol     Protocol  `json:"protocol"`
}

// OtelPlugin is the plugin for OpenTelemetry
type OtelPlugin struct {
	url       string
	traceType TraceType
	protocol  Protocol

	client OtelClient
}

// Init function for the OTEL plugin
func Init(ctx context.Context, config *Config, _logger schemas.Logger) (*OtelPlugin, error) {
	logger = _logger
	var err error
	objectPool = NewObjectPool(2000)
	p := &OtelPlugin{
		url:       config.CollectorURL,
		traceType: config.TraceType,
		protocol:  config.Protocol,
	}
	if config.Protocol == ProtocolGRPC {
		p.client, err = NewOtelClientGRPC(config.CollectorURL)
		if err != nil {
			return nil, err
		}
	}
	if config.Protocol == ProtocolHTTP {
		p.client, err = NewOtelClientHTTP(config.CollectorURL)
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

// PreHook function for the OTEL plugin
func (p *OtelPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if p.client == nil {
		logger.Warn("otel client is not initialized")
		return req, nil, nil
	}
	traceID := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	spanID := fmt.Sprintf("%s-root-span", traceID)
	// We just fire on a go routine to avoid blocking the request
	go p.client.Emit(*ctx, []*ResourceSpan{requestToResourceSpan(traceID, spanID, time.Now(), req)})
	return req, nil, nil
}

// PostHook function for the OTEL plugin
func (p *OtelPlugin) PostHook(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		return resp, bifrostErr, nil
	}
	traceID := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	spanID := fmt.Sprintf("%s-root-span", traceID)
	childSpanID := uuid.New().String()

	go p.client.Emit(*ctx, []*ResourceSpan{responseToResourceSpan(traceID, spanID, childSpanID, time.Now(), resp, bifrostErr)})
	return resp, nil, nil
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	return nil
}
