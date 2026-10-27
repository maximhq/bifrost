package handlers

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"github.com/valyala/fasthttp"
)

// stubRealtimeUpstream is an in-memory provider connection.
type stubRealtimeUpstream struct{ closed bool }

func (u *stubRealtimeUpstream) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (u *stubRealtimeUpstream) WriteMessage(int, []byte) error    { return nil }
func (u *stubRealtimeUpstream) Close() error                      { u.closed = true; return nil }

// connectorRealtimeProvider owns its realtime transport; the websocket methods must not be used.
type connectorRealtimeProvider struct {
	schemas.RealtimeProvider
	upstream *stubRealtimeUpstream
	model    string
	intent   string
}

func (p *connectorRealtimeProvider) ConnectRealtime(_ *schemas.BifrostContext, _ schemas.Key, model, intent string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
	p.model, p.intent = model, intent
	return p.upstream, nil
}

func (p *connectorRealtimeProvider) RealtimeWebSocketURL(*schemas.BifrostContext, schemas.Key, string, string) (string, *schemas.BifrostError) {
	panic("a RealtimeConnector provider must not be dialed as a websocket")
}

// TestOpenRealtimeUpstreamPrefersProviderConnector pins that providers with their own realtime
// transport (Bedrock Nova Sonic) are connected through it instead of the websocket pool, which
// this handler deliberately leaves nil.
func TestOpenRealtimeUpstreamPrefersProviderConnector(t *testing.T) {
	provider := &connectorRealtimeProvider{upstream: &stubRealtimeUpstream{}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	upstream, release, bifrostErr := (&WSRealtimeHandler{}).openRealtimeUpstream(ctx, provider, schemas.Bedrock, schemas.Key{}, "amazon.nova-2-sonic-v1:0", "")
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %+v", bifrostErr)
	}
	if upstream != provider.upstream || provider.model != "amazon.nova-2-sonic-v1:0" {
		t.Fatalf("connector was not used: upstream=%v model=%q", upstream, provider.model)
	}
	release()
	if !provider.upstream.closed {
		t.Fatal("release must close the provider connection")
	}
}

// failedRealtimeUpstream is a provider connection whose reads fail with err.
type failedRealtimeUpstream struct {
	stubRealtimeUpstream
	err error
}

func (u *failedRealtimeUpstream) ReadMessage() (int, []byte, error) { return 0, nil, u.err }

// TestRelayRealtimeProviderToClientReportsOnlyRealUpstreamFailures pins that an upstream read error
// reaches the client only when the provider failed: not when the gateway is tearing the session
// down itself, and not when a provider-owned transport (Bedrock) signals its stream ended, which it
// does with io.EOF after any failure has already been sent in-band, unless it ends mid-turn.
func TestRelayRealtimeProviderToClientReportsOnlyRealUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		provider    schemas.RealtimeProvider // nil is a pooled websocket provider
		readErr     error
		tearingDown bool
		turnActive  bool
		inputQueued bool // user input recorded, but the provider has not opened its response yet
		wantError   bool
	}{
		{name: "websocket provider dropped", readErr: net.ErrClosed, wantError: true},
		{name: "gateway ended the session", readErr: net.ErrClosed, tearingDown: true},
		{name: "connector stream ended", provider: &connectorRealtimeProvider{}, readErr: io.EOF},
		{name: "connector closed", provider: &connectorRealtimeProvider{}, readErr: net.ErrClosed},
		{name: "connector stream ended mid-turn", provider: &connectorRealtimeProvider{}, readErr: io.EOF, turnActive: true, wantError: true},
		{name: "connector stream ended with input awaiting a response", provider: &connectorRealtimeProvider{}, readErr: io.EOF, inputQueued: true, wantError: true},
		{name: "gateway ended a connector session mid-turn", provider: &connectorRealtimeProvider{}, readErr: net.ErrClosed, tearingDown: true, turnActive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverConn, client, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			clientConn := newRealtimeClientConn(serverConn)
			session, err := bfws.NewSessionManager(1).Create(serverConn)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			if tc.tearingDown {
				clientConn.closeDone()
			}
			if tc.inputQueued {
				session.RecordRealtimeInput("item_1", "What is the capital of France?", `{"type":"conversation.item.create"}`)
			}
			if tc.turnActive {
				session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{RequestID: "turn", StartedAt: time.Now(), PostHookRunner: func(_ *schemas.BifrostContext, _ *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
					return nil, err
				}})
			}

			_ = (&WSRealtimeHandler{}).relayRealtimeProviderToClient(clientConn, session, &failedRealtimeUpstream{err: tc.readErr}, tc.provider, ctx, schemas.OpenAI, "gpt-realtime", schemas.Key{}, false)

			_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			_, frame, readErr := client.ReadMessage()
			if gotError := readErr == nil; gotError != tc.wantError {
				t.Fatalf("client received an error frame: %v, want %v (frame %s, read error %v)", gotError, tc.wantError, frame, readErr)
			}
		})
	}
}

