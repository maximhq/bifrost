// Package telemetry provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. It includes middleware for HTTP request tracking
// and a plugin for tracking upstream provider metrics.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
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
	startTimeKey         schemas.BifrostContextKey = "bf-prom-start-time"
	activeRequestTypeKey schemas.BifrostContextKey = "bf-prom-active-req-type"
	mcpStartTimeKey      schemas.BifrostContextKey = "bf-prom-mcp-start-time"
	mcpClientNameKey     schemas.BifrostContextKey = "bf-prom-mcp-client-name"
	mcpToolNameKey       schemas.BifrostContextKey = "bf-prom-mcp-tool-name"

	// Overhead is measured across the transport hooks rather than the LLM hooks,
	// so the window matches the OTEL root span. See recordOverhead.
	transportStartTimeKey schemas.BifrostContextKey = "bf-prom-transport-start-time"
	overheadLabelsKey     schemas.BifrostContextKey = "bf-prom-overhead-labels"
)

// PushGatewayConfig holds the configuration for pushing metrics to a Prometheus Push Gateway.
// This enables accurate metrics aggregation in multi-node cluster deployments where
// traditional /metrics scraping may miss nodes behind load balancers.
type PushGatewayConfig struct {
	// Enabled controls whether pushing metrics to the Push Gateway is active
	Enabled bool `json:"enabled"`
	// PushGatewayURL is the URL of the Prometheus Push Gateway (e.g., http://pushgateway:9091). Supports env.VAR_NAME.
	PushGatewayURL *schemas.SecretVar `json:"push_gateway_url"`
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
	Username *schemas.SecretVar `json:"username"`
	Password *schemas.SecretVar `json:"password"`
}

// MarshalForStorage serializes Config to JSON with *SecretVar fields as plain strings
// ("env.VAR_NAME" or the literal value) for database/config-file persistence.
// For HTTP API responses use json.Marshal directly so clients receive full SecretVar objects.
func (c *Config) MarshalForStorage() ([]byte, error) {
	type basicAuthStorage struct {
		Username string `json:"username,omitempty"`
		Password string `json:"password,omitempty"`
	}
	type pushGatewayStorage struct {
		Enabled        bool              `json:"enabled"`
		PushGatewayURL string            `json:"push_gateway_url,omitempty"`
		JobName        string            `json:"job_name,omitempty"`
		InstanceID     string            `json:"instance_id,omitempty"`
		PushInterval   int               `json:"push_interval,omitempty"`
		BasicAuth      *basicAuthStorage `json:"basic_auth,omitempty"`
	}
	type configStorage struct {
		CustomLabels   []string            `json:"custom_labels,omitempty"`
		MetricsEnabled *bool               `json:"metrics_enabled,omitempty"`
		PushGateway    *pushGatewayStorage `json:"push_gateway,omitempty"`
	}
	storage := configStorage{
		CustomLabels:   c.CustomLabels,
		MetricsEnabled: c.MetricsEnabled,
	}
	if c.PushGateway != nil {
		pgw := &pushGatewayStorage{
			Enabled:        c.PushGateway.Enabled,
			PushGatewayURL: schemas.SecretVarAsString(c.PushGateway.PushGatewayURL),
			JobName:        c.PushGateway.JobName,
			InstanceID:     c.PushGateway.InstanceID,
			PushInterval:   c.PushGateway.PushInterval,
		}
		if c.PushGateway.BasicAuth != nil {
			pgw.BasicAuth = &basicAuthStorage{
				Username: schemas.SecretVarAsString(c.PushGateway.BasicAuth.Username),
				Password: schemas.SecretVarAsString(c.PushGateway.BasicAuth.Password),
			}
		}
		storage.PushGateway = pgw
	}
	return sonic.Marshal(storage)
}

// Redacted returns a copy of the config with sensitive SecretVar fields redacted for API responses.
// PushGatewayURL is not a secret and is returned unchanged so the UI can display and re-submit
// it without failing URL validation. For env var references on that field, only the resolved
// value is hidden; the env_var name is preserved. Basic auth credentials are masked.
func (c *Config) Redacted() *Config {
	if c == nil {
		return nil
	}
	redacted := *c
	if c.PushGateway != nil {
		pg := *c.PushGateway
		pg.PushGatewayURL = hideResolvedEnvValue(c.PushGateway.PushGatewayURL)
		if c.PushGateway.BasicAuth != nil {
			ba := *c.PushGateway.BasicAuth
			ba.Username = c.PushGateway.BasicAuth.Username.Redacted()
			ba.Password = c.PushGateway.BasicAuth.Password.FullyRedacted()
			pg.BasicAuth = &ba
		}
		redacted.PushGateway = &pg
	}
	return &redacted
}

