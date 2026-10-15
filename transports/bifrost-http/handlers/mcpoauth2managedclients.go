package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Bounds on administrator-supplied managed-client fields. A client_id can be a
// client ID metadata document URL, so it gets the same cap as redirect URIs.
const (
	maxManagedClientIDLen   = 2048
	maxManagedClientNameLen = 255
	managedClientIDPrefix   = "bfmc_"
)

// OAuth2ManagedClientsHandler serves the admin API for clients allowed to use
// the jwt-bearer (ID-JAG) grant:
//
//   - GET    /api/oauth2/managed-clients
//   - POST   /api/oauth2/managed-clients
//   - PATCH  /api/oauth2/managed-clients/{id}
//   - DELETE /api/oauth2/managed-clients/{id}
//   - POST   /api/oauth2/managed-clients/{id}/rotate-secret
type OAuth2ManagedClientsHandler struct {
	store            *lib.Config
	identityResolver OAuth2IdentityResolver // nil in OSS-only builds; gates the user identity
}

// NewOAuth2ManagedClientsHandler creates the managed-clients handler. identityResolver may be nil.
func NewOAuth2ManagedClientsHandler(store *lib.Config, identityResolver OAuth2IdentityResolver) *OAuth2ManagedClientsHandler {
	return &OAuth2ManagedClientsHandler{store: store, identityResolver: identityResolver}
}

// RegisterRoutes wires the managed-client admin routes behind the API middleware chain.
func (h *OAuth2ManagedClientsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/oauth2/managed-clients", lib.ChainMiddlewares(h.list, middlewares...))
	r.POST("/api/oauth2/managed-clients", lib.ChainMiddlewares(h.create, middlewares...))
	r.PATCH("/api/oauth2/managed-clients/{id}", lib.ChainMiddlewares(h.update, middlewares...))
	r.DELETE("/api/oauth2/managed-clients/{id}", lib.ChainMiddlewares(h.delete, middlewares...))
	r.POST("/api/oauth2/managed-clients/{id}/rotate-secret", lib.ChainMiddlewares(h.rotateSecret, middlewares...))
}

// managedClientView is the wire shape of a managed client. The secret hash never leaves the server.
type managedClientView struct {
	configtables.TableOAuth2ManagedClient
	HasSecret    bool   `json:"has_secret"`
	ClientSecret string `json:"client_secret,omitempty"` // create and rotate-secret responses only
}

// managedClientCreateRequest is the POST body. An empty client_id is generated.
type managedClientCreateRequest struct {
	Name         string `json:"name"`
	ClientID     string `json:"client_id"`
	Public       bool   `json:"public"`
	Identity     string `json:"identity"`
	VirtualKeyID string `json:"virtual_key_id"`
}

// managedClientUpdateRequest is the PATCH body; nil fields are left unchanged.
// client_id is immutable because the IdP's resource connection references it.
type managedClientUpdateRequest struct {
	Name         *string `json:"name"`
	Identity     *string `json:"identity"`
	VirtualKeyID *string `json:"virtual_key_id"`
	Disabled     *bool   `json:"disabled"`
}

// list returns every managed client.
func (h *OAuth2ManagedClientsHandler) list(ctx *fasthttp.RequestCtx) {
	if !h.storeReady(ctx) {
		return
	}
	clients, err := h.store.ConfigStore.ListOAuth2ManagedClients(ctx)
	if err != nil {
		logger.Error("oauth2 managed clients: list failed: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to list managed clients")
		return
	}
	views := make([]managedClientView, 0, len(clients))
	for _, c := range clients {
		views = append(views, managedClientView{TableOAuth2ManagedClient: c, HasSecret: c.HasSecret()})
	}
	resp := map[string]any{"clients": views, "user_identity_available": h.userIdentityAvailable()}
	if suggester, ok := h.identityResolver.(ManagedAuthIssuerSuggester); ok {
		if issuer := suggester.SuggestedManagedAuthIssuer(ctx); issuer != "" {
			resp["suggested_trusted_issuer"] = issuer
		}
	}
	SendJSON(ctx, resp)
}