// TestRelayRealtimeProviderToClientBlamesTheTurnOnTheRightSide pins how a turn in progress is ended
// when the upstream read fails: as a provider failure only when the provider dropped, and as a
// client-side end when the gateway closed the upstream itself while tearing the session down.
func TestRelayRealtimeProviderToClientBlamesTheTurnOnTheRightSide(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tearingDown bool
		wantStatus  int
		wantCode    string
	}{
		{name: "provider dropped", wantStatus: 502, wantCode: "upstream_connection_error"},
		{name: "gateway ended the session", tearingDown: true, wantStatus: 499, wantCode: "client_closed_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverConn, _, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			clientConn := newRealtimeClientConn(serverConn)
			session, err := bfws.NewSessionManager(1).Create(serverConn)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			var ended *schemas.BifrostError
			session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{RequestID: "turn", StartedAt: time.Now(), PostHookRunner: func(_ *schemas.BifrostContext, _ *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				ended = err
				return nil, err
			}})
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			if tc.tearingDown {
				clientConn.closeDone()
			}

			_ = (&WSRealtimeHandler{}).relayRealtimeProviderToClient(clientConn, session, &failedRealtimeUpstream{err: net.ErrClosed}, nil, ctx, schemas.OpenAI, "gpt-realtime", schemas.Key{}, false)

			if ended == nil || ended.StatusCode == nil || ended.Error == nil || ended.Error.Code == nil {
				t.Fatalf("the turn was not ended with an error: %+v", ended)
			}
			if *ended.StatusCode != tc.wantStatus || *ended.Error.Code != tc.wantCode {
				t.Fatalf("turn ended as %d %s, want %d %s", *ended.StatusCode, *ended.Error.Code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

// TestResolveRealtimeTargetAzurePaths pins that the Azure realtime paths read Voice Live's model and Azure
// OpenAI's deployment selectors without choosing a provider: a bare model is left for routing, and a
// provider prefix still picks its provider.
func TestResolveRealtimeTargetAzurePaths(t *testing.T) {
	for _, tc := range []struct {
		path, model, deployment string
		wantProvider            schemas.ModelProvider
		wantModel               string
	}{
		{"/azure/voice-live/realtime", "gpt-realtime", "", "", "gpt-realtime"},
		{"/voice-live/realtime", "gpt-realtime-vl", "", "", "gpt-realtime-vl"},
		{"/azure/openai/realtime", "", "gpt-realtime", "", "gpt-realtime"},
		{"/azure/openai/v1/realtime", "openai/gpt-realtime", "", schemas.OpenAI, "gpt-realtime"},
		{"/v1/realtime", "gpt-realtime", "", "", "gpt-realtime"},
	} {
		provider, model, err := resolveRealtimeTarget(nil, nil, tc.path, tc.model, tc.deployment)
		if err != nil || provider != tc.wantProvider || model != tc.wantModel {
			t.Errorf("%s: %q %q %v, want %q %q", tc.path, provider, model, err, tc.wantProvider, tc.wantModel)
		}
	}
}

// TestRealtimeIntegrationTypeForPath pins the integration a realtime path reports to routing, which the
// model catalog uses to prefer that integration's provider for a bare model.
func TestRealtimeIntegrationTypeForPath(t *testing.T) {
	for path, want := range map[string]string{
		"/openai/v1/realtime":        "openai",
		"/azure/voice-live/realtime": "azure",
		"/voice-live/realtime":       "azure",
		"/azure/openai/realtime":     "azure",
		"/v1/realtime":               "",
	} {
		if got := realtimeIntegrationTypeForPath(path); got != want {
			t.Errorf("realtimeIntegrationTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestWSRealtimeRegistersAzurePaths pins the WebSocket paths Azure clients build from a resource endpoint.
func TestWSRealtimeRegistersAzurePaths(t *testing.T) {
	r := router.New()
	(&WSRealtimeHandler{}).RegisterRoutes(r)
	for _, path := range []string{"/azure/voice-live/realtime", "/voice-live/realtime", "/azure/openai/realtime", "/azure/openai/v1/realtime"} {
		if handler, _ := r.Lookup(fasthttp.MethodGet, path, &fasthttp.RequestCtx{}); handler == nil {
			t.Errorf("%s is not registered", path)
		}
	}
}
