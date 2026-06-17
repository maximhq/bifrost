package handlers

import (
	"fmt"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// jwtMCPClaims are the custom claims embedded in Bifrost-issued /mcp JWTs.
type jwtMCPClaims struct {
	jwt.RegisteredClaims
	BfMode string `json:"bf_mode"` // user | vk | session
	Scope  string `json:"scope"`
}

// extractBearerJWT returns the raw JWT string from an Authorization: Bearer
// header when the token looks like a JWT (starts with "eyJ"). Returns empty
// string when the header is absent or the token is a VK (starts with "sk-bf-").
func extractBearerJWT(ctx *fasthttp.RequestCtx) string {
	auth := strings.TrimSpace(string(ctx.Request.Header.Peek("Authorization")))
	if auth == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return ""
	}
	token := strings.TrimSpace(auth[7:])
	// JWTs are base64url-encoded JSON starting with '{', which encodes to "eyJ".
	// VKs start with the "sk-bf-" prefix. Anything not starting with "eyJ" is
	// treated as a non-JWT credential and left to the VK path.
	if !strings.HasPrefix(token, "eyJ") {
		return ""
	}
	return token
}

// verifyMCPJWT parses and verifies a Bifrost-issued JWT for the /mcp endpoint.
// It validates the RS256 signature using the active signing key, checks the
// audience matches the canonical /mcp resource URL (RFC 8707), and returns
// the verified claims.
func verifyMCPJWT(ctx *fasthttp.RequestCtx, rawToken string, store *lib.Config) (*jwtMCPClaims, error) {
	if store.ConfigStore == nil {
		return nil, fmt.Errorf("config store unavailable")
	}

	signingKey, err := store.ConfigStore.GetOAuth2SigningKey(ctx)
	if err != nil || signingKey == nil {
		return nil, fmt.Errorf("signing key unavailable")
	}

	privKey, err := parseRSAPrivateKeyPEM(signingKey.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("invalid signing key: %w", err)
	}
	pubKey := &privKey.PublicKey

	// Pin the issuer to this instance: the kid + signature checks only prove the
	// token was signed by our key, so a different authorization server sharing
	// the same keypair would otherwise pass. Issuance stamps iss from the same
	// oauth2IssuerURL, so the two always agree.
	issuer := oauth2IssuerURL(ctx, store)

	claims := &jwtMCPClaims{}
	tok, err := jwt.ParseWithClaims(rawToken, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		if kid, _ := t.Header["kid"].(string); kid != signingKey.KID {
			return nil, fmt.Errorf("unknown key id %q", kid)
		}
		return pubKey, nil
	}, jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithIssuer(issuer),
		// Accept exactly the algorithm we issue. The SigningMethodRSA type
		// assertion above admits the whole RS family (RS256/384/512); pin to
		// RS256 so verification matches issuance.
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}))
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if !tok.Valid {
		return nil, fmt.Errorf("token is not valid")
	}
	// WithIssuedAt only validates iat when present; require it, since every
	// token we issue stamps one.
	if claims.IssuedAt == nil {
		return nil, fmt.Errorf("token missing iat claim")
	}

	// RFC 8707: the token must have been issued for this specific resource.
	resource := oauth2MCPResourceURL(ctx, store)
	aud, err := claims.GetAudience()
	if err != nil || !slices.Contains(aud, resource) {
		return nil, fmt.Errorf("token audience does not match this resource")
	}

	return claims, nil
}

// injectJWTContext sets the identity context keys from verified JWT claims,
// mirroring what header auth sets today so everything downstream (governance,
// per-user upstream OAuth, tool-group filtering) works unchanged.
//
// bf_mode=user    → BifrostContextKeyUserID
// bf_mode=vk      → BifrostContextKeyVirtualKey (governance derives the VK row ID from it)
// bf_mode=session → BifrostContextKeyMCPSessionID
func injectJWTContext(bifrostCtx *schemas.BifrostContext, claims *jwtMCPClaims, vk *configtables.TableVirtualKey) error {
	sub := claims.Subject
	if sub == "" {
		return fmt.Errorf("JWT missing sub claim")
	}
	switch schemas.MCPAuthMode(claims.BfMode) {
	case schemas.MCPAuthModeUser:
		bifrostCtx.SetValue(schemas.BifrostContextKeyUserID, sub)
	case schemas.MCPAuthModeVK:
		if vk == nil {
			return fmt.Errorf("VK not provided for vk-mode JWT injection")
		}
		// Set the VK value only. Governance's PreMCPConnectionHook resolves it to
		// the VK row ID (BifrostContextKeyGovernanceVirtualKeyID) on the connect
		// path before the per-user credential resolver needs it — the same way the
		// x-bf-vk header path does, which never stamps the row ID at ingress either.
		bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, vk.Value)
	case schemas.MCPAuthModeSession:
		bifrostCtx.SetValue(schemas.BifrostContextKeyMCPSessionID, sub)
	default:
		return fmt.Errorf("unknown bf_mode %q in JWT", claims.BfMode)
	}
	return nil
}

// wwwAuthenticateValue returns the WWW-Authenticate header value pointing at
// the /mcp resource metadata endpoint, per RFC 9728 §5.
func wwwAuthenticateValue(ctx *fasthttp.RequestCtx, store *lib.Config) string {
	base := oauth2IssuerURL(ctx, store)
	return fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base)
}
