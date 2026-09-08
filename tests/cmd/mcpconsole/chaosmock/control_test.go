package chaosmock_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// The control API is the surface a scenario drives, so its contract matters as
// much as the protocol one: a verb that silently does nothing would look
// exactly like the system under test behaving correctly.

func post(t *testing.T, url string, body any) (*http.Response, chaosmock.State) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("failed to encode body: %v", err)
		}
	}
	resp, err := http.Post(url, "application/json", &buf)
	if err != nil {
		t.Fatalf("control request to %s failed: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	var state chaosmock.State
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
			t.Fatalf("failed to decode state from %s: %v", url, err)
		}
	}
	return resp, state
}

func controlFleet(t *testing.T) *chaosmock.Fleet {
	t.Helper()
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "first_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)
	return fleet
}

func TestControlMutatesToolsAndReturnsResultingState(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	_, state := post(t, base+"/tools/add", map[string]any{
		"tool": map[string]any{"name": "second_tool", "description": "added by control"},
	})
	if len(state.Tools) != 2 {
		t.Fatalf("expected 2 tools after add, got %d", len(state.Tools))
	}

	_, state = post(t, base+"/tools/remove", map[string]any{"name": "first_tool"})
	if len(state.Tools) != 1 || state.Tools[0].Name != "second_tool" {
		t.Fatalf("expected only second_tool after remove, got %+v", state.Tools)
	}

	_, state = post(t, base+"/tools/rename", map[string]any{"from": "second_tool", "to": "renamed_tool"})
	if len(state.Tools) != 1 || state.Tools[0].Name != "renamed_tool" {
		t.Fatalf("expected renamed_tool, got %+v", state.Tools)
	}
	// A rename keeps the description, which is what distinguishes it from a
	// remove followed by an add.
	if state.Tools[0].Description != "added by control" {
		t.Errorf("rename must preserve the description, got %q", state.Tools[0].Description)
	}

	_, state = post(t, base+"/tools/describe", map[string]any{"name": "renamed_tool", "text": "new text"})
	if state.Tools[0].Description != "new text" {
		t.Errorf("expected the description to change, got %q", state.Tools[0].Description)
	}

	// Every mutation advances the revision, so a scenario can tell a genuine
	// change from a re-list that happened to produce equal content.
	if state.Revision < 4 {
		t.Errorf("expected the revision to advance with each mutation, got %d", state.Revision)
	}
}

func TestControlRejectsUnknownVerbAndUnknownField(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	resp, _ := post(t, base+"/tools/obliterate", map[string]any{"name": "first_tool"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown verb, got %d", resp.StatusCode)
	}

	// A typo in a field name must fail loudly. Accepting it silently would
	// leave a scenario asserting against a mutation that never happened.
	resp, _ = post(t, base+"/tools/add", map[string]any{"toool": map[string]any{"name": "x"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown field, got %d", resp.StatusCode)
	}
}

func TestControlFaultsAffectALiveClient(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()
	initClient(t, c, ctx)

	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list should work before any fault: %v", err)
	}

	post(t, base+"/faults", map[string]any{"faults": map[string]any{"list_fail": true}})
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err == nil {
		t.Fatalf("expected tools/list to fail while list_fail is set")
	}

	post(t, base+"/faults/clear", nil)
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list should recover once the fault is cleared: %v", err)
	}
}

func TestControlCounterResetKeepsLiveStreamCount(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()
	initClient(t, c, ctx)

	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}

	_, state := post(t, base+"/counters/reset", nil)
	if state.Counters.ToolsList != 0 {
		t.Errorf("expected tallies to reset, got tools_list %d", state.Counters.ToolsList)
	}
	if state.Sessions != 1 {
		t.Errorf("resetting counters must not drop live sessions, got %d", state.Sessions)
	}
}

func TestFleetControlAddsAndRemovesServers(t *testing.T) {
	fleet := controlFleet(t)

	resp, _ := post(t, fleet.URL()+"/control", map[string]any{"add": "beta"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 adding a server, got %d", resp.StatusCode)
	}
	if _, ok := fleet.Get("beta"); !ok {
		t.Fatalf("expected beta to be registered")
	}

	// A request against a removed server is a 404 rather than a refused
	// connection, and the difference is visible to a client: one reads as
	// gone, the other as unreachable.
	post(t, fleet.URL()+"/control", map[string]any{"remove": "beta"})
	res, err := http.Get(fleet.ServerURL("beta"))
	if err != nil {
		t.Fatalf("request to a removed server should still get a response: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for a removed server, got %d", res.StatusCode)
	}
}
