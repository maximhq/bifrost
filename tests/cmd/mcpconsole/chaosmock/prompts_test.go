package chaosmock_test

import (
	"testing"

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
