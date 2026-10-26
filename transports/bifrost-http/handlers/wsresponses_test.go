package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type testWSHandlerStore struct {
	matcher         *lib.HeaderMatcher
	allowDirectKeys bool
}

func (s testWSHandlerStore) GetHeaderMatcher() *lib.HeaderMatcher {
	return s.matcher
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

func (s testWSHandlerStore) ShouldAllowPerRequestStorageOverride() bool      { return false }
func (s testWSHandlerStore) ShouldAllowPerRequestRawOverride() bool          { return false }
func (s testWSHandlerStore) ShouldAllowDirectKeys() bool                     { return s.allowDirectKeys }
func (s testWSHandlerStore) ShouldSendExtraFieldsInHeaders() bool            { return false }
func (s testWSHandlerStore) IsProviderConfigured(schemas.ModelProvider) bool { return false }
func (s testWSHandlerStore) GetMCPExternalServerURL() string                 { return "" }
func (s testWSHandlerStore) GetMCPExternalClientURL() string                 { return "" }

func (s testWSHandlerStore) GetCustomProviderPassthrough(schemas.ModelProvider) (schemas.ModelProvider, bool, bool) {
	return "", false, false
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return false }

func TestResolveWSStreamIdleTimeoutUsesProviderOverride(t *testing.T) {
	cfg := &lib.Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {
				NetworkConfig: &schemas.NetworkConfig{StreamIdleTimeoutInSeconds: 7},
			},
		},
	}

	timeout := resolveWSStreamIdleTimeout(cfg, schemas.OpenAI)
	assert.Equal(t, 7*time.Second, timeout)
}

func TestResolveWSStreamIdleTimeoutFallsBackToDefault(t *testing.T) {
	timeout := resolveWSStreamIdleTimeout(&lib.Config{}, schemas.OpenAI)
	assert.Equal(t, time.Duration(schemas.DefaultStreamIdleTimeoutInSeconds)*time.Second, timeout)
}

func TestIsWSReadTimeout(t *testing.T) {
	assert.True(t, isWSReadTimeout(timeoutNetError{}))
	assert.False(t, isWSReadTimeout(net.UnknownNetworkError("unknown")))
	assert.False(t, isWSReadTimeout(errors.New("boom")))
	assert.False(t, isWSReadTimeout(nil))
}

