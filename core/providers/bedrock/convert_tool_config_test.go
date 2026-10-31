package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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
	tools, betas := collectBedrockServerTools("us.anthropic.claude-opus-5", params)
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
	tools, betas := collectBedrockServerTools("us.anthropic.claude-opus-5", params)
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
	_, betas := collectBedrockServerTools("us.anthropic.claude-opus-5", params)
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
	tools, betas := collectBedrockServerTools("us.anthropic.claude-opus-5", params)
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
	tools, betas := collectBedrockServerTools("us.anthropic.claude-opus-5", params)
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
		Provider: schemas.Bedrock,
		Model:    "global.anthropic.claude-sonnet-4-6",
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
	if err := convertChatParameters(nil, bifrostReq, bedrockReq, schemas.ResolveModelCaps(bifrostReq.Provider, bifrostReq.Model)); err != nil {
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
		Provider: schemas.Bedrock,
		Model:    "amazon.nova-pro-v1:0",
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
	if err := convertChatParameters(ctx, bifrostReq, bedrockReq, schemas.ResolveModelCaps(bifrostReq.Provider, bifrostReq.Model)); err != nil {
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

	glm := convertInferenceConfig(params, schemas.ResolveModelCaps(schemas.Bedrock, "zai.glm-5"))
	if glm.StopSequences != nil {
		t.Fatalf("expected StopSequences to be omitted for GLM, got %v", glm.StopSequences)
	}
	// Other inference params must still pass through.
	if glm.MaxTokens == nil || *glm.MaxTokens != 12000 {
		t.Fatalf("expected MaxTokens to be preserved, got %v", glm.MaxTokens)
	}

	other := convertInferenceConfig(params, schemas.ResolveModelCaps(schemas.Bedrock, "anthropic.claude-sonnet-4"))
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

// installBedrockCapabilityRecord points the capability resolver at one model for
// the duration of the test, so a gate can be driven from the datasheet side.
func installBedrockCapabilityRecord(t *testing.T, model string, record *schemas.ModelCapabilities) {
	t.Helper()
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if m != model {
			return nil
		}
		return record
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

// TestBedrockCapabilityGatesReadDatasheet covers the datasheet side of the gates
// migrated off model-family checks. The name-based fallbacks stay covered by the
// GLM / cache-point / tool-choice tests elsewhere in the package; these pin that
// a record overrides them, in both directions.
func TestBedrockCapabilityGatesReadDatasheet(t *testing.T) {
	t.Run("stop_survives_when_row_marks_it_supported", func(t *testing.T) {
		const model = "zai.glm-5"
		installBedrockCapabilityRecord(t, model, &schemas.ModelCapabilities{
			UnsupportedFields: map[string]bool{schemas.FieldStop: false},
		})

		config := convertInferenceConfig(
			&schemas.ChatParameters{Stop: []string{"hi"}},
			schemas.ResolveModelCaps(schemas.Bedrock, model),
		)
		if len(config.StopSequences) != 1 {
			t.Fatalf("an explicit false must beat the GLM name check, got %v", config.StopSequences)
		}
	})

	t.Run("stop_dropped_when_row_marks_it_unsupported", func(t *testing.T) {
		const model = "anthropic.claude-sonnet-4"
		installBedrockCapabilityRecord(t, model, &schemas.ModelCapabilities{
			UnsupportedFields: map[string]bool{schemas.FieldStop: true},
		})

		config := convertInferenceConfig(
			&schemas.ChatParameters{Stop: []string{"hi"}},
			schemas.ResolveModelCaps(schemas.Bedrock, model),
		)
		if config.StopSequences != nil {
			t.Fatalf("an explicit true must drop stop on a non-GLM model, got %v", config.StopSequences)
		}
	})

	t.Run("tool_choice_pin_dropped_when_row_marks_struct_unsupported", func(t *testing.T) {
		const model = "anthropic.claude-sonnet-4"
		installBedrockCapabilityRecord(t, model, &schemas.ModelCapabilities{
			ToolChoiceStructSupported: new(false),
		})

		params := &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type:     schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{Name: "get_weather"},
			}},
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type:     schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{Name: "get_weather"},
				},
			},
		}
		config := convertToolConfig(model, params)
		if config == nil {
			t.Fatal("expected a tool config")
		}
		if config.ToolChoice != nil {
			t.Fatalf("the named pin must be dropped, got %+v", config.ToolChoice)
		}
	})

	t.Run("cache_points_stripped_when_row_says_unsupported", func(t *testing.T) {
		const model = "anthropic.claude-sonnet-4"
		installBedrockCapabilityRecord(t, model, &schemas.ModelCapabilities{
			SupportsCachePoint: new(false),
		})

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req, err := ToBedrockChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    model,
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
			}},
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{{
					Type:         schemas.ChatToolTypeFunction,
					Function:     &schemas.ChatToolFunction{Name: "get_weather"},
					CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
				}},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.ToolConfig != nil {
			for _, tool := range req.ToolConfig.Tools {
				if tool.CachePoint != nil {
					t.Fatal("cache points must be stripped when the row says unsupported")
				}
			}
		}
	})
}

