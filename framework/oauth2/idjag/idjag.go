// Package idjag verifies Identity Assertion JWT Authorization Grants (ID-JAG,
// draft-ietf-oauth-identity-assertion-authz-grant) presented to Bifrost's
// authorization server with the jwt-bearer grant (RFC 7523). An ID-JAG is signed
// by the enterprise IdP (Okta Cross App Access); Bifrost trusts exactly one
// configured issuer and checks the assertion is addressed to its own issuer.
package idjag

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Wire constants for the jwt-bearer grant and the ID-JAG profile.
const (
	GrantTypeJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	GrantProfileIDJAG  = "urn:ietf:params:oauth:grant-profile:id-jag"
	TokenType          = "oauth-id-jag+jwt"
)

// Verification failures. Callers log the specific error and answer the client
// with a generic invalid_grant, so these never reach the wire verbatim.
var (
	ErrMalformed = errors.New("malformed assertion")
	ErrType      = errors.New("assertion is not an ID-JAG")
	ErrAlgorithm = errors.New("assertion signing algorithm not allowed")
	ErrIssuer    = errors.New("assertion issuer is not trusted")
	ErrKey       = errors.New("assertion signing key not found")
	ErrSignature = errors.New("assertion signature invalid")
	ErrExpired   = errors.New("assertion expired or not yet valid")
	ErrAudience  = errors.New("assertion audience mismatch")
	ErrClient    = errors.New("assertion client_id mismatch")
	ErrResource  = errors.New("assertion resource mismatch")
	ErrClaims    = errors.New("assertion missing required claims")
	// ErrProofRequired marks an ID-JAG bound to a key (cnf claim). Bifrost does
	// not check proof of possession, so it must refuse it (draft §9.8.1.2.2).
	ErrProofRequired = errors.New("assertion requires proof of possession")
)

// allowedAlgs are the asymmetric algorithms an IdP may sign ID-JAGs with.
var allowedAlgs = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}

// Claims are the ID-JAG claims Bifrost acts on.
type Claims struct {
	Issuer   string
	Subject  string
	Audience []string
	ClientID string
	JTI      string
	Scope    string
	Resource []string
	Email    string
	Tenant   string
	IssuedAt time.Time
	Expiry   time.Time
}

// Config pins the trust relationship. ExpectedAudience is Bifrost's own issuer.
// HTTPClient builds the client for each discovery and JWKS fetch; callers pass
// the guarded OAuth client so proxy and SSRF policy match the rest of Bifrost.
type Config struct {
	TrustedIssuer    string
	JWKSURL          string // optional; discovered from TrustedIssuer when empty
	ExpectedAudience string
	HTTPClient       func() *http.Client
	Leeway           time.Duration
}

// Verifier validates ID-JAGs for one trust configuration. Safe for concurrent use.
type Verifier struct {
	cfg  Config
	keys *keySet
}

// NewVerifier builds a verifier; keys are fetched lazily on first use.
func NewVerifier(cfg Config) *Verifier {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = func() *http.Client { return &http.Client{Timeout: 10 * time.Second} }
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 60 * time.Second
	}
	return &Verifier{cfg: cfg, keys: newKeySet(cfg.TrustedIssuer, cfg.JWKSURL, cfg.HTTPClient)}
}

// Config returns the trust configuration the verifier was built for.
func (v *Verifier) Config() Config { return v.cfg }

