package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/network"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/valyala/fasthttp"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Client gateway telemetry: a client's OTLP/HTTP exports are stamped with the
// Bifrost identity it signed in as and forwarded to the operator's collectors.
const (
	// Resource attributes Bifrost stamps on every forwarded export. Any copy a
	// client sent, at any level, is removed first, so a client cannot pose as
	// another identity.
	telemetryAttrGatewayClient = "bifrost.gateway.client"
	telemetryAttrAuthMode      = "bifrost.auth.mode"
	telemetryAttrVirtualKeyID  = "bifrost.virtual_key.id"
	telemetryAttrVirtualKey    = "bifrost.virtual_key.name"
	telemetryAttrTeamID        = "bifrost.team.id"
	telemetryAttrCustomerID    = "bifrost.customer.id"
	telemetryAttrEndUserID     = "enduser.id"

	// clientGatewayTelemetryMaxBody caps one export as received (413 beyond it).
	clientGatewayTelemetryMaxBody = 4 << 20
	// clientGatewayTelemetryMaxDecoded caps a gzip export once decompressed.
	clientGatewayTelemetryMaxDecoded = 16 << 20
	// clientGatewayTelemetryQueueItems and clientGatewayTelemetryQueueBytes bound
	// the forwarding queue shared by every gateway; past either, new exports are
	// dropped (and counted) while the client still gets 200.
	clientGatewayTelemetryQueueItems = 10000
	clientGatewayTelemetryQueueBytes = 64 << 20
	// clientGatewayTelemetryWorkers forward exports concurrently.
	clientGatewayTelemetryWorkers = 4
	// clientGatewayTelemetryExportTimeout bounds one POST to one collector.
	clientGatewayTelemetryExportTimeout = 10 * time.Second
	// clientGatewayTelemetryIdentityLimit caps exports per signed-in identity per
	// minute (per process). Clients flush every few seconds per signal, so this
	// only stops a runaway or hostile session from filling the queue.
	clientGatewayTelemetryIdentityLimit = 600
)

// clientGatewayIdentity is the identity a gateway bearer resolved to, as
// telemetry stamps it.
type clientGatewayIdentity struct {
	Mode       string
	Subject    string
	VKID       string
	VKName     string
	TeamID     string
	CustomerID string
}

// clientGatewayClaimsKey holds the verified gateway token claims the bearer
// check leaves on the request.
type clientGatewayClaimsKey struct{}

// telemetryIdentity builds the identity to stamp from the claims the bearer
// check verified, looking up the virtual key's name and owner for a vk-mode
// token (a governance cache hit in a running server). It is built here, not in
// the bearer check, so inference requests never pay for it. It returns nil
// when no verified claims are on the request.
func (ep *clientGatewayEndpoint) telemetryIdentity(ctx *fasthttp.RequestCtx) *clientGatewayIdentity {
	claims, _ := ctx.UserValue(clientGatewayClaimsKey{}).(*jwtMCPClaims)
	if claims == nil {
		return nil
	}
	id := &clientGatewayIdentity{Mode: claims.BfMode, Subject: claims.Subject}
	if claims.BfMode == "vk" {
		id.VKID = claims.Subject
		if vk, err := ep.virtualKeyByID(ctx, claims.Subject); err == nil && vk != nil {
			id.VKName, id.TeamID, id.CustomerID = vk.Name, derefString(vk.TeamID), derefString(vk.CustomerID)
		}
	}
	return id
}

// telemetryConfig returns this gateway's telemetry block under the read lock.
func (ep *clientGatewayEndpoint) telemetryConfig() *configtables.ClientGatewayTelemetryConfig {
	ep.store.Mu.RLock()
	defer ep.store.Mu.RUnlock()
	if ep.store.ClientConfig == nil {
		return nil
	}
	return ep.profile.Telemetry(ep.store.ClientConfig.OAuth2ServerConfig)
}

// --- POST {prefix}/v1/{metrics,logs,traces} ---