// create registers a managed client, returning its secret once for confidential clients.
func (h *OAuth2ManagedClientsHandler) create(ctx *fasthttp.RequestCtx) {
	if !h.storeReady(ctx) || !h.requireAuthenticatedAdmin(ctx) {
		return
	}
	var req managedClientCreateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "malformed request body")
		return
	}
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.Name = strings.TrimSpace(req.Name)
	if req.ClientID == "" {
		generated, err := generateSecureToken(16)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to generate client_id")
			return
		}
		req.ClientID = managedClientIDPrefix + generated
	}
	if len(req.ClientID) > maxManagedClientIDLen || strings.ContainsAny(req.ClientID, " \t\r\n") {
		SendError(ctx, fasthttp.StatusBadRequest, "client_id must be a single token of at most 2048 characters")
		return
	}
	client := &configtables.TableOAuth2ManagedClient{
		ID:           uuid.New().String(),
		ClientID:     req.ClientID,
		Name:         req.Name,
		Identity:     req.Identity,
		VirtualKeyID: strings.TrimSpace(req.VirtualKeyID),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	normalizeManagedClient(client)
	if err := h.validateClient(ctx, client, true); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	var secret string
	if !req.Public {
		var err error
		if secret, err = generateSecureToken(32); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "failed to generate client secret")
			return
		}
		client.ClientSecretHash = hashSHA256Hex(secret)
	}
	if err := h.store.ConfigStore.CreateOAuth2ManagedClient(ctx, client); err != nil {
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "a managed client with this client_id already exists")
			return
		}
		logger.Error("oauth2 managed clients: create failed: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to create managed client")
		return
	}
	SendJSONWithStatus(ctx, managedClientView{TableOAuth2ManagedClient: *client, HasSecret: client.HasSecret(), ClientSecret: secret}, fasthttp.StatusCreated)
}

// update changes a managed client's name, identity binding or disabled flag.
func (h *OAuth2ManagedClientsHandler) update(ctx *fasthttp.RequestCtx) {
	if !h.storeReady(ctx) || !h.requireAuthenticatedAdmin(ctx) {
		return
	}
	client, ok := h.loadClient(ctx)
	if !ok {
		return
	}
	before := *client
	var req managedClientUpdateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "malformed request body")
		return
	}
	if req.Name != nil {
		client.Name = strings.TrimSpace(*req.Name)
	}
	if req.Identity != nil {
		client.Identity = *req.Identity
	}
	if req.VirtualKeyID != nil {
		client.VirtualKeyID = strings.TrimSpace(*req.VirtualKeyID)
	}
	if req.Disabled != nil {
		client.Disabled = *req.Disabled
	}
	normalizeManagedClient(client)
	// A key deactivated after the client was bound must not block renaming or
	// disabling it, so the binding is re-checked only when the PATCH changes it.
	bindingChanged := client.Identity != before.Identity || client.VirtualKeyID != before.VirtualKeyID
	if err := h.validateClient(ctx, client, bindingChanged); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	// Write only what the request carried, so an overlapping PATCH or secret
	// rotation never clobbers the other's fields with this request's stale copy.
	patch := configtables.OAuth2ManagedClientPatch{Name: req.Name, Identity: req.Identity, Disabled: req.Disabled}
	if req.Name != nil {
		patch.Name = &client.Name
	}
	if req.VirtualKeyID != nil || client.VirtualKeyID != before.VirtualKeyID {
		patch.VirtualKeyID = &client.VirtualKeyID
	}
	if err := h.store.ConfigStore.UpdateOAuth2ManagedClient(ctx, client.ID, patch); err != nil {
		h.sendStoreError(ctx, err, "update")
		return
	}
	SendJSON(ctx, managedClientView{TableOAuth2ManagedClient: *client, HasSecret: client.HasSecret()})
}

// delete removes a managed client. Tokens it already minted run out on their own lifetime.
func (h *OAuth2ManagedClientsHandler) delete(ctx *fasthttp.RequestCtx) {
	if !h.storeReady(ctx) || !h.requireAuthenticatedAdmin(ctx) {
		return
	}
	id, _ := ctx.UserValue("id").(string)
	if err := h.store.ConfigStore.DeleteOAuth2ManagedClient(ctx, id); err != nil {
		h.sendStoreError(ctx, err, "delete")
		return
	}
	SendJSON(ctx, map[string]any{"status": "success"})
}

