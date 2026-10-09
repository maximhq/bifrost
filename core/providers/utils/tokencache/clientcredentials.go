package tokencache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// fallbackTokenLifetime is assumed when the token endpoint returns no expires_in. Short on
// purpose: an unknown lifetime re-minted every five minutes is cheap, while trusting a token
// past its real expiry turns into upstream 401s on the inference path.
const fallbackTokenLifetime = 5 * time.Minute

// ClientCredentialsConfig is what one OAuth 2.0 client_credentials exchange needs.
type ClientCredentialsConfig struct {
	TokenURL      string           // Token endpoint
	ClientID      string           // Client identifier
	ClientSecret  string           // Client secret
	Scopes        []string         // Scopes requested
	Audience      string           // Optional "audience" form parameter
	AuthStyle     oauth2.AuthStyle // Where the client credentials go (header by default)
	ExtraParams   url.Values       // Extra form parameters
	RefreshMargin time.Duration    // How long before expiry to re-mint (default DefaultRefreshMargin)
}

// ClientCredentialsMinter returns a Minter that performs the client_credentials grant on hc,
// so the exchange leaves through the provider's proxy_config and TLS settings. One call is
// one token request; caching and single-flight belong to the Cache that calls it. Redirects
// from the endpoint are held to the same https-or-loopback rule as token_url itself, since a
// 307/308 replays the credential to wherever it points.
func ClientCredentialsMinter(cfg ClientCredentialsConfig, hc *http.Client) Minter[string] {
	hc = guardRedirects(hc)
	authStyle := cfg.AuthStyle
	if authStyle == oauth2.AuthStyleAutoDetect {
		// Auto-detect retries a rejected header-style request with the secret in the body,
		// which doubles every failure against the endpoint and ships the secret a second
		// way the operator never chose. Header is the RFC 6749 default; body is opt-in.
		authStyle = oauth2.AuthStyleInHeader
	}
	conf := &clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
		AuthStyle:    authStyle,
	}
	if cfg.Audience != "" || len(cfg.ExtraParams) > 0 {
		params := url.Values{}
		for k, vs := range cfg.ExtraParams {
			params[k] = append([]string(nil), vs...)
		}
		if cfg.Audience != "" {
			params.Set("audience", cfg.Audience)
		}
		conf.EndpointParams = params
	}
	margin := cfg.RefreshMargin
	if margin <= 0 {
		margin = DefaultRefreshMargin
	}

	return func(ctx context.Context, _ *Entry[string]) (*Entry[string], *schemas.BifrostError) {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
		token, err := conf.Token(ctx)
		if err != nil {
			return nil, tokenRequestError(cfg.TokenURL, err)
		}
		return entryFor(token.AccessToken, token.Expiry, margin), nil
	}
}

// entryFor builds the cache entry for a minted token. A zero expiry means the endpoint sent
// no expires_in, so the fallback lifetime applies.
func entryFor(token string, expiry time.Time, margin time.Duration) *Entry[string] {
	if expiry.IsZero() {
		expiry = time.Now().Add(fallbackTokenLifetime)
	}
	refreshAt := expiry.Add(-margin)
	if !refreshAt.After(time.Now()) {
		// A token that is already inside its refresh margin is still usable for its remaining
		// life; trust it for half of what is left rather than minting on every request.
		refreshAt = time.Now().Add(time.Until(expiry) / 2)
	}
	return &Entry[string]{Value: token, ExpiresAt: expiry, RefreshAt: refreshAt}
}

// tokenRequestError maps an x/oauth2 error onto a BifrostError. A RetrieveError carries the
// endpoint's status, which is what decides whether the failure is cached as permanent or
// transient; its body is never echoed, since token endpoints can reflect the request back.
func tokenRequestError(tokenURL string, err error) *schemas.BifrostError {
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) && rerr.Response != nil {
		detail := strings.TrimSpace(strings.Join([]string{rerr.ErrorCode, rerr.ErrorDescription}, " "))
		bErr := tokenEndpointError(tokenURL, rerr.Response.StatusCode, detail)
		providerUtils.ApplyRetryAfterHTTP(bErr, rerr.Response.Header)
		return bErr
	}
	if strings.Contains(err.Error(), "missing access_token") {
		// x/oauth2 reports a 2xx body without an access_token as a plain error.
		return emptyTokenError(tokenURL, http.StatusOK)
	}
	return transportError(tokenURL, err)
}

// tokenEndpointError builds the error for a non-2xx answer from the token endpoint. A
// credential or configuration fault (any 4xx other than 429) blocks fallbacks, since a
// different provider will not repair this key; a rate limit or a server error is a
// temporary outage of the endpoint and leaves fallbacks open.
func tokenEndpointError(tokenURL string, statusCode int, detail string) *schemas.BifrostError {
	if detail == "" {
		detail = "(no detail)"
	}
	bErr := providerUtils.NewProviderAPIError(fmt.Sprintf("oauth token request to %s failed (%d): %s",
		redactURL(tokenURL), statusCode, detail), nil, statusCode, nil, nil)
	if IsPermanentError(bErr) {
		bErr.AllowFallbacks = schemas.Ptr(false)
	}
	return bErr
}

// emptyTokenError is a 2xx answer with no access_token: the endpoint is misconfigured for
// this client, which no fallback provider repairs. The error carries 502, never the
// endpoint's own 2xx, because the status becomes the HTTP status the client receives and a
// failed request must not read as a success; the endpoint's status stays in the message.
func emptyTokenError(tokenURL string, statusCode int) *schemas.BifrostError {
	bErr := providerUtils.NewProviderAPIError(
		fmt.Sprintf("oauth token endpoint %s returned no access_token (%d)", redactURL(tokenURL), statusCode),
		nil, http.StatusBadGateway, nil, nil)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}

// transportError is a token request that got no HTTP status: the endpoint could not be
// reached. Status 0 classifies as transient and leaves fallbacks open. The wrapped error is
// rebuilt without the request URL, because a url.Error prints the full URL including its
// query, ErrorField.MarshalJSON serialises that string to callers, and a token_url may
// carry a secret as a query parameter.
func transportError(tokenURL string, err error) *schemas.BifrostError {
	return providerUtils.NewProviderAPIError(
		fmt.Sprintf("oauth token request to %s could not be completed", redactURL(tokenURL)),
		sanitizeTransportError(tokenURL, err), 0, nil, nil)
}

// sanitizeTransportError strips the request URL from a network error chain, keeping the
// underlying cause for diagnostics.
func sanitizeTransportError(tokenURL string, err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = fmt.Errorf("%s %s: %w", ue.Op, redactURL(tokenURL), ue.Err)
	}
	// Belt and braces: whatever wrapped the error, the raw URL must not survive in its text.
	if msg := err.Error(); strings.Contains(msg, tokenURL) {
		err = errors.New(strings.ReplaceAll(msg, tokenURL, redactURL(tokenURL)))
	}
	return err
}

// redactURL strips userinfo and query from a token URL before it appears in an error message.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "the token endpoint"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
