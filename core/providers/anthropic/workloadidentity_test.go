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
	"strconv"
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

// writeIdentityTokenFile writes token to a temp file and allows the temp directory to be read from.
func writeIdentityTokenFile(t *testing.T, token string) string {
	t.Helper()
	t.Setenv(identityTokenDirsEnv, os.TempDir())
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(token+"\n"), 0o600))
	return path
}

func wifKey(tokenFile string) schemas.Key {
	return schemas.Key{
		ID: "wif-key",
		AnthropicKeyConfig: &schemas.AnthropicKeyConfig{WorkloadIdentity: &schemas.AnthropicWorkloadIdentityConfig{
			FederationRuleID: *schemas.NewSecretVar("fdrl_rule"),
			OrganizationID:   *schemas.NewSecretVar("00000000-0000-0000-0000-000000000000"),
			ServiceAccountID: *schemas.NewSecretVar("svac_account"),
			WorkspaceID:      schemas.NewSecretVar("wrkspc_workspace"),
			IdentitySource:   fileSource(tokenFile),
		}},
	}
}

func fileSource(path string) *schemas.IdentitySource {
	return &schemas.IdentitySource{Type: schemas.IdentitySourceFile, Path: schemas.NewSecretVar(path)}
}

func inlineSource(token string) *schemas.IdentitySource {
	return &schemas.IdentitySource{Type: schemas.IdentitySourceInline, Token: schemas.NewSecretVar(token)}
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
	key.AnthropicKeyConfig.WorkloadIdentity.IdentitySource = inlineSource(assertion)
	provider := newWIFTestProvider(upstream.URL)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.ChatCompletion(ctx, key, truncationChatRequest())
	require.Nil(t, bErr)
	exchanges, api := upstream.snapshot()
	require.Len(t, exchanges, 1)
	require.Contains(t, string(exchanges[0].body), assertion)
	requireBearerAuth(t, api[0])
}

// The identity token is the proof behind a cached access token, so two keys that share every
// identifier but carry different inline tokens never share a cache slot: a key whose own token is
// rejected must fail rather than ride on the other key's access token.
func TestWorkloadIdentityInlineTokensDoNotShareCachedAccess(t *testing.T) {
	accepted := fakeIdentityToken(`{"sub":"accepted"}`)
	upstream := &wifUpstream{}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstream.mu.Lock()
		defer upstream.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != workloadIdentityTokenPath {
			upstream.api = append(upstream.api, wifRecorded{path: r.URL.Path, header: r.Header.Clone(), body: body})
			_, _ = w.Write([]byte(wifMessageResponse))
			return
		}
		upstream.exchanges = append(upstream.exchanges, wifRecorded{path: r.URL.Path, header: r.Header.Clone(), body: body})
		if !strings.Contains(string(body), accepted) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"}}`))
			return
		}
		_, _ = w.Write([]byte(wifTokenOK))
	}))
	t.Cleanup(upstream.Close)
	provider := newWIFTestProvider(upstream.URL)

	inlineKey := func(token string) schemas.Key {
		key := wifKey("")
		key.AnthropicKeyConfig.WorkloadIdentity.IdentitySource = inlineSource(token)
		return key
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.ChatCompletion(ctx, inlineKey(accepted), truncationChatRequest())
	require.Nil(t, bErr)

	_, bErr = provider.ChatCompletion(ctx, inlineKey(fakeIdentityToken(`{"sub":"rejected"}`)), truncationChatRequest())
	require.NotNil(t, bErr, "a key whose own identity token is rejected must not use another key's access token")
	require.Equal(t, http.StatusUnauthorized, *bErr.StatusCode)

	exchanges, api := upstream.snapshot()
	require.Len(t, exchanges, 2, "each inline token is exchanged on its own")
	require.Len(t, api, 1, "only the accepted key reaches the API")
}

// Adding, editing or removing any key rebuilds the provider. The token cache outlives the rebuild,
// so it does not re-present a single-use identity token Anthropic has already accepted.
func TestWorkloadIdentityTokenSurvivesProviderRebuild(t *testing.T) {
	upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, wifMessageResponse)
	key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s","jti":"j1"}`)))
	for range 2 {
		provider := newWIFTestProvider(upstream.URL) // what a key edit does: a new provider from the same config
		_, bErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, truncationChatRequest())
		require.Nil(t, bErr)
	}
	exchanges, api := upstream.snapshot()
	require.Len(t, exchanges, 1, "the rebuilt provider must reuse the token instead of exchanging again")
	require.Len(t, api, 2)
}

