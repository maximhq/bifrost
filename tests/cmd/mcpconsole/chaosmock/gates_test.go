package chaosmock_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// A gate is a barrier: it holds the requests it matches until a scenario opens
// it, which is how a test lines two requests up in a known order instead of
// hoping the scheduler does.

// gateState returns the named gate from a server's state.
func gateState(t *testing.T, srv *chaosmock.Server, name string) chaosmock.GateState {
	t.Helper()
	for _, g := range srv.State().Gates {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no gate %q in the state", name)
	return chaosmock.GateState{}
}

// waitForHeld polls until the named gate holds n requests.
func waitForHeld(t *testing.T, srv *chaosmock.Server, name string, n int64) {
	t.Helper()
	if !waitFor(t, 5*time.Second, func() bool { return gateState(t, srv, name).Waiting == n }) {
		t.Fatalf("expected gate %q to hold %d, got %+v", name, n, gateState(t, srv, name))
	}
}

// inBackground runs fn and returns a channel that carries its error once it
// finishes.
func inBackground(fn func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

// stillRunning reports whether done has not finished yet.
func stillRunning(done <-chan error) bool {
	select {
	case err := <-done:
		_ = err
		return false
	default:
		return true
	}
}

func TestGateHoldsAMethodUntilOpened(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{Tools: []chaosmock.Tool{{Name: "echo_tool"}}})
	base := fleet.URL() + "/control/alpha"
	alice, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")

	resp, state := post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "listing", "method": "tools/list"}})
	if resp.StatusCode != http.StatusOK || len(state.Gates) != 1 || state.Gates[0].Open {
		t.Fatalf("expected one closed gate, got %d %+v", resp.StatusCode, state.Gates)
	}

	done := inBackground(func() error { _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); return err })
	waitForHeld(t, srv, "listing", 1)
	if !stillRunning(done) {
		t.Fatalf("the listing finished while the gate was closed")
	}
	// The held request has arrived and is counted, but was not answered.
	if c := srv.Counters().Snapshot(); c.Methods["tools/list"] != 1 || c.ToolsList != 0 {
		t.Errorf("expected the held listing counted but unanswered, got methods %v, tools_list %d", c.Methods, c.ToolsList)
	}

	_, state = post(t, base+"/gates/open", map[string]any{"name": "listing"})
	if err := <-done; err != nil {
		t.Fatalf("the released listing failed: %v", err)
	}
	if g := gateState(t, srv, "listing"); !g.Open || g.Waiting != 0 || g.Passed != 1 || g.TimedOut != 0 {
		t.Errorf("expected an open gate that passed one, got %+v", g)
	}

	// An open gate lets later requests straight through, and counts them.
	if _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("a listing through the open gate failed: %v", err)
	}
	if g := gateState(t, srv, "listing"); g.Passed != 2 {
		t.Errorf("expected the open gate to have passed two, got %+v", g)
	}
	// Other methods never meet the gate.
	var call mcp.CallToolRequest
	call.Params.Name = "echo_tool"
	if _, err := alice.CallTool(ctx, call); err != nil {
		t.Fatalf("tools/call failed: %v", err)
	}
	if g := gateState(t, srv, "listing"); g.Passed != 2 {
		t.Errorf("a call must not pass a listing gate, got %+v", g)
	}
}

func TestGateNarrowedToOneIdentityHoldsOnlyThatCaller(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{Tools: []chaosmock.Tool{{Name: "echo_tool"}}})
	base := fleet.URL() + "/control/alpha"
	alice, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")
	bob, _, _ := connectAs(t, fleet.ServerURL("alpha"), "token-b")

	post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "alice_listing", "method": "tools/list", "identity": "alice"}})

	done := inBackground(func() error { _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); return err })
	waitForHeld(t, srv, "alice_listing", 1)

	// bob is not alice, so he lists while she is held.
	if _, err := bob.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("bob's listing must not be held: %v", err)
	}
	if !stillRunning(done) {
		t.Fatalf("alice's listing finished while her gate was closed")
	}
	if g := gateState(t, srv, "alice_listing"); g.Waiting != 1 || g.Passed != 0 {
		t.Errorf("expected only alice held, got %+v", g)
	}

	post(t, base+"/gates/open", map[string]any{})
	if err := <-done; err != nil {
		t.Fatalf("alice's released listing failed: %v", err)
	}
}

