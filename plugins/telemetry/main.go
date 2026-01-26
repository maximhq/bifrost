// Package telemetry provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. It includes middleware for HTTP request tracking
// and a plugin for tracking upstream provider metrics.
package telemetry

import (
	"fmt"
	"strconv"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/valyala/fasthttp"
)

const (
	PluginName = "telemetry"
)

const (
	startTimeKey schemas.BifrostContextKey = "bf-prom-start-time"
)

// PrometheusPlugin implements the schemas.Plugin interface for Prometheus metrics.
// It tracks metrics for upstream provider requests, including:
//   - Total number of requests
//   - Request latency
//   - Error counts
type PrometheusPlugin struct {
	pricingManager *modelcatalog.ModelCatalog
	registry       *prometheus.Registry

	logger schemas.Logger

	// Built-in collectors registered by this plugin
	GoCollector      prometheus.Collector
	ProcessCollector prometheus.Collector

	// Metrics are defined using promauto for automatic registration
	HTTPRequestsTotal              *prometheus.CounterVec
	HTTPRequestDuration            *prometheus.HistogramVec
	HTTPRequestSizeBytes           *prometheus.HistogramVec
	HTTPResponseSizeBytes          *prometheus.HistogramVec
	UpstreamRequestsTotal          *prometheus.CounterVec
	UpstreamLatencySeconds         *prometheus.HistogramVec
	SuccessRequestsTotal           *prometheus.CounterVec
	ErrorRequestsTotal             *prometheus.CounterVec
	InputTokensTotal               *prometheus.CounterVec
	OutputTokensTotal              *prometheus.CounterVec
	CacheHitsTotal                 *prometheus.CounterVec
	CostTotal                      *prometheus.CounterVec
	StreamInterTokenLatencySeconds *prometheus.HistogramVec
	StreamFirstTokenLatencySeconds *prometheus.HistogramVec
	customLabels                   []string

	defaultHTTPLabels    []string
	defaultBifrostLabels []string
}

type Config struct {
	CustomLabels []string `json:"custom_labels"`
	Registry     *prometheus.Registry
}

