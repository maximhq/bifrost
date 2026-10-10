package openai

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostResponsesRequest converts an OpenAI responses request to Bifrost format
func (resp *OpenAIResponsesRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	if resp == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(resp.Model, "")

	input := resp.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: resp.Input.OpenAIResponsesRequestInputStr},
			},
		}
	}

	return &schemas.BifrostResponsesRequest{
		Provider:  provider,
		Model:     model,
		Input:     input,
		Params:    &resp.ResponsesParameters,
		Fallbacks: schemas.ParseFallbacks(resp.Fallbacks),
	}
}

// ResponsesFeatureSupport describes which OpenAI Responses wire extensions an
// OpenAI-compatible backend accepts. Providers absent from ProviderFeatures keep
// everything (safe default for custom providers), matching the Anthropic matrix.
type ResponsesFeatureSupport struct {
	// AdditionalToolsItem reports whether the backend accepts codex
	// `additional_tools` input items; when false their tools are hoisted into
	// the top-level tools param instead.
	AdditionalToolsItem bool
	// ContextManagement reports whether the backend accepts the context_management
	// Responses body field.
	ContextManagement bool
	// WebSearchContentTypes reports whether the backend accepts search_content_types
	// on web_search tools.
	WebSearchContentTypes bool
}

// ProviderFeatures maps each OpenAI-compatible provider to its supported
// Responses wire extensions. Only providers with a known deviation are listed.
var ProviderFeatures = map[schemas.ModelProvider]ResponsesFeatureSupport{
	schemas.OpenAI: {AdditionalToolsItem: true, ContextManagement: true, WebSearchContentTypes: true},
	// Bedrock Mantle validates `input` against the standard union and rejects
	// additional_tools with "Invalid 'input': value did not match any expected
	// variant", but accepts the same tools at the top level. It also rejects
	// context_management outright as an unknown parameter.
	schemas.Bedrock:       {AdditionalToolsItem: false, ContextManagement: false},
	schemas.BedrockMantle: {AdditionalToolsItem: false, ContextManagement: false},
}

// reservedToolNamespaces is the hardcoded fallback for the namespace-tool names a
// provider keeps for its own server-side tools, used when the datasheet row for the
// (base provider, model) publishes no reserved_tool_namespaces. Bedrock rejects a
// user-defined namespace with one of these names outright on both the
// bedrock-mantle and bedrock-runtime Responses endpoints: "Invalid Value:
// 'tools.namespace'. User-defined namespace 'web' collides with an existing tool
// namespace." (HTTP 400). Codex registers a client-side "web" namespace (web.run)
// whenever it believes it is talking to OpenAI, which is the case when Bifrost is
// configured through openai_base_url. Live-verified against openai.gpt-5.6-luna on
// 2026-09-09.
var reservedToolNamespaces = map[schemas.ModelProvider][]string{
	schemas.Bedrock:       {"web", "image_gen", "browser", "python"},
	schemas.BedrockMantle: {"web", "image_gen", "browser", "python"},
}

// resolveReservedToolNamespaces returns the reserved-name set for one attempt:
// the datasheet row for (toolProvider, capModel) when it publishes one, else the
// hardcoded fallback. toolProvider is the BASE provider, so a custom provider
// wrapping Mantle reads the bedrock_mantle row and the bedrock_mantle fallback.
func resolveReservedToolNamespaces(toolProvider schemas.ModelProvider, capModel string) map[string]bool {
	names := schemas.ResolveModelCaps(toolProvider, capModel).ReservedToolNamespaces(reservedToolNamespaces[toolProvider])
	if len(names) == 0 {
		return nil
	}
	reserved := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			reserved[name] = true
		}
	}
	return reserved
}

// dropReservedNamespaceTools returns a copy of tools without the namespace tools
// the provider reserves. A dropped "web" namespace is Codex's client-side web.run
// tool, which only executes against OpenAI's search endpoint anyway. When
// substituteWebSearch is set (bedrock-mantle, which hosts web search) and the
// caller sent no web_search tool, the hosted tool is appended with
// external_web_access=false, matching what Codex sends to non-OpenAI providers and
// what AWS documents for Codex on Mantle
// (https://docs.aws.amazon.com/bedrock/latest/userguide/web-search.html).
func dropReservedNamespaceTools(tools []schemas.ResponsesTool, reserved map[string]bool, substituteWebSearch bool) []schemas.ResponsesTool {
	out := make([]schemas.ResponsesTool, 0, len(tools)+1)
	droppedWeb, hasWebSearch := false, false
	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeWebSearch {
			hasWebSearch = true
		}
		if tool.Type == schemas.ResponsesToolTypeNamespace && tool.Name != nil && reserved[*tool.Name] {
			if *tool.Name == "web" {
				droppedWeb = true
			}
			continue
		}
		out = append(out, tool)
	}
	if substituteWebSearch && droppedWeb && !hasWebSearch {
		out = append(out, schemas.ResponsesTool{
			Type:                   schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{ExternalWebAccess: new(false)},
		})
	}
	return out
}

// supportsAdditionalToolsItem reports whether provider accepts codex
// additional_tools input items. Unlisted providers are assumed to.
func supportsAdditionalToolsItem(provider schemas.ModelProvider) bool {
	features, ok := ProviderFeatures[provider]
	if !ok {
		return true
	}
	return features.AdditionalToolsItem
}

// supportsWebSearchContentTypes reports whether a model accepts
// search_content_types on web_search tools. The datasheet overrides the
// provider default; unlisted providers are assumed to support it.
func supportsWebSearchContentTypes(caps schemas.ModelCaps, provider schemas.ModelProvider) bool {
	features, ok := ProviderFeatures[provider]
	if !ok {
		return !caps.FieldUnsupported(schemas.FieldSearchContentTypes, false)
	}
	return !caps.FieldUnsupported(schemas.FieldSearchContentTypes, !features.WebSearchContentTypes)
}

// hoistAdditionalTools decodes the tools carried by a codex additional_tools item.
// The entries are ResponsesTool-shaped but live in the item's preserved raw bytes,
// so they are decoded here rather than read off the typed message.
func hoistAdditionalTools(message schemas.ResponsesMessage) []schemas.ResponsesTool {
	if len(message.AdditionalTools) == 0 {
		return nil
	}
	var tools []schemas.ResponsesTool
	if err := schemas.Unmarshal(message.AdditionalTools, &tools); err != nil {
		return nil
	}
	return tools
}

// normalizeAsyncTools keeps OpenAI's async marker only where the API accepts it:
// on function/custom tool definitions for models that support async tool calling.
// Namespace children are normalized recursively. Every modified node is copied so
// conversion never mutates the caller-owned request.
func normalizeAsyncTools(tools []schemas.ResponsesTool, supported bool) []schemas.ResponsesTool {
	if len(tools) == 0 {
		return tools
	}

	normalized := make([]schemas.ResponsesTool, len(tools))
	for i, tool := range tools {
		if !supported || (tool.Type != schemas.ResponsesToolTypeFunction && tool.Type != schemas.ResponsesToolTypeCustom) {
			tool.Async = nil
		}
		if tool.ResponsesToolNamespace != nil && len(tool.ResponsesToolNamespace.Tools) > 0 {
			namespaceCopy := *tool.ResponsesToolNamespace
			namespaceCopy.Tools = normalizeAsyncTools(tool.ResponsesToolNamespace.Tools, supported)
			tool.ResponsesToolNamespace = &namespaceCopy
		}
		normalized[i] = tool
	}
	return normalized
}

// normalizeAsyncCallItems applies the same capability gate to replayed calls.
// OpenAI defines async only on function_call and custom_tool_call items.
func normalizeAsyncCallItems(messages []schemas.ResponsesMessage, supported bool) []schemas.ResponsesMessage {
	for i := range messages {
		message := &messages[i]
		if message.ResponsesToolMessage == nil || message.ResponsesToolMessage.Async == nil {
			continue
		}
		allowedType := message.Type != nil &&
			(*message.Type == schemas.ResponsesMessageTypeFunctionCall ||
				*message.Type == schemas.ResponsesMessageTypeCustomToolCall)
		if supported && allowedType {
			continue
		}

		toolMessageCopy := *message.ResponsesToolMessage
		toolMessageCopy.Async = nil
		message.ResponsesToolMessage = &toolMessageCopy
	}
	return messages
}

// PromptCacheBreakpointModeExplicit is the only mode a prompt_cache_breakpoint
// accepts. OpenAI defined the field for gpt-5.6+; OpenRouter reuses it as the
// Responses-shaped spelling of an Anthropic cache breakpoint.
const PromptCacheBreakpointModeExplicit = "explicit"

