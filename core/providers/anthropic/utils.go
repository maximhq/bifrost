package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// anthropicToolTypePrefixToFeature maps Anthropic server-tool type prefixes
// to the corresponding ProviderFeatureSupport flag. Mirrors the structure of
// betaHeaderPrefixToFeature (defined later in this file) so tool-type gating
// and beta-header gating share the same shape.
//
// Prefix-based so future version bumps (e.g. web_search_20261231) flow
// through without a code change. Exact-match types (currently just
// "mcp_toolset") are handled separately.
var anthropicToolTypePrefixToFeature = map[string]func(ProviderFeatureSupport) bool{
	"web_search_":       func(f ProviderFeatureSupport) bool { return f.WebSearch },
	"web_fetch_":        func(f ProviderFeatureSupport) bool { return f.WebFetch },
	"code_execution_":   func(f ProviderFeatureSupport) bool { return f.CodeExecution },
	"computer_":         func(f ProviderFeatureSupport) bool { return f.ComputerUse },
	"bash_":             func(f ProviderFeatureSupport) bool { return f.Bash },
	"memory_":           func(f ProviderFeatureSupport) bool { return f.Memory },
	"text_editor_":      func(f ProviderFeatureSupport) bool { return f.TextEditor },
	"tool_search_tool_": func(f ProviderFeatureSupport) bool { return f.ToolSearch },
}

// isAnthropicServerToolSupported returns whether the given Anthropic server-tool
// type string is supported by the provider's ProviderFeatureSupport. Unknown
// types return true (forward-compat: let the provider reject if truly invalid
// rather than Bifrost dropping a tool Anthropic has just added).
func isAnthropicServerToolSupported(toolType string, features ProviderFeatureSupport) bool {
	// Exact-match types first.
	if toolType == "mcp_toolset" {
		return features.MCP
	}
	// Prefix match for versioned types.
	for prefix, check := range anthropicToolTypePrefixToFeature {
		if strings.HasPrefix(toolType, prefix) {
			return check(features)
		}
	}
	return true
}

// ValidateChatToolsForProvider is the chat-path mirror of
// ValidateToolsForProvider. It partitions []schemas.ChatTool into a keep-set
// (function/custom tools + server tools supported on the target provider)
// and a dropped-set (server-tool Type strings the provider doesn't support
// per ProviderFeatures).
//
// Does NOT mutate its input. Callers decide the policy (silent strip vs
// fail-fast). The Bedrock ChatCompletion path uses silent strip so the
// request still reaches the provider without the unsupported tool; the model
// responds with a prose completion instead of tool use.
//
// Unknown providers keep all tools (safe default for custom providers),
// matching ValidateToolsForProvider.
func ValidateChatToolsForProvider(tools []schemas.ChatTool, provider schemas.ModelProvider) (keep []schemas.ChatTool, dropped []string) {
	features, ok := ProviderFeatures[provider]
	if !ok {
		return tools, nil
	}
	for _, tool := range tools {
		// Function/custom tools are universal — always keep.
		if tool.Function != nil || tool.Custom != nil {
			keep = append(keep, tool)
			continue
		}
		t := string(tool.Type)
		if isAnthropicServerToolSupported(t, features) {
			keep = append(keep, tool)
		} else {
			dropped = append(dropped, t)
		}
	}
	return keep, dropped
}

