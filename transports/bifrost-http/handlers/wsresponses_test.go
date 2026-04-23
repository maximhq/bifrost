package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

type testWSHandlerStore struct {
	allowDirectKeys bool
}

func (s testWSHandlerStore) ShouldAllowDirectKeys() bool {
	return s.allowDirectKeys
}

func (s testWSHandlerStore) GetHeaderMatcher() *lib.HeaderMatcher {
	return nil
}

func (s testWSHandlerStore) GetAvailableProviders() []schemas.ModelProvider {
	return nil
}

func (s testWSHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return nil
}

func (s testWSHandlerStore) GetAsyncJobExecutor() *logstore.AsyncJobExecutor {
	return nil
}

func (s testWSHandlerStore) GetAsyncJobResultTTL() int {
	return 0
}

func (s testWSHandlerStore) GetKVStore() *kvstore.Store {
	return nil
}

func (s testWSHandlerStore) GetMCPHeaderCombinedAllowlist() schemas.WhiteList {
	return nil
}

func TestCreateBifrostContextFromAuth_BaggageSessionIDSetsGrouping(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		baggage: "foo=bar, session-id=rt-ws-123, baz=qux",
	})
	defer cancel()

	if got, _ := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); got != "rt-ws-123" {
		t.Fatalf("parent request id = %q, want %q", got, "rt-ws-123")
	}
}

func TestCreateBifrostContextFromAuth_EmptyBaggageSessionIDIgnored(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		baggage: "session-id=   ",
	})
	defer cancel()

	if got := ctx.Value(schemas.BifrostContextKeyParentRequestID); got != nil {
		t.Fatalf("parent request id should be unset, got %#v", got)
	}
}

// ---------------------------------------------------------------------------
// wsUpgradeHeaderBlocklist: verify hop-by-hop and Bifrost-internal headers are blocked
// ---------------------------------------------------------------------------

func TestWSUpgradeHeaderBlocklist_HopByHopDropped(t *testing.T) {
	hopByHop := []string{
		"connection",
		"upgrade",
		"sec-websocket-key",
		"sec-websocket-version",
		"sec-websocket-extensions",
		"sec-websocket-protocol",
		"keep-alive",
		"proxy-authorization",
		"proxy-authenticate",
		"te",
		"trailer",
		"trailers",
		"transfer-encoding",
	}
	for _, h := range hopByHop {
		if !wsUpgradeHeaderBlocklist[strings.ToLower(h)] {
			t.Errorf("expected hop-by-hop header %q to be in blocklist, but it is not", h)
		}
	}
}

func TestWSUpgradeHeaderBlocklist_OriginDropped(t *testing.T) {
	// origin must be blocked: forwarding Origin: http://localhost... would cause
	// chatgpt.com to CORS-reject the upstream WebSocket connection.
	if !wsUpgradeHeaderBlocklist["origin"] {
		t.Error("expected \"origin\" to be in blocklist")
	}
}

func TestWSUpgradeHeaderBlocklist_NetworkTopologyDropped(t *testing.T) {
	// These headers must not leak internal network topology to external upstreams.
	topology := []string{
		"x-forwarded-for",
		"x-forwarded-host",
		"x-forwarded-proto",
		"x-real-ip",
	}
	for _, h := range topology {
		if !wsUpgradeHeaderBlocklist[strings.ToLower(h)] {
			t.Errorf("expected network topology header %q to be in blocklist, but it is not", h)
		}
	}
}

func TestWSUpgradeHeaderBlocklist_SecurityHeadersDropped(t *testing.T) {
	security := []string{
		"host",
		"cookie",
		"authorization",
		"x-api-key",
		"x-goog-api-key",
		"baggage",
	}
	for _, h := range security {
		if !wsUpgradeHeaderBlocklist[strings.ToLower(h)] {
			t.Errorf("expected security header %q to be in blocklist, but it is not", h)
		}
	}
}

func TestWSUpgradeHeaderBlocklist_CodexIdentityHeadersAllowed(t *testing.T) {
	// These first-party Codex headers must NOT be in the blocklist so they
	// flow through to the upstream WS connection.
	allowed := []string{
		"originator",
		"version",
		"user-agent",
		"session_id",
	}
	for _, h := range allowed {
		if wsUpgradeHeaderBlocklist[strings.ToLower(h)] {
			t.Errorf("expected Codex identity header %q to be allowed (not in blocklist)", h)
		}
	}
}

