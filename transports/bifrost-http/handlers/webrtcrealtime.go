package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/plugins/modelcatalogresolver"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/valyala/fasthttp"
)

type WebRTCRealtimeHandler struct {
	webrtcRelayRegistry
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	legacyRoutes map[string]schemas.ModelProvider // path → default provider (legacy raw-SDP routes)
}

func NewWebRTCRealtimeHandler(client *bifrost.Bifrost, config *lib.Config) *WebRTCRealtimeHandler {
	return &WebRTCRealtimeHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		legacyRoutes: make(map[string]schemas.ModelProvider),
	}
}

func (h *WebRTCRealtimeHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleRequest, middlewares...)

	// Base bifrost route — GA /calls format (multipart sdp + session)
	r.POST("/v1/realtime/calls", handler)

	// Base bifrost route — legacy format (raw SDP or multipart on /v1/realtime)
	h.legacyRoutes["/v1/realtime"] = ""
	r.POST("/v1/realtime", handler)

	// OpenAI integration routes — /calls variants (GA format)
	for _, path := range integrations.OpenAIRealtimeWebRTCCallsPaths("/openai") {
		r.POST(path, handler)
	}

	// OpenAI integration routes — legacy variants (raw SDP, beta format)
	for _, path := range integrations.OpenAIRealtimePaths("/openai") {
		h.legacyRoutes[path] = schemas.OpenAI
		r.POST(path, handler)
	}
}

func (h *WebRTCRealtimeHandler) Close() {
	if h == nil {
		return
	}
	h.closeAll()
}

func (h *WebRTCRealtimeHandler) handleRequest(ctx *fasthttp.RequestCtx) {
	if defaultProvider, isLegacy := h.legacyRoutes[string(ctx.Path())]; isLegacy {
		h.handleLegacyRequest(ctx, defaultProvider)
	} else {
		h.handleCallsRequest(ctx)
	}
}

// handleCallsRequest handles the GA /realtime/calls format.
// Multipart bodies strictly require both "sdp" and "session" form fields —
// the model is read from session.model, not from a ?model= query param.
// Raw SDP bodies (application/sdp) fall back to ?model= for the legacy
// raw-SDP path only; the multipart contract has no ?model= fallback.
func (h *WebRTCRealtimeHandler) handleCallsRequest(ctx *fasthttp.RequestCtx) {
	sdpOffer, providerKey, model, normalizedSession, transcriptionSession, bifrostErr := parseCallsWebRTCRequest(ctx, h.config)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	rtProvider, bifrostErr := h.resolveWebRTCProvider(providerKey)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	exchangeSDP := func(rCtx *schemas.BifrostContext, key schemas.Key, upstreamOffer string, session []byte) (string, *schemas.BifrostError) {
		return rtProvider.ExchangeRealtimeWebRTCSDP(rCtx, key, model, upstreamOffer, session)
	}

	h.runWebRTCRelay(ctx, rtProvider, providerKey, model, sdpOffer, normalizedSession, transcriptionSession, exchangeSDP)
}

func parseCallsWebRTCRequest(ctx *fasthttp.RequestCtx, config *lib.Config) (string, schemas.ModelProvider, string, []byte, bool, *schemas.BifrostError) {
	contentType := strings.ToLower(string(ctx.Request.Header.ContentType()))
	path := string(ctx.Path())
	if strings.HasPrefix(contentType, "multipart/form-data") {
		form, err := ctx.MultipartForm()
		if err != nil {
			return "", "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "failed to parse multipart form", err)
		}

		sdpOffer := firstMultipartValue(form.Value, "sdp")
		if strings.TrimSpace(sdpOffer) == "" {
			return "", "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "sdp form field is required", nil)
		}

		sessionField := firstMultipartValue(form.Value, "session")
		if strings.TrimSpace(sessionField) == "" {
			return "", "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session form field is required", nil)
		}
		providerKey, model, normalizedSession, transcriptionSession, bifrostErr := resolveRealtimeSDPTarget(ctx, config, path, []byte(sessionField))
		if bifrostErr != nil {
			return "", "", "", nil, false, bifrostErr
		}
		return sdpOffer, providerKey, model, normalizedSession, transcriptionSession, nil
	}

	sdpOffer := string(ctx.Request.Body())
	if strings.TrimSpace(sdpOffer) == "" {
		return "", "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "SDP is required", nil)
	}

	rawModel := strings.TrimSpace(string(ctx.QueryArgs().Peek("model")))
	if rawModel == "" {
		return "", "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "model query param is required", nil)
	}

	providerKey, model := schemas.ParseModelString(rawModel, realtimeDefaultProviderForPath(path))
	// Model catalog auto-resolution for bare model names on base /v1 routes
	if providerKey == "" && strings.TrimSpace(model) != "" {
		selected, candidates := modelcatalogresolver.ResolveProviderFromCatalog(nil, config.ModelCatalog, model)
		if selected != "" {
			ctx.SetUserValue(lib.FastHTTPUserValueModelCatalogResolution, &lib.ModelCatalogResolution{
				Model:            model,
				ResolvedProvider: selected,
				AllProviders:     candidates,
			})
			providerKey = selected
		}
	}
	if providerKey == "" || strings.TrimSpace(model) == "" {
		if realtimeDefaultProviderForPath(path) == "" {
			return "", "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "model must use provider/model on /v1 realtime routes", nil)
		}
		return "", "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "invalid model: "+rawModel, nil)
	}

	return sdpOffer, providerKey, model, nil, false, nil
}

