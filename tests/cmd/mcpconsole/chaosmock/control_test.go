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
		"reply and reply_error":     {"name": "x", "reply": map[string]any{"content": []any{}}, "reply_error": map[string]any{"code": -32021, "message": "m"}},
		"reply_error not an object": {"name": "x", "reply_error": "boom"},
		"reply_error without code":  {"name": "x", "reply_error": map[string]any{"message": "m"}},
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

// An overlay verb with a trailing slash is still the overlay verb: without an
// identity it is refused instead of replacing the base list, and with one it
// sets that identity's own list.
func TestControlOverlayVerbsKeepTheirMeaningWithATrailingSlash(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"
	for _, tc := range []struct {
		verb, field string
		item        map[string]any
	}{
		{"prompts/overlay/", "prompts", map[string]any{"name": "p1"}},
		{"resources/overlay/", "resources", map[string]any{"uri": "notes://1", "name": "n1"}},
		{"templates/overlay/", "templates", map[string]any{"uriTemplate": "notes://{id}", "name": "t1"}},
	} {
		resp, _ := post(t, base+"/"+tc.verb, map[string]any{tc.field: []any{tc.item}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s with no identity: expected 400, got %d", tc.verb, resp.StatusCode)
		}
		resp, _ = post(t, base+"/"+tc.verb, map[string]any{"identity": "alice", tc.field: []any{tc.item}})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s for alice: expected 200, got %d", tc.verb, resp.StatusCode)
		}
	}
}

func TestControlSetsPromptsResourcesAndTemplatesPerIdentity(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	resp, state := post(t, base+"/prompts/set", map[string]any{"prompts": []any{map[string]any{
		"name": "summarize", "description": "shared",
		"arguments": []any{map[string]any{"name": "topic", "required": true}},
	}}})
	if resp.StatusCode != http.StatusOK || len(state.Prompts) != 1 || state.Prompts[0].Name != "summarize" || len(state.Prompts[0].Arguments) != 1 {
		t.Fatalf("expected summarize with its argument in the state, got %d %+v", resp.StatusCode, state.Prompts)
	}

	_, state = post(t, base+"/prompts/overlay", map[string]any{"identity": "alice", "prompts": []any{map[string]any{"name": "alice_only"}}})
	if v := state.PromptViews["alice"]; len(v) != 1 || v[0].Name != "alice_only" {
		t.Errorf("expected alice's own prompt list in the state, got %+v", state.PromptViews)
	}

	_, state = post(t, base+"/resources/set", map[string]any{"resources": []any{map[string]any{"uri": "file:///readme.md", "mimeType": "text/markdown"}}})
	if len(state.Resources) != 1 || state.Resources[0].URI != "file:///readme.md" || state.Resources[0].MimeType != "text/markdown" {
		t.Errorf("expected the readme with its mime type, got %+v", state.Resources)
	}

	// An overlay with no list shows that identity nothing, which is different
	// from having no overlay at all.
	_, state = post(t, base+"/resources/overlay", map[string]any{"identity": "alice"})
	if v, ok := state.ResourceViews["alice"]; !ok || len(v) != 0 {
		t.Errorf("expected alice to have an empty resource overlay, got %+v", state.ResourceViews)
	}

	_, state = post(t, base+"/templates/set", map[string]any{"templates": []any{map[string]any{"uriTemplate": "notes://{id}", "name": "note"}}})
	if len(state.ResourceTemplates) != 1 || state.ResourceTemplates[0].URITemplate != "notes://{id}" {
		t.Errorf("expected the notes template, got %+v", state.ResourceTemplates)
	}
	_, state = post(t, base+"/templates/overlay", map[string]any{"identity": "bob", "templates": []any{map[string]any{"uriTemplate": "docs://{path}"}}})
	if v := state.TemplateViews["bob"]; len(v) != 1 || v[0].URITemplate != "docs://{path}" {
		t.Errorf("expected bob's own template, got %+v", state.TemplateViews)
	}

	// Clearing returns an identity to the base list.
	_, state = post(t, base+"/prompts/overlay", map[string]any{"identity": "alice", "clear": true})
	if _, ok := state.PromptViews["alice"]; ok {
		t.Errorf("expected alice's prompt overlay to be gone, got %+v", state.PromptViews)
	}

	// Tool overlays read back the same way.
	_, state = post(t, base+"/tools/overlay", map[string]any{"identity": "alice", "tools": []any{map[string]any{"name": "alice_tool"}}})
	if v := state.Views["alice"]; len(v) != 1 || v[0].Name != "alice_tool" {
		t.Errorf("expected alice's tool overlay in the state, got %+v", state.Views)
	}
}

