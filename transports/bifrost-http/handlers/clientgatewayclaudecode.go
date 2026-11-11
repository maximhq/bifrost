package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Claude Code gateway profile constants. Claude Code's /login (Cloud gateway)
// is pointed at {issuer}/gateway/claude-code and derives every other URL from it.
const (
	claudeCodeGatewayPathPrefix = "/gateway/claude-code"
	// claudeCodeClientID names the built-in OAuth client every Claude Code
	// device grant and refresh token is bound to. Claude Code sends no client_id
	// at either endpoint, so the gateway supplies this one.
	claudeCodeClientID   = "bifrost-claude-code"
	claudeCodeClientName = "Claude Code"
	claudeCodeScope      = "claude-code"
)

// claudeCodeProfile is the Claude Code client gateway: Claude Code's /login
// (Cloud gateway) sign-in, Anthropic Messages inference, managed settings and an
// OTLP sink, with failures in the Anthropic error envelope.
type claudeCodeProfile struct{}

func (claudeCodeProfile) Name() string        { return "claude-code" }
func (claudeCodeProfile) PathPrefix() string  { return claudeCodeGatewayPathPrefix }
func (claudeCodeProfile) ClientID() string    { return claudeCodeClientID }
func (claudeCodeProfile) DisplayName() string { return claudeCodeClientName }
func (claudeCodeProfile) Scope() string       { return claudeCodeScope }
func (claudeCodeProfile) SignInHint() string  { return "sign in again with /login" }

func (claudeCodeProfile) Enabled(cfg *configtables.OAuth2ServerConfig) bool {
	return cfg.IsClaudeCodeGatewayEnabled()
}

// InferenceRoutes mounts the Anthropic integration's Messages, count_tokens and
// models routes. Claude Code only speaks the Messages API, so the legacy
// completions route is left out.
func (claudeCodeProfile) InferenceRoutes(prefix string, store *lib.Config) []integrations.RouteConfig {
	var routes []integrations.RouteConfig
	for _, route := range integrations.CreateAnthropicRouteConfigs(prefix, logger) {
		if strings.HasPrefix(route.Path, prefix+"/v1/messages") {
			routes = append(routes, route)
		}
	}
	routes = append(routes, integrations.CreateAnthropicCountTokensRouteConfigs(prefix, store)...)
	routes = append(routes, integrations.CreateAnthropicListModelsRouteConfigs(prefix, store)...)
	return routes
}

// WriteAuthError writes the Anthropic error envelope, which Claude Code's SDK
// surfaces to the user verbatim. x-should-retry: false on a 401 makes Claude
// Code prompt for /login at once instead of retrying.
func (claudeCodeProfile) WriteAuthError(ctx *fasthttp.RequestCtx, status int, unauthorized bool, message string) {
	switch {
	case unauthorized:
		ctx.Response.Header.Set("x-should-retry", "false")
		sendAnthropicError(ctx, status, "authentication_error", message)
	case status == fasthttp.StatusNotFound:
		sendAnthropicError(ctx, status, "not_found_error", message)
	default:
		sendAnthropicError(ctx, status, "api_error", message)
	}
}

// RegisterExtraRoutes adds managed settings and the OTLP sink, both behind the
// gateway bearer.
func (p claudeCodeProfile) RegisterExtraRoutes(r *router.Router, ep *clientGatewayEndpoint) {
	prefix := ep.profile.PathPrefix()
	r.GET(prefix+"/managed/settings", ep.BearerMiddleware(p.managedSettingsHandler(ep.store)))
	for _, signal := range []string{"metrics", "logs", "traces"} {
		r.POST(prefix+"/v1/"+signal, ep.BearerMiddleware(handleClaudeCodeOTLP))
	}
}

// --- GET /gateway/claude-code/managed/settings ---

type claudeCodeManagedSettingsResponse struct {
	UUID     string         `json:"uuid"`
	Checksum string         `json:"checksum"`
	Settings map[string]any `json:"settings"`
}

// managedSettingsHandler serves the configured managed-settings document. The
// checksum is the one Claude Code computes over the settings it holds
// ("sha256:" + hex SHA-256 of key-sorted, whitespace-free JSON), so its hourly
// If-None-Match poll is answered with 304 while nothing changed.
func (claudeCodeProfile) managedSettingsHandler(store *lib.Config) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		store.Mu.RLock()
		var settings map[string]any
		if cfg := store.ClientConfig.OAuth2ServerConfig; cfg.IsClaudeCodeGatewayEnabled() {
			settings = cfg.ClaudeCodeGateway.ManagedSettings
		}
		store.Mu.RUnlock()
		if settings == nil {
			// No managed policy, which Claude Code distinguishes from an empty one.
			sendAnthropicError(ctx, fasthttp.StatusNotFound, "not_found_error", "no managed settings are configured on this gateway")
			return
		}
		checksum, err := claudeCodeSettingsChecksum(settings)
		if err != nil {
			sendAnthropicError(ctx, fasthttp.StatusInternalServerError, "api_error", "failed to encode managed settings")
			return
		}
		etag := `"` + checksum + `"`
		ctx.Response.Header.Set("ETag", etag)
		if string(ctx.Request.Header.Peek("If-None-Match")) == etag {
			ctx.SetStatusCode(fasthttp.StatusNotModified)
			return
		}
		SendJSON(ctx, claudeCodeManagedSettingsResponse{UUID: checksum, Checksum: checksum, Settings: settings})
	}
}

// claudeCodeSettingsChecksum returns "sha256:" + the hex SHA-256 of settings
// serialized with sorted keys and no whitespace, matching the value Claude Code
// sends back in If-None-Match. encoding/json sorts map keys at every level;
// HTML escaping is turned off because the client's serializer does not escape.
func claudeCodeSettingsChecksum(settings map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(settings); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// --- POST /gateway/claude-code/v1/{metrics,logs,traces} ---

// handleClaudeCodeOTLP accepts Claude Code's OTLP/HTTP exports. While signed in
// to a gateway the client sends telemetry here instead of to its configured
// collector; the protocol asks for 200 whether the gateway forwards or discards,
// since a 404 makes the exporter log an error on every flush.
func handleClaudeCodeOTLP(ctx *fasthttp.RequestCtx) {
	ctx.SetStatusCode(fasthttp.StatusOK)
}

// sendAnthropicError writes the Anthropic error envelope.
func sendAnthropicError(ctx *fasthttp.RequestCtx, status int, errType, message string) {
	data, err := sonic.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errType, "message": message},
	})
	if err != nil {
		ctx.Error(message, status)
		return
	}
	ctx.SetStatusCode(status)
	ctx.SetContentType("application/json")
	ctx.SetBody(data)
}
