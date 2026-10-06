package handlers

import (
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/pion/webrtc/v4"
	"github.com/valyala/fasthttp"
)

const (
	// liveWebRTCDataChannelLabel is the data channel GPT Live carries its JSON events on.
	liveWebRTCDataChannelLabel = "oai-events"
	// liveWebRTCMinimumSeconds is what OpenAI bills a WebRTC session when it is created, credited
	// against its running time.
	liveWebRTCMinimumSeconds = 15.0
)

// WebRTCLiveHandler creates GPT Live WebRTC sessions and relays them. Bifrost terminates WebRTC on
// both sides, so every data-channel event passes the same checks and metering as the WebSocket.
type WebRTCLiveHandler struct {
	webrtcRelayRegistry
	gateway *liveGateway
}

// NewWebRTCLiveHandler creates a new GPT Live WebRTC handler.
func NewWebRTCLiveHandler(client *bifrost.Bifrost, config *lib.Config) *WebRTCLiveHandler {
	return &WebRTCLiveHandler{gateway: &liveGateway{client: client, config: config, handlerStore: config}}
}

// RegisterRoutes registers session create at the base path and the OpenAI integration paths. The
// WebSocket handler serves GET on the same paths.
func (h *WebRTCLiveHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleCreate, middlewares...)
	r.POST("/v1/live/sessions", handler)
	for _, path := range integrations.OpenAILivePaths("/openai") {
		r.POST(path, handler)
	}
}

func (h *WebRTCLiveHandler) Close() {
	if h == nil {
		return
	}
	h.closeAll()
}

// liveCreateRequest is the WebRTC create body. It is relayed as sent, apart from model names and
// the SDP offer, which Bifrost replaces with its own.
type liveCreateRequest struct {
	Session   *schemas.LiveSession   `json:"session"`
	Transport *schemas.LiveTransport `json:"transport"`
}

func (h *WebRTCLiveHandler) handleCreate(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	body := append([]byte(nil), ctx.PostBody()...)
	var create liveCreateRequest
	if err := schemas.Unmarshal(body, &create); err != nil {
		SendBifrostError(ctx, newRealtimeWireBifrostError(400, "invalid_request_error", "the request body must be JSON with session and transport"))
		return
	}
	if create.Transport == nil || create.Transport.Type != "webrtc" || strings.TrimSpace(create.Transport.SDP) == "" {
		SendBifrostError(ctx, newRealtimeWireBifrostError(400, "invalid_request_error", `transport must be {"type": "webrtc", "sdp": "<SDP offer>"}`))
		return
	}
	if create.Session == nil || strings.TrimSpace(create.Session.Model) == "" {
		SendBifrostError(ctx, newRealtimeWireBifrostError(400, "invalid_request_error", "session.model is required"))
		return
	}

	auth := captureAuthHeaders(ctx)
	preReqCtx, preReqCancel := createBifrostContextFromAuth(h.gateway.handlerStore, auth)
	defer preReqCancel()
	preReqCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.LiveRequest)
	if strings.HasPrefix(path, "/openai") {
		preReqCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}
	if authErr := h.gateway.refuseAnonymous(preReqCtx); authErr != nil {
		SendBifrostError(ctx, authErr)
		return
	}
	populateRealtimeRequestContext(ctx, preReqCtx)
	middlewareValues := snapshotRealtimeMiddlewareValues(ctx)

	target, bifrostErr := h.gateway.resolveTarget(preReqCtx, path, create.Session)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}
	sessionID := uuid.NewString()
	admission, bifrostErr := h.gateway.admit(auth, preReqCtx, middlewareValues, path, target, sessionID)
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}
	admission.meter.setTransport("webrtc")
	body, err := rewriteLiveModels(body, create.Session, admission.key, target.voiceModel, target.backendModel)
	if err != nil {
		bifrostErr := newRealtimeWireBifrostError(500, "server_error", "failed to prepare the session: "+err.Error())
		admission.meter.abort(bifrostErr)
		admission.cancel()
		SendBifrostError(ctx, bifrostErr)
		return
	}

	messages := &liveWebRTCMessages{}
	messages.liveSessionController = admission.controller(h.gateway, messages)
	var created *schemas.LiveCreateResponse
	browserAnswer, bifrostErr := establishWebRTCRelay(webrtcRelaySetup{
		requestType:      schemas.LiveRequest,
		handler:          messages,
		dataChannelLabel: liveWebRTCDataChannelLabel,
		browserOffer:     create.Transport.SDP,
		handshakeCtx:     admission.ctx,
		exchangeSDP: func(upstreamOffer string) (string, *schemas.BifrostError) {
			created, bifrostErr = createLiveWebRTCSession(admission, body, upstreamOffer)
			if bifrostErr != nil {
				return "", bifrostErr
			}
			return created.Transport.SDP, nil
		},
		onCreate: func(relay *webrtcRelay) {
			messages.relay = relay
			h.registerRelay(sessionID, relay)
		},
		onClose: func() { h.unregisterRelay(sessionID) },
		cancel:  admission.cancel,
	})
	if bifrostErr != nil {
		// A relay that failed before it existed has not billed or released its admission yet.
		admission.meter.abort(bifrostErr)
		admission.cancel()
		SendBifrostError(ctx, bifrostErr)
		return
	}
	go messages.watchStale()

	response, err := schemas.Marshal(schemas.LiveCreateResponse{
		Session:   &schemas.LiveSession{ID: created.Session.ID},
		Transport: &schemas.LiveTransport{Type: "webrtc", SDP: browserAnswer},
	})
	if err != nil {
		messages.relay.close()
		SendBifrostError(ctx, newRealtimeWireBifrostError(500, "server_error", "failed to encode the session response"))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusCreated)
	ctx.SetContentType("application/json")
	ctx.SetBody(response)
}

