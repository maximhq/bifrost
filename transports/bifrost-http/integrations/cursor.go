package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// Chat completion chunk types for Cursor responses
// These lightweight structs produce clean chat completion JSON without the
// extra_fields that BifrostChatResponse would include.

type cursorChatChunk struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []cursorChatChoice `json:"choices"`
	Usage   *cursorUsage       `json:"usage,omitempty"`
}

type cursorChatChoice struct {
	Index        int             `json:"index"`
	Delta        cursorChatDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type cursorChatDelta struct {
	Role      *string               `json:"role,omitempty"`
	Content   *string               `json:"content,omitempty"`
	Reasoning *string               `json:"reasoning,omitempty"`
	ToolCalls []cursorToolCallDelta `json:"tool_calls,omitempty"`
}

type cursorToolCallDelta struct {
	Index    int                    `json:"index"`
	ID       *string                `json:"id,omitempty"`
	Type     *string                `json:"type,omitempty"`
	Function *cursorToolCallFnDelta `json:"function,omitempty"`
}

type cursorToolCallFnDelta struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

type cursorUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Non-streaming chat completion types

type cursorChatCompletion struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"`
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []cursorChatCompletionChoice `json:"choices"`
	Usage   *cursorUsage                 `json:"usage,omitempty"`
}

type cursorChatCompletionChoice struct {
	Index        int                         `json:"index"`
	Message      cursorChatCompletionMessage `json:"message"`
	FinishReason string                      `json:"finish_reason"`
}

type cursorChatCompletionMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []cursorToolCall `json:"tool_calls,omitempty"`
}

type cursorToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function cursorToolCallFn `json:"function"`
}

type cursorToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Converter helpers

// cursorChunkID builds a deterministic chunk ID from the response extra fields.
func cursorChunkID(extras *schemas.BifrostResponseExtraFields) string {
	return "chatcmpl-bifrost-" + strconv.Itoa(extras.ChunkIndex)
}

// cursorModel returns the best model name available from extra fields.
func cursorModel(extras *schemas.BifrostResponseExtraFields) string {
	if extras.ModelDeployment != "" {
		return extras.ModelDeployment
	}
	return extras.ModelRequested
}

