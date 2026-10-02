// Package main implements a Bifrost plugin that serves Azure AI Voice Live
// realtime models (gpt-realtime, phi4-mm-realtime, ...) over Bifrost's existing
// `GET /v1/realtime` endpoint — using only the HTTP transport plugin hooks.
//
// # The idea
//
// An HTTP-to-WebSocket upgrade is nothing more than a 101 response plus a hijack
// of the underlying TCP socket. fasthttp exposes that as RequestCtx.Hijack, and
// HTTPTransportPreHook runs inside the per-request goroutine that owns the
// connection. So a plugin that can reach the *fasthttp.RequestCtx can perform
// the upgrade itself and own the whole realtime lifecycle — no core change, and
// no dependency on Bifrost having a RealtimeProvider for the target service.
//
// It can. The context handed to HTTPTransportPreHook is a plugin-scoped
// BifrostContext, and WithPluginScope copies the RequestCtx into the scoped
// context's parent while leaving the scoped value map empty. So:
//
//	rctx, ok := ctx.GetParentCtxWithUserValues().(*fasthttp.RequestCtx)
//
// returns the live RequestCtx, on which Hijack is available.
//
// # Flow
//
//  1. HTTPTransportPreHook sees GET /v1/realtime with an Upgrade header.
//  2. If the requested model is not one of ours, return (nil, nil) — Bifrost's
//     own realtime handler serves it exactly as before.
//  3. Otherwise: capacity check, recover the RequestCtx, upgrade the client
//     socket, and return a 101 short-circuit so Bifrost's handler is bypassed.
//  4. In the hijacked goroutine, dial Azure Voice Live and relay frames in both
//     directions until either side closes.
//
// # THE ONE RULE
//
// Never touch the *fasthttp.RequestCtx inside the hijack handler. fasthttp
// recycles it as soon as the request handler returns, which is BEFORE the hijack
// handler runs. Everything the connection needs (model, URL, headers) is
// captured into locals before Upgrade is called. The hijacked net.Conn — and the
// *ws.Conn wrapping it — outlive the RequestCtx and are safe.
//
// # What this does NOT get you
//
// Short-circuiting at the transport layer means Bifrost core never sees the
// request: no PreLLMHook/PostLLMHook, and therefore no governance, no logging,
// no telemetry, no cost tracking for these connections. Session limits and the
// upstream connection pool are Bifrost's, not ours, so this plugin enforces its
// own.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const pluginName = "voicelive-realtime"

const (
	// defaultAPIVersion is the Voice Live API version used when config omits one.
	// Azure revises this; it is configurable precisely because the right value
	// depends on the resource, and a stale default fails the handshake.
	defaultAPIVersion = "2025-10-01"

	defaultDialTimeout  = 10 * time.Second
	defaultIdleTimeout  = 5 * time.Minute
	defaultMaxConns     = 100
	clientPingInterval  = 30 * time.Second
	upstreamCloseGrace  = 2 * time.Second
	realtimeSubprotocol = "realtime"

	// writeTimeout bounds a single relayed frame. Without it a peer that stops
	// reading while it keeps sending blocks the write forever: the source read
	// deadline is refreshed by every successful read, so nothing ever unblocks
	// the relay, and it holds its activeConns slot for the life of the process.
	// Matches realtimeWSWriteTimeout in Bifrost's own realtime handler, and is
	// deliberately much shorter than idleTimeout — an idle connection is normal,
	// a write that cannot drain for 30s is not.
	writeTimeout = 30 * time.Second
)

// Config is this plugin's typed configuration.
type Config struct {
	// Endpoint is the Azure AI resource root, e.g.
	// "https://my-resource.services.ai.azure.com". http(s) is rewritten to ws(s).
	Endpoint string `json:"endpoint"`

	// APIKey authenticates to Voice Live. Declared as *schemas.SecretVar so it
	// can be a literal, "env.NAME", or "vault.path/to/secret".
	APIKey *schemas.SecretVar `json:"api_key"`

	// APIVersion is the Voice Live api-version query parameter.
	APIVersion string `json:"api_version,omitempty"`

	// Models are the model names this plugin claims. A realtime upgrade for any
	// other model falls through to Bifrost's native handler untouched.
	Models []string `json:"models,omitempty"`

	// MaxConnections caps concurrent plugin-owned realtime connections. Bifrost's
	// own session manager does not apply to hijacked sockets, so without this the
	// endpoint is unbounded.
	MaxConnections int `json:"max_connections,omitempty"`

	// AllowedOrigins gates the upgrade by Origin. Empty uses the library's safe
	// default (same-host only); ["*"] allows any origin.
	AllowedOrigins []string `json:"allowed_origins,omitempty"`

	DialTimeoutSeconds int `json:"dial_timeout_seconds,omitempty"`
	IdleTimeoutSeconds int `json:"idle_timeout_seconds,omitempty"`
}