// maxResponsesCacheBreakpoints mirrors the ceiling the Anthropic Messages API
// enforces on blocks carrying cache_control. Exceeding it is a hard rejection
// rather than a degradation ("A maximum of 4 blocks with cache_control may be
// provided. Found 5."), and it binds here because OpenRouter converts every
// prompt_cache_breakpoint it receives back into an Anthropic breakpoint before
// dispatching to Claude. Kept local rather than imported from the anthropic
// provider so this package takes on no provider-to-provider dependency; see
// AnthropicMaxCacheBreakpoints in core/providers/anthropic/utils.go for the
// live verification behind the number.
const maxResponsesCacheBreakpoints = 4

// responsesUsesPromptCacheBreakpoints reports whether this provider and model take
// caching intent as prompt_cache_breakpoint on a content block rather than as
// Anthropic-style per-block cache_control.
//
// Two unrelated families landed on the same field. OpenRouter does not expose
// per-block cache_control through /v1/responses and converts a breakpoint back into
// an Anthropic one (#6290). OpenAI defined the field for gpt-5.6 and later, where it pairs with
// request-level prompt_cache_options; Azure and Bedrock Mantle serve the same models
// through the same wire format, so they inherit it (#6180). Bedrock is listed for the
// same reason: its OpenAI-compatible surfaces on both hosts speak that wire format, and
// a request there reports the bedrock key rather than bedrock_mantle.
//
// Everything else either accepts cache_control directly or caches implicitly, and for
// those the serializer's existing strip is the correct behaviour.
func responsesUsesPromptCacheBreakpoints(caps schemas.ModelCaps, provider schemas.ModelProvider, model string) bool {
	switch provider {
	case schemas.OpenRouter:
		return caps.SupportsPromptCacheBreakpoints(
			schemas.IsAnthropicModel(model) || schemas.ModelSupportsPromptCacheBreakpoint(model),
		)
	case schemas.OpenAI, schemas.Azure, schemas.BedrockMantle, schemas.Bedrock:
		return caps.SupportsPromptCacheBreakpoint(schemas.ModelSupportsPromptCacheBreakpoint(model))
	default:
		return false
	}
}

// responsesHasPromptCacheBreakpoint reports whether any content block carries a
// breakpoint. Used to decide whether explicit cache mode is warranted: switching a
// request to explicit mode with no breakpoint anywhere opts it out of caching
// entirely, which is strictly worse than the implicit default it replaced.
func responsesHasPromptCacheBreakpoint(messages []schemas.ResponsesMessage) bool {
	for i := range messages {
		if messages[i].Content != nil {
			for j := range messages[i].Content.ContentBlocks {
				if messages[i].Content.ContentBlocks[j].PromptCacheBreakpoint != nil {
					return true
				}
			}
		}
		if blocks := responsesToolOutputBlocks(&messages[i]); blocks != nil {
			for j := range blocks {
				if blocks[j].PromptCacheBreakpoint != nil {
					return true
				}
			}
		}
	}
	return false
}

// responsesToolOutputBlocks returns the block-form output of a function_call_output
// item, or nil when the item is not one or its output is a bare string.
func responsesToolOutputBlocks(msg *schemas.ResponsesMessage) []schemas.ResponsesMessageContentBlock {
	if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.Output == nil {
		return nil
	}
	return msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
}

// isMarkableResponsesInputBlock reports whether a message content block has a
// documented home for prompt_cache_breakpoint. input_text is documented by both
// OpenAI and OpenRouter; input_image and input_file only by OpenAI.
func isMarkableResponsesInputBlock(b schemas.ResponsesMessageContentBlock, openAIFamily bool) bool {
	switch b.Type {
	case schemas.ResponsesInputMessageContentBlockTypeText:
		return b.Text != nil
	case schemas.ResponsesInputMessageContentBlockTypeImage, schemas.ResponsesInputMessageContentBlockTypeFile:
		return openAIFamily
	}
	return false
}

// isMarkableResponsesToolOutputBlock is the function_call_output counterpart:
// OpenAI documents text, image and file parts there. The Anthropic converter
// tags a tool_result text block as output_text on assistant turns, so that
// spelling counts as text too.
func isMarkableResponsesToolOutputBlock(b schemas.ResponsesMessageContentBlock) bool {
	if b.Type == schemas.ResponsesOutputMessageContentTypeText {
		return b.Text != nil
	}
	return isMarkableResponsesInputBlock(b, true)
}

// responsesUsesPromptCacheOptions reports whether the target also needs request-level
// prompt_cache_options to honour an explicit breakpoint.
//
// This is the OpenAI half only (gpt-5.6 and later). Those models default to IMPLICIT caching, which puts
// the breakpoint on the latest message - so an agent loop rewrites the whole growing
// prompt every turn at the cache-write rate. The block marker alone does not switch
// that off; mode=explicit does. OpenRouter has no equivalent field and needs none.
func responsesUsesPromptCacheOptions(caps schemas.ModelCaps, provider schemas.ModelProvider, model string) bool {
	switch provider {
	case schemas.OpenAI, schemas.Azure, schemas.BedrockMantle, schemas.Bedrock:
		return caps.SupportsPromptCacheBreakpoint(schemas.ModelSupportsPromptCacheBreakpoint(model))
	default:
		return false
	}
}

