package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// OAuth2IdentityResolver is an optional extension point for user identity
// resolution during the OAuth2 consent flow. When nil, only vk and session
// modes are offered. Implementations are registered at server init time.
type OAuth2IdentityResolver interface {
	// IsUserModeAvailable reports whether user mode can be offered (e.g. an
	// identity provider is configured).
	IsUserModeAvailable() bool
	// ResolveUserIdentity reads the current session from the request context
	// and returns the resolved userID and display name. The auth middleware
	// populates the session into context before the handler runs (same path as
	// the existing per-user upstream OAuth consent pages). Returns empty
	// userID when no valid session is present.
	ResolveUserIdentity(ctx *fasthttp.RequestCtx) (userID string, name string, err error)
	// ResolveVKUserUpgrade checks whether a VK is bound to a specific user.
	// Returns the userID if bound, empty string if unbound.
	ResolveVKUserUpgrade(ctx context.Context, vkID string) (userID string, err error)
	// ResolveUserVirtualKey returns the ID of a virtual key that represents the
	// user's effective MCP grant, or an empty string when the user has none.
	// Callers use it to scope the /mcp tool listing for user-mode tokens, which
	// carry no virtual key of their own. When a user maps to several equivalent
	// virtual keys, any one of them may be returned.
	ResolveUserVirtualKey(ctx context.Context, userID string) (vkID string, err error)
	// IsUserActive reports whether the given user identity still exists and is
	// usable. Returns (false, nil) when the user is gone or deactivated; an
	// error is reserved for transient lookup failures that should be retried.
	// Used to cut off a deleted user's grant at request and refresh time rather
	// than waiting for the access token to expire, mirroring the virtual-key
	// liveness check.
	IsUserActive(ctx context.Context, userID string) (active bool, err error)
}

// OAuth2ConsentHandler serves the two consent flow APIs:
//
//   - GET  /api/oauth2/consent/flows/{id} — flow detail + available modes
//   - PUT  /api/oauth2/consent/flows/{id} — identity resolution + code mint
//
// These routes go through the standard auth middleware chain. The
// oauth2_consent temp token (embedded in the consent page URL fragment) acts
// as the credential via the middleware's temp-token fallback path — identical
// to how the existing per-user upstream OAuth consent pages work.
type OAuth2ConsentHandler struct {
	store            *lib.Config
	tempTokens       *temptoken.Service     // used to invalidate the token after consent
	identityResolver OAuth2IdentityResolver // optional; nil = vk + session modes only
}

// NewOAuth2ConsentHandler creates a new consent handler. identityResolver may
// be nil — the handler degrades gracefully to vk + session modes only.
func NewOAuth2ConsentHandler(store *lib.Config, tempTokens *temptoken.Service, identityResolver OAuth2IdentityResolver) *OAuth2ConsentHandler {
	return &OAuth2ConsentHandler{
		store:            store,
		tempTokens:       tempTokens,
		identityResolver: identityResolver,
	}
}

// RegisterRoutes wires the two consent routes with the provided middlewares.
func (h *OAuth2ConsentHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/oauth2/consent/flows/{id}", lib.ChainMiddlewares(h.flowDetail, middlewares...))
	r.PUT("/api/oauth2/consent/flows/{id}", lib.ChainMiddlewares(h.flowSubmit, middlewares...))
}

// consentFlowDetailResponse is the wire shape for GET /api/oauth2/consent/flows/{id}.
type consentFlowDetailResponse struct {
	ClientName     string            `json:"client_name"`
	AvailableModes []consentFlowMode `json:"available_modes"`
	LoggedInUser   *loggedInUser     `json:"logged_in_user,omitempty"` // non-nil when a valid session is present
	ExpiresAt      string            `json:"expires_at"`
}