// codexRootOneOfSchema is the Codex automation_update shape: a tool whose
// input_schema is a root-level oneOf, which Claude rejects on Converse.
const codexRootOneOfSchema = `{"type":"object","oneOf":[{"type":"object","properties":{"mode":{"const":"create"},"name":{"type":"string"}},"required":["mode","name"],"additionalProperties":false},{"type":"object","properties":{"mode":{"const":"update"},"id":{"type":"string"}},"required":["mode","id"],"additionalProperties":false}]}`

func mustToolSchema(t testing.TB, raw string) *schemas.ToolFunctionParameters {
	t.Helper()
	var params schemas.ToolFunctionParameters
	if err := schemas.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("unmarshal tool schema: %v", err)
	}
	return &params
}

// tooManyBranchesSchema exceeds the Anthropic normalizer's branch bound.
func tooManyBranchesSchema(t testing.TB) *schemas.ToolFunctionParameters {
	branches := make([]string, 0, 65)
	for i := range 65 {
		branches = append(branches, fmt.Sprintf(`{"type":"object","properties":{"p%d":{"type":"string"}}}`, i))
	}
	return mustToolSchema(t, `{"type":"object","oneOf":[`+strings.Join(branches, ",")+`]}`)
}

func bedrockChatToolSchemaRequest(model string, params *schemas.ToolFunctionParameters) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type:     schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{Name: "automation_update", Parameters: params},
			}},
		},
	}
}

func bedrockResponsesToolSchemaRequest(model string, params *schemas.ToolFunctionParameters) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input:    []schemas.ResponsesMessage{glmUserMessage("hi")},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  schemas.Ptr("automation_update"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: params},
			}},
		},
	}
}

// convertBedrockToolSchema runs one Converse builder and returns the decoded
// inputSchema of its single function tool.
func convertBedrockToolSchema(t *testing.T, path string, ctx *schemas.BifrostContext, model string, params *schemas.ToolFunctionParameters) (map[string]any, error) {
	t.Helper()
	var toolConfig *BedrockToolConfig
	switch path {
	case "chat":
		req, err := ToBedrockChatCompletionRequest(ctx, bedrockChatToolSchemaRequest(model, params))
		if err != nil {
			return nil, err
		}
		toolConfig = req.ToolConfig
	case "responses":
		req, err := ToBedrockResponsesRequest(ctx, bedrockResponsesToolSchemaRequest(model, params))
		if err != nil {
			return nil, err
		}
		toolConfig = req.ToolConfig
	}
	if toolConfig == nil || len(toolConfig.Tools) == 0 || toolConfig.Tools[0].ToolSpec == nil {
		t.Fatalf("expected a function toolSpec, got %+v", toolConfig)
	}
	var schema map[string]any
	if err := json.Unmarshal(toolConfig.Tools[0].ToolSpec.InputSchema.JSON, &schema); err != nil {
		t.Fatalf("decode inputSchema: %v", err)
	}
	return schema, nil
}

