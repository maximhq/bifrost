package compat

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// convertUnsupportedParamValues rewrites param values the model cannot accept
// into the closest value it can. Returns one "param: from -> to" entry per change.
//
// Today that is the output token cap: a value above the model's catalog
// max_output_tokens is lowered to it. Values are only ever lowered, never
// raised or filled in.
func convertUnsupportedParamValues(req *schemas.BifrostRequest, maxOutputTokens int) []string {
	if req == nil || maxOutputTokens <= 0 {
		return nil
	}

	var changes []string

	if req.ChatRequest != nil && req.ChatRequest.Params != nil {
		params := req.ChatRequest.Params
		// max_tokens is folded into max_completion_tokens before PreLLMHook runs.
		if clampTokenCap(&changes, "max_completion_tokens", &params.MaxCompletionTokens, maxOutputTokens) && params.Reasoning != nil {
			clampReasoningBudget(&changes, &params.Reasoning.MaxTokens, maxOutputTokens)
		}
	}

	if req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil {
		params := req.ResponsesRequest.Params
		if clampTokenCap(&changes, "max_output_tokens", &params.MaxOutputTokens, maxOutputTokens) && params.Reasoning != nil {
			clampReasoningBudget(&changes, &params.Reasoning.MaxTokens, maxOutputTokens)
		}
	}

	if req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil {
		clampTokenCap(&changes, "max_tokens", &req.TextCompletionRequest.Params.MaxTokens, maxOutputTokens)
	}

	return changes
}

// clampTokenCap lowers the value to limit when it is above it and reports
// whether it did. It swaps in a new pointer rather than writing through the
// old one: cloneBifrostReq copies params shallowly, so the old pointer is
// still the caller's.
func clampTokenCap(changes *[]string, param string, field **int, limit int) bool {
	if *field == nil || **field <= limit {
		return false
	}
	*changes = append(*changes, fmt.Sprintf("%s: %d -> %d", param, **field, limit))
	*field = new(limit)
	return true
}

// clampReasoningBudget keeps the thinking budget strictly below a cap this
// plugin just lowered. Anthropic rejects budget_tokens >= max_tokens, so
// clamping only the outer cap would swap one upstream 400 for another. It runs
// only after a clamp: a cap/budget pairing the caller sent is not ours to fix.
func clampReasoningBudget(changes *[]string, budget **int, limit int) {
	if *budget == nil || **budget < limit || limit <= 1 {
		return
	}
	*changes = append(*changes, fmt.Sprintf("reasoning.max_tokens: %d -> %d", **budget, limit-1))
	*budget = new(limit - 1)
}