// handleLegacyRequest handles the beta /realtime endpoint.
// Accepts both multipart (sdp + session) and raw SDP (application/sdp) from clients.
func (h *WebRTCRealtimeHandler) handleLegacyRequest(ctx *fasthttp.RequestCtx, defaultProvider schemas.ModelProvider) {
	sdpOffer, rawModel, sessionJSON, bifrostErr := parseLegacyWebRTCRequest(ctx, defaultProvider)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	providerKey, model := schemas.ParseModelString(rawModel, defaultProvider)
	// Model catalog auto-resolution for bare model names on base /v1 routes
	if providerKey == "" && strings.TrimSpace(model) != "" {
		selected, candidates := modelcatalogresolver.ResolveProviderFromCatalog(nil, h.config.ModelCatalog, model)
		if selected != "" {
			ctx.SetUserValue(lib.FastHTTPUserValueModelCatalogResolution, &lib.ModelCatalogResolution{
				Model:            model,
				ResolvedProvider: selected,
				AllProviders:     candidates,
			})
			providerKey = selected
		}
	}
	if providerKey == "" || model == "" {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "invalid model: "+rawModel, nil))
		return
	}

	rtProvider, bifrostErr := h.resolveWebRTCProvider(providerKey)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	legacyProvider, ok := rtProvider.(schemas.RealtimeLegacyWebRTCProvider)
	if !ok {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "provider does not support legacy realtime WebRTC: "+string(providerKey), nil))
		return
	}

	// Strip provider prefixes from nested model fields (e.g. input_audio_transcription.model)
	if sessionJSON != nil {
		if root, parseErr := schemas.ParseRealtimeClientSecretBody(sessionJSON); parseErr == nil {
			openai.StripNestedModelPrefixes(root)
			if updated, marshalErr := json.Marshal(root); marshalErr == nil {
				sessionJSON = updated
			}
		}
	}

	exchangeSDP := func(rCtx *schemas.BifrostContext, key schemas.Key, upstreamOffer string, _ []byte) (string, *schemas.BifrostError) {
		return legacyProvider.ExchangeLegacyRealtimeWebRTCSDP(rCtx, key, upstreamOffer, sessionJSON, model)
	}

	h.runWebRTCRelay(ctx, rtProvider, providerKey, model, sdpOffer, sessionJSON, false, exchangeSDP)
}

// parseLegacyWebRTCRequest extracts SDP, model, and optional session from a legacy request.
// Handles both multipart (sdp + session fields) and raw SDP (body + ?model= query param).
func parseLegacyWebRTCRequest(ctx *fasthttp.RequestCtx, defaultProvider schemas.ModelProvider) (sdpOffer, rawModel string, sessionJSON json.RawMessage, err *schemas.BifrostError) {
	if strings.HasPrefix(strings.ToLower(string(ctx.Request.Header.ContentType())), "multipart/form-data") {
		form, formErr := ctx.MultipartForm()
		if formErr != nil {
			return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "failed to parse multipart form", formErr)
		}
		sdpOffer = firstMultipartValue(form.Value, "sdp")
		if sessionField := firstMultipartValue(form.Value, "session"); sessionField != "" {
			sessionJSON = json.RawMessage(sessionField)
			if root, parseErr := schemas.ParseRealtimeClientSecretBody(sessionJSON); parseErr == nil {
				if modelJSON, ok := root["model"]; ok {
					var m string
					if json.Unmarshal(modelJSON, &m) == nil {
						rawModel = m
					}
				}
			}
		}
	} else {
		sdpOffer = string(ctx.Request.Body())
	}

	if strings.TrimSpace(sdpOffer) == "" {
		return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "SDP is required", nil)
	}

	// Query param model takes precedence
	if queryModel := strings.TrimSpace(string(ctx.QueryArgs().Peek("model"))); queryModel != "" {
		rawModel = queryModel
	}
	if rawModel == "" {
		return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "model is required (query param or session field)", nil)
	}

	return sdpOffer, rawModel, sessionJSON, nil
}

