package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// HELPERS
// =============================================================================

// mockToolClientManager is a ClientManager that returns a pre-defined set of MCP tools.
// It is used to drive ParseAndAddToolsToRequest without a real MCP server.
type mockToolClientManager struct {
	tools []schemas.ChatTool
}

func (m *mockToolClientManager) GetClientByName(clientName string) *schemas.MCPClientState {
	if clientName == "test-client" {
		return &schemas.MCPClientState{
			Name: "test-client",
			ExecutionConfig: &schemas.MCPClientConfig{
				ID:              "test-client",
				Name:            "test-client",
				IsCodeModeClient: false,
				ToolsToExecute:  []string{"*"},
			},
		}
	}
	return nil
}

func (m *mockToolClientManager) GetClientForTool(toolName string) *schemas.MCPClientState {
	return nil
}

func (m *mockToolClientManager) GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool {
	return map[string][]schemas.ChatTool{
		"test-client": m.tools,
	}
}

// makeTool is a convenience constructor for test tool fixtures.
func makeTool(name string) schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: name,
		},
	}
}

// toolNamesFromChatRequest collects the names of every tool in the request's
// ChatRequest.Params.Tools slice.
func toolNamesFromChatRequest(req *schemas.BifrostRequest) []string {
	if req.ChatRequest == nil || req.ChatRequest.Params == nil {
		return nil
	}
	names := make([]string, 0, len(req.ChatRequest.Params.Tools))
	for _, t := range req.ChatRequest.Params.Tools {
		if t.Function != nil {
			names = append(names, t.Function.Name)
		}
	}
	return names
}

// toolNamesFromResponsesRequest collects the names of every tool in the request's
// ResponsesRequest.Params.Tools slice (ResponsesParameters uses *string Name).
func toolNamesFromResponsesRequest(req *schemas.BifrostRequest) []string {
	if req.ResponsesRequest == nil || req.ResponsesRequest.Params == nil {
		return nil
	}
	names := make([]string, 0, len(req.ResponsesRequest.Params.Tools))
	for _, t := range req.ResponsesRequest.Params.Tools {
		if t.Name != nil {
			names = append(names, *t.Name)
		}
	}
	return names
}

// countOccurrences returns how many times name appears in slice.
func countOccurrences(slice []string, name string) int {
	n := 0
	for _, s := range slice {
		if s == name {
			n++
		}
	}
	return n
}

// newToolsManagerForTest creates a minimal ToolsManager backed by the provided
// ClientManager, with no code mode, no plugin pipeline, and a no-op logger.
func newToolsManagerForTest(cm ClientManager) *ToolsManager {
	return NewToolsManager(
		&schemas.MCPToolManagerConfig{
			MaxAgentDepth: 5,
		},
		cm,
		nil, // fetchNewRequestIDFunc
		nil, // pluginPipelineProvider
		nil, // releasePluginPipeline
		nil, // oauth2Provider
		&MockLogger{},
	)
}

// contextWithUserAgent creates a BifrostContext with BifrostContextKeyUserAgent set.
func contextWithUserAgent(ua string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUserAgent, ua)
	return ctx
}

// =============================================================================
// buildIntegrationDuplicateCheckMap – unit tests
// =============================================================================

func TestBuildIntegrationDuplicateCheckMap_EmptyInputs(t *testing.T) {
	t.Parallel()

	m := buildIntegrationDuplicateCheckMap(nil, "", defaultLogger)
	if len(m) != 0 {
		t.Errorf("expected empty map for nil tools and no agent, got %d entries", len(m))
	}
}

func TestBuildIntegrationDuplicateCheckMap_NoUserAgent_DirectMatch(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("echo"),
		makeTool("calculator"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, "", defaultLogger)

	for _, want := range []string{"echo", "calculator"} {
		if !m[want] {
			t.Errorf("expected %q to be in map", want)
		}
	}
	if len(m) != 2 {
		t.Errorf("expected exactly 2 entries, got %d", len(m))
	}
}

func TestBuildIntegrationDuplicateCheckMap_NilFunction_IsSkipped(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		{Type: schemas.ChatToolTypeFunction, Function: nil}, // nil Function
		makeTool(""),            // empty name
		makeTool("valid_tool"),  // valid
	}

	m := buildIntegrationDuplicateCheckMap(tools, "", defaultLogger)

	if !m["valid_tool"] {
		t.Error("expected valid_tool to be in map")
	}
	// "" is technically inserted because the loop guard is `Name != ""` — nothing else
	if m[""] {
		t.Error("empty tool name should not be inserted")
	}
	// nil Function is skipped entirely; no panic
}

