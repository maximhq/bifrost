package streaming

import (
	"fmt"
	"sort"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// deepCopyResponsesStreamResponse creates a deep copy of BifrostResponsesStreamResponse
// to prevent shared data mutation between different plugin accumulators
func deepCopyResponsesStreamResponse(original *schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse {
	if original == nil {
		return nil
	}

	copy := &schemas.BifrostResponsesStreamResponse{
		Type:           original.Type,
		SequenceNumber: original.SequenceNumber,
		ExtraFields:    original.ExtraFields, // ExtraFields can be safely shared as they're typically read-only
	}

	// Deep copy Response if present
	if original.Response != nil {
		copy.Response = &schemas.BifrostResponsesResponse{}
		*copy.Response = *original.Response // Shallow copy the struct

		// Deep copy the Output slice if present
		if original.Response.Output != nil {
			copy.Response.Output = make([]schemas.ResponsesMessage, len(original.Response.Output))
			for i, msg := range original.Response.Output {
				copy.Response.Output[i] = deepCopyResponsesMessage(msg)
			}
		}

		// Copy Usage if present (Usage can be shallow copied as it's typically immutable)
		if original.Response.Usage != nil {
			copyUsage := *original.Response.Usage
			copy.Response.Usage = &copyUsage
		}
	}

	// Copy pointer fields
	if original.OutputIndex != nil {
		copyOutputIndex := *original.OutputIndex
		copy.OutputIndex = &copyOutputIndex
	}

	if original.Item != nil {
		copyItem := deepCopyResponsesMessage(*original.Item)
		copy.Item = &copyItem
	}

	if original.ContentIndex != nil {
		copyContentIndex := *original.ContentIndex
		copy.ContentIndex = &copyContentIndex
	}

	if original.ItemID != nil {
		copyItemID := *original.ItemID
		copy.ItemID = &copyItemID
	}

	if original.Part != nil {
		copyPart := deepCopyResponsesMessageContentBlock(*original.Part)
		copy.Part = &copyPart
	}

	if original.Delta != nil {
		copyDelta := *original.Delta
		copy.Delta = &copyDelta
	}

	// Deep copy LogProbs slice if present
	if original.LogProbs != nil {
		copy.LogProbs = make([]schemas.ResponsesOutputMessageContentTextLogProb, len(original.LogProbs))
		for i, logProb := range original.LogProbs {
			copiedLogProb := schemas.ResponsesOutputMessageContentTextLogProb{
				LogProb: logProb.LogProb,
				Token:   logProb.Token,
			}
			// Deep copy Bytes slice
			if logProb.Bytes != nil {
				copiedLogProb.Bytes = make([]int, len(logProb.Bytes))
				for j, byteValue := range logProb.Bytes {
					copiedLogProb.Bytes[j] = byteValue
				}
			}
			// Deep copy TopLogProbs slice
			if logProb.TopLogProbs != nil {
				copiedLogProb.TopLogProbs = make([]schemas.LogProb, len(logProb.TopLogProbs))
				for j, topLogProb := range logProb.TopLogProbs {
					copiedLogProb.TopLogProbs[j] = schemas.LogProb{
						Bytes:   topLogProb.Bytes,
						LogProb: topLogProb.LogProb,
						Token:   topLogProb.Token,
					}
				}
			}
			copy.LogProbs[i] = copiedLogProb
		}
	}

	if original.Text != nil {
		copyText := *original.Text
		copy.Text = &copyText
	}

	if original.Refusal != nil {
		copyRefusal := *original.Refusal
		copy.Refusal = &copyRefusal
	}

	if original.Arguments != nil {
		copyArguments := *original.Arguments
		copy.Arguments = &copyArguments
	}

	if original.PartialImageB64 != nil {
		copyPartialImageB64 := *original.PartialImageB64
		copy.PartialImageB64 = &copyPartialImageB64
	}

	if original.PartialImageIndex != nil {
		copyPartialImageIndex := *original.PartialImageIndex
		copy.PartialImageIndex = &copyPartialImageIndex
	}

	if original.Annotation != nil {
		copyAnnotation := *original.Annotation
		copy.Annotation = &copyAnnotation
	}

	if original.AnnotationIndex != nil {
		copyAnnotationIndex := *original.AnnotationIndex
		copy.AnnotationIndex = &copyAnnotationIndex
	}

	if original.Code != nil {
		copyCode := *original.Code
		copy.Code = &copyCode
	}

	if original.Message != nil {
		copyMessage := *original.Message
		copy.Message = &copyMessage
	}

	if original.Param != nil {
		copyParam := *original.Param
		copy.Param = &copyParam
	}

	return copy
}

// deepCopyResponsesMessage creates a deep copy of a ResponsesMessage
func deepCopyResponsesMessage(original schemas.ResponsesMessage) schemas.ResponsesMessage {
	copy := schemas.ResponsesMessage{}

	if original.ID != nil {
		copyID := *original.ID
		copy.ID = &copyID
	}

	if original.Type != nil {
		copyType := *original.Type
		copy.Type = &copyType
	}

	if original.Role != nil {
		copyRole := *original.Role
		copy.Role = &copyRole
	}

	if original.Content != nil {
		copy.Content = &schemas.ResponsesMessageContent{}

		if original.Content.ContentStr != nil {
			copyContentStr := *original.Content.ContentStr
			copy.Content.ContentStr = &copyContentStr
		}

		if original.Content.ContentBlocks != nil {
			copy.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, len(original.Content.ContentBlocks))
			for i, block := range original.Content.ContentBlocks {
				copy.Content.ContentBlocks[i] = deepCopyResponsesMessageContentBlock(block)
			}
		}
	}

	// Deep copy ResponsesReasoning if present
	if original.ResponsesReasoning != nil {
		copy.ResponsesReasoning = &schemas.ResponsesReasoning{}

		// Deep copy Summary slice
		if original.ResponsesReasoning.Summary != nil {
			copy.ResponsesReasoning.Summary = make([]schemas.ResponsesReasoningSummary, len(original.ResponsesReasoning.Summary))
			for i, summary := range original.ResponsesReasoning.Summary {
				copy.ResponsesReasoning.Summary[i] = schemas.ResponsesReasoningSummary{
					Type: summary.Type,
					Text: summary.Text,
				}
			}
		}

		// Deep copy EncryptedContent if present
		if original.ResponsesReasoning.EncryptedContent != nil {
			copyEncrypted := *original.ResponsesReasoning.EncryptedContent
			copy.ResponsesReasoning.EncryptedContent = &copyEncrypted
		}
	}

	if original.ResponsesToolMessage != nil {
		copy.ResponsesToolMessage = &schemas.ResponsesToolMessage{}

		// Deep copy primitive fields
		if original.ResponsesToolMessage.CallID != nil {
			copyCallID := *original.ResponsesToolMessage.CallID
			copy.ResponsesToolMessage.CallID = &copyCallID
		}

		if original.ResponsesToolMessage.Name != nil {
			copyName := *original.ResponsesToolMessage.Name
			copy.ResponsesToolMessage.Name = &copyName
		}

		if original.ResponsesToolMessage.Arguments != nil {
			copyArguments := *original.ResponsesToolMessage.Arguments
			copy.ResponsesToolMessage.Arguments = &copyArguments
		}

		if original.ResponsesToolMessage.Error != nil {
			copyError := *original.ResponsesToolMessage.Error
			copy.ResponsesToolMessage.Error = &copyError
		}

		// Deep copy Output
		if original.ResponsesToolMessage.Output != nil {
			copy.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}

			if original.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
				copyStr := *original.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
				copy.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = &copyStr
			}

			if original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
				copy.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = make([]schemas.ResponsesMessageContentBlock, len(original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks))
				for i, block := range original.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
					copy.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks[i] = deepCopyResponsesMessageContentBlock(block)
				}
			}

			if original.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput != nil {
				copyOutput := *original.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput
				copy.ResponsesToolMessage.Output.ResponsesComputerToolCallOutput = &copyOutput
			}
		}

		// Deep copy Action
		if original.ResponsesToolMessage.Action != nil {
			copy.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{}

			if original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction
				// Deep copy Path slice
				if copyAction.Path != nil {
					copyAction.Path = make([]schemas.ResponsesComputerToolCallActionPath, len(copyAction.Path))
					for i, path := range original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction.Path {
						copyAction.Path[i] = path // struct copy is fine for simple structs
					}
				}
				// Deep copy Keys slice
				if copyAction.Keys != nil {
					copyAction.Keys = make([]string, len(copyAction.Keys))
					for i, key := range original.ResponsesToolMessage.Action.ResponsesComputerToolCallAction.Keys {
						copyAction.Keys[i] = key
					}
				}
				copy.ResponsesToolMessage.Action.ResponsesComputerToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
				copy.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction
				copy.ResponsesToolMessage.Action.ResponsesLocalShellToolCallAction = &copyAction
			}

			if original.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction != nil {
				copyAction := *original.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction
				copy.ResponsesToolMessage.Action.ResponsesMCPApprovalRequestAction = &copyAction
			}
		}

		// Deep copy embedded tool call structs
		if original.ResponsesToolMessage.ResponsesFileSearchToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesFileSearchToolCall
			// Deep copy Queries slice
			if copyToolCall.Queries != nil {
				copyToolCall.Queries = make([]string, len(copyToolCall.Queries))
				for i, query := range original.ResponsesToolMessage.ResponsesFileSearchToolCall.Queries {
					copyToolCall.Queries[i] = query
				}
			}
			// Deep copy Results slice
			if copyToolCall.Results != nil {
				copyToolCall.Results = make([]schemas.ResponsesFileSearchToolCallResult, len(copyToolCall.Results))
				for i, result := range original.ResponsesToolMessage.ResponsesFileSearchToolCall.Results {
					copyResult := result
					// Deep copy Attributes map if present
					if result.Attributes != nil {
						copyAttrs := make(map[string]any, len(*result.Attributes))
						for k, v := range *result.Attributes {
							copyAttrs[k] = v
						}
						copyResult.Attributes = &copyAttrs
					}
					copyToolCall.Results[i] = copyResult
				}
			}
			copy.ResponsesToolMessage.ResponsesFileSearchToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesComputerToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesComputerToolCall
			// Deep copy PendingSafetyChecks slice
			if copyToolCall.PendingSafetyChecks != nil {
				copyToolCall.PendingSafetyChecks = make([]schemas.ResponsesComputerToolCallPendingSafetyCheck, len(copyToolCall.PendingSafetyChecks))
				for i, check := range original.ResponsesToolMessage.ResponsesComputerToolCall.PendingSafetyChecks {
					copyToolCall.PendingSafetyChecks[i] = check
				}
			}
			copy.ResponsesToolMessage.ResponsesComputerToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesComputerToolCallOutput != nil {
			copyOutput := *original.ResponsesToolMessage.ResponsesComputerToolCallOutput
			// Deep copy AcknowledgedSafetyChecks slice
			if copyOutput.AcknowledgedSafetyChecks != nil {
				copyOutput.AcknowledgedSafetyChecks = make([]schemas.ResponsesComputerToolCallAcknowledgedSafetyCheck, len(copyOutput.AcknowledgedSafetyChecks))
				for i, check := range original.ResponsesToolMessage.ResponsesComputerToolCallOutput.AcknowledgedSafetyChecks {
					copyOutput.AcknowledgedSafetyChecks[i] = check
				}
			}
			copy.ResponsesToolMessage.ResponsesComputerToolCallOutput = &copyOutput
		}

		if original.ResponsesToolMessage.ResponsesCodeInterpreterToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesCodeInterpreterToolCall
			// Deep copy Outputs slice
			if copyToolCall.Outputs != nil {
				copyToolCall.Outputs = make([]schemas.ResponsesCodeInterpreterOutput, len(copyToolCall.Outputs))
				for i, output := range original.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs {
					copyToolCall.Outputs[i] = output
				}
			}
			copy.ResponsesToolMessage.ResponsesCodeInterpreterToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesMCPToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesMCPToolCall
			copy.ResponsesToolMessage.ResponsesMCPToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesCustomToolCall != nil {
			copyToolCall := *original.ResponsesToolMessage.ResponsesCustomToolCall
			copy.ResponsesToolMessage.ResponsesCustomToolCall = &copyToolCall
		}

		if original.ResponsesToolMessage.ResponsesImageGenerationCall != nil {
			copyCall := *original.ResponsesToolMessage.ResponsesImageGenerationCall
			copy.ResponsesToolMessage.ResponsesImageGenerationCall = &copyCall
		}

		if original.ResponsesToolMessage.ResponsesMCPListTools != nil {
			copyListTools := *original.ResponsesToolMessage.ResponsesMCPListTools
			// Deep copy Tools slice
			if copyListTools.Tools != nil {
				copyListTools.Tools = make([]schemas.ResponsesMCPTool, len(copyListTools.Tools))
				for i, tool := range original.ResponsesToolMessage.ResponsesMCPListTools.Tools {
					copyListTools.Tools[i] = tool
				}
			}
			copy.ResponsesToolMessage.ResponsesMCPListTools = &copyListTools
		}

		if original.ResponsesToolMessage.ResponsesMCPApprovalResponse != nil {
			copyApproval := *original.ResponsesToolMessage.ResponsesMCPApprovalResponse
			copy.ResponsesToolMessage.ResponsesMCPApprovalResponse = &copyApproval
		}
	}

	return copy
}