// TestConverseToolSchemaRootCompositionClaude pins the Converse fix for Claude:
// Bedrock forwards toolSpec.inputSchema to Claude, which rejects oneOf/anyOf/allOf
// at the root ("input_schema does not support oneOf, allOf, or anyOf at the top
// level"), so the Chat and Responses builders flatten it the way the Anthropic
// provider does. Non-Claude Converse models accept root compositions and keep them.
func TestConverseToolSchemaRootCompositionClaude(t *testing.T) {
	for _, path := range []string{"chat", "responses"} {
		t.Run(path+"/claude_root_oneOf_flattened", func(t *testing.T) {
			params := mustToolSchema(t, codexRootOneOfSchema)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			schema, err := convertBedrockToolSchema(t, path, ctx, "global.anthropic.claude-sonnet-4-6", params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
				if _, ok := schema[keyword]; ok {
					t.Fatalf("root %s must be flattened for Claude, got %v", keyword, schema)
				}
			}
			properties, _ := schema["properties"].(map[string]any)
			for _, name := range []string{"mode", "name", "id"} {
				if _, ok := properties[name]; !ok {
					t.Fatalf("expected merged property %q, got %v", name, properties)
				}
			}
			if required, _ := schema["required"].([]any); len(required) != 1 || required[0] != "mode" {
				t.Fatalf("required = %v, want [mode] (intersection of the alternatives)", schema["required"])
			}
			if len(params.OneOf) != 2 {
				t.Fatalf("caller-owned schema was mutated: oneOf has %d branches", len(params.OneOf))
			}
		})

		t.Run(path+"/claude_alias_root_anyOf_flattened", func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
				Key:    "my-claude-profile",
				Config: &schemas.AliasConfig{ModelID: "45xnvsodrshx", ModelFamily: new(schemas.ModelFamilyAnthropic)},
			})
			params := mustToolSchema(t, strings.Replace(codexRootOneOfSchema, `"oneOf"`, `"anyOf"`, 1))
			schema, err := convertBedrockToolSchema(t, path, ctx, "my-claude-profile", params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, ok := schema["anyOf"]; ok {
				t.Fatalf("root anyOf must be flattened for an Anthropic-family alias, got %v", schema)
			}
		})

		t.Run(path+"/claude_root_allOf_flattened", func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			params := mustToolSchema(t, `{"type":"object","allOf":[{"type":"object","properties":{"left":{"type":"string"}},"required":["left"]},{"type":"object","properties":{"right":{"type":"string"}},"required":["right"]}]}`)
			schema, err := convertBedrockToolSchema(t, path, ctx, "us.anthropic.claude-opus-4-8", params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, ok := schema["allOf"]; ok {
				t.Fatalf("root allOf must be flattened for Claude, got %v", schema)
			}
			if required, _ := schema["required"].([]any); len(required) != 2 {
				t.Fatalf("required = %v, want [left right] (union of allOf members)", schema["required"])
			}
		})

		for _, model := range []string{"us.amazon.nova-pro-v1:0", "us.meta.llama4-maverick-17b-instruct-v1:0", "qwen.qwen3-32b-v1:0"} {
			t.Run(path+"/non_claude_keeps_root_oneOf/"+model, func(t *testing.T) {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				schema, err := convertBedrockToolSchema(t, path, ctx, model, mustToolSchema(t, codexRootOneOfSchema))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if branches, _ := schema["oneOf"].([]any); len(branches) != 2 {
					t.Fatalf("non-Claude model must receive the root oneOf unchanged, got %v", schema)
				}
			})
		}

		t.Run(path+"/claude_too_complex_is_bad_request", func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, err := convertBedrockToolSchema(t, path, ctx, "global.anthropic.claude-sonnet-4-6", tooManyBranchesSchema(t))
			if err == nil || !providerUtils.IsInvalidRequestError(err) {
				t.Fatalf("expected a caller-facing invalid request error, got %v", err)
			}
		})
	}
}

