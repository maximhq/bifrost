package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// HELPERS
// =============================================================================

// modeClientManager is a ClientManager with several clients, each in its own
// tool mode, so GetAvailableTools and the search meta-tools can be driven
// without a real MCP server.
type modeClientManager struct {
	clients map[string]*schemas.MCPClientState
	tools   map[string][]schemas.ChatTool
	// canned is returned by RunWithPluginPipeline without calling op, keyed by
	// the inner tool name. Lets executeTool be tested without a live connection.
	canned map[string]*schemas.BifrostMCPResponse
	// lastRequest records the request RunWithPluginPipeline saw.
	lastRequest *schemas.BifrostMCPRequest
}

func newModeClientManager() *modeClientManager {
	return &modeClientManager{
		clients: map[string]*schemas.MCPClientState{},
		tools:   map[string][]schemas.ChatTool{},
		canned:  map[string]*schemas.BifrostMCPResponse{},
	}
}

func (m *modeClientManager) addClient(name string, mode schemas.MCPToolMode, autoExecute schemas.WhiteList, tools ...schemas.ChatTool) {
	cfg := &schemas.MCPClientConfig{
		ID:                 name,
		Name:               name,
		ToolMode:           mode,
		ToolsToExecute:     []string{"*"},
		ToolsToAutoExecute: autoExecute,
	}
	cfg.NormalizeToolMode()
	toolMap := make(map[string]schemas.ChatTool, len(tools))
	for _, t := range tools {
		toolMap[t.Function.Name] = t
	}
	m.clients[name] = &schemas.MCPClientState{
		Name:            name,
		ExecutionConfig: cfg,
		ToolMap:         toolMap,
		State:           schemas.MCPConnectionStateHealthy,
	}
	m.tools[name] = tools
}

func (m *modeClientManager) GetClientByName(clientName string) *schemas.MCPClientState {
	return m.clients[clientName]
}

func (m *modeClientManager) GetClientForTool(toolName string) *schemas.MCPClientState {
	for _, c := range m.clients {
		if _, ok := c.ToolMap[toolName]; ok {
			return c
		}
	}
	return nil
}

func (m *modeClientManager) GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool {
	out := make(map[string][]schemas.ChatTool, len(m.tools))
	for k, v := range m.tools {
		out[k] = v
	}
	return out
}

func (m *modeClientManager) GetPluginPipeline() PluginPipeline             { return nil }
func (m *modeClientManager) ReleasePluginPipeline(pipeline PluginPipeline) {}
func (m *modeClientManager) AcquireClientConn(ctx *schemas.BifrostContext, state *schemas.MCPClientState) (*client.Client, func(), error) {
	return nil, func() {}, nil
}
func (m *modeClientManager) ReconnectClient(id string) error { return nil }
func (m *modeClientManager) AwaitReconnect(clientID string, budget time.Duration) (bool, error) {
	return false, nil
}
func (m *modeClientManager) RunWithPluginPipeline(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest, op MCPOpFunc) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	m.lastRequest = req
	if resp, ok := m.canned[req.GetToolName()]; ok {
		return resp, nil
	}
	return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no canned response for " + req.GetToolName()}}
}

// describedTool builds a tool with a description and a small parameter schema
// whose properties also carry descriptions.
func describedTool(name, description string, required []string, props ...schemas.Pair) schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        name,
			Description: schemas.Ptr(description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: schemas.NewOrderedMapFromPairs(props...),
				Required:   required,
			},
		},
	}
}

func prop(name, typ, description string) schemas.Pair {
	return schemas.KV(name, map[string]interface{}{"type": typ, "description": description})
}

func toolCallFor(name string, args map[string]interface{}) schemas.ChatAssistantMessageToolCall {
	raw, _ := json.Marshal(args)
	return schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("call_" + name),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr(name),
			Arguments: string(raw),
		},
	}
}

func messageText(t *testing.T, msg *schemas.ChatMessage) string {
	t.Helper()
	require.NotNil(t, msg)
	require.NotNil(t, msg.Content)
	require.NotNil(t, msg.Content.ContentStr)
	return *msg.Content.ContentStr
}

