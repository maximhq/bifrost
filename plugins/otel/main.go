// Package otel is OpenTelemetry plugin for Bifrost
package otel

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"go.opentelemetry.io/otel/attribute"
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

// Protocol is the protocol to use for the OTEL collector
type Protocol string

const (
	ProtocolHTTP Protocol = "http" // default
	ProtocolGRPC Protocol = "grpc"
)

// PluginSpanFilter, its mode type, and the include/exclude constants are shared across
// all observability connectors and live in core/schemas. They are re-exported here as
// aliases so existing OTEL config parsing, tests, and the UI keep their import paths.
type (
	PluginSpanFilterMode = schemas.PluginSpanFilterMode
	PluginSpanFilter     = schemas.PluginSpanFilter
)

const (
	PluginSpanFilterModeInclude = schemas.PluginSpanFilterModeInclude
	PluginSpanFilterModeExclude = schemas.PluginSpanFilterModeExclude
)

// Profile is a single OTEL export target: a collector endpoint and an optional
// metrics-push destination. A Config holds one or more profiles; each profile gets
// its own trace client and (when enabled) metrics exporter at runtime.
//
// Headers are plain strings using the "env.VAR_NAME" convention; they are resolved
// against the environment at Init time via injectEnvToHeaders.
type Profile struct {
	// Enabled gates whether this profile exports anything. The plugin itself is always on;
	// a disabled profile builds no trace client or metrics exporter, so no traces/metrics
	// are sent for it. Defaults to true when omitted.
	Enabled      bool               `json:"enabled"`
	ServiceName  string             `json:"service_name"`
	CollectorURL *schemas.SecretVar `json:"collector_url"`
	Headers      map[string]string  `json:"headers,omitempty"`
	TraceType    TraceType          `json:"trace_type"`
	Protocol     Protocol           `json:"protocol"`
	TLSCACert    string             `json:"tls_ca_cert,omitempty"`
	Insecure     bool               `json:"insecure"` // Skip TLS when true; ignored if TLSCACert is set. Defaults to true when omitted.

	// Metrics push configuration
	MetricsEnabled      bool               `json:"metrics_enabled"`
	MetricsEndpoint     *schemas.SecretVar `json:"metrics_endpoint,omitempty"`
	MetricsPushInterval int                `json:"metrics_push_interval,omitempty"` // in seconds, default 15

	// RequestHeaders lists request-header name patterns (exact or wildcard like "x-custom-*"
	// or "*") whose captured values are attached to the root span as attributes.
	RequestHeaders []string `json:"request_headers,omitempty"`

	// DisableContentLogging controls whether message content is exported to the OTEL collector.
	// When true, only metadata (model, tokens, latency, etc.) is exported; input/output message
	// content, tool definitions, and tool call arguments/results are dropped from span attributes.
	DisableContentLogging bool `json:"disable_content_logging,omitempty"`

	// GroupTracesBySession, when true, groups all requests sharing the same x-bf-session-id
	// header into a single OTEL trace: every span adopts a session-derived trace ID and each
	// request's root span becomes a top-level sibling under one synthetic session parent
	// (default: false). An inbound W3C traceparent always takes precedence, leaving that
	// request on its own distributed trace.
	GroupTracesBySession bool `json:"group_traces_by_session,omitempty"`

	// DisableRootSpanContent controls whether input/output message content is duplicated onto
	// the root span. The framework propagates a copy of the input/output onto the root span so
	// backends like Langfuse can show it at the trace level, but this duplicates the payload
	// already stored on the llm.call span and inflates downstream storage. When true, content
	// attributes are dropped from the root span only (child spans keep full content), so the
	// trace-level Input/Output goes empty while the generation observation retains everything.
	DisableRootSpanContent bool `json:"disable_root_span_content,omitempty"`
}

// UnmarshalJSON applies field defaults that the zero-value wouldn't capture.
// Specifically, Insecure defaults to true when the key is omitted so http://
// collectors work out-of-the-box without forcing users to set it explicitly.
func (p *Profile) UnmarshalJSON(data []byte) error {
	type alias Profile
	aux := struct {
		Enabled  *bool `json:"enabled"`
		Insecure *bool `json:"insecure"`
		*alias
	}{
		alias: (*alias)(p),
	}
	if err := sonic.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Insecure == nil {
		p.Insecure = true
	} else {
		p.Insecure = *aux.Insecure
	}
	if aux.Enabled == nil {
		p.Enabled = true
	} else {
		p.Enabled = *aux.Enabled
	}
	return nil
}

// Config is the OTEL plugin configuration: a set of export profiles plus a single
// shared span filter. It accepts two JSON shapes (see UnmarshalJSON):
//   - the canonical wrapper {"profiles": [ ... ], "plugin_span_filter": { ... }}
//   - a legacy single profile object, which is normalized into a one-element Profiles slice.
type Config struct {
	Profiles []*Profile `json:"profiles"`

	// PluginSpanFilter is a single policy applied across every profile. In a legacy
	// single-object config it is read from the object; in a profiles wrapper it is read
	// from the top-level field (or hoisted from the first profile that carries one).
	PluginSpanFilter *PluginSpanFilter `json:"plugin_span_filter,omitempty"`
}