// telemetryHandler accepts one OTLP/HTTP signal behind the gateway bearer. With
// telemetry switched off the export is acknowledged and discarded, since the
// client's exporter logs an error on every flush for anything but 2xx. With it
// on, the export is decoded (protobuf or JSON, optionally gzip), stamped with
// the signed-in identity, stripped of the configured attributes, re-encoded as
// protobuf and queued for the forwarder; the client is answered as soon as it
// is queued, so a slow collector never slows the session.
func (ep *clientGatewayEndpoint) telemetryHandler(signal string) func(*fasthttp.RequestCtx) {
	return func(ctx *fasthttp.RequestCtx) {
		isJSON := strings.HasPrefix(strings.ToLower(string(ctx.Request.Header.ContentType())), "application/json")
		cfg := ep.telemetryConfig()
		if cfg == nil || !cfg.Enabled {
			ep.telemetry.metrics.exports.WithLabelValues(ep.profile.Name(), signal, "discarded").Inc()
			writeOTLPSuccess(ctx, isJSON)
			return
		}
		if len(ctx.PostBody()) > clientGatewayTelemetryMaxBody {
			ep.telemetry.metrics.exports.WithLabelValues(ep.profile.Name(), signal, "too_large").Inc()
			ctx.SetStatusCode(fasthttp.StatusRequestEntityTooLarge)
			return
		}
		id := ep.telemetryIdentity(ctx)
		if id != nil && !ep.telemetry.identityLimiter.allow(id.Mode+":"+id.Subject, time.Now()) {
			ep.telemetry.metrics.exports.WithLabelValues(ep.profile.Name(), signal, "rate_limited").Inc()
			ctx.Response.Header.Set("Retry-After", "60")
			ctx.SetStatusCode(fasthttp.StatusTooManyRequests)
			return
		}
		payload, err := stampTelemetryExport(signal, ctx.PostBody(), isJSON, string(ctx.Request.Header.Peek("Content-Encoding")), ep.profile.Name(), id, cfg.DropAttributes)
		if err != nil {
			outcome := "invalid"
			status := fasthttp.StatusBadRequest
			if errors.Is(err, errTelemetryTooLarge) {
				outcome, status = "too_large", fasthttp.StatusRequestEntityTooLarge
			}
			ep.telemetry.metrics.exports.WithLabelValues(ep.profile.Name(), signal, outcome).Inc()
			ctx.SetStatusCode(status)
			ctx.SetBodyString(err.Error())
			return
		}
		if ep.telemetry.enqueue(telemetryItem{ep: ep, signal: signal, body: payload}) {
			ep.telemetry.metrics.exports.WithLabelValues(ep.profile.Name(), signal, "queued").Inc()
		} else {
			ep.telemetry.metrics.exports.WithLabelValues(ep.profile.Name(), signal, "dropped").Inc()
		}
		writeOTLPSuccess(ctx, isJSON)
	}
}

// writeOTLPSuccess answers an export with an empty Export*ServiceResponse in the
// request's encoding: zero bytes for protobuf, {} for JSON.
func writeOTLPSuccess(ctx *fasthttp.RequestCtx, isJSON bool) {
	ctx.SetStatusCode(fasthttp.StatusOK)
	if isJSON {
		ctx.SetContentType("application/json")
		ctx.SetBodyString("{}")
		return
	}
	ctx.SetContentType("application/x-protobuf")
}

var errTelemetryTooLarge = errors.New("telemetry export is too large")

