// Package telemetry provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. It includes middleware for HTTP request tracking
// and a plugin for tracking upstream provider metrics.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/valyala/fasthttp"
)

const (
	PluginName = "telemetry"
)

const (
	startTimeKey schemas.BifrostContextKey = "bf-prom-start-time"
)

// PushGatewayConfig holds the configuration for pushing metrics to a Prometheus Push Gateway.
// This enables accurate metrics aggregation in multi-node cluster deployments where
// traditional /metrics scraping may miss nodes behind load balancers.
type PushGatewayConfig struct {
	// Enabled controls whether pushing metrics to the Push Gateway is active
	Enabled bool `json:"enabled"`
	// PushGatewayURL is the URL of the Prometheus Push Gateway (e.g., http://pushgateway:9091)
	PushGatewayURL string `json:"push_gateway_url"`
	// JobName is the job label for pushed metrics (default: "bifrost")
	JobName string `json:"job_name"`
	// InstanceID is the instance label for grouping metrics. If empty, hostname is used.
	InstanceID string `json:"instance_id"`
	// PushInterval is how often to push metrics in seconds (default: 15)
	PushInterval int `json:"push_interval"`
	// BasicAuth credentials for the Push Gateway
	BasicAuth *BasicAuthConfig `json:"basic_auth"`
}

// BasicAuthConfig holds basic authentication credentials for the Push Gateway
type BasicAuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PrometheusPlugin implements the schemas.LLMPlugin interface for Prometheus metrics.
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

	// Push gateway fields
	pushConfig *PushGatewayConfig
	pusher     *push.Pusher
	pushCtx    context.Context
	pushCancel context.CancelFunc
	pushWg     sync.WaitGroup
	pushMu     sync.RWMutex
	pushActive bool
}

type Config struct {
	CustomLabels []string `json:"custom_labels"`
	Registry     *prometheus.Registry
	PushGateway  *PushGatewayConfig `json:"push_gateway"`
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
		"routing_engine_used",
		"routing_rule_id",
		"routing_rule_name",
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

	plugin := &PrometheusPlugin{
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
	}

	// Start push gateway if configured
	if config.PushGateway != nil && config.PushGateway.Enabled && config.PushGateway.PushGatewayURL != "" {
		if err := plugin.EnablePushGateway(config.PushGateway); err != nil {
			return nil, fmt.Errorf("failed to start push gateway: %w", err)
		}
	}

	return plugin, nil
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

// PreLLMHook records the start time of the request in the context.
// This time is used later in PostLLMHook to calculate request duration.
func (p *PrometheusPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	ctx.SetValue(startTimeKey, time.Now())
	return req, nil, nil
}

// PostLLMHook calculates duration and records upstream metrics for successful requests.
// It records:
//   - Request latency
//   - Total request count
func (p *PrometheusPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestType, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	startTime, ok := ctx.Value(startTimeKey).(time.Time)
	if !ok {
		p.logger.Warn("Warning: startTime not found in context for Prometheus PostLLMHook")
		return result, bifrostErr, nil
	}

	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)
	routingRuleID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceRoutingRuleID)
	routingRuleName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceRoutingRuleName)

	selectedKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	selectedKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyName)

	numberOfRetries := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyNumberOfRetries)
	fallbackIndex := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyFallbackIndex)
	// Get routing engines array and join into comma-separated string
	routingEngines := []string{}
	if engines, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string); ok {
		routingEngines = engines
	}
	routingEngineUsed := strings.Join(routingEngines, ",")

	teamID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID)
	teamName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamName)
	customerID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID)
	customerName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerName)

	// Extract ALL context values BEFORE spawning the goroutine.
	labelValues := map[string]string{
		"provider":            string(provider),
		"model":               model,
		"method":              string(requestType),
		"virtual_key_id":      virtualKeyID,
		"virtual_key_name":    virtualKeyName,
		"routing_engine_used": routingEngineUsed,
		"routing_rule_id":     routingRuleID,
		"routing_rule_name":   routingRuleName,
		"selected_key_id":     selectedKeyID,
		"selected_key_name":   selectedKeyName,
		"number_of_retries":   strconv.Itoa(numberOfRetries),
		"fallback_index":      strconv.Itoa(fallbackIndex),
		"team_id":             teamID,
		"team_name":           teamName,
		"customer_id":         customerID,
		"customer_name":       customerName,
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
			cost = p.pricingManager.CalculateCostWithScopes(result, modelcatalog.PricingLookupScopes{
				VirtualKeyID:  virtualKeyID,
				ProviderKeyID: selectedKeyID,
				ProviderID:    string(provider),
			})
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

// EnablePushGateway starts pushing metrics to a Prometheus Push Gateway.
// If push gateway is already active, it stops the existing one first.
func (p *PrometheusPlugin) EnablePushGateway(config *PushGatewayConfig) error {
	if config == nil || config.PushGatewayURL == "" {
		return fmt.Errorf("push_gateway_url is required")
	}

	// Stop existing push gateway if running
	p.DisablePushGateway()

	// Apply defaults
	if config.JobName == "" {
		config.JobName = "bifrost"
	}
	if config.PushInterval <= 0 {
		config.PushInterval = 15
	}
	if config.InstanceID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			config.InstanceID = "unknown"
		} else {
			config.InstanceID = hostname
		}
	}

	// Create the pusher with the registry
	pusher := push.New(config.PushGatewayURL, config.JobName).
		Gatherer(p.registry).
		Grouping("instance", config.InstanceID)

	if config.BasicAuth != nil && config.BasicAuth.Username != "" {
		pusher = pusher.BasicAuth(config.BasicAuth.Username, config.BasicAuth.Password)
	}

	ctx, cancel := context.WithCancel(context.Background())

	p.pushMu.Lock()
	p.pushConfig = config
	p.pusher = pusher
	p.pushCtx = ctx
	p.pushCancel = cancel
	p.pushActive = true
	p.pushWg.Add(1)
	p.pushMu.Unlock()

	go p.pushLoop()

	p.logger.Info("push gateway started, pushing to %s every %d seconds",
		config.PushGatewayURL, config.PushInterval)

	return nil
}

