package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/valyala/fasthttp"
)

const (
	webrtcRealtimeHandshakeTimeout   = 10 * time.Second
	webrtcRealtimeICEGatherTimeout   = 3 * time.Second
	webrtcRealtimeMaxPendingMessages = 1000
)

var defaultAudioCodec = webrtc.RTPCodecCapability{
	MimeType:    webrtc.MimeTypeOpus,
	ClockRate:   48000,
	Channels:    2,
	SDPFmtpLine: "minptime=10;useinbandfec=1",
}

var realtimeSDPMaxMessageSizePattern = regexp.MustCompile(`(?m)^a=max-message-size:(\d+)\s*$`)

type WebRTCRealtimeHandler struct {
	client       *bifrost.Bifrost
	config       *lib.Config
	handlerStore lib.HandlerStore
	mu           sync.Mutex
	relays       map[string]*webrtcRealtimeRelay
	legacyRoutes map[string]schemas.ModelProvider // path → default provider (legacy raw-SDP routes)
}

func NewWebRTCRealtimeHandler(client *bifrost.Bifrost, config *lib.Config) *WebRTCRealtimeHandler {
	return &WebRTCRealtimeHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		relays:       make(map[string]*webrtcRealtimeRelay),
		legacyRoutes: make(map[string]schemas.ModelProvider),
	}
}

func (h *WebRTCRealtimeHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleRequest, middlewares...)

	// Base bifrost route — GA /calls format (multipart sdp + session)
	r.POST("/v1/realtime/calls", handler)

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

	h.mu.Lock()
	relays := make([]*webrtcRealtimeRelay, 0, len(h.relays))
	for _, relay := range h.relays {
		relays = append(relays, relay)
	}
	h.mu.Unlock()

	for _, relay := range relays {
		relay.closeWithShutdownSignal()
	}
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
	sdpOffer, providerKey, model, normalizedSession, bifrostErr := parseCallsWebRTCRequest(ctx)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	rtProvider, bifrostErr := h.resolveWebRTCProvider(providerKey)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	exchangeSDP := func(rCtx *schemas.BifrostContext, key schemas.Key, upstreamOffer string) (string, *schemas.BifrostError) {
		return rtProvider.ExchangeRealtimeWebRTCSDP(rCtx, key, model, upstreamOffer, normalizedSession)
	}

	h.runWebRTCRelay(ctx, rtProvider, providerKey, model, sdpOffer, exchangeSDP)
}

func parseCallsWebRTCRequest(ctx *fasthttp.RequestCtx) (string, schemas.ModelProvider, string, []byte, *schemas.BifrostError) {
	contentType := strings.ToLower(string(ctx.Request.Header.ContentType()))
	path := string(ctx.Path())
	if strings.HasPrefix(contentType, "multipart/form-data") {
		form, err := ctx.MultipartForm()
		if err != nil {
			return "", "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "failed to parse multipart form", err)
		}

		sdpOffer := firstMultipartValue(form.Value, "sdp")
		if strings.TrimSpace(sdpOffer) == "" {
			return "", "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "sdp form field is required", nil)
		}

		sessionField := firstMultipartValue(form.Value, "session")
		if strings.TrimSpace(sessionField) == "" {
			return "", "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session form field is required", nil)
		}
		providerKey, model, normalizedSession, bifrostErr := resolveRealtimeSDPTarget(path, []byte(sessionField))
		if bifrostErr != nil {
			return "", "", "", nil, bifrostErr
		}
		return sdpOffer, providerKey, model, normalizedSession, nil
	}

	sdpOffer := string(ctx.Request.Body())
	if strings.TrimSpace(sdpOffer) == "" {
		return "", "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "SDP is required", nil)
	}

	rawModel := strings.TrimSpace(string(ctx.QueryArgs().Peek("model")))
	if rawModel == "" {
		return "", "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "model query param is required", nil)
	}

	providerKey, model := schemas.ParseModelString(rawModel, realtimeDefaultProviderForPath(path))
	if providerKey == "" || strings.TrimSpace(model) == "" {
		if realtimeDefaultProviderForPath(path) == "" {
			return "", "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "model must use provider/model on /v1 realtime routes", nil)
		}
		return "", "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "invalid model: "+rawModel, nil)
	}

	return sdpOffer, providerKey, model, nil, nil
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

	exchangeSDP := func(rCtx *schemas.BifrostContext, key schemas.Key, upstreamOffer string) (string, *schemas.BifrostError) {
		return legacyProvider.ExchangeLegacyRealtimeWebRTCSDP(rCtx, key, upstreamOffer, sessionJSON, model)
	}

	h.runWebRTCRelay(ctx, rtProvider, providerKey, model, sdpOffer, exchangeSDP)
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
	exchangeSDP func(ctx *schemas.BifrostContext, key schemas.Key, upstreamOffer string) (string, *schemas.BifrostError),
) {
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.handlerStore)
	defer cancel()
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	if strings.HasPrefix(string(ctx.Path()), "/openai") {
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}

	authKey, selectedKey, err := h.resolveRealtimeWebRTCKeys(ctx, bifrostCtx, providerKey, model)
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

	boundExchange := func(rCtx *schemas.BifrostContext, upstreamOffer string) (string, *schemas.BifrostError) {
		return exchangeSDP(rCtx, authKey, upstreamOffer)
	}

	relayCtx, relayCancel := newRealtimeRelayContext(bifrostCtx)
	session := bfws.NewSession(nil)
	browserAnswer, relayErr := h.establishRelay(relayCtx, relayCancel, session, rtProvider, providerKey, model, selectedKey, sdpOffer, boundExchange)
	if relayErr != nil {
		relayCancel()
		SendBifrostError(ctx, relayErr)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/sdp")
	ctx.SetBodyString(browserAnswer)
}

