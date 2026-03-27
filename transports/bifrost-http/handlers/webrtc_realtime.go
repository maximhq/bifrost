package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/valyala/fasthttp"
)

const (
	webrtcRealtimeHandshakeTimeout = 10 * time.Second
	webrtcRealtimeICEGatherTimeout = 3 * time.Second
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
	turnLogger   *realtimeTurnLogger
	httpClient   *http.Client
	mu           sync.Mutex
	relays       map[string]*webrtcRealtimeRelay
}

func NewWebRTCRealtimeHandler(client *bifrost.Bifrost, config *lib.Config, notifyLog func(*logstore.Log)) *WebRTCRealtimeHandler {
	return &WebRTCRealtimeHandler{
		client:       client,
		config:       config,
		handlerStore: config,
		turnLogger:   newRealtimeTurnLogger(config, notifyLog),
		httpClient: &http.Client{
			Timeout: webrtcRealtimeHandshakeTimeout,
		},
		relays: make(map[string]*webrtcRealtimeRelay),
	}
}

func (h *WebRTCRealtimeHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleRequest, middlewares...)
	r.POST("/v1/realtime", handler)
	for _, path := range integrations.OpenAIRealtimePaths("/openai") {
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
	if !strings.HasPrefix(strings.ToLower(string(ctx.Request.Header.ContentType())), "multipart/form-data") {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "Content-Type must be multipart/form-data", nil))
		return
	}

	authorization := strings.TrimSpace(string(ctx.Request.Header.Peek("Authorization")))
	if authorization == "" {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusUnauthorized, "invalid_request_error", "Authorization header is required", nil))
		return
	}

	form, err := ctx.MultipartForm()
	if err != nil {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "failed to parse multipart form", err))
		return
	}

	sdpOffer := firstMultipartValue(form.Value, "sdp")
	if strings.TrimSpace(sdpOffer) == "" {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "sdp form field is required", nil))
		return
	}

	sessionField := firstMultipartValue(form.Value, "session")
	if strings.TrimSpace(sessionField) == "" {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "session form field is required", nil))
		return
	}

	providerKey, model, normalizedSession, bifrostErr := resolveRealtimeSDPTarget(string(ctx.Path()), []byte(sessionField))
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr)
		return
	}

	provider := h.client.GetProviderByKey(providerKey)
	if provider == nil {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "provider not found: "+string(providerKey), nil))
		return
	}

	rtProvider, ok := provider.(schemas.RealtimeProvider)
	if !ok || !rtProvider.SupportsRealtimeAPI() {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "provider does not support realtime: "+string(providerKey), nil))
		return
	}

	upstreamURL := strings.TrimSpace(rtProvider.RealtimeWebRTCURL(model))
	if upstreamURL == "" {
		SendBifrostError(ctx, newRealtimeWebRTCError(fasthttp.StatusBadRequest, "invalid_request_error", "provider does not support realtime WebRTC: "+string(providerKey), nil))
		return
	}

	bifrostCtx, cancel := lib.ConvertToBifrostContext(
		ctx,
		h.handlerStore.ShouldAllowDirectKeys(),
		h.config.GetHeaderMatcher(),
		h.config.GetMCPHeaderCombinedAllowlist(),
	)
	defer cancel()
	bifrostCtx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.RealtimeRequest)
	if strings.HasPrefix(string(ctx.Path()), "/openai") {
		bifrostCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
	}
	relayCtx, relayCancel := newRealtimeRelayContext(bifrostCtx)

	session := bfws.NewSession(nil)
	answer, relayErr := h.establishRelay(bifrostCtx, relayCtx, relayCancel, session, rtProvider, providerKey, model, authorization, normalizedSession, sdpOffer, ctx)
	if relayErr != nil {
		relayCancel()
		SendBifrostError(ctx, relayErr)
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/sdp")
	ctx.SetBodyString(answer)
}

