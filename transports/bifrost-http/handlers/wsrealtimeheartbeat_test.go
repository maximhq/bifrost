package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/azure"
	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestRealtimeAudioCommitAllowsResponse checks the socket relay admits manual and automatic responses once.
func TestRealtimeAudioCommitAllowsResponse(t *testing.T) {
	for _, provider := range []schemas.RealtimeProvider{&openaiProvider.OpenAIProvider{}, &azure.AzureProvider{}} {
		for _, mode := range []string{"manual", "automatic", "transcription"} {
			t.Run(fmt.Sprintf("%T/%s", provider, mode), func(t *testing.T) {
				gateway := newLiveOwnerTestGateway(t, nil)
				server, client, cleanup := dialRealtimeTestConn(t)
				defer cleanup()
				upstream, providerConn := dialLiveTestUpstream(t)
				session := bfws.NewSession(nil)
				defer session.Close()
				ctx := liveCallerCtx(t, "")
				handler := &WSRealtimeHandler{client: gateway.client}
				conn := newRealtimeClientConn(server)
				transcription := mode == "transcription"
				done := make(chan error, 1)
				go func() {
					done <- handler.relayRealtimeProviderToClient(conn, session, upstream, provider, ctx, schemas.OpenAI, "gpt-realtime-mini", schemas.Key{}, transcription)
				}()
				defer func() {
					_ = upstream.Close()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("provider relay did not terminate")
					}
				}()
				require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
				require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"input_audio_buffer.committed","item_id":"audio"}`)))
				_, raw, err := client.ReadMessage()
				require.NoError(t, err)
				require.Contains(t, string(raw), "input_audio_buffer.committed")
				if transcription {
					require.NotNil(t, session.PeekRealtimeTurnHooks())
					require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"audio","transcript":"hello"}`)))
					_, raw, err = client.ReadMessage()
					require.NoError(t, err)
					require.Contains(t, string(raw), "input_audio_transcription.completed")
					require.Nil(t, session.PeekRealtimeTurnHooks())
					return
				}
				require.Nil(t, session.PeekRealtimeTurnHooks())
				if mode == "manual" {
					stop, err := handler.processRealtimeClientMessage(conn, session, upstream, provider, ctx, schemas.OpenAI, "gpt-realtime-mini", schemas.Key{}, false, ws.TextMessage, []byte(`{"type":"response.create"}`))
					require.NoError(t, err)
					require.False(t, stop)
					require.NotNil(t, session.PeekRealtimeTurnHooks())
					require.NoError(t, providerConn.SetReadDeadline(time.Now().Add(time.Second)))
					_, raw, err = providerConn.ReadMessage()
					require.NoError(t, err)
					require.Contains(t, string(raw), "response.create")
				}
				started := session.PeekRealtimeTurnHooks()
				require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"response.created","response":{"id":"response"}}`)))
				_, raw, err = client.ReadMessage()
				require.NoError(t, err)
				require.Contains(t, string(raw), "response.created")
				require.NotNil(t, session.PeekRealtimeTurnHooks())
				if started != nil {
					require.Same(t, started, session.PeekRealtimeTurnHooks())
				}
				require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"response.done","response":{"id":"response","status":"completed"}}`)))
				_, raw, err = client.ReadMessage()
				require.NoError(t, err)
				require.Contains(t, string(raw), "response.done")
				require.Nil(t, session.PeekRealtimeTurnHooks(), "completion must release the pipeline")
			})
		}
	}
}