// convertResponsesStreamToChatChunk maps a Responses API stream event to a
// chat completion chunk. Returns ("", nil, nil) for events that should be skipped.
func convertResponsesStreamToChatChunk(resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
	switch resp.Type {
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		if resp.Item == nil {
			return "", nil, nil
		}
		// Function call item → send first tool call chunk with id, type, and name
		// NOTE: This must be checked before the role branch because function_call
		// items can also carry role:"assistant", which would cause an early return
		// and prevent the tool-call id/type/name chunk from being emitted.
		if resp.Item.Type != nil && *resp.Item.Type == schemas.ResponsesMessageTypeFunctionCall &&
			resp.Item.ResponsesToolMessage != nil {
			fnType := "function"
			toolCallIndex := 0
			if resp.OutputIndex != nil {
				toolCallIndex = *resp.OutputIndex
			}
			tc := cursorToolCallDelta{
				Index:    toolCallIndex,
				ID:       resp.Item.CallID,
				Type:     &fnType,
				Function: &cursorToolCallFnDelta{Name: resp.Item.Name},
			}
			return "", &cursorChatChunk{
				ID:      cursorChunkID(&resp.ExtraFields),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   cursorModel(&resp.ExtraFields),
				Choices: []cursorChatChoice{{
					Index: 0,
					Delta: cursorChatDelta{ToolCalls: []cursorToolCallDelta{tc}},
				}},
			}, nil
		}
		// Assistant text output item → send role delta
		if resp.Item.Role != nil && *resp.Item.Role == schemas.ResponsesInputMessageRoleAssistant {
			role := "assistant"
			return "", &cursorChatChunk{
				ID:      cursorChunkID(&resp.ExtraFields),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   cursorModel(&resp.ExtraFields),
				Choices: []cursorChatChoice{{
					Index: 0,
					Delta: cursorChatDelta{Role: &role},
				}},
			}, nil
		}
		return "", nil, nil

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		if resp.Delta == nil {
			return "", nil, nil
		}
		toolCallIndex := 0
		if resp.OutputIndex != nil {
			toolCallIndex = *resp.OutputIndex
		}
		tc := cursorToolCallDelta{
			Index:    toolCallIndex,
			Function: &cursorToolCallFnDelta{Arguments: resp.Delta},
		}
		return "", &cursorChatChunk{
			ID:      cursorChunkID(&resp.ExtraFields),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   cursorModel(&resp.ExtraFields),
			Choices: []cursorChatChoice{{
				Index: 0,
				Delta: cursorChatDelta{ToolCalls: []cursorToolCallDelta{tc}},
			}},
		}, nil

	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		if resp.Delta == nil {
			return "", nil, nil
		}
		return "", &cursorChatChunk{
			ID:      cursorChunkID(&resp.ExtraFields),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   cursorModel(&resp.ExtraFields),
			Choices: []cursorChatChoice{{
				Index: 0,
				Delta: cursorChatDelta{Content: resp.Delta},
			}},
		}, nil

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		if resp.Delta == nil {
			return "", nil, nil
		}
		return "", &cursorChatChunk{
			ID:      cursorChunkID(&resp.ExtraFields),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   cursorModel(&resp.ExtraFields),
			Choices: []cursorChatChoice{{
				Index: 0,
				Delta: cursorChatDelta{Reasoning: resp.Delta},
			}},
		}, nil

	case schemas.ResponsesStreamResponseTypeRefusalDelta:
		// Map refusal to content so Cursor can display it
		if resp.Refusal == nil {
			return "", nil, nil
		}
		return "", &cursorChatChunk{
			ID:      cursorChunkID(&resp.ExtraFields),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   cursorModel(&resp.ExtraFields),
			Choices: []cursorChatChoice{{
				Index: 0,
				Delta: cursorChatDelta{Content: resp.Refusal},
			}},
		}, nil

	case schemas.ResponsesStreamResponseTypeCompleted:
		finishReason := "stop"
		// If the response contains function call items, use "tool_calls" finish reason
		if resp.Response != nil {
			for _, item := range resp.Response.Output {
				if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall {
					finishReason = "tool_calls"
					break
				}
			}
		}
		chunk := &cursorChatChunk{
			ID:      cursorChunkID(&resp.ExtraFields),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   cursorModel(&resp.ExtraFields),
			Choices: []cursorChatChoice{{
				Index:        0,
				Delta:        cursorChatDelta{},
				FinishReason: &finishReason,
			}},
		}
		// Include usage from the completed response if available
		if resp.Response != nil && resp.Response.Usage != nil {
			chunk.Usage = &cursorUsage{
				PromptTokens:     resp.Response.Usage.InputTokens,
				CompletionTokens: resp.Response.Usage.OutputTokens,
				TotalTokens:      resp.Response.Usage.TotalTokens,
			}
			// Use response ID if available
			if resp.Response.ID != nil {
				chunk.ID = "chatcmpl-" + *resp.Response.ID
			}
		}
		return "", chunk, nil

	case schemas.ResponsesStreamResponseTypeFailed:
		finishReason := "stop"
		return "", &cursorChatChunk{
			ID:      cursorChunkID(&resp.ExtraFields),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   cursorModel(&resp.ExtraFields),
			Choices: []cursorChatChoice{{
				Index:        0,
				Delta:        cursorChatDelta{},
				FinishReason: &finishReason,
			}},
		}, nil

	default:
		// Skip all other Responses API events (created, in_progress, content part events, etc.)
		return "", nil, nil
	}
}