// hideResolvedEnvValue returns v unchanged for literal values (URLs are not secrets).
// For env var references it zeroes out the resolved Val so the actual env content is
// not leaked in API responses, while keeping the env_var name for round-trip edits.
func hideResolvedEnvValue(v *schemas.SecretVar) *schemas.SecretVar {
	if v == nil || !v.IsFromSecret() {
		return v
	}
	return v.Redacted()
}

// PrometheusPlugin implements the schemas.LLMPlugin interface for Prometheus metrics.
// It tracks metrics for upstream provider requests, including:
//   - Total number of requests
//   - Request latency
//   - Error counts
type PrometheusPlugin struct {
	pricingManager *modelcatalog.ModelCatalog
	registry       *prometheus.Registry // Bifrost metrics only — used for push gateway
	systemRegistry *prometheus.Registry // Go/process collectors — /metrics scraping only

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
	OverheadLatencySeconds         *prometheus.HistogramVec
	SuccessRequestsTotal           *prometheus.CounterVec
	ErrorRequestsTotal             *prometheus.CounterVec
	InputTokensTotal               *prometheus.CounterVec
	OutputTokensTotal              *prometheus.CounterVec
	CacheHitsTotal                 *prometheus.CounterVec
	CacheReadInputTokensTotal      *prometheus.CounterVec
	CacheWriteInputTokensTotal     *prometheus.CounterVec
	CacheWriteInputTokens5mTotal   *prometheus.CounterVec
	CacheWriteInputTokens1hTotal   *prometheus.CounterVec
	CostTotal                      *prometheus.CounterVec
	StreamInterTokenLatencySeconds *prometheus.HistogramVec
	StreamFirstTokenLatencySeconds *prometheus.HistogramVec
	RequestRetries                 *prometheus.HistogramVec
	KeyRotationEventsTotal         *prometheus.CounterVec
	ActiveRequests                 *prometheus.GaugeVec
	ProviderKeyUp                  *prometheus.GaugeVec
	MCPToolDuration                *prometheus.HistogramVec
	customLabels                   []string

	defaultHTTPLabels    []string
	defaultBifrostLabels []string
	defaultMCPLabels     []string

	// Push gateway fields
	pushConfig *PushGatewayConfig
	pusher     *push.Pusher
	pushCtx    context.Context
	pushCancel context.CancelFunc
	pushWg     sync.WaitGroup
	pushMu     sync.RWMutex
	pushActive bool

	// MetricsEnabled gates the /metrics scrape endpoint.
	metricsEnabled atomic.Bool
}

type Config struct {
	CustomLabels []string `json:"custom_labels"`
	Registry     *prometheus.Registry
	PushGateway  *PushGatewayConfig `json:"push_gateway"`
	// MetricsEnabled controls whether the /metrics scrape endpoint is served.
	MetricsEnabled *bool `json:"metrics_enabled,omitempty"`
}

// Keep in sync with plugins/otel/metrics.go's identical arrays so the Prometheus
// and OTel exporters report the same quantile estimates for the same metric.
var (
	// upstreamLatencyBuckets: end-to-end / upstream LLM call latency. Top end (900s)
	// covers reasoning-model and long-context outliers; without these buckets p99
	// collapses to the highest finite bucket boundary.
	upstreamLatencyBuckets = []float64{
		.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5,
		10, 15, 30, 45, 60, 90, 120, 180, 300, 600, 900,
	}

	// overheadLatencyBuckets: Bifrost's own processing cost, i.e. total minus time
	// blocked on upstream sockets. A different scale entirely from upstream latency —
	// healthy values are sub-millisecond to low tens of ms, dominated by request and
	// response marshalling. Reusing upstreamLatencyBuckets would pile almost every
	// request into the first two buckets and make regressions invisible. The long
	// tail up to 30s exists to catch queue saturation and pathological payloads.
	overheadLatencyBuckets = []float64{
		.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1,
		.25, .5, 1, 2.5, 5, 10, 30,
	}

	// firstTokenLatencyBuckets: TTFT. Bimodal - sub-second for fast streaming
	// providers, tens to hundreds of seconds for reasoning models. Purely additive
	// over prometheus.DefBuckets so historical le-label queries remain valid.
	firstTokenLatencyBuckets = []float64{
		.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5,
		10, 20, 30, 60, 120, 300,
	}

	// interTokenLatencyBuckets: inter-token latency. Typically single-digit ms to ~1s.
	// Adds .001 below DefBuckets for fast models (Haiku) and keeps 10 at the top so
	// the array is purely additive over the previous DefBuckets fallback.
	interTokenLatencyBuckets = []float64{
		.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10,
	}
)

// Compile-time checks that PrometheusPlugin implements the hook interfaces it
// registers metrics for (MCP hooks are auto-discovered by rebuildInterfaceCaches).
var (
	_ schemas.LLMPlugin = (*PrometheusPlugin)(nil)
	_ schemas.MCPPlugin = (*PrometheusPlugin)(nil)
)