// ValidateToolsForProvider checks if all tools in the request are supported by the given provider.
// Returns an error for the first unsupported tool found.
func ValidateToolsForProvider(tools []schemas.ResponsesTool, provider schemas.ModelProvider) error {
	features, ok := ProviderFeatures[provider]
	if !ok {
		// Unknown provider — allow all tools (safe default for custom providers)
		return nil
	}

	for _, tool := range tools {
		switch tool.Type {
		case schemas.ResponsesToolTypeWebSearch, schemas.ResponsesToolTypeWebSearchPreview:
			if !features.WebSearch {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeWebFetch:
			if !features.WebFetch {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeCodeInterpreter:
			if !features.CodeExecution {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeComputerUsePreview:
			if !features.ComputerUse {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeMCP:
			if !features.MCP {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeLocalShell:
			if !features.Bash {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeMemory:
			if !features.Memory {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeToolSearch:
			if !features.ToolSearch {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeFileSearch:
			if !features.FileSearch {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
		case schemas.ResponsesToolTypeImageGeneration:
			if !features.ImageGeneration {
				return fmt.Errorf("tool type '%s' is not supported by provider '%s'", tool.Type, provider)
			}
			// ResponsesToolTypeFunction, ResponsesToolTypeCustom, etc. are always allowed
		}
	}
	return nil
}

var (
	// Maps provider-specific finish reasons to Bifrost format
	anthropicFinishReasonToBifrost = map[AnthropicStopReason]string{
		AnthropicStopReasonEndTurn:      "stop",
		AnthropicStopReasonMaxTokens:    "length",
		AnthropicStopReasonStopSequence: "stop",
		AnthropicStopReasonToolUse:      "tool_calls",
		AnthropicStopReasonCompaction:   "compaction",
	}

	// Maps Bifrost finish reasons to provider-specific format
	bifrostToAnthropicFinishReason = map[string]AnthropicStopReason{
		"stop":       AnthropicStopReasonEndTurn, // canonical default
		"length":     AnthropicStopReasonMaxTokens,
		"tool_calls": AnthropicStopReasonToolUse,
		"compaction": AnthropicStopReasonCompaction,
	}
)

// stripUnsupportedAnthropicFields removes request-level and tool-level fields
// that the target Anthropic-family provider does not support, according to the
// ProviderFeatures map (types.go). Tool-type validation (fail-closed) is handled
// separately by ValidateToolsForProvider; this helper handles request-level
// fields (strip silently, since they're additive enhancements).
//
// Mutates req in place. Safe to call multiple times.
func stripUnsupportedAnthropicFields(req *AnthropicMessageRequest, provider schemas.ModelProvider, model string) {
	if req == nil {
		return
	}
	features, ok := ProviderFeatures[provider]
	if !ok {
		// Unknown provider — safe default: don't strip anything.
		return
	}

	// Request-level fields gated by ProviderFeatures flags.
	if req.Container != nil {
		// Skills form (object with skills[]) is beta-gated; bare string id is universal.
		// Intent signal: non-empty skills = caller explicitly wants skills; empty
		// skills:[] = likely caller oversight we can silently correct.
		hasSkills := req.Container.ContainerObject != nil && len(req.Container.ContainerObject.Skills) > 0
		// Strip an explicit empty or non-empty skills array on Skills=false
		// providers. omitempty already handles this at serialize time for empty
		// arrays, but we clear it explicitly so hasSkills-based decisions below
		// and raw-path parity both stay correct.
		if !features.Skills && req.Container.ContainerObject != nil && req.Container.ContainerObject.Skills != nil {
			req.Container.ContainerObject.Skills = nil
		}
		switch {
		case hasSkills && !features.Skills:
			// Caller wanted non-empty skills but provider doesn't support them.
			req.Container = nil
		case !hasSkills && !features.ContainerBasic:
			req.Container = nil
		}
	}
	if len(req.MCPServers) > 0 && !features.MCP {
		req.MCPServers = nil
	}
	// Speed is both provider-gated (FastMode flag) and model-gated
	// (Opus 4.6 only per SupportsFastMode). Strip if either gate fails —
	// Anthropic's API rejects speed:"fast" on non-Opus-4.6 models with a 400.
	if req.Speed != nil && (!features.FastMode || !SupportsFastMode(model)) {
		req.Speed = nil
	}
	if req.OutputConfig != nil && req.OutputConfig.TaskBudget != nil && !features.TaskBudgets {
		req.OutputConfig.TaskBudget = nil
		// Clean up an empty OutputConfig so it doesn't serialize as {}
		if req.OutputConfig.Format == nil && req.OutputConfig.Effort == nil {
			req.OutputConfig = nil
		}
	}
	if req.InferenceGeo != nil && !features.InferenceGeo {
		req.InferenceGeo = nil
	}
	// cache_control.scope — strip on providers without PromptCachingScope
	// support at every slot scope can live: top-level request, tools, system
	// blocks, and message content blocks. Vertex additionally uses the
	// marshal-time SetStripCacheControlScope mechanism (vertex/utils.go:104,
	// types.go MarshalJSON); after this strip runs, that marshal-time pass
	// becomes a safe no-op for Vertex (nothing left to strip).
	if !features.PromptCachingScope {
		// Top-level.
		if req.CacheControl != nil && req.CacheControl.Scope != nil {
			req.CacheControl.Scope = nil
			// If scope was the only meaningful field, drop the whole CacheControl
			// so we don't serialize an empty object.
			if req.CacheControl.TTL == nil && req.CacheControl.Type == "" {
				req.CacheControl = nil
			}
		}
		// Per-tool cache_control.scope.
		for i := range req.Tools {
			if req.Tools[i].CacheControl != nil && req.Tools[i].CacheControl.Scope != nil {
				req.Tools[i].CacheControl.Scope = nil
				// Drop the parent if scope was the only meaningful field.
				if req.Tools[i].CacheControl.TTL == nil && req.Tools[i].CacheControl.Type == "" {
					req.Tools[i].CacheControl = nil
				}
			}
		}
		// System block scopes.
		if req.System != nil {
			for i := range req.System.ContentBlocks {
				if req.System.ContentBlocks[i].CacheControl != nil && req.System.ContentBlocks[i].CacheControl.Scope != nil {
					req.System.ContentBlocks[i].CacheControl.Scope = nil
					if req.System.ContentBlocks[i].CacheControl.TTL == nil && req.System.ContentBlocks[i].CacheControl.Type == "" {
						req.System.ContentBlocks[i].CacheControl = nil
					}
				}
			}
		}
		// Message block scopes.
		for mi := range req.Messages {
			for ci := range req.Messages[mi].Content.ContentBlocks {
				cc := req.Messages[mi].Content.ContentBlocks[ci].CacheControl
				if cc != nil && cc.Scope != nil {
					cc.Scope = nil
					if cc.TTL == nil && cc.Type == "" {
						req.Messages[mi].Content.ContentBlocks[ci].CacheControl = nil
					}
				}
			}
		}
	}
	if req.ContextManagement != nil {
		// Gate edits by their type — compaction vs context-editing flags.
		kept := make([]ContextManagementEdit, 0, len(req.ContextManagement.Edits))
		for _, edit := range req.ContextManagement.Edits {
			switch edit.Type {
			case ContextManagementEditTypeCompact:
				if features.Compaction {
					kept = append(kept, edit)
				}
			case ContextManagementEditTypeClearToolUses, ContextManagementEditTypeClearThinking:
				if features.ContextEditing {
					kept = append(kept, edit)
				}
			default:
				// Unknown edit type — keep and let upstream reject.
				kept = append(kept, edit)
			}
		}
		if len(kept) == 0 {
			req.ContextManagement = nil
		} else {
			req.ContextManagement.Edits = kept
		}
	}

	// Tool-level flags — strip per-tool without dropping the tool itself.
	for i := range req.Tools {
		tool := &req.Tools[i]
		if tool.DeferLoading != nil && !features.AdvancedToolUse {
			tool.DeferLoading = nil
		}
		if len(tool.AllowedCallers) > 0 && !features.AdvancedToolUse {
			tool.AllowedCallers = nil
		}
		// InputExamples has its own feature flag (InputExamples) because
		// Bedrock supports the tool-examples-2025-10-29 header standalone —
		// without the full advanced-tool-use-2025-11-20 bundle. On Anthropic
		// and Azure, the bundle flag (AdvancedToolUse) is also set, so either
		// gate would work there.
		if len(tool.InputExamples) > 0 && !features.InputExamples {
			tool.InputExamples = nil
		}
		if tool.EagerInputStreaming != nil && !features.EagerInputStreaming {
			tool.EagerInputStreaming = nil
		}
		if tool.Strict != nil && !features.StructuredOutputs {
			tool.Strict = nil
		}
	}
}

// StripUnsupportedFieldsFromRawBody is the raw-JSON equivalent of
// StripUnsupportedAnthropicFields. It mutates the request body bytes using
// sjson/gjson (preserving key order for prompt caching) so the raw-body
// passthrough path has behavioural parity with the typed conversion path.
//
// Scope: every field the typed helper handles.
//   - top-level: speed (provider + model gated), container (.skills gated by
//     features.Skills, bare string by features.ContainerBasic), mcp_servers,
//     inference_geo, cache_control.scope, output_config.task_budget,
//     context_management.edits[] (gated per edit type).
//   - nested: tool.CacheControl.Scope, system block scopes, message block
//     scopes (all stripped when !features.PromptCachingScope).
//   - per-tool: defer_loading, allowed_callers (AdvancedToolUse bundle),
//     input_examples (narrow InputExamples flag), eager_input_streaming
//     (EagerInputStreaming), strict (StructuredOutputs).
//
// Unknown providers: safe default — no stripping (parity with the typed helper).
// Unknown edit types in context_management: left in place for the provider
// to reject (parity with the typed helper).
func StripUnsupportedFieldsFromRawBody(jsonBody []byte, provider schemas.ModelProvider, model string) ([]byte, error) {
	if len(jsonBody) == 0 {
		return jsonBody, nil
	}
	features, ok := ProviderFeatures[provider]
	if !ok {
		return jsonBody, nil
	}

	// Fall back to body-embedded model when caller didn't pass one.
	if model == "" {
		if modelResult := providerUtils.GetJSONField(jsonBody, "model"); modelResult.Exists() {
			model = modelResult.String()
		}
	}

	var err error

	// speed — provider AND model gate
	if providerUtils.JSONFieldExists(jsonBody, "speed") {
		if !features.FastMode || !SupportsFastMode(model) {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "speed")
			if err != nil {
				return nil, fmt.Errorf("strip raw speed: %w", err)
			}
		}
	}

	// inference_geo
	if !features.InferenceGeo && providerUtils.JSONFieldExists(jsonBody, "inference_geo") {
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "inference_geo")
		if err != nil {
			return nil, fmt.Errorf("strip raw inference_geo: %w", err)
		}
	}

	// mcp_servers
	if !features.MCP && providerUtils.JSONFieldExists(jsonBody, "mcp_servers") {
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "mcp_servers")
		if err != nil {
			return nil, fmt.Errorf("strip raw mcp_servers: %w", err)
		}
	}

	// container — two variants: bare string id (ContainerBasic), or object
	// {id, skills[]} where skills require Skills flag.
	// Distinguishes three states: no skills field (bare form), skills:[] (empty
	// array — caller oversight, silently strip), skills:[…] (non-empty — caller
	// explicitly wants skills). Mirrors the typed path's hybrid decision.
	if containerResult := providerUtils.GetJSONField(jsonBody, "container"); containerResult.Exists() {
		hasSkillsField, hasNonEmptySkills := false, false
		if containerResult.IsObject() {
			if skills := containerResult.Get("skills"); skills.Exists() {
				hasSkillsField = true
				if skills.IsArray() && len(skills.Array()) > 0 {
					hasNonEmptySkills = true
				}
			}
		}
		// Always strip the skills key on Skills=false providers — critical on
		// the raw path since bytes flow directly to the provider and an
		// explicit empty array would still be rejected as unknown field.
		if !features.Skills && hasSkillsField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "container.skills")
			if err != nil {
				return nil, fmt.Errorf("strip raw container.skills: %w", err)
			}
		}
		drop := false
		switch {
		case hasNonEmptySkills:
			drop = !features.Skills
		default:
			drop = !features.ContainerBasic
		}
		if drop {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "container")
			if err != nil {
				return nil, fmt.Errorf("strip raw container: %w", err)
			}
		}
	}

	// output_config.task_budget
	if !features.TaskBudgets && providerUtils.JSONFieldExists(jsonBody, "output_config.task_budget") {
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "output_config.task_budget")
		if err != nil {
			return nil, fmt.Errorf("strip raw output_config.task_budget: %w", err)
		}
		// Drop an empty parent so we don't serialize output_config:{} (matches
		// typed-path behavior at lines 129-134).
		if oc := providerUtils.GetJSONField(jsonBody, "output_config"); oc.IsObject() && len(oc.Map()) == 0 {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "output_config")
			if err != nil {
				return nil, fmt.Errorf("strip raw output_config: %w", err)
			}
		}
	}

	// top-level cache_control.scope
	if !features.PromptCachingScope && providerUtils.JSONFieldExists(jsonBody, "cache_control.scope") {
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "cache_control.scope")
		if err != nil {
			return nil, fmt.Errorf("strip raw cache_control.scope: %w", err)
		}
		// Drop an empty parent so we don't serialize cache_control:{} (matches
		// typed-path behavior at lines 147-153).
		if cc := providerUtils.GetJSONField(jsonBody, "cache_control"); cc.IsObject() && len(cc.Map()) == 0 {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "cache_control")
			if err != nil {
				return nil, fmt.Errorf("strip raw cache_control: %w", err)
			}
		}
	}

	// context_management — if the provider doesn't accept the field at all (e.g. Vertex),
	// drop it entirely. Otherwise gate per edit.type.
	if providerUtils.JSONFieldExists(jsonBody, "context_management") {
		if !features.ContextManagementField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "context_management")
			if err != nil {
				return nil, fmt.Errorf("strip raw context_management: %w", err)
			}
		} else if editsResult := providerUtils.GetJSONField(jsonBody, "context_management.edits"); editsResult.Exists() && editsResult.IsArray() {
			edits := editsResult.Array()
			// Collect indices to drop (iterate forwards, delete in reverse).
			dropIndices := []int{}
			for i, edit := range edits {
				editType := edit.Get("type").String()
				keep := true
				switch editType {
				case string(ContextManagementEditTypeCompact):
					keep = features.Compaction
				case string(ContextManagementEditTypeClearToolUses), string(ContextManagementEditTypeClearThinking):
					keep = features.ContextEditing
				}
				if !keep {
					dropIndices = append(dropIndices, i)
				}
			}
			if len(dropIndices) == len(edits) {
				// No edits to keep (either empty input or all unsupported) — drop the whole context_management.
				jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "context_management")
				if err != nil {
					return nil, fmt.Errorf("strip raw context_management: %w", err)
				}
			} else {
				for i := len(dropIndices) - 1; i >= 0; i-- {
					path := fmt.Sprintf("context_management.edits.%d", dropIndices[i])
					jsonBody, err = providerUtils.DeleteJSONField(jsonBody, path)
					if err != nil {
						return nil, fmt.Errorf("strip raw context_management.edits[%d]: %w", dropIndices[i], err)
					}
				}
			}
		}
	}

	// per-tool flags + nested scope
	if toolsResult := providerUtils.GetJSONField(jsonBody, "tools"); toolsResult.Exists() && toolsResult.IsArray() {
		for i := range toolsResult.Array() {
			base := fmt.Sprintf("tools.%d", i)
			if !features.AdvancedToolUse {
				if providerUtils.JSONFieldExists(jsonBody, base+".defer_loading") {
					jsonBody, err = providerUtils.DeleteJSONField(jsonBody, base+".defer_loading")
					if err != nil {
						return nil, fmt.Errorf("strip raw %s.defer_loading: %w", base, err)
					}
				}
				if providerUtils.JSONFieldExists(jsonBody, base+".allowed_callers") {
					jsonBody, err = providerUtils.DeleteJSONField(jsonBody, base+".allowed_callers")
					if err != nil {
						return nil, fmt.Errorf("strip raw %s.allowed_callers: %w", base, err)
					}
				}
			}
			if !features.InputExamples && providerUtils.JSONFieldExists(jsonBody, base+".input_examples") {
				jsonBody, err = providerUtils.DeleteJSONField(jsonBody, base+".input_examples")
				if err != nil {
					return nil, fmt.Errorf("strip raw %s.input_examples: %w", base, err)
				}
			}
			if !features.EagerInputStreaming && providerUtils.JSONFieldExists(jsonBody, base+".eager_input_streaming") {
				jsonBody, err = providerUtils.DeleteJSONField(jsonBody, base+".eager_input_streaming")
				if err != nil {
					return nil, fmt.Errorf("strip raw %s.eager_input_streaming: %w", base, err)
				}
			}
			if !features.StructuredOutputs && providerUtils.JSONFieldExists(jsonBody, base+".strict") {
				jsonBody, err = providerUtils.DeleteJSONField(jsonBody, base+".strict")
				if err != nil {
					return nil, fmt.Errorf("strip raw %s.strict: %w", base, err)
				}
			}
			if !features.PromptCachingScope && providerUtils.JSONFieldExists(jsonBody, base+".cache_control.scope") {
				jsonBody, err = providerUtils.DeleteJSONField(jsonBody, base+".cache_control.scope")
				if err != nil {
					return nil, fmt.Errorf("strip raw %s.cache_control.scope: %w", base, err)
				}
				// Drop the parent if cache_control is now an empty object, so
				// we don't forward a malformed `cache_control: {}` marker.
				if ccResult := providerUtils.GetJSONField(jsonBody, base+".cache_control"); ccResult.Exists() && ccResult.IsObject() && len(ccResult.Map()) == 0 {
					jsonBody, err = providerUtils.DeleteJSONField(jsonBody, base+".cache_control")
					if err != nil {
						return nil, fmt.Errorf("strip raw %s.cache_control empty parent: %w", base, err)
					}
				}
			}
		}
	}

	// Nested scope on system blocks (system can be a string OR array of blocks).
	if !features.PromptCachingScope {
		if systemResult := providerUtils.GetJSONField(jsonBody, "system"); systemResult.Exists() && systemResult.IsArray() {
			for i := range systemResult.Array() {
				path := fmt.Sprintf("system.%d.cache_control.scope", i)
				if providerUtils.JSONFieldExists(jsonBody, path) {
					jsonBody, err = providerUtils.DeleteJSONField(jsonBody, path)
					if err != nil {
						return nil, fmt.Errorf("strip raw system[%d].cache_control.scope: %w", i, err)
					}
					parentPath := fmt.Sprintf("system.%d.cache_control", i)
					if ccResult := providerUtils.GetJSONField(jsonBody, parentPath); ccResult.Exists() && ccResult.IsObject() && len(ccResult.Map()) == 0 {
						jsonBody, err = providerUtils.DeleteJSONField(jsonBody, parentPath)
						if err != nil {
							return nil, fmt.Errorf("strip raw system[%d].cache_control empty parent: %w", i, err)
						}
					}
				}
			}
		}
		// Nested scope on messages[].content[] blocks.
		if messagesResult := providerUtils.GetJSONField(jsonBody, "messages"); messagesResult.Exists() && messagesResult.IsArray() {
			messages := messagesResult.Array()
			for mi := range messages {
				contentResult := providerUtils.GetJSONField(jsonBody, fmt.Sprintf("messages.%d.content", mi))
				if !contentResult.Exists() || !contentResult.IsArray() {
					continue
				}
				for ci := range contentResult.Array() {
					path := fmt.Sprintf("messages.%d.content.%d.cache_control.scope", mi, ci)
					if providerUtils.JSONFieldExists(jsonBody, path) {
						jsonBody, err = providerUtils.DeleteJSONField(jsonBody, path)
						if err != nil {
							return nil, fmt.Errorf("strip raw messages[%d].content[%d].cache_control.scope: %w", mi, ci, err)
						}
						parentPath := fmt.Sprintf("messages.%d.content.%d.cache_control", mi, ci)
						if ccResult := providerUtils.GetJSONField(jsonBody, parentPath); ccResult.Exists() && ccResult.IsObject() && len(ccResult.Map()) == 0 {
							jsonBody, err = providerUtils.DeleteJSONField(jsonBody, parentPath)
							if err != nil {
								return nil, fmt.Errorf("strip raw messages[%d].content[%d].cache_control empty parent: %w", mi, ci, err)
							}
						}
					}
				}
			}
		}
	}

	return jsonBody, nil
}

// IsOpus47 returns true if the model is Claude Opus 4.7 or a later generation where:
//   - Extended thinking (budget_tokens) is removed — only adaptive thinking is supported.
//   - temperature, top_p, and top_k are not supported (setting them returns a 400).
func IsOpus47(model string) bool {
	model = strings.ToLower(model)
	if !strings.Contains(model, "opus") {
		return false
	}
	return strings.Contains(model, "4-7") || strings.Contains(model, "4.7")
}

// SupportsNativeEffort returns true if the model supports Anthropic's native output_config.effort parameter.
// Currently supported on Claude Opus 4.5 and Opus 4.6.
func SupportsNativeEffort(model string) bool {
	model = strings.ToLower(model)
	if !strings.Contains(model, "opus") {
		return false
	}
	return strings.Contains(model, "4-5") || strings.Contains(model, "4.5") ||
		strings.Contains(model, "4-6") || strings.Contains(model, "4.6")
}

// SupportsFastMode returns true if the model supports speed:"fast" (research
// preview). Per Anthropic's fast-mode docs, only Opus 4.6 supports it;
// requests carrying speed:"fast" to any other model are rejected with 400.
// Beta header: fast-mode-2026-02-01.
//
// Source: https://platform.claude.com/docs/en/build-with-claude/fast-mode
func SupportsFastMode(model string) bool {
	model = strings.ToLower(model)
	if !strings.Contains(model, "opus") {
		return false
	}
	return strings.Contains(model, "4-6") || strings.Contains(model, "4.6")
}

// SupportsAdaptiveThinking returns true if the model supports thinking.type: "adaptive".
// Currently supported on Claude Opus 4.6, Claude Sonnet 4.6, and Claude Opus 4.7+.
// On Opus 4.7+ adaptive is the only thinking-on mode; on Opus 4.6 and Sonnet 4.6 it
// coexists with the deprecated budget_tokens-based extended thinking.
func SupportsAdaptiveThinking(model string) bool {
	if IsOpus47(model) {
		return true
	}
	model = strings.ToLower(model)
	if !strings.Contains(model, "4-6") && !strings.Contains(model, "4.6") {
		return false
	}
	return strings.Contains(model, "opus") || strings.Contains(model, "sonnet")
}