// deepCopyResponsesMessageContentBlock creates a deep copy of a ResponsesMessageContentBlock
func deepCopyResponsesMessageContentBlock(original schemas.ResponsesMessageContentBlock) schemas.ResponsesMessageContentBlock {
	copy := schemas.ResponsesMessageContentBlock{
		Type: original.Type,
	}

	if original.Text != nil {
		copyText := *original.Text
		copy.Text = &copyText
	}

	// Copy other specific content type fields as needed
	if original.ResponsesOutputMessageContentText != nil {
		t := *original.ResponsesOutputMessageContentText
		// Annotations
		if t.Annotations != nil {
			t.Annotations = append([]schemas.ResponsesOutputMessageContentTextAnnotation(nil), t.Annotations...)
		}
		// LogProbs (and their inner slices)
		if t.LogProbs != nil {
			newLP := make([]schemas.ResponsesOutputMessageContentTextLogProb, len(t.LogProbs))
			for i := range t.LogProbs {
				lp := t.LogProbs[i]
				if lp.Bytes != nil {
					lp.Bytes = append([]int(nil), lp.Bytes...)
				}
				if lp.TopLogProbs != nil {
					lp.TopLogProbs = append([]schemas.LogProb(nil), lp.TopLogProbs...)
				}
				newLP[i] = lp
			}
			t.LogProbs = newLP
		}
		copy.ResponsesOutputMessageContentText = &t
	}

	if original.ResponsesOutputMessageContentRefusal != nil {
		copyRefusal := schemas.ResponsesOutputMessageContentRefusal{
			Refusal: original.ResponsesOutputMessageContentRefusal.Refusal,
		}
		copy.ResponsesOutputMessageContentRefusal = &copyRefusal
	}

	return copy
}