func (h *WebRTCRealtimeHandler) resolveRealtimeWebRTCKeys(
	ctx *fasthttp.RequestCtx,
	bifrostCtx *schemas.BifrostContext,
	providerKey schemas.ModelProvider,
	model string,
) (schemas.Key, *schemas.Key, error) {
	inboundToken := extractRealtimeBearerToken(ctx)
	mapping, mapped := lookupRealtimeEphemeralKeyMapping(h.handlerStore.GetKVStore(), inboundToken)
	if mapped {
		applyRealtimeEphemeralKeyMapping(bifrostCtx, mapping)
	}
	if isRealtimeEphemeralToken(inboundToken) && !mapped {
		bifrostCtx.ClearValue(schemas.BifrostContextKeyAPIKeyID)
		bifrostCtx.ClearValue(schemas.BifrostContextKeyAPIKeyName)
		bifrostCtx.ClearValue(schemas.BifrostContextKeySelectedKeyID)
		bifrostCtx.ClearValue(schemas.BifrostContextKeySelectedKeyName)
		authKey := schemas.Key{Value: *schemas.NewEnvVar(inboundToken)}
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
	if mapped && inboundToken != "" {
		authKey.Value = *schemas.NewEnvVar(inboundToken)
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
		if mapping.KeyID != "" || mapping.VirtualKey != "" {
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

// establishRelay sets up the bidirectional WebRTC relay between the browser and the upstream provider.
// exchangeSDP is called with the upstream peer connection's SDP offer and must return the provider's
// SDP answer. This allows the handler to plug in different exchange strategies (GA calls vs legacy).
func (h *WebRTCRealtimeHandler) establishRelay(
	relayCtx *schemas.BifrostContext,
	relayCancel context.CancelFunc,
	session *bfws.Session,
	provider schemas.RealtimeProvider,
	providerKey schemas.ModelProvider,
	model string,
	key *schemas.Key,
	browserOffer string,
	exchangeSDP func(ctx *schemas.BifrostContext, upstreamOffer string) (string, *schemas.BifrostError),
) (string, *schemas.BifrostError) {
	downstreamPC, err := newRealtimePeerConnection()
	if err != nil {
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to create browser peer connection", err)
	}
	upstreamPC, err := newRealtimePeerConnection()
	if err != nil {
		_ = downstreamPC.Close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to create upstream peer connection", err)
	}

	relay := &webrtcRealtimeRelay{
		client:       h.client,
		downstreamPC: downstreamPC,
		upstreamPC:   upstreamPC,
		session:      session,
		bifrostCtx:   relayCtx,
		cancel:       relayCancel,
		provider:     provider,
		providerKey:  providerKey,
		model:        model,
		key:          key,
	}
	relay.onClose = func() {
		h.unregisterRelay(session.ID())
	}
	relay.installCloseHandlers()
	h.registerRelay(session.ID(), relay)

	// Downstream local audio track carries provider audio back to the browser.
	providerToBrowserTrack, err := webrtc.NewTrackLocalStaticRTP(defaultAudioCodec, "audio", "bifrost-provider-audio")
	if err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to create browser audio track", err)
	}
	providerToBrowserSender, err := downstreamPC.AddTrack(providerToBrowserTrack)
	if err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to attach browser audio track", err)
	}
	relay.providerToBrowserTrack = providerToBrowserTrack
	go relay.forwardRTCP(providerToBrowserSender, upstreamPC)

	// Upstream local audio track carries browser audio to the provider.
	browserToProviderTrack, err := webrtc.NewTrackLocalStaticRTP(defaultAudioCodec, "audio", "bifrost-browser-audio")
	if err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to create provider audio track", err)
	}
	browserToProviderSender, err := upstreamPC.AddTrack(browserToProviderTrack)
	if err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to attach provider audio track", err)
	}
	relay.browserToProviderTrack = browserToProviderTrack
	go relay.forwardRTCP(browserToProviderSender, downstreamPC)

	relay.installTrackForwarders()
	if err := relay.installDataChannelRelay(); err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to create upstream realtime data channel", err)
	}

	if err := downstreamPC.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  browserOffer,
	}); err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "invalid browser SDP offer", err)
	}

	upstreamOffer, err := relay.createOffer(upstreamPC)
	if err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to create upstream SDP offer", err)
	}
	upstreamOffer = constrainRealtimeSDPMaxMessageSize(upstreamOffer, browserOffer)

	upstreamAnswer, exchangeErr := exchangeSDP(relayCtx, upstreamOffer)
	if exchangeErr != nil {
		relay.close()
		return "", exchangeErr
	}

	if err := upstreamPC.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  upstreamAnswer,
	}); err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusBadGateway, "upstream_connection_error", "invalid upstream SDP answer", err)
	}

	waitCtx, waitCancel := context.WithTimeout(relayCtx, webrtcRealtimeHandshakeTimeout)
	defer waitCancel()

	if err := relay.waitForUpstream(waitCtx); err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusBadGateway, "upstream_connection_error", "upstream realtime WebRTC connection failed", err)
	}

	browserAnswer, err := relay.createAnswer(downstreamPC)
	if err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to create browser SDP answer", err)
	}

	return browserAnswer, nil
}

