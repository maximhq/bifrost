package bedrockmantle

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const explicitPromptCacheMode = "explicit"

// applyExplicitPromptCache pins GPT-5.6 Mantle Responses requests to explicit
// cache mode with a breakpoint on the first cacheable input block.
//
// Mantle's default (implicit) breakpoint sits on the latest message, so agent
// loops rewrite the full prompt at the 1.25× cache-write rate every turn.
// A fixed breakpoint on the stable prefix (instructions/tools plus the first
// input block) is what AWS documents for that workload. Requests that already
// set prompt_cache_options or a breakpoint are left unchanged.
func applyExplicitPromptCache(request *schemas.BifrostResponsesRequest) {
	if request == nil {
		return
	}
	_, model := parseBedrockRegionAndModel(request.Model)
	if !strings.Contains(model, "gpt-5.6") {
		return
	}
	if requestHasPromptCache(request) {
		return
	}
	if !markFirstCacheableBreakpoint(request) {
		return
	}
	params := schemas.ResponsesParameters{}
	if request.Params != nil {
		params = *request.Params
	}
	mode := explicitPromptCacheMode
	params.PromptCacheOptions = &schemas.PromptCacheOptions{Mode: &mode}
	request.Params = &params
}

func requestHasPromptCache(request *schemas.BifrostResponsesRequest) bool {
	if request.Params != nil && request.Params.PromptCacheOptions != nil {
		return true
	}
	for _, msg := range request.Input {
		if msg.Content == nil {
			continue
		}
		for _, block := range msg.Content.ContentBlocks {
			if block.PromptCacheBreakpoint != nil {
				return true
			}
		}
	}
	return false
}

func markFirstCacheableBreakpoint(request *schemas.BifrostResponsesRequest) bool {
	mode := explicitPromptCacheMode
	breakpoint := &schemas.PromptCacheBreakpoint{Mode: &mode}
	for i := range request.Input {
		msg := request.Input[i]
		if msg.Content == nil {
			continue
		}
		if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
			copied := schemas.DeepCopyResponsesMessage(msg)
			text := *copied.Content.ContentStr
			copied.Content.ContentStr = nil
			copied.Content.ContentBlocks = []schemas.ResponsesMessageContentBlock{{
				Type:                  schemas.ResponsesInputMessageContentBlockTypeText,
				Text:                  &text,
				PromptCacheBreakpoint: breakpoint,
			}}
			request.Input[i] = copied
			return true
		}
		for j, block := range msg.Content.ContentBlocks {
			if !isCacheableContentBlock(block.Type) {
				continue
			}
			copied := schemas.DeepCopyResponsesMessage(msg)
			copied.Content.ContentBlocks[j].PromptCacheBreakpoint = breakpoint
			request.Input[i] = copied
			return true
		}
	}
	return false
}

func isCacheableContentBlock(blockType schemas.ResponsesMessageContentBlockType) bool {
	switch blockType {
	case schemas.ResponsesInputMessageContentBlockTypeText,
		schemas.ResponsesInputMessageContentBlockTypeImage,
		schemas.ResponsesInputMessageContentBlockTypeFile:
		return true
	default:
		return false
	}
}