// buildCompleteMessageFromResponsesStreamChunks builds complete messages from accumulated responses stream chunks
func (a *Accumulator) buildCompleteMessageFromResponsesStreamChunks(chunks []*ResponsesStreamChunk) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage

	// Sort chunks by chunk index to ensure correct processing order
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].StreamResponse == nil || chunks[j].StreamResponse == nil {
			return false
		}
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})

	for _, chunk := range chunks {
		if chunk.StreamResponse == nil {
			continue
		}

		resp := chunk.StreamResponse
		switch resp.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			// Always append new items - this fixes multiple function calls issue
			// Deep copy to prevent shared pointer mutation when deltas are appended
			if resp.Item != nil {
				messages = append(messages, deepCopyResponsesMessage(*resp.Item))
			}

		case schemas.ResponsesStreamResponseTypeContentPartAdded:
			// Add content part to the most recent message, create message if none exists
			// Deep copy to prevent shared pointer mutation
			if resp.Part != nil {
				if len(messages) == 0 {
					messages = append(messages, createNewMessage())
				}

				lastMsg := &messages[len(messages)-1]
				if lastMsg.Content == nil {
					lastMsg.Content = &schemas.ResponsesMessageContent{}
				}
				if lastMsg.Content.ContentBlocks == nil {
					lastMsg.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, 0)
				}
				lastMsg.Content.ContentBlocks = append(lastMsg.Content.ContentBlocks, deepCopyResponsesMessageContentBlock(*resp.Part))
			}

		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if len(messages) == 0 {
				messages = append(messages, createNewMessage())
			}
			// Append text delta to the most recent message
			if resp.Delta != nil && resp.ContentIndex != nil && len(messages) > 0 {
				a.appendTextDeltaToResponsesMessage(&messages[len(messages)-1], *resp.Delta, *resp.ContentIndex)
			}

		case schemas.ResponsesStreamResponseTypeRefusalDelta:
			if len(messages) == 0 {
				messages = append(messages, createNewMessage())
			}
			// Append refusal delta to the most recent message
			if resp.Refusal != nil && resp.ContentIndex != nil && len(messages) > 0 {
				a.appendRefusalDeltaToResponsesMessage(&messages[len(messages)-1], *resp.Refusal, *resp.ContentIndex)
			}

		case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
			if len(messages) == 0 {
				messages = append(messages, createNewMessage())
			}
			// Deep copy to prevent shared pointer mutation when arguments are appended
			if resp.Item != nil {
				messages = append(messages, deepCopyResponsesMessage(*resp.Item))
			}
			// Append arguments to the most recent message
			if resp.Delta != nil && len(messages) > 0 {
				a.appendFunctionArgumentsDeltaToResponsesMessage(&messages[len(messages)-1], *resp.Delta)
			}

		case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
			// Create new reasoning message if none exists, or find existing reasoning message to append delta to
			if (resp.Delta != nil || resp.Signature != nil) && resp.ItemID != nil {
				var targetMessage *schemas.ResponsesMessage

				// Find the reasoning message by ItemID
				for i := len(messages) - 1; i >= 0; i-- {
					if messages[i].ID != nil && *messages[i].ID == *resp.ItemID {
						targetMessage = &messages[i]
						break
					}
				}

				// If no message found, create a new reasoning message
				if targetMessage == nil {
					// Deep copy ItemID to prevent shared pointer mutation
					var copyID *string
					if resp.ItemID != nil {
						id := *resp.ItemID
						copyID = &id
					}
					newMessage := schemas.ResponsesMessage{
						ID:   copyID,
						Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						ResponsesReasoning: &schemas.ResponsesReasoning{
							Summary: []schemas.ResponsesReasoningSummary{},
						},
					}
					messages = append(messages, newMessage)
					targetMessage = &messages[len(messages)-1]
				}

				// Handle text delta
				if resp.Delta != nil {
					a.appendReasoningDeltaToResponsesMessage(targetMessage, *resp.Delta, resp.ContentIndex)
				}

				// Handle signature delta
				if resp.Signature != nil {
					a.appendReasoningSignatureToResponsesMessage(targetMessage, *resp.Signature, resp.ContentIndex)
				}
			}
		}
	}

	return messages
}

