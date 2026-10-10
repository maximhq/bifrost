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
	err  *schemas.BifrostError
}

func (f *fakeLiveWebRTCProvider) CreateLiveWebRTCSession(_ *schemas.BifrostContext, _ schemas.Key, body []byte) (*schemas.LiveCreateResponse, *schemas.BifrostError) {
	f.body = body
	if f.err != nil {
		return nil, f.err
	}
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
	messages.liveSessionController = newTestLiveController(fakeLiveModels{allowed: true, blocked: "gpt-5.6-sol"}, meter, messages)
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

	// Physical model support cannot override governance's model-specific key restriction.
	runner.restrictKeys("gpt-5.6-terra", []string{"key-2"})
	update := `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"gpt-5.6-terra"}}}}`
	browser.send(t, update)
	assert.Contains(t, browser.next(t), "the key serving this session is not allowed for model gpt-5.6-terra")
	browser.send(t, marker)
	assert.Equal(t, marker, openai.next(t), "the forbidden update never reached the provider")
	runner.restrictKeys("gpt-5.6-terra", []string{"key-1"})
	browser.send(t, update)
	assert.Equal(t, update, openai.next(t), "a permitted switch still reaches the provider")

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
	io := &recordingLiveIO{}
	messages := &liveWebRTCMessages{relay: &webrtcRelay{}}
	messages.liveSessionController = &liveSessionController{io: io, meter: meter, upstreamDone: make(chan struct{})}

	messages.closed()
	_, posts, _ := runner.snapshot()
	assert.Empty(t, posts, "a close before the relay is established bills nothing")
	assert.Empty(t, io.client, "the create handler answers the browser; the relay says nothing")
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
	io2 := &recordingLiveIO{}
	established.liveSessionController = &liveSessionController{io: io2, meter: meter2, upstreamDone: make(chan struct{})}
	before := len(posts)
	established.closed()
	_, posts, _ = runner.snapshot()
	assert.Greater(t, len(posts), before, "an established relay's close finishes the session")
	require.Len(t, io2.client, 1, "the browser is told the session ended without session.closed")
	assert.Contains(t, string(io2.client[0]), "before session.closed")
}

// recordingLiveIO is a session's client and upstream ends that only remember what was sent.
type recordingLiveIO struct {
	client, upstream [][]byte
}

func (io *recordingLiveIO) sendUpstream(message []byte) error {
	io.upstream = append(io.upstream, message)
	return nil
}
func (io *recordingLiveIO) sendClient(message []byte) error {
	io.client = append(io.client, message)
	return nil
}
func (io *recordingLiveIO) abandonUpstream() {}

func TestLiveWebRTCCloseConcurrentWithTranscript(t *testing.T) {
	for n := 0; n < 10; n++ {
		runner := &fakeLiveRunner{}
		meter := newTestLiveMeter(runner)
		require.Nil(t, meter.admit("gpt-live-1", ""))
		messages := &liveWebRTCMessages{relay: &webrtcRelay{}}
		require.True(t, messages.relay.markEstablished())
		messages.liveSessionController = newTestLiveController(fakeLiveModels{allowed: true}, meter, &recordingLiveIO{})
		started, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			frame := []byte(`{"type":"session.input_transcript.delta","delta":"hello","start_ms":1,"end_ms":2}`)
			messages.fromUpstream(frame)
			close(started)
			for i := 0; i < 1000; i++ {
				messages.fromUpstream(frame)
			}
		}()
		<-started
		messages.closed()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("provider message handling did not finish after shutdown")
		}
		opens, posts, cleanups := runner.snapshot()
		require.Len(t, posts, 1)
		require.NotNil(t, posts[0].live)
		assert.Equal(t, messages.transcript.snapshot(), posts[0].live.Transcript, "shutdown freezes the transcript carried by the closing unit")
		assert.Equal(t, len(opens), cleanups, "shutdown closes the admitted unit exactly once")
	}
}

