package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebRTCRelayCloseDrainsBrowserMessages(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "192.0.2.0/24", LoggerFactory: logging.NewDefaultLoggerFactory()})
	require.NoError(t, err)
	newPeer := func(ip string) *webrtc.PeerConnection {
		network, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
		require.NoError(t, err)
		require.NoError(t, router.AddNet(network))
		settings := webrtc.SettingEngine{}
		settings.SetNet(network)
		settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
		require.NoError(t, err)
		return pc
	}
	sender, browser := newPeer("192.0.2.1"), newPeer("192.0.2.2")
	var blockPackets atomic.Bool
	router.AddChunkFilter(func(vnet.Chunk) bool { return !blockPackets.Load() })
	require.NoError(t, router.Start())
	t.Cleanup(func() { _ = router.Stop() })
	t.Cleanup(func() { _ = browser.Close(); _ = sender.Close() })
	t.Cleanup(func() { blockPackets.Store(false) })
	received := make(chan string, 1)
	browser.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { received <- string(msg.Data) })
	})
	dc, err := sender.CreateDataChannel("oai-events", nil)
	require.NoError(t, err)
	relay := &webrtcRelay{downstreamPC: sender, handler: newFakeWebRTCHandler()}
	relay.bindDownstreamChannel(dc)
	require.True(t, relay.markEstablished())
	offer, err := sender.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, browser.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: gatheredSDP(t, sender, offer)}))
	answer, err := browser.CreateAnswer(nil)
	require.NoError(t, err)
	require.NoError(t, sender.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: gatheredSDP(t, browser, answer)}))
	require.Eventually(t, func() bool { return dc.ReadyState() == webrtc.DataChannelStateOpen }, 5*time.Second, time.Millisecond)

	// Make the final frame wait for retransmission, independently of CPU speed.
	blockPackets.Store(true)
	terminal := `{"type":"session.closed","usage":{"seconds":97}}`
	relay.sendDownstream([]byte(terminal), true)
	require.Positive(t, dc.BufferedAmount())
	done := make(chan struct{})
	go func() { relay.close(); close(done) }()
	select {
	case <-done:
		t.Fatal("relay closed while the browser's final frame was still buffered")
	case <-time.After(100 * time.Millisecond):
	}
	blockPackets.Store(false)
	select {
	case frame := <-received:
		assert.Equal(t, terminal, frame)
	case <-time.After(5 * time.Second):
		t.Fatal("browser did not receive the final frame after the network recovered")
	}
	waitSignal(t, done, "close after browser output drained")
}

func TestWebRTCRelayDownstreamDrainIsBounded(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	dc, err := pc.CreateDataChannel("oai-events", nil)
	require.NoError(t, err)
	relay := &webrtcRelay{downstreamChannel: dc}
	relay.sendDownstream([]byte(`{"type":"error"}`), true)
	require.True(t, relay.hasPendingDownstream(), "an unopened channel may still deliver pending frames")
	started := time.Now()
	relay.drainDownstream(20 * time.Millisecond)
	assert.GreaterOrEqual(t, time.Since(started), 20*time.Millisecond)
	assert.Less(t, time.Since(started), time.Second, "a channel that never opens cannot block teardown")
	require.True(t, relay.hasPendingDownstream(), "the timeout must not pretend output was delivered")
	require.NoError(t, dc.Close())
	assert.False(t, relay.hasPendingDownstream(), "a departed browser cannot receive queued output")
}

// fakeWebRTCHandler records what the relay reports to its handler.
type fakeWebRTCHandler struct {
	mu          sync.Mutex
	goneCalls   int
	closedCalls int
	browserLeft chan struct{}
	done        chan struct{}
}

func newFakeWebRTCHandler() *fakeWebRTCHandler {
	return &fakeWebRTCHandler{browserLeft: make(chan struct{}, 1), done: make(chan struct{}, 1)}
}