func TestNewBifrostError(t *testing.T) {
	bifrostErr := newBifrostError(504, "upstream_timeout", "upstream websocket stream timed out")
	if bifrostErr == nil {
		t.Fatal("expected bifrost error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 504 {
		t.Fatalf("status code = %#v, want 504", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil {
		t.Fatal("expected error field, got nil")
	}
	if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "upstream_timeout" {
		t.Fatalf("error type = %#v, want upstream_timeout", bifrostErr.Error.Type)
	}
	if bifrostErr.Error.Message != "upstream websocket stream timed out" {
		t.Fatalf("error message = %q, want upstream websocket stream timed out", bifrostErr.Error.Message)
	}
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

// A connection settles its identity at upgrade the way an HTTP request does at conversion, so
// governance sees a settled request whether or not a key was presented.
func TestCreateBifrostContextFromAuth_SettlesIdentity(t *testing.T) {
	cases := map[string]struct {
		auth     *authHeaders
		wantKind string
		wantKey  string
	}{
		"no credential":       {auth: &authHeaders{}},
		"virtual key header":  {auth: &authHeaders{virtualKey: "sk-bf-ws"}, wantKind: string(grant.CredentialVirtualKey), wantKey: "sk-bf-ws"},
		"bearer virtual key":  {auth: &authHeaders{authorization: "Bearer sk-bf-bearer"}, wantKind: string(grant.CredentialVirtualKey), wantKey: "sk-bf-bearer"},
		"provider key bearer": {auth: &authHeaders{authorization: "Bearer sk-openai"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, tc.auth)
			defer cancel()

			g := ctx.Grant()
			if g == nil {
				t.Fatal("expected a grant on the connection context")
			}
			identity := g.Identity()
			if identity == nil {
				t.Fatal("expected a settled identity on the grant")
			}
			if got := identity.Credential(); got.Kind != tc.wantKind || got.Value != tc.wantKey {
				t.Fatalf("credential = %+v, want kind %q value %q", got, tc.wantKind, tc.wantKey)
			}
		})
	}
}

// A caller's own provider key is honoured at upgrade under the same gate as on HTTP: the server
// setting and the per-request header, with a virtual key never mistaken for one.
func TestCreateBifrostContextFromAuth_DirectKey(t *testing.T) {
	direct := map[string][]string{"x-bf-direct-key": {"true"}}
	cases := map[string]struct {
		store   testWSHandlerStore
		auth    *authHeaders
		wantKey string
	}{
		"bearer provider key":       {store: testWSHandlerStore{allowDirectKeys: true}, auth: &authHeaders{authorization: "Bearer sk-openai", headers: direct}, wantKey: "sk-openai"},
		"x-api-key provider key":    {store: testWSHandlerStore{allowDirectKeys: true}, auth: &authHeaders{apiKey: "sk-openai", headers: direct}, wantKey: "sk-openai"},
		"x-goog-api-key":            {store: testWSHandlerStore{allowDirectKeys: true}, auth: &authHeaders{googAPIKey: "AIza", headers: direct}, wantKey: "AIza"},
		"virtual key is not direct": {store: testWSHandlerStore{allowDirectKeys: true}, auth: &authHeaders{authorization: "Bearer sk-bf-vk", headers: direct}},
		"server setting off":        {store: testWSHandlerStore{}, auth: &authHeaders{authorization: "Bearer sk-openai", headers: direct}},
		"header absent":             {store: testWSHandlerStore{allowDirectKeys: true}, auth: &authHeaders{authorization: "Bearer sk-openai"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := createBifrostContextFromAuth(tc.store, tc.auth)
			defer cancel()

			key, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
			if tc.wantKey == "" {
				assert.False(t, ok, "no direct key expected, got %+v", key)
				return
			}
			require.True(t, ok, "expected a direct key on the connection context")
			assert.Equal(t, "header-provided", key.ID)
			assert.Equal(t, tc.wantKey, key.Value.GetValue())
			assert.True(t, governance.PresentedCredentialResolved(ctx), "a direct key is a resolved credential")
		})
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

func TestCreateBifrostContextFromAuth_ForwardsPrefixedHeaders(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		headers: map[string][]string{
			"x-bf-eh-originator": {"my-test-client"},
			"x-bf-eh-x-trace-id": {"abc-123"},
			"x-bf-eh-cookie":     {"blocked"},
		},
	})
	defer cancel()

	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		t.Fatal("expected websocket extra headers in context")
	}
	assert.Equal(t, []string{"my-test-client"}, extraHeaders["originator"])
	assert.Equal(t, []string{"abc-123"}, extraHeaders["x-trace-id"])
	assert.NotContains(t, extraHeaders, "cookie")
}

func TestCreateBifrostContextFromAuth_AppliesHeaderFilterAndDirectAllowlist(t *testing.T) {
	matcher := lib.NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"originator", "anthropic-*"},
		Denylist:  []string{"anthropic-secret"},
	})
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{matcher: matcher}, &authHeaders{
		headers: map[string][]string{
			"x-bf-eh-originator":       {"allowed-prefix"},
			"x-bf-eh-x-trace-id":       {"blocked-by-allowlist"},
			"anthropic-beta":           {"allowed-direct"},
			"anthropic-secret":         {"blocked-by-denylist"},
			"x-bf-eh-anthropic-secret": {"blocked-prefix-denylist"},
		},
	})
	defer cancel()

	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		t.Fatal("expected websocket extra headers in context")
	}
	assert.Equal(t, []string{"allowed-prefix"}, extraHeaders["originator"])
	assert.Equal(t, []string{"allowed-direct"}, extraHeaders["anthropic-beta"])
	assert.NotContains(t, extraHeaders, "x-trace-id")
	assert.NotContains(t, extraHeaders, "anthropic-secret")
}

