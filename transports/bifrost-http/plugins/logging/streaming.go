// Package logging provides streaming-related functionality for the GORM-based logging plugin
package logging

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// appendContentToMessage efficiently appends content to a message
func (p *LoggerPlugin) appendContentToMessage(message *schemas.BifrostMessage, newContent string) {
	if message == nil {
		return
	}
	if message.Content.ContentStr != nil {
		// Append to existing string content
		*message.Content.ContentStr += newContent
	} else if message.Content.ContentBlocks != nil {
		// Find the last text block and append, or create new one
		blocks := *message.Content.ContentBlocks
		if len(blocks) > 0 && blocks[len(blocks)-1].Type == schemas.ContentBlockTypeText && blocks[len(blocks)-1].Text != nil {
			// Append to last text block
			*blocks[len(blocks)-1].Text += newContent
		} else {
			// Create new text block
			blocks = append(blocks, schemas.ContentBlock{
				Type: schemas.ContentBlockTypeText,
				Text: &newContent,
			})
			message.Content.ContentBlocks = &blocks
		}
	} else {
		// Initialize with string content
		message.Content.ContentStr = &newContent
	}
}

// accumulateToolCallsInMessage efficiently accumulates tool calls in a message
func (p *LoggerPlugin) accumulateToolCallsInMessage(message *schemas.BifrostMessage, deltaToolCalls []schemas.ToolCall) {
	if message == nil {
		return
	}
	if message.AssistantMessage == nil {
		message.AssistantMessage = &schemas.AssistantMessage{}
	}

	if message.AssistantMessage.ToolCalls == nil {
		message.AssistantMessage.ToolCalls = &[]schemas.ToolCall{}
	}

	existingToolCalls := *message.AssistantMessage.ToolCalls

	for _, deltaToolCall := range deltaToolCalls {
		// Find existing tool call with same ID or create new one
		found := false
		for i := range existingToolCalls {
			if existingToolCalls[i].ID != nil && deltaToolCall.ID != nil &&
				*existingToolCalls[i].ID == *deltaToolCall.ID {
				// Append arguments to existing tool call
				existingToolCalls[i].Function.Arguments += deltaToolCall.Function.Arguments
				found = true
				break
			}
		}

		if !found {
			// Add new tool call
			existingToolCalls = append(existingToolCalls, deltaToolCall)
		}
	}

	message.AssistantMessage.ToolCalls = &existingToolCalls
}