// stampTelemetryExport decodes one OTLP export, removes every client-sent
// identity attribute and every attribute named in drop, stamps the Bifrost
// identity on each resource, and returns it re-encoded as protobuf.
func stampTelemetryExport(signal string, body []byte, isJSON bool, contentEncoding, gateway string, id *clientGatewayIdentity, drop []string) ([]byte, error) {
	if strings.EqualFold(strings.TrimSpace(contentEncoding), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("invalid gzip body: %w", err)
		}
		decoded, err := io.ReadAll(io.LimitReader(zr, clientGatewayTelemetryMaxDecoded+1))
		if err != nil {
			return nil, fmt.Errorf("invalid gzip body: %w", err)
		}
		if len(decoded) > clientGatewayTelemetryMaxDecoded {
			return nil, errTelemetryTooLarge
		}
		body = decoded
	} else if contentEncoding != "" && !strings.EqualFold(strings.TrimSpace(contentEncoding), "identity") {
		return nil, fmt.Errorf("unsupported content encoding %q", contentEncoding)
	}

	var msg proto.Message
	switch signal {
	case "metrics":
		msg = &colmetricspb.ExportMetricsServiceRequest{}
	case "logs":
		msg = &collogspb.ExportLogsServiceRequest{}
	case "traces":
		msg = &coltracepb.ExportTraceServiceRequest{}
	default:
		return nil, fmt.Errorf("unknown signal %q", signal)
	}
	if isJSON {
		normalized, err := otlpJSONIDsToBase64(body)
		if err != nil {
			return nil, fmt.Errorf("invalid OTLP JSON: %w", err)
		}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(normalized, msg); err != nil {
			return nil, fmt.Errorf("invalid OTLP JSON: %w", err)
		}
	} else if err := proto.Unmarshal(body, msg); err != nil {
		return nil, fmt.Errorf("invalid OTLP protobuf: %w", err)
	}

	dropped := make(map[string]struct{}, len(drop))
	for _, key := range drop {
		dropped[key] = struct{}{}
	}
	keep := func(key string) bool {
		if strings.HasPrefix(key, "bifrost.") || key == telemetryAttrEndUserID {
			return false
		}
		_, gone := dropped[key]
		return !gone
	}
	stamp := telemetryIdentityAttributes(gateway, id)
	stampResource := func(r **resourcepb.Resource) {
		if *r == nil {
			*r = &resourcepb.Resource{}
		}
		(*r).Attributes = append(filterTelemetryAttributes((*r).Attributes, keep), stamp...)
	}

	switch m := msg.(type) {
	case *colmetricspb.ExportMetricsServiceRequest:
		for _, rm := range m.ResourceMetrics {
			stampResource(&rm.Resource)
			for _, sm := range rm.ScopeMetrics {
				if sm.Scope != nil {
					sm.Scope.Attributes = filterTelemetryAttributes(sm.Scope.Attributes, keep)
				}
				for _, metric := range sm.Metrics {
					if g := metric.GetGauge(); g != nil {
						for _, dp := range g.DataPoints {
							dp.Attributes = filterTelemetryAttributes(dp.Attributes, keep)
						}
					}
					if s := metric.GetSum(); s != nil {
						for _, dp := range s.DataPoints {
							dp.Attributes = filterTelemetryAttributes(dp.Attributes, keep)
						}
					}
					if h := metric.GetHistogram(); h != nil {
						for _, dp := range h.DataPoints {
							dp.Attributes = filterTelemetryAttributes(dp.Attributes, keep)
						}
					}
					if h := metric.GetExponentialHistogram(); h != nil {
						for _, dp := range h.DataPoints {
							dp.Attributes = filterTelemetryAttributes(dp.Attributes, keep)
						}
					}
					if s := metric.GetSummary(); s != nil {
						for _, dp := range s.DataPoints {
							dp.Attributes = filterTelemetryAttributes(dp.Attributes, keep)
						}
					}
				}
			}
		}
	case *collogspb.ExportLogsServiceRequest:
		for _, rl := range m.ResourceLogs {
			stampResource(&rl.Resource)
			for _, sl := range rl.ScopeLogs {
				if sl.Scope != nil {
					sl.Scope.Attributes = filterTelemetryAttributes(sl.Scope.Attributes, keep)
				}
				for _, rec := range sl.LogRecords {
					rec.Attributes = filterTelemetryAttributes(rec.Attributes, keep)
				}
			}
		}
	case *coltracepb.ExportTraceServiceRequest:
		for _, rs := range m.ResourceSpans {
			stampResource(&rs.Resource)
			for _, ss := range rs.ScopeSpans {
				if ss.Scope != nil {
					ss.Scope.Attributes = filterTelemetryAttributes(ss.Scope.Attributes, keep)
				}
				for _, span := range ss.Spans {
					span.Attributes = filterTelemetryAttributes(span.Attributes, keep)
					for _, ev := range span.Events {
						ev.Attributes = filterTelemetryAttributes(ev.Attributes, keep)
					}
					for _, link := range span.Links {
						link.Attributes = filterTelemetryAttributes(link.Attributes, keep)
					}
				}
			}
		}
	}
	return proto.Marshal(msg)
}

// filterTelemetryAttributes keeps the attributes keep accepts, in place.
func filterTelemetryAttributes(attrs []*commonpb.KeyValue, keep func(string) bool) []*commonpb.KeyValue {
	out := attrs[:0]
	for _, kv := range attrs {
		if kv != nil && keep(kv.Key) {
			out = append(out, kv)
		}
	}
	return out
}