// UnmarshalJSON normalizes both supported config shapes into Profiles. A wrapper object
// (one with a "profiles" key) is read directly; any other object is treated as a single
// legacy profile, with its plugin_span_filter hoisted to the shared Config level.
func (c *Config) UnmarshalJSON(data []byte) error {
	// Canonical wrapper shape.
	if node, err := sonic.Get(data, "profiles"); err == nil && node.Exists() {
		type wrapper Config
		var w wrapper
		if err := sonic.Unmarshal(data, &w); err != nil {
			return err
		}
		*c = Config(w)
		// Allow plugin_span_filter to live on the first profile too; hoist it if the
		// top-level field was omitted.
		if c.PluginSpanFilter == nil {
			c.PluginSpanFilter = hoistSpanFilter(data)
		}
		return nil
	}

	// Legacy single-object shape: the whole object is one profile.
	var prof Profile
	if err := sonic.Unmarshal(data, &prof); err != nil {
		return err
	}
	c.Profiles = []*Profile{&prof}
	c.PluginSpanFilter = spanFilterFrom(data)
	return nil
}

// spanFilterCarrier captures only the plugin_span_filter field from a config or profile object.
type spanFilterCarrier struct {
	PluginSpanFilter *PluginSpanFilter `json:"plugin_span_filter,omitempty"`
}

// spanFilterFrom extracts a top-level plugin_span_filter from a JSON object, or nil.
func spanFilterFrom(data []byte) *PluginSpanFilter {
	var c spanFilterCarrier
	if err := sonic.Unmarshal(data, &c); err != nil {
		return nil
	}
	return c.PluginSpanFilter
}

// hoistSpanFilter returns the first plugin_span_filter found among the profiles of a
// wrapper-shaped config, used as a fallback when the top-level field is absent.
func hoistSpanFilter(data []byte) *PluginSpanFilter {
	var w struct {
		Profiles []spanFilterCarrier `json:"profiles"`
	}
	if err := sonic.Unmarshal(data, &w); err != nil {
		return nil
	}
	for _, p := range w.Profiles {
		if p.PluginSpanFilter != nil {
			return p.PluginSpanFilter
		}
	}
	return nil
}

// profileForStorage is the persisted form of a single profile: *SecretVar fields are
// flattened to plain strings ("env.VAR_NAME" or the literal value) for DB/config-file
// persistence.
type profileForStorage struct {
	Enabled                bool              `json:"enabled"`
	ServiceName            string            `json:"service_name"`
	CollectorURL           string            `json:"collector_url"`
	Headers                map[string]string `json:"headers,omitempty"`
	TraceType              TraceType         `json:"trace_type"`
	Protocol               Protocol          `json:"protocol"`
	TLSCACert              string            `json:"tls_ca_cert,omitempty"`
	Insecure               bool              `json:"insecure"`
	MetricsEnabled         bool              `json:"metrics_enabled"`
	MetricsEndpoint        string            `json:"metrics_endpoint,omitempty"`
	MetricsPushInterval    int               `json:"metrics_push_interval,omitempty"`
	RequestHeaders         []string          `json:"request_headers,omitempty"`
	DisableContentLogging  bool              `json:"disable_content_logging,omitempty"`
	GroupTracesBySession   bool              `json:"group_traces_by_session,omitempty"`
	DisableRootSpanContent bool              `json:"disable_root_span_content,omitempty"`
}

// configForStorage is the persisted wrapper shape.
type configForStorage struct {
	Profiles         []profileForStorage `json:"profiles"`
	PluginSpanFilter *PluginSpanFilter   `json:"plugin_span_filter,omitempty"`
}

// MarshalForStorage serializes Config to JSON with *SecretVar fields as plain strings
// ("env.VAR_NAME" or the literal value) for database/config-file persistence. Output is
// always the canonical {"profiles": [...]} wrapper regardless of the input shape.
// For HTTP API responses use json.Marshal directly so clients receive full SecretVar objects.
func (c *Config) MarshalForStorage() ([]byte, error) {
	out := configForStorage{
		Profiles:         make([]profileForStorage, 0, len(c.Profiles)),
		PluginSpanFilter: c.PluginSpanFilter,
	}
	for _, p := range c.Profiles {
		if p == nil {
			continue
		}
		out.Profiles = append(out.Profiles, profileForStorage{
			Enabled:                p.Enabled,
			ServiceName:            p.ServiceName,
			CollectorURL:           schemas.SecretVarAsString(p.CollectorURL),
			Headers:                p.Headers,
			TraceType:              p.TraceType,
			Protocol:               p.Protocol,
			TLSCACert:              p.TLSCACert,
			Insecure:               p.Insecure,
			MetricsEnabled:         p.MetricsEnabled,
			MetricsEndpoint:        schemas.SecretVarAsString(p.MetricsEndpoint),
			MetricsPushInterval:    p.MetricsPushInterval,
			RequestHeaders:         p.RequestHeaders,
			DisableContentLogging:  p.DisableContentLogging,
			GroupTracesBySession:   p.GroupTracesBySession,
			DisableRootSpanContent: p.DisableRootSpanContent,
		})
	}
	return sonic.Marshal(out)
}

