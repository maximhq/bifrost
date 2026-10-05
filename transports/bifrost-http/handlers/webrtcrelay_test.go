package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
