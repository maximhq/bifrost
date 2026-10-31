package anthropic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Workload Identity Federation: a key with anthropic_key_config.workload_identity and no value
// exchanges the workload's OIDC identity token at POST {base_url}/v1/oauth/token (RFC 7523
// jwt-bearer grant, JSON body) for a short-lived sk-ant-oat01 access token. That token is sent
// as an OAuth bearer together with the OAuth beta, matching the official Anthropic SDKs.
//
// Request-path cost: a key without the config pays one nil check in resolveKey. A federated key
// pays the token cache's warm path (a cache-key hash and one sync.Map load); the exchange itself
// runs once per token lifetime, single-flight per credential.
//
// Retained memory: one cache slot per distinct federated credential (endpoint, rule, organization,
// service account, workspace, token source) the process has seen, a few hundred bytes holding the
// access token and a digest of the identity token it was minted from. Slots are kept for the life of
// the process, as tokencache keeps them; see workloadIdentityTokens for why the cache is not per provider.
const (
	// anthropicFederationBetaHeader is sent with the OAuth beta on jwt-bearer exchanges.
	anthropicFederationBetaHeader = "oidc-federation-2026-04-01"

	workloadIdentityTokenPath       = "/v1/oauth/token"
	workloadIdentityGrantType       = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	workloadIdentityCacheKeyVersion = "anthropic-wif-v1"
	// maxIdentityTokenBytes is the largest assertion Anthropic accepts.
	maxIdentityTokenBytes = 16 * 1024
	// maxExchangeResponseBytes caps a token endpoint response; a real one is a few hundred bytes.
	maxExchangeResponseBytes = 64 * 1024
	// workloadIdentityRefreshMargin is how long before expiry a token is re-minted, at most half its lifetime.
	workloadIdentityRefreshMargin = 120 * time.Second
	// workloadIdentityExpirySafety is how long before expiry a token stops being sent at all, at most
	// an eighth of its lifetime, so a token cannot lapse on its way to Anthropic.
	workloadIdentityExpirySafety = 30 * time.Second
	// workloadIdentityFallbackLifetime applies when the exchange response carries no expires_in.
	workloadIdentityFallbackLifetime = 5 * time.Minute
	// unrotatedRecheckInterval is how often a token minted from a single-use identity token that has
	// not rotated yet looks at its source again during the refresh window.
	unrotatedRecheckInterval = 5 * time.Second
)

// rejectedTokenMinAge is how long a token must have been in use before a 401 from Anthropic drops
// it. A token rejected right after its exchange is not repaired by another exchange, so this keeps
// a persistent rejection from turning every request into a token request. A var so tests can lower it.
var rejectedTokenMinAge = 30 * time.Second

// workloadIdentityTokens caches minted access tokens by credential, at package level like Copilot's
// copilotTokens: adding, editing or removing any key rebuilds the provider, and a per-provider cache
// would lose every token, making the next request re-present a single-use identity token Anthropic
// has already accepted. The cache key names the endpoint and the whole credential, so providers can
// share it. A refresh that fails while the current token still has serving time left keeps that
// token until its backoff or Retry-After elapses, as the official SDKs do, even on a 401: Anthropic
// answers temporary faults, such as its JWKS cache lagging a signing-key rotation, with the same
// opaque 401 as a real denial.
var workloadIdentityTokens = tokencache.New[workloadIdentityToken](tokencache.Options{KeepOnPermanentFailure: true})

// workloadIdentityToken is a minted access token and the identity token it was exchanged for.
type workloadIdentityToken struct {
	accessToken     string
	assertionDigest [sha256.Size]byte
	singleUse       bool      // the identity token carries a jti, so Anthropic accepts it only once
	mintedAt        time.Time // when the exchange returned the access token
}