// runWebRTCRelay is the shared relay setup: creates bifrost context, selects key, establishes relay.
func (h *WebRTCRealtimeHandler) runWebRTCRelay(
	ctx *fasthttp.RequestCtx,
	rtProvider schemas.RealtimeProvider,
	providerKey schemas.ModelProvider,
	model string,
	sdpOffer string,
	sessionJSON []byte,
	transcriptionSession bool,
	exchangeSDP func(ctx *schemas.BifrostContext, key schemas.Key, upstreamOffer string, session []byte) (string, *schemas.BifrostError),
) {
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore)
	defer cancel()
	// Apply governance/routing values from the transport middleware.
	// ConvertToBifrostContext creates a fresh context that doesn't carry the user
	// values the middleware stored on the fasthttp RequestCtx via SetUserValue.
	applyRealtimeMiddlewareValues(bifrostCtx, snapshotRealtimeMiddlewareValues(ctx))
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	if strings.HasPrefix(string(ctx.Path()), "/openai") {
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}

	// Admission check, before any key is selected: resolveRealtimeWebRTCKeys below reaches
	// SelectKeyForProviderRequestType and hands the relay the operator's provider key. Covers
	// both the GA /realtime/calls and the legacy raw-SDP routes, which funnel through here.
	if authErr := refuseUnauthenticatedRealtime(
		h.config.ClientConfig.EnforceAuthOnInference,
		bifrostCtx,
		string(ctx.Request.Header.Peek("Authorization")),
	); authErr != nil {
		SendBifrostError(ctx, authErr)
		return
	}

	// Resolve any Bifrost-minted ephemeral token mapping before the per-request pipeline runs,
	// so governance resolves the originating virtual key rather than the opaque token string,
	// and the resolution check below can refuse a mapped token whose virtual key has since been
	// revoked before any key selection or relay work.
	inboundToken := extractRealtimeBearerToken(ctx)
	mapping, mapped := lookupRealtimeEphemeralKeyMapping(h.handlerStore.GetKVStore(), inboundToken)
	if mapped {
		applyRealtimeEphemeralKeyMapping(bifrostCtx, mapping)
	}

	// Run the per-request pipeline so governance resolves the request's access onto the grant,
	// exactly as the WebSocket path does before its upgrade. The request's provider and model
	// are already resolved for WebRTC, so routing mutations are not read back; this exists so
	// the resolution check below reads the same answer the per-turn pipeline will.
	h.client.RunPreRequestHooks(bifrostCtx, &schemas.BifrostRequest{
		RequestType: schemas.RealtimeRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: providerKey,
			Model:    model,
		},
	})
	// Second admission question: a presented credential that resolved to nothing (forged or
	// revoked sk-bf-*) is refused before any key is selected and before the SDP exchange
	// reaches the provider.
	if authErr := refuseUnresolvedRealtimeCredential(
		h.config.ClientConfig.EnforceAuthOnInference,
		bifrostCtx,
		inboundToken,
		mapped && mapping.VirtualKey != "",
	); authErr != nil {
		SendBifrostError(ctx, authErr)
		return
	}

	authKey, selectedKey, err := h.resolveRealtimeWebRTCKeys(bifrostCtx, providerKey, model, inboundToken, mapping, mapped)
	if err != nil {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", err.Error(), nil))
		return
	}

	// Resolve model alias so the provider receives the actual model identifier.
	if selectedKey != nil {
		model = selectedKey.Aliases.Resolve(model)
	} else {
		model = authKey.Aliases.Resolve(model)
	}

	if transcriptionSession {
		var normalizeErr *schemas.BifrostError
		sessionJSON, normalizeErr = pinRealtimeSDPTranscriptionModel(sessionJSON, model)
		if normalizeErr != nil {
			SendBifrostError(ctx, normalizeErr)
			return
		}
	}

	// Compute raw storage flag from provider config + per-request header overrides.
	// Normal inference computes this inside bifrost.executeRequest, which is bypassed
	// for realtime WebRTC connections.
	applyRealtimeRawStorageContext(bifrostCtx, h.client.ComputeRawStorageForProvider(bifrostCtx, providerKey))

	boundExchange := func(rCtx *schemas.BifrostContext, upstreamOffer string) (string, *schemas.BifrostError) {
		return exchangeSDP(rCtx, authKey, upstreamOffer, sessionJSON)
	}

	relayCtx, relayCancel := newRealtimeRelayContext(bifrostCtx)
	session := bfws.NewSession(nil)
	browserAnswer, relayErr := h.establishRelay(relayCtx, relayCancel, session, rtProvider, providerKey, model, selectedKey, sdpOffer, transcriptionSession, boundExchange)
	if relayErr != nil {
		relayCancel()
		SendBifrostError(ctx, relayErr)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/sdp")
	ctx.SetBodyString(browserAnswer)
}