// Init creates a new PrometheusPlugin with initialized metrics.
// defaultBifrostLabelNames is the canonical set of Prometheus labels attached to
// bifrost.* metrics. It is a package var (not an Init local) so the connector-
// parity conformance test can assert it against the shared enrichment registry
// (core/schemas). Metric-tier dimensions only — no high-cardinality (user, arrays).
var defaultBifrostLabelNames = []string{
	"provider",
	"model",
	"alias",
	"method",
	"virtual_key_id",
	"virtual_key_name",
	"routing_engine_used",
	"routing_rule_id",
	"routing_rule_name",
	"selected_key_id",
	"selected_key_name",
	"fallback_index",
	"team_id",
	"team_name",
	"customer_id",
	"customer_name",
	"business_unit_id",
	"business_unit_name",
}

// defaultMCPLabelNames is the label set for bifrost_mcp_* metrics: the MCP semconv
// dimensions available in the hook plus the governance identity. No network_transport
// (core stamps it on the span, not context) and no provider/model.
var defaultMCPLabelNames = []string{
	"mcp_client",
	"mcp_tool_name",
	"mcp_method",
	"error_type",
	"virtual_key_id",
	"virtual_key_name",
	"team_id",
	"team_name",
	"customer_id",
	"customer_name",
	"business_unit_id",
	"business_unit_name",
}

