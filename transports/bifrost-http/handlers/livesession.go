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
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
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

// resolveTarget runs the pre-request pipeline (credential resolution, routing) and resolves the
// session's provider and models. Nothing is spent yet: no key is selected and no upstream opened.
func (g *liveGateway) resolveTarget(preReqCtx *schemas.BifrostContext, path string, start *schemas.LiveSession) (liveTarget, *schemas.BifrostError) {
	providerKey, voiceModel := schemas.ParseModelString(start.Model, realtimeDefaultProviderForPath(path))
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

// admit builds the session context, selects the one key that serves both models (OpenAI calls
// the backend on it too) and opens the session's billing. The caller owns the returned cancel.
func (g *liveGateway) admit(auth *authHeaders, preReqCtx *schemas.BifrostContext, middlewareValues map[any]any, path string, target liveTarget, sessionID string) (*liveAdmission, *schemas.BifrostError) {
	ctx, cancel := g.sessionContext(auth, preReqCtx, middlewareValues, path)
	key, err := g.client.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, target.providerKey, target.voiceModel, target.backendModel)
	if err != nil {
		cancel()
		return nil, newRealtimeWireBifrostError(400, "invalid_request_error", err.Error())
	}
	meter := newLiveMeter(g.client, ctx, target.providerKey, key, sessionID)
	if bifrostErr := meter.admit(target.voiceModel, target.backendModel); bifrostErr != nil {
		cancel()
		return nil, bifrostErr
	}
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

// controlProvider resolves the provider a route on an existing session names: the integration
// prefix, or, on the bare route, the standard OpenAI provider when configured, else the one
// configured provider that serves live sessions. Custom OpenAI-based providers serve live
// sessions too, so the bare route cannot be left to a scan alone.
func (g *liveGateway) controlProvider(path string) (schemas.LiveProvider, schemas.ModelProvider, *schemas.BifrostError) {
	providerKey := realtimeDefaultProviderForPath(path)
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
			return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", "several providers serve live sessions; use the provider's route, such as /openai/v1/live/sessions/{session_id}/...")
		}
	}
	provider, ok := g.client.GetProviderByKey(providerKey).(schemas.LiveProvider)
	if !ok {
		return nil, "", newRealtimeWireBifrostError(400, "invalid_request_error", "provider does not support live sessions: "+string(providerKey))
	}
	return provider, providerKey, nil
}

// controlKey selects the key a route on an existing session acts with. Sessions are not yet
// bound to the key that opened them, so this is ordinary selection: the caller's pinned key, or
// any key for the provider; the provider refuses a key that does not own the session.
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
	return values
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

	upstreamDone     chan struct{}
	upstreamDoneOnce sync.Once
	clientGone       atomic.Bool
	closeOnce        sync.Once
	refusalOnce      sync.Once
	drainMu          sync.Mutex
	drainTimer       *time.Timer
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
	}
	return message, true
}

// fromUpstream meters an upstream control event and reports whether it ended the session. The
// frame itself is forwarded unchanged by the transport.
func (c *liveSessionController) fromUpstream(message []byte) bool {
	switch schemas.LiveEventTypeOf(message) {
	case schemas.LiveEventSessionStarted:
		c.meter.setProviderSessionID(providerUtils.GetJSONField(message, "session.id").Str)
	case schemas.LiveEventSessionUpdated:
		// A sideband can switch the backend too; the primary bills whatever the session now runs.
		if model := providerUtils.GetJSONField(message, "session.delegation.responses.model").Str; model != "" {
			if refusal := c.meter.switchBackend(model); refusal != nil {
				c.endForRefusal(refusal)
			}
		}
	case schemas.LiveEventUsageUpdated:
		if refusal := c.meter.onUsage(providerUtils.GetJSONField(message, "usage.seconds").Float()); refusal != nil {
			c.endForRefusal(refusal)
		}
	case schemas.LiveEventResponseEvent:
		switch schemas.ResponsesStreamResponseType(providerUtils.GetJSONField(message, "event.type").Str) {
		case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesStreamResponseTypeFailed:
			event, err := schemas.ParseLiveEvent(message)
			if err != nil || event.Event == nil || event.Event.Response == nil {
				logger.Warn("live session: failed to read backend usage from response.event: %v", err)
				return false
			}
			if refusal := c.meter.onBackendResponse(event.Event.Response.Response); refusal != nil {
				c.endForRefusal(refusal)
			}
		}
	case schemas.LiveEventSessionClosed:
		c.meter.finish(providerUtils.GetJSONField(message, "usage.seconds").Float())
		c.markUpstreamDone()
		return true
	}
	return false
}

// forwardToClient sends an upstream frame to the client; a client that cannot be reached is
// treated as gone.
func (c *liveSessionController) forwardToClient(message []byte) {
	if c.clientGone.Load() {
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
	c.meter.finish(c.meter.lastReportedSeconds())
	c.markUpstreamDone()
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