// A held request is answered against the server as it is when its gate lets it
// go, so a token revoked while a streamable request is held refuses it. A legacy
// SSE message carries no credential of its own: it rides the session its stream
// authenticated, so the same revocation leaves it to be answered.
func TestGateReleasedRequestMeetsARevokedToken(t *testing.T) {
	t.Run("streamable HTTP authenticates again and is refused", func(t *testing.T) {
		fleet, srv := identityFleet(t, chaosmock.Seed{Tools: []chaosmock.Tool{{Name: "echo_tool"}}})
		base := fleet.URL() + "/control/alpha"
		alice, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")
		post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "listing", "method": "tools/list"}})

		done := inBackground(func() error { _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); return err })
		waitForHeld(t, srv, "listing", 1)
		if !srv.RevokeToken("token-a") {
			t.Fatalf("token-a was not granted")
		}
		post(t, base+"/gates/open", map[string]any{"name": "listing"})
		if err := <-done; err == nil {
			t.Fatalf("expected the released listing to be refused once its token was revoked")
		}
	})

	t.Run("a legacy SSE message rides its stream's session and is answered", func(t *testing.T) {
		fleet, srv := identityFleet(t, chaosmock.Seed{Tools: []chaosmock.Tool{{Name: "echo_tool"}}})
		base := fleet.URL() + "/control/alpha"
		alice, ctx := connectSSE(t, fleet.SSEURL("alpha"), transport.WithHeaders(map[string]string{"Authorization": "Bearer token-a"}))
		post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "listing", "method": "tools/list"}})

		done := inBackground(func() error { _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); return err })
		waitForHeld(t, srv, "listing", 1)
		if !srv.RevokeToken("token-a") {
			t.Fatalf("token-a was not granted")
		}
		post(t, base+"/gates/open", map[string]any{"name": "listing"})
		if err := <-done; err != nil {
			t.Fatalf("expected the released SSE listing to be answered on its session, got %v", err)
		}
	})
}

func TestGateHoldsTheTokenEndpoint(t *testing.T) {
	fleet, srv := oauthFleet(t, map[string]any{"clients": []any{map[string]any{"client_id": "c1", "client_secret": "s1"}}})
	base := fleet.URL() + "/control/alpha"
	post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "exchange", "endpoint": "token"}})

	code, verifier := codeFor(t, fleet, "alice")
	done := inBackground(func() error {
		resp, err := http.PostForm(fleet.URL()+"/s/alpha/as/token", url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
			"client_id": {"c1"}, "client_secret": {"s1"}, "redirect_uri": {testRedirect},
		})
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return &statusError{resp.StatusCode}
		}
		return nil
	})
	waitForHeld(t, srv, "exchange", 1)
	if st := srv.State().OAuth; st.Counters.TokenCode != 1 || len(st.Grants) != 0 {
		t.Errorf("expected the held exchange counted but not yet granted, got %+v / %d grants", st.Counters, len(st.Grants))
	}

	post(t, base+"/gates/open", map[string]any{"name": "exchange"})
	if err := <-done; err != nil {
		t.Fatalf("the released exchange failed: %v", err)
	}
	if g := gateState(t, srv, "exchange"); g.Passed != 1 {
		t.Errorf("expected the token gate to have passed one, got %+v", g)
	}
	// Discovery and authorization never meet a token gate.
	if g := gateState(t, srv, "exchange"); g.Waiting != 0 || g.TimedOut != 0 {
		t.Errorf("expected nothing else held, got %+v", g)
	}
}

type statusError struct{ status int }

func (e *statusError) Error() string { return http.StatusText(e.status) }

func TestGateLetsARequestThroughAfterItsTimeout(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{Tools: []chaosmock.Tool{{Name: "echo_tool"}}})
	base := fleet.URL() + "/control/alpha"
	alice, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")

	_, state := post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "slow", "method": "tools/list", "timeout": "100ms"}})
	if len(state.Gates) != 1 || state.Gates[0].Timeout != chaosmock.Duration(100*time.Millisecond) {
		t.Fatalf("expected the gate's timeout in the state, got %+v", state.Gates)
	}

	start := time.Now()
	if _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("a listing let through by the timeout failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("expected the listing held for the timeout, it took %v", elapsed)
	}
	if g := gateState(t, srv, "slow"); g.Open || g.TimedOut != 1 || g.Passed != 0 || g.Waiting != 0 {
		t.Errorf("expected a still-closed gate with one timed out, got %+v", g)
	}

	// Without a timeout of its own a gate waits 20s.
	_, state = post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "default", "method": "ping"}})
	for _, g := range state.Gates {
		if g.Name == "default" && g.Timeout != chaosmock.Duration(20*time.Second) {
			t.Errorf("expected a 20s default timeout, got %+v", g)
		}
	}
}

