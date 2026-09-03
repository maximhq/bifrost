package githubcopilot

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
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
// Key.Value holds a Copilot API token, GitHub's documented "direct API token" method. The
// token is used verbatim.
//
// base_url is required alongside it. A Copilot token does not carry its own host: paid
// plans are served from api.individual, api.business or api.enterprise, and only the token
// exchange reveals which. Defaulting to the public host would send a Business token
// somewhere it is not valid and surface as a 401 that reads like a bad credential. GitHub
// pairs GITHUB_COPILOT_API_TOKEN with COPILOT_API_URL for the same reason.
//
// See https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/authenticate
func resolveCredentials(
	key schemas.Key,
	configuredBaseURL string,
) (*copilotCredentials, *schemas.BifrostError) {
	token := strings.TrimSpace(key.Value.GetValue())
	if token == "" {
		return nil, configurationError(
			"github copilot: no credentials on this key. Set value to a GitHub Copilot API token.",
		)
	}

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
