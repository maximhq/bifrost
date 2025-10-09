// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/pricing"
	"github.com/maximhq/bifrost/framework/streaming"
)

// logger is the logger for the OTEL plugin
var logger schemas.Logger

// ContextKey is a custom type for context keys to prevent collisions
type ContextKey string

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
	ctx    context.Context
	cancel context.CancelFunc

	url       string
	traceType TraceType
	protocol  Protocol

	ongoingSpans *TTLSyncMap

	client OtelClient

	pricingManager *pricing.PricingManager
	accumulator    *streaming.Accumulator // Accumulator for streaming chunks
}

// Init function for the OTEL plugin
func Init(ctx context.Context, config *Config, _logger schemas.Logger, pricingManager *pricing.PricingManager) (*OtelPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	logger = _logger
	var err error
	p := &OtelPlugin{
		url:            config.CollectorURL,
		traceType:      config.TraceType,
		ongoingSpans:   NewTTLSyncMap(20*time.Minute, 1*time.Minute),
		protocol:       config.Protocol,
		pricingManager: pricingManager,
		accumulator:    streaming.NewAccumulator(pricingManager, logger),
	}
	p.ctx, p.cancel = context.WithCancel(ctx)
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

// TransportInterceptor is not used for this plugin
func (p *OtelPlugin) TransportInterceptor(url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	return headers, body, nil
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
	traceIDValue := (*ctx).Value(schemas.BifrostContextKeyRequestID)
	if traceIDValue == nil {
		logger.Warn("trace id not found in context")
		return req, nil, nil
	}
	traceID, ok := traceIDValue.(string)
	if !ok {
		logger.Warn("trace id not found in context")
		return req, nil, nil
	}
	spanID := fmt.Sprintf("%s-root-span", traceID)
	createdTimestamp := time.Now()
	if bifrost.IsStreamRequestType(req.RequestType) {
		p.accumulator.CreateStreamAccumulator(traceID, createdTimestamp)
	}
	p.ongoingSpans.Set(traceID, createResourceSpan(traceID, spanID, time.Now(), req))
	return req, nil, nil
}

// PostHook function for the OTEL plugin
func (p *OtelPlugin) PostHook(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	traceIDValue := (*ctx).Value(schemas.BifrostContextKeyRequestID)
	if traceIDValue == nil {
		logger.Warn("trace id not found in context")
		return resp, bifrostErr, nil
	}
	traceID, ok := traceIDValue.(string)
	if !ok {
		logger.Warn("trace id not found in context")
		return resp, bifrostErr, nil
	}
	span, ok := p.ongoingSpans.Get(traceID)
	if !ok {
		logger.Warn("span not found in ongoing spans")
		return resp, bifrostErr, nil
	}
	requestType, _, _ := bifrost.GetRequestFields(resp, bifrostErr)
	if span, ok := span.(*ResourceSpan); ok {
		// We handle streaming responses differently, we will use the accumulator to process the response and then emit the final response
		if bifrost.IsStreamRequestType(requestType) {
			streamResponse, err := p.accumulator.ProcessStreamingResponse(ctx, resp, bifrostErr)
			if err != nil {
				logger.Error("failed to process streaming response: %v", err)
			}
			if streamResponse != nil && streamResponse.Type == streaming.StreamResponseTypeFinal {
				defer p.ongoingSpans.Delete(traceID)
				p.client.Emit(p.ctx, []*ResourceSpan{completeResourceSpan(span, time.Now(), streamResponse.ToBifrostResponse(), bifrostErr, p.pricingManager)})
			}
			return resp, bifrostErr, nil
		}
		defer p.ongoingSpans.Delete(traceID)
		p.client.Emit(p.ctx, []*ResourceSpan{completeResourceSpan(span, time.Now(), resp, bifrostErr, p.pricingManager)})
	}
	return resp, bifrostErr, nil
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.ongoingSpans != nil {
		p.ongoingSpans.Stop()
	}
	if p.accumulator != nil {
		p.accumulator.Cleanup()
	}
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}