// convertResponsesResponseToChatCompletion converts a non-streaming Responses API
// response to a chat completion response object.
func convertResponsesResponseToChatCompletion(resp *schemas.BifrostResponsesResponse) *cursorChatCompletion {
	// Extract text content and tool calls from output messages
	var sb strings.Builder
	var toolCalls []cursorToolCall
	finishReason := "stop"

	for _, msg := range resp.Output {
		// Function call items
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall && msg.ResponsesToolMessage != nil {
			callID := ""
			if msg.CallID != nil {
				callID = *msg.CallID
			}
			name := ""
			if msg.Name != nil {
				name = *msg.Name
			}
			args := ""
			if msg.Arguments != nil {
				args = *msg.Arguments
			}
			toolCalls = append(toolCalls, cursorToolCall{
				ID:   callID,
				Type: "function",
				Function: cursorToolCallFn{
					Name:      name,
					Arguments: args,
				},
			})
			finishReason = "tool_calls"
			continue
		}
		// Text content
		if msg.Content == nil {
			continue
		}
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == schemas.ResponsesOutputMessageContentTypeText && block.Text != nil {
				sb.WriteString(*block.Text)
			}
		}
	}
	content := sb.String()

	id := "chatcmpl-bifrost"
	if resp.ID != nil {
		id = "chatcmpl-" + *resp.ID
	}

	message := cursorChatCompletionMessage{
		Role:    "assistant",
		Content: content,
	}
	if len(toolCalls) > 0 {
		message.ToolCalls = toolCalls
	}

	result := &cursorChatCompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: int64(resp.CreatedAt),
		Model:   resp.Model,
		Choices: []cursorChatCompletionChoice{{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		}},
	}

	if resp.Usage != nil {
		result.Usage = &cursorUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}

	return result
}

// Cursor raw tool type
// cursorRawTool represents the tool format Cursor actually sends:
//
//	{"name":"Shell","description":"...","input_schema":{"type":"object",...}}
//
// This is neither Responses API format (which requires a "type" field) nor
// Chat Completions format (which wraps in {"type":"function","function":{...}}).
type cursorRawTool struct {
	Name        string                          `json:"name"`
	Description string                          `json:"description"`
	InputSchema *schemas.ToolFunctionParameters `json:"input_schema,omitempty"`
}

// Cursor request parsing
// cursorRequestParser handles Cursor's hybrid request format where input uses
// Responses API format but tools may use chat completions format
// ({"type":"function","function":{"name":"...","parameters":{...}}}).
//
// This is used as a RequestParser on the route config so it is explicitly called
// by the router, avoiding reliance on sonic's UnmarshalJSON dispatch through interface{}.
func cursorRequestParser(ctx *fasthttp.RequestCtx, req interface{}) error {
	cursorReq, ok := req.(*openai.OpenAIResponsesRequest)
	if !ok {
		return errors.New("invalid request type for cursor parser")
	}

	data := ctx.Request.Body()
	if len(data) == 0 {
		return nil
	}

	// Happy path: standard Responses API format (or no tools at all)
	if err := sonic.Unmarshal(data, cursorReq); err == nil {
		// If input is empty, Cursor may have sent "messages" (chat completions key)
		// instead of "input" (Responses API key). Convert messages → input.
		if len(cursorReq.Input.OpenAIResponsesRequestInputArray) == 0 && cursorReq.Input.OpenAIResponsesRequestInputStr == nil {
			cursorConvertMessagesToInput(data, cursorReq)
		}
		cursorConvertAnthropicToolBlocks(data, cursorReq)
		cursorMergeToolResultsFromMessages(data, cursorReq)
		normalizeInputContentBlocks(cursorReq)
		return nil
	}

	// Fallback: tools may be in Cursor's flat format (no "type" field, "input_schema"
	// instead of "parameters") which causes ResponsesTool.UnmarshalJSON to fail.
	// Parse all fields except tools using a tools-free struct to avoid triggering
	// ResponsesTool.UnmarshalJSON.
	type responsesParamsNoTools struct {
		schemas.ResponsesParameters
		Tools json.RawMessage `json:"tools,omitempty"` // shadow to absorb without parsing
	}
	var base struct {
		Model     string                             `json:"model"`
		Input     openai.OpenAIResponsesRequestInput `json:"input"`
		Stream    *bool                              `json:"stream,omitempty"`
		Fallbacks []string                           `json:"fallbacks,omitempty"`
		responsesParamsNoTools
	}
	if err := sonic.Unmarshal(data, &base); err != nil {
		return err
	}

	cursorReq.Model = base.Model
	cursorReq.Input = base.Input
	cursorReq.Stream = base.Stream
	cursorReq.Fallbacks = base.Fallbacks
	cursorReq.ResponsesParameters = base.ResponsesParameters

	// If input is empty, Cursor may have sent "messages" instead of "input"
	if len(cursorReq.Input.OpenAIResponsesRequestInputArray) == 0 && cursorReq.Input.OpenAIResponsesRequestInputStr == nil {
		cursorConvertMessagesToInput(data, cursorReq)
	}

	cursorConvertAnthropicToolBlocks(data, cursorReq)
	cursorMergeToolResultsFromMessages(data, cursorReq)
	normalizeInputContentBlocks(cursorReq)

	// Parse tools from Cursor's flat format:
	//   {"name":"Shell","description":"...","input_schema":{"type":"object","properties":{...},"required":[...]}}
	// This differs from both Responses API format (has "type" field) and Chat Completions format
	// (wraps in {"type":"function","function":{...}}). Cursor uses "input_schema" instead of "parameters".
	var toolsWrapper struct {
		Tools []cursorRawTool `json:"tools"`
	}
	if err := sonic.Unmarshal(data, &toolsWrapper); err != nil {
		return err
	}
	for i := range toolsWrapper.Tools {
		t := &toolsWrapper.Tools[i]
		name := t.Name
		desc := t.Description
		cursorReq.ResponsesParameters.Tools = append(cursorReq.ResponsesParameters.Tools, schemas.ResponsesTool{
			Type:        schemas.ResponsesToolTypeFunction,
			Name:        &name,
			Description: &desc,
			ResponsesToolFunction: &schemas.ResponsesToolFunction{
				Parameters: t.InputSchema,
			},
		})
	}

	return nil
}