// resolveRealtimeWebRTCKeys turns the inbound credential into the upstream auth key. The
// ephemeral-token mapping is resolved and applied by the caller before the per-request pipeline
// runs (so admission and governance see the originating virtual key); this function only consumes
// that result.
func (h *WebRTCRealtimeHandler) resolveRealtimeWebRTCKeys(
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
	inboundToken string,
	mapping realtimeEphemeralKeyMapping,
	mapped bool,
) (schemas.Key, *schemas.Key, error) {
	if isRealtimeEphemeralToken(inboundToken) && !mapped {
		bifrostCtx.ClearValue(schemas.BifrostContextKeyAPIKeyID)
		bifrostCtx.ClearValue(schemas.BifrostContextKeyAPIKeyName)
		bifrostCtx.ClearValue(schemas.BifrostContextKeySelectedKeyID)
		bifrostCtx.ClearValue(schemas.BifrostContextKeySelectedKeyName)
		authKey := schemas.Key{Value: *schemas.NewSecretVar(inboundToken)}
		return authKey, nil, nil
	}

	selectedKey, err := h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.RealtimeRequest, providerKey, model)
	if err != nil && mapped && mapping.KeyID != "" {
		bifrostCtx.ClearValue(schemas.BifrostContextKeyAPIKeyID)
		selectedKey, err = h.client.SelectKeyForProviderRequestType(bifrostCtx, schemas.RealtimeRequest, providerKey, model)
	}
	if err != nil {
		return schemas.Key{}, nil, err
	}

	authKey := selectedKey
	if mapped && mapping.ProviderToken != "" {
		authKey.Value = *schemas.NewSecretVar(mapping.ProviderToken)
	} else if mapped && inboundToken != "" {
		authKey.Value = *schemas.NewSecretVar(inboundToken)
	}
	return authKey, &selectedKey, nil
}

func lookupRealtimeEphemeralKeyMapping(kv schemas.KVStore, token string) (realtimeEphemeralKeyMapping, bool) {
	if kv == nil || strings.TrimSpace(token) == "" {
		return realtimeEphemeralKeyMapping{}, false
	}

	raw, err := kv.Get(buildRealtimeEphemeralKeyMappingKey(token))
	if err != nil {
		return realtimeEphemeralKeyMapping{}, false
	}

	switch value := raw.(type) {
	case realtimeEphemeralKeyMapping:
		return value, true
	case string:
		return parseRealtimeEphemeralKeyMappingValue([]byte(value))
	case []byte:
		return parseRealtimeEphemeralKeyMappingValue(value)
	default:
		return realtimeEphemeralKeyMapping{}, false
	}
}

func parseRealtimeEphemeralKeyMappingValue(raw []byte) (realtimeEphemeralKeyMapping, bool) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return realtimeEphemeralKeyMapping{}, false
	}

	var mapping realtimeEphemeralKeyMapping
	if err := json.Unmarshal(raw, &mapping); err == nil {
		mapping.KeyID = strings.TrimSpace(mapping.KeyID)
		mapping.VirtualKey = strings.TrimSpace(mapping.VirtualKey)
		mapping.ProviderToken = strings.TrimSpace(mapping.ProviderToken)
		if mapping.KeyID != "" || mapping.VirtualKey != "" || mapping.ProviderToken != "" {
			return mapping, true
		}
	}

	var keyID string
	if err := json.Unmarshal(raw, &keyID); err == nil {
		keyID = strings.TrimSpace(keyID)
		if keyID != "" {
			return realtimeEphemeralKeyMapping{KeyID: keyID}, true
		}
	}

	keyID = strings.TrimSpace(string(raw))
	if keyID == "" {
		return realtimeEphemeralKeyMapping{}, false
	}
	return realtimeEphemeralKeyMapping{KeyID: keyID}, true
}

