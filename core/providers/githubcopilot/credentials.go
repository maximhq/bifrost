package githubcopilot

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// defaultCopilotAPIBaseURL is the public Copilot inference host. Paid plans are served from
// tier-specific subdomains (api.individual / api.business / api.enterprise), which arrive in
// the token exchange response rather than being knowable at config time.
const defaultCopilotAPIBaseURL = "https://api.githubcopilot.com"

// copilotCredentials is what one inference call needs: the bearer token and the host to send
// it to. The host is a property of the credential, not of the configuration.
type copilotCredentials struct {
	Token   string
	BaseURL string // validated, without a trailing slash
	SDK     bool   // a PAT, sent under the Copilot SDK's integration ID instead of VS Code's
}

// integrationID is the copilot-integration-id the credential must be sent under.
func (c *copilotCredentials) integrationID() string {
	if c.SDK {
		return sdkIntegrationID
	}
	return copilotIntegrationID
}

// resolveCredentials produces the credentials for one request from Key.Value:
//
//   - A fine-grained PAT (github_pat_) is sent to Copilot as is, under the Copilot SDK's
//     integration ID, at the plan host looked up for the account.
//   - A GitHub OAuth token (ghu_ or gho_), which is what "Sign in with GitHub" puts in the key,
//     is exchanged for a Copilot token, refreshed before expiry.
//   - Anything else is a pre-minted Copilot API token, used verbatim. base_url is required
//     with it because the token does not carry its host, and guessing the public host would
//     turn a Business token's 401 into what reads like a bad credential.
//
// See https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/authenticate
func resolveCredentials(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	token := strings.TrimSpace(key.Value.GetValue())
	switch {
	case token == "":
		return nil, configurationError(
			"github copilot: no credentials on this key. Paste a fine-grained personal access token, " +
				"or sign in with GitHub from the key in the Bifrost UI.")
	case isPersonalAccessToken(token):
		return resolvePATCredentials(ctx, token, client, configuredBaseURL, logger)
	case isGithubUserToken(token):
		return mintFromUserToken(ctx, token, client, configuredBaseURL, logger)
	case strings.HasPrefix(token, "ghp_"), strings.HasPrefix(token, "ghs_"):
		return nil, configurationError(
			"github copilot: classic personal access tokens (ghp_) and GitHub App installation tokens " +
				"(ghs_) are not supported by Copilot. Paste a fine-grained personal access token, " +
				"or sign in with GitHub from the key in the Bifrost UI.")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(configuredBaseURL), "/")
	if baseURL == "" {
		return nil, configurationError(
			"github copilot: a Copilot API token needs network_config.base_url set to the host it " +
				"was issued for, because the token does not carry one. Paid plans use " +
				"api.individual, api.business or api.enterprise.githubcopilot.com.")
	}
	if err := requireSecureConfiguredURL(baseURL); err != nil {
		return nil, configurationError("github copilot: network_config.base_url " + err.Error())
	}
	return &copilotCredentials{Token: token, BaseURL: baseURL}, nil
}

// requireSecureConfiguredURL refuses a configured base URL that would send the credential in
// cleartext: https is required, and plain http is allowed only for a loopback host.
func requireSecureConfiguredURL(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not an absolute URL", baseURL)
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if strings.EqualFold(u.Scheme, "http") && (strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return fmt.Errorf("must use https (http is allowed only for localhost), got %q", u.Scheme)
}