type loggedInUser struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// GET /api/oauth2/consent/flows/{id}
func (h *OAuth2ConsentHandler) flowDetail(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return
	}
	flowID, ok := ctx.UserValue("id").(string)
	if !ok || flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid flow id")
		return
	}
	req := h.loadPendingFlow(ctx, flowID)
	if req == nil {
		return
	}

	client, err := h.store.ConfigStore.GetOAuth2ClientByClientID(ctx, req.ClientID)
	if err != nil || client == nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusInternalServerError, "client registration not found")
			return
		}
		logger.Error("oauth2 consent: failed to load client: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to load client")
		return
	}

	resp := consentFlowDetailResponse{
		ClientName:     client.ClientName,
		AvailableModes: h.availableModes(),
		ExpiresAt:      req.ExpiresAt.UTC().Format(time.RFC3339),
	}

	// If a valid session is present, surface the user so the consent page can
	// offer "Continue as {user}" without requiring a login redirect.
	if h.identityResolver != nil {
		if userID, name, err := h.identityResolver.ResolveUserIdentity(ctx); err == nil && userID != "" {
			resp.LoggedInUser = &loggedInUser{ID: userID, Name: name}
		}
	}

	SendJSON(ctx, resp)
}

type consentFlowMode string

const (
	consentFlowModeVK      consentFlowMode = "vk"
	consentFlowModeSession consentFlowMode = "session"
	consentFlowModeUser    consentFlowMode = "user"
)

// consentFlowSubmitRequest is the wire shape for PUT /api/oauth2/consent/flows/{id}.
type consentFlowSubmitRequest struct {
	Mode  consentFlowMode `json:"mode"`  // "vk" | "session" | "user"
	Value string          `json:"value"` // VK plaintext value for mode=vk; empty for session/user
}

// consentFlowSubmitResponse is returned after successful consent. The frontend
// navigates to RedirectURL to complete the OAuth handshake with the MCP client.
type consentFlowSubmitResponse struct {
	RedirectURL string `json:"redirect_url"`
}

// PUT /api/oauth2/consent/flows/{id}
func (h *OAuth2ConsentHandler) flowSubmit(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return
	}
	flowID, ok := ctx.UserValue("id").(string)
	if !ok || flowID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid flow id")
		return
	}
	req := h.loadPendingFlow(ctx, flowID)
	if req == nil {
		return
	}

	var body consentFlowSubmitRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &body); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid request body")
		return
	}

	allowed := h.availableModes()
	modeAllowed := slices.Contains(allowed, body.Mode)
	if !modeAllowed {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("mode %q is not available", body.Mode))
		return
	}

	bfMode, bfSub, err := h.resolveIdentity(ctx, body)
	if err != nil {
		// resolveIdentity tags each failure with its HTTP status: client mistakes
		// stay 400, infrastructure faults (crypto/DB) surface as 500. Fall back to
		// 400 for any untagged error.
		status := fasthttp.StatusBadRequest
		if ce, ok := errors.AsType[*consentError](err); ok {
			status = ce.status
		}
		SendError(ctx, status, err.Error())
		return
	}

	// Mint the authorization code. Only the SHA256 hash is stored — the
	// plaintext travels to the client via the redirect URI and is never
	// persisted anywhere (RFC 6749 §4.1.2).
	code, err := generateSecureToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to generate authorization code")
		return
	}

	req.BfMode = string(bfMode)
	req.BfSub = bfSub
	hash := hashSHA256Hex(code)
	req.CodeHash = &hash
	req.Status = configtables.OAuth2AuthorizeRequestStatusConsented
	req.UpdatedAt = time.Now()

	if err := h.store.ConfigStore.ConsentOAuth2AuthorizeRequest(ctx, req); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			// The flow was concurrently consented (or otherwise left pending) between
			// load and write — a racing duplicate submit. Don't overwrite the code
			// the winning request already minted.
			SendError(ctx, fasthttp.StatusConflict, "authorization flow has already been completed")
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to record consent")
		return
	}

	// Invalidate the temp token so the consent page cannot be submitted twice.
	if h.tempTokens != nil {
		_, _ = h.tempTokens.DeleteByResourceID(ctx, temptoken.OAuth2ConsentScopeName, flowID)
	}

	issuer := oauth2IssuerURL(ctx, h.store)
	redirectURL, err := buildRedirectURL(req.RedirectURI, map[string]string{
		"code":  code,
		"state": req.State,
		"iss":   issuer, // RFC 9207: include issuer so client can validate
	})
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to build redirect URL: %v", err))
		return
	}

	SendJSON(ctx, consentFlowSubmitResponse{RedirectURL: redirectURL})
}

