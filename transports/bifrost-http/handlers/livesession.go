package handlers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	liveCloseDrainTimeout  = 15 * time.Second
	liveStaleCheckInterval = 5 * time.Second
)

const liveAuthRefusalMessage = "authentication is required for live sessions. Provide a virtual key (x-bf-vk or an sk-bf- bearer token)."

// liveGateway admits GPT Live sessions for every transport: it resolves the session's provider and
// models, picks the one key that serves them, and opens the session's billing.
type liveGateway struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
}

// liveTarget is what a session.start (or a WebRTC create body) resolves to.
type liveTarget struct {
	provider     schemas.LiveProvider
	providerKey  schemas.ModelProvider
	voiceModel   string
	backendModel string // empty for client delegation
}

// liveAdmission is an admitted session: its context, key and open billing.
type liveAdmission struct {
	liveTarget
	ctx            *schemas.BifrostContext
	cancel         context.CancelFunc
	key            schemas.Key
	meter          *liveMeter
	checkKeyModels bool // false for a caller-supplied direct key, which carries no model lists
}

// refuseAnonymous reports the refusal for a caller that presented no credential, when auth is enforced.
func (g *liveGateway) refuseAnonymous(preReqCtx *schemas.BifrostContext) *schemas.BifrostError {
	if g.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedAnyCredential(preReqCtx) {
		return newRealtimeWireBifrostError(401, "invalid_request_error", liveAuthRefusalMessage)
	}
	return nil
}

// liveRequest is what a live route knows before it acts: the request context, credentials and
// middleware values, which outlive the recycled fasthttp ctx, and, for a route on an existing
// session, that session and its provider.
type liveRequest struct {
	path             string
	auth             *authHeaders
	preReqCtx        *schemas.BifrostContext
	cancel           context.CancelFunc
	middlewareValues map[any]any

	sessionID   string
	provider    schemas.LiveProvider
	providerKey schemas.ModelProvider
}

// prepareRequest builds the request context and refuses an anonymous caller with a plain 401
// before anything is opened on the operator's key. A refusal has been written to ctx.
func (g *liveGateway) prepareRequest(ctx *fasthttp.RequestCtx) (*liveRequest, bool) {
	path := string(ctx.Path())
	auth := captureAuthHeaders(ctx)
	preReqCtx, cancel := createBifrostContextFromAuth(g.handlerStore, auth)
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveRequest)
	if strings.HasPrefix(path, "/openai") {
		preReqCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}
	if integrations.IsAzureSDKRequest(ctx) {
		preReqCtx.SetValue(schemas.BifrostContextKeyIsAzureUserAgent, true)
	}
	if authErr := g.refuseAnonymous(preReqCtx); authErr != nil {
		cancel()
		SendBifrostError(ctx, authErr)
		return nil, false
	}
	populateRealtimeRequestContext(ctx, preReqCtx)
	return &liveRequest{path: path, auth: auth, preReqCtx: preReqCtx, cancel: cancel, middlewareValues: snapshotRealtimeMiddlewareValues(ctx)}, true
}

// resolveSession settles a route on an existing session: the session id from the path, its
// provider, and a credential that resolves. A refusal has been written to ctx and req released.
func (g *liveGateway) resolveSession(ctx *fasthttp.RequestCtx, req *liveRequest) bool {
	refuse := func(bifrostErr *schemas.BifrostError) bool {
		req.cancel()
		SendBifrostError(ctx, bifrostErr)
		return false
	}
	id, _ := ctx.UserValue("session_id").(string)
	req.sessionID = strings.TrimSpace(id)
	if req.sessionID == "" {
		return refuse(newRealtimeWireBifrostError(400, "invalid_request_error", "a session id is required"))
	}
	provider, providerKey, bifrostErr := g.controlProvider(req.path, liveControlProviderName(ctx))
	if bifrostErr != nil {
		return refuse(bifrostErr)
	}
	req.provider, req.providerKey = provider, providerKey
	g.client.RunPreRequestHooks(req.preReqCtx, &schemas.BifrostRequest{
		RequestType:      schemas.LiveRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: providerKey},
	})
	if g.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedCredentialResolved(req.preReqCtx) {
		return refuse(newRealtimeWireBifrostError(401, "invalid_request_error", realtimeUnresolvedCredentialMessage))
	}
	if bifrostErr := g.checkLiveSessionOwner(req.preReqCtx, providerKey, req.sessionID); bifrostErr != nil {
		return refuse(bifrostErr)
	}
	return true
}

// A live session is opened on the operator's provider key, which every virtual key allowed on
// the provider shares, and is afterwards addressed by the provider's session id alone. The two
// functions below make it belong to the virtual key that opened it, the way governance binds
// batches and files: the id is recorded under that key as soon as the provider names it, and a
// route on the session from another virtual key is refused as not found before a key is
// selected or anything is dialed. The rule is the same narrow one: a caller with no virtual key
// is unrestricted, and a session nobody recorded (opened before this binding existed, or with no
// virtual key) stays reachable.