type webrtcRealtimeRelay struct {
	client       *bifrost.Bifrost
	downstreamPC *webrtc.PeerConnection
	upstreamPC   *webrtc.PeerConnection

	downstreamChannel *webrtc.DataChannel
	upstreamChannel   *webrtc.DataChannel

	providerToBrowserTrack *webrtc.TrackLocalStaticRTP
	browserToProviderTrack *webrtc.TrackLocalStaticRTP

	session     *bfws.Session
	bifrostCtx  *schemas.BifrostContext
	cancel      context.CancelFunc
	provider    schemas.RealtimeProvider
	providerKey schemas.ModelProvider
	model       string
	key         *schemas.Key
	onClose     func()

	closeOnce sync.Once

	channelMu                sync.Mutex
	pendingToUpstream        []queuedDataChannelMessage
	pendingToDownstream      []queuedDataChannelMessage
	upstreamConnectedOrError chan error
}

type queuedDataChannelMessage struct {
	payload  []byte
	isString bool
}

func (r *webrtcRealtimeRelay) installCloseHandlers() {
	r.upstreamConnectedOrError = make(chan error, 1)

	handleState := func(name string, pc *webrtc.PeerConnection) {
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			switch state {
			case webrtc.PeerConnectionStateConnected:
				if name == "upstream" {
					select {
					case r.upstreamConnectedOrError <- nil:
					default:
					}
				}
			case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
				if name == "upstream" {
					select {
					case r.upstreamConnectedOrError <- fmt.Errorf("peer connection state %s", state.String()):
					default:
					}
				}
				r.close()
			case webrtc.PeerConnectionStateDisconnected:
				r.close()
			}
		})
	}

	handleState("downstream", r.downstreamPC)
	handleState("upstream", r.upstreamPC)
}