// Redacted returns a copy of the config with sensitive fields redacted for API responses.
// URLs (CollectorURL, MetricsEndpoint) are not secrets and are returned unchanged so the UI
// can display and re-submit them without failing URL validation. For env var references on
// those fields, only the resolved value is hidden; the env_var name is preserved.
// Header values may carry auth tokens, so literal values are masked while "env." references
// are preserved.
func (c *Config) Redacted() *Config {
	if c == nil {
		return nil
	}
	redacted := &Config{PluginSpanFilter: c.PluginSpanFilter}
	if c.Profiles != nil {
		redacted.Profiles = make([]*Profile, 0, len(c.Profiles))
		for _, p := range c.Profiles {
			if p == nil {
				redacted.Profiles = append(redacted.Profiles, nil)
				continue
			}
			rp := *p
			rp.CollectorURL = hideResolvedEnvValue(p.CollectorURL)
			rp.MetricsEndpoint = hideResolvedEnvValue(p.MetricsEndpoint)
			if p.Headers != nil {
				rp.Headers = make(map[string]string, len(p.Headers))
				for k, v := range p.Headers {
					rp.Headers[k] = redactHeaderValue(v)
				}
			}
			redacted.Profiles = append(redacted.Profiles, &rp)
		}
	}
	return redacted
}

// redactHeaderValue masks a plain-string header value for API responses. "env." references
// are returned unchanged (they are not secrets), while literal values are masked using the
// same scheme as SecretVar.Redacted so the API surface stays consistent.
func redactHeaderValue(v string) string {
	if strings.HasPrefix(v, "env.") {
		return v
	}
	return schemas.SecretVarAsString(schemas.NewSecretVar(v).Redacted())
}

// hideResolvedEnvValue returns v unchanged for literal values (URLs are not secrets).
// For env var references it replaces a resolved Val with a redaction marker so API
// consumers can tell the value exists without leaking env content. Unresolved env
// references keep an empty Val, while preserving env_var for round-trip edits.
func hideResolvedEnvValue(v *schemas.SecretVar) *schemas.SecretVar {
	if v == nil || !v.IsFromSecret() {
		return v
	}
	return v.Redacted()
}

// otelTarget is the runtime state for a single configured profile: one trace client
// plus an optional metrics exporter, along with the per-profile identity (service name)
// used when converting traces for this destination.
type otelTarget struct {
	serviceName            string
	url                    string
	traceType              TraceType
	client                 OtelClient
	metricsExporter        *MetricsExporter
	requestHeaders         []string
	disableContentLogging  bool
	groupTracesBySession   bool
	disableRootSpanContent bool
}

// OtelPlugin is the plugin for OpenTelemetry.
// It implements the ObservabilityPlugin interface to receive completed traces
// from the tracing middleware and forward them to one or more OTEL collectors.
type OtelPlugin struct {
	ctx    context.Context
	cancel context.CancelFunc

	// targets holds one runtime per configured profile. Each completed trace is exported
	// to every target's collector, and metrics are recorded against every target's exporter.
	targets []*otelTarget

	bifrostVersion string

	attributesFromEnvironment []*commonpb.KeyValue
	instanceAttrs             []*commonpb.KeyValue // machine ID + pod labels, added only to root spans

	pricingManager *modelcatalog.ModelCatalog

	pluginSpanFilter *PluginSpanFilter
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
		return nil, fmt.Errorf("at least one otel profile is required")
	}
	if err := config.PluginSpanFilter.Validate(); err != nil {
		return nil, err
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

	// Build instance-level attrs (machine ID + pod labels) — added only to root spans
	instanceAttrs := make([]*commonpb.KeyValue, 0)
	if hostname, herr := os.Hostname(); herr == nil && hostname != "" {
		instanceAttrs = append(instanceAttrs, kvStr("service.instance.id", hostname))
	}
	if podName := firstNonEmpty(os.Getenv("MY_POD_NAME"), os.Getenv("POD_NAME")); podName != "" {
		instanceAttrs = append(instanceAttrs, kvStr("k8s.pod.name", podName))
	}
	if podNamespace := firstNonEmpty(os.Getenv("MY_POD_NAMESPACE"), os.Getenv("POD_NAMESPACE"), os.Getenv("NAMESPACE")); podNamespace != "" {
		instanceAttrs = append(instanceAttrs, kvStr("k8s.namespace.name", podNamespace))
	}
	if nodeName := firstNonEmpty(os.Getenv("MY_NODE_NAME"), os.Getenv("NODE_NAME")); nodeName != "" {
		instanceAttrs = append(instanceAttrs, kvStr("k8s.node.name", nodeName))
	}
	// Preparing the plugin
	p := &OtelPlugin{
		pricingManager:            pricingManager,
		bifrostVersion:            bifrostVersion,
		attributesFromEnvironment: attributesFromEnvironment,
		instanceAttrs:             instanceAttrs,
		pluginSpanFilter:          config.PluginSpanFilter,
	}
	p.ctx, p.cancel = context.WithCancel(ctx)

	for i, profile := range config.Profiles {
		// A disabled profile exports nothing — skip building its client/exporter entirely.
		if profile != nil && !profile.Enabled {
			logger.Info("OTEL profile %d is disabled, skipping", i)
			continue
		}
		target, err := p.buildTarget(i, profile)
		if err != nil {
			// Tear down any targets already initialized so we don't leak clients/exporters.
			_ = p.Cleanup()
			return nil, err
		}
		p.targets = append(p.targets, target)
	}

	return p, nil
}

