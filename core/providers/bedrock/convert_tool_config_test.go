package bedrock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestConvertToolConfig_DropsServerToolsOnBedrock locks in the bug fix from
// the user-reported repro: sending `web_search_20260209` via the OpenAI-
// compatible /v1/chat/completions endpoint to Bedrock was producing a
// malformed ToolConfig that Bedrock rejected with 400 "The provided request
// is not valid". The fix strips unsupported server tools before the
// conversion loop so the outbound request is valid.
func TestConvertToolConfig_DropsServerToolsOnBedrock(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        "get_weather",
					Description: schemas.Ptr("Get weather by city"),
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
					},
				},
			},
			{
				// Server tool — Bedrock doesn't support web_search per Table 20.
				// Should be stripped silently.
				Type: schemas.ChatToolType("web_search_20260209"),
				Name: "web_search",
			},
		},
	}

	cfg := convertToolConfig("global.anthropic.claude-sonnet-4-6", params)
	if cfg == nil {
		t.Fatalf("expected ToolConfig, got nil (function tool should have survived)")
	}
	if len(cfg.Tools) != 1 {
		t.Fatalf("expected exactly 1 tool (function), got %d: %+v", len(cfg.Tools), cfg.Tools)
	}
	if cfg.Tools[0].ToolSpec == nil || cfg.Tools[0].ToolSpec.Name != "get_weather" {
		t.Errorf("expected function tool 'get_weather' to survive, got %+v", cfg.Tools[0])
	}
}

// TestConvertToolConfig_ReturnsNilWhenAllDropped locks in the empty-slice
// guard. Bedrock's Converse API rejects `"toolConfig": {"tools": []}` with a
// 400; when every tool is unsupported and gets stripped, convertToolConfig
// must return nil so no ToolConfig ships at all.
func TestConvertToolConfig_ReturnsNilWhenAllDropped(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type: schemas.ChatToolType("web_search_20260209"),
				Name: "web_search",
			},
			{
				Type: schemas.ChatToolType("web_fetch_20260309"),
				Name: "web_fetch",
			},
			{
				Type: schemas.ChatToolType("code_execution_20250825"),
				Name: "code_execution",
			},
		},
	}

	cfg := convertToolConfig("global.anthropic.claude-sonnet-4-6", params)
	if cfg != nil {
		t.Fatalf("expected nil ToolConfig (all tools unsupported on Bedrock), got %+v", cfg)
	}
}

// TestConvertToolConfig_KeepsBedrockSupportedServerTools — locks in that
// Bedrock-supported server tools (bash, memory, text_editor, computer,
// tool_search) do NOT appear in Converse's typed toolConfig.tools slot —
// they must be tunneled via additionalModelRequestFields (exercised in
// TestCollectBedrockServerTools_*). If the only tool is a server tool,
// toolConfig is nil so we don't ship {"toolConfig": {"tools": []}}.
func TestConvertToolConfig_KeepsBedrockSupportedServerTools(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type: schemas.ChatToolType("bash_20250124"),
				Name: "bash",
			},
		},
	}

	cfg := convertToolConfig("global.anthropic.claude-sonnet-4-6", params)
	if cfg != nil {
		t.Fatalf("expected nil toolConfig (server tools flow via additionalModelRequestFields, not toolSpec), got %+v", cfg)
	}
}

// TestCollectBedrockServerTools_BashOnly — bash is Bedrock-supported per the
// B-header list; the helper must emit it as a native-JSON tool entry with no
// derived beta header (bash has no high-confidence 1:1 beta-header mapping;
// callers rely on extra-headers for that).
func TestCollectBedrockServerTools_BashOnly(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type: schemas.ChatToolType("bash_20250124"),
				Name: "bash",
			},
		},
	}
	tools, betas := collectBedrockServerTools(params)
	if len(tools) != 1 {
		t.Fatalf("expected 1 server tool, got %d", len(tools))
	}
	got := string(tools[0])
	if !strings.Contains(got, `"type":"bash_20250124"`) || !strings.Contains(got, `"name":"bash"`) {
		t.Errorf("expected native Anthropic bash shape, got %s", got)
	}
	if len(betas) != 0 {
		t.Errorf("expected no derived beta headers for bash (no 1:1 mapping), got %v", betas)
	}
}