// ---------------------------------------------------------------------------
// mergeClientWSHeaders: verify merge semantics
// ---------------------------------------------------------------------------

// TestMergeClientWSHeaders_ClientHeadersFlowThrough models the production case:
// providerHeaders comes from chatGPTOAuthWebSocketHeaders (OAuth only, no identity
// defaults), and the client sends real Codex identity headers.  The merge must
// preserve the client's values unchanged.
func TestMergeClientWSHeaders_ClientHeadersFlowThrough(t *testing.T) {
	// Production-accurate: chatGPTOAuthWebSocketHeaders returns only OAuth headers.
	provider := map[string]string{
		"Authorization":      "Bearer token",
		"chatgpt-account-id": "acct-123",
		"OpenAI-Beta":        "responses=experimental",
	}
	// Client (Codex) sends its own identity headers.
	client := map[string]string{
		"originator": "codex_cli_rs",
		"version":    "0.121.0",
		"user-agent": "codex/0.121.0 (Linux; amd64)",
	}
	got := mergeClientWSHeaders(provider, client, true)

	// Client identity headers must flow through unchanged.
	if got["originator"] != "codex_cli_rs" {
		t.Errorf("originator = %q, want %q", got["originator"], "codex_cli_rs")
	}
	// Critical: client's version (0.121.0) must NOT be replaced by the default (0.111.0).
	if got["version"] != "0.121.0" {
		t.Errorf("version = %q, want %q (client value must win over default)", got["version"], "0.121.0")
	}
	if got["user-agent"] != "codex/0.121.0 (Linux; amd64)" {
		t.Errorf("user-agent = %q, want codex/0.121.0 (Linux; amd64)", got["user-agent"])
	}
	// OAuth headers must also be present.
	if got["Authorization"] != "Bearer token" {
		t.Errorf("Authorization = %q, want %q", got["Authorization"], "Bearer token")
	}
}

// TestMergeClientWSHeaders_DefaultsInjectedWhenClientSendsNeither verifies that
// mergeClientWSHeaders injects the identity fallbacks when neither the client nor
// the provider supplied originator or version.
func TestMergeClientWSHeaders_DefaultsInjectedWhenClientSendsNeither(t *testing.T) {
	provider := map[string]string{
		"Authorization":      "Bearer token",
		"chatgpt-account-id": "acct-123",
		"OpenAI-Beta":        "responses=experimental",
	}
	// Client sends no identity headers at all.
	client := map[string]string{
		"user-agent": "some-agent/1.0",
	}
	got := mergeClientWSHeaders(provider, client, true)

	if got["originator"] != chatGPTOAuthCodexDefaultOriginator {
		t.Errorf("originator = %q, want default %q", got["originator"], chatGPTOAuthCodexDefaultOriginator)
	}
	if got["version"] != chatGPTOAuthCodexDefaultVersionFallback {
		t.Errorf("version = %q, want default %q", got["version"], chatGPTOAuthCodexDefaultVersionFallback)
	}
}

// TestMergeClientWSHeaders_ClientOriginatorWinsOverDefault verifies that when the
// client supplies a custom originator, it wins over the injected default.
func TestMergeClientWSHeaders_ClientOriginatorWinsOverDefault(t *testing.T) {
	provider := map[string]string{
		"Authorization": "Bearer token",
	}
	client := map[string]string{
		"originator": "something-else",
		"version":    "9.9.9",
	}
	got := mergeClientWSHeaders(provider, client, true)

	if got["originator"] != "something-else" {
		t.Errorf("originator = %q, want %q (client value must win)", got["originator"], "something-else")
	}
	if got["version"] != "9.9.9" {
		t.Errorf("version = %q, want %q (client value must win)", got["version"], "9.9.9")
	}
}

func TestMergeClientWSHeaders_ProviderHeadersWinOnConflict(t *testing.T) {
	provider := map[string]string{
		"Authorization": "Bearer oauth-token",
	}
	client := map[string]string{
		// Client should never win on Authorization — blocklist prevents this in
		// captureAuthHeaders, but mergeClientWSHeaders defends in depth.
		"Authorization": "Bearer client-should-lose",
		"originator":    "codex_cli_rs",
	}
	got := mergeClientWSHeaders(provider, client, true)

	if got["Authorization"] != "Bearer oauth-token" {
		t.Errorf("Authorization = %q, want provider value %q", got["Authorization"], "Bearer oauth-token")
	}
	if got["originator"] != "codex_cli_rs" {
		t.Errorf("originator = %q, want %q", got["originator"], "codex_cli_rs")
	}
}