// BenchmarkConverseToolSchemaClaude measures Claude Chat conversion with a plain
// object tool schema (the common case) and with a root oneOf that gets flattened.
func BenchmarkConverseToolSchemaClaude(b *testing.B) {
	for _, bc := range []struct {
		name string
		raw  string
	}{
		{"plain_object", `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`},
		{"root_oneOf", codexRootOneOfSchema},
	} {
		b.Run(bc.name, func(b *testing.B) {
			params := mustToolSchema(b, bc.raw)
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				if _, err := ToBedrockChatCompletionRequest(ctx, bedrockChatToolSchemaRequest("global.anthropic.claude-sonnet-4-6", params)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// converseToolChoiceField decodes additionalModelRequestFields.tool_choice, whatever Go type carries it.
func converseToolChoiceField(t *testing.T, req *BedrockConverseRequest) map[string]any {
	t.Helper()
	v, ok := req.AdditionalModelRequestFields.Get("tool_choice")
	if !ok {
		return nil
	}
	raw, err := providerUtils.MarshalSorted(v)
	if err != nil {
		t.Fatalf("marshal tool_choice: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode tool_choice %s: %v", raw, err)
	}
	return out
}

func parallelChatTools(names ...string) []schemas.ChatTool {
	tools := make([]schemas.ChatTool, 0, len(names))
	for _, n := range names {
		tools = append(tools, schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: n}})
	}
	return tools
}

// parallel_tool_calls: false reaches Claude on Converse only as a typeless
// additionalModelRequestFields.tool_choice.disable_parallel_tool_use next to
// toolConfig.toolChoice; a type there conflicts with the typed choice (ValidationException).
func TestToBedrockChatCompletionRequest_ParallelToolCalls(t *testing.T) {
	str := func(s schemas.ChatToolChoiceType) *schemas.ChatToolChoice {
		return &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr(string(s))}
	}
	bash := schemas.ChatTool{Type: schemas.ChatToolType("bash_20250124"), Name: "bash"}
	computer := schemas.ChatTool{Type: schemas.ChatToolType("computer_20251124"), Name: "computer", DisplayWidthPx: schemas.Ptr(1280)}

	tests := []struct {
		name      string
		model     string
		tools     []schemas.ChatTool
		choice    *schemas.ChatToolChoice
		parallel  *bool
		wantTyped string // "auto", "any", "tool" or "" for no typed toolChoice
		wantField map[string]any
	}{
		{"false with no tool choice adds typed auto", "global.anthropic.claude-sonnet-4-6", parallelChatTools("get_weather", "get_time"), nil, schemas.Ptr(false),
			"auto", map[string]any{"disable_parallel_tool_use": true}},
		{"false with auto", "global.anthropic.claude-sonnet-4-6", parallelChatTools("get_weather"), str(schemas.ChatToolChoiceTypeAuto), schemas.Ptr(false),
			"auto", map[string]any{"disable_parallel_tool_use": true}},
		{"false with required keeps typed any", "global.anthropic.claude-sonnet-4-6", parallelChatTools("get_weather"), str(schemas.ChatToolChoiceTypeRequired), schemas.Ptr(false),
			"any", map[string]any{"disable_parallel_tool_use": true}},
		{"false with none sends nothing", "global.anthropic.claude-sonnet-4-6", parallelChatTools("get_weather"), str(schemas.ChatToolChoiceTypeNone), schemas.Ptr(false),
			"", nil},
		{"true sends nothing", "global.anthropic.claude-sonnet-4-6", parallelChatTools("get_weather"), nil, schemas.Ptr(true),
			"", nil},
		{"non-Anthropic model sends nothing", "amazon.nova-pro-v1:0", parallelChatTools("get_weather"), nil, schemas.Ptr(false),
			"", nil},
		{"server tools only carry the type themselves", "global.anthropic.claude-sonnet-4-6", []schemas.ChatTool{bash}, nil, schemas.Ptr(false),
			"", map[string]any{"type": "auto", "disable_parallel_tool_use": true}},
		{"pinned server tool carries the flag in the tunneled choice", "global.anthropic.claude-sonnet-4-6", []schemas.ChatTool{computer},
			&schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{Type: schemas.ChatToolChoiceTypeFunction, Function: &schemas.ChatToolChoiceFunction{Name: "computer"}}},
			schemas.Ptr(false), "", map[string]any{"type": "tool", "name": "computer", "disable_parallel_tool_use": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req := &schemas.BifrostChatRequest{
				Provider: schemas.Bedrock,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather in Tokyo, Paris and Lima?")}}},
				Params:   &schemas.ChatParameters{Tools: tt.tools, ToolChoice: tt.choice, ParallelToolCalls: tt.parallel},
			}
			out, err := ToBedrockChatCompletionRequest(ctx, req)
			if err != nil {
				t.Fatalf("ToBedrockChatCompletionRequest failed: %v", err)
			}
			assertConverseParallelToolUse(t, out, tt.wantTyped, tt.wantField)
		})
	}
}

func TestToBedrockResponsesRequest_ParallelToolCalls(t *testing.T) {
	str := func(s schemas.ResponsesToolChoiceType) *schemas.ResponsesToolChoice {
		return &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr(string(s))}
	}
	tests := []struct {
		name      string
		model     string
		choice    *schemas.ResponsesToolChoice
		parallel  *bool
		wantTyped string
		wantField map[string]any
	}{
		{"false with no tool choice adds typed auto", "global.anthropic.claude-sonnet-4-6", nil, schemas.Ptr(false), "auto", map[string]any{"disable_parallel_tool_use": true}},
		{"false with required keeps typed any", "global.anthropic.claude-sonnet-4-6", str(schemas.ResponsesToolChoiceTypeRequired), schemas.Ptr(false), "any", map[string]any{"disable_parallel_tool_use": true}},
		{"false with none sends nothing", "global.anthropic.claude-sonnet-4-6", str(schemas.ResponsesToolChoiceTypeNone), schemas.Ptr(false), "", nil},
		{"true sends nothing", "global.anthropic.claude-sonnet-4-6", nil, schemas.Ptr(true), "", nil},
		{"non-Anthropic model sends nothing", "amazon.nova-pro-v1:0", nil, schemas.Ptr(false), "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			tool := func(name string) schemas.ResponsesTool {
				return schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(name), ResponsesToolFunction: &schemas.ResponsesToolFunction{}}
			}
			req := &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Tokyo, Paris and Lima?")},
				}},
				Params: &schemas.ResponsesParameters{
					Tools:             []schemas.ResponsesTool{tool("get_weather"), tool("get_time")},
					ToolChoice:        tt.choice,
					ParallelToolCalls: tt.parallel,
				},
			}
			out, err := ToBedrockResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
			}
			assertConverseParallelToolUse(t, out, tt.wantTyped, tt.wantField)
		})
	}
}