func isErrorMessage(msg *schemas.ChatMessage) bool {
	return msg != nil && msg.ChatToolMessage != nil && msg.ChatToolMessage.IsError != nil && *msg.ChatToolMessage.IsError
}

func testCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// =============================================================================
// SCHEMA HELPERS
// =============================================================================

func TestResolvedToolMode(t *testing.T) {
	assert.Equal(t, schemas.MCPToolModeDirect, (&schemas.MCPClientConfig{}).ResolvedToolMode())
	assert.Equal(t, schemas.MCPToolModeCode, (&schemas.MCPClientConfig{IsCodeModeClient: true}).ResolvedToolMode())
	assert.Equal(t, schemas.MCPToolModeSearch, (&schemas.MCPClientConfig{ToolMode: schemas.MCPToolModeSearch}).ResolvedToolMode())
	// An explicit tool_mode beats the legacy flag.
	assert.Equal(t, schemas.MCPToolModeCompact, (&schemas.MCPClientConfig{IsCodeModeClient: true, ToolMode: schemas.MCPToolModeCompact}).ResolvedToolMode())
	var nilCfg *schemas.MCPClientConfig
	assert.Equal(t, schemas.MCPToolModeDirect, nilCfg.ResolvedToolMode())
}

func TestNormalizeToolMode_KeepsLegacyFlagInSync(t *testing.T) {
	legacy := &schemas.MCPClientConfig{IsCodeModeClient: true}
	legacy.NormalizeToolMode()
	assert.Equal(t, schemas.MCPToolModeCode, legacy.ToolMode)
	assert.True(t, legacy.IsCodeModeClient)

	search := &schemas.MCPClientConfig{IsCodeModeClient: true, ToolMode: schemas.MCPToolModeSearch}
	search.NormalizeToolMode()
	assert.Equal(t, schemas.MCPToolModeSearch, search.ToolMode)
	assert.False(t, search.IsCodeModeClient, "a rolled-back binary must not see this client as code mode")

	empty := &schemas.MCPClientConfig{}
	empty.NormalizeToolMode()
	assert.Equal(t, schemas.MCPToolModeDirect, empty.ToolMode)
	assert.False(t, empty.IsCodeModeClient)
}

// =============================================================================
// COMPACT MODE
// =============================================================================

func TestCompactToolDefinition_StripsDescriptionsKeepsSchema(t *testing.T) {
	tool := describedTool("svc-create_issue", "Create an issue in the tracker.", []string{"title"},
		prop("title", "string", "Issue title"),
		// A parameter literally named "description" must survive: it is a
		// property name, not the schema keyword.
		prop("description", "string", "Issue body"),
		schemas.KV("labels", map[string]interface{}{
			"type":        "array",
			"description": "Labels to apply",
			"items":       map[string]interface{}{"type": "string", "description": "one label"},
		}),
		schemas.KV("priority", map[string]interface{}{
			"type": "integer", "description": "1-4", "enum": []interface{}{1, 2, 3, 4},
		}),
	)

	compact := compactToolDefinition(tool)

	require.NotNil(t, compact.Function)
	assert.Equal(t, "svc-create_issue", compact.Function.Name)
	assert.Nil(t, compact.Function.Description)
	require.NotNil(t, compact.Function.Parameters)
	assert.Equal(t, []string{"title"}, compact.Function.Parameters.Required)

	raw, err := json.Marshal(compact.Function.Parameters)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "Issue title")
	assert.NotContains(t, string(raw), "Issue body")
	assert.NotContains(t, string(raw), "Labels to apply")
	assert.NotContains(t, string(raw), "one label")
	assert.NotContains(t, string(raw), `"1-4"`)

	var generic map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &generic))
	props := generic["properties"].(map[string]interface{})
	assert.Contains(t, props, "description", "property named description must be kept")
	assert.Equal(t, "string", props["description"].(map[string]interface{})["type"])
	assert.Equal(t, "string", props["labels"].(map[string]interface{})["items"].(map[string]interface{})["type"])
	assert.Len(t, props["priority"].(map[string]interface{})["enum"], 4)

	// The original is untouched.
	assert.NotNil(t, tool.Function.Description)
	origRaw, _ := json.Marshal(tool.Function.Parameters)
	assert.Contains(t, string(origRaw), "Issue title")
}

