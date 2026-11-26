package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultProfile is the profile of a caller whose name no profile has, so any
// number of ad-hoc callers discover the same content as alice.
const defaultProfile = "alice"

// profile is what one kind of caller discovers. Every caller whose name is the
// profile's name (or, for defaultProfile, no profile's name) gets it.
type profile struct {
	name         string
	summary      string // one line for /admin/state
	instructions string
	tools        []toolDef
	resources    []resourceDef
	templates    []templateDef
	prompts      []promptDef
	pageSize     int     // 0: lists are not paginated
	faults       []fault // always applied, before the runtime ones
}

type toolDef struct {
	tool    mcp.Tool
	handler server.ToolHandlerFunc
}

type resourceDef struct {
	resource mcp.Resource
	text     string
}

type templateDef struct {
	template mcp.ResourceTemplate
}

type promptDef struct {
	prompt mcp.Prompt
	text   string
}

// catalog returns every profile by name.
func catalog(cfg config) map[string]*profile {
	notes := func() []toolDef { return []toolDef{whoamiTool(), searchNotesTool(), createNoteTool()} }
	handbook := resourceDef{
		resource: mcp.NewResource("notes://handbook", "handbook", mcp.WithResourceDescription("How notes are organised"), mcp.WithMIMEType("text/plain")),
		text:     "Notes are grouped by project and tagged by week.",
	}
	noteTemplate := templateDef{template: mcp.NewResourceTemplate("notes://note/{id}", "note", mcp.WithTemplateDescription("One note by id"))}
	summarize := promptDef{
		prompt: mcp.NewPrompt("summarize_notes", mcp.WithPromptDescription("Summarize the notes on a topic"), mcp.WithArgument("topic", mcp.RequiredArgument())),
		text:   "Summarize every note about the topic in five bullet points.",
	}
	const notesInstructions = "Notes workspace: search before you create, and never create two notes with one title."

	profiles := []*profile{
		{
			name:         "alice",
			summary:      "baseline: three tools, instructions, a resource, a template and a prompt",
			instructions: notesInstructions,
			tools:        notes(),
			resources:    []resourceDef{handbook},
			templates:    []templateDef{noteTemplate},
			prompts:      []promptDef{summarize},
		},
		{
			name:         "bob",
			summary:      "exactly alice's discovery, so the two share one cached copy",
			instructions: notesInstructions,
			tools:        notes(),
			resources:    []resourceDef{handbook},
			templates:    []templateDef{noteTemplate},
			prompts:      []promptDef{summarize},
		},
		{
			name:         "carol",
			summary:      "alice plus a destructive delete_note tool",
			instructions: notesInstructions,
			tools:        append(notes(), deleteNoteTool()),
			resources:    []resourceDef{handbook},
			templates:    []templateDef{noteTemplate},
			prompts:      []promptDef{summarize},
		},
		{
			name:         "dave",
			summary:      "alice's tools and instructions with another resource and prompt",
			instructions: notesInstructions,
			tools:        notes(),
			resources: []resourceDef{{
				resource: mcp.NewResource("notes://archive", "archive", mcp.WithResourceDescription("Notes older than a year"), mcp.WithMIMEType("text/plain")),
				text:     "The archive holds every note older than a year.",
			}},
			templates: []templateDef{noteTemplate},
			prompts: []promptDef{{
				prompt: mcp.NewPrompt("weekly_review", mcp.WithPromptDescription("Review the week's notes")),
				text:   "List what changed in the notes this week.",
			}},
		},
		{
			name:         "erin",
			summary:      "alice's tools carrying titles, icons, annotations, output schemas and _meta",
			instructions: notesInstructions,
			tools:        []toolDef{whoamiTool(), detailedSearchNotesTool(), detailedCreateNoteTool()},
			resources:    []resourceDef{handbook},
			templates:    []templateDef{noteTemplate},
			prompts:      []promptDef{summarize},
		},
		{
			name:    "frank",
			summary: "an empty discovery: no tools, instructions, resources or prompts",
		},
		{
			name:         "grace",
			summary:      "an upstream tool named connect, the name of Bifrost's own connect tool",
			instructions: "Connect workspace: the connect tool links a calendar.",
			tools: []toolDef{whoamiTool(), {
				tool:    mcp.NewTool("connect", mcp.WithDescription("Link a calendar to the workspace."), mcp.WithString("calendar", mcp.Required(), mcp.Description("Calendar to link"))),
				handler: echoHandler,
			}},
		},
		{
			name:         "heidi",
			summary:      "hyphenated tool names, which Bifrost maps to underscores",
			instructions: "Inventory workspace: fetch-item takes an id, item-count takes nothing.",
			tools: []toolDef{whoamiTool(), {
				tool:    mcp.NewTool("fetch-item", mcp.WithDescription("Fetch one inventory item."), mcp.WithString("id", mcp.Required(), mcp.Description("Item id"))),
				handler: echoHandler,
			}, {
				tool:    mcp.NewTool("item-count", mcp.WithDescription("Count the inventory items.")),
				handler: echoHandler,
			}},
		},
		{
			name:         "ivan",
			summary:      "150 tools with large schemas, listed 50 a page",
			instructions: "Bulk workspace: every bulk tool takes the same eight fields.",
			tools:        bulkTools(150),
			pageSize:     50,
		},
		{
			name:         "legacy",
			summary:      "alice's discovery from a server older than protocol 2026-07-28, reached through initialize",
			instructions: notesInstructions,
			tools:        notes(),
			resources:    []resourceDef{handbook},
			templates:    []templateDef{noteTemplate},
			prompts:      []promptDef{summarize},
			// A server from before 2026-07-28 does not know the probe a modern
			// client opens with, which sends the client to initialize and the
			// newest revision before it.
			faults: []fault{{Method: "server/discover", Kind: faultMethodNotFound}},
		},
		{
			name:         "faulty",
			summary:      "tools/list always answers a JSON-RPC error",
			instructions: notesInstructions,
			tools:        notes(),
			faults:       []fault{{Method: "tools/list", Kind: faultError}},
		},
		{
			name:         "slow",
			summary:      fmt.Sprintf("tools/list takes %s, past Bifrost's sign-in and connect bounds", cfg.slowDelay),
			instructions: notesInstructions,
			tools:        notes(),
			faults:       []fault{{Method: "tools/list", Kind: faultDelay, DelayMS: int(cfg.slowDelay.Milliseconds())}},
		},
		{
			name:         "partial",
			summary:      "tools list fine; resources, templates and prompts answer a JSON-RPC error",
			instructions: notesInstructions,
			tools:        notes(),
			resources:    []resourceDef{handbook},
			templates:    []templateDef{noteTemplate},
			prompts:      []promptDef{summarize},
			faults: []fault{
				{Method: "resources/list", Kind: faultError},
				{Method: "resources/templates/list", Kind: faultError},
				{Method: "prompts/list", Kind: faultError},
			},
		},
		{
			name:         "admin",
			summary:      "an admin_audit tool only, for the admin sign-in that verifies a client",
			instructions: "Admin workspace: audit before you change anything.",
			tools: []toolDef{whoamiTool(), {
				tool:    mcp.NewTool("admin_audit", mcp.WithDescription("List recent administrative changes.")),
				handler: echoHandler,
			}},
		},
	}
	byName := make(map[string]*profile, len(profiles))
	for _, p := range profiles {
		byName[p.name] = p
	}
	return byName
}

