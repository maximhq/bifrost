package handlers

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
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

func (p *connectorRealtimeProvider) RealtimeWebSocketURL(schemas.Key, string, string) (string, *schemas.BifrostError) {
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
// does with io.EOF after any failure has already been sent in-band.
func TestRelayRealtimeProviderToClientReportsOnlyRealUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		provider    schemas.RealtimeProvider // nil is a pooled websocket provider
		readErr     error
		tearingDown bool
		wantError   bool
	}{
		{name: "websocket provider dropped", readErr: net.ErrClosed, wantError: true},
		{name: "gateway ended the session", readErr: net.ErrClosed, tearingDown: true},
		{name: "connector stream ended", provider: &connectorRealtimeProvider{}, readErr: io.EOF},
		{name: "connector closed", provider: &connectorRealtimeProvider{}, readErr: net.ErrClosed},
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

			_ = (&WSRealtimeHandler{}).relayRealtimeProviderToClient(clientConn, session, &failedRealtimeUpstream{err: tc.readErr}, tc.provider, ctx, schemas.OpenAI, "gpt-realtime", schemas.Key{}, false)

			_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			_, frame, readErr := client.ReadMessage()
			if gotError := readErr == nil; gotError != tc.wantError {
				t.Fatalf("client received an error frame: %v, want %v (frame %s, read error %v)", gotError, tc.wantError, frame, readErr)
			}
		})
	}
}