func TestCompactToolDefinition_NoParameters(t *testing.T) {
	tool := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
		Name: "svc-ping", Description: schemas.Ptr("Ping."),
	}}
	compact := compactToolDefinition(tool)
	assert.Nil(t, compact.Function.Description)
	assert.Nil(t, compact.Function.Parameters)
	assert.Equal(t, "svc-ping", compact.Function.Name)
}

// =============================================================================
// BM25
// =============================================================================

func TestTokenizeForSearch(t *testing.T) {
	assert.Equal(t, []string{"linear", "create", "issue"}, tokenizeForSearch("Linear-create_issue"))
	// camelCase yields the whole word plus its parts.
	assert.Equal(t, []string{"listcalendarevent", "list", "calendar", "event"}, tokenizeForSearch("listCalendarEvents"))
	assert.Equal(t, []string{"github", "git", "hub"}, tokenizeForSearch("GitHub"))
	assert.Equal(t, []string{"github"}, tokenizeForSearch("Github"))
	assert.Equal(t, []string{"sqlite"}, tokenizeForSearch("SQLite"))
	assert.Equal(t, []string{"company", "search"}, tokenizeForSearch("companies search!"))
	assert.Equal(t, []string{"gpt", "4o"}, tokenizeForSearch("gpt-4o"))
	assert.Empty(t, tokenizeForSearch("a - ! "))
}

func TestBM25_RanksNameMatchAboveDescriptionMatch(t *testing.T) {
	idx := newBM25Index([]searchIndexEntry{
		{toolName: "Cal-events_list", server: "Cal", description: "List events from a calendar within a time window."},
		{toolName: "Cal-events_create", server: "Cal", description: "Create a calendar event with attendees."},
		{toolName: "Cal-settings_get", server: "Cal", description: "Read the user's calendar settings, including the timezone used when listing events."},
		{toolName: "Git-issues_list", server: "Git", description: "List issues in a repository."},
	})

	hits := idx.search("list calendar events", "", 10)
	require.NotEmpty(t, hits)
	assert.Equal(t, "Cal-events_list", hits[0].doc.toolName)
	names := make([]string, 0, len(hits))
	for _, h := range hits {
		names = append(names, h.doc.toolName)
	}
	assert.Contains(t, names, "Git-issues_list", "shares the 'list' term so it should still score")

	// Server filter is case-insensitive and excludes other servers.
	hits = idx.search("list", "git", 10)
	require.Len(t, hits, 1)
	assert.Equal(t, "Git-issues_list", hits[0].doc.toolName)

	// Off-topic queries return nothing rather than noise.
	assert.Empty(t, idx.search("deploy kubernetes", "", 10))
	// Limit is honored.
	assert.Len(t, idx.search("calendar", "", 2), 2)
}

// =============================================================================
// GetAvailableTools MODE MATRIX
// =============================================================================