// applyResponsesCacheBreakpoints rewrites Anthropic-style per-block cache_control
// markers into the prompt_cache_breakpoint representation the target actually accepts.
//
// OpenRouter does not expose per-block cache_control through /v1/responses. The
// documented equivalent is prompt_cache_breakpoint on an individual input_text
// block, which OpenRouter converts back into a default Anthropic breakpoint when
// the request routes to Claude:
// https://openrouter.ai/docs/features/prompt-caching#anthropic-claude
//
// The gpt-5.6 family reaches the same field from the other direction: OpenAI defined
// prompt_cache_breakpoint natively, and callers arriving through an Anthropic-shaped
// surface (or through breakpoint injection, which writes the neutral marker) express
// the same intent as cache_control. Translating here means one implementation serves
// both, and callers do not have to know which dialect their target speaks.
//
// Without this, OpenAIResponsesRequestInput.MarshalJSON deletes the marker and
// puts nothing in its place, so Claude prompt caching never activates on the
// Responses path (#6290). The Chat path needs no equivalent: OpenRouter accepts
// cache_control verbatim there, so OpenAIChatRequest.MarshalJSON simply keeps it
// behind its keepCacheControl branch (#4203).
//
// Two deliberate limits, both forced by the wire format rather than chosen:
//
//   - Only input_text blocks are marked. The OpenRouter doc places the
//     breakpoint on a text content block and names input_text as its Responses
//     spelling. Marking output_text would be an unverified capability claim, and
//     a rejected field costs more than the miss it would fix.
//   - Tool definitions are not marked. schemas.ResponsesTool has no
//     PromptCacheBreakpoint field and neither target documents a tool-level
//     Responses breakpoint.
//   - input_image and input_file blocks, and function_call_output items, are
//     marked only when openAIFamily is set, which the caller derives from the
//     OpenAI half of the gate. OpenAI's Responses reference accepts
//     prompt_cache_breakpoint on input_text, input_image and input_file parts,
//     both in a message and inside a function_call_output; OpenRouter documents
//     only input_text. The tool-output case is the turn Claude Code produces
//     after every tool call: its marker sits on the tool_result, which the
//     Anthropic converter carries as message-level CacheControl (string body) or
//     as a marker on an output block. A bare string body is promoted to a single
//     input_text block so the marker has somewhere to live, and
//     isFunctionCallOutputBlocksFlattenable then declines to collapse it back
//     into a string.
//
// A marker's TTL is not carried across, and there is nowhere to carry it to. The
// only request-level field on this path is prompt_cache_options.ttl, whose "only
// supported value, 30m, is also the default"
// (https://developers.openai.com/api/docs/guides/prompt-caching) - so writing it
// changes nothing, while the values cache_control actually carries (absent for 5m,
// or "1h": https://platform.claude.com/docs/en/build-with-claude/prompt-caching) are
// rejected there outright. OpenRouter, the one target where a "1h" marker would mean
// something, exposes no equivalent field at all, so its Claude requests fall back to
// the default breakpoint lifetime. Another limit forced by the wire format.
//
// messages is this function's own slice, but each element's Content pointer and
// the block array beneath it still alias bifrostReq.Input, which plugins and the
// fallback chain reuse. Both are copied before a marker is written.
func applyResponsesCacheBreakpoints(messages []schemas.ResponsesMessage, openAIFamily bool) {
	// Locate every markable block in render order, and count the breakpoints the
	// caller already set, before anything is copied. A toolOutput ref addresses a
	// function_call_output item: block is the index into its output blocks, or -1
	// when the output is a bare string that must be promoted first.
	type blockRef struct {
		msg, block int
		toolOutput bool
	}
	var refs []blockRef
	existing := 0
	// "ephemeral" is the only cache type Anthropic defines, and nothing upstream
	// of here validates it. Converting an empty or unknown type would manufacture
	// a valid breakpoint out of a malformed marker and spend clamp budget on it,
	// so require the documented value.
	isEphemeral := func(cc *schemas.CacheControl) bool {
		return cc != nil && cc.Type == schemas.CacheControlTypeEphemeral
	}
	for i := range messages {
		if messages[i].Content != nil {
			for j, block := range messages[i].Content.ContentBlocks {
				if block.PromptCacheBreakpoint != nil {
					existing++
					continue
				}
				if !isEphemeral(block.CacheControl) || !isMarkableResponsesInputBlock(block, openAIFamily) {
					continue
				}
				refs = append(refs, blockRef{msg: i, block: j})
			}
		}
		if messages[i].Type == nil || *messages[i].Type != schemas.ResponsesMessageTypeFunctionCallOutput ||
			messages[i].ResponsesToolMessage == nil || messages[i].ResponsesToolMessage.Output == nil {
			continue
		}
		output := messages[i].ResponsesToolMessage.Output
		if blocks := output.ResponsesFunctionToolCallOutputBlocks; blocks != nil {
			// A marker already on a block counts against the budget whether or
			// not this target may mark tool outputs.
			marked := -1
			for j, block := range blocks {
				if block.PromptCacheBreakpoint != nil {
					existing++
					continue
				}
				if openAIFamily && isEphemeral(block.CacheControl) && isMarkableResponsesToolOutputBlock(block) {
					marked = j
				}
			}
			if marked < 0 && openAIFamily && isEphemeral(messages[i].CacheControl) {
				// Message-level marker with a block body: mark the last markable
				// block, which is the end of the item and so the end of the cached prefix.
				for j := len(blocks) - 1; j >= 0; j-- {
					if isMarkableResponsesToolOutputBlock(blocks[j]) && blocks[j].PromptCacheBreakpoint == nil {
						marked = j
						break
					}
				}
			}
			if marked >= 0 {
				refs = append(refs, blockRef{msg: i, block: marked, toolOutput: true})
			}
		} else if openAIFamily && output.ResponsesToolCallOutputStr != nil && isEphemeral(messages[i].CacheControl) {
			refs = append(refs, blockRef{msg: i, block: -1, toolOutput: true})
		}
	}
	if len(refs) == 0 {
		return
	}

	// Spend only the budget the caller's own breakpoints leave behind. A caller
	// who sets more than the ceiling by hand owns that request and its upstream
	// error; Bifrost declines to manufacture one on top.
	budget := maxResponsesCacheBreakpoints - existing
	if budget <= 0 {
		return
	}
	// Caching is cumulative up to each breakpoint, so a marker later in render
	// order anchors a strictly longer prefix. When over budget, drop the
	// EARLIEST markers and keep the longest cached prefix, matching the
	// trade-off clampAnthropicCacheBreakpoints makes for the direct path.
	if len(refs) > budget {
		refs = refs[len(refs)-budget:]
	}

	breakpoint := func() *schemas.PromptCacheBreakpoint {
		return &schemas.PromptCacheBreakpoint{Mode: schemas.Ptr(PromptCacheBreakpointModeExplicit)}
	}
	copiedContent := make(map[int]bool, len(refs))
	for _, ref := range refs {
		if ref.toolOutput {
			// Copy the tool message, its output struct and the block slice: all
			// three still alias bifrostReq.Input. A message with one ref here never
			// also has a content ref, so no second copy map is needed.
			toolMsgCopy := *messages[ref.msg].ResponsesToolMessage
			outputCopy := *toolMsgCopy.Output
			if ref.block < 0 {
				text := *outputCopy.ResponsesToolCallOutputStr
				outputCopy.ResponsesToolCallOutputStr = nil
				outputCopy.ResponsesFunctionToolCallOutputBlocks = []schemas.ResponsesMessageContentBlock{{
					Type:                  schemas.ResponsesInputMessageContentBlockTypeText,
					Text:                  &text,
					PromptCacheBreakpoint: breakpoint(),
				}}
			} else {
				blocks := outputCopy.ResponsesFunctionToolCallOutputBlocks
				outputCopy.ResponsesFunctionToolCallOutputBlocks = append(
					make([]schemas.ResponsesMessageContentBlock, 0, len(blocks)), blocks...)
				outputCopy.ResponsesFunctionToolCallOutputBlocks[ref.block].PromptCacheBreakpoint = breakpoint()
			}
			toolMsgCopy.Output = &outputCopy
			messages[ref.msg].ResponsesToolMessage = &toolMsgCopy
			continue
		}
		if !copiedContent[ref.msg] {
			contentCopy := *messages[ref.msg].Content
			contentCopy.ContentBlocks = append(
				make([]schemas.ResponsesMessageContentBlock, 0, len(messages[ref.msg].Content.ContentBlocks)),
				messages[ref.msg].Content.ContentBlocks...,
			)
			messages[ref.msg].Content = &contentCopy
			copiedContent[ref.msg] = true
		}
		messages[ref.msg].Content.ContentBlocks[ref.block].PromptCacheBreakpoint = breakpoint()
	}
}

// ToOpenAIResponsesRequest converts a Bifrost responses request to OpenAI format
// bareTextContentBlockType is the Anthropic/Gemini spelling of a text content block.
// OpenAI's Responses input only knows input_text / output_text, so the converter
// retags it by role.
const bareTextContentBlockType schemas.ResponsesMessageContentBlockType = "text"

func ToOpenAIResponsesRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostResponsesRequest) *OpenAIResponsesRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	// Canonical model for capability gating only; wire model is untouched.
	capModel := schemas.ResolveCanonicalModel(ctx, bifrostReq.Model)
	caps := schemas.ResolveModelCaps(bifrostReq.Provider, capModel)

	var messages []schemas.ResponsesMessage
	// OpenAI models (except for gpt-oss) do not support reasoning content blocks, so we need to convert them to summaries, if there are any
	// OpenAI also doesn't support compaction content blocks, so we need to convert them to text blocks,
	// nor Anthropic's server-side fallback boundary markers, which are dropped outright.
	messages = make([]schemas.ResponsesMessage, 0, len(bifrostReq.Input))
	// Tools lifted out of codex additional_tools items for providers that reject them.
	var hoistedTools []schemas.ResponsesTool
	keepAdditionalTools := supportsAdditionalToolsItem(bifrostReq.Provider)
	replayAssistantTextAsInput := isMantleGPTOSSResponses(ctx, bifrostReq.Provider, capModel)
	replayComputerActions := bifrostReq.Params != nil && slices.ContainsFunc(bifrostReq.Params.Tools, func(t schemas.ResponsesTool) bool {
		return t.Type == schemas.ResponsesToolTypeComputer
	})
	for _, message := range bifrostReq.Input {
		if !keepAdditionalTools && message.Type != nil &&
			*message.Type == schemas.ResponsesMessageTypeAdditionalTools {
			hoistedTools = append(hoistedTools, hoistAdditionalTools(message)...)
			continue
		}
		// Anthropic's per-message effort override (a system item with empty content and
		// output_config.effort) has no OpenAI equivalent: the key is unknown to OpenAI and an
		// empty content array is rejected, so the effort-only item is dropped and any other
		// item sheds the key. `message` is the range copy, so the caller's input is untouched.
		if message.OutputConfig != nil {
			if message.IsEffortOnlySystemItem() {
				continue
			}
			message.OutputConfig = nil
		}
		// First, check if message has compaction/fallback content blocks and rewrite them
		if message.Content != nil && len(message.Content.ContentBlocks) > 0 {
			needsRewrite := false
			for _, block := range message.Content.ContentBlocks {
				if block.Type == schemas.ResponsesOutputMessageContentTypeCompaction ||
					block.Type == schemas.ResponsesOutputMessageContentTypeFallback {
					needsRewrite = true
					break
				}
			}

			if needsRewrite {
				// Create a new message with converted content blocks
				newMessage := message
				newContentBlocks := make([]schemas.ResponsesMessageContentBlock, 0, len(message.Content.ContentBlocks))

				for _, block := range message.Content.ContentBlocks {
					switch block.Type {
					case schemas.ResponsesOutputMessageContentTypeCompaction:
						// Convert compaction block to text block
						if block.ResponsesOutputMessageContentCompaction != nil && block.ResponsesOutputMessageContentCompaction.Summary != "" {
							newContentBlocks = append(newContentBlocks, schemas.ResponsesMessageContentBlock{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: schemas.Ptr(block.ResponsesOutputMessageContentCompaction.Summary),
							})
						}
						// If summary is empty, skip the block entirely
					case schemas.ResponsesOutputMessageContentTypeFallback:
						// Anthropic-only server-side fallback boundary marker. Unlike
						// compaction it carries no user content (only from/to model
						// names), so drop it rather than rendering it as text.
					default:
						// Keep every other block as-is
						newContentBlocks = append(newContentBlocks, block)
					}
				}

				// Only update if we have blocks remaining after conversion
				if len(newContentBlocks) > 0 {
					newMessage.Content = &schemas.ResponsesMessageContent{
						ContentBlocks: newContentBlocks,
					}
					message = newMessage
				} else {
					// Nothing survived (empty-summary compaction and/or fallback markers)
					continue
				}
			}
		}

		// A bare "text" content block (the Anthropic/Gemini spelling) is not an OpenAI
		// content part: OpenAI rejects it with "Invalid value: 'text'". Retag by role,
		// output_text on assistant history and input_text elsewhere, before the later
		// passes that key on those canonical types. Clone the blocks first; the caller's
		// input (shared with fallback providers) stays untouched.
		if message.Content != nil && (message.Type == nil || *message.Type == schemas.ResponsesMessageTypeMessage) {
			for _, b := range message.Content.ContentBlocks {
				if b.Type != bareTextContentBlockType {
					continue
				}
				target := schemas.ResponsesInputMessageContentBlockTypeText
				if message.Role != nil && *message.Role == schemas.ResponsesInputMessageRoleAssistant {
					target = schemas.ResponsesOutputMessageContentTypeText
				}
				contentCopy := *message.Content
				contentCopy.ContentBlocks = slices.Clone(message.Content.ContentBlocks)
				for i := range contentCopy.ContentBlocks {
					if contentCopy.ContentBlocks[i].Type == bareTextContentBlockType {
						contentCopy.ContentBlocks[i].Type = target
					}
				}
				message.Content = &contentCopy
				break
			}
		}

		// OpenAI's Responses schema requires "detail" on input_image items, and strict
		// downstream validators (e.g. vLLM importing the official OpenAI types) reject
		// requests without it. Blocks converted from non-OpenAI surfaces (Anthropic,
		// Gemini, Cohere, chat bridge) never carry one, so default missing values to "auto".
		message = defaultImageDetail(message)

		if replayComputerActions {
			message = computerCallActionAsActions(message)
		}

		if replayAssistantTextAsInput {
			message = assistantOutputTextAsInputText(message)
		}

		// Strip provider reasoning signatures (e.g. Gemini thoughtSignatures smuggled into
		// call_id as "<baseID>_ts_<sig>") from tool call IDs, but only when the id exceeds
		// OpenAI's limit — shorter IDs are left intact so distinct upstream IDs are preserved.
		// Deterministic, so a call and its output still match. Clone first — the
		// ResponsesToolMessage pointer is shared with the caller's input.
		if message.ResponsesToolMessage != nil && message.ResponsesToolMessage.CallID != nil &&
			len(*message.ResponsesToolMessage.CallID) > MaxToolCallIDLength {
			if stripped := utils.StripThoughtSignature(*message.ResponsesToolMessage.CallID); stripped != *message.ResponsesToolMessage.CallID {
				toolMsgCopy := *message.ResponsesToolMessage
				toolMsgCopy.CallID = &stripped
				message.ResponsesToolMessage = &toolMsgCopy
			}
		}

		// OpenAI requires an input function_call item's id to begin with "fc". Foreign
		// histories (e.g. Gemini streaming reuses the "<id>_ts_<sig>" call id as the item
		// id) trip this even through a fallback. The id is optional on input, so drop it;
		// call_id is left intact so the function_call_output still pairs with its call.
		// message is a value copy, so the caller's input is untouched.
		if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeFunctionCall &&
			message.ID != nil && *message.ID != "" && !strings.HasPrefix(*message.ID, "fc") {
			message.ID = nil
		}

		// OpenAI accepts role only on message input items.
		if (message.Type != nil && *message.Type != schemas.ResponsesMessageTypeMessage) ||
			(message.Type == nil && message.ResponsesReasoning != nil) {
			message.Role = nil
		}

		// Gemini streaming sets status on all item types, but OpenAI rejects status on input.
		// Strip it, except on apply_patch items, where OpenAI requires it and it carries
		// whether the patch failed. message is a value copy, so the caller's input is untouched.
		if message.Type == nil || (*message.Type != schemas.ResponsesMessageTypeApplyPatchCall &&
			*message.Type != schemas.ResponsesMessageTypeApplyPatchCallOutput) {
			message.Status = nil
		}

		// Gemini streaming generates non-standard IDs for reasoning items (msg_<id>_reasoning_N,
		// reasoning_N) and function_call_output items (func_resp_<id>). OpenAI rejects these.
		// Drop them; the call_id and output pairing are unaffected. message is a value copy.
		// But preserve native OpenAI reasoning IDs (rs_ prefix) which are needed for replay.
		if message.Type != nil && (*message.Type == schemas.ResponsesMessageTypeReasoning ||
			*message.Type == schemas.ResponsesMessageTypeFunctionCallOutput ||
			*message.Type == schemas.ResponsesMessageTypeWebSearchCall) &&
			message.ID != nil && *message.ID != "" &&
			!(*message.Type == schemas.ResponsesMessageTypeReasoning && strings.HasPrefix(*message.ID, "rs_")) {
			message.ID = nil
		}

		// Gemini sets Name on function_call_output items (the tool name), but OpenAI's
		// function_call_output does not accept this field. Strip it. message is a value copy.
		if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeFunctionCallOutput &&
			message.ResponsesToolMessage != nil && message.ResponsesToolMessage.Name != nil {
			// Clone to avoid mutating the caller's input
			toolMsgCopy := *message.ResponsesToolMessage
			toolMsgCopy.Name = nil
			message.ResponsesToolMessage = &toolMsgCopy
		}

		// Strip signatures (e.g., Gemini thoughtSignature) from content blocks.
		// OpenAI does not accept this field on input. Only mutate if a signature exists;
		// copy message.Content and clone the ContentBlocks slice before clearing to preserve
		// the caller's data and avoid data races with fallback providers.
		if message.Content != nil {
			for _, b := range message.Content.ContentBlocks {
				if b.Signature == nil {
					continue
				}
				// Signature found; copy content and blocks before clearing
				contentCopy := *message.Content
				contentCopy.ContentBlocks = slices.Clone(message.Content.ContentBlocks)
				for i := range contentCopy.ContentBlocks {
					contentCopy.ContentBlocks[i].Signature = nil
				}
				message.Content = &contentCopy
				break
			}
		}

		// OpenAI's web_search_call input item carries only id, status and action. The
		// Gemini-shaped history also sets call_id and name, which OpenAI rejects with
		// "Unknown parameter: input[N].call_id". Clone before clearing.
		if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeWebSearchCall &&
			message.ResponsesToolMessage != nil &&
			(message.ResponsesToolMessage.CallID != nil || message.ResponsesToolMessage.Name != nil) {
			toolMsgCopy := *message.ResponsesToolMessage
			toolMsgCopy.CallID = nil
			toolMsgCopy.Name = nil
			message.ResponsesToolMessage = &toolMsgCopy
		}

		// arguments is a required string on function_call items. Gemini streaming emits
		// "" for argument-less calls and foreign histories may omit the field; OpenAI
		// rejects both with "Missing required parameter: input[N].arguments", so send the
		// empty object. Clone before setting.
		if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeFunctionCall &&
			(message.ResponsesToolMessage == nil || message.ResponsesToolMessage.Arguments == nil ||
				*message.ResponsesToolMessage.Arguments == "") {
			var toolMsgCopy schemas.ResponsesToolMessage
			if message.ResponsesToolMessage != nil {
				toolMsgCopy = *message.ResponsesToolMessage
			}
			toolMsgCopy.Arguments = schemas.Ptr("{}")
			message.ResponsesToolMessage = &toolMsgCopy
		}

		// summary is a required array on reasoning items. A reasoning item that arrives
		// without one (a foreign shape the schema could not map) must still carry
		// "summary": [] - OpenAI accepts that and rejects the item without the field;
		// a nil slice would marshal as null. Clone before setting.
		if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeReasoning {
			if message.ResponsesReasoning == nil {
				message.ResponsesReasoning = &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{}}
			} else if message.ResponsesReasoning.Summary == nil {
				reasoningCopy := *message.ResponsesReasoning
				reasoningCopy.Summary = []schemas.ResponsesReasoningSummary{}
				message.ResponsesReasoning = &reasoningCopy
			}
		}

		if message.ResponsesReasoning != nil {
			usesContentBlocks := caps.SupportsReasoningContentBlocks(defaultSupportsReasoningContentBlocks(capModel))
			isReasoning := caps.SupportsReasoning(IsOpenAIReasoningModel(capModel))

			// Models that read reasoning from summary[] have nothing to gain from a
			// reasoning-only message carrying content blocks alone, so skip it.
			// For non-reasoning models (e.g., gpt-4o), also skip when EncryptedContent is present since
			// these models don't produce encrypted reasoning — any encrypted content is cross-provider
			// (e.g., Gemini ThoughtSignatures) and cannot be decrypted by OpenAI.
			if len(message.ResponsesReasoning.Summary) == 0 &&
				message.Content != nil &&
				len(message.Content.ContentBlocks) > 0 &&
				!usesContentBlocks &&
				(message.ResponsesReasoning.EncryptedContent == nil || !isReasoning) {
				continue
			}

			// Models that read reasoning from content blocks need the summaries rewritten as blocks
			if len(message.ResponsesReasoning.Summary) > 0 && usesContentBlocks &&
				(message.Content == nil || len(message.Content.ContentBlocks) == 0) {
				var newMessage schemas.ResponsesMessage
				newMessage.ID = message.ID
				newMessage.Type = message.Type
				newMessage.Status = message.Status
				newMessage.Role = message.Role

				// Convert summaries to content blocks
				contentBlocks := make([]schemas.ResponsesMessageContentBlock, 0, len(message.ResponsesReasoning.Summary))
				for _, summary := range message.ResponsesReasoning.Summary {
					contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesOutputMessageContentTypeReasoning,
						Text: schemas.Ptr(summary.Text),
					})
				}
				newMessage.Content = &schemas.ResponsesMessageContent{
					ContentBlocks: contentBlocks,
				}
				messages = append(messages, newMessage)
			} else {
				// Clone the embedded pointer to avoid mutating the original input
				reasoningCopy := *message.ResponsesReasoning
				message.ResponsesReasoning = &reasoningCopy
				// Strip cross-provider encrypted content that non-reasoning models cannot decrypt.
				// Reasoning models (o1/o3/o4/GPT-5) may use EncryptedContent for multi-turn state.
				// Compaction items always carry encrypted_content and must never be stripped.
				isCompactionMessage := message.Type != nil && *message.Type == schemas.ResponsesMessageTypeCompaction
				if !isReasoning && !isCompactionMessage {
					message.ResponsesReasoning.EncryptedContent = nil
				}
				// Only gpt-oss carries its reasoning in reasoning_text content blocks. Every
				// other OpenAI/Azure reasoning model keeps its retained state in summary +
				// encrypted_content and caps reasoning.content at zero entries, rejecting a
				// populated array with "Invalid 'input[N].content': array too long. Expected
				// an array with maximum length 0". Replayed items reach us with content anyway:
				// Anthropic thinking blocks translate into reasoning_text blocks (with Anthropic
				// signatures attached), and the response path can round-trip content as a string.
				// message is a value copy, so reassign its Content pointer without mutating the
				// caller's input.
				if message.Content != nil {
					switch {
					case !usesContentBlocks:
						// Summary and encrypted_content already carry everything OpenAI will accept.
						message.Content = nil
					case message.Content.ContentStr != nil:
						// OpenAI types reasoning.content as an array of reasoning_text blocks, so a
						// string value is rejected ("expected an array ... got a string"): drop empty
						// strings, promote non-empty ones to a block.
						if text := *message.Content.ContentStr; text == "" {
							message.Content = nil
						} else {
							message.Content = &schemas.ResponsesMessageContent{
								ContentBlocks: []schemas.ResponsesMessageContentBlock{{
									Type: schemas.ResponsesOutputMessageContentTypeReasoning,
									Text: schemas.Ptr(text),
								}},
							}
						}
					case len(message.Content.ContentBlocks) == 0:
						message.Content = nil
					}
				}
				messages = append(messages, message)
			}
		} else if message.ResponsesToolMessage != nil {
			message.ResponsesToolMessage = openAIComputerToolMessage(message.ResponsesToolMessage)
			messages = append(messages, message)
		} else {
			messages = append(messages, message)
		}
	}
	// Targets that speak prompt_cache_breakpoint need the caching intent translated
	// out of cache_control, which their serializer would otherwise strip.
	// Match on the BASE provider, not the key the request arrived under. A custom
	// provider reports its own name ("my-openai"), which no case in either predicate
	// knows, so gating on the unresolved key sends every such request down the default
	// branch: the caller's cache_control is stripped by the serializer with nothing put
	// in its place, and the request silently falls back to implicit caching - the exact
	// billing profile #6180 exists to escape. The injector resolves the same way in
	// core/bifrost.go, and providers/utils does too; this call site was the odd one out.
	cachePromptProvider := schemas.ResolveBaseProvider(ctx, bifrostReq.Provider)
	needsExplicitPromptCacheMode := false
	if responsesUsesPromptCacheBreakpoints(caps, cachePromptProvider, capModel) {
		// Only the OpenAI family documents a breakpoint on image/file blocks and on
		// function_call_output content; that is the same set of targets that takes
		// prompt_cache_options.
		usesPromptCacheOptions := responsesUsesPromptCacheOptions(caps, cachePromptProvider, capModel)
		applyResponsesCacheBreakpoints(messages, usesPromptCacheOptions)
		needsExplicitPromptCacheMode = usesPromptCacheOptions && responsesHasPromptCacheBreakpoint(messages)
	}

	// Updating params
	params := bifrostReq.Params
	asyncToolsSupported := caps.SupportsAsyncTools(defaultSupportsAsyncTools(capModel))
	messages = normalizeAsyncCallItems(messages, asyncToolsSupported)
	// Create the responses request with properly mapped parameters
	req := &OpenAIResponsesRequest{
		Model:    bifrostReq.Model,
		Provider: bifrostReq.Provider,
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: messages,
		},
	}

	if params != nil {
		req.ResponsesParameters = *params
		req.ServiceTier = serviceTierForModel(caps, req.ServiceTier)
		if req.ResponsesParameters.MaxOutputTokens != nil && *req.ResponsesParameters.MaxOutputTokens < MinMaxCompletionTokens {
			req.ResponsesParameters.MaxOutputTokens = schemas.Ptr(MinMaxCompletionTokens)
		}
		// Drop user field if it exceeds OpenAI's 64 character limit
		req.ResponsesParameters.User = SanitizeUserField(req.ResponsesParameters.User)
		// Fable 5.1+ rejects forced tool use outright. Drop the choice so the model
		// answers under the default "auto" rather than the provider returning a 400.
		if req.ResponsesParameters.ToolChoice.IsForced() &&
			!caps.SupportsForcedToolChoice(schemas.DefaultSupportsForcedToolChoice(capModel)) {
			req.ResponsesParameters.ToolChoice = nil
		}
		// The Anthropic integration emits the provider-generic forced tool choice "any".
		// OpenAI accepts only "none", "auto" and "required" as string tool choices and
		// rejects "any" with HTTP 400, so map it to "required" on a copy of the choice
		// without mutating the caller's parameters. Destinations that accept "any"
		// natively keep it.
		if tc := req.ResponsesParameters.ToolChoice; tc != nil && tc.ResponsesToolChoiceStr != nil &&
			*tc.ResponsesToolChoiceStr == string(schemas.ResponsesToolChoiceTypeAny) &&
			!caps.ToolChoiceAnySupported(toolChoiceAnySupported(bifrostReq.Provider, capModel)) {
			req.ResponsesParameters.ToolChoice = &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStr: schemas.Ptr(string(schemas.ResponsesToolChoiceTypeRequired)),
			}
		}

		// Handle reasoning parameter: OpenAI uses effort-based reasoning
		// Priority: effort (native) > max_tokens (estimated)
		if req.ResponsesParameters.Reasoning != nil {
			// Clone the Reasoning pointer to avoid mutating the original params
			reasoningCopy := *req.ResponsesParameters.Reasoning
			req.ResponsesParameters.Reasoning = &reasoningCopy
			if req.ResponsesParameters.Reasoning.Effort != nil {
				// Native field is provided, use it (and clear max_tokens)
				effort := *req.ResponsesParameters.Reasoning.Effort
				req.ResponsesParameters.Reasoning.Effort = schemas.Ptr(caps.NormalizeReasoningEffort(effort, defaultEffortControl(capModel)))
				// Clear max_tokens since OpenAI doesn't use it
				req.ResponsesParameters.Reasoning.MaxTokens = nil
			} else if req.ResponsesParameters.Reasoning.MaxTokens != nil {
				// Estimate effort from max_tokens
				maxTokens := *req.ResponsesParameters.Reasoning.MaxTokens
				maxOutputTokens := utils.GetMaxOutputTokensOrDefault(req.Provider, capModel, DefaultCompletionMaxTokens)
				if req.ResponsesParameters.MaxOutputTokens != nil {
					maxOutputTokens = *req.ResponsesParameters.MaxOutputTokens
				}
				effort := utils.GetReasoningEffortFromBudgetTokens(maxTokens, MinReasoningMaxTokens, maxOutputTokens)
				req.ResponsesParameters.Reasoning.Effort = schemas.Ptr(effort)
				// Clear max_tokens since OpenAI doesn't use it
				req.ResponsesParameters.Reasoning.MaxTokens = nil
			}
			// A model that always reasons rejects "none"; "minimal" normalizes to its lowest level.
			if e := req.ResponsesParameters.Reasoning.Effort; e != nil && *e == schemas.ReasoningEffortNone &&
				!caps.CanDisableReasoning(defaultCanDisableReasoning(capModel)) {
				req.ResponsesParameters.Reasoning.Effort = schemas.Ptr(caps.NormalizeReasoningEffort(schemas.ReasoningEffortMinimal, defaultEffortControl(capModel)))
			}

			// summary:"none" is Anthropic-specific (maps to display:"omitted"); strip it for OpenAI.
			if req.ResponsesParameters.Reasoning.Summary != nil && *req.ResponsesParameters.Reasoning.Summary == "none" {
				req.ResponsesParameters.Reasoning.Summary = nil
			}

			// reasoning.context is gated per value: every OpenAI reasoning model takes
			// "auto"/"current_turn", but "all_turns" is a hard 400 before gpt-5.4
			// ("Unsupported value: 'all_turns' is not supported with the 'gpt-5-pro'
			// model"). Drop a value the model does not list so the request runs under
			// its own default instead of failing; the datasheet row
			// supported_reasoning_contexts overrides the name default. Other
			// OpenAI-compatible upstreams are left alone.
			// Match on the base provider: a custom provider built on OpenAI or Azure
			// reports its own key ("my-openai"), so gating on the unresolved key
			// would let an unsupported value through to the upstream 400.
			contextProvider := schemas.ResolveBaseProvider(ctx, bifrostReq.Provider)
			if c := req.ResponsesParameters.Reasoning.Context; c != nil &&
				(contextProvider == schemas.OpenAI || contextProvider == schemas.Azure) &&
				!slices.Contains(caps.SupportedReasoningContexts(defaultReasoningContexts(capModel)), *c) {
				req.ResponsesParameters.Reasoning.Context = nil
			}

			// Bedrock's OpenAI-compatible surfaces accept only "auto". They answer
			// "concise" and "detailed" with a 400 ("Unsupported parameter:
			// 'reasoning.summary' is not supported with the ... model") even though the
			// schema advertises all three, on both bedrock-runtime and bedrock-mantle.
			// The Anthropic converter pins "detailed" for every Claude Code request, so
			// without this each such session fails on its first message. Narrow to a
			// summary the target can serve rather than dropping it: the caller asked to
			// see reasoning, and "auto" is the only way to say yes here.
			if summary := req.ResponsesParameters.Reasoning.Summary; summary != nil && *summary != "auto" {
				switch schemas.ResolveBaseProvider(ctx, bifrostReq.Provider) {
				case schemas.Bedrock, schemas.BedrockMantle:
					req.ResponsesParameters.Reasoning.Summary = schemas.Ptr("auto")
				}
			}

			// Handle xAI-specific parameter filtering
			// Strip reasoning_effort only for the models known to reject it; current-generation
			// models (grok-4.5, grok-4.6, grok-4.20-*) accept it.
			if bifrostReq.Provider == schemas.XAI &&
				caps.FieldUnsupported(schemas.FieldReasoningEffort,
					schemas.IsGrokReasoningModel(capModel) && !schemas.SupportsGrokReasoningEffort(capModel)) {
				req.ResponsesParameters.Reasoning.Effort = nil
			}

			// Handle OpenAI-specific parameter filtering
			// Only o1/o3 series models support reasoning.effort
			// Regular models like gpt-4o, gpt-4, gpt-3.5-turbo don't support it
			if (bifrostReq.Provider == schemas.OpenAI || bifrostReq.Provider == schemas.Azure) && !caps.SupportsReasoning(IsOpenAIReasoningModel(capModel)) {
				// Clear reasoning for non-reasoning OpenAI models to avoid API errors
				req.ResponsesParameters.Reasoning = nil
			}
		}

		effort := ""
		if req.ResponsesParameters.Reasoning != nil &&
			req.ResponsesParameters.Reasoning.Effort != nil {
			effort = *req.ResponsesParameters.Reasoning.Effort
		}
		if samplingParamUnsupported(caps, schemas.FieldTopP, capModel, effort) {
			req.ResponsesParameters.TopP = nil
		}
		// Only OpenAI hosts gate these; third-party gpt-oss hosts accept them.
		if base := schemas.ResolveBaseProvider(ctx, bifrostReq.Provider); base == schemas.OpenAI || base == schemas.Azure {
			if samplingParamUnsupported(caps, schemas.FieldTemperature, capModel, effort) {
				req.ResponsesParameters.Temperature = nil
			}
			if samplingParamUnsupported(caps, schemas.FieldTopLogprobs, capModel, effort) {
				req.ResponsesParameters.TopLogProbs = nil
			}
			if samplingParamUnsupported(caps, schemas.FieldLogprobs, capModel, effort) &&
				slices.Contains(req.ResponsesParameters.Include, "message.output_text.logprobs") {
				// Clone: Include shares its backing array with bifrostReq.Params.
				req.ResponsesParameters.Include = slices.DeleteFunc(slices.Clone(req.ResponsesParameters.Include), func(s string) bool {
					return s == "message.output_text.logprobs"
				})
			}
		}
	}

	// gpt-5.6 defaults to IMPLICIT caching, which anchors the breakpoint on the latest
	// message - so an agent loop rewrites the whole growing prompt every turn at the
	// cache-write rate and reads almost nothing back (#6180). A block marker alone does
	// not switch that off; mode=explicit does. Runs after the params assignment above so
	// a caller that set prompt_cache_options themselves is visible, and wins.
	if needsExplicitPromptCacheMode && req.ResponsesParameters.PromptCacheOptions == nil {
		req.ResponsesParameters.PromptCacheOptions = &schemas.PromptCacheOptions{
			Mode: schemas.Ptr(PromptCacheBreakpointModeExplicit),
		}
	}

	// Append tools hoisted out of additional_tools items. Runs after the params
	// assignment above, which would otherwise clobber Tools, and before the
	// normalization below so hoisted function tools get the same treatment.
	if len(hoistedTools) > 0 {
		req.Tools = append(append(make([]schemas.ResponsesTool, 0, len(req.Tools)+len(hoistedTools)), req.Tools...), hoistedTools...)
	}

	// Drop namespace tools the provider reserves; they are a hard 400. Runs after
	// the hoist so additional_tools namespaces get the same treatment, and before
	// filterUnsupportedTools so a substituted web_search goes through its copy path.
	// Match on the base provider: a custom provider built on bedrock reports its own
	// key, which neither the datasheet nor the fallback map knows, so the reserved
	// namespace would reach AWS.
	toolProvider := schemas.ResolveBaseProvider(ctx, bifrostReq.Provider)
	if reserved := resolveReservedToolNamespaces(toolProvider, capModel); len(reserved) > 0 && len(req.Tools) > 0 {
		substitute := toolProvider == schemas.BedrockMantle && caps.SupportsWebSearch(true)
		req.Tools = dropReservedNamespaceTools(req.Tools, reserved, substitute)
	}

	// Normalize function tool parameters for deterministic JSON serialization, and
	// default a nil strict to false — OpenAI resolves null to false anyway, while
	// strict-pydantic upstreams (e.g. sglang) reject the explicit null outright.
	// We must copy the Tools slice since it shares the backing array with bifrostReq.Params.Tools.
	if len(req.Tools) > 0 {
		normalizedTools := normalizeAsyncTools(req.Tools, asyncToolsSupported)
		for i, tool := range normalizedTools {
			if tool.Type == schemas.ResponsesToolTypeFunction &&
				tool.ResponsesToolFunction != nil {
				funcCopy := *tool.ResponsesToolFunction
				if funcCopy.Parameters != nil {
					funcCopy.Parameters = funcCopy.Parameters.Normalized()
				}
				if funcCopy.Strict == nil {
					funcCopy.Strict = new(false)
				}
				normalizedTools[i].ResponsesToolFunction = &funcCopy
			}
		}
		req.Tools = normalizedTools
	}

	// Filter out tools that the OpenAI-compatible target doesn't support.
	toolCaps := schemas.ResolveModelCaps(toolProvider, capModel)
	req.filterUnsupportedTools(supportsWebSearchContentTypes(toolCaps, toolProvider), toolProvider)
	req.keepDeferLoading = toolCaps.SupportsToolSearch(defaultSupportsToolSearch(toolProvider, capModel))

	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}

	if features, ok := ProviderFeatures[bifrostReq.Provider]; ok && !features.ContextManagement {
		req.ContextManagement = nil
		delete(req.ExtraParams, "context_management")
	}

	return req
}