// cursorConvertAnthropicToolBlocks handles Cursor's Anthropic-style tool_use and tool_result
// content blocks. Cursor can send these inside Responses API messages, but the standard
// ResponsesMessageContentBlock struct doesn't have fields for tool_use_id or tool_result content,
// so the data is lost during parsing. This function re-parses the raw JSON to extract tool blocks
// and converts them to proper Responses API messages (function_call / function_call_output).
func cursorConvertAnthropicToolBlocks(data []byte, cursorReq *openai.OpenAIResponsesRequest) {
	// Quick check: only process if there are tool_use or tool_result blocks in the raw JSON
	if !bytes.Contains(data, []byte("\"tool_use\"")) && !bytes.Contains(data, []byte("\"tool_result\"")) {
		return
	}

	// Re-parse from raw JSON to access tool block fields
	// Try both "input" (Responses API) and "messages" (chat completions) keys
	type rawMessage struct {
		Type    *string         `json:"type,omitempty"`
		Role    *string         `json:"role,omitempty"`
		ID      *string         `json:"id,omitempty"`
		Status  *string         `json:"status,omitempty"`
		Content json.RawMessage `json:"content,omitempty"`
	}

	var rawInput struct {
		Input    []rawMessage `json:"input"`
		Messages []rawMessage `json:"messages"`
	}
	if err := sonic.Unmarshal(data, &rawInput); err != nil {
		return
	}

	// Use whichever array has content - prefer input, fallback to messages
	rawMessages := rawInput.Input
	if len(rawMessages) == 0 && len(rawInput.Messages) > 0 {
		rawMessages = rawInput.Messages
	}
	if len(rawMessages) == 0 {
		return
	}

	// Save the pre-converted input so we can reuse rich messages (with image blocks,
	// multi-part content, etc.) instead of falling back to createBasicMessage which
	// only preserves plain-string content.
	preConvertedInput := cursorReq.Input.OpenAIResponsesRequestInputArray

	type anthropicContentBlock struct {
		// Anthropic-style fields
		Type      string          `json:"type"`
		Text      *string         `json:"text,omitempty"`
		ID        *string         `json:"id,omitempty"`
		Name      *string         `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID *string         `json:"tool_use_id,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
		// OpenAI-style fields (Cursor may use these instead of Anthropic-style)
		ToolCallID *string `json:"tool_call_id,omitempty"`
		Function   *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function,omitempty"`
	}

	// Helper to create a basic message from raw data when no tool blocks present
	createBasicMessage := func(rawMsg rawMessage) schemas.ResponsesMessage {
		msgType := schemas.ResponsesMessageTypeMessage
		var role schemas.ResponsesMessageRoleType
		if rawMsg.Role != nil {
			switch *rawMsg.Role {
			case "assistant":
				role = schemas.ResponsesInputMessageRoleAssistant
			case "system":
				role = schemas.ResponsesInputMessageRoleSystem
			default:
				role = schemas.ResponsesInputMessageRoleUser
			}
		} else {
			role = schemas.ResponsesInputMessageRoleUser
		}
		msg := schemas.ResponsesMessage{
			Type: &msgType,
			Role: &role,
		}
		// Try to set content
		if rawMsg.Content != nil && len(rawMsg.Content) > 0 {
			// Try as string first
			var contentStr string
			if err := sonic.Unmarshal(rawMsg.Content, &contentStr); err == nil {
				msg.Content = &schemas.ResponsesMessageContent{
					ContentStr: &contentStr,
				}
			}
		}
		return msg
	}

	var newInput []schemas.ResponsesMessage
	for i, rawMsg := range rawMessages {
		messageStart := len(newInput)
		if rawMsg.Content == nil || len(rawMsg.Content) == 0 {
			// Keep original message as-is if available, otherwise create basic message
			if i < len(preConvertedInput) {
				newInput = append(newInput, preConvertedInput[i])
			} else {
				newInput = append(newInput, createBasicMessage(rawMsg))
			}
			continue
		}

		// Try to parse content as array of blocks
		var blocks []anthropicContentBlock
		if err := sonic.Unmarshal(rawMsg.Content, &blocks); err != nil {
			// Content is a string or unparseable — keep original or create basic
			if i < len(preConvertedInput) {
				newInput = append(newInput, preConvertedInput[i])
			} else {
				newInput = append(newInput, createBasicMessage(rawMsg))
			}
			continue
		}

		hasToolBlocks := false
		for _, b := range blocks {
			if b.Type == "tool_use" || b.Type == "tool_result" {
				hasToolBlocks = true
				break
			}
		}

		if !hasToolBlocks {
			// No Anthropic tool blocks — keep original message or create basic
			if i < len(preConvertedInput) {
				newInput = append(newInput, preConvertedInput[i])
			} else {
				newInput = append(newInput, createBasicMessage(rawMsg))
			}
			continue
		}

		// Split message into regular content blocks and tool blocks
		var regularBlocks []schemas.ResponsesMessageContentBlock
		nextRegularIdx := 0
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				// Convert to function_call message
				// Support both Anthropic-style (id, name, input) and OpenAI-style (tool_call_id, function.name, function.arguments)
				callID := b.ID
				if callID == nil {
					callID = b.ToolCallID
				}
				toolName := b.Name
				var arguments *string
				if b.Function != nil {
					// OpenAI-style: function.name and function.arguments
					if toolName == nil {
						fnName := b.Function.Name
						toolName = &fnName
					}
					if b.Function.Arguments != "" {
						arguments = &b.Function.Arguments
					}
				}
				if arguments == nil && len(b.Input) > 0 && string(b.Input) != "null" {
					argStr := string(b.Input)
					arguments = &argStr
				}
				fcType := schemas.ResponsesMessageTypeFunctionCall
				newInput = append(newInput, schemas.ResponsesMessage{
					ID:     callID,
					Type:   &fcType,
					Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    callID,
						Name:      toolName,
						Arguments: arguments,
					},
				})
			case "tool_result":
				// Convert to function_call_output message
				// Support both Anthropic-style (tool_use_id) and OpenAI-style (tool_call_id)
				resultCallID := b.ToolUseID
				if resultCallID == nil {
					resultCallID = b.ToolCallID
				}
				fcoType := schemas.ResponsesMessageTypeFunctionCallOutput
				msg := schemas.ResponsesMessage{
					Type: &fcoType,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: resultCallID,
					},
				}
				// Extract output content
				if len(b.Content) > 0 && string(b.Content) != "null" {
					// Try as string first
					var contentStr string
					if err := sonic.Unmarshal(b.Content, &contentStr); err == nil {
						msg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{
							ResponsesToolCallOutputStr: &contentStr,
						}
					} else {
						// Try as array of content blocks
						var contentBlocks []struct {
							Type string  `json:"type"`
							Text *string `json:"text,omitempty"`
						}
						if err := sonic.Unmarshal(b.Content, &contentBlocks); err == nil {
							var text strings.Builder
							for _, cb := range contentBlocks {
								if cb.Text != nil {
									text.WriteString(*cb.Text)
								}
							}
							if text.Len() > 0 {
								s := text.String()
								msg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{
									ResponsesToolCallOutputStr: &s,
								}
							}
						}
					}
				}
				newInput = append(newInput, msg)
			default:
				// Regular content block (text, image, etc.) — collect for the original message
				matched := false
				if i < len(preConvertedInput) {
					origMsg := preConvertedInput[i]
					if origMsg.Content != nil {
						for nextRegularIdx < len(origMsg.Content.ContentBlocks) {
							origBlock := origMsg.Content.ContentBlocks[nextRegularIdx]
							nextRegularIdx++
							if string(origBlock.Type) == b.Type {
								regularBlocks = append(regularBlocks, origBlock)
								matched = true
								break
							}
						}
					}
				}
				// Fallback: create a text block if we have text
				if !matched && b.Text != nil {
					blockType := schemas.ResponsesInputMessageContentBlockTypeText
					if rawMsg.Role != nil && *rawMsg.Role == string(schemas.ResponsesInputMessageRoleAssistant) {
						blockType = schemas.ResponsesOutputMessageContentTypeText
					}
					regularBlocks = append(regularBlocks, schemas.ResponsesMessageContentBlock{
						Type: blockType,
						Text: b.Text,
					})
				}
			}
		}

		// If there were regular content blocks alongside tool blocks, add the original message with just those
		if len(regularBlocks) > 0 {
			var role *schemas.ResponsesMessageRoleType
			if rawMsg.Role != nil {
				r := schemas.ResponsesMessageRoleType(*rawMsg.Role)
				role = &r
			}
			msg := schemas.ResponsesMessage{
				Role: role,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: regularBlocks,
				},
			}
			if rawMsg.Type != nil {
				mt := schemas.ResponsesMessageType(*rawMsg.Type)
				msg.Type = &mt
			}
			// Insert the regular content message before the tool messages
			// Find the position where we started adding tool messages
			insertPos := len(newInput)
			for j := len(newInput) - 1; j >= messageStart; j-- {
				if newInput[j].Type != nil && (*newInput[j].Type == schemas.ResponsesMessageTypeFunctionCall || *newInput[j].Type == schemas.ResponsesMessageTypeFunctionCallOutput) {
					insertPos = j
				} else {
					break
				}
			}
			newInput = append(newInput[:insertPos], append([]schemas.ResponsesMessage{msg}, newInput[insertPos:]...)...)
		}
	}

	cursorReq.Input = openai.OpenAIResponsesRequestInput{
		OpenAIResponsesRequestInputArray: newInput,
	}
}