// TestCollectBedrockServerTools_ComputerDerivesBeta — computer_YYYYMMDD must
// derive computer-use-YYYY-MM-DD as the beta header, gated through
// FilterBetaHeadersForProvider(Bedrock) which keeps computer-use-* headers.
func TestCollectBedrockServerTools_ComputerDerivesBeta(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type:            schemas.ChatToolType("computer_20251124"),
				Name:            "computer",
				DisplayWidthPx:  schemas.Ptr(1280),
				DisplayHeightPx: schemas.Ptr(800),
			},
		},
	}
	tools, betas := collectBedrockServerTools(params)
	if len(tools) != 1 {
		t.Fatalf("expected 1 server tool, got %d", len(tools))
	}
	if !strings.Contains(string(tools[0]), `"display_width_px":1280`) {
		t.Errorf("expected computer variant fields to flow through, got %s", string(tools[0]))
	}
	if len(betas) != 1 || betas[0] != "computer-use-2025-11-24" {
		t.Errorf("expected [computer-use-2025-11-24], got %v", betas)
	}
}

// TestCollectBedrockServerTools_MemoryDerivesContextManagement — memory
// activates via the context-management-2025-06-27 bundle on Bedrock (cite:
// anthropic/types.go:179).
func TestCollectBedrockServerTools_MemoryDerivesContextManagement(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type: schemas.ChatToolType("memory_20250818"),
				Name: "memory",
			},
		},
	}
	_, betas := collectBedrockServerTools(params)
	if len(betas) != 1 || betas[0] != "context-management-2025-06-27" {
		t.Errorf("expected [context-management-2025-06-27], got %v", betas)
	}
}

// TestCollectBedrockServerTools_StripsUnsupported — web_search isn't in
// Bedrock's ProviderFeatures (WebSearch=false), so ValidateChatToolsForProvider
// drops it and the helper must emit nothing.
func TestCollectBedrockServerTools_StripsUnsupported(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type: schemas.ChatToolType("web_search_20260209"),
				Name: "web_search",
			},
		},
	}
	tools, betas := collectBedrockServerTools(params)
	if len(tools) != 0 {
		t.Errorf("expected no server tools (web_search unsupported on Bedrock), got %d", len(tools))
	}
	if len(betas) != 0 {
		t.Errorf("expected no betas when all tools filtered, got %v", betas)
	}
}

// TestCollectBedrockServerTools_FunctionToolsIgnored — function/custom tools
// go through convertToolConfig, not this helper.
func TestCollectBedrockServerTools_FunctionToolsIgnored(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name: "get_weather",
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
					},
				},
			},
		},
	}
	tools, betas := collectBedrockServerTools(params)
	if len(tools) != 0 || len(betas) != 0 {
		t.Errorf("function tools should not flow through server-tool helper, got tools=%d betas=%v", len(tools), betas)
	}
}

// TestBuildBedrockServerToolChoice_PinnedServerTool — caller pins a kept
// server tool (computer) by name. Converse's typed toolConfig.toolChoice path
// can't carry this because toolConfig.tools doesn't include server tools; the
// existing reconciliation silently drops the pin. The tunneled path must
// emit {"type":"tool","name":"computer"} into additionalModelRequestFields.
func TestBuildBedrockServerToolChoice_PinnedServerTool(t *testing.T) {
	computer := schemas.ChatTool{
		Type:           schemas.ChatToolType("computer_20251124"),
		Name:           "computer",
		DisplayWidthPx: schemas.Ptr(1280),
	}
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{computer},
		ToolChoice: &schemas.ChatToolChoice{
			ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "computer"},
			},
		},
	}
	choice, ok := buildBedrockServerToolChoice(params, []schemas.ChatTool{computer})
	if !ok {
		t.Fatalf("expected tunneled tool_choice for pinned server tool, got (nil, false)")
	}
	got := string(choice)
	if !strings.Contains(got, `"type":"tool"`) || !strings.Contains(got, `"name":"computer"`) {
		t.Errorf("expected Anthropic-native {type:tool,name:computer}, got %s", got)
	}
}

// TestBuildBedrockServerToolChoice_PinnedFunctionTool_NotTunneled — function
// tool pins stay on Converse's typed path (toolConfig.toolChoice.tool). The
// helper must not double-emit.
func TestBuildBedrockServerToolChoice_PinnedFunctionTool_NotTunneled(t *testing.T) {
	fn := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:       "get_weather",
			Parameters: &schemas.ToolFunctionParameters{Type: "object"},
		},
	}
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{fn},
		ToolChoice: &schemas.ChatToolChoice{
			ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "get_weather"},
			},
		},
	}
	if _, ok := buildBedrockServerToolChoice(params, []schemas.ChatTool{fn}); ok {
		t.Errorf("expected no tunneling for function-tool pin (typed Converse path handles it)")
	}
}

