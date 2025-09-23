// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// ContextKey is a custom type for context keys to prevent collisions
type ContextKey string

// objectPool is the pool for the OTEL plugin
var objectPool *ObjectPool

// Context keys for otel plugin
const (
	TraceIDKey      ContextKey = "plugin-otel-trace-id"
	SpanIDKey       ContextKey = "plugin-otel-span-id"
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
	CollectorURL string
	TraceType    TraceType
	Protocol     Protocol
}

// OtelPlugin is the plugin for OpenTelemetry
type OtelPlugin struct {
	url       string
	traceType TraceType
	protocol  Protocol

	client OtelClient

	logger schemas.Logger
}

// Init function for the OTEL plugin
func Init(ctx context.Context, config *Config, logger schemas.Logger) (*OtelPlugin, error) {
	var err error
	objectPool = NewObjectPool(2000)
	p := &OtelPlugin{
		url:       config.CollectorURL,
		traceType: config.TraceType,
		protocol:  config.Protocol,
		logger:    logger,
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
	return p, nil
}

// GetName function for the OTEL plugin
func (p *OtelPlugin) GetName() string {
	return PluginName
}

// PreHook function for the OTEL plugin
func (p *OtelPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if p.client == nil {
		p.logger.Warn("otel client is not initialized")
		return req, nil, nil
	}
	traceID := uuid.New().String()
	spanID := uuid.New().String()
	*ctx = context.WithValue(*ctx, TraceIDKey, traceID)
	*ctx = context.WithValue(*ctx, SpanIDKey, spanID)
	// We just fire on a go routine to avoid blocking the request
	go p.client.Emit(*ctx, []*ResourceSpan{requestToResourceSpan(traceID, spanID, time.Now(), req)})
	return req, nil, nil
}

// PostHook function for the OTEL plugin
func (p *OtelPlugin) PostHook(ctx *context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		return resp, bifrostErr, nil
	}
	traceID, ok := (*ctx).Value(TraceIDKey).(string)
	if !ok {
		p.logger.Warn("otel trace id is not found in context")
		return resp, nil, nil
	}
	spanID, ok := (*ctx).Value(SpanIDKey).(string)
	if !ok {
		p.logger.Warn("otel span id is not found in context")
		return resp, nil, nil
	}
	childSpanID := uuid.New().String()
	go p.client.Emit(*ctx, []*ResourceSpan{responseToResourceSpan(traceID, spanID, childSpanID, time.Now(), resp)})
	return resp, nil, nil
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	return nil
}