// liveSessionUnrecordedMessage is what a caller is told when its session's owner cannot be
// recorded. The store's own error stays in the server log.
const liveSessionUnrecordedMessage = "the live session could not be bound to its virtual key and was not opened"

// errLiveSessionNoConfigStore is returned by bindLiveSession for a virtual-key session on a
// deployment without a config store: nothing can record its owner, and the control routes refuse
// virtual keys there, so the opener could never reach the session again.
var errLiveSessionNoConfigStore = errors.New("live sessions opened with a virtual key need a config store to bind them to the key; configure a config store or open the session without a virtual key")

// liveSessionBindError is the client-facing refusal for an error from bindLiveSession.
func liveSessionBindError(err error) *schemas.BifrostError {
	if errors.Is(err, errLiveSessionNoConfigStore) {
		return newRealtimeWireBifrostError(403, "invalid_request_error", errLiveSessionNoConfigStore.Error())
	}
	return newRealtimeWireBifrostError(500, "server_error", liveSessionUnrecordedMessage)
}

// liveSessionVirtualKeyID is the virtual key a live request is attributed to, read from its
// Grant, or empty when the caller presented no key.
func liveSessionVirtualKeyID(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	grant := ctx.Grant()
	if grant == nil {
		return ""
	}
	identity := grant.Identity()
	if identity == nil {
		return ""
	}
	virtualKey := identity.VirtualKey()
	if virtualKey == nil {
		return ""
	}
	return virtualKey.ID
}

// ownershipEnforced reports whether live sessions are restricted to the virtual key that opened
// them (client.enforce_vk_object_ownership, on by default). It reads the live client config, so a
// saved change applies to the next route.
func (g *liveGateway) ownershipEnforced() bool {
	return g.config == nil || g.config.ShouldEnforceVKObjectOwnership()
}

// bindLiveSession records vkID, the virtual key that opened the provider session, if any, trying
// once more when the write fails. A record that still cannot be written is returned: the caller
// ends the session rather than hand out an id nobody is recorded for, which every key on the
// provider could then reach. This holds while client.enforce_vk_object_ownership is off too, so
// turning it on later binds every session opened in the meantime.
func (g *liveGateway) bindLiveSession(ctx *schemas.BifrostContext, vkID string, providerKey schemas.ModelProvider, providerSessionID string) error {
	if vkID == "" {
		return nil
	}
	if g.config == nil || g.config.ConfigStore == nil {
		return errLiveSessionNoConfigStore
	}
	object := &tables.TableProviderObject{
		ID:           tables.ProviderObjectID(tables.ProviderObjectKindLiveSession, string(providerKey), providerSessionID),
		Kind:         tables.ProviderObjectKindLiveSession,
		Provider:     string(providerKey),
		ObjectID:     providerSessionID,
		VirtualKeyID: vkID,
	}
	err := g.config.ConfigStore.UpsertProviderObject(ctx, object)
	if err != nil {
		err = g.config.ConfigStore.UpsertProviderObject(ctx, object)
	}
	if err != nil {
		logger.Error("live session %s: failed to record the virtual key that opened it (after a retry): %v", providerSessionID, err)
		return err
	}
	return nil
}

// checkLiveSessionOwner refuses a route on a session that another virtual key opened. Without a
// config store nothing is recorded anywhere, so a virtual-key request cannot be checked and is
// refused, as governance refuses bound provider objects; a store failure is refused too rather
// than let an unverified request through. Nothing is refused while client.enforce_vk_object_ownership
// is turned off; bindLiveSession still records the opener so turning it back on covers the session.
//
// The session is looked up by its id under every provider name it was recorded under, not only
// the one the route resolved to: the bare control route prefers openai, while a session opened on
// a custom provider built on OpenAI's API is recorded under that provider's name, and both names
// reach the same upstream session by the same id. The record is found even after the provider that
// opened the session is removed, since another provider may still reach that session.
func (g *liveGateway) checkLiveSessionOwner(ctx *schemas.BifrostContext, providerKey schemas.ModelProvider, sessionID string) *schemas.BifrostError {
	vkID := liveSessionVirtualKeyID(ctx)
	if vkID == "" {
		return nil
	}
	if !g.ownershipEnforced() {
		return nil
	}
	if g.config == nil || g.config.ConfigStore == nil {
		return newRealtimeWireBifrostError(403, "invalid_request_error", "live session access cannot be verified for virtual keys without a config store")
	}
	objects, err := g.config.ConfigStore.GetProviderObjectsByObjectID(ctx, tables.ProviderObjectKindLiveSession, sessionID)
	if err != nil {
		logger.Error("live session %s: failed to load its virtual key for the ownership check: %v", sessionID, err)
		return newRealtimeWireBifrostError(500, "server_error", "failed to verify access to the live session")
	}
	for _, object := range objects {
		if object != nil && object.VirtualKeyID != "" && object.VirtualKeyID != vkID {
			return newRealtimeWireBifrostError(404, "invalid_request_error", "live session not found")
		}
	}
	return nil
}