func TestControlRejectsPromptsResourcesAndTemplatesThatCannotBeServed(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	// Each verb accepts a body it can serve, so the refusals below are about
	// the bodies rather than the verbs.
	for verb, body := range map[string]map[string]any{
		"prompts/set":       {"prompts": []any{map[string]any{"name": "p", "arguments": []any{map[string]any{"name": "a"}}}}},
		"prompts/overlay":   {"identity": "alice", "prompts": []any{map[string]any{"name": "p"}}},
		"resources/set":     {"resources": []any{map[string]any{"uri": "a://x"}}},
		"resources/overlay": {"identity": "alice", "resources": []any{}},
		"templates/set":     {"templates": []any{map[string]any{"uriTemplate": "notes://{id}/{rest}"}}},
		"templates/overlay": {"identity": "alice", "templates": []any{map[string]any{"uriTemplate": "static://only"}}},
		"tools/add":         {"tool": map[string]any{"name": "x", "outputSchema": map[string]any{"type": "object"}}},
	} {
		if resp, _ := post(t, base+"/"+verb, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: expected a valid body to be accepted, got %d", verb, resp.StatusCode)
		}
	}

	for name, tc := range map[string]struct {
		verb string
		body map[string]any
	}{
		"prompt without a name":          {"prompts/set", map[string]any{"prompts": []any{map[string]any{"description": "x"}}}},
		"resource without a uri":         {"resources/set", map[string]any{"resources": []any{map[string]any{"name": "r"}}}},
		"template without a uriTemplate": {"templates/set", map[string]any{"templates": []any{map[string]any{"name": "t"}}}},
		"identity on a set verb":         {"prompts/set", map[string]any{"identity": "alice", "prompts": []any{map[string]any{"name": "p"}}}},
		"overlay without an identity":    {"prompts/overlay", map[string]any{"prompts": []any{map[string]any{"name": "p"}}}},
		"invalid prompt in an overlay":   {"prompts/overlay", map[string]any{"identity": "alice", "prompts": []any{map[string]any{}}}},
		"clearing an overlay never set":  {"resources/overlay", map[string]any{"identity": "nobody", "clear": true}},
		"clearing a template overlay":    {"templates/overlay", map[string]any{"identity": "nobody", "clear": true}},
		"output schema not an object":    {"tools/add", map[string]any{"tool": map[string]any{"name": "x", "outputSchema": "text"}}},
		"output schema with reply_env":   {"tools/add", map[string]any{"tool": map[string]any{"name": "x", "outputSchema": map[string]any{"type": "object"}, "reply_env": "CHAOS_TAG"}}},
	} {
		resp, _ := post(t, base+"/"+tc.verb, tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, resp.StatusCode)
		}
	}
}

func TestControlSetsAndClearsFaultsAndInstructionsForOneIdentity(t *testing.T) {
	fleet := controlFleet(t)
	base := fleet.URL() + "/control/alpha"

	_, state := post(t, base+"/faults/identity", map[string]any{"identity": "alice", "faults": map[string]any{"list_fail": true}})
	if f, ok := state.IdentityFaults["alice"]; !ok || !f.ListFail {
		t.Fatalf("expected alice's faults in the state, got %+v", state.IdentityFaults)
	}
	if state.Faults.ListFail {
		t.Errorf("an identity's faults must not change the server-wide set, got %+v", state.Faults)
	}

	_, state = post(t, base+"/instructions", map[string]any{"identity": "alice", "text": "alice reads this"})
	if state.IdentityInstructions["alice"] != "alice reads this" || state.Instructions != "" {
		t.Errorf("expected alice's instructions only, got %q / %+v", state.Instructions, state.IdentityInstructions)
	}

	for name, tc := range map[string]struct {
		verb string
		body map[string]any
	}{
		"identity faults without an identity":    {"faults/identity", map[string]any{"faults": map[string]any{"list_fail": true}}},
		"identity faults without faults":         {"faults/identity", map[string]any{"identity": "alice"}},
		"clearing faults never set":              {"faults/identity", map[string]any{"identity": "bob", "clear": true}},
		"clearing instructions never set":        {"instructions", map[string]any{"identity": "bob", "clear": true}},
		"identity instructions without any text": {"instructions", map[string]any{"identity": "bob"}},
	} {
		resp, _ := post(t, base+"/"+tc.verb, tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, resp.StatusCode)
		}
	}

	_, state = post(t, base+"/faults/identity", map[string]any{"identity": "alice", "clear": true})
	if _, ok := state.IdentityFaults["alice"]; ok {
		t.Errorf("expected alice's faults cleared, got %+v", state.IdentityFaults)
	}
	_, state = post(t, base+"/instructions", map[string]any{"identity": "alice", "clear": true})
	if _, ok := state.IdentityInstructions["alice"]; ok {
		t.Errorf("expected alice's instructions cleared, got %+v", state.IdentityInstructions)
	}

	// faults/clear returns every caller to healthy, not only the server-wide set.
	post(t, base+"/faults/identity", map[string]any{"identity": "carol", "faults": map[string]any{"status": 503}})
	_, state = post(t, base+"/faults/clear", nil)
	if len(state.IdentityFaults) != 0 {
		t.Errorf("expected faults/clear to drop identity faults too, got %+v", state.IdentityFaults)
	}
}