func (r *webrtcRealtimeRelay) installTrackForwarders() {
	r.downstreamPC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		r.forwardRTPTrack(track, r.browserToProviderTrack)
	})

	r.upstreamPC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		r.forwardRTPTrack(track, r.providerToBrowserTrack)
	})
}

func (r *webrtcRealtimeRelay) installDataChannelRelay() error {
	label := strings.TrimSpace(r.provider.RealtimeWebRTCDataChannelLabel())
	if label == "" {
		return nil
	}
	upstreamDC, err := r.upstreamPC.CreateDataChannel(label, nil)
	if err != nil {
		return err
	}
	r.bindUpstreamChannel(upstreamDC)

	r.downstreamPC.OnDataChannel(func(dc *webrtc.DataChannel) {
		r.bindDownstreamChannel(dc)
	})
	return nil
}

func (r *webrtcRealtimeRelay) bindUpstreamChannel(dc *webrtc.DataChannel) {
	r.channelMu.Lock()
	r.upstreamChannel = dc
	r.channelMu.Unlock()

	dc.OnOpen(func() {
		r.flushPending()
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		r.handleUpstreamMessage(msg)
	})
	dc.OnClose(func() { r.close() })
	dc.OnError(func(err error) {
		logger.Warn("upstream realtime data channel error: %v", err)
		r.close()
	})
}

func (r *webrtcRealtimeRelay) bindDownstreamChannel(dc *webrtc.DataChannel) {
	r.channelMu.Lock()
	if r.downstreamChannel != nil {
		r.channelMu.Unlock()
		_ = dc.Close()
		return
	}
	r.downstreamChannel = dc
	r.channelMu.Unlock()

	dc.OnOpen(func() {
		r.flushPending()
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		r.handleDownstreamMessage(msg)
	})
	dc.OnClose(func() { r.close() })
	dc.OnError(func(err error) {
		logger.Warn("browser realtime data channel error: %v", err)
		r.close()
	})
}

func (r *webrtcRealtimeRelay) createOffer(pc *webrtc.PeerConnection) (string, error) {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return "", err
	}
	select {
	case <-gatherComplete:
	case <-time.After(webrtcRealtimeICEGatherTimeout):
	}
	if pc.LocalDescription() == nil {
		return "", errors.New("local description not set")
	}
	return pc.LocalDescription().SDP, nil
}

func (r *webrtcRealtimeRelay) createAnswer(pc *webrtc.PeerConnection) (string, error) {
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	select {
	case <-gatherComplete:
	case <-time.After(webrtcRealtimeICEGatherTimeout):
	}
	if pc.LocalDescription() == nil {
		return "", errors.New("local description not set")
	}
	return pc.LocalDescription().SDP, nil
}

func (r *webrtcRealtimeRelay) waitForUpstream(ctx context.Context) error {
	select {
	case err := <-r.upstreamConnectedOrError:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *webrtcRealtimeRelay) forwardRTPTrack(track *webrtc.TrackRemote, target *webrtc.TrackLocalStaticRTP) {
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if err := target.WriteRTP(packet); err != nil {
			return
		}
	}
}

func (r *webrtcRealtimeRelay) forwardRTCP(sender *webrtc.RTPSender, target *webrtc.PeerConnection) {
	if sender == nil || target == nil {
		return
	}
	buf := make([]byte, 1500)
	for {
		n, _, readErr := sender.Read(buf)
		if readErr != nil {
			return
		}
		pkts, parseErr := rtcp.Unmarshal(buf[:n])
		if parseErr != nil {
			continue
		}
		if writeErr := target.WriteRTCP(pkts); writeErr != nil {
			return
		}
	}
}