func TestCaptureAuthHeaders_PreservesDuplicateHeaderValues(t *testing.T) {
	var req fasthttp.Request
	req.Header.Set("Host", "example.test")
	req.Header.Add("x-bf-eh-x-trace-id", "trace-a")
	req.Header.Add("x-bf-eh-x-trace-id", "trace-b")

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

	auth := captureAuthHeaders(ctx)
	assert.Equal(t, []string{"trace-a", "trace-b"}, auth.headers["x-bf-eh-x-trace-id"])
}

func TestCreateBifrostContextFromAuth_PreservesMultipleForwardedHeaderValues(t *testing.T) {
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{}, &authHeaders{
		headers: map[string][]string{
			"x-bf-eh-x-trace-id": {"trace-a", "trace-b"},
		},
	})
	defer cancel()

	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		t.Fatal("expected websocket extra headers in context")
	}
	assert.Equal(t, []string{"trace-a", "trace-b"}, extraHeaders["x-trace-id"])
}

func TestCreateBifrostContextFromAuth_BlocksWebSocketHandshakeForwardedHeaders(t *testing.T) {
	matcher := lib.NewHeaderMatcher(&configstoreTables.GlobalHeaderFilterConfig{
		Allowlist: []string{"*"},
	})
	ctx, cancel := createBifrostContextFromAuth(testWSHandlerStore{matcher: matcher}, &authHeaders{
		headers: map[string][]string{
			"x-bf-eh-upgrade":                {"websocket"},
			"x-bf-eh-sec-websocket-protocol": {"realtime"},
			"sec-websocket-extensions":       {"permessage-deflate"},
			"x-bf-eh-originator":             {"safe"},
		},
	})
	defer cancel()

	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		t.Fatal("expected websocket extra headers in context")
	}
	assert.Equal(t, []string{"safe"}, extraHeaders["originator"])
	assert.NotContains(t, extraHeaders, "upgrade")
	assert.NotContains(t, extraHeaders, "sec-websocket-protocol")
	assert.NotContains(t, extraHeaders, "sec-websocket-extensions")
}

func TestMergeWebSocketHeaders_ForwardedHeadersOverrideProviderHeadersAndPreserveValues(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"originator":    {"my-test-client"},
		"authorization": {"Bearer malicious"},
		"x-static":      {"client-value-1", "client-value-2"},
	})

	merged := mergeWebSocketHeaders(ctx, map[string]string{
		"Authorization": "Bearer provider-key",
		"x-static":      "provider-value",
	})

	assert.Equal(t, []string{"my-test-client"}, merged.Values("originator"))
	assert.Equal(t, []string{"Bearer provider-key"}, merged.Values("Authorization"))
	assert.Equal(t, []string{"client-value-1", "client-value-2"}, merged.Values("x-static"))
	assert.NotContains(t, merged.Values("Authorization"), "Bearer malicious")
}

func TestHasWebSocketForwardedHeaders(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	assert.False(t, hasWebSocketForwardedHeaders(ctx))

	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"authorization": {"Bearer malicious"},
	})
	assert.False(t, hasWebSocketForwardedHeaders(ctx))

	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"x-trace-id": {"abc-123"},
	})
	assert.True(t, hasWebSocketForwardedHeaders(ctx))
}

// A session pins the pooled upstream it borrowed so later turns reuse it. When the session ends,
// normally or after a recovered panic, that connection must go back through the pool exactly once:
// closing it on the session alone leaves it counted in flight, and a pool at its limit then refuses
// every later borrow and pushes requests onto the HTTP bridge.
func TestEndSessionReleasesThePinnedPoolConnection(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer upstreamSrv.Close()
	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer clientSrv.Close()

	pool := bfws.NewPool(&schemas.WSPoolConfig{MaxTotalConnections: 1})
	defer pool.Close()
	h := &WSResponsesHandler{pool: pool, sessions: bfws.NewSessionManager(10)}
	key := bfws.PoolKey{Provider: schemas.OpenAI, KeyID: "key-1", Endpoint: "ws" + strings.TrimPrefix(upstreamSrv.URL, "http")}

	for i := 0; i < 3; i++ {
		clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(clientSrv.URL, "http"), nil)
		require.NoError(t, err)
		session, err := h.sessions.Create(clientConn)
		require.NoError(t, err)
		upstream, err := pool.Get(key, nil, nil)
		require.NoError(t, err, "session %d could not borrow: the previous session's connection was never released", i)
		session.SetUpstream(upstream)
		h.endSession(clientConn)
		assert.True(t, upstream.IsClosed(), "the pinned connection is closed with its session")
	}
}