// createLiveWebRTCSession creates the session at the provider with Bifrost's own SDP offer, since
// Bifrost, not the browser, is the provider's WebRTC peer.
func createLiveWebRTCSession(admission *liveAdmission, body []byte, upstreamOffer string) (*schemas.LiveCreateResponse, *schemas.BifrostError) {
	offer, err := schemas.Marshal(upstreamOffer)
	if err != nil {
		return nil, newRealtimeWireBifrostError(500, "server_error", "failed to encode the SDP offer")
	}
	upstreamBody, err := providerUtils.SetRawJSONField(body, "transport.sdp", offer)
	if err != nil {
		return nil, newRealtimeWireBifrostError(500, "server_error", "failed to prepare the session: "+err.Error())
	}
	created, bifrostErr := admission.provider.CreateLiveWebRTCSession(admission.ctx, admission.key, upstreamBody)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	admission.meter.setMinimumSeconds(liveWebRTCMinimumSeconds)
	admission.meter.setProviderSessionID(created.Session.ID)
	return created, nil
}

// liveWebRTCMessages carries a live session's data channel through the session controller. Audio
// travels on the media tracks and never reaches it.
type liveWebRTCMessages struct {
	*liveSessionController
	relay *webrtcRelay
}

func (m *liveWebRTCMessages) sendUpstream(message []byte) error {
	m.relay.sendUpstream(message, true)
	return nil
}

func (m *liveWebRTCMessages) sendClient(message []byte) error {
	m.relay.sendDownstream(message, true)
	return nil
}

func (m *liveWebRTCMessages) abandonUpstream() {
	m.relay.close()
}

func (m *liveWebRTCMessages) fromBrowser(r *webrtcRelay, msg webrtc.DataChannelMessage) {
	if forward, ok := m.fromClient(msg.Data); ok {
		r.sendUpstream(forward, msg.IsString)
	}
}

func (m *liveWebRTCMessages) fromProvider(r *webrtcRelay, msg webrtc.DataChannelMessage) {
	sessionClosed := m.fromUpstream(msg.Data)
	m.forwardToClient(msg.Data)
	if sessionClosed {
		// Let session.closed reach the browser before the relay goes.
		go func() {
			time.Sleep(100 * time.Millisecond)
			r.close()
		}()
	}
}

// browserGone closes the session upstream and keeps the relay until session.closed, so the
// final usage is still billed.
func (m *liveWebRTCMessages) browserGone(_ *webrtcRelay) {
	m.clientLeft()
}

// closed bills what OpenAI last reported when the relay ends before session.closed.
func (m *liveWebRTCMessages) closed() {
	// A relay closed during setup never ran a session: establishment then fails and the create
	// handler aborts the meter with the failure, instead of finish billing a minimum as a success.
	if m.relay.closedDuringSetup() {
		return
	}
	m.upstreamEnded()
}