// workloadIdentityExchangeRequest is the token endpoint request body.
type workloadIdentityExchangeRequest struct {
	GrantType        string `json:"grant_type"`
	Assertion        string `json:"assertion"`
	FederationRuleID string `json:"federation_rule_id"`
	OrganizationID   string `json:"organization_id"`
	ServiceAccountID string `json:"service_account_id,omitempty"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
}

// workloadIdentityExchangeResponse is the RFC 6749 §5.1 success body, plus the Anthropic API error
// shape the endpoint uses for failures.
type workloadIdentityExchangeResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// newWorkloadIdentityHTTPClient returns the client token exchanges are sent with. It honours
// proxy_config and the TLS settings like every other upstream call, and never follows a redirect:
// a 307/308 would replay the identity token to a host the base URL check never saw.
func newWorkloadIdentityHTTPClient(config *schemas.ProviderConfig, logger schemas.Logger) *http.Client {
	hc := providerUtils.NewProviderHTTPClient(config.ProxyConfig, config.NetworkConfig, logger)
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return hc
}

// resolveKey leaves key alone unless it federates: then it sets Value to the access token minted
// (or served from cache) for its workload identity, which setKeyAuth sends as a bearer. key is the
// calling method's own copy, so it is updated in place and a static key pays only the nil check.
// A static value wins over the config, as with oauth_key_config, and the config is dropped from
// the copy so the value goes out as x-api-key.
func (provider *AnthropicProvider) resolveKey(ctx context.Context, key *schemas.Key) *schemas.BifrostError {
	if !key.AnthropicKeyConfig.HasWorkloadIdentity() {
		return nil
	}
	// Trimmed, as validateKey and key selection treat a whitespace-only value as no value.
	if strings.TrimSpace(key.Value.GetValue()) != "" {
		key.AnthropicKeyConfig = nil
		return nil
	}
	token, bErr := provider.workloadIdentityAccessToken(ctx, key.AnthropicKeyConfig.WorkloadIdentity)
	if bErr != nil {
		return bErr
	}
	key.Value = schemas.SecretVar{Val: token, SecretType: schemas.SecretTypePlainText}
	return nil
}

// workloadIdentityAccessToken returns the cached access token for cfg, exchanging the identity
// token when there is none or it is due for refresh.
func (provider *AnthropicProvider) workloadIdentityAccessToken(ctx context.Context, cfg *schemas.AnthropicWorkloadIdentityConfig) (string, *schemas.BifrostError) {
	tokenURL := provider.workloadIdentityTokenURL
	entry, bErr := workloadIdentityTokens.Get(ctx, provider.workloadIdentityCacheKey(cfg), func(ctx context.Context, prev *tokencache.Entry[workloadIdentityToken]) (*tokencache.Entry[workloadIdentityToken], *schemas.BifrostError) {
		return exchangeWorkloadIdentityToken(ctx, provider.authHTTPClient, tokenURL, cfg, prev)
	})
	if bErr != nil {
		return "", bErr
	}
	return entry.Value.accessToken, nil
}

// workloadIdentityCacheKey identifies the token cache slot of a federated credential: what the
// minted token acts as (endpoint, rule, organization, service account, workspace) plus the identity
// source that proves it, the token file or the inline token. The proof is part of
// the key because the identifiers alone are only configuration: two keys that share them but hold
// different identity tokens must never serve each other's access token.
func (provider *AnthropicProvider) workloadIdentityCacheKey(cfg *schemas.AnthropicWorkloadIdentityConfig) string {
	src := cfg.IdentitySource
	if src == nil {
		src = &schemas.IdentitySource{}
	}
	return tokencache.CacheKey(workloadIdentityCacheKeyVersion,
		provider.workloadIdentityTokenURL,
		cfg.FederationRuleID.GetValue(),
		cfg.OrganizationID.GetValue(),
		cfg.ServiceAccountID.GetValue(),
		cfg.WorkspaceID.GetValue(),
		string(src.Type),
		src.Path.GetValue(),
		src.Token.GetValue(),
	)
}

// dropRejectedToken discards a federated key's cached access token when Anthropic answered a request
// made with it with 401, so the next request exchanges again instead of reusing a token Anthropic no
// longer accepts until its refresh time, as Vertex's removeVertexClient does. key is the resolved key
// the request was sent with. Only 401 counts: a 403 on a federated token means the endpoint is
// outside the rule's scope, which no new token changes. The slot is left alone unless it still holds
// the rejected token and that token has been in use for rejectedTokenMinAge. A token minted from a
// single-use identity token also stays while the source still holds that identity token: another
// exchange cannot succeed, so dropping it on a 401 that says nothing about the token (a client's
// passthrough call to the token endpoint, say) would take the key down until the platform rotates
// it. The clear is InvalidateEntry against the entry that was checked, so a token a concurrent
// refresh has just stored survives, and that refresh is not made to exchange its single-use
// identity token twice.
func (provider *AnthropicProvider) dropRejectedToken(key schemas.Key, statusCode int) {
	if statusCode != http.StatusUnauthorized || !key.AnthropicKeyConfig.HasWorkloadIdentity() {
		return
	}
	cfg := key.AnthropicKeyConfig.WorkloadIdentity
	cacheKey := provider.workloadIdentityCacheKey(cfg)
	cached := workloadIdentityTokens.Peek(cacheKey)
	if cached == nil || cached.Value.accessToken != key.Value.GetValue() || time.Since(cached.Value.mintedAt) < rejectedTokenMinAge {
		return
	}
	if cached.Value.singleUse {
		if assertion, bErr := readIdentityToken(cfg.IdentitySource); bErr != nil || sha256.Sum256([]byte(assertion)) == cached.Value.assertionDigest {
			return
		}
	}
	workloadIdentityTokens.InvalidateEntry(cacheKey, cached)
}

// dropRejectedTokenOnError is dropRejectedToken for the error a request helper returned.
func (provider *AnthropicProvider) dropRejectedTokenOnError(key schemas.Key, bErr *schemas.BifrostError) {
	if bErr != nil && bErr.StatusCode != nil {
		provider.dropRejectedToken(key, *bErr.StatusCode)
	}
}

// parseKeyError is ParseAnthropicError for a response to a request authenticated with key, dropping
// a federated key's cached token when Anthropic rejected it.
func (provider *AnthropicProvider) parseKeyError(key schemas.Key, resp *fasthttp.Response) *schemas.BifrostError {
	provider.dropRejectedToken(key, resp.StatusCode())
	return ParseAnthropicError(resp)
}

// exchangeWorkloadIdentityToken reads the identity token and exchanges it for an access token.
func exchangeWorkloadIdentityToken(ctx context.Context, hc *http.Client, tokenURL string, cfg *schemas.AnthropicWorkloadIdentityConfig, prev *tokencache.Entry[workloadIdentityToken]) (*tokencache.Entry[workloadIdentityToken], *schemas.BifrostError) {
	if !secureTokenURL(tokenURL) {
		return nil, workloadIdentityConfigError("anthropic workload identity needs an https base_url (http is allowed for loopback hosts only), since the identity token is sent to it")
	}
	assertion, bErr := readIdentityToken(cfg.IdentitySource)
	if bErr != nil {
		return nil, bErr
	}
	digest := sha256.Sum256([]byte(assertion))
	// Anthropic refuses a single-use identity token it has already exchanged, so while the source
	// still holds the token the current access token was minted from, keep serving that access
	// token and look again shortly, rather than spending a refresh that is bound to be rejected.
	if now := time.Now(); prev != nil && prev.Value.singleUse && prev.Value.assertionDigest == digest && now.Before(prev.ExpiresAt) {
		next := *prev
		next.RefreshAt = now.Add(unrotatedRecheckInterval)
		if next.RefreshAt.After(prev.ExpiresAt) {
			next.RefreshAt = prev.ExpiresAt
		}
		return &next, nil
	}

	body, err := sonic.Marshal(workloadIdentityExchangeRequest{
		GrantType:        workloadIdentityGrantType,
		Assertion:        assertion,
		FederationRuleID: strings.TrimSpace(cfg.FederationRuleID.GetValue()),
		OrganizationID:   strings.TrimSpace(cfg.OrganizationID.GetValue()),
		ServiceAccountID: strings.TrimSpace(cfg.ServiceAccountID.GetValue()),
		WorkspaceID:      strings.TrimSpace(cfg.WorkspaceID.GetValue()),
	})
	if err != nil {
		return nil, workloadIdentityConfigError("anthropic workload identity token request could not be built: " + err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, workloadIdentityConfigError("anthropic workload identity token request could not be built: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(AnthropicBetaHeader, AnthropicOAuthBetaHeader+","+anthropicFederationBetaHeader)

	resp, err := hc.Do(req)
	if err != nil {
		// Status 0 classifies as transient: the endpoint could not be reached.
		return nil, providerUtils.NewProviderAPIError("anthropic workload identity token exchange could not reach "+tokenURL, scrubbedError(err, assertion), 0, nil, nil)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxExchangeResponseBytes))
	if err != nil {
		return nil, providerUtils.NewProviderAPIError("anthropic workload identity token response could not be read", scrubbedError(err, assertion), 0, nil, nil)
	}

	var parsed workloadIdentityExchangeResponse
	_ = sonic.Unmarshal(respBody, &parsed)
	if resp.StatusCode != http.StatusOK {
		bErr := workloadIdentityExchangeError(resp.StatusCode, &parsed, assertion, cfg.WorkspaceID.GetValue() == "")
		providerUtils.ApplyRetryAfterHTTP(bErr, resp.Header)
		return nil, bErr
	}
	if parsed.AccessToken == "" {
		return nil, workloadIdentityBadResponseError("returned no access_token")
	}
	if parsed.TokenType != "" && !strings.EqualFold(parsed.TokenType, "Bearer") {
		return nil, workloadIdentityBadResponseError(fmt.Sprintf("returned token_type %q, want Bearer", parsed.TokenType))
	}

	now := time.Now()
	lifetime := time.Duration(parsed.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = workloadIdentityFallbackLifetime
	}
	margin := min(workloadIdentityRefreshMargin, lifetime/2)
	// ExpiresAt is when the cache stops serving the token, which is also the last moment a failed
	// refresh may fall back on it: the safety window keeps that short of the real expiry.
	safety := min(workloadIdentityExpirySafety, lifetime/8)
	return &tokencache.Entry[workloadIdentityToken]{
		Value: workloadIdentityToken{
			accessToken:     parsed.AccessToken,
			assertionDigest: digest,
			singleUse:       hasJTIClaim(assertion),
			mintedAt:        now,
		},
		ExpiresAt: now.Add(lifetime - safety),
		RefreshAt: now.Add(lifetime - margin),
	}, nil
}

// identityTokenDirsEnv lists, comma separated, the directories identity token files may be read
// from, replacing defaultIdentityTokenDirs. It is read from the environment only, never from a key,
// so whoever can edit a key cannot have Bifrost read an arbitrary local file and send it out.
const identityTokenDirsEnv = "BIFROST_OIDC_ALLOWED_CREDENTIAL_DIRS"

// defaultIdentityTokenDirs are the standard container credential mounts, where Kubernetes projects
// service account tokens.
var defaultIdentityTokenDirs = []string{"/var/run/secrets", "/run/secrets"}

// allowedIdentityTokenPath returns the real path of an identity token file once it is known to sit
// under an allowed credential directory. The path is checked before it is resolved, so a refusal
// says nothing about whether a file exists elsewhere, and again after, so a symlink cannot lead out
// of the directory; the symlinks Kubernetes rotates a projected token through stay inside its mount.
func allowedIdentityTokenPath(path string) (string, *schemas.BifrostError) {
	if !filepath.IsAbs(path) {
		return "", workloadIdentityConfigError("anthropic workload identity token file " + path + " must be an absolute path")
	}
	dirs := defaultIdentityTokenDirs
	if raw := os.Getenv(identityTokenDirsEnv); strings.TrimSpace(raw) != "" {
		dirs = strings.Split(raw, ",")
	}
	// Each directory as written and with its symlinks resolved, so both the configured path and the
	// real path of a file under it match (/var/run is a link to /run on most distributions).
	roots := make([]string, 0, 2*len(dirs))
	for _, dir := range dirs {
		if dir = strings.TrimSpace(dir); !filepath.IsAbs(dir) {
			continue
		}
		roots = append(roots, filepath.Clean(dir))
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			roots = append(roots, real)
		}
	}
	within := func(p string) bool {
		for _, root := range roots {
			if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	refuse := func() (string, *schemas.BifrostError) {
		return "", workloadIdentityConfigError("anthropic workload identity token file " + path + " is outside the allowed credential directories; set " +
			identityTokenDirsEnv + " to the directories to read from, comma separated (default " + strings.Join(defaultIdentityTokenDirs, ",") + ")")
	}
	if !within(filepath.Clean(path)) {
		return refuse()
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", providerUtils.NewConfigurationError("anthropic workload identity token file could not be read: " + err.Error())
	}
	if !within(resolved) {
		return refuse()
	}
	return resolved, nil
}

// readIdentityToken returns the identity token from src, re-reading a file source on every call so
// a token the platform rotates on disk (a Kubernetes projected service account token) is always
// current. The file is read only from an allowed credential directory (see allowedIdentityTokenPath).
func readIdentityToken(src *schemas.IdentitySource) (string, *schemas.BifrostError) {
	if src == nil {
		return "", workloadIdentityConfigError("anthropic workload identity has no identity_source")
	}
	var token string
	switch src.Type {
	case schemas.IdentitySourceFile:
		path := strings.TrimSpace(src.Path.GetValue())
		if path == "" {
			return "", workloadIdentityConfigError("anthropic workload identity source of type file has no path")
		}
		resolved, bErr := allowedIdentityTokenPath(path)
		if bErr != nil {
			return "", bErr
		}
		f, err := os.Open(resolved)
		if err != nil {
			return "", providerUtils.NewConfigurationError("anthropic workload identity token file could not be read: " + err.Error())
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxIdentityTokenBytes+1))
		if err != nil {
			return "", providerUtils.NewConfigurationError("anthropic workload identity token file could not be read: " + err.Error())
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return "", providerUtils.NewConfigurationError("anthropic workload identity token file " + path + " is empty")
		}
	case schemas.IdentitySourceInline:
		token = strings.TrimSpace(src.Token.GetValue())
		if token == "" {
			return "", workloadIdentityConfigError("anthropic workload identity source of type inline has no token")
		}
	default:
		return "", workloadIdentityConfigError(fmt.Sprintf("anthropic workload identity source type %q is not supported: use file or inline", src.Type))
	}
	if len(token) > maxIdentityTokenBytes {
		return "", workloadIdentityConfigError(fmt.Sprintf("anthropic workload identity token exceeds the %d-byte limit", maxIdentityTokenBytes))
	}
	return token, nil
}

// hasJTIClaim reports whether a JWT's payload carries a jti claim. Anthropic treats such an
// assertion as single-use; anything unparseable is treated as reusable and left to the endpoint.
func hasJTIClaim(assertion string) bool {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	return providerUtils.JSONFieldExists(payload, "jti")
}

// workloadIdentityExchangeError maps a non-2xx token endpoint answer. Anthropic answers every
// denied assertion with the same opaque 401 and records the reason only in the Console, so a 401
// carries the checks worth making. A 4xx other than 429 is a fault in this key's federation and
// blocks fallbacks, as for oauth_key_config; a rate limit or server error leaves them open.
func workloadIdentityExchangeError(statusCode int, parsed *workloadIdentityExchangeResponse, assertion string, workspaceUnset bool) *schemas.BifrostError {
	detail := "(no detail)"
	var errorType *string
	if parsed.Error != nil {
		if parsed.Error.Message != "" {
			detail = scrubAssertion(parsed.Error.Message, assertion)
		}
		if parsed.Error.Type != "" {
			errorType = new(parsed.Error.Type)
		}
	}
	message := fmt.Sprintf("anthropic workload identity token exchange failed (%d): %s", statusCode, detail)
	if parsed.RequestID != "" {
		message += " (request_id " + parsed.RequestID + ")"
	}
	if statusCode == http.StatusUnauthorized {
		message += ". Check that the identity token's iss, sub and aud match the federation rule, and that a single-use identity token rotates before the minted token expires"
		if workspaceUnset {
			message += "; set workspace_id when the rule spans several workspaces"
		}
		message += ". The deny reason is in the Claude Console under Workload identity, Authentication history"
	}
	bErr := providerUtils.NewProviderAPIError(message, nil, statusCode, errorType, nil)
	if tokencache.IsPermanentError(bErr) {
		bErr.AllowFallbacks = new(false)
	}
	return bErr
}

// workloadIdentityBadResponseError is a 2xx answer Bifrost cannot use. It carries 502, never the
// endpoint's 2xx, because the status becomes the one the client receives.
func workloadIdentityBadResponseError(problem string) *schemas.BifrostError {
	bErr := providerUtils.NewProviderAPIError("anthropic workload identity token endpoint "+problem, nil, http.StatusBadGateway, nil, nil)
	bErr.AllowFallbacks = new(false)
	return bErr
}

// workloadIdentityConfigError is a configuration fault that no fallback provider repairs.
func workloadIdentityConfigError(message string) *schemas.BifrostError {
	bErr := providerUtils.NewConfigurationError(message)
	bErr.AllowFallbacks = new(false)
	return bErr
}

// scrubAssertion masks the identity token wherever an error quotes it back.
func scrubAssertion(s, assertion string) string {
	if assertion == "" {
		return s
	}
	return strings.ReplaceAll(s, assertion, "[redacted]")
}

// scrubbedError rebuilds err with the identity token masked out of its text.
func scrubbedError(err error, assertion string) error {
	if msg := err.Error(); strings.Contains(msg, assertion) {
		return fmt.Errorf("%s", scrubAssertion(msg, assertion))
	}
	return err
}

// secureTokenURL reports whether the identity token may be sent to tokenURL: https, or http to a
// loopback host where it never leaves the machine.
func secureTokenURL(tokenURL string) bool {
	u, err := url.Parse(tokenURL)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}
