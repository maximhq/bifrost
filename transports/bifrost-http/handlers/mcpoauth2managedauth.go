package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/framework/oauth2/idjag"
	"github.com/valyala/fasthttp"
)

// maxAssertionLen bounds the ID-JAG accepted at the public token endpoint.
const maxAssertionLen = 16 * 1024

// managedAuthScope is the only scope Bifrost issues: /mcp access. What a token
// can reach is still decided by the bound virtual key or user.
const managedAuthScope = "mcp"

// ErrManagedAuthUserDenied marks a ManagedAuthUserResolver refusal (unknown
// user, no role, provisioning disabled). It yields invalid_grant; any other
// resolver error is treated as transient and yields server_error.
var ErrManagedAuthUserDenied = errors.New("managed auth user denied")

// ManagedAuthUserResolver is optionally implemented by the OAuth2IdentityResolver
// to map a verified ID-JAG subject to a Bifrost user. provision reports whether
// an unknown subject may be created as a new user (managed_auth provision_users,
// off by default). Without it, only managed clients bound to a
// virtual key can obtain tokens.
type ManagedAuthUserResolver interface {
	ResolveManagedAuthUser(ctx context.Context, claims *idjag.Claims, provision bool) (userID string, err error)
}

// ManagedAuthAuditor is optionally implemented by the OAuth2IdentityResolver to
// record every jwt-bearer grant outcome, since /oauth2/* has no audit middleware.
type ManagedAuthAuditor interface {
	RecordManagedAuthOutcome(ctx context.Context, outcome ManagedAuthOutcome)
}

// ManagedAuthIssuerSuggester is optionally implemented by the OAuth2IdentityResolver
// to prefill the trusted issuer from the configured identity provider (for Okta,
// its org authorization server). It is a UI hint only; the stored setting rules.
type ManagedAuthIssuerSuggester interface {
	SuggestedManagedAuthIssuer(ctx context.Context) string
}

// ManagedAuthOutcome describes one jwt-bearer grant attempt. Reason is internal
// and never sent to the client; the assertion itself is never recorded.
type ManagedAuthOutcome struct {
	Issued   bool
	Reason   string
	ClientID string
	Issuer   string
	Subject  string
	Email    string
	JTI      string
	BfMode   string
	BfSub    string
}

// managedAuthDenial is a refused jwt-bearer grant: the RFC 6749 error sent to
// the client, its HTTP status, and the internal reason that is logged and
// audited but never sent.
type managedAuthDenial struct {
	status      int
	code        string
	description string
	reason      string
}

// Error returns the internal reason.
func (d *managedAuthDenial) Error() string { return d.reason }

// invalidClient refuses client authentication (RFC 6749 §5.2: 401).
func invalidClient(reason string) *managedAuthDenial {
	return &managedAuthDenial{status: fasthttp.StatusUnauthorized, code: "invalid_client", description: "client authentication failed", reason: reason}
}

// invalidGrant refuses the assertion or the principal it maps to.
func invalidGrant(description, reason string) *managedAuthDenial {
	return &managedAuthDenial{status: fasthttp.StatusBadRequest, code: "invalid_grant", description: description, reason: reason}
}

// serverError reports a transient failure the client may retry.
func serverError(description, reason string) *managedAuthDenial {
	return &managedAuthDenial{status: fasthttp.StatusInternalServerError, code: "server_error", description: description, reason: reason}
}

// errNoPrincipal is the client-facing description for a subject that maps to no usable principal.
const errNoPrincipal = "the identity assertion does not map to an authorized principal"

// managedPrincipal is the bf_mode/bf_sub pair a managed client's tokens act as.
type managedPrincipal struct {
	mode string
	sub  string
}

// clientCredentials are the client_id and optional secret a token request authenticated with.
type clientCredentials struct {
	id        string
	secret    string
	hasSecret bool
}