// Verify checks an ID-JAG in the order the profile requires: the issuer is bound
// before any signature work so an attacker-run IdP can never steer key lookup,
// then signature, lifetime, audience, client binding and resource. A non-empty
// expectedResource is enforced only when the assertion carries a resource claim.
func (v *Verifier) Verify(ctx context.Context, assertion, expectedClientID, expectedResource string) (*Claims, error) {
	parser := jwt.NewParser()
	unverified, _, err := parser.ParseUnverified(assertion, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if !isIDJAGType(unverified.Header["typ"]) {
		return nil, ErrType
	}
	alg, _ := unverified.Header["alg"].(string)
	if !slices.Contains(allowedAlgs, alg) {
		return nil, fmt.Errorf("%w: %q", ErrAlgorithm, alg)
	}
	unverifiedClaims, _ := unverified.Claims.(jwt.MapClaims)
	iss, _ := unverifiedClaims["iss"].(string)
	if iss == "" || !sameURL(iss, v.cfg.TrustedIssuer) {
		return nil, fmt.Errorf("%w: %q", ErrIssuer, iss)
	}

	kid, _ := unverified.Header["kid"].(string)
	verified, err := jwt.NewParser(
		jwt.WithValidMethods(allowedAlgs),
		jwt.WithLeeway(v.cfg.Leeway),
		jwt.WithIssuedAt(),
		jwt.WithExpirationRequired(),
	).Parse(assertion, func(*jwt.Token) (any, error) {
		return v.keys.key(ctx, kid)
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrKey):
			return nil, err
		case errors.Is(err, jwt.ErrTokenExpired), errors.Is(err, jwt.ErrTokenNotValidYet), errors.Is(err, jwt.ErrTokenUsedBeforeIssued), errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
			return nil, fmt.Errorf("%w: %v", ErrExpired, err)
		default:
			return nil, fmt.Errorf("%w: %v", ErrSignature, err)
		}
	}
	mc, _ := verified.Claims.(jwt.MapClaims)
	claims := claimsFrom(mc)

	// The profile allows aud as a string or a one-element array holding our issuer.
	if len(claims.Audience) != 1 || !sameURL(claims.Audience[0], v.cfg.ExpectedAudience) {
		return nil, fmt.Errorf("%w: %v", ErrAudience, claims.Audience)
	}
	if claims.ClientID == "" || claims.ClientID != expectedClientID {
		return nil, fmt.Errorf("%w: %q", ErrClient, claims.ClientID)
	}
	if len(claims.Resource) > 0 && expectedResource != "" && !slices.ContainsFunc(claims.Resource, func(r string) bool { return sameURL(r, expectedResource) }) {
		return nil, fmt.Errorf("%w: %v", ErrResource, claims.Resource)
	}
	if claims.Subject == "" || claims.JTI == "" || claims.IssuedAt.IsZero() {
		return nil, ErrClaims
	}
	if _, bound := mc["cnf"]; bound {
		return nil, ErrProofRequired
	}
	// scope is a space-delimited string (RFC 6749 §3.3); any other shape is malformed,
	// not absent, so it cannot fall through to the no-scope default.
	if raw, ok := mc["scope"]; ok {
		if _, isString := raw.(string); !isString {
			return nil, fmt.Errorf("%w: scope is not a string", ErrMalformed)
		}
	}
	return claims, nil
}

// isIDJAGType accepts the typ header with or without the "application/" prefix (RFC 8725 §3.11).
func isIDJAGType(raw any) bool {
	typ, _ := raw.(string)
	typ = strings.ToLower(typ)
	return typ == TokenType || typ == "application/"+TokenType
}

// sameURL compares identifiers ignoring a trailing slash, which admins often add to issuer URLs.
func sameURL(a, b string) bool {
	return a != "" && strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// claimsFrom extracts the ID-JAG claims from a verified claim set.
func claimsFrom(mc jwt.MapClaims) *Claims {
	c := &Claims{
		Issuer:   stringClaim(mc, "iss"),
		Subject:  stringClaim(mc, "sub"),
		ClientID: stringClaim(mc, "client_id"),
		JTI:      stringClaim(mc, "jti"),
		Scope:    stringClaim(mc, "scope"),
		Email:    stringClaim(mc, "email"),
		Tenant:   stringClaim(mc, "tenant"),
		Audience: stringsClaim(mc, "aud"),
		Resource: stringsClaim(mc, "resource"),
	}
	if iat, err := mc.GetIssuedAt(); err == nil && iat != nil {
		c.IssuedAt = iat.Time
	}
	if exp, err := mc.GetExpirationTime(); err == nil && exp != nil {
		c.Expiry = exp.Time
	}
	return c
}

// stringClaim returns a string claim or "".
func stringClaim(mc jwt.MapClaims, name string) string {
	s, _ := mc[name].(string)
	return s
}

// stringsClaim returns a claim that may be a string or an array of strings.
func stringsClaim(mc jwt.MapClaims, name string) []string {
	switch v := mc[name].(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
