package chaosmock_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// TestRealClientReadsPromptsAndResources points mcp-go's client at a server given a prompt, a
// resource and a template, and checks every method and the reads the server recorded.
func TestRealClientReadsPromptsAndResources(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "echo"})
	if err := srv.SetPrompts([]chaosmock.Prompt{{
		Name:        "greet",
		Arguments:   []chaosmock.PromptArgument{{Name: "name", Required: true}},
		Text:        "Hello, {name}",
		Completions: map[string][]string{"name": {"Ada", "Alan", "Grace"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.SetResources(
		[]chaosmock.Resource{{URI: "file:///notes.txt", Text: "hello"}},
		[]chaosmock.ResourceTemplate{{URITemplate: "file:///docs/{name}", Text: "doc {uri}"}},
	); err != nil {
		t.Fatal(err)
	}
	c, ctx := connect(t, fleet.ServerURL("alpha"))

	prompts, err := c.ListPrompts(ctx, mcp.ListPromptsRequest{})
	if err != nil || len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != "greet" || len(prompts.Prompts[0].Arguments) != 1 {
		t.Fatalf("prompts/list = %+v, %v", prompts, err)
	}
	get := mcp.GetPromptRequest{}
	get.Params.Name = "greet"
	get.Params.Arguments = map[string]string{"name": "Ada"}
	got, err := c.GetPrompt(ctx, get)
	if err != nil || len(got.Messages) != 1 || got.Messages[0].Content.(mcp.TextContent).Text != "Hello, Ada" {
		t.Fatalf("prompts/get = %+v, %v", got, err)
	}
	get.Params.Name = "missing"
	if _, err := c.GetPrompt(ctx, get); err == nil {
		t.Fatal("prompts/get of an unknown prompt must fail")
	}

	resources, err := c.ListResources(ctx, mcp.ListResourcesRequest{})
	if err != nil || len(resources.Resources) != 1 || resources.Resources[0].URI != "file:///notes.txt" {
		t.Fatalf("resources/list = %+v, %v", resources, err)
	}
	templates, err := c.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
	if err != nil || len(templates.ResourceTemplates) != 1 {
		t.Fatalf("resources/templates/list = %+v, %v", templates, err)
	}
	read := mcp.ReadResourceRequest{}
	read.Params.URI = "file:///notes.txt"
	contents, err := c.ReadResource(ctx, read)
	if err != nil || contents.Contents[0].(mcp.TextResourceContents).Text != "hello" {
		t.Fatalf("resources/read = %+v, %v", contents, err)
	}
	read.Params.URI = "file:///docs/a"
	contents, err = c.ReadResource(ctx, read)
	if err != nil || contents.Contents[0].(mcp.TextResourceContents).Text != "doc file:///docs/a" {
		t.Fatalf("resources/read through the template = %+v, %v", contents, err)
	}
	read.Params.URI = "file:///missing"
	if _, err := c.ReadResource(ctx, read); err == nil {
		t.Fatal("resources/read of an unknown URI must fail")
	}

	complete := mcp.CompleteRequest{}
	complete.Params.Ref = mcp.PromptReference{Type: "ref/prompt", Name: "greet"}
	complete.Params.Argument.Name = "name"
	complete.Params.Argument.Value = "A"
	completed, err := c.Complete(ctx, complete)
	if err != nil || len(completed.Completion.Values) != 2 {
		t.Fatalf("completion/complete = %+v, %v", completed, err)
	}

	reads := srv.Reads()
	if len(reads) != 4 || reads[0].Method != "prompts/get" || reads[0].Arguments["name"] != "Ada" || reads[3].Method != "completion/complete" || reads[3].Value != "A" {
		t.Fatalf("reads = %+v", reads)
	}

	// A counters reset clears the reads log but keeps the configured prompts.
	post(t, fleet.URL()+"/control/alpha/counters/reset", nil)
	if reads := srv.Reads(); len(reads) != 0 {
		t.Fatalf("counters/reset must clear the reads log, got %+v", reads)
	}
	if prompts, err := c.ListPrompts(ctx, mcp.ListPromptsRequest{}); err != nil || len(prompts.Prompts) != 1 {
		t.Fatalf("counters/reset must keep the prompts, prompts/list = %+v, %v", prompts, err)
	}
}

// TestServerWithoutPromptsAdvertisesToolsOnly checks a server given no prompts or resources keeps
// the tools-only handshake every other scenario relies on.
func TestServerWithoutPromptsAdvertisesToolsOnly(t *testing.T) {
	fleet, _ := newFleetWithTools(t, chaosmock.Tool{Name: "echo"})
	c, _ := connect(t, fleet.ServerURL("alpha"))
	caps := c.GetServerCapabilities()
	if caps.Tools == nil || caps.Prompts != nil || caps.Resources != nil {
		t.Fatalf("capabilities = %+v", caps)
	}
}

// rpcResult posts one JSON-RPC message and decodes its result, failing on a
// JSON-RPC error.
func rpcResult(t *testing.T, url, session, body string) map[string]any {
	t.Helper()
	status, _, raw := rawPost(t, url, session, body)
	if status != http.StatusOK {
		t.Fatalf("%s returned %d: %s", body, status, raw)
	}
	var out struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error != nil {
		t.Fatalf("%s did not succeed: %s", body, raw)
	}
	return out.Result
}

// TestSeededServerServesItsPromptsAndResourcesOnTheRawWire boots a server whose prompts,
// resources and templates come from its seed, and reads them as an anonymous caller.
func TestSeededServerServesItsPromptsAndResourcesOnTheRawWire(t *testing.T) {
	fleet := chaosmock.NewFleet()
	fleet.AddSeeded("alpha", chaosmock.Seed{
		Prompts: []chaosmock.Prompt{{Name: "summarize", Text: "Summarize {topic}"}},
		Resources: []chaosmock.Resource{
			{URI: "notes://pinned", Text: "pinned"}, {URI: "file:///b.txt", Text: "b"}, {URI: "file:///a.txt", Text: "a"},
		},
		Templates: []chaosmock.ResourceTemplate{{URITemplate: "notes://{id}", Text: "note {uri}"}, {URITemplate: "static://only", Text: "static {uri}"}},
	})
	fleet.Start()
	t.Cleanup(fleet.Stop)
	url := fleet.ServerURL("alpha")

	_, session, raw := rawPost(t, url, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	var init struct {
		Result struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &init); err != nil {
		t.Fatalf("bad initialize: %s", raw)
	}
	// Seeded prompts and resources are advertised like ones the control API set.
	caps := init.Result.Capabilities
	if string(caps["prompts"]) != "{}" || string(caps["resources"]) != "{}" || string(caps["completions"]) != "{}" || caps["tools"] == nil {
		t.Errorf("expected tools, prompts, resources and completions, got %s", raw)
	}

	listed := rpcResult(t, url, session, `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`)
	if at(listed, "resources", 0, "uri") != "notes://pinned" || at(listed, "resources", 2, "uri") != "file:///a.txt" {
		t.Errorf("expected resources in the order the seed gave them, got %v", listed)
	}

	got := rpcResult(t, url, session, `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"summarize","arguments":{"topic":"notes"}}}`)
	if at(got, "messages", 0, "content", "text") != "Summarize notes" {
		t.Errorf("expected the seeded prompt's text with its argument filled in, got %v", got)
	}

	for uri, want := range map[string]string{
		// A listed resource wins over a template that also matches it.
		"notes://pinned": "pinned",
		"notes://7":      "note notes://7",
		// A template with no variables matches only itself.
		"static://only": "static static://only",
	} {
		read := rpcResult(t, url, session, `{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"`+uri+`"}}`)
		if text := at(read, "contents", 0, "text"); text != want {
			t.Errorf("%s: expected %q, got %v", uri, want, text)
		}
	}

	_, _, raw = rawPost(t, url, session, `{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"static://only/more"}}`)
	if !json.Valid(raw) || at(decode(t, string(raw)), "error", "code") != float64(-32002) {
		t.Errorf("expected a uri past a literal template to be not found, got %s", raw)
	}
}

// TestPromptViewFollowsTheSessionIdentityOverSSE checks that a legacy SSE message is answered
// from the view of the identity its stream authenticated as.
func TestPromptViewFollowsTheSessionIdentityOverSSE(t *testing.T) {
	fleet := chaosmock.NewFleet()
	fleet.AddSeeded("alpha", chaosmock.Seed{
		Auth:        &chaosmock.Auth{Bearer: map[string]string{"token-a": "alice"}},
		PromptViews: map[string][]chaosmock.Prompt{"alice": {{Name: "alice_only", Text: "alice's prompt"}}},
	})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := client.NewSSEMCPClient(fleet.SSEURL("alpha"), transport.WithHeaders(map[string]string{"Authorization": "Bearer token-a"}))
	if err != nil {
		t.Fatalf("failed to create SSE client: %v", err)
	}
	defer c.Close()
	initClient(t, c, ctx)

	var get mcp.GetPromptRequest
	get.Params.Name = "alice_only"
	res, err := c.GetPrompt(ctx, get)
	if err != nil {
		t.Fatalf("prompts/get over SSE failed: %v", err)
	}
	if text, ok := mcp.AsTextContent(res.Messages[0].Content); !ok || text.Text != "alice's prompt" {
		t.Errorf("expected alice's own prompt, got %+v", res.Messages)
	}
}