func applyRealtimeEphemeralKeyMapping(bifrostCtx *schemas.BifrostContext, mapping realtimeEphemeralKeyMapping) {
	if bifrostCtx == nil {
		return
	}
	if mapping.VirtualKey != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, mapping.VirtualKey)
		// The ephemeral key stood in for the virtual key it was minted from, so the request is
		// that key's.
		lib.RecordCredential(bifrostCtx, grant.NewCredential(grant.CredentialVirtualKey, mapping.VirtualKey))
	}
	if mapping.KeyID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, mapping.KeyID)
	}
}

func extractRealtimeBearerToken(ctx *fasthttp.RequestCtx) string {
	if ctx == nil {
		return ""
	}
	return extractRealtimeBearerTokenFromHeader(string(ctx.Request.Header.Peek("Authorization")))
}

func extractRealtimeBearerTokenFromHeader(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if len(authHeader) < len("Bearer ")+1 || !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return ""
	}
	return strings.TrimSpace(authHeader[7:])
}

func isRealtimeEphemeralToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), "ek_")
}

// resolveWebRTCProvider validates and returns a RealtimeProvider that supports WebRTC.
func (h *WebRTCRealtimeHandler) resolveWebRTCProvider(providerKey schemas.ModelProvider) (schemas.RealtimeProvider, *schemas.BifrostError) {
	provider := h.client.GetProviderByKey(providerKey)
	if provider == nil {
		return nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "provider not found: "+string(providerKey), nil)
	}

	rtProvider, ok := provider.(schemas.RealtimeProvider)
	if !ok || !rtProvider.SupportsRealtimeAPI() {
		return nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "provider does not support realtime: "+string(providerKey), nil)
	}

	if !rtProvider.SupportsRealtimeWebRTC() {
		return nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "provider does not support realtime WebRTC: "+string(providerKey), nil)
	}

	return rtProvider, nil
}

// establishRelay sets up a Realtime session's WebRTC relay; exchangeSDP picks the GA or legacy
// SDP exchange.
func (h *WebRTCRealtimeHandler) establishRelay(
	relayCtx *schemas.BifrostContext,
	relayCancel context.CancelFunc,
	session *bfws.Session,
	provider schemas.RealtimeProvider,
	providerKey schemas.ModelProvider,
	model string,
	key *schemas.Key,
	browserOffer string,
	transcriptionSession bool,
	exchangeSDP func(ctx *schemas.BifrostContext, upstreamOffer string) (string, *schemas.BifrostError),
) (string, *schemas.BifrostError) {
	return establishWebRTCRelay(webrtcRelaySetup{
		requestType: schemas.RealtimeRequest,
		handler: &realtimeWebRTCMessages{
			client:               h.client,
			session:              session,
			bifrostCtx:           relayCtx,
			provider:             provider,
			providerKey:          providerKey,
			model:                model,
			key:                  key,
			transcriptionSession: transcriptionSession,
		},
		dataChannelLabel: provider.RealtimeWebRTCDataChannelLabel(),
		browserOffer:     browserOffer,
		handshakeCtx:     relayCtx,
		exchangeSDP: func(upstreamOffer string) (string, *schemas.BifrostError) {
			return exchangeSDP(relayCtx, upstreamOffer)
		},
		onCreate: func(relay *webrtcRelay) { h.registerRelay(session.ID(), relay) },
		onClose:  func() { h.unregisterRelay(session.ID()) },
		cancel:   relayCancel,
	})
}

// realtimeWebRTCMessages translates Realtime events across a WebRTC relay and runs a plugin
// pass per turn.
type realtimeWebRTCMessages struct {
	client               *bifrost.Bifrost
	session              *bfws.Session
	bifrostCtx           *schemas.BifrostContext
	provider             schemas.RealtimeProvider
	providerKey          schemas.ModelProvider
	model                string
	key                  *schemas.Key
	transcriptionSession bool
}