// Init creates a new PrometheusPlugin with initialized metrics.
func Init(config *Config, pricingManager *modelcatalog.ModelCatalog, logger schemas.Logger) (*PrometheusPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	if pricingManager == nil {
		logger.Warn("telemetry plugin requires model catalog to calculate cost, all cost calculations will be skipped.")
	}

	registry := config.Registry
	// If config has no registry, create a new one
	if registry == nil {
		registry = prometheus.NewRegistry()
	}

	// Create collectors and store references for cleanup
	goCollector := collectors.NewGoCollector()
	if err := registry.Register(goCollector); err != nil {
		return nil, fmt.Errorf("failed to register Go collector: %v", err)
	}

	processCollector := collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})
	if err := registry.Register(processCollector); err != nil {
		return nil, fmt.Errorf("failed to register process collector: %v", err)
	}

	defaultHTTPLabels := []string{"path", "method", "status"}
	defaultBifrostLabels := []string{
		"provider",
		"model",
		"method",
		"virtual_key_id",
		"virtual_key_name",
		"selected_key_id",
		"selected_key_name",
		"number_of_retries",
		"fallback_index",
		"team_id",
		"team_name",
		"customer_id",
		"customer_name",
	}

	var filteredCustomLabels []string
	if len(config.CustomLabels) > 0 {
		for _, label := range config.CustomLabels {
			if !containsLabel(defaultBifrostLabels, label) && !containsLabel(defaultHTTPLabels, label) {
				filteredCustomLabels = append(filteredCustomLabels, label)
			} else {
				logger.Info("custom label %s is already a default label, it will be ignored", label)
			}
		}
	}

	factory := promauto.With(registry)

	// Upstream LLM latency buckets - extended range for AI model inference times
	upstreamLatencyBuckets := []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 15, 30, 45, 60, 90} // in seconds

	httpRequestsTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		append(defaultHTTPLabels, filteredCustomLabels...),
	)

	// httpRequestDuration tracks the duration of HTTP requests
	httpRequestDuration := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests.",
			Buckets: prometheus.DefBuckets,
		},
		append(defaultHTTPLabels, filteredCustomLabels...),
	)

	// httpRequestSizeBytes tracks the size of incoming HTTP requests
	httpRequestSizeBytes := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_size_bytes",
			Help:    "Size of HTTP requests.",
			Buckets: prometheus.ExponentialBuckets(100, 10, 8), // 100B to 1GB
		},
		append(defaultHTTPLabels, filteredCustomLabels...),
	)

	// httpResponseSizeBytes tracks the size of outgoing HTTP responses
	httpResponseSizeBytes := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_response_size_bytes",
			Help:    "Size of HTTP responses.",
			Buckets: prometheus.ExponentialBuckets(100, 10, 8), // 100B to 1GB
		},
		append(defaultHTTPLabels, filteredCustomLabels...),
	)

	// Bifrost Upstream Metrics
	bifrostUpstreamRequestsTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_upstream_requests_total",
			Help: "Total number of requests forwarded to upstream providers by Bifrost.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostUpstreamLatencySeconds := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bifrost_upstream_latency_seconds",
			Help:    "Latency of requests forwarded to upstream providers by Bifrost.",
			Buckets: upstreamLatencyBuckets, // Extended range for AI model inference times
		},
		append(append(defaultBifrostLabels, "is_success"), filteredCustomLabels...),
	)

	bifrostSuccessRequestsTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_success_requests_total",
			Help: "Total number of successful requests forwarded to upstream providers by Bifrost.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostErrorRequestsTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_error_requests_total",
			Help: "Total number of error requests forwarded to upstream providers by Bifrost.",
		},
		append(append(defaultBifrostLabels, "reason"), filteredCustomLabels...),
	)

	bifrostInputTokensTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_input_tokens_total",
			Help: "Total number of input tokens forwarded to upstream providers by Bifrost.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostOutputTokensTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_output_tokens_total",
			Help: "Total number of output tokens forwarded to upstream providers by Bifrost.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostCacheHitsTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_cache_hits_total",
			Help: "Total number of cache hits forwarded to upstream providers by Bifrost, separated by cache type (direct/semantic).",
		},
		append(append(defaultBifrostLabels, "cache_type"), filteredCustomLabels...),
	)

	bifrostCostTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_cost_total",
			Help: "Total cost in USD for requests to upstream providers.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostStreamInterTokenLatencySeconds := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "bifrost_stream_inter_token_latency_seconds",
			Help: "Latency of the intermediate tokens of a stream response.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostStreamFirstTokenLatencySeconds := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "bifrost_stream_first_token_latency_seconds",
			Help: "Latency of the first token of a stream response.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	return &PrometheusPlugin{
		logger:                         logger,
		pricingManager:                 pricingManager,
		registry:                       registry,
		GoCollector:                    goCollector,
		ProcessCollector:               processCollector,
		HTTPRequestsTotal:              httpRequestsTotal,
		HTTPRequestDuration:            httpRequestDuration,
		HTTPRequestSizeBytes:           httpRequestSizeBytes,
		HTTPResponseSizeBytes:          httpResponseSizeBytes,
		UpstreamRequestsTotal:          bifrostUpstreamRequestsTotal,
		UpstreamLatencySeconds:         bifrostUpstreamLatencySeconds,
		SuccessRequestsTotal:           bifrostSuccessRequestsTotal,
		ErrorRequestsTotal:             bifrostErrorRequestsTotal,
		InputTokensTotal:               bifrostInputTokensTotal,
		OutputTokensTotal:              bifrostOutputTokensTotal,
		CacheHitsTotal:                 bifrostCacheHitsTotal,
		CostTotal:                      bifrostCostTotal,
		StreamInterTokenLatencySeconds: bifrostStreamInterTokenLatencySeconds,
		StreamFirstTokenLatencySeconds: bifrostStreamFirstTokenLatencySeconds,
		customLabels:                   filteredCustomLabels,
		defaultHTTPLabels:              defaultHTTPLabels,
		defaultBifrostLabels:           defaultBifrostLabels,
	}, nil
}

