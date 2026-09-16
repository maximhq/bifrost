package databricks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Claude-backed Databricks endpoints reason through Anthropic's own knob. They reject
// reasoning_effort ("Extra inputs are not permitted") and instead accept a "thinking" object
// on the OpenAI-shaped request, exactly as the native Anthropic API spells it:
//
//	{"thinking": {"type": "enabled", "budget_tokens": N}}   // budget models
//	{"thinking": {"type": "adaptive"}}                       // adaptive-only models
//
// with the hard rule that max_tokens must exceed budget_tokens. Every model, Claude or not,
// then answers with reasoning as a content block rather than a top-level field:
//
//	"content": [{"type": "reasoning", "summary": [{"type": "summary_text", "text": "...", "signature": "..."}]},
//	            {"type": "text", "text": "..."}]
//
// Streaming deltas carry the same block shape for the thinking phase and a plain string for
// the answer. Neither shape is something Bifrost's OpenAI-shaped parser models (delta.content
// is a string there), so the request is translated on the way out and the response is
// normalised on the way in.

// thinkingParam builds the "thinking" extra param for a Claude-backed endpoint from the
// neutral reasoning parameters. It returns nil when nothing should be sent. maxTokens is the
// max_completion_tokens the request should carry to satisfy the budget rule, or nil to leave
// the caller's value alone.
func thinkingParam(support paramSupport, params *schemas.ChatParameters) (thinking map[string]any, maxTokens *int) {
	if params == nil || !support.anthropicThinking {
		return nil, nil
	}
	if _, callerSet := params.ExtraParams["thinking"]; callerSet {
		// The caller spoke Databricks' dialect directly; it passes through untouched.
		return nil, nil
	}

	var effort string
	var budget *int
	var enabled *bool
	if params.Reasoning != nil {
		if params.Reasoning.Effort != nil {
			effort = *params.Reasoning.Effort
		}
		budget = params.Reasoning.MaxTokens
		enabled = params.Reasoning.Enabled
	}
	if effort == "" {
		if raw, ok := params.ExtraParams["reasoning_effort"].(string); ok {
			effort = raw
		}
	}
	// An explicit enabled flag wins over effort and budget: false sends nothing, true turns
	// thinking on even when the other knobs say otherwise, at the model's default budget when
	// none is given.
	switch {
	case enabled != nil && !*enabled:
		return nil, nil
	case enabled != nil && *enabled:
		if effort == "none" {
			effort = ""
		}
	case effort == "none" || (effort == "" && budget == nil):
		return nil, nil
	}

	if support.adaptiveOnlyThinking {
		thinking = map[string]any{"type": "adaptive"}
		// thinking.display ("summarized" | "omitted") is an adaptive-thinking option; it
		// controls whether the reasoning is returned, not whether it happens.
		if params.Reasoning != nil && params.Reasoning.Display != nil && *params.Reasoning.Display != "" {
			thinking["display"] = *params.Reasoning.Display
		}
		return thinking, nil
	}

	budgetTokens := 0
	switch {
	case budget != nil && *budget > 0:
		budgetTokens = *budget
	default:
		// -1 asks for a dynamic budget, which Claude does not offer; an effort label is
		// scaled against the output ceiling the same way the Anthropic provider does.
		ceiling := anthropic.AnthropicDefaultMaxTokens
		if params.MaxCompletionTokens != nil && *params.MaxCompletionTokens > 0 {
			ceiling = *params.MaxCompletionTokens
		}
		if effort == "" {
			budgetTokens = anthropic.MinimumReasoningMaxTokens
		} else if computed, err := providerUtils.GetBudgetTokensFromReasoningEffort(effort, anthropic.MinimumReasoningMaxTokens, ceiling); err == nil {
			budgetTokens = computed
		} else {
			budgetTokens = anthropic.MinimumReasoningMaxTokens
		}
	}
	if budgetTokens < anthropic.MinimumReasoningMaxTokens {
		budgetTokens = anthropic.MinimumReasoningMaxTokens
	}

	thinking = map[string]any{"type": "enabled", "budget_tokens": budgetTokens}

	// max_tokens must be greater than budget_tokens. When the caller set no ceiling, give
	// the answer the same room the Anthropic provider defaults to on top of the budget. An
	// explicit ceiling below the budget is the caller's contradiction and is left for the
	// endpoint to report, as the native Anthropic API would.
	if params.MaxCompletionTokens == nil || *params.MaxCompletionTokens <= 0 {
		maxTokens = schemas.Ptr(budgetTokens + anthropic.AnthropicDefaultMaxTokens)
	} else if budget == nil && *params.MaxCompletionTokens <= budgetTokens {
		// Effort was scaled against the caller's ceiling; "max" lands exactly on it. A
		// ceiling too small to fit the minimum budget cannot carry thinking at all, and
		// an effort label is a preference rather than a contract, so it is omitted.
		fitted := *params.MaxCompletionTokens - 1
		if fitted < anthropic.MinimumReasoningMaxTokens {
			return nil, nil
		}
		thinking["budget_tokens"] = fitted
	}
	return thinking, maxTokens
}