// A 401 for the old token can land while a refresh is already exchanging a rotated single-use
// identity token. Dropping the old token must not make that refresh throw its fresh token away and
// exchange again: Anthropic would refuse the identity token it has just accepted.
func TestWorkloadIdentityLateRejectionKeepsFreshToken(t *testing.T) {
	previous := rejectedTokenMinAge
	rejectedTokenMinAge = 0
	t.Cleanup(func() { rejectedTokenMinAge = previous })

	refreshStarted, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	seen := map[string]bool{}
	exchanges := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != workloadIdentityTokenPath {
			_, _ = w.Write([]byte(wifMessageResponse))
			return
		}
		var body struct {
			Assertion string `json:"assertion"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		exchanges++
		n, reused := exchanges, seen[body.Assertion]
		seen[body.Assertion] = true
		mu.Unlock()
		if n == 2 {
			close(refreshStarted)
			<-release
		}
		if reused { // single-use: Anthropic refuses an identity token it has already exchanged
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"sk-ant-oat01-minted-` + strconv.Itoa(n) + `","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(upstream.Close)

	path := writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s","jti":"j1"}`))
	key := wifKey(path)
	provider := newWIFTestProvider(upstream.URL)
	cacheKey := provider.workloadIdentityCacheKey(key.AnthropicKeyConfig.WorkloadIdentity)
	_, bErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, truncationChatRequest())
	require.Nil(t, bErr)

	// The platform rotates the identity token and the cached access token comes due for refresh.
	require.NoError(t, os.WriteFile(path, []byte(fakeIdentityToken(`{"sub":"s","jti":"j2"}`)), 0o600))
	workloadIdentityTokens.Expire(cacheKey)
	refreshed := make(chan *schemas.BifrostError, 1)
	go func() {
		_, bErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, truncationChatRequest())
		refreshed <- bErr
	}()
	<-refreshStarted

	// While that refresh is in flight, a request sent with the old token comes back 401.
	rejected := key
	rejected.Value = *schemas.NewSecretVar("sk-ant-oat01-minted-1")
	provider.dropRejectedToken(rejected, http.StatusUnauthorized)
	close(release)

	require.Nil(t, <-refreshed, "the in-flight refresh must keep the token it minted")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, exchanges, "the rotated identity token is exchanged once")
	require.Equal(t, "sk-ant-oat01-minted-2", workloadIdentityTokens.Peek(cacheKey).Value.accessToken)
}

func TestReadIdentityTokenErrors(t *testing.T) {
	t.Setenv(identityTokenDirsEnv, os.TempDir())
	_, bErr := readIdentityToken(fileSource(filepath.Join(t.TempDir(), "missing")))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "could not be read")
	require.Nil(t, bErr.StatusCode, "a missing file is retried on the transient backoff: the volume may not be mounted yet")

	_, bErr = readIdentityToken(fileSource(writeIdentityTokenFile(t, "  ")))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "is empty")

	_, bErr = readIdentityToken(fileSource(writeIdentityTokenFile(t, strings.Repeat("a", maxIdentityTokenBytes+1))))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "limit")

	_, bErr = readIdentityToken(inlineSource("  "))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "has no token")

	_, bErr = readIdentityToken(&schemas.IdentitySource{Type: "gcp_metadata"})
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "not supported")
	require.False(t, *bErr.AllowFallbacks)

	_, bErr = readIdentityToken(nil)
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "no identity_source")
}