func createNewMessage() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: make([]schemas.ResponsesMessageContentBlock, 0),
		},
	}
}

// appendTextDeltaToResponsesMessage appends text delta to a responses message
func (a *Accumulator) appendTextDeltaToResponsesMessage(message *schemas.ResponsesMessage, delta string, contentIndex int) {
	if message.Content == nil {
		message.Content = &schemas.ResponsesMessageContent{}
	}

	// If we don't have content blocks yet, create them
	if message.Content.ContentBlocks == nil {
		message.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, contentIndex+1)
	}

	// Ensure we have enough content blocks
	for len(message.Content.ContentBlocks) <= contentIndex {
		message.Content.ContentBlocks = append(message.Content.ContentBlocks, schemas.ResponsesMessageContentBlock{})
	}

	// Initialize the content block if needed
	if message.Content.ContentBlocks[contentIndex].Type == "" {
		message.Content.ContentBlocks[contentIndex].Type = schemas.ResponsesOutputMessageContentTypeText
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentText = &schemas.ResponsesOutputMessageContentText{}
	}

	// Append to existing text or create new text
	if message.Content.ContentBlocks[contentIndex].Text == nil {
		message.Content.ContentBlocks[contentIndex].Text = &delta
	} else {
		*message.Content.ContentBlocks[contentIndex].Text += delta
	}
}

