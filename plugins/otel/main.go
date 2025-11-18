// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
	ServiceName  string            `json:"service_name"`
	CollectorURL string            `json:"collector_url"`
	Headers      map[string]string `json:"headers"`
	TraceType    TraceType         `json:"trace_type"`
	Protocol     Protocol          `json:"protocol"`
}

// OtelPlugin is the plugin for OpenTelemetry
type OtelPlugin struct {
	ctx    context.Context
	cancel context.CancelFunc

	serviceName string
	url         string
	headers     map[string]string
	traceType   TraceType
	protocol    Protocol

	bifrostVersion string

	ongoingSpans *TTLSyncMap

	client OtelClient

	pricingManager *modelcatalog.ModelCatalog
	accumulator    *streaming.Accumulator // Accumulator for streaming chunks

	emitWg sync.WaitGroup // Track in-flight emissions
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
	// If headers are present , and any of them start with env., we will replace the value with the environment variable
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
	p := &OtelPlugin{
		serviceName:    config.ServiceName,
		url:            config.CollectorURL,
		traceType:      config.TraceType,
		headers:        config.Headers,
		ongoingSpans:   NewTTLSyncMap(20*time.Minute, 1*time.Minute),
		protocol:       config.Protocol,
		pricingManager: pricingManager,
		accumulator:    streaming.NewAccumulator(pricingManager, logger),
		emitWg:         sync.WaitGroup{},
		bifrostVersion: bifrostVersion,
	}
	p.ctx, p.cancel = context.WithCancel(ctx)
	if config.Protocol == ProtocolGRPC {
		p.client, err = NewOtelClientGRPC(config.CollectorURL, config.Headers)
		if err != nil {
			return nil, err
		}
	}
	if config.Protocol == ProtocolHTTP {
		p.client, err = NewOtelClientHTTP(config.CollectorURL, config.Headers)
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
func (p *OtelPlugin) TransportInterceptor(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
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
	p.ongoingSpans.Set(traceID, p.createResourceSpan(traceID, spanID, time.Now(), req))
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

	virtualKeyID := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-virtual-key-id"))
	virtualKeyName := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-virtual-key-name"))

	selectedKeyID := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKeySelectedKeyID)
	selectedKeyName := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKeySelectedKeyName)

	numberOfRetries := bifrost.GetIntFromContext(*ctx, schemas.BifrostContextKeyNumberOfRetries)
	fallbackIndex := bifrost.GetIntFromContext(*ctx, schemas.BifrostContextKeyFallbackIndex)

	teamID := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-team-id"))
	teamName := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-team-name"))
	customerID := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-customer-id"))
	customerName := bifrost.GetStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-customer-name"))

	// Track every PostHook emission, stream and non-stream.
	p.emitWg.Add(1)
	go func() {
		defer p.emitWg.Done()
		span, ok := p.ongoingSpans.Get(traceID)
		if !ok {
			logger.Warn("span not found in ongoing spans")
			return
		}
		requestType, _, _ := bifrost.GetResponseFields(resp, bifrostErr)
		if span, ok := span.(*ResourceSpan); ok {
			// We handle streaming responses differently, we will use the accumulator to process the response and then emit the final response
			if bifrost.IsStreamRequestType(requestType) {
				streamResponse, err := p.accumulator.ProcessStreamingResponse(ctx, resp, bifrostErr)
				if err != nil {
					logger.Debug("failed to process streaming response: %v", err)
				}
				if streamResponse != nil && streamResponse.Type == streaming.StreamResponseTypeFinal {
					defer p.ongoingSpans.Delete(traceID)
					if err := p.client.Emit(p.ctx, []*ResourceSpan{completeResourceSpan(
						span,
						time.Now(),
						streamResponse.ToBifrostResponse(),
						bifrostErr,
						p.pricingManager,
						virtualKeyID,
						virtualKeyName,
						selectedKeyID,
						selectedKeyName,
						numberOfRetries,
						fallbackIndex,
						teamID,
						teamName,
						customerID,
						customerName,
					)}); err != nil {
						logger.Error("failed to emit response span for request %s: %v", traceID, err)
					}
				}
				return
			}
			defer p.ongoingSpans.Delete(traceID)
			rs := completeResourceSpan(
				span,
				time.Now(),
				resp,
				bifrostErr,
				p.pricingManager,
				virtualKeyID,
				virtualKeyName,
				selectedKeyID,
				selectedKeyName,
				numberOfRetries,
				fallbackIndex,
				teamID,
				teamName,
				customerID,
				customerName,
			)
			if err := p.client.Emit(p.ctx, []*ResourceSpan{rs}); err != nil {
				logger.Error("failed to emit response span for request %s: %v", traceID, err)
			}
		}
	}()
	return resp, bifrostErr, nil
}

// Cleanup function for the OTEL plugin
func (p *OtelPlugin) Cleanup() error {
	p.emitWg.Wait()
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