func TestBuildIntegrationDuplicateCheckMap_UnknownAgent_FallsBackToDirectMatch(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("search"),
		makeTool("weather"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, "unknown-agent-xyz", defaultLogger)

	for _, want := range []string{"search", "weather"} {
		if !m[want] {
			t.Errorf("expected %q in map for unknown agent", want)
		}
	}
	if len(m) != 2 {
		t.Errorf("expected 2 entries for unknown agent, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// ClaudeCLI — pattern: mcp__{server}__{tool_name}
// ---------------------------------------------------------------------------

func TestBuildIntegrationDuplicateCheckMap_ClaudeCLI_StripsPrefix(t *testing.T) {
	t.Parallel()

	// These are the tool names Claude CLI sends when it has already connected to a
	// Bifrost MCP server. The prefix is mcp__{server}__{tool_name}.
	tools := []schemas.ChatTool{
		makeTool("mcp__bifrost__echo"),
		makeTool("mcp__bifrost__calculator"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.ClaudeCLI.String(), defaultLogger)

	// Bare tool names must be recognised so Bifrost does not inject them again.
	for _, want := range []string{"echo", "calculator"} {
		if !m[want] {
			t.Errorf("ClaudeCLI: expected bare name %q to be in duplicate map", want)
		}
	}
	// Original prefixed names must also be present.
	for _, want := range []string{"mcp__bifrost__echo", "mcp__bifrost__calculator"} {
		if !m[want] {
			t.Errorf("ClaudeCLI: expected prefixed name %q to be in duplicate map", want)
		}
	}
}

func TestBuildIntegrationDuplicateCheckMap_ClaudeCLI_MultipleServers(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("mcp__bifrost__executeToolCode"),
		makeTool("mcp__bifrost__listToolFiles"),
		makeTool("mcp__calculator__calculator_add"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.ClaudeCLI.String(), defaultLogger)

	// Last segment is always the bare tool name.
	for _, want := range []string{"executeToolCode", "listToolFiles", "calculator_add"} {
		if !m[want] {
			t.Errorf("ClaudeCLI multi-server: expected bare name %q in map", want)
		}
	}
}

func TestBuildIntegrationDuplicateCheckMap_ClaudeCLI_TwoPartName_NotExtracted(t *testing.T) {
	t.Parallel()

	// A two-segment name like "mcp__only_two" doesn't satisfy the len(parts) >= 3 guard,
	// so only the raw name ends up in the map — the "only_two" portion is NOT extracted.
	tools := []schemas.ChatTool{
		makeTool("mcp__only_two"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.ClaudeCLI.String(), defaultLogger)

	if !m["mcp__only_two"] {
		t.Error("ClaudeCLI: original two-part name should still appear as direct match")
	}
	if m["only_two"] {
		t.Error("ClaudeCLI: bare segment from two-part name should NOT be extracted")
	}
}

func TestBuildIntegrationDuplicateCheckMap_ClaudeCLI_ToolWithUnderscores(t *testing.T) {
	t.Parallel()

	// Tool names that themselves contain underscores must not be split — only the
	// double-underscore __ delimiter is meaningful.
	tools := []schemas.ChatTool{
		makeTool("mcp__my_server__get_weather_data"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.ClaudeCLI.String(), defaultLogger)

	// The entire last segment, including its internal underscores, is the tool name.
	if !m["get_weather_data"] {
		t.Error("ClaudeCLI: tool name with underscores should be extracted as-is")
	}
	if !m["mcp__my_server__get_weather_data"] {
		t.Error("ClaudeCLI: original prefixed name should be retained")
	}
	// Partial segments must not appear.
	for _, unexpected := range []string{"get", "weather", "data"} {
		if m[unexpected] {
			t.Errorf("ClaudeCLI: unexpected partial segment %q in map", unexpected)
		}
	}
}

func TestBuildIntegrationDuplicateCheckMap_ClaudeCLI_MixedPrefixedAndBare(t *testing.T) {
	t.Parallel()

	// A request might contain some already-prefixed tools from one MCP server and some
	// bare tools injected directly (e.g., by a plugin). Both should be in the map.
	tools := []schemas.ChatTool{
		makeTool("mcp__bifrost__echo"),
		makeTool("search"), // already bare
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.ClaudeCLI.String(), defaultLogger)

	for _, want := range []string{"echo", "mcp__bifrost__echo", "search"} {
		if !m[want] {
			t.Errorf("ClaudeCLI mixed: expected %q in map", want)
		}
	}
}

// ---------------------------------------------------------------------------
// GeminiCLI — pattern: mcp_{server}_{tool_name} (single underscore)
// The current implementation treats GeminiCLI tools as direct matches only.
// These tests document the current (direct-match) behaviour and also define the
// expected behaviour once prefix-stripping for the mcp_{server}_{tool} pattern
// is added in a follow-up.
// ---------------------------------------------------------------------------

func TestBuildIntegrationDuplicateCheckMap_GeminiCLI_DirectMatch(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("echo"),
		makeTool("calculator"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.GeminiCLI.String(), defaultLogger)

	for _, want := range []string{"echo", "calculator"} {
		if !m[want] {
			t.Errorf("GeminiCLI direct match: expected %q in map", want)
		}
	}
}

// TestBuildIntegrationDuplicateCheckMap_GeminiCLI_PrefixedTools_StripsPrefix verifies that
// the GeminiCLI case correctly extracts the tool name from the mcp_{server}_{tool} pattern
// by stripping "mcp_" and skipping past the first "_" (server name boundary).
func TestBuildIntegrationDuplicateCheckMap_GeminiCLI_PrefixedTools_StripsPrefix(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("mcp_bifrost_echo"),
		makeTool("mcp_bifrost_calculator"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.GeminiCLI.String(), defaultLogger)

	for _, want := range []string{"echo", "calculator"} {
		if !m[want] {
			t.Errorf("GeminiCLI: expected bare name %q after prefix stripping", want)
		}
	}
}

// TestBuildIntegrationDuplicateCheckMap_GeminiCLI_ClientPrefixedTools verifies the
// dual-gateway scenario: Bifrost stores tools as "{client}-{tool}" and Gemini CLI
// wraps them as "mcp_{server}_{client}-{tool}". The dedup must extract "{client}-{tool}".
func TestBuildIntegrationDuplicateCheckMap_GeminiCLI_ClientPrefixedTools(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("mcp_bifrost_testing_exa-web_fetch_exa"),
		makeTool("mcp_bifrost_testing_exa-web_search_exa"),
		makeTool("mcp_bifrost_testing_websets-cancel_enrichment"),
		makeTool("mcp_bifrost_ctx7-resolve-library-id"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.GeminiCLI.String(), defaultLogger)

	for _, want := range []string{
		"testing_exa-web_fetch_exa",
		"testing_exa-web_search_exa",
		"testing_websets-cancel_enrichment",
		"ctx7-resolve-library-id",
	} {
		if !m[want] {
			t.Errorf("GeminiCLI client-prefixed: expected %q in duplicate map", want)
		}
	}
}

// ---------------------------------------------------------------------------
// New integrations — Cursor, Codex CLI, n8n, Qwen CLI
//
// These agents are expected to use tool names without provider-specific prefixes
// (i.e. direct matching). Once the corresponding constants are added to
// core/schemas/useragents.go and the switch cases are wired in, these tests
// verify that the deduplication map is correctly populated.
//
// String literals are used instead of schema constants because the constants do
// not exist yet. Replace with schemas.CursorEditor.String() etc. once the
// constants land.
// ---------------------------------------------------------------------------

func TestBuildIntegrationDuplicateCheckMap_CursorEditor_DirectMatch(t *testing.T) {
	t.Parallel()

	const cursorUA = "cursor"

	tools := []schemas.ChatTool{
		makeTool("echo"),
		makeTool("read_file"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, cursorUA, defaultLogger)

	for _, want := range []string{"echo", "read_file"} {
		if !m[want] {
			t.Errorf("Cursor: expected %q in duplicate map", want)
		}
	}
	if len(m) != 2 {
		t.Errorf("Cursor: expected 2 entries, got %d", len(m))
	}
}

func TestBuildIntegrationDuplicateCheckMap_CodexCLI_DirectMatch(t *testing.T) {
	t.Parallel()

	const codexUA = "codex"

	tools := []schemas.ChatTool{
		makeTool("bash"),
		makeTool("list_files"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, codexUA, defaultLogger)

	for _, want := range []string{"bash", "list_files"} {
		if !m[want] {
			t.Errorf("Codex: expected %q in duplicate map", want)
		}
	}
}

func TestBuildIntegrationDuplicateCheckMap_N8N_DirectMatch(t *testing.T) {
	t.Parallel()

	const n8nUA = "n8n"

	tools := []schemas.ChatTool{
		makeTool("http_request"),
		makeTool("send_email"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, n8nUA, defaultLogger)

	for _, want := range []string{"http_request", "send_email"} {
		if !m[want] {
			t.Errorf("n8n: expected %q in duplicate map", want)
		}
	}
}

func TestBuildIntegrationDuplicateCheckMap_QwenCLI_DirectMatch(t *testing.T) {
	t.Parallel()

	const qwenUA = "qwen-code"

	tools := []schemas.ChatTool{
		makeTool("code_interpreter"),
		makeTool("web_search"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, qwenUA, defaultLogger)

	for _, want := range []string{"code_interpreter", "web_search"} {
		if !m[want] {
			t.Errorf("Qwen: expected %q in duplicate map", want)
		}
	}
}

// TestBuildIntegrationDuplicateCheckMap_CodexCLI_ClientPrefixedTools verifies the
// dual-gateway scenario for Codex CLI: format is mcp__{server}__{tool_name} but ALL
// hyphens in the original Bifrost tool name are converted to underscores. The dedup
// map stores the all-underscore form; callers must normalize before lookup.
func TestBuildIntegrationDuplicateCheckMap_CodexCLI_ClientPrefixedTools(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("mcp__bifrost__testing_exa_web_fetch_exa"),
		makeTool("mcp__bifrost__testing_exa_web_search_exa"),
		makeTool("mcp__bifrost__testing_websets_cancel_enrichment"),
		makeTool("mcp__bifrost__ctx7_query_docs"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.CodexCLI.String(), defaultLogger)

	// All-underscore forms must be in the map (callers normalize Bifrost names before lookup).
	for _, want := range []string{
		"testing_exa_web_fetch_exa",
		"testing_exa_web_search_exa",
		"testing_websets_cancel_enrichment",
		"ctx7_query_docs",
	} {
		if !m[want] {
			t.Errorf("CodexCLI: expected %q in duplicate map", want)
		}
	}
}

// TestBuildIntegrationDuplicateCheckMap_QwenCLI_ClientPrefixedTools verifies the
// dual-gateway scenario for Qwen CLI: format is mcp__{server}__{tool_name} (double
// underscores). Strip "mcp__" then skip past first "__" to get the Bifrost tool name.
func TestBuildIntegrationDuplicateCheckMap_QwenCLI_ClientPrefixedTools(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("mcp__bifrost__testing_exa-web_fetch_exa"),
		makeTool("mcp__bifrost__testing_exa-web_search_exa"),
		makeTool("mcp__bifrost__testing_websets-cancel_enrichment"),
		makeTool("mcp__bifrost__ctx7-resolve-library-id"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.QwenCodeCLI.String(), defaultLogger)

	for _, want := range []string{
		"testing_exa-web_fetch_exa",
		"testing_exa-web_search_exa",
		"testing_websets-cancel_enrichment",
		"ctx7-resolve-library-id",
	} {
		if !m[want] {
			t.Errorf("QwenCLI client-prefixed: expected %q in duplicate map", want)
		}
	}
}

// =============================================================================
// ParseAndAddToolsToRequest – end-to-end deduplication tests
//
// These tests wire a ToolsManager backed by a mockToolClientManager and verify
// that ParseAndAddToolsToRequest does not inject duplicate tools when the
// request already carries tools that match MCP-registered ones.
// =============================================================================

// buildChatRequest builds a minimal BifrostChatRequest with the given pre-existing
// tool names already populated in Params.Tools.
func buildChatRequest(existingToolNames ...string) *schemas.BifrostRequest {
	tools := make([]schemas.ChatTool, 0, len(existingToolNames))
	for _, name := range existingToolNames {
		tools = append(tools, makeTool(name))
	}
	return &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
			Params: &schemas.ChatParameters{
				Tools: tools,
			},
		},
		RequestType: schemas.ChatCompletionRequest,
	}
}

func TestParseAndAddToolsToRequest_NoUserAgent_NoDuplicate(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("echo"),
			makeTool("calculator"),
		},
	}
	tm := newToolsManagerForTest(cm)

	// Request already has "echo" — Bifrost should not add it again.
	req := buildChatRequest("echo")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if countOccurrences(names, "echo") != 1 {
		t.Errorf("expected exactly 1 'echo', got %d (names: %v)", countOccurrences(names, "echo"), names)
	}
	// "calculator" is not in the existing tools so it should be added exactly once.
	if countOccurrences(names, "calculator") != 1 {
		t.Errorf("expected exactly 1 'calculator', got %d", countOccurrences(names, "calculator"))
	}
}

func TestParseAndAddToolsToRequest_NoUserAgent_HyphenUnderscoreDistinct(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("foo-bar"),
			makeTool("foo_bar"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildChatRequest()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if countOccurrences(names, "foo-bar") != 1 || countOccurrences(names, "foo_bar") != 1 {
		t.Errorf("without Codex UA, foo-bar and foo_bar are distinct tools; got names %v", names)
	}
}

func TestParseAndAddToolsToRequest_ClaudeCLI_NoDuplicate(t *testing.T) {
	t.Parallel()

	// Available MCP tools registered on the Bifrost server.
	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("echo"),
			makeTool("calculator"),
		},
	}
	tm := newToolsManagerForTest(cm)

	// Claude CLI already carries the tools under the mcp__{server}__{name} pattern.
	req := buildChatRequest("mcp__bifrost__echo", "mcp__bifrost__calculator")
	ctx := contextWithUserAgent(schemas.ClaudeCLI.String())

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)

	// The bare names must NOT be injected as new entries — they were already present
	// under the prefixed names.
	if countOccurrences(names, "echo") != 0 {
		t.Errorf("ClaudeCLI: bare 'echo' should not be added again, got %d occurrences (names: %v)",
			countOccurrences(names, "echo"), names)
	}
	if countOccurrences(names, "calculator") != 0 {
		t.Errorf("ClaudeCLI: bare 'calculator' should not be added again, got %d occurrences",
			countOccurrences(names, "calculator"))
	}
	// Prefixed originals must still be present exactly once.
	for _, want := range []string{"mcp__bifrost__echo", "mcp__bifrost__calculator"} {
		if countOccurrences(names, want) != 1 {
			t.Errorf("ClaudeCLI: expected exactly 1 %q, got %d (names: %v)", want,
				countOccurrences(names, want), names)
		}
	}
}

func TestParseAndAddToolsToRequest_ClaudeCLI_NewToolsInjected(t *testing.T) {
	t.Parallel()

	// MCP server exposes echo and search. Claude CLI has only echo (prefixed).
	// Bifrost should inject search (not already present in any form).
	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("echo"),
			makeTool("search"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildChatRequest("mcp__bifrost__echo")
	ctx := contextWithUserAgent(schemas.ClaudeCLI.String())

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)

	// echo is covered by the prefixed entry — must not be injected again.
	if countOccurrences(names, "echo") != 0 {
		t.Errorf("ClaudeCLI new tool: bare 'echo' injected unexpectedly (names: %v)", names)
	}
	// search is new — must be injected exactly once.
	if countOccurrences(names, "search") != 1 {
		t.Errorf("ClaudeCLI new tool: 'search' should be injected once, got %d (names: %v)",
			countOccurrences(names, "search"), names)
	}
}

func TestParseAndAddToolsToRequest_GeminiCLI_NoDuplicate(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("echo"),
		},
	}
	tm := newToolsManagerForTest(cm)

	// Gemini CLI sends tools with their bare names (no prefix in current implementation).
	req := buildChatRequest("echo")
	ctx := contextWithUserAgent(schemas.GeminiCLI.String())

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if countOccurrences(names, "echo") != 1 {
		t.Errorf("GeminiCLI: expected exactly 1 'echo', got %d (names: %v)",
			countOccurrences(names, "echo"), names)
	}
}

func TestParseAndAddToolsToRequest_CursorEditor_NoDuplicate(t *testing.T) {
	t.Parallel()

	const cursorUA = "cursor"

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("echo"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildChatRequest("echo")
	ctx := contextWithUserAgent(cursorUA)

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if countOccurrences(names, "echo") != 1 {
		t.Errorf("Cursor: expected exactly 1 'echo', got %d (names: %v)",
			countOccurrences(names, "echo"), names)
	}
}

func TestParseAndAddToolsToRequest_CodexCLI_NoDuplicate(t *testing.T) {
	t.Parallel()

	const codexUA = "codex"

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("bash"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildChatRequest("bash")
	ctx := contextWithUserAgent(codexUA)

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if countOccurrences(names, "bash") != 1 {
		t.Errorf("Codex: expected exactly 1 'bash', got %d (names: %v)",
			countOccurrences(names, "bash"), names)
	}
}

func TestParseAndAddToolsToRequest_CodexCLI_MCPHyphenUnderscoreVariantsDeduped(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("foo-bar"),
			makeTool("foo_bar"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildChatRequest()
	ctx := contextWithUserAgent(schemas.CodexCLI.String())

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if len(names) != 1 {
		t.Fatalf("Codex: hyphen/underscore variants should yield one injected tool, got %d: %v", len(names), names)
	}
	if countOccurrences(names, "foo-bar")+countOccurrences(names, "foo_bar") != 1 {
		t.Errorf("Codex: expected exactly one of foo-bar or foo_bar, got %v", names)
	}
}

func TestParseAndAddToolsToRequest_N8N_NoDuplicate(t *testing.T) {
	t.Parallel()

	const n8nUA = "n8n"

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("http_request"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildChatRequest("http_request")
	ctx := contextWithUserAgent(n8nUA)

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if countOccurrences(names, "http_request") != 1 {
		t.Errorf("n8n: expected exactly 1 'http_request', got %d (names: %v)",
			countOccurrences(names, "http_request"), names)
	}
}

func TestParseAndAddToolsToRequest_QwenCLI_NoDuplicate(t *testing.T) {
	t.Parallel()

	const qwenUA = "qwen-code"

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("code_interpreter"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildChatRequest("code_interpreter")
	ctx := contextWithUserAgent(qwenUA)

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)
	if countOccurrences(names, "code_interpreter") != 1 {
		t.Errorf("Qwen: expected exactly 1 'code_interpreter', got %d (names: %v)",
			countOccurrences(names, "code_interpreter"), names)
	}
}

// =============================================================================
// Responses API deduplication – mirrors the ChatRequest tests above but for
// the /responses endpoint path in ParseAndAddToolsToRequest.
// =============================================================================

// buildResponsesRequest creates a minimal BifrostResponsesRequest with the given
// pre-existing tool names already populated in Params.Tools.
func buildResponsesRequest(existingToolNames ...string) *schemas.BifrostRequest {
	tools := make([]schemas.ResponsesTool, 0, len(existingToolNames))
	for _, name := range existingToolNames {
		n := name // capture
		tools = append(tools, schemas.ResponsesTool{Name: &n})
	}
	return &schemas.BifrostRequest{
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
			Params: &schemas.ResponsesParameters{
				Tools: tools,
			},
		},
		RequestType: schemas.ResponsesRequest,
	}
}

func TestParseAndAddToolsToRequest_ResponsesAPI_ClaudeCLI_NoDuplicate(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("echo"),
		},
	}
	tm := newToolsManagerForTest(cm)

	// Responses API: tool is carried under the Claude CLI prefix.
	req := buildResponsesRequest("mcp__bifrost__echo")
	ctx := contextWithUserAgent(schemas.ClaudeCLI.String())

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromResponsesRequest(result)
	if countOccurrences(names, "echo") != 0 {
		t.Errorf("Responses/ClaudeCLI: bare 'echo' should not be injected, got %d (names: %v)",
			countOccurrences(names, "echo"), names)
	}
	if countOccurrences(names, "mcp__bifrost__echo") != 1 {
		t.Errorf("Responses/ClaudeCLI: prefixed name should remain exactly once, got %d",
			countOccurrences(names, "mcp__bifrost__echo"))
	}
}

func TestParseAndAddToolsToRequest_ResponsesAPI_NoUserAgent_NoDuplicate(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("echo"),
			makeTool("search"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildResponsesRequest("echo")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromResponsesRequest(result)
	if countOccurrences(names, "echo") != 1 {
		t.Errorf("Responses/no-agent: expected exactly 1 'echo', got %d (names: %v)",
			countOccurrences(names, "echo"), names)
	}
	// search is new — must be injected.
	if countOccurrences(names, "search") != 1 {
		t.Errorf("Responses/no-agent: 'search' should be injected once, got %d", countOccurrences(names, "search"))
	}
}

func TestParseAndAddToolsToRequest_ResponsesAPI_CodexCLI_MCPHyphenUnderscoreVariantsDeduped(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("foo-bar"),
			makeTool("foo_bar"),
		},
	}
	tm := newToolsManagerForTest(cm)

	req := buildResponsesRequest()
	ctx := contextWithUserAgent(schemas.CodexCLI.String())

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromResponsesRequest(result)
	if len(names) != 1 {
		t.Fatalf("Responses/Codex: hyphen/underscore variants should yield one injected tool, got %d: %v", len(names), names)
	}
	if countOccurrences(names, "foo-bar")+countOccurrences(names, "foo_bar") != 1 {
		t.Errorf("Responses/Codex: expected exactly one of foo-bar or foo_bar, got %v", names)
	}
}

// ---------------------------------------------------------------------------
// OpenCode — pattern: {server_name}_{tool_name} (no mcp_ prefix, hyphens preserved)
// ---------------------------------------------------------------------------

// TestBuildIntegrationDuplicateCheckMap_OpenCode_ClientPrefixedTools verifies the
// dual-gateway scenario for OpenCode: format is {server}_{tool_name} (no mcp_ prefix,
// single underscore separator, hyphens in the Bifrost tool name are preserved).
// Strip up to and including the first "_" to recover the Bifrost tool name.
func TestBuildIntegrationDuplicateCheckMap_OpenCode_ClientPrefixedTools(t *testing.T) {
	t.Parallel()

	tools := []schemas.ChatTool{
		makeTool("bifrost_testing_exa-web_fetch_exa"),
		makeTool("bifrost_testing_exa-web_search_exa"),
		makeTool("bifrost_testing_websets-cancel_enrichment"),
		makeTool("bifrost_ctx7-query-docs"),
		makeTool("bifrost_filesystem-create_directory"),
	}

	m := buildIntegrationDuplicateCheckMap(tools, schemas.OpenCode.String(), defaultLogger)

	for _, want := range []string{
		"testing_exa-web_fetch_exa",
		"testing_exa-web_search_exa",
		"testing_websets-cancel_enrichment",
		"ctx7-query-docs",
		"filesystem-create_directory",
	} {
		if !m[want] {
			t.Errorf("OpenCode: expected %q in duplicate map", want)
		}
	}
	// Original prefixed names must also be retained.
	for _, want := range []string{
		"bifrost_testing_exa-web_fetch_exa",
		"bifrost_ctx7-query-docs",
	} {
		if !m[want] {
			t.Errorf("OpenCode: expected original prefixed name %q in duplicate map", want)
		}
	}
}

// TestParseAndAddToolsToRequest_OpenCode_NoDuplicate verifies end-to-end deduplication
// for OpenCode in the dual-gateway scenario.
func TestParseAndAddToolsToRequest_OpenCode_NoDuplicate(t *testing.T) {
	t.Parallel()

	cm := &mockToolClientManager{
		tools: []schemas.ChatTool{
			makeTool("testing_exa-web_fetch_exa"),
			makeTool("ctx7-query-docs"),
			makeTool("filesystem-create_directory"),
		},
	}
	tm := newToolsManagerForTest(cm)

	// OpenCode sends tools prefixed as {server}_{tool_name}.
	req := buildChatRequest(
		"bifrost_testing_exa-web_fetch_exa",
		"bifrost_ctx7-query-docs",
		"bifrost_filesystem-create_directory",
	)
	ctx := contextWithUserAgent(schemas.OpenCode.String())

	result := tm.ParseAndAddToolsToRequest(ctx, req)

	names := toolNamesFromChatRequest(result)

	// Bare Bifrost names must NOT be injected again — they're covered by prefixed entries.
	for _, bare := range []string{"testing_exa-web_fetch_exa", "ctx7-query-docs", "filesystem-create_directory"} {
		if countOccurrences(names, bare) != 0 {
			t.Errorf("OpenCode: bare %q should not be injected, got %d (names: %v)",
				bare, countOccurrences(names, bare), names)
		}
	}
	// Prefixed originals must remain exactly once.
	for _, prefixed := range []string{
		"bifrost_testing_exa-web_fetch_exa",
		"bifrost_ctx7-query-docs",
		"bifrost_filesystem-create_directory",
	} {
		if countOccurrences(names, prefixed) != 1 {
			t.Errorf("OpenCode: expected exactly 1 %q, got %d (names: %v)",
				prefixed, countOccurrences(names, prefixed), names)
		}
	}
}