// mcpOperationDurationBuckets: the OTel MCP semconv boundaries, matching plugins/otel
// so both exporters report the same quantiles for the same operation.
var mcpOperationDurationBuckets = []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}

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

	// GoCollector and ProcessCollector go into a separate registry so they are served
	// on /metrics but never pushed to the push gateway (the gateway itself registers
	// the same metric names and conflicts/spams warnings when they collide).
	systemRegistry := prometheus.NewRegistry()
	goCollector := collectors.NewGoCollector()
	if err := systemRegistry.Register(goCollector); err != nil {
		return nil, fmt.Errorf("failed to register Go collector: %v", err)
	}

	processCollector := collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})
	if err := systemRegistry.Register(processCollector); err != nil {
		return nil, fmt.Errorf("failed to register process collector: %v", err)
	}

	defaultHTTPLabels := []string{"path", "method", "status"}
	defaultBifrostLabels := append([]string(nil), defaultBifrostLabelNames...)

	var filteredCustomLabels []string
	if len(config.CustomLabels) > 0 {
		for _, label := range config.CustomLabels {
			if !containsLabel(defaultBifrostLabels, label) && !containsLabel(defaultHTTPLabels, label) && !containsLabel(defaultMCPLabelNames, label) {
				filteredCustomLabels = append(filteredCustomLabels, label)
			} else {
				logger.Info("custom label %s is already a default label, it will be ignored", label)
			}
		}
	}

	factory := promauto.With(registry)

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
			Buckets: upstreamLatencyBuckets,
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

	// Labelled without is_success: unlike upstream latency, overhead is dominated by
	// payload marshalling and is not expected to differ between success and failure,
	// and a failed request often has no response to marshal at all.
	bifrostOverheadLatencySeconds := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bifrost_overhead_latency_seconds",
			Help:    "Latency added by Bifrost itself: total request time minus time blocked on upstream providers.",
			Buckets: overheadLatencyBuckets,
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
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
		append(append(defaultBifrostLabels, "status_code"), filteredCustomLabels...),
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

	// Provider-side prompt cache tokens (Anthropic/OpenAI/Gemini prompt caching). Distinct
	// from bifrost_cache_hits_total, which counts Bifrost's own semantic-cache hits.
	bifrostCacheReadInputTokensTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_cache_read_input_tokens_total",
			Help: "Total provider-side prompt-cache read (cached) input tokens. Billed at a reduced rate by the provider.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostCacheWriteInputTokensTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_cache_write_input_tokens_total",
			Help: "Total provider-side prompt-cache creation (write) input tokens.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostCacheWriteInputTokens5mTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_cache_write_input_tokens_5m_total",
			Help: "Provider-side prompt-cache write input tokens with a 5-minute TTL (Anthropic only). Subset of bifrost_cache_write_input_tokens_total — do not sum with it.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostCacheWriteInputTokens1hTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_cache_write_input_tokens_1h_total",
			Help: "Provider-side prompt-cache write input tokens with a 1-hour TTL (Anthropic only). Subset of bifrost_cache_write_input_tokens_total — do not sum with it.",
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
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
			Name:    "bifrost_stream_inter_token_latency_seconds",
			Help:    "Latency of the intermediate tokens of a stream response.",
			Buckets: interTokenLatencyBuckets,
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostStreamFirstTokenLatencySeconds := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bifrost_stream_first_token_latency_seconds",
			Help:    "Latency of the first token of a stream response.",
			Buckets: firstTokenLatencyBuckets,
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	bifrostRequestRetries := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bifrost_request_retries",
			Help:    "Number of retries used per request (observed once per request).",
			Buckets: []float64{0, 1, 2, 3, 5, 10},
		},
		append(defaultBifrostLabels, filteredCustomLabels...),
	)

	// bifrostKeyRotationEventsTotal counts key-swap events from the attempt trail.
	// One observation is emitted only when a failed attempt triggered rotation to a different key
	// on the next retry (TriggeredRotation == true, fail_reason non-nil). Use this to track actual
	// key-rotation pressure per provider/key/failure reason.

	bifrostKeyRotationEventsTotal := factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_key_rotation_events_total",
			Help: "Number of key rotations, broken down by provider, key, and failure reason. One increment per per-key failure (rate-limit/auth/billing/permission) that triggered a switch to a different key on the next retry.",
		},
		[]string{"provider", "requested_model", "key_id", "key_name", "fail_reason"},
	)

	bifrostActiveRequests := factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bifrost_active_requests",
			Help: "Number of LLM requests currently in-flight.",
		},
		[]string{"method"},
	)

	bifrostProviderKeyUp := factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "bifrost_provider_key_up",
			Help: "Health of a provider key. 1 = last attempt succeeded, 0 = last attempt failed.",
		},
		[]string{"provider", "key_id", "key_name"},
	)

	// Mirrors the OTel semconv metric mcp.client.operation.duration. _count gives
	// tool-call volume and error_type the error rate, so no separate MCP counters.
	defaultMCPLabels := append([]string(nil), defaultMCPLabelNames...)
	bifrostMCPToolDuration := factory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bifrost_mcp_client_operation_duration_seconds",
			Help:    "Duration of an MCP tool call as observed by Bifrost (the MCP client).",
			Buckets: mcpOperationDurationBuckets,
		},
		append(defaultMCPLabels, filteredCustomLabels...),
	)

	plugin := &PrometheusPlugin{
		logger:                         logger,
		pricingManager:                 pricingManager,
		registry:                       registry,
		systemRegistry:                 systemRegistry,
		GoCollector:                    goCollector,
		ProcessCollector:               processCollector,
		HTTPRequestsTotal:              httpRequestsTotal,
		HTTPRequestDuration:            httpRequestDuration,
		HTTPRequestSizeBytes:           httpRequestSizeBytes,
		HTTPResponseSizeBytes:          httpResponseSizeBytes,
		UpstreamRequestsTotal:          bifrostUpstreamRequestsTotal,
		UpstreamLatencySeconds:         bifrostUpstreamLatencySeconds,
		OverheadLatencySeconds:         bifrostOverheadLatencySeconds,
		SuccessRequestsTotal:           bifrostSuccessRequestsTotal,
		ErrorRequestsTotal:             bifrostErrorRequestsTotal,
		InputTokensTotal:               bifrostInputTokensTotal,
		OutputTokensTotal:              bifrostOutputTokensTotal,
		CacheHitsTotal:                 bifrostCacheHitsTotal,
		CacheReadInputTokensTotal:      bifrostCacheReadInputTokensTotal,
		CacheWriteInputTokensTotal:     bifrostCacheWriteInputTokensTotal,
		CacheWriteInputTokens5mTotal:   bifrostCacheWriteInputTokens5mTotal,
		CacheWriteInputTokens1hTotal:   bifrostCacheWriteInputTokens1hTotal,
		CostTotal:                      bifrostCostTotal,
		StreamInterTokenLatencySeconds: bifrostStreamInterTokenLatencySeconds,
		StreamFirstTokenLatencySeconds: bifrostStreamFirstTokenLatencySeconds,
		RequestRetries:                 bifrostRequestRetries,
		KeyRotationEventsTotal:         bifrostKeyRotationEventsTotal,
		ActiveRequests:                 bifrostActiveRequests,
		ProviderKeyUp:                  bifrostProviderKeyUp,
		MCPToolDuration:                bifrostMCPToolDuration,
		customLabels:                   filteredCustomLabels,
		defaultHTTPLabels:              defaultHTTPLabels,
		defaultBifrostLabels:           defaultBifrostLabels,
		defaultMCPLabels:               defaultMCPLabels,
	}

	// Default /metrics scraping to on when the config omits the field — preserves
	// behavior for existing connectors written before metrics_enabled existed.
	metricsEnabled := true
	if config.MetricsEnabled != nil {
		metricsEnabled = *config.MetricsEnabled
	}
	plugin.metricsEnabled.Store(metricsEnabled)

	// Start push gateway if configured
	if config.PushGateway != nil && config.PushGateway.Enabled && config.PushGateway.PushGatewayURL.IsSet() {
		if err := plugin.EnablePushGateway(config.PushGateway); err != nil {
			return nil, fmt.Errorf("failed to start push gateway: %w", err)
		}
	}

	return plugin, nil
}

