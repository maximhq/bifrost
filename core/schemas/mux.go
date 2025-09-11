package schemas

import (
	"github.com/maximhq/bifrost/core/schemas/apis/openai"
)

// INTELLIGENT MULTIPLEXER (MUX) IMPLEMENTATION
//
// This file contains intelligent multiplexer methods that provide bidirectional
// synchronization between normal/standard fields and OpenAI Responses API fields.
//
// The mux logic follows this pattern:
// - When usingResponsesAPI = true:  Normal fields → Responses API fields (only if Responses API fields are nil)
// - When usingResponsesAPI = false: Responses API fields → Normal fields (only if Normal fields are nil)
//
// Key Benefits:
// 1. Conditional Transfer: Only transfers when destination is empty (nil pointers)
// 2. Recursive Handling: Automatically handles nested structures
// 3. Initialization: Creates embedded structs when needed
// 4. Conflict Avoidance: Skips transfer if both sides have data
//
// Usage Example:
//   tool := &Tool{Type: &[]string{"function"}[0], Function: &Function{Name: "test"}}
//   tool.Mux(true)  // Transfers to Responses API format
//   tool.Mux(false) // Transfers back to normal format

// Mux intelligently synchronizes data between normal fields and Responses API fields
// If usingResponsesAPI is true: transfers normal fields → Responses API fields (only if Responses API fields are nil)
// If usingResponsesAPI is false: transfers Responses API fields → normal fields (only if normal fields are nil)
func (t *Tool) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Initialize ResponsesAPIExtendedTool if nil
		if t.ResponsesAPIExtendedTool == nil {
			t.ResponsesAPIExtendedTool = &openai.ResponsesAPIExtendedTool{}
		}

		// Transfer normal fields to Responses API fields (only if Responses API fields are nil)
		if t.ResponsesAPIExtendedTool.Name == nil && t.Function != nil {
			t.ResponsesAPIExtendedTool.Name = &t.Function.Name
		}
		if t.ResponsesAPIExtendedTool.Description == nil && t.Function != nil {
			t.ResponsesAPIExtendedTool.Description = &t.Function.Description
		}

		// Handle function parameters and set appropriate tool type
		if t.Function != nil && t.ResponsesAPIExtendedTool.ToolFunction == nil {
			t.ResponsesAPIExtendedTool.ToolFunction = &openai.ToolFunction{
				Parameters: t.Function.Parameters.Properties,
				Strict:     false, // Default value
			}
		}

		// Set the type in the embedded ResponsesAPITool (parent struct)
		if t.Type == nil {
			t.Type = Ptr(string(ToolChoiceTypeFunction))
		}
	} else {
		// Transfer Responses API fields to normal fields (only if normal fields are nil)
		if t.Function == nil && t.ResponsesAPIExtendedTool != nil {
			// Initialize Function if we have Responses API data
			if t.ResponsesAPIExtendedTool.Name != nil || t.ResponsesAPIExtendedTool.Description != nil ||
				t.ResponsesAPIExtendedTool.ToolFunction != nil {
				t.Function = &Function{}

				if t.ResponsesAPIExtendedTool.Name != nil {
					t.Function.Name = *t.ResponsesAPIExtendedTool.Name
				}
				if t.ResponsesAPIExtendedTool.Description != nil {
					t.Function.Description = *t.ResponsesAPIExtendedTool.Description
				}
				if t.ResponsesAPIExtendedTool.ToolFunction != nil {
					t.Function.Parameters = FunctionParameters{
						Type:       "object", // Default for function parameters
						Properties: t.ResponsesAPIExtendedTool.ToolFunction.Parameters,
					}
				}
			}
		}

		// Transfer Type field
		if t.Type == nil {
			t.Type = Ptr(string(ToolChoiceTypeFunction))
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (tc *ToolChoiceStruct) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Initialize ResponsesAPIToolChoice if nil
		if tc.ResponsesAPIExtendedToolChoice == nil {
			tc.ResponsesAPIExtendedToolChoice = &openai.ResponsesAPIExtendedToolChoice{}
		}

		// Transfer normal fields to Responses API fields
		if tc.Type != nil {
			// Convert ToolChoiceType to string for Responses API
			switch *tc.Type {
			case ToolChoiceTypeNone, ToolChoiceTypeAuto, ToolChoiceTypeRequired:
				if tc.ResponsesAPIExtendedToolChoice.Mode == nil {
					modeStr := string(*tc.Type)
					tc.ResponsesAPIExtendedToolChoice.Mode = &modeStr
				}
			case ToolChoiceTypeFunction:
				tc.ResponsesAPIExtendedToolChoice.Type = "function"

				if tc.Function != nil && tc.ResponsesAPIExtendedToolChoice.Name == nil {
					tc.ResponsesAPIExtendedToolChoice.Name = &tc.Function.Name
				}
			}
		}
	} else {
		// Transfer Responses API fields to normal fields
		if tc.Type == nil && tc.ResponsesAPIExtendedToolChoice != nil {
			// Determine type from Responses API data
			if tc.ResponsesAPIExtendedToolChoice.Mode != nil {
				switch *tc.ResponsesAPIExtendedToolChoice.Mode {
				case "none":
					tc.Type = &[]ToolChoiceType{ToolChoiceTypeNone}[0]
				case "auto":
					tc.Type = &[]ToolChoiceType{ToolChoiceTypeAuto}[0]
				case "required":
					tc.Type = &[]ToolChoiceType{ToolChoiceTypeRequired}[0]
				}
			} else if tc.ResponsesAPIExtendedToolChoice.Type != "" {
				switch tc.ResponsesAPIExtendedToolChoice.Type {
				case "function":
					tc.Type = &[]ToolChoiceType{ToolChoiceTypeFunction}[0]
					// Transfer function name
					if tc.Function == nil && tc.ResponsesAPIExtendedToolChoice.Name != nil {
						tc.Function = &ToolChoiceFunction{
							Name: *tc.ResponsesAPIExtendedToolChoice.Name,
						}
					}
				}
			}
		}
	}
}

