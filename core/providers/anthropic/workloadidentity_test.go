package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

const wifMintedToken = "sk-ant-oat01-minted"

// fakeIdentityToken builds a JWT-shaped identity token; the signature is never checked here.
func fakeIdentityToken(claims string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256","kid":"k1"}`)) + "." + enc([]byte(claims)) + ".c2ln"
}

// wifRecorded is what the fake upstream saw on one request.
type wifRecorded struct {
	path   string
	header http.Header
	body   []byte
}

// wifUpstream plays both the Anthropic token endpoint and the API.
type wifUpstream struct {
	*httptest.Server
	mu        sync.Mutex
	exchanges []wifRecorded
	api       []wifRecorded
}

func (u *wifUpstream) snapshot() ([]wifRecorded, []wifRecorded) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]wifRecorded(nil), u.exchanges...), append([]wifRecorded(nil), u.api...)
}

// newWIFUpstream answers the token endpoint with tokenStatus/tokenBody and every other path with
// apiBody (served as SSE when it starts with "event:").
func newWIFUpstream(t *testing.T, tokenStatus int, tokenBody, apiBody string) *wifUpstream {
	t.Helper()
	u := &wifUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := wifRecorded{path: r.URL.Path, header: r.Header.Clone(), body: body}
		u.mu.Lock()
		if r.URL.Path == workloadIdentityTokenPath {
			u.exchanges = append(u.exchanges, rec)
		} else {
			u.api = append(u.api, rec)
		}
		u.mu.Unlock()
		if r.URL.Path == workloadIdentityTokenPath {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tokenStatus)
			_, _ = w.Write([]byte(tokenBody))
			return
		}
		if strings.HasPrefix(apiBody, "event:") {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = w.Write([]byte(apiBody))
	}))
	t.Cleanup(u.Close)
	return u
}

const wifTokenOK = `{"access_token":"` + wifMintedToken + `","token_type":"Bearer","expires_in":3600,"scope":"workspace:inference"}`

const wifMessageResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-repro","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func writeIdentityTokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(token+"\n"), 0o600))
	return path
}

func wifKey(tokenFile string) schemas.Key {
	return schemas.Key{
		ID: "wif-key",
		AnthropicKeyConfig: &schemas.AnthropicKeyConfig{WorkloadIdentity: &schemas.AnthropicWorkloadIdentityConfig{
			FederationRuleID:  *schemas.NewSecretVar("fdrl_rule"),
			OrganizationID:    *schemas.NewSecretVar("00000000-0000-0000-0000-000000000000"),
			ServiceAccountID:  *schemas.NewSecretVar("svac_account"),
			WorkspaceID:       schemas.NewSecretVar("wrkspc_workspace"),
			IdentityTokenFile: schemas.NewSecretVar(tokenFile),
		}},
	}
}

func newWIFTestProvider(baseURL string) *AnthropicProvider {
	return NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 30},
	}, truncationTestLogger{})
}

func requireBearerAuth(t *testing.T, rec wifRecorded) {
	t.Helper()
	require.Equal(t, "Bearer "+wifMintedToken, rec.header.Get("Authorization"))
	require.Empty(t, rec.header.Get("x-api-key"), "a federated key must not also send x-api-key")
	require.Contains(t, strings.Split(rec.header.Get(AnthropicBetaHeader), ","), AnthropicOAuthBetaHeader)
}