var defaultModels = []string{"gpt-realtime", "phi4-mm-realtime"}

var (
	config   atomic.Pointer[Config]
	upgrader atomic.Pointer[ws.FastHTTPUpgrader]

	activeConns  atomic.Int64
	totalConns   atomic.Int64
	refusedConns atomic.Int64
	failedDials  atomic.Int64

	// shutdown is closed by Cleanup so live relays tear down with the process.
	shutdown   chan struct{}
	shutdownMu sync.Mutex
)

func parseConfig(raw any) (*Config, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if c.APIVersion = strings.TrimSpace(c.APIVersion); c.APIVersion == "" {
		c.APIVersion = defaultAPIVersion
	}
	if len(c.Models) == 0 {
		c.Models = append([]string(nil), defaultModels...)
	}
	normalized := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		if m = stripProviderPrefix(m); m != "" {
			normalized = append(normalized, m)
		}
	}
	c.Models = normalized
	if c.MaxConnections <= 0 {
		c.MaxConnections = defaultMaxConns
	}
	if c.DialTimeoutSeconds <= 0 {
		c.DialTimeoutSeconds = int(defaultDialTimeout / time.Second)
	}
	if c.IdleTimeoutSeconds <= 0 {
		c.IdleTimeoutSeconds = int(defaultIdleTimeout / time.Second)
	}
	return &c, nil
}

// Init validates configuration and builds the upgrader.
func Init(raw any) error {
	c, err := parseConfig(raw)
	if err != nil {
		return fmt.Errorf("%s: failed to parse plugin config: %w", pluginName, err)
	}
	if c.Endpoint == "" {
		return fmt.Errorf("%s: plugin config endpoint is required (e.g. https://<resource>.services.ai.azure.com)", pluginName)
	}
	if c.APIKey.GetValue() == "" {
		return fmt.Errorf("%s: plugin config api_key must resolve to a non-empty value", pluginName)
	}
	// The api-key rides in a header on the upstream hop, so a cleartext endpoint
	// hands it to anyone on the path. Loopback stays allowed so tests and local
	// fakes work; a bare host with no scheme already defaults to wss://.
	if isCleartextEndpoint(c.Endpoint) && !isLoopbackEndpoint(c.Endpoint) {
		return fmt.Errorf("%s: plugin config endpoint must use https:// outside loopback — the api-key is sent on this connection", pluginName)
	}
	config.Store(c)

	allowAll := false
	allowed := make(map[string]struct{}, len(c.AllowedOrigins))
	for _, o := range c.AllowedOrigins {
		o = strings.ToLower(strings.TrimSpace(o))
		if o == "*" {
			allowAll = true
		} else if o != "" {
			allowed[o] = struct{}{}
		}
	}
	up := &ws.FastHTTPUpgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		Subprotocols:    []string{realtimeSubprotocol},
	}
	if allowAll || len(allowed) > 0 {
		up.CheckOrigin = func(ctx *fasthttp.RequestCtx) bool {
			origin := strings.ToLower(string(ctx.Request.Header.Peek("Origin")))
			if origin == "" || allowAll {
				return true
			}
			_, ok := allowed[origin]
			return ok
		}
	}
	upgrader.Store(up)

	shutdownMu.Lock()
	shutdown = make(chan struct{})
	shutdownMu.Unlock()

	fmt.Printf("[%s] initialized: endpoint=%s api_version=%s models=%v max_connections=%d\n",
		pluginName, c.Endpoint, c.APIVersion, c.Models, c.MaxConnections)
	return nil
}

// GetName returns the plugin's system identifier.
func GetName() string {
	return pluginName
}