// With no declared tools, Converse needs tools rebuilt from the history's tool calls;
// parallel_tool_calls=false must still cover those, so it is applied after the rebuild.
func TestConverseParallelToolCalls_ToolsRebuiltFromHistory(t *testing.T) {
	const model = "global.anthropic.claude-sonnet-4-6"
	text := func(s string) *schemas.ChatMessageContent {
		return &schemas.ChatMessageContent{ContentStr: schemas.Ptr(s)}
	}

	t.Run("chat", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    model,
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: text("Weather in Tokyo?")},
				{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID:       schemas.Ptr("call_1"),
					Type:     schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Tokyo"}`},
				}}}},
				{Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_1")}, Content: text("sunny")},
				{Role: schemas.ChatMessageRoleUser, Content: text("Now Paris and Lima.")},
			},
			Params: &schemas.ChatParameters{ParallelToolCalls: schemas.Ptr(false)},
		}
		out, err := ToBedrockChatCompletionRequest(ctx, req)
		if err != nil {
			t.Fatalf("ToBedrockChatCompletionRequest failed: %v", err)
		}
		if out.ToolConfig == nil || len(out.ToolConfig.Tools) == 0 {
			t.Fatalf("expected tools rebuilt from history")
		}
		assertConverseParallelToolUse(t, out, "auto", map[string]any{"disable_parallel_tool_use": true})
	})

	t.Run("responses", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		user := func(s string) schemas.ResponsesMessage {
			return schemas.ResponsesMessage{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(s)}}
		}
		req := &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    model,
			Input: []schemas.ResponsesMessage{
				user("Weather in Tokyo?"),
				{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("get_weather"), Arguments: schemas.Ptr(`{"city":"Tokyo"}`),
				}},
				{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput), ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("call_1"), Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("sunny")},
				}},
				user("Now Paris and Lima."),
			},
			Params: &schemas.ResponsesParameters{ParallelToolCalls: schemas.Ptr(false)},
		}
		out, err := ToBedrockResponsesRequest(ctx, req)
		if err != nil {
			t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
		}
		if out.ToolConfig == nil || len(out.ToolConfig.Tools) == 0 {
			t.Fatalf("expected tools rebuilt from history")
		}
		assertConverseParallelToolUse(t, out, "auto", map[string]any{"disable_parallel_tool_use": true})
	})
}

