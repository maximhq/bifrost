package handlers

import (
	"context"
	"slices"
	"strings"
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
	body  []byte
	err   *schemas.BifrostError
	noSDP bool // created, but the answer is missing
}

func (f *fakeLiveWebRTCProvider) CreateLiveWebRTCSession(_ *schemas.BifrostContext, _ schemas.Key, body []byte) (*schemas.LiveCreateResponse, *schemas.BifrostError) {
	f.body = body
	if f.err != nil {
		return nil, f.err
	}
	if f.noSDP {
		return &schemas.LiveCreateResponse{Session: &schemas.LiveSession{ID: "live_webrtc"}}, nil
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
	body := []byte(`{"session":{"model":"gpt-live-1","instructions":"Be brief.","client":{"data_channel":{"allowed_client_events":["session.close"],"allowed_server_events":[{"type":"session.started"},{"type":"session.closed"},{"type":"error"}]}}},"transport":{"type":"webrtc","sdp":"browser offer"}}`)

	created, filter, bifrostErr := createLiveWebRTCSession(admission, body, "bifrost offer")
	require.Nil(t, bifrostErr)
	require.NotNil(t, filter)
	assert.Equal(t, "live_webrtc", created.Session.ID)
	assert.Equal(t, "bifrost offer", gjson.GetBytes(provider.body, "transport.sdp").Str, "Bifrost, not the browser, is OpenAI's peer")
	assert.Equal(t, "Be brief.", gjson.GetBytes(provider.body, "session.instructions").Str, "the rest of the body is relayed as sent")
	assert.Equal(t, `["session.close"]`, gjson.GetBytes(provider.body, "session.client.data_channel.allowed_client_events").Raw, "the browser's command permissions must stay unchanged")
	var upstreamEvents []string
	for _, selector := range gjson.GetBytes(provider.body, "session.client.data_channel.allowed_server_events").Array() {
		upstreamEvents = append(upstreamEvents, selector.Get("type").Str+"/"+selector.Get("response_event").Str)
	}
	assert.Contains(t, upstreamEvents, "session.usage.updated/", "voice usage must reach Bifrost even when the browser excludes it")
	assert.Contains(t, upstreamEvents, "response.event/response.completed", "backend usage must reach Bifrost without an application sideband")

	// A created session bills at least the 15 seconds OpenAI charges at creation.
	meter.finish(4)
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, 15.0, postSeconds(t, posts[0]))
}