// appendRefusalDeltaToResponsesMessage appends refusal delta to a responses message
func (a *Accumulator) appendRefusalDeltaToResponsesMessage(message *schemas.ResponsesMessage, refusal string, contentIndex int) {
	if message.Content == nil {
		message.Content = &schemas.ResponsesMessageContent{}
	}

	// If we don't have content blocks yet, create them
	if message.Content.ContentBlocks == nil {
		message.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, contentIndex+1)
	}

	// Ensure we have enough content blocks
	for len(message.Content.ContentBlocks) <= contentIndex {
		message.Content.ContentBlocks = append(message.Content.ContentBlocks, schemas.ResponsesMessageContentBlock{})
	}

	// Initialize the content block if needed
	if message.Content.ContentBlocks[contentIndex].Type == "" {
		message.Content.ContentBlocks[contentIndex].Type = schemas.ResponsesOutputMessageContentTypeRefusal
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal = &schemas.ResponsesOutputMessageContentRefusal{}
	}

	// Append to existing refusal text
	if message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal == nil {
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal = &schemas.ResponsesOutputMessageContentRefusal{
			Refusal: refusal,
		}
	} else {
		message.Content.ContentBlocks[contentIndex].ResponsesOutputMessageContentRefusal.Refusal += refusal
	}
}