func (f *fakeWebRTCHandler) fromBrowser(*webrtcRelay, webrtc.DataChannelMessage)  {}
func (f *fakeWebRTCHandler) fromProvider(*webrtcRelay, webrtc.DataChannelMessage) {}

func (f *fakeWebRTCHandler) browserGone(*webrtcRelay) {
	f.mu.Lock()
	f.goneCalls++
	f.mu.Unlock()
	select {
	case f.browserLeft <- struct{}{}:
	default:
	}
}

func (f *fakeWebRTCHandler) closed() {
	f.mu.Lock()
	f.closedCalls++
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
}

func (f *fakeWebRTCHandler) counts() (browserGone, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.goneCalls, f.closedCalls
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestWebRTCRelayCloseRunsOnceInOrder(t *testing.T) {
	t.Parallel()

	var order []string
	var mu sync.Mutex
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}
	handler := newFakeWebRTCHandler()
	_, cancel := context.WithCancel(context.Background())
	relay := &webrtcRelay{
		handler: handler,
		onClose: func() { record("onClose") },
		cancel:  func() { record("cancel"); cancel() },
	}

	relay.close()
	relay.close()
	relay.closeWithErrorEvent([]byte(`{"type":"error"}`))

	_, closed := handler.counts()
	assert.Equal(t, 1, closed, "the handler learns of the close once")
	assert.Equal(t, []string{"onClose", "cancel"}, order, "the handler finalises first, then the owner, then the context")
}

func TestWebRTCRelayQueuesUntilChannelOpensAndCapsTheQueue(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	handler := newFakeWebRTCHandler()
	relay := &webrtcRelay{handler: handler}

	payload := []byte(`{"type":"session.update"}`)
	relay.sendUpstream(payload, true)
	payload[2] = 'X'
	require.Len(t, relay.pendingToUpstream, 1)
	assert.Equal(t, `{"type":"session.update"}`, string(relay.pendingToUpstream[0].payload), "a queued message is copied, not aliased")
	assert.True(t, relay.pendingToUpstream[0].isString)

	for len(relay.pendingToUpstream) < webrtcMaxPendingMessages {
		relay.sendUpstream([]byte(`{}`), true)
	}
	_, closed := handler.counts()
	assert.Equal(t, 0, closed, "a full queue is still fine")

	relay.sendUpstream([]byte(`{}`), true)
	waitSignal(t, handler.done, "relay close on queue overflow")
	assert.Len(t, relay.pendingToUpstream, webrtcMaxPendingMessages, "the overflowing message is dropped")

	// The downstream queue has the same cap.
	relay = &webrtcRelay{handler: newFakeWebRTCHandler()}
	for range webrtcMaxPendingMessages {
		relay.sendDownstream([]byte(`{}`), false)
	}
	relay.sendDownstream([]byte(`{}`), false)
	waitSignal(t, relay.handler.(*fakeWebRTCHandler).done, "relay close on downstream overflow")
	assert.False(t, relay.pendingToDownstream[0].isString)
}

// TestWebRTCRelayRoutesPeerEndings pins the asymmetry: the browser ending is the handler's call,
// the provider ending closes the relay.
func TestWebRTCRelayRoutesPeerEndings(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	newRelay := func(t *testing.T) (*webrtcRelay, *fakeWebRTCHandler) {
		t.Helper()
		downstreamPC, err := newWebRTCPeerConnection()
		require.NoError(t, err)
		upstreamPC, err := newWebRTCPeerConnection()
		require.NoError(t, err)
		handler := newFakeWebRTCHandler()
		relay := &webrtcRelay{downstreamPC: downstreamPC, upstreamPC: upstreamPC, handler: handler}
		relay.installCloseHandlers()
		t.Cleanup(relay.close)
		return relay, handler
	}

	t.Run("browser ends", func(t *testing.T) {
		relay, handler := newRelay(t)
		require.NoError(t, relay.downstreamPC.Close())
		waitSignal(t, handler.browserLeft, "browserGone")
		_, closed := handler.counts()
		assert.Equal(t, 0, closed, "the relay stays up so the handler can drain the provider side")
	})

	t.Run("provider ends", func(t *testing.T) {
		relay, handler := newRelay(t)
		require.NoError(t, relay.upstreamPC.Close())
		waitSignal(t, handler.done, "closed")
		assert.Error(t, relay.waitForUpstream(context.Background()), "a handshake in flight learns the upstream is gone")
	})
}