func (r *webrtcRealtimeRelay) handleDownstreamMessage(msg webrtc.DataChannelMessage) {
	event, err := schemas.ParseRealtimeEvent(msg.Data)
	if err != nil {
		logger.Warn("failed to parse browser realtime event: %v", err)
		r.sendUpstream(msg.Data, msg.IsString)
		return
	}
	toolItemID, toolSummary := pendingRealtimeToolOutputUpdate(event)
	if toolSummary != "" {
		r.session.RecordRealtimeToolOutput(toolItemID, toolSummary, string(msg.Data))
	}
	inputItemID, inputSummary := pendingRealtimeInputUpdate(event)
	if inputSummary != "" {
		r.session.RecordRealtimeInput(inputItemID, inputSummary, string(msg.Data))
	}
	startsTurn := r.provider.ShouldStartRealtimeTurn(event)
	if startsTurn {
		if r.session.PeekRealtimeTurnHooks() != nil {
			r.sendDownstream(newRealtimeTurnErrorEventPayload(newRealtimeWireBifrostError(400, "invalid_request_error", "Conversation already has an active response in progress.")), true)
			return
		}
		if bifrostErr := startRealtimeTurnHooks(r.client, r.bifrostCtx, r.session, r.provider, r.providerKey, r.model, r.key, event.Type); bifrostErr != nil {
			r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
			return
		}
	}

	providerEvent, err := r.provider.ToProviderRealtimeEvent(event)
	if err != nil {
		if startsTurn {
			if finalizeErr := finalizeRealtimeTurnHooksOnTransportError(
				r.client,
				r.bifrostCtx,
				r.session,
				r.providerKey,
				r.model,
				r.key,
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
	r.sendUpstream(providerEvent, msg.IsString)
}

func (r *webrtcRealtimeRelay) handleUpstreamMessage(msg webrtc.DataChannelMessage) {
	event, err := r.provider.ToBifrostRealtimeEvent(msg.Data)
	if err != nil {
		if finalizeErr := finalizeRealtimeTurnHooksOnTransportError(
			r.client,
			r.bifrostCtx,
			r.session,
			r.providerKey,
			r.model,
			r.key,
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
			r.session.SetProviderSessionID(event.Session.ID)
		}
		inputItemID, inputSummary := pendingRealtimeInputUpdate(event)
		if inputSummary != "" {
			r.session.RecordRealtimeInput(inputItemID, inputSummary, string(msg.Data))
		}
		if event.Delta != nil && r.provider.ShouldAccumulateRealtimeOutput(event.Type) {
			r.session.AppendRealtimeOutputText(event.Delta.Text)
			r.session.AppendRealtimeOutputText(event.Delta.Transcript)
		}
		if r.provider.ShouldStartRealtimeTurn(event) && r.session.PeekRealtimeTurnHooks() == nil {
			if bifrostErr := startRealtimeTurnHooks(r.client, r.bifrostCtx, r.session, r.provider, r.providerKey, r.model, r.key, event.Type); bifrostErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
				return
			}
		}
	}
	if event != nil {
		if !r.provider.ShouldForwardRealtimeEvent(event) {
			return
		}
		if event.Type == r.provider.RealtimeTurnFinalEvent() {
			contentOverride := r.session.ConsumeRealtimeOutputText()
			if bifrostErr := finalizeRealtimeTurnHooks(r.client, r.bifrostCtx, r.session, r.provider, r.providerKey, r.model, r.key, msg.Data, contentOverride); bifrostErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
				return
			}
		} else if event.Error != nil {
			if finalizeErr := finalizeRealtimeTurnHooksWithError(
				r.client,
				r.bifrostCtx,
				r.session,
				r.providerKey,
				r.model,
				r.key,
				event.Type,
				msg.Data,
				newBifrostErrorFromRealtimeError(r.providerKey, r.model, msg.Data, event.Error),
			); finalizeErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(finalizeErr))
				return
			}
		}
		msg.Data, err = r.provider.ToProviderRealtimeEvent(event)
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

func (r *webrtcRealtimeRelay) sendUpstream(payload []byte, isString bool) {
	r.channelMu.Lock()
	defer r.channelMu.Unlock()
	if isDataChannelOpen(r.upstreamChannel) {
		sendDataChannelMessage(r.upstreamChannel, payload, isString)
		return
	}
	if len(r.pendingToUpstream) >= webrtcRealtimeMaxPendingMessages {
		logger.Warn("upstream pending buffer exceeded %d messages, closing relay", webrtcRealtimeMaxPendingMessages)
		go r.close()
		return
	}
	r.pendingToUpstream = append(r.pendingToUpstream, queuedDataChannelMessage{payload: append([]byte(nil), payload...), isString: isString})
}

func (r *webrtcRealtimeRelay) sendDownstream(payload []byte, isString bool) {
	r.channelMu.Lock()
	defer r.channelMu.Unlock()
	if isDataChannelOpen(r.downstreamChannel) {
		sendDataChannelMessage(r.downstreamChannel, payload, isString)
		return
	}
	if len(r.pendingToDownstream) >= webrtcRealtimeMaxPendingMessages {
		logger.Warn("downstream pending buffer exceeded %d messages, closing relay", webrtcRealtimeMaxPendingMessages)
		go r.close()
		return
	}
	r.pendingToDownstream = append(r.pendingToDownstream, queuedDataChannelMessage{payload: append([]byte(nil), payload...), isString: isString})
}

func (r *webrtcRealtimeRelay) flushPending() {
	r.channelMu.Lock()
	defer r.channelMu.Unlock()

	if isDataChannelOpen(r.upstreamChannel) && len(r.pendingToUpstream) > 0 {
		for _, msg := range r.pendingToUpstream {
			sendDataChannelMessage(r.upstreamChannel, msg.payload, msg.isString)
		}
		r.pendingToUpstream = nil
	}
	if isDataChannelOpen(r.downstreamChannel) && len(r.pendingToDownstream) > 0 {
		for _, msg := range r.pendingToDownstream {
			sendDataChannelMessage(r.downstreamChannel, msg.payload, msg.isString)
		}
		r.pendingToDownstream = nil
	}
}

func (r *webrtcRealtimeRelay) close() {
	r.closeOnce.Do(func() {
		if r.session != nil {
			_ = finalizeRealtimeTurnHooksOnTransportError(
				r.client,
				r.bifrostCtx,
				r.session,
				r.providerKey,
				r.model,
				r.key,
				502,
				"connection_closed",
				"realtime WebRTC session closed before turn completed",
			)
			r.session.ClearRealtimeTurnHooks()
		}

		if r.onClose != nil {
			r.onClose()
		}
		if r.cancel != nil {
			r.cancel()
		}

		r.channelMu.Lock()
		if r.downstreamChannel != nil {
			_ = r.downstreamChannel.Close()
		}
		if r.upstreamChannel != nil {
			_ = r.upstreamChannel.Close()
		}
		r.channelMu.Unlock()

		if r.downstreamPC != nil {
			_ = r.downstreamPC.Close()
		}
		if r.upstreamPC != nil {
			_ = r.upstreamPC.Close()
		}
	})
}

func (r *webrtcRealtimeRelay) closeWithShutdownSignal() {
	r.close()
}

func (r *webrtcRealtimeRelay) closeWithErrorEvent(payload []byte) {
	r.channelMu.Lock()
	dc := r.downstreamChannel
	r.channelMu.Unlock()

	if isDataChannelOpen(dc) && len(payload) > 0 {
		sendDataChannelMessage(dc, payload, true)
		go func() {
			time.Sleep(100 * time.Millisecond)
			r.close()
		}()
		return
	}

	r.close()
}

func (h *WebRTCRealtimeHandler) registerRelay(sessionID string, relay *webrtcRealtimeRelay) {
	if strings.TrimSpace(sessionID) == "" || relay == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.relays[sessionID] = relay
}

func (h *WebRTCRealtimeHandler) unregisterRelay(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.relays, sessionID)
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
		schemas.BifrostContextKeyGovernanceRoutingRuleID,
		schemas.BifrostContextKeyGovernanceRoutingRuleName,
		schemas.BifrostContextKeyGovernanceCustomerID,
		schemas.BifrostContextKeyGovernanceCustomerName,
		schemas.BifrostContextKeyGovernanceTeamID,
		schemas.BifrostContextKeyGovernanceTeamName,
		schemas.BifrostContextKeyUserID,
		schemas.BifrostContextKeyUserName,
		schemas.BifrostContextKeyGovernanceIncludeOnlyKeys,
		schemas.BifrostContextKeyGovernancePluginName,
		schemas.BifrostContextKeySelectedKeyID,
		schemas.BifrostContextKeySelectedKeyName,
		schemas.BifrostContextKeyIsEnterprise,
	} {
		if value := requestCtx.Value(key); value != nil {
			relayCtx.SetValue(key, value)
		}
	}

	return relayCtx, cancel
}