func TestGetAvailableTools_ToolModeMatrix(t *testing.T) {
	cm := newModeClientManager()
	cm.addClient("Direct", schemas.MCPToolModeDirect, nil,
		describedTool("Direct-a", "Direct tool A", nil, prop("x", "string", "the x")))
	cm.addClient("Compact", schemas.MCPToolModeCompact, nil,
		describedTool("Compact-b", "Compact tool B", nil, prop("y", "string", "the y")))
	cm.addClient("Code", schemas.MCPToolModeCode, nil,
		describedTool("Code-c", "Code tool C", nil))
	cm.addClient("Search1", schemas.MCPToolModeSearch, nil,
		describedTool("Search1-d", "Search tool D", nil),
		describedTool("Search1-e", "Search tool E", nil))
	cm.addClient("Search2", schemas.MCPToolModeSearch, nil,
		describedTool("Search2-f", "Search tool F", nil))

	tm := newToolsManagerForTest(cm)
	ctx := testCtx()
	tools := tm.GetAvailableTools(ctx)

	byName := map[string]schemas.ChatTool{}
	for _, tool := range tools {
		byName[tool.Function.Name] = tool
	}

	// Direct: full definition.
	require.Contains(t, byName, "Direct-a")
	assert.NotNil(t, byName["Direct-a"].Function.Description)
	// Compact: present but stripped.
	require.Contains(t, byName, "Compact-b")
	assert.Nil(t, byName["Compact-b"].Function.Description)
	raw, _ := json.Marshal(byName["Compact-b"].Function.Parameters)
	assert.NotContains(t, string(raw), "the y")
	// Code and search tools are hidden; no CodeMode impl is configured in the
	// test manager, so no code meta-tools either.
	assert.NotContains(t, byName, "Code-c")
	assert.NotContains(t, byName, "Search1-d")
	assert.NotContains(t, byName, "Search2-f")
	assert.NotContains(t, byName, ToolTypeListToolFiles)
	// Search meta-tools are present exactly once and describe the hidden catalog.
	require.Contains(t, byName, ToolTypeSearchTools)
	require.Contains(t, byName, ToolTypeGetToolDetails)
	require.Contains(t, byName, ToolTypeExecuteTool)
	desc := *byName[ToolTypeSearchTools].Function.Description
	assert.Contains(t, desc, "3 tools")
	assert.Contains(t, desc, "Search1, Search2")
	assert.NotContains(t, desc, "Direct")

	// Hidden tools are still recorded as MCP-added in the context.
	added, _ := ctx.Value(schemas.BifrostContextKeyMCPAddedTools).([]string)
	assert.Contains(t, added, "Search1-d")
	assert.Contains(t, added, "Code-c")

	// Exactly 2 real tools + 3 meta-tools.
	assert.Len(t, tools, 5)
}

func TestGetAvailableTools_NoSearchClients_NoMetaTools(t *testing.T) {
	cm := newModeClientManager()
	cm.addClient("Direct", schemas.MCPToolModeDirect, nil, describedTool("Direct-a", "A", nil))
	tools := newToolsManagerForTest(cm).GetAvailableTools(testCtx())
	require.Len(t, tools, 1)
	assert.Equal(t, "Direct-a", tools[0].Function.Name)
}

// =============================================================================
// SEARCH MODE HANDLERS
// =============================================================================

func searchFixture() (*ToolsManager, *modeClientManager) {
	cm := newModeClientManager()
	cm.addClient("Linear", schemas.MCPToolModeSearch, schemas.WhiteList{"*"},
		describedTool("Linear-create_issue", "Create a new issue in a Linear team. Returns the issue identifier.", []string{"title", "teamId"},
			prop("title", "string", "Issue title"),
			prop("teamId", "string", "Team to file the issue in"),
			prop("description", "string", "Markdown body"),
			schemas.KV("priority", map[string]interface{}{"type": "integer", "description": "0-4", "enum": []interface{}{0, 1, 2, 3, 4}}),
		),
		describedTool("Linear-list_issues", "List issues, optionally filtered by team or assignee.", nil,
			prop("teamId", "string", "Team filter")),
	)
	cm.addClient("Calendar", schemas.MCPToolModeSearch, schemas.WhiteList{"events_list"},
		describedTool("Calendar-events_list", "List calendar events in a time window.", []string{"timeMin", "timeMax"},
			prop("timeMin", "string", "RFC3339 start"), prop("timeMax", "string", "RFC3339 end")),
		describedTool("Calendar-events_create", "Create a calendar event.", []string{"summary"},
			prop("summary", "string", "Event title")),
	)
	cm.addClient("Direct", schemas.MCPToolModeDirect, schemas.WhiteList{"*"},
		describedTool("Direct-echo", "Echo input back.", nil))
	return newToolsManagerForTest(cm), cm
}