func (m *realtimeWebRTCMessages) fromBrowser(r *webrtcRelay, msg webrtc.DataChannelMessage) {
	event, err := schemas.ParseRealtimeEvent(msg.Data)
	if err != nil {
		logger.Warn("failed to parse browser realtime event: %v", err)
		r.sendUpstream(msg.Data, msg.IsString)
		return
	}
	toolItemID, toolSummary := pendingRealtimeToolOutputUpdate(event)
	if toolSummary != "" {
		m.session.RecordRealtimeToolOutput(toolItemID, toolSummary, string(msg.Data))
	}
	inputItemID, inputSummary := pendingRealtimeInputUpdate(event)
	if inputSummary != "" {
		m.session.RecordRealtimeInput(inputItemID, inputSummary, string(msg.Data))
	}
	startsTurn := m.provider.ShouldStartRealtimeTurn(event)
	if startsTurn {
		if m.session.PeekRealtimeTurnHooks() != nil {
			r.sendDownstream(newRealtimeTurnErrorEventPayload(newRealtimeWireBifrostError(400, "invalid_request_error", "Conversation already has an active response in progress.")), true)
			return
		}
		if bifrostErr := startRealtimeTurnHooks(m.client, m.bifrostCtx, m.session, m.provider, m.providerKey, m.model, m.key, event); bifrostErr != nil {
			r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
			return
		}
	}

	sanitizeRealtimeSessionEventForProvider(event)
	providerEvent, err := m.provider.ToProviderRealtimeEvent(event)
	if err != nil {
		if startsTurn {
			if finalizeErr := finalizeRealtimeTurnHooksOnTransportError(
				m.client,
				m.bifrostCtx,
				m.session,
				m.providerKey,
				m.model,
				m.key,
				400,
				"invalid_request_error",
				err.Error(),
			); finalizeErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(finalizeErr))
				return
			}
			r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(newRealtimeWireBifrostError(400, "invalid_request_error", err.Error())))
			return
		}
		logger.Warn("failed to translate browser realtime event: %v", err)
		r.sendUpstream(msg.Data, msg.IsString)
		return
	}
	// Track session metadata only after provider translation succeeds. Rejected
	// session.update events must not affect later turn logs.
	updateRealtimeSessionFromEvent(m.session, event)
	r.sendUpstream(providerEvent, msg.IsString)
}

func (m *realtimeWebRTCMessages) fromProvider(r *webrtcRelay, msg webrtc.DataChannelMessage) {
	event, err := m.provider.ToBifrostRealtimeEvent(msg.Data)
	if err != nil {
		if finalizeErr := finalizeRealtimeTurnHooksOnTransportError(
			m.client,
			m.bifrostCtx,
			m.session,
			m.providerKey,
			m.model,
			m.key,
			502,
			"server_error",
			"failed to translate upstream realtime event",
		); finalizeErr != nil {
			r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(finalizeErr))
			return
		}
		logger.Warn("failed to translate upstream realtime event: %v", err)
		r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(newRealtimeWireBifrostError(502, "server_error", "failed to translate upstream realtime event")))
		return
	}
	if event != nil {
		if event.Session != nil && event.Session.ID != "" {
			m.session.SetProviderSessionID(event.Session.ID)
		}
		// Track session tool definitions from session.created/session.updated (server→client).
		updateRealtimeSessionFromEvent(m.session, event)
		inputItemID, inputSummary := pendingRealtimeInputUpdate(event)
		if inputSummary != "" {
			m.session.RecordRealtimeInput(inputItemID, inputSummary, string(msg.Data))
		}
		if event.Delta != nil && m.provider.ShouldAccumulateRealtimeOutput(event.Type) {
			m.session.AppendRealtimeOutputText(event.Delta.Text)
			m.session.AppendRealtimeOutputText(event.Delta.Transcript)
		}
		if m.provider.ShouldStartRealtimeTurn(event) && m.session.PeekRealtimeTurnHooks() == nil {
			if bifrostErr := startRealtimeTurnHooks(m.client, m.bifrostCtx, m.session, m.provider, m.providerKey, m.model, m.key, event); bifrostErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
				return
			}
		}
	}
	if event != nil {
		if !m.provider.ShouldForwardRealtimeEvent(event) {
			return
		}
		terminalEventType := realtimeTurnFinalEvent(m.provider, m.transcriptionSession)
		if event.Type == terminalEventType {
			inputItemID, inputSummary, contentOverride := realtimeTurnCompletionContent(m.session, event, m.transcriptionSession)
			if inputSummary != "" {
				m.session.RecordRealtimeInput(inputItemID, inputSummary, string(msg.Data))
			}
			if bifrostErr := finalizeRealtimeTurnHooks(m.client, m.bifrostCtx, m.session, m.provider, m.providerKey, m.model, m.key, msg.Data, contentOverride, terminalEventType, m.transcriptionSession); bifrostErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
				return
			}
		} else if event.Error != nil {
			if finalizeErr := finalizeRealtimeTurnHooksWithError(
				m.client,
				m.bifrostCtx,
				m.session,
				m.providerKey,
				m.model,
				m.key,
				event.Type,
				msg.Data,
				newBifrostErrorFromRealtimeError(m.providerKey, m.model, msg.Data, event.Error),
			); finalizeErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(finalizeErr))
				return
			}
		}
		msg.Data, err = m.provider.ToProviderRealtimeEvent(event)
		if err != nil {
			logger.Warn("failed to encode translated realtime event: %v", err)
			// Lifecycle events (response.done / error) must reach the client so it
			// can transition turn state — if encoding fails after the turn was
			// finalized server-side, swallowing this would leave the client hung.
			r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(
				newRealtimeWireBifrostError(502, "server_error", "failed to encode translated realtime event: "+err.Error()),
			))
			return
		}
	}

	r.sendDownstream(msg.Data, msg.IsString)
}

