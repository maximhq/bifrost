package handlers

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"
)

// panicWatchLogger counts broadcast errors that carry a recovered panic.
//
// sendMessageSafely converts panics into errors, so a panic inside the websocket
// library is only observable through the message the broadcaster logs.
type panicWatchLogger struct {
	mockLogger

	mu       sync.Mutex
	panics   atomic.Int64
	messages []string
}

func (l *panicWatchLogger) Error(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !strings.Contains(msg, "panic") {
		return
	}
	l.panics.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.messages) < 10 {
		l.messages = append(l.messages, msg)
	}
}

func (l *panicWatchLogger) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.messages...)
}

// startWebSocketTestServer serves the real /ws route on a random local port.
func startWebSocketTestServer(t *testing.T) (*WebSocketHandler, string, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	h := NewWebSocketHandler(ctx, nil)

	r := router.New()
	h.RegisterRoutes(r)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("listen: %v", err)
	}

	// Stop() waits on the heartbeat goroutine's done channel, so the heartbeat
	// has to be running for the handler to be stoppable at all.
	h.StartHeartbeat()

	srv := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = srv.Serve(ln) }()

	// Shutting down drains the heartbeat goroutine before the test returns.
	// Without that, it outlives the test and races the next SetLogger call on
	// the package-level logger.
	var once sync.Once
	return h, ln.Addr().String(), func() {
		once.Do(func() {
			h.Stop()
			_ = srv.Shutdown()
			cancel()
		})
	}
}

// waitForClientCount blocks until the handler's client map reaches want.
func waitForClientCount(t *testing.T, h *WebSocketHandler, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.RLock()
		got := len(h.clients)
		h.mu.RUnlock()
		if got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d connected websocket clients", want)
}

// dialWebSocket opens a client connection to the test server's /ws route.
//
// The handshake response is closed rather than discarded: on a failed upgrade it carries
// the server's real body, and on a successful one it is an empty placeholder, so closing
// it unconditionally is both correct and what bodyclose expects. It is closed inline
// rather than deferred because the disconnect-storm test dials in a loop, where deferred
// closes would pile up until the test returns.
func dialWebSocket(t *testing.T, addr string) *websocket.Conn {
	t.Helper()

	client, resp, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return client
}

// snapshotClients mirrors the snapshot BroadcastMarshaledMessage takes before
// it writes outside the handler lock.
func snapshotClients(h *WebSocketHandler) []*WebSocketClient {
	h.mu.RLock()
	defer h.mu.RUnlock()

	clients := make([]*WebSocketClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	return clients
}

// TestWebSocketWriteAfterHandlerReturn covers a broadcaster that snapshotted a
// client just before that client disconnected.
//
// Once the upgrade closure returns, fasthttp calls releaseHijackConn on the
// hijacked connection, which nils the embedded net.Conn and returns the wrapper
// to a pool. A write issued after that point dereferences the nil net.Conn deep
// inside the websocket library, and the panic leaves the connection's isWriting
// flag latched so every later write reports a bogus "concurrent write".
func TestWebSocketWriteAfterHandlerReturn(t *testing.T) {
	SetLogger(&mockLogger{})

	h, addr, stop := startWebSocketTestServer(t)
	defer stop()

	client := dialWebSocket(t, addr)
	waitForClientCount(t, h, 1)

	stale := snapshotClients(h)
	if len(stale) != 1 {
		t.Fatalf("expected 1 client in snapshot, got %d", len(stale))
	}

	// The client vanishes, the read loop fails, and the upgrade closure returns.
	_ = client.UnderlyingConn().Close()
	waitForClientCount(t, h, 0)
	time.Sleep(250 * time.Millisecond) // let fasthttp recycle the hijacked conn

	// Three writes: the first would nil-deref, the rest would report a
	// "concurrent write" that never happened.
	for i := range 3 {
		err := h.sendMessageSafely(stale[0], websocket.TextMessage, []byte(`{"type":"store_update"}`))
		if err == nil {
			t.Fatalf("write %d to a disconnected client unexpectedly succeeded", i+1)
		}
		if strings.Contains(err.Error(), "panic") {
			t.Fatalf("write %d panicked inside the websocket library: %v", i+1, err)
		}
	}
}

// TestWebSocketBroadcastDuringDisconnectStorm exercises the real race: a
// broadcast loop running while clients connect and drop.
func TestWebSocketBroadcastDuringDisconnectStorm(t *testing.T) {
	watcher := &panicWatchLogger{}
	SetLogger(watcher)
	defer SetLogger(&mockLogger{})

	h, addr, stop := startWebSocketTestServer(t)
	defer stop()

	done := make(chan struct{})
	var broadcasters sync.WaitGroup
	// t.Fatalf stops the test goroutine with runtime.Goexit, which runs deferred calls
	// but not the statements below them, and explicitly does not stop other goroutines.
	// The dial and both waitForClientCount calls in the loop can each end the test that
	// way, so the shutdown has to be deferred -- otherwise four goroutines keep
	// broadcasting for the rest of the package run, racing the package-level SetLogger
	// the next test performs.
	var stopBroadcastersOnce sync.Once
	stopBroadcasters := func() {
		stopBroadcastersOnce.Do(func() {
			close(done)
			broadcasters.Wait()
		})
	}
	// Registered after defer stop(), so LIFO drains the broadcasters before the handler
	// shuts down and before the logger is restored.
	defer stopBroadcasters()

	for range 4 {
		broadcasters.Add(1)
		go func() {
			defer broadcasters.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				h.BroadcastUpdatesToClients([]string{"Providers"})
			}
		}()
	}

	for range 40 {
		client := dialWebSocket(t, addr)
		waitForClientCount(t, h, 1)
		_ = client.UnderlyingConn().Close()
		waitForClientCount(t, h, 0)
	}

	stopBroadcasters()

	if n := watcher.panics.Load(); n > 0 {
		t.Fatalf("broadcasting during disconnects recovered %d panic(s): %v", n, watcher.recorded())
	}
}

// TestWebSocketStopClosesClients checks that Stop marks live clients closed so
// no write can reach them afterwards.
func TestWebSocketStopClosesClients(t *testing.T) {
	SetLogger(&mockLogger{})

	h, addr, stop := startWebSocketTestServer(t)
	defer stop()

	client := dialWebSocket(t, addr)
	defer client.Close()
	waitForClientCount(t, h, 1)

	stale := snapshotClients(h)
	stop() // calls h.Stop()

	if err := h.sendMessageSafely(stale[0], websocket.TextMessage, []byte(`{"type":"store_update"}`)); err == nil {
		t.Fatal("write to a stopped handler's client unexpectedly succeeded")
	} else if strings.Contains(err.Error(), "panic") {
		t.Fatalf("write after Stop panicked inside the websocket library: %v", err)
	}
}
