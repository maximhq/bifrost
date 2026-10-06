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

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/valyala/fasthttp"
)

const (
	webrtcHandshakeTimeout   = 10 * time.Second
	webrtcICEGatherTimeout   = 3 * time.Second
	webrtcMaxPendingMessages = 1000
)

var defaultAudioCodec = webrtc.RTPCodecCapability{
	MimeType:    webrtc.MimeTypeOpus,
	ClockRate:   48000,
	Channels:    2,
	SDPFmtpLine: "minptime=10;useinbandfec=1",
}

var sdpMaxMessageSizePattern = regexp.MustCompile(`(?m)^a=max-message-size:(\d+)\s*$`)

// webrtcMessageHandler is what an API plugs into a WebRTC relay. The relay owns the peer
// connections, audio tracks and data-channel queueing; the handler owns the API's events.
type webrtcMessageHandler interface {
	// fromBrowser and fromProvider receive each data-channel message and decide what is sent on.
	fromBrowser(relay *webrtcRelay, msg webrtc.DataChannelMessage)
	fromProvider(relay *webrtcRelay, msg webrtc.DataChannelMessage)
	// browserGone runs when the browser's connection or data channel ends before the provider's.
	browserGone(relay *webrtcRelay)
	// closed runs once, as the relay closes.
	closed()
}

// webrtcRelaySetup is what establishWebRTCRelay needs from the API it relays.
type webrtcRelaySetup struct {
	requestType      schemas.RequestType // the relayed API, for errors
	handler          webrtcMessageHandler
	dataChannelLabel string // empty for an API without a data channel
	browserOffer     string
	handshakeCtx     context.Context
	// exchangeSDP sends the relay's upstream offer to the provider and returns its SDP answer.
	exchangeSDP func(upstreamOffer string) (string, *schemas.BifrostError)
	// onCreate registers the relay as soon as it exists, so shutdown can reach it mid-handshake.
	onCreate func(relay *webrtcRelay)
	onClose  func()
	cancel   context.CancelFunc
}

// webrtcRelay terminates WebRTC on both sides: one peer connection to the browser, one to the
// provider. Audio RTP is forwarded as packets and never decoded.
type webrtcRelay struct {
	downstreamPC *webrtc.PeerConnection
	upstreamPC   *webrtc.PeerConnection

	downstreamChannel *webrtc.DataChannel
	upstreamChannel   *webrtc.DataChannel

	providerToBrowserTrack *webrtc.TrackLocalStaticRTP
	browserToProviderTrack *webrtc.TrackLocalStaticRTP

	handler          webrtcMessageHandler
	dataChannelLabel string
	cancel           context.CancelFunc
	onClose          func()

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

// establishWebRTCRelay sets up the bidirectional relay between the browser and the provider and
// returns the SDP answer for the browser.
func establishWebRTCRelay(setup webrtcRelaySetup) (string, *schemas.BifrostError) {
	downstreamPC, err := newWebRTCPeerConnection()
	if err != nil {
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to create browser peer connection", err)
	}
	upstreamPC, err := newWebRTCPeerConnection()
	if err != nil {
		_ = downstreamPC.Close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to create upstream peer connection", err)
	}

	relay := &webrtcRelay{
		downstreamPC:     downstreamPC,
		upstreamPC:       upstreamPC,
		handler:          setup.handler,
		dataChannelLabel: strings.TrimSpace(setup.dataChannelLabel),
		cancel:           setup.cancel,
		onClose:          setup.onClose,
	}
	relay.installCloseHandlers()
	if setup.onCreate != nil {
		setup.onCreate(relay)
	}

	// Downstream local audio track carries provider audio back to the browser.
	providerToBrowserTrack, err := webrtc.NewTrackLocalStaticRTP(defaultAudioCodec, "audio", "bifrost-provider-audio")
	if err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to create browser audio track", err)
	}
	providerToBrowserSender, err := downstreamPC.AddTrack(providerToBrowserTrack)
	if err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to attach browser audio track", err)
	}
	relay.providerToBrowserTrack = providerToBrowserTrack
	go relay.forwardRTCP(providerToBrowserSender, upstreamPC)

	// Upstream local audio track carries browser audio to the provider.
	browserToProviderTrack, err := webrtc.NewTrackLocalStaticRTP(defaultAudioCodec, "audio", "bifrost-browser-audio")
	if err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to create provider audio track", err)
	}
	browserToProviderSender, err := upstreamPC.AddTrack(browserToProviderTrack)
	if err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to attach provider audio track", err)
	}
	relay.browserToProviderTrack = browserToProviderTrack
	go relay.forwardRTCP(browserToProviderSender, downstreamPC)

	relay.installTrackForwarders()
	if err := relay.installDataChannelRelay(); err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to create upstream data channel", err)
	}

	if err := setRemoteDescription(downstreamPC, webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  setup.browserOffer,
	}); err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusBadRequest, "invalid_request_error", "invalid browser SDP offer", err)
	}

	upstreamOffer, err := relay.createOffer(upstreamPC)
	if err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to create upstream SDP offer", err)
	}
	upstreamOffer = constrainSDPMaxMessageSize(upstreamOffer, setup.browserOffer)

	upstreamAnswer, exchangeErr := setup.exchangeSDP(upstreamOffer)
	if exchangeErr != nil {
		relay.close()
		return "", exchangeErr
	}

	if err := setRemoteDescription(upstreamPC, webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  upstreamAnswer,
	}); err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusBadGateway, "upstream_connection_error", "invalid upstream SDP answer", err)
	}

	waitCtx, waitCancel := context.WithTimeout(setup.handshakeCtx, webrtcHandshakeTimeout)
	defer waitCancel()

	if err := relay.waitForUpstream(waitCtx); err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusBadGateway, "upstream_connection_error", "upstream WebRTC connection failed", err)
	}

	browserAnswer, err := relay.createAnswer(downstreamPC)
	if err != nil {
		relay.close()
		return "", setup.fail(fasthttp.StatusInternalServerError, "server_error", "failed to create browser SDP answer", err)
	}

	return browserAnswer, nil
}