// normalizeInputContentBlocks ensures all input messages have ContentBlocks instead of
// ContentStr. Some providers (e.g. Anthropic) require content as an array of content blocks,
// but Cursor may send content as a plain string. This must run after all parsing paths.
func normalizeInputContentBlocks(req *openai.OpenAIResponsesRequest) {
	for i := range req.Input.OpenAIResponsesRequestInputArray {
		msg := &req.Input.OpenAIResponsesRequestInputArray[i]
		if msg.Content != nil && msg.Content.ContentStr != nil {
			text := msg.Content.ContentStr
			blockType := schemas.ResponsesInputMessageContentBlockTypeText
			if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleAssistant {
				blockType = schemas.ResponsesOutputMessageContentTypeText
			}
			msg.Content = &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: blockType,
					Text: text,
				}},
			}
		}
	}
}

// isEffectivelyEmptyContent checks whether a message's content would produce an empty
// content array after provider-level conversion. Cursor can send content blocks with
// unrecognized types (e.g., "tool_result") that downstream converters filter out,
// resulting in empty content. This uses a whitelist of known-good content types.
func isEffectivelyEmptyContent(content *schemas.ResponsesMessageContent) bool {
	if content == nil {
		return true
	}
	if content.ContentStr != nil && strings.TrimSpace(*content.ContentStr) != "" {
		return false
	}
	if len(content.ContentBlocks) == 0 {
		return true
	}
	for _, block := range content.ContentBlocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText:
			if block.Text != nil && strings.TrimSpace(*block.Text) != "" {
				return false
			}
		case schemas.ResponsesInputMessageContentBlockTypeImage:
			if block.ResponsesInputMessageContentBlockImage != nil {
				return false
			}
		case schemas.ResponsesInputMessageContentBlockTypeFile:
			if block.ResponsesInputMessageContentBlockFile != nil {
				return false
			}
		case schemas.ResponsesInputMessageContentBlockTypeAudio:
			if block.Audio != nil {
				return false
			}
		case schemas.ResponsesOutputMessageContentTypeCompaction:
			if block.ResponsesOutputMessageContentCompaction != nil {
				return false
			}
			// All other types (tool_result, unknown types, etc.) are considered
			// effectively empty since downstream converters will filter them out.
		}
	}
	return true
}