func (p *PrometheusPlugin) GetRegistry() *prometheus.Registry {
	return p.registry
}

// GetName returns the name of the plugin.
func (p *PrometheusPlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used for this plugin
func (p *PrometheusPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *PrometheusPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *PrometheusPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreHook records the start time of the request in the context.
// This time is used later in PostHook to calculate request duration.
func (p *PrometheusPlugin) PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	ctx.SetValue(startTimeKey, time.Now())
	return req, nil, nil
}

// PostHook calculates duration and records upstream metrics for successful requests.
// It records:
//   - Request latency
//   - Total request count
func (p *PrometheusPlugin) PostHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestType, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	startTime, ok := ctx.Value(startTimeKey).(time.Time)
	if !ok {
		p.logger.Warn("Warning: startTime not found in context for Prometheus PostHook")
		return result, bifrostErr, nil
	}

	virtualKeyID := getStringFromContext(ctx, schemas.BifrostContextKey("bf-governance-virtual-key-id"))
	virtualKeyName := getStringFromContext(ctx, schemas.BifrostContextKey("bf-governance-virtual-key-name"))

	selectedKeyID := getStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	selectedKeyName := getStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyName)

	numberOfRetries := getIntFromContext(ctx, schemas.BifrostContextKeyNumberOfRetries)
	fallbackIndex := getIntFromContext(ctx, schemas.BifrostContextKeyFallbackIndex)

	teamID := getStringFromContext(ctx, schemas.BifrostContextKey("bf-governance-team-id"))
	teamName := getStringFromContext(ctx, schemas.BifrostContextKey("bf-governance-team-name"))
	customerID := getStringFromContext(ctx, schemas.BifrostContextKey("bf-governance-customer-id"))
	customerName := getStringFromContext(ctx, schemas.BifrostContextKey("bf-governance-customer-name"))

	// Extract ALL context values BEFORE spawning the goroutine.
	labelValues := map[string]string{
		"provider":          string(provider),
		"model":             model,
		"method":            string(requestType),
		"virtual_key_id":    virtualKeyID,
		"virtual_key_name":  virtualKeyName,
		"selected_key_id":   selectedKeyID,
		"selected_key_name": selectedKeyName,
		"number_of_retries": strconv.Itoa(numberOfRetries),
		"fallback_index":    strconv.Itoa(fallbackIndex),
		"team_id":           teamID,
		"team_name":         teamName,
		"customer_id":       customerID,
		"customer_name":     customerName,
	}

	// Get all custom prometheus labels from context BEFORE the goroutine
	for _, key := range p.customLabels {
		if value := ctx.Value(schemas.BifrostContextKey(key)); value != nil {
			if strValue, ok := value.(string); ok {
				labelValues[key] = strValue
			}
		}
	}

	// Get label values in the correct order (cache_type will be handled separately for cache hits)
	promLabelValues := getPrometheusLabelValues(append(p.defaultBifrostLabels, p.customLabels...), labelValues)

	// Extract stream end indicator BEFORE the goroutine
	streamEndIndicatorValue := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator)
	isFinalChunk, hasFinalChunkIndicator := streamEndIndicatorValue.(bool)

	// Calculate cost and record metrics in a separate goroutine to avoid blocking the main thread
	go func() {
		// For streaming requests, handle per-token metrics for intermediate chunks
		if bifrost.IsStreamRequestType(requestType) {
			// For intermediate chunks, record per-token metrics and exit.
			// The final chunk will fall through to record full request metrics.
			if !hasFinalChunkIndicator || !isFinalChunk {
				// Record metrics for the first token
				if result != nil {
					extraFields := result.GetExtraFields()
					if extraFields.ChunkIndex == 0 {
						p.StreamFirstTokenLatencySeconds.WithLabelValues(promLabelValues...).Observe(float64(extraFields.Latency) / 1000.0)
					} else {
						p.StreamInterTokenLatencySeconds.WithLabelValues(promLabelValues...).Observe(float64(extraFields.Latency) / 1000.0)
					}
				}
				return // Exit goroutine for intermediate chunks
			}
		}

		cost := 0.0
		if p.pricingManager != nil && result != nil {
			cost = p.pricingManager.CalculateCostWithCacheDebug(result)
		}

		p.UpstreamRequestsTotal.WithLabelValues(promLabelValues...).Inc()

		// Record latency
		duration := time.Since(startTime).Seconds()
		latencyLabelValues := make([]string, 0, len(promLabelValues)+1)
		latencyLabelValues = append(latencyLabelValues, promLabelValues[:len(p.defaultBifrostLabels)]...) // all default labels
		latencyLabelValues = append(latencyLabelValues, strconv.FormatBool(bifrostErr == nil))            // is_success
		latencyLabelValues = append(latencyLabelValues, promLabelValues[len(p.defaultBifrostLabels):]...) // then custom labels
		p.UpstreamLatencySeconds.WithLabelValues(latencyLabelValues...).Observe(duration)

		// Record cost using the dedicated cost counter
		if cost > 0 {
			p.CostTotal.WithLabelValues(promLabelValues...).Add(cost)
		}

		// Record error and success counts
		if bifrostErr != nil {
			// Add reason to label values (create new slice to avoid modifying original)
			errorPromLabelValues := make([]string, 0, len(promLabelValues)+1)
			errorPromLabelValues = append(errorPromLabelValues, promLabelValues[:len(p.defaultBifrostLabels)]...) // all default labels
			errorPromLabelValues = append(errorPromLabelValues, bifrostErr.Error.Message)                         // reason
			errorPromLabelValues = append(errorPromLabelValues, promLabelValues[len(p.defaultBifrostLabels):]...) // then custom labels

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
				cacheHitLabelValues = append(cacheHitLabelValues, promLabelValues[:len(p.defaultBifrostLabels)]...) // all default labels
				cacheHitLabelValues = append(cacheHitLabelValues, cacheType)                                        // cache_type
				cacheHitLabelValues = append(cacheHitLabelValues, promLabelValues[len(p.defaultBifrostLabels):]...) // then custom labels

				p.CacheHitsTotal.WithLabelValues(cacheHitLabelValues...).Inc()
			}
		}
	}()

	return result, bifrostErr, nil
}