// Computer-use tool generations.
//   - "20251124" — Opus 4.7, Opus 4.6, Sonnet 4.6, Opus 4.5
//   - "20250124" — everything else (Sonnet 4.5, Haiku 4.5, Opus 4.1, Sonnet 4, Opus 4, Sonnet 3.7)
//
// The bash tool is generation-invariant (always bash_20250124).
const (
	ComputerUseGen20251124 = "20251124"
	ComputerUseGen20250124 = "20250124"
)

// ComputerUseGeneration returns the tool-version generation a Claude model
// uses for computer-use / text-editor tools. This drives:
//   - Which beta header to inject (computer-use-2025-11-24 vs 2025-01-24).
//   - Which computer_*/text_editor_* type the upstream API will accept.
//   - Which `name` literal Anthropic's Pydantic validator demands for text_editor.
func ComputerUseGeneration(model string) string {
	m := strings.ToLower(model)
	// Opus 4.7+ falls into the new generation.
	if IsOpus47(m) {
		return ComputerUseGen20251124
	}
	// Opus 4.6 / Sonnet 4.6 / Opus 4.5 also use the new generation.
	if strings.Contains(m, "opus") {
		if strings.Contains(m, "4-5") || strings.Contains(m, "4.5") ||
			strings.Contains(m, "4-6") || strings.Contains(m, "4.6") {
			return ComputerUseGen20251124
		}
	}
	if strings.Contains(m, "sonnet") {
		if strings.Contains(m, "4-6") || strings.Contains(m, "4.6") {
			return ComputerUseGen20251124
		}
	}
	return ComputerUseGen20250124
}

// NormalizedToolSpec returns the canonical {type, name} pair Anthropic's API
// expects for a server tool, given the model's computer-use generation.
// baseTool is the family name with no version suffix: "computer", "text_editor", or "bash".
// Returns ("", "") if baseTool is unknown.
func NormalizedToolSpec(generation, baseTool string) (toolType, toolName string) {
	switch baseTool {
	case "computer":
		if generation == ComputerUseGen20251124 {
			return string(AnthropicToolTypeComputer20251124), "computer"
		}
		return string(AnthropicToolTypeComputer20250124), "computer"
	case "bash":
		// bash_20250124 is generation-invariant per Anthropic's docs.
		return string(AnthropicToolTypeBash20250124), "bash"
	case "text_editor":
		if generation == ComputerUseGen20251124 {
			return string(AnthropicToolTypeTextEditor20250728), "str_replace_based_edit_tool"
		}
		return string(AnthropicToolTypeTextEditor20250124), "str_replace_editor"
	}
	return "", ""
}

// computerUseBaseTool extracts the family name from a versioned tool type.
// Returns "" for tool types that are not part of the computer-use family.
//
// Examples:
//
//	computer_20251124       -> "computer"
//	text_editor_20250728    -> "text_editor"
//	bash_20250124           -> "bash"
//	web_search_20250305     -> ""
func computerUseBaseTool(toolType string) string {
	switch {
	case strings.HasPrefix(toolType, "computer_"):
		return "computer"
	case strings.HasPrefix(toolType, "text_editor_"):
		return "text_editor"
	case strings.HasPrefix(toolType, "bash_"):
		return "bash"
	}
	return ""
}

// MapBifrostEffortToAnthropic maps a Bifrost effort level to an Anthropic effort level.
// Anthropic supports "low", "medium", "high", "max"; Bifrost also has "minimal" which maps to "low".
func MapBifrostEffortToAnthropic(effort string) string {
	if effort == "minimal" {
		return "low"
	}
	return effort
}

// setEffortOnOutputConfig merges the effort value into the request's OutputConfig,
// preserving any existing Format field (used for structured outputs).
func setEffortOnOutputConfig(req *AnthropicMessageRequest, effort string) {
	if req.OutputConfig == nil {
		req.OutputConfig = &AnthropicOutputConfig{}
	}
	req.OutputConfig.Effort = &effort
}

// getRequestBodyForResponses serializes a BifrostResponsesRequest into the Anthropic wire format.
// It delegates to BuildAnthropicResponsesRequestBody with the appropriate provider and streaming config.
func getRequestBodyForResponses(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, isStreaming bool, excludeFields []string, shouldSendBackRawRequest bool, shouldSendBackRawResponse bool) ([]byte, *schemas.BifrostError) {
	return BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:                  schemas.Anthropic,
		IsStreaming:               isStreaming,
		ExcludeFields:             excludeFields,
		ShouldSendBackRawRequest:  shouldSendBackRawRequest,
		ShouldSendBackRawResponse: shouldSendBackRawResponse,
	})
}

// AddMissingBetaHeadersToContext analyzes the Anthropic request and adds missing beta headers to the context.
// The provider parameter controls which headers are included — unsupported headers for the given provider are skipped.
func AddMissingBetaHeadersToContext(ctx *schemas.BifrostContext, req *AnthropicMessageRequest, provider schemas.ModelProvider) error {
	features, hasProvider := ProviderFeatures[provider]
	headers := []string{}
	hasCachingScope := false
	if req.Tools != nil {
		for _, tool := range req.Tools {
			// Check for version-specific beta headers based on tool type
			if tool.Type != nil {
				switch *tool.Type {
				case AnthropicToolTypeComputer20251124:
					if !hasProvider || features.ComputerUse {
						headers = appendUniqueHeader(headers, AnthropicComputerUseBetaHeader20251124)
					}
				case AnthropicToolTypeComputer20250124:
					if !hasProvider || features.ComputerUse {
						headers = appendUniqueHeader(headers, AnthropicComputerUseBetaHeader20250124)
					}
				}
			}
			// Check for strict (structured-outputs)
			if tool.Strict != nil && *tool.Strict {
				if !hasProvider || features.StructuredOutputs {
					headers = appendUniqueHeader(headers, AnthropicStructuredOutputsBetaHeader)
				}
			}
			// Check for advanced-tool-use features. defer_loading and
			// allowed_callers are only available as part of the bundle
			// header; input_examples additionally has a standalone header
			// (tool-examples-2025-10-29) used on Bedrock where the bundle is
			// not accepted.
			if tool.DeferLoading != nil && *tool.DeferLoading {
				if !hasProvider || features.AdvancedToolUse {
					headers = appendUniqueHeader(headers, AnthropicAdvancedToolUseBetaHeader)
				}
			}
			if len(tool.InputExamples) > 0 {
				if !hasProvider || features.AdvancedToolUse {
					// Bundle header covers input_examples transitively.
					headers = appendUniqueHeader(headers, AnthropicAdvancedToolUseBetaHeader)
				} else if features.InputExamples {
					// Narrow standalone header (e.g. Bedrock).
					headers = appendUniqueHeader(headers, AnthropicToolExamplesBetaHeader)
				}
			}
			if len(tool.AllowedCallers) > 0 {
				if !hasProvider || features.AdvancedToolUse {
					headers = appendUniqueHeader(headers, AnthropicAdvancedToolUseBetaHeader)
				}
			}
			// input_examples has both bundle coverage AND a standalone header.
			// Prefer the bundle header when the provider accepts the bundle
			// (covers input_examples transitively); fall back to the narrow
			// standalone header (Bedrock) when only InputExamples is set.
			if len(tool.InputExamples) > 0 {
				if !hasProvider || features.AdvancedToolUse {
					headers = appendUniqueHeader(headers, AnthropicAdvancedToolUseBetaHeader)
				} else if features.InputExamples {
					headers = appendUniqueHeader(headers, AnthropicToolExamplesBetaHeader)
				}
			}
			// Check for fine-grained tool streaming (eager_input_streaming).
			// Beta fine-grained-tool-streaming-2025-05-14 — required for
			// input_json_delta streaming on custom tools.
			if tool.EagerInputStreaming != nil && *tool.EagerInputStreaming {
				if !hasProvider || features.EagerInputStreaming {
					headers = appendUniqueHeader(headers, AnthropicEagerInputStreamingBetaHeader)
				}
			}
			// Check for cache control with scope
			if !hasCachingScope && tool.CacheControl != nil && tool.CacheControl.Scope != nil {
				if !hasProvider || features.PromptCachingScope {
					headers = appendUniqueHeader(headers, AnthropicPromptCachingScopeBetaHeader)
					hasCachingScope = true
				}
			}
		}
	}
	// Check for cache control with scope at the top level of the request
	// (mirrors the tool/system/message checks below).
	if !hasCachingScope && req.CacheControl != nil && req.CacheControl.Scope != nil {
		if !hasProvider || features.PromptCachingScope {
			headers = appendUniqueHeader(headers, AnthropicPromptCachingScopeBetaHeader)
			hasCachingScope = true
		}
	}
	// Check for compaction
	if req.ContextManagement != nil {
		for _, edit := range req.ContextManagement.Edits {
			if edit.Type == ContextManagementEditTypeCompact {
				if !hasProvider || features.Compaction {
					headers = appendUniqueHeader(headers, AnthropicCompactionBetaHeader)
				}
			}
			if edit.Type == ContextManagementEditTypeClearToolUses || edit.Type == ContextManagementEditTypeClearThinking {
				if !hasProvider || features.ContextEditing {
					headers = appendUniqueHeader(headers, AnthropicContextManagementBetaHeader)
				}
			}
		}
	}
	// Check for MCP servers
	if len(req.MCPServers) > 0 {
		if !hasProvider || features.MCP {
			headers = appendUniqueHeader(headers, AnthropicMCPClientBetaHeader)
		}
	}
	// Check for interleaved thinking (required for older Claude 4 models with thinking enabled)
	if req.Thinking != nil && req.Thinking.Type == "enabled" {
		if !hasProvider || features.InterleavedThinking {
			headers = appendUniqueHeader(headers, AnthropicInterleavedThinkingBetaHeader)
		}
	}
	// Check for fast mode. Only add the beta header when both the provider
	// supports fast mode AND the model does (Opus 4.6 only per
	// SupportsFastMode); otherwise sending the header guarantees a 400.
	if req.Speed != nil && *req.Speed == "fast" {
		if (!hasProvider || features.FastMode) && SupportsFastMode(req.Model) {
			headers = appendUniqueHeader(headers, AnthropicFastModeBetaHeader)
		}
	}
	// Check for task budget
	if req.OutputConfig != nil && req.OutputConfig.TaskBudget != nil {
		if !hasProvider || features.TaskBudgets {
			headers = appendUniqueHeader(headers, AnthropicTaskBudgetsBetaHeader)
		}
	}
	// Check for output format (structured outputs)
	if req.OutputFormat != nil {
		if !hasProvider || features.StructuredOutputs {
			headers = appendUniqueHeader(headers, AnthropicStructuredOutputsBetaHeader)
		}
	}
	// Check for cache control with scope in system message (only if not already found)
	if !hasCachingScope && req.System != nil && req.System.ContentBlocks != nil {
		for _, block := range req.System.ContentBlocks {
			if block.CacheControl != nil && block.CacheControl.Scope != nil {
				if !hasProvider || features.PromptCachingScope {
					headers = appendUniqueHeader(headers, AnthropicPromptCachingScopeBetaHeader)
					hasCachingScope = true
				}
				break
			}
		}
	}
	// Check for cache control with scope in messages (only if not already found)
	if !hasCachingScope {
		for _, message := range req.Messages {
			if message.Content.ContentBlocks != nil {
				for _, block := range message.Content.ContentBlocks {
					if block.CacheControl != nil && block.CacheControl.Scope != nil {
						if !hasProvider || features.PromptCachingScope {
							headers = appendUniqueHeader(headers, AnthropicPromptCachingScopeBetaHeader)
							hasCachingScope = true
						}
						break
					}
				}
				if hasCachingScope {
					break
				}
			}
		}
	}
	if len(headers) == 0 {
		return nil
	}
	var extraHeaders map[string][]string
	if ctx.Value(schemas.BifrostContextKeyExtraHeaders) == nil {
		extraHeaders = map[string][]string{}
	} else {
		if ctxExtraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
			extraHeaders = ctxExtraHeaders
		}
	}
	existing := extraHeaders[AnthropicBetaHeader]
	if len(existing) == 0 {
		extraHeaders[AnthropicBetaHeader] = headers
	} else {
		// Passthrough wins: skip auto-injected headers when a same-prefix header
		// already exists from passthrough. This prevents conflicting versions
		// (e.g. mcp-client-2025-04-04 + mcp-client-2025-11-20) in the same request.
		for _, h := range headers {
			if !betaHeaderPrefixExists(existing, h) {
				existing = append(existing, h)
			}
		}
		extraHeaders[AnthropicBetaHeader] = existing
	}
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
	return nil
}