// resolveTarget runs the pre-request pipeline (credential resolution, routing) and resolves the
// session's provider and models. Nothing is spent yet: no key is selected and no upstream opened.
// A model without a provider prefix runs on the provider the session's later routes default to,
// so a control route by id finds it: Azure for the Azure OpenAI SDK, else the route's own, OpenAI
// on /openai, as file and batch creates resolve it.
func (g *liveGateway) resolveTarget(preReqCtx *schemas.BifrostContext, path string, start *schemas.LiveSession) (liveTarget, *schemas.BifrostError) {
	defaultProvider := realtimeDefaultProviderForPath(path)
	if azureSDK, _ := preReqCtx.Value(schemas.BifrostContextKeyIsAzureUserAgent).(bool); azureSDK {
		defaultProvider = schemas.Azure
	}
	providerKey, voiceModel := schemas.ParseModelString(start.Model, defaultProvider)
	preReq := &schemas.BifrostRequest{
		RequestType:      schemas.LiveRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: providerKey, Model: voiceModel},
	}
	g.client.RunPreRequestHooks(preReqCtx, preReq)
	if g.config.ClientConfig.EnforceAuthOnInference && !governance.PresentedCredentialResolved(preReqCtx) {
		return liveTarget{}, newRealtimeWireBifrostError(401, "invalid_request_error", realtimeUnresolvedCredentialMessage)
	}
	providerKey, voiceModel, _ = preReq.GetRequestFields()
	if providerKey == "" || strings.TrimSpace(voiceModel) == "" {
		return liveTarget{}, newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("no provider could be resolved for model %q (set as provider/model or configure the model catalog)", start.Model))
	}
	backendModel, err := liveBackendModel(start, providerKey)
	if err != nil {
		return liveTarget{}, newRealtimeWireBifrostError(400, "invalid_request_error", err.Error())
	}
	liveProvider, ok := g.client.GetProviderByKey(providerKey).(schemas.LiveProvider)
	if !ok {
		return liveTarget{}, newRealtimeWireBifrostError(400, "invalid_request_error", "provider does not support live sessions: "+string(providerKey))
	}
	return liveTarget{provider: liveProvider, providerKey: providerKey, voiceModel: voiceModel, backendModel: backendModel}, nil
}

// admit builds the session context, opens the session's billing and selects the one key that
// serves both models (OpenAI calls the backend on it too). Governance admits the units first: it
// publishes the keys the session may use, so the key is chosen among them, never outside them.
// A virtual-key session on a deployment without a config store is refused here, before billing
// opens or the provider is called, since nothing could bind it to its key. The caller owns the
// returned cancel.
func (g *liveGateway) admit(auth *authHeaders, preReqCtx *schemas.BifrostContext, middlewareValues map[any]any, path string, target liveTarget, sessionID string) (*liveAdmission, *schemas.BifrostError) {
	// The opener is the key the pre-request pipeline resolved for the caller, the same context the
	// owner check on a later route reads; the session context rebuilds the identity from the
	// presented credential and does not carry that resolution.
	opener := liveSessionVirtualKeyID(preReqCtx)
	if opener != "" && (g.config == nil || g.config.ConfigStore == nil) {
		return nil, liveSessionBindError(errLiveSessionNoConfigStore)
	}
	ctx, cancel := g.sessionContext(auth, preReqCtx, middlewareValues, path)
	meter := newLiveMeter(g.client, ctx, target.providerKey, schemas.Key{}, sessionID)
	meter.bind = func(providerSessionID string) error {
		return g.bindLiveSession(ctx, opener, target.providerKey, providerSessionID)
	}
	if bifrostErr := meter.admit(target.voiceModel, target.backendModel); bifrostErr != nil {
		cancel()
		return nil, bifrostErr
	}
	key, err := g.client.SelectKeyForProviderRequestType(meter.keySelectionContext(), schemas.LiveRequest, target.providerKey, target.voiceModel, target.backendModel)
	if err != nil {
		bifrostErr := newRealtimeWireBifrostError(400, "invalid_request_error", err.Error())
		meter.abort(bifrostErr)
		cancel()
		return nil, bifrostErr
	}
	meter.setKey(key)
	_, isDirectKey := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	return &liveAdmission{liveTarget: target, ctx: ctx, cancel: cancel, key: key, meter: meter, checkKeyModels: !isDirectKey}, nil
}

// sessionContext builds the context a session (or a sideband on it) runs under: the caller's
// identity as the pre-request pipeline settled it. The caller owns the returned cancel.
func (g *liveGateway) sessionContext(auth *authHeaders, preReqCtx *schemas.BifrostContext, middlewareValues map[any]any, path string) (*schemas.BifrostContext, context.CancelFunc) {
	ctx, cancel := createBifrostContextFromAuth(g.handlerStore, auth)
	applyRealtimeMiddlewareValues(ctx, liveMiddlewareValues(middlewareValues, preReqCtx))
	lib.SettleIdentity(ctx)
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveRequest)
	if strings.HasPrefix(path, "/openai") {
		ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}
	return ctx, cancel
}