// Identity token files are read only from the allowed credential directories, which the operator sets
// in the environment, so whoever can edit a key cannot have Bifrost read an arbitrary local file and
// send it to the token endpoint.
func TestWorkloadIdentityTokenFileAllowlist(t *testing.T) {
	const allowlistEnv = "BIFROST_OIDC_ALLOWED_CREDENTIAL_DIRS"
	assertion := fakeIdentityToken(`{"sub":"s"}`)
	write := func(dir, name, token string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(token), 0o600))
		return path
	}
	read := func(path string) (string, *schemas.BifrostError) {
		return readIdentityToken(fileSource(path))
	}
	requireRefused := func(t *testing.T, path string) {
		t.Helper()
		_, bErr := read(path)
		require.NotNil(t, bErr, "%s must be refused", path)
		require.Contains(t, bErr.Error.Message, allowlistEnv)
		require.NotContains(t, bErr.Error.Message, "could not be read", "a refusal must not say whether the file exists")
		require.False(t, *bErr.AllowFallbacks)
	}
	allowed := t.TempDir()
	outside := t.TempDir()
	inside := write(allowed, "token", assertion)

	t.Run("the default directories refuse a file elsewhere", func(t *testing.T) {
		t.Setenv(allowlistEnv, "")
		requireRefused(t, inside)
	})
	t.Run("a refused file never reaches the token endpoint", func(t *testing.T) {
		t.Setenv(allowlistEnv, "")
		upstream := newWIFUpstream(t, http.StatusOK, wifTokenOK, wifMessageResponse)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, bErr := newWIFTestProvider(upstream.URL).ChatCompletion(ctx, wifKey(inside), truncationChatRequest())
		require.NotNil(t, bErr)
		require.Contains(t, bErr.Error.Message, allowlistEnv)
		exchanges, api := upstream.snapshot()
		require.Empty(t, exchanges)
		require.Empty(t, api)
	})
	t.Run("a file under any listed directory is read", func(t *testing.T) {
		t.Setenv(allowlistEnv, " /nonexistent-bifrost-dir , "+allowed+" ")
		got, bErr := read(inside)
		require.Nil(t, bErr)
		require.Equal(t, assertion, got)
	})
	t.Run("a file outside every listed directory is refused, present or missing", func(t *testing.T) {
		t.Setenv(allowlistEnv, allowed)
		requireRefused(t, write(outside, "token", assertion))
		requireRefused(t, filepath.Join(outside, "missing"))
		requireRefused(t, write(allowed+"-sibling", "token", assertion))
		requireRefused(t, filepath.Join(allowed, "..", filepath.Base(outside), "token"))
	})
	t.Run("a symlink out of a listed directory is refused", func(t *testing.T) {
		t.Setenv(allowlistEnv, allowed)
		link := filepath.Join(allowed, "escape")
		require.NoError(t, os.Symlink(write(outside, "secret", assertion), link))
		requireRefused(t, link)
	})
	t.Run("a relative path is refused", func(t *testing.T) {
		t.Setenv(allowlistEnv, allowed)
		_, bErr := read("token")
		require.NotNil(t, bErr)
		require.Contains(t, bErr.Error.Message, "absolute")
	})
	// Kubernetes projects a token as token -> ..data/token with ..data a symlink it swaps on rotation,
	// all inside the mount.
	t.Run("a projected token rotating through symlinks in the mount is read", func(t *testing.T) {
		mount := filepath.Join(allowed, "projected")
		t.Setenv(allowlistEnv, mount)
		write(mount, filepath.Join("..2026_10_30_a", "token"), "first")
		require.NoError(t, os.Symlink("..2026_10_30_a", filepath.Join(mount, "..data")))
		require.NoError(t, os.Symlink(filepath.Join("..data", "token"), filepath.Join(mount, "token")))
		got, bErr := read(filepath.Join(mount, "token"))
		require.Nil(t, bErr)
		require.Equal(t, "first", got)

		write(mount, filepath.Join("..2026_10_30_b", "token"), "second")
		require.NoError(t, os.Symlink("..2026_10_30_b", filepath.Join(mount, "..data_tmp")))
		require.NoError(t, os.Rename(filepath.Join(mount, "..data_tmp"), filepath.Join(mount, "..data")))
		got, bErr = read(filepath.Join(mount, "token"))
		require.Nil(t, bErr)
		require.Equal(t, "second", got)
	})
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
	t.Setenv(identityTokenDirsEnv, os.TempDir())
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

