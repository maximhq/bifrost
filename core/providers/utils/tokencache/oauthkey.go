package tokencache

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/oauth2"
)

// oauthKeyCacheVersion namespaces cache keys derived from an OAuthKeyConfig. Bump it when the
// set of hashed fields changes.
const oauthKeyCacheVersion = "oauthkey-v1"

// ResolveBearer is the one entry point for bearer auth on a key that may be static or minted.
// value is Key.Value; when set it is used verbatim. When it is empty and cfg is nil the result
// is an empty, non-nil map, so a keyless custom provider still works. Otherwise the token is
// taken from cache, minting through hc when needed.
//
// Request-path cost for a static key: one comparison and the one-entry header map that
// BearerAuthHeader already allocates today.
func ResolveBearer(ctx context.Context, cache *Cache[string], value string, cfg *schemas.OAuthKeyConfig, hc *http.Client) (map[string]string, *schemas.BifrostError) {
	if value != "" {
		return map[string]string{"Authorization": "Bearer " + value}, nil
	}
	if cfg == nil {
		return map[string]string{}, nil
	}
	return ResolveBearerFrom(ctx, cache, *cfg, hc)
}

// ResolveBearerFrom is ResolveBearer for a config that is known to exist, taken by value so a
// provider that derives it per request (Databricks) keeps it on the stack.
func ResolveBearerFrom(ctx context.Context, cache *Cache[string], cfg schemas.OAuthKeyConfig, hc *http.Client) (map[string]string, *schemas.BifrostError) {
	// The minter is built only on the cold path: a warm request pays for the cache key and
	// the lookup, not for a clientcredentials.Config it will never use.
	cacheKey, bErr := validateConfig(&cfg)
	if bErr != nil {
		return nil, bErr
	}
	entry, bErr := cache.Get(ctx, cacheKey, func(ctx context.Context, prev *Entry[string]) (*Entry[string], *schemas.BifrostError) {
		_, mint, bErr := MinterForConfig(&cfg, hc)
		if bErr != nil {
			return nil, bErr
		}
		return mint(ctx, prev)
	})
	if bErr != nil {
		return nil, bErr
	}
	return map[string]string{"Authorization": "Bearer " + entry.Value}, nil
}

// MinterForConfig validates cfg for its grant and returns the cache key that identifies the
// credential together with the minter that exchanges it. Validation runs before anything
// touches the network, and a configuration fault blocks fallbacks: a different provider will
// not repair this key.
func MinterForConfig(cfg *schemas.OAuthKeyConfig, hc *http.Client) (string, Minter[string], *schemas.BifrostError) {
	cacheKey, bErr := validateConfig(cfg)
	if bErr != nil {
		return "", nil, bErr
	}
	tokenURL := strings.TrimSpace(cfg.TokenURL.GetValue())

	switch cfg.GrantType {
	case schemas.OAuthGrantClientCredentials:
		clientID, clientSecret := secret(cfg.ClientID), secret(cfg.ClientSecret)
		authStyle := oauth2.AuthStyleInHeader
		switch cfg.AuthStyle {
		case "", schemas.OAuthAuthStyleHeader:
		case schemas.OAuthAuthStyleBody:
			authStyle = oauth2.AuthStyleInParams
		default:
			return "", nil, configurationError("oauth_key_config.auth_style must be header or body")
		}
		mint := ClientCredentialsMinter(ClientCredentialsConfig{
			TokenURL:     tokenURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       cfg.Scopes,
			Audience:     cfg.Audience,
			AuthStyle:    authStyle,
			ExtraParams:  extraParams(cfg.ExtraParams),
		}, hc)
		return cacheKey, mint, nil
	case schemas.OAuthGrantJWTBearer:
		if alg := cfg.SigningAlgorithm; alg != "" && !slices.Contains(schemas.OAuthSigningAlgorithms, alg) {
			return "", nil, configurationError("oauth_key_config.signing_algorithm must be one of " + strings.Join(schemas.OAuthSigningAlgorithms, ", "))
		}
		mint := JWTBearerMinter(JWTBearerConfig{
			TokenURL:      tokenURL,
			PrivateKeyPEM: secret(cfg.PrivateKey),
			Issuer:        strings.TrimSpace(cfg.Issuer),
			Subject:       strings.TrimSpace(cfg.Subject),
			Audience:      strings.TrimSpace(cfg.Audience),
			KeyID:         cfg.KeyID,
			Scopes:        cfg.Scopes,
			Algorithm:     cfg.SigningAlgorithm,
			Lifetime:      time.Duration(cfg.AssertionLifetimeSeconds) * time.Second,
			ExtraParams:   extraParams(cfg.ExtraParams),
		}, hc)
		return cacheKey, mint, nil
	default:
		return "", nil, configurationError("oauth_key_config.grant_type must be client_credentials or jwt_bearer")
	}
}

