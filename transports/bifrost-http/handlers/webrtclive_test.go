package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

func TestLiveWebRTCCreateRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	h := &WebRTCLiveHandler{gateway: &liveGateway{}}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"not json", `{"session":`, "must be JSON"},
		{"no transport", `{"session":{"model":"gpt-live-1"}}`, "transport must be"},
		{"not webrtc", `{"session":{"model":"gpt-live-1"},"transport":{"type":"websocket","sdp":"v=0"}}`, "transport must be"},
		{"no sdp", `{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":" "}}`, "transport must be"},
		{"no model", `{"session":{},"transport":{"type":"webrtc","sdp":"v=0"}}`, "session.model is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/v1/live/sessions")
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.SetBodyString(tc.body)
			h.handleCreate(ctx)
			assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
			assert.Contains(t, string(ctx.Response.Body()), tc.want)
		})
	}
}

// fakeLiveWebRTCProvider records the create body and answers with a fixed session.
type fakeLiveWebRTCProvider struct {
	schemas.LiveProvider
	body []byte
}

func (f *fakeLiveWebRTCProvider) CreateLiveWebRTCSession(_ *schemas.BifrostContext, _ schemas.Key, body []byte) (*schemas.LiveCreateResponse, *schemas.BifrostError) {
	f.body = body
	return &schemas.LiveCreateResponse{
		Session:   &schemas.LiveSession{ID: "live_webrtc"},
		Transport: &schemas.LiveTransport{Type: "webrtc", SDP: "v=0 answer"},
	}, nil
}

func TestCreateLiveWebRTCSessionSendsBifrostOffer(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", ""))
	provider := &fakeLiveWebRTCProvider{}
	admission := &liveAdmission{
		liveTarget: liveTarget{provider: provider, providerKey: schemas.OpenAI, voiceModel: "gpt-live-1"},
		ctx:        schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		meter:      meter,
	}
	body := []byte(`{"session":{"model":"gpt-live-1","instructions":"Be brief."},"transport":{"type":"webrtc","sdp":"browser offer"}}`)

	created, bifrostErr := createLiveWebRTCSession(admission, body, "bifrost offer")
	require.Nil(t, bifrostErr)
	assert.Equal(t, "live_webrtc", created.Session.ID)
	assert.Equal(t, "bifrost offer", gjson.GetBytes(provider.body, "transport.sdp").Str, "Bifrost, not the browser, is OpenAI's peer")
	assert.Equal(t, "Be brief.", gjson.GetBytes(provider.body, "session.instructions").Str, "the rest of the body is relayed as sent")

	// A created session bills at least the 15 seconds OpenAI charges at creation.
	meter.finish(4)
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, 15.0, postSeconds(t, posts[0]))
}

// webrtcTestPeer is one end of a pion connection with its data channel's messages collected.
type webrtcTestPeer struct {
	pc       *webrtc.PeerConnection
	mu       sync.Mutex
	dc       *webrtc.DataChannel
	messages chan string
	opened   chan struct{}
}

func newWebRTCTestPeer(t *testing.T) *webrtcTestPeer {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	return &webrtcTestPeer{pc: pc, messages: make(chan string, 64), opened: make(chan struct{})}
}

func (p *webrtcTestPeer) bind(dc *webrtc.DataChannel) {
	p.mu.Lock()
	p.dc = dc
	p.mu.Unlock()
	dc.OnOpen(func() { close(p.opened) })
	dc.OnMessage(func(msg webrtc.DataChannelMessage) { p.messages <- string(msg.Data) })
}

func (p *webrtcTestPeer) send(t *testing.T, message string) {
	t.Helper()
	p.mu.Lock()
	dc := p.dc
	p.mu.Unlock()
	require.NoError(t, dc.SendText(message))
}

func (p *webrtcTestPeer) next(t *testing.T) string {
	t.Helper()
	select {
	case message := <-p.messages:
		return message
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a data-channel message")
		return ""
	}
}

func (p *webrtcTestPeer) waitOpen(t *testing.T) {
	t.Helper()
	select {
	case <-p.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("data channel did not open")
	}
}

func gatheredSDP(t *testing.T, pc *webrtc.PeerConnection, description webrtc.SessionDescription) string {
	t.Helper()
	gathered := webrtc.GatheringCompletePromise(pc)
	require.NoError(t, pc.SetLocalDescription(description))
	<-gathered
	return pc.LocalDescription().SDP
}