func TestWorkloadIdentityChatMintsOnceAndSendsBearer(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, wifMessageResponse)
	assertion := fakeIdentityToken(`{"iss":"https://issuer.example","sub":"system:serviceaccount:ai:bifrost","jti":"j1"}`)
	key := wifKey(writeIdentityTokenFile(t, assertion))
	provider := newWIFTestProvider(upstream.URL)

	for range 3 {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{AnthropicBetaHeader: {"context-1m-2025-08-07"}})
		_, bErr := provider.ChatCompletion(ctx, key, truncationChatRequest())
		require.Nil(t, bErr)
	}

	exchanges, api := upstream.snapshot()
	require.Len(t, exchanges, 1, "concurrent and repeated requests reuse one minted token")
	require.Equal(t, "application/json", exchanges[0].header.Get("Content-Type"))
	require.Equal(t, AnthropicOAuthBetaHeader+","+anthropicFederationBetaHeader, exchanges[0].header.Get(AnthropicBetaHeader))
	var sent map[string]string
	require.NoError(t, json.Unmarshal(exchanges[0].body, &sent))
	require.Equal(t, map[string]string{
		"grant_type":         "urn:ietf:params:oauth:grant-type:jwt-bearer",
		"assertion":          assertion,
		"federation_rule_id": "fdrl_rule",
		"organization_id":    "00000000-0000-0000-0000-000000000000",
		"service_account_id": "svac_account",
		"workspace_id":       "wrkspc_workspace",
	}, sent)

	require.Len(t, api, 3)
	for _, rec := range api {
		require.Equal(t, "/v1/messages", rec.path)
		requireBearerAuth(t, rec)
		require.Contains(t, strings.Split(rec.header.Get(AnthropicBetaHeader), ","), "context-1m-2025-08-07", "the OAuth beta is merged with the request's betas, not replacing them")
	}
}

func TestWorkloadIdentityStreamsSendBearer(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, anthropicMessageStart+anthropicTextDelta+anthropicMessageStop)
	key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`)))
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr := provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil, key, truncationChatRequest())
	require.Nil(t, bErr)
	collectTruncationChunks(t, stream)

	ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr = provider.ResponsesStream(ctx, truncationPassthroughPostHook, nil, key, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-repro",
		Input: []schemas.ResponsesMessage{{
			Role:    new(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: new("hi")},
		}},
	})
	require.Nil(t, bErr)
	collectTruncationChunks(t, stream)

	_, api := upstream.snapshot()
	require.Len(t, api, 2)
	for _, rec := range api {
		requireBearerAuth(t, rec)
	}
}

func TestWorkloadIdentityStaticValueWins(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, wifMessageResponse)
	key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`)))
	key.Value = *schemas.NewSecretVar("sk-ant-static")
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.ChatCompletion(ctx, key, truncationChatRequest())
	require.Nil(t, bErr)

	exchanges, api := upstream.snapshot()
	require.Empty(t, exchanges, "a static value is used verbatim and nothing is minted")
	require.Len(t, api, 1)
	require.Equal(t, "sk-ant-static", api[0].header.Get("x-api-key"))
	require.Empty(t, api[0].header.Get("Authorization"))
	require.NotContains(t, api[0].header.Get(AnthropicBetaHeader), AnthropicOAuthBetaHeader)
}

func TestWorkloadIdentityExchangeRejected(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusUnauthorized,
		`{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"},"request_id":"req_123"}`, wifMessageResponse)
	assertion := fakeIdentityToken(`{"sub":"s","jti":"j1"}`)
	key := wifKey(writeIdentityTokenFile(t, assertion))
	key.AnthropicKeyConfig.WorkloadIdentity.WorkspaceID = nil
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.ChatCompletion(ctx, key, truncationChatRequest())
	require.NotNil(t, bErr)
	require.Equal(t, http.StatusUnauthorized, *bErr.StatusCode)
	require.NotNil(t, bErr.AllowFallbacks)
	require.False(t, *bErr.AllowFallbacks, "a federation fault on this key is not something a fallback repairs")
	require.Equal(t, "authentication_error", *bErr.Error.Type)
	require.Contains(t, bErr.Error.Message, "Authentication failed")
	require.Contains(t, bErr.Error.Message, "request_id req_123")
	require.Contains(t, bErr.Error.Message, "workspace_id", "an unset workspace is the commonest cause of an opaque 401")
	require.NotContains(t, bErr.Error.Message, assertion)

	// The failure is cached, so a retry inside the backoff does not hit the endpoint again.
	_, bErr = provider.ChatCompletion(ctx, key, truncationChatRequest())
	require.NotNil(t, bErr)
	exchanges, api := upstream.snapshot()
	require.Len(t, exchanges, 1)
	require.Empty(t, api, "no upstream call is made without a token")
}

