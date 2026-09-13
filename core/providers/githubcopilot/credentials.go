package githubcopilot

import (
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

// resolveCredentials produces the credentials for one request.
//
// Two auth modes, checked in this order:
//
//  1. A pre-minted Copilot API token in Key.Value. This is GitHub's documented "direct API
//     token" method and the token is used verbatim. base_url is required alongside it: a
//     Copilot token does not carry its own host, paid plans are served from api.individual,
//     api.business or api.enterprise, and only the token exchange reveals which. Guessing
//     the public host would surface a Business token as a 401 that reads like a bad
//     credential, which is why GitHub pairs GITHUB_COPILOT_API_TOKEN with COPILOT_API_URL.
//     Copilot tokens live about 30 minutes, so this suits testing.
//  2. GitHub App credentials, from which Bifrost mints its own tokens server-to-server.
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
		baseURL := strings.TrimRight(strings.TrimSpace(configuredBaseURL), "/")
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
			"github copilot: no credentials on this key. Set either value (a Copilot API token) " +
				"or github_copilot_key_config (GitHub App credentials).",
		)
	}

	return mintCredentials(ctx, key.GithubCopilotKeyConfig, client, configuredBaseURL, logger)
}