func (h *WebRTCRealtimeHandler) establishRelay(
	requestCtx *schemas.BifrostContext,
	relayCtx *schemas.BifrostContext,
	relayCancel context.CancelFunc,
	session *bfws.Session,
	provider schemas.RealtimeProvider,
	providerKey schemas.ModelProvider,
	model string,
	authorization string,
	normalizedSession []byte,
	browserOffer string,
	ctx *fasthttp.RequestCtx,
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
		turnLogger:   h.turnLogger,
		bifrostCtx:   relayCtx,
		cancel:       relayCancel,
		provider:     provider,
		providerKey:  providerKey,
		model:        model,
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
	relay.installDataChannelRelay()

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

	upstreamCtx, cancel := context.WithTimeout(requestCtx, webrtcRealtimeHandshakeTimeout)
	defer cancel()

	upstreamAnswer, upstreamErr := h.exchangeUpstreamSDP(upstreamCtx, provider, authorization, normalizedSession, upstreamOffer, string(ctx.Request.Header.Peek("X-OpenAI-Agents-SDK")), providerKey, model)
	if upstreamErr != nil {
		relay.close()
		return "", upstreamErr
	}
	if err := upstreamPC.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  upstreamAnswer,
	}); err != nil {
		relay.close()
		return "", newRealtimeWebRTCError(fasthttp.StatusBadGateway, "upstream_connection_error", "invalid upstream SDP answer", err)
	}

	if err := relay.waitForUpstream(upstreamCtx); err != nil {
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

func (h *WebRTCRealtimeHandler) exchangeUpstreamSDP(
	ctx context.Context,
	provider schemas.RealtimeProvider,
	authorization string,
	sessionJSON []byte,
	sdp string,
	agentsSDKHeader string,
	providerKey schemas.ModelProvider,
	model string,
) (string, *schemas.BifrostError) {
	bodyBuf := &bytes.Buffer{}
	writer := multipart.NewWriter(bodyBuf)
	if err := writer.WriteField("sdp", sdp); err != nil {
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to encode upstream SDP body", err)
	}
	if err := writer.WriteField("session", string(sessionJSON)); err != nil {
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to encode upstream session body", err)
	}
	if err := writer.Close(); err != nil {
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to finalize upstream SDP body", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.RealtimeWebRTCURL(model), bodyBuf)
	if err != nil {
		return "", newRealtimeWebRTCError(fasthttp.StatusInternalServerError, "server_error", "failed to build upstream realtime request", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	for k, v := range provider.RealtimeWebRTCHeaders(authorization) {
		req.Header.Set(k, v)
	}
	if strings.TrimSpace(agentsSDKHeader) != "" {
		req.Header.Set("X-OpenAI-Agents-SDK", agentsSDKHeader)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", newRealtimeWebRTCError(fasthttp.StatusBadGateway, "upstream_connection_error", "upstream realtime handshake timed out", err)
		}
		return "", newRealtimeWebRTCError(fasthttp.StatusBadGateway, "upstream_connection_error", "failed to call upstream realtime WebRTC endpoint", err)
	}
	defer resp.Body.Close()

	answerBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return "", newRealtimeWebRTCError(fasthttp.StatusBadGateway, "upstream_connection_error", "failed to read upstream SDP answer", readErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(fasthttp.StatusBadGateway),
			Error: &schemas.ErrorField{
				Type:    schemas.Ptr("upstream_connection_error"),
				Message: fmt.Sprintf("upstream realtime WebRTC handshake failed for %s/%s", providerKey, model),
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType:    schemas.RealtimeRequest,
				Provider:       providerKey,
				ModelRequested: model,
				RawResponse: map[string]any{
					"status": resp.StatusCode,
					"body":   string(answerBody),
				},
			},
		}
	}

	return string(answerBody), nil
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
	turnLogger  *realtimeTurnLogger
	bifrostCtx  *schemas.BifrostContext
	cancel      context.CancelFunc
	provider    schemas.RealtimeProvider
	providerKey schemas.ModelProvider
	model       string
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

func (r *webrtcRealtimeRelay) installDataChannelRelay() {
	label := strings.TrimSpace(r.provider.RealtimeWebRTCDataChannelLabel())
	if label == "" {
		return
	}
	upstreamDC, err := r.upstreamPC.CreateDataChannel(label, nil)
	if err == nil {
		r.bindUpstreamChannel(upstreamDC)
	}

	r.downstreamPC.OnDataChannel(func(dc *webrtc.DataChannel) {
		r.bindDownstreamChannel(dc)
	})
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
	if toolSummary := finalizedRealtimeToolOutputSummary(event); toolSummary != "" {
		r.session.AddRealtimeToolOutput(toolSummary, string(msg.Data))
	}
	if inputSummary := finalizedRealtimeInputSummary(event); inputSummary != "" {
		r.session.SetRealtimeInputText(inputSummary)
		r.session.SetRealtimeInputRaw(string(msg.Data))
	}
	if startEvent := r.provider.RealtimeTurnStartEvent(); startEvent != "" && event.Type == startEvent {
		if r.session.PeekRealtimeTurnHooks() != nil {
			r.sendDownstream(newRealtimeTurnErrorEventPayload(newRealtimeWireBifrostError(400, "invalid_request_error", "Conversation already has an active response in progress.")), true)
			return
		}
		if bifrostErr := startRealtimeTurnHooks(r.client, r.bifrostCtx, r.session, r.provider, r.providerKey, r.model, nil); bifrostErr != nil {
			r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
			return
		}
	}

	providerEvent, err := r.provider.ToProviderRealtimeEvent(event)
	if err != nil {
		logger.Warn("failed to translate browser realtime event: %v", err)
		r.sendUpstream(msg.Data, msg.IsString)
		return
	}
	r.sendUpstream(providerEvent, msg.IsString)
}

func (r *webrtcRealtimeRelay) handleUpstreamMessage(msg webrtc.DataChannelMessage) {
	event, err := r.provider.ToBifrostRealtimeEvent(msg.Data)
	if err != nil {
		logger.Warn("failed to translate upstream realtime event: %v", err)
		r.sendDownstream(msg.Data, msg.IsString)
		return
	}
	if event != nil {
		if event.Session != nil && event.Session.ID != "" {
			r.session.SetProviderSessionID(event.Session.ID)
		}
		if inputSummary := finalizedRealtimeInputSummary(event); inputSummary != "" {
			r.session.SetRealtimeInputText(inputSummary)
			r.session.SetRealtimeInputRaw(string(msg.Data))
		}
		if event.Delta != nil && r.provider.ShouldAccumulateRealtimeOutput(event.Type) {
			r.session.AppendRealtimeOutputText(event.Delta.Text)
			r.session.AppendRealtimeOutputText(event.Delta.Transcript)
		}
	}
	if event != nil {
		if !r.provider.ShouldForwardRealtimeEvent(event) {
			return
		}
		if event.Type == r.provider.RealtimeTurnFinalEvent() {
			contentOverride := r.session.ConsumeRealtimeOutputText()
			if bifrostErr := finalizeRealtimeTurnHooks(r.client, r.bifrostCtx, r.session, r.provider, r.providerKey, r.model, nil, msg.Data, contentOverride); bifrostErr != nil {
				r.closeWithErrorEvent(newRealtimeTurnErrorEventPayload(bifrostErr))
				return
			}
		}
		msg.Data, err = r.provider.ToProviderRealtimeEvent(event)
		if err != nil {
			logger.Warn("failed to encode translated realtime event: %v", err)
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
	r.pendingToUpstream = append(r.pendingToUpstream, queuedDataChannelMessage{payload: append([]byte(nil), payload...), isString: isString})
}

func (r *webrtcRealtimeRelay) sendDownstream(payload []byte, isString bool) {
	r.channelMu.Lock()
	defer r.channelMu.Unlock()
	if isDataChannelOpen(r.downstreamChannel) {
		sendDataChannelMessage(r.downstreamChannel, payload, isString)
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
		if r.onClose != nil {
			r.onClose()
		}
		if r.cancel != nil {
			r.cancel()
		}
		if r.downstreamChannel != nil {
			_ = r.downstreamChannel.Close()
		}
		if r.upstreamChannel != nil {
			_ = r.upstreamChannel.Close()
		}
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
		schemas.BifrostContextKeyHTTPRequestType,
		schemas.BifrostContextKeyIntegrationType,
		schemas.BifrostContextKeyVirtualKey,
		schemas.BifrostContextKeyAPIKeyName,
		schemas.BifrostContextKeyAPIKeyID,
		schemas.BifrostContextKeyDirectKey,
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
		schemas.BifrostContextKeyGovernanceUserID,
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

func realtimeSDPMaxMessageSize(sdp string) string {
	matches := realtimeSDPMaxMessageSizePattern.FindStringSubmatch(sdp)
	if len(matches) < 2 {
		return "absent"
	}
	return matches[1]
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