// IsMetricsEnabled reports whether the /metrics scrape endpoint should serve
// metrics on this instance. Safe to call from request-handling goroutines.
func (p *PrometheusPlugin) IsMetricsEnabled() bool {
	return p.metricsEnabled.Load()
}

func (p *PrometheusPlugin) GetRegistry() *prometheus.Registry {
	return p.registry
}

// GetMetricsGatherer returns a combined gatherer for the /metrics endpoint,
// including both Bifrost metrics and Go/process runtime collectors.
func (p *PrometheusPlugin) GetMetricsGatherer() prometheus.Gatherer {
	return prometheus.Gatherers{p.registry, p.systemRegistry}
}

// GetName returns the name of the plugin.
func (p *PrometheusPlugin) GetName() string {
	return PluginName
}

// MarshalConfigForStorage implements schemas.ConfigMarshallerPlugin.
func (p *PrometheusPlugin) MarshalConfigForStorage(raw map[string]any) (map[string]any, error) {
	b, err := sonic.Marshal(raw)
	if err != nil {
		return raw, err
	}
	var c Config
	if err := sonic.Unmarshal(b, &c); err != nil {
		return raw, err
	}
	normalized, err := c.MarshalForStorage()
	if err != nil {
		return raw, err
	}
	var out map[string]any
	if err := sonic.Unmarshal(normalized, &out); err != nil {
		return raw, err
	}
	return out, nil
}