// samplingParamUnsupported reports whether the model rejects a sampling field
// (top_p, temperature, top_logprobs, logprobs). The datasheet can mark it
// unsupported outright, or conditionally via "when_effort_none" — accepted only
// while reasoning is off. The fallback is name detection: OpenAI reasoning models
// (o1/o3 series, GPT-6) reject it, except GPT-5.x while effort is "none". An
// omitted effort counts as "none" only where that is the model's default
// (GPT-5.1 through 5.4). The -pro variants always reason, so they always strip.
// Gated on the OpenAI-family name rather than caps.SupportsReasoning:
// this asks whether the API rejects the field, which is not the same question for
// xAI/Groq.
func samplingParamUnsupported(caps schemas.ModelCaps, field, model, effort string) bool {
	effortIsNone := effort == schemas.ReasoningEffortNone || (effort == "" && !omittedEffortReasons(model))
	// An outright unsupported_fields entry outranks the conditional label: the
	// row rejects the field whatever the effort. Probed with a false fallback so
	// only an explicit true short-circuits.
	if caps.FieldUnsupported(field, false) {
		return true
	}
	if caps.FieldCondition(field) == schemas.ConditionWhenEffortNone {
		return !effortIsNone
	}

	fallback := true
	if !IsOpenAIReasoningModel(model) {
		fallback = false
	} else {
		_, parsedModel := schemas.ParseModelString(model, schemas.OpenAI)
		modelLower := strings.ToLower(parsedModel)
		if strings.Contains(modelLower, "gpt-5.") && effortIsNone &&
			!strings.Contains(modelLower, "-pro") {
			fallback = false
		}
	}
	return caps.FieldUnsupported(field, fallback)
}