// wsTwoKeyAccount exposes OpenAI with two equally weighted keys, both served from baseURL, so a
// turn that rotates keys is visible to the fake upstream by its Authorization header alone.
type wsTwoKeyAccount struct{ baseURL string }

func (wsTwoKeyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (wsTwoKeyAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != schemas.OpenAI {
		return nil, nil
	}
	return []schemas.Key{
		{ID: "key-a", Name: "key-a", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1.0},
		{ID: "key-b", Name: "key-b", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1.0},
	}, nil
}

func (a wsTwoKeyAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.OpenAI {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	networkConfig := schemas.DefaultNetworkConfig
	networkConfig.BaseURL = a.baseURL
	return &schemas.ProviderConfig{
		NetworkConfig:            networkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// fakeOpenAIResponses serves both Responses surfaces from one base URL: the WS mode socket and
// the HTTP stream the bridge uses. With refuseEncrypted set, either surface refuses a turn that
// replays encrypted_content, the way OpenAI refuses ciphertext another organization minted.
type fakeOpenAIResponses struct {
	refuseEncrypted bool

	mu         sync.Mutex
	wsAuth     []string
	httpBodies []string
}

const fakeEncryptedRefusal = `{"type":"error","error":{"message":"The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","param":"input","code":"invalid_encrypted_content"},"status":400}`

func (f *fakeOpenAIResponses) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if websocket.IsWebSocketUpgrade(r) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		auth := r.Header.Get("Authorization")
		for turn := 0; ; turn++ {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.wsAuth = append(f.wsAuth, auth)
			f.mu.Unlock()
			if f.refuseEncrypted && strings.Contains(string(data), "encrypted_content") {
				_ = c.WriteMessage(websocket.TextMessage, []byte(fakeEncryptedRefusal))
				continue
			}
			completed := fmt.Sprintf(`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_ws_%d","object":"response","model":"gpt-5.5","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`, turn)
			_ = c.WriteMessage(websocket.TextMessage, []byte(completed))
		}
	}

	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.httpBodies = append(f.httpBodies, string(body))
	f.mu.Unlock()
	if f.refuseEncrypted && strings.Contains(string(body), "encrypted_content") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","param":"input","code":"invalid_encrypted_content"}}`))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_http\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-5.5\",\"output\":[]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_http\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"))
}

// newTwoKeyWSResponsesHarness wires a WSResponsesHandler to fake over a two-key OpenAI account and
// opens a client session whose relayed messages arrive on the returned channel.
func newTwoKeyWSResponsesHarness(t *testing.T, fake *fakeOpenAIResponses) (*WSResponsesHandler, *bfws.Session, <-chan string) {
	t.Helper()
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))

	upstreamSrv := httptest.NewServer(fake)
	t.Cleanup(upstreamSrv.Close)

	relayed := make(chan string, 256)
	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			relayed <- string(data)
		}
	}))
	t.Cleanup(clientSrv.Close)

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: wsTwoKeyAccount{baseURL: upstreamSrv.URL},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)

	pool := bfws.NewPool(&schemas.WSPoolConfig{})
	t.Cleanup(pool.Close)

	h := &WSResponsesHandler{
		client:       client,
		config:       &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}},
		handlerStore: testWSHandlerStore{},
		pool:         pool,
		sessions:     bfws.NewSessionManager(10),
	}

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(clientSrv.URL, "http"), nil)
	require.NoError(t, err)
	session := bfws.NewSession(clientConn)
	t.Cleanup(session.Close)
	return h, session, relayed
}