// RedactConfig implements schemas.ConfigMarshallerPlugin.
func (p *PrometheusPlugin) RedactConfig(raw map[string]any) (map[string]any, error) {
	b, err := sonic.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := sonic.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	out, err := sonic.Marshal(c.Redacted())
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := sonic.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// Ensure PrometheusPlugin satisfies the marshaller contract, and self-register it so
// the server can redact/normalize this plugin's stored config even when it is disabled.
var _ schemas.ConfigMarshallerPlugin = (*PrometheusPlugin)(nil)

func init() {
	schemas.RegisterConfigMarshaller(PluginName, &PrometheusPlugin{})
}

// HTTPTransportPreHook is not used for this plugin
func (p *PrometheusPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	ctx.SetValue(transportStartTimeKey, time.Now())
	return nil, nil
}

// HTTPTransportPostHook records Bifrost's own overhead.
//
// This is the widest window the plugin can see — the middleware order is
// Tracing.pre → TransportInterceptor.pre → handler → TransportInterceptor.post
// → Tracing.defer — so it brackets request parsing, the full core pipeline and
// response marshalling, matching what OTEL derives from the root span. Measuring
// across PreLLMHook/PostLLMHook instead would miss the transport work, and
// marshalling a large response body is a real part of the cost.
//
// Running here also makes the observation once-per-request rather than
// once-per-attempt, so a retried request no longer contributes several times.
func (p *PrometheusPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	start, ok := ctx.Value(transportStartTimeKey).(time.Time)
	if !ok {
		return nil
	}
	p.recordOverhead(ctx, time.Since(start))
	return nil
}

// recordOverhead observes total-minus-upstream against the labels the LLM hook
// resolved. Silent when either is missing: no accumulator means upstream was
// never measured, and reporting the full duration as overhead would be wrong.
func (p *PrometheusPlugin) recordOverhead(ctx *schemas.BifrostContext, total time.Duration) {
	labels, ok := ctx.Value(overheadLabelsKey).([]string)
	if !ok || len(labels) == 0 {
		return
	}
	if overhead, ok := schemas.CalculateOverhead(ctx, total); ok {
		p.OverheadLatencySeconds.WithLabelValues(labels...).Observe(overhead.Seconds())
	}
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *PrometheusPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreRequestHook implements schemas.LLMPlugin (no-op — required for plugin indexing).
func (p *PrometheusPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook records the start time of the request in the context.
// This time is used later in PostLLMHook to calculate request duration.
func (p *PrometheusPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	ctx.SetValue(startTimeKey, time.Now())
	ctx.SetValue(activeRequestTypeKey, req.RequestType)
	p.ActiveRequests.WithLabelValues(string(req.RequestType)).Inc()
	return req, nil, nil
}

// applyCustomLabels resolves each configured custom label into labelValues.
// Resolution order (first match wins):
//  1. x-bf-dim-* headers (canonical; BifrostContextKeyDimensions)
//  2. x-bf-prom-* headers (deprecated; kept for backward compatibility)
//  3. Direct BifrostContextKey lookup (Go SDK usage — documented API)
func (p *PrometheusPlugin) applyCustomLabels(ctx *schemas.BifrostContext, labelValues map[string]string) {
	dims, _ := ctx.Value(schemas.BifrostContextKeyDimensions).(map[string]string)
	requestHeaders, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	for _, key := range p.customLabels {
		if dims != nil {
			if v, ok := dims[key]; ok {
				labelValues[key] = v
				continue
			}
		}
		if requestHeaders != nil {
			if v, ok := requestHeaders["x-bf-prom-"+key]; ok {
				labelValues[key] = v
				continue
			}
		}
		if value := ctx.Value(schemas.BifrostContextKey(key)); value != nil {
			if strValue, ok := value.(string); ok {
				labelValues[key] = strValue
			}
		}
	}
}

// PreMCPHook stashes the tool-call start time so PostMCPHook has a wall-time
// fallback on the error path (where the response — and its latency — is absent).
func (p *PrometheusPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if ctx != nil && req != nil && req.RequestType.IsExecuteTool() {
		ctx.SetValue(mcpStartTimeKey, time.Now())
		// Stash identity so the error path (resp == nil) still has tool/client
		// for codemode filtering and metric labels.
		ctx.SetValue(mcpClientNameKey, req.ClientName)
		ctx.SetValue(mcpToolNameKey, req.GetToolName())
	}
	return req, nil, nil
}

// PostMCPHook records the MCP tool-call duration. Only execute-tool calls are
// recorded (codemode tools skipped); ping/list_tools are lifecycle, not tool calls.
// The gate stamps MCPRequestType on both the success response and the error.
func (p *PrometheusPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		return resp, bifrostErr, nil
	}
	mcpReqType := schemas.MCPRequestType("")
	toolName, clientName := "", ""
	if resp != nil {
		mcpReqType = resp.ExtraFields.MCPRequestType
		toolName = resp.ExtraFields.ToolName
		clientName = resp.ExtraFields.ClientName
	} else if bifrostErr != nil {
		mcpReqType = bifrostErr.ExtraFields.MCPRequestType
		// No response on the error path — recover identity stashed in PreMCPHook.
		clientName = bifrost.GetStringFromContext(ctx, mcpClientNameKey)
		toolName = bifrost.GetStringFromContext(ctx, mcpToolNameKey)
	}
	if !mcpReqType.IsExecuteTool() || bifrost.IsCodemodeTool(toolName) {
		return resp, bifrostErr, nil
	}

	// Prefer the wire tool-call latency; fall back to wall-time on the error path.
	var durationSeconds float64
	if resp != nil && resp.ExtraFields.Latency > 0 {
		durationSeconds = float64(resp.ExtraFields.Latency) / 1000.0
	} else if start, ok := ctx.Value(mcpStartTimeKey).(time.Time); ok {
		durationSeconds = time.Since(start).Seconds()
	}

	errorType := ""
	if bifrostErr != nil {
		errorType = mcpErrorType(bifrostErr)
	}

	labelValues := map[string]string{
		"mcp_client":         clientName,
		"mcp_tool_name":      toolName,
		"mcp_method":         mcpReqType.OTelMethodName(),
		"error_type":         errorType,
		"virtual_key_id":     bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID),
		"virtual_key_name":   bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName),
		"team_id":            bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID),
		"team_name":          bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamName),
		"customer_id":        bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID),
		"customer_name":      bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerName),
		"business_unit_id":   bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitID),
		"business_unit_name": bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitName),
	}
	p.applyCustomLabels(ctx, labelValues)

	promLabelValues := getPrometheusLabelValues(append(p.defaultMCPLabels, p.customLabels...), labelValues)
	p.MCPToolDuration.WithLabelValues(promLabelValues...).Observe(durationSeconds)

	return resp, bifrostErr, nil
}

// mcpErrorType classifies an MCP tool-call failure into a low-cardinality error_type.
// Coarse by design: timeout/tool_error granularity (which the OTel/Datadog span path
// derives from error.type) would require core's error sentinels here.
func mcpErrorType(bifrostErr *schemas.BifrostError) string {
	if bifrostErr != nil && bifrostErr.ExtraFields.MCPAuthRequired != nil {
		return "auth_required"
	}
	return "_OTHER"
}