func newRealtimePeerConnection() (*webrtc.PeerConnection, error) {
	return webrtc.NewPeerConnection(webrtc.Configuration{})
}

func isDataChannelOpen(dc *webrtc.DataChannel) bool {
	return dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen
}

func realtimeEventTypeFromPayload(payload []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Type)
}

func parseRealtimeSDPMaxMessageSize(sdp string) (int64, bool) {
	matches := realtimeSDPMaxMessageSizePattern.FindStringSubmatch(sdp)
	if len(matches) < 2 {
		return 0, false
	}
	size, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || size <= 0 {
		return 0, false
	}
	return size, true
}

func setRealtimeSDPMaxMessageSize(sdp string, maxMessageSize int64) string {
	line := "a=max-message-size:" + strconv.FormatInt(maxMessageSize, 10)
	if realtimeSDPMaxMessageSizePattern.MatchString(sdp) {
		return realtimeSDPMaxMessageSizePattern.ReplaceAllString(sdp, line)
	}
	if strings.Contains(sdp, "\r\nm=application ") {
		return strings.Replace(sdp, "\r\nm=application ", "\r\n"+line+"\r\nm=application ", 1)
	}
	if strings.Contains(sdp, "\nm=application ") {
		return strings.Replace(sdp, "\nm=application ", "\n"+line+"\nm=application ", 1)
	}
	return sdp
}