// liveControlProviderName is the provider a caller names on a route that only carries a session
// id: the ?provider= query parameter, else the x-model-provider header, else azure for the Azure
// OpenAI SDK, as the file, batch and video routes accept.
func liveControlProviderName(ctx *fasthttp.RequestCtx) string {
	if named := strings.TrimSpace(string(ctx.QueryArgs().Peek("provider"))); named != "" {
		return named
	}
	if named := strings.TrimSpace(string(ctx.Request.Header.Peek("x-model-provider"))); named != "" {
		return named
	}
	if integrations.IsAzureSDKRequest(ctx) {
		return string(schemas.Azure)
	}
	return ""
}

// controlProvider resolves the provider a route on an existing session names: the provider the
// caller named, else the integration prefix, else, on the bare route, the standard OpenAI
// provider when configured, else the one configured provider that serves live sessions. Custom
// OpenAI-based providers serve live sessions too, so the bare route cannot be left to a scan alone.
func (g *liveGateway) controlProvider(path, named string) (schemas.LiveProvider, schemas.ModelProvider, *schemas.BifrostError) {
	providerKey := schemas.ModelProvider(named)
	if providerKey != "" && g.client.GetProviderByKey(providerKey) == nil {
		return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("provider %q is not configured", named))
	}
	if providerKey == "" {
		providerKey = realtimeDefaultProviderForPath(path)
	}
	if providerKey == "" {
		configured, err := g.client.GetConfiguredProviders()
		if err != nil {
			return nil, "", newRealtimeWireBifrostError(500, "server_error", "failed to list providers: "+err.Error())
		}
		var candidates []schemas.ModelProvider
		for _, candidate := range configured {
			if _, ok := g.client.GetProviderByKey(candidate).(schemas.LiveProvider); ok {
				candidates = append(candidates, candidate)
			}
		}
		switch {
		case slices.Contains(candidates, schemas.OpenAI):
			providerKey = schemas.OpenAI
		case len(candidates) == 1:
			providerKey = candidates[0]
		case len(candidates) == 0:
			return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", "no configured provider serves live sessions")
		default:
			return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", "several providers serve live sessions; name one with ?provider= or the x-model-provider header")
		}
	}
	provider, ok := g.client.GetProviderByKey(providerKey).(schemas.LiveProvider)
	if !ok {
		return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", "provider does not support live sessions: "+string(providerKey))
	}
	return provider, providerKey, nil
}

// admitSideband opens a sideband as a request of its own through the plugin pipeline. Governance
// holds it to the caller's virtual key as the primary session is held: the provider must be
// allowed, and the keys the caller may use are published for selection. Budgets and request
// limits are skipped: a sideband spends nothing, and the session it joins is what they govern, so
// an exhausted key keeps control of the session it already has. Logging gives it a row naming the
// provider session. The caller settles the row with closeSideband.
func (g *liveGateway) admitSideband(ctx *schemas.BifrostContext, providerKey schemas.ModelProvider, sessionID string) (*bifrost.RealtimeTurnHooks, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveSidebandRequest)
	ctx.SetValue(schemas.BifrostContextKeyRealtimeProviderSessionID, sessionID)
	ctx.SetValue(schemas.BifrostContextKeySkipBudgetAndRateLimits, true)
	// The sideband outlives the attach request: its row is delivered when its own trace completes,
	// so it must not inherit the transport's, as live units and realtime turns do not.
	ctx.ClearValue(schemas.BifrostContextKeyTraceID)
	ctx.ClearValue(schemas.BifrostContextKeyExportTraceID)
	setRealtimeTurnStreamContext(ctx, time.Now(), false)
	return g.client.RunRealtimeTurnPreHooks(ctx, &schemas.BifrostRequest{
		RequestType:      schemas.LiveSidebandRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: providerKey},
	})
}

// closeSideband settles a sideband's row: the key it dialed with and how long it stayed
// attached, or why it never was.
func closeSideband(ctx *schemas.BifrostContext, hooks *bifrost.RealtimeTurnHooks, providerKey schemas.ModelProvider, key schemas.Key, startedAt time.Time, bifrostErr *schemas.BifrostError) {
	if hooks == nil {
		return
	}
	defer func() {
		if hooks.Cleanup != nil {
			hooks.Cleanup()
		}
	}()
	if hooks.PostHookRunner == nil {
		return
	}
	setRealtimeTurnStreamContext(ctx, startedAt, true)
	// The key is chosen after the pre-hooks ran; the row names it, as a unit's does.
	if strings.TrimSpace(key.ID) != "" {
		ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, key.ID)
	}
	if strings.TrimSpace(key.Name) != "" {
		ctx.SetValue(schemas.BifrostContextKeySelectedKeyName, key.Name)
	}
	// The row is delivered when the sideband's trace completes, as a turn's is.
	defer completeRealtimeTurnTrace(ctx)
	if bifrostErr != nil {
		postErr := *bifrostErr
		postErr.ExtraFields.RequestType = schemas.LiveSidebandRequest
		postErr.ExtraFields.Provider = providerKey
		hooks.PostHookRunner(ctx, nil, &postErr)
		return
	}
	hooks.PostHookRunner(ctx, &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{
		Object:      "live.sideband",
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.LiveSidebandRequest, Provider: providerKey, Latency: time.Since(startedAt).Milliseconds()},
	}}, nil)
}