func TestFleetResetRestoresSeededPromptsResourcesAndIdentityState(t *testing.T) {
	fleet := chaosmock.NewFleet()
	seed := chaosmock.Seed{
		Tools:                []chaosmock.Tool{{Name: "seed_tool"}},
		Prompts:              []chaosmock.Prompt{{Name: "summarize"}},
		PromptViews:          map[string][]chaosmock.Prompt{"alice": {{Name: "alice_only"}}},
		Resources:            []chaosmock.Resource{{URI: "file:///readme.md"}},
		ResourceViews:        map[string][]chaosmock.Resource{"alice": {}},
		Templates:            []chaosmock.ResourceTemplate{{URITemplate: "notes://{id}"}},
		TemplateViews:        map[string][]chaosmock.ResourceTemplate{"bob": {{URITemplate: "docs://{path}"}}},
		IdentityInstructions: map[string]string{"alice": "alice reads this"},
		IdentityFaults:       map[string]chaosmock.Faults{"bob": {ListFail: true}},
	}
	if err := seed.Validate(); err != nil {
		t.Fatalf("a valid seed was refused: %v", err)
	}
	fleet.AddSeeded("library", seed)
	fleet.Start()
	t.Cleanup(fleet.Stop)
	base := fleet.URL() + "/control/library"

	post(t, base+"/prompts/set", map[string]any{"prompts": []any{}})
	post(t, base+"/resources/overlay", map[string]any{"identity": "alice", "clear": true})
	post(t, base+"/faults/identity", map[string]any{"identity": "bob", "clear": true})
	post(t, base+"/instructions", map[string]any{"identity": "alice", "clear": true})

	resp, err := http.Post(fleet.URL()+"/control", "application/json", strings.NewReader(`{"reset":"library"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("reset failed: %v %v", err, resp)
	}
	resp.Body.Close()
	srv, _ := fleet.Get("library")
	state := srv.State()
	if len(state.Prompts) != 1 || len(state.PromptViews["alice"]) != 1 || len(state.Resources) != 1 || len(state.ResourceTemplates) != 1 || len(state.TemplateViews["bob"]) != 1 {
		t.Errorf("expected the seeded prompts, resources and templates back, got %+v", state)
	}
	if v, ok := state.ResourceViews["alice"]; !ok || len(v) != 0 {
		t.Errorf("expected alice's empty resource overlay back, got %+v", state.ResourceViews)
	}
	if state.IdentityInstructions["alice"] != "alice reads this" || !state.IdentityFaults["bob"].ListFail {
		t.Errorf("expected the seeded identity instructions and faults back, got %+v / %+v", state.IdentityInstructions, state.IdentityFaults)
	}

	for name, bad := range map[string]chaosmock.Seed{
		"prompt without a name":       {Prompts: []chaosmock.Prompt{{}}},
		"view resource without a uri": {ResourceViews: map[string][]chaosmock.Resource{"alice": {{Name: "x"}}}},
		"template without a uri":      {Templates: []chaosmock.ResourceTemplate{{Name: "t"}}},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s: expected the seed to be refused", name)
		}
	}
}
