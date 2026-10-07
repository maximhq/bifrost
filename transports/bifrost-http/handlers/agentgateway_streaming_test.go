package handlers

import (
	"context"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// disconnectDetectionBudget bounds how long the test waits for a vanished
// downstream client to be noticed. It is a failure guard, not a synchronization
// device: every wait below completes as soon as the channel it watches fires.
const disconnectDetectionBudget = 30 * time.Second

// executorStream is one live upstream stream whose lifetime the test drives
// explicitly, so every streaming assertion is made on channels rather than on
// sleeps.
type executorStream struct {
	events chan a2a.Event
	done   chan struct{} // closed when the upstream side of this stream stops
	ctxErr chan error    // receives the upstream request context's error, if any
}

// streamingUpstream is the upstream agent behind the gateway. It implements the
// SDK request handler directly rather than going through an executor, because an
// executor's event sequence is terminated by the SDK as soon as it yields a
// message; this test needs a stream whose end it controls. The embedded nil
// interface supplies the rest of the method set at compile time — only the
// streaming method is ever called.
type streamingUpstream struct {
	a2asrv.RequestHandler
	created chan *executorStream
}

func (e *streamingUpstream) SendStreamingMessage(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	stream := &executorStream{
		events: make(chan a2a.Event, 8),
		done:   make(chan struct{}),
		ctxErr: make(chan error, 1),
	}
	e.created <- stream
	return func(yield func(a2a.Event, error) bool) {
		defer close(stream.done)
		for {
			select {
			case <-ctx.Done():
				stream.ctxErr <- ctx.Err()
				return
			case event := <-stream.events:
				if !yield(event, nil) {
					return
				}
			}
		}
	}
}

// TestAgentGatewaySSEStreamsIncrementallyOverFastHTTPRoute drives a real SSE
// stream through the registered fasthttp route, which is the only path that
// exercises the fasthttp-to-net/http bridge the gateway actually serves on. The
// net/http handler alone cannot prove this: the bridge decides between buffering
// the whole response and streaming it, so a regression there would silently turn
// every A2A stream into one flush at the end without failing any core test.
//
// Two properties are asserted end to end:
//
//   - Events reach the client while the upstream stream is still open, which is
//     only possible if the bridge flushes incrementally.
//   - A downstream disconnect ends the upstream stream and releases its client
//     generation lease, proven by the manager shutting down (which blocks until
//     the last lease is released) rather than hanging.
func TestAgentGatewaySSEStreamsIncrementallyOverFastHTTPRoute(t *testing.T) {
	upstreamHandler := &streamingUpstream{created: make(chan *executorStream, 4)}
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	// The upstream must advertise streaming, without which the SDK client the
	// gateway builds silently downgrades every stream to a unary send.
	card := &a2a.AgentCard{
		Name:                "stream-fixture",
		Version:             "1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true},
	}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(upstreamHandler))

	store := &agentAuthStore{}
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: false}}
	manager, err := agent.NewManager(context.Background(), store, nil, "http://gateway", http.DefaultClient, agent.ManagerConfig{AuthPolicy: AgentGatewayAuthPolicy(config)})
	require.NoError(t, err)
	closeManager := sync.OnceFunc(manager.Close)
	defer closeManager()
	_, err = manager.Create(context.Background(), agent.CreateRequest{Name: "stream-fixture", AgentCardURL: upstream.URL + a2asrv.WellKnownAgentCardPath})
	require.NoError(t, err)

	h := NewAgentGatewayHandler(manager, nil, config, nil)
	r := router.New()
	h.RegisterProtocolRoutes(r, AgentGatewayAuthenticationMiddleware(config, &agentAuthCache{store: store}, nil))
	server := &fasthttp.Server{Handler: r.Handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown() //nolint:errcheck

	client, err := a2aclient.NewFromEndpoints(
		context.Background(),
		[]*a2a.AgentInterface{a2a.NewAgentInterface("http://"+ln.Addr().String()+"/agents/a2a/stream-fixture"+agent.GatewayJSONRPCPathSuffix, a2a.TransportProtocolJSONRPC)},
		a2aclient.WithJSONRPCTransport(http.DefaultClient),
	)
	require.NoError(t, err)
	defer client.Destroy() //nolint:errcheck

	streamCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	downstreamEvents := make(chan a2a.Event, 8)
	downstreamErrs := make(chan error, 4)
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for event, streamErr := range client.SendStreamingMessage(streamCtx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))}) {
			if streamErr != nil {
				downstreamErrs <- streamErr
				return
			}
			select {
			case downstreamEvents <- event:
			case <-streamCtx.Done():
				return
			}
		}
	}()

	var upstreamStream *executorStream
	select {
	case upstreamStream = <-upstreamHandler.created:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream stream was never opened through the fasthttp route")
	}

	nextDownstreamEvent := func(what string) a2a.Event {
		t.Helper()
		select {
		case event := <-downstreamEvents:
			return event
		case streamErr := <-downstreamErrs:
			t.Fatalf("the stream failed instead of delivering %s: %v", what, streamErr)
			return nil
		case <-time.After(10 * time.Second):
			t.Fatalf("%s never reached the downstream client", what)
			return nil
		}
	}

	// Each event must arrive while the upstream stream is still open. A bridge
	// that buffered the response would deliver nothing until the stream ended.
	upstreamStream.events <- a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("one"))
	require.NotNil(t, nextDownstreamEvent("the first event"))
	select {
	case <-upstreamStream.done:
		t.Fatal("the upstream stream ended, so the first event proves nothing about incremental delivery")
	default:
	}

	upstreamStream.events <- a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("two"))
	require.NotNil(t, nextDownstreamEvent("the second event"), "events are delivered one at a time, not as a single flush at the end")

	// The downstream client vanishes. A closed TCP peer is only discovered by
	// writing to it — the first write after the close still succeeds and only the
	// one after the reset fails — so the disconnect is noticed on a subsequent
	// write to the SSE stream. The gateway's own keep-alive supplies that write on
	// an idle stream, but only once per keep-alive interval, so the upstream here
	// keeps producing events until it is torn down. That models a live agent; the
	// assertions below still wait on channels, never on elapsed time.
	disconnect()
	stopProducing := make(chan struct{})
	defer close(stopProducing)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopProducing:
				return
			case <-upstreamStream.done:
				return
			case <-ticker.C:
				select {
				case upstreamStream.events <- a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("after-disconnect")):
				default:
				}
			}
		}
	}()

	select {
	case ctxErr := <-upstreamStream.ctxErr:
		require.Error(t, ctxErr, "the upstream request context ends when the downstream client goes away")
	case <-upstreamStream.done:
	case <-time.After(disconnectDetectionBudget):
		t.Fatal("the downstream disconnect never terminated the upstream stream")
	}
	select {
	case <-upstreamStream.done:
	case <-time.After(disconnectDetectionBudget):
		t.Fatal("the upstream stream never finished")
	}
	select {
	case <-consumerDone:
	case <-time.After(disconnectDetectionBudget):
		t.Fatal("the downstream consumer never finished")
	}

	// Manager shutdown retires every client generation and blocks until the last
	// lease is released, so completing it proves the stream released its lease.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		closeManager()
	}()
	select {
	case <-closed:
	case <-time.After(disconnectDetectionBudget):
		t.Fatal("the stream never released its client generation lease")
	}
}