// appendFunctionArgumentsDeltaToResponsesMessage appends function arguments delta to a responses message
func (a *Accumulator) appendFunctionArgumentsDeltaToResponsesMessage(message *schemas.ResponsesMessage, arguments string) {
	if message.ResponsesToolMessage == nil {
		message.ResponsesToolMessage = &schemas.ResponsesToolMessage{}
	}

	if message.ResponsesToolMessage.Arguments == nil {
		message.ResponsesToolMessage.Arguments = &arguments
	} else {
		*message.ResponsesToolMessage.Arguments += arguments
	}
}

// appendReasoningDeltaToResponsesMessage appends reasoning delta to a responses message
func (a *Accumulator) appendReasoningDeltaToResponsesMessage(message *schemas.ResponsesMessage, delta string, contentIndex *int) {
	// Handle reasoning content in two ways:
	// 1. Content blocks (for reasoning_text content blocks)
	// 2. ResponsesReasoning.Summary (for reasoning summary accumulation)

	// If we have a content index, this is reasoning content in content blocks
	if contentIndex != nil {
		if message.Content == nil {
			message.Content = &schemas.ResponsesMessageContent{}
		}

		// If we don't have content blocks yet, create them
		if message.Content.ContentBlocks == nil {
			message.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, *contentIndex+1)
		}

		// Ensure we have enough content blocks
		for len(message.Content.ContentBlocks) <= *contentIndex {
			message.Content.ContentBlocks = append(message.Content.ContentBlocks, schemas.ResponsesMessageContentBlock{})
		}

		// Initialize the content block if needed
		if message.Content.ContentBlocks[*contentIndex].Type == "" {
			message.Content.ContentBlocks[*contentIndex].Type = schemas.ResponsesOutputMessageContentTypeReasoning
		}

		// Append to existing reasoning text or create new text
		if message.Content.ContentBlocks[*contentIndex].Text == nil {
			message.Content.ContentBlocks[*contentIndex].Text = &delta
		} else {
			*message.Content.ContentBlocks[*contentIndex].Text += delta
		}
	} else {
		// No content index - this is reasoning summary accumulation
		if message.ResponsesReasoning == nil {
			message.ResponsesReasoning = &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			}
		}

		// For now, accumulate into a single summary entry
		// In the future, this could be enhanced to handle multiple summary entries
		if len(message.ResponsesReasoning.Summary) == 0 {
			message.ResponsesReasoning.Summary = append(message.ResponsesReasoning.Summary, schemas.ResponsesReasoningSummary{
				Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
				Text: delta,
			})
		} else {
			// Append to the first (and typically only) summary entry
			message.ResponsesReasoning.Summary[0].Text += delta
		}
	}
}

