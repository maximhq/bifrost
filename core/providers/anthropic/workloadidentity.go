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
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	schemas "github.com/maximhq/bifrost/core/schemas"
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
// Retained memory: one cache slot per distinct federated credential (rule, organization, service
// account, workspace, token source) seen by the provider, holding the access token and a digest
// of the identity token it was minted from. The cache lives on the provider, so a provider
// rebuild drops it.
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
	// workloadIdentityFallbackLifetime applies when the exchange response carries no expires_in.
	workloadIdentityFallbackLifetime = 5 * time.Minute
	// unrotatedRecheckInterval is how often a token minted from a single-use identity token that has
	// not rotated yet looks at its source again during the refresh window.
	unrotatedRecheckInterval = 5 * time.Second
)

// workloadIdentityToken is a minted access token and the identity token it was exchanged for.
type workloadIdentityToken struct {
	accessToken     string
	assertionDigest [sha256.Size]byte
	singleUse       bool // the identity token carries a jti, so Anthropic accepts it only once
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
// token when there is none or it is due for refresh. The cache key is what the minted token acts
// as (rule, organization, service account, workspace, endpoint) plus the token file, never the
// identity token itself: hashing a JWT on every request costs more than it buys, since two keys
// that share every identifier mint interchangeable tokens.
func (provider *AnthropicProvider) workloadIdentityAccessToken(ctx context.Context, cfg *schemas.AnthropicWorkloadIdentityConfig) (string, *schemas.BifrostError) {
	tokenURL := provider.workloadIdentityTokenURL
	cacheKey := tokencache.CacheKey(workloadIdentityCacheKeyVersion,
		tokenURL,
		cfg.FederationRuleID.GetValue(),
		cfg.OrganizationID.GetValue(),
		cfg.ServiceAccountID.GetValue(),
		cfg.WorkspaceID.GetValue(),
		cfg.IdentityTokenFile.GetValue(),
	)
	entry, bErr := provider.tokens.Get(ctx, cacheKey, func(ctx context.Context, prev *tokencache.Entry[workloadIdentityToken]) (*tokencache.Entry[workloadIdentityToken], *schemas.BifrostError) {
		return exchangeWorkloadIdentityToken(ctx, provider.authHTTPClient, tokenURL, cfg, prev)
	})
	if bErr != nil {
		return "", bErr
	}
	return entry.Value.accessToken, nil
}

// exchangeWorkloadIdentityToken reads the identity token and exchanges it for an access token.
func exchangeWorkloadIdentityToken(ctx context.Context, hc *http.Client, tokenURL string, cfg *schemas.AnthropicWorkloadIdentityConfig, prev *tokencache.Entry[workloadIdentityToken]) (*tokencache.Entry[workloadIdentityToken], *schemas.BifrostError) {
	if !secureTokenURL(tokenURL) {
		return nil, workloadIdentityConfigError("anthropic workload identity needs an https base_url (http is allowed for loopback hosts only), since the identity token is sent to it")
	}
	assertion, bErr := readIdentityToken(cfg)
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
	return &tokencache.Entry[workloadIdentityToken]{
		Value: workloadIdentityToken{
			accessToken:     parsed.AccessToken,
			assertionDigest: digest,
			singleUse:       hasJTIClaim(assertion),
		},
		ExpiresAt: now.Add(lifetime),
		RefreshAt: now.Add(lifetime - margin),
	}, nil
}

// readIdentityToken returns the identity token, re-reading the file on every call so a token the
// platform rotates on disk (a Kubernetes projected service account token) is always current.
func readIdentityToken(cfg *schemas.AnthropicWorkloadIdentityConfig) (string, *schemas.BifrostError) {
	var token string
	if path := strings.TrimSpace(cfg.IdentityTokenFile.GetValue()); path != "" {
		f, err := os.Open(path)
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
	} else {
		token = strings.TrimSpace(cfg.IdentityToken.GetValue())
		if token == "" {
			return "", workloadIdentityConfigError("anthropic workload identity has no identity token: set identity_token_file or identity_token")
		}
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