// normalizeBifrostInputContentBlocks ensures all input messages in a BifrostResponsesRequest
// have ContentBlocks instead of ContentStr. This is a defense-in-depth normalization that runs
// AFTER ToBifrostResponsesRequest, which can re-introduce ContentStr when the input is a string.
func normalizeBifrostInputContentBlocks(req *schemas.BifrostResponsesRequest) {
	if req == nil {
		return
	}
	for i := range req.Input {
		msg := &req.Input[i]
		// Normalize message content: ContentStr → ContentBlocks
		if msg.Content != nil && msg.Content.ContentStr != nil {
			text := msg.Content.ContentStr
			blockType := schemas.ResponsesInputMessageContentBlockTypeText
			if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleAssistant {
				blockType = schemas.ResponsesOutputMessageContentTypeText
			}
			msg.Content = &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: blockType,
					Text: text,
				}},
			}
		}
		// Cursor can send user messages with nil, empty, or effectively empty content
		// (e.g., text blocks with nil Text pointers that downstream providers filter out).
		// Anthropic requires user messages to have non-empty, non-whitespace content.
		// Backfill with a placeholder to prevent 400 errors.
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleUser &&
			isEffectivelyEmptyContent(msg.Content) {
			placeholder := "..."
			msg.Content = &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: &placeholder,
				}},
			}
		}
		// Normalize tool output: ResponsesToolCallOutputStr → ResponsesFunctionToolCallOutputBlocks
		if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Output != nil &&
			msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil &&
			msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks == nil {
			text := msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
			msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: text,
			}}
			msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = nil
		}
	}
}

