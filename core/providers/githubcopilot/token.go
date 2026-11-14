package githubcopilot

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// githubCopilotTokenURL exchanges a GitHub user token for a Copilot API token and host.
	githubCopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	// githubCopilotUserURL describes the account's Copilot seat, including its plan's API host.
	githubCopilotUserURL   = "https://api.github.com/copilot_internal/user"
	copilotPublicAPIDomain = "githubcopilot.com"
	githubAcceptJSON       = "application/vnd.github+json"

	// tokenRefreshMargin is how long before expiry a token is considered stale.
	tokenRefreshMargin = 60 * time.Second
	// minRefreshIn floors the refresh_in hint Copilot returns, so a small or zero value
	// cannot turn into a refresh loop.
	minRefreshIn = 60 * time.Second
	// maxTokenLifetime rejects absurd expiry values rather than trusting them.
	maxTokenLifetime = 24 * time.Hour
	// maxExchangeBodyBytes caps every token exchange response.
	maxExchangeBodyBytes = 64 * 1024

	cacheKeyVersion = "ghcp-user-v1"

	// patCacheKeyVersion namespaces PAT host lookups apart from token exchanges.
	patCacheKeyVersion = "ghcp-pat-v1"
	// patHostLifetime is how long a looked-up plan host is reused. A host only changes when
	// the seat moves to another plan, so an hour costs one GitHub call per PAT per hour.
	patHostLifetime = time.Hour
)

// copilotToken is one cached exchange result. Immutable once stored.
type copilotToken struct {
	token     string
	baseURL   string
	expiresAt time.Time
	refreshAt time.Time
}

// copilotTokens caches Copilot tokens by GitHub user token. Caching, single-flight refresh and
// the negative cache live in tokencache; an entry is about 1 KB and is replaced on refresh.
var copilotTokens = tokencache.New[copilotToken](tokencache.Options{})

