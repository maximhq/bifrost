package handlers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	azureProvider "github.com/maximhq/bifrost/core/providers/azure"
	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"
)

// TestRealtimeAudioTurnLifecycle exercises the actual websocket relay against a
// local upstream. An audio commit accepts input; it must not reserve a response.
func TestRealtimeAudioTurnLifecycle(t *testing.T) {
	for _, provider := range []struct {
		name schemas.ModelProvider
		impl schemas.RealtimeProvider
	}{
		{schemas.OpenAI, &openaiProvider.OpenAIProvider{}},
		{schemas.Azure, &azureProvider.AzureProvider{}},
	} {
		for _, mode := range []string{"manual", "automatic", "transcription"} {
			t.Run(string(provider.name)+"/"+mode, func(t *testing.T) {
				serverConn, peer, cleanup := dialRealtimeTestConn(t)
				defer cleanup()
				clientConn := newRealtimeClientConn(serverConn)
				client, err := bifrost.Init(t.Context(), schemas.BifrostConfig{
					Account: wsSpanTestAccount{},
					Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
				})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Shutdown()
				h := &WSRealtimeHandler{client: client}
				connections := make(chan *ws.Conn, 1)
				release := make(chan struct{})
				upgrader := ws.Upgrader{}
				upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					connections <- conn
					<-release
				}))
				defer upstreamServer.Close()
				defer close(release)
				upstream, err := bfws.DialUpstream("ws"+strings.TrimPrefix(upstreamServer.URL, "http"), nil, provider.name, "test-key-1", nil)
				if err != nil {
					t.Fatal(err)
				}
				var remote *ws.Conn
				select {
				case remote = <-connections:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream upgrade timed out")
				}
				session := bfws.NewSession(serverConn)
				ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
				key := schemas.Key{ID: "test-key-1"}
				transcription := mode == "transcription"
				done := make(chan struct{})
				go func() {
					defer close(done)
					_ = h.relayRealtimeProviderToClient(clientConn, session, upstream, provider.impl, ctx, provider.name, "gpt-realtime", key, transcription)
				}()
				defer func() {
					_ = upstream.Close()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("provider relay did not stop")
					}
				}()
				read := func(conn *ws.Conn) string {
					t.Helper()
					_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
					_, data, err := conn.ReadMessage()
					if err != nil {
						t.Fatalf("read websocket: %v", err)
					}
					return string(data)
				}
				providerEvent := func(data string) {
					t.Helper()
					_ = remote.SetWriteDeadline(time.Now().Add(2 * time.Second))
					if err := remote.WriteMessage(ws.TextMessage, []byte(data)); err != nil {
						t.Fatal(err)
					}
					if got := read(peer); got != data {
						t.Fatalf("provider event = %s, want %s", got, data)
					}
				}
				clientEvent := func(data string) {
					t.Helper()
					stop, err := h.processRealtimeClientMessage(clientConn, session, upstream, provider.impl, ctx, provider.name, "gpt-realtime", key, transcription, ws.TextMessage, []byte(data))
					if stop || err != nil {
						t.Fatalf("client event stopped relay: stop=%v err=%v", stop, err)
					}
				}
				for turn := 0; turn < 2; turn++ {
					clientEvent(`{"type":"input_audio_buffer.commit"}`)
					if got := read(remote); !strings.Contains(got, "input_audio_buffer.commit") {
						t.Fatal(got)
					}
					providerEvent(`{"type":"input_audio_buffer.committed","item_id":"audio-input"}`)
					if transcription {
						if session.PeekRealtimeTurnHooks() == nil {
							t.Fatal("transcription commit did not start hooks")
						}
						providerEvent(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"audio-input","transcript":"hello"}`)
					} else {
						if mode == "manual" {
							clientEvent(`{"type":"response.create"}`)
							_ = remote.SetReadDeadline(time.Now().Add(2 * time.Second))
							_, data, err := remote.ReadMessage()
							if err != nil {
								t.Fatalf("response.create never reached upstream; gateway returned %s", read(peer))
							}
							if !strings.Contains(string(data), "response.create") {
								t.Fatal(string(data))
							}
						} else if session.PeekRealtimeTurnHooks() != nil {
							t.Fatal("audio commit incorrectly started response hooks before automatic response.created")
						}
						previousHooks := session.PeekRealtimeTurnHooks()
						providerEvent(`{"type":"response.created","response":{"id":"response-1","status":"in_progress"}}`)
						hooks := session.PeekRealtimeTurnHooks()
						if hooks == nil {
							t.Fatal("response.created did not start hooks")
						}
						if mode == "manual" && (previousHooks == nil || hooks != previousHooks) {
							t.Fatal("response.created replaced the client-created turn hooks")
						}
						clientEvent(`{"type":"response.create"}`)
						if got := read(peer); !strings.Contains(got, "active response in progress") {
							t.Fatalf("duplicate response error = %s", got)
						}
						if session.PeekRealtimeTurnHooks() != hooks {
							t.Fatal("duplicate replaced active hooks")
						}
						providerEvent(`{"type":"response.done","response":{"id":"response-1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
					}
					if session.PeekRealtimeTurnHooks() != nil {
						t.Fatal("completed turn left hooks active")
					}
				}
			})
		}
	}
}

