package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestConvertToolConfig_NovaGetsWebSearchAndCodeExecution is a regression test:
// the chat-completions Bedrock builder had no branch converting web_search/
// code_execution into Bedrock's Nova system-tool shape at all — only the
// Responses-path builder (ToBedrockResponsesRequest) did — so a Nova2 model could
// never receive nova_grounding/nova_code_interpreter via chat completions, even
// though ValidateChatToolsForProvider (once aligned with the Responses-path
// validator) keeps these tool types for Bedrock via the WebSearchNova/CodeExecNova
// carve-out.
func TestConvertToolConfig_NovaGetsWebSearchAndCodeExecution(t *testing.T) {
	params := &schemas.ChatParameters{
		Tools: []schemas.ChatTool{
			{Type: schemas.ChatToolType("web_search_20260209"), Name: "web_search"},
			{Type: schemas.ChatToolType("code_execution_20250825"), Name: "code_execution"},
		},
	}

	cfg := convertToolConfig("amazon.nova-2-lite-v1:0", params)
	if cfg == nil {
		t.Fatalf("expected a ToolConfig with Nova system tools, got nil")
	}
	if len(cfg.Tools) != 2 {
		t.Fatalf("expected 2 Nova system tools, got %d: %+v", len(cfg.Tools), cfg.Tools)
	}
	var gotGrounding, gotCodeInterpreter bool
	for _, tool := range cfg.Tools {
		if tool.SystemTool == nil {
			t.Errorf("expected a SystemTool entry, got %+v", tool)
			continue
		}
		switch tool.SystemTool.Name {
		case BedrockSystemToolNovaGrounding:
			gotGrounding = true
		case BedrockSystemToolNovaCodeInterpreter:
			gotCodeInterpreter = true
		}
	}
	if !gotGrounding {
		t.Error("expected nova_grounding system tool for web_search")
	}
	if !gotCodeInterpreter {
		t.Error("expected nova_code_interpreter system tool for code_execution")
	}
}

// TestToBedrockChatCompletionRequest_NovaAliasGetsWebSearch is a regression test for a
// CodeRabbit finding on PR #5821: convertChatParameters already computes capModel :=
// ResolveCanonicalModel(ctx, bifrostReq.Model) for capability gating, but the call to
// convertToolConfigFromFiltered passed the raw bifrostReq.Model instead — so a model
// alias that resolves to Nova2 but whose raw string doesn't literally contain "nova-2"
// would fail IsNova2Model's substring check and silently drop web_search/code_execution
// instead of converting them to nova_grounding/nova_code_interpreter. The Responses-path
// sibling (ToBedrockResponsesRequest) already passes capModel correctly.
func TestToBedrockChatCompletionRequest_NovaAliasGetsWebSearch(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "my-nova-alias",
		Config: &schemas.AliasConfig{
			ModelID: "amazon.nova-2-lite-v1:0",
		},
	})
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "my-nova-alias", // raw string does not contain "nova-2"
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("search the web")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{Type: schemas.ChatToolType("web_search_20260209"), Name: "web_search"},
			},
		},
	}

	bedrockReq, err := ToBedrockChatCompletionRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bedrockReq.ToolConfig == nil || len(bedrockReq.ToolConfig.Tools) != 1 {
		t.Fatalf("expected exactly 1 tool (nova_grounding), got %+v", bedrockReq.ToolConfig)
	}
	tool := bedrockReq.ToolConfig.Tools[0]
	if tool.SystemTool == nil || tool.SystemTool.Name != BedrockSystemToolNovaGrounding {
		t.Errorf("expected nova_grounding system tool via the aliased canonical model, got %+v", tool)
	}
}

// TestToBedrockChatCompletionRequest_DropsUnsupportedToolsSignaled is a regression
// test: tools silently dropped from a Bedrock-bound request (because the target
// provider/model doesn't support them) must be surfaced via
// BifrostContextKeyDroppedUnsupportedTools so ChatCompletion/Responses can populate
// ExtraFields.DroppedUnsupportedTools instead of leaving the caller uninformed.
func TestToBedrockChatCompletionRequest_DropsUnsupportedToolsSignaled(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "global.anthropic.claude-sonnet-4-6", // not Nova2
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{Type: schemas.ChatToolType("web_fetch_20260309"), Name: "web_fetch"},   // provider-level drop (WebFetch=false)
				{Type: schemas.ChatToolType("web_search_20260209"), Name: "web_search"}, // model-level drop (non-Nova2)
			},
		},
	}

	_, err := ToBedrockChatCompletionRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dropped, ok := ctx.Value(schemas.BifrostContextKeyDroppedUnsupportedTools).([]string)
	if !ok {
		t.Fatalf("expected BifrostContextKeyDroppedUnsupportedTools to be set")
	}
	wantDropped := map[string]bool{"web_fetch_20260309": true, "web_search_20260209": true}
	if len(dropped) != len(wantDropped) {
		t.Fatalf("expected %d dropped tools, got %d: %v", len(wantDropped), len(dropped), dropped)
	}
	for _, d := range dropped {
		if !wantDropped[d] {
			t.Errorf("unexpected dropped tool %q", d)
		}
	}
}