// A token is never served in the last stretch of its life, so a refresh that fails while the token
// endpoint is down cannot fall back on a token that would lapse on its way to Anthropic.
func TestWorkloadIdentityTokenStopsServingBeforeExpiry(t *testing.T) {
	cfg := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`))).AnthropicKeyConfig.WorkloadIdentity
	for _, tc := range []struct {
		expiresIn          string
		wantExpiry, wantAt time.Duration // offsets from now: when serving stops, and when the refresh is due
	}{
		{"600", 570 * time.Second, 480 * time.Second},                    // 30s safety, 120s refresh margin
		{"60", 60*time.Second - 7500*time.Millisecond, 30 * time.Second}, // both capped for a short token
	} {
		t.Run(tc.expiresIn+"s", func(t *testing.T) {
			upstream := newWIFUpstream(t, http.StatusOK, `{"access_token":"`+wifMintedToken+`","token_type":"Bearer","expires_in":`+tc.expiresIn+`}`, wifMessageResponse)
			entry, bErr := exchangeWorkloadIdentityToken(context.Background(), upstream.Client(), upstream.URL+workloadIdentityTokenPath, cfg, nil)
			require.Nil(t, bErr)
			require.WithinDuration(t, time.Now().Add(tc.wantExpiry), entry.ExpiresAt, time.Second)
			require.WithinDuration(t, time.Now().Add(tc.wantAt), entry.RefreshAt, time.Second)
		})
	}
}

// newRejectingWIFUpstream mints sk-ant-oat01-minted-1, -2, ... in turn, and answers API requests
// made with the first one with rejectStatus while accepting every later one.
func newRejectingWIFUpstream(t *testing.T, rejectStatus int) *wifUpstream {
	t.Helper()
	u := &wifUpstream{}
	minted := 0
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := wifRecorded{path: r.URL.Path, header: r.Header.Clone(), body: body}
		u.mu.Lock()
		defer u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == workloadIdentityTokenPath {
			u.exchanges = append(u.exchanges, rec)
			minted++
			_, _ = w.Write([]byte(`{"access_token":"sk-ant-oat01-minted-` + strconv.Itoa(minted) + `","token_type":"Bearer","expires_in":3600}`))
			return
		}
		u.api = append(u.api, rec)
		if r.Header.Get("Authorization") == "Bearer sk-ant-oat01-minted-1" {
			w.WriteHeader(rejectStatus)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid bearer token"}}`))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/models/"):
			_, _ = w.Write([]byte(`{"type":"model","id":"claude-repro","display_name":"Claude","created_at":"2025-09-29T00:00:00Z"}`))
		default:
			_, _ = w.Write([]byte(wifMessageResponse))
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// A 401 from Anthropic drops the cached token, so the next request exchanges again and recovers,
// on every kind of call path: the shared request helpers, the provider's own requests, and passthrough.
func TestWorkloadIdentityUpstream401DropsToken(t *testing.T) {
	previous := rejectedTokenMinAge
	rejectedTokenMinAge = 0
	t.Cleanup(func() { rejectedTokenMinAge = previous })

	paths := map[string]func(provider *AnthropicProvider, key schemas.Key) int{
		"chat completion": func(provider *AnthropicProvider, key schemas.Key) int {
			_, bErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, truncationChatRequest())
			if bErr != nil {
				return *bErr.StatusCode
			}
			return http.StatusOK
		},
		"model retrieve": func(provider *AnthropicProvider, key schemas.Key) int {
			_, bErr := provider.ModelRetrieve(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, &schemas.BifrostModelRetrieveRequest{Model: "claude-repro"})
			if bErr != nil {
				return *bErr.StatusCode
			}
			return http.StatusOK
		},
		"passthrough": func(provider *AnthropicProvider, key schemas.Key) int {
			resp, bErr := provider.Passthrough(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, &schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: "/v1/messages", Body: []byte(`{}`)})
			require.Nil(t, bErr)
			return resp.StatusCode
		},
	}
	for name, call := range paths {
		t.Run(name, func(t *testing.T) {
			upstream := newRejectingWIFUpstream(t, http.StatusUnauthorized)
			provider := newWIFTestProvider(upstream.URL)
			key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`)))

			require.Equal(t, http.StatusUnauthorized, call(provider, key))
			require.Equal(t, http.StatusOK, call(provider, key), "the rejected token must not be reused")
			exchanges, api := upstream.snapshot()
			require.Len(t, exchanges, 2)
			require.Equal(t, "Bearer sk-ant-oat01-minted-2", api[1].header.Get("Authorization"))
		})
	}
}

// newSingleUseWIFUpstream refuses an identity token it has already exchanged, as Anthropic does for
// one with a jti, mints sk-ant-oat01-minted-1, -2, ... otherwise, and answers a request that reaches
// the token endpoint with a bearer (a passthrough call) with 401.
func newSingleUseWIFUpstream(t *testing.T) *wifUpstream {
	t.Helper()
	u := &wifUpstream{}
	seen := map[string]bool{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := wifRecorded{path: r.URL.Path, header: r.Header.Clone(), body: body}
		u.mu.Lock()
		defer u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != workloadIdentityTokenPath || r.Header.Get("Authorization") != "" {
			u.api = append(u.api, rec)
			if r.URL.Path == workloadIdentityTokenPath {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"}}`))
				return
			}
			_, _ = w.Write([]byte(wifMessageResponse))
			return
		}
		u.exchanges = append(u.exchanges, rec)
		var req struct {
			Assertion string `json:"assertion"`
		}
		_ = json.Unmarshal(body, &req)
		if seen[req.Assertion] {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"}}`))
			return
		}
		seen[req.Assertion] = true
		_, _ = w.Write([]byte(`{"access_token":"sk-ant-oat01-minted-` + strconv.Itoa(len(seen)) + `","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(u.Close)
	return u
}

// A 401 can come from a call that says nothing about the token, such as a client's passthrough
// request to the token endpoint. While the source still holds the single-use identity token the
// cached token was minted from, another exchange cannot succeed, so the token must survive; once the
// platform has rotated the identity token, a 401 drops it and the next request exchanges the new one.
func TestWorkloadIdentityRejectionKeepsTokenWhileIdentityTokenIsUnrotated(t *testing.T) {
	previous := rejectedTokenMinAge
	rejectedTokenMinAge = 0
	t.Cleanup(func() { rejectedTokenMinAge = previous })

	upstream := newSingleUseWIFUpstream(t)
	path := writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s","jti":"j1"}`))
	key := wifKey(path)
	provider := newWIFTestProvider(upstream.URL)
	chat := func() *schemas.BifrostError {
		_, bErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, truncationChatRequest())
		return bErr
	}
	junkPassthrough := func() {
		resp, bErr := provider.Passthrough(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key,
			&schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: workloadIdentityTokenPath, Body: []byte(`{"assertion":"junk"}`)})
		require.Nil(t, bErr)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}

	require.Nil(t, chat())
	junkPassthrough()
	require.Nil(t, chat(), "a 401 another exchange cannot repair must not take the key down")
	exchanges, _ := upstream.snapshot()
	require.Len(t, exchanges, 1, "the unrotated single-use identity token is not presented again")

	require.NoError(t, os.WriteFile(path, []byte(fakeIdentityToken(`{"sub":"s","jti":"j2"}`)), 0o600))
	junkPassthrough()
	require.Nil(t, chat())
	exchanges, api := upstream.snapshot()
	require.Len(t, exchanges, 2, "after rotation the 401 drops the token and the new identity token is exchanged")
	require.Equal(t, "Bearer sk-ant-oat01-minted-2", api[len(api)-1].header.Get("Authorization"))
}