// controlKey selects the key a route on an existing session acts with. The session's virtual
// key was checked by resolveSession; the provider key is ordinary selection, the caller's pinned
// key or any key for the provider, and the provider refuses a key that does not own the session.
func (g *liveGateway) controlKey(ctx *schemas.BifrostContext, providerKey schemas.ModelProvider) (schemas.Key, *schemas.BifrostError) {
	key, err := g.client.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, providerKey, "")
	if err != nil {
		return schemas.Key{}, newRealtimeWireBifrostError(400, "invalid_request_error", err.Error())
	}
	return key, nil
}

// controller returns the session controller for this admission, driven by a transport through io.
func (a *liveAdmission) controller(g *liveGateway, io liveSessionIO) *liveSessionController {
	return &liveSessionController{
		liveUpdatePolicy: liveUpdatePolicy{models: g.client, provider: a.providerKey, key: a.key, checkKeyModels: a.checkKeyModels},
		io:               io,
		meter:            a.meter,
		drainTimeout:     liveCloseDrainTimeout,
		staleCheckEvery:  liveStaleCheckInterval,
		upstreamDone:     make(chan struct{}),
	}
}

// liveBackendModel returns the Responses backend model, which must be on the session's provider.
// It is empty for client delegation, where the app calls its backend itself.
func liveBackendModel(start *schemas.LiveSession, providerKey schemas.ModelProvider) (string, error) {
	if start.Delegation == nil || start.Delegation.Type != schemas.LiveDelegationResponses || start.Delegation.Responses == nil {
		return "", nil
	}
	backendProvider, model := schemas.ParseModelString(start.Delegation.Responses.Model, providerKey)
	if strings.TrimSpace(model) == "" {
		return "", errors.New("session.delegation.responses.model is required for responses delegation")
	}
	if backendProvider != providerKey {
		return "", fmt.Errorf("session.delegation.responses.model must be served by %s, the provider of session.model", providerKey)
	}
	return model, nil
}

// rewriteLiveModels sends OpenAI the bare, alias-resolved model names; other bytes stay as sent.
// It serves both the session.start frame and the WebRTC create body, which share these paths.
func rewriteLiveModels(payload []byte, start *schemas.LiveSession, key schemas.Key, voiceModel, backendModel string) ([]byte, error) {
	payload, err := setLiveModel(payload, "session.model", start.Model, key.Aliases.Resolve(voiceModel))
	if err != nil || backendModel == "" {
		return payload, err
	}
	return setLiveModel(payload, "session.delegation.responses.model", start.Delegation.Responses.Model, key.Aliases.Resolve(backendModel))
}

func setLiveModel(payload []byte, path, sent, wire string) ([]byte, error) {
	if sent == wire {
		return payload, nil
	}
	encoded, err := schemas.Marshal(wire)
	if err != nil {
		return nil, err
	}
	return providerUtils.SetRawJSONField(payload, path, encoded)
}

// liveMiddlewareValues adds what the pre-request pipeline resolved (routing, identity) to the
// values captured from the transport middleware.
func liveMiddlewareValues(snapshot map[any]any, preReqCtx *schemas.BifrostContext) map[any]any {
	values := make(map[any]any, len(snapshot))
	maps.Copy(values, snapshot)
	for _, k := range realtimeMiddlewareKeys {
		if v := preReqCtx.Value(k); v != nil {
			values[k] = v
		}
	}
	return mergeRealtimeTurnValues(values, preReqCtx)
}

// liveModelChecker is the slice of *bifrost.Bifrost the controller needs; tests substitute it.
type liveModelChecker interface {
	KeySupportsModel(providerKey schemas.ModelProvider, key schemas.Key, model string) bool
}

// liveSessionIO is how a transport carries the controller's writes.
type liveSessionIO interface {
	sendUpstream(message []byte) error
	sendClient(message []byte) error
	// abandonUpstream drops the upstream connection when session.closed never arrives.
	abandonUpstream()
}

// liveUpdatePolicy checks a session.update against the session's key before it reaches the
// provider. The primary and every sideband of a session apply it.
type liveUpdatePolicy struct {
	models         liveModelChecker
	provider       schemas.ModelProvider
	key            schemas.Key
	checkKeyModels bool
}

// check returns the frame to forward with model names resolved, and the backend model it
// switches to, if any.
func (p liveUpdatePolicy) check(message []byte) ([]byte, string, *schemas.BifrostError) {
	sent := providerUtils.GetJSONField(message, "session.delegation.responses.model").Str
	if strings.TrimSpace(sent) == "" {
		return message, "", nil
	}
	provider, model := schemas.ParseModelString(sent, p.provider)
	if provider != p.provider {
		return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("session.delegation.responses.model must be served by %s, the provider of session.model", p.provider))
	}
	if p.checkKeyModels && !p.models.KeySupportsModel(p.provider, p.key, model) {
		return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", fmt.Sprintf("the key serving this session does not support model %s", model))
	}
	message, err := setLiveModel(message, "session.delegation.responses.model", sent, p.key.Aliases.Resolve(model))
	if err != nil {
		return nil, "", newRealtimeWireBifrostError(500, "server_error", "failed to prepare session.update: "+err.Error())
	}
	return message, model, nil
}

