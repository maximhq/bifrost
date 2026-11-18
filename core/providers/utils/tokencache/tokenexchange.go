package tokencache

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	// TokenExchangeGrantType is the RFC 8693 grant_type value.
	TokenExchangeGrantType = "urn:ietf:params:oauth:grant-type:token-exchange"
	// maxSubjectTokenBytes caps the subject token read from disk; a projected service account
	// token is about a kilobyte.
	maxSubjectTokenBytes = 16 * 1024
)

// TokenExchangeConfig is what one OAuth 2.0 token exchange (RFC 8693) needs.
type TokenExchangeConfig struct {
	TokenURL         string                       // Token endpoint; must be on the operator allowlist
	SubjectTokenFile string                       // Absolute path of the subject token, re-read on every exchange
	SubjectTokenType string                       // RFC 8693 token type URI (default jwt)
	Encoding         schemas.OAuthRequestEncoding // form (default) or json
	Scopes           []string                     // Optional scope parameter, space-joined
	Audience         string                       // Optional audience parameter
	ExtraParams      url.Values                   // Provider parameters, e.g. OpenAI's identity_provider_id and service_account_id
	RefreshMargin    time.Duration                // How long before expiry to re-mint (default DefaultRefreshMargin)
}

// TokenExchangeMinter returns a Minter that reads the subject token from its file and exchanges
// it on hc. The file is read on every mint, so a token the platform rotates on disk is always
// current, and only from an allowed credential directory (AllowedCredentialPath). The endpoint
// must be on the operator allowlist (schemas.TokenExchangeURLAllowed), checked here as well as
// at save time because a token_url reference resolves only now. Redirects are refused, so the
// subject token is never replayed to a host the allowlist never saw.
func TokenExchangeMinter(cfg TokenExchangeConfig, hc *http.Client) Minter[string] {
	margin := cfg.RefreshMargin
	if margin <= 0 {
		margin = DefaultRefreshMargin
	}
	tokenType := cfg.SubjectTokenType
	if tokenType == "" {
		tokenType = schemas.OAuthSubjectTokenTypeJWT
	}
	hc = noRedirects(exchangeClient(hc))
	return func(ctx context.Context, _ *Entry[string]) (*Entry[string], *schemas.BifrostError) {
		// Checked here too, not only in MinterForConfig, so a Go SDK caller that builds the
		// minter directly gets the same guard before the file is read.
		if !httpsOrLoopbackURL(cfg.TokenURL) || !schemas.TokenExchangeURLAllowed(cfg.TokenURL) {
			return nil, configurationError("oauth token exchange endpoint " + redactURL(cfg.TokenURL) + " is not allowed; list it in " +
				schemas.TokenExchangeURLsEnv + " (default " + strings.Join(schemas.DefaultTokenExchangeURLs, ",") + ")")
		}
		subject, err := ReadCredentialFile(cfg.SubjectTokenFile, maxSubjectTokenBytes)
		if err != nil {
			return nil, subjectTokenError(err)
		}
		params := map[string]string{
			"grant_type":         TokenExchangeGrantType,
			"subject_token":      subject,
			"subject_token_type": tokenType,
		}
		if len(cfg.Scopes) > 0 {
			params["scope"] = strings.Join(cfg.Scopes, " ")
		}
		if cfg.Audience != "" {
			params["audience"] = cfg.Audience
		}
		// Extra params never override the grant's own fields.
		submitted := []string{subject}
		for k := range cfg.ExtraParams {
			if _, reserved := params[k]; reserved {
				continue
			}
			v := cfg.ExtraParams.Get(k)
			params[k] = v
			submitted = append(submitted, v)
		}

		var (
			body        []byte
			contentType string
		)
		if cfg.Encoding == schemas.OAuthRequestEncodingJSON {
			body, err = sonic.Marshal(params)
			if err != nil {
				return nil, configurationError("oauth token exchange request could not be built: " + err.Error())
			}
			contentType = "application/json"
		} else {
			form := url.Values{}
			for k, v := range params {
				form.Set(k, v)
			}
			body, contentType = []byte(form.Encode()), "application/x-www-form-urlencoded"
		}
		resp, bErr := postToken(ctx, hc, cfg.TokenURL, body, contentType, submitted)
		if bErr != nil {
			return nil, bErr
		}
		// RFC 8693 lets the endpoint issue something other than a bearer token; Bifrost can only
		// send a bearer.
		if resp.tokenType != "" && !strings.EqualFold(resp.tokenType, "Bearer") {
			// The value is not quoted back: an endpoint could reflect a credential in it.
			bErr := providerUtils.NewProviderAPIError("oauth token exchange at "+redactURL(cfg.TokenURL)+" returned a token_type other than Bearer", nil, http.StatusBadGateway, nil, nil)
			bErr.AllowFallbacks = schemas.Ptr(false)
			return nil, bErr
		}
		return entryFor(resp.accessToken, resp.expiry, margin), nil
	}
}

// noRedirects stops hc from following any redirect: the token endpoint was checked against an
// exact allowlist, and a 307/308 would replay the subject token somewhere else.
func noRedirects(hc *http.Client) *http.Client {
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// subjectTokenError maps a failed subject token read onto a configuration fault: nothing was
// sent, and no fallback provider repairs a missing or misplaced token file.
func subjectTokenError(err error) *schemas.BifrostError {
	msg := "oauth_key_config.subject_token_file could not be read: " + err.Error()
	if errors.Is(err, ErrCredentialPathNotAllowed) {
		msg = "oauth_key_config.subject_token_file " + err.Error()
	}
	bErr := providerUtils.NewConfigurationError(msg)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}
