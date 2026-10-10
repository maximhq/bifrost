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
	provider, providerKey, bifrostErr := g.controlProvider(req.path)
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
	return true
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

// admit builds the session context, opens the session's billing and selects the one key that
// serves both models (OpenAI calls the backend on it too). Governance admits the units first: it
// publishes the keys the session may use, so the key is chosen among them, never outside them.
// The caller owns the returned cancel.
func (g *liveGateway) admit(auth *authHeaders, preReqCtx *schemas.BifrostContext, middlewareValues map[any]any, path string, target liveTarget, sessionID string) (*liveAdmission, *schemas.BifrostError) {
	ctx, cancel := g.sessionContext(auth, preReqCtx, middlewareValues, path)
	meter := newLiveMeter(g.client, ctx, target.providerKey, schemas.Key{}, sessionID)
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
	transcript       liveTranscript // written on the upstream pump only
	startedAt        time.Time      // when session.started arrived: zero of the session timeline

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

// fromUpstream meters an upstream control event and reports whether it ended the session. The
// frame itself is forwarded unchanged by the transport.
func (c *liveSessionController) fromUpstream(message []byte) bool {
	switch schemas.LiveEventTypeOf(message) {
	case schemas.LiveEventSessionStarted:
		c.startedAt = time.Now()
		c.meter.setProviderSessionID(providerUtils.GetJSONField(message, "session.id").Str)
	case schemas.LiveEventDelegationCreated:
		c.noteDelegationCreated(message)
	case schemas.LiveEventSessionUpdated:
		// A sideband can switch the backend too; the primary bills whatever the session now runs.
		if model := providerUtils.GetJSONField(message, "session.delegation.responses.model").Str; model != "" {
			if refusal := c.meter.switchBackend(model); refusal != nil {
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
		// A delegation the provider refuses to run never opens; the session goes on without it.
		if bifrostErr := delegationError(message); bifrostErr != nil {
			if refusal := c.meter.failBackend(bifrostErr); refusal != nil {
				c.endForRefusal(refusal)
			}
		}
	case schemas.LiveEventSessionClosed:
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
	c.meter.setEnding(c.transcript.snapshot())
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