// TestBuildBedrockServerToolChoice_AnyWithOnlyServerTools — tool_choice:any
// with only server tools: convertToolConfig returns nil (bedrockTools empty),
// so the typed any-contract is lost. The tunneled path must emit
// {"type":"any"} to preserve the forcing semantics.
func TestBuildBedrockServerToolChoice_AnyWithOnlyServerTools(t *testing.T) {
	bash := schemas.ChatTool{
		Type: schemas.ChatToolType("bash_20250124"),
		Name: "bash",
	}
	anyStr := string(schemas.ChatToolChoiceTypeAny)
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{bash},
		ToolChoice: &schemas.ChatToolChoice{
			ChatToolChoiceStr: &anyStr,
		},
	}
	choice, ok := buildBedrockServerToolChoice(params, []schemas.ChatTool{bash})
	if !ok {
		t.Fatalf("expected tunneled any-contract when only server tools are present, got (nil, false)")
	}
	got := string(choice)
	if !strings.Contains(got, `"type":"any"`) {
		t.Errorf("expected {type:any}, got %s", got)
	}
}

// TestBuildBedrockServerToolChoice_AnyWithFunctionTool_NotTunneled — when at
// least one function/custom tool is present, Converse's typed
// toolConfig.toolChoice.any carries the any-contract. Don't double-emit.
func TestBuildBedrockServerToolChoice_AnyWithFunctionTool_NotTunneled(t *testing.T) {
	fn := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:       "get_weather",
			Parameters: &schemas.ToolFunctionParameters{Type: "object"},
		},
	}
	bash := schemas.ChatTool{
		Type: schemas.ChatToolType("bash_20250124"),
		Name: "bash",
	}
	anyStr := string(schemas.ChatToolChoiceTypeAny)
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{fn, bash},
		ToolChoice: &schemas.ChatToolChoice{
			ChatToolChoiceStr: &anyStr,
		},
	}
	if _, ok := buildBedrockServerToolChoice(params, []schemas.ChatTool{fn, bash}); ok {
		t.Errorf("expected no tunneling when function/custom tool is present (typed Converse path handles any)")
	}
}

// TestBuildBedrockServerToolChoice_UnsupportedServerToolPin_NotTunneled — the
// caller pins web_search, which ValidateChatToolsForProvider strips on
// Bedrock. The pin name is absent from the filtered set; the helper must not
// fabricate a tunneled tool_choice for a tool that isn't in the request.
func TestBuildBedrockServerToolChoice_UnsupportedServerToolPin_NotTunneled(t *testing.T) {
	// The caller's original request had web_search, but it's been stripped.
	// We pass the filtered slice (empty for the server-tool axis) to mimic
	// the convertChatParameters call path.
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{{Type: schemas.ChatToolType("web_search_20260209"), Name: "web_search"}},
		ToolChoice: &schemas.ChatToolChoice{
			ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type:     schemas.ChatToolChoiceTypeFunction,
				Function: &schemas.ChatToolChoiceFunction{Name: "web_search"},
			},
		},
	}
	// Filtered (post-ValidateChatToolsForProvider(Bedrock)) — web_search is dropped.
	filtered := []schemas.ChatTool{}
	if _, ok := buildBedrockServerToolChoice(params, filtered); ok {
		t.Errorf("expected no tunneling when pinned name was stripped by provider validation")
	}
}