// betaHeaderPrefixKnown maps known beta header prefixes for prefix-aware dedup.
var betaHeaderPrefixKnown = []string{
	"computer-use-",
	AnthropicStructuredOutputsBetaHeaderPrefix,
	AnthropicMCPClientBetaHeaderPrefix,
	AnthropicPromptCachingScopeBetaHeaderPrefix,
	"compact-",
	"context-management-",
	"files-api-",
	AnthropicAdvancedToolUseBetaHeaderPrefix,
	AnthropicToolExamplesBetaHeaderPrefix,
	AnthropicInterleavedThinkingBetaHeaderPrefix,
	AnthropicSkillsBetaHeaderPrefix,
	AnthropicContext1MBetaHeaderPrefix,
	AnthropicFastModeBetaHeaderPrefix,
	AnthropicRedactThinkingBetaHeaderPrefix,
	AnthropicTaskBudgetsBetaHeaderPrefix,
	AnthropicEagerInputStreamingBetaHeaderPrefix,
}

// betaHeaderPrefixExists checks if any header in existing shares a known prefix with newHeader.
// Returns true if a same-prefix header is already present (passthrough wins).
// Handles comma-separated values within a single header string (per HTTP spec).
func betaHeaderPrefixExists(existing []string, newHeader string) bool {
	// Find which known prefix the new header belongs to
	var matchedPrefix string
	for _, prefix := range betaHeaderPrefixKnown {
		if strings.HasPrefix(newHeader, prefix) {
			matchedPrefix = prefix
			break
		}
	}
	match := func(candidate string) bool {
		if matchedPrefix == "" {
			return candidate == newHeader
		}
		return strings.HasPrefix(candidate, matchedPrefix)
	}
	for _, headerValue := range existing {
		for _, candidate := range strings.Split(headerValue, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if match(candidate) {
				return true
			}
		}
	}
	return false
}

// ToolVersionRemap defines a mapping from an unsupported tool version to a supported one.
type ToolVersionRemap struct {
	From string
	To   string
}

// providerToolVersionRemaps defines version downgrades per provider.
// When a raw request contains a tool type not supported by the target provider,
// it gets remapped to the supported version.
var providerToolVersionRemaps = map[schemas.ModelProvider][]ToolVersionRemap{
	schemas.Vertex: {
		// Vertex only supports basic web search, not dynamic filtering
		{From: string(AnthropicToolTypeWebSearch20260209), To: string(AnthropicToolTypeWebSearch20250305)},
		// Vertex does not support web fetch at all — no remap, these should error
		// Vertex does not support code execution — no remap, these should error
	},
	// Bedrock does not support web search, web fetch, or code execution at all — no remaps
	// Anthropic and Azure support all versions — no remaps needed
}

// unsupportedRawToolTypes lists tool type prefixes that should be rejected per provider
// when found in raw request bodies (no remap possible, the feature itself is unsupported).
var unsupportedRawToolTypes = map[schemas.ModelProvider][]string{
	schemas.Vertex: {
		"web_fetch_",     // No web fetch support on Vertex
		"code_execution", // No code execution on Vertex
	},
	schemas.Bedrock: {
		"web_search_",    // No web search on Bedrock
		"web_fetch_",     // No web fetch on Bedrock
		"code_execution", // No code execution on Bedrock
	},
}

// doesWebSearchOrFetchAutoInjectCodeExecution reports whether the given web search/fetch tool type
// automatically injects a code-execution beta header. Newer tool versions (2026-02-09 and later)
// require it; older ones do not. Defaults to true for unrecognized types to preserve backward compatibility.
func doesWebSearchOrFetchAutoInjectCodeExecution(toolType string) bool {
	switch toolType {
	case string(AnthropicToolTypeWebSearch20250305):
		return false
	case string(AnthropicToolTypeWebSearch20260209):
		return true
	case string(AnthropicToolTypeWebFetch20260309):
		return true
	case string(AnthropicToolTypeWebFetch20250910):
		return false
	case string(AnthropicToolTypeWebFetch20260209):
		return true
	}

	// Keeping it for backward compatibility as this always used to be true
	return true
}

// StripEmptyThinkingBlocks removes thinking content blocks where
// "thinking" is an empty string. Anthropic rejects such blocks with a 400.
func StripEmptyThinkingBlocks(jsonBody []byte) ([]byte, error) {
	messagesResult := providerUtils.GetJSONField(jsonBody, "messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return jsonBody, nil
	}
	var err error
	for mi, msg := range messagesResult.Array() {
		contentResult := msg.Get("content")
		if !contentResult.Exists() || !contentResult.IsArray() {
			continue
		}
		var toStrip []int
		for ci, block := range contentResult.Array() {
			if block.Get("type").String() == "thinking" && block.Get("thinking").String() == "" {
				toStrip = append(toStrip, ci)
			}
		}
		for i := len(toStrip) - 1; i >= 0; i-- {
			path := fmt.Sprintf("messages.%d.content.%d", mi, toStrip[i])
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, path)
			if err != nil {
				return nil, fmt.Errorf("failed to strip empty thinking block at %s: %w", path, err)
			}

		}
	}
	return jsonBody, nil
}

// StripAutoInjectableTools removes code_execution tools from the raw JSON body's tools array
// when web_search or web_fetch tools are also present. The Anthropic API auto-injects
// code_execution when web_search_20260209 or web_fetch_20260209 is included in the request,
// and returns an error if code_execution is also explicitly included.
// This function strips code_execution only in that case to prevent the
// "Auto-injecting tools would conflict" error.
func StripAutoInjectableTools(jsonBody []byte) ([]byte, error) {
	toolsResult := providerUtils.GetJSONField(jsonBody, "tools")
	if !toolsResult.Exists() || !toolsResult.IsArray() {
		return jsonBody, nil
	}

	tools := toolsResult.Array()
	if len(tools) == 0 {
		return jsonBody, nil
	}

	// Check if web_search or web_fetch is present — only then does Anthropic
	// auto-inject code_execution, causing a conflict if it's also explicit.
	hasWebSearchOrFetchWithAutoInjectableCodeExecution := false
	for _, tool := range tools {
		toolType := tool.Get("type").String()
		if strings.HasPrefix(toolType, "web_search_") || strings.HasPrefix(toolType, "web_fetch_") {
			hasWebSearchOrFetchWithAutoInjectableCodeExecution = doesWebSearchOrFetchAutoInjectCodeExecution(toolType)
			break
		}
	}

	if !hasWebSearchOrFetchWithAutoInjectableCodeExecution {
		return jsonBody, nil
	}

	// Collect indices of code_execution tools to strip
	var indicesToStrip []int
	for i, tool := range tools {
		toolType := tool.Get("type").String()
		if strings.HasPrefix(toolType, "code_execution") {
			indicesToStrip = append(indicesToStrip, i)
		}
	}

	if len(indicesToStrip) == 0 {
		return jsonBody, nil
	}

	// If all tools would be stripped, remove the tools key entirely
	if len(indicesToStrip) == len(tools) {
		return providerUtils.DeleteJSONField(jsonBody, "tools")
	}

	// Delete in reverse order to preserve indices
	var err error
	for i := len(indicesToStrip) - 1; i >= 0; i-- {
		path := fmt.Sprintf("tools.%d", indicesToStrip[i])
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, path)
		if err != nil {
			return nil, fmt.Errorf("failed to strip auto-injectable tool at index %d: %w", indicesToStrip[i], err)
		}
	}

	return jsonBody, nil
}

// RemapRawToolVersionsForProvider inspects tools in a raw JSON body and remaps
// unsupported tool versions to supported ones for the target provider, and
// normalizes computer-use / text-editor / bash tool {type, name} pairs to match
// the model's required generation. Returns an error if a tool type is
// fundamentally unsupported (no remap possible).
//
// model is the request's "model" field; it drives ComputerUseGeneration so that
// (e.g.) a request pairing claude-sonnet-4-6 with text_editor_20250124 gets
// rewritten to text_editor_20250728 + str_replace_based_edit_tool before
// hitting Anthropic's strict Pydantic validator.
func RemapRawToolVersionsForProvider(jsonBody []byte, provider schemas.ModelProvider, model string) ([]byte, error) {
	toolsResult := providerUtils.GetJSONField(jsonBody, "tools")
	if !toolsResult.Exists() || !toolsResult.IsArray() {
		return jsonBody, nil
	}

	var err error
	tools := toolsResult.Array()

	// Check for unsupported types first
	if prefixes, ok := unsupportedRawToolTypes[provider]; ok {
		for _, tool := range tools {
			toolType := tool.Get("type").String()
			for _, prefix := range prefixes {
				if strings.HasPrefix(toolType, prefix) {
					return nil, fmt.Errorf("tool type '%s' is not supported by provider '%s'", toolType, provider)
				}
			}
		}
	}

	// Normalize computer-use / text-editor / bash tools to the canonical
	// (type, name) pair for the model's generation. Runs before
	// providerToolVersionRemaps so downgrades still work for non-Anthropic
	// providers that share the schema.
	generation := ComputerUseGeneration(model)
	for i, tool := range tools {
		toolType := tool.Get("type").String()
		baseTool := computerUseBaseTool(toolType)
		if baseTool == "" {
			continue
		}
		wantType, wantName := NormalizedToolSpec(generation, baseTool)
		if wantType == "" {
			continue
		}
		if toolType != wantType {
			path := fmt.Sprintf("tools.%d.type", i)
			jsonBody, err = providerUtils.SetJSONField(jsonBody, path, wantType)
			if err != nil {
				return nil, fmt.Errorf("failed to normalize tool type: %w", err)
			}
		}
		// Only set name if the tool has one (custom tools use input_schema; computer-use family always has a name).
		if existingName := tool.Get("name").String(); existingName != "" && existingName != wantName {
			path := fmt.Sprintf("tools.%d.name", i)
			jsonBody, err = providerUtils.SetJSONField(jsonBody, path, wantName)
			if err != nil {
				return nil, fmt.Errorf("failed to normalize tool name: %w", err)
			}
		}
	}

	// Apply provider-specific version remaps (e.g. web_search downgrades for non-Anthropic providers)
	remaps, ok := providerToolVersionRemaps[provider]
	if !ok {
		return jsonBody, nil
	}

	// Re-fetch tools array since paths may have changed via SetJSONField above
	tools = providerUtils.GetJSONField(jsonBody, "tools").Array()
	for i, tool := range tools {
		toolType := tool.Get("type").String()
		for _, remap := range remaps {
			if toolType == remap.From {
				path := fmt.Sprintf("tools.%d.type", i)
				jsonBody, err = providerUtils.SetJSONField(jsonBody, path, remap.To)
				if err != nil {
					return nil, fmt.Errorf("failed to remap tool type: %w", err)
				}
				break
			}
		}
	}

	return jsonBody, nil
}

// betaHeaderPrefixToFeature maps each known beta header prefix to a function that checks
// whether the feature is supported by the provider's default feature set.
var betaHeaderPrefixToFeature = map[string]func(ProviderFeatureSupport) bool{
	"computer-use-": func(f ProviderFeatureSupport) bool { return f.ComputerUse },
	AnthropicStructuredOutputsBetaHeaderPrefix:  func(f ProviderFeatureSupport) bool { return f.StructuredOutputs },
	AnthropicMCPClientBetaHeaderPrefix:          func(f ProviderFeatureSupport) bool { return f.MCP },
	AnthropicPromptCachingScopeBetaHeaderPrefix: func(f ProviderFeatureSupport) bool { return f.PromptCachingScope },
	"compact-":                                   func(f ProviderFeatureSupport) bool { return f.Compaction },
	"context-management-":                        func(f ProviderFeatureSupport) bool { return f.ContextEditing },
	"files-api-":                                 func(f ProviderFeatureSupport) bool { return f.FilesAPI },
	AnthropicAdvancedToolUseBetaHeaderPrefix:     func(f ProviderFeatureSupport) bool { return f.AdvancedToolUse },
	AnthropicToolExamplesBetaHeaderPrefix:        func(f ProviderFeatureSupport) bool { return f.InputExamples },
	AnthropicInterleavedThinkingBetaHeaderPrefix: func(f ProviderFeatureSupport) bool { return f.InterleavedThinking },
	AnthropicSkillsBetaHeaderPrefix:              func(f ProviderFeatureSupport) bool { return f.Skills },
	AnthropicContext1MBetaHeaderPrefix:           func(f ProviderFeatureSupport) bool { return f.Context1M },
	AnthropicFastModeBetaHeaderPrefix:            func(f ProviderFeatureSupport) bool { return f.FastMode },
	AnthropicRedactThinkingBetaHeaderPrefix:      func(f ProviderFeatureSupport) bool { return f.RedactThinking },
	AnthropicTaskBudgetsBetaHeaderPrefix:         func(f ProviderFeatureSupport) bool { return f.TaskBudgets },
	AnthropicEagerInputStreamingBetaHeaderPrefix: func(f ProviderFeatureSupport) bool { return f.EagerInputStreaming },
}