// TestRealtimeProviderErrorsRemainRecoverable preserves native errors and lets the provider continue.
func TestRealtimeProviderErrorsRemainRecoverable(t *testing.T) {
	for _, code := range []string{"invalid_value", "response_cancel_not_active", "rate_limit_exceeded", "server_error"} {
		t.Run(code, func(t *testing.T) {
			server, client, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			upstream, providerConn := dialLiveTestUpstream(t)
			session := bfws.NewSession(nil)
			calls, cleanups := 0, 0
			session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{
				RequestID: "turn", StartedAt: time.Now(),
				PostHookRunner: func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
					calls++
					return result, err
				},
				Cleanup: func() { cleanups++ },
			})
			done := make(chan error, 1)
			go func() {
				done <- (&WSRealtimeHandler{}).relayRealtimeProviderToClient(newRealtimeClientConn(server), session, upstream, &openaiProvider.OpenAIProvider{}, nil, schemas.OpenAI, "realtime", schemas.Key{}, false)
			}()
			defer func() {
				_ = upstream.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("provider relay did not terminate")
				}
				// Hook callbacks run on the relay goroutine.
				require.Equal(t, 1, calls)
				require.Equal(t, 1, cleanups)
			}()
			raw := []byte(fmt.Sprintf(`{"type":"error","event_id":"server_event","error":{"type":"invalid_request_error","code":%q,"message":"recoverable","param":"response.output_modalities","event_id":"client_event","extra":{"keep":true}}}`, code))
			require.NoError(t, providerConn.WriteMessage(ws.TextMessage, raw))
			require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
			_, actual, err := client.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, string(raw), string(actual))
			if code == "response_cancel_not_active" {
				// A rejected cancel must not consume the current or pending turn.
				require.NotNil(t, session.PeekRealtimeTurnHooks())
			}
			followup := []byte(`{"type":"response.done","response":{"id":"next","status":"completed"}}`)
			require.NoError(t, providerConn.WriteMessage(ws.TextMessage, followup))
			_, actual, err = client.ReadMessage()
			require.NoError(t, err, "recoverable provider error must not end the relay")
			require.Equal(t, string(followup), string(actual))
		})
	}
}

// TestRealtimeProviderClosePreservesCode forwards provider close frames without a synthetic error.
func TestRealtimeProviderClosePreservesCode(t *testing.T) {
	for _, code := range []int{ws.CloseNormalClosure, ws.CloseGoingAway, ws.ClosePolicyViolation, ws.CloseInternalServerErr, 4004} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server, client, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			upstream, providerConn := dialLiveTestUpstream(t)
			done := make(chan error, 1)
			go func() {
				done <- (&WSRealtimeHandler{}).relayRealtimeProviderToClient(newRealtimeClientConn(server), bfws.NewSession(nil), upstream, &openaiProvider.OpenAIProvider{}, nil, schemas.OpenAI, "realtime", schemas.Key{}, false)
			}()
			reason := "invalid_request_error.model_not_found"
			require.NoError(t, providerConn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(code, reason), time.Now().Add(time.Second)))
			require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
			_, data, err := client.ReadMessage()
			require.True(t, ws.IsCloseError(err, code), "got data %s and error %v", data, err)
			var closeErr *ws.CloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, reason, closeErr.Text)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("close must end the provider relay")
			}
		})
	}
}

// TestRealtimeProviderErrorBeforeAbruptClose avoids replacing an already delivered provider error.
func TestRealtimeProviderErrorBeforeAbruptClose(t *testing.T) {
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(t)
	done := make(chan error, 1)
	go func() {
		done <- (&WSRealtimeHandler{}).relayRealtimeProviderToClient(newRealtimeClientConn(server), bfws.NewSession(nil), upstream, &openaiProvider.OpenAIProvider{}, nil, schemas.OpenAI, "realtime", schemas.Key{}, false)
	}()
	raw := []byte(`{"type":"error","error":{"type":"server_error","code":"upstream_detail","message":"original failure"}}`)
	require.NoError(t, providerConn.WriteMessage(ws.TextMessage, raw))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
	_, actual, err := client.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, string(raw), string(actual))
	require.NoError(t, providerConn.Close())
	_, actual, err = client.ReadMessage()
	require.True(t, ws.IsCloseError(err, ws.CloseInternalServerErr), "unexpected replacement event %s, error %v", actual, err)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("abrupt provider closure did not terminate relay")
	}
}