// HTTPMiddleware wraps a FastHTTP handler to collect Prometheus metrics.
// It tracks:
//   - Total number of requests
//   - Request duration
//   - Request and response sizes
//   - HTTP status codes
//   - Bifrost upstream requests and errors
func (p *PrometheusPlugin) HTTPMiddleware(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		start := time.Now()

		// Collect request metrics and headers
		promKeyValues := collectPrometheusKeyValues(ctx)
		reqSize := float64(ctx.Request.Header.ContentLength())

		// Process the request
		handler(ctx)

		// Record metrics after request completion
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(ctx.Response.StatusCode())
		respSize := float64(ctx.Response.Header.ContentLength())

		// Add status to the label values
		promKeyValues["status"] = status

		// Get label values in the correct order
		promLabelValues := getPrometheusLabelValues(append([]string{"path", "method", "status"}, p.customLabels...), promKeyValues)

		// Record all metrics with prometheus labels
		p.HTTPRequestsTotal.WithLabelValues(promLabelValues...).Inc()
		p.HTTPRequestDuration.WithLabelValues(promLabelValues...).Observe(duration)
		if reqSize >= 0 {
			safeObserve(p.HTTPRequestSizeBytes, reqSize, promLabelValues...)
		}
		if respSize >= 0 {
			safeObserve(p.HTTPResponseSizeBytes, respSize, promLabelValues...)
		}
	}
}

func (p *PrometheusPlugin) Cleanup() error {
	// No-op. With a local registry, there's no need to unregister metrics.
	// The registry and all its metrics will be garbage collected with the plugin instance.
	return nil
}