// extractProviderCacheTokens returns provider-side prompt-cache token counts from a
// response's usage: cache-read (cached) input tokens, cache-write (creation) input tokens,
// and the Anthropic-only 5m/1h TTL breakdown of the write total. Chat/text-completion carry
// these on Usage.PromptTokensDetails; the Responses API carries them on
// Usage.InputTokensDetails. Mirrors the response-type switch used for input/output tokens.
func extractProviderCacheTokens(result *schemas.BifrostResponse) (read, write, write5m, write1h int) {
	var promptDetails *schemas.ChatPromptTokensDetails
	var inputDetails *schemas.ResponsesResponseInputTokens

	switch {
	case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
		promptDetails = result.TextCompletionResponse.Usage.PromptTokensDetails
	case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
		promptDetails = result.ChatResponse.Usage.PromptTokensDetails
	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		inputDetails = result.ResponsesResponse.Usage.InputTokensDetails
	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
		inputDetails = result.ResponsesStreamResponse.Response.Usage.InputTokensDetails
	}

	switch {
	case promptDetails != nil:
		read, write = promptDetails.CachedReadTokens, promptDetails.CachedWriteTokens
		if d := promptDetails.CachedWriteTokenDetails; d != nil {
			write5m, write1h = d.CachedWriteTokens5m, d.CachedWriteTokens1h
		}
	case inputDetails != nil:
		read, write = inputDetails.CachedReadTokens, inputDetails.CachedWriteTokens
		if d := inputDetails.CachedWriteTokenDetails; d != nil {
			write5m, write1h = d.CachedWriteTokens5m, d.CachedWriteTokens1h
		}
	}
	return
}