// liveSessionController is a live session's transport-neutral policy: update checks, metering,
// budget refusal, and draining until session.closed.
type liveSessionController struct {
	liveUpdatePolicy
	io              liveSessionIO
	meter           *liveMeter
	drainTimeout    time.Duration
	staleCheckEvery time.Duration

	upstreamMu       sync.Mutex // serializes provider events with finalization
	upstreamDone     chan struct{}
	upstreamDoneOnce sync.Once
	clientGone       atomic.Bool
	sessionClosed    atomic.Bool // the provider sent session.closed
	startFailed      atomic.Bool // the provider refused to create the session
	withholdUpstream atomic.Bool // the session's owner could not be recorded; nothing more reaches the client
	closeOnce        sync.Once
	refusalOnce      sync.Once
	drainMu          sync.Mutex
	drainTimer       *time.Timer
	transcript       liveTranscript // guarded by upstreamMu
	startedAt        time.Time      // guarded by upstreamMu; session timeline origin

	delegationMu    sync.Mutex
	delegations     map[string]*liveDelegationRecord // a delegation's output so far, until its terminal event
	callDelegations map[string]string                // function call id → delegation, until the app answers it
}

// liveDelegationRecord is what the relay has seen of one delegation since it was created.
type liveDelegationRecord struct {
	startedMs int64
	items     []schemas.ResponsesMessage
}

const (
	// liveDelegationMaxItems bounds what is kept of one delegation's output before it completes.
	liveDelegationMaxItems = 32
	// liveDelegationMaxCalls bounds the function calls remembered while the app runs them.
	liveDelegationMaxCalls = 64
)

// sessionClockMs is the session timeline now, in ms since session.started.
func (c *liveSessionController) sessionClockMs() int64 {
	if c.startedAt.IsZero() {
		return 0
	}
	return time.Since(c.startedAt).Milliseconds()
}

func (c *liveSessionController) delegationLocked(id string) *liveDelegationRecord {
	if c.delegations == nil {
		c.delegations = make(map[string]*liveDelegationRecord)
	}
	record := c.delegations[id]
	if record == nil {
		record = &liveDelegationRecord{}
		c.delegations[id] = record
	}
	return record
}

// noteDelegationCreated stamps a delegation with its place on the session timeline.
func (c *liveSessionController) noteDelegationCreated(message []byte) {
	id := providerUtils.GetJSONField(message, "delegation.id").Str
	if id == "" {
		return
	}
	c.delegationMu.Lock()
	defer c.delegationMu.Unlock()
	if record := c.delegationLocked(id); record.startedMs == 0 {
		record.startedMs = c.sessionClockMs()
	}
}

// recordDelegationItem keeps a finished output item (a message, a tool call) for the delegation's
// log entry; reasoning items carry nothing readable and are dropped.
func (c *liveSessionController) recordDelegationItem(message []byte) {
	event, err := schemas.ParseLiveEvent(message)
	if err != nil || event.DelegationID == nil || event.Event == nil || event.Event.Response == nil || event.Event.Response.Item == nil {
		return
	}
	item := event.Event.Response.Item
	if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeReasoning {
		return
	}
	c.delegationMu.Lock()
	defer c.delegationMu.Unlock()
	if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall && item.ResponsesToolMessage != nil && item.ResponsesToolMessage.CallID != nil {
		if c.callDelegations == nil {
			c.callDelegations = make(map[string]string)
		}
		if len(c.callDelegations) < liveDelegationMaxCalls {
			c.callDelegations[*item.ResponsesToolMessage.CallID] = *event.DelegationID
		}
	}
	c.appendDelegationItemLocked(*event.DelegationID, *item)
}

func (c *liveSessionController) appendDelegationItemLocked(id string, item schemas.ResponsesMessage) {
	record := c.delegationLocked(id)
	if len(record.items) >= liveDelegationMaxItems {
		return
	}
	record.items = append(record.items, item)
}

// recordFunctionOutput keeps the result the app sends for a function call, so the delegation's
// log shows the call, its result and the answer that followed.
func (c *liveSessionController) recordFunctionOutput(message []byte) {
	if providerUtils.GetJSONField(message, "item.type").Str != string(schemas.ResponsesMessageTypeFunctionCallOutput) {
		return
	}
	var item schemas.ResponsesMessage
	if err := schemas.Unmarshal([]byte(providerUtils.GetJSONField(message, "item").Raw), &item); err != nil || item.ResponsesToolMessage == nil || item.ResponsesToolMessage.CallID == nil {
		return
	}
	c.delegationMu.Lock()
	defer c.delegationMu.Unlock()
	id, ok := c.callDelegations[*item.ResponsesToolMessage.CallID]
	if !ok {
		return
	}
	delete(c.callDelegations, *item.ResponsesToolMessage.CallID)
	c.appendDelegationItemLocked(id, item)
}

