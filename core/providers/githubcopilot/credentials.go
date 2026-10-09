package githubcopilot

import (
	"net"
	"net/url"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// defaultCopilotAPIBaseURL is the public Copilot inference host. Paid plans are served
// from tier-specific subdomains (api.individual / api.business / api.enterprise), which
// arrive in the token exchange response rather than being knowable at config time.
const defaultCopilotAPIBaseURL = "https://api.githubcopilot.com"

// copilotCredentials is everything a single inference call needs: the bearer token and
// the host to send it to. Both are per-request rather than per-provider, because the
// Copilot API base URL is a property of the credential, not of the configuration.
type copilotCredentials struct {
	// Token is the Copilot API bearer token.
	Token string
	// BaseURL is the validated inference host, without a trailing slash.
	BaseURL string
}

// GitHub token prefixes. Copilot accepts the first three as the bearer on inference
// requests; a classic personal access token is not accepted.
const (
	githubOAuthTokenPrefix     = "gho_"
	githubAppUserTokenPrefix   = "ghu_"
	githubFineGrainedPATPrefix = "github_pat_"
	githubClassicPATPrefix     = "ghp_"
)

// isGithubUserToken reports whether token is a GitHub token that Copilot accepts directly.
func isGithubUserToken(token string) bool {
	return strings.HasPrefix(token, githubOAuthTokenPrefix) ||
		strings.HasPrefix(token, githubAppUserTokenPrefix) ||
		strings.HasPrefix(token, githubFineGrainedPATPrefix)
}

// isHTTPSOrLoopback reports whether rawURL is safe to send a GitHub token to: https to any
// host, or a loopback address, where the bytes never leave the machine. This is the same
// rule as providerUtils.StripCallerAuthForInsecureURL.
func isHTTPSOrLoopback(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveCredentials produces the credentials for one request.
//
// Three auth modes, checked in this order:
//
//  1. A GitHub token in Key.Value: an OAuth token, a GitHub App user token or a fine-grained
//     personal access token. It is used verbatim and does not expire on a 30 minute cycle.
//     Usage bills to the Copilot subscription of the user that owns the token. base_url is
//     optional, because these tokens work on the public host. A configured base_url must
//     use https, or be a loopback address, so the token is never sent in cleartext.
//  2. A pre-minted Copilot API token in Key.Value. This is GitHub's documented "direct API
//     token" method and the token is used verbatim. base_url is required alongside it: a
//     Copilot token does not carry its own host, paid plans are served from api.individual,
//     api.business or api.enterprise, and only the token exchange reveals which. Guessing
//     the public host would surface a Business token as a 401 that reads like a bad
//     credential, which is why GitHub pairs GITHUB_COPILOT_API_TOKEN with COPILOT_API_URL.
//     Copilot tokens live about 30 minutes, so this suits testing.
//  3. GitHub App credentials, from which Bifrost mints its own tokens server-to-server.
//     base_url is optional here because the exchange reports the host. This is the mode
//     intended for real deployments: usage bills to the organization and no individual
//     Copilot seat is involved.
//
// See https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/authenticate
func resolveCredentials(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	if token := strings.TrimSpace(key.Value.GetValue()); token != "" {
		if strings.HasPrefix(token, githubClassicPATPrefix) {
			return nil, configurationError(
				"github copilot: a classic personal access token (ghp_) is not accepted by Copilot. " +
					"Use a fine-grained personal access token with the Copilot Requests permission, " +
					"or a GitHub OAuth token.",
			)
		}
		baseURL := strings.TrimRight(strings.TrimSpace(configuredBaseURL), "/")
		if isGithubUserToken(token) {
			if baseURL == "" {
				baseURL = defaultCopilotAPIBaseURL
			} else if !isHTTPSOrLoopback(baseURL) {
				return nil, configurationError(
					"github copilot: network_config.base_url must use https when the key value is a " +
						"GitHub token, because the token is sent to that host. http is accepted only " +
						"for a loopback address.",
				)
			}
		}
		if baseURL == "" {
			return nil, configurationError(
				"github copilot: a Copilot API token needs network_config.base_url set to the host it " +
					"was issued for, because the token does not carry one. Paid plans use " +
					"api.individual, api.business or api.enterprise.githubcopilot.com.",
			)
		}
		return &copilotCredentials{
			Token:   token,
			BaseURL: baseURL,
		}, nil
	}

	if key.GithubCopilotKeyConfig == nil {
		return nil, configurationError(
			"github copilot: no credentials on this key. Set either value (a GitHub token or a " +
				"Copilot API token) or github_copilot_key_config (GitHub App credentials).",
		)
	}

	return mintCredentials(ctx, key.GithubCopilotKeyConfig, client, configuredBaseURL, logger)
}