// PostLLMHook calculates duration and records upstream metrics for successful requests.
// It records:
//   - Request latency
//   - Total request count
func (p *PrometheusPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestType, provider, originalModel, resolvedModel := bifrost.GetResponseFields(result, bifrostErr)

	// Determine effective model label and alias label (mirrors applyModelAlias logic in logging)
	model := originalModel
	alias := ""
	if resolvedModel != "" {
		model = resolvedModel
		if resolvedModel != originalModel {
			alias = originalModel
		}
	}

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
	attemptTrail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
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
	businessUnitID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitID)
	businessUnitName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitName)

	// Extract ALL context values BEFORE spawning the goroutine.
	labelValues := map[string]string{
		"provider":            string(provider),
		"model":               model,
		"alias":               alias,
		"method":              string(requestType),
		"virtual_key_id":      virtualKeyID,
		"virtual_key_name":    virtualKeyName,
		"routing_engine_used": routingEngineUsed,
		"routing_rule_id":     routingRuleID,
		"routing_rule_name":   routingRuleName,
		"selected_key_id":     selectedKeyID,
		"selected_key_name":   selectedKeyName,
		"fallback_index":      strconv.Itoa(fallbackIndex),
		"team_id":             teamID,
		"team_name":           teamName,
		"customer_id":         customerID,
		"customer_name":       customerName,
		"business_unit_id":    businessUnitID,
		"business_unit_name":  businessUnitName,
	}

	// Get all custom prometheus labels from context BEFORE the goroutine.
	p.applyCustomLabels(ctx, labelValues)

	// Get label values in the correct order (cache_type will be handled separately for cache hits)
	promLabelValues := getPrometheusLabelValues(append(p.defaultBifrostLabels, p.customLabels...), labelValues)

	// Extract stream end indicator BEFORE the goroutine
	streamEndIndicatorValue := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator)
	isFinalChunk, hasFinalChunkIndicator := streamEndIndicatorValue.(bool)

	// Decrement active requests on the final (or only) call for this request
	isStreamFinal := !bifrost.IsStreamRequestType(requestType) || (hasFinalChunkIndicator && isFinalChunk)
	if isStreamFinal {
		if method, ok := ctx.Value(activeRequestTypeKey).(schemas.RequestType); ok {
			p.ActiveRequests.WithLabelValues(string(method)).Dec()
		}
	}

	pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(provider))

	// Labels for HTTPTransportPostHook, which observes overhead. Written before
	// the goroutine launches so the transport hook can't race it; on a retry the
	// final attempt's labels win.
	if isStreamFinal {
		ctx.SetValue(overheadLabelsKey, slices.Clone(promLabelValues))
	}

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
			cost = p.pricingManager.CalculateCost(result, pricingScopes)
		}

		// Emit one rotation counter increment per attempt that actually caused a key swap on the
		// next try (per-key failure — rate-limit/auth/billing/permission — with retries remaining).
		// Mark the key unhealthy on any failure, since key health is per-failure not per-rotation.
		for _, record := range attemptTrail {
			if record.TriggeredRotation && record.FailReason != nil {
				p.KeyRotationEventsTotal.WithLabelValues(
					string(provider), originalModel, record.KeyID, record.KeyName, *record.FailReason,
				).Inc()
			}
			if record.FailReason != nil {
				p.ProviderKeyUp.WithLabelValues(string(provider), record.KeyID, record.KeyName).Set(0)
			}
		}
		// Mark the selected key healthy if the request ultimately succeeded
		if bifrostErr == nil && selectedKeyID != "" {
			p.ProviderKeyUp.WithLabelValues(string(provider), selectedKeyID, selectedKeyName).Set(1)
		}

		p.UpstreamRequestsTotal.WithLabelValues(promLabelValues...).Inc()

		// Record retries used for this request. Observed once per request (per the goroutine
		// guarding around isStreamFinal), so .Sum/.Count map cleanly to "total retry attempts"
		// and "total requests"; bucket le="0" gives "requests that succeeded on the first try".
		p.RequestRetries.WithLabelValues(promLabelValues...).Observe(float64(numberOfRetries))

		// Record latency
		duration := time.Since(startTime).Seconds()
		latencyLabelValues := make([]string, 0, len(promLabelValues)+1)
		latencyLabelValues = append(latencyLabelValues, promLabelValues[:len(p.defaultBifrostLabels)]...) // all default labels
		latencyLabelValues = append(latencyLabelValues, strconv.FormatBool(bifrostErr == nil))            // is_success
		latencyLabelValues = append(latencyLabelValues, promLabelValues[len(p.defaultBifrostLabels):]...) // then custom labels
		p.UpstreamLatencySeconds.WithLabelValues(latencyLabelValues...).Observe(duration)

		// SDK caller — no transport hooks, so this window is all there is.
		if _, viaTransport := ctx.Value(transportStartTimeKey).(time.Time); !viaTransport {
			p.recordOverhead(ctx, time.Since(startTime))
		}

		// Record cost using the dedicated cost counter
		if cost > 0 {
			p.CostTotal.WithLabelValues(promLabelValues...).Add(cost)
		}

		// Record error and success counts
		if bifrostErr != nil {
			// Add status_code to label values (create new slice to avoid modifying original)
			statusCode := "unknown"
			if bifrostErr.StatusCode != nil {
				statusCode = strconv.Itoa(*bifrostErr.StatusCode)
			}
			errorPromLabelValues := make([]string, 0, len(promLabelValues)+1)
			errorPromLabelValues = append(errorPromLabelValues, promLabelValues[:len(p.defaultBifrostLabels)]...) // all default labels
			errorPromLabelValues = append(errorPromLabelValues, statusCode)                                       // status_code
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
			case result.CompactionResponse != nil && result.CompactionResponse.Usage != nil:
				if u := result.CompactionResponse.Usage.ToBifrostLLMUsage(); u != nil {
					inputTokens = u.PromptTokens
					outputTokens = u.CompletionTokens
				}
			case result.ImageGenerationResponse != nil && result.ImageGenerationResponse.Usage != nil:
				inputTokens = result.ImageGenerationResponse.Usage.InputTokens
				outputTokens = result.ImageGenerationResponse.Usage.OutputTokens
			case result.PassthroughResponse != nil && result.PassthroughResponse.PassthroughUsage != nil && result.PassthroughResponse.PassthroughUsage.LLMUsage != nil:
				inputTokens = result.PassthroughResponse.PassthroughUsage.LLMUsage.PromptTokens
				outputTokens = result.PassthroughResponse.PassthroughUsage.LLMUsage.CompletionTokens
			}

			p.InputTokensTotal.WithLabelValues(promLabelValues...).Add(float64(inputTokens))
			p.OutputTokensTotal.WithLabelValues(promLabelValues...).Add(float64(outputTokens))

			// Record provider-side prompt cache tokens (Anthropic/OpenAI/Gemini prompt
			// caching). Distinct from the cache-hit counter below, which tracks Bifrost's
			// own semantic cache. 5m/1h are an Anthropic-only TTL breakdown of the write total.
			cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h := extractProviderCacheTokens(result)
			if cacheRead > 0 {
				p.CacheReadInputTokensTotal.WithLabelValues(promLabelValues...).Add(float64(cacheRead))
			}
			if cacheWrite > 0 {
				p.CacheWriteInputTokensTotal.WithLabelValues(promLabelValues...).Add(float64(cacheWrite))
			}
			if cacheWrite5m > 0 {
				p.CacheWriteInputTokens5mTotal.WithLabelValues(promLabelValues...).Add(float64(cacheWrite5m))
			}
			if cacheWrite1h > 0 {
				p.CacheWriteInputTokens1hTotal.WithLabelValues(promLabelValues...).Add(float64(cacheWrite1h))
			}

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
	if config == nil || config.PushGatewayURL.GetValue() == "" {
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
	pusher := push.New(config.PushGatewayURL.GetValue(), config.JobName).
		Gatherer(p.registry).
		Grouping("instance", config.InstanceID)

	if config.BasicAuth != nil && config.BasicAuth.Username.IsSet() && config.BasicAuth.Password.IsSet() {
		pusher = pusher.BasicAuth(config.BasicAuth.Username.GetValue(), config.BasicAuth.Password.GetValue())
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
		config.PushGatewayURL.GetValue(), config.PushInterval)

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