// TestConvertChatParameters_ServerToolNameFromBody is a diagnostic probe for the
// Bedrock managed-tool harness failures (`tools.0.<type>.name: Field required`). It
// reproduces the real inbound seam: the harness body is unmarshaled into
// schemas.ChatParameters (exactly what OpenAIChatRequest.UnmarshalJSON does via sonic),
// then run through convertChatParameters, and the tunneled
// additionalModelRequestFields.tools entry is checked for the `name`. Unlike
// TestCollectBedrockServerTools_BashOnly (which builds the struct directly), this
// exercises the full body -> Unmarshal -> Bedrock convert path where the name is
// suspected to drop.
func TestConvertChatParameters_ServerToolNameFromBody(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		model    string
		wantName string
	}{
		{"bash", `{"tools":[{"type":"bash_20250124","name":"bash"}],"max_tokens":1024}`, "global.anthropic.claude-sonnet-4-6", "bash"},
		{"computer", `{"tools":[{"type":"computer_20251124","name":"computer","display_width_px":1280,"display_height_px":800}],"max_tokens":1024}`, "global.anthropic.claude-sonnet-4-6", "computer"},
		{"text_editor", `{"tools":[{"type":"text_editor_20250728","name":"str_replace_based_edit_tool"}],"max_tokens":1024}`, "global.anthropic.claude-sonnet-4-6", "str_replace_based_edit_tool"},
		{"memory", `{"tools":[{"type":"memory_20250818","name":"memory"}],"max_tokens":1024}`, "global.anthropic.claude-opus-4-7", "memory"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var params schemas.ChatParameters
			if err := schemas.Unmarshal([]byte(tc.body), &params); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			// Sanity: the inbound parse should retain the top-level name.
			if len(params.Tools) != 1 || params.Tools[0].Name != tc.wantName {
				t.Fatalf("inbound ChatParameters dropped name: %+v", params.Tools)
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			bifrostReq := &schemas.BifrostChatRequest{
				Model:  tc.model,
				Params: &params,
				Input: []schemas.ChatMessage{
					{
						Role:    schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Run ls")},
					},
				},
			}
			// Go through the real public entrypoint (messages + ensureChatToolConfig
			// + cache-point stripping), not just convertChatParameters.
			bedrockReq, err := ToBedrockChatCompletionRequest(ctx, bifrostReq)
			if err != nil {
				t.Fatalf("ToBedrockChatCompletionRequest: %v", err)
			}

			if bedrockReq.AdditionalModelRequestFields == nil {
				t.Fatalf("expected additionalModelRequestFields.tools to be set")
			}
			raw, ok := bedrockReq.AdditionalModelRequestFields.Get("tools")
			if !ok {
				t.Fatalf("expected tunneled server tools, got none")
			}
			serialized, err := schemas.Marshal(raw)
			if err != nil {
				t.Fatalf("marshal tunneled tools: %v", err)
			}
			if !strings.Contains(string(serialized), `"name":"`+tc.wantName+`"`) {
				t.Errorf("tunneled server tool is missing name (in-memory value); got %s", string(serialized))
			}

			// Wire-level: marshal the FULL request exactly as bedrock.go does
			// (providerUtils.MarshalSorted at bedrock.go:2990). This routes the
			// []json.RawMessage tools value through OrderedMap.MarshalJSON, the
			// suspected drop point for the harness 400s.
			wire, err := schemas.MarshalSorted(bedrockReq)
			if err != nil {
				t.Fatalf("marshal full request: %v", err)
			}
			if !strings.Contains(string(wire), `"name":"`+tc.wantName+`"`) {
				t.Errorf("WIRE request is missing tool name; got %s", string(wire))
			}
		})
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

// TestConvertInferenceConfig_GLMSkipsStopSequences locks in the fix for the
// user-reported 400 ("This model doesn't support the stopSequences field"):
// GLM models on Bedrock reject inferenceConfig.stopSequences, so we omit it
// for them while keeping it for every other model family.
func TestConvertInferenceConfig_GLMSkipsStopSequences(t *testing.T) {
	params := &schemas.ChatParameters{
		MaxCompletionTokens: schemas.Ptr(12000),
		Stop:                []string{"hi"},
	}

	glm := convertInferenceConfig(params, "zai.glm-5")
	if glm.StopSequences != nil {
		t.Fatalf("expected StopSequences to be omitted for GLM, got %v", glm.StopSequences)
	}
	// Other inference params must still pass through.
	if glm.MaxTokens == nil || *glm.MaxTokens != 12000 {
		t.Fatalf("expected MaxTokens to be preserved, got %v", glm.MaxTokens)
	}

	other := convertInferenceConfig(params, "anthropic.claude-sonnet-4")
	if len(other.StopSequences) != 1 || other.StopSequences[0] != "hi" {
		t.Fatalf("expected StopSequences to be preserved for non-GLM, got %v", other.StopSequences)
	}
}

// TestToBedrockResponsesRequest_GLMSkipsStopSequences verifies the Responses
// path (where `stop` arrives via ExtraParams) also drops stopSequences for GLM.
func TestToBedrockResponsesRequest_GLMSkipsStopSequences(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostResponsesRequest{
		Model: "zai.glm-5",
		Input: []schemas.ResponsesMessage{glmUserMessage("hello")},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(32000),
			ExtraParams:     map[string]interface{}{"stop": []string{"hi"}},
		},
	}

	bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
	}
	if bedrockReq.InferenceConfig != nil && bedrockReq.InferenceConfig.StopSequences != nil {
		t.Fatalf("expected StopSequences to be omitted for GLM, got %v", bedrockReq.InferenceConfig.StopSequences)
	}
}

