// Package telemetry provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. It includes middleware for HTTP request tracking
// and a plugin for tracking upstream provider metrics.
package telemetry

import (
	"context"
	"log"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/pricing"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	PluginName = "telemetry"
)

// ContextKey is a custom type for prometheus context keys to prevent collisions
type ContextKey string

const (
	startTimeKey ContextKey = "bf-prom-start-time"
)

// PrometheusPlugin implements the schemas.Plugin interface for Prometheus metrics.
// It tracks metrics for upstream provider requests, including:
//   - Total number of requests
//   - Request latency
//   - Error counts
type PrometheusPlugin struct {
	pricingManager *pricing.PricingManager

	// Metrics are defined using promauto for automatic registration
	UpstreamRequestsTotal   *prometheus.CounterVec
	UpstreamLatency         *prometheus.HistogramVec
	SuccessRequestsTotal    *prometheus.CounterVec
	ErrorRequestsTotal      *prometheus.CounterVec
	InputTokensTotal        *prometheus.CounterVec
	OutputTokensTotal       *prometheus.CounterVec
	CacheHitsTotal          *prometheus.CounterVec
	CostTotal               *prometheus.CounterVec
	StreamInterTokenLatency *prometheus.HistogramVec
	StreamFirstTokenLatency *prometheus.HistogramVec
}

// Init creates a new PrometheusPlugin with initialized metrics.
func Init(pricingManager *pricing.PricingManager, logger schemas.Logger) (*PrometheusPlugin, error) {
	if pricingManager == nil {
		logger.Warn("telemetry plugin requires pricing manager to calculate cost, all cost calculations will be skipped.")
	}

	return &PrometheusPlugin{
		pricingManager:          pricingManager,
		UpstreamRequestsTotal:   bifrostUpstreamRequestsTotal,
		UpstreamLatency:         bifrostUpstreamLatencySeconds,
		SuccessRequestsTotal:    bifrostSuccessRequestsTotal,
		ErrorRequestsTotal:      bifrostErrorRequestsTotal,
		InputTokensTotal:        bifrostInputTokensTotal,
		OutputTokensTotal:       bifrostOutputTokensTotal,
		CacheHitsTotal:          bifrostCacheHitsTotal,
		CostTotal:               bifrostCostTotal,
		StreamInterTokenLatency: bifrostStreamInterTokenLatencySeconds,
		StreamFirstTokenLatency: bifrostStreamFirstTokenLatencySeconds,
	}, nil
}

// GetName returns the name of the plugin.
func (p *PrometheusPlugin) GetName() string {
	return PluginName
}

// TransportInterceptor is not used for this plugin
func (p *PrometheusPlugin) TransportInterceptor(url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	return headers, body, nil
}

// PreHook records the start time of the request in the context.
// This time is used later in PostHook to calculate request duration.
func (p *PrometheusPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	*ctx = context.WithValue(*ctx, startTimeKey, time.Now())

	return req, nil, nil
}

