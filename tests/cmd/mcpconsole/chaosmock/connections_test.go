package chaosmock_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// Connection accounting is what separates a leak from a reconnect storm, which
// look identical from the request counters alone. These pin the distinction so
// an invariant built on it means what it says.

func TestKeepAliveClientReusesOneConnection(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "some_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()
	initClient(t, c, ctx)

	for i := 0; i < 10; i++ {
		if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
			t.Fatalf("tools/list %d failed: %v", i, err)
		}
	}

	conns := fleet.Connections()
	// Many requests over one kept-alive connection. If this ever reports ten,
	// the client stopped reusing connections, which is itself worth knowing.
	if conns.Opened != 1 {
		t.Errorf("expected 1 connection for a keep-alive client, got %d", conns.Opened)
	}
	if conns.Open != 1 {
		t.Errorf("expected the connection to still be open, got %d", conns.Open)
	}
	if got := srv.Counters().Snapshot().Requests; got < 11 {
		t.Errorf("expected at least 11 requests, got %d", got)
	}
}

// TestSeparateClientsShareThePooledConnection pins a behaviour that would
// otherwise produce a badly wrong invariant. Every mcp-go streamable HTTP
// transport is built with a bare &http.Client{}, whose nil Transport resolves
// to the package-level http.DefaultTransport. That pool is shared process-wide,
// so several clients to the same host reuse one keep-alive connection rather
// than dialling their own.
//
// The consequence for this harness: TCP connection counts are NOT the reconnect
// signal for HTTP transports. Session count is, because every initialize mints
// a new session id regardless of which socket carried it. Connection counting
// stays meaningful for stream-per-connection transports and for spotting a true
// fleet-wide leak, which is why it is kept.
func TestSeparateClientsShareThePooledConnection(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "some_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const clients = 4
	for i := 0; i < clients; i++ {
		c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"))
		if err != nil {
			t.Fatalf("failed to create client %d: %v", i, err)
		}
		initClient(t, c, ctx)
		if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
			t.Fatalf("tools/list for client %d failed: %v", i, err)
		}
		_ = c.Close()
	}

	// Sessions are the reliable count: one per client, every time.
	if got := srv.Counters().Snapshot().Sessions; got != clients {
		t.Errorf("expected %d sessions, got %d", clients, got)
	}
	// Connections are not. Asserting the pooling explicitly means a future
	// change to how clients are built shows up here rather than silently
	// invalidating an invariant that rests on it.
	if got := fleet.Connections().Opened; got >= clients {
		t.Errorf("expected pooled reuse to keep connections below %d, got %d", clients, got)
	}
}

// TestUnpooledClientsDialSeparateConnections is the counterpart: given a
// transport that will not reuse sockets, the connection counter tracks dials
// exactly. A scenario that genuinely needs per-connection accounting has to opt
// out of pooling this way, and accept that doing so changes what it measures.
func TestUnpooledClientsDialSeparateConnections(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "some_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const clients = 3
	for i := 0; i < clients; i++ {
		httpClient := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"),
			transport.WithHTTPBasicClient(httpClient))
		if err != nil {
			t.Fatalf("failed to create client %d: %v", i, err)
		}
		initClient(t, c, ctx)
		_ = c.Close()
	}

	if got := fleet.Connections().Opened; got < clients {
		t.Errorf("expected at least %d connections without keep-alive, got %d", clients, got)
	}
	if got := srv.Counters().Snapshot().Sessions; got != clients {
		t.Errorf("expected %d sessions, got %d", clients, got)
	}
}

func TestRefusedConnectionClosesWithoutAResponse(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "some_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	srv.SetFaults(chaosmock.Faults{Refuse: true})

	// A refused connection is severed rather than answered, so this is a
	// transport error and not an HTTP status. A client must be able to tell
	// the two apart, since only one of them carries a retry hint.
	resp, err := http.Post(fleet.ServerURL("alpha"), "application/json", nil)
	if err == nil {
		defer resp.Body.Close()
		t.Fatalf("expected a transport error while refusing, got status %d", resp.StatusCode)
	}

	srv.ClearFaults()
	resp2, err := http.Post(fleet.ServerURL("alpha"), "application/json", nil)
	if err != nil {
		t.Fatalf("expected service to resume once the fault cleared: %v", err)
	}
	defer resp2.Body.Close()
}