// dialRealtimeTestConn returns the server side of a live websocket connection.
//
// The upgrade handler is held open until cleanup runs, so the connection stays
// valid for the duration of the test.
func dialRealtimeTestConn(t *testing.T) (*ws.Conn, *ws.Conn, func()) {
	t.Helper()

	upgrader := ws.FastHTTPUpgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(*fasthttp.RequestCtx) bool { return true },
	}

	serverConns := make(chan *ws.Conn, 1)
	release := make(chan struct{})
	released := make(chan struct{})

	r := router.New()
	r.GET("/realtime", func(ctx *fasthttp.RequestCtx) {
		_ = upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			serverConns <- conn
			<-release
			close(released)
		})
	})

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = srv.Serve(ln) }()

	client, _, err := ws.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/realtime", nil)
	if err != nil {
		_ = srv.Shutdown()
		t.Fatalf("dial: %v", err)
	}

	var serverConn *ws.Conn
	select {
	case serverConn = <-serverConns:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server side of the connection")
	}

	return serverConn, client, func() {
		close(release)
		<-released
		_ = client.Close()
		_ = srv.Shutdown()
	}
}

func TestDiscoverRealtimeTranscriptionModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "nested transcription model",
			message: `{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":" openai/gpt-4o-transcribe "}}}}}`,
			want:    "openai/gpt-4o-transcribe",
		},
		{name: "wrong event type", message: `{"type":"input_audio_buffer.append","session":{"audio":{"input":{"transcription":{"model":"openai/gpt-4o-transcribe"}}}}}`},
		{name: "top-level model", message: `{"type":"session.update","model":"openai/gpt-4o-transcribe"}`},
		{name: "invalid JSON", message: `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := discoverRealtimeTranscriptionModel([]byte(tt.message)); got != tt.want {
				t.Fatalf("discoverRealtimeTranscriptionModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPinRealtimeTranscriptionModel(t *testing.T) {
	t.Parallel()

	provider := &openaiProvider.OpenAIProvider{}
	tests := []struct {
		name                 string
		transcriptionSession bool
		message              string
		wantModel            string
	}{
		{
			name:                 "transcription session uses pinned alias-resolved model",
			transcriptionSession: true,
			message:              `{"type":"session.update","session":{"type":"transcription","audio":{"input":{"transcription":{"model":"openai/transcription-alias","language":"en"}}}}}`,
			wantModel:            "gpt-4o-transcribe",
		},
		{
			name:                 "normal realtime keeps input transcription model",
			transcriptionSession: false,
			message:              `{"type":"session.update","session":{"type":"realtime","model":"gpt-realtime","audio":{"input":{"transcription":{"model":"openai/whisper-1","language":"en"}}}}}`,
			wantModel:            "whisper-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			event, err := schemas.ParseRealtimeEvent([]byte(tt.message))
			if err != nil {
				t.Fatalf("ParseRealtimeEvent() error = %v", err)
			}
			if err := pinRealtimeTranscriptionModel(event, "gpt-4o-transcribe", tt.transcriptionSession); err != nil {
				t.Fatalf("pinRealtimeTranscriptionModel() error = %v", err)
			}
			sanitizeRealtimeSessionEventForProvider(event)
			serialized, err := provider.ToProviderRealtimeEvent(event)
			if err != nil {
				t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
			}

			var payload struct {
				Session struct {
					Audio struct {
						Input struct {
							Transcription struct {
								Model    string `json:"model"`
								Language string `json:"language"`
							} `json:"transcription"`
						} `json:"input"`
					} `json:"audio"`
				} `json:"session"`
			}
			if err := json.Unmarshal(serialized, &payload); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if payload.Session.Audio.Input.Transcription.Model != tt.wantModel {
				t.Fatalf("transcription model = %q, want %q", payload.Session.Audio.Input.Transcription.Model, tt.wantModel)
			}
			if payload.Session.Audio.Input.Transcription.Language != "en" {
				t.Fatalf("transcription language = %q, want en", payload.Session.Audio.Input.Transcription.Language)
			}
		})
	}
}

func TestSnapshotRealtimeMiddlewareValuesWithContext(t *testing.T) {
	t.Parallel()

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "transport-virtual-key-id")
	ctx.SetUserValue(schemas.BifrostContextKeyTraceID, "dead-upgrade-trace")

	bifrostCtx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, "selected-key")

	values := snapshotRealtimeMiddlewareValuesWithContext(ctx, bifrostCtx)
	if got := values[schemas.BifrostContextKeyGovernanceVirtualKeyID]; got != "transport-virtual-key-id" {
		t.Fatalf("governance virtual key ID = %v, want transport-virtual-key-id", got)
	}
	if got := values[schemas.BifrostContextKeySelectedKeyID]; got != "selected-key" {
		t.Fatalf("selected key ID = %v, want selected-key", got)
	}
	if _, ok := values[schemas.BifrostContextKeyTraceID]; ok {
		t.Fatal("upgrade trace ID must not be inherited by realtime turns")
	}
}

func TestBufferRealtimeTranscriptionBootstrapPreservesFrames(t *testing.T) {
	serverConn, peerConn, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)
	frames := []realtimeWebSocketFrame{
		{messageType: ws.BinaryMessage, data: []byte{0, 1, 2, 3}},
		{messageType: ws.TextMessage, data: []byte(`{"type":"input_audio_buffer.append","audio":"AQID"}`)},
		{messageType: ws.TextMessage, data: []byte(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":"openai/gpt-4o-transcribe"}}}}}`)},
	}

	writeErr := make(chan error, 1)
	go func() {
		for _, frame := range frames {
			if err := peerConn.WriteMessage(frame.messageType, frame.data); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	buffered, model, err := bufferRealtimeTranscriptionBootstrap(client)
	if err != nil {
		t.Fatalf("bufferRealtimeTranscriptionBootstrap() error = %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write bootstrap frames: %v", err)
	}
	if model != "openai/gpt-4o-transcribe" {
		t.Fatalf("model = %q, want %q", model, "openai/gpt-4o-transcribe")
	}
	if len(buffered) != len(frames) {
		t.Fatalf("buffered frame count = %d, want %d", len(buffered), len(frames))
	}
	for i := range frames {
		if buffered[i].messageType != frames[i].messageType || string(buffered[i].data) != string(frames[i].data) {
			t.Fatalf("buffered[%d] = (%d, %q), want (%d, %q)", i, buffered[i].messageType, buffered[i].data, frames[i].messageType, frames[i].data)
		}
	}
}

// TestRealtimeStopHeartbeatWaitsForPingGoroutine pins the invariant that makes
// the realtime upgrade handler safe to return from: once stopHeartbeat returns,
// no goroutine can still be writing to the client connection.
//
// fasthttp nils out the hijacked connection's net.Conn the moment the handler
// returns, so a ping still in flight at that point dereferences nil. There is no
// recover on the heartbeat goroutine, so that panic is fatal to the process.
func TestRealtimeStopHeartbeatWaitsForPingGoroutine(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)
	client.pingInterval = time.Millisecond
	client.startHeartbeat()

	// Let the heartbeat tick a few times so it is genuinely running.
	time.Sleep(50 * time.Millisecond)

	client.stopHeartbeat()

	select {
	case <-client.heartbeatDone:
	default:
		t.Fatal("stopHeartbeat returned while the ping goroutine was still running")
	}
}

// TestRealtimeStopHeartbeatWithoutStart covers the early-return paths that fail
// before the heartbeat is ever started.
func TestRealtimeStopHeartbeatWithoutStart(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.stopHeartbeat()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stopHeartbeat blocked when the heartbeat had never been started")
	}
}

// TestRealtimeStopHeartbeatIsIdempotent guards the deferred-stop paths, which
// can run more than once as a session unwinds.
func TestRealtimeStopHeartbeatIsIdempotent(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)
	client.pingInterval = time.Millisecond
	client.startHeartbeat()
	time.Sleep(20 * time.Millisecond)

	client.stopHeartbeat()
	client.stopHeartbeat()
}