func TestMergeClientWSHeaders_EmptyClientHeadersGetDefaultsInjected(t *testing.T) {
	// Even with no client headers, the identity defaults are always injected so
	// chatgpt.com's anti-abuse gate receives the required identity markers.
	provider := map[string]string{
		"Authorization": "Bearer tok",
	}
	got := mergeClientWSHeaders(provider, nil, true)
	if got["Authorization"] != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", got["Authorization"], "Bearer tok")
	}
	if got["originator"] != chatGPTOAuthCodexDefaultOriginator {
		t.Errorf("originator = %q, want default %q", got["originator"], chatGPTOAuthCodexDefaultOriginator)
	}
	if got["version"] != chatGPTOAuthCodexDefaultVersionFallback {
		t.Errorf("version = %q, want default %q", got["version"], chatGPTOAuthCodexDefaultVersionFallback)
	}
}

// TestMergeClientWSHeaders_NonOAuthPathNoDefaultsInjected verifies that when
// injectCodexDefaults is false (standard api.openai.com, non-OAuth provider),
// mergeClientWSHeaders does NOT inject the chatgpt.com-specific originator or
// version headers even when the client sends none.
func TestMergeClientWSHeaders_NonOAuthPathNoDefaultsInjected(t *testing.T) {
	// Standard OpenAI provider headers — no OAuth headers.
	provider := map[string]string{
		"Authorization": "Bearer sk-abc123",
	}
	// Client sends no identity headers.
	client := map[string]string{
		"user-agent": "myapp/1.0",
	}
	got := mergeClientWSHeaders(provider, client, false)

	if _, hasOriginator := got["originator"]; hasOriginator {
		t.Errorf("originator must NOT be injected on non-OAuth path, but found %q", got["originator"])
	}
	if _, hasVersion := got["version"]; hasVersion {
		t.Errorf("version must NOT be injected on non-OAuth path, but found %q", got["version"])
	}
	// Provider and client headers must still be merged correctly.
	if got["Authorization"] != "Bearer sk-abc123" {
		t.Errorf("Authorization = %q, want %q", got["Authorization"], "Bearer sk-abc123")
	}
	if got["user-agent"] != "myapp/1.0" {
		t.Errorf("user-agent = %q, want %q", got["user-agent"], "myapp/1.0")
	}
}

// ---------------------------------------------------------------------------
// createBifrostContextFromAuth: verify BifrostContextKeyRequestHeaders is set
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// extractStreamEventType: lightweight type field extraction
// ---------------------------------------------------------------------------

func TestExtractStreamEventType_ValidTerminal(t *testing.T) {
	got := extractStreamEventType([]byte(`{"type":"response.completed","sequence_number":5}`))
	if got != schemas.ResponsesStreamResponseTypeCompleted {
		t.Errorf("got %q, want %q", got, schemas.ResponsesStreamResponseTypeCompleted)
	}
}