func TestSearchTools_RanksAndFormats(t *testing.T) {
	tm, _ := searchFixture()
	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeSearchTools, map[string]interface{}{"query": "create linear issue"}))
	require.NoError(t, err)
	assert.False(t, isErrorMessage(msg))
	text := messageText(t, msg)
	lines := strings.Split(text, "\n")
	assert.Contains(t, lines[0], "of 4 tools", "direct-mode tools must not be counted")
	assert.True(t, strings.HasPrefix(lines[1], "- Linear-create_issue: Create a new issue in a Linear team."), text)
	assert.NotContains(t, text, "Returns the issue identifier", "only the first sentence is shown")
	assert.NotContains(t, text, "Direct-echo")
	assert.Equal(t, "call_"+ToolTypeSearchTools, *msg.ChatToolMessage.ToolCallID)
}

func TestSearchTools_ServerFilterAndListing(t *testing.T) {
	tm, _ := searchFixture()

	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeSearchTools, map[string]interface{}{"query": "list", "server": "calendar"}))
	require.NoError(t, err)
	text := messageText(t, msg)
	assert.Contains(t, text, "Calendar-events_list")
	assert.NotContains(t, text, "Linear-list_issues")

	// Empty query + server = browse listing.
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeSearchTools, map[string]interface{}{"query": "", "server": "Linear"}))
	require.NoError(t, err)
	text = messageText(t, msg)
	assert.Contains(t, text, "2 tools on Linear")
	assert.Contains(t, text, "Linear-create_issue")
	assert.Contains(t, text, "Linear-list_issues")

	// Unknown server is an actionable error listing the real ones.
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeSearchTools, map[string]interface{}{"query": "x", "server": "Nope"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
	assert.Contains(t, messageText(t, msg), "Calendar, Linear")

	// Missing query without a server is an error.
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeSearchTools, map[string]interface{}{}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
}

func TestSearchTools_NoMatchIsNotAnError(t *testing.T) {
	tm, _ := searchFixture()
	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeSearchTools, map[string]interface{}{"query": "kubernetes deploy"}))
	require.NoError(t, err)
	assert.False(t, isErrorMessage(msg))
	assert.Contains(t, messageText(t, msg), "No tools matched")
}

func TestGetToolDetails_RendersParamsAndExample(t *testing.T) {
	tm, _ := searchFixture()
	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeGetToolDetails, map[string]interface{}{"tool": "Linear-create_issue"}))
	require.NoError(t, err)
	assert.False(t, isErrorMessage(msg))
	text := messageText(t, msg)
	assert.Contains(t, text, "Tool: Linear-create_issue (server: Linear)")
	assert.Contains(t, text, "Returns the issue identifier")
	assert.Contains(t, text, "- title (string, required): Issue title")
	assert.Contains(t, text, "- teamId (string, required)")
	assert.Contains(t, text, "- description (string, optional): Markdown body")
	assert.Contains(t, text, "- priority (integer, optional): 0-4 [one of: 0, 1, 2, 3, 4]")
	// Required params come first.
	assert.Less(t, strings.Index(text, "- teamId"), strings.Index(text, "- description"))
	// Example only fills required params.
	assert.Contains(t, text, `Example: executeTool({"arguments":{"teamId":"<teamId>","title":"<title>"},"tool":"Linear-create_issue"})`)
}