// MergeBetaHeaders collects anthropic-beta values from provider ExtraHeaders and
// per-request context headers, deduplicating them.
func MergeBetaHeaders(ctx context.Context, providerExtraHeaders map[string]string) []string {
	seen := make(map[string]bool)
	var all []string
	add := func(v string) {
		for part := range strings.SplitSeq(v, ",") {
			if t := strings.TrimSpace(part); t != "" && !seen[t] {
				seen[t] = true
				all = append(all, t)
			}
		}
	}
	for k, v := range providerExtraHeaders {
		if strings.EqualFold(k, AnthropicBetaHeader) && v != "" {
			add(v)
		}
	}
	if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
		for k, vals := range ctxHeaders {
			if !strings.EqualFold(k, AnthropicBetaHeader) {
				continue
			}
			for _, v := range vals {
				add(v)
			}
		}
	}
	return all
}

// FilterBetaHeadersForProvider validates that all beta headers are supported by the given provider.
// Returns an error if a known beta header is not supported by the provider.
// Unknown headers are forwarded only to Anthropic; for other providers they are silently dropped.
// If overrides is non-nil, its entries (keyed by prefix) take precedence over the hardcoded defaults.
func FilterBetaHeadersForProvider(headers []string, provider schemas.ModelProvider, overrides ...map[string]bool) []string {
	features, hasProvider := ProviderFeatures[provider]
	if !hasProvider {
		// Unknown provider — allow all headers (safe default for custom providers)
		return headers
	}

	var overrideMap map[string]bool
	if len(overrides) > 0 {
		overrideMap = overrides[0]
	}

	filtered := make([]string, 0, len(headers))
	for _, h := range headers {
		for token := range strings.SplitSeq(h, ",") {
			token = strings.TrimSpace(token)

			if token == "" {
				continue
			}

			// Find which known prefix this token matches
			var matchedPrefix string
			for _, prefix := range betaHeaderPrefixKnown {
				if strings.HasPrefix(token, prefix) {
					matchedPrefix = prefix
					break
				}
			}

			if matchedPrefix == "" {
				// Check if any custom override prefix matches this unknown header
				if overrideMap != nil {
					matched := false
					for prefix, allowed := range overrideMap {
						if strings.HasPrefix(token, prefix) {
							if allowed {
								filtered = append(filtered, token)
							}
							// If not allowed, silently drop — custom overrides are user preferences,
							// not hard incompatibilities that should break the request.
							matched = true
							break
						}
					}
					if matched {
						continue
					}
				}
				// No override match — forward only to Anthropic API for forward compatibility.
				// Non-Anthropic providers reject unrecognized headers, so drop unknown ones.
				if provider == schemas.Anthropic {
					filtered = append(filtered, token)
				}
				continue
			}

			// Check override first, then fall back to hardcoded feature support
			supported := false
			if overrideMap != nil {
				if override, hasOverride := overrideMap[matchedPrefix]; hasOverride {
					supported = override
				} else if featureCheck, ok := betaHeaderPrefixToFeature[matchedPrefix]; ok {
					supported = featureCheck(features)
				}
			} else if featureCheck, ok := betaHeaderPrefixToFeature[matchedPrefix]; ok {
				supported = featureCheck(features)
			}

			if !supported {
				continue
			}
			filtered = append(filtered, token)
		}
	}
	return filtered
}

// appendUniqueHeader adds a header to the slice if not already present
func appendUniqueHeader(slice []string, item string) []string {
	if slices.Contains(slice, item) {
		return slice
	}
	return append(slice, item)
}

// appendBetaHeader appends a beta header to the request, preserving any existing beta headers
func appendBetaHeader(req *fasthttp.Request, betaHeader string) {
	existing := string(req.Header.Peek(AnthropicBetaHeader))
	if existing == "" {
		req.Header.Set(AnthropicBetaHeader, betaHeader)
		return
	}
	// Check if header already present
	for _, h := range strings.Split(existing, ",") {
		if strings.TrimSpace(h) == betaHeader {
			return
		}
	}
	req.Header.Set(AnthropicBetaHeader, existing+","+betaHeader)
}

// convertChatResponseFormatToTool converts a response_format config to an Anthropic tool for structured output
// This is used when the provider is Vertex, which doesn't support native structured outputs
func convertChatResponseFormatToTool(ctx *schemas.BifrostContext, params *schemas.ChatParameters) *AnthropicTool {
	if params == nil || params.ResponseFormat == nil {
		return nil
	}

	// ResponseFormat is stored as interface{}, need to parse it
	responseFormatMap, ok := (*params.ResponseFormat).(map[string]interface{})
	if !ok {
		return nil
	}

	// Check if type is "json_schema"
	formatType, ok := responseFormatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	// Extract json_schema object
	jsonSchemaObj, ok := responseFormatMap["json_schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract name and schema
	toolName, ok := jsonSchemaObj["name"].(string)
	if !ok || toolName == "" {
		toolName = "json_response"
	}

	schemaObj, ok := jsonSchemaObj["schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract description from schema if available
	description := "Returns structured JSON output"
	if desc, ok := schemaObj["description"].(string); ok && desc != "" {
		description = desc
	}

	// Set bifrost context key structured output tool name
	toolName = fmt.Sprintf("bf_so_%s", toolName)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	// Create the Anthropic tool
	normalizedSchema := normalizeSchemaForAnthropic(schemaObj)
	schemaParams := convertMapToToolFunctionParameters(normalizedSchema)

	return &AnthropicTool{
		Name:        toolName,
		Description: schemas.Ptr(description),
		InputSchema: schemaParams,
	}
}

// convertResponsesTextFormatToTool converts a text config to an Anthropic tool for structured output
// This is used when the provider is Vertex, which doesn't support native structured outputs
func convertResponsesTextFormatToTool(ctx *schemas.BifrostContext, textConfig *schemas.ResponsesTextConfig) *AnthropicTool {
	if textConfig == nil || textConfig.Format == nil {
		return nil
	}

	format := textConfig.Format
	if format.Type != "json_schema" {
		return nil
	}

	toolName := "json_response"
	if format.Name != nil && strings.TrimSpace(*format.Name) != "" {
		toolName = strings.TrimSpace(*format.Name)
	}

	description := "Returns structured JSON output"
	if format.JSONSchema != nil && format.JSONSchema.Description != nil {
		description = *format.JSONSchema.Description
	}

	toolName = fmt.Sprintf("bf_so_%s", toolName)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	var schemaParams *schemas.ToolFunctionParameters
	if format.JSONSchema != nil {
		schemaParams = convertJSONSchemaToToolParameters(format.JSONSchema)
	} else {
		return nil // Schema is required for tooling
	}

	return &AnthropicTool{
		Name:        toolName,
		Description: schemas.Ptr(description),
		InputSchema: schemaParams,
	}
}

// convertJSONSchemaToToolParameters directly converts ResponsesTextConfigFormatJSONSchema to ToolFunctionParameters
func convertJSONSchemaToToolParameters(schema *schemas.ResponsesTextConfigFormatJSONSchema) *schemas.ToolFunctionParameters {
	if schema == nil {
		return nil
	}

	// Default type to "object" if not specified
	schemaType := "object"
	if schema.Type != nil {
		schemaType = *schema.Type
	}

	params := &schemas.ToolFunctionParameters{
		Type:                 schemaType,
		Description:          schema.Description,
		Required:             schema.Required,
		Enum:                 schema.Enum,
		Ref:                  schema.Ref,
		MinItems:             schema.MinItems,
		MaxItems:             schema.MaxItems,
		Format:               schema.Format,
		Pattern:              schema.Pattern,
		MinLength:            schema.MinLength,
		MaxLength:            schema.MaxLength,
		Minimum:              schema.Minimum,
		Maximum:              schema.Maximum,
		Title:                schema.Title,
		Default:              schema.Default,
		Nullable:             schema.Nullable,
		AdditionalProperties: schema.AdditionalProperties,
	}

	// Convert map[string]any to OrderedMap for Properties
	if schema.Properties != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Properties); ok {
			params.Properties = orderedMap
		}
	}

	// Convert map[string]any to OrderedMap for Defs
	if schema.Defs != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Defs); ok {
			params.Defs = orderedMap
		}
	}

	// Convert map[string]any to OrderedMap for Definitions
	if schema.Definitions != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Definitions); ok {
			params.Definitions = orderedMap
		}
	}

	// Convert map[string]any to OrderedMap for Items
	if schema.Items != nil {
		if orderedMap, ok := schemas.SafeExtractOrderedMap(*schema.Items); ok {
			params.Items = orderedMap
		}
	}

	// Convert []map[string]any to []OrderedMap for composition fields
	if len(schema.AnyOf) > 0 {
		params.AnyOf = make([]schemas.OrderedMap, 0, len(schema.AnyOf))
		for _, item := range schema.AnyOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				params.AnyOf = append(params.AnyOf, *orderedMap)
			}
		}
	}

	if len(schema.OneOf) > 0 {
		params.OneOf = make([]schemas.OrderedMap, 0, len(schema.OneOf))
		for _, item := range schema.OneOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				params.OneOf = append(params.OneOf, *orderedMap)
			}
		}
	}

	if len(schema.AllOf) > 0 {
		params.AllOf = make([]schemas.OrderedMap, 0, len(schema.AllOf))
		for _, item := range schema.AllOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				params.AllOf = append(params.AllOf, *orderedMap)
			}
		}
	}

	return params
}

// convertMapToToolFunctionParameters converts a map to ToolFunctionParameters
func convertMapToToolFunctionParameters(m map[string]interface{}) *schemas.ToolFunctionParameters {
	params := &schemas.ToolFunctionParameters{}

	if typeVal, ok := m["type"].(string); ok {
		params.Type = typeVal
	}
	if desc, ok := m["description"].(string); ok {
		params.Description = &desc
	}
	if props, ok := schemas.SafeExtractOrderedMap(m["properties"]); ok {
		params.Properties = props
	}
	if req, ok := m["required"].([]interface{}); ok {
		required := make([]string, 0, len(req))
		for _, r := range req {
			if str, ok := r.(string); ok {
				required = append(required, str)
			}
		}
		params.Required = required
	}
	if addProps, ok := m["additionalProperties"]; ok {
		if addPropsBool, ok := addProps.(bool); ok {
			params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesBool: &addPropsBool,
			}
		} else if addPropsMap, ok := schemas.SafeExtractOrderedMap(addProps); ok {
			params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesMap: addPropsMap,
			}
		}
	}
	if defs, ok := schemas.SafeExtractOrderedMap(m["$defs"]); ok {
		params.Defs = defs
	}
	if definitions, ok := schemas.SafeExtractOrderedMap(m["definitions"]); ok {
		params.Definitions = definitions
	}
	if ref, ok := m["$ref"].(string); ok {
		params.Ref = &ref
	}
	if items, ok := schemas.SafeExtractOrderedMap(m["items"]); ok {
		params.Items = items
	}
	if minItems, ok := anthropicExtractInt64(m["minItems"]); ok {
		params.MinItems = schemas.Ptr(minItems)
	}
	if maxItems, ok := anthropicExtractInt64(m["maxItems"]); ok {
		params.MaxItems = schemas.Ptr(maxItems)
	}
	if anyOf, ok := m["anyOf"].([]interface{}); ok {
		anyOfMaps := make([]schemas.OrderedMap, 0, len(anyOf))
		for _, item := range anyOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				anyOfMaps = append(anyOfMaps, *orderedMap)
			}
		}
		if len(anyOfMaps) > 0 {
			params.AnyOf = anyOfMaps
		}
	}
	if oneOf, ok := m["oneOf"].([]interface{}); ok {
		oneOfMaps := make([]schemas.OrderedMap, 0, len(oneOf))
		for _, item := range oneOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				oneOfMaps = append(oneOfMaps, *orderedMap)
			}
		}
		if len(oneOfMaps) > 0 {
			params.OneOf = oneOfMaps
		}
	}
	if allOf, ok := m["allOf"].([]interface{}); ok {
		allOfMaps := make([]schemas.OrderedMap, 0, len(allOf))
		for _, item := range allOf {
			if orderedMap, ok := schemas.SafeExtractOrderedMap(item); ok {
				allOfMaps = append(allOfMaps, *orderedMap)
			}
		}
		if len(allOfMaps) > 0 {
			params.AllOf = allOfMaps
		}
	}
	if format, ok := m["format"].(string); ok {
		params.Format = &format
	}
	if pattern, ok := m["pattern"].(string); ok {
		params.Pattern = &pattern
	}
	if minLength, ok := anthropicExtractInt64(m["minLength"]); ok {
		params.MinLength = schemas.Ptr(minLength)
	}
	if maxLength, ok := anthropicExtractInt64(m["maxLength"]); ok {
		params.MaxLength = schemas.Ptr(maxLength)
	}
	if minimum, ok := anthropicExtractFloat64(m["minimum"]); ok {
		params.Minimum = &minimum
	}
	if maximum, ok := anthropicExtractFloat64(m["maximum"]); ok {
		params.Maximum = &maximum
	}
	if title, ok := m["title"].(string); ok {
		params.Title = &title
	}
	if enumVal, ok := m["enum"]; ok {
		switch e := enumVal.(type) {
		case []interface{}:
			enumStrs := make([]string, 0, len(e))
			for _, v := range e {
				if s, ok := v.(string); ok {
					enumStrs = append(enumStrs, s)
				}
			}
			if len(enumStrs) > 0 {
				params.Enum = enumStrs
			}
		case []string:
			if len(e) > 0 {
				params.Enum = e
			}
		}
	}
	if def, ok := m["default"]; ok {
		params.Default = def
	}
	if nullable, ok := m["nullable"].(bool); ok {
		params.Nullable = &nullable
	}

	if params.Type == "" {
		params.Type = "object"
	}

	return params
}