// filterUnsupportedTools removes tool types that OpenAI doesn't support
// defaultImageDetail fills "auto" into any input_image content block missing the
// detail field. Clones content on write — the Content pointer and the image block
// pointers inside it are shared with the caller's input.
func defaultImageDetail(message schemas.ResponsesMessage) schemas.ResponsesMessage {
	if message.Content == nil || len(message.Content.ContentBlocks) == 0 {
		return message
	}

	needsDetail := func(block schemas.ResponsesMessageContentBlock) bool {
		return block.Type == schemas.ResponsesInputMessageContentBlockTypeImage &&
			block.ResponsesInputMessageContentBlockImage != nil &&
			block.ResponsesInputMessageContentBlockImage.Detail == nil
	}

	fixNeeded := false
	for _, block := range message.Content.ContentBlocks {
		if needsDetail(block) {
			fixNeeded = true
			break
		}
	}
	if !fixNeeded {
		return message
	}

	newBlocks := make([]schemas.ResponsesMessageContentBlock, len(message.Content.ContentBlocks))
	copy(newBlocks, message.Content.ContentBlocks)
	for i, block := range newBlocks {
		if needsDetail(block) {
			imageCopy := *block.ResponsesInputMessageContentBlockImage
			imageCopy.Detail = schemas.Ptr("auto")
			newBlocks[i].ResponsesInputMessageContentBlockImage = &imageCopy
		}
	}

	contentCopy := *message.Content
	contentCopy.ContentBlocks = newBlocks
	message.Content = &contentCopy
	return message
}