// appendReasoningSignatureToResponsesMessage appends reasoning signature to a responses message
func (a *Accumulator) appendReasoningSignatureToResponsesMessage(message *schemas.ResponsesMessage, signature string, contentIndex *int) {
	// Handle signature content in content blocks or ResponsesReasoning.EncryptedContent

	// If we have a content index, this is signature content in content blocks
	if contentIndex != nil {
		if message.Content == nil {
			message.Content = &schemas.ResponsesMessageContent{}
		}

		// If we don't have content blocks yet, create them
		if message.Content.ContentBlocks == nil {
			message.Content.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, *contentIndex+1)
		}

		// Ensure we have enough content blocks
		for len(message.Content.ContentBlocks) <= *contentIndex {
			message.Content.ContentBlocks = append(message.Content.ContentBlocks, schemas.ResponsesMessageContentBlock{})
		}

		// Initialize the content block if needed
		if message.Content.ContentBlocks[*contentIndex].Type == "" {
			message.Content.ContentBlocks[*contentIndex].Type = schemas.ResponsesOutputMessageContentTypeReasoning
		}

		// Set or append signature to the content block
		if message.Content.ContentBlocks[*contentIndex].Signature == nil {
			message.Content.ContentBlocks[*contentIndex].Signature = &signature
		} else {
			*message.Content.ContentBlocks[*contentIndex].Signature += signature
		}
	} else {
		// No content index - this is encrypted content at the reasoning level
		if message.ResponsesReasoning == nil {
			message.ResponsesReasoning = &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			}
		}

		// Set or append to encrypted content
		if message.ResponsesReasoning.EncryptedContent == nil {
			message.ResponsesReasoning.EncryptedContent = &signature
		} else {
			*message.ResponsesReasoning.EncryptedContent += signature
		}
	}
}

// processAccumulatedResponsesStreamingChunks processes all accumulated responses streaming chunks in order
func (a *Accumulator) processAccumulatedResponsesStreamingChunks(requestID string, respErr *schemas.BifrostError, isFinalChunk bool) (*AccumulatedData, error) {
	accumulator := a.getOrCreateStreamAccumulator(requestID)
	// Lock the accumulator
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	// Note: Cleanup is handled by CleanupStreamAccumulator when refcount reaches 0
	// This is called from completeDeferredSpan after streaming ends

	// Calculate Time to First Token (TTFT) in milliseconds
	var ttft int64
	if !accumulator.StartTimestamp.IsZero() && !accumulator.FirstChunkTimestamp.IsZero() {
		ttft = accumulator.FirstChunkTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}

	// Initialize accumulated data
	data := &AccumulatedData{
		RequestID:        requestID,
		Status:           "success",
		Stream:           true,
		StartTimestamp:   accumulator.StartTimestamp,
		EndTimestamp:     accumulator.FinalTimestamp,
		Latency:          0,
		TimeToFirstToken: ttft,
		OutputMessages:   nil,
		ToolCalls:        nil,
		ErrorDetails:     respErr,
		TokenUsage:       nil,
		CacheDebug:       nil,
		Cost:             nil,
	}

	// Build complete messages from accumulated chunks
	completeMessages := a.buildCompleteMessageFromResponsesStreamChunks(accumulator.ResponsesStreamChunks)

	if !isFinalChunk {
		data.OutputMessages = completeMessages
		return data, nil
	}

	// Update database with complete messages
	data.Status = "success"
	if respErr != nil {
		data.Status = "error"
	}

	if accumulator.StartTimestamp.IsZero() || accumulator.FinalTimestamp.IsZero() {
		data.Latency = 0
	} else {
		data.Latency = accumulator.FinalTimestamp.Sub(accumulator.StartTimestamp).Nanoseconds() / 1e6
	}

	data.EndTimestamp = accumulator.FinalTimestamp
	data.OutputMessages = completeMessages

	data.ErrorDetails = respErr

	// Update metadata from the chunk with highest index (contains TokenUsage, Cost, FinishReason)
	if lastChunk := accumulator.getLastResponsesChunkLocked(); lastChunk != nil {
		if lastChunk.TokenUsage != nil {
			data.TokenUsage = lastChunk.TokenUsage
		}
		if lastChunk.SemanticCacheDebug != nil {
			data.CacheDebug = lastChunk.SemanticCacheDebug
		}
		if lastChunk.Cost != nil {
			data.Cost = lastChunk.Cost
		}
		data.FinishReason = lastChunk.FinishReason
	}

	// Accumulate raw response using strings.Builder to avoid O(n^2) string concatenation
	if len(accumulator.ResponsesStreamChunks) > 0 {
		// Sort chunks by chunk index
		sort.Slice(accumulator.ResponsesStreamChunks, func(i, j int) bool {
			return accumulator.ResponsesStreamChunks[i].ChunkIndex < accumulator.ResponsesStreamChunks[j].ChunkIndex
		})
		var rawBuilder strings.Builder
		for _, chunk := range accumulator.ResponsesStreamChunks {
			if chunk.RawResponse != nil {
				if rawBuilder.Len() > 0 {
					rawBuilder.WriteString("\n\n")
				}
				rawBuilder.WriteString(*chunk.RawResponse)
			}
		}
		if rawBuilder.Len() > 0 {
			s := rawBuilder.String()
			data.RawResponse = &s
		}
	}

	return data, nil
}

