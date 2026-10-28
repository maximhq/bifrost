package streamusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Estimate fills in usage the provider never reported for a stream that was
// cancelled or timed out, so every post-hook (governance, logging, telemetry)
// bills the same numbers. It runs once, before the post-hooks see the failure.
//
// The output is the tracer accumulator's snapshot of chunks already received,
// which the logging or Maxim plugin maintains; without one there is nothing to
// count and the provider-reported usage (if any) is left as is. The snapshot
// holds the first choice only, so n>1 streams are undercounted.
func Estimate(ctx *schemas.BifrostContext, request *schemas.BifrostRequest, model string, failure *schemas.BifrostError) {
	if failure == nil || failure.Error == nil {
		return
	}
	// A stream that goes silent mid-output fails untyped, wrapping ErrStreamIdleTimeout.
	idle := failure.Error.Type == nil && errors.Is(failure.Error.Error, providerUtils.ErrStreamIdleTimeout)
	if !idle && (failure.Error.Type == nil || (*failure.Error.Type != schemas.RequestCancelled && *failure.Error.Type != schemas.RequestTimedOut)) {
		return
	}
	snapshot := ctx.GetAccumulatedResponse()
	if snapshot == nil {
		return // nothing received yet, or no accumulator
	}
	reported := failure.ExtraFields.BilledUsage
	if reported == nil && idle {
		// The idle-timeout path attaches no billed usage; read the provider's handle.
		if u, ok := ctx.Value(schemas.BifrostContextKeyStreamAccumulatedUsage).(*schemas.BifrostLLMUsage); ok && u != nil && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
			reported = u
		}
	}
	if reported == nil {
		if u := snapshot.NormalizedUsage(); u != nil && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
			reported = u
		}
	}
	estimatePrompt, estimateCompletion := reported == nil, reported == nil
	// Claude streams report a provisional output count in message_start; only a
	// later message_delta (or a finished stream) makes it authoritative. Every host
	// of the shared Anthropic stream handlers (Vertex, Bedrock, Azure, ...) counts
	// those usage events, so the counter's presence identifies them.
	if updates, ok := ctx.Value(schemas.BifrostContextKeyStreamUsageUpdates).(int); ok && updates < 2 && reported != nil && !finished(snapshot) {
		estimateCompletion = true
	}
	if !estimatePrompt && !estimateCompletion {
		return
	}
	c, err := selectCounter(model)
	if err != nil {
		return
	}
	usage := reported.DeepCopy()
	if usage == nil {
		usage = &schemas.BifrostLLMUsage{}
	}
	if estimatePrompt {
		if n, err := promptCount(c, request); err != nil {
			estimatePrompt = false
		} else {
			usage.PromptTokens = n
		}
	}
	if output, reasoning := responseText(snapshot); estimateCompletion && output == "" && len(reasoning) == 0 {
		estimateCompletion = false // e.g. only images were received
	} else if estimateCompletion {
		n, err := c.count(output)
		if err != nil {
			return
		}
		// Reasoning text is model-agnostic, so it is always counted with cl100k_base.
		rc, err := selectCounter("")
		if err != nil {
			return
		}
		rn := 0
		for _, part := range reasoning {
			n, err := rc.count(part)
			if err != nil {
				return
			}
			rn += n
		}
		usage.CompletionTokens = n + rn
		if len(reasoning) > 0 && (usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens == 0) {
			if usage.CompletionTokensDetails == nil {
				usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
			}
			usage.CompletionTokensDetails.ReasoningTokens = rn
		}
	}
	if !estimatePrompt && !estimateCompletion {
		return
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	usage.Estimation = &schemas.TokenUsageEstimation{Prompt: estimatePrompt, Completion: estimateCompletion, Tokenizer: c.name}
	failure.ExtraFields.BilledUsage = usage
}

func finished(response *schemas.BifrostResponse) bool {
	if r := response.ChatResponse; r != nil {
		for _, c := range r.Choices {
			if c.FinishReason != nil {
				return true
			}
		}
	}
	if r := response.TextCompletionResponse; r != nil {
		for _, c := range r.Choices {
			if c.FinishReason != nil {
				return true
			}
		}
	}
	if r := response.ResponsesResponse; r != nil && r.Status != nil {
		return *r.Status == "completed" || *r.Status == "incomplete"
	}
	return false
}

func toolJSON(id, name, kind, arguments string) string {
	// Tool calls are counted as their JSON form, keys sorted for a stable count.
	value := struct {
		Function struct {
			Arguments string `json:"arguments"`
			Name      string `json:"name"`
		} `json:"function"`
		ID   string `json:"id"`
		Type string `json:"type"`
	}{ID: id, Type: kind}
	value.Function.Name, value.Function.Arguments = name, arguments
	b, _ := providerUtils.MarshalSorted(value)
	return string(b)
}