func TestGateCountsARequestWhoseClientGaveUp(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{Tools: []chaosmock.Tool{{Name: "echo_tool"}}})
	base := fleet.URL() + "/control/alpha"
	alice, _, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")
	post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "listing", "method": "tools/list"}})

	// The client gives up only once the gate holds its request: a deadline could
	// end before the request arrives, and a request never sent is not abandoned.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := inBackground(func() error { _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); return err })
	waitForHeld(t, srv, "listing", 1)
	cancel()
	if err := <-done; err == nil {
		t.Fatalf("expected the listing to give up while held")
	}
	if !waitFor(t, 5*time.Second, func() bool { return gateState(t, srv, "listing").Abandoned == 1 }) {
		t.Fatalf("expected one abandoned request, got %+v", gateState(t, srv, "listing"))
	}
	// An abandoned request is never served.
	if g, c := gateState(t, srv, "listing"), srv.Counters().Snapshot(); g.Waiting != 0 || c.ToolsList != 0 {
		t.Errorf("expected nothing held and nothing answered, got %+v, tools_list %d", g, c.ToolsList)
	}
}

func TestGateClearReleasesWhatItHolds(t *testing.T) {
	fleet, srv := identityFleet(t, chaosmock.Seed{Tools: []chaosmock.Tool{{Name: "echo_tool"}}})
	base := fleet.URL() + "/control/alpha"
	alice, ctx, _ := connectAs(t, fleet.ServerURL("alpha"), "token-a")
	post(t, base+"/gates/set", map[string]any{"gate": map[string]any{"name": "listing", "method": "tools/list"}})

	done := inBackground(func() error { _, err := alice.ListTools(ctx, mcp.ListToolsRequest{}); return err })
	waitForHeld(t, srv, "listing", 1)
	_, state := post(t, base+"/gates/clear", map[string]any{"name": "listing"})
	if err := <-done; err != nil {
		t.Fatalf("the listing released by clear failed: %v", err)
	}
	if len(state.Gates) != 0 {
		t.Errorf("expected no gates after clear, got %+v", state.Gates)
	}
}

func TestControlRefusesGatesThatCannotHoldAnything(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"
	// The verbs exist and accept a gate that can hold something, so the
	// refusals below are about the bodies.
	for _, step := range []struct {
		verb string
		body map[string]any
	}{
		{"gates/set", map[string]any{"gate": map[string]any{"name": "open_one", "method": "ping"}}},
		{"gates/open", map[string]any{"name": "open_one"}},
		{"gates/set", map[string]any{"gate": map[string]any{"name": "token_one", "endpoint": "token", "timeout": "1s"}}},
		{"gates/clear", map[string]any{"name": "token_one"}},
	} {
		if resp, _ := post(t, base+"/"+step.verb, step.body); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %v: expected 200, got %d", step.verb, step.body, resp.StatusCode)
		}
	}

	for name, tc := range map[string]struct {
		verb string
		body map[string]any
	}{
		"gate without a body":              {"gates/set", map[string]any{}},
		"gate without a name":              {"gates/set", map[string]any{"gate": map[string]any{"method": "tools/list"}}},
		"gate with nothing to match":       {"gates/set", map[string]any{"gate": map[string]any{"name": "g"}}},
		"gate on a method and an endpoint": {"gates/set", map[string]any{"gate": map[string]any{"name": "g", "method": "tools/list", "endpoint": "token"}}},
		"gate on another endpoint":         {"gates/set", map[string]any{"gate": map[string]any{"name": "g", "endpoint": "authorize"}}},
		"endpoint gate for one identity":   {"gates/set", map[string]any{"gate": map[string]any{"name": "g", "endpoint": "token", "identity": "alice"}}},
		"negative timeout":                 {"gates/set", map[string]any{"gate": map[string]any{"name": "g", "method": "ping", "timeout": "-1s"}}},
		"opening an unknown gate":          {"gates/open", map[string]any{"name": "nobody"}},
		"opening an open gate":             {"gates/open", map[string]any{"name": "open_one"}},
		"opening when none is closed":      {"gates/open", map[string]any{}},
		"clearing an unknown gate":         {"gates/clear", map[string]any{"name": "nobody"}},
	} {
		resp, _ := post(t, base+"/"+tc.verb, tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, resp.StatusCode)
		}
	}
}