// ConvertAnthropicFinishReasonToBifrost converts provider finish reasons to Bifrost format
func ConvertAnthropicFinishReasonToBifrost(providerReason AnthropicStopReason) string {
	if bifrostReason, ok := anthropicFinishReasonToBifrost[providerReason]; ok {
		return bifrostReason
	}
	return string(providerReason)
}

// ConvertBifrostFinishReasonToAnthropic converts Bifrost finish reasons to provider format
func ConvertBifrostFinishReasonToAnthropic(bifrostReason string) AnthropicStopReason {
	if providerReason, ok := bifrostToAnthropicFinishReason[bifrostReason]; ok {
		return providerReason
	}
	return AnthropicStopReason(bifrostReason)
}

// ConvertToAnthropicImageBlock converts a Bifrost image block to Anthropic format
// Uses the same pattern as the original buildAnthropicImageSourceMap function
func ConvertToAnthropicImageBlock(block schemas.ChatContentBlock) AnthropicContentBlock {
	imageBlock := AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeImage,
		CacheControl: block.CacheControl,
		Source:       &AnthropicBlockSource{SourceObj: &AnthropicSource{}},
	}

	if block.ImageURLStruct == nil {
		return imageBlock
	}

	// Use the centralized utility functions from schemas package
	sanitizedURL, err := schemas.SanitizeImageURL(block.ImageURLStruct.URL)
	if err != nil {
		// Best-effort: treat as a regular URL without sanitization
		imageBlock.Source.SourceObj.Type = "url"
		imageBlock.Source.SourceObj.URL = &block.ImageURLStruct.URL
		return imageBlock
	}
	urlTypeInfo := schemas.ExtractURLTypeInfo(sanitizedURL)

	formattedImgContent := &AnthropicImageContent{
		Type: urlTypeInfo.Type,
	}

	if urlTypeInfo.MediaType != nil {
		formattedImgContent.MediaType = *urlTypeInfo.MediaType
	}

	if urlTypeInfo.DataURLWithoutPrefix != nil {
		formattedImgContent.URL = *urlTypeInfo.DataURLWithoutPrefix
	} else {
		formattedImgContent.URL = sanitizedURL
	}

	// Convert to Anthropic source format
	if formattedImgContent.Type == schemas.ImageContentTypeURL {
		imageBlock.Source.SourceObj.Type = "url"
		imageBlock.Source.SourceObj.URL = &formattedImgContent.URL
	} else {
		if formattedImgContent.MediaType != "" {
			imageBlock.Source.SourceObj.MediaType = &formattedImgContent.MediaType
		}
		imageBlock.Source.SourceObj.Type = "base64"
		// Use the base64 data without the data URL prefix
		if urlTypeInfo.DataURLWithoutPrefix != nil {
			imageBlock.Source.SourceObj.Data = urlTypeInfo.DataURLWithoutPrefix
		} else {
			imageBlock.Source.SourceObj.Data = &formattedImgContent.URL
		}
	}

	return imageBlock
}

// ConvertToAnthropicDocumentBlock converts a Bifrost file block to Anthropic document format
func ConvertToAnthropicDocumentBlock(block schemas.ChatContentBlock) AnthropicContentBlock {
	documentBlock := AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeDocument,
		CacheControl: block.CacheControl,
		Source:       &AnthropicBlockSource{SourceObj: &AnthropicSource{}},
	}

	if block.Citations != nil {
		documentBlock.Citations = &AnthropicCitations{Config: block.Citations}
	}

	if block.File == nil {
		return documentBlock
	}

	file := block.File

	// Set title if provided
	if file.Filename != nil {
		documentBlock.Title = file.Filename
	}

	// Handle file URL
	if file.FileURL != nil && *file.FileURL != "" {
		documentBlock.Source.SourceObj.Type = "url"
		documentBlock.Source.SourceObj.URL = file.FileURL
		return documentBlock
	}

	// Handle file_data (base64 encoded data)
	if file.FileData != nil && *file.FileData != "" {
		fileData := *file.FileData

		// Check if it's plain text based on file type
		if file.FileType != nil && (*file.FileType == "text/plain" || *file.FileType == "txt") {
			documentBlock.Source.SourceObj.Type = "text"
			documentBlock.Source.SourceObj.Data = &fileData
			return documentBlock
		}

		if strings.HasPrefix(fileData, "data:") {
			urlTypeInfo := schemas.ExtractURLTypeInfo(fileData)

			if urlTypeInfo.DataURLWithoutPrefix != nil {
				// It's a data URL, extract the base64 content
				documentBlock.Source.SourceObj.Type = "base64"
				documentBlock.Source.SourceObj.Data = urlTypeInfo.DataURLWithoutPrefix

				// Set media type from data URL or file type
				if urlTypeInfo.MediaType != nil {
					documentBlock.Source.SourceObj.MediaType = urlTypeInfo.MediaType
				} else if file.FileType != nil {
					documentBlock.Source.SourceObj.MediaType = file.FileType
				}
				return documentBlock
			}
		}

		// Default to base64 for binary files
		documentBlock.Source.SourceObj.Type = "base64"
		documentBlock.Source.SourceObj.Data = &fileData

		// Set media type
		if file.FileType != nil {
			documentBlock.Source.SourceObj.MediaType = file.FileType
		} else {
			// Default to PDF if not specified
			mediaType := "application/pdf"
			documentBlock.Source.SourceObj.MediaType = &mediaType
		}
		return documentBlock
	}

	return documentBlock
}

// ConvertResponsesFileBlockToAnthropic converts a Responses file block directly to Anthropic document format
func ConvertResponsesFileBlockToAnthropic(fileBlock *schemas.ResponsesInputMessageContentBlockFile, cacheControl *schemas.CacheControl, citations *schemas.Citations) AnthropicContentBlock {
	documentBlock := AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeDocument,
		CacheControl: cacheControl,
		Source:       &AnthropicBlockSource{SourceObj: &AnthropicSource{}},
	}

	if citations != nil {
		documentBlock.Citations = &AnthropicCitations{Config: citations}
	}

	if fileBlock == nil {
		return documentBlock
	}

	// Set title if provided
	if fileBlock.Filename != nil {
		documentBlock.Title = fileBlock.Filename
	}

	// Handle file_data (base64 encoded data or plain text)
	if fileBlock.FileData != nil && *fileBlock.FileData != "" {
		fileData := *fileBlock.FileData

		// Check if it's plain text based on file type
		if fileBlock.FileType != nil && (*fileBlock.FileType == "text/plain" || *fileBlock.FileType == "txt") {
			documentBlock.Source.SourceObj.Type = "text"
			documentBlock.Source.SourceObj.Data = &fileData
			documentBlock.Source.SourceObj.MediaType = schemas.Ptr("text/plain")
			return documentBlock
		}

		// Check if it's a data URL (e.g., "data:application/pdf;base64,...")
		if strings.HasPrefix(fileData, "data:") {
			urlTypeInfo := schemas.ExtractURLTypeInfo(fileData)

			if urlTypeInfo.DataURLWithoutPrefix != nil {
				// It's a data URL, extract the base64 content
				documentBlock.Source.SourceObj.Type = "base64"
				documentBlock.Source.SourceObj.Data = urlTypeInfo.DataURLWithoutPrefix

				// Set media type from data URL or file type
				if urlTypeInfo.MediaType != nil {
					documentBlock.Source.SourceObj.MediaType = urlTypeInfo.MediaType
				} else if fileBlock.FileType != nil {
					documentBlock.Source.SourceObj.MediaType = fileBlock.FileType
				}
				return documentBlock
			}
		}

		// Default to base64 for binary files (raw base64 without prefix)
		documentBlock.Source.SourceObj.Type = "base64"
		documentBlock.Source.SourceObj.Data = &fileData

		// Set media type
		if fileBlock.FileType != nil {
			documentBlock.Source.SourceObj.MediaType = fileBlock.FileType
		} else {
			// Default to PDF if not specified
			mediaType := "application/pdf"
			documentBlock.Source.SourceObj.MediaType = &mediaType
		}
		return documentBlock
	}

	// Handle file URL
	if fileBlock.FileURL != nil && *fileBlock.FileURL != "" {
		documentBlock.Source.SourceObj.Type = "url"
		documentBlock.Source.SourceObj.URL = fileBlock.FileURL
		return documentBlock
	}

	return documentBlock
}

func (block AnthropicContentBlock) ToBifrostContentImageBlock() schemas.ChatContentBlock {
	return schemas.ChatContentBlock{
		Type: schemas.ChatContentBlockTypeImage,
		ImageURLStruct: &schemas.ChatInputImage{
			URL: getImageURLFromBlock(block),
		},
	}
}

func getImageURLFromBlock(block AnthropicContentBlock) string {
	// Image blocks always carry object-form sources (never string form).
	if block.Source == nil || block.Source.SourceObj == nil {
		return ""
	}
	src := block.Source.SourceObj

	// Handle base64 data - convert to data URL
	if src.Data != nil {
		mime := "image/png"
		if src.MediaType != nil && *src.MediaType != "" {
			mime = *src.MediaType
		}
		return "data:" + mime + ";base64," + *src.Data
	}

	// Handle regular URLs
	if src.URL != nil {
		return *src.URL
	}

	return ""
}

// parseJSONInput returns a json.RawMessage that preserves the original key ordering
// of the JSON input. This is critical for prompt caching, which relies on exact
// byte-for-byte matching of the request prefix sent to providers.
func parseJSONInput(jsonStr string) json.RawMessage {
	if jsonStr == "" || jsonStr == "{}" {
		return json.RawMessage("{}")
	}

	// Compact removes insignificant whitespace while preserving key order.
	compacted := compactJSONBytes([]byte(jsonStr))
	if compacted != nil {
		return json.RawMessage(compacted)
	}

	// If compaction fails (invalid JSON), return json.RawMessage of the raw string
	return json.RawMessage(jsonStr)
}

// compactJSONBytes compacts JSON bytes, removing insignificant whitespace while
// preserving key ordering. Returns nil if the input is not valid JSON.
func compactJSONBytes(data []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return nil
	}
	return buf.Bytes()
}

// extractTypesFromValue extracts type strings from various formats (string, []string, []interface{})
func extractTypesFromValue(typeVal interface{}) []string {
	switch t := typeVal.(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []interface{}:
		types := make([]string, 0, len(t))
		for _, item := range t {
			if typeStr, ok := item.(string); ok {
				types = append(types, typeStr)
			}
		}
		return types
	default:
		return nil
	}
}

// filterEnumValuesByType filters enum values to only include those matching the specified JSON schema type.
// This ensures that when we split multi-type fields into anyOf branches, each branch only contains
// enum values compatible with its declared type.
func filterEnumValuesByType(enumValues []interface{}, schemaType string) []interface{} {
	if len(enumValues) == 0 {
		return nil
	}

	filtered := make([]interface{}, 0, len(enumValues))
	for _, val := range enumValues {
		// Determine the actual type of the enum value
		var actualType string
		switch val.(type) {
		case string:
			actualType = "string"
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			actualType = "integer"
		case float32, float64:
			// Check if it's actually an integer value in float form
			if fv, ok := val.(float64); ok && fv == float64(int64(fv)) {
				actualType = "integer"
			} else {
				actualType = "number"
			}
		case bool:
			actualType = "boolean"
		case nil:
			actualType = "null"
		default:
			// For other types (objects, arrays), include them in all branches
			filtered = append(filtered, val)
			continue
		}

		// Include the value if its type matches the schema type
		// Also handle "number" type which includes both integers and floats
		if actualType == schemaType || (schemaType == "number" && actualType == "integer") {
			filtered = append(filtered, val)
		}
	}

	return filtered
}

// NormalizeSchemaForAnthropic is the exported entry point for normalizeSchemaForAnthropic,
// used by providers (e.g. Bedrock) that share Anthropic's schema validation rules.
func NormalizeSchemaForAnthropic(schema map[string]interface{}) map[string]interface{} {
	return normalizeSchemaForAnthropic(schema)
}