func TestExtractStreamEventType_ValidNonTerminal(t *testing.T) {
	got := extractStreamEventType([]byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	if got != schemas.ResponsesStreamResponseTypeOutputTextDelta {
		t.Errorf("got %q, want %q", got, schemas.ResponsesStreamResponseTypeOutputTextDelta)
	}
}

func TestExtractStreamEventType_MalformedJSON(t *testing.T) {
	got := extractStreamEventType([]byte(`not json at all`))
	if got != "" {
		t.Errorf("expected empty string for malformed JSON, got %q", got)
	}
}

func TestExtractStreamEventType_MissingTypeField(t *testing.T) {
	got := extractStreamEventType([]byte(`{"sequence_number":1,"delta":"hello"}`))
	if got != "" {
		t.Errorf("expected empty string for missing type field, got %q", got)
	}
}

func TestExtractStreamEventType_UnknownExtraFields(t *testing.T) {
	// Simulates a chatgpt.com codex.rate_limits event with unknown nested structure
	raw := []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"requests":{"limit":50,"remaining":48}}},"code_review_rate_limits":null}`)
	got := extractStreamEventType(raw)
	if got != schemas.ResponsesStreamResponseType("codex.rate_limits") {
		t.Errorf("got %q, want %q", got, "codex.rate_limits")
	}
}

// ---------------------------------------------------------------------------
// synthesizeTerminalStreamResponse: minimal struct construction
// ---------------------------------------------------------------------------

func TestSynthesizeTerminalStreamResponse_FieldsPopulated(t *testing.T) {
	resp := synthesizeTerminalStreamResponse(schemas.OpenAI, "gpt-4o", schemas.ResponsesStreamResponseTypeCompleted)
	if resp == nil {
		t.Fatal("got nil response")
	}
	if resp.Type != schemas.ResponsesStreamResponseTypeCompleted {
		t.Errorf("Type = %q, want %q", resp.Type, schemas.ResponsesStreamResponseTypeCompleted)
	}
	if resp.ExtraFields.Provider != schemas.OpenAI {
		t.Errorf("Provider = %q, want %q", resp.ExtraFields.Provider, schemas.OpenAI)
	}
	if resp.ExtraFields.OriginalModelRequested != "gpt-4o" {
		t.Errorf("OriginalModelRequested = %q, want %q", resp.ExtraFields.OriginalModelRequested, "gpt-4o")
	}
	if resp.ExtraFields.RequestType != schemas.ResponsesStreamRequest {
		t.Errorf("RequestType = %v, want %v", resp.ExtraFields.RequestType, schemas.ResponsesStreamRequest)
	}
}

// ---------------------------------------------------------------------------
// isTerminalStreamType: terminal detection
// ---------------------------------------------------------------------------

func TestIsTerminalStreamType_TerminalTypes(t *testing.T) {
	terminals := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCompleted,
		schemas.ResponsesStreamResponseTypeFailed,
		schemas.ResponsesStreamResponseTypeIncomplete,
		schemas.ResponsesStreamResponseTypeError,
	}
	for _, tt := range terminals {
		if !isTerminalStreamType(tt) {
			t.Errorf("expected %q to be terminal", tt)
		}
	}
}

func TestIsTerminalStreamType_NonTerminalTypes(t *testing.T) {
	nonTerminals := []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseType("codex.rate_limits"),
		schemas.ResponsesStreamResponseType(""),
	}
	for _, tt := range nonTerminals {
		if isTerminalStreamType(tt) {
			t.Errorf("expected %q to be non-terminal", tt)
		}
	}
}

func TestCreateBifrostContextFromAuth_RequestHeadersPopulated(t *testing.T) {
	auth := &authHeaders{
		authorization: "Bearer some-token",
		apiKey:        "",
		clientHeaders: map[string]string{
			"originator": "codex_cli_rs",
			"version":    "0.121.0",
			"user-agent": "codex/0.121.0",
		},
		extraHeaders: make(map[string]string),
	}

	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, auth)
	defer cancel()

	headers, ok := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if !ok || headers == nil {
		t.Fatal("BifrostContextKeyRequestHeaders not set or wrong type")
	}
	if headers["originator"] != "codex_cli_rs" {
		t.Errorf("originator = %q, want %q", headers["originator"], "codex_cli_rs")
	}
	if headers["authorization"] != "Bearer some-token" {
		t.Errorf("authorization = %q, want %q", headers["authorization"], "Bearer some-token")
	}
}

// ---------------------------------------------------------------------------
// upstreamWSIdleTimeout: config resolution
// ---------------------------------------------------------------------------

// TestUpstreamWSIdleTimeout_FallbackWhenConfigNil verifies that the default
// 60 s constant is returned when h.config is nil (no per-provider override).
func TestUpstreamWSIdleTimeout_FallbackWhenConfigNil(t *testing.T) {
	h := &WSResponsesHandler{config: nil}
	got := h.upstreamWSIdleTimeout(schemas.OpenAI)
	if got != wsUpstreamIdleTimeout {
		t.Errorf("upstreamWSIdleTimeout = %v, want %v (wsUpstreamIdleTimeout)", got, wsUpstreamIdleTimeout)
	}
}

// TestUpstreamWSIdleTimeout_DefaultIs60s verifies the constant value itself is
// 60 s so that documentation comments remain accurate.
func TestUpstreamWSIdleTimeout_DefaultIs60s(t *testing.T) {
	want := 60 * time.Second
	if wsUpstreamIdleTimeout != want {
		t.Errorf("wsUpstreamIdleTimeout = %v, want %v", wsUpstreamIdleTimeout, want)
	}
}