// DisablePushGateway stops the push gateway loop if active
func (p *PrometheusPlugin) DisablePushGateway() {
	p.pushMu.Lock()
	if !p.pushActive {
		p.pushMu.Unlock()
		return
	}
	p.pushActive = false
	p.pushCancel()
	p.pushMu.Unlock()

	p.pushWg.Wait()
	p.logger.Info("push gateway stopped")
}

// GetPushGatewayConfig returns the current push gateway configuration
func (p *PrometheusPlugin) GetPushGatewayConfig() *PushGatewayConfig {
	p.pushMu.RLock()
	defer p.pushMu.RUnlock()
	return p.pushConfig
}

// IsPushGatewayRunning returns whether the push gateway loop is active
func (p *PrometheusPlugin) IsPushGatewayRunning() bool {
	p.pushMu.RLock()
	defer p.pushMu.RUnlock()
	return p.pushActive
}

// pushLoop periodically pushes metrics to the Push Gateway
func (p *PrometheusPlugin) pushLoop() {
	defer p.pushWg.Done()

	p.pushMu.RLock()
	interval := p.pushConfig.PushInterval
	p.pushMu.RUnlock()

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	// Initial push
	p.doPush()

	for {
		select {
		case <-p.pushCtx.Done():
			// Final push before shutdown
			p.logger.Info("push gateway shutting down, performing final push")
			p.doPush()
			return
		case <-ticker.C:
			p.doPush()
		}
	}
}

// doPush performs a single push to the Push Gateway
func (p *PrometheusPlugin) doPush() {
	p.pushMu.RLock()
	pusher := p.pusher
	p.pushMu.RUnlock()

	if pusher == nil {
		return
	}

	if err := pusher.Push(); err != nil {
		p.logger.Error("failed to push metrics to push gateway: %v", err)
	}
}

func (p *PrometheusPlugin) Cleanup() error {
	p.DisablePushGateway()
	return nil
}