func TestGetToolDetails_UnknownToolSuggests(t *testing.T) {
	tm, _ := searchFixture()
	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeGetToolDetails, map[string]interface{}{"tool": "Linear-create_issues"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
	assert.Contains(t, messageText(t, msg), "Did you mean: Linear-create_issue")

	// Direct-mode tools are not part of the search catalog.
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeGetToolDetails, map[string]interface{}{"tool": "Direct-echo"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
}

func TestExecuteTool_RunsInnerToolThroughPipeline(t *testing.T) {
	tm, cm := searchFixture()
	cm.canned["Linear-create_issue"] = &schemas.BifrostMCPResponse{
		ChatMessage: createToolResponseMessage(schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("inner")}, "created LIN-42", false),
	}
	outer := toolCallFor(ToolTypeExecuteTool, map[string]interface{}{
		"tool":      "Linear-create_issue",
		"arguments": map[string]interface{}{"title": "Bug", "teamId": "ENG"},
	})
	msg, err := tm.executeSearchModeTool(testCtx(), outer)
	require.NoError(t, err)
	assert.False(t, isErrorMessage(msg))
	assert.Equal(t, "created LIN-42", messageText(t, msg))
	// Re-enveloped under the outer call id, not the nested one.
	assert.Equal(t, "call_"+ToolTypeExecuteTool, *msg.ChatToolMessage.ToolCallID)

	require.NotNil(t, cm.lastRequest)
	assert.Equal(t, "Linear", cm.lastRequest.ClientName)
	assert.Equal(t, "Linear-create_issue", cm.lastRequest.GetToolName())
	assert.JSONEq(t, `{"teamId":"ENG","title":"Bug"}`, cm.lastRequest.ChatAssistantMessageToolCall.Function.Arguments)
}

func TestExecuteTool_AcceptsStringifiedArguments(t *testing.T) {
	tm, cm := searchFixture()
	cm.canned["Linear-list_issues"] = &schemas.BifrostMCPResponse{
		ChatMessage: createToolResponseMessage(schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("inner")}, "[]", false),
	}
	outer := toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Linear-list_issues", "arguments": `{"teamId":"ENG"}`})
	msg, err := tm.executeSearchModeTool(testCtx(), outer)
	require.NoError(t, err)
	assert.False(t, isErrorMessage(msg))
	assert.JSONEq(t, `{"teamId":"ENG"}`, cm.lastRequest.ChatAssistantMessageToolCall.Function.Arguments)
}

func TestExecuteTool_PropagatesInnerToolError(t *testing.T) {
	tm, cm := searchFixture()
	cm.canned["Linear-list_issues"] = &schemas.BifrostMCPResponse{
		ChatMessage: createToolResponseMessage(schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("inner")}, "team not found", true),
	}
	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Linear-list_issues"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
	assert.Equal(t, "team not found", messageText(t, msg))
}

func TestExecuteTool_Refusals(t *testing.T) {
	tm, cm := searchFixture()

	// Unknown tool.
	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Nope-x"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
	assert.Contains(t, messageText(t, msg), "Unknown tool")
	assert.Nil(t, cm.lastRequest)

	// A direct-mode tool cannot be reached through executeTool.
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Direct-echo"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
	assert.Contains(t, messageText(t, msg), "not exposed through executeTool")
	assert.Nil(t, cm.lastRequest)

	// Request-context client filter is enforced.
	ctx := testCtx()
	ctx.SetValue(schemas.MCPContextKeyIncludeClients, []string{"Calendar"})
	msg, err = tm.executeSearchModeTool(ctx, toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Linear-list_issues"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
	assert.Contains(t, messageText(t, msg), "not permitted")
	assert.Nil(t, cm.lastRequest)

	// Missing tool argument.
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeExecuteTool, map[string]interface{}{}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))

	// Non-object arguments.
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Linear-list_issues", "arguments": 42}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
}

// =============================================================================
// AGENT AUTO-EXECUTION GATING
// =============================================================================

func TestResolveExecuteToolTarget(t *testing.T) {
	_, cm := searchFixture()

	client, inner, ok := resolveExecuteToolTarget(toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Calendar-events_list"}), cm)
	require.True(t, ok)
	assert.Equal(t, "Calendar", client.ExecutionConfig.Name)
	assert.Equal(t, "Calendar-events_list", inner)
	// Calendar only auto-executes events_list, so events_create must be gated.
	assert.True(t, canAutoExecuteTool("Calendar-events_list", client.ExecutionConfig))
	assert.False(t, canAutoExecuteTool("Calendar-events_create", client.ExecutionConfig))

	_, _, ok = resolveExecuteToolTarget(toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Nope-x"}), cm)
	assert.False(t, ok)
	_, _, ok = resolveExecuteToolTarget(toolCallFor(ToolTypeExecuteTool, map[string]interface{}{}), cm)
	assert.False(t, ok)
	_, _, ok = resolveExecuteToolTarget(schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: "not json"}}, cm)
	assert.False(t, ok)
}