// handleTokenAssertion serves grant_type=jwt-bearer with an ID-JAG (MCP
// Enterprise-Managed Authorization). It authenticates the managed client,
// verifies the assertion against the trusted issuer, resolves the principal the
// client is bound to and returns an access token without a refresh token: the
// client goes back to the IdP for a new ID-JAG, which is how an IdP-side
// revocation takes effect. serverCfg is the caller's snapshot, with managed
// auth enabled, so every check in the request sees the same settings.
func (h *OAuth2IssuanceHandler) handleTokenAssertion(ctx *fasthttp.RequestCtx, serverCfg *configtables.OAuth2ServerConfig) {
	cfg := serverCfg.ManagedAuth
	var outcome ManagedAuthOutcome
	deny := func(d *managedAuthDenial) {
		outcome.Reason = d.reason
		h.recordManagedAuthOutcome(ctx, outcome)
		if d.code == "invalid_client" && hasBasicAuth(ctx) {
			ctx.Response.Header.Set("WWW-Authenticate", `Basic realm="bifrost"`)
		}
		sendOAuthError(ctx, d.status, d.code, d.description)
	}

	// Body only (RFC 6749 §3.2): an assertion in the query string would land in access logs.
	assertion := string(ctx.PostArgs().Peek("assertion"))
	if assertion == "" || len(assertion) > maxAssertionLen {
		deny(&managedAuthDenial{status: fasthttp.StatusBadRequest, code: "invalid_request", description: "a single ID-JAG assertion is required", reason: "missing or oversized assertion"})
		return
	}
	creds, err := clientCredentialsFromRequest(ctx)
	outcome.ClientID = creds.id
	if err != nil {
		deny(invalidClient(err.Error()))
		return
	}
	client, denial := h.authenticateManagedClient(ctx, creds)
	if denial != nil {
		deny(denial)
		return
	}

	resource := oauth2MCPResourceURL(ctx, h.store)
	if requested := string(ctx.PostArgs().Peek("resource")); requested != "" && strings.TrimRight(requested, "/") != strings.TrimRight(resource, "/") {
		deny(&managedAuthDenial{status: fasthttp.StatusBadRequest, code: "invalid_target", description: "resource does not identify this MCP server", reason: "resource mismatch: " + requested})
		return
	}

	claims, err := h.managedAuthVerifier(cfg, oauth2IssuerURL(ctx, h.store)).Verify(ctx, assertion, creds.id, resource)
	if err != nil {
		deny(invalidGrant("the identity assertion was rejected", err.Error()))
		return
	}
	outcome.Issuer, outcome.Subject, outcome.Email, outcome.JTI = claims.Issuer, claims.Subject, claims.Email, claims.JTI
	if denial := checkManagedAuthScope(claims.Scope); denial != nil {
		deny(denial)
		return
	}

	principal, denial := h.resolveManagedPrincipal(ctx, serverCfg, client, claims)
	outcome.BfMode, outcome.BfSub = principal.mode, principal.sub
	if denial != nil {
		deny(denial)
		return
	}

	ttl := cfg.EffectiveAccessTokenTTL()
	accessToken, err := h.signAccessToken(ctx, principal.mode, principal.sub, managedAuthScope, resource, ttl, client.ID)
	if err != nil {
		outcome.Reason = "signing failed"
		h.recordManagedAuthOutcome(ctx, outcome)
		return
	}
	if err := h.store.ConfigStore.TouchOAuth2ManagedClientLastUsed(ctx, client.ID, time.Now()); err != nil {
		logger.Warn("[ManagedAuth] failed to stamp last use for managed client %s: %v", client.ID, err)
	}
	outcome.Issued = true
	h.recordManagedAuthOutcome(ctx, outcome)
	sendTokenResponse(ctx, accessToken, "", managedAuthScope, ttl)
}

// authenticateManagedClient loads the managed client and checks it is enabled
// and that the presented secret matches.
func (h *OAuth2IssuanceHandler) authenticateManagedClient(ctx *fasthttp.RequestCtx, creds clientCredentials) (*configtables.TableOAuth2ManagedClient, *managedAuthDenial) {
	client, err := h.store.ConfigStore.GetOAuth2ManagedClientByClientID(ctx, creds.id)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil, invalidClient("client is not a registered managed client")
	}
	if err != nil {
		logger.Error("[ManagedAuth] managed client lookup failed: %v", err)
		return nil, serverError("failed to look up client", "client lookup error")
	}
	if client.Disabled {
		return nil, invalidClient("client is disabled")
	}
	if err := checkManagedClientSecret(client, creds); err != nil {
		return nil, invalidClient(err.Error())
	}
	return client, nil
}