// HTTPTransportPreHook claims the realtime upgrade for Voice Live models and
// serves it from a hijacked socket. Every other request is passed straight
// through, including realtime upgrades for models Bifrost serves natively.
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	c := config.Load()
	up := upgrader.Load()
	if c == nil || up == nil || !isRealtimeUpgrade(req) {
		return nil, nil
	}

	model := stripProviderPrefix(req.CaseInsensitiveQueryLookup("model"))
	if model == "" {
		model = stripProviderPrefix(req.CaseInsensitiveQueryLookup("deployment"))
	}
	if !contains(c.Models, model) {
		// Not ours. Bifrost's realtime handler serves this exactly as before.
		return nil, nil
	}

	// Bifrost's session manager does not see hijacked sockets, so the cap is ours
	// to enforce. Reserve the slot before upgrading and release it on every path
	// that does not reach the relay.
	if activeConns.Add(1) > int64(c.MaxConnections) {
		activeConns.Add(-1)
		refusedConns.Add(1)
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf("voice-live upgrade refused: at capacity (%d)", c.MaxConnections))
		return jsonResponse(429, "too many concurrent Voice Live connections", map[string]string{"Retry-After": "5"}), nil
	}

	// Recover the live RequestCtx. This is what makes the hijack possible.
	rctx, ok := ctx.GetParentCtxWithUserValues().(*fasthttp.RequestCtx)
	if !ok {
		activeConns.Add(-1)
		refusedConns.Add(1)
		// Fail loudly rather than falling through: Bifrost's native handler has no
		// Voice Live provider, so passing this on would only produce a confusing
		// "provider does not support realtime" further down.
		ctx.Log(schemas.LogLevelError, "cannot reach fasthttp.RequestCtx; socket hijack unavailable")
		return jsonResponse(503, "voice-live bridge cannot access the underlying connection", nil), nil
	}

	// Everything the relay needs is captured HERE, while rctx is still valid.
	// The hijack handler must never read rctx — see the package comment.
	upstreamURL := buildUpstreamURL(c, model, strings.TrimSpace(req.CaseInsensitiveQueryLookup("intent")))
	upstreamHeaders := http.Header{}
	upstreamHeaders.Set("api-key", c.APIKey.GetValue())
	dialTimeout := time.Duration(c.DialTimeoutSeconds) * time.Second
	idleTimeout := time.Duration(c.IdleTimeoutSeconds) * time.Second

	ctx.Log(schemas.LogLevelInfo, fmt.Sprintf("voice-live upgrade claimed: model=%s upstream=%s", model, redactURL(upstreamURL)))

	// On success the hijack handler owns the reserved slot and releases it when the
	// relay ends; on failure it never runs, so the slot is released here instead.
	err := up.Upgrade(rctx, func(client *ws.Conn) {
		defer activeConns.Add(-1)
		defer client.Close()
		totalConns.Add(1)
		runRelay(client, upstreamURL, upstreamHeaders, dialTimeout, idleTimeout, model)
	})
	if err != nil {
		activeConns.Add(-1)
		// Upgrade already wrote its own error response onto rctx.
		return nil, fmt.Errorf("voice-live websocket upgrade failed: %w", err)
	}

	// Short-circuit so the transport stops here and Bifrost's realtime handler is
	// bypassed. Headers and Body are nil on purpose: applyHTTPResponseToCtx only
	// Sets what it is given, so the handshake Upgrade already wrote survives.
	return &schemas.HTTPResponse{StatusCode: fasthttp.StatusSwitchingProtocols}, nil
}

// Cleanup tears down live relays and reports totals.
func Cleanup() error {
	shutdownMu.Lock()
	if shutdown != nil {
		close(shutdown)
		shutdown = nil
	}
	shutdownMu.Unlock()

	fmt.Printf("[%s] cleanup: total=%d active=%d refused=%d failed_dials=%d\n",
		pluginName, totalConns.Load(), activeConns.Load(), refusedConns.Load(), failedDials.Load())
	return nil
}

// --- relay --------------------------------------------------------------------

// runRelay dials Voice Live and pumps frames in both directions until either
// side closes. It runs in the hijacked goroutine, so a panic or a hang here
// affects this connection only — every other request keeps its own goroutine.
func runRelay(client *ws.Conn, upstreamURL string, headers http.Header, dialTimeout, idleTimeout time.Duration, model string) {
	dialer := ws.Dialer{
		HandshakeTimeout: dialTimeout,
		Subprotocols:     []string{realtimeSubprotocol},
	}
	upstream, resp, err := dialer.Dial(upstreamURL, headers)
	if err != nil {
		failedDials.Add(1)
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		// The client socket is already upgraded, so the only way to report is in
		// band, as a realtime error event.
		writeRealtimeError(client, fmt.Sprintf("failed to connect to Azure Voice Live for model %q (upstream status %d): %v", model, status, err))
		return
	}
	defer upstream.Close()

	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	shutdownMu.Lock()
	shutdownCh := shutdown
	shutdownMu.Unlock()

	go pump(client, upstream, idleTimeout, stop)
	go pump(upstream, client, idleTimeout, stop)
	go keepalive(client, done)

	select {
	case <-done:
	case <-shutdownCh:
	}

	// Closing both sockets unblocks whichever pump is still in ReadMessage.
	_ = client.WriteControl(ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseNormalClosure, ""), time.Now().Add(upstreamCloseGrace))
	_ = upstream.WriteControl(ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseNormalClosure, ""), time.Now().Add(upstreamCloseGrace))
}