// newSwitchableWIFUpstream answers the token endpoint with tokenBody until reject is called, and
// with an Anthropic error of the given status (and Retry-After, when set) after it.
func newSwitchableWIFUpstream(t *testing.T, tokenBody string) (*wifUpstream, func(status int, retryAfter string)) {
	t.Helper()
	u := &wifUpstream{}
	rejectStatus, rejectRetryAfter := 0, ""
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := wifRecorded{path: r.URL.Path, header: r.Header.Clone(), body: body}
		u.mu.Lock()
		defer u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != workloadIdentityTokenPath {
			u.api = append(u.api, rec)
			_, _ = w.Write([]byte(wifMessageResponse))
			return
		}
		u.exchanges = append(u.exchanges, rec)
		if rejectStatus != 0 {
			if rejectRetryAfter != "" {
				w.Header().Set("Retry-After", rejectRetryAfter)
			}
			w.WriteHeader(rejectStatus)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Authentication failed"}}`))
			return
		}
		_, _ = w.Write([]byte(tokenBody))
	}))
	t.Cleanup(u.Close)
	return u, func(status int, retryAfter string) {
		u.mu.Lock()
		defer u.mu.Unlock()
		rejectStatus, rejectRetryAfter = status, retryAfter
	}
}

// Anthropic answers temporary faults, such as its JWKS cache lagging a signing-key rotation, with
// the same opaque 401 as a real denial. A refresh that fails while the current token still has
// serving time left keeps that token, as the official SDKs do, and the next attempt waits for the
// failure's backoff or the endpoint's Retry-After; the failure reaches callers only once the token
// is out of serving time.
func TestWorkloadIdentityFailedRefreshKeepsServingValidToken(t *testing.T) {
	chat := func(provider *AnthropicProvider, key schemas.Key) *schemas.BifrostError {
		_, bErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, truncationChatRequest())
		return bErr
	}

	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
	}{
		{"a 401 while the token has serving time left", http.StatusUnauthorized, ""},
		{"a 429 with Retry-After while the token has serving time left", http.StatusTooManyRequests, "120"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, reject := newSwitchableWIFUpstream(t, wifTokenOK)
			key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`)))
			provider := newWIFTestProvider(upstream.URL)
			cacheKey := provider.workloadIdentityCacheKey(key.AnthropicKeyConfig.WorkloadIdentity)
			require.Nil(t, chat(provider, key))

			reject(tc.status, tc.retryAfter)
			workloadIdentityTokens.Expire(cacheKey)
			require.Nil(t, chat(provider, key), "a failed refresh must not reach callers while the token is still good")
			exchanges, api := upstream.snapshot()
			require.Len(t, exchanges, 2, "the refresh was attempted")
			requireBearerAuth(t, api[len(api)-1])

			// The token comes due again at once; the failure's wait still holds off the next exchange.
			workloadIdentityTokens.Expire(cacheKey)
			require.Nil(t, chat(provider, key))
			exchanges, api = upstream.snapshot()
			require.Len(t, exchanges, 2, "a retry inside the failure's backoff or Retry-After must not reach the endpoint")
			requireBearerAuth(t, api[len(api)-1])
		})
	}

	t.Run("once the token is out of serving time", func(t *testing.T) {
		upstream, reject := newSwitchableWIFUpstream(t, `{"access_token":"`+wifMintedToken+`","token_type":"Bearer","expires_in":1}`)
		key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`)))
		provider := newWIFTestProvider(upstream.URL)
		require.Nil(t, chat(provider, key))

		reject(http.StatusUnauthorized, "")
		time.Sleep(time.Second) // serving stops 125ms before a 1s token expires
		bErr := chat(provider, key)
		require.NotNil(t, bErr)
		require.Equal(t, http.StatusUnauthorized, *bErr.StatusCode)
	})
}