func constrainRealtimeSDPMaxMessageSize(upstreamOffer string, browserOffer string) string {
	browserMax, ok := parseRealtimeSDPMaxMessageSize(browserOffer)
	if !ok {
		return upstreamOffer
	}

	upstreamMax, ok := parseRealtimeSDPMaxMessageSize(upstreamOffer)
	if ok && upstreamMax <= browserMax {
		return upstreamOffer
	}

	return setRealtimeSDPMaxMessageSize(upstreamOffer, browserMax)
}

func sendDataChannelMessage(dc *webrtc.DataChannel, payload []byte, isString bool) {
	if dc == nil {
		return
	}
	var err error
	if isString {
		err = dc.SendText(string(payload))
	} else {
		err = dc.Send(payload)
	}
	if err != nil {
		eventType := realtimeEventTypeFromPayload(payload)
		if eventType != "" {
			logger.Warn("failed to send realtime data channel message: type=%s size=%d bytes err=%v", eventType, len(payload), err)
			return
		}
		logger.Warn("failed to send realtime data channel message: size=%d bytes err=%v", len(payload), err)
	}
}

func resolveRealtimeSDPTarget(path string, sessionJSON []byte) (schemas.ModelProvider, string, []byte, *schemas.BifrostError) {
	root, err := schemas.ParseRealtimeClientSecretBody(sessionJSON)
	if err != nil {
		return "", "", nil, err
	}

	modelJSON, ok := root["model"]
	if !ok {
		return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model is required", nil)
	}

	var rawModel string
	if err := json.Unmarshal(modelJSON, &rawModel); err != nil {
		return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model must be a string", err)
	}

	providerKey, model := schemas.ParseModelString(strings.TrimSpace(rawModel), realtimeDefaultProviderForPath(path))
	if providerKey == "" || strings.TrimSpace(model) == "" {
		if realtimeDefaultProviderForPath(path) == "" {
			return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model must use provider/model on /v1 realtime routes", nil)
		}
		return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session.model is required", nil)
	}

	normalizedModel, marshalErr := json.Marshal(model)
	if marshalErr != nil {
		return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to encode normalized session model", marshalErr)
	}
	root["model"] = normalizedModel
	normalizedSession, marshalErr := json.Marshal(root)
	if marshalErr != nil {
		return "", "", nil, newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to encode normalized realtime session", marshalErr)
	}

	return providerKey, strings.TrimSpace(model), normalizedSession, nil
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