// A /bedrock Converse caller spells the flag the way AWS accepts it; it must become
// ParallelToolCalls (so InvokeModel and other providers see it) and go out exactly once.
func TestBedrockConverseIngress_ParallelToolUseRoundTrip(t *testing.T) {
	body := `{
		"messages": [{"role": "user", "content": [{"text": "Weather in Tokyo, Paris and Lima?"}]}],
		"toolConfig": {
			"tools": [{"toolSpec": {"name": "get_weather", "inputSchema": {"json": {"type": "object"}}}}],
			"toolChoice": {"auto": {}}
		},
		"additionalModelRequestFields": {"tool_choice": {"disable_parallel_tool_use": true}, "top_k": 5}
	}`
	var converseReq BedrockConverseRequest
	if err := json.Unmarshal([]byte(body), &converseReq); err != nil {
		t.Fatalf("decode: %v", err)
	}
	converseReq.ModelID = "bedrock/global.anthropic.claude-sonnet-4-6"

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
	if err != nil {
		t.Fatalf("ingress: %v", err)
	}
	if p := bifrostReq.Params.ParallelToolCalls; p == nil || *p {
		t.Fatalf("ParallelToolCalls = %v, want false", p)
	}
	if fwd, ok := bifrostReq.Params.ExtraParams["additionalModelRequestFieldPaths"].(*schemas.OrderedMap); !ok {
		t.Fatalf("expected top_k to keep forwarding, got %#v", bifrostReq.Params.ExtraParams["additionalModelRequestFieldPaths"])
	} else if _, ok := fwd.Get("tool_choice"); ok {
		t.Errorf("tool_choice must be consumed, not forwarded verbatim next to the re-synthesized one")
	}

	bifrostReq.Provider = schemas.Bedrock
	out, err := ToBedrockResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("egress: %v", err)
	}
	assertConverseParallelToolUse(t, out, "auto", map[string]any{"disable_parallel_tool_use": true})
	if v, ok := out.AdditionalModelRequestFields.Get("top_k"); !ok || fmt.Sprint(v) != "5" {
		t.Errorf("top_k = %v, want 5", v)
	}
}

func assertConverseParallelToolUse(t *testing.T, out *BedrockConverseRequest, wantTyped string, wantField map[string]any) {
	t.Helper()
	gotTyped := ""
	if out.ToolConfig != nil && out.ToolConfig.ToolChoice != nil {
		switch tc := out.ToolConfig.ToolChoice; {
		case tc.Auto != nil:
			gotTyped = "auto"
		case tc.Any != nil:
			gotTyped = "any"
		case tc.Tool != nil:
			gotTyped = "tool"
		}
	}
	if gotTyped != wantTyped {
		t.Errorf("toolConfig.toolChoice = %q, want %q", gotTyped, wantTyped)
	}
	gotField := converseToolChoiceField(t, out)
	if (gotField == nil) != (wantField == nil) || fmt.Sprint(gotField) != fmt.Sprint(wantField) {
		t.Errorf("additionalModelRequestFields.tool_choice = %v, want %v", gotField, wantField)
	}
}