// browserGone ends a Realtime relay as soon as the browser leaves.
func (m *realtimeWebRTCMessages) browserGone(r *webrtcRelay) {
	r.close()
}

// closed finalizes a turn still in flight, so plugins see both halves of it.
func (m *realtimeWebRTCMessages) closed() {
	if m.session == nil {
		return
	}
	_ = finalizeRealtimeTurnHooksOnTransportError(
		m.client,
		m.bifrostCtx,
		m.session,
		m.providerKey,
		m.model,
		m.key,
		502,
		"connection_closed",
		"realtime WebRTC session closed before turn completed",
	)
	m.session.ClearRealtimeTurnHooks()
}

func newRealtimeRelayContext(requestCtx *schemas.BifrostContext) (*schemas.BifrostContext, context.CancelFunc) {
	relayCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	if requestCtx == nil {
		return relayCtx, cancel
	}

	for _, key := range []any{
		schemas.BifrostContextKeyRequestID,
		schemas.BifrostContextKeyHTTPRequestType,
		schemas.BifrostContextKeyIntegrationType,
		schemas.BifrostContextKeyParentRequestID,
		schemas.BifrostContextKeyVirtualKey,
		schemas.BifrostContextKeyAPIKeyName,
		schemas.BifrostContextKeyAPIKeyID,
		schemas.BifrostContextKeyExtraHeaders,
		schemas.BifrostContextKeyRequestHeaders,
		schemas.BifrostContextKeyUserAgent,
		schemas.BifrostContextKeyGovernanceVirtualKeyID,
		schemas.BifrostContextKeyGovernanceVirtualKeyName,
		schemas.BifrostContextKeyGovernanceDisableContentLogging,
		governanceVirtualKeyMetadataContextKey,
		schemas.BifrostContextKeyGovernanceRoutingRuleID,
		schemas.BifrostContextKeyGovernanceRoutingRuleName,
		schemas.BifrostContextKeyGovernanceCustomerID,
		schemas.BifrostContextKeyGovernanceCustomerName,
		schemas.BifrostContextKeyGovernanceTeamID,
		schemas.BifrostContextKeyGovernanceTeamName,
		schemas.BifrostContextKeyGovernanceProjectID,
		schemas.BifrostContextKeyGovernanceProjectName,
		schemas.BifrostContextKeyUserID,
		schemas.BifrostContextKeyUserName,
		schemas.BifrostContextKeyGovernanceIncludeOnlyKeys,
		schemas.BifrostContextKeyGovernancePluginName,
		schemas.BifrostContextKeySelectedKeyID,
		schemas.BifrostContextKeySelectedKeyName,
		schemas.BifrostContextKeyIsEnterprise,
		schemas.BifrostContextKeyRoutingEnginesUsed,
		schemas.BifrostContextKeyRoutingEngineLogs,
		schemas.BifrostContextKeyShouldStoreRawInLogs,
		schemas.BifrostContextKeyAllowPerRequestStorageOverride,
		schemas.BifrostContextKeyAllowPerRequestRawOverride,
		schemas.BifrostContextKeyStoreRawRequestResponse,
		schemas.BifrostContextKeyDisableContentLogging,
		schemas.BifrostContextKeyCaptureRawRequest,
		schemas.BifrostContextKeyCaptureRawResponse,
		schemas.BifrostContextKeyDropRawRequestFromClient,
		schemas.BifrostContextKeyDropRawResponseFromClient,
	} {
		if value := requestCtx.Value(key); value != nil {
			relayCtx.SetValue(key, value)
		}
	}

	// The relay is the request that opened it, kept alive past the request: it carries that
	// request's grant, not a copy, the same way a realtime turn carries its session's (see
	// newRealtimeTurnContext). Every turn derives from the relay, so without this no turn would
	// carry a grant and governance would refuse each one.
	if g := requestCtx.Grant(); g != nil {
		relayCtx.SetGrant(g)
	}

	// Tag the relay context with transport type for downstream logging/metadata.
	relayCtx.SetValue(schemas.BifrostContextKeyRealtimeTransport, "webrtc")

	return relayCtx, cancel
}