// fail builds a relay setup error attributed to the relayed API.
func (setup webrtcRelaySetup) fail(status int, errorType, message string, err error) *schemas.BifrostError {
	bifrostErr := newRealtimeWebRTCError(status, errorType, message, err)
	bifrostErr.ExtraFields.RequestType = setup.requestType
	return bifrostErr
}

// installCloseHandlers closes the relay when the provider side ends, and lets the handler decide
// what the browser side ending means.
func (r *webrtcRelay) installCloseHandlers() {
	r.upstreamConnectedOrError = make(chan error, 1)

	r.upstreamPC.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			select {
			case r.upstreamConnectedOrError <- nil:
			default:
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			select {
			case r.upstreamConnectedOrError <- fmt.Errorf("peer connection state %s", state.String()):
			default:
			}
			r.close()
		case webrtc.PeerConnectionStateDisconnected:
			r.close()
		}
	})
	r.downstreamPC.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			r.handler.browserGone(r)
		}
	})
}

func (r *webrtcRelay) installTrackForwarders() {
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

func (r *webrtcRelay) installDataChannelRelay() error {
	if r.dataChannelLabel == "" {
		return nil
	}
	upstreamDC, err := r.upstreamPC.CreateDataChannel(r.dataChannelLabel, nil)
	if err != nil {
		return err
	}
	r.bindUpstreamChannel(upstreamDC)

	r.downstreamPC.OnDataChannel(func(dc *webrtc.DataChannel) {
		r.bindDownstreamChannel(dc)
	})
	return nil
}

func (r *webrtcRelay) bindUpstreamChannel(dc *webrtc.DataChannel) {
	r.channelMu.Lock()
	r.upstreamChannel = dc
	r.channelMu.Unlock()

	dc.OnOpen(func() {
		r.flushPending()
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		r.handler.fromProvider(r, msg)
	})
	dc.OnClose(func() { r.close() })
	dc.OnError(func(err error) {
		logger.Warn("upstream WebRTC data channel error: %v", err)
		r.close()
	})
}

func (r *webrtcRelay) bindDownstreamChannel(dc *webrtc.DataChannel) {
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
		r.handler.fromBrowser(r, msg)
	})
	dc.OnClose(func() { r.handler.browserGone(r) })
	dc.OnError(func(err error) {
		logger.Warn("browser WebRTC data channel error: %v", err)
		r.handler.browserGone(r)
	})
}

func (r *webrtcRelay) createOffer(pc *webrtc.PeerConnection) (string, error) {
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
	case <-time.After(webrtcICEGatherTimeout):
	}
	if pc.LocalDescription() == nil {
		return "", errors.New("local description not set")
	}
	return pc.LocalDescription().SDP, nil
}

func (r *webrtcRelay) createAnswer(pc *webrtc.PeerConnection) (string, error) {
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
	case <-time.After(webrtcICEGatherTimeout):
	}
	if pc.LocalDescription() == nil {
		return "", errors.New("local description not set")
	}
	return pc.LocalDescription().SDP, nil
}