// BenchmarkConverseParallelToolCalls measures Claude Converse conversion with
// parallel_tool_calls unset (the common case) and false, on Chat and Responses.
func BenchmarkConverseParallelToolCalls(b *testing.B) {
	for _, bc := range []struct {
		name     string
		parallel *bool
	}{
		{"unset", nil},
		{"false", schemas.Ptr(false)},
	} {
		b.Run("chat_"+bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				req := &schemas.BifrostChatRequest{
					Provider: schemas.Bedrock,
					Model:    "global.anthropic.claude-sonnet-4-6",
					Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather in Tokyo?")}}},
					Params:   &schemas.ChatParameters{Tools: parallelChatTools("get_weather", "get_time"), ParallelToolCalls: bc.parallel},
				}
				if _, err := ToBedrockChatCompletionRequest(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("responses_"+bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				req := &schemas.BifrostResponsesRequest{
					Provider: schemas.Bedrock,
					Model:    "global.anthropic.claude-sonnet-4-6",
					Input: []schemas.ResponsesMessage{{
						Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Tokyo?")},
					}},
					Params: &schemas.ResponsesParameters{
						Tools:             []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}}},
						ParallelToolCalls: bc.parallel,
					},
				}
				if _, err := ToBedrockResponsesRequest(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// converseToolStrict returns toolConfig.tools[i].toolSpec.strict as it goes on
// the Converse wire, so a field the struct cannot carry fails the assertion.
func converseToolStrict(t *testing.T, out *BedrockConverseRequest, i int) (value, present bool) {
	t.Helper()
	if out == nil || out.ToolConfig == nil {
		t.Fatalf("expected a toolConfig, got %+v", out)
	}
	raw, err := json.Marshal(out.ToolConfig)
	if err != nil {
		t.Fatalf("marshal toolConfig: %v", err)
	}
	res := providerUtils.GetJSONField(raw, fmt.Sprintf("tools.%d.toolSpec.strict", i))
	return res.Bool(), res.Exists()
}

// strictWeatherSchema is a schema Bedrock accepts under strict tool use:
// every property required and additionalProperties false.
func strictWeatherSchema() *schemas.ToolFunctionParameters {
	return &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("city", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
		),
		Required:             []string{"city"},
		AdditionalProperties: &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: schemas.Ptr(false)},
	}
}

// TestToBedrockChatCompletionRequest_ForwardsToolStrict covers /v1/chat/completions
// to Claude on Bedrock: Converse ToolSpecification has a strict member ("Flag to
// enable structured output enforcement on a tool usage response",
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolSpecification.html),
// so function.strict must reach toolSpec.strict instead of being dropped.
// strict:false is the default and stays off the wire, so a client that never
// asked for enforcement (Codex flags every tool false) sends the pre-strict
// bytes even to a profile without structured outputs, such as in.anthropic
// Claude Haiku 4.5; an explicit true is forwarded for Bedrock to accept or 400.
func TestToBedrockChatCompletionRequest_ForwardsToolStrict(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		strict      *bool
		wantPresent bool
		wantValue   bool
	}{
		{"claude strict true", "global.anthropic.claude-sonnet-4-6", schemas.Ptr(true), true, true},
		{"claude strict false omitted", "global.anthropic.claude-sonnet-4-6", schemas.Ptr(false), false, false},
		{"claude strict unset", "global.anthropic.claude-sonnet-4-6", nil, false, false},
		{"in.anthropic haiku 4.5 strict false omitted", "in.anthropic.claude-haiku-4-5-20251001-v1:0", schemas.Ptr(false), false, false},
		{"in.anthropic haiku 4.5 strict true forwarded", "in.anthropic.claude-haiku-4-5-20251001-v1:0", schemas.Ptr(true), true, true},
		{"non-Anthropic model drops strict", "meta.llama3-3-70b-instruct-v1:0", schemas.Ptr(true), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req := &schemas.BifrostChatRequest{
				Provider: schemas.Bedrock,
				Model:    tt.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")}}},
				Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:       "get_weather",
						Parameters: strictWeatherSchema(),
						Strict:     tt.strict,
					},
				}}},
			}
			out, err := ToBedrockChatCompletionRequest(ctx, req)
			if err != nil {
				t.Fatalf("ToBedrockChatCompletionRequest failed: %v", err)
			}
			got, present := converseToolStrict(t, out, 0)
			if present != tt.wantPresent || got != tt.wantValue {
				t.Errorf("toolSpec.strict = (present=%v, value=%v), want (present=%v, value=%v)", present, got, tt.wantPresent, tt.wantValue)
			}
		})
	}
}