// pump relays every frame from src to dst, preserving the message type so text
// events and binary audio both survive the hop unchanged.
func pump(src, dst *ws.Conn, idleTimeout time.Duration, stop func()) {
	defer stop()
	for {
		if idleTimeout > 0 {
			if err := src.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
				return
			}
		}
		messageType, message, err := src.ReadMessage()
		if err != nil {
			return
		}
		// Bound the write too. fasthttp/websocket applies this deadline to the
		// underlying conn inside WriteMessage, so a peer that has stopped reading
		// fails the relay instead of parking it forever.
		if err := dst.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return
		}
		if err := dst.WriteMessage(messageType, message); err != nil {
			return
		}
	}
}

// keepalive pings the client so idle sessions are not reaped by intermediaries.
func keepalive(client *ws.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(clientPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := client.WriteControl(ws.PingMessage, nil, time.Now().Add(upstreamCloseGrace)); err != nil {
				return
			}
		}
	}
}

// writeRealtimeError reports a failure in band, in the shape a realtime client
// already knows how to parse.
func writeRealtimeError(client *ws.Conn, message string) {
	payload, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "server_error",
			"code":    "voice_live_upstream_unavailable",
			"message": message,
		},
	})
	if err != nil {
		return
	}
	_ = client.SetWriteDeadline(time.Now().Add(upstreamCloseGrace))
	_ = client.WriteMessage(ws.TextMessage, payload)
}

// --- helpers -------------------------------------------------------------------

// buildUpstreamURL assembles the Voice Live WebSocket URL. Voice Live lives at
// /voice-live/realtime and requires an api-version — which is why Bifrost's
// Azure provider cannot reach it: that provider hardcodes /openai/v1/realtime.
func buildUpstreamURL(c *Config, model, intent string) string {
	endpoint := c.Endpoint
	endpoint = strings.Replace(endpoint, "https://", "wss://", 1)
	endpoint = strings.Replace(endpoint, "http://", "ws://", 1)
	if !strings.HasPrefix(endpoint, "ws://") && !strings.HasPrefix(endpoint, "wss://") {
		endpoint = "wss://" + endpoint
	}

	query := url.Values{}
	query.Set("api-version", c.APIVersion)
	query.Set("model", model)
	if intent != "" {
		query.Set("intent", intent)
	}
	return endpoint + "/voice-live/realtime?" + query.Encode()
}

// isCleartextEndpoint reports whether the configured endpoint would produce an
// unencrypted upstream connection. A bare host is not cleartext: buildUpstreamURL
// defaults it to wss://.
func isCleartextEndpoint(endpoint string) bool {
	lower := strings.ToLower(strings.TrimSpace(endpoint))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "ws://")
}

// isLoopbackEndpoint reports whether the endpoint points at this machine, which
// is the one case where cleartext carries no exposure — and the case the tests
// and local Voice Live fakes rely on.
func isLoopbackEndpoint(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// isRealtimeUpgrade reports whether this is a realtime WebSocket upgrade. The
// Upgrade header check also keeps the WebRTC SDP endpoint under the same prefix
// (a POST to /realtime/calls) out of scope.
func isRealtimeUpgrade(req *schemas.HTTPRequest) bool {
	if req == nil || !strings.EqualFold(req.Method, "GET") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(req.CaseInsensitiveHeaderLookup("Upgrade")), "websocket") {
		return false
	}
	path := strings.TrimSuffix(req.Path, "/")
	return path == "/v1/realtime" ||
		path == "/openai/v1/realtime" ||
		path == "/openai/realtime" ||
		path == "/openai/openai/realtime"
}

// stripProviderPrefix turns "azure/gpt-realtime" into "gpt-realtime".
func stripProviderPrefix(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.Index(model, "/"); idx >= 0 {
		return model[idx+1:]
	}
	return model
}

func contains(values []string, target string) bool {
	if target == "" {
		return false
	}
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// redactURL strips the query string, which carries deployment details that do
// not belong in logs.
func redactURL(raw string) string {
	if idx := strings.Index(raw, "?"); idx >= 0 {
		return raw[:idx]
	}
	return raw
}

func jsonResponse(status int, message string, extraHeaders map[string]string) *schemas.HTTPResponse {
	body, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
	}
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	return &schemas.HTTPResponse{StatusCode: status, Headers: headers, Body: body}
}