func TestLiveWebRTCFinalizationIgnoresLateProviderEvents(t *testing.T) {
	t.Parallel()
	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", ""))
	messages := &liveWebRTCMessages{relay: &webrtcRelay{}}
	require.True(t, messages.relay.markEstablished())
	messages.liveSessionController = newTestLiveController(fakeLiveModels{allowed: true}, meter, &recordingLiveIO{})
	messages.fromUpstream([]byte(`{"type":"session.input_transcript.delta","delta":"hello","start_ms":1,"end_ms":2}`))
	messages.fromUpstream([]byte(`{"type":"session.usage.updated","usage":{"seconds":7}}`))
	messages.closed()

	// Pion may deliver a callback already queued when the close callback finalized the session.
	messages.fromUpstream([]byte(`{"type":"session.output_transcript.delta","delta":"late","start_ms":3,"end_ms":4}`))
	messages.fromUpstream([]byte(`{"type":"session.usage.updated","usage":{"seconds":99}}`))
	messages.fromUpstream([]byte(`{"type":"session.closed","usage":{"seconds":100}}`))
	messages.closed()
	_, posts, cleanups := runner.snapshot()
	require.Len(t, posts, 1, "terminal callbacks do not run the post-hooks twice")
	assert.Equal(t, 1, cleanups)
	assert.Equal(t, 7.0, postSeconds(t, posts[0]))
	assert.Equal(t, 7.0, meter.lastReportedSeconds(), "late events cannot accrue usage after billing ends")
	require.NotNil(t, posts[0].live)
	expected := []schemas.LiveTranscriptLine{{Role: "user", Text: "hello", StartMs: 1, EndMs: 2}}
	assert.Equal(t, expected, posts[0].live.Transcript)
	assert.Equal(t, expected, messages.transcript.snapshot(), "late fragments do not mutate finalized state")
	assert.Equal(t, expected, meter.ending.transcript, "late terminal events cannot replace the final snapshot")
}

func TestLiveWebRTCSetupFailureSettlesCreatedSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		refuse  bool
		seconds float64
		billed  float64
	}{
		{name: "upstream refusal", refuse: true},
		{name: "handshake failure after create", billed: 15},
		{name: "short reported usage", seconds: 4, billed: 15},
		{name: "usage exceeds minimum", seconds: 20, billed: 20},
		{name: "usage already billed in a window", seconds: 35, billed: 35},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeLiveRunner{}
			meter := newTestLiveMeter(runner)
			meter.setTransport("webrtc")
			require.Nil(t, meter.admit("gpt-live-1", "gpt-5.6-luna"))
			provider := &fakeLiveWebRTCProvider{}
			if tc.refuse {
				provider.err = newRealtimeWireBifrostError(403, "permission_error", "upstream refused session creation")
			}
			admission := &liveAdmission{
				liveTarget: liveTarget{provider: provider},
				ctx:        schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				meter:      meter,
			}
			created, setupErr := createLiveWebRTCSession(admission, []byte(`{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":"browser"}}`), "relay")
			if tc.refuse {
				require.Nil(t, created)
				require.NotNil(t, setupErr)
				assert.Zero(t, meter.minimumSeconds)
			} else {
				require.Nil(t, setupErr)
				require.NotNil(t, created)
				setupErr = newRealtimeWireBifrostError(502, "upstream_connection_error", "upstream ICE failed after session creation")
				if tc.seconds > 0 {
					require.Nil(t, meter.onUsage(tc.seconds))
				}
			}
			meter.abort(setupErr)
			meter.abort(newRealtimeWireBifrostError(500, "server_error", "duplicate failure"))
			meter.finish(100)
			opens, posts, cleanups := runner.snapshot()
			require.Len(t, posts, len(opens))
			assert.Equal(t, len(opens), cleanups, "every admitted unit closes exactly once")
			var billed float64
			for _, post := range posts {
				if post.kind != liveUnitVoice {
					continue
				}
				if post.resp != nil {
					billed += postSeconds(t, post)
				} else if post.err != nil && post.err.ExtraFields.BilledUsage != nil {
					require.NotNil(t, post.err.ExtraFields.BilledUsage.AudioSeconds)
					billed += *post.err.ExtraFields.BilledUsage.AudioSeconds
				}
			}
			assert.Equal(t, tc.billed, billed, "only incurred usage is billed, including the initialization minimum")
			last := posts[len(posts)-1]
			assert.True(t, last.end)
			require.NotNil(t, last.err, "the setup failure is preserved alongside billable usage")
			assert.Equal(t, setupErr.Error.Message, last.err.Error.Message)
			assert.Equal(t, schemas.LiveRequest, last.err.ExtraFields.RequestType)
			assert.Nil(t, last.resp, "a failed setup stays an error, not a successful response")
			assert.Nil(t, setupErr.ExtraFields.BilledUsage, "settlement does not mutate the caller's error")
			if tc.refuse {
				assert.Nil(t, last.err.ExtraFields.BilledUsage, "an upstream refusal incurred no usage")
			}
		})
	}
}