// awaitTerminalWSMessage returns the first relayed message that ends a turn.
func awaitTerminalWSMessage(t *testing.T, relayed <-chan string) string {
	t.Helper()
	for {
		select {
		case msg := <-relayed:
			if strings.Contains(msg, `"response.completed"`) || strings.Contains(msg, `"type":"error"`) {
				return msg
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the turn's terminal message")
			return ""
		}
	}
}

// TestResponsesWSKeepsEveryTurnOnTheSessionKey pins the key a WS session's turns go out on.
// OpenAI binds a reasoning item's encrypted_content to the organization of the key that minted
// it, and Codex replays those items on every turn, so a session whose turns rotate across a
// multi-key pool fails with invalid_encrypted_content as soon as a turn lands on another org.
func TestResponsesWSKeepsEveryTurnOnTheSessionKey(t *testing.T) {
	fake := &fakeOpenAIResponses{}
	h, session, relayed := newTwoKeyWSResponsesHarness(t, fake)

	const turns = 20
	for i := 0; i < turns; i++ {
		h.handleResponseCreate(session, nil, []byte(`{"type":"response.create","model":"openai/gpt-5.5","input":"hello"}`))
		msg := awaitTerminalWSMessage(t, relayed)
		require.Contains(t, msg, `"response.completed"`, "turn %d did not complete: %s", i, msg)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.wsAuth, turns, "every turn should take the native WS path")
	seen := map[string]bool{}
	for _, auth := range fake.wsAuth {
		seen[auth] = true
	}
	assert.Len(t, seen, 1, "a WS session's turns must stay on one key, saw %v", fake.wsAuth)
}

// TestResponsesWSHealsUnverifiableEncryptedReasoning covers a turn that replays reasoning another
// organization minted, e.g. the first turn of a reconnected Codex session landing on a different
// key. The HTTP path strips encrypted_content and retries once; the native WS path must reach the
// same heal instead of relaying OpenAI's refusal to the client.
func TestResponsesWSHealsUnverifiableEncryptedReasoning(t *testing.T) {
	fake := &fakeOpenAIResponses{refuseEncrypted: true}
	h, session, relayed := newTwoKeyWSResponsesHarness(t, fake)

	event := `{"type":"response.create","model":"openai/gpt-5.5","input":[` +
		`{"type":"message","role":"user","content":"first question"},` +
		`{"type":"reasoning","id":"rs_foreign","summary":[{"type":"summary_text","text":"earlier thought"}],"encrypted_content":"gAAAAforeignciphertext"},` +
		`{"type":"message","role":"assistant","content":"first answer"},` +
		`{"type":"message","role":"user","content":"follow up"}]}`
	h.handleResponseCreate(session, nil, []byte(event))

	msg := awaitTerminalWSMessage(t, relayed)
	require.NotContains(t, msg, "invalid_encrypted_content", "the refusal reached the client instead of being healed: %s", msg)
	require.Contains(t, msg, `"response.completed"`, "the healed turn should complete: %s", msg)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.NotEmpty(t, fake.httpBodies, "the refused turn should be retried through the HTTP bridge")
	last := fake.httpBodies[len(fake.httpBodies)-1]
	assert.NotContains(t, last, "encrypted_content", "the retry must not replay the unverifiable ciphertext")
	assert.Contains(t, last, "earlier thought", "the retry keeps the reasoning summary")
}

func TestEventLoopIgnoresResponseInterrupt(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))
	h := &WSResponsesHandler{sessions: bfws.NewSessionManager(10)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := newWSTestUpgrader()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		session, err := h.sessions.Create(c)
		if err != nil {
			return
		}
		defer h.sessions.Remove(c)
		h.eventLoop(c, session, &authHeaders{})
	}))
	defer srv.Close()

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer clientConn.Close()

	// Codex sends response.interrupt when the user types mid-stream; it is dropped without a reply.
	require.NoError(t, clientConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"resp_1","mode":"discard_partial_items"}`)))
	require.NoError(t, clientConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.unknown"}`)))

	require.NoError(t, clientConn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, data, err := clientConn.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(data), "unsupported event type: response.unknown", "the interrupt must not produce an error frame ahead of the next event's reply")
}