// MuxMessageContent handles message content synchronization
func (mc *MessageContent) Mux(usingResponsesAPI bool) {
	// Handle content blocks if they contain embedded Responses API structures
	if mc.ContentBlocks != nil {
		for i := range *mc.ContentBlocks {
			(*mc.ContentBlocks)[i].Mux(usingResponsesAPI)
		}
	}
}

// MuxContentBlocks handles content block synchronization for messages
func (cb *ContentBlock) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Transfer ContentBlock to Responses API content blocks
		if cb.ImageURL != nil && cb.InputMessageContentBlockImage == nil {
			cb.InputMessageContentBlockImage = &openai.InputMessageContentBlockImage{
				ImageURL: &cb.ImageURL.URL,
				Detail:   cb.ImageURL.Detail,
			}
		}

		if cb.File != nil && cb.InputMessageContentBlockFile == nil {
			cb.InputMessageContentBlockFile = &openai.InputMessageContentBlockFile{
				FileData: cb.File.FileData,
				FileID:   cb.File.FileID,
				Filename: cb.File.Filename,
			}
		}
	} else {
		// Transfer Responses API content blocks to ContentBlock
		if cb.InputMessageContentBlockImage != nil && cb.ImageURL == nil {
			cb.ImageURL = &ImageURLStruct{}
			if cb.InputMessageContentBlockImage.ImageURL != nil {
				cb.ImageURL.URL = *cb.InputMessageContentBlockImage.ImageURL
			}
			cb.ImageURL.Detail = cb.InputMessageContentBlockImage.Detail
		}

		if cb.InputMessageContentBlockFile != nil && cb.File == nil {
			// TODO if fileData is nil but fileURL is not nil, then download the file
			cb.File = &InputFile{
				FileData: cb.InputMessageContentBlockFile.FileData,
				FileID:   cb.InputMessageContentBlockFile.FileID,
				Filename: cb.InputMessageContentBlockFile.Filename,
			}
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (tm *ToolMessage) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Initialize ResponsesAPIExtendedToolMessage if nil
		if tm.ResponsesAPIExtendedToolMessage == nil {
			tm.ResponsesAPIExtendedToolMessage = &openai.ResponsesAPIExtendedToolMessage{}
		}
		// Map call id from chat-completions side → responses side
		if tm.ToolCallID != nil && *tm.ToolCallID != "" && tm.ResponsesAPIExtendedToolMessage.CallID == nil {
			tm.ResponsesAPIExtendedToolMessage.CallID = tm.ToolCallID
		}
	} else {
		// Transfer Responses API fields to normal fields
		// Extract tool call ID
		if tm.ToolCallID == nil && tm.ResponsesAPIExtendedToolMessage != nil && tm.ResponsesAPIExtendedToolMessage.CallID != nil {
			tm.ToolCallID = tm.ResponsesAPIExtendedToolMessage.CallID
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (am *AssistantMessage) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Convert Chat Completion Assistant Message to Responses API Output Message

		// Initialize ResponsesAPIExtendedOutputMessageText if nil
		if am.ResponsesAPIExtendedAssistantMessage == nil {
			am.ResponsesAPIExtendedAssistantMessage = &openai.ResponsesAPIExtendedAssistantMessage{}
		}

		// If there is exactly one tool call, mirror its arguments into FunctionToolCall
		if am.ToolCalls != nil && len(*am.ToolCalls) == 1 {
			tc := (*am.ToolCalls)[0]
			if am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall == nil {
				am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall = &openai.FunctionToolCall{
					Arguments: tc.Function.Arguments,
				}
			}
		}
	} else {
		// Transfer Responses API fields to normal fields
		// If FunctionToolCall exists and we don't yet have ToolCalls, synthesize a single tool call
		if am.ToolCalls == nil && am.ResponsesAPIExtendedAssistantMessage != nil && am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall != nil {
			toolType := "function"
			calls := []ToolCall{
				{
					Type: &toolType,
					// ID and Name will be filled by the paired tool message if present
					Function: FunctionCall{
						Arguments: am.ResponsesAPIExtendedAssistantMessage.FunctionToolCall.Arguments,
					},
				},
			}
			am.ToolCalls = &calls
		}
	}

	// Recursively handle annotations
	for i := range am.Annotations {
		am.Annotations[i].Mux(usingResponsesAPI)
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (a *Annotation) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Initialize ResponsesAPIExtendedOutputMessageTextAnnotation if nil
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation == nil {
			a.ResponsesAPIExtendedOutputMessageTextAnnotation = &openai.ResponsesAPIExtendedOutputMessageTextAnnotation{}
		}

		// Transfer citation data to Responses API format
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex == nil {
			a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex = &a.Citation.StartIndex
		}
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex == nil {
			a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex = &a.Citation.EndIndex
		}

		// Handle different citation types
		switch a.Type {
		case "url_citation":
			if a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation == nil {
				a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation = &openai.OutputMessageTextAnnotationURLCitation{
					Title: a.Citation.Title,
				}
				if a.Citation.URL != nil {
					a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation.URL = *a.Citation.URL
				}
			}
		}
	} else {
		// Transfer Responses API fields to normal fields
		if a.ResponsesAPIExtendedOutputMessageTextAnnotation != nil {
			// Extract citation information from Responses API format
			if a.Citation.StartIndex == 0 && a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex != nil {
				a.Citation.StartIndex = *a.ResponsesAPIExtendedOutputMessageTextAnnotation.StartIndex
			}
			if a.Citation.EndIndex == 0 && a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex != nil {
				a.Citation.EndIndex = *a.ResponsesAPIExtendedOutputMessageTextAnnotation.EndIndex
			}

			// Handle URL citation
			if a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation != nil {
				urlCitation := a.ResponsesAPIExtendedOutputMessageTextAnnotation.OutputMessageTextAnnotationURLCitation
				if a.Citation.Title == "" {
					a.Citation.Title = urlCitation.Title
				}
				if a.Citation.URL == nil {
					a.Citation.URL = &urlCitation.URL
				}
				if a.Type == "" {
					a.Type = "url_citation"
				}
			}
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (brc *BifrostResponseChoice) Mux(usingResponsesAPI bool) {
	// Handle both stream and non-stream response choices
	if brc.BifrostNonStreamResponseChoice != nil {
		brc.BifrostNonStreamResponseChoice.Message.Mux(usingResponsesAPI)
	}
	// Stream choices would be handled similarly if needed
}

// User Facing Fields

func (p *ModelParameters) Mux(usingResponsesAPI bool) {
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (bm *BifrostMessage) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Convert chat completion message to Responses API message

		if bm.ResponsesAPIExtendedBifrostMessage == nil {
			bm.ResponsesAPIExtendedBifrostMessage = &openai.ResponsesAPIExtendedBifrostMessage{}
		}

		messageType := "message"

		if bm.AssistantMessage != nil {
			if bm.ResponsesAPIExtendedAssistantMessage == nil {
				bm.ResponsesAPIExtendedAssistantMessage = &openai.ResponsesAPIExtendedAssistantMessage{}
			}

			// Transfer simple text content from chat-completions content → responses assistant text
			if bm.Content.ContentStr != nil && *bm.Content.ContentStr != "" && bm.ResponsesAPIExtendedAssistantMessage.Text == "" {
				bm.ResponsesAPIExtendedAssistantMessage.Text = *bm.Content.ContentStr
			}

			if bm.AssistantMessage.Refusal != nil {
				messageType = "refusal"
			}

			if bm.AssistantMessage.Thought != nil {
				messageType = "reasoning"
			}

			if bm.AssistantMessage.ToolCalls != nil {
				messageType = "function_call"
			}
		}
		if bm.ToolMessage != nil {
			if bm.ResponsesAPIExtendedToolMessage == nil {
				bm.ResponsesAPIExtendedToolMessage = &openai.ResponsesAPIExtendedToolMessage{}
			}
			messageType = "function_call_output"

			// Map ToolCallID → CallID
			if bm.ToolMessage.ToolCallID != nil && *bm.ToolMessage.ToolCallID != "" && bm.ResponsesAPIExtendedToolMessage.CallID == nil {
				bm.ResponsesAPIExtendedToolMessage.CallID = bm.ToolMessage.ToolCallID
			}

			// If tool output content exists on chat-completions side, mirror into responses function_call_output
			if bm.Content.ContentStr != nil && *bm.Content.ContentStr != "" {
				if bm.ResponsesAPIExtendedToolMessage.FunctionToolCallOutput == nil {
					bm.ResponsesAPIExtendedToolMessage.FunctionToolCallOutput = &openai.FunctionToolCallOutput{}
				}
				if bm.ResponsesAPIExtendedToolMessage.FunctionToolCallOutput.Output == "" {
					bm.ResponsesAPIExtendedToolMessage.FunctionToolCallOutput.Output = *bm.Content.ContentStr
				}
			}
		}

		bm.ResponsesAPIExtendedBifrostMessage.Type = &messageType
	}

	bm.Content.Mux(usingResponsesAPI)

	if bm.AssistantMessage != nil {
		bm.AssistantMessage.Mux(usingResponsesAPI)
	}

	if bm.ToolMessage != nil {
		bm.ToolMessage.Mux(usingResponsesAPI)
	}

	if !usingResponsesAPI {
		// Responses → Chat completions mapping
		if bm.AssistantMessage != nil && bm.ResponsesAPIExtendedAssistantMessage != nil {
			if (bm.Content.ContentStr == nil || *bm.Content.ContentStr == "") && bm.ResponsesAPIExtendedAssistantMessage.Text != "" {
				text := bm.ResponsesAPIExtendedAssistantMessage.Text
				bm.Content.ContentStr = &text
			}
		}
		if bm.ToolMessage != nil && bm.ResponsesAPIExtendedToolMessage != nil && bm.ResponsesAPIExtendedToolMessage.FunctionToolCallOutput != nil {
			out := bm.ResponsesAPIExtendedToolMessage.FunctionToolCallOutput.Output
			if out != "" && bm.Content.ContentStr == nil {
				bm.Content.ContentStr = &out
			}
		}
		// Map CallID → ToolCallID
		if bm.ToolMessage != nil && bm.ToolMessage.ToolCallID == nil && bm.ResponsesAPIExtendedToolMessage != nil && bm.ResponsesAPIExtendedToolMessage.CallID != nil {
			bm.ToolMessage.ToolCallID = bm.ResponsesAPIExtendedToolMessage.CallID
		}
	}
}

// Mux intelligently synchronizes data between normal fields and Responses API fields
func (br *BifrostResponse) Mux(usingResponsesAPI bool) {
	if usingResponsesAPI {
		// Initialize ResponseAPIExtendedResponse if nil
		if br.ResponseAPIExtendedResponse == nil {
			br.ResponseAPIExtendedResponse = &openai.ResponseAPIExtendedResponse{}
		}

		// Normalize instructions and output to Responses API shape
		if br.Instructions != nil {
			normalized := NormalizeMessagesMux(true, *br.Instructions)
			*br.Instructions = normalized
		}

		if br.Output != nil && len(*br.Output) > 0 {
			normalized := NormalizeMessagesMux(true, *br.Output)
			*br.Output = normalized
		} else if len(br.Choices) > 0 {
			// Derive Output from Choices (Chat Completions → Responses)
			out := make([]BifrostMessage, 0, len(br.Choices))
			for i := range br.Choices {
				if br.Choices[i].BifrostNonStreamResponseChoice != nil {
					msg := br.Choices[i].BifrostNonStreamResponseChoice.Message
					msg.Mux(true)
					out = append(out, msg)
				}
			}
			if len(out) > 0 {
				normalized := NormalizeMessagesMux(true, out)
				br.Output = &normalized
			}
		}

		// Map params (ExtraFields.Params → Responses request params)
		if br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams == nil {
			br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams = &openai.ResponsesAPIExtendedRequestParams{}
		}
		p := &br.ExtraFields.Params
		if p.Temperature != nil && br.ResponseAPIExtendedResponse.Temperature == nil {
			br.ResponseAPIExtendedResponse.Temperature = p.Temperature
		}
		if p.TopP != nil && br.ResponseAPIExtendedResponse.TopP == nil {
			br.ResponseAPIExtendedResponse.TopP = p.TopP
		}
		if p.TopLogProbs != nil && br.ResponseAPIExtendedResponse.TopLogProbs == nil {
			br.ResponseAPIExtendedResponse.TopLogProbs = p.TopLogProbs
		}
		if p.ParallelToolCalls != nil && br.ResponseAPIExtendedResponse.ParallelToolCalls == nil {
			br.ResponseAPIExtendedResponse.ParallelToolCalls = p.ParallelToolCalls
		}
		if p.MaxTokens != nil && br.ResponseAPIExtendedResponse.MaxOutputTokens == nil {
			// Map chat max_tokens → responses max_output_tokens
			br.ResponseAPIExtendedResponse.MaxOutputTokens = p.MaxTokens
		}
		if p.StreamOptions != nil {
			if br.ResponseAPIExtendedResponse.StreamOptions == nil {
				br.ResponseAPIExtendedResponse.StreamOptions = &openai.StreamOptions{}
			}
			if p.StreamOptions.IncludeObfuscation != nil && br.ResponseAPIExtendedResponse.StreamOptions.IncludeObfuscation == nil {
				br.ResponseAPIExtendedResponse.StreamOptions.IncludeObfuscation = p.StreamOptions.IncludeObfuscation
			}
		}

		// Map usage (chat-style → responses-style)
		if br.Usage != nil {
			if br.Usage.ResponsesAPIExtendedResponseUsage == nil {
				br.Usage.ResponsesAPIExtendedResponseUsage = &openai.ResponsesAPIExtendedResponseUsage{}
			}
			if br.Usage.PromptTokens > 0 && br.Usage.InputTokens == 0 {
				br.Usage.InputTokens = br.Usage.PromptTokens
			}
			if br.Usage.CompletionTokens > 0 && br.Usage.OutputTokens == 0 {
				br.Usage.OutputTokens = br.Usage.CompletionTokens
			}
			if br.Usage.TokenDetails != nil {
				if br.Usage.InputTokensDetails == nil {
					br.Usage.InputTokensDetails = &openai.ResponsesAPIResponseInputTokens{}
				}
				if br.Usage.InputTokensDetails.CachedTokens == 0 && br.Usage.TokenDetails.CachedTokens > 0 {
					br.Usage.InputTokensDetails.CachedTokens = br.Usage.TokenDetails.CachedTokens
				}
			}
			if br.Usage.CompletionTokensDetails != nil {
				if br.Usage.OutputTokensDetails == nil {
					br.Usage.OutputTokensDetails = &openai.ResponsesAPIResponseOutputTokens{}
				}
				if br.Usage.OutputTokensDetails.ReasoningTokens == 0 && br.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
					br.Usage.OutputTokensDetails.ReasoningTokens = br.Usage.CompletionTokensDetails.ReasoningTokens
				}
			}
		}

	} else {
		// Responses → Chat Completions normalization
		if br.Output != nil && len(*br.Output) > 0 {
			normalized := NormalizeMessagesMux(false, *br.Output)
			// Build choices from normalized output
			if len(normalized) > 0 {
				choices := make([]BifrostResponseChoice, 0, len(normalized))
				for idx, m := range normalized {
					m.Mux(false)
					ch := BifrostResponseChoice{
						Index: idx,
						BifrostNonStreamResponseChoice: &BifrostNonStreamResponseChoice{
							Message: m,
						},
					}
					choices = append(choices, ch)
				}
				br.Choices = choices
				*br.Output = normalized
			}
		}

		// Map params (responses request params → ExtraFields.Params)
		if br.ResponseAPIExtendedResponse != nil && br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams != nil {
			rp := br.ResponseAPIExtendedResponse.ResponsesAPIExtendedRequestParams
			// Only fill if target fields are nil to avoid clobbering
			if br.ExtraFields.Params.Temperature == nil && rp.Temperature != nil {
				br.ExtraFields.Params.Temperature = rp.Temperature
			}
			if br.ExtraFields.Params.TopP == nil && rp.TopP != nil {
				br.ExtraFields.Params.TopP = rp.TopP
			}
			if br.ExtraFields.Params.TopLogProbs == nil && rp.TopLogProbs != nil {
				br.ExtraFields.Params.TopLogProbs = rp.TopLogProbs
			}
			if br.ExtraFields.Params.ParallelToolCalls == nil && rp.ParallelToolCalls != nil {
				br.ExtraFields.Params.ParallelToolCalls = rp.ParallelToolCalls
			}
			if br.ExtraFields.Params.MaxTokens == nil && rp.MaxOutputTokens != nil {
				br.ExtraFields.Params.MaxTokens = rp.MaxOutputTokens
			}
			if rp.StreamOptions != nil {
				if br.ExtraFields.Params.StreamOptions == nil {
					br.ExtraFields.Params.StreamOptions = &StreamOptions{}
				}
				if br.ExtraFields.Params.StreamOptions.IncludeObfuscation == nil && rp.StreamOptions.IncludeObfuscation != nil {
					br.ExtraFields.Params.StreamOptions.IncludeObfuscation = rp.StreamOptions.IncludeObfuscation
				}
			}
		}

		// Map usage (responses-style → chat-style)
		if br.Usage != nil && br.Usage.ResponsesAPIExtendedResponseUsage != nil {
			if br.Usage.PromptTokens == 0 && br.Usage.InputTokens > 0 {
				br.Usage.PromptTokens = br.Usage.InputTokens
			}
			if br.Usage.CompletionTokens == 0 && br.Usage.OutputTokens > 0 {
				br.Usage.CompletionTokens = br.Usage.OutputTokens
			}
			if br.Usage.TotalTokens == 0 && (br.Usage.PromptTokens+br.Usage.CompletionTokens) > 0 {
				br.Usage.TotalTokens = br.Usage.PromptTokens + br.Usage.CompletionTokens
			}
			if br.Usage.InputTokensDetails != nil {
				if br.Usage.TokenDetails == nil {
					br.Usage.TokenDetails = &TokenDetails{}
				}
				if br.Usage.TokenDetails.CachedTokens == 0 && br.Usage.InputTokensDetails.CachedTokens > 0 {
					br.Usage.TokenDetails.CachedTokens = br.Usage.InputTokensDetails.CachedTokens
				}
			}
			if br.Usage.OutputTokensDetails != nil {
				if br.Usage.CompletionTokensDetails == nil {
					br.Usage.CompletionTokensDetails = &CompletionTokensDetails{}
				}
				if br.Usage.CompletionTokensDetails.ReasoningTokens == 0 && br.Usage.OutputTokensDetails.ReasoningTokens > 0 {
					br.Usage.CompletionTokensDetails.ReasoningTokens = br.Usage.OutputTokensDetails.ReasoningTokens
				}
			}
		}
	}

	// Recursively handle choices
	for i := range br.Choices {
		br.Choices[i].Mux(usingResponsesAPI)
	}
}

func Ptr(v string) *string {
	return &v
}

// ExpandAssistantToolCallsToResponsesItems converts a single Chat Completions-style assistant message
// (with multiple tool calls in AssistantMessage.ToolCalls) into multiple Responses API items, where
// each tool call becomes its own BifrostMessage with `Type = "function_call"` and a ToolMessage payload.
//
// Notes:
//   - This does NOT mutate the input message.
//   - Each returned message will have only ToolMessage populated (AssistantMessage will be nil),
//     to avoid embedded pointer conflicts during JSON marshalling.
//   - Function name and call_id (if present) are preserved on the item.
func expandAssistantToolCallsToResponsesItems(msg BifrostMessage) []BifrostMessage {
	if msg.AssistantMessage == nil || msg.AssistantMessage.ToolCalls == nil {
		return nil
	}

	toolCalls := *msg.AssistantMessage.ToolCalls
	if len(toolCalls) == 0 {
		return nil
	}

	items := make([]BifrostMessage, 0, len(toolCalls))
	for _, tc := range toolCalls {
		itemType := "function_call"

		var callID *string
		if tc.ID != nil && *tc.ID != "" {
			callID = tc.ID
		}

		var namePtr *string
		if tc.Function.Name != nil && *tc.Function.Name != "" {
			name := *tc.Function.Name
			namePtr = &name
		}

		// Build AssistantMessage with FunctionToolCall and a ToolMessage to carry name/call_id
		assistantMsg := &AssistantMessage{
			ResponsesAPIExtendedAssistantMessage: &openai.ResponsesAPIExtendedAssistantMessage{
				FunctionToolCall: &openai.FunctionToolCall{
					Arguments: tc.Function.Arguments,
				},
			},
		}

		toolMsg := &ToolMessage{
			ResponsesAPIExtendedToolMessage: &openai.ResponsesAPIExtendedToolMessage{
				Name:   namePtr,
				CallID: callID,
			},
		}

		items = append(items, BifrostMessage{
			Role:             ModelChatMessageRoleAssistant,
			Content:          MessageContent{},
			AssistantMessage: assistantMsg,
			ToolMessage:      toolMsg,
			ResponsesAPIExtendedBifrostMessage: &openai.ResponsesAPIExtendedBifrostMessage{
				Type: &itemType,
			},
		})
	}

	return items
}

// AggregateFunctionCallItemsToAssistant converts multiple Responses API items of type
// "function_call" into a single Chat Completions-style assistant message that aggregates
// them under AssistantMessage.ToolCalls.
//
// Only items that:
// - have ResponsesAPIExtendedBifrostMessage with Type=="function_call", and
// - have ToolMessage with a FunctionToolCall payload
// are considered. Others are ignored.
func aggregateFunctionCallItemsToAssistant(items []BifrostMessage) *BifrostMessage {
	if len(items) == 0 {
		return nil
	}

	accumulated := make([]ToolCall, 0, len(items))
	for _, it := range items {
		if it.ResponsesAPIExtendedBifrostMessage == nil || it.ResponsesAPIExtendedBifrostMessage.Type == nil {
			continue
		}
		if *it.ResponsesAPIExtendedBifrostMessage.Type != "function_call" {
			continue
		}
		if it.AssistantMessage == nil || it.AssistantMessage.ResponsesAPIExtendedAssistantMessage == nil {
			continue
		}
		ame := it.AssistantMessage.ResponsesAPIExtendedAssistantMessage
		if ame.FunctionToolCall == nil {
			continue
		}

		// Build a ToolCall entry
		toolType := "function"
		var namePtr *string
		// Name/CallID live on ToolMessage; be tolerant if missing
		var name string
		if it.ToolMessage != nil && it.ToolMessage.ResponsesAPIExtendedToolMessage != nil && it.ToolMessage.ResponsesAPIExtendedToolMessage.Name != nil && *it.ToolMessage.ResponsesAPIExtendedToolMessage.Name != "" {
			name = *it.ToolMessage.ResponsesAPIExtendedToolMessage.Name
			namePtr = &name
		}

		accumulated = append(accumulated, ToolCall{
			Type: &toolType,
			ID: func() *string {
				if it.ToolMessage != nil && it.ToolMessage.ResponsesAPIExtendedToolMessage != nil {
					return it.ToolMessage.ResponsesAPIExtendedToolMessage.CallID
				}
				return nil
			}(),
			Function: FunctionCall{
				Name:      namePtr,
				Arguments: ame.FunctionToolCall.Arguments,
			},
		})
	}

	if len(accumulated) == 0 {
		return nil
	}

	// Create a single assistant message with multiple tool_calls
	return &BifrostMessage{
		Role:    ModelChatMessageRoleAssistant,
		Content: MessageContent{},
		AssistantMessage: &AssistantMessage{
			ToolCalls: &accumulated,
		},
	}
}

// normalizeMessagesMux is a pipeline that converts between Chat Completions-style
// and Responses API-style lists while populating both sets of fields via Mux.
//
// If usingResponsesAPI == true:
//   - Expands any assistant message with multiple tool_calls into multiple
//     items with Type = "function_call" (one per tool).
//   - Calls Mux(true) on every resulting message to populate Responses fields.
//
// If usingResponsesAPI == false:
//   - Aggregates consecutive items with Type = "function_call" into a single
//     assistant message with tool_calls[].
//   - Calls Mux(false) on every resulting message to populate Chat Completions fields.
func NormalizeMessagesMux(usingResponsesAPI bool, messages []BifrostMessage) []BifrostMessage {
	if len(messages) == 0 {
		return messages
	}

	if usingResponsesAPI {
		// Expand assistant tool_calls to function_call items
		out := make([]BifrostMessage, 0, len(messages))
		for _, m := range messages {
			if m.AssistantMessage != nil && m.AssistantMessage.ToolCalls != nil && len(*m.AssistantMessage.ToolCalls) > 0 {
				expanded := expandAssistantToolCallsToResponsesItems(m)
				for i := range expanded {
					expanded[i].Mux(true)
					out = append(out, expanded[i])
				}
				continue
			}
			m.Mux(true)
			out = append(out, m)
		}
		return out
	}

	// Aggregate consecutive function_call items
	out := make([]BifrostMessage, 0, len(messages))
	pending := make([]BifrostMessage, 0)

	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		if agg := aggregateFunctionCallItemsToAssistant(pending); agg != nil {
			agg.Mux(false)
			out = append(out, *agg)
		}
		pending = pending[:0]
	}

	for _, m := range messages {
		if m.ResponsesAPIExtendedBifrostMessage != nil && m.ResponsesAPIExtendedBifrostMessage.Type != nil && *m.ResponsesAPIExtendedBifrostMessage.Type == "function_call" {
			pending = append(pending, m)
			continue
		}
		flushPending()
		m.Mux(false)
		out = append(out, m)
	}
	flushPending()
	return out
}