// TestRealtimeTurnMetadataIsolation checks setup snapshots and unit contexts for
// concurrent connections, including the separate pre-request context used by Live.
func TestRealtimeTurnMetadataIsolation(t *testing.T) {
	type metadataKey uint8
	const userKey metadataKey = 0
	const requestOnlyKey metadataKey = 1
	for i := range 12 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			userID := fmt.Sprintf("user-%d", i)
			request, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			transport := &fasthttp.RequestCtx{}
			transport.SetUserValue(lib.FastHTTPUserValueBifrostContext, request)
			name := "custom-plugin"
			scoped := request.WithPluginScope(&name)
			scoped.SetValueForRealtimeTurns(userKey, userID)
			scoped.SetValue(requestOnlyKey, "request-only")
			scoped.SetValueForRealtimeTurns(schemas.BifrostContextKeyTraceID, "dead-trace")
			scoped.SetValueForRealtimeTurns(schemas.BifrostContextKeyExportTraceID, "dead-export")
			scoped.ReleasePluginScope()
			values := snapshotRealtimeMiddlewareValues(transport)
			preReq := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			preReq.SetValueForRealtimeTurns(metadataKey(2), "pre-request")
			withContext := snapshotRealtimeMiddlewareValuesWithContext(transport, preReq)
			liveValues := liveMiddlewareValues(values, preReq)
			cancel()
			transport.ResetUserValues()
			request.SetValueForRealtimeTurns(userKey, "after-snapshot")
			for _, snapshot := range []map[any]any{values, withContext, liveValues} {
				session := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				applyRealtimeMiddlewareValues(session, snapshot)
				for turn := range 2 {
					unit := newRealtimeTurnContext(session, fmt.Sprint(turn), "session", "", "", "", nil)
					live := newLiveMeter(nil, session, schemas.OpenAI, schemas.Key{}, "session")
					liveUnit := live.unitContext(fmt.Sprint(turn), false, "voice")
					backendUnit := live.unitContext(fmt.Sprint(turn), false, "backend")
					for _, ctx := range []*schemas.BifrostContext{unit, liveUnit, backendUnit} {
						if ctx.Value(userKey) != userID || ctx.Value(requestOnlyKey) != nil {
							t.Fatalf("unit metadata = %v, request-only = %v", ctx.Value(userKey), ctx.Value(requestOnlyKey))
						}
						if ctx.Value(schemas.BifrostContextKeyTraceID) != nil || ctx.Value(schemas.BifrostContextKeyExportTraceID) != nil {
							t.Fatal("unit inherited a dead upgrade trace")
						}
						ctx.SetValue(userKey, "unit-local")
					}
				}
				if session.Value(userKey) != userID {
					t.Fatal("unit mutated the session's metadata")
				}
			}
			if withContext[metadataKey(2)] != "pre-request" || liveValues[metadataKey(2)] != "pre-request" {
				t.Fatal("separate pre-request context lost its opted-in metadata")
			}
			request.DrainPluginLogs()
		})
	}
}

// TestRealtimeTurnMetadataPreservesAuth verifies setup values cannot replace resolved auth.
func TestRealtimeTurnMetadataPreservesAuth(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "resolved")
	applyRealtimeMiddlewareValues(ctx, map[any]any{schemas.BifrostContextKeyVirtualKey: "stale"})
	if ctx.Value(schemas.BifrostContextKeyVirtualKey) != "resolved" {
		t.Fatal("metadata replaced the resolved credential")
	}
}

// BenchmarkRealtimeMetadataHandoff measures the middleware and session snapshots
// together, excluding existing context allocation and per-turn processing.
func BenchmarkRealtimeMetadataHandoff(b *testing.B) {
	type metadataKey uint8
	for _, marked := range []bool{false, true} {
		b.Run(fmt.Sprint(marked), func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if marked {
				ctx.SetValueForRealtimeTurns(metadataKey(0), "test-alice")
			}
			transport := &fasthttp.RequestCtx{}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				transport.ResetUserValues()
				retainRealtimeTurnValues(transport, ctx)
				_ = snapshotRealtimeMiddlewareValues(transport)
			}
		})
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