// TestConvertChatParameters_PinnedServerToolE2E — end-to-end verification
// that convertChatParameters composes convertToolConfig +
// collectBedrockServerTools + buildBedrockServerToolChoice such that a
// request pinning a kept server tool produces:
//   - AdditionalModelRequestFields.tools containing the server tool
//   - AdditionalModelRequestFields.tool_choice with Anthropic-native shape
//   - ToolConfig nil (no function tools → Converse's typed path is inactive)
func TestConvertChatParameters_PinnedServerToolE2E(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Model: "global.anthropic.claude-sonnet-4-6",
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{
					Type:           schemas.ChatToolType("computer_20251124"),
					Name:           "computer",
					DisplayWidthPx: schemas.Ptr(1280),
				},
			},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type:     schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{Name: "computer"},
				},
			},
		},
	}
	bedrockReq := &BedrockConverseRequest{}
	if err := convertChatParameters(nil, bifrostReq, bedrockReq); err != nil {
		t.Fatalf("convertChatParameters failed: %v", err)
	}
	if bedrockReq.ToolConfig != nil {
		t.Errorf("expected nil ToolConfig (no function/custom tools), got %+v", bedrockReq.ToolConfig)
	}
	if bedrockReq.AdditionalModelRequestFields == nil {
		t.Fatalf("expected AdditionalModelRequestFields to carry server-tool payload, got nil")
	}
	tools, ok := bedrockReq.AdditionalModelRequestFields.Get("tools")
	if !ok {
		t.Errorf("expected additionalModelRequestFields.tools to be set for server tool")
	} else if toolsSlice, castOK := tools.([]json.RawMessage); !castOK || len(toolsSlice) != 1 {
		t.Errorf("expected 1 server tool in additionalModelRequestFields.tools, got %+v", tools)
	}
	choice, ok := bedrockReq.AdditionalModelRequestFields.Get("tool_choice")
	if !ok {
		t.Fatalf("expected additionalModelRequestFields.tool_choice to carry pinned server-tool contract")
	}
	choiceRaw, castOK := choice.(json.RawMessage)
	if !castOK {
		t.Fatalf("expected tool_choice value to be json.RawMessage, got %T", choice)
	}
	got := string(choiceRaw)
	if !strings.Contains(got, `"type":"tool"`) || !strings.Contains(got, `"name":"computer"`) {
		t.Errorf("expected {type:tool,name:computer}, got %s", got)
	}
}

// TestConvertChatParameters_ResponseFormatWithPinnedServerTool_NoConflictingChoice
// locks in the fix for the "two conflicting tool-choice directives" hazard:
// when response_format forces the synthetic bf_so_* tool via
// ToolConfig.ToolChoice, the tunneled additionalModelRequestFields.tool_choice
// (which would pin a server tool) must be suppressed so Bedrock doesn't
// receive both pins in the same Converse call. Uses a Nova model since
// Anthropic models route response_format through native output_config.format
// (no synthetic tool), so the conflict only surfaces on non-Anthropic
// Bedrock targets.
func TestConvertChatParameters_ResponseFormatWithPinnedServerTool_NoConflictingChoice(t *testing.T) {
	responseFormat := any(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "classification",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"topic": map[string]any{"type": "string"},
				},
				"required": []any{"topic"},
			},
		},
	})

	bifrostReq := &schemas.BifrostChatRequest{
		Model: "amazon.nova-pro-v1:0",
		Params: &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolType("bash_20250124"),
					Name: "bash",
				},
			},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type:     schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{Name: "bash"},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq := &BedrockConverseRequest{}
	if err := convertChatParameters(ctx, bifrostReq, bedrockReq); err != nil {
		t.Fatalf("convertChatParameters failed: %v", err)
	}

	// Synthetic bf_so_* tool must be injected and pinned via Converse's typed path.
	if bedrockReq.ToolConfig == nil {
		t.Fatalf("expected ToolConfig with synthetic bf_so_* tool, got nil")
	}
	if bedrockReq.ToolConfig.ToolChoice == nil || bedrockReq.ToolConfig.ToolChoice.Tool == nil {
		t.Fatalf("expected ToolConfig.ToolChoice.Tool to pin synthetic structured-output tool, got %+v", bedrockReq.ToolConfig.ToolChoice)
	}
	if !strings.HasPrefix(bedrockReq.ToolConfig.ToolChoice.Tool.Name, "bf_so_") {
		t.Errorf("expected ToolConfig.ToolChoice.Tool.Name to start with bf_so_, got %q", bedrockReq.ToolConfig.ToolChoice.Tool.Name)
	}

	// Server tool must still be tunneled so the model has it available.
	if bedrockReq.AdditionalModelRequestFields == nil {
		t.Fatalf("expected AdditionalModelRequestFields to carry tunneled server-tool payload, got nil")
	}
	if _, ok := bedrockReq.AdditionalModelRequestFields.Get("tools"); !ok {
		t.Errorf("expected additionalModelRequestFields.tools to still carry bash server tool")
	}

	// Guarded field: tunneled tool_choice MUST be absent because response_format
	// forces the synthetic tool. Two tool-choice directives in the same request
	// would let Bedrock pick one and silently violate the structured-output contract.
	if _, ok := bedrockReq.AdditionalModelRequestFields.Get("tool_choice"); ok {
		t.Errorf("expected NO additionalModelRequestFields.tool_choice when response_format pins bf_so_* (conflict hazard)")
	}
}