// rotateSecret replaces the client secret and returns the new one once. It also
// turns a public client into a confidential one.
func (h *OAuth2ManagedClientsHandler) rotateSecret(ctx *fasthttp.RequestCtx) {
	if !h.storeReady(ctx) || !h.requireAuthenticatedAdmin(ctx) {
		return
	}
	client, ok := h.loadClient(ctx)
	if !ok {
		return
	}
	secret, err := generateSecureToken(32)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "failed to generate client secret")
		return
	}
	client.ClientSecretHash = hashSHA256Hex(secret)
	if err := h.store.ConfigStore.UpdateOAuth2ManagedClient(ctx, client.ID, configtables.OAuth2ManagedClientPatch{ClientSecretHash: &client.ClientSecretHash}); err != nil {
		h.sendStoreError(ctx, err, "rotate secret for")
		return
	}
	SendJSON(ctx, managedClientView{TableOAuth2ManagedClient: *client, HasSecret: true, ClientSecret: secret})
}

// normalizeManagedClient drops a virtual key binding that the identity does not use.
func normalizeManagedClient(c *configtables.TableOAuth2ManagedClient) {
	if c.Identity == configtables.ManagedClientIdentityUser {
		c.VirtualKeyID = ""
	}
}

// validateClient reports, as a client-facing error, why the client is unusable.
// checkBinding also verifies the identity binding is live (an active virtual
// key, or an identity provider for user identity); callers pass false when a
// PATCH leaves the binding untouched.
func (h *OAuth2ManagedClientsHandler) validateClient(ctx *fasthttp.RequestCtx, c *configtables.TableOAuth2ManagedClient, checkBinding bool) error {
	if len(c.Name) > maxManagedClientNameLen {
		return errors.New("name must be at most 255 characters")
	}
	switch c.Identity {
	case configtables.ManagedClientIdentityVK:
		if c.VirtualKeyID == "" {
			return errors.New("virtual_key_id is required when identity is vk")
		}
		if !checkBinding {
			return nil
		}
		vk, err := h.store.ConfigStore.GetVirtualKey(ctx, c.VirtualKeyID)
		if err != nil || vk == nil || !vk.IsActiveValue() {
			return errors.New("virtual_key_id must reference an active virtual key")
		}
	case configtables.ManagedClientIdentityUser:
		if checkBinding && !h.userIdentityAvailable() {
			return errors.New("user identity requires an identity provider")
		}
	default:
		return errors.New("identity must be vk or user")
	}
	return nil
}

// userIdentityAvailable reports whether ID-JAG subjects can be mapped to Bifrost users.
func (h *OAuth2ManagedClientsHandler) userIdentityAvailable() bool {
	return h.identityResolver != nil && h.identityResolver.IsUserModeAvailable()
}

// requireAuthenticatedAdmin refuses credential-minting changes when dashboard auth
// is bypassed, matching the allowed_redirect_uris rule. Writes 403 and returns false.
func (h *OAuth2ManagedClientsHandler) requireAuthenticatedAdmin(ctx *fasthttp.RequestCtx) bool {
	if isAuthBypassed(ctx) {
		SendError(ctx, fasthttp.StatusForbidden, "managing managed clients requires an authenticated admin session")
		return false
	}
	return true
}

// storeReady writes 503 and returns false when no config store is configured.
func (h *OAuth2ManagedClientsHandler) storeReady(ctx *fasthttp.RequestCtx) bool {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store unavailable")
		return false
	}
	return true
}

// loadClient reads the {id} path client, writing 404/500 and returning false on failure.
func (h *OAuth2ManagedClientsHandler) loadClient(ctx *fasthttp.RequestCtx) (*configtables.TableOAuth2ManagedClient, bool) {
	id, _ := ctx.UserValue("id").(string)
	client, err := h.store.ConfigStore.GetOAuth2ManagedClientByID(ctx, id)
	if err != nil {
		h.sendStoreError(ctx, err, "load")
		return nil, false
	}
	return client, true
}

// sendStoreError maps ErrNotFound to 404 and everything else to a logged 500.
func (h *OAuth2ManagedClientsHandler) sendStoreError(ctx *fasthttp.RequestCtx, err error, action string) {
	if errors.Is(err, configstore.ErrNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, "managed client not found")
		return
	}
	logger.Error("oauth2 managed clients: %s failed: %v", action, err)
	SendError(ctx, fasthttp.StatusInternalServerError, "failed to "+action+" managed client")
}