func TestLiveWebRTCServerEventPermissions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, value  string
		all, invalid bool
	}{
		{name: "omitted", all: true},
		{name: "all", value: `"all"`, all: true},
		{name: "none", value: `[]`},
		{name: "status only", value: `[{"type":"session.started"},{"type":"session.closed"},{"type":"error"}]`},
		{name: "nested responses", value: `[{"type":"response.event","response_event":"response.output_text.delta"}]`},
		{name: "future event", value: `[{"type":"session.future"}]`},
		{name: "null", value: `null`, invalid: true},
		{name: "wrong string", value: `"none"`, invalid: true},
		{name: "object", value: `{}`, invalid: true},
		{name: "string selector", value: `["session.started"]`, invalid: true},
		{name: "null selector", value: `[null]`, invalid: true},
		{name: "missing type", value: `[{}]`, invalid: true},
		{name: "empty type", value: `[{"type":""}]`, invalid: true},
		{name: "numeric type", value: `[{"type":123}]`, invalid: true},
		{name: "long type", value: `[{"type":"` + strings.Repeat("x", 257) + `"}]`, invalid: true},
		{name: "missing response selector", value: `[{"type":"response.event"}]`, invalid: true},
		{name: "empty response selector", value: `[{"type":"response.event","response_event":""}]`, invalid: true},
		{name: "null response selector", value: `[{"type":"response.event","response_event":null}]`, invalid: true},
		{name: "long response selector", value: `[{"type":"response.event","response_event":"` + strings.Repeat("x", 257) + `"}]`, invalid: true},
		{name: "unexpected response selector", value: `[{"type":"session.started","response_event":"response.completed"}]`, invalid: true},
		{name: "unexpected null response selector", value: `[{"type":"session.started","response_event":null}]`, invalid: true},
		{name: "unsupported selector field", value: `[{"type":"session.started","private_only":true}]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissions := `"allowed_client_events":["session.close"]`
			if tc.value != "" {
				permissions += `,"allowed_server_events":` + tc.value
			}
			body := []byte(`{"session":{"model":"gpt-live-1","input":[{"role":"user","content":"private history"}],"client":{"data_channel":{` + permissions + `}}},"transport":{"type":"webrtc","sdp":"offer"},"future_setting":{"b":2,"a":1}}`)
			upstream, filter, bifrostErr := prepareLiveWebRTCEvents(body)
			if tc.invalid {
				require.NotNil(t, bifrostErr)
				assert.Equal(t, 400, *bifrostErr.StatusCode)
				assert.Contains(t, bifrostErr.Error.Message, liveServerEventsPath)
				assert.Nil(t, upstream)
				return
			}
			require.Nil(t, bifrostErr)
			assert.Equal(t, `["session.close"]`, gjson.GetBytes(upstream, "session.client.data_channel.allowed_client_events").Raw)
			assert.Equal(t, gjson.GetBytes(body, "session.input").Raw, gjson.GetBytes(upstream, "session.input").Raw)
			assert.Equal(t, `{"b":2,"a":1}`, gjson.GetBytes(upstream, "future_setting").Raw, "unrelated JSON is not reserialized")
			if tc.all {
				assert.Nil(t, filter)
				assert.Equal(t, body, upstream, "the default and all need no rewrite")
			} else {
				require.NotNil(t, filter)
				assert.JSONEq(t, tc.value, string(filter.original))
			}
			for _, message := range []string{
				`{"type":"session.started"}`, `{"type":"session.usage.updated","usage":{"seconds":30}}`,
				`{"type":"session.input_transcript.delta","delta":"secret"}`, `{"type":"session.future"}`, `{"type":"error"}`,
				`{"type":"response.event","event":{"type":"response.output_text.delta","delta":"secret"}}`,
				`{"type":"response.event","event":{"type":"response.completed"}}`,
				`{"type":"response.event","event":{}}`, `{}`, `{"type":123}`,
			} {
				allowed := tc.all
				for _, selector := range gjson.Parse(tc.value).Array() {
					kind := gjson.Get(message, "type").Str
					if selector.Get("type").Str == kind && (kind != "response.event" || selector.Get("response_event").Str == gjson.Get(message, "event.type").Str) {
						allowed = true
					}
				}
				client, err := filter.clientMessage([]byte(message))
				require.NoError(t, err)
				if allowed {
					assert.Equal(t, message, string(client), "allowed events keep their exact bytes")
				} else {
					assert.Nil(t, client, "unrequested or malformed event must not reach the browser: %s", message)
				}
			}
		})
	}
}

func TestLiveWebRTCServerEventPermissionsRestoreSnapshots(t *testing.T) {
	t.Parallel()
	const original = `[{"type":"session.started"},{"type":"session.updated"},{"type":"session.closed"}]`
	upstream, filter, bifrostErr := prepareLiveWebRTCEvents([]byte(`{"session":{"client":{"data_channel":{"allowed_server_events":` + original + `}}}}`))
	require.Nil(t, bifrostErr)
	for _, kind := range []string{"session.started", "session.updated", "session.closed"} {
		message := []byte(`{"type":"` + kind + `","event_id":"evt_1","session":{"id":"live_1","client":{"data_channel":{"allowed_client_events":["session.close"],"allowed_server_events":` + gjson.GetBytes(upstream, liveServerEventsPath).Raw + `}},"future":{"b":2,"a":1}}}`)
		client, err := filter.clientMessage(message)
		require.NoError(t, err)
		assert.JSONEq(t, original, gjson.GetBytes(client, liveServerEventsPath).Raw)
		assert.Equal(t, "evt_1", gjson.GetBytes(client, "event_id").Str)
		assert.Equal(t, "live_1", gjson.GetBytes(client, "session.id").Str)
		assert.Equal(t, `["session.close"]`, gjson.GetBytes(client, "session.client.data_channel.allowed_client_events").Raw)
		assert.Equal(t, `{"b":2,"a":1}`, gjson.GetBytes(client, "session.future").Raw)
	}
}

func TestLiveWebRTCServerEventPermissionsRejectBeforeProviderCreate(t *testing.T) {
	t.Parallel()
	provider := &fakeLiveWebRTCProvider{}
	admission := &liveAdmission{liveTarget: liveTarget{provider: provider}}
	_, _, bifrostErr := createLiveWebRTCSession(admission, []byte(`{"session":{"client":{"data_channel":{"allowed_server_events":[{"type":"response.event"}]}}}}`), "offer")
	require.NotNil(t, bifrostErr)
	assert.Equal(t, 400, *bifrostErr.StatusCode)
	assert.Empty(t, provider.body, "invalid permissions must not create a billable upstream session")
}

func TestLiveWebRTCServerEventPermissionsApplyToGatewayErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, allowed string
		wantError     bool
	}{
		{name: "none", allowed: `[]`},
		{name: "status only", allowed: `[{"type":"session.started"}]`},
		{name: "errors", allowed: `[{"type":"error"}]`, wantError: true},
		{name: "all", allowed: `"all"`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, filter, bifrostErr := prepareLiveWebRTCEvents([]byte(`{"session":{"client":{"data_channel":{"allowed_server_events":` + tc.allowed + `}}}}`))
			require.Nil(t, bifrostErr)
			messages := &liveWebRTCMessages{relay: &webrtcRelay{}, serverEvents: filter}
			messages.liveSessionController = &liveSessionController{io: messages}
			messages.sendError(newRealtimeWireBifrostError(403, "permission_error", "private policy failure"))
			frames := messages.relay.pendingToDownstream
			if !tc.wantError {
				assert.Empty(t, frames, "filter gateway errors before they can enter the browser's pending queue")
				return
			}
			require.Len(t, frames, 1)
			assert.Equal(t, "error", gjson.GetBytes(frames[0].payload, "type").Str)
			assert.Contains(t, string(frames[0].payload), "private policy failure")
		})
	}
}

// The refusal is OpenAI's own, live-probed 2026-10-28; Azure rejects session.client, so the
// gateway is the only enforcement there.
func TestLiveWebRTCClientEventPermissions(t *testing.T) {
	t.Parallel()
	const refusal = `{"type":"error","error":{"type":"invalid_request_error","code":"event_not_allowed","message":"This event is not allowed on the frontend data channel.","client_event_id":"evt_1"}}`
	for _, tc := range []struct {
		name, value string
		allowed     []string
		invalid     bool
	}{
		{name: "omitted", allowed: []string{"session.input_audio.mute", "session.commentary.append", "session.future", ""}},
		{name: "all", value: `"all"`, allowed: []string{"session.input_audio.mute", "session.commentary.append", "session.future", ""}},
		{name: "none", value: `[]`},
		{name: "mute only", value: `["session.input_audio.mute"]`, allowed: []string{"session.input_audio.mute"}},
		{name: "future event", value: `["session.future"]`, allowed: []string{"session.future"}},
		{name: "null", value: `null`, invalid: true},
		{name: "wrong string", value: `"none"`, invalid: true},
		{name: "object", value: `{}`, invalid: true},
		{name: "selector object", value: `[{"type":"session.close"}]`, invalid: true},
		{name: "numeric entry", value: `[1]`, invalid: true},
		{name: "empty entry", value: `[""]`, invalid: true},
		{name: "long entry", value: `["` + strings.Repeat("x", 257) + `"]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := `{"model":"gpt-live-1"}`
			if tc.value != "" {
				session = `{"model":"gpt-live-1","client":{"data_channel":{"allowed_client_events":` + tc.value + `}}}`
			}
			filter, bifrostErr := liveWebRTCClientEvents([]byte(`{"session":` + session + `,"transport":{"type":"webrtc","sdp":"offer"}}`))
			if tc.invalid {
				require.NotNil(t, bifrostErr)
				assert.Equal(t, 400, *bifrostErr.StatusCode)
				assert.Contains(t, bifrostErr.Error.Message, "session.client.data_channel.allowed_client_events")
				return
			}
			require.Nil(t, bifrostErr)
			for _, kind := range []string{"session.input_audio.mute", "session.commentary.append", "session.future", ""} {
				messages := &liveWebRTCMessages{relay: &webrtcRelay{}, clientEvents: filter}
				messages.liveSessionController = &liveSessionController{io: messages}
				message := `{"type":"` + kind + `","event_id":"evt_1"}`
				messages.fromBrowser(messages.relay, webrtc.DataChannelMessage{IsString: true, Data: []byte(message)})
				if slices.Contains(tc.allowed, kind) {
					require.Len(t, messages.relay.pendingToUpstream, 1)
					assert.Equal(t, message, string(messages.relay.pendingToUpstream[0].payload))
					assert.Empty(t, messages.relay.pendingToDownstream)
					continue
				}
				assert.Empty(t, messages.relay.pendingToUpstream, "a refused event must not reach the provider: %s", message)
				require.Len(t, messages.relay.pendingToDownstream, 1)
				assert.Equal(t, refusal, string(messages.relay.pendingToDownstream[0].payload))
			}
		})
	}
}