// cursorMergeToolResultsFromMessages checks whether the parsed input is missing
// function_call_output messages and, if so, looks for tool results in the "messages"
// key (chat completions format with role:"tool"). When Cursor sends both "input" and
// "messages", the input array may contain the conversation but omit tool results,
// while the messages array has the complete conversation including role:"tool" entries.
// In that case we replace input with the fully converted messages.
func cursorMergeToolResultsFromMessages(data []byte, cursorReq *openai.OpenAIResponsesRequest) {
	// If we already have function_call_output messages, tool results are present
	for _, msg := range cursorReq.Input.OpenAIResponsesRequestInputArray {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			return
		}
	}

	// Quick check: does the raw body contain role:"tool" in messages?
	type msgProbeWithContent struct {
		Messages []struct {
			Role    *string         `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	var probe msgProbeWithContent
	if err := sonic.Unmarshal(data, &probe); err != nil || len(probe.Messages) == 0 {
		return
	}

	hasToolMessages := false
	for _, m := range probe.Messages {
		if m.Role != nil && *m.Role == "tool" {
			hasToolMessages = true
			break
		}
		// Cursor sends tool results as user messages with Anthropic-style tool_result content blocks
		if m.Role != nil && *m.Role == "user" && bytes.Contains(m.Content, []byte("\"tool_result\"")) {
			hasToolMessages = true
			break
		}
	}
	if !hasToolMessages {
		return
	}

	// Tool results exist in messages but not in input — use messages path instead.
	// This replaces input entirely with the properly converted messages array,
	// which includes function_call and function_call_output messages via ToResponsesMessages().
	cursorConvertMessagesToInput(data, cursorReq)
	// Re-run Anthropic tool-block conversion on the newly replaced input.
	// ToResponsesMessages() doesn't handle Anthropic-style tool_result/tool_use content
	// blocks inside user messages, so we need cursorConvertAnthropicToolBlocks to
	// extract and convert them to proper function_call/function_call_output messages.
	cursorConvertAnthropicToolBlocks(data, cursorReq)
}

// cursorConvertMessagesToInput handles Cursor's use of "messages" (chat completions key)
// instead of "input" (Responses API key). It parses chat completions messages and converts
// them to Responses API input format using ChatMessage.ToResponsesMessages().
func cursorConvertMessagesToInput(data []byte, cursorReq *openai.OpenAIResponsesRequest) {
	var messagesWrapper struct {
		Messages []schemas.ChatMessage `json:"messages"`
	}
	if err := sonic.Unmarshal(data, &messagesWrapper); err != nil || len(messagesWrapper.Messages) == 0 {
		return
	}
	var allInput []schemas.ResponsesMessage
	for i := range messagesWrapper.Messages {
		allInput = append(allInput, messagesWrapper.Messages[i].ToResponsesMessages()...)
	}
	// Normalize ContentStr → ContentBlocks for all messages.
	// ToResponsesMessages() produces ContentStr (string) for user/system/developer messages,
	// but some providers (e.g. Anthropic) require content as an array of content blocks.
	for i := range allInput {
		if allInput[i].Content != nil && allInput[i].Content.ContentStr != nil {
			text := allInput[i].Content.ContentStr
			blockType := schemas.ResponsesInputMessageContentBlockTypeText
			if allInput[i].Role != nil && *allInput[i].Role == schemas.ResponsesInputMessageRoleAssistant {
				blockType = schemas.ResponsesOutputMessageContentTypeText
			}
			allInput[i].Content = &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: blockType,
					Text: text,
				}},
			}
		}
	}
	cursorReq.Input = openai.OpenAIResponsesRequestInput{
		OpenAIResponsesRequestInputArray: allInput,
	}
}

// CursorRouter holds route registrations for Cursor IDE endpoints.
// Cursor sends hybrid payloads using the OpenAI Responses API format
// (input field with input_text content blocks) to chat completions endpoints.
// This router routes /cursor/v1/chat/completions through the Responses API pipeline
// while converting responses back to chat completions format that Cursor expects.
type CursorRouter struct {
	*GenericRouter
}

// CreateCursorChatCompletionsRouteConfigs creates route configs for Cursor's chat completions endpoint.
// It parses requests as OpenAI Responses API format since Cursor's payload is valid Responses API format,
// but converts responses back to chat completions format (choices/delta/content).
func CreateCursorChatCompletionsRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	routes := []RouteConfig{}

	for _, path := range []string{
		"/v1/chat/completions",
		"/chat/completions",
	} {
		routes = append(routes, RouteConfig{
			Type:   RouteConfigTypeOpenAI,
			Path:   pathPrefix + path,
			Method: "POST",
			GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ResponsesRequest
			},
			GetRequestTypeInstance: func(ctx context.Context) interface{} {
				return &openai.OpenAIResponsesRequest{}
			},
			RequestParser: cursorRequestParser,
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*openai.OpenAIResponsesRequest); ok {
					bifrostReq := openaiReq.ToBifrostResponsesRequest(ctx)
					if bifrostReq == nil {
						return nil, errors.New("bifrost responses request conversion returned nil")
					}
					normalizeBifrostInputContentBlocks(bifrostReq)
					return &schemas.BifrostRequest{
						ResponsesRequest: bifrostReq,
					}, nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
				return convertResponsesResponseToChatCompletion(resp), nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
			StreamConfig: &StreamConfig{
				ResponsesStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
					return convertResponsesStreamToChatChunk(resp)
				},
				ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
					return err
				},
			},
		})
	}

	return routes
}

// NewCursorRouter creates a new CursorRouter with the given bifrost client.
func NewCursorRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *CursorRouter {
	routes := []RouteConfig{}

	// Custom Responses-based chat completions handler for Cursor's hybrid payloads
	routes = append(routes, CreateCursorChatCompletionsRouteConfigs("/cursor", handlerStore)...)

	// Add OpenAI list models route for /cursor/v1/models
	routes = append(routes, CreateOpenAIListModelsRouteConfigs("/cursor", handlerStore)...)

	// Add Anthropic routes for /cursor/anthropic/...
	routes = append(routes, CreateAnthropicRouteConfigs("/cursor", logger)...)

	// Add Anthropic count tokens route
	routes = append(routes, CreateAnthropicCountTokensRouteConfigs("/cursor", handlerStore)...)

	// Add GenAI routes for /cursor/genai/...
	routes = append(routes, CreateGenAIRouteConfigs("/cursor")...)

	// Add Bedrock routes for /cursor/bedrock/...
	routes = append(routes, CreateBedrockRouteConfigs("/cursor", handlerStore)...)

	// Add Cohere routes for /cursor/cohere/...
	routes = append(routes, CreateCohereRouteConfigs("/cursor")...)

	return &CursorRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, nil, logger),
	}
}