// telemetryIdentityAttributes is the resource attributes stamped for id. With
// no identity (never the case behind the bearer check) only the gateway is named.
func telemetryIdentityAttributes(gateway string, id *clientGatewayIdentity) []*commonpb.KeyValue {
	attrs := []*commonpb.KeyValue{telemetryStringAttr(telemetryAttrGatewayClient, gateway)}
	if id == nil {
		return attrs
	}
	attrs = append(attrs, telemetryStringAttr(telemetryAttrAuthMode, id.Mode))
	for _, kv := range [][2]string{
		{telemetryAttrVirtualKeyID, id.VKID},
		{telemetryAttrVirtualKey, id.VKName},
		{telemetryAttrTeamID, id.TeamID},
		{telemetryAttrCustomerID, id.CustomerID},
	} {
		if kv[1] != "" {
			attrs = append(attrs, telemetryStringAttr(kv[0], kv[1]))
		}
	}
	if id.Mode == "user" && id.Subject != "" {
		attrs = append(attrs, telemetryStringAttr(telemetryAttrEndUserID, id.Subject))
	}
	return attrs
}

func telemetryStringAttr(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

// otlpJSONIDsToBase64 rewrites the trace and span IDs of an OTLP/JSON export
// from the hex the OTLP JSON encoding uses to the base64 protojson expects for
// bytes fields. Everything else is left as sent.
func otlpJSONIDsToBase64(body []byte) ([]byte, error) {
	var doc any
	if err := sonic.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				switch k {
				case "traceId", "spanId", "parentSpanId", "trace_id", "span_id", "parent_span_id":
					if s, ok := child.(string); ok && s != "" {
						if raw, err := hex.DecodeString(s); err == nil {
							t[k] = base64.StdEncoding.EncodeToString(raw)
						}
					}
				default:
					walk(child)
				}
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(doc)
	return sonic.Marshal(doc)
}

// --- Forwarder ---

// telemetryItem is one stamped export waiting to be forwarded.
type telemetryItem struct {
	ep     *clientGatewayEndpoint
	signal string
	body   []byte
}

// telemetryForwarder owns the bounded queue and the workers that post queued
// exports to each destination of the gateway they came from. Destinations are
// read from the live config when an export is sent, so a config change applies
// to exports already queued. It is shared by every client gateway.
type telemetryForwarder struct {
	queue    chan telemetryItem
	bytes    atomic.Int64
	maxBytes int64
	client   func() *http.Client

	identityLimiter *ipRateLimiter
	metrics         *telemetryMetrics

	startOnce sync.Once
	closing   atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
	workers   sync.WaitGroup
}

// newTelemetryForwarder creates the forwarder; its workers start with the first
// queued export. httpClient, when non-nil, supplies the outbound client (the
// proxy-aware factory in a running server).
func newTelemetryForwarder(httpClient func() *http.Client) *telemetryForwarder {
	if httpClient == nil {
		fallback := &http.Client{Timeout: clientGatewayTelemetryExportTimeout}
		httpClient = func() *http.Client { return fallback }
	}
	return &telemetryForwarder{
		queue:           make(chan telemetryItem, clientGatewayTelemetryQueueItems),
		maxBytes:        clientGatewayTelemetryQueueBytes,
		client:          httpClient,
		identityLimiter: newIPRateLimiter(clientGatewayTelemetryIdentityLimit, time.Minute),
		metrics:         newTelemetryMetrics(),
		done:            make(chan struct{}),
	}
}

// telemetryHTTPClient returns the outbound client factory for store: the
// proxy-aware API client when the server has a factory, nil otherwise.
func telemetryHTTPClient(factory *network.HTTPClientFactory) func() *http.Client {
	if factory == nil {
		return nil
	}
	return func() *http.Client { return factory.GetHTTPClient(network.ClientPurposeAPI) }
}

// enqueue queues item unless the forwarder is closing or the queue is at its
// item or byte bound, and reports whether it was queued.
func (f *telemetryForwarder) enqueue(item telemetryItem) bool {
	if f.closing.Load() {
		return false
	}
	n := int64(len(item.body))
	for {
		cur := f.bytes.Load()
		if cur+n > f.maxBytes {
			return false
		}
		if f.bytes.CompareAndSwap(cur, cur+n) {
			break
		}
	}
	f.startOnce.Do(f.start)
	select {
	case f.queue <- item:
		f.metrics.queueDepth.Set(float64(len(f.queue)))
		f.metrics.queueBytes.Set(float64(f.bytes.Load()))
		return true
	default:
		f.bytes.Add(-n)
		return false
	}
}

// start launches the workers.
func (f *telemetryForwarder) start() {
	for i := 0; i < clientGatewayTelemetryWorkers; i++ {
		f.workers.Add(1)
		go f.work()
	}
}

// work forwards queued exports until the forwarder closes, then drains what is
// left (bounded by Close's deadline through the per-export timeout).
func (f *telemetryForwarder) work() {
	defer f.workers.Done()
	for {
		select {
		case item := <-f.queue:
			f.forward(item)
		case <-f.done:
			for {
				select {
				case item := <-f.queue:
					f.forward(item)
				default:
					return
				}
			}
		}
	}
}

// forward posts one export to every destination of its gateway.
func (f *telemetryForwarder) forward(item telemetryItem) {
	defer func() {
		f.bytes.Add(-int64(len(item.body)))
		f.metrics.queueDepth.Set(float64(len(f.queue)))
		f.metrics.queueBytes.Set(float64(f.bytes.Load()))
	}()
	gateway := item.ep.profile.Name()
	cfg := item.ep.telemetryConfig()
	if cfg == nil || !cfg.Enabled {
		f.metrics.forwards.WithLabelValues(gateway, item.signal, "skipped").Inc()
		return
	}
	for _, d := range cfg.Destinations {
		if err := f.post(d, item); err != nil {
			f.metrics.forwards.WithLabelValues(gateway, item.signal, "error").Inc()
			logger.Warn("%s gateway: failed to forward %s telemetry: %v", gateway, item.signal, err)
			continue
		}
		f.metrics.forwards.WithLabelValues(gateway, item.signal, "ok").Inc()
	}
}

// post sends one export to one destination as OTLP/HTTP protobuf.
func (f *telemetryForwarder) post(d configtables.ClientGatewayTelemetryDestination, item telemetryItem) error {
	if d.Endpoint == nil {
		return fmt.Errorf("destination has no endpoint")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(d.Endpoint.GetValue()), "/")
	if endpoint == "" {
		return fmt.Errorf("destination endpoint resolved to an empty value")
	}
	ctx, cancel := context.WithTimeout(context.Background(), clientGatewayTelemetryExportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/"+item.signal, bytes.NewReader(item.body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	for name, v := range d.Headers {
		if v != nil {
			req.Header.Set(name, v.GetValue())
		}
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("collector answered %d", resp.StatusCode)
	}
	return nil
}

// Close stops accepting exports and waits for the workers to drain the queue,
// or for ctx to end.
func (f *telemetryForwarder) Close(ctx context.Context) {
	f.closeOnce.Do(func() {
		f.closing.Store(true)
		close(f.done)
	})
	finished := make(chan struct{})
	go func() {
		f.workers.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
	}
}

// telemetryMetrics are the forwarder's Prometheus metrics, on their own registry
// so the server can serve them next to the telemetry plugin's.
type telemetryMetrics struct {
	registry   *prometheus.Registry
	exports    *prometheus.CounterVec
	forwards   *prometheus.CounterVec
	queueDepth prometheus.Gauge
	queueBytes prometheus.Gauge
}

func newTelemetryMetrics() *telemetryMetrics {
	m := &telemetryMetrics{
		registry: prometheus.NewRegistry(),
		exports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bifrost_client_gateway_telemetry_exports_total",
			Help: "Client gateway OTLP exports received, by gateway, signal and outcome (queued, dropped, discarded, rate_limited, too_large, invalid).",
		}, []string{"gateway", "signal", "outcome"}),
		forwards: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bifrost_client_gateway_telemetry_forwards_total",
			Help: "Client gateway OTLP exports forwarded to a collector, by gateway, signal and result (ok, error, skipped).",
		}, []string{"gateway", "signal", "result"}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bifrost_client_gateway_telemetry_queue_depth",
			Help: "Client gateway OTLP exports waiting to be forwarded.",
		}),
		queueBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bifrost_client_gateway_telemetry_queue_bytes",
			Help: "Bytes of client gateway OTLP exports waiting to be forwarded.",
		}),
	}
	m.registry.MustRegister(m.exports, m.forwards, m.queueDepth, m.queueBytes)
	return m
}

// derefString returns *s, or "" for nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
