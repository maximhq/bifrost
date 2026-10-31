package chaosmock_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

func TestControlSetsAndClearsInstructions(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	_, state := post(t, base+"/instructions", map[string]any{"text": "Call first_tool first"})
	if state.Instructions != "Call first_tool first" {
		t.Fatalf("expected the instructions in the state, got %q", state.Instructions)
	}

	// An empty body could be a misspelled text field, so it is refused
	// rather than read as "clear".
	resp, _ := post(t, base+"/instructions", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for instructions without text or clear, got %d", resp.StatusCode)
	}

	_, state = post(t, base+"/instructions", map[string]any{"clear": true})
	if state.Instructions != "" {
		t.Errorf("expected clear to remove the instructions, got %q", state.Instructions)
	}
}

func TestControlRejectsToolsThatWouldMisbehaveQuietly(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	for name, tool := range map[string]map[string]any{
		"annotations not an object": {"name": "x", "annotations": []any{"readOnlyHint"}},
		"reply not an object":       {"name": "x", "reply": "hi"},
		"two reply sources":         {"name": "x", "reply": map[string]any{"content": []any{}}, "reply_env": "CHAOS_TAG"},
		"fault on the handshake":    {"name": "x", "fault": map[string]any{"init_fail": true}},
		"fault scoped by method":    {"name": "x", "fault": map[string]any{"status": 503, "methods": []string{"tools/call"}}},
	} {
		resp, _ := post(t, base+"/tools/add", map[string]any{"tool": tool})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, resp.StatusCode)
		}
		resp, _ = post(t, base+"/tools/set", map[string]any{"tools": []any{tool}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400 from tools/set, got %d", name, resp.StatusCode)
		}
	}

	// A valid definition is accepted, and its mock-only fields read back so a
	// transcript shows what the server was told to do.
	_, state := post(t, base+"/tools/add", map[string]any{"tool": map[string]any{
		"name":        "echo_ro",
		"annotations": map[string]any{"readOnlyHint": true},
		"reply":       map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi"}}},
		"fault":       map[string]any{"status": 503, "status_once": true, "message": "busy"},
	}})
	var got *chaosmock.Tool
	for i := range state.Tools {
		if state.Tools[i].Name == "echo_ro" {
			got = &state.Tools[i]
		}
	}
	if got == nil || got.Fault == nil || got.Fault.Message != "busy" || len(got.Reply) == 0 || len(got.Annotations) == 0 {
		t.Fatalf("expected echo_ro to read back with its annotations, reply and fault, got %+v", got)
	}
}

func TestFleetResetRestoresASeededServer(t *testing.T) {
	fleet := chaosmock.NewFleet()
	catalog := chaosmock.Route{Method: "GET", Path: "/catalog.json", JSON: json.RawMessage(`{"servers":[]}`)}
	fleet.AddSeeded("library", chaosmock.Seed{
		Tools:        []chaosmock.Tool{{Name: "seed_tool"}},
		Routes:       []chaosmock.Route{catalog},
		Instructions: "seeded",
	})
	fleet.Add("plain")
	fleet.Start()
	t.Cleanup(fleet.Stop)
	base := fleet.URL() + "/control/library"

	// Change everything a scenario might, then put it back.
	post(t, base+"/routes/set", map[string]any{"routes": []any{map[string]any{"path": "/catalog.json", "body": "{"}}})
	post(t, base+"/tools/add", map[string]any{"tool": map[string]any{"name": "extra_tool"}})
	post(t, base+"/faults", map[string]any{"faults": map[string]any{"status": 500}})
	post(t, base+"/instructions", map[string]any{"clear": true})
	if res, err := http.Get(fleet.URL() + "/s/library/rest/catalog.json"); err == nil {
		res.Body.Close()
	}

	resp, err := http.Post(fleet.URL()+"/control", "application/json", strings.NewReader(`{"reset":"library"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("reset failed: %v %v", err, resp)
	}
	resp.Body.Close()
	srv, _ := fleet.Get("library")
	state := srv.State()
	if len(state.Tools) != 1 || state.Tools[0].Name != "seed_tool" || state.Faults.Status != 0 || state.Instructions != "seeded" {
		t.Errorf("expected the seeded tools, faults and instructions back, got %+v", state)
	}
	if state.REST == nil || len(state.REST.Routes) != 1 || string(state.REST.Routes[0].JSON) != `{"servers":[]}` || len(state.REST.Calls) != 0 {
		t.Errorf("expected the seeded route and an empty log, got %+v", state.REST)
	}

	// A server added without a seed, or never added, cannot be reset.
	for _, name := range []string{"plain", "nobody"} {
		resp, err := http.Post(fleet.URL()+"/control", "application/json", strings.NewReader(`{"reset":"`+name+`"}`))
		if err != nil {
			t.Fatalf("reset request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: expected 404, got %d", name, resp.StatusCode)
		}
	}
}