func resolveRealtimeSDPTarget(ctx *fasthttp.RequestCtx, config *lib.Config, path string, sessionJSON []byte) (schemas.ModelProvider, string, []byte, bool, *schemas.BifrostError) {
	root, err := schemas.ParseRealtimeClientSecretBody(sessionJSON)
	if err != nil {
		return "", "", nil, false, err
	}

	modelJSON, hasRootModel := root["model"]
	transcriptionSession := false
	if !hasRootModel {
		modelJSON = nestedRealtimeTranscriptionModel(root)
		transcriptionSession = len(modelJSON) > 0
	}
	if len(modelJSON) == 0 {
		return "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model or session.audio.input.transcription.model is required", nil)
	}

	var rawModel string
	if err := json.Unmarshal(modelJSON, &rawModel); err != nil {
		return "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model must be a string", err)
	}

	providerKey, model := schemas.ParseModelString(strings.TrimSpace(rawModel), realtimeDefaultProviderForPath(path))
	// Model catalog auto-resolution for bare model names in session body
	if providerKey == "" && strings.TrimSpace(model) != "" {
		selected, candidates := modelcatalogresolver.ResolveProviderFromCatalog(nil, config.ModelCatalog, model)
		if selected != "" {
			ctx.SetUserValue(lib.FastHTTPUserValueModelCatalogResolution, &lib.ModelCatalogResolution{
				Model:            model,
				ResolvedProvider: selected,
				AllProviders:     candidates,
			})
			providerKey = selected
		}
	}
	if providerKey == "" || strings.TrimSpace(model) == "" {
		if realtimeDefaultProviderForPath(path) == "" {
			return "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model must use provider/model on /v1 realtime routes", nil)
		}
		return "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model is required", nil)
	}

	normalizedModel, marshalErr := json.Marshal(model)
	if marshalErr != nil {
		return "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to encode normalized session model", marshalErr)
	}
	if transcriptionSession {
		root["type"] = json.RawMessage(`"transcription"`)
	} else {
		root["model"] = normalizedModel
		openai.StripNestedModelPrefixes(root)
	}
	normalizedSession, marshalErr := json.Marshal(root)
	if marshalErr != nil {
		return "", "", nil, false, newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to encode normalized realtime session", marshalErr)
	}

	return providerKey, strings.TrimSpace(model), normalizedSession, transcriptionSession, nil
}

func nestedRealtimeTranscriptionModel(root map[string]json.RawMessage) json.RawMessage {
	var audio struct {
		Input struct {
			Transcription map[string]json.RawMessage `json:"transcription"`
		} `json:"input"`
	}
	if json.Unmarshal(root["audio"], &audio) != nil {
		return nil
	}
	return audio.Input.Transcription["model"]
}

func pinRealtimeSDPTranscriptionModel(sessionJSON []byte, model string) ([]byte, *schemas.BifrostError) {
	root, err := schemas.ParseRealtimeClientSecretBody(sessionJSON)
	if err != nil {
		return nil, err
	}
	var audio map[string]json.RawMessage
	var input map[string]json.RawMessage
	var transcription map[string]json.RawMessage
	if json.Unmarshal(root["audio"], &audio) != nil || json.Unmarshal(audio["input"], &input) != nil || json.Unmarshal(input["transcription"], &transcription) != nil {
		return nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.audio.input.transcription must be an object", nil)
	}
	transcription["model"] = json.RawMessage(strconv.Quote(model))
	input["transcription"], _ = json.Marshal(transcription)
	audio["input"], _ = json.Marshal(input)
	root["audio"], _ = json.Marshal(audio)
	normalized, marshalErr := json.Marshal(root)
	if marshalErr != nil {
		return nil, newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to encode normalized realtime session", marshalErr)
	}
	return normalized, nil
}

func firstMultipartValue(values map[string][]string, key string) string {
	if len(values[key]) == 0 {
		return ""
	}
	return values[key][0]
}

func newRealtimeWebRTCError(status int, errorType, message string, err error) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(status),
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(errorType),
			Message: message,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.RealtimeRequest,
		},
	}
}