// buildTarget constructs the runtime for a single profile: it resolves headers, validates
// the protocol, opens the trace client, and (when enabled) starts the metrics exporter.
func (p *OtelPlugin) buildTarget(index int, profile *Profile) (*otelTarget, error) {
	if profile == nil {
		return nil, fmt.Errorf("profile %d is nil", index)
	}
	if profile.CollectorURL == nil || profile.CollectorURL.GetValue() == "" {
		return nil, fmt.Errorf("profile %d: collector url is required", index)
	}

	serviceName := profile.ServiceName
	if serviceName == "" {
		serviceName = "bifrost"
	}

	// Copy headers before resolving so the stored config is never mutated, then resolve
	// any "env." references against the environment (errors if a referenced var is unset).
	headers := make(map[string]string, len(profile.Headers))
	maps.Copy(headers, profile.Headers)
	if err := injectEnvToHeaders(headers); err != nil {
		return nil, fmt.Errorf("profile %d: %w", index, err)
	}

	url := profile.CollectorURL.GetValue()
	target := &otelTarget{
		serviceName:            serviceName,
		url:                    url,
		traceType:              profile.TraceType,
		requestHeaders:         slices.Clone(profile.RequestHeaders),
		disableContentLogging:  profile.DisableContentLogging,
		groupTracesBySession:   profile.GroupTracesBySession,
		disableRootSpanContent: profile.DisableRootSpanContent,
	}

	var err error
	switch profile.Protocol {
	case ProtocolGRPC:
		target.client, err = NewOtelClientGRPC(url, headers, profile.TLSCACert, profile.Insecure)
	case ProtocolHTTP:
		target.client, err = NewOtelClientHTTP(url, headers, profile.TLSCACert, profile.Insecure)
	default:
		return nil, fmt.Errorf("profile %d: invalid protocol type %q", index, profile.Protocol)
	}
	if err != nil {
		return nil, fmt.Errorf("profile %d: %w", index, err)
	}

	// Initialize metrics exporter if enabled
	if profile.MetricsEnabled {
		if profile.MetricsEndpoint.GetValue() == "" {
			target.client.Close()
			return nil, fmt.Errorf("profile %d: metrics_endpoint is required when metrics_enabled is true", index)
		}
		pushInterval := profile.MetricsPushInterval
		if pushInterval <= 0 {
			pushInterval = 15 // default 15 seconds
		} else if pushInterval > 300 {
			target.client.Close()
			return nil, fmt.Errorf("profile %d: metrics_push_interval must be between 1 and 300 seconds, got %d", index, pushInterval)
		}
		metricsConfig := &MetricsConfig{
			ServiceName:  serviceName,
			Endpoint:     profile.MetricsEndpoint.GetValue(),
			Headers:      headers,
			Protocol:     profile.Protocol,
			TLSCACert:    profile.TLSCACert,
			Insecure:     profile.Insecure,
			PushInterval: pushInterval,
		}
		target.metricsExporter, err = NewMetricsExporter(p.ctx, metricsConfig)
		if err != nil {
			// Clean up trace client if metrics exporter fails
			if target.client != nil {
				target.client.Close()
			}
			return nil, fmt.Errorf("profile %d: failed to initialize metrics exporter: %w", index, err)
		}
		logger.Info("OTEL metrics push enabled for profile %d, pushing to %s every %d seconds", index, profile.MetricsEndpoint.GetValue(), pushInterval)
	}

	return target, nil
}

// GetName function for the OTEL plugin
func (p *OtelPlugin) GetName() string {
	return PluginName
}