// resolveManagedPrincipal returns the principal a managed client's tokens act
// as. The principal is returned alongside a denial when it is known but not
// usable, so the outcome can still name it.
func (h *OAuth2IssuanceHandler) resolveManagedPrincipal(ctx *fasthttp.RequestCtx, serverCfg *configtables.OAuth2ServerConfig, client *configtables.TableOAuth2ManagedClient, claims *idjag.Claims) (managedPrincipal, *managedAuthDenial) {
	userModeAvailable := h.identityResolver != nil && h.identityResolver.IsUserModeAvailable()
	switch client.Identity {
	case configtables.ManagedClientIdentityVK:
		// Same cutoff the refresh grant and /mcp apply once VK identity is disabled.
		if userModeAvailable && serverCfg.DisableVKIdentity {
			return managedPrincipal{}, invalidGrant(errNoPrincipal, "virtual-key identity is disabled")
		}
		vk, err := h.store.ConfigStore.GetVirtualKey(ctx, client.VirtualKeyID)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			return managedPrincipal{}, serverError("failed to resolve the principal", "virtual key lookup error: "+err.Error())
		}
		if vk == nil || !vk.IsActiveValue() {
			return managedPrincipal{}, invalidGrant(errNoPrincipal, "bound virtual key is missing or inactive")
		}
		return managedPrincipal{mode: string(schemas.MCPAuthModeVK), sub: vk.ID}, nil
	case configtables.ManagedClientIdentityUser:
		resolver, ok := h.identityResolver.(ManagedAuthUserResolver)
		if !ok || !userModeAvailable {
			return managedPrincipal{}, invalidGrant(errNoPrincipal, "user identity is not available on this deployment")
		}
		provision := serverCfg.ManagedAuth.ProvisionUsers
		userID, err := resolver.ResolveManagedAuthUser(ctx, claims, provision)
		switch {
		case errors.Is(err, ErrManagedAuthUserDenied):
			return managedPrincipal{}, invalidGrant(errNoPrincipal, err.Error())
		case err != nil:
			return managedPrincipal{}, serverError("failed to resolve the principal", "user resolution error: "+err.Error())
		case userID == "":
			return managedPrincipal{}, invalidGrant(errNoPrincipal, "subject does not map to a user")
		}
		principal := managedPrincipal{mode: string(schemas.MCPAuthModeUser), sub: userID}
		active, err := h.identityResolver.IsUserActive(ctx, userID)
		if err != nil {
			return principal, serverError("failed to resolve the principal", "user liveness error: "+err.Error())
		}
		if !active {
			return principal, invalidGrant(errNoPrincipal, "user is not active")
		}
		return principal, nil
	}
	return managedPrincipal{}, invalidGrant(errNoPrincipal, "managed client has an unknown identity binding")
}

// managedAuthVerifier returns the cached verifier for the current trust
// settings, building a new one when the issuer, JWKS URL or audience changed.
// Key fetches use the guarded OAuth client, so the MCP OAuth private-target
// allowlist and proxy settings apply to the identity provider too.
func (h *OAuth2IssuanceHandler) managedAuthVerifier(cfg *configtables.ManagedAuthConfig, audience string) *idjag.Verifier {
	if v := h.idjagVerifier.Load(); v != nil {
		c := v.Config()
		if c.TrustedIssuer == cfg.TrustedIssuer && c.JWKSURL == cfg.JWKSURL && c.ExpectedAudience == audience {
			return v
		}
	}
	v := idjag.NewVerifier(idjag.Config{
		TrustedIssuer: cfg.TrustedIssuer, JWKSURL: cfg.JWKSURL, ExpectedAudience: audience,
		HTTPClient: func() *http.Client { return oauth2.NewAdminConfiguredHTTPClient(idpFetchTimeout) },
	})
	h.idjagVerifier.Store(v)
	return v
}

// idpFetchTimeout bounds one discovery or JWKS request to the trusted issuer.
const idpFetchTimeout = 10 * time.Second