// The cases that must leave the cached token alone: a 403 (the endpoint is outside the rule's
// scope, which a new token does not change), and a 401 on a token minted moments ago (another
// exchange would not help, and dropping it would send every request to the token endpoint).
func TestWorkloadIdentityKeepsTokenWhenRejectionIsNotRepairable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		minAge time.Duration
	}{
		{"403 scope rejection", http.StatusForbidden, 0},
		{"401 on a freshly minted token", http.StatusUnauthorized, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := rejectedTokenMinAge
			rejectedTokenMinAge = tc.minAge
			t.Cleanup(func() { rejectedTokenMinAge = previous })

			upstream := newRejectingWIFUpstream(t, tc.status)
			provider := newWIFTestProvider(upstream.URL)
			key := wifKey(writeIdentityTokenFile(t, fakeIdentityToken(`{"sub":"s"}`)))
			for range 2 {
				_, bErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, truncationChatRequest())
				require.NotNil(t, bErr)
				require.Equal(t, tc.status, *bErr.StatusCode)
			}
			exchanges, _ := upstream.snapshot()
			require.Len(t, exchanges, 1, "the token must be kept, so no second exchange")
		})
	}

	t.Run("a static key's 401 touches no cache", func(t *testing.T) {
		provider := newWIFTestProvider("https://api.anthropic.com")
		provider.dropRejectedToken(schemas.Key{Value: *schemas.NewSecretVar("sk-ant-static")}, http.StatusUnauthorized)
		provider.dropRejectedTokenOnError(schemas.Key{}, &schemas.BifrostError{StatusCode: new(http.StatusUnauthorized)})
	})
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
	b.Setenv(identityTokenDirsEnv, os.TempDir())
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

func BenchmarkResolveKeyWorkloadIdentityInlineWarm(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(wifTokenOK))
	}))
	defer upstream.Close()
	provider := newWIFTestProvider(upstream.URL)
	key := wifKey("")
	// A realistically sized identity token: projected Kubernetes and cloud tokens run 800-1200 bytes.
	key.AnthropicKeyConfig.WorkloadIdentity.IdentitySource = inlineSource(fakeIdentityToken(`{"sub":"system:serviceaccount:ai:bifrost","pad":"` + strings.Repeat("x", 700) + `"}`))
	ctx := context.Background()
	warm := key
	require.Nil(b, provider.resolveKey(ctx, &warm))
	b.ReportAllocs()
	for b.Loop() {
		k := key
		_ = provider.resolveKey(ctx, &k)
	}
}