// validateConfig runs the allocation-free shape checks every request pays for and returns the
// credential's cache key. Grant-specific checks that need more than a nil test live in
// MinterForConfig, on the cold path.
func validateConfig(cfg *schemas.OAuthKeyConfig) (string, *schemas.BifrostError) {
	if cfg == nil {
		return "", configurationError("oauth_key_config is not set")
	}
	tokenURL := strings.TrimSpace(cfg.TokenURL.GetValue())
	if tokenURL == "" {
		return "", configurationError("oauth_key_config.token_url is required")
	}
	if !httpsOrLoopbackURL(tokenURL) {
		return "", configurationError("oauth_key_config.token_url must be https (http is allowed for loopback hosts only)")
	}
	switch cfg.GrantType {
	case schemas.OAuthGrantClientCredentials:
		if secret(cfg.ClientID) == "" || secret(cfg.ClientSecret) == "" {
			return "", configurationError("oauth_key_config.client_id and oauth_key_config.client_secret are required for the client_credentials grant")
		}
	case schemas.OAuthGrantJWTBearer:
		if secret(cfg.PrivateKey) == "" || strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.Audience) == "" {
			return "", configurationError("oauth_key_config.private_key, oauth_key_config.issuer and oauth_key_config.audience are required for the jwt_bearer grant")
		}
	case "":
		return "", configurationError("oauth_key_config.grant_type is required")
	default:
		return "", configurationError("oauth_key_config.grant_type must be client_credentials or jwt_bearer")
	}
	return oauthKeyCacheKey(cfg, tokenURL), nil
}

// httpsOrLoopbackURL is the request-path shape check for token_url, without a url.Parse
// allocation: an https URL with a host, or an http URL whose host is loopback. The client
// secret travels on this request (Basic auth or form body), so plain http to any other host
// would send it in cleartext. The transport layer validates the URL in full when the key is
// saved; this check also covers Go SDK callers that never pass through the transport.
func httpsOrLoopbackURL(raw string) bool {
	if rest, ok := strings.CutPrefix(raw, "https://"); ok {
		return hostOf(rest) != ""
	}
	rest, ok := strings.CutPrefix(raw, "http://")
	if !ok {
		return false
	}
	host := hostOf(rest)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	// Only a literal loopback address qualifies. A prefix test would let a public name such as
	// 127.attacker.example through, and netip.ParseAddr rejects anything that is not an IP.
	addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	return err == nil && addr.IsLoopback()
}

// hostOf returns the host (without port) at the start of a URL's authority section, or ""
// when there is none.
func hostOf(rest string) string {
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		rest = rest[at+1:]
	}
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end <= 0 {
			return ""
		}
		// Only a port may follow the bracket; "[::1]evil" is not a host.
		if tail := rest[end+1:]; tail != "" && tail[0] != ':' {
			return ""
		}
		return rest[:end+1]
	}
	if colon := strings.LastIndexByte(rest, ':'); colon >= 0 {
		rest = rest[:colon]
	}
	return rest
}

// oauthKeyCacheKey hashes every field that changes which token comes back, so two keys that
// differ in any of them never share a cached token.
func oauthKeyCacheKey(cfg *schemas.OAuthKeyConfig, tokenURL string) string {
	return CacheKey(oauthKeyCacheVersion,
		string(cfg.GrantType),
		tokenURL,
		secret(cfg.ClientID),
		secret(cfg.ClientSecret),
		secret(cfg.PrivateKey),
		cfg.Issuer,
		cfg.Subject,
		cfg.Audience,
		cfg.KeyID,
		cfg.SigningAlgorithm,
		string(cfg.AuthStyle),
		strings.Join(cfg.Scopes, " "),
		encodedExtraParams(cfg.ExtraParams),
	)
}

// encodedExtraParams renders the extra params sorted by key, or "" when there are none.
func encodedExtraParams(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	return extraParams(m).Encode()
}

// extraParams converts the config map to form values with a stable order.
func extraParams(m map[string]string) url.Values {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	v := url.Values{}
	for _, k := range keys {
		v.Set(k, m[k])
	}
	return v
}

func secret(s *schemas.SecretVar) string {
	if s == nil {
		return ""
	}
	return s.GetValue()
}

// configurationError builds a configuration-fault error that must not drain onto a fallback
// provider.
func configurationError(message string) *schemas.BifrostError {
	bErr := providerUtils.NewConfigurationError(message)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}