// PostHook calculates duration and records upstream metrics for successful requests.
// It records:
//   - Request latency
//   - Total request count
func (p *PrometheusPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestType, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	startTime, ok := (*ctx).Value(startTimeKey).(time.Time)
	if !ok {
		log.Println("Warning: startTime not found in context for Prometheus PostHook")
		return result, bifrostErr, nil
	}

	// Calculate cost and record metrics in a separate goroutine to avoid blocking the main thread
	go func() {
		labelValues := map[string]string{
			"provider": string(provider),
			"model":    model,
			"method":   string(requestType),
		}

		// Get all prometheus labels from context
		for _, key := range customLabels {
			if value := (*ctx).Value(ContextKey(key)); value != nil {
				if strValue, ok := value.(string); ok {
					labelValues[key] = strValue
				}
			}
		}

		// Get label values in the correct order (cache_type will be handled separately for cache hits)
		promLabelValues := getPrometheusLabelValues(append([]string{"provider", "model", "method"}, customLabels...), labelValues)

		// For streaming requests, handle per-token metrics for intermediate chunks
		if bifrost.IsStreamRequestType(requestType) {
			// Determine if this is the final chunk
			streamEndIndicatorValue := (*ctx).Value(schemas.BifrostContextKeyStreamEndIndicator)
			isFinalChunk, ok := streamEndIndicatorValue.(bool)

			// For intermediate chunks, record per-token metrics and exit.
			// The final chunk will fall through to record full request metrics.
			if !ok || !isFinalChunk {
				// Record metrics for the first token
				if result != nil {
					extraFields := result.GetExtraFields()
					if extraFields.ChunkIndex == 0 {
						p.StreamFirstTokenLatency.WithLabelValues(promLabelValues...).Observe(float64(extraFields.Latency) / 1000.0)
					} else {
						p.StreamInterTokenLatency.WithLabelValues(promLabelValues...).Observe(float64(extraFields.Latency) / 1000.0)
					}
				}
				return // Exit goroutine for intermediate chunks
			}
		}

		cost := 0.0
		if p.pricingManager != nil && result != nil {
			cost = p.pricingManager.CalculateCostWithCacheDebug(result)
		}

		duration := time.Since(startTime).Seconds()
		p.UpstreamLatency.WithLabelValues(promLabelValues...).Observe(duration)
		p.UpstreamRequestsTotal.WithLabelValues(promLabelValues...).Inc()

		// Record cost using the dedicated cost counter
		if cost > 0 {
			p.CostTotal.WithLabelValues(promLabelValues...).Add(cost)
		}

		// Record error and success counts
		if bifrostErr != nil {
			// Add reason to label values (create new slice to avoid modifying original)
			errorPromLabelValues := make([]string, 0, len(promLabelValues)+1)
			errorPromLabelValues = append(errorPromLabelValues, promLabelValues[:3]...)   // provider, model, method
			errorPromLabelValues = append(errorPromLabelValues, bifrostErr.Error.Message) // reason
			errorPromLabelValues = append(errorPromLabelValues, promLabelValues[3:]...)   // then custom labels

			p.ErrorRequestsTotal.WithLabelValues(errorPromLabelValues...).Inc()
		} else {
			p.SuccessRequestsTotal.WithLabelValues(promLabelValues...).Inc()
		}

		if result != nil {
			// Record input and output tokens
			var inputTokens, outputTokens int

			switch {
			case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
				inputTokens = result.TextCompletionResponse.Usage.PromptTokens
				outputTokens = result.TextCompletionResponse.Usage.CompletionTokens
			case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
				inputTokens = result.ChatResponse.Usage.PromptTokens
				outputTokens = result.ChatResponse.Usage.CompletionTokens
			case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
				inputTokens = result.ResponsesResponse.Usage.InputTokens
				outputTokens = result.ResponsesResponse.Usage.OutputTokens
			case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
				inputTokens = result.ResponsesStreamResponse.Response.Usage.InputTokens
				outputTokens = result.ResponsesStreamResponse.Response.Usage.OutputTokens
			case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
				inputTokens = result.EmbeddingResponse.Usage.PromptTokens
				outputTokens = result.EmbeddingResponse.Usage.CompletionTokens
			case result.SpeechStreamResponse != nil && result.SpeechStreamResponse.Usage != nil:
				inputTokens = result.SpeechStreamResponse.Usage.InputTokens
				outputTokens = result.SpeechStreamResponse.Usage.OutputTokens
			case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil:
				if result.TranscriptionResponse.Usage.InputTokens != nil {
					inputTokens = *result.TranscriptionResponse.Usage.InputTokens
				}
				if result.TranscriptionResponse.Usage.OutputTokens != nil {
					outputTokens = *result.TranscriptionResponse.Usage.OutputTokens
				}
			case result.TranscriptionStreamResponse != nil && result.TranscriptionStreamResponse.Usage != nil:
				if result.TranscriptionStreamResponse.Usage.InputTokens != nil {
					inputTokens = *result.TranscriptionStreamResponse.Usage.InputTokens
				}
				if result.TranscriptionStreamResponse.Usage.OutputTokens != nil {
					outputTokens = *result.TranscriptionStreamResponse.Usage.OutputTokens
				}
			}

			p.InputTokensTotal.WithLabelValues(promLabelValues...).Add(float64(inputTokens))
			p.OutputTokensTotal.WithLabelValues(promLabelValues...).Add(float64(outputTokens))

			// Record cache hits with cache type
			extraFields := result.GetExtraFields()
			if extraFields.CacheDebug != nil && extraFields.CacheDebug.CacheHit {
				cacheType := "unknown"
				if extraFields.CacheDebug.HitType != nil {
					cacheType = *extraFields.CacheDebug.HitType
				}

				// Add cache_type to label values (create new slice to avoid modifying original)
				cacheHitLabelValues := make([]string, 0, len(promLabelValues)+1)
				cacheHitLabelValues = append(cacheHitLabelValues, promLabelValues[:3]...) // provider, model, method
				cacheHitLabelValues = append(cacheHitLabelValues, cacheType)              // cache_type
				cacheHitLabelValues = append(cacheHitLabelValues, promLabelValues[3:]...) // then custom labels

				p.CacheHitsTotal.WithLabelValues(cacheHitLabelValues...).Inc()
			}
		}
	}()

	return result, bifrostErr, nil
}

func (p *PrometheusPlugin) Cleanup() error {
	return nil
}
