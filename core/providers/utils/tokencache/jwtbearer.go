package tokencache

import (
	"context"
	"crypto"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/golang-jwt/jwt/v5"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	// jwtBearerGrantType is the RFC 7523 grant_type value.
	jwtBearerGrantType = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	// DefaultAssertionLifetime is the assertion's exp - iat when the config sets none.
	DefaultAssertionLifetime = 5 * time.Minute
	// assertionIssuedAtSkew backdates iat so a slightly fast host clock does not produce an
	// assertion the server sees as issued in the future.
	assertionIssuedAtSkew = 30 * time.Second
	// maxTokenResponseBytes caps a token endpoint response; a real one is a few hundred bytes.
	maxTokenResponseBytes = 64 * 1024
)

// JWTBearerConfig is what one RFC 7523 JWT bearer exchange needs: Bifrost signs an assertion
// with PrivateKeyPEM and posts it to TokenURL for an access token.
type JWTBearerConfig struct {
	TokenURL      string        // Token endpoint
	PrivateKeyPEM string        // RSA (PKCS#1 or PKCS#8) or EC private key
	Issuer        string        // iss claim
	Subject       string        // sub claim (defaults to Issuer)
	Audience      string        // aud claim
	KeyID         string        // kid header, when the server selects keys by id
	Scopes        []string      // Scopes requested, sent space-joined
	Algorithm     string        // RS256 (default), RS384, RS512, PS256, PS384, PS512, ES256, ES384, ES512
	Lifetime      time.Duration // Assertion exp - iat (default DefaultAssertionLifetime)
	ExtraParams   url.Values    // Extra form parameters
	RefreshMargin time.Duration // How long before expiry to re-mint (default DefaultRefreshMargin)
}

// tokenResponse is the RFC 6749 §5.1 success shape, and the fields of §5.2 an error carries.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// JWTBearerMinter returns a Minter that signs a fresh assertion and exchanges it on hc.
// The private key is parsed on every mint rather than cached: a mint happens once per token
// lifetime, and holding a parsed key for the life of the process is a larger secret surface
// than re-parsing a few hundred bytes of PEM.
func JWTBearerMinter(cfg JWTBearerConfig, hc *http.Client) Minter[string] {
	margin := cfg.RefreshMargin
	if margin <= 0 {
		margin = DefaultRefreshMargin
	}
	// Bounded first, then guarded: a nil client gets the fallback timeout and the redirect policy.
	hc = guardRedirects(exchangeClient(hc))
	return func(ctx context.Context, _ *Entry[string]) (*Entry[string], *schemas.BifrostError) {
		assertion, err := signAssertion(cfg, time.Now())
		if err != nil {
			bErr := configurationError("oauth jwt_bearer: " + err.Error())
			return nil, bErr
		}
		form := url.Values{}
		for k, vs := range cfg.ExtraParams {
			form[k] = append([]string(nil), vs...)
		}
		form.Set("grant_type", jwtBearerGrantType)
		form.Set("assertion", assertion)
		if len(cfg.Scopes) > 0 {
			form.Set("scope", strings.Join(cfg.Scopes, " "))
		}
		token, expiry, bErr := postTokenRequest(ctx, hc, cfg.TokenURL, form)
		if bErr != nil {
			return nil, bErr
		}
		return entryFor(token, expiry, margin), nil
	}
}

// signAssertion builds and signs the RFC 7523 §3 assertion: iss, sub, aud, exp, plus iat,
// nbf and a random jti so a server that enforces single use can.
func signAssertion(cfg JWTBearerConfig, now time.Time) (string, error) {
	alg := cfg.Algorithm
	if alg == "" {
		alg = "RS256"
	}
	// Asymmetric only: an HMAC algorithm would turn the private key into a shared secret.
	method := jwt.GetSigningMethod(alg)
	if method == nil || !slices.Contains(schemas.OAuthSigningAlgorithms, alg) {
		return "", fmt.Errorf("signing_algorithm %q is not supported", alg)
	}
	key, err := parsePrivateKey(NormalizePEM(cfg.PrivateKeyPEM), alg)
	if err != nil {
		return "", err
	}
	lifetime := cfg.Lifetime
	if lifetime <= 0 {
		lifetime = DefaultAssertionLifetime
	}
	subject := cfg.Subject
	if subject == "" {
		subject = cfg.Issuer
	}
	var jti [16]byte
	if _, err := io.ReadFull(rand.Reader, jti[:]); err != nil {
		return "", fmt.Errorf("could not generate jti: %w", err)
	}
	issuedAt := now.Add(-assertionIssuedAtSkew)
	// MapClaims rather than RegisteredClaims so aud is a plain string: RFC 7519 allows either,
	// but Entra ID and others reject the one-element array RegisteredClaims would emit.
	token := jwt.NewWithClaims(method, jwt.MapClaims{
		"iss": cfg.Issuer,
		"sub": subject,
		"aud": cfg.Audience,
		"iat": issuedAt.Unix(),
		"nbf": issuedAt.Unix(),
		"exp": now.Add(lifetime).Unix(),
		"jti": hex.EncodeToString(jti[:]),
	})
	if cfg.KeyID != "" {
		token.Header["kid"] = cfg.KeyID
	}
	return token.SignedString(key)
}

// parsePrivateKey parses the PEM as the key family the algorithm needs.
func parsePrivateKey(pemData, alg string) (crypto.PrivateKey, error) {
	switch {
	case strings.HasPrefix(alg, "ES"):
		key, err := jwt.ParseECPrivateKeyFromPEM([]byte(pemData))
		if err != nil {
			return nil, fmt.Errorf("private_key could not be parsed as an EC PEM for %s: %w", alg, err)
		}
		return key, nil
	default:
		key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(pemData))
		if err != nil {
			return nil, fmt.Errorf("private_key could not be parsed as an RSA PEM (PKCS#1 or PKCS#8) for %s: %w", alg, err)
		}
		return key, nil
	}
}

// postTokenRequest posts a form to the token endpoint and reads the access token. Errors
// carry the endpoint's status so the cache classifies them, and never echo the body beyond
// the standard error and error_description fields.
func postTokenRequest(ctx context.Context, hc *http.Client, tokenURL string, form url.Values) (string, time.Time, *schemas.BifrostError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, configurationError("oauth token request could not be built: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return "", time.Time{}, transportError(tokenURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return "", time.Time{}, providerUtils.NewProviderAPIError(
			fmt.Sprintf("oauth token response from %s could not be read", redactURL(tokenURL)),
			sanitizeTransportError(tokenURL, err), 0, nil, nil)
	}

	var parsed tokenResponse
	_ = sonic.Unmarshal(body, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		bErr := tokenEndpointError(tokenURL, resp.StatusCode, strings.TrimSpace(parsed.Error+" "+parsed.ErrorDescription))
		providerUtils.ApplyRetryAfterHTTP(bErr, resp.Header)
		return "", time.Time{}, bErr
	}
	if parsed.AccessToken == "" {
		return "", time.Time{}, emptyTokenError(tokenURL, resp.StatusCode)
	}
	var expiry time.Time
	if parsed.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	}
	return parsed.AccessToken, expiry, nil
}