// The gateway's own close is not a frontend event, so a list without session.close cannot keep a
// departed browser's session open.
func TestLiveWebRTCClientEventPermissionsSkipGatewayClose(t *testing.T) {
	t.Parallel()
	filter, bifrostErr := liveWebRTCClientEvents([]byte(`{"session":{"client":{"data_channel":{"allowed_client_events":[]}}}}`))
	require.Nil(t, bifrostErr)
	messages := &liveWebRTCMessages{relay: &webrtcRelay{}, clientEvents: filter}
	messages.liveSessionController = &liveSessionController{io: messages, upstreamDone: make(chan struct{}), drainTimeout: time.Hour}
	messages.browserGone(messages.relay)
	t.Cleanup(messages.stopDrainTimer)
	require.Len(t, messages.relay.pendingToUpstream, 1)
	assert.Equal(t, `{"type":"session.close"}`, string(messages.relay.pendingToUpstream[0].payload))
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

type pausedLiveWebRTCMessages struct {
	*liveWebRTCMessages
	paused chan struct{}
	resume chan struct{}
}

func (m *pausedLiveWebRTCMessages) fromProvider(r *webrtcRelay, msg webrtc.DataChannelMessage) {
	m.liveWebRTCMessages.fromProvider(r, msg)
	if schemas.LiveEventTypeOf(msg.Data) == schemas.LiveEventSessionStarted {
		close(m.paused)
		<-m.resume
	}
}

// Acknowledged SCTP messages can still be waiting behind an application callback.
// DTLS close must not finalize billing before the data-channel reader drains them.
func TestLiveWebRTCTransportCloseDrainsReceivedUsage(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	for _, tc := range []struct {
		name        string
		frames      []string
		seconds     float64
		closed      bool
		browserGone bool
	}{
		{name: "usage then session closed", frames: []string{`{"type":"session.usage.updated","usage":{"seconds":97}}`, `{"type":"session.closed","usage":{"seconds":97}}`}, seconds: 97, closed: true},
		{name: "final usage only", frames: []string{`{"type":"session.closed","usage":{"seconds":97}}`}, seconds: 97, closed: true},
		{name: "final usage increases last snapshot", frames: []string{`{"type":"session.usage.updated","usage":{"seconds":31}}`, `{"type":"session.closed","usage":{"seconds":97}}`}, seconds: 97, closed: true},
		{name: "duplicate snapshots do not double bill", frames: []string{`{"type":"session.usage.updated","usage":{"seconds":97}}`, `{"type":"session.usage.updated","usage":{"seconds":97}}`, `{"type":"session.closed","usage":{"seconds":97}}`}, seconds: 97, closed: true},
		{name: "late usage after session closed is ignored", frames: []string{`{"type":"session.closed","usage":{"seconds":97}}`, `{"type":"session.usage.updated","usage":{"seconds":999}}`}, seconds: 97, closed: true},
		{name: "usage then hangup", frames: []string{`{"type":"session.usage.updated","usage":{"seconds":97}}`}, seconds: 97},
		{name: "hangup without usage", seconds: 15},
		{name: "browser left before final usage", frames: []string{`{"type":"session.closed","usage":{"seconds":97}}`}, seconds: 97, closed: true, browserGone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newWebRTCTestPeer(t)
			receiver := newWebRTCTestPeer(t)
			provider.pc.OnDataChannel(provider.bind)
			runner := &fakeLiveRunner{}
			meter := newTestLiveMeter(runner)
			require.Nil(t, meter.admit("gpt-live-1", ""))
			meter.setMinimumSeconds(15)
			messages := &pausedLiveWebRTCMessages{
				liveWebRTCMessages: &liveWebRTCMessages{},
				paused:             make(chan struct{}), resume: make(chan struct{}),
			}
			messages.liveSessionController = newTestLiveController(fakeLiveModels{allowed: true}, meter, messages.liveWebRTCMessages)
			closed, peerEnded := make(chan struct{}), make(chan struct{}, 1)
			relay := &webrtcRelay{upstreamPC: receiver.pc, handler: messages, onClose: func() { close(closed) }}
			messages.relay = relay
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(messages.resume) }) }
			t.Cleanup(relay.close)
			t.Cleanup(resume)
			receiver.pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
				relay.onUpstreamState(state)
				if state == webrtc.PeerConnectionStateClosed {
					select {
					case peerEnded <- struct{}{}:
					default:
					}
				}
			})
			dc, err := receiver.pc.CreateDataChannel(liveWebRTCDataChannelLabel, nil)
			require.NoError(t, err)
			relay.bindUpstreamChannel(dc)
			offer, err := receiver.pc.CreateOffer(nil)
			require.NoError(t, err)
			require.NoError(t, provider.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: gatheredSDP(t, receiver.pc, offer)}))
			answer, err := provider.pc.CreateAnswer(nil)
			require.NoError(t, err)
			require.NoError(t, receiver.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: gatheredSDP(t, provider.pc, answer)}))
			provider.waitOpen(t)
			require.True(t, relay.markEstablished())
			provider.send(t, `{"type":"session.started","session":{"id":"live_queued_usage"}}`)
			waitSignal(t, messages.paused, "paused application callback")
			if tc.browserGone {
				relay.onDownstreamState(webrtc.PeerConnectionStateFailed)
			}
			for _, frame := range tc.frames {
				provider.send(t, frame)
			}
			provider.mu.Lock()
			providerDC := provider.dc
			provider.mu.Unlock()
			require.Eventually(t, func() bool { return providerDC.BufferedAmount() == 0 }, 5*time.Second, time.Millisecond, "all frames acknowledged while application callback is blocked")
			require.NoError(t, provider.pc.Close())
			waitSignal(t, peerEnded, "DTLS peer close")
			resume()
			waitSignal(t, closed, "relay finalization")
			_, posts, _ := runner.snapshot()
			var seconds float64
			for _, post := range posts {
				seconds += postSeconds(t, post)
			}
			assert.Equal(t, tc.seconds, seconds, "bill received usage before finalizing")
			assert.Equal(t, tc.closed, messages.sessionClosed.Load(), "queued terminal event is processed")
		})
	}
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
		noSDP   bool
		seconds float64
		billed  float64
	}{
		{name: "upstream refusal", refuse: true},
		{name: "created without an SDP answer", noSDP: true, billed: 15},
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
			provider := &fakeLiveWebRTCProvider{noSDP: tc.noSDP}
			if tc.refuse {
				provider.err = newRealtimeWireBifrostError(403, "permission_error", "upstream refused session creation")
			}
			admission := &liveAdmission{
				liveTarget: liveTarget{provider: provider},
				ctx:        schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				meter:      meter,
			}
			created, _, setupErr := createLiveWebRTCSession(admission, []byte(`{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":"browser"}}`), "relay")
			if tc.refuse {
				require.Nil(t, created)
				require.NotNil(t, setupErr)
				assert.Zero(t, meter.minimumSeconds)
			} else if tc.noSDP {
				// The provider created the session, so its minimum and id are kept before the answer is rejected.
				require.Nil(t, created)
				require.NotNil(t, setupErr)
				assert.Equal(t, 502, *setupErr.StatusCode)
				assert.Equal(t, liveWebRTCMinimumSeconds, meter.minimumSeconds)
				assert.Equal(t, "live_webrtc", meter.providerSessionID)
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

// BenchmarkLiveWebRTCFromBrowser is the per-event cost of a browser data-channel message, with
// and without the frontend's client event permissions.
func BenchmarkLiveWebRTCFromBrowser(b *testing.B) {
	message := webrtc.DataChannelMessage{IsString: true, Data: []byte(`{"type":"session.input_audio.mute","event_id":"evt_1"}`)}
	for _, tc := range []struct{ name, body string }{
		{name: "unrestricted", body: `{"session":{}}`},
		{name: "allowed", body: `{"session":{"client":{"data_channel":{"allowed_client_events":["session.input_audio.mute"]}}}}`},
		{name: "refused", body: `{"session":{"client":{"data_channel":{"allowed_client_events":["session.close"]}}}}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			filter, bifrostErr := liveWebRTCClientEvents([]byte(tc.body))
			require.Nil(b, bifrostErr)
			messages := &liveWebRTCMessages{relay: &webrtcRelay{}, clientEvents: filter}
			messages.liveSessionController = &liveSessionController{io: messages}
			b.ReportAllocs()
			for b.Loop() {
				messages.fromBrowser(messages.relay, message)
				messages.relay.pendingToUpstream = messages.relay.pendingToUpstream[:0]
				messages.relay.pendingToDownstream = messages.relay.pendingToDownstream[:0]
			}
		})
	}
}