func TestWorkloadIdentityExchangeWithoutAccessToken(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, `{"token_type":"Bearer"}`, wifMessageResponse)
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.ChatCompletion(ctx, wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`))), truncationChatRequest())
	require.NotNil(t, bErr)
	require.Equal(t, http.StatusBadGateway, *bErr.StatusCode)
	require.False(t, *bErr.AllowFallbacks)
}

func TestWorkloadIdentityRefusesInsecureBaseURL(t *testing.T) {
	cfg := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`))).AnthropicKeyConfig.WorkloadIdentity
	_, bErr := exchangeWorkloadIdentityToken(context.Background(), http.DefaultClient, "http://anthropic.example/v1/oauth/token", cfg, nil)
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "https")
	require.False(t, *bErr.AllowFallbacks)

	require.True(t, secureTokenURL("https://api.anthropic.com/v1/oauth/token"))
	require.True(t, secureTokenURL("http://127.0.0.1:8080/v1/oauth/token"))
	require.True(t, secureTokenURL("http://localhost/v1/oauth/token"))
	require.False(t, secureTokenURL("http://10.0.0.1/v1/oauth/token"))
}

// A single-use identity token the platform has not rotated yet is not presented again: the
// current access token keeps serving and the source is re-read shortly. Once it rotates, the new
// one is exchanged. A reusable identity token (no jti) is re-exchanged as usual.
func TestWorkloadIdentityRefreshFollowsTokenRotation(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, wifMessageResponse)
	first := fakeIdentityToken(`{"sub":"s","jti":"j1"}`)
	path := writeIdentityTokenFile(t, first)
	cfg := wifKey(path).AnthropicKeyConfig.WorkloadIdentity
	tokenURL := upstream.URL + workloadIdentityTokenPath
	hc := upstream.Client()

	entry, bErr := exchangeWorkloadIdentityToken(context.Background(), hc, tokenURL, cfg, nil)
	require.Nil(t, bErr)
	require.True(t, entry.Value.singleUse)
	require.WithinDuration(t, time.Now().Add(time.Hour-workloadIdentityRefreshMargin), entry.RefreshAt, 5*time.Second)

	unrotated, bErr := exchangeWorkloadIdentityToken(context.Background(), hc, tokenURL, cfg, entry)
	require.Nil(t, bErr)
	require.Equal(t, entry.Value.accessToken, unrotated.Value.accessToken)
	require.Equal(t, entry.ExpiresAt, unrotated.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(unrotatedRecheckInterval), unrotated.RefreshAt, time.Second)
	exchanges, _ := upstream.snapshot()
	require.Len(t, exchanges, 1, "an unrotated single-use identity token is not presented twice")

	second := fakeIdentityToken(`{"sub":"s","jti":"j2"}`)
	require.NoError(t, os.WriteFile(path, []byte(second), 0o600))
	_, bErr = exchangeWorkloadIdentityToken(context.Background(), hc, tokenURL, cfg, unrotated)
	require.Nil(t, bErr)
	exchanges, _ = upstream.snapshot()
	require.Len(t, exchanges, 2)
	require.Contains(t, string(exchanges[1].body), second, "the token file is re-read on every exchange")

	reusable := fakeIdentityToken(`{"sub":"s"}`)
	require.NoError(t, os.WriteFile(path, []byte(reusable), 0o600))
	entry, bErr = exchangeWorkloadIdentityToken(context.Background(), hc, tokenURL, cfg, nil)
	require.Nil(t, bErr)
	require.False(t, entry.Value.singleUse)
	_, bErr = exchangeWorkloadIdentityToken(context.Background(), hc, tokenURL, cfg, entry)
	require.Nil(t, bErr)
	exchanges, _ = upstream.snapshot()
	require.Len(t, exchanges, 4)
}

func TestWorkloadIdentityInlineIdentityToken(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, wifMessageResponse)
	assertion := fakeIdentityToken(`{"sub":"s"}`)
	key := wifKey("")
	key.AnthropicKeyConfig.WorkloadIdentity.IdentityTokenFile = nil
	key.AnthropicKeyConfig.WorkloadIdentity.IdentityToken = schemas.NewSecretVar(assertion)
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.ChatCompletion(ctx, key, truncationChatRequest())
	require.Nil(t, bErr)
	exchanges, api := upstream.snapshot()
	require.Len(t, exchanges, 1)
	require.Contains(t, string(exchanges[0].body), assertion)
	requireBearerAuth(t, api[0])
}