// processResponsesStreamingResponse processes a responses streaming response
func (a *Accumulator) processResponsesStreamingResponse(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*ProcessedStreamResponse, error) {
	a.logger.Debug("[streaming] processing responses streaming response")

	// Extract accumulator ID from context
	requestID, ok := getAccumulatorID(ctx)
	if !ok || requestID == "" {
		return nil, fmt.Errorf("accumulator-id not found in context or is empty")
	}

	_, provider, model := bifrost.GetResponseFields(result, bifrostErr)

	isFinalChunk := bifrost.IsFinalChunk(ctx)
	chunk := a.getResponsesStreamChunk()
	chunk.Timestamp = time.Now()
	chunk.ErrorDetails = bifrostErr

	if bifrostErr != nil {
		chunk.FinishReason = bifrost.Ptr("error")
	} else if result != nil && result.ResponsesStreamResponse != nil {
		if result.ResponsesStreamResponse.ExtraFields.RawResponse != nil {
			chunk.RawResponse = bifrost.Ptr(fmt.Sprintf("%v", result.ResponsesStreamResponse.ExtraFields.RawResponse))
		}
		// Store a deep copy of the stream response to prevent shared data mutation between plugins
		chunk.StreamResponse = deepCopyResponsesStreamResponse(result.ResponsesStreamResponse)
		// Extract token usage from stream response if available
		if result.ResponsesStreamResponse.Response != nil &&
			result.ResponsesStreamResponse.Response.Usage != nil {
			chunk.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     result.ResponsesStreamResponse.Response.Usage.InputTokens,
				CompletionTokens: result.ResponsesStreamResponse.Response.Usage.OutputTokens,
				TotalTokens:      result.ResponsesStreamResponse.Response.Usage.TotalTokens,
				Cost:             result.ResponsesStreamResponse.Response.Usage.Cost,
			}
		}
		chunk.ChunkIndex = result.ResponsesStreamResponse.ExtraFields.ChunkIndex
		if isFinalChunk {
			if a.pricingManager != nil {
				cost := a.pricingManager.CalculateCostWithCacheDebug(result)
				chunk.Cost = bifrost.Ptr(cost)
			}
			chunk.SemanticCacheDebug = result.GetExtraFields().CacheDebug
		}
	}

	if addErr := a.addResponsesStreamChunk(requestID, chunk, isFinalChunk); addErr != nil {
		return nil, fmt.Errorf("failed to add responses stream chunk for request %s: %w", requestID, addErr)
	}

	// If this is the final chunk, process accumulated chunks
	// Always return data on final chunk - multiple plugins may need the result
	if isFinalChunk {
		// Get the accumulator and mark as complete (idempotent)
		accumulator := a.getOrCreateStreamAccumulator(requestID)
		accumulator.mu.Lock()
		if !accumulator.IsComplete {
			accumulator.IsComplete = true
		}
		accumulator.mu.Unlock()

		// Always process and return data on final chunk
		// Multiple plugins can call this - the processing is idempotent
		data, processErr := a.processAccumulatedResponsesStreamingChunks(requestID, bifrostErr, isFinalChunk)
		if processErr != nil {
			a.logger.Error("failed to process accumulated responses chunks for request %s: %v", requestID, processErr)
			return nil, processErr
		}

		var rawRequest interface{}
		if result != nil && result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.ExtraFields.RawRequest != nil {
			rawRequest = result.ResponsesStreamResponse.ExtraFields.RawRequest
		}

		return &ProcessedStreamResponse{
			RequestID:  requestID,
			StreamType: StreamTypeResponses,
			Provider:   provider,
			Model:      model,
			Data:       data,
			RawRequest: &rawRequest,
		}, nil
	}

	return &ProcessedStreamResponse{
		RequestID:  requestID,
		StreamType: StreamTypeResponses,
		Provider:   provider,
		Model:      model,
		Data:       nil,
	}, nil
}