// takeDelegation returns what was seen of a delegation and forgets it; its terminal event has
// arrived. A later response of the same delegation starts a new record.
func (c *liveSessionController) takeDelegation(id string) ([]schemas.ResponsesMessage, int64) {
	c.delegationMu.Lock()
	defer c.delegationMu.Unlock()
	record := c.delegations[id]
	if record == nil {
		return nil, 0
	}
	delete(c.delegations, id)
	return record.items, record.startedMs
}

// fromClient returns the client frame to forward upstream, or false when it was refused (the
// client has been told why) and must be dropped.
func (c *liveSessionController) fromClient(message []byte) ([]byte, bool) {
	switch schemas.LiveEventTypeOf(message) {
	case schemas.LiveEventSessionUpdate:
		checked, bifrostErr := c.admitSessionUpdate(message)
		if bifrostErr != nil {
			c.sendError(bifrostErr)
			return nil, false
		}
		return checked, true
	case schemas.LiveEventSessionClose:
		c.closeOnce.Do(c.startDrainTimer)
	case schemas.LiveEventResponseItemCreate:
		c.recordFunctionOutput(message)
	}
	return message, true
}

// fromUpstream meters an upstream control event and reports whether the session has ended. The
// frame itself is forwarded unchanged by the transport. WebRTC close callbacks run independently
// of provider messages, so processing an event and finalizing the session share one lock.
func (c *liveSessionController) fromUpstream(message []byte) bool {
	c.upstreamMu.Lock()
	defer c.upstreamMu.Unlock()
	select {
	case <-c.upstreamDone:
		return true
	default:
	}
	switch schemas.LiveEventTypeOf(message) {
	case schemas.LiveEventSessionStarted:
		c.startedAt = time.Now()
		if err := c.meter.setProviderSessionID(providerUtils.GetJSONField(message, "session.id").Str); err != nil {
			// The id is never forwarded: this frame and every later one are withheld, the client is
			// told the session is ending, and the provider is asked to close it so what was spent is
			// still reported and billed.
			c.withholdUpstream.Store(true)
			c.endForRefusal(liveSessionBindError(err))
		}
	case schemas.LiveEventDelegationCreated:
		c.noteDelegationCreated(message)
	case schemas.LiveEventSessionUpdated:
		// A sideband can switch the backend too; the primary bills whatever the session now runs.
		if model := providerUtils.GetJSONField(message, "session.delegation.responses.model").Str; model != "" {
			if refusal := c.meter.confirmBackend(model); refusal != nil {
				c.endForRefusal(refusal)
			}
		}
	case schemas.LiveEventInputTranscriptDelta:
		c.appendTranscript("user", message)
	case schemas.LiveEventOutputTranscriptDelta:
		c.appendTranscript("assistant", message)
	case schemas.LiveEventUsageUpdated:
		if refusal := c.meter.onUsage(providerUtils.GetJSONField(message, "usage.seconds").Float()); refusal != nil {
			c.endForRefusal(refusal)
		}
	case schemas.LiveEventResponseEvent:
		switch schemas.ResponsesStreamResponseType(providerUtils.GetJSONField(message, "event.type").Str) {
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			c.recordDelegationItem(message)
		case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesStreamResponseTypeFailed:
			event, err := schemas.ParseLiveEvent(message)
			if err != nil || event.Event == nil || event.Event.Response == nil || event.Event.Response.Response == nil {
				logger.Warn("live session: failed to read backend usage from response.event: %v", err)
				return false
			}
			response := event.Event.Response.Response
			var delegationID string
			var startedMs int64
			if event.DelegationID != nil {
				delegationID = *event.DelegationID
				var items []schemas.ResponsesMessage
				items, startedMs = c.takeDelegation(delegationID)
				// The terminal event carries no output on the live socket; the items arrived one by one.
				if len(response.Output) == 0 {
					response.Output = items
				}
			}
			if refusal := c.meter.onBackendResponse(response, delegationID, startedMs); refusal != nil {
				c.endForRefusal(refusal)
			}
		}
	case schemas.LiveEventError:
		// An error before the provider created the session is a startup error: the session never ran.
		if c.meter.failStart(liveUpstreamError(message)) {
			c.startFailed.Store(true)
			return false
		}
		// A delegation the provider refuses to run never opens; the session goes on without it.
		if bifrostErr := delegationError(message); bifrostErr != nil {
			if refusal := c.meter.failBackend(bifrostErr); refusal != nil {
				c.endForRefusal(refusal)
			}
		}
	case schemas.LiveEventSessionClosed:
		c.sessionClosed.Store(true)
		c.meter.setEnding(c.transcript.snapshot())
		c.meter.finish(providerUtils.GetJSONField(message, "usage.seconds").Float())
		c.markUpstreamDone()
		return true
	}
	return false
}