// A disconnected peer connection is transient: ICE consent checks lapsed and may resume, and pion
// moves a connection that does not recover to failed. Ending the session on disconnected billed a
// slow first WebRTC session at its 15 s minimum and dropped the usage OpenAI reported afterwards.
func TestWebRTCRelayDisconnectedIsNotAnEnding(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	t.Run("browser leg", func(t *testing.T) {
		handler := newFakeWebRTCHandler()
		relay := &webrtcRelay{handler: handler, upstreamConnectedOrError: make(chan error, 1)}
		relay.onDownstreamState(webrtc.PeerConnectionStateDisconnected)
		gone, _ := handler.counts()
		assert.Equal(t, 0, gone, "disconnected must not end the browser side")
		relay.onDownstreamState(webrtc.PeerConnectionStateFailed)
		gone, _ = handler.counts()
		assert.Equal(t, 1, gone, "failed ends the browser side")
	})
	t.Run("provider leg", func(t *testing.T) {
		handler := newFakeWebRTCHandler()
		relay := &webrtcRelay{handler: handler, upstreamConnectedOrError: make(chan error, 1)}
		relay.onUpstreamState(webrtc.PeerConnectionStateDisconnected)
		_, closed := handler.counts()
		assert.Equal(t, 0, closed, "disconnected must not close the relay")
		relay.onUpstreamState(webrtc.PeerConnectionStateFailed)
		_, closed = handler.counts()
		assert.Equal(t, 1, closed, "failed closes the relay")
	})
}

func TestWebRTCRelayUpstreamDrainLifecycle(t *testing.T) {
	for _, state := range []webrtc.PeerConnectionState{webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateFailed} {
		t.Run(state.String(), func(t *testing.T) {
			handler := newFakeWebRTCHandler()
			relay := &webrtcRelay{handler: handler, upstreamChannel: &webrtc.DataChannel{}}
			require.True(t, relay.markEstablished())
			t.Cleanup(relay.close)
			relay.onUpstreamState(state)
			_, closed := handler.counts()
			assert.Zero(t, closed, "transport ending does not overtake queued callbacks")
			require.NotNil(t, relay.upstreamDrainTimer)
			timer := relay.upstreamDrainTimer
			relay.onUpstreamState(state)
			assert.Same(t, timer, relay.upstreamDrainTimer, "repeated terminal states do not extend the deadline")
			relay.close() // data-channel OnClose/OnError after the reader drains
			assert.Nil(t, relay.upstreamDrainTimer, "reader completion releases the timer")
			relay.onUpstreamState(state)
			assert.Nil(t, relay.upstreamDrainTimer, "late state changes cannot retain the closed relay")
			_, closed = handler.counts()
			assert.Equal(t, 1, closed)
		})
	}
	t.Run("unopened channel is bounded", func(t *testing.T) {
		handler := newFakeWebRTCHandler()
		relay := &webrtcRelay{handler: handler, upstreamChannel: &webrtc.DataChannel{}}
		require.True(t, relay.markEstablished())
		t.Cleanup(relay.close)
		relay.onUpstreamState(webrtc.PeerConnectionStateClosed)
		relay.channelMu.Lock()
		relay.upstreamDrainTimer.Reset(time.Millisecond)
		relay.channelMu.Unlock()
		waitSignal(t, handler.done, "bounded drain for a channel without a reader")
		_, closed := handler.counts()
		assert.Equal(t, 1, closed)
	})
	t.Run("setup failure does not wait", func(t *testing.T) {
		handler := newFakeWebRTCHandler()
		relay := &webrtcRelay{handler: handler, upstreamChannel: &webrtc.DataChannel{}}
		relay.onUpstreamState(webrtc.PeerConnectionStateFailed)
		_, closed := handler.counts()
		assert.Equal(t, 1, closed)
		assert.Nil(t, relay.upstreamDrainTimer)
	})
}

