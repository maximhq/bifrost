package tokencache

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	ExpiresAt        int64  `json:"expires_at"` // absolute Unix expiry, sent by some endpoints (OpenAI) next to expires_in
	Error            any    `json:"error"`      // RFC 6749 string, or an object such as OpenAI's {"code","message"}
	ErrorDescription string `json:"error_description"`
}

// errorDetail renders the error code and description of a failed token response: the RFC 6749
// error and error_description pair, or the code (or type) and message of a nested error object.
func (r *tokenResponse) errorDetail() string {
	parts := []string{}
	switch e := r.Error.(type) {
	case string:
		parts = append(parts, e)
	case map[string]any:
		for _, k := range []string{"code", "type", "message"} {
			if v, ok := e[k].(string); ok && v != "" {
				if k == "type" && len(parts) > 0 {
					continue // a code already names the failure
				}
				parts = append(parts, v)
			}
		}
	}
	parts = append(parts, r.ErrorDescription)
	return strings.TrimSpace(strings.Join(parts, " "))
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

// SignAssertion signs an RFC 7523 assertion for cfg now. With Issuer and Subject
// set to a client ID and Audience to a token endpoint it is also an RFC 7523
// §2.2 client assertion (private_key_jwt client authentication).
func SignAssertion(cfg JWTBearerConfig) (string, error) {
	return signAssertion(cfg, time.Now())
}

// ValidatePrivateKey reports whether pemData parses as the key family alg (RS256
// when empty) needs, the check SignAssertion runs, without signing anything.
func ValidatePrivateKey(pemData, alg string) error {
	if alg == "" {
		alg = "RS256"
	}
	if jwt.GetSigningMethod(alg) == nil || !slices.Contains(schemas.OAuthSigningAlgorithms, alg) {
		return fmt.Errorf("signing_algorithm %q is not supported", alg)
	}
	_, err := parsePrivateKey(NormalizePEM(pemData), alg)
	return err
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
		// The signer refuses a curve that doesn't match the algorithm; catch it here so
		// ValidatePrivateKey does too.
		if m, ok := jwt.GetSigningMethod(alg).(*jwt.SigningMethodECDSA); ok && key.Curve.Params().BitSize != m.CurveBits {
			return nil, fmt.Errorf("private_key is a %s key, which %s cannot sign with", key.Curve.Params().Name, alg)
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

// postTokenRequest posts a form to the token endpoint and reads the access token. Every
// submitted value except grant_type and scope is scrubbed from what an error reports back.
func postTokenRequest(ctx context.Context, hc *http.Client, tokenURL string, form url.Values) (string, time.Time, *schemas.BifrostError) {
	// The signed assertion and every extra parameter are what this request sent; none of them
	// may come back to the caller through the error code or description. grant_type and scope are not
	// credentials, and an error naming the scope it refused is worth keeping readable.
	var submitted []string
	for k, vs := range form {
		if k != "grant_type" && k != "scope" {
			submitted = append(submitted, vs...)
		}
	}
	resp, bErr := postToken(ctx, hc, tokenURL, []byte(form.Encode()), "application/x-www-form-urlencoded", submitted)
	if bErr != nil {
		return "", time.Time{}, bErr
	}
	return resp.accessToken, resp.expiry, nil
}

// postedToken is a successful token endpoint answer.
type postedToken struct {
	accessToken string
	tokenType   string
	expiry      time.Time // zero when the endpoint sent no lifetime
}

// postToken posts an encoded body to the token endpoint and reads the access token. Errors
// carry the endpoint's status so the cache classifies them, and never echo the body beyond
// the error code and description, with every submitted value scrubbed from those.
func postToken(ctx context.Context, hc *http.Client, tokenURL string, body []byte, contentType string, submitted []string) (*postedToken, *schemas.BifrostError) {
	// The lifetime counts from issuance, which is no earlier than the moment the request leaves.
	sent := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, configurationError("oauth token request could not be built: " + err.Error())
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		// A transport error can quote the response (Go reports a malformed redirect Location
		// in full), so it is scrubbed of submitted values like an error body.
		if msg := err.Error(); scrubSubmitted(msg, submitted) != msg {
			err = errors.New(scrubSubmitted(msg, submitted))
		}
		return nil, transportError(tokenURL, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return nil, providerUtils.NewProviderAPIError(
			fmt.Sprintf("oauth token response from %s could not be read", redactURL(tokenURL)),
			sanitizeTransportError(tokenURL, err), 0, nil, nil)
	}

	var parsed tokenResponse
	_ = sonic.Unmarshal(respBody, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		bErr := tokenEndpointError(tokenURL, resp.StatusCode, scrubSubmitted(parsed.errorDetail(), submitted))
		providerUtils.ApplyRetryAfterHTTP(bErr, resp.Header)
		return nil, bErr
	}
	if parsed.AccessToken == "" {
		return nil, emptyTokenError(tokenURL, resp.StatusCode)
	}
	var expiry time.Time
	if parsed.ExpiresIn > 0 {
		expiry = sent.Add(time.Duration(parsed.ExpiresIn) * time.Second)
	}
	// An absolute expiry (OpenAI's expires_at) wins when it is earlier.
	if parsed.ExpiresAt > 0 {
		if at := time.Unix(parsed.ExpiresAt, 0); expiry.IsZero() || at.Before(expiry) {
			expiry = at
		}
	}
	if !expiry.IsZero() && !expiry.After(time.Now()) {
		bErr := providerUtils.NewProviderAPIError(fmt.Sprintf("oauth token endpoint %s returned a token that has already expired", redactURL(tokenURL)),
			nil, http.StatusBadGateway, nil, nil)
		bErr.AllowFallbacks = schemas.Ptr(false)
		return nil, bErr
	}
	return &postedToken{accessToken: parsed.AccessToken, tokenType: parsed.TokenType, expiry: expiry}, nil
}