func (r *webrtcRelay) waitForUpstream(ctx context.Context) error {
	select {
	case err := <-r.upstreamConnectedOrError:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *webrtcRelay) forwardRTPTrack(track *webrtc.TrackRemote, target *webrtc.TrackLocalStaticRTP) {
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

func (r *webrtcRelay) forwardRTCP(sender *webrtc.RTPSender, target *webrtc.PeerConnection) {
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

func (r *webrtcRelay) sendUpstream(payload []byte, isString bool) {
	r.channelMu.Lock()
	defer r.channelMu.Unlock()
	if isDataChannelOpen(r.upstreamChannel) {
		sendDataChannelMessage(r.upstreamChannel, payload, isString)
		return
	}
	if len(r.pendingToUpstream) >= webrtcMaxPendingMessages {
		logger.Warn("upstream pending buffer exceeded %d messages, closing relay", webrtcMaxPendingMessages)
		go r.close()
		return
	}
	r.pendingToUpstream = append(r.pendingToUpstream, queuedDataChannelMessage{payload: append([]byte(nil), payload...), isString: isString})
}

func (r *webrtcRelay) sendDownstream(payload []byte, isString bool) {
	r.channelMu.Lock()
	defer r.channelMu.Unlock()
	if isDataChannelOpen(r.downstreamChannel) {
		sendDataChannelMessage(r.downstreamChannel, payload, isString)
		return
	}
	if len(r.pendingToDownstream) >= webrtcMaxPendingMessages {
		logger.Warn("downstream pending buffer exceeded %d messages, closing relay", webrtcMaxPendingMessages)
		go r.close()
		return
	}
	r.pendingToDownstream = append(r.pendingToDownstream, queuedDataChannelMessage{payload: append([]byte(nil), payload...), isString: isString})
}

func (r *webrtcRelay) flushPending() {
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

func (r *webrtcRelay) close() {
	r.closeOnce.Do(func() {
		if r.handler != nil {
			r.handler.closed()
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

func (r *webrtcRelay) closeWithShutdownSignal() {
	r.close()
}

// closeWithErrorEvent sends a final event to the browser, then closes the relay shortly after.
func (r *webrtcRelay) closeWithErrorEvent(payload []byte) {
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

// webrtcRelayRegistry tracks a handler's open relays so shutdown can close them.
type webrtcRelayRegistry struct {
	mu     sync.Mutex
	relays map[string]*webrtcRelay
}

func (reg *webrtcRelayRegistry) registerRelay(sessionID string, relay *webrtcRelay) {
	if strings.TrimSpace(sessionID) == "" || relay == nil {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.relays == nil {
		reg.relays = make(map[string]*webrtcRelay)
	}
	reg.relays[sessionID] = relay
}

func (reg *webrtcRelayRegistry) unregisterRelay(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	delete(reg.relays, sessionID)
}

func (reg *webrtcRelayRegistry) closeAll() {
	reg.mu.Lock()
	relays := make([]*webrtcRelay, 0, len(reg.relays))
	for _, relay := range reg.relays {
		relays = append(relays, relay)
	}
	reg.mu.Unlock()

	for _, relay := range relays {
		relay.closeWithShutdownSignal()
	}
}

func newWebRTCPeerConnection() (*webrtc.PeerConnection, error) {
	return webrtc.NewPeerConnection(webrtc.Configuration{})
}

// setRemoteDescription applies desc to pc and converts a panic raised inside the
// SDP parser into an error. The description is caller-supplied bytes, and the
// handler runs on the request goroutine, so a parser panic that escaped here
// would end the whole process instead of failing the one request.
func setRemoteDescription(pc *webrtc.PeerConnection, desc webrtc.SessionDescription) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sdp rejected: %v", r)
		}
	}()
	return pc.SetRemoteDescription(desc)
}

func isDataChannelOpen(dc *webrtc.DataChannel) bool {
	return dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen
}

func dataChannelEventType(payload []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Type)
}

func parseSDPMaxMessageSize(sdp string) (int64, bool) {
	matches := sdpMaxMessageSizePattern.FindStringSubmatch(sdp)
	if len(matches) < 2 {
		return 0, false
	}
	size, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || size <= 0 {
		return 0, false
	}
	return size, true
}

func setSDPMaxMessageSize(sdp string, maxMessageSize int64) string {
	line := "a=max-message-size:" + strconv.FormatInt(maxMessageSize, 10)
	if sdpMaxMessageSizePattern.MatchString(sdp) {
		return sdpMaxMessageSizePattern.ReplaceAllString(sdp, line)
	}
	if strings.Contains(sdp, "\r\nm=application ") {
		return strings.Replace(sdp, "\r\nm=application ", "\r\n"+line+"\r\nm=application ", 1)
	}
	if strings.Contains(sdp, "\nm=application ") {
		return strings.Replace(sdp, "\nm=application ", "\n"+line+"\nm=application ", 1)
	}
	return sdp
}

func constrainSDPMaxMessageSize(upstreamOffer string, browserOffer string) string {
	browserMax, ok := parseSDPMaxMessageSize(browserOffer)
	if !ok {
		return upstreamOffer
	}

	upstreamMax, ok := parseSDPMaxMessageSize(upstreamOffer)
	if ok && upstreamMax <= browserMax {
		return upstreamOffer
	}

	return setSDPMaxMessageSize(upstreamOffer, browserMax)
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
		eventType := dataChannelEventType(payload)
		if eventType != "" {
			logger.Warn("failed to send WebRTC data channel message: type=%s size=%d bytes err=%v", eventType, len(payload), err)
			return
		}
		logger.Warn("failed to send WebRTC data channel message: size=%d bytes err=%v", len(payload), err)
	}
}