// TestToBedrockResponsesRequest_OmitsToolChoiceWhenNoTools reproduces the 400
// ("The provided request is not valid"): a web_search tool with tool_choice:auto
// on GLM. Web search is skipped for GLM, so no tools survive — the request must
// not carry a toolConfig that holds a toolChoice with an empty tools list.
func TestToBedrockResponsesRequest_OmitsToolChoiceWhenNoTools(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	autoStr := string(schemas.ResponsesToolChoiceTypeAuto)
	req := &schemas.BifrostResponsesRequest{
		Model: "zai.glm-5",
		Input: []schemas.ResponsesMessage{glmUserMessage("Perform a web search")},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(32000),
			Tools:           []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
			ToolChoice: &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStr: &autoStr,
			},
		},
	}

	bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
	}
	if bedrockReq.ToolConfig != nil {
		t.Fatalf("expected no ToolConfig when all tools are skipped, got %+v", bedrockReq.ToolConfig)
	}
}

// TestToBedrockResponsesRequest_WebSearchPreviewGetsNovaGrounding is a regression test
// for a CodeRabbit finding on PR #5821: ValidateResponsesToolsForProvider keeps both
// web_search and web_search_preview for Bedrock (case schemas.ResponsesToolTypeWebSearch,
// schemas.ResponsesToolTypeWebSearchPreview), but the Nova-conversion loop only checked
// tool.Type == ResponsesToolTypeWebSearch — a kept-but-unconverted web_search_preview
// tool would silently disappear: no nova_grounding system tool on Nova2, no
// drop-signal on non-Nova2.
func TestToBedrockResponsesRequest_WebSearchPreviewGetsNovaGrounding(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostResponsesRequest{
		Model: "amazon.nova-2-lite-v1:0",
		Input: []schemas.ResponsesMessage{glmUserMessage("search the web")},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(2000),
			Tools:           []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearchPreview}},
		},
	}

	bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
	}
	if bedrockReq.ToolConfig == nil || len(bedrockReq.ToolConfig.Tools) != 1 {
		t.Fatalf("expected exactly 1 tool (nova_grounding), got %+v", bedrockReq.ToolConfig)
	}
	tool := bedrockReq.ToolConfig.Tools[0]
	if tool.SystemTool == nil || tool.SystemTool.Name != BedrockSystemToolNovaGrounding {
		t.Errorf("expected nova_grounding system tool for web_search_preview, got %+v", tool)
	}
}

func glmUserMessage(text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentStr: schemas.Ptr(text),
		},
	}
}

// TestExtractToolsFromConversationHistory_StableOrder locks in the fix for the
// non-deterministic tool ordering that both reconstruction paths had: toolConfig
// was built by ranging a Go map, so identical requests produced different tool
// orders. Tools are the first thing in Bedrock's prompt-cache prefix, so every
// reshuffle was a guaranteed cache miss.
func TestExtractToolsFromConversationHistory_StableOrder(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	names := []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"}

	t.Run("responses", func(t *testing.T) {
		input := []schemas.ResponsesMessage{}
		for i, name := range names {
			input = append(input, schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(fmt.Sprintf("call_%d", i)),
					Name:   schemas.Ptr(name),
				},
			})
		}
		for i := 0; i < 50; i++ {
			_, tools := extractToolsFromResponsesConversationHistory(ctx, input, "us.anthropic.claude-opus-5")
			assertToolOrder(t, tools, names)
		}
	})

	t.Run("chat", func(t *testing.T) {
		toolCalls := []schemas.ChatAssistantMessageToolCall{}
		for i, name := range names {
			toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
				ID:       schemas.Ptr(fmt.Sprintf("call_%d", i)),
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(name)},
			})
		}
		input := []schemas.ChatMessage{{
			Role:                 schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: toolCalls},
		}}
		for i := 0; i < 50; i++ {
			_, tools := extractToolsFromConversationHistory(ctx, input)
			assertToolOrder(t, tools, names)
		}
	})
}

func assertToolOrder(t *testing.T, tools []BedrockTool, want []string) {
	t.Helper()
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for i, tool := range tools {
		if tool.ToolSpec == nil {
			t.Fatalf("tools[%d] has no ToolSpec", i)
		}
		if tool.ToolSpec.Name != want[i] {
			t.Fatalf("tool order drifted: got %s at index %d, want %s", tool.ToolSpec.Name, i, want[i])
		}
	}
}