// loadPendingFlow reads the authorize request and validates it is still pending
// and within its TTL. Writes an error response and returns nil on any failure.
func (h *OAuth2ConsentHandler) loadPendingFlow(ctx *fasthttp.RequestCtx, flowID string) *configtables.TableOAuth2AuthorizeRequest {
	req, err := h.store.ConfigStore.GetOAuth2AuthorizeRequestByID(ctx, flowID)
	if err != nil || req == nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "authorization flow not found or expired")
			return nil
		}
		logger.Error("oauth2 consent: failed to load flow: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to load flow")
		return nil
	}
	if req.Status != configtables.OAuth2AuthorizeRequestStatusPending {
		SendError(ctx, fasthttp.StatusGone, "authorization flow has already been completed")
		return nil
	}
	if time.Now().After(req.ExpiresAt) {
		SendError(ctx, fasthttp.StatusGone, "authorization flow has expired")
		return nil
	}
	return req
}

// availableModes derives which identity modes to offer based on server config.
func (h *OAuth2ConsentHandler) availableModes() []consentFlowMode {
	h.store.Mu.RLock()
	enforceAuth := h.store.ClientConfig.EnforceAuthOnInference
	h.store.Mu.RUnlock()
	// oauth2ServerCfg is nil-safe: OAuth2ServerConfig is an unset pointer until the
	// AS is configured, and a raw deref here panics the request worker. It takes its
	// own read lock, so it's called outside the lock above.
	disableVKIdentity := oauth2ServerCfg(h.store).DisableVKIdentity

	userModeAvailable := h.identityResolver != nil && h.identityResolver.IsUserModeAvailable()

	// Virtual-key identity may be suppressed, but only when user identity is
	// available — so the flow always retains the identity-provider path and can
	// never be left with no usable mode.
	disableVK := userModeAvailable && disableVKIdentity

	modes := []consentFlowMode{}
	if !disableVK {
		modes = append(modes, consentFlowModeVK)
	}
	if !enforceAuth {
		modes = append(modes, consentFlowModeSession)
	}
	if userModeAvailable {
		modes = append(modes, consentFlowModeUser)
	}
	return modes
}

// consentError carries an HTTP status so the caller can distinguish a client
// mistake (400 — bad/inactive VK, no session) from a server-side failure
// (500 — crypto or DB error). resolveIdentity mixes both kinds; without the
// status every failure would be reported as a 400, mislabeling a broken server
// as a bad request and leaking the cause to the client.
type consentError struct {
	status  int
	message string
}

func (e *consentError) Error() string { return e.message }