// TestToBedrockResponsesRequest_ForwardsToolStrict is the /v1/responses (and Go
// SDK) twin of TestToBedrockChatCompletionRequest_ForwardsToolStrict.
func TestToBedrockResponsesRequest_ForwardsToolStrict(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		strict      *bool
		wantPresent bool
		wantValue   bool
	}{
		{"claude strict true", "global.anthropic.claude-sonnet-4-6", schemas.Ptr(true), true, true},
		{"claude strict false omitted", "global.anthropic.claude-sonnet-4-6", schemas.Ptr(false), false, false},
		{"claude strict unset", "global.anthropic.claude-sonnet-4-6", nil, false, false},
		{"in.anthropic haiku 4.5 strict false omitted", "in.anthropic.claude-haiku-4-5-20251001-v1:0", schemas.Ptr(false), false, false},
		{"in.anthropic haiku 4.5 strict true forwarded", "in.anthropic.claude-haiku-4-5-20251001-v1:0", schemas.Ptr(true), true, true},
		{"non-Anthropic model drops strict", "meta.llama3-3-70b-instruct-v1:0", schemas.Ptr(true), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req := &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")},
				}},
				Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
					Type: schemas.ResponsesToolTypeFunction,
					Name: schemas.Ptr("get_weather"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: strictWeatherSchema(),
						Strict:     tt.strict,
					},
				}}},
			}
			out, err := ToBedrockResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
			}
			got, present := converseToolStrict(t, out, 0)
			if present != tt.wantPresent || got != tt.wantValue {
				t.Errorf("toolSpec.strict = (present=%v, value=%v), want (present=%v, value=%v)", present, got, tt.wantPresent, tt.wantValue)
			}
		})
	}
}

// TestBedrockConverseIngress_ToolStrictRoundTrip covers the native
// /bedrock/model/{modelId}/converse route: AWS's own strict tool example must
// keep toolSpec.strict through ingress decode, the neutral request, and egress.
func TestBedrockConverseIngress_ToolStrictRoundTrip(t *testing.T) {
	body := `{
		"messages": [{"role": "user", "content": [{"text": "What's the weather like in New York?"}]}],
		"toolConfig": {
			"tools": [{"toolSpec": {
				"name": "get_weather",
				"description": "Get the current weather for a specified location",
				"strict": true,
				"inputSchema": {"json": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"], "additionalProperties": false}}
			}}]
		}
	}`
	var converseReq BedrockConverseRequest
	if err := json.Unmarshal([]byte(body), &converseReq); err != nil {
		t.Fatalf("decode: %v", err)
	}
	converseReq.ModelID = "bedrock/global.anthropic.claude-sonnet-4-6"

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
	if err != nil {
		t.Fatalf("ingress: %v", err)
	}
	if len(bifrostReq.Params.Tools) != 1 || bifrostReq.Params.Tools[0].ResponsesToolFunction == nil {
		t.Fatalf("expected one function tool, got %+v", bifrostReq.Params.Tools)
	}
	if s := bifrostReq.Params.Tools[0].ResponsesToolFunction.Strict; s == nil || !*s {
		t.Errorf("ingress ResponsesToolFunction.Strict = %v, want true", s)
	}

	bifrostReq.Provider = schemas.Bedrock
	out, err := ToBedrockResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("egress: %v", err)
	}
	if got, present := converseToolStrict(t, out, 0); !present || !got {
		t.Errorf("egress toolSpec.strict = (present=%v, value=%v), want (present=true, value=true)", present, got)
	}
}

// BenchmarkConverseToolStrict measures the chat egress with and without tool
// strict flags. Forwarding reuses the inbound pointer and resolves the model
// family once per request, so a flagged request costs at most one extra
// allocation however many tools carry the flag.
func BenchmarkConverseToolStrict(b *testing.B) {
	for _, bc := range []struct {
		name   string
		strict *bool
		tools  int
	}{
		{"strict_unset", nil, 1},
		{"strict_true", schemas.Ptr(true), 1},
		{"strict_unset_30_tools", nil, 30},
		{"strict_false_30_tools", schemas.Ptr(false), 30},
	} {
		b.Run(bc.name, func(b *testing.B) {
			params := &schemas.ChatParameters{}
			for i := range bc.tools {
				params.Tools = append(params.Tools, schemas.ChatTool{
					Type:     schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{Name: fmt.Sprintf("tool_%d", i), Parameters: strictWeatherSchema(), Strict: bc.strict},
				})
			}
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				req := &schemas.BifrostChatRequest{
					Provider: schemas.Bedrock,
					Model:    "global.anthropic.claude-sonnet-4-6",
					Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")}}},
					Params:   params,
				}
				if _, err := ToBedrockChatCompletionRequest(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