func BenchmarkWebRTCRelayUpstreamState(b *testing.B) {
	for _, state := range []webrtc.PeerConnectionState{webrtc.PeerConnectionStateConnected, webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateClosed} {
		b.Run(state.String(), func(b *testing.B) {
			relay := &webrtcRelay{upstreamChannel: &webrtc.DataChannel{}}
			relay.markEstablished()
			b.ReportAllocs()
			for b.Loop() {
				relay.onUpstreamState(state)
				if state == webrtc.PeerConnectionStateClosed {
					relay.close()
					relay.closeOnce = sync.Once{}
					relay.closing = false
				}
			}
		})
	}
}

// pion fails a connection disconnectedTimeout + failedTimeout after the last packet. That sum is
// how long a peer that vanished without closing keeps its session (and its billing) open, so it
// is pinned here; disconnected must stay shorter so a stall is ridden out before the leg ends.
func TestWebRTCRelayICETimeouts(t *testing.T) {
	assert.Equal(t, 10*time.Second, webrtcICEDisconnectedTimeout+webrtcICEFailedTimeout)
	assert.Less(t, webrtcICEDisconnectedTimeout, webrtcICEDisconnectedTimeout+webrtcICEFailedTimeout)
	assert.Less(t, webrtcICEKeepaliveInterval, webrtcICEDisconnectedTimeout, "keepalives must arrive well inside the disconnected window")
	pc, err := newWebRTCPeerConnection()
	require.NoError(t, err)
	require.NoError(t, pc.Close())
}

func TestWebRTCRelayRegistryClosesAll(t *testing.T) {
	t.Parallel()

	registry := &webrtcRelayRegistry{}
	first, second := newFakeWebRTCHandler(), newFakeWebRTCHandler()
	registry.registerRelay("a", &webrtcRelay{handler: first})
	registry.registerRelay("b", &webrtcRelay{handler: second})
	registry.registerRelay("", &webrtcRelay{handler: newFakeWebRTCHandler()})
	registry.unregisterRelay("b")

	registry.closeAll()
	_, firstClosed := first.counts()
	_, secondClosed := second.counts()
	assert.Equal(t, 1, firstClosed)
	assert.Equal(t, 0, secondClosed, "an unregistered relay is left alone")
}

func TestConstrainSDPMaxMessageSize(t *testing.T) {
	t.Parallel()

	const application = "m=application 9 UDP/DTLS/SCTP webrtc-datachannel"
	upstream := "v=0\r\na=max-message-size:262144\r\n" + application + "\r\n"
	for _, tc := range []struct {
		name, upstream, browser string
		want                    int64
	}{
		{"browser sets no limit", upstream, "v=0\r\n", 262144},
		{"browser limit is higher", upstream, "a=max-message-size:1000000\r\n", 262144},
		{"browser limit is lower", upstream, "a=max-message-size:65536\r\n", 65536},
		{"upstream sets no limit", "v=0\r\n" + application + "\r\n", "a=max-message-size:65536\r\n", 65536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := constrainSDPMaxMessageSize(tc.upstream, tc.browser)
			size, ok := parseSDPMaxMessageSize(got)
			require.True(t, ok)
			assert.Equal(t, tc.want, size, "the offer never promises more than the browser accepts")
			assert.Contains(t, got, application)
		})
	}
}