func TestReadIdentityTokenErrors(t *testing.T) {
	cfg := &schemas.AnthropicWorkloadIdentityConfig{IdentityTokenFile: schemas.NewSecretVar(filepath.Join(t.TempDir(), "missing"))}
	_, bErr := readIdentityToken(cfg)
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "could not be read")
	require.Nil(t, bErr.StatusCode, "a missing file is retried on the transient backoff: the volume may not be mounted yet")

	cfg.IdentityTokenFile = schemas.NewSecretVar(writeIdentityTokenFile(t, "  "))
	_, bErr = readIdentityToken(cfg)
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "is empty")

	cfg.IdentityTokenFile = schemas.NewSecretVar(writeIdentityTokenFile(t, strings.Repeat("a", maxIdentityTokenBytes+1)))
	_, bErr = readIdentityToken(cfg)
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "limit")
}

func TestHasJTIClaim(t *testing.T) {
	require.True(t, hasJTIClaim(fakeIdentityToken(`{"sub":"s","jti":"j1"}`)))
	require.False(t, hasJTIClaim(fakeIdentityToken(`{"sub":"s"}`)))
	require.False(t, hasJTIClaim("not-a-jwt"))
	require.False(t, hasJTIClaim("a.!!!.c"))
}

// Files, batches and passthrough build their requests directly; each must carry the bearer and
// keep its own betas.
func TestWorkloadIdentityDirectRequestPaths(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, `{"data":[],"has_more":false,"first_id":null,"last_id":null}`)
	key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`)))
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.FileList(ctx, []schemas.Key{key}, &schemas.BifrostFileListRequest{Provider: schemas.Anthropic})
	require.Nil(t, bErr)
	_, bErr = provider.Passthrough(ctx, key, &schemas.BifrostPassthroughRequest{
		Method:      http.MethodGet,
		Path:        "/v1/models",
		SafeHeaders: map[string]string{AnthropicBetaHeader: "client-beta-2026-01-01"},
	})
	require.Nil(t, bErr)

	_, api := upstream.snapshot()
	require.Len(t, api, 2)
	for _, rec := range api {
		requireBearerAuth(t, rec)
	}
	require.Contains(t, strings.Split(api[0].header.Get(AnthropicBetaHeader), ","), AnthropicFilesAPIBetaHeader)
	require.Contains(t, strings.Split(api[1].header.Get(AnthropicBetaHeader), ","), "client-beta-2026-01-01")
}

// A key whose token cannot be minted is skipped like any failing key, and the next one serves.
func TestWorkloadIdentityMultiKeyLoopSkipsFailedMint(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK,
		`{"id":"msgbatch_1","type":"message_batch","processing_status":"ended","request_counts":{"processing":0,"succeeded":1,"errored":0,"canceled":0,"expired":0}}`)
	broken := wifKey(filepath.Join(t.TempDir(), "missing"))
	static := schemas.Key{ID: "static", Value: *schemas.NewSecretVar("sk-ant-static")}
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.BatchRetrieve(ctx, []schemas.Key{broken, static}, &schemas.BifrostBatchRetrieveRequest{Provider: schemas.Anthropic, BatchID: "msgbatch_1"})
	require.Nil(t, bErr)
	exchanges, api := upstream.snapshot()
	require.Empty(t, exchanges)
	require.Len(t, api, 1)
	require.Equal(t, "sk-ant-static", api[0].header.Get("x-api-key"))
}

func BenchmarkResolveKeyStatic(b *testing.B) {
	provider := newWIFTestProvider("https://api.anthropic.com")
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-ant-static")}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_ = provider.resolveKey(ctx, &key)
	}
}

func BenchmarkResolveKeyWorkloadIdentityWarm(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(wifTokenOK))
	}))
	defer upstream.Close()
	path := filepath.Join(b.TempDir(), "token")
	require.NoError(b, os.WriteFile(path, []byte(fakeIdentityToken(`{"sub":"s"}`)), 0o600))
	provider := newWIFTestProvider(upstream.URL)
	key := wifKey(path)
	ctx := context.Background()
	warm := key
	require.Nil(b, provider.resolveKey(ctx, &warm))
	b.ReportAllocs()
	for b.Loop() {
		k := key
		_ = provider.resolveKey(ctx, &k)
	}
}