// imageDataURL matches a generated image streamed as a base64 data URL in content.
var imageDataURL = regexp.MustCompile(`data:image/[^;,\s]+;base64,[A-Za-z0-9+/=]*`)

func responseText(response *schemas.BifrostResponse) (string, []string) {
	if response == nil {
		return "", nil
	}
	if response.ResponsesResponse != nil {
		return responsesOutputText(response.ResponsesResponse.Output)
	}
	var text strings.Builder
	var reasoning []string
	if response.TextCompletionResponse != nil {
		for _, c := range response.TextCompletionResponse.Choices {
			if c.TextCompletionResponseChoice != nil && c.Text != nil {
				text.WriteString(*c.Text)
			}
		}
	}
	if response.ChatResponse != nil {
		for _, c := range response.ChatResponse.Choices {
			if c.ChatNonStreamResponseChoice == nil || c.Message == nil {
				continue
			}
			m := c.Message
			if m.Content != nil && m.Content.ContentStr != nil {
				text.WriteString(imageDataURL.ReplaceAllString(*m.Content.ContentStr, ""))
			}
			if m.ChatAssistantMessage != nil {
				if m.Refusal != nil {
					text.WriteString(*m.Refusal)
				}
				// Providers that fill both fields carry the same text in each.
				if m.Reasoning != nil && *m.Reasoning != "" {
					reasoning = append(reasoning, *m.Reasoning)
				} else {
					for _, rd := range m.ReasoningDetails {
						if rd.Text != nil && *rd.Text != "" {
							reasoning = append(reasoning, *rd.Text)
						} else if rd.Summary != nil && *rd.Summary != "" {
							reasoning = append(reasoning, *rd.Summary)
						}
					}
				}
				for _, t := range m.ToolCalls {
					id, name, kind := "", "", "function"
					if t.ID != nil {
						id = *t.ID
					}
					if t.Function.Name != nil {
						name = *t.Function.Name
					}
					if t.Type != nil {
						kind = *t.Type
					}
					text.WriteString(toolJSON(id, name, kind, t.Function.Arguments))
				}
			}
		}
	}
	return text.String(), reasoning
}

// responsesOutputText reads a Responses snapshot's output items directly: the
// chat conversion holds reasoning back until a following message arrives, which
// a stream cancelled while thinking never receives.
func responsesOutputText(output []schemas.ResponsesMessage) (string, []string) {
	var text strings.Builder
	var reasoning []string
	for _, item := range output {
		kind := schemas.ResponsesMessageTypeMessage
		if item.Type != nil {
			kind = *item.Type
		}
		switch kind {
		case schemas.ResponsesMessageTypeReasoning:
			// Raw reasoning text when present; otherwise the summary of it.
			var raw []string
			if item.Content != nil {
				for _, block := range item.Content.ContentBlocks {
					if block.Type == schemas.ResponsesOutputMessageContentTypeReasoning && block.Text != nil && *block.Text != "" {
						raw = append(raw, *block.Text)
					}
				}
			}
			if len(raw) == 0 && item.ResponsesReasoning != nil {
				for _, summary := range item.Summary {
					if summary.Text != "" {
						raw = append(raw, summary.Text)
					}
				}
			}
			reasoning = append(reasoning, raw...)
		case schemas.ResponsesMessageTypeFunctionCall:
			if call := item.ResponsesToolMessage; call != nil {
				id, name, arguments := "", "", ""
				if call.CallID != nil {
					id = *call.CallID
				}
				if call.Name != nil {
					name = *call.Name
				}
				if call.Arguments != nil {
					arguments = *call.Arguments
				}
				text.WriteString(toolJSON(id, name, "function", arguments))
			}
		case schemas.ResponsesMessageTypeCustomToolCall:
			if call := item.ResponsesToolMessage; call != nil && call.ResponsesCustomToolCall != nil {
				if call.Name != nil {
					text.WriteString(*call.Name)
				}
				text.WriteString(call.ResponsesCustomToolCall.Input)
			}
		case schemas.ResponsesMessageTypeMessage:
			if item.Content == nil {
				continue
			}
			if item.Content.ContentStr != nil {
				text.WriteString(imageDataURL.ReplaceAllString(*item.Content.ContentStr, ""))
			}
			for _, block := range item.Content.ContentBlocks {
				switch {
				case block.Type == schemas.ResponsesOutputMessageContentTypeText && block.Text != nil:
					text.WriteString(imageDataURL.ReplaceAllString(*block.Text, ""))
				case block.Type == schemas.ResponsesOutputMessageContentTypeRefusal && block.ResponsesOutputMessageContentRefusal != nil:
					text.WriteString(block.Refusal)
				}
			}
		}
	}
	return text.String(), reasoning
}