// MarshalConfigForStorage implements schemas.ConfigMarshallerPlugin.
func (p *OtelPlugin) MarshalConfigForStorage(raw map[string]any) (map[string]any, error) {
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
func (p *OtelPlugin) RedactConfig(raw map[string]any) (map[string]any, error) {
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

// Ensure OtelPlugin satisfies the marshaller contract, and self-register it so the
// server can redact/normalize this plugin's stored config even when it is disabled.
var _ schemas.ConfigMarshallerPlugin = (*OtelPlugin)(nil)

func init() {
	schemas.RegisterConfigMarshaller(PluginName, &OtelPlugin{})
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

// PreRequestHook implements schemas.LLMPlugin (no-op — required for plugin indexing).
func (p *OtelPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook is a no-op - tracing is handled via the Inject method.
// The OTEL plugin receives completed traces from TracingMiddleware.
func (p *OtelPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook records the cache-hit metric. Every other metric is derived from the
// completed trace in recordMetricsFromTrace, but semantic-cache hits short-circuit the
// request in a PreHook before any llm.call span exists, so the cache signal never reaches
// a span. We therefore read CacheDebug straight off the response here, mirroring how the
// Prometheus telemetry plugin and the Datadog plugin emit this metric.
//
// This is the ONLY place RecordCacheHit is called — do not also emit it from
// recordMetricsFromTrace, or cache hits will double-count.
func (p *OtelPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if resp == nil || !p.anyMetricsEnabled() {
		return resp, bifrostErr, nil
	}
	extra := resp.GetExtraFields()
	if extra == nil || extra.CacheDebug == nil || !extra.CacheDebug.CacheHit {
		return resp, bifrostErr, nil
	}

	cacheType := "unknown"
	if extra.CacheDebug.HitType != nil && *extra.CacheDebug.HitType != "" {
		cacheType = *extra.CacheDebug.HitType
	}

	// Same dimensions as the trace-derived metrics (so the cache-hit counter shares labels
	// with every other bifrost_* OTEL metric), but sourced from context — a short-circuited
	// cache hit has no span to read.
	attrs := append(buildContextAttrs(ctx, resp, bifrostErr), attribute.String("cache_type", cacheType))

	for _, t := range p.targets {
		if t.metricsExporter != nil {
			t.metricsExporter.RecordCacheHit(ctx, attrs...)
		}
	}

	return resp, bifrostErr, nil
}

// anyMetricsEnabled reports whether at least one profile has a metrics exporter running.
func (p *OtelPlugin) anyMetricsEnabled() bool {
	for _, t := range p.targets {
		if t.metricsExporter != nil {
			return true
		}
	}
	return false
}

// RecordHTTPMetrics records HTTP-layer metrics (request count, duration, request/response
// sizes) against every profile's metrics exporter. The HTTP transport's middleware calls
// this once per completed request; it is a no-op when no profile has metrics enabled.
// Non-positive sizes are skipped (fasthttp reports -1 when Content-Length is unknown).
func (p *OtelPlugin) RecordHTTPMetrics(ctx context.Context, path, method, status string, durationSeconds, requestSizeBytes, responseSizeBytes float64) {
	if !p.anyMetricsEnabled() {
		return
	}
	attrs := BuildHTTPAttributes(path, method, status)
	for _, t := range p.targets {
		if t.metricsExporter == nil {
			continue
		}
		t.metricsExporter.RecordHTTPRequest(ctx, attrs...)
		t.metricsExporter.RecordHTTPRequestDuration(ctx, durationSeconds, attrs...)
		if requestSizeBytes > 0 {
			t.metricsExporter.RecordHTTPRequestSize(ctx, requestSizeBytes, attrs...)
		}
		if responseSizeBytes > 0 {
			t.metricsExporter.RecordHTTPResponseSize(ctx, responseSizeBytes, attrs...)
		}
	}
}

// Inject receives a completed trace and sends it to the OTEL collector.
// Implements schemas.ObservabilityPlugin interface.
// This method is called asynchronously by TracingMiddleware after the response
// has been written to the client.
func (p *OtelPlugin) Inject(ctx context.Context, trace *schemas.Trace) error {
	if trace == nil {
		return nil
	}
	// Emit the trace to every configured profile's collector, and record metrics against
	// each profile's exporter. Conversion is per-target because the resource service name
	// differs per profile; everything else (filter, instance attrs) is shared.
	var wg sync.WaitGroup
	for _, t := range p.targets {
		wg.Add(1)
		go func(t *otelTarget) {
			defer wg.Done()
			if t.client != nil {
				resourceSpan := p.convertTraceToResourceSpan(t.serviceName, trace, t.requestHeaders, t.disableContentLogging, t.groupTracesBySession, t.disableRootSpanContent)
				if err := t.client.Emit(ctx, []*ResourceSpan{resourceSpan}); err != nil {
					logger.Error("failed to emit trace %s to %s: %v", trace.TraceID, t.url, err)
				}
			}
			if t.metricsExporter != nil {
				p.recordMetricsFromTrace(ctx, t.metricsExporter, trace)
				p.recordMCPMetricsFromTrace(ctx, t.metricsExporter, trace)
			}
		}(t)
	}
	wg.Wait()
	return nil
}

// RequestHeaderPatterns returns the deduplicated union of request-header name patterns
// across all enabled profiles. The tracing middleware uses this to capture matching
// headers onto the trace; each profile filters to its own subset at conversion time.
func (p *OtelPlugin) RequestHeaderPatterns() []string {
	seen := make(map[string]struct{})
	var patterns []string
	for _, t := range p.targets {
		for _, h := range t.requestHeaders {
			normalized := strings.ToLower(strings.TrimSpace(h))
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			patterns = append(patterns, normalized)
		}
	}
	return patterns
}

// Helper functions for type-safe attribute extraction from trace spans
func getStringAttr(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	if v, ok := attrs[key].(string); ok {
		return v
	}
	return ""
}

func getIntAttr(attrs map[string]any, key string) int {
	if attrs == nil {
		return 0
	}
	switch v := attrs[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func getFloat64Attr(attrs map[string]any, key string) float64 {
	if attrs == nil {
		return 0
	}
	switch v := attrs[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// getFloat64AttrOK is getFloat64Attr with presence reporting. Needed where zero
// is a meaningful value distinct from "absent" — an upstream total of 0 (a cache
// hit, or a request rejected before any provider call) means all of the elapsed
// time was Bifrost's, whereas a missing attribute means it was never measured.
// overheadSecondsFromTrace reads Bifrost's own cost off the root span, where the
// tracer stamps it as AttrBifrostOverheadDurationMs (root duration minus the
// upstream total). Reading the stamped value rather than recomputing keeps the
// overhead metric and the span attribute in lockstep. Returns false when the
// request carried no measurement; absent must not be reported as zero overhead.
func overheadSecondsFromTrace(trace *schemas.Trace) (float64, bool) {
	if trace == nil || trace.RootSpan == nil {
		return 0, false
	}
	overheadMs, ok := getFloat64AttrOK(trace.RootSpan.Attributes, schemas.AttrBifrostOverheadDurationMs)
	if !ok {
		return 0, false
	}
	return overheadMs / 1000.0, true
}

func getFloat64AttrOK(attrs map[string]any, key string) (float64, bool) {
	if attrs == nil {
		return 0, false
	}
	switch v := attrs[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// buildSpanAttrs extracts metric dimension attrs from a single attempt span.
func buildSpanAttrs(span *schemas.Span) []attribute.KeyValue {
	attrs := span.Attributes
	method := getStringAttr(attrs, "request.type")
	if method == "" {
		method = span.Name
	}
	return BuildBifrostAttributes(
		getStringAttr(attrs, schemas.AttrProviderName),
		getStringAttr(attrs, schemas.AttrRequestModel),
		method,
		getStringAttr(attrs, schemas.AttrVirtualKeyID),
		getStringAttr(attrs, schemas.AttrVirtualKeyName),
		getStringAttr(attrs, schemas.AttrSelectedKeyID),
		getStringAttr(attrs, schemas.AttrSelectedKeyName),
		getIntAttr(attrs, schemas.AttrFallbackIndex),
		getStringAttr(attrs, schemas.AttrTeamID),
		getStringAttr(attrs, schemas.AttrTeamName),
		getStringAttr(attrs, schemas.AttrCustomerID),
		getStringAttr(attrs, schemas.AttrCustomerName),
	)
}

// buildContextAttrs builds the same metric dimension attrs as buildSpanAttrs, but sourced
// from the request context and response instead of a completed span. Used by hook-based
// metrics (e.g. cache hits) that fire without a provider-attempt span to read from.
func buildContextAttrs(ctx context.Context, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) []attribute.KeyValue {
	requestType, provider, originalModel, resolvedModel := bifrost.GetResponseFields(resp, bifrostErr)
	model := originalModel
	if resolvedModel != "" {
		model = resolvedModel
	}
	return BuildBifrostAttributes(
		string(provider),
		model,
		string(requestType),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyName),
		bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyFallbackIndex),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamName),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID),
		bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerName),
	)
}

// buildMCPSpanAttrs builds the duration-metric dimensions: semconv attrs + governance
// identity. error.type is appended by the caller on failure. Empty optionals skipped.
func buildMCPSpanAttrs(span *schemas.Span) []attribute.KeyValue {
	attrs := span.Attributes
	out := []attribute.KeyValue{
		attribute.String(schemas.AttrMCPMethodName, getStringAttr(attrs, schemas.AttrMCPMethodName)),
	}
	if tool := getStringAttr(attrs, schemas.AttrToolName); tool != "" {
		out = append(out, attribute.String(schemas.AttrToolName, tool))
	}
	if transport := getStringAttr(attrs, schemas.AttrNetworkTransport); transport != "" {
		out = append(out, attribute.String(schemas.AttrNetworkTransport, transport))
	}
	// Governance identity: bifrost.* span attrs → flat metric label names.
	for spanKey, labelKey := range mcpGovernanceLabelMap {
		if v := getStringAttr(attrs, spanKey); v != "" {
			out = append(out, attribute.String(labelKey, v))
		}
	}
	return out
}

// mcpGovernanceLabelMap maps bifrost.* span attr keys to the flat metric label names.
var mcpGovernanceLabelMap = map[string]string{
	schemas.AttrBifrostVirtualKeyID:     "virtual_key_id",
	schemas.AttrBifrostVirtualKeyName:   "virtual_key_name",
	schemas.AttrBifrostTeamID:           "team_id",
	schemas.AttrBifrostTeamName:         "team_name",
	schemas.AttrBifrostCustomerID:       "customer_id",
	schemas.AttrBifrostCustomerName:     "customer_name",
	schemas.AttrBifrostBusinessUnitID:   "business_unit_id",
	schemas.AttrBifrostBusinessUnitName: "business_unit_name",
}

// recordMCPMetricsFromTrace records the duration metric once per MCP client span. Called
// from Inject alongside recordMetricsFromTrace.
func (p *OtelPlugin) recordMCPMetricsFromTrace(ctx context.Context, exporter *MetricsExporter, trace *schemas.Trace) {
	if trace == nil || exporter == nil {
		return
	}
	for _, span := range trace.Spans {
		// Both MCP kinds are client operations for mcp.client.operation.duration:
		// SpanKindMCPTool (tool calls) and SpanKindMCPClient (ping/list_tools/connect).
		if span == nil || (span.Kind != schemas.SpanKindMCPClient && span.Kind != schemas.SpanKindMCPTool) {
			continue
		}
		// Skip un-enriched spans so we never emit an empty mcp.method.name dimension.
		if getStringAttr(span.Attributes, schemas.AttrMCPMethodName) == "" {
			continue
		}
		mcpAttrs := buildMCPSpanAttrs(span)
		if span.Status == schemas.SpanStatusError {
			errorType := getStringAttr(span.Attributes, schemas.AttrErrorTypeSpec)
			if errorType == "" {
				errorType = "_OTHER"
			}
			mcpAttrs = append(mcpAttrs, attribute.String(schemas.AttrErrorTypeSpec, errorType))
		}
		// Prefer tool-execution (CallTool) latency over span wall-time (which covers PostHooks).
		// Fall back to wall-time when it's absent (e.g. the op failed before returning one).
		var durationSeconds float64
		if toolMs := getIntAttr(span.Attributes, schemas.AttrBifrostMCPToolDurationMs); toolMs > 0 {
			durationSeconds = float64(toolMs) / 1000.0
		} else if !span.StartTime.IsZero() && !span.EndTime.IsZero() {
			durationSeconds = span.EndTime.Sub(span.StartTime).Seconds()
		}
		exporter.RecordMCPOperationDuration(ctx, durationSeconds, mcpAttrs...)
	}
}

// recordMetricsFromTrace extracts metrics data from a completed trace and records them
// via the OTEL metrics exporter. This is called from Inject after trace emission.
//
// Per-attempt metrics (upstream_requests, errors, success, latency) are recorded once
// per llm.call/retry span so fallback attempts and failed retries are counted with
// their own provider/model/fallback_index labels. Per-trace metrics (tokens, cost,
// TTFT) are recorded once, keyed off the final (latest) attempt span.
func (p *OtelPlugin) recordMetricsFromTrace(ctx context.Context, exporter *MetricsExporter, trace *schemas.Trace) {
	if trace == nil || exporter == nil {
		return
	}

	var finalSpan *schemas.Span
	for _, span := range trace.Spans {
		if span.Kind != schemas.SpanKindLLMCall && span.Kind != schemas.SpanKindRetry {
			continue
		}

		spanAttrs := buildSpanAttrs(span)

		exporter.RecordUpstreamRequest(ctx, spanAttrs...)

		if !span.StartTime.IsZero() && !span.EndTime.IsZero() {
			latencySeconds := span.EndTime.Sub(span.StartTime).Seconds()
			exporter.RecordUpstreamLatency(ctx, latencySeconds, spanAttrs...)
		}

		if span.Status == schemas.SpanStatusError {
			statusCode := "unknown"
			if code := getIntAttr(span.Attributes, schemas.AttrHTTPResponseStatusCode); code != 0 {
				statusCode = strconv.Itoa(code)
			}
			errorAttrs := append(spanAttrs[:len(spanAttrs):len(spanAttrs)], attribute.String("status_code", statusCode))
			exporter.RecordErrorRequest(ctx, errorAttrs...)
		} else {
			exporter.RecordSuccessRequest(ctx, spanAttrs...)
		}

		if finalSpan == nil || span.EndTime.After(finalSpan.EndTime) {
			finalSpan = span
		}
	}

	// Bifrost's own overhead. Derived once per trace from the root span, which is
	// the only span whose duration covers the whole request — including queue
	// wait, plugin hooks and transport work that no llm.call span sees.
	//
	// Labelled off the final attempt span so provider/model dimensions match the
	// other per-request metrics (retries, tokens, cost). The root span carries
	// only HTTP attributes.
	if overheadSeconds, ok := overheadSecondsFromTrace(trace); ok {
		labelSpan := finalSpan
		if labelSpan == nil {
			labelSpan = trace.RootSpan
		}
		exporter.RecordOverheadLatency(ctx, overheadSeconds, buildSpanAttrs(labelSpan)...)
	}

	if finalSpan == nil {
		finalSpan = trace.RootSpan
	}
	if finalSpan == nil {
		return
	}

	attrs := finalSpan.Attributes
	otelAttrs := buildSpanAttrs(finalSpan)

	// Record retries used for this request. Read off the final span (the last attempt's
	// attempt index) so the value is "total retries used", matching the Prometheus side.
	retries := getIntAttr(attrs, schemas.AttrNumberOfRetries)
	exporter.RecordRequestRetries(ctx, float64(retries), otelAttrs...)

	// Record token usage - try both naming conventions
	inputTokens := getIntAttr(attrs, schemas.AttrPromptTokens)
	if inputTokens == 0 {
		inputTokens = getIntAttr(attrs, schemas.AttrInputTokens)
	}
	if inputTokens > 0 {
		exporter.RecordInputTokens(ctx, int64(inputTokens), otelAttrs...)
	}

	outputTokens := getIntAttr(attrs, schemas.AttrCompletionTokens)
	if outputTokens == 0 {
		outputTokens = getIntAttr(attrs, schemas.AttrOutputTokens)
	}
	if outputTokens > 0 {
		exporter.RecordOutputTokens(ctx, int64(outputTokens), otelAttrs...)
	}

	// Record cost if available
	cost := getFloat64Attr(attrs, schemas.AttrUsageCost)
	if cost > 0 {
		exporter.RecordCost(ctx, cost, otelAttrs...)
	}

	// Record streaming latency metrics if available
	ttft := getFloat64Attr(attrs, schemas.AttrTimeToFirstToken)
	if ttft > 0 {
		// Convert from nanoseconds to seconds if needed (check the unit)
		exporter.RecordStreamFirstTokenLatency(ctx, ttft/1e9, otelAttrs...)
	}

	// Record provider-side prompt cache tokens (cache_read / cache_creation). Unlike the
	// cache-hit counter, these ride real upstream calls, so the values are on the final
	// attempt span just like input/output tokens. The read/write totals share unified
	// span-attr keys across the chat and responses APIs; the 5m/1h breakdown uses
	// API-family-specific keys that are mutually exclusive per request, so a fallback read
	// covers both.
	if n := getIntAttr(attrs, schemas.AttrUsageCacheReadInputTokens); n > 0 {
		exporter.RecordCacheReadInputTokens(ctx, int64(n), otelAttrs...)
	}
	if n := getIntAttr(attrs, schemas.AttrUsageCacheCreationInputTokens); n > 0 {
		exporter.RecordCacheWriteInputTokens(ctx, int64(n), otelAttrs...)
	}
	cacheWrite5m := getIntAttr(attrs, schemas.AttrPromptTokenDetailsCachedWrite5m)
	if cacheWrite5m == 0 {
		cacheWrite5m = getIntAttr(attrs, schemas.AttrInputTokenDetailsCachedWrite5m)
	}
	if cacheWrite5m > 0 {
		exporter.RecordCacheWriteInputTokens5m(ctx, int64(cacheWrite5m), otelAttrs...)
	}
	cacheWrite1h := getIntAttr(attrs, schemas.AttrPromptTokenDetailsCachedWrite1h)
	if cacheWrite1h == 0 {
		cacheWrite1h = getIntAttr(attrs, schemas.AttrInputTokenDetailsCachedWrite1h)
	}
	if cacheWrite1h > 0 {
		exporter.RecordCacheWriteInputTokens1h(ctx, int64(cacheWrite1h), otelAttrs...)
	}
}

// Cleanup function for the OTEL plugin. It shuts down every profile's metrics exporter
// and closes every trace client, returning the first client-close error encountered.
func (p *OtelPlugin) Cleanup() error {
	if p.cancel != nil {
		p.cancel()
	}
	var firstErr error
	for _, t := range p.targets {
		// Shutdown metrics exporter first
		if t.metricsExporter != nil {
			if err := t.metricsExporter.Shutdown(context.Background()); err != nil {
				logger.Error("failed to shutdown metrics exporter: %v", err)
			}
		}
		if t.client != nil {
			if err := t.client.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// GetMetricsExporter returns the first profile's metrics exporter for external use
// (e.g., by the telemetry plugin). Returns nil if no profile has metrics enabled.
func (p *OtelPlugin) GetMetricsExporter() *MetricsExporter {
	for _, t := range p.targets {
		if t.metricsExporter != nil {
			return t.metricsExporter
		}
	}
	return nil
}

// firstNonEmpty returns the first non-empty string from the provided values.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Compile-time check that OtelPlugin implements ObservabilityPlugin
var _ schemas.ObservabilityPlugin = (*OtelPlugin)(nil)