// recordManagedAuthOutcome logs the outcome and forwards it to the auditor when one is wired.
func (h *OAuth2IssuanceHandler) recordManagedAuthOutcome(ctx *fasthttp.RequestCtx, o ManagedAuthOutcome) {
	if o.Issued {
		logger.Debug("[ManagedAuth] issued token client_id=%q iss=%q sub=%q jti=%q bf_mode=%s bf_sub=%s", o.ClientID, o.Issuer, o.Subject, o.JTI, o.BfMode, o.BfSub)
	} else {
		logger.Warn("[ManagedAuth] denied jwt-bearer grant reason=%q client_id=%q iss=%q sub=%q email=%q jti=%q", o.Reason, o.ClientID, o.Issuer, o.Subject, o.Email, o.JTI)
	}
	if auditor, ok := h.identityResolver.(ManagedAuthAuditor); ok {
		auditor.RecordManagedAuthOutcome(ctx, o)
	}
}

// clientCredentialsFromRequest reads client credentials from HTTP Basic auth
// (RFC 6749 §2.3.1, form-encoded halves) or the POST body. Using both with
// different client IDs, or both secrets, is refused. Secrets are read from the
// body only: a query-string secret would land in access logs.
func clientCredentialsFromRequest(ctx *fasthttp.RequestCtx) (clientCredentials, error) {
	// Body only, like the secret: FormValue would let a query-string client_id win.
	bodyID := string(ctx.PostArgs().Peek("client_id"))
	bodySecretPresent := ctx.PostArgs().Has("client_secret")
	bodySecret := string(ctx.PostArgs().Peek("client_secret"))
	auth := string(ctx.Request.Header.Peek("Authorization"))
	const prefix = "Basic "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[len(prefix):]))
		if err != nil {
			return clientCredentials{}, errors.New("malformed basic credentials")
		}
		rawID, rawSecret, _ := strings.Cut(string(decoded), ":")
		id, idErr := url.QueryUnescape(rawID)
		sec, secErr := url.QueryUnescape(rawSecret)
		if idErr != nil || secErr != nil || id == "" {
			return clientCredentials{}, errors.New("malformed basic credentials")
		}
		creds := clientCredentials{id: id, secret: sec, hasSecret: sec != ""}
		if (bodyID != "" && bodyID != id) || bodySecretPresent {
			return clientCredentials{id: id}, errors.New("client credentials sent by more than one method")
		}
		return creds, nil
	}
	if bodyID == "" {
		return clientCredentials{}, errors.New("missing client_id")
	}
	return clientCredentials{id: bodyID, secret: bodySecret, hasSecret: bodySecret != ""}, nil
}

// checkManagedAuthScope applies the IdP's scope decision (ID-JAG draft §4.4.1).
// No scope claim grants the default. A claim must include managedAuthScope;
// other scopes in it are ignored, since the granted scope may be a subset.
func checkManagedAuthScope(claim string) *managedAuthDenial {
	if strings.TrimSpace(claim) == "" || slices.Contains(strings.Fields(claim), managedAuthScope) {
		return nil
	}
	return &managedAuthDenial{
		status: fasthttp.StatusBadRequest, code: "invalid_scope",
		description: fmt.Sprintf("the identity assertion does not grant the %q scope", managedAuthScope),
		reason:      "assertion scope lacks " + managedAuthScope + ": " + claim,
	}
}

// checkManagedClientSecret enforces confidential-client secrets in constant time.
// A secret sent for a public client is refused so a misconfigured connector is
// visible rather than silently accepted.
func checkManagedClientSecret(client *configtables.TableOAuth2ManagedClient, creds clientCredentials) error {
	if !client.HasSecret() {
		if creds.hasSecret {
			return errors.New("client secret sent for a public client")
		}
		return nil
	}
	if !creds.hasSecret || subtle.ConstantTimeCompare([]byte(hashSHA256Hex(creds.secret)), []byte(client.ClientSecretHash)) != 1 {
		return errors.New("client secret mismatch")
	}
	return nil
}

// hasBasicAuth reports whether the request used HTTP Basic client authentication.
func hasBasicAuth(ctx *fasthttp.RequestCtx) bool {
	auth := ctx.Request.Header.Peek("Authorization")
	return len(auth) > 6 && strings.EqualFold(string(auth[:6]), "basic ")
}