// copilotTokenResponse is the exchange response.
type copilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int64  `json:"refresh_in"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// copilotUserResponse is the part of the seat description that names the plan's API host.
type copilotUserResponse struct {
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// githubErrorBody is the bare error shape api.github.com returns.
type githubErrorBody struct {
	Message string `json:"message"`
}

// isGithubUserToken reports whether a key value is a GitHub OAuth token (ghu_ from a GitHub
// App user flow, gho_ from an OAuth app), which Copilot's token exchange accepts.
func isGithubUserToken(value string) bool {
	return strings.HasPrefix(value, "ghu_") || strings.HasPrefix(value, "gho_")
}

// isPersonalAccessToken reports whether a key value is a fine-grained PAT. Copilot's token
// exchange refuses PATs, but its API accepts one directly as the bearer token.
func isPersonalAccessToken(value string) bool {
	return strings.HasPrefix(value, "github_pat_")
}

// resolvePATCredentials returns credentials that send the PAT itself to Copilot, at the plan
// host looked up for the account and cached by PAT.
func resolvePATCredentials(
	ctx *schemas.BifrostContext,
	pat string,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	parent := context.Background()
	if ctx != nil {
		parent = ctx
	}
	return resolvePATWithUserURL(parent, githubCopilotUserURL, pat, client, configuredBaseURL, logger)
}

// resolvePATWithUserURL is split from resolvePATCredentials so tests can point the lookup at
// a fake GitHub. A configured base URL skips the lookup entirely.
func resolvePATWithUserURL(
	parent context.Context,
	userURL string,
	pat string,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	if baseURL := strings.TrimRight(strings.TrimSpace(configuredBaseURL), "/"); baseURL != "" {
		if err := requireSecureConfiguredURL(baseURL); err != nil {
			return nil, configurationError("github copilot: network_config.base_url " + err.Error())
		}
		return &copilotCredentials{Token: pat, BaseURL: baseURL, SDK: true}, nil
	}
	cacheKey := tokencache.CacheKey(patCacheKeyVersion, pat)
	entry, bErr := copilotTokens.Get(parent, cacheKey, func(ctx context.Context, _ *tokencache.Entry[copilotToken]) (*tokencache.Entry[copilotToken], *schemas.BifrostError) {
		baseURL, bErr := lookupPATHost(ctx, client, userURL, pat, logger)
		if bErr != nil {
			return nil, bErr
		}
		now := time.Now()
		return &tokencache.Entry[copilotToken]{
			Value:     copilotToken{baseURL: baseURL},
			ExpiresAt: now.Add(patHostLifetime),
			RefreshAt: now.Add(patHostLifetime - tokenRefreshMargin),
		}, nil
	})
	if bErr != nil {
		return nil, bErr
	}
	return &copilotCredentials{Token: pat, BaseURL: entry.Value.baseURL, SDK: true}, nil
}

// lookupPATHost reads the account's plan host from its Copilot seat description. Copilot
// serves paid plans from api.individual / api.business / api.enterprise and refuses a PAT on
// the wrong one, so the host cannot be guessed.
func lookupPATHost(ctx context.Context, client *fasthttp.Client, userURL, pat string, logger schemas.Logger) (string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(userURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Authorization", "token "+pat)
	req.Header.Set("Accept", githubAcceptJSON)
	req.Header.SetUserAgent(copilotUserAgent)

	if err := doRequest(ctx, client, req, resp); err != nil {
		return "", providerUtils.NewProviderAPIError(
			"github copilot: could not reach GitHub to look up the Copilot plan for this token", err, 0, nil, nil)
	}
	if status := resp.StatusCode(); status != http.StatusOK {
		bErr := classifyPATLookupError(status, resp.Body())
		providerUtils.ApplyRetryAfter(bErr, &resp.Header)
		return "", bErr
	}

	var parsed copilotUserResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return "", providerUtils.NewProviderAPIError(
			"github copilot: could not parse GitHub's Copilot seat response", err, resp.StatusCode(), nil, nil)
	}
	baseURL, err := validateCopilotAPIBaseURL(parsed.Endpoints.API)
	if err != nil {
		if logger != nil {
			logger.Warn("[github-copilot] ignoring the advertised Copilot host and falling back to %s: %v",
				defaultCopilotAPIBaseURL, err)
		}
		baseURL = defaultCopilotAPIBaseURL
	}
	return baseURL, nil
}

// classifyPATLookupError maps a failed seat lookup for a PAT to an error that names the fix.
func classifyPATLookupError(status int, body []byte) *schemas.BifrostError {
	detail := githubMessage(body)
	switch status {
	case http.StatusUnauthorized:
		return blockingError("github copilot: GitHub rejected the personal access token (401). It was "+
			"revoked or has expired; create a new one. GitHub said: "+detail, status)
	case http.StatusForbidden, http.StatusNotFound:
		return blockingError("github copilot: this personal access token has no Copilot access. The account "+
			"needs an active Copilot plan or an assigned seat. GitHub said: "+detail, status)
	default:
		return providerUtils.NewProviderAPIError(
			fmt.Sprintf("github copilot: Copilot plan lookup failed (%d): %s", status, detail), nil, status, nil, nil)
	}
}

// mintFromUserToken exchanges a GitHub user token for Copilot credentials through the cache,
// which refreshes them before expiry and learns the plan's API host from the exchange.
func mintFromUserToken(
	ctx *schemas.BifrostContext,
	userToken string,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	// A nil *BifrostContext must not reach the cache as a non-nil context.Context interface.
	parent := context.Background()
	if ctx != nil {
		parent = ctx
	}
	return mintWithTokenURL(parent, githubCopilotTokenURL, userToken, client, configuredBaseURL, logger)
}

// mintWithTokenURL is split from mintFromUserToken so tests can point the exchange at a fake
// GitHub. The cache detaches the exchange from the caller's cancellation, bounds it with its
// own timeout, collapses concurrent callers onto one exchange and caches failures.
func mintWithTokenURL(
	parent context.Context,
	tokenURL string,
	userToken string,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	configured := strings.TrimRight(strings.TrimSpace(configuredBaseURL), "/")
	if configured != "" {
		if err := requireSecureConfiguredURL(configured); err != nil {
			return nil, configurationError("github copilot: network_config.base_url " + err.Error())
		}
	}
	cacheKey := tokencache.CacheKey(cacheKeyVersion, userToken)
	entry, bErr := copilotTokens.Get(parent, cacheKey, func(ctx context.Context, _ *tokencache.Entry[copilotToken]) (*tokencache.Entry[copilotToken], *schemas.BifrostError) {
		tok, bErr := exchangeCopilotToken(ctx, client, tokenURL, userToken, logger)
		if bErr != nil {
			return nil, bErr
		}
		return &tokencache.Entry[copilotToken]{Value: *tok, ExpiresAt: tok.expiresAt, RefreshAt: tok.refreshAt}, nil
	})
	if bErr != nil {
		return nil, bErr
	}
	// An explicitly configured base URL wins over the advertised one, so an operator can
	// point at a recording proxy.
	baseURL := entry.Value.baseURL
	if configured != "" {
		baseURL = configured
	}
	return &copilotCredentials{Token: entry.Value.token, BaseURL: baseURL}, nil
}

// exchangeCopilotToken trades a GitHub user token for a Copilot API token and host.
func exchangeCopilotToken(
	ctx context.Context,
	client *fasthttp.Client,
	tokenURL string,
	userToken string,
	logger schemas.Logger,
) (*copilotToken, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(tokenURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Authorization", "token "+userToken)
	req.Header.Set("Accept", githubAcceptJSON)
	// Every shipping Copilot client sends these on the exchange. GitHub does not document
	// them, but omitting them is a known source of unexplained rejections.
	req.Header.Set("Editor-Version", editorVersion)
	req.Header.Set("Editor-Plugin-Version", editorPluginVersion)
	req.Header.SetUserAgent(copilotUserAgent)

	if err := doRequest(ctx, client, req, resp); err != nil {
		return nil, providerUtils.NewProviderAPIError(
			"github copilot: could not reach GitHub to exchange the Copilot token", err, 0, nil, nil)
	}

	if status := resp.StatusCode(); status != http.StatusOK {
		bErr := classifyExchangeError(status, resp.Body())
		providerUtils.ApplyRetryAfter(bErr, &resp.Header)
		return nil, bErr
	}

	var parsed copilotTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, providerUtils.NewProviderAPIError(
			"github copilot: could not parse GitHub's Copilot token response", err,
			resp.StatusCode(), nil, nil)
	}
	if parsed.Token == "" {
		return nil, blockingError(
			"github copilot: no active Copilot entitlement for this account (GitHub returned an empty token)",
			resp.StatusCode())
	}

	expiresAt := time.Unix(parsed.ExpiresAt, 0)
	if bErr := checkExpiry(expiresAt); bErr != nil {
		return nil, bErr
	}

	baseURL, err := validateCopilotAPIBaseURL(parsed.Endpoints.API)
	if err != nil {
		if logger != nil {
			logger.Warn("[github-copilot] ignoring the advertised Copilot host and falling back to %s: %v",
				defaultCopilotAPIBaseURL, err)
		}
		baseURL = defaultCopilotAPIBaseURL
	}

	refreshAt := expiresAt.Add(-tokenRefreshMargin)
	if parsed.RefreshIn > 0 {
		hinted := max(time.Duration(parsed.RefreshIn)*time.Second, minRefreshIn)
		// The hint must never outlive the token it refreshes.
		if candidate := time.Now().Add(hinted); candidate.Before(refreshAt) {
			refreshAt = candidate
		}
	}

	return &copilotToken{
		token:     parsed.Token,
		baseURL:   baseURL,
		expiresAt: expiresAt,
		refreshAt: refreshAt,
	}, nil
}

// checkExpiry rejects an expiry that is already past or implausibly far out. Caching an
// expired token would send every later request down the slow path, an unbounded request
// amplifier pointed at api.github.com.
func checkExpiry(expiresAt time.Time) *schemas.BifrostError {
	now := time.Now()
	if !expiresAt.After(now) {
		return configurationError("github copilot: GitHub returned an already-expired Copilot token")
	}
	if expiresAt.Sub(now) > maxTokenLifetime {
		return configurationError("github copilot: GitHub returned an implausible Copilot token expiry")
	}
	return nil
}

// validateCopilotAPIBaseURL vets the endpoints.api value before it becomes the base URL of
// every inference request. It is response-controlled and decides where prompts and a bearer
// token are sent, so it is treated as untrusted even over TLS.
//
// Returns a rebuilt URL rather than the input, so nothing in the input survives unexamined.
// DNS is deliberately not resolved here: ConfigureDialer already resolves and rejects private
// and link-local addresses at connect time and then dials the IP literal, which closes the
// rebinding window a validate-then-connect design would open.
func validateCopilotAPIBaseURL(rawURL string) (string, error) {
	if strings.TrimSpace(rawURL) == "" {
		return "", fmt.Errorf("endpoints.api is empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("endpoints.api is not a valid URL: %w", err)
	}
	if u.Opaque != "" {
		return "", fmt.Errorf("endpoints.api must not be an opaque URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("endpoints.api scheme %q is not https", u.Scheme)
	}
	// https://api.githubcopilot.com@evil.com parses with Host=evil.com. Rejecting the shape
	// outright is clearer than relying on the host check alone.
	if u.User != nil {
		return "", fmt.Errorf("endpoints.api must not contain userinfo")
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", fmt.Errorf("endpoints.api port %q is not allowed", port)
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", fmt.Errorf("endpoints.api has no host")
	}
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("endpoints.api host %q is an IP literal", host)
	}
	// The "." prefix is the entire security property here. A bare
	// strings.HasSuffix(host, domain) also accepts evilgithubcopilot.com.
	if host != copilotPublicAPIDomain && !strings.HasSuffix(host, "."+copilotPublicAPIDomain) {
		return "", fmt.Errorf("endpoints.api host %q is not %s or a subdomain of it", host, copilotPublicAPIDomain)
	}
	return "https://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// doRequest issues one exchange request, honouring the context deadline the cache set.
func doRequest(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) error {
	if deadline, ok := ctx.Deadline(); ok {
		return client.DoDeadline(req, resp, deadline)
	}
	return client.DoTimeout(req, resp, tokencache.DefaultExchangeTimeout)
}

// blockingError builds a configuration-fault error that must not drain onto a fallback
// provider.
func blockingError(message string, statusCode int) *schemas.BifrostError {
	bErr := providerUtils.NewProviderAPIError(message, nil, statusCode, nil, nil)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}

// githubMessage pulls GitHub's own error text out of a response body.
func githubMessage(body []byte) string {
	var parsed githubErrorBody
	if err := sonic.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	return "(no detail)"
}

// classifyExchangeError maps a failed token exchange to an error that names the fix.
func classifyExchangeError(status int, body []byte) *schemas.BifrostError {
	detail := githubMessage(body)
	switch status {
	case http.StatusUnauthorized:
		return blockingError("github copilot: GitHub rejected the token (401). It was revoked or has "+
			"expired; paste a new one or sign in again. GitHub said: "+detail, status)
	case http.StatusForbidden, http.StatusNotFound:
		return blockingError("github copilot: this GitHub token cannot get a Copilot token. The account "+
			"needs an active Copilot plan or an assigned seat, and the token must come from a GitHub App "+
			"Copilot accepts. GitHub said: "+detail, status)
	default:
		return providerUtils.NewProviderAPIError(
			fmt.Sprintf("github copilot: Copilot token exchange failed (%d): %s", status, detail),
			nil, status, nil, nil)
	}
}