// clientConsentError is a 400 the caller can act on; the message is shown verbatim.
func clientConsentError(format string, args ...any) error {
	return &consentError{status: fasthttp.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

// serverConsentError is a 500 infrastructure failure. The cause is logged by the
// caller; only the generic message reaches the client.
func serverConsentError(message string) error {
	return &consentError{status: fasthttp.StatusInternalServerError, message: message}
}

// resolveIdentity resolves bf_mode and bf_sub for the submitted consent.
func (h *OAuth2ConsentHandler) resolveIdentity(ctx *fasthttp.RequestCtx, body consentFlowSubmitRequest) (schemas.MCPAuthMode, string, error) {
	switch body.Mode {
	case consentFlowModeVK:
		return h.resolveVKIdentity(ctx, body.Value)
	case consentFlowModeSession:
		// Server-mints the session token — never client-asserted.
		sessionToken, err := generateSecureToken(32)
		if err != nil {
			// crypto/rand failure is a server fault, not a bad request.
			logger.Error("oauth2 consent: failed to generate session token: %v", err)
			return "", "", serverConsentError("failed to generate session token")
		}
		return schemas.MCPAuthModeSession, sessionToken, nil
	case consentFlowModeUser:
		if h.identityResolver == nil {
			return "", "", clientConsentError("user mode is not available")
		}
		userID, _, err := h.identityResolver.ResolveUserIdentity(ctx)
		if err != nil {
			// Keep the cause server-side — it can carry infrastructure details
			// that must not leak to the client.
			logger.Error("oauth2 consent: user identity resolution failed: %v", err)
			return "", "", serverConsentError("failed to resolve user identity")
		}
		if userID == "" {
			return "", "", clientConsentError("no active session; please sign in first")
		}
		return schemas.MCPAuthModeUser, userID, nil
	default:
		return "", "", clientConsentError("unknown mode %q", body.Mode)
	}
}

// resolveVKIdentity validates a VK and checks for user binding.
// If a user-bound VK is presented when an identity provider is configured,
// the logged-in user must match the VK owner before the upgrade proceeds.
func (h *OAuth2ConsentHandler) resolveVKIdentity(ctx *fasthttp.RequestCtx, vkValue string) (schemas.MCPAuthMode, string, error) {
	if strings.TrimSpace(vkValue) == "" {
		return "", "", clientConsentError("virtual key value is required")
	}

	vk, err := h.store.ConfigStore.GetVirtualKeyByValue(ctx, vkValue)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return "", "", clientConsentError("virtual key not found")
		}
		// A non-NotFound lookup error is an infrastructure fault - log the cause
		// and return a generic 500.
		logger.Error("oauth2 consent: failed to load virtual key: %v", err)
		return "", "", serverConsentError("failed to validate virtual key")
	}
	if !vk.IsActiveValue() {
		return "", "", clientConsentError("virtual key is inactive")
	}

	// Check for a VK→user binding. If the VK is bound to a specific user,
	// upgrade the identity so the JWT carries the user ID — this ensures
	// per-user upstream OAuth tokens are unified under a single identity
	// regardless of whether auth happened via VK or direct user login.
	if h.identityResolver != nil {
		userID, err := h.identityResolver.ResolveVKUserUpgrade(ctx, vk.ID)
		if err != nil {
			// Keep the cause server-side — a DB/driver error can carry table or
			// connection details that must not leak to the client.
			logger.Error("oauth2 consent: VK user binding lookup failed: %v", err)
			return "", "", serverConsentError("failed to check VK user binding")
		}
		if userID != "" {
			// VK is user-bound. Verify the currently logged-in user matches the
			// VK owner before upgrading — possession of a user-bound VK alone is
			// not sufficient proof of identity when session auth is available.
			if h.identityResolver.IsUserModeAvailable() {
				loggedInUserID, _, sessionErr := h.identityResolver.ResolveUserIdentity(ctx)
				if sessionErr != nil || loggedInUserID == "" {
					return "", "", clientConsentError("this key belongs to a specific user; please sign in to continue")
				}
				if loggedInUserID != userID {
					return "", "", clientConsentError("this key belongs to a different user than the currently signed-in user")
				}
			}
			return schemas.MCPAuthModeUser, userID, nil
		}
	}

	return schemas.MCPAuthModeVK, vk.ID, nil
}

// buildRedirectURL appends query params to a base URL using net/url so
// existing query params are preserved and values are correctly percent-encoded.
// Returns an error when base cannot be parsed so the caller can return a 500
// before the temp token and authorization code become irrecoverable.
func buildRedirectURL(base string, params map[string]string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid redirect URI: %w", err)
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