func TestIsSearchModeTool(t *testing.T) {
	assert.True(t, IsSearchModeTool(ToolTypeSearchTools))
	assert.True(t, IsSearchModeTool(ToolTypeGetToolDetails))
	assert.True(t, IsSearchModeTool(ToolTypeExecuteTool))
	assert.False(t, IsSearchModeTool(ToolTypeExecuteToolCode))
	assert.False(t, IsSearchModeTool("Linear-create_issue"))
}

func TestBM25_NameHitBeatsLongDescription(t *testing.T) {
	long := strings.Repeat("prospect campaign sequence contact account enrichment ", 40) + "users teammates list."
	idx := newBM25Index([]searchIndexEntry{
		{toolName: "Apollo-apollo_users_search", server: "Apollo", description: "List or search the users (teammates) in your team's Apollo account. " + long},
		{toolName: "Linear-list_users", server: "Linear", description: "Retrieve users in the Linear workspace"},
		{toolName: "Notion-get-users", server: "Notion", description: "Retrieves a list of users in the current workspace."},
		{toolName: "Apollo-apollo_contacts_search", server: "Apollo", description: "Search for contacts. " + long},
	})
	hits := idx.search("list apollo workspace users", "", 5)
	require.NotEmpty(t, hits)
	assert.Equal(t, "Apollo-apollo_users_search", hits[0].doc.toolName, "an exact name+server hit must not be buried by a long description")
}

func TestNameOnlyToolDefinition(t *testing.T) {
	tool := describedTool("svc-create_issue", "Create an issue.", []string{"title"}, prop("title", "string", "Issue title"))
	nameOnly := nameOnlyToolDefinition(tool)
	require.NotNil(t, nameOnly.Function)
	assert.Equal(t, "svc-create_issue", nameOnly.Function.Name)
	assert.Nil(t, nameOnly.Function.Description)
	raw, err := json.Marshal(nameOnly.Function.Parameters)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object","properties":{},"additionalProperties":true}`, string(raw))
	// original untouched
	assert.NotNil(t, tool.Function.Description)
	assert.Equal(t, []string{"title"}, tool.Function.Parameters.Required)
}

func TestGetAvailableTools_CompactNamesAddsDetailsToolOnly(t *testing.T) {
	cm := newModeClientManager()
	cm.addClient("Names", schemas.MCPToolModeCompactNames, nil,
		describedTool("Names-a", "Tool A", []string{"x"}, prop("x", "string", "the x")))
	tm := newToolsManagerForTest(cm)
	tools := tm.GetAvailableTools(testCtx())
	byName := map[string]schemas.ChatTool{}
	for _, tool := range tools {
		byName[tool.Function.Name] = tool
	}
	require.Contains(t, byName, "Names-a")
	assert.Nil(t, byName["Names-a"].Function.Description)
	assert.Equal(t, 0, byName["Names-a"].Function.Parameters.Properties.Len())
	require.Contains(t, byName, ToolTypeGetToolDetails)
	assert.NotContains(t, byName, ToolTypeSearchTools)
	assert.NotContains(t, byName, ToolTypeExecuteTool)
	assert.Len(t, tools, 2)

	// getToolDetails resolves compact_names tools with their full schema.
	msg, err := tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeGetToolDetails, map[string]interface{}{"tool": "Names-a"}))
	require.NoError(t, err)
	assert.False(t, isErrorMessage(msg))
	assert.Contains(t, messageText(t, msg), "- x (string, required): the x")

	// but searchTools does not list them, and executeTool refuses them.
	cm.addClient("Srch", schemas.MCPToolModeSearch, nil, describedTool("Srch-b", "Tool B for searching", nil))
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeSearchTools, map[string]interface{}{"query": "tool"}))
	require.NoError(t, err)
	text := messageText(t, msg)
	assert.Contains(t, text, "Srch-b")
	assert.NotContains(t, text, "Names-a")
	msg, err = tm.executeSearchModeTool(testCtx(), toolCallFor(ToolTypeExecuteTool, map[string]interface{}{"tool": "Names-a"}))
	require.NoError(t, err)
	assert.True(t, isErrorMessage(msg))
}