// TestLiveWebRTCRelayEndToEnd runs real browser and OpenAI peers through the relay, including a
// refused update and a browser that leaves before the final usage arrives.
func TestLiveWebRTCRelayEndToEnd(t *testing.T) {
	SetLogger(&mockLogger{})
	browser := newWebRTCTestPeer(t)
	_, err := browser.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio)
	require.NoError(t, err)
	browserDC, err := browser.pc.CreateDataChannel(liveWebRTCDataChannelLabel, nil)
	require.NoError(t, err)
	browser.bind(browserDC)
	offer, err := browser.pc.CreateOffer(nil)
	require.NoError(t, err)
	browserOffer := gatheredSDP(t, browser.pc, offer)

	openai := newWebRTCTestPeer(t)
	openai.pc.OnDataChannel(openai.bind)
	openai.pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
			}
		}()
	})

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))
	messages := &liveWebRTCMessages{}
	messages.liveSessionController = newTestLiveController(fakeLiveModels{allowed: false}, meter, messages)
	closed := make(chan struct{})
	var closeOnce sync.Once

	handshakeCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	answer, bifrostErr := establishWebRTCRelay(webrtcRelaySetup{
		handler:          messages,
		dataChannelLabel: liveWebRTCDataChannelLabel,
		browserOffer:     browserOffer,
		handshakeCtx:     handshakeCtx,
		exchangeSDP: func(upstreamOffer string) (string, *schemas.BifrostError) {
			require.NoError(t, openai.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: upstreamOffer}))
			providerAnswer, err := openai.pc.CreateAnswer(nil)
			require.NoError(t, err)
			return gatheredSDP(t, openai.pc, providerAnswer), nil
		},
		onCreate: func(relay *webrtcRelay) { messages.relay = relay },
		onClose:  func() { closeOnce.Do(func() { close(closed) }) },
		cancel:   cancel,
	})
	require.Nil(t, bifrostErr)
	require.NoError(t, browser.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
	browser.waitOpen(t)
	openai.waitOpen(t)
	go messages.watchStale()

	// Provider events reach the browser unchanged, and usage is metered.
	started := `{"type":"session.started","session":{"id":"live_webrtc"}}`
	openai.send(t, started)
	assert.Equal(t, started, browser.next(t))
	usage := `{"type":"session.usage.updated","usage":{"seconds":31}}`
	openai.send(t, usage)
	assert.Equal(t, usage, browser.next(t))

	// A backend switch the session key cannot serve is refused and never reaches OpenAI.
	browser.send(t, `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"gpt-5.6-sol"}}}}`)
	assert.Contains(t, browser.next(t), "does not support model gpt-5.6-sol")
	marker := `{"type":"session.thinking.append","delegation_id":null,"content":"hi"}`
	browser.send(t, marker)
	assert.Equal(t, marker, openai.next(t))

	// The browser leaves: Bifrost closes the session upstream and waits for the final usage.
	require.NoError(t, browser.pc.Close())
	assert.Equal(t, `{"type":"session.close"}`, openai.next(t))
	openai.send(t, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":40}}`)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("relay did not close after session.closed")
	}

	var voice []float64
	_, posts, _ := runner.snapshot()
	for _, post := range posts {
		if post.model == "gpt-live-1" && post.resp != nil {
			voice = append(voice, postSeconds(t, post))
		}
	}
	assert.Equal(t, []float64{31, 9}, voice, "a full window, then the rest reported by session.closed")
}

// TestLiveWebRTCCloseBeforeEstablishedDoesNotFinish: a relay that fails during setup must not run
// the session's finish, which would bill the transport minimum as a success; the create handler
// aborts the meter instead. Once the relay is established, a close finishes the session.
func TestLiveWebRTCCloseBeforeEstablishedDoesNotFinish(t *testing.T) {
	t.Parallel()

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))
	messages := &liveWebRTCMessages{relay: &webrtcRelay{}}
	messages.liveSessionController = &liveSessionController{meter: meter, upstreamDone: make(chan struct{})}

	messages.closed()
	_, posts, _ := runner.snapshot()
	assert.Empty(t, posts, "a close before the relay is established bills nothing")
	assert.False(t, messages.relay.markEstablished(), "a relay closed during setup cannot then be established: setup fails and the handler aborts")

	refusal := newRealtimeWireBifrostError(502, "upstream_connection_error", "upstream WebRTC connection failed")
	meter.abort(refusal)
	_, posts, _ = runner.snapshot()
	require.NotEmpty(t, posts, "abort posts the failure to the plugins")
	for _, post := range posts {
		assert.NotNil(t, post.err, "the session's outcome is the setup failure")
	}

	established := &liveWebRTCMessages{relay: &webrtcRelay{}}
	require.True(t, established.relay.markEstablished(), "setup wins when no close raced it")
	meter2 := newTestLiveMeter(runner)
	require.Nil(t, meter2.admit("gpt-live-1", "gpt-5.6-luna"))
	established.liveSessionController = &liveSessionController{meter: meter2, upstreamDone: make(chan struct{})}
	before := len(posts)
	established.closed()
	_, posts, _ = runner.snapshot()
	assert.Greater(t, len(posts), before, "an established relay's close finishes the session")
}
