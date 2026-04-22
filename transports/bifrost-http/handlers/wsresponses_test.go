package handlers

import (
	"strings"
	"testing"

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
		"trailers",
		"transfer-encoding",
	}
	for _, h := range hopByHop {
		if !wsUpgradeHeaderBlocklist[strings.ToLower(h)] {
			t.Errorf("expected hop-by-hop header %q to be in blocklist, but it is not", h)
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

func TestMergeClientWSHeaders_ClientHeadersFlowThrough(t *testing.T) {
	provider := map[string]string{
		"Authorization":      "Bearer token",
		"chatgpt-account-id": "acct-123",
		"OpenAI-Beta":        "responses=experimental",
	}
	client := map[string]string{
		"originator": "codex_cli_rs",
		"version":    "0.121.0",
		"user-agent": "codex/0.121.0 (Linux; amd64)",
	}
	got := mergeClientWSHeaders(provider, client)

	if got["originator"] != "codex_cli_rs" {
		t.Errorf("originator = %q, want %q", got["originator"], "codex_cli_rs")
	}
	if got["version"] != "0.121.0" {
		t.Errorf("version = %q, want %q", got["version"], "0.121.0")
	}
	if got["user-agent"] != "codex/0.121.0 (Linux; amd64)" {
		t.Errorf("user-agent = %q, want codex/0.121.0 (Linux; amd64)", got["user-agent"])
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
	got := mergeClientWSHeaders(provider, client)

	if got["Authorization"] != "Bearer oauth-token" {
		t.Errorf("Authorization = %q, want provider value %q", got["Authorization"], "Bearer oauth-token")
	}
	if got["originator"] != "codex_cli_rs" {
		t.Errorf("originator = %q, want %q", got["originator"], "codex_cli_rs")
	}
}

func TestMergeClientWSHeaders_EmptyClientHeadersReturnProviderUnchanged(t *testing.T) {
	provider := map[string]string{
		"Authorization": "Bearer tok",
	}
	got := mergeClientWSHeaders(provider, nil)
	if got["Authorization"] != "Bearer tok" || len(got) != 1 {
		t.Errorf("expected provider headers unchanged when clientHeaders is nil, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// createBifrostContextFromAuth: verify BifrostContextKeyRequestHeaders is set
// ---------------------------------------------------------------------------

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