func promptCount(c namedCounter, request *schemas.BifrostRequest) (int, error) {
	chat := request.ChatRequest
	fromResponses := chat == nil && request.ResponsesRequest != nil
	if fromResponses {
		chat = request.ResponsesRequest.ToChatRequest()
	}
	if request.TextCompletionRequest != nil && request.TextCompletionRequest.Input != nil {
		input := request.TextCompletionRequest.Input
		if input.PromptStr != nil {
			return c.count(*input.PromptStr)
		}
		total := 0
		for _, prompt := range input.PromptArray {
			n, err := c.count(prompt)
			if err != nil {
				return 0, err
			}
			total += n
		}
		return total, nil
	}
	if chat == nil {
		return 0, fmt.Errorf("request unavailable")
	}
	// Full-document decode is intentional: every string-valued message field is
	// counted, plus content blocks and tool arguments.
	encoded, err := providerUtils.MarshalSorted(chat.Input)
	if err != nil {
		return 0, err
	}
	var messages []map[string]interface{}
	if err := sonic.Unmarshal(encoded, &messages); err != nil {
		return 0, err
	}
	total := 3
	for _, message := range messages {
		overhead, nameOverhead := 3, 1
		if chat.Model == "gpt-3.5-turbo-0301" {
			overhead, nameOverhead = 4, -1
		}
		total += overhead
		for key, value := range message {
			switch v := value.(type) {
			case string:
				// Bifrost emits both reasoning spellings; the neutral source is one field.
				if key == "reasoning_content" {
					continue
				}
				n, err := c.count(v)
				if err != nil {
					return 0, err
				}
				total += n
				if key == "name" {
					total += nameOverhead
				}
			case []interface{}:
				for _, item := range v {
					block, ok := item.(map[string]interface{})
					if !ok {
						continue
					}
					var text string
					if key == "tool_calls" {
						if fn, ok := block["function"].(map[string]interface{}); ok {
							text, _ = fn["arguments"].(string)
						}
					} else if key == "content" {
						text, _ = block["text"].(string)
						if text == "" {
							if typ, _ := block["type"].(string); typ != "text" && typ != "input_text" && typ != "output_text" {
								return 0, fmt.Errorf("unsupported prompt content %s", typ)
							}
						}
					}
					n, err := c.count(text)
					if err != nil {
						return 0, err
					}
					total += n
				}
			}
		}
	}
	params := chat.Params
	if fromResponses {
		// The chat conversion keeps only function tools; count tools from the original.
		if params != nil {
			withoutTools := *params
			withoutTools.Tools = nil
			params = &withoutTools
		}
		n, err := responsesToolsCount(c, request.ResponsesRequest.Params)
		if err != nil {
			return 0, err
		}
		total += n
	}
	n, err := paramsCount(c, params)
	if err != nil {
		return 0, err
	}
	return total + n, nil
}

// responsesToolsCount counts the Responses tools whose definitions are rendered
// into the prompt: function, custom (including grammars) and namespace tools.
func responsesToolsCount(c namedCounter, params *schemas.ResponsesParameters) (int, error) {
	if params == nil {
		return 0, nil
	}
	total := 0
	for _, tool := range params.Tools {
		switch tool.Type {
		case schemas.ResponsesToolTypeFunction, schemas.ResponsesToolTypeCustom, schemas.ResponsesToolTypeNamespace:
		default:
			continue
		}
		encoded, err := providerUtils.MarshalSorted(tool)
		if err != nil {
			return 0, err
		}
		n, err := c.count(string(encoded))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// paramsCount counts the request parameters providers render into the prompt:
// function and custom tool definitions, and a JSON-schema response format.
// Server-side tools (web search and the like) carry no prompt text.
func paramsCount(c namedCounter, params *schemas.ChatParameters) (int, error) {
	if params == nil {
		return 0, nil
	}
	var parts []interface{}
	for _, tool := range params.Tools {
		switch {
		case tool.Function != nil:
			parts = append(parts, tool.Function)
		case tool.Custom != nil:
			parts = append(parts, tool.Custom)
		}
	}
	total := 0
	if params.ResponseFormat != nil {
		switch format := (*params.ResponseFormat).(type) {
		case json.RawMessage: // how HTTP requests keep it, preserving schema key order
			if gjson.GetBytes(format, "type").String() == "json_schema" {
				if schema := gjson.GetBytes(format, "json_schema"); schema.Exists() {
					n, err := c.count(schema.Raw)
					if err != nil {
						return 0, err
					}
					total += n
				}
			}
		case map[string]interface{}:
			if format["type"] == "json_schema" && format["json_schema"] != nil {
				parts = append(parts, format["json_schema"])
			}
		}
	}
	for _, part := range parts {
		encoded, err := providerUtils.MarshalSorted(part)
		if err != nil {
			return 0, err
		}
		n, err := c.count(string(encoded))
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