// sjsonEscapeKey escapes characters that have special meaning in sjson path
// syntax. Necessary for property names that include such characters; for the
// common JSON Schema case (alphanumeric + underscore + $ + -) this is a no-op.
func sjsonEscapeKey(k string) string {
	if !strings.ContainsAny(k, `.*?#\`) {
		return k
	}
	var b strings.Builder
	b.Grow(len(k) + 2)
	for _, r := range k {
		switch r {
		case '.', '*', '?', '#', '\\':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// filterEnumValuesByTypeRaw is the gjson equivalent of filterEnumValuesByType.
// Object/array enum values pass through to all branches (matches the map
// version's default-case behavior).
func filterEnumValuesByTypeRaw(values []gjson.Result, schemaType string) []gjson.Result {
	if len(values) == 0 {
		return nil
	}
	out := make([]gjson.Result, 0, len(values))
	for _, v := range values {
		var actual string
		switch v.Type {
		case gjson.String:
			actual = "string"
		case gjson.Number:
			f := v.Float()
			if f == float64(int64(f)) {
				actual = "integer"
			} else {
				actual = "number"
			}
		case gjson.True, gjson.False:
			actual = "boolean"
		case gjson.Null:
			actual = "null"
		case gjson.JSON:
			out = append(out, v)
			continue
		default:
			continue
		}
		if actual == schemaType || (schemaType == "number" && actual == "integer") {
			out = append(out, v)
		}
	}
	return out
}

// NormalizeSchemaForAnthropicRaw is the json.RawMessage equivalent of
// NormalizeSchemaForAnthropic. Operates on raw JSON bytes throughout via
// sjson/gjson; produces functionally identical output to the map-based
// version. Use this when the caller already has the schema as raw bytes
// and wants to avoid a map round-trip.
func NormalizeSchemaForAnthropicRaw(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 {
		return schema
	}
	if !gjson.ParseBytes(schema).IsObject() {
		return schema
	}

	body := append([]byte(nil), schema...)

	if typeVal := gjson.GetBytes(body, "type"); typeVal.IsArray() {
		var types []string
		for _, t := range typeVal.Array() {
			types = append(types, t.String())
		}
		nonNullTypes := make([]string, 0, len(types))
		for _, t := range types {
			if t != "null" {
				nonNullTypes = append(nonNullTypes, t)
			}
		}

		switch {
		case len(nonNullTypes) == 0:
			body, _ = sjson.SetBytes(body, "type", "null")
		case len(nonNullTypes) == 1 && len(types) == 1:
			body, _ = sjson.SetBytes(body, "type", nonNullTypes[0])
		default:
			body, _ = sjson.DeleteBytes(body, "type")

			enumVal := gjson.GetBytes(body, "enum")
			hasEnum := enumVal.Exists() && enumVal.IsArray()
			var enumArr []gjson.Result
			if hasEnum {
				enumArr = enumVal.Array()
			}

			anyOf := []byte("[]")
			i := 0
			for _, t := range nonNullTypes {
				branch := []byte(`{}`)
				branch, _ = sjson.SetBytes(branch, "type", t)
				if hasEnum {
					filtered := filterEnumValuesByTypeRaw(enumArr, t)
					if len(filtered) > 0 {
						enumOut := []byte("[]")
						for j, v := range filtered {
							enumOut, _ = sjson.SetRawBytes(enumOut, fmt.Sprintf("%d", j), []byte(v.Raw))
						}
						branch, _ = sjson.SetRawBytes(branch, "enum", enumOut)
					}
				}
				anyOf, _ = sjson.SetRawBytes(anyOf, fmt.Sprintf("%d", i), branch)
				i++
			}
			if len(nonNullTypes) < len(types) {
				nullBranch, _ := sjson.SetBytes([]byte(`{}`), "type", "null")
				anyOf, _ = sjson.SetRawBytes(anyOf, fmt.Sprintf("%d", i), nullBranch)
			}
			body, _ = sjson.SetRawBytes(body, "anyOf", anyOf)
			body, _ = sjson.DeleteBytes(body, "enum")
		}
	}

	for _, key := range []string{"properties", "definitions", "$defs"} {
		val := gjson.GetBytes(body, key)
		if !val.IsObject() {
			continue
		}
		newObj := []byte("{}")
		val.ForEach(func(k, v gjson.Result) bool {
			child := []byte(v.Raw)
			if v.IsObject() {
				child = NormalizeSchemaForAnthropicRaw(child)
			}
			newObj, _ = sjson.SetRawBytes(newObj, sjsonEscapeKey(k.String()), child)
			return true
		})
		body, _ = sjson.SetRawBytes(body, sjsonEscapeKey(key), newObj)
	}

	if items := gjson.GetBytes(body, "items"); items.IsObject() {
		body, _ = sjson.SetRawBytes(body, "items", NormalizeSchemaForAnthropicRaw([]byte(items.Raw)))
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		arr := gjson.GetBytes(body, key)
		if !arr.IsArray() {
			continue
		}
		newArr := []byte("[]")
		for i, item := range arr.Array() {
			child := []byte(item.Raw)
			if item.IsObject() {
				child = NormalizeSchemaForAnthropicRaw(child)
			}
			newArr, _ = sjson.SetRawBytes(newArr, fmt.Sprintf("%d", i), child)
		}
		body, _ = sjson.SetRawBytes(body, key, newArr)
	}

	return body
}

// normalizeSchemaForAnthropic recursively normalizes a JSON schema to be compatible with Anthropic's API.
// This handles cases where:
// 1. type is an array like ["string", "null"] - converted to single type
// 2. type is an array with multiple types like ["string", "integer"] - converted to anyOf
// 3. Enums with nullable types need special handling
func normalizeSchemaForAnthropic(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	normalized := make(map[string]interface{})
	for k, v := range schema {
		normalized[k] = v
	}

	// Handle type field if it's an array (e.g., ["string", "null"] or ["string", "integer"])
	if typeVal, exists := normalized["type"]; exists {
		types := extractTypesFromValue(typeVal)
		if len(types) > 0 {
			nonNullTypes := make([]string, 0, len(types))
			for _, t := range types {
				if t != "null" {
					nonNullTypes = append(nonNullTypes, t)
				}
			}

			if len(nonNullTypes) == 0 {
				// Only null type
				normalized["type"] = "null"
			} else if len(nonNullTypes) == 1 && len(types) == 1 {
				// Single type, no null (e.g., ["string"])
				// Just use the single type
				normalized["type"] = nonNullTypes[0]
			} else {
				// Multiple types OR single type with null
				// Convert to anyOf structure for correctness
				// Examples: ["string", "null"], ["string", "integer"], ["string", "integer", "null"]
				delete(normalized, "type")

				// Build anyOf with each non-null type
				anyOfSchemas := make([]interface{}, 0, len(types))
				for _, t := range nonNullTypes {
					typeSchema := map[string]interface{}{"type": t}

					// If there's an enum, filter enum values by type for each anyOf branch
					if enumVal, hasEnum := normalized["enum"]; hasEnum {
						// Convert enum to []interface{} if it's []string or other slice type
						var enumArray []interface{}
						switch e := enumVal.(type) {
						case []interface{}:
							enumArray = e
						case []string:
							enumArray = make([]interface{}, len(e))
							for i, v := range e {
								enumArray[i] = v
							}
						default:
							// If enum is not a slice, skip filtering
							typeSchema["enum"] = enumVal
							anyOfSchemas = append(anyOfSchemas, typeSchema)
							continue
						}

						filteredEnum := filterEnumValuesByType(enumArray, t)
						if len(filteredEnum) > 0 {
							typeSchema["enum"] = filteredEnum
						}
					}

					anyOfSchemas = append(anyOfSchemas, typeSchema)
				}

				// If original had null, add it to anyOf
				if len(nonNullTypes) < len(types) {
					anyOfSchemas = append(anyOfSchemas, map[string]interface{}{"type": "null"})
				}

				normalized["anyOf"] = anyOfSchemas

				// Remove enum from top level since it's now in anyOf branches
				delete(normalized, "enum")
			}
		}
	}

	// Recursively normalize properties
	if properties, ok := schema["properties"].(map[string]interface{}); ok {
		newProps := make(map[string]interface{})
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]interface{}); ok {
				newProps[key] = normalizeSchemaForAnthropic(propMap)
			} else {
				newProps[key] = prop
			}
		}
		normalized["properties"] = newProps
	}

	// Recursively normalize items (for arrays)
	if items, ok := schema["items"].(map[string]interface{}); ok {
		normalized["items"] = normalizeSchemaForAnthropic(items)
	}

	// Recursively normalize anyOf
	if anyOf, ok := schema["anyOf"].([]interface{}); ok {
		newAnyOf := make([]interface{}, 0, len(anyOf))
		for _, item := range anyOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newAnyOf = append(newAnyOf, normalizeSchemaForAnthropic(itemMap))
			} else {
				newAnyOf = append(newAnyOf, item)
			}
		}
		normalized["anyOf"] = newAnyOf
	}

	// Recursively normalize oneOf
	if oneOf, ok := schema["oneOf"].([]interface{}); ok {
		newOneOf := make([]interface{}, 0, len(oneOf))
		for _, item := range oneOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newOneOf = append(newOneOf, normalizeSchemaForAnthropic(itemMap))
			} else {
				newOneOf = append(newOneOf, item)
			}
		}
		normalized["oneOf"] = newOneOf
	}

	// Recursively normalize allOf
	if allOf, ok := schema["allOf"].([]interface{}); ok {
		newAllOf := make([]interface{}, 0, len(allOf))
		for _, item := range allOf {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newAllOf = append(newAllOf, normalizeSchemaForAnthropic(itemMap))
			} else {
				newAllOf = append(newAllOf, item)
			}
		}
		normalized["allOf"] = newAllOf
	}

	// Recursively normalize definitions/defs
	if definitions, ok := schema["definitions"].(map[string]interface{}); ok {
		newDefs := make(map[string]interface{})
		for key, def := range definitions {
			if defMap, ok := def.(map[string]interface{}); ok {
				newDefs[key] = normalizeSchemaForAnthropic(defMap)
			} else {
				newDefs[key] = def
			}
		}
		normalized["definitions"] = newDefs
	}

	if defs, ok := schema["$defs"].(map[string]interface{}); ok {
		newDefs := make(map[string]interface{})
		for key, def := range defs {
			if defMap, ok := def.(map[string]interface{}); ok {
				newDefs[key] = normalizeSchemaForAnthropic(defMap)
			} else {
				newDefs[key] = def
			}
		}
		normalized["$defs"] = newDefs
	}

	return normalized
}

// convertChatResponseFormatToAnthropicOutputFormat converts OpenAI Chat Completions response_format
// to Anthropic's output_format structure.
//
// OpenAI Chat Completions format:
//
//	{
//	  "type": "json_schema",
//	  "json_schema": {
//	    "name": "MySchema",
//	    "schema": {...},
//	    "strict": true
//	  }
//	}
//
// Anthropic's expected format (per https://docs.claude.com/en/docs/build-with-claude/structured-outputs):
//
//	{
//	  "type": "json_schema",
//	  "name": "MySchema",
//	  "schema": {...},
//	  "strict": true
//	}
func convertChatResponseFormatToAnthropicOutputFormat(responseFormat *interface{}) json.RawMessage {
	if responseFormat == nil {
		return nil
	}

	formatMap, ok := (*responseFormat).(map[string]interface{})
	if !ok {
		return nil
	}

	formatType, ok := formatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	// Extract the nested json_schema object
	jsonSchemaObj, ok := formatMap["json_schema"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Build the flattened Anthropic-compatible output_format structure
	// Note: name, description, and strict are NOT included as they are not permitted
	// in Anthropic's GA structured outputs API (output_config.format)
	outputFormat := map[string]interface{}{
		"type": formatType,
	}

	if schema, ok := jsonSchemaObj["schema"].(map[string]interface{}); ok {
		// Normalize the schema to handle type arrays like ["string", "null"]
		normalizedSchema := normalizeSchemaForAnthropic(schema)
		outputFormat["schema"] = normalizedSchema
	}

	result, err := providerUtils.MarshalSorted(outputFormat)
	if err != nil {
		return nil
	}
	return json.RawMessage(result)
}

// convertResponsesTextConfigToAnthropicOutputFormat converts OpenAI Responses API text config
// to Anthropic's output_format structure.
//
// OpenAI Responses API format:
//
//	{
//	  "text": {
//	    "format": {
//	      "type": "json_schema",
//	      "schema": {...}
//	    }
//	  }
//	}
//
// Anthropic's expected format (per https://docs.claude.com/en/docs/build-with-claude/structured-outputs):
//
//	{
//	  "type": "json_schema",
//	  "schema": {...}
//	}
func convertResponsesTextConfigToAnthropicOutputFormat(textConfig *schemas.ResponsesTextConfig) json.RawMessage {
	if textConfig == nil || textConfig.Format == nil {
		return nil
	}

	format := textConfig.Format
	// Anthropic currently only supports json_schema type
	if format.Type != "json_schema" {
		return nil
	}

	// Build the Anthropic-compatible output_format structure
	outputFormat := map[string]interface{}{
		"type": format.Type,
	}

	if format.JSONSchema != nil {
		// Convert the schema structure
		schema := map[string]interface{}{}

		if format.JSONSchema.Type != nil {
			schema["type"] = *format.JSONSchema.Type
		}

		if format.JSONSchema.Properties != nil {
			schema["properties"] = *format.JSONSchema.Properties
		}

		if len(format.JSONSchema.Required) > 0 {
			schema["required"] = format.JSONSchema.Required
		}

		if format.JSONSchema.Type != nil && *format.JSONSchema.Type == "object" {
			schema["additionalProperties"] = false
		} else if format.JSONSchema.AdditionalProperties != nil {
			schema["additionalProperties"] = *format.JSONSchema.AdditionalProperties
		}

		// Normalize the schema to handle type arrays like ["string", "null"]
		normalizedSchema := normalizeSchemaForAnthropic(schema)
		outputFormat["schema"] = normalizedSchema
	}

	result, err := providerUtils.MarshalSorted(outputFormat)
	if err != nil {
		return nil
	}
	return json.RawMessage(result)
}

// convertAnthropicOutputFormatToResponsesTextConfig converts Anthropic's output_format structure
// to OpenAI Responses API text config.
//
// Anthropic format:
//
//	{
//	  "type": "json_schema",
//	  "schema": {...},
//	}
//
// OpenAI Responses API format:
//
//	{
//	  "text": {
//	    "format": {
//	      "type": "json_schema",
//	      "json_schema": {...},
//	      "name": "...",
//	      "strict": true
//	    }
//	  }
//	}
func convertAnthropicOutputFormatToResponsesTextConfig(outputFormat json.RawMessage) *schemas.ResponsesTextConfig {
	if outputFormat == nil {
		return nil
	}

	// Unmarshal to map
	var formatMap map[string]interface{}
	if err := sonic.Unmarshal(outputFormat, &formatMap); err != nil {
		return nil
	}

	// Extract type
	formatType, ok := formatMap["type"].(string)
	if !ok || formatType != "json_schema" {
		return nil
	}

	format := &schemas.ResponsesTextConfigFormat{
		Type: formatType,
	}

	// Extract name if present
	if name, ok := formatMap["name"].(string); ok && strings.TrimSpace(name) != "" {
		format.Name = schemas.Ptr(strings.TrimSpace(name))
	} else {
		format.Name = schemas.Ptr("output_format")
	}

	// Extract schema if present
	if schemaMap, ok := formatMap["schema"].(map[string]interface{}); ok {
		jsonSchema := &schemas.ResponsesTextConfigFormatJSONSchema{}

		if schemaType, ok := schemaMap["type"].(string); ok {
			jsonSchema.Type = &schemaType
		}

		if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
			jsonSchema.Properties = &properties
		}

		if required, ok := schemaMap["required"].([]interface{}); ok {
			requiredStrs := make([]string, 0, len(required))
			for _, r := range required {
				if rStr, ok := r.(string); ok {
					requiredStrs = append(requiredStrs, rStr)
				}
			}
			if len(requiredStrs) > 0 {
				jsonSchema.Required = requiredStrs
			}
		}

		if additionalProps, ok := schemaMap["additionalProperties"].(bool); ok {
			jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesBool: &additionalProps,
			}
		}

		if additionalProps, ok := schemas.SafeExtractOrderedMap(schemaMap["additionalProperties"]); ok {
			jsonSchema.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
				AdditionalPropertiesMap: additionalProps,
			}
		}

		// Extract description
		if description, ok := schemaMap["description"].(string); ok {
			jsonSchema.Description = &description
		}

		// Extract $defs (JSON Schema draft 2019-09+)
		if defs, ok := schemaMap["$defs"].(map[string]interface{}); ok {
			jsonSchema.Defs = &defs
		}

		// Extract definitions (legacy JSON Schema draft-07)
		if definitions, ok := schemaMap["definitions"].(map[string]interface{}); ok {
			jsonSchema.Definitions = &definitions
		}

		// Extract $ref
		if ref, ok := schemaMap["$ref"].(string); ok {
			jsonSchema.Ref = &ref
		}

		// Extract items (array element schema)
		if items, ok := schemaMap["items"].(map[string]interface{}); ok {
			jsonSchema.Items = &items
		}

		// Extract minItems
		if minItems, ok := anthropicExtractInt64(schemaMap["minItems"]); ok {
			jsonSchema.MinItems = &minItems
		}

		// Extract maxItems
		if maxItems, ok := anthropicExtractInt64(schemaMap["maxItems"]); ok {
			jsonSchema.MaxItems = &maxItems
		}

		// Extract anyOf
		if anyOf, ok := schemaMap["anyOf"].([]interface{}); ok {
			anyOfMaps := make([]map[string]any, 0, len(anyOf))
			for _, item := range anyOf {
				if m, ok := item.(map[string]interface{}); ok {
					anyOfMaps = append(anyOfMaps, m)
				}
			}
			if len(anyOfMaps) > 0 {
				jsonSchema.AnyOf = anyOfMaps
			}
		}

		// Extract oneOf
		if oneOf, ok := schemaMap["oneOf"].([]interface{}); ok {
			oneOfMaps := make([]map[string]any, 0, len(oneOf))
			for _, item := range oneOf {
				if m, ok := item.(map[string]interface{}); ok {
					oneOfMaps = append(oneOfMaps, m)
				}
			}
			if len(oneOfMaps) > 0 {
				jsonSchema.OneOf = oneOfMaps
			}
		}

		// Extract allOf
		if allOf, ok := schemaMap["allOf"].([]interface{}); ok {
			allOfMaps := make([]map[string]any, 0, len(allOf))
			for _, item := range allOf {
				if m, ok := item.(map[string]interface{}); ok {
					allOfMaps = append(allOfMaps, m)
				}
			}
			if len(allOfMaps) > 0 {
				jsonSchema.AllOf = allOfMaps
			}
		}

		// Extract format
		if formatVal, ok := schemaMap["format"].(string); ok {
			jsonSchema.Format = &formatVal
		}

		// Extract pattern
		if pattern, ok := schemaMap["pattern"].(string); ok {
			jsonSchema.Pattern = &pattern
		}

		// Extract minLength
		if minLength, ok := anthropicExtractInt64(schemaMap["minLength"]); ok {
			jsonSchema.MinLength = &minLength
		}

		// Extract maxLength
		if maxLength, ok := anthropicExtractInt64(schemaMap["maxLength"]); ok {
			jsonSchema.MaxLength = &maxLength
		}

		// Extract minimum
		if minimum, ok := anthropicExtractFloat64(schemaMap["minimum"]); ok {
			jsonSchema.Minimum = &minimum
		}

		// Extract maximum
		if maximum, ok := anthropicExtractFloat64(schemaMap["maximum"]); ok {
			jsonSchema.Maximum = &maximum
		}

		// Extract title
		if title, ok := schemaMap["title"].(string); ok {
			jsonSchema.Title = &title
		}

		// Extract default
		if defaultVal, exists := schemaMap["default"]; exists {
			jsonSchema.Default = defaultVal
		}

		// Extract nullable
		if nullable, ok := schemaMap["nullable"].(bool); ok {
			jsonSchema.Nullable = &nullable
		}

		// Extract enum
		if enum, ok := schemaMap["enum"].([]interface{}); ok {
			enumStrs := make([]string, 0, len(enum))
			for _, e := range enum {
				if str, ok := e.(string); ok {
					enumStrs = append(enumStrs, str)
				}
			}
			if len(enumStrs) > 0 {
				jsonSchema.Enum = enumStrs
			}
		} else if enumStrs, ok := schemaMap["enum"].([]string); ok && len(enumStrs) > 0 {
			jsonSchema.Enum = enumStrs
		}

		format.JSONSchema = jsonSchema
	}

	return &schemas.ResponsesTextConfig{
		Format: format,
	}
}

// sanitizeWebSearchArguments sanitizes WebSearch tool arguments by removing conflicting domain filters.
// Anthropic only allows one of allowed_domains or blocked_domains, not both.
// This function handles empty and non-empty arrays:
// - If one array is empty, delete that one
// - If both arrays are filled, delete blocked_domains
// - If both arrays are empty, delete blocked_domains
func sanitizeWebSearchArguments(argumentsJSON string) string {
	var toolArgs map[string]interface{}
	if err := sonic.Unmarshal([]byte(argumentsJSON), &toolArgs); err != nil {
		return argumentsJSON // Return original if parse fails
	}

	allowedVal, hasAllowed := toolArgs["allowed_domains"]
	blockedVal, hasBlocked := toolArgs["blocked_domains"]

	// Only process if both fields exist
	if hasAllowed && hasBlocked {
		// Helper function to check if array is empty
		isEmptyArray := func(val interface{}) bool {
			if arr, ok := val.([]interface{}); ok {
				return len(arr) == 0
			}
			return false
		}

		allowedEmpty := isEmptyArray(allowedVal)
		blockedEmpty := isEmptyArray(blockedVal)

		var shouldDelete string
		if allowedEmpty && !blockedEmpty {
			// Delete allowed_domains if it's empty and blocked is not
			shouldDelete = "allowed_domains"
		} else if blockedEmpty && !allowedEmpty {
			// Delete blocked_domains if it's empty and allowed is not
			shouldDelete = "blocked_domains"
		} else {
			// Both are filled or both are empty: delete blocked_domains
			shouldDelete = "blocked_domains"
		}

		delete(toolArgs, shouldDelete)

		// Re-marshal the sanitized arguments
		if sanitizedBytes, err := providerUtils.MarshalSorted(toolArgs); err == nil {
			return string(sanitizedBytes)
		}
	}

	return argumentsJSON
}

// attachWebSearchSourcesToCall finds a web_search_call by tool_use_id and attaches sources to it.
// It searches backwards through bifrostMessages to find the matching call and updates its action.
func attachWebSearchSourcesToCall(bifrostMessages []schemas.ResponsesMessage, toolUseID string, resultBlock AnthropicContentBlock, includeExtendedFields bool) {
	// Search backwards to find matching web_search_call
	for i := len(bifrostMessages) - 1; i >= 0; i-- {
		msg := &bifrostMessages[i]
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeWebSearchCall &&
			msg.ID != nil &&
			*msg.ID == toolUseID {

			if msg.ResponsesToolMessage == nil {
				msg.ResponsesToolMessage = &schemas.ResponsesToolMessage{}
			}

			// Found the matching web_search_call, add sources
			if resultBlock.Content != nil && len(resultBlock.Content.ContentBlocks) > 0 {
				sources := extractWebSearchSources(resultBlock.Content.ContentBlocks, includeExtendedFields)

				// Initialize action if needed
				if msg.ResponsesToolMessage.Action == nil {
					msg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{}
				}
				if msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction == nil {
					msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction = &schemas.ResponsesWebSearchToolCallAction{
						Type: "search",
					}
				}
				msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources = sources
			}
			break
		}
	}
}

// extractWebSearchSources extracts search sources from Anthropic content blocks.
// When includeExtendedFields is true, it includes EncryptedContent, PageAge, and Title fields.
func extractWebSearchSources(contentBlocks []AnthropicContentBlock, includeExtendedFields bool) []schemas.ResponsesWebSearchToolCallActionSearchSource {
	sources := make([]schemas.ResponsesWebSearchToolCallActionSearchSource, 0, len(contentBlocks))

	for _, result := range contentBlocks {
		if result.Type == AnthropicContentBlockTypeWebSearchResult && result.URL != nil {
			source := schemas.ResponsesWebSearchToolCallActionSearchSource{
				Type: "url",
				URL:  *result.URL,
			}

			if includeExtendedFields {
				source.EncryptedContent = result.EncryptedContent
				source.PageAge = result.PageAge

				if result.Title != nil {
					source.Title = result.Title
				} else {
					source.Title = schemas.Ptr(*result.URL)
				}
			}

			sources = append(sources, source)
		}
	}

	return sources
}

// anthropicExtractInt64 extracts an int64 from various numeric types
func anthropicExtractInt64(v interface{}) (int64, bool) {
	switch val := v.(type) {
	case int:
		return int64(val), true
	case int64:
		return val, true
	case float64:
		return int64(val), true
	case float32:
		return int64(val), true
	default:
		return 0, false
	}
}

// anthropicExtractFloat64 extracts a float64 from various numeric types
func anthropicExtractFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}

// IsClaudeCodeMaxMode checks if the request is a Claude Code max mode request.
// In the max mode - we don't need to forward the key
func IsClaudeCodeMaxMode(ctx *schemas.BifrostContext) bool {
	userAgent, _ := ctx.Value(schemas.BifrostContextKeyUserAgent).(string)
	skipKeySelection, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool)
	return schemas.ClaudeCLI.Matches(userAgent) && skipKeySelection
}

// IsClaudeCodeRequest checks if the request is a Claude Code request.
func IsClaudeCodeRequest(ctx *schemas.BifrostContext) bool {
	if userAgent, ok := ctx.Value(schemas.BifrostContextKeyUserAgent).(string); ok {
		return schemas.ClaudeCLI.Matches(userAgent)
	}
	return false
}