// delegationError reads an upstream error frame about the session's delegation; nil for any other.
func delegationError(message []byte) *schemas.BifrostError {
	if !strings.HasPrefix(providerUtils.GetJSONField(message, "error.param").Str, "session.delegation") {
		return nil
	}
	return liveUpstreamError(message)
}

// liveUpstreamError reads an upstream error frame as a Bifrost error.
func liveUpstreamError(message []byte) *schemas.BifrostError {
	code := providerUtils.GetJSONField(message, "error.code").Str
	if code == "" {
		code = "invalid_request_error"
	}
	return newRealtimeWireBifrostError(400, code, providerUtils.GetJSONField(message, "error.message").Str)
}

// appendTranscript records one transcript fragment with its place on the session timeline.
func (c *liveSessionController) appendTranscript(role string, message []byte) {
	c.transcript.append(role, providerUtils.GetJSONField(message, "delta").Str, providerUtils.GetJSONField(message, "start_ms").Int(), providerUtils.GetJSONField(message, "end_ms").Int())
}

// forwardToClient sends an upstream frame to the client; a client that cannot be reached is
// treated as gone.
func (c *liveSessionController) forwardToClient(message []byte) {
	if c.clientGone.Load() || c.withholdUpstream.Load() {
		return
	}
	if err := c.io.sendClient(message); err != nil {
		c.clientLeft()
	}
}

// clientLeft closes the session upstream so its final usage still arrives and is billed.
func (c *liveSessionController) clientLeft() {
	c.clientGone.Store(true)
	c.requestClose()
}

// upstreamEnded bills what OpenAI last reported when the upstream ended without session.closed.
func (c *liveSessionController) upstreamEnded() {
	c.upstreamMu.Lock()
	defer c.upstreamMu.Unlock()
	select {
	case <-c.upstreamDone:
		return
	default:
	}
	// The watcher is released even if billing panics: the session is over either way, and a
	// watcher left behind would outlive it.
	defer c.markUpstreamDone()
	c.meter.setEnding(c.transcript.snapshot())
	c.meter.finish(c.meter.lastReportedSeconds())
}

// upstreamEndedEarly is upstreamEnded for an upstream that ended without session.closed: a
// client still connected is told, since nothing else will tell it. Reports whether it was.
func (c *liveSessionController) upstreamEndedEarly() bool {
	c.upstreamEnded()
	if c.clientGone.Load() || c.sessionClosed.Load() || c.startFailed.Load() {
		return false
	}
	c.sendError(newRealtimeWireBifrostError(502, "server_error", "the live session's upstream connection ended before session.closed"))
	return true
}

func (c *liveSessionController) markUpstreamDone() {
	c.upstreamDoneOnce.Do(func() {
		close(c.upstreamDone)
		c.stopDrainTimer()
	})
}

// admitSessionUpdate re-checks a changed backend model before it reaches OpenAI: the session's key
// must serve it and governance must admit it.
func (c *liveSessionController) admitSessionUpdate(message []byte) ([]byte, *schemas.BifrostError) {
	checked, model, bifrostErr := c.check(message)
	if bifrostErr != nil || model == "" {
		return checked, bifrostErr
	}
	if bifrostErr := c.meter.switchBackend(model); bifrostErr != nil {
		return nil, bifrostErr
	}
	return checked, nil
}

func (c *liveSessionController) sendError(bifrostErr *schemas.BifrostError) {
	if c.clientGone.Load() {
		return
	}
	_ = c.io.sendClient(newRealtimeTurnErrorEventPayload(bifrostErr))
}

// endForRefusal tells the client why the session is ending, then closes it gracefully so the
// usage already spent is still reported and billed.
func (c *liveSessionController) endForRefusal(refusal *schemas.BifrostError) {
	c.refusalOnce.Do(func() {
		c.sendError(refusal)
		c.requestClose()
	})
}

// requestClose sends session.close upstream once and bounds the wait for session.closed.
func (c *liveSessionController) requestClose() {
	c.closeOnce.Do(func() {
		select {
		case <-c.upstreamDone:
			return
		default:
		}
		_ = c.io.sendUpstream([]byte(`{"type":"session.close"}`))
		c.startDrainTimer()
	})
}

func (c *liveSessionController) startDrainTimer() {
	c.drainMu.Lock()
	defer c.drainMu.Unlock()
	if c.drainTimer == nil {
		c.drainTimer = time.AfterFunc(c.drainTimeout, c.io.abandonUpstream)
	}
}

func (c *liveSessionController) stopDrainTimer() {
	c.drainMu.Lock()
	defer c.drainMu.Unlock()
	if c.drainTimer != nil {
		c.drainTimer.Stop()
	}
}

// watchStale drives the meter's wall-time budget check until the session ends.
func (c *liveSessionController) watchStale() {
	ticker := time.NewTicker(c.staleCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-c.upstreamDone:
			return
		case now := <-ticker.C:
			if refusal := c.meter.checkStale(now); refusal != nil {
				c.endForRefusal(refusal)
			}
		}
	}
}