// betaTool is the tool a headers caller gets on top of their profile's with
// X-Workspace: beta.
func betaTool() toolDef {
	return toolDef{
		tool:    mcp.NewTool("beta_search", mcp.WithDescription("Search with the beta ranking."), mcp.WithString("query", mcp.Required(), mcp.Description("What to look for"))),
		handler: echoHandler,
	}
}

func whoamiTool() toolDef {
	return toolDef{
		tool:    mcp.NewTool("whoami", mcp.WithDescription("Name the caller this server sees: the door, identity, profile and workspace.")),
		handler: whoamiHandler,
	}
}

func searchNotesTool() toolDef {
	return toolDef{
		tool:    mcp.NewTool("search_notes", mcp.WithDescription("Search the caller's notes."), mcp.WithString("query", mcp.Required(), mcp.Description("What to look for"))),
		handler: echoHandler,
	}
}

func createNoteTool() toolDef {
	return toolDef{
		tool: mcp.NewTool("create_note", mcp.WithDescription("Create a note."),
			mcp.WithString("title", mcp.Required(), mcp.Description("Note title")),
			mcp.WithString("body", mcp.Description("Note body"))),
		handler: echoHandler,
	}
}

func deleteNoteTool() toolDef {
	return toolDef{
		tool: mcp.NewTool("delete_note", mcp.WithDescription("Delete a note for good."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Note id")),
			mcp.WithDestructiveHintAnnotation(true)),
		handler: echoHandler,
	}
}

// noteIcon is a 1x1 SVG, small enough to sit inline in a tool definition.
var noteIcon = mcp.Icon{
	Src:      "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIxIiBoZWlnaHQ9IjEiLz4=",
	MIMEType: "image/svg+xml",
	Sizes:    []string{"any"},
}

func detailedSearchNotesTool() toolDef {
	def := searchNotesTool()
	def.tool.Title = "Search notes"
	def.tool.Icons = []mcp.Icon{noteIcon}
	def.tool.Annotations = mcp.ToolAnnotation{
		Title:          "Search notes",
		ReadOnlyHint:   mcp.ToBoolPtr(true),
		IdempotentHint: mcp.ToBoolPtr(true),
		OpenWorldHint:  mcp.ToBoolPtr(false),
	}
	def.tool.RawOutputSchema = json.RawMessage(`{"type":"object","properties":{"results":{"type":"array","items":{"type":"string"}}},"required":["results"]}`)
	def.tool.Meta = &mcp.Meta{AdditionalFields: map[string]any{"example.com/owner": "notes-team"}}
	def.handler = searchResultsHandler
	return def
}

func detailedCreateNoteTool() toolDef {
	def := createNoteTool()
	def.tool.Title = "Create a note"
	def.tool.Annotations = mcp.ToolAnnotation{
		Title:           "Create a note",
		ReadOnlyHint:    mcp.ToBoolPtr(false),
		DestructiveHint: mcp.ToBoolPtr(false),
	}
	return def
}

// bulkTools returns n tools that each take the same eight string fields.
func bulkTools(n int) []toolDef {
	fields := []string{"account", "region", "project", "environment", "owner", "label", "window", "reason"}
	tools := make([]toolDef, 0, n)
	for i := 1; i <= n; i++ {
		opts := []mcp.ToolOption{mcp.WithDescription(fmt.Sprintf(
			"Bulk operation %03d. Runs one administrative operation across every matching resource, "+
				"scoped by account, region, project and environment, and records who ran it and why.", i))}
		for _, field := range fields {
			opts = append(opts, mcp.WithString(field, mcp.Required(), mcp.Description("The "+field+" the operation is scoped to")))
		}
		tools = append(tools, toolDef{tool: mcp.NewTool(fmt.Sprintf("bulk_tool_%03d", i), opts...), handler: echoHandler})
	}
	return tools
}

// whoamiHandler names the caller the request came from.
func whoamiHandler(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c := callerFrom(ctx)
	return mcp.NewToolResultText(fmt.Sprintf("door=%s identity=%s profile=%s workspace=%s", c.door, c.identity, c.profile, c.workspace)), nil
}

// echoHandler answers any other tool with its name, the caller and the
// arguments it was called with, so a test can tell which caller's credential
// a call reached the upstream with.
func echoHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c := callerFrom(ctx)
	args, _ := json.Marshal(req.Params.Arguments)
	return mcp.NewToolResultText(fmt.Sprintf("tool=%s identity=%s profile=%s args=%s", req.Params.Name, c.identity, c.profile, args)), nil
}

// searchResultsHandler answers with the results detailedSearchNotesTool's output schema requires,
// and with echoHandler's text for clients that read only text content.
func searchResultsHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	echo, err := echoHandler(ctx, req)
	if err != nil {
		return nil, err
	}
	text, _ := echo.Content[0].(mcp.TextContent)
	args, _ := req.Params.Arguments.(map[string]any)
	query, _ := args["query"].(string)
	return mcp.NewToolResultStructured(map[string]any{"results": []string{"note matching " + query}}, text.Text), nil
}

// sortedNames returns the names of tools in order.
func sortedNames(tools []toolDef) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.tool.Name
	}
	sort.Strings(names)
	return names
}

// profileFor returns the profile name for an identity.
func profileFor(profiles map[string]*profile, identity string) string {
	if _, ok := profiles[strings.ToLower(identity)]; ok {
		return strings.ToLower(identity)
	}
	return defaultProfile
}