// chatResponseHandler parses a Databricks chat completion (or stream event) after lifting
// reasoning content blocks into Bifrost's reasoning fields. It stands in for the shared
// handler's default parse; raw request/response capture is preserved with the upstream
// bytes, not the normalised ones.
func chatResponseHandler(responseBody []byte, response *schemas.BifrostChatResponse, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bErr *schemas.BifrostError) {
	rawRequest, _, bErr = providerUtils.HandleProviderResponse(normalizeReasoningBlocks(responseBody), response, requestBody, sendBackRawRequest, false)
	if bErr != nil {
		return nil, nil, bErr
	}
	if sendBackRawResponse {
		rawResponse = compactJSON(responseBody)
	}
	return rawRequest, rawResponse, nil
}

// normalizeReasoningBlocks rewrites the Databricks reasoning content-block shape into the
// OpenAI-shaped fields Bifrost parses: reasoning text goes to "reasoning" (with a
// reasoning_details entry carrying the signature), and the remaining blocks stay as content,
// collapsed to a string when they are all text so that streaming deltas parse. A body
// without array content is returned untouched.
func normalizeReasoningBlocks(body []byte) []byte {
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return body
	}

	out := body
	choices.ForEach(func(idx, choice gjson.Result) bool {
		for _, carrier := range []string{"message", "delta"} {
			content := choice.Get(carrier + ".content")
			if !content.IsArray() {
				continue
			}

			var reasoning strings.Builder
			var signature string
			var texts []string
			var kept []string
			allText := true
			content.ForEach(func(_, block gjson.Result) bool {
				switch block.Get("type").String() {
				case "reasoning":
					block.Get("summary").ForEach(func(_, summary gjson.Result) bool {
						reasoning.WriteString(summary.Get("text").String())
						if sig := summary.Get("signature").String(); sig != "" {
							signature = sig
						}
						return true
					})
					return true
				case "text":
					texts = append(texts, block.Get("text").String())
				default:
					allText = false
				}
				kept = append(kept, block.Raw)
				return true
			})

			base := fmt.Sprintf("choices.%d.%s", idx.Int(), carrier)
			var err error
			switch {
			case len(kept) == 0 && carrier == "delta":
				out, err = sjson.DeleteBytes(out, base+".content")
			case len(kept) == 0:
				out, err = sjson.SetBytes(out, base+".content", "")
			case allText:
				out, err = sjson.SetBytes(out, base+".content", strings.Join(texts, ""))
			default:
				out, err = sjson.SetRawBytes(out, base+".content", []byte("["+strings.Join(kept, ",")+"]"))
			}
			if err != nil {
				out = body
				return false
			}

			if reasoning.Len() == 0 && signature == "" {
				continue
			}
			detail := schemas.ChatReasoningDetails{
				Index: 0,
				Type:  schemas.BifrostReasoningDetailsTypeText,
				Text:  schemas.Ptr(reasoning.String()),
			}
			if signature != "" {
				detail.Signature = schemas.Ptr(signature)
			}
			if out, err = sjson.SetBytes(out, base+".reasoning", reasoning.String()); err != nil {
				out = body
				return false
			}
			if out, err = sjson.SetBytes(out, base+".reasoning_details", []schemas.ChatReasoningDetails{detail}); err != nil {
				out = body
				return false
			}
		}
		return true
	})
	return out
}

// compactJSON strips insignificant whitespace so a raw response can ride inside an SSE frame.
func compactJSON(body []byte) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return json.RawMessage(bytes.TrimSpace(body))
	}
	return json.RawMessage(buf.Bytes())
}