// isMantleGPTOSSResponses reports whether the request is gpt-oss served by Bedrock Mantle's
// /v1 Responses backend; gpt-5.x on /openai/v1 and gpt-oss elsewhere keep output_text history.
func isMantleGPTOSSResponses(ctx *schemas.BifrostContext, provider schemas.ModelProvider, capModel string) bool {
	base := schemas.ResolveBaseProvider(ctx, provider)
	return (base == schemas.Bedrock || base == schemas.BedrockMantle) &&
		strings.Contains(strings.ToLower(capModel), "gpt-oss") &&
		schemas.ResolveBedrockMantleBasePath(capModel) == schemas.BedrockMantleBasePathV1
}

// assistantOutputTextAsInputText retags a replayed assistant message's output_text blocks
// as input_text. Mantle /v1 strips id, status and annotations from assistant items before
// validating, so output_text history matches no input variant and the turn fails (#7074).
// openAIComputerAction replaces what OpenAI's computer actions lack: zoom becomes a
// screenshot and the region goes. It reports whether anything changed.
func openAIComputerAction(action schemas.ResponsesComputerToolCallAction) (schemas.ResponsesComputerToolCallAction, bool) {
	if action.Type != "zoom" && action.Region == nil {
		return action, false
	}
	action.Region = nil
	if action.Type == "zoom" {
		action.Type = "screenshot"
	}
	return action, true
}

// openAIComputerToolMessage applies openAIComputerAction to a computer call's action and to
// every entry of its actions list, copying before it changes anything.
func openAIComputerToolMessage(toolMsg *schemas.ResponsesToolMessage) *schemas.ResponsesToolMessage {
	var out *schemas.ResponsesToolMessage
	own := func() *schemas.ResponsesToolMessage {
		if out == nil {
			c := *toolMsg
			out = &c
		}
		return out
	}
	if toolMsg.Action != nil && toolMsg.Action.ResponsesComputerToolCallAction != nil {
		if action, changed := openAIComputerAction(*toolMsg.Action.ResponsesComputerToolCallAction); changed {
			actionStruct := *toolMsg.Action
			actionStruct.ResponsesComputerToolCallAction = &action
			own().Action = &actionStruct
		}
	}
	if toolMsg.ResponsesComputerToolCall != nil {
		var actions []schemas.ResponsesComputerToolCallAction
		for i, entry := range toolMsg.ResponsesComputerToolCall.Actions {
			if cleaned, changed := openAIComputerAction(entry); changed {
				if actions == nil {
					actions = slices.Clone(toolMsg.ResponsesComputerToolCall.Actions)
				}
				actions[i] = cleaned
			}
		}
		if actions != nil {
			call := *toolMsg.ResponsesComputerToolCall
			call.Actions = actions
			own().ResponsesComputerToolCall = &call
		}
	}
	if out == nil {
		return toolMsg
	}
	return out
}

// computerCallActionAsActions replays a computer_call's single action as its actions list.
// The GA computer tool rejects action, alone or beside actions, on OpenAI, Azure and Bedrock.
func computerCallActionAsActions(message schemas.ResponsesMessage) schemas.ResponsesMessage {
	if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeComputerCall || message.ResponsesToolMessage == nil {
		return message
	}
	toolMsg := *message.ResponsesToolMessage
	if toolMsg.Action == nil || toolMsg.Action.ResponsesComputerToolCallAction == nil {
		return message
	}
	call := schemas.ResponsesComputerToolCall{}
	if toolMsg.ResponsesComputerToolCall != nil {
		call = *toolMsg.ResponsesComputerToolCall
	}
	if len(call.Actions) == 0 {
		call.Actions = []schemas.ResponsesComputerToolCallAction{*toolMsg.Action.ResponsesComputerToolCallAction}
	}
	toolMsg.ResponsesComputerToolCall = &call
	toolMsg.Action = nil
	message.ResponsesToolMessage = &toolMsg
	return message
}

func assistantOutputTextAsInputText(message schemas.ResponsesMessage) schemas.ResponsesMessage {
	if message.Role == nil || *message.Role != schemas.ResponsesInputMessageRoleAssistant ||
		message.Content == nil || len(message.Content.ContentBlocks) == 0 {
		return message
	}
	fixNeeded := false
	for _, block := range message.Content.ContentBlocks {
		if block.Type == schemas.ResponsesOutputMessageContentTypeText {
			fixNeeded = true
			break
		}
	}
	if !fixNeeded {
		return message
	}

	newBlocks := make([]schemas.ResponsesMessageContentBlock, len(message.Content.ContentBlocks))
	copy(newBlocks, message.Content.ContentBlocks)
	for i := range newBlocks {
		if newBlocks[i].Type == schemas.ResponsesOutputMessageContentTypeText {
			newBlocks[i].Type = schemas.ResponsesInputMessageContentBlockTypeText
			newBlocks[i].ResponsesOutputMessageContentText = nil
		}
	}

	contentCopy := *message.Content
	contentCopy.ContentBlocks = newBlocks
	message.Content = &contentCopy
	return message
}

// isOpenAISupportedToolType reports whether a tool type is forwarded to OpenAI-compatible providers.
func isOpenAISupportedToolType(t schemas.ResponsesToolType, provider schemas.ModelProvider) bool {
	switch t {
	case schemas.ResponsesToolTypeFunction,
		schemas.ResponsesToolTypeFileSearch,
		schemas.ResponsesToolTypeComputer,
		schemas.ResponsesToolTypeWebSearch,
		schemas.ResponsesToolTypeWebFetch,
		schemas.ResponsesToolTypeMCP,
		schemas.ResponsesToolTypeApplyPatch,
		schemas.ResponsesToolTypeCustom,
		schemas.ResponsesToolTypeWebSearchPreview,
		schemas.ResponsesToolTypeMemory,
		schemas.ResponsesToolTypeToolSearch,
		schemas.ResponsesToolTypeNamespace:
		return true
	case schemas.ResponsesToolTypeShell,
		schemas.ResponsesToolTypeLocalShell,
		schemas.ResponsesToolTypeCodeInterpreter,
		schemas.ResponsesToolTypeImageGeneration,
		schemas.ResponsesToolTypeComputerUsePreview,
		schemas.ResponsesToolTypeProgrammaticToolCalling:
		return provider != schemas.BedrockMantle && provider != schemas.Bedrock
	case schemas.ResponsesToolTypeXSearch:
		return provider == schemas.XAI
	}
	return false
}