// fasthttp ignores Close on a hijacked connection, so when the upstream ends the relay reading the
// client stays blocked; the teardown must release it and tell the client the session is over.
func TestRunRealtimeRelaysReleasesTheClientWhenTheUpstreamEnds(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clientAnswer bool // the client reads, so its library answers the close frame
	}{
		{name: "client answers the close", clientAnswer: true},
		{name: "silent client", clientAnswer: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverConn, client, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			clientConn := newRealtimeClientConn(serverConn)

			toClient := func() error { return nil } // the upstream ended
			toProvider := func() error {
				_, _, err := clientConn.ReadMessage()
				return err
			}
			closeBoth := func() { _ = clientConn.Close() }

			clientClose := make(chan error, 1)
			if tc.clientAnswer {
				go func() {
					_, _, err := client.ReadMessage()
					clientClose <- err
				}()
			}
			result := make(chan error, 1)
			go func() { result <- runRealtimeRelays(serverConn, toProvider, toClient, closeBoth) }()

			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("a clean shutdown reported a relay error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runRealtimeRelays stayed blocked on the client after the upstream ended")
			}
			if tc.clientAnswer {
				select {
				case err := <-clientClose:
					if !ws.IsCloseError(err, ws.CloseNormalClosure) {
						t.Fatalf("expected the client to receive a normal close frame, got %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the client never received a close frame")
				}
			}
		})
	}
}

// After the upstream ends, a client relay that fails on its own before its read is forced to return
// (a panic in a hook, say) still ends the session with that error.
func TestRunRealtimeRelaysReportsAClientFailureAfterTheUpstreamEnds(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	clientConn := newRealtimeClientConn(serverConn)

	tornDown := make(chan struct{})
	toClient := func() error { return nil } // the upstream ended
	toProvider := func() error {
		<-tornDown
		panic("hook exploded during teardown")
	}
	closeBoth := func() {
		_ = clientConn.Close()
		close(tornDown)
	}

	err := runRealtimeRelays(serverConn, toProvider, toClient, closeBoth)

	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("expected the client relay's panic to be reported, got %v", err)
	}
}

// The forced read deadline only releases the relay reading the client; a timeout from the relay
// writing to it during teardown is a failed delivery and is reported.
func TestRunRealtimeRelaysReportsAClientWriteTimeoutDuringTeardown(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	clientConn := newRealtimeClientConn(serverConn)

	tornDown := make(chan struct{})
	toProvider := func() error { return nil } // the client side ended first
	toClient := func() error {
		<-tornDown
		time.Sleep(realtimeCloseHandshakeGrace + 200*time.Millisecond) // still writing when the deadline is forced
		return &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	closeBoth := func() {
		_ = clientConn.Close()
		close(tornDown)
	}

	err := runRealtimeRelays(serverConn, toProvider, toClient, closeBoth)

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected the client write timeout to be reported, got %v", err)
	}
}

// Only the timeout the teardown sets itself is expected; a client read that timed out first still
// ended the session and is reported.
func TestRunRealtimeRelaysReportsAClientReadTimeoutBeforeTeardown(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	clientConn := newRealtimeClientConn(serverConn)

	upstreamClosed := make(chan struct{})
	toProvider := func() error {
		_ = serverConn.SetReadDeadline(time.Now().Add(-time.Second))
		_, _, err := clientConn.ReadMessage()
		return err
	}
	toClient := func() error {
		<-upstreamClosed
		return nil
	}
	closeBoth := func() {
		_ = clientConn.Close()
		close(upstreamClosed)
	}

	err := runRealtimeRelays(serverConn, toProvider, toClient, closeBoth)

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected the client read timeout to be reported, got %v", err)
	}
}

// A relay runs on its own goroutine, past the recover deferred by the upgrade callback, so a
// panic inside it (a plugin hook on the turn pipeline) must be caught by the relay runner
// itself: it ends the session like any other relay error instead of ending the process.
func TestRunRealtimeRelaysEndsSessionWhenARelayPanics(t *testing.T) {
	closed := make(chan struct{})
	var closes int
	toProvider := func() error {
		panic("plugin hook exploded")
	}
	toClient := func() error {
		<-closed // a blocking read, released by the teardown
		return net.ErrClosed
	}
	closeBoth := func() {
		closes++
		close(closed)
	}

	result := make(chan error, 1)
	go func() { result <- runRealtimeRelays(nil, toProvider, toClient, closeBoth) }()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "panic") {
			t.Fatalf("expected the panic to end the session as a relay error, got %v", err)
		}
		if strings.Contains(err.Error(), "exploded") {
			t.Fatalf("the panic value must not be echoed into the relay error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runRealtimeRelays did not return after a relay panicked")
	}
	if closes != 1 {
		t.Fatalf("expected both connections to be closed once, closeBoth ran %d times", closes)
	}
}
