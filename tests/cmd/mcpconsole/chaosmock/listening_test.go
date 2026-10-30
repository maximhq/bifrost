package chaosmock_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// The streamable HTTP transport has two server-to-client channels: the stream
// returned in response to a POST, and a standalone long-lived GET stream. Only
// the second can carry an unsolicited notification, and a client only has one
// if it explicitly opted in. These tests pin both halves of that, because the
// convergence bound every tool-list assertion uses depends on which is true for
// a given configuration.

func startFleetForListening(t *testing.T) (*chaosmock.Fleet, *chaosmock.Server) {
	t.Helper()
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "some_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)
	return fleet, srv
}

func initClient(t *testing.T, c *client.Client, ctx context.Context) {
	t.Helper()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start client: %v", err)
	}
	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-listening", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, req); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
}

// waitFor polls until cond holds or the budget runs out. Polling rather than
// signalling keeps the assertion honest about what an observer can actually
// see, which is the same position the harness itself is in.
func waitFor(t *testing.T, budget time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestListeningClientReceivesToolListChanged(t *testing.T) {
	fleet, srv := startFleetForListening(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"),
		transport.WithContinuousListening())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()

	var received atomic.Int64
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == chaosmock.NotificationToolListChanged {
			received.Add(1)
		}
	})

	initClient(t, c, ctx)

	// The listening stream is opened asynchronously, so the push has to wait
	// for it. Asserting on the server's own view first means a failure below
	// distinguishes "never connected" from "connected but ignored it".
	if !waitFor(t, 5*time.Second, func() bool { return srv.OpenStreams() == 1 }) {
		t.Fatalf("expected the client to open 1 listening stream, got %d", srv.OpenStreams())
	}

	srv.Tools().Add(chaosmock.Tool{Name: "new_tool"})
	if delivered := srv.NotifyToolListChanged(); delivered != 1 {
		t.Fatalf("expected the notification to reach 1 stream, reached %d", delivered)
	}

	if !waitFor(t, 5*time.Second, func() bool { return received.Load() == 1 }) {
		t.Fatalf("expected the client to receive the notification, got %d", received.Load())
	}
}

// TestNonListeningClientOpensNoStream is the counterpart, and the more
// important of the two: without the opt-in there is no channel at all, so an
// unsolicited notification has nowhere to go. A configuration in that state can
// only learn about a tool change by asking again.
func TestNonListeningClientOpensNoStream(t *testing.T) {
	fleet, srv := startFleetForListening(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()

	var received atomic.Int64
	c.OnNotification(func(mcp.JSONRPCNotification) { received.Add(1) })

	initClient(t, c, ctx)

	// Give it the same grace the listening client got, so this is a real
	// absence rather than a race won by the assertion.
	time.Sleep(500 * time.Millisecond)

	if got := srv.OpenStreams(); got != 0 {
		t.Fatalf("expected no listening stream without the opt-in, got %d", got)
	}
	srv.Tools().Add(chaosmock.Tool{Name: "new_tool"})
	if delivered := srv.NotifyToolListChanged(); delivered != 0 {
		t.Fatalf("expected the notification to reach nobody, reached %d", delivered)
	}
	if got := received.Load(); got != 0 {
		t.Fatalf("expected no notification to arrive, got %d", got)
	}

	// The change is still discoverable, just only by asking. This is the slow
	// path a non-listening configuration is permanently on.
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("expected the new tool to be visible on request, got %d tools", len(res.Tools))
	}
}

func TestDroppedStreamEndsWithoutKillingTheSession(t *testing.T) {
	fleet, srv := startFleetForListening(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"),
		transport.WithContinuousListening())
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()
	initClient(t, c, ctx)

	if !waitFor(t, 5*time.Second, func() bool { return srv.OpenStreams() == 1 }) {
		t.Fatalf("expected 1 listening stream, got %d", srv.OpenStreams())
	}

	srv.DropStreams()

	// The session survives the stream going away, so ordinary requests keep
	// working. A client that treats a dropped stream as a dead session would
	// re-initialize here, which the session counter would reveal.
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list should still work after the stream dropped: %v", err)
	}
	if got := srv.SessionCount(); got != 1 {
		t.Errorf("expected the session to survive a dropped stream, got %d sessions", got)
	}
}