func (resp *OpenAIResponsesRequest) filterUnsupportedTools(webSearchContentTypesSupported bool, baseProvider schemas.ModelProvider) {
	if len(resp.Tools) == 0 {
		return
	}

	// Filter tools to only include supported types
	filteredTools := make([]schemas.ResponsesTool, 0, len(resp.Tools))
	for i := range resp.Tools {
		tool := &resp.Tools[i]
		// OpenRouter exposes server-side tools under the "openrouter:" namespace
		// (web_search, web_fetch, datetime, image_generation, apply_patch, subagent, ...).
		// They are native to OpenRouter and must not be stripped by the
		// OpenAI-oriented whitelist. Match the whole namespace so future tools are
		// covered without per-tool additions.
		isOpenRouterServerTool := resp.Provider == schemas.OpenRouter &&
			strings.HasPrefix(string(tool.Type), schemas.ResponsesToolTypeOpenRouterPrefix)
		if isOpenAISupportedToolType(tool.Type, baseProvider) || isOpenRouterServerTool {
			// check for computer use preview
			if tool.Type == schemas.ResponsesToolTypeComputerUsePreview && tool.ResponsesToolComputerUsePreview != nil && tool.ResponsesToolComputerUsePreview.EnableZoom != nil {
				newTool := *tool
				newComputerUse := &schemas.ResponsesToolComputerUsePreview{
					DisplayHeight: tool.ResponsesToolComputerUsePreview.DisplayHeight,
					DisplayWidth:  tool.ResponsesToolComputerUsePreview.DisplayWidth,
					Environment:   tool.ResponsesToolComputerUsePreview.Environment,
					// EnableZoom is intentionally omitted (nil) - OpenAI doesn't support it
				}
				newTool.ResponsesToolComputerUsePreview = newComputerUse
				filteredTools = append(filteredTools, newTool)
			} else if tool.Type == schemas.ResponsesToolTypeWebSearch && tool.ResponsesToolWebSearch != nil {
				// Create a proper deep copy with new nested pointers to avoid mutating the original
				newTool := *tool
				newWebSearch := &schemas.ResponsesToolWebSearch{}

				// MaxUses is intentionally omitted (nil) - OpenAI doesn't support it

				// Handle Filters: OpenAI doesn't support BlockedDomains or TimeRangeFilter
				if tool.ResponsesToolWebSearch.Filters != nil {
					hasAllowedDomains := len(tool.ResponsesToolWebSearch.Filters.AllowedDomains) > 0

					if hasAllowedDomains {
						// Keep only AllowedDomains (copy the slice to avoid sharing)
						newWebSearch.Filters = &schemas.ResponsesToolWebSearchFilters{
							AllowedDomains: append([]string(nil), tool.ResponsesToolWebSearch.Filters.AllowedDomains...),
							// BlockedDomains and TimeRangeFilter are intentionally omitted - OpenAI doesn't support it
						}
					}
					// If only blocked domains or both empty, Filters stays nil
				}

				if tool.ResponsesToolWebSearch.ExternalWebAccess != nil {
					externalWebAccess := *tool.ResponsesToolWebSearch.ExternalWebAccess
					newWebSearch.ExternalWebAccess = &externalWebAccess
				}
				if webSearchContentTypesSupported && len(tool.ResponsesToolWebSearch.SearchContentTypes) > 0 {
					newWebSearch.SearchContentTypes = append([]string(nil), tool.ResponsesToolWebSearch.SearchContentTypes...)
				}

				// Copy other fields if they exist
				if tool.ResponsesToolWebSearch.UserLocation != nil {
					newWebSearch.UserLocation = tool.ResponsesToolWebSearch.UserLocation
				}
				if tool.ResponsesToolWebSearch.SearchContextSize != nil {
					newWebSearch.SearchContextSize = tool.ResponsesToolWebSearch.SearchContextSize
				}

				newTool.ResponsesToolWebSearch = newWebSearch
				filteredTools = append(filteredTools, newTool)
			} else {
				filteredTools = append(filteredTools, *tool)
			}
		}
	}
	resp.Tools = filteredTools

	// If every tool was stripped, a leftover tool_choice would cause a 400 from
	// the upstream ("tool_choice must be specified with tools").
	if len(resp.Tools) == 0 {
		resp.ToolChoice = nil
	}
}

// OpenAICompactionRequest is the wire format for POST /v1/responses/compact.
// It is a strict subset of OpenAIResponsesRequest — no tools, no sampling params, no streaming.
type OpenAICompactionRequest struct {
	Model                string                      `json:"model"`
	Input                OpenAIResponsesRequestInput `json:"input,omitempty"`
	Instructions         *string                     `json:"instructions,omitempty"`
	PreviousResponseID   *string                     `json:"previous_response_id,omitempty"`
	PromptCacheKey       *string                     `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string                     `json:"prompt_cache_retention,omitempty"`
	PromptCacheOptions   *schemas.PromptCacheOptions `json:"prompt_cache_options,omitempty"`
	ServiceTier          *schemas.BifrostServiceTier `json:"service_tier,omitempty"`
	ExtraParams          map[string]interface{}      `json:"-"`
}

// GetExtraParams implements RequestBodyWithExtraParams.
func (r *OpenAICompactionRequest) GetExtraParams() map[string]interface{} { return r.ExtraParams }

// MarshalJSON serializes the compaction request. The embedded Input is shadowed
// by a json.RawMessage so the OpenAIResponsesRequestInput union is written via
// its own MarshalJSON (a pointer-receiver method that default struct encoding of
// the value field would skip, emitting `input` as an object the endpoint
// rejects). Empty input is omitted, since a previous_response_id-only compaction
// is valid. Mirrors OpenAIResponsesRequest.MarshalJSON.
func (r *OpenAICompactionRequest) MarshalJSON() ([]byte, error) {
	type Alias OpenAICompactionRequest
	var input json.RawMessage
	if r.Input.OpenAIResponsesRequestInputStr != nil || r.Input.OpenAIResponsesRequestInputArray != nil {
		b, err := r.Input.MarshalJSON()
		if err != nil {
			return nil, err
		}
		input = json.RawMessage(b)
	}
	return utils.MarshalSorted(struct {
		*Alias
		Input json.RawMessage `json:"input,omitempty"`
	}{Alias: (*Alias)(r), Input: input})
}

// ToOpenAICompactionRequest converts a BifrostCompactionRequest to the OpenAI wire format.
func ToOpenAICompactionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostCompactionRequest) *OpenAICompactionRequest {
	if req == nil {
		return nil
	}
	r := &OpenAICompactionRequest{
		Model:                req.Model,
		Instructions:         req.Instructions,
		PreviousResponseID:   req.PreviousResponseID,
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
		PromptCacheOptions:   req.PromptCacheOptions,
		ServiceTier:          req.ServiceTier,
		ExtraParams:          req.ExtraParams,
	}
	if len(req.Input) > 0 {
		// Run through the same normalization as ToOpenAIResponsesRequest so reasoning
		// role cleanup, compaction-content conversion, etc. are applied consistently.
		normalized := ToOpenAIResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
			Provider: req.Provider,
			Model:    req.Model,
			Input:    req.Input,
		})
		if normalized != nil {
			r.Input = normalized.Input
		}
	}
	return r
}

// ToBifrostCompactionRequest converts an OpenAICompactionRequest to Bifrost format.
func (r *OpenAICompactionRequest) ToBifrostCompactionRequest(ctx *schemas.BifrostContext) *schemas.BifrostCompactionRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, "")
	input := r.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 && r.Input.OpenAIResponsesRequestInputStr != nil {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: r.Input.OpenAIResponsesRequestInputStr},
			},
		}
	}
	return &schemas.BifrostCompactionRequest{
		Provider:             provider,
		Model:                model,
		Input:                input,
		Instructions:         r.Instructions,
		PreviousResponseID:   r.PreviousResponseID,
		PromptCacheKey:       r.PromptCacheKey,
		PromptCacheRetention: r.PromptCacheRetention,
		PromptCacheOptions:   r.PromptCacheOptions,
		ServiceTier:          r.ServiceTier,
		ExtraParams:          r.ExtraParams,
	}
}
