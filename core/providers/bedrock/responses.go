package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// BedrockResponsesStreamState tracks state during streaming conversion for responses API
type BedrockResponsesStreamState struct {
	ContentIndexToOutputIndex map[int]int                                                    // Maps Bedrock contentBlockIndex to OpenAI output_index
	ToolArgumentBuffers       map[int]string                                                 // Maps output_index to accumulated tool argument JSON
	ItemIDs                   map[int]string                                                 // Maps output_index to item ID for stable IDs
	ToolCallIDs               map[int]string                                                 // Maps output_index to tool call ID (callID)
	ToolCallNames             map[int]string                                                 // Maps output_index to tool call name
	ReasoningContentIndices   map[int]bool                                                   // Tracks which content indices are reasoning blocks
	CodeInterpreterIndices    map[int]bool                                                   // Tracks which output indices are nova_code_interpreter calls
	NovaGroundingIndices      map[int]bool                                                   // Tracks which output indices are nova_grounding (web_search_call) blocks
	NovaGroundingCitations    map[int][]schemas.ResponsesWebSearchToolCallActionSearchSource // Collected citation sources per nova_grounding output index
	CompletedOutputIndices    map[int]bool                                                   // Tracks which output indices have been completed
	AnnotationIndices         map[int]int                                                    // Maps output_index to next annotation index for sequential citation numbering
	TextBuffers               map[int]*strings.Builder                                       // Maps output_index to accumulated text content for done events
	CurrentOutputIndex        int                                                            // Current output index counter
	MessageID                 *string                                                        // Message ID (generated)
	Model                     *string                                                        // Model name
	StopReason                *string                                                        // Stop reason for the message
	CreatedAt                 int                                                            // Timestamp for created_at consistency
	HasEmittedCreated         bool                                                           // Whether we've emitted response.created
	HasEmittedInProgress      bool                                                           // Whether we've emitted response.in_progress
	UsedStructuredOutputTool  bool                                                           // True when the SO tool block was intercepted and converted to text content
	Ctx                       context.Context                                                // Request context for restoring aliased tool names
}

// bedrockResponsesStreamStatePool provides a pool for Bedrock responses stream state objects.
var bedrockResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &BedrockResponsesStreamState{
			ContentIndexToOutputIndex: make(map[int]int),
			ToolArgumentBuffers:       make(map[int]string),
			ItemIDs:                   make(map[int]string),
			ToolCallIDs:               make(map[int]string),
			ToolCallNames:             make(map[int]string),
			ReasoningContentIndices:   make(map[int]bool),
			CodeInterpreterIndices:    make(map[int]bool),
			NovaGroundingIndices:      make(map[int]bool),
			NovaGroundingCitations:    make(map[int][]schemas.ResponsesWebSearchToolCallActionSearchSource),
			CompletedOutputIndices:    make(map[int]bool),
			AnnotationIndices:         make(map[int]int),
			TextBuffers:               make(map[int]*strings.Builder),
			CurrentOutputIndex:        0,
			CreatedAt:                 int(time.Now().Unix()),
			HasEmittedCreated:         false,
			HasEmittedInProgress:      false,
		}
	},
}

// acquireBedrockResponsesStreamState gets a Bedrock responses stream state from the pool.
func acquireBedrockResponsesStreamState() *BedrockResponsesStreamState {
	state := bedrockResponsesStreamStatePool.Get().(*BedrockResponsesStreamState)
	// Clear maps (they're already initialized from New or previous flush)
	// Only initialize if nil (shouldn't happen, but defensive)
	if state.ContentIndexToOutputIndex == nil {
		state.ContentIndexToOutputIndex = make(map[int]int)
	} else {
		clear(state.ContentIndexToOutputIndex)
	}
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[int]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ToolCallIDs == nil {
		state.ToolCallIDs = make(map[int]string)
	} else {
		clear(state.ToolCallIDs)
	}
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	if state.ReasoningContentIndices == nil {
		state.ReasoningContentIndices = make(map[int]bool)
	} else {
		clear(state.ReasoningContentIndices)
	}
	if state.CodeInterpreterIndices == nil {
		state.CodeInterpreterIndices = make(map[int]bool)
	} else {
		clear(state.CodeInterpreterIndices)
	}
	if state.NovaGroundingIndices == nil {
		state.NovaGroundingIndices = make(map[int]bool)
	} else {
		clear(state.NovaGroundingIndices)
	}
	if state.NovaGroundingCitations == nil {
		state.NovaGroundingCitations = make(map[int][]schemas.ResponsesWebSearchToolCallActionSearchSource)
	} else {
		clear(state.NovaGroundingCitations)
	}
	if state.CompletedOutputIndices == nil {
		state.CompletedOutputIndices = make(map[int]bool)
	} else {
		clear(state.CompletedOutputIndices)
	}
	if state.AnnotationIndices == nil {
		state.AnnotationIndices = make(map[int]int)
	} else {
		clear(state.AnnotationIndices)
	}
	if state.TextBuffers == nil {
		state.TextBuffers = make(map[int]*strings.Builder)
	} else {
		clear(state.TextBuffers)
	}
	// Reset other fields
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.Model = nil
	state.StopReason = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.UsedStructuredOutputTool = false
	state.Ctx = nil
	return state
}

// NewBedrockResponsesStreamState returns a freshly initialised stream state for use in tests.
func NewBedrockResponsesStreamState() *BedrockResponsesStreamState {
	return acquireBedrockResponsesStreamState()
}

// releaseBedrockResponsesStreamState returns a Bedrock responses stream state to the pool.
func releaseBedrockResponsesStreamState(state *BedrockResponsesStreamState) {
	if state != nil {
		state.flush() // Clean before returning to pool
		bedrockResponsesStreamStatePool.Put(state)
	}
}

func (state *BedrockResponsesStreamState) flush() {
	// Clear maps (reuse if already initialized, otherwise initialize)
	if state.ContentIndexToOutputIndex == nil {
		state.ContentIndexToOutputIndex = make(map[int]int)
	} else {
		clear(state.ContentIndexToOutputIndex)
	}
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[int]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ToolCallIDs == nil {
		state.ToolCallIDs = make(map[int]string)
	} else {
		clear(state.ToolCallIDs)
	}
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	if state.ReasoningContentIndices == nil {
		state.ReasoningContentIndices = make(map[int]bool)
	} else {
		clear(state.ReasoningContentIndices)
	}
	if state.CodeInterpreterIndices == nil {
		state.CodeInterpreterIndices = make(map[int]bool)
	} else {
		clear(state.CodeInterpreterIndices)
	}
	if state.NovaGroundingIndices == nil {
		state.NovaGroundingIndices = make(map[int]bool)
	} else {
		clear(state.NovaGroundingIndices)
	}
	if state.NovaGroundingCitations == nil {
		state.NovaGroundingCitations = make(map[int][]schemas.ResponsesWebSearchToolCallActionSearchSource)
	} else {
		clear(state.NovaGroundingCitations)
	}
	if state.CompletedOutputIndices == nil {
		state.CompletedOutputIndices = make(map[int]bool)
	} else {
		clear(state.CompletedOutputIndices)
	}
	if state.AnnotationIndices == nil {
		state.AnnotationIndices = make(map[int]int)
	} else {
		clear(state.AnnotationIndices)
	}
	if state.TextBuffers == nil {
		state.TextBuffers = make(map[int]*strings.Builder)
	} else {
		clear(state.TextBuffers)
	}
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.Model = nil
	state.StopReason = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.UsedStructuredOutputTool = false
}

// ToBifrostResponsesStream converts a Bedrock stream event to a Bifrost Responses Stream response
// Returns a slice of responses to support cases where a single event produces multiple responses
func (chunk *BedrockStreamEvent) ToBifrostResponsesStream(sequenceNumber int, state *BedrockResponsesStreamState) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError, bool) {
	switch {
	case chunk.Role != nil:
		// Message start - emit response.created and response.in_progress (OpenAI-style lifecycle)
		var responses []*schemas.BifrostResponsesStreamResponse

		// Generate message ID if not already set
		if state.MessageID == nil {
			messageID := fmt.Sprintf("msg_%d", state.CreatedAt)
			state.MessageID = &messageID
		}

		// Emit response.created
		if !state.HasEmittedCreated {
			response := &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				CreatedAt: state.CreatedAt,
			}
			if state.Model != nil {
				response.Model = *state.Model
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeCreated,
				SequenceNumber: sequenceNumber,
				Response:       response,
			})
			state.HasEmittedCreated = true
		}

		// Emit response.in_progress
		if !state.HasEmittedInProgress {
			response := &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				CreatedAt: state.CreatedAt, // Use same timestamp
			}
			if state.Model != nil {
				response.Model = *state.Model
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeInProgress,
				SequenceNumber: sequenceNumber + len(responses),
				Response:       response,
			})
			state.HasEmittedInProgress = true
		}

		// Don't pre-create any items here - let each content block create its own item when it first appears

		if len(responses) > 0 {
			return responses, nil, false
		}

	case chunk.Start != nil:
		// Handle content block start (text content or tool use)
		contentBlockIndex := 0
		if chunk.ContentBlockIndex != nil {
			contentBlockIndex = *chunk.ContentBlockIndex
		}

		// Check if this is a tool use start
		if chunk.Start.ToolUse != nil {
			var responses []*schemas.BifrostResponsesStreamResponse

			// Close any open reasoning blocks first (Anthropic sends content_block_stop before starting new blocks)
			for prevContentIndex := range state.ReasoningContentIndices {
				prevOutputIndex, prevExists := state.ContentIndexToOutputIndex[prevContentIndex]
				if !prevExists {
					continue
				}

				// Skip already completed output indices
				if state.CompletedOutputIndices[prevOutputIndex] {
					continue
				}

				itemID := state.ItemIDs[prevOutputIndex]

				// For reasoning items, content_index is always 0
				reasoningContentIndex := 0

				// Emit reasoning_summary_text.done
				emptyText := ""
				reasoningDoneResponse := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(prevOutputIndex),
					ContentIndex:   &reasoningContentIndex,
					Text:           &emptyText,
				}
				if itemID != "" {
					reasoningDoneResponse.ItemID = &itemID
				}
				responses = append(responses, reasoningDoneResponse)

				// Emit content_part.done for reasoning
				part := &schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeReasoning,
					Text: &emptyText,
				}
				partDoneResponse := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(prevOutputIndex),
					ContentIndex:   &reasoningContentIndex,
					Part:           part,
				}
				if itemID != "" {
					partDoneResponse.ItemID = &itemID
				}
				responses = append(responses, partDoneResponse)

				// Emit output_item.done for reasoning
				statusCompleted := "completed"
				messageType := schemas.ResponsesMessageTypeReasoning
				role := schemas.ResponsesInputMessageRoleAssistant
				doneItem := &schemas.ResponsesMessage{
					Type:   &messageType,
					Role:   &role,
					Status: &statusCompleted,
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningSummary{},
					},
				}
				if itemID != "" {
					doneItem.ID = &itemID
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(prevOutputIndex),
					ContentIndex:   &reasoningContentIndex,
					Item:           doneItem,
				})

				// Mark this output index as completed
				state.CompletedOutputIndices[prevOutputIndex] = true
			}
			// Clear reasoning content indices after closing them
			clear(state.ReasoningContentIndices)

			// Close any open text blocks before starting tool calls
			// This ensures all text content is closed before tool calls begin
			for prevContentIndex, prevOutputIndex := range state.ContentIndexToOutputIndex {
				// Skip reasoning blocks (already handled above)
				if state.ReasoningContentIndices[prevContentIndex] {
					continue
				}

				// Skip already completed output indices
				if state.CompletedOutputIndices[prevOutputIndex] {
					continue
				}

				// Check if this is a text block (not a tool call)
				prevToolCallID := state.ToolCallIDs[prevOutputIndex]
				if prevToolCallID != "" {
					continue // This is a tool call, skip it for now
				}

				// This is a text block - close it
				prevItemID := state.ItemIDs[prevOutputIndex]
				if prevItemID == "" {
					continue
				}

				prevAccText := ""
				if buf := state.TextBuffers[prevOutputIndex]; buf != nil {
					prevAccText = buf.String()
				}

				// Emit output_text.done with accumulated text
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(prevOutputIndex),
					ContentIndex:   &prevContentIndex,
					ItemID:         &prevItemID,
					Text:           &prevAccText,
					LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
				})

				// Emit content_part.done for text with accumulated text
				prevPartText := prevAccText
				part := &schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &prevPartText,
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
					},
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(prevOutputIndex),
					ContentIndex:   &prevContentIndex,
					ItemID:         &prevItemID,
					Part:           part,
				})

				// Emit output_item.done for text with content blocks
				statusCompleted := "completed"
				var prevContentBlocks []schemas.ResponsesMessageContentBlock
				if prevAccText != "" {
					prevItemText := prevAccText
					prevContentBlocks = []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: &prevItemText,
							ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
								Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
								LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
							},
						},
					}
				}
				messageType := schemas.ResponsesMessageTypeMessage
				role := schemas.ResponsesInputMessageRoleAssistant
				doneItem := &schemas.ResponsesMessage{
					Type:   &messageType,
					Role:   &role,
					Status: &statusCompleted,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: prevContentBlocks,
					},
				}
				if prevItemID != "" {
					doneItem.ID = &prevItemID
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(prevOutputIndex),
					ContentIndex:   &prevContentIndex,
					Item:           doneItem,
				})
				delete(state.TextBuffers, prevOutputIndex)

				// Mark this output index as completed
				state.CompletedOutputIndices[prevOutputIndex] = true
			}

			// Close any open tool call blocks before starting a new one (Anthropic completes each block before starting next)
			for prevContentIndex, prevOutputIndex := range state.ContentIndexToOutputIndex {
				// Skip reasoning blocks (already handled above)
				if state.ReasoningContentIndices[prevContentIndex] {
					continue
				}

				// Skip already completed output indices
				if state.CompletedOutputIndices[prevOutputIndex] {
					continue
				}

				// Check if this is a tool call
				prevToolCallID := state.ToolCallIDs[prevOutputIndex]
				if prevToolCallID == "" {
					continue // Not a tool call
				}

				prevItemID := state.ItemIDs[prevOutputIndex]
				prevToolName := state.ToolCallNames[prevOutputIndex]
				accumulatedArgs := state.ToolArgumentBuffers[prevOutputIndex]
				statusCompleted := "completed"

				if state.CodeInterpreterIndices[prevOutputIndex] {
					ciEvents := emitCodeInterpreterDoneEvents(prevOutputIndex, prevContentIndex, prevItemID, prevToolCallID, accumulatedArgs, sequenceNumber+len(responses))
					responses = append(responses, ciEvents...)
				} else if state.NovaGroundingIndices[prevOutputIndex] {
					citations := state.NovaGroundingCitations[prevOutputIndex]
					wsEvents := emitNovaGroundingDoneEvents(prevOutputIndex, prevContentIndex, prevItemID, citations, accumulatedArgs, sequenceNumber+len(responses))
					responses = append(responses, wsEvents...)
				} else {
					// Close a regular function_call block
					emptyText := ""
					part := &schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: &emptyText,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						},
					}
					responses = append(responses, &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(prevOutputIndex),
						ContentIndex:   schemas.Ptr(prevContentIndex),
						ItemID:         &prevItemID,
						Part:           part,
					})

					if accumulatedArgs != "" {
						var doneItem *schemas.ResponsesMessage
						if prevToolCallID != "" || prevToolName != "" {
							doneItem = &schemas.ResponsesMessage{
								ResponsesToolMessage: &schemas.ResponsesToolMessage{},
							}
							if prevToolCallID != "" {
								doneItem.ResponsesToolMessage.CallID = &prevToolCallID
							}
							if prevToolName != "" {
								doneItem.ResponsesToolMessage.Name = &prevToolName
							}
						}
						argsDoneResponse := &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
							SequenceNumber: sequenceNumber + len(responses),
							OutputIndex:    schemas.Ptr(prevOutputIndex),
							Arguments:      &accumulatedArgs,
						}
						if prevItemID != "" {
							argsDoneResponse.ItemID = &prevItemID
						}
						if doneItem != nil {
							argsDoneResponse.Item = doneItem
						}
						responses = append(responses, argsDoneResponse)
					}

					toolDoneItem := &schemas.ResponsesMessage{
						ID:     &prevItemID,
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: &statusCompleted,
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    &prevToolCallID,
							Name:      &prevToolName,
							Arguments: &accumulatedArgs,
						},
					}
					responses = append(responses, &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(prevOutputIndex),
						ContentIndex:   schemas.Ptr(prevContentIndex),
						ItemID:         &prevItemID,
						Item:           toolDoneItem,
					})
				}

				// Mark this output index as completed
				state.CompletedOutputIndices[prevOutputIndex] = true
			}

			// Create new output index for this tool use
			outputIndex := state.CurrentOutputIndex
			state.ContentIndexToOutputIndex[contentBlockIndex] = outputIndex
			state.CurrentOutputIndex++

			toolUseID := chunk.Start.ToolUse.ToolUseID
			toolName := bedrockRestoreToolName(state.Ctx, chunk.Start.ToolUse.Name)
			state.ItemIDs[outputIndex] = toolUseID
			state.ToolCallIDs[outputIndex] = toolUseID
			state.ToolCallNames[outputIndex] = toolName

			// Initialize argument buffer
			state.ToolArgumentBuffers[outputIndex] = ""

			statusInProgress := "in_progress"

			if toolName == "nova_code_interpreter" {
				// Emit output_item.added then code_interpreter_call.in_progress
				state.CodeInterpreterIndices[outputIndex] = true
				item := &schemas.ResponsesMessage{
					ID:     &toolUseID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
					Status: &statusInProgress,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
							ContainerID: toolUseID,
							Outputs:     []schemas.ResponsesCodeInterpreterOutput{},
						},
					},
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(contentBlockIndex),
					Item:           item,
				})
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallInProgress,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(contentBlockIndex),
					Item:           item,
				})
			} else if toolName == string(BedrockSystemToolNovaGrounding) {
				state.NovaGroundingIndices[outputIndex] = true
				state.NovaGroundingCitations[outputIndex] = nil
				item := &schemas.ResponsesMessage{
					ID:     &toolUseID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status: &statusInProgress,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: &toolUseID,
						Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
								Type: "search",
							},
						},
					},
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(contentBlockIndex),
					Item:           item,
				})
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ItemID:         &toolUseID,
				})
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ItemID:         &toolUseID,
				})
			} else {
				item := &schemas.ResponsesMessage{
					ID:     &toolUseID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status: &statusInProgress,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &toolUseID,
						Name:      &toolName,
						Arguments: schemas.Ptr(""),
					},
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(contentBlockIndex),
					Item:           item,
				})
			}

			return responses, nil, false
		}
		// Text content start is handled by Role event, so we can ignore Start for text

	case chunk.ContentBlockIndex != nil && chunk.Delta != nil:
		// Handle contentBlockDelta event
		contentBlockIndex := *chunk.ContentBlockIndex
		outputIndex, exists := state.ContentIndexToOutputIndex[contentBlockIndex]
		if !exists {
			// Check if this is a new content block that should close previous reasoning blocks
			var responses []*schemas.BifrostResponsesStreamResponse

			// If this is a text delta with a new content block index, close any open reasoning blocks
			if chunk.Delta.Text != nil && contentBlockIndex > 0 {
				for prevContentIndex := range state.ReasoningContentIndices {
					if prevContentIndex < contentBlockIndex {
						prevOutputIndex, prevExists := state.ContentIndexToOutputIndex[prevContentIndex]
						if !prevExists {
							continue
						}

						itemID := state.ItemIDs[prevOutputIndex]

						// For reasoning items, content_index is always 0
						reasoningContentIndex := 0

						// Emit reasoning_summary_text.done
						emptyText := ""
						reasoningDoneResponse := &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
							SequenceNumber: sequenceNumber + len(responses),
							OutputIndex:    schemas.Ptr(prevOutputIndex),
							ContentIndex:   &reasoningContentIndex,
							Text:           &emptyText,
						}
						if itemID != "" {
							reasoningDoneResponse.ItemID = &itemID
						}
						responses = append(responses, reasoningDoneResponse)

						// Emit content_part.done for reasoning
						part := &schemas.ResponsesMessageContentBlock{
							Type: schemas.ResponsesOutputMessageContentTypeReasoning,
							Text: &emptyText,
						}
						partDoneResponse := &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
							SequenceNumber: sequenceNumber + len(responses),
							OutputIndex:    schemas.Ptr(prevOutputIndex),
							ContentIndex:   &reasoningContentIndex,
							Part:           part,
						}
						if itemID != "" {
							partDoneResponse.ItemID = &itemID
						}
						responses = append(responses, partDoneResponse)

						// Emit output_item.done for reasoning
						statusCompleted := "completed"
						messageType := schemas.ResponsesMessageTypeReasoning
						role := schemas.ResponsesInputMessageRoleAssistant
						doneItem := &schemas.ResponsesMessage{
							Type:   &messageType,
							Role:   &role,
							Status: &statusCompleted,
							ResponsesReasoning: &schemas.ResponsesReasoning{
								Summary: []schemas.ResponsesReasoningSummary{},
							},
						}
						if itemID != "" {
							doneItem.ID = &itemID
						}
						responses = append(responses, &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
							SequenceNumber: sequenceNumber + len(responses),
							OutputIndex:    schemas.Ptr(prevOutputIndex),
							ContentIndex:   &reasoningContentIndex,
							Item:           doneItem,
						})

						// Clear the reasoning content index tracking
						delete(state.ReasoningContentIndices, prevContentIndex)

						// Mark this output index as completed
						state.CompletedOutputIndices[prevOutputIndex] = true
					}
				}
			}

			// Create new output index for this content block
			outputIndex = state.CurrentOutputIndex
			state.CurrentOutputIndex++
			state.ContentIndexToOutputIndex[contentBlockIndex] = outputIndex

			// If this is a text delta for a new content block, create the text item
			if chunk.Delta.Text != nil {
				// Generate stable ID for text item
				var itemID string
				if state.MessageID == nil {
					itemID = fmt.Sprintf("item_%d", outputIndex)
				} else {
					itemID = fmt.Sprintf("msg_%s_item_%d", *state.MessageID, outputIndex)
				}
				state.ItemIDs[outputIndex] = itemID

				// Create text item
				messageType := schemas.ResponsesMessageTypeMessage
				role := schemas.ResponsesInputMessageRoleAssistant
				item := &schemas.ResponsesMessage{
					ID:   &itemID,
					Type: &messageType,
					Role: &role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{}, // Empty blocks slice for mutation support
					},
				}

				// Emit output_item.added for text
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					Item:           item,
				})

				// Emit content_part.added with empty output_text part
				emptyText := ""
				part := &schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &emptyText,
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
					},
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					ItemID:         &itemID,
					Part:           part,
				})
			}

			// If this is a text delta for a new content block, also emit the text delta in the same batch
			if chunk.Delta.Text != nil && *chunk.Delta.Text != "" {
				text := *chunk.Delta.Text
				// Accumulate text for done events
				if state.TextBuffers[outputIndex] == nil {
					state.TextBuffers[outputIndex] = &strings.Builder{}
				}
				state.TextBuffers[outputIndex].WriteString(text)
				itemID := state.ItemIDs[outputIndex]
				textDeltaResponse := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					Delta:          &text,
					LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
				}
				if itemID != "" {
					textDeltaResponse.ItemID = &itemID
				}
				responses = append(responses, textDeltaResponse)
			}

			// If we have responses to return (either from closing reasoning or creating text item), return them first
			if len(responses) > 0 {
				return responses, nil, false
			}
		}

		switch {
		case chunk.Delta.Text != nil:
			// Handle text delta
			text := *chunk.Delta.Text
			if text != "" {
				// Accumulate text for done events
				if state.TextBuffers[outputIndex] == nil {
					state.TextBuffers[outputIndex] = &strings.Builder{}
				}
				state.TextBuffers[outputIndex].WriteString(text)
				itemID := state.ItemIDs[outputIndex]
				response := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					Delta:          &text,
					LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}

		case chunk.Delta.Citation != nil:
			citation := chunk.Delta.Citation
			if citation.Location.Web != nil {
				if state.NovaGroundingIndices[outputIndex] {
					domain := citation.Location.Web.Domain
					state.NovaGroundingCitations[outputIndex] = append(
						state.NovaGroundingCitations[outputIndex],
						schemas.ResponsesWebSearchToolCallActionSearchSource{
							Type:  "url",
							URL:   citation.Location.Web.URL,
							Title: &domain,
						},
					)
				}
				// Emit as url_citation annotation (covers both nova_grounding and text blocks).
				itemID := state.ItemIDs[outputIndex]
				annotationIndex := state.AnnotationIndices[outputIndex]
				state.AnnotationIndices[outputIndex]++
				annotation := &schemas.ResponsesOutputMessageContentTextAnnotation{
					Type:  "url_citation",
					URL:   schemas.Ptr(citation.Location.Web.URL),
					Title: schemas.Ptr(citation.Location.Web.Domain),
				}
				response := &schemas.BifrostResponsesStreamResponse{
					Type:            schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
					SequenceNumber:  sequenceNumber,
					OutputIndex:     schemas.Ptr(outputIndex),
					ContentIndex:    &contentBlockIndex,
					AnnotationIndex: &annotationIndex,
					Annotation:      annotation,
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}

		case chunk.Delta.ToolUse != nil:
			// Handle tool use delta - function call arguments or code interpreter code
			toolUseDelta := chunk.Delta.ToolUse

			if toolUseDelta.Input != "" {
				state.ToolArgumentBuffers[outputIndex] += toolUseDelta.Input

				itemID := state.ItemIDs[outputIndex]

				var response *schemas.BifrostResponsesStreamResponse
				if state.CodeInterpreterIndices[outputIndex] {
					// Each nova_code_interpreter delta is a complete JSON object {"snippet":"..."}.
					codeDelta := providerUtils.GetJSONField([]byte(toolUseDelta.Input), "snippet").String()
					response = &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   &contentBlockIndex,
						Delta:          &codeDelta,
					}
				} else {
					response = &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   &contentBlockIndex,
						Delta:          &toolUseDelta.Input,
					}
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}

		case chunk.Delta.ReasoningContent != nil:
			// Handle reasoning content delta
			reasoningDelta := chunk.Delta.ReasoningContent

			// Check if this is the first reasoning delta for this content block
			if !state.ReasoningContentIndices[contentBlockIndex] {
				// First reasoning delta - emit output_item.added and content_part.added
				var responses []*schemas.BifrostResponsesStreamResponse

				// Generate stable ID for reasoning item
				var itemID string
				if state.MessageID == nil {
					itemID = fmt.Sprintf("reasoning_%d", outputIndex)
				} else {
					itemID = fmt.Sprintf("msg_%s_reasoning_%d", *state.MessageID, outputIndex)
				}
				state.ItemIDs[outputIndex] = itemID

				// Create reasoning item
				messageType := schemas.ResponsesMessageTypeReasoning
				role := schemas.ResponsesInputMessageRoleAssistant
				item := &schemas.ResponsesMessage{
					ID:   &itemID,
					Type: &messageType,
					Role: &role,
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningSummary{},
					},
				}

				// Preserve signature if present
				if reasoningDelta.Signature != nil {
					item.ResponsesReasoning.EncryptedContent = reasoningDelta.Signature
				}

				// Track that this content index is a reasoning block
				state.ReasoningContentIndices[contentBlockIndex] = true

				// Emit output_item.added
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					Item:           item,
				})

				// Emit content_part.added with empty reasoning_text part
				emptyText := ""
				part := &schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeReasoning,
					Text: &emptyText,
				}
				// Preserve signature in the content part if present
				if reasoningDelta.Signature != nil {
					part.Signature = reasoningDelta.Signature
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentBlockIndex,
					ItemID:         &itemID,
					Part:           part,
				})

				// If there's text content, also emit the delta
				if reasoningDelta.Text != nil && *reasoningDelta.Text != "" {
					deltaResponse := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   &contentBlockIndex,
						Delta:          reasoningDelta.Text,
						ItemID:         &itemID,
					}
					responses = append(responses, deltaResponse)
				}

				return responses, nil, false
			} else {
				// Subsequent reasoning deltas - just emit the delta
				if reasoningDelta.Text != nil && *reasoningDelta.Text != "" {
					itemID := state.ItemIDs[outputIndex]
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   &contentBlockIndex,
						Delta:          reasoningDelta.Text,
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
				}

				// Handle signature deltas
				if reasoningDelta.Signature != nil {
					itemID := state.ItemIDs[outputIndex]
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   &contentBlockIndex,
						Signature:      reasoningDelta.Signature, // Use signature field instead of delta
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
				}
			}
		}

	case chunk.StopReason != nil:
		// Stop reason - track it for the final response
		stopReason := convertBedrockStopReason(*chunk.StopReason)
		state.StopReason = &stopReason
		// Items should be closed explicitly when content blocks end
		return nil, nil, false
	}

	return nil, nil, false
}

// emitCodeInterpreterDoneEvents extracts the code from accumulated JSON args and emits
// code_interpreter_call.code.done + code_interpreter_call.completed + output_item.done in sequence.
func emitCodeInterpreterDoneEvents(outputIndex, contentIndex int, itemID, containerID, accumulatedArgs string, baseSequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	code := providerUtils.GetJSONField([]byte(accumulatedArgs), "snippet").String()
	statusCompleted := "completed"
	codeDone := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone,
		SequenceNumber: baseSequenceNumber,
		OutputIndex:    schemas.Ptr(outputIndex),
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Delta:          &code,
	}
	doneItem := &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
		Status: &statusCompleted,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
				Code:        &code,
				ContainerID: containerID,
				Outputs:     []schemas.ResponsesCodeInterpreterOutput{},
			},
		},
	}
	completed := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted,
		SequenceNumber: baseSequenceNumber + 1,
		OutputIndex:    schemas.Ptr(outputIndex),
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Item:           doneItem,
	}
	outputDone := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: baseSequenceNumber + 2,
		OutputIndex:    schemas.Ptr(outputIndex),
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Item:           doneItem,
	}
	return []*schemas.BifrostResponsesStreamResponse{codeDone, completed, outputDone}
}

// emitNovaGroundingDoneEvents emits web_search_call.completed + output_item.done for a nova_grounding block.
// accumulatedArgs holds the raw toolUse input JSON (e.g. `{"query":"..."}`) from the block's deltas.
func emitNovaGroundingDoneEvents(outputIndex, contentIndex int, itemID string, citations []schemas.ResponsesWebSearchToolCallActionSearchSource, accumulatedArgs string, baseSequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	statusCompleted := "completed"
	action := &schemas.ResponsesWebSearchToolCallAction{
		Type:    "search",
		Sources: citations,
	}
	// Extract the search query from the accumulated toolUse input.
	if q := providerUtils.GetJSONField([]byte(accumulatedArgs), "query").String(); q != "" {
		action.Query = &q
		action.Queries = []string{q}
	}
	doneItem := &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
		Status: &statusCompleted,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &itemID,
			Action: &schemas.ResponsesToolMessageActionStruct{
				ResponsesWebSearchToolCallAction: action,
			},
		},
	}
	return []*schemas.BifrostResponsesStreamResponse{
		{
			Type:           schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
			SequenceNumber: baseSequenceNumber,
			OutputIndex:    schemas.Ptr(outputIndex),
			ItemID:         &itemID,
		},
		{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: baseSequenceNumber + 1,
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Item:           doneItem,
		},
	}
}

// FinalizeBedrockStream finalizes the stream by closing any open items and emitting completed event
func FinalizeBedrockStream(state *BedrockResponsesStreamState, sequenceNumber int, usage *schemas.ResponsesResponseUsage, trace *BedrockConverseTrace) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Synthesize lifecycle events if Bedrock never sent a messageStart
	if !state.HasEmittedCreated {
		if state.MessageID == nil {
			messageID := fmt.Sprintf("msg_%d", state.CreatedAt)
			state.MessageID = &messageID
		}
		createdResponse := &schemas.BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
			Usage:     usage,
		}
		if state.Model != nil {
			createdResponse.Model = *state.Model
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCreated,
			SequenceNumber: sequenceNumber + len(responses),
			Response:       createdResponse,
		})
		state.HasEmittedCreated = true
	}

	if !state.HasEmittedInProgress {
		inProgressResponse := &schemas.BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
		}
		if state.Model != nil {
			inProgressResponse.Model = *state.Model
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeInProgress,
			SequenceNumber: sequenceNumber + len(responses),
			Response:       inProgressResponse,
		})
		state.HasEmittedInProgress = true
	}

	// Close any open items (text items and tool calls)
	for contentIndex, outputIndex := range state.ContentIndexToOutputIndex {
		// Skip reasoning blocks
		if state.ReasoningContentIndices[contentIndex] {
			continue
		}

		// Skip already completed output indices
		if state.CompletedOutputIndices[outputIndex] {
			continue
		}

		itemID := state.ItemIDs[outputIndex]
		if itemID == "" {
			continue
		}

		// Check if this is a tool call by looking at the tool call IDs
		toolCallID := state.ToolCallIDs[outputIndex]
		isToolCall := toolCallID != ""

		if isToolCall {
			toolName := state.ToolCallNames[outputIndex]
			accumulatedArgs := state.ToolArgumentBuffers[outputIndex]
			statusCompleted := "completed"

			if state.CodeInterpreterIndices[outputIndex] {
				ciEvents := emitCodeInterpreterDoneEvents(outputIndex, contentIndex, itemID, toolCallID, accumulatedArgs, sequenceNumber+len(responses))
				responses = append(responses, ciEvents...)
			} else if state.NovaGroundingIndices[outputIndex] {
				citations := state.NovaGroundingCitations[outputIndex]
				wsEvents := emitNovaGroundingDoneEvents(outputIndex, contentIndex, itemID, citations, accumulatedArgs, sequenceNumber+len(responses))
				responses = append(responses, wsEvents...)
			} else {
				// Close a regular function_call
				emptyText := ""
				part := &schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &emptyText,
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
					},
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentIndex,
					ItemID:         &itemID,
					Part:           part,
				})

				if accumulatedArgs != "" {
					var doneItem *schemas.ResponsesMessage
					if toolCallID != "" || toolName != "" {
						doneItem = &schemas.ResponsesMessage{
							ResponsesToolMessage: &schemas.ResponsesToolMessage{},
						}
						if toolCallID != "" {
							doneItem.ResponsesToolMessage.CallID = &toolCallID
						}
						if toolName != "" {
							doneItem.ResponsesToolMessage.Name = &toolName
						}
					}
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						Arguments:      &accumulatedArgs,
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					if doneItem != nil {
						response.Item = doneItem
					}
					responses = append(responses, response)
				}

				doneItem := &schemas.ResponsesMessage{
					ID:     &itemID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status: &statusCompleted,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &toolCallID,
						Name:      &toolName,
						Arguments: &accumulatedArgs,
					},
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   &contentIndex,
					ItemID:         &itemID,
					Item:           doneItem,
				})
			} // end else (regular function call)
		} else {
			// This is likely a text item that needs to be closed
			accText := ""
			if buf := state.TextBuffers[outputIndex]; buf != nil {
				accText = buf.String()
			}

			// Emit output_text.done with accumulated text
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   &contentIndex,
				ItemID:         &itemID,
				Text:           &accText,
				LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
			})

			// Emit content_part.done for text with accumulated text
			partText := accText
			part := &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: &partText,
				ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
					LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
				},
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   &contentIndex,
				ItemID:         &itemID,
				Part:           part,
			})

			// Emit output_item.done for text with content blocks
			statusCompleted := "completed"
			contentBlocks := []schemas.ResponsesMessageContentBlock{}
			if accText != "" {
				itemText := accText
				contentBlocks = []schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: &itemText,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						},
					},
				}
			}
			messageType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant
			doneItem := &schemas.ResponsesMessage{
				Type:   &messageType,
				Role:   &role,
				Status: &statusCompleted,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: contentBlocks,
				},
			}
			if itemID != "" {
				doneItem.ID = &itemID
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   &contentIndex,
				Item:           doneItem,
			})
			delete(state.TextBuffers, outputIndex)
		}

		// Mark this output index as completed
		state.CompletedOutputIndices[outputIndex] = true
	}

	// Close any open reasoning items
	for contentIndex := range state.ReasoningContentIndices {
		outputIndex, exists := state.ContentIndexToOutputIndex[contentIndex]
		if !exists {
			continue
		}

		// Skip already completed output indices
		if state.CompletedOutputIndices[outputIndex] {
			continue
		}

		itemID := state.ItemIDs[outputIndex]

		// For reasoning items, content_index is always 0 (reasoning content is the first and only content part)
		reasoningContentIndex := 0

		// Emit reasoning_summary_text.done
		emptyText := ""
		reasoningDoneResponse := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   &reasoningContentIndex,
			Text:           &emptyText,
		}
		if itemID != "" {
			reasoningDoneResponse.ItemID = &itemID
		}
		responses = append(responses, reasoningDoneResponse)

		// Emit content_part.done for reasoning
		part := &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesOutputMessageContentTypeReasoning,
			Text: &emptyText,
		}
		partDoneResponse := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   &reasoningContentIndex,
			Part:           part,
		}
		if itemID != "" {
			partDoneResponse.ItemID = &itemID
		}
		responses = append(responses, partDoneResponse)

		// Emit output_item.done for reasoning
		statusCompleted := "completed"
		messageType := schemas.ResponsesMessageTypeReasoning
		role := schemas.ResponsesInputMessageRoleAssistant
		doneItem := &schemas.ResponsesMessage{
			Type:   &messageType,
			Role:   &role,
			Status: &statusCompleted,
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			},
		}
		if itemID != "" {
			doneItem.ID = &itemID
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    schemas.Ptr(outputIndex),
			ContentIndex:   &reasoningContentIndex,
			Item:           doneItem,
		})

		// Mark this output index as completed
		state.CompletedOutputIndices[outputIndex] = true
	}

	// Note: Tool calls are already closed in the first loop above.
	// This section is intentionally left empty to avoid duplicate events.

	if usage.InputTokensDetails != nil {
		usage.InputTokens = usage.InputTokens + usage.InputTokensDetails.CachedReadTokens + usage.InputTokensDetails.CachedWriteTokens
	}

	// Emit response.completed
	response := &schemas.BifrostResponsesResponse{
		ID:        state.MessageID,
		CreatedAt: state.CreatedAt,
		Usage:     usage,
	}

	if trace != nil {
		response.ProviderExtraFields = map[string]interface{}{
			"trace": trace,
		}
	}

	if state.Model != nil {
		response.Model = *state.Model
	}
	if state.StopReason != nil {
		stopReason := *state.StopReason
		// If only the SO tool was consumed (no real tool calls in state), downgrade tool_calls → stop.
		if stopReason == string(schemas.BifrostFinishReasonToolCalls) && state.UsedStructuredOutputTool {
			hasRealToolCall := false
			for _, toolCallID := range state.ToolCallIDs {
				if toolCallID != "" {
					hasRealToolCall = true
					break
				}
			}
			if !hasRealToolCall {
				stopReason = string(schemas.BifrostFinishReasonStop)
			}
		}
		response.StopReason = &stopReason
	} else {
		// Infer stop reason based on whether tool calls are present
		hasToolCalls := false
		for _, toolCallID := range state.ToolCallIDs {
			if toolCallID != "" {
				hasToolCalls = true
				break
			}
		}
		if hasToolCalls {
			response.StopReason = schemas.Ptr("tool_calls")
		} else {
			response.StopReason = schemas.Ptr("stop")
		}
	}

	// Set Status/IncompleteDetails and the terminal event type per OpenAI's
	// Responses-API contract, matching the non-streaming switch above so
	// unmapped reasons leave Status unset on both paths.
	terminalEventType := schemas.ResponsesStreamResponseTypeCompleted
	if response.StopReason != nil {
		switch *response.StopReason {
		case string(schemas.BifrostFinishReasonLength):
			terminalEventType = schemas.ResponsesStreamResponseTypeIncomplete
			response.Status = schemas.Ptr(schemas.ResponsesResponseStatusIncomplete)
			response.IncompleteDetails = &schemas.ResponsesResponseIncompleteDetails{
				Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens,
			}
		case string(schemas.BifrostFinishReasonStop), string(schemas.BifrostFinishReasonToolCalls):
			if response.Status == nil {
				response.Status = schemas.Ptr(schemas.ResponsesResponseStatusCompleted)
			}
		}
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           terminalEventType,
		SequenceNumber: sequenceNumber + len(responses),
		Response:       response,
	})

	return responses
}

// buildBedrockTokenUsage maps Responses usage to Bedrock usage. Cached tokens are folded
// into InputTokens upstream, so they are copied into the cache fields and subtracted back
// out of InputTokens. Shared by the streaming and non-streaming converters to keep them in sync.
func buildBedrockTokenUsage(usage *schemas.ResponsesResponseUsage) *BedrockTokenUsage {
	if usage == nil {
		return nil
	}
	out := &BedrockTokenUsage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if usage.InputTokensDetails != nil {
		if usage.InputTokensDetails.CachedReadTokens > 0 {
			out.CacheReadInputTokens = usage.InputTokensDetails.CachedReadTokens
			out.InputTokens -= usage.InputTokensDetails.CachedReadTokens
		}
		if usage.InputTokensDetails.CachedWriteTokens > 0 {
			out.CacheWriteInputTokens = usage.InputTokensDetails.CachedWriteTokens
			if d := usage.InputTokensDetails.CachedWriteTokenDetails; d != nil {
				var cacheDetails []BedrockCacheWriteDetails
				if d.CachedWriteTokens5m > 0 {
					cacheDetails = append(cacheDetails, BedrockCacheWriteDetails{InputTokens: d.CachedWriteTokens5m, TTL: BedrockCacheWriteTTL5m})
				}
				if d.CachedWriteTokens1h > 0 {
					cacheDetails = append(cacheDetails, BedrockCacheWriteDetails{InputTokens: d.CachedWriteTokens1h, TTL: BedrockCacheWriteTTL1h})
				}
				out.CacheDetails = &cacheDetails
			}
			out.InputTokens -= usage.InputTokensDetails.CachedWriteTokens
		}
	}
	return out
}

// ToBedrockConverseStreamResponse converts a Bifrost Responses stream response to Bedrock streaming format
// Returns a BedrockStreamEvent that represents the streaming event in Bedrock's format
func ToBedrockConverseStreamResponse(bifrostResp *schemas.BifrostResponsesStreamResponse) (*BedrockStreamEvent, error) {
	if bifrostResp == nil {
		return nil, fmt.Errorf("bifrost stream response is nil")
	}

	event := &BedrockStreamEvent{}

	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypeCreated:
		// Message start - emit role event
		// Always set role for message start event
		role := "assistant"
		event.Role = &role

	case schemas.ResponsesStreamResponseTypeInProgress:
		// In progress - no-op for Bedrock (it doesn't have an explicit in_progress event)
		// Return nil to skip this event
		return nil, nil

	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		// Content block start — handles nova_grounding (web_search_call), function calls, and text items.
		if bifrostResp.Item != nil && bifrostResp.Item.ResponsesToolMessage != nil {
			contentBlockIndex := 0
			if bifrostResp.ContentIndex != nil {
				contentBlockIndex = *bifrostResp.ContentIndex
			}
			// web_search_call (nova_grounding): CallID is set, Name is nil
			if bifrostResp.Item.Type != nil && *bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebSearchCall &&
				bifrostResp.Item.ResponsesToolMessage.CallID != nil {
				event.ContentBlockIndex = &contentBlockIndex
				event.Start = &BedrockContentBlockStart{
					ToolUse: &BedrockToolUseStart{
						ToolUseID: *bifrostResp.Item.ResponsesToolMessage.CallID,
						Name:      string(BedrockSystemToolNovaGrounding),
					},
				}
			} else if bifrostResp.Item.ResponsesToolMessage.Name != nil && bifrostResp.Item.ResponsesToolMessage.CallID != nil {
				// Regular function call
				event.ContentBlockIndex = &contentBlockIndex
				event.Start = &BedrockContentBlockStart{
					ToolUse: &BedrockToolUseStart{
						ToolUseID: *bifrostResp.Item.ResponsesToolMessage.CallID,
						Name:      *bifrostResp.Item.ResponsesToolMessage.Name,
					},
				}
			} else {
				return nil, nil
			}
		} else if bifrostResp.Item != nil {
			// Text item added - Bedrock doesn't have an explicit text start event, so we skip it
			if bifrostResp.Item.Content != nil || (bifrostResp.Item.Type != nil && *bifrostResp.Item.Type == schemas.ResponsesMessageTypeMessage) {
				return nil, nil
			}
		}

	case schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded:
		// url_citation annotation → contentBlockDelta.citation
		if bifrostResp.Annotation != nil && bifrostResp.Annotation.URL != nil {
			contentBlockIndex := 0
			if bifrostResp.ContentIndex != nil {
				contentBlockIndex = *bifrostResp.ContentIndex
			}
			domain := ""
			if bifrostResp.Annotation.Title != nil {
				domain = *bifrostResp.Annotation.Title
			}
			event.ContentBlockIndex = &contentBlockIndex
			event.Delta = &BedrockContentBlockDelta{
				Citation: &BedrockCitation{
					Location: BedrockCitationLocation{
						Web: &BedrockWebCitationLocation{
							URL:    *bifrostResp.Annotation.URL,
							Domain: domain,
						},
					},
				},
			}
		} else {
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsAdded,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsCompleted:
		// No Bedrock equivalent for these status events — skip.
		return nil, nil

	case schemas.ResponsesStreamResponseTypeCodeInterpreterCallInProgress:
		// nova_code_interpreter → contentBlockStart
		if bifrostResp.Item != nil && bifrostResp.Item.ResponsesToolMessage != nil &&
			bifrostResp.Item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall != nil {
			toolUseID := bifrostResp.Item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.ContainerID
			if toolUseID == "" && bifrostResp.Item.ID != nil {
				toolUseID = *bifrostResp.Item.ID
			}
			contentBlockIndex := 0
			if bifrostResp.ContentIndex != nil {
				contentBlockIndex = *bifrostResp.ContentIndex
			}
			event.ContentBlockIndex = &contentBlockIndex
			event.Start = &BedrockContentBlockStart{
				ToolUse: &BedrockToolUseStart{
					ToolUseID: toolUseID,
					Name:      string(BedrockSystemToolNovaCodeInterpreter),
				},
			}
		} else {
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta:
		// nova_code_interpreter toolUse delta — wrap snippet back into {"snippet":"..."} JSON
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			contentBlockIndex := 0
			if bifrostResp.ContentIndex != nil {
				contentBlockIndex = *bifrostResp.ContentIndex
			}
			inputJSON, _ := json.Marshal(map[string]string{"snippet": *bifrostResp.Delta})
			event.ContentBlockIndex = &contentBlockIndex
			event.Delta = &BedrockContentBlockDelta{
				ToolUse: &BedrockToolUseDelta{
					Input: string(inputJSON),
				},
			}
		} else {
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallInterpreting:
		// No Bedrock equivalent — skip.
		return nil, nil

	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		// Text delta
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			contentBlockIndex := 0
			if bifrostResp.ContentIndex != nil {
				contentBlockIndex = *bifrostResp.ContentIndex
			}
			event.ContentBlockIndex = &contentBlockIndex
			event.Delta = &BedrockContentBlockDelta{
				Text: bifrostResp.Delta,
			}
		} else {
			// Skip empty deltas
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		// Tool use delta (function call arguments)
		if bifrostResp.Delta != nil {
			contentBlockIndex := 0
			if bifrostResp.ContentIndex != nil {
				contentBlockIndex = *bifrostResp.ContentIndex
			}
			event.ContentBlockIndex = &contentBlockIndex
			event.Delta = &BedrockContentBlockDelta{
				ToolUse: &BedrockToolUseDelta{
					Input: *bifrostResp.Delta,
				},
			}
		}

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		// Reasoning content delta
		contentBlockIndex := 0
		if bifrostResp.ContentIndex != nil {
			contentBlockIndex = *bifrostResp.ContentIndex
		}
		event.ContentBlockIndex = &contentBlockIndex

		// Check if this is a signature delta or text delta
		if bifrostResp.Signature != nil {
			// This is a signature delta
			event.Delta = &BedrockContentBlockDelta{
				ReasoningContent: &BedrockReasoningContentText{
					Signature: bifrostResp.Signature,
				},
			}
		} else if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			// This is reasoning text delta
			event.Delta = &BedrockContentBlockDelta{
				ReasoningContent: &BedrockReasoningContentText{
					Text: bifrostResp.Delta,
				},
			}
		} else {
			// Skip empty deltas
			return nil, nil
		}

	case schemas.ResponsesStreamResponseTypeOutputTextDone,
		schemas.ResponsesStreamResponseTypeContentPartDone,
		schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone:
		// Content block done - the contentBlockStop is emitted on OutputItemDone,
		// matching the invoke path
		return nil, nil

	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		// Item done - emit contentBlockStop. Bedrock terminates every content block
		// with a contentBlockStop event carrying the block's index; consumers that
		// assemble the message on block boundaries never finalize a block without it.
		contentBlockIndex := 0
		if bifrostResp.ContentIndex != nil {
			contentBlockIndex = *bifrostResp.ContentIndex
		}
		event.ContentBlockIndex = &contentBlockIndex
		event.ContentBlockStop = true

	case schemas.ResponsesStreamResponseTypeCompleted:
		// Message stop - always set stopReason.
		// Derive stop reason: StopReason > IncompleteDetails > tool_use detection > end_turn,
		// matching the non-streaming Converse converter.
		stopReason := "end_turn"
		if bifrostResp.Response != nil {
			if bifrostResp.Response.StopReason != nil {
				stopReason = convertBifrostToBedrockStopReason(*bifrostResp.Response.StopReason)
			} else if bifrostResp.Response.IncompleteDetails != nil {
				stopReason = convertIncompleteReasonToBedrockStopReason(bifrostResp.Response.IncompleteDetails.Reason)
			} else {
				for _, msg := range bifrostResp.Response.Output {
					if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall {
						stopReason = "tool_use"
						break
					}
				}
			}
		}
		event.StopReason = &stopReason

		// Add usage if available
		if bifrostResp.Response != nil {
			event.Usage = buildBedrockTokenUsage(bifrostResp.Response.Usage)
		}

		// Restore guardrail trace from provider extra fields
		if bifrostResp.Response != nil && bifrostResp.Response.ProviderExtraFields != nil {
			event.Trace = extractBedrockTrace(bifrostResp.Response.ProviderExtraFields["trace"])
		}

	case schemas.ResponsesStreamResponseTypeError:
		// Error - errors are handled separately by the router via BifrostError in the stream chunk
		// Return nil to skip this chunk
		return nil, nil

	default:
		// Unknown type - skip
		return nil, nil
	}

	return event, nil
}

// BedrockEncodedEvent represents a single event ready for encoding to AWS Event Stream
type BedrockEncodedEvent struct {
	EventType string
	Payload   interface{}
}

// BedrockInvokeStreamChunkEvent represents the chunk event for invoke-with-response-stream
type BedrockInvokeStreamChunkEvent struct {
	Bytes []byte `json:"bytes"`
}

// ToEncodedEvents converts the flat BedrockStreamEvent into a sequence of specific events
func (event *BedrockStreamEvent) ToEncodedEvents() []BedrockEncodedEvent {
	var events []BedrockEncodedEvent

	for _, rawChunk := range event.InvokeModelRawChunks {
		events = append(events, BedrockEncodedEvent{
			EventType: "chunk",
			Payload: BedrockInvokeStreamChunkEvent{
				Bytes: rawChunk,
			},
		})
	}

	if event.Role != nil {
		events = append(events, BedrockEncodedEvent{
			EventType: "messageStart",
			Payload: BedrockMessageStartEvent{
				Role: *event.Role,
			},
		})
	}

	if event.Start != nil {
		events = append(events, BedrockEncodedEvent{
			EventType: "contentBlockStart",
			Payload: struct {
				Start             *BedrockContentBlockStart `json:"start"`
				ContentBlockIndex *int                      `json:"contentBlockIndex"`
			}{
				Start:             event.Start,
				ContentBlockIndex: event.ContentBlockIndex,
			},
		})
	}

	if event.Delta != nil {
		events = append(events, BedrockEncodedEvent{
			EventType: "contentBlockDelta",
			Payload: struct {
				Delta             *BedrockContentBlockDelta `json:"delta"`
				ContentBlockIndex *int                      `json:"contentBlockIndex"`
			}{
				Delta:             event.Delta,
				ContentBlockIndex: event.ContentBlockIndex,
			},
		})
	}

	if event.ContentBlockStop {
		events = append(events, BedrockEncodedEvent{
			EventType: "contentBlockStop",
			Payload: struct {
				ContentBlockIndex *int `json:"contentBlockIndex"`
			}{
				ContentBlockIndex: event.ContentBlockIndex,
			},
		})
	}

	if event.StopReason != nil {
		events = append(events, BedrockEncodedEvent{
			EventType: "messageStop",
			Payload: BedrockMessageStopEvent{
				StopReason: *event.StopReason,
			},
		})
	}

	if event.Usage != nil || event.Metrics != nil || event.Trace != nil {
		events = append(events, BedrockEncodedEvent{
			EventType: "metadata",
			Payload: BedrockMetadataEvent{
				Usage:   event.Usage,
				Metrics: event.Metrics,
				Trace:   event.Trace,
			},
		})
	}

	return events
}

// ToBifrostResponsesRequest converts a BedrockConverseRequest to Bifrost Responses Request format
func (request *BedrockConverseRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) (*schemas.BifrostResponsesRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("bedrock request is nil")
	}

	// Extract provider from model ID (format: "bedrock/model-name")
	provider, model := schemas.ParseModelString(request.ModelID, "")

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider:  provider,
		Model:     model,
		Params:    &schemas.ResponsesParameters{},
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}

	// Convert messages using the new conversion method
	convertedMessages := ConvertBedrockMessagesToBifrostMessages(ctx, request.Messages, request.System, false)
	bifrostReq.Input = convertedMessages

	// Convert inference config to parameters
	if request.InferenceConfig != nil {
		if request.InferenceConfig.MaxTokens != nil {
			bifrostReq.Params.MaxOutputTokens = request.InferenceConfig.MaxTokens
		}
		if request.InferenceConfig.Temperature != nil {
			bifrostReq.Params.Temperature = request.InferenceConfig.Temperature
		}
		if request.InferenceConfig.TopP != nil {
			bifrostReq.Params.TopP = request.InferenceConfig.TopP
		}
		if len(request.InferenceConfig.StopSequences) > 0 {
			if bifrostReq.Params.ExtraParams == nil {
				bifrostReq.Params.ExtraParams = make(map[string]interface{})
			}
			bifrostReq.Params.ExtraParams["stop"] = request.InferenceConfig.StopSequences
		}
	}

	// Convert tool config
	if request.ToolConfig != nil && len(request.ToolConfig.Tools) > 0 {
		for _, tool := range request.ToolConfig.Tools {
			if tool.ToolSpec != nil {
				bifrostTool := schemas.ResponsesTool{
					Type:                  schemas.ResponsesToolTypeFunction,
					Name:                  &tool.ToolSpec.Name,
					Description:           tool.ToolSpec.Description,
					ResponsesToolFunction: &schemas.ResponsesToolFunction{},
				}

				// Handle different types for InputSchema.JSON
				if len(tool.ToolSpec.InputSchema.JSON) > 0 {
					var params schemas.ToolFunctionParameters
					if err := sonic.Unmarshal(tool.ToolSpec.InputSchema.JSON, &params); err == nil {
						bifrostTool.ResponsesToolFunction.Parameters = &params
					} else {
						// Fallback: unmarshal as map and convert
						var paramsMap map[string]interface{}
						if err := sonic.Unmarshal(tool.ToolSpec.InputSchema.JSON, &paramsMap); err == nil {
							params := convertMapToToolFunctionParameters(paramsMap)
							bifrostTool.ResponsesToolFunction.Parameters = params
						}
					}
				}

				bifrostReq.Params.Tools = append(bifrostReq.Params.Tools, bifrostTool)
			} else if tool.SystemTool != nil {
				// Nova system tools: nova_grounding → web_search, nova_code_interpreter → code_interpreter
				var toolType schemas.ResponsesToolType
				switch tool.SystemTool.Name {
				case BedrockSystemToolNovaGrounding:
					toolType = schemas.ResponsesToolTypeWebSearch
				case BedrockSystemToolNovaCodeInterpreter:
					toolType = schemas.ResponsesToolTypeCodeInterpreter
				default:
					continue
				}
				bifrostReq.Params.Tools = append(bifrostReq.Params.Tools, schemas.ResponsesTool{Type: toolType})
			} else if tool.CachePoint != nil && !schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
				// add cache control to last tool in tools array
				if len(bifrostReq.Params.Tools) > 0 {
					bifrostReq.Params.Tools[len(bifrostReq.Params.Tools)-1].CacheControl = &schemas.CacheControl{
						Type: schemas.CacheControlTypeEphemeral,
						TTL:  tool.CachePoint.TTL,
					}
				}
			}
		}
	}

	// Convert tool choice
	if request.ToolConfig != nil && request.ToolConfig.ToolChoice != nil {
		toolChoice := request.ToolConfig.ToolChoice
		if toolChoice.Auto != nil {
			autoStr := string(schemas.ResponsesToolChoiceTypeAuto)
			bifrostReq.Params.ToolChoice = &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStr: &autoStr,
			}
		} else if toolChoice.Any != nil {
			anyStr := string(schemas.ResponsesToolChoiceTypeAny)
			bifrostReq.Params.ToolChoice = &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStr: &anyStr,
			}
		} else if toolChoice.Tool != nil {
			bifrostReq.Params.ToolChoice = &schemas.ResponsesToolChoice{
				ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: &toolChoice.Tool.Name,
				},
			}
		}
	}

	// Convert guardrail config to extra params
	if request.GuardrailConfig != nil {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}

		guardrailMap := map[string]interface{}{
			"guardrailIdentifier": request.GuardrailConfig.GuardrailIdentifier,
			"guardrailVersion":    request.GuardrailConfig.GuardrailVersion,
		}
		if request.GuardrailConfig.Trace != nil {
			guardrailMap["trace"] = *request.GuardrailConfig.Trace
		}
		bifrostReq.Params.ExtraParams["guardrailConfig"] = guardrailMap
	}

	// Convert additional model request fields to extra params
	if request.AdditionalModelRequestFields.Len() > 0 {
		// Handle Anthropic thinking/reasoning_config format
		reasoningConfig, ok := request.AdditionalModelRequestFields.Get("thinking")
		if !ok {
			reasoningConfig, ok = request.AdditionalModelRequestFields.Get("reasoning_config")
		}
		if ok {
			// May arrive as *OrderedMap (HTTP unmarshal) or map[string]interface{} (in-process)
			if reasoningConfigOrderedMap, ok := schemas.SafeExtractOrderedMap(reasoningConfig); ok && reasoningConfigOrderedMap != nil {
				reasoningConfigMap := reasoningConfigOrderedMap.ToMap()
				if typeStr, ok := schemas.SafeExtractString(reasoningConfigMap["type"]); ok {
					if typeStr == "enabled" || typeStr == "adaptive" {
						var summary *string
						if summaryValue, ok := schemas.SafeExtractStringPointer(request.ExtraParams["reasoning_summary"]); ok {
							summary = summaryValue
						}
						var (
							effortStr string
							found     bool
						)
						// Check for native output_config.effort first.
						// output_config may be preserved as OrderedMap by the merge path.
						if outputConfig, ok := request.AdditionalModelRequestFields.Get("output_config"); ok {
							if outputConfigOrderedMap, ok := schemas.SafeExtractOrderedMap(outputConfig); ok && outputConfigOrderedMap != nil {
								if effortValue, exists := outputConfigOrderedMap.Get("effort"); exists {
									effortStr, found = schemas.SafeExtractString(effortValue)
								}
							} else if outputConfigMap, ok := outputConfig.(map[string]interface{}); ok {
								effortStr, found = schemas.SafeExtractString(outputConfigMap["effort"])
							}
						}
						if found {
							var maxTokens *int
							if budgetTokens, ok := schemas.SafeExtractInt(reasoningConfigMap["budget_tokens"]); ok {
								maxTokens = schemas.Ptr(budgetTokens)
							}
							bifrostReq.Params.Reasoning = &schemas.ResponsesParametersReasoning{
								Effort:    schemas.Ptr(effortStr),
								MaxTokens: maxTokens,
								Summary:   summary,
							}
						} else if maxTokens, ok := schemas.SafeExtractInt(reasoningConfigMap["budget_tokens"]); ok {
							// Fallback: convert budget_tokens to effort
							minBudgetTokens := 0
							defaultMaxTokens := providerUtils.GetMaxOutputTokensOrDefault(bifrostReq.Model, DefaultCompletionMaxTokens)
							if request.InferenceConfig != nil && request.InferenceConfig.MaxTokens != nil {
								defaultMaxTokens = *request.InferenceConfig.MaxTokens
							}
							if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
								minBudgetTokens = anthropic.MinimumReasoningMaxTokens
							}
							effort := providerUtils.GetReasoningEffortFromBudgetTokens(maxTokens, minBudgetTokens, defaultMaxTokens)
							bifrostReq.Params.Reasoning = &schemas.ResponsesParametersReasoning{
								Effort:    schemas.Ptr(effort),
								MaxTokens: schemas.Ptr(maxTokens),
								Summary:   summary,
							}
						} else {
							// Adaptive with no explicit effort — default to "high"
							bifrostReq.Params.Reasoning = &schemas.ResponsesParametersReasoning{
								Effort:  schemas.Ptr("high"),
								Summary: summary,
							}
						}
					} else {
						bifrostReq.Params.Reasoning = &schemas.ResponsesParametersReasoning{
							Effort: schemas.Ptr("none"),
						}
					}
				}
			}
		}

		// Handle Nova reasoningConfig format (camelCase)
		if novaReasoningConfig, ok := request.AdditionalModelRequestFields.Get("reasoningConfig"); ok {
			if novaReasoningConfigOrderedMap, ok := schemas.SafeExtractOrderedMap(novaReasoningConfig); ok && novaReasoningConfigOrderedMap != nil {
				novaReasoningConfigMap := novaReasoningConfigOrderedMap.ToMap()
				if typeStr, ok := schemas.SafeExtractString(novaReasoningConfigMap["type"]); ok {
					if typeStr == "enabled" {
						// Extract maxReasoningEffort from Nova format
						if effortStr, ok := schemas.SafeExtractString(novaReasoningConfigMap["maxReasoningEffort"]); ok {
							bifrostReq.Params.Reasoning = &schemas.ResponsesParametersReasoning{
								Effort: schemas.Ptr(effortStr),
							}
						}
					} else if typeStr == "disabled" {
						bifrostReq.Params.Reasoning = &schemas.ResponsesParametersReasoning{
							Effort: schemas.Ptr("none"),
						}
					}
				}
			}
		}
	}

	if include, ok := schemas.SafeExtractStringSlice(request.ExtraParams["include"]); ok {
		bifrostReq.Params.Include = include
	}

	// Convert performance config to extra params
	if request.PerformanceConfig != nil {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}

		perfConfigMap := map[string]interface{}{}
		if request.PerformanceConfig.Latency != nil {
			perfConfigMap["latency"] = *request.PerformanceConfig.Latency
		}
		if len(perfConfigMap) > 0 {
			bifrostReq.Params.ExtraParams["performanceConfig"] = perfConfigMap
		}
	}

	// Convert prompt variables to extra params
	if len(request.PromptVariables) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}

		promptVarsMap := make(map[string]interface{})
		for key, value := range request.PromptVariables {
			varMap := map[string]interface{}{}
			if value.Text != nil {
				varMap["text"] = *value.Text
			}
			if len(varMap) > 0 {
				promptVarsMap[key] = varMap
			}
		}
		if len(promptVarsMap) > 0 {
			bifrostReq.Params.ExtraParams["promptVariables"] = promptVarsMap
		}
	}

	// Convert request metadata to extra params
	if len(request.RequestMetadata) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["requestMetadata"] = request.RequestMetadata
	}

	// Convert additional model request fields to extra params
	if request.AdditionalModelRequestFields.Len() > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["additionalModelRequestFieldPaths"] = request.AdditionalModelRequestFields
	}

	// Convert additional model response field paths to extra params
	if len(request.AdditionalModelResponseFieldPaths) > 0 {
		if bifrostReq.Params.ExtraParams == nil {
			bifrostReq.Params.ExtraParams = make(map[string]interface{})
		}
		bifrostReq.Params.ExtraParams["additionalModelResponseFieldPaths"] = request.AdditionalModelResponseFieldPaths
	}

	return bifrostReq, nil
}

// ToBedrockResponsesRequest converts a BifrostRequest (Responses structure) back to BedrockConverseRequest
func ToBedrockResponsesRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostResponsesRequest) (*BedrockConverseRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	// capModel is the canonical model used only for Anthropic capability gating
	capModel := schemas.ResolveCanonicalModel(ctx, bifrostReq.Model)

	// Filter provider-unsupported tools (e.g. an `mcp` server tool that points
	// back at Bifrost's own gateway) instead of failing the whole request. This
	// mirrors the Chat path (bedrock/utils.go ValidateChatToolsForProvider) and
	// restores pre-v1.5.0 behavior: function/custom tools are always kept, so the
	// model still sees the tools Bifrost injected/executes; only tools Bedrock's
	// Converse API genuinely can't consume are dropped. The kept slice is used
	// locally below — bifrostReq.Params.Tools is never mutated.
	var keepTools []schemas.ResponsesTool
	if bifrostReq.Params != nil && bifrostReq.Params.Tools != nil {
		keepTools, _ = anthropic.ValidateResponsesToolsForProvider(bifrostReq.Params.Tools, schemas.Bedrock)
	}

	bedrockReq := &BedrockConverseRequest{
		ModelID: bifrostReq.Model,
	}

	// map bifrost messages to bedrock messages using the new conversion method
	if bifrostReq.Input != nil {
		input := bifrostReq.Input
		if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) && ctx.Value(schemas.BifrostContextKeySupportsAssistantPrefill) == false {
			trimmed := len(input)
			for trimmed > 0 && input[trimmed-1].Role != nil && *input[trimmed-1].Role == schemas.ResponsesInputMessageRoleAssistant {
				trimmed--
			}
			input = input[:trimmed]
		}

		// Inline mid-conversation system reminders for Anthropic models (keeps Bedrock's
		// prefix-based prompt cache stable); hoist-everything for other families.
		messages, systemMessages, err := ConvertBifrostMessagesToBedrockMessages(ctx, input, schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model))
		if err != nil {
			return nil, fmt.Errorf("failed to convert Responses messages: %w", err)
		}
		bedrockReq.Messages = messages
		if len(systemMessages) > 0 {
			bedrockReq.System = systemMessages
		} else {
			if bifrostReq.Params != nil && bifrostReq.Params.Instructions != nil {
				// if no system messages, check if instructions are present
				bedrockReq.System = []BedrockSystemMessage{
					{
						Text: bifrostReq.Params.Instructions,
					},
				}
			}
		}

		// Trim trailing whitespace from the last assistant message text blocks
		// (only for Anthropic models which use text-based prefill)
		lastMsgIndex := len(bedrockReq.Messages) - 1
		if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) && lastMsgIndex >= 0 && bedrockReq.Messages[lastMsgIndex].Role == BedrockMessageRoleAssistant {
			blocks := bedrockReq.Messages[lastMsgIndex].Content
			for j := len(blocks) - 1; j >= 0; j-- {
				if blocks[j].Text != nil {
					bedrockReq.Messages[lastMsgIndex].Content[j].Text = schemas.Ptr(strings.TrimRight(*blocks[j].Text, " \n\r\t"))
					break
				}
			}
		}
	}

	var responsesStructuredOutputTool *BedrockTool

	// Map basic parameters to inference config
	if bifrostReq.Params != nil {
		inferenceConfig := &BedrockInferenceConfig{}

		if bifrostReq.Params.MaxOutputTokens != nil {
			inferenceConfig.MaxTokens = bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			inferenceConfig.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			inferenceConfig.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.Reasoning != nil {
			if bedrockReq.AdditionalModelRequestFields == nil {
				bedrockReq.AdditionalModelRequestFields = schemas.NewOrderedMap()
			}
			if bifrostReq.Params.Reasoning.MaxTokens != nil {
				tokenBudget := *bifrostReq.Params.Reasoning.MaxTokens
				if *bifrostReq.Params.Reasoning.MaxTokens == -1 {
					// bedrock does not support dynamic reasoning budget like gemini
					// setting it to default max tokens
					tokenBudget = anthropic.MinimumReasoningMaxTokens
				}
				if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
					if anthropic.IsAdaptiveOnlyThinkingModel(capModel) {
						bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
							"type": "adaptive",
						})
						// Preserve a co-present effort — these models support effort,
						// and the budget is otherwise dropped.
						if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
							setOutputConfigField(bedrockReq.AdditionalModelRequestFields, "effort", anthropic.MapBifrostEffortToAnthropic(*bifrostReq.Params.Reasoning.Effort))
						}
					} else {
						if tokenBudget < anthropic.MinimumReasoningMaxTokens {
							return nil, fmt.Errorf("reasoning.max_tokens must be >= %d for anthropic", anthropic.MinimumReasoningMaxTokens)
						}
						bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
							"type":          "enabled",
							"budget_tokens": tokenBudget,
						})
					}
				} else if schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
					minBudgetTokens := MinimumReasoningMaxTokens
					modelDefaultMaxTokens := providerUtils.GetMaxOutputTokensOrDefault(bifrostReq.Model, DefaultCompletionMaxTokens)
					defaultMaxTokens := modelDefaultMaxTokens
					if inferenceConfig.MaxTokens != nil {
						defaultMaxTokens = *inferenceConfig.MaxTokens
					} else {
						inferenceConfig.MaxTokens = schemas.Ptr(modelDefaultMaxTokens)
					}
					maxReasoningEffort := providerUtils.GetReasoningEffortFromBudgetTokens(tokenBudget, minBudgetTokens, defaultMaxTokens)
					typeStr := "enabled"
					switch maxReasoningEffort {
					case "high":
						inferenceConfig.MaxTokens = nil
						inferenceConfig.Temperature = nil
						inferenceConfig.TopP = nil
					case "minimal":
						maxReasoningEffort = "low"
					case "none":
						typeStr = "disabled"
					}

					config := map[string]any{
						"type": typeStr,
					}
					if typeStr != "disabled" {
						config["maxReasoningEffort"] = maxReasoningEffort
					}

					bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", config)
				}
			} else {
				if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
					if schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
						effort := *bifrostReq.Params.Reasoning.Effort
						typeStr := "enabled"
						switch effort {
						case "high", "xhigh", "max":
							// Nova's maxReasoningEffort enum tops out at "high"; clamp
							// xhigh/max and unset these fields at high effort.
							effort = "high"
							inferenceConfig.MaxTokens = nil
							inferenceConfig.Temperature = nil
							inferenceConfig.TopP = nil
						case "low", "medium":
							// no special handling needed for low and medium
						case "minimal":
							effort = "low"
						case "none":
							typeStr = "disabled"
						}

						config := map[string]any{
							"type": typeStr,
						}
						if typeStr != "disabled" {
							config["maxReasoningEffort"] = effort
						}

						bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", config)
					} else if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
						if anthropic.SupportsAdaptiveThinking(capModel) {
							// Opus 4.6+: adaptive thinking + output_config.effort
							effort := anthropic.MapBifrostEffortToAnthropic(*bifrostReq.Params.Reasoning.Effort)
							thinkingConfig := map[string]any{
								"type": "adaptive",
							}
							// default to "summarized" for Opus 4.7+ where omitting is the provider default.
							if bifrostReq.Params.Reasoning.Summary != nil {
								if *bifrostReq.Params.Reasoning.Summary == "none" {
									thinkingConfig["display"] = "omitted"
								} else {
									thinkingConfig["display"] = "summarized"
								}
							} else if anthropic.IsAdaptiveOnlyThinkingModel(capModel) {
								thinkingConfig["display"] = "summarized"
							}
							bedrockReq.AdditionalModelRequestFields.Set("thinking", thinkingConfig)
							setOutputConfigField(bedrockReq.AdditionalModelRequestFields, "effort", effort)
						} else {
							// Opus 4.5 and older Anthropic models: budget_tokens thinking
							modelDefaultMaxTokens := providerUtils.GetMaxOutputTokensOrDefault(bifrostReq.Model, DefaultCompletionMaxTokens)
							defaultMaxTokens := modelDefaultMaxTokens
							if inferenceConfig.MaxTokens != nil {
								defaultMaxTokens = *inferenceConfig.MaxTokens
							} else {
								inferenceConfig.MaxTokens = schemas.Ptr(modelDefaultMaxTokens)
							}
							budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, anthropic.MinimumReasoningMaxTokens, defaultMaxTokens)
							if err != nil {
								return nil, err
							}
							bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
								"type":          "enabled",
								"budget_tokens": budgetTokens,
							})
						}
					} else {
						modelDefaultMaxTokens := providerUtils.GetMaxOutputTokensOrDefault(bifrostReq.Model, DefaultCompletionMaxTokens)
						defaultMaxTokens := modelDefaultMaxTokens
						if inferenceConfig.MaxTokens != nil {
							defaultMaxTokens = *inferenceConfig.MaxTokens
						} else {
							inferenceConfig.MaxTokens = schemas.Ptr(modelDefaultMaxTokens)
						}
						budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, MinimumReasoningMaxTokens, defaultMaxTokens)
						if err != nil {
							return nil, err
						}
						bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", map[string]any{
							"type":          "enabled",
							"budget_tokens": budgetTokens,
						})
					}
				} else {
					if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
						if !anthropic.IsFableFamily(capModel) {
							// Fable/Mythos reject thinking:{type:"disabled"}; omit it
							// entirely (adaptive thinking is always on for that family).
							bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
								"type": "disabled",
							})
						}
					} else if schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
						bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", map[string]any{
							"type": "disabled",
						})
					} else {
						bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", map[string]any{
							"type": "disabled",
						})
					}
				}
			}
		}
		if bifrostReq.Params.Text != nil {
			if bifrostReq.Params.Text.Format != nil {
				// Bedrock structured output goes through the synthetic `bf_so_*`
				// tool path for all models, including Anthropic. We capture the
				// tool here and defer injection until after normal tool/tool_choice
				// conversion so the forced structured-output tool choice is not
				// overwritten.
				responseFormatTool, _ := convertTextFormatToTool(ctx, bifrostReq.Model, bifrostReq.Params.Text)
				if responseFormatTool != nil {
					responsesStructuredOutputTool = responseFormatTool
				}
			}
		}
		if bifrostReq.Params.ExtraParams != nil {
			bedrockReq.ExtraParams = bifrostReq.Params.ExtraParams
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				delete(bedrockReq.ExtraParams, "stop")
				// GLM models on Bedrock reject the stopSequences field.
				if !schemas.IsGLMModel(capModel) {
					inferenceConfig.StopSequences = stop
				}
			}
			applyBedrockExtraParams(bedrockReq.ExtraParams, bedrockReq)
			if len(bedrockReq.ExtraParams) == 0 {
				bedrockReq.ExtraParams = nil
			}
		}

		bedrockReq.InferenceConfig = inferenceConfig

		if bifrostReq.Params.ServiceTier != nil {
			bedrockReq.ServiceTier = &BedrockServiceTier{
				Type: mapBifrostServiceTierToBedrock(*bifrostReq.Params.ServiceTier),
			}
		}
	}

	// Convert tools (using the provider-filtered keepTools set computed above).
	if len(keepTools) > 0 {
		var bedrockTools []BedrockTool
		isNova2 := schemas.IsNova2Model(capModel)
		for _, tool := range keepTools {
			if tool.Type == schemas.ResponsesToolTypeWebSearch || tool.Type == schemas.ResponsesToolTypeCodeInterpreter {
				if !isNova2 {
					// skip adding this tool
					continue
				}
				var systemToolName BedrockSystemToolType
				switch tool.Type {
				case schemas.ResponsesToolTypeWebSearch:
					systemToolName = BedrockSystemToolNovaGrounding
				case schemas.ResponsesToolTypeCodeInterpreter:
					systemToolName = BedrockSystemToolNovaCodeInterpreter
				}
				bedrockTools = append(bedrockTools, BedrockTool{
					SystemTool: &BedrockSystemTool{Name: systemToolName},
				})
				continue
			}
			if tool.ResponsesToolFunction != nil {
				// Create the complete schema object that Bedrock expects
				var schemaObject interface{}
				if tool.ResponsesToolFunction.Parameters != nil {
					schemaObject = tool.ResponsesToolFunction.Parameters
				} else {
					// Fallback to empty object schema if no parameters
					schemaObject = map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					}
				}

				if tool.Name == nil || *tool.Name == "" {
					return nil, fmt.Errorf("responses tool is missing required name for Bedrock function conversion")
				}
				name := *tool.Name
				toolSpecName := bedrockAliasToolName(ctx, name)

				// Use the tool description if available, otherwise use a generic description
				description := "Function tool"
				if tool.Description != nil {
					description = *tool.Description
				}

				schemaObjectBytes, err := providerUtils.MarshalSorted(schemaObject)
				if err != nil {
					return nil, fmt.Errorf("failed to serialize tool schema %q: %w", name, err)
				}
				bedrockTool := BedrockTool{
					ToolSpec: &BedrockToolSpec{
						Name:        toolSpecName,
						Description: &description,
						InputSchema: BedrockToolInputSchema{
							JSON: json.RawMessage(schemaObjectBytes),
						},
					},
				}
				bedrockTools = append(bedrockTools, bedrockTool)

				if tool.CacheControl != nil && !schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
					bedrockTools = append(bedrockTools, BedrockTool{
						CachePoint: newBedrockCachePoint(tool.CacheControl.TTL),
					})
				}
			}
		}

		if len(bedrockTools) > 0 {
			bedrockReq.ToolConfig = &BedrockToolConfig{
				Tools: bedrockTools,
			}
		}
	}

	// Convert tool choice
	if bifrostReq.Params != nil && bifrostReq.Params.ToolChoice != nil {
		bedrockToolChoice := convertResponsesToolChoice(*bifrostReq.Params.ToolChoice)
		if bedrockToolChoice != nil && bedrockToolChoice.Tool != nil && bedrockToolChoice.Tool.Name != "" {
			bedrockToolChoice.Tool.Name = bedrockAliasToolName(ctx, bedrockToolChoice.Tool.Name)
			// Reconcile the pinned tool against the converted (filtered) tool set.
			// Tools dropped by ValidateResponsesToolsForProvider above (e.g. an
			// unsupported `mcp` server tool) never reach bedrockReq.ToolConfig.Tools,
			// so a toolChoice.tool that names a dropped tool would make Bedrock
			// reject the request ("tool not found in toolConfig.tools"). Fall back
			// to Bedrock's default "auto" in that case. Mirrors the Chat path's
			// buildBedrockServerToolChoice reconciliation against its filtered set.
			pinPresent := false
			if bedrockReq.ToolConfig != nil {
				for _, t := range bedrockReq.ToolConfig.Tools {
					if t.ToolSpec != nil && t.ToolSpec.Name == bedrockToolChoice.Tool.Name {
						pinPresent = true
						break
					}
				}
			}
			if !pinPresent {
				bedrockToolChoice = nil
			}
		}
		// Per-model gate: Bedrock Converse rejects toolConfig.toolChoice.tool
		// on Meta Llama variants ("This model doesn't support the
		// toolConfig.toolChoice.tool field"). Drop the forced specific-tool
		// pin on Llama; the bound tool list is unaffected so the model can
		// still call the intended tool under Bedrock's default "auto"
		// behavior. See per-model support matrix at
		// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolChoice.html
		// (mirrors the gate in convertToolConfigFromFiltered for ChatCompletions).
		if bedrockToolChoice != nil && bedrockToolChoice.Tool != nil && schemas.IsLlamaModelFamily(ctx, bifrostReq.Model) {
			bedrockToolChoice = nil
		}
		// Only attach tool_choice when tools are actually present. Bedrock
		// Converse rejects a toolConfig that carries a toolChoice with an empty
		// tools list (e.g. the requested tools were all filtered/skipped, like
		// web_search on GLM) with "The provided request is not valid".
		if bedrockToolChoice != nil && bedrockReq.ToolConfig != nil && len(bedrockReq.ToolConfig.Tools) > 0 {
			bedrockReq.ToolConfig.ToolChoice = bedrockToolChoice
		}
	}

	// If text.format was converted to a synthetic tool, inject it after the normal
	// tool/tool_choice pass so it is not overwritten by the above conversion.
	if responsesStructuredOutputTool != nil {
		if bedrockReq.ToolConfig == nil {
			bedrockReq.ToolConfig = &BedrockToolConfig{}
		}
		bedrockReq.ToolConfig.Tools = append([]BedrockTool{*responsesStructuredOutputTool}, bedrockReq.ToolConfig.Tools...)
		// Force the model to use this specific tool, EXCEPT on Meta Llama where
		// Bedrock Converse rejects toolConfig.toolChoice.tool with HTTP 400
		// ("This model doesn't support the toolConfig.toolChoice.tool field").
		// With only the synthetic bf_so_* tool bound, omitting tool_choice
		// (Bedrock default = "auto") yields the same outcome on Llama because
		// there's exactly one tool the model can call. See the per-model
		// support matrix at
		// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolChoice.html
		// (mirrors the gate applied in convertChatParameters).
		thinkingEnabled := bifrostReq.Params.Reasoning != nil &&
			(bifrostReq.Params.Reasoning.MaxTokens != nil ||
				(bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none"))
		if !schemas.IsLlamaModelFamily(ctx, bifrostReq.Model) && !thinkingEnabled {
			bedrockReq.ToolConfig.ToolChoice = &BedrockToolChoice{
				Tool: &BedrockToolChoiceTool{
					Name: responsesStructuredOutputTool.ToolSpec.Name,
				},
			}
		}
	}

	// Ensure tool config is present when tool content exists (similar to Chat Completions)
	ensureResponsesToolConfigForConversation(ctx, bifrostReq, bedrockReq)

	if !schemas.BedrockModelSupportsCachePoints(capModel) {
		stripCachePointsFromBedrockRequest(bedrockReq)
	} else if !schemas.BedrockModelSupportsExtendedCacheTTL(capModel) {
		downgradeExtendedCacheTTLInBedrockRequest(bedrockReq)
	}

	return bedrockReq, nil
}

// ToBifrostResponsesResponse converts BedrockConverseResponse to BifrostResponsesResponse
func (response *BedrockConverseResponse) ToBifrostResponsesResponse(ctx *schemas.BifrostContext) (*schemas.BifrostResponsesResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("bedrock response is nil")
	}

	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr(uuid.New().String()),
		CreatedAt: int(time.Now().Unix()),
	}

	// Convert output message to Responses format using the new conversion method
	if response.Output != nil && response.Output.Message != nil {
		outputMessages := ConvertBedrockMessagesToBifrostMessages(ctx, []BedrockMessage{*response.Output.Message}, []BedrockSystemMessage{}, true)
		bifrostResp.Output = outputMessages
	}

	if response.Usage != nil {
		// Convert usage information
		bifrostResp.Usage = &schemas.ResponsesResponseUsage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}
		// Handle cached tokens if present
		if response.Usage.CacheReadInputTokens > 0 {
			if bifrostResp.Usage.InputTokensDetails == nil {
				bifrostResp.Usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
			}
			bifrostResp.Usage.InputTokensDetails.CachedReadTokens = response.Usage.CacheReadInputTokens
			bifrostResp.Usage.InputTokens = bifrostResp.Usage.InputTokens + response.Usage.CacheReadInputTokens
		}
		if response.Usage.CacheWriteInputTokens > 0 {
			if bifrostResp.Usage.InputTokensDetails == nil {
				bifrostResp.Usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
			}
			bifrostResp.Usage.InputTokensDetails.CachedWriteTokens = response.Usage.CacheWriteInputTokens
			if response.Usage.CacheDetails != nil {
				if bifrostResp.Usage.InputTokensDetails.CachedWriteTokenDetails == nil {
					bifrostResp.Usage.InputTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
				}
				for _, cacheDetail := range *response.Usage.CacheDetails {
					if cacheDetail.TTL == BedrockCacheWriteTTL5m {
						bifrostResp.Usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = cacheDetail.InputTokens
					}
					if cacheDetail.TTL == BedrockCacheWriteTTL1h {
						bifrostResp.Usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = cacheDetail.InputTokens
					}
				}
			}
			bifrostResp.Usage.InputTokens = bifrostResp.Usage.InputTokens + response.Usage.CacheWriteInputTokens
		}
	}

	if response.ServiceTier != nil && response.ServiceTier.Type != "" {
		tier := mapBedrockServiceTierToBifrost(response.ServiceTier.Type)
		bifrostResp.ServiceTier = &tier
	}

	if response.StopReason != "" {
		stopReason := convertBedrockStopReason(response.StopReason)
		if stopReason == string(schemas.BifrostFinishReasonToolCalls) {
			if toolName, hasSO := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); hasSO && toolName != "" {
				hasRealToolCall := false
				for _, msg := range bifrostResp.Output {
					if msg.Type == nil {
						continue
					}
					switch *msg.Type {
					case schemas.ResponsesMessageTypeFunctionCall,
						schemas.ResponsesMessageTypeWebSearchCall,
						schemas.ResponsesMessageTypeCodeInterpreterCall:
						hasRealToolCall = true
					}
					if hasRealToolCall {
						break
					}
				}
				if !hasRealToolCall {
					stopReason = string(schemas.BifrostFinishReasonStop)
				}
			}
		}
		bifrostResp.StopReason = &stopReason
		// Surface truncation via Status + IncompleteDetails per OpenAI's
		// Responses-API contract; without these, truncations are silent.
		switch stopReason {
		case string(schemas.BifrostFinishReasonLength):
			bifrostResp.Status = schemas.Ptr(schemas.ResponsesResponseStatusIncomplete)
			bifrostResp.IncompleteDetails = &schemas.ResponsesResponseIncompleteDetails{
				Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens,
			}
		case string(schemas.BifrostFinishReasonStop), string(schemas.BifrostFinishReasonToolCalls):
			if bifrostResp.Status == nil {
				bifrostResp.Status = schemas.Ptr(schemas.ResponsesResponseStatusCompleted)
			}
		}
	}

	if response.Trace != nil {
		bifrostResp.ProviderExtraFields = map[string]interface{}{
			"trace": response.Trace,
		}
	}

	return bifrostResp, nil
}

// ToBedrockConverseResponse converts Bifrost Responses response to Bedrock Converse response
func ToBedrockConverseResponse(bifrostResp *schemas.BifrostResponsesResponse) (*BedrockConverseResponse, error) {
	if bifrostResp == nil {
		return nil, fmt.Errorf("bifrost response is nil")
	}

	bedrockResp := &BedrockConverseResponse{
		Output:  &BedrockConverseOutput{},
		Usage:   &BedrockTokenUsage{},
		Metrics: &BedrockConverseMetrics{},
	}

	var hasToolUse bool
	message := &BedrockMessage{
		Role:    BedrockMessageRoleAssistant,
		Content: []BedrockContentBlock{},
	}

	if len(bifrostResp.Output) > 0 {
		// Convert Bifrost messages back to Bedrock messages using the new conversion method.
		// Response-side conversion does not perform outbound fetches in practice (model output
		// blocks already carry inline data), so context.Background() is acceptable here.
		// Response output never contains mid-conversation system reminders, so disable inlining.
		bedrockMessages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), bifrostResp.Output, false)
		if err != nil {
			return nil, fmt.Errorf("failed to convert bifrost output messages: %w", err)
		}

		// Merge all content blocks from converted messages into a single message
		for _, bedrockMsg := range bedrockMessages {
			message.Content = append(message.Content, bedrockMsg.Content...)
		}

		for _, msg := range bifrostResp.Output {
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall {
				hasToolUse = true
				break
			}
		}
	}

	bedrockResp.Output.Message = message

	// Derive stop reason: StopReason > IncompleteDetails > tool_use detection > end_turn
	stopReason := "end_turn"
	if bifrostResp.StopReason != nil {
		stopReason = convertBifrostToBedrockStopReason(*bifrostResp.StopReason)
	} else if bifrostResp.IncompleteDetails != nil {
		stopReason = convertIncompleteReasonToBedrockStopReason(bifrostResp.IncompleteDetails.Reason)
	} else if hasToolUse {
		stopReason = "tool_use"
	}
	bedrockResp.StopReason = stopReason

	// Convert usage stats
	if bedrockUsage := buildBedrockTokenUsage(bifrostResp.Usage); bedrockUsage != nil {
		bedrockResp.Usage = bedrockUsage
	}

	// Set metrics
	if bifrostResp.ExtraFields.Latency > 0 {
		bedrockResp.Metrics.LatencyMs = bifrostResp.ExtraFields.Latency
	}

	// Restore guardrail trace from provider extra fields
	if bifrostResp.ProviderExtraFields != nil {
		bedrockResp.Trace = extractBedrockTrace(bifrostResp.ProviderExtraFields["trace"])
	}

	return bedrockResp, nil
}

// Helper functions

// extractBedrockTrace recovers a *BedrockConverseTrace from ProviderExtraFields["trace"].
// It handles two cases:
//   - in-memory pointer (normal non-streaming path): direct type assertion
//   - map[string]interface{} (JSON-deserialized path, e.g. async job retrieval): marshal→unmarshal fallback
func extractBedrockTrace(v interface{}) *BedrockConverseTrace {
	if v == nil {
		return nil
	}
	if t, ok := v.(*BedrockConverseTrace); ok {
		return t
	}
	b, err := sonic.Marshal(v)
	if err != nil {
		return nil
	}
	var t BedrockConverseTrace
	if err := sonic.Unmarshal(b, &t); err != nil {
		return nil
	}
	return &t
}

// ensureResponsesToolConfigForConversation ensures toolConfig is present when tool content exists
func ensureResponsesToolConfigForConversation(ctx context.Context, bifrostReq *schemas.BifrostResponsesRequest, bedrockReq *BedrockConverseRequest) {
	if bedrockReq.ToolConfig != nil {
		return // Already has tool config
	}

	hasToolContent, tools := extractToolsFromResponsesConversationHistory(ctx, bifrostReq.Input, bifrostReq.Model)
	if hasToolContent && len(tools) > 0 {
		bedrockReq.ToolConfig = &BedrockToolConfig{Tools: tools}
	}
}

// extractToolsFromResponsesConversationHistory extracts tools from Responses conversation history
func extractToolsFromResponsesConversationHistory(ctx context.Context, messages []schemas.ResponsesMessage, model string) (bool, []BedrockTool) {
	var hasToolContent bool
	toolMap := make(map[string]*schemas.ResponsesTool) // Use map to deduplicate by name
	var hasNovaGrounding, hasNovaCodeInterpreter bool

	for _, msg := range messages {
		// Check if message contains tool use or tool result
		if msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesMessageTypeFunctionCallOutput:
				hasToolContent = true
				// Try to infer tool definition from tool call/result
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
					toolName := *msg.ResponsesToolMessage.Name
					if _, exists := toolMap[toolName]; !exists {
						// Create a minimal tool definition
						toolMap[toolName] = &schemas.ResponsesTool{
							Type: "function",
							Name: &toolName,
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type:       "object",
									Properties: schemas.NewOrderedMap(),
								},
							},
						}
					}
				}
			case schemas.ResponsesMessageTypeWebSearchCall:
				hasToolContent = true
				hasNovaGrounding = true
			case schemas.ResponsesMessageTypeCodeInterpreterCall:
				hasToolContent = true
				hasNovaCodeInterpreter = true
			}
		}
	}

	// Convert function tool map to BedrockTool slice
	var tools []BedrockTool
	for _, tool := range toolMap {
		if tool.Name != nil && tool.ResponsesToolFunction != nil {
			schemaObject := tool.ResponsesToolFunction.Parameters
			if schemaObject == nil {
				schemaObject = &schemas.ToolFunctionParameters{
					Type:       "object",
					Properties: schemas.NewOrderedMap(),
				}
			}

			description := "Function tool"
			if tool.Description != nil {
				description = *tool.Description
			}

			schemaObjectBytes2, _ := providerUtils.MarshalSorted(schemaObject)
			bedrockTool := BedrockTool{
				ToolSpec: &BedrockToolSpec{
					Name:        bedrockAliasToolName(ctx, *tool.Name),
					Description: &description,
					InputSchema: BedrockToolInputSchema{
						JSON: json.RawMessage(schemaObjectBytes2),
					},
				},
			}
			tools = append(tools, bedrockTool)
		}
	}

	// Append system tools found in history — only valid on Nova 2 models
	if schemas.IsNova2Model(model) {
		if hasNovaGrounding {
			tools = append(tools, BedrockTool{SystemTool: &BedrockSystemTool{Name: BedrockSystemToolNovaGrounding}})
		}
		if hasNovaCodeInterpreter {
			tools = append(tools, BedrockTool{SystemTool: &BedrockSystemTool{Name: BedrockSystemToolNovaCodeInterpreter}})
		}
	}

	return hasToolContent, tools
}

func convertResponsesToolChoice(toolChoice schemas.ResponsesToolChoice) *BedrockToolChoice {
	// Check if it's a string choice
	if toolChoice.ResponsesToolChoiceStr != nil {
		switch schemas.ResponsesToolChoiceType(*toolChoice.ResponsesToolChoiceStr) {
		case schemas.ResponsesToolChoiceTypeAuto:
			return &BedrockToolChoice{
				Auto: &BedrockToolChoiceAuto{},
			}
		case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ResponsesToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - just don't include tools
			return nil
		}
	}

	// Check if it's a struct choice
	if toolChoice.ResponsesToolChoiceStruct != nil {
		switch toolChoice.ResponsesToolChoiceStruct.Type {
		case schemas.ResponsesToolChoiceTypeFunction:
			// Extract the actual function name from the struct
			if toolChoice.ResponsesToolChoiceStruct.Name != nil && *toolChoice.ResponsesToolChoiceStruct.Name != "" {
				return &BedrockToolChoice{
					Tool: &BedrockToolChoiceTool{
						Name: *toolChoice.ResponsesToolChoiceStruct.Name,
					},
				}
			}
			// If Name is nil or empty, return nil as we can't construct a valid tool choice
			return nil
		case schemas.ResponsesToolChoiceTypeAuto:
			return &BedrockToolChoice{
				Auto: &BedrockToolChoiceAuto{},
			}
		case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ResponsesToolChoiceTypeNone:
			return nil
		}
	}

	return nil
}

// ToolCallState represents the state of a single tool call in the conversion process
type ToolCallState string

const (
	// Tool call states
	ToolCallStateInitialized    ToolCallState = "initialized"     // Tool call message received
	ToolCallStateQueued         ToolCallState = "queued"          // Tool call queued for emission
	ToolCallStateEmitted        ToolCallState = "emitted"         // Tool call emitted in assistant message
	ToolCallStateAwaitingResult ToolCallState = "awaiting_result" // Waiting for tool result
	ToolCallStateCompleted      ToolCallState = "completed"       // Tool call + result complete
)

// ToolCall represents a tool call with its full lifecycle state
type ToolCall struct {
	CallID            string
	ToolName          string
	Arguments         string
	State             ToolCallState
	AssistantMsgIndex int // Index in final bedrockMessages where this call was emitted
	Result            *ToolResult
	CacheControl      *schemas.CacheControl
}

// ToolResult represents the result of a tool call
type ToolResult struct {
	CallID       string
	Content      []BedrockContentBlock
	Status       string
	Emitted      bool
	CacheControl *schemas.CacheControl
}

// ToolCallBatch tracks a group of tool calls that should be emitted together
type ToolCallBatch struct {
	ID                string               // Unique batch identifier
	ToolCalls         map[string]*ToolCall // Maps CallID to ToolCall
	State             ToolCallState
	AssistantMsgIndex int // Where this batch's assistant message is in bedrockMessages
}

// ToolCallStateManager manages the lifecycle of tool calls through conversion
type ToolCallStateManager struct {
	// All tool calls indexed by ID
	toolCalls map[string]*ToolCall

	// Current batch being accumulated
	currentBatch *ToolCallBatch
	batches      []*ToolCallBatch

	// Pending operations
	pendingToolCallIDs []string               // Tool calls waiting to be emitted, in registration order
	pendingResults     map[string]*ToolResult // Results waiting to be matched
	pendingResultIDs   []string               // Insertion-order tracking for pendingResults
}

// NewToolCallStateManager creates a new state manager
func NewToolCallStateManager() *ToolCallStateManager {
	return &ToolCallStateManager{
		toolCalls:      make(map[string]*ToolCall),
		pendingResults: make(map[string]*ToolResult),
	}
}

// RegisterToolCall registers a new tool call in the system
func (m *ToolCallStateManager) RegisterToolCall(callID, toolName, arguments string, cacheControl *schemas.CacheControl) {
	if m.toolCalls[callID] != nil {
		// Tool call already registered, skip
		return
	}

	toolCall := &ToolCall{
		CallID:            callID,
		ToolName:          toolName,
		Arguments:         arguments,
		State:             ToolCallStateInitialized,
		AssistantMsgIndex: -1,
		CacheControl:      cacheControl,
	}

	m.toolCalls[callID] = toolCall
	m.pendingToolCallIDs = append(m.pendingToolCallIDs, callID)
}

// RegisterToolResult registers a tool result
func (m *ToolCallStateManager) RegisterToolResult(callID string, content []BedrockContentBlock, status string, cacheControl *schemas.CacheControl) {
	// Attemp to deduplicate the result similar to tool call. Need to check in 2 places, since after moving
	// on from pendingResults into a completed toolCall, the same ID might come again.
	if _, ok := m.pendingResults[callID]; ok {
		return
	}

	if toolCall, exists := m.toolCalls[callID]; exists && toolCall.Result != nil {
		// Tool result already processed for this call ID, skip
		return
	}

	result := &ToolResult{
		CallID:       callID,
		Content:      content,
		Status:       status,
		Emitted:      false,
		CacheControl: cacheControl,
	}

	m.pendingResults[callID] = result
	m.pendingResultIDs = append(m.pendingResultIDs, callID)

	// If we have the corresponding tool call, attach the result
	if toolCall, exists := m.toolCalls[callID]; exists {
		toolCall.Result = result
		if toolCall.State == ToolCallStateEmitted {
			toolCall.State = ToolCallStateCompleted
		} else if toolCall.State == ToolCallStateAwaitingResult {
			toolCall.State = ToolCallStateCompleted
		}
	}
}

// EmitPendingToolCalls prepares all pending tool calls for emission as an assistant message
func (m *ToolCallStateManager) EmitPendingToolCalls() []string {
	if len(m.pendingToolCallIDs) == 0 {
		return nil
	}

	// Create a new batch for these tool calls
	batchID := fmt.Sprintf("batch_%d", len(m.batches))
	batch := &ToolCallBatch{
		ID:        batchID,
		ToolCalls: make(map[string]*ToolCall),
		State:     ToolCallStateQueued,
	}

	// Mark all pending tool calls as queued
	for _, callID := range m.pendingToolCallIDs {
		if toolCall, exists := m.toolCalls[callID]; exists {
			toolCall.State = ToolCallStateQueued
			batch.ToolCalls[callID] = toolCall
		}
	}

	m.batches = append(m.batches, batch)
	m.currentBatch = batch

	// Return the IDs that should be emitted
	emitIDs := make([]string, len(m.pendingToolCallIDs))
	copy(emitIDs, m.pendingToolCallIDs)
	m.pendingToolCallIDs = nil

	return emitIDs
}

// MarkToolCallsEmitted marks tool calls as having been emitted in an assistant message
func (m *ToolCallStateManager) MarkToolCallsEmitted(callIDs []string, assistantMsgIndex int) {
	for _, callID := range callIDs {
		if toolCall, exists := m.toolCalls[callID]; exists {
			toolCall.State = ToolCallStateEmitted
			toolCall.AssistantMsgIndex = assistantMsgIndex
		}
	}

	if m.currentBatch != nil {
		m.currentBatch.State = ToolCallStateEmitted
		m.currentBatch.AssistantMsgIndex = assistantMsgIndex
	}
}

// GetPendingResults returns all pending results that are ready to be emitted.
// Deprecated: use GetPendingResultsOrdered to guarantee deterministic ordering.
func (m *ToolCallStateManager) GetPendingResults() map[string]*ToolResult {
	return m.pendingResults
}

// GetPendingResultsOrdered returns pending result IDs in registration order.
// Callers must look up each ID in the map returned by GetPendingResults.
// Use this instead of iterating GetPendingResults directly to avoid the
// non-deterministic map iteration that causes Bedrock to reject requests.
func (m *ToolCallStateManager) GetPendingResultsOrdered() []string {
	ids := make([]string, 0, len(m.pendingResultIDs))
	for _, id := range m.pendingResultIDs {
		if _, ok := m.pendingResults[id]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// MarkResultsEmitted marks results as having been emitted in a user message
func (m *ToolCallStateManager) MarkResultsEmitted(callIDs []string) {
	emitted := make(map[string]bool, len(callIDs))
	for _, callID := range callIDs {
		if result, exists := m.pendingResults[callID]; exists {
			result.Emitted = true
			delete(m.pendingResults, callID)
			emitted[callID] = true

			// Update tool call state
			if toolCall, exists := m.toolCalls[callID]; exists {
				toolCall.State = ToolCallStateCompleted
			}
		}
	}
	// Remove emitted IDs from the ordered tracking slice.
	filtered := m.pendingResultIDs[:0]
	for _, id := range m.pendingResultIDs {
		if !emitted[id] {
			filtered = append(filtered, id)
		}
	}
	m.pendingResultIDs = filtered
}

// HasPendingToolCalls checks if there are tool calls waiting to be emitted
func (m *ToolCallStateManager) HasPendingToolCalls() bool {
	return len(m.pendingToolCallIDs) > 0
}

// HasPendingResults checks if there are results waiting to be emitted
func (m *ToolCallStateManager) HasPendingResults() bool {
	return len(m.pendingResults) > 0
}

// ConvertBifrostMessagesToBedrockMessages converts an array of Bifrost ResponsesMessage to Bedrock message format
// This is the main conversion method from Bifrost to Bedrock - handles all message types and returns messages + system messages
// Uses a state machine to properly track and manage tool call lifecycles.
// The ctx is propagated to URL fetches inside content blocks. inlineSystemReminders selects the
// mid-conversation system-message handling: when true, only the leading run of system/developer
// messages is hoisted into the top-level `system` block and later (mid-conversation) ones are
// inlined in place; when false, every system/developer message is hoisted (historical behavior).
// Callers compute it from the provider+model — see the call site in ToBedrockResponsesRequest.
func ConvertBifrostMessagesToBedrockMessages(ctx context.Context, bifrostMessages []schemas.ResponsesMessage, inlineSystemReminders bool) ([]BedrockMessage, []BedrockSystemMessage, error) {
	// If only a single system message is present, convert it user message (since openai allows it)
	if len(bifrostMessages) == 1 && bifrostMessages[0].Role != nil && (*bifrostMessages[0].Role == schemas.ResponsesInputMessageRoleSystem || *bifrostMessages[0].Role == schemas.ResponsesInputMessageRoleDeveloper) {
		msg := bifrostMessages[0]
		msg.Role = schemas.Ptr(schemas.ResponsesInputMessageRoleUser)
		if bedrockMsg := convertBifrostMessageToBedrockMessage(ctx, &msg); bedrockMsg != nil {
			if len(bedrockMsg.Content) > 0 {
				return []BedrockMessage{*bedrockMsg}, nil, nil
			}
		}
	}

	// Bedrock's prompt cache is prefix-based, so growing the top-level `system` block invalidates
	// the cached conversation behind it. A mid-conversation role=system message (e.g. the reminders
	// Claude Code injects) hoisted into `system` collapses the cache to the tools/system floor every
	// time one appears. When inlineSystemReminders is set we instead keep only the leading run of
	// system/developer messages in `system` and inline later ones in place. This is the Bedrock
	// counterpart of the native Anthropic provider's mid-conversation system support
	// (SupportsMidConversationSystem) — Bedrock has no message-level system role, so the inlined
	// message is rendered as a user turn (see convertBifrostSystemReminderToBedrockUserMessage).
	// When false, every system/developer message is hoisted (historical behavior).

	var bedrockMessages []BedrockMessage
	var systemMessages []BedrockSystemMessage
	var pendingReasoningContentBlocks []BedrockContentBlock
	// pendingServerToolBlocks accumulates nova_grounding / nova_code_interpreter toolUse+toolResult
	// blocks that must be prepended to the next assistant text message (same-turn server-managed tools).
	var pendingServerToolBlocks []BedrockContentBlock

	// Set once the leading system prompt ends (first non-system message); gates inlineSystemReminders.
	seenNonSystemMessage := false

	// Initialize the state manager for tracking tool calls and results
	stateManager := NewToolCallStateManager()

	// Helper to flush pending tool result blocks into user messages using state manager
	flushPendingToolResults := func() {
		// Emit any pending results from the state manager
		if stateManager.HasPendingResults() {
			pendingResults := stateManager.GetPendingResults()
			orderedIDs := stateManager.GetPendingResultsOrdered()
			var resultBlocks []BedrockContentBlock
			for _, callID := range orderedIDs {
				result := pendingResults[callID]
				resultBlocks = append(resultBlocks, BedrockContentBlock{
					ToolResult: &BedrockToolResult{
						ToolUseID: callID,
						Content:   result.Content,
						Status:    schemas.Ptr(result.Status),
					},
				})
				if result.CacheControl != nil {
					resultBlocks = append(resultBlocks, BedrockContentBlock{
						CachePoint: newBedrockCachePoint(result.CacheControl.TTL),
					})
				}
			}

			if len(resultBlocks) > 0 {
				bedrockMessages = append(bedrockMessages, BedrockMessage{
					Role:    BedrockMessageRoleUser,
					Content: resultBlocks,
				})
				stateManager.MarkResultsEmitted(orderedIDs)
			}
		}
	}

	// Helper to flush pending tool call blocks into a single assistant message using state manager
	flushPendingToolCalls := func() {
		if stateManager.HasPendingToolCalls() {
			callIDs := stateManager.EmitPendingToolCalls()
			// Create assistant message with tool calls
			var contentBlocks []BedrockContentBlock

			// Prepend pending reasoning blocks first (Bedrock requires reasoning before tool_use)
			if len(pendingReasoningContentBlocks) > 0 {
				contentBlocks = append(contentBlocks, pendingReasoningContentBlocks...)
				pendingReasoningContentBlocks = nil
			}

			// Add tool use blocks
			for _, callID := range callIDs {
				if toolCall, exists := stateManager.toolCalls[callID]; exists {
					toolUseBlock := &BedrockContentBlock{
						ToolUse: &BedrockToolUse{
							ToolUseID: toolCall.CallID,
							Name:      toolCall.ToolName,
						},
					}
					// Preserve original key ordering of tool arguments for prompt caching.
					var input json.RawMessage
					var buf bytes.Buffer
					if err := json.Compact(&buf, []byte(toolCall.Arguments)); err == nil {
						input = buf.Bytes()
					} else {
						input = json.RawMessage("{}")
					}
					toolUseBlock.ToolUse.Input = input
					contentBlocks = append(contentBlocks, *toolUseBlock)
					if toolCall.CacheControl != nil {
						contentBlocks = append(contentBlocks, BedrockContentBlock{
							CachePoint: newBedrockCachePoint(toolCall.CacheControl.TTL),
						})
					}
				}
			}

			if len(contentBlocks) > 0 {
				bedrockMessages = append(bedrockMessages, BedrockMessage{
					Role:    BedrockMessageRoleAssistant,
					Content: contentBlocks,
				})
				stateManager.MarkToolCallsEmitted(callIDs, len(bedrockMessages)-1)
			}
		}
	}

	for i, msg := range bifrostMessages {
		// Handle nil Type as regular message
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}

		// First non-system message closes the leading system-prompt run (see seenNonSystemMessage).
		isSystemMessage := msgType == schemas.ResponsesMessageTypeMessage && msg.Role != nil &&
			(*msg.Role == schemas.ResponsesInputMessageRoleSystem || *msg.Role == schemas.ResponsesInputMessageRoleDeveloper)
		if !isSystemMessage {
			seenNonSystemMessage = true
		}

		// If we're processing a non-reasoning message and have pending reasoning blocks,
		// flush them into the previous assistant message (if it exists)
		if msgType != schemas.ResponsesMessageTypeReasoning && len(pendingReasoningContentBlocks) > 0 {
			if len(bedrockMessages) > 0 && bedrockMessages[len(bedrockMessages)-1].Role == BedrockMessageRoleAssistant {
				// Prepend reasoning blocks to the last assistant message
				lastMsg := &bedrockMessages[len(bedrockMessages)-1]
				lastMsg.Content = append(pendingReasoningContentBlocks, lastMsg.Content...)
				pendingReasoningContentBlocks = nil
			}
		}

		switch msgType {
		case schemas.ResponsesMessageTypeFunctionCall:
			// Register tool call in state manager
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
				toolName := ""
				if msg.ResponsesToolMessage.Name != nil {
					toolName = bedrockAliasToolName(ctx, *msg.ResponsesToolMessage.Name)
				}
				arguments := ""
				if msg.ResponsesToolMessage.Arguments != nil {
					arguments = *msg.ResponsesToolMessage.Arguments
				}

				stateManager.RegisterToolCall(*msg.ResponsesToolMessage.CallID, toolName, arguments, msg.CacheControl)
			}

		case schemas.ResponsesMessageTypeFunctionCallOutput:
			// Register tool result in state manager
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
				resultContent := []BedrockContentBlock{}
				status := "success"
				if msg.Status != nil && *msg.Status != "" {
					// Validate status is one of the allowed values
					switch *msg.Status {
					case "success", "error":
						status = *msg.Status
					default:
						// Default to success for unknown status values
						status = "success"
					}
				}

				// Convert result content to Bedrock format
				if msg.ResponsesToolMessage.Output != nil {
					if msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
						outputStr := *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
						if blocks, ok := decodeBedrockToolResultEnvelope(outputStr); ok {
							resultContent = append(resultContent, blocks...)
						} else {
							resultContent = append(resultContent, tryParseJSONIntoContentBlock(outputStr))
						}
					} else if msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
						// Handle structured output blocks
						for _, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
							if block.Text != nil {
								resultContent = append(resultContent, tryParseJSONIntoContentBlock(*block.Text))
							} else if block.Type == schemas.ResponsesInputMessageContentBlockTypeImage &&
								block.ResponsesInputMessageContentBlockImage != nil &&
								block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
								imageSource, err := convertImageToBedrockSource(ctx, *block.ResponsesInputMessageContentBlockImage.ImageURL)
								if err != nil {
									// Bedrock only supports base64 data URIs for images. If conversion
									// fails (e.g. remote URL), the image is dropped from the tool result
									// which silently degrades the model's ability to see tool output.
									_ = fmt.Errorf("bedrock: converting tool result image: %w", err)
								} else {
									resultContent = append(resultContent, BedrockContentBlock{Image: imageSource})
								}
							}
						}
					}
				}

				stateManager.RegisterToolResult(*msg.ResponsesToolMessage.CallID, resultContent, status, msg.CacheControl)
			}

			// Check if next message is not a function call output - if so, flush tool calls and results
			isLastResultInSequence := true
			if i+1 < len(bifrostMessages) {
				nextMsg := bifrostMessages[i+1]
				nextMsgType := schemas.ResponsesMessageTypeMessage
				if nextMsg.Type != nil {
					nextMsgType = *nextMsg.Type
				}
				if nextMsgType == schemas.ResponsesMessageTypeFunctionCallOutput {
					isLastResultInSequence = false
				}
			}

			// If this is the last result in a sequence, flush tool calls and results together
			if isLastResultInSequence {
				// Emit pending tool calls first
				if stateManager.HasPendingToolCalls() {
					callIDs := stateManager.EmitPendingToolCalls()
					var contentBlocks []BedrockContentBlock

					// Prepend pending reasoning blocks first (Bedrock requires reasoning before tool_use)
					if len(pendingReasoningContentBlocks) > 0 {
						contentBlocks = append(contentBlocks, pendingReasoningContentBlocks...)
						pendingReasoningContentBlocks = nil
					}

					// Add tool use blocks
					for _, callID := range callIDs {
						if toolCall, exists := stateManager.toolCalls[callID]; exists {
							toolUseBlock := &BedrockContentBlock{
								ToolUse: &BedrockToolUse{
									ToolUseID: toolCall.CallID,
									Name:      toolCall.ToolName,
								},
							}
							// Preserve original key ordering of tool arguments for prompt caching.
							var input json.RawMessage
							var buf bytes.Buffer
							if err := json.Compact(&buf, []byte(toolCall.Arguments)); err == nil {
								input = buf.Bytes()
							} else {
								input = json.RawMessage("{}")
							}
							toolUseBlock.ToolUse.Input = input
							contentBlocks = append(contentBlocks, *toolUseBlock)
							if toolCall.CacheControl != nil {
								contentBlocks = append(contentBlocks, BedrockContentBlock{
									CachePoint: newBedrockCachePoint(toolCall.CacheControl.TTL),
								})
							}
						}
					}

					if len(contentBlocks) > 0 {
						bedrockMessages = append(bedrockMessages, BedrockMessage{
							Role:    BedrockMessageRoleAssistant,
							Content: contentBlocks,
						})
						stateManager.MarkToolCallsEmitted(callIDs, len(bedrockMessages)-1)
					}
				}

				// Emit pending results after tool calls
				if stateManager.HasPendingResults() {
					pendingResults := stateManager.GetPendingResults()
					orderedIDs := stateManager.GetPendingResultsOrdered()
					var resultBlocks []BedrockContentBlock
					for _, callID := range orderedIDs {
						result := pendingResults[callID]
						resultBlocks = append(resultBlocks, BedrockContentBlock{
							ToolResult: &BedrockToolResult{
								ToolUseID: callID,
								Content:   result.Content,
								Status:    schemas.Ptr(result.Status),
							},
						})
						if result.CacheControl != nil {
							resultBlocks = append(resultBlocks, BedrockContentBlock{
								CachePoint: newBedrockCachePoint(result.CacheControl.TTL),
							})
						}
					}

					if len(resultBlocks) > 0 {
						bedrockMessages = append(bedrockMessages, BedrockMessage{
							Role:    BedrockMessageRoleUser,
							Content: resultBlocks,
						})
						stateManager.MarkResultsEmitted(orderedIDs)
					}
				}
			}

		case schemas.ResponsesMessageTypeMessage:
			// Check if Role is present, skip message if not
			if msg.Role == nil {
				continue
			}

			// Extract role from the Responses message structure
			role := *msg.Role

			// Always flush pending tool calls and results before processing a new message
			// This ensures tool calls and results are properly paired
			if stateManager.HasPendingToolCalls() {
				callIDs := stateManager.EmitPendingToolCalls()
				// Create assistant message with tool calls
				var toolUseBlocks []BedrockContentBlock
				for _, callID := range callIDs {
					if toolCall, exists := stateManager.toolCalls[callID]; exists {
						toolUseBlock := &BedrockContentBlock{
							ToolUse: &BedrockToolUse{
								ToolUseID: toolCall.CallID,
								Name:      toolCall.ToolName,
							},
						}
						// Preserve original key ordering of tool arguments for prompt caching.
						var input json.RawMessage
						var buf bytes.Buffer
						if err := json.Compact(&buf, []byte(toolCall.Arguments)); err == nil {
							input = buf.Bytes()
						} else {
							input = json.RawMessage("{}")
						}
						toolUseBlock.ToolUse.Input = input
						toolUseBlocks = append(toolUseBlocks, *toolUseBlock)
						// Preserve the cache breakpoint Claude Code placed on this tool call, else the
						// next turn can't match the prefix and collapses to the tools/system floor.
						if toolCall.CacheControl != nil {
							toolUseBlocks = append(toolUseBlocks, BedrockContentBlock{
								CachePoint: newBedrockCachePoint(toolCall.CacheControl.TTL),
							})
						}
					}
				}

				if len(toolUseBlocks) > 0 {
					bedrockMessages = append(bedrockMessages, BedrockMessage{
						Role:    BedrockMessageRoleAssistant,
						Content: toolUseBlocks,
					})
					stateManager.MarkToolCallsEmitted(callIDs, len(bedrockMessages)-1)
				}
			}

			// Emit any pending results after tool calls
			if stateManager.HasPendingResults() {
				pendingResults := stateManager.GetPendingResults()
				orderedIDs := stateManager.GetPendingResultsOrdered()
				var resultBlocks []BedrockContentBlock
				for _, callID := range orderedIDs {
					result := pendingResults[callID]
					resultBlocks = append(resultBlocks, BedrockContentBlock{
						ToolResult: &BedrockToolResult{
							ToolUseID: callID,
							Content:   result.Content,
							Status:    schemas.Ptr(result.Status),
						},
					})
					// Preserve the cache breakpoint Claude Code placed on this tool result.
					if result.CacheControl != nil {
						resultBlocks = append(resultBlocks, BedrockContentBlock{
							CachePoint: newBedrockCachePoint(result.CacheControl.TTL),
						})
					}
				}

				if len(resultBlocks) > 0 {
					bedrockMessages = append(bedrockMessages, BedrockMessage{
						Role:    BedrockMessageRoleUser,
						Content: resultBlocks,
					})
					stateManager.MarkResultsEmitted(orderedIDs)
				}
			}

			// Convert regular message
			if (role == schemas.ResponsesInputMessageRoleSystem || role == schemas.ResponsesInputMessageRoleDeveloper) &&
				(!inlineSystemReminders || !seenNonSystemMessage) {
				// Leading system prompt (or any system message for non-Anthropic models): hoist into `system`.
				systemMsgs := convertBifrostMessageToBedrockSystemMessages(&msg)
				systemMessages = append(systemMessages, systemMsgs...)
			} else if role == schemas.ResponsesInputMessageRoleSystem || role == schemas.ResponsesInputMessageRoleDeveloper {
				// Mid-conversation reminder: inline in place instead of hoisting (see inlineSystemReminders).
				bedrockMsg := convertBifrostSystemReminderToBedrockUserMessage(&msg)
				if bedrockMsg != nil {
					bedrockMessages = append(bedrockMessages, *bedrockMsg)
				}
			} else {
				// Convert user/assistant text message
				bedrockMsg := convertBifrostMessageToBedrockMessage(ctx, &msg)
				if bedrockMsg != nil {
					// Prepend buffered server-managed tool blocks (nova_grounding / nova_code_interpreter)
					// to the assistant message they belong to — they're part of the same turn.
					if bedrockMsg.Role == BedrockMessageRoleAssistant && len(pendingServerToolBlocks) > 0 {
						bedrockMsg.Content = append(pendingServerToolBlocks, bedrockMsg.Content...)
						pendingServerToolBlocks = nil
					}
					bedrockMessages = append(bedrockMessages, *bedrockMsg)
				}
			}

		case schemas.ResponsesMessageTypeReasoning:
			// Handle reasoning as content in next assistant message
			// For now, just add to pending content blocks
			reasoningBlocks := convertBifrostReasoningToBedrockReasoning(&msg)
			if len(reasoningBlocks) > 0 {
				pendingReasoningContentBlocks = append(pendingReasoningContentBlocks, reasoningBlocks...)
			}

		case schemas.ResponsesMessageTypeWebSearchCall:
			// Convert web_search_call → nova_grounding toolUse + toolResult.
			if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.CallID == nil {
				continue
			}
			callID := *msg.ResponsesToolMessage.CallID
			// Build toolUse input from the search query (matches original Bedrock format).
			inputMap := map[string]string{}
			if msg.ResponsesToolMessage.Action != nil &&
				msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
				action := msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
				if action.Query != nil {
					inputMap["query"] = *action.Query
				}
			}
			inputBytes, _ := json.Marshal(inputMap)
			toolUseBlock := BedrockContentBlock{
				ToolUse: &BedrockToolUse{
					ToolUseID: callID,
					Name:      string(BedrockSystemToolNovaGrounding),
					Input:     json.RawMessage(inputBytes),
					Type:      "server_tool_use",
				},
			}
			// Serialize sources as JSON for the toolResult content; preserve type and status.
			sourcesText := "[]"
			if msg.ResponsesToolMessage.Action != nil &&
				msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
				action := msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
				if len(action.Sources) > 0 {
					if b, err := json.Marshal(action.Sources); err == nil {
						sourcesText = string(b)
					}
				}
			}
			resultType := BedrockNovaGroundingResultType
			toolResultBlock := BedrockContentBlock{
				ToolResult: &BedrockToolResult{
					ToolUseID: callID,
					Type:      &resultType,
					Status:    schemas.Ptr("success"),
					Content:   []BedrockContentBlock{{Text: &sourcesText}},
				},
			}
			pendingServerToolBlocks = append(pendingServerToolBlocks, toolUseBlock, toolResultBlock)

		case schemas.ResponsesMessageTypeCodeInterpreterCall:
			// Convert code_interpreter_call → nova_code_interpreter toolUse + toolResult.
			// Both blocks are buffered and prepended to the next assistant message.
			if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall == nil {
				continue
			}
			ci := msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall
			toolUseID := ci.ContainerID
			if toolUseID == "" && msg.ID != nil {
				toolUseID = *msg.ID
			}
			code := ""
			if ci.Code != nil {
				code = *ci.Code
			}
			inputBytes, _ := json.Marshal(map[string]string{"snippet": code})
			toolUseBlock := BedrockContentBlock{
				ToolUse: &BedrockToolUse{
					ToolUseID: toolUseID,
					Name:      string(BedrockSystemToolNovaCodeInterpreter),
					Input:     json.RawMessage(inputBytes),
					Type:      "server_tool_use",
				},
			}
			// Build toolResult from outputs (stdout/stderr).
			var stdOut, stdErr string
			for _, output := range ci.Outputs {
				if output.ResponsesCodeInterpreterOutputLogs != nil {
					stdOut += output.ResponsesCodeInterpreterOutputLogs.Logs
				}
			}
			execResultBytes, _ := json.Marshal(struct {
				StdOut string `json:"stdOut"`
				StdErr string `json:"stdErr"`
			}{StdOut: stdOut, StdErr: stdErr})
			execResultStr := string(execResultBytes)
			resultType := BedrockNovaCodeInterpreterResultType
			toolResultBlock := BedrockContentBlock{
				ToolResult: &BedrockToolResult{
					ToolUseID: toolUseID,
					Type:      &resultType,
					Content:   []BedrockContentBlock{{Text: &execResultStr}},
				},
			}
			pendingServerToolBlocks = append(pendingServerToolBlocks, toolUseBlock, toolResultBlock)
		}
	}

	// Flush any remaining server-managed tool blocks (no following assistant message).
	if len(pendingServerToolBlocks) > 0 {
		bedrockMessages = append(bedrockMessages, BedrockMessage{
			Role:    BedrockMessageRoleAssistant,
			Content: pendingServerToolBlocks,
		})
		pendingServerToolBlocks = nil
	}

	// Flush any remaining pending tool calls
	flushPendingToolCalls()

	// Flush any remaining pending tool results
	flushPendingToolResults()

	// For Bedrock compatibility, reasoning blocks must not be the final block in an assistant message
	// If we have pending reasoning blocks and the last message is an assistant message,
	// merge them into a single message with reasoning first
	if len(pendingReasoningContentBlocks) > 0 {
		if len(bedrockMessages) > 0 && bedrockMessages[len(bedrockMessages)-1].Role == BedrockMessageRoleAssistant {
			// Last message is an assistant message - prepend reasoning blocks to it
			lastMsg := &bedrockMessages[len(bedrockMessages)-1]
			lastMsg.Content = append(pendingReasoningContentBlocks, lastMsg.Content...)
			pendingReasoningContentBlocks = nil
		}
		// If no assistant message to merge into, discard the reasoning blocks
		// (they cannot exist alone in Bedrock without violating the constraint)
	}

	// Merge consecutive messages with the same role
	// This ensures document blocks are in the same message as text blocks (Bedrock requirement)
	mergedMessages := []BedrockMessage{}
	for i := 0; i < len(bedrockMessages); i++ {
		currentMsg := bedrockMessages[i]

		// Merge any consecutive messages with the same role
		for i+1 < len(bedrockMessages) && bedrockMessages[i+1].Role == currentMsg.Role {
			i++
			currentMsg.Content = append(currentMsg.Content, bedrockMessages[i].Content...)
		}

		mergedMessages = append(mergedMessages, currentMsg)
	}
	bedrockMessages = mergedMessages

	return bedrockMessages, systemMessages, nil
}

// ConvertBedrockMessagesToBifrostMessages converts an array of Bedrock messages to Bifrost ResponsesMessage format
// This is the main conversion method from Bedrock to Bifrost - handles all message types and content blocks
func ConvertBedrockMessagesToBifrostMessages(ctx *schemas.BifrostContext, bedrockMessages []BedrockMessage, systemMessages []BedrockSystemMessage, isOutputMessage bool) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage

	// Convert system messages first
	systemBifrostMsgs := convertBedrockSystemMessageToBifrostMessages(systemMessages)
	if len(systemBifrostMsgs) > 0 {
		bifrostMessages = append(bifrostMessages, systemBifrostMsgs...)
	}

	// Convert regular messages
	for _, msg := range bedrockMessages {
		convertedMessages := convertSingleBedrockMessageToBifrostMessages(ctx, &msg, isOutputMessage)
		bifrostMessages = append(bifrostMessages, convertedMessages...)
	}

	return bifrostMessages
}

// Helper functions for converting individual Bedrock message types

// convertBifrostMessageToBedrockSystemMessages converts a Bifrost system message to Bedrock system messages
func convertBifrostMessageToBedrockSystemMessages(msg *schemas.ResponsesMessage) []BedrockSystemMessage {
	var systemMessages []BedrockSystemMessage

	if msg.Content != nil {
		if msg.Content.ContentStr != nil {
			systemMessages = append(systemMessages, BedrockSystemMessage{
				Text: msg.Content.ContentStr,
			})
		} else if msg.Content.ContentBlocks != nil {
			for _, block := range msg.Content.ContentBlocks {
				if block.Text != nil {
					systemMessages = append(systemMessages, BedrockSystemMessage{
						Text: block.Text,
					})
					if block.CacheControl != nil {
						systemMessages = append(systemMessages, BedrockSystemMessage{
							CachePoint: newBedrockCachePoint(block.CacheControl.TTL),
						})
					}
				}
			}
		}
	}

	return systemMessages
}

// convertBifrostSystemReminderToBedrockUserMessage renders a mid-conversation role=system reminder
// as a user message (Bedrock has no message-level system role), wrapping each text block in the
// same <system-reminder>\n...\n</system-reminder>\n envelope Claude Code uses for pre-wrapped ones.
// Returns nil for content that yields no text, so the caller skips the append.
func convertBifrostSystemReminderToBedrockUserMessage(msg *schemas.ResponsesMessage) *BedrockMessage {
	if msg.Content == nil {
		return nil
	}

	var contentBlocks []BedrockContentBlock
	wrap := func(text string) {
		wrapped := "<system-reminder>\n" + text + "\n</system-reminder>\n"
		contentBlocks = append(contentBlocks, BedrockContentBlock{Text: &wrapped})
	}

	// Text-only by design: reminders never carry images, and we deliberately attach no cache point
	// here — a breakpoint on this moving-tail message would shift every turn and defeat the prefix
	// caching this inlining exists for.
	if msg.Content.ContentStr != nil {
		wrap(*msg.Content.ContentStr)
	} else if msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.Text != nil {
				wrap(*block.Text)
			}
		}
	}

	if len(contentBlocks) == 0 {
		return nil
	}

	return &BedrockMessage{
		Role:    BedrockMessageRoleUser,
		Content: contentBlocks,
	}
}

// convertBifrostMessageToBedrockMessage converts a regular Bifrost message to Bedrock message.
// The ctx is propagated to URL fetches inside content blocks.
func convertBifrostMessageToBedrockMessage(ctx context.Context, msg *schemas.ResponsesMessage) *BedrockMessage {
	// Ensure Content is present
	if msg.Content == nil {
		return nil
	}

	bedrockMsg := BedrockMessage{
		Role: BedrockMessageRole(*msg.Role),
	}

	// Convert content
	contentBlocks, err := convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(ctx, *msg.Content)
	if err != nil {
		return nil
	}
	bedrockMsg.Content = contentBlocks

	return &bedrockMsg
}

// convertBedrockSystemMessageToBifrostMessages converts a Bedrock system message to Bifrost messages
func convertBedrockSystemMessageToBifrostMessages(systemMessages []BedrockSystemMessage) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage

	for _, sysMsg := range systemMessages {
		if sysMsg.CachePoint != nil {
			// add it to last content block of last message
			if len(bifrostMessages) > 0 {
				lastMessage := &bifrostMessages[len(bifrostMessages)-1]
				if lastMessage.Content != nil && len(lastMessage.Content.ContentBlocks) > 0 {
					lastMessage.Content.ContentBlocks[len(lastMessage.Content.ContentBlocks)-1].CacheControl = &schemas.CacheControl{
						Type: schemas.CacheControlTypeEphemeral,
						TTL:  sysMsg.CachePoint.TTL,
					}
				}
			}
		}
		if sysMsg.Text != nil {
			systemRole := schemas.ResponsesInputMessageRoleSystem
			msgType := schemas.ResponsesMessageTypeMessage
			bifrostMessages = append(bifrostMessages, schemas.ResponsesMessage{
				Type: &msgType,
				Role: &systemRole,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: sysMsg.Text,
						},
					},
				},
			})
		}

	}
	return bifrostMessages
}

// Helper to convert Bedrock role to Bifrost role
func convertBedrockRoleToBifrostRole(bedrockRole BedrockMessageRole) schemas.ResponsesMessageRoleType {
	switch bedrockRole {
	case BedrockMessageRoleUser:
		return schemas.ResponsesInputMessageRoleUser
	case BedrockMessageRoleAssistant:
		return schemas.ResponsesInputMessageRoleAssistant
	default:
		return schemas.ResponsesInputMessageRoleUser
	}
}

// Helper to create a text message
func createTextMessage(
	text *string,
	role schemas.ResponsesMessageRoleType,
	textBlockType schemas.ResponsesMessageContentBlockType,
	isOutputMessage bool,
) schemas.ResponsesMessage {
	contentBlock := schemas.ResponsesMessageContentBlock{
		Type: textBlockType,
		Text: text,
	}
	if textBlockType == schemas.ResponsesOutputMessageContentTypeText {
		contentBlock.ResponsesOutputMessageContentText = &schemas.ResponsesOutputMessageContentText{
			Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
			LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
		}
	}
	bifrostMsg := schemas.ResponsesMessage{
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Status: schemas.Ptr("completed"),
		Role:   &role,
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{contentBlock},
		},
	}
	if isOutputMessage {
		bifrostMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	return bifrostMsg
}

// convertSingleBedrockMessageToBifrostMessages converts a single Bedrock message to Bifrost messages
func convertSingleBedrockMessageToBifrostMessages(ctx *schemas.BifrostContext, msg *BedrockMessage, isOutputMessage bool) []schemas.ResponsesMessage {
	var outputMessages []schemas.ResponsesMessage
	var reasoningContentBlocks []schemas.ResponsesMessageContentBlock

	// Check if we have a structured output tool
	var structuredOutputToolName string
	if ctx != nil {
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}
	}

	// Pre-scan: build toolUseId → toolResult map for nova_code_interpreter_result blocks
	// so we can attach execution output when we encounter the matching toolUse block.
	novaCodeResults := make(map[string]*BedrockToolResult)
	for i := range msg.Content {
		r := msg.Content[i].ToolResult
		if r != nil && r.Type != nil && *r.Type == BedrockNovaCodeInterpreterResultType {
			novaCodeResults[r.ToolUseID] = r
		}
	}

	// Pre-scan: collect nova_grounding toolUseIDs and citation sources from citationsContent.
	// nova_grounding toolResults (paired with the toolUse) are skipped in the main loop;
	// citation URLs from text blocks are surfaced as sources on the web_search_call item.
	novaGroundingToolUseIDs := make(map[string]bool)
	var novaGroundingSources []schemas.ResponsesWebSearchToolCallActionSearchSource
	seenCitationURLs := make(map[string]bool)
	for i := range msg.Content {
		if msg.Content[i].ToolUse != nil && msg.Content[i].ToolUse.Name == string(BedrockSystemToolNovaGrounding) {
			novaGroundingToolUseIDs[msg.Content[i].ToolUse.ToolUseID] = true
		}
		if msg.Content[i].CitationsContent != nil {
			for _, citation := range msg.Content[i].CitationsContent.Citations {
				if citation.Location.Web != nil && !seenCitationURLs[citation.Location.Web.URL] {
					seenCitationURLs[citation.Location.Web.URL] = true
					domain := citation.Location.Web.Domain
					novaGroundingSources = append(novaGroundingSources, schemas.ResponsesWebSearchToolCallActionSearchSource{
						Type:  "url",
						URL:   citation.Location.Web.URL,
						Title: &domain,
					})
				}
			}
		}
	}

	// lastTextOutputIdx tracks the index into outputMessages of the most recently appended
	// text message, so standalone citationsContent blocks can be attached to it as annotations.
	lastTextOutputIdx := -1

	for _, block := range msg.Content {
		// Skip nova_code_interpreter_result tool results — they are consumed via novaCodeResults above.
		if block.ToolResult != nil && block.ToolResult.Type != nil && *block.ToolResult.Type == BedrockNovaCodeInterpreterResultType {
			continue
		}
		// Skip nova_grounding tool results — server-managed, consumed by the pre-scan above.
		if block.ToolResult != nil && novaGroundingToolUseIDs[block.ToolResult.ToolUseID] {
			continue
		}

		if block.Text != nil {
			// Text content
			role := convertBedrockRoleToBifrostRole(msg.Role)

			// For assistant messages (previous model outputs), use output_text type
			// For user/system messages, use input_text type
			textBlockType := schemas.ResponsesInputMessageContentBlockTypeText
			if isOutputMessage || msg.Role == BedrockMessageRoleAssistant {
				textBlockType = schemas.ResponsesOutputMessageContentTypeText
			}

			bifrostMsg := createTextMessage(block.Text, role, textBlockType, isOutputMessage)
			if isOutputMessage {
				bifrostMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
			}
			outputMessages = append(outputMessages, bifrostMsg)
			// Track this message so standalone citationsContent blocks can be attached to it.
			lastTextOutputIdx = len(outputMessages) - 1

		} else if block.CitationsContent != nil {
			// Standalone citationsContent block — attach citations as url_citation annotations
			// to the most recently created text message (interleaved in the Bedrock format).
			if lastTextOutputIdx >= 0 {
				lastMsg := &outputMessages[lastTextOutputIdx]
				if lastMsg.Content != nil && len(lastMsg.Content.ContentBlocks) > 0 {
					cb := &lastMsg.Content.ContentBlocks[0]
					if cb.ResponsesOutputMessageContentText == nil {
						cb.ResponsesOutputMessageContentText = &schemas.ResponsesOutputMessageContentText{
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						}
					}
					for _, citation := range block.CitationsContent.Citations {
						if citation.Location.Web == nil {
							continue
						}
						cb.ResponsesOutputMessageContentText.Annotations = append(
							cb.ResponsesOutputMessageContentText.Annotations,
							schemas.ResponsesOutputMessageContentTextAnnotation{
								Type:  "url_citation",
								URL:   schemas.Ptr(citation.Location.Web.URL),
								Title: schemas.Ptr(citation.Location.Web.Domain),
							},
						)
					}
				}
			}

		} else if block.ReasoningContent != nil {
			// Reasoning content - collect to create a single reasoning message
			if block.ReasoningContent.ReasoningText != nil {
				reasoningContentBlocks = append(reasoningContentBlocks, schemas.ResponsesMessageContentBlock{
					Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
					Text:      block.ReasoningContent.ReasoningText.Text,
					Signature: block.ReasoningContent.ReasoningText.Signature,
				})
			}
		} else if block.ToolUse != nil {
			// Tool use content
			// Create copies of the values to avoid range loop variable capture
			toolUseID := block.ToolUse.ToolUseID
			toolUseName := block.ToolUse.Name

			// Check if this is a structured output tool - if so, convert to text content
			if structuredOutputToolName != "" && toolUseName == structuredOutputToolName {
				// This is a structured output tool - convert to text message
				role := convertBedrockRoleToBifrostRole(msg.Role)

				// Marshal the tool input to JSON string
				var contentStr string
				if block.ToolUse.Input != nil {
					contentStr = string(block.ToolUse.Input)
				} else {
					contentStr = "{}"
				}

				bifrostMsg := createTextMessage(&contentStr, role, schemas.ResponsesOutputMessageContentTypeText, isOutputMessage)
				if isOutputMessage {
					bifrostMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
				}
				outputMessages = append(outputMessages, bifrostMsg)
			} else if toolUseName == "nova_code_interpreter" {
				// Nova code interpreter: build a code_interpreter_call message.
				// Bedrock returns the code under the "snippet" key in toolUse.input.
				var snippetInput []byte
				if block.ToolUse.Input != nil {
					snippetInput = block.ToolUse.Input
				}
				codeSnippet := providerUtils.GetJSONField(snippetInput, "snippet").String()

				// Build outputs from the paired toolResult (pre-scanned above).
				var ciOutputs []schemas.ResponsesCodeInterpreterOutput
				if result, ok := novaCodeResults[toolUseID]; ok {
					// Extract the JSON payload: {"stdOut":"...","stdErr":"...","exitCode":0,"isError":false}
					var execResult struct {
						StdOut string `json:"stdOut"`
						StdErr string `json:"stdErr"`
					}
					for _, c := range result.Content {
						if c.Text != nil {
							_ = json.Unmarshal([]byte(*c.Text), &execResult)
							break
						}
					}
					if execResult.StdOut != "" {
						ciOutputs = append(ciOutputs, schemas.ResponsesCodeInterpreterOutput{
							ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{
								Type: "logs",
								Logs: execResult.StdOut,
							},
						})
					}
					if execResult.StdErr != "" {
						ciOutputs = append(ciOutputs, schemas.ResponsesCodeInterpreterOutput{
							ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{
								Type: "logs",
								Logs: execResult.StdErr,
							},
						})
					}
				}
				if ciOutputs == nil {
					ciOutputs = []schemas.ResponsesCodeInterpreterOutput{}
				}

				ciMsg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
							Code:        &codeSnippet,
							ContainerID: toolUseID,
							Outputs:     ciOutputs,
						},
					},
				}
				if isOutputMessage {
					ciMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
					role := schemas.ResponsesInputMessageRoleAssistant
					ciMsg.Role = &role
				}
				outputMessages = append(outputMessages, ciMsg)

			} else if toolUseName == string(BedrockSystemToolNovaGrounding) {
				// nova_grounding → web_search_call with query from toolUse.input and citations from text blocks.
				wsAction := &schemas.ResponsesWebSearchToolCallAction{
					Type:    "search",
					Sources: novaGroundingSources,
				}
				if block.ToolUse.Input != nil {
					if q := providerUtils.GetJSONField(block.ToolUse.Input, "query").String(); q != "" {
						wsAction.Query = &q
						wsAction.Queries = []string{q}
					}
				}
				wsMsg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: &toolUseID,
						Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: wsAction,
						},
					},
				}
				if isOutputMessage {
					wsMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
					role := schemas.ResponsesInputMessageRoleAssistant
					wsMsg.Role = &role
				}
				outputMessages = append(outputMessages, wsMsg)
			} else {
				// Normal tool call message
				arguments := "{}"
				if block.ToolUse.Input != nil {
					arguments = string(block.ToolUse.Input)
				}
				restoredToolUseName := bedrockRestoreToolName(ctx, toolUseName)
				toolMsg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &toolUseID,
						Name:      &restoredToolUseName,
						Arguments: schemas.Ptr(arguments),
					},
				}
				if isOutputMessage {
					toolMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
					role := schemas.ResponsesInputMessageRoleAssistant
					toolMsg.Role = &role
				}
				outputMessages = append(outputMessages, toolMsg)
			}

		} else if block.Document != nil {
			// Document content
			role := convertBedrockRoleToBifrostRole(msg.Role)

			// Convert document to file block
			fileBlock := schemas.ResponsesMessageContentBlock{
				Type:                                  schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{},
			}

			// Set filename from document name
			if block.Document.Name != "" {
				fileBlock.ResponsesInputMessageContentBlockFile.Filename = &block.Document.Name
			}

			fileType := "application/pdf"
			// Set file type based on format
			if block.Document.Format != "" {
				switch block.Document.Format {
				case "pdf":
					fileType = "application/pdf"
				case "txt":
					fileType = "text/plain"
				case "md":
					fileType = "text/markdown"
				case "html":
					fileType = "text/html"
				case "csv":
					fileType = "text/csv"
				case "doc":
					fileType = "application/msword"
				case "docx":
					fileType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
				case "xls":
					fileType = "application/vnd.ms-excel"
				case "xlsx":
					fileType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
				default:
					fileType = "application/pdf" // Default to PDF
				}
				fileBlock.ResponsesInputMessageContentBlockFile.FileType = &fileType
			}

			// Convert document source data
			if block.Document.Source != nil {
				if block.Document.Source.Text != nil {
					// Plain text content
					fileBlock.ResponsesInputMessageContentBlockFile.FileData = block.Document.Source.Text
				} else if block.Document.Source.Bytes != nil {
					// Base64 encoded bytes (PDF)
					fileDataURL := *block.Document.Source.Bytes
					if !strings.HasPrefix(fileDataURL, "data:") {
						fileDataURL = fmt.Sprintf("data:%s;base64,%s", fileType, fileDataURL)
					}
					fileBlock.ResponsesInputMessageContentBlockFile.FileData = &fileDataURL
				}
			}

			bifrostMsg := schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: &role,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{fileBlock},
				},
			}
			if isOutputMessage {
				bifrostMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
			}
			outputMessages = append(outputMessages, bifrostMsg)

		} else if block.ToolResult != nil {
			// Tool result content - typically not in assistant output but handled for completeness
			// Prefer JSON payloads without unmarshalling; fallback to text.
			// If the content contains a searchResult (or any other block Bifrost's intermediate
			// can't model natively), serialize the full content array into a sentinel envelope
			// so it round-trips losslessly via ToBedrockResponsesRequest.
			var resultContent string
			hasUnrepresentableBlock := false
			for _, c := range block.ToolResult.Content {
				if c.SearchResult != nil || c.Video != nil {
					hasUnrepresentableBlock = true
					break
				}
			}
			if hasUnrepresentableBlock {
				if envelope, err := encodeBedrockToolResultEnvelope(block.ToolResult.Content); err == nil {
					resultContent = envelope
				}
			}
			if resultContent == "" && len(block.ToolResult.Content) > 0 {
				// JSON first (no unmarshal; just one marshal to string when present)
				for _, c := range block.ToolResult.Content {
					if c.JSON != nil {
						resultContent = string(c.JSON)
						break
					}
				}
				// Fallback to first available text block
				if resultContent == "" {
					for _, c := range block.ToolResult.Content {
						if c.Text != nil {
							resultContent = *c.Text
							break
						}
					}
				}
			}

			// Create a copy of the value to avoid range loop variable capture
			toolResultID := block.ToolResult.ToolUseID

			resultMsg := schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: &toolResultID,
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &resultContent,
					},
				},
			}
			if isOutputMessage {
				resultMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
				role := schemas.ResponsesInputMessageRoleAssistant
				resultMsg.Role = &role
				resultMsg.Content = &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: &resultContent,
						},
					},
				}
			}
			outputMessages = append(outputMessages, resultMsg)
		} else if block.CachePoint != nil {
			// Add cache control to last message
			if len(outputMessages) > 0 {
				lastMessage := &outputMessages[len(outputMessages)-1]
				// First try: set on last content block (for text/image messages)
				if lastMessage.Content != nil && len(lastMessage.Content.ContentBlocks) > 0 {
					lastMessage.Content.ContentBlocks[len(lastMessage.Content.ContentBlocks)-1].CacheControl = &schemas.CacheControl{
						Type: schemas.CacheControlTypeEphemeral,
						TTL:  block.CachePoint.TTL,
					}
				} else {
					// Fallback: set on message itself (for function_call/function_call_output)
					lastMessage.CacheControl = &schemas.CacheControl{
						Type: schemas.CacheControlTypeEphemeral,
						TTL:  block.CachePoint.TTL,
					}
				}
			}
		}
	}

	// Handle reasoning blocks - prepend reasoning message if we collected any
	if len(reasoningContentBlocks) > 0 {
		reasoningMessage := schemas.ResponsesMessage{
			ID:   schemas.Ptr("rs_" + fmt.Sprintf("%d", time.Now().UnixNano())),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			},
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: reasoningContentBlocks,
			},
		}
		// Prepend the reasoning message to the start of the messages list
		outputMessages = append([]schemas.ResponsesMessage{reasoningMessage}, outputMessages...)
	}

	return outputMessages
}

// convertBifrostReasoningToBedrockReasoning converts a Bifrost reasoning message to Bedrock reasoning blocks
func convertBifrostReasoningToBedrockReasoning(msg *schemas.ResponsesMessage) []BedrockContentBlock {
	var reasoningBlocks []BedrockContentBlock

	if msg.Content != nil && msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == schemas.ResponsesOutputMessageContentTypeReasoning && block.Text != nil {
				reasoningBlock := BedrockContentBlock{
					ReasoningContent: &BedrockReasoningContent{
						ReasoningText: &BedrockReasoningContentText{
							Text:      block.Text,
							Signature: reasoningSignatureForBedrock(block.Signature),
						},
					},
				}
				reasoningBlocks = append(reasoningBlocks, reasoningBlock)
			}
		}
	} else if msg.ResponsesReasoning != nil {
		if msg.ResponsesReasoning.Summary != nil {
			for _, reasoningContent := range msg.ResponsesReasoning.Summary {
				reasoningBlock := BedrockContentBlock{
					ReasoningContent: &BedrockReasoningContent{
						ReasoningText: &BedrockReasoningContentText{
							Text: &reasoningContent.Text,
						},
					},
				}
				reasoningBlocks = append(reasoningBlocks, reasoningBlock)
			}
		} else if msg.ResponsesReasoning.EncryptedContent != nil {
			// Bedrock doesn't have a direct equivalent to encrypted content,
			// so we'll store it as a regular reasoning block with a special marker
			encryptedText := fmt.Sprintf("[ENCRYPTED_REASONING: %s]", *msg.ResponsesReasoning.EncryptedContent)
			reasoningBlock := BedrockContentBlock{
				ReasoningContent: &BedrockReasoningContent{
					ReasoningText: &BedrockReasoningContentText{
						Text: &encryptedText,
					},
				},
			}
			reasoningBlocks = append(reasoningBlocks, reasoningBlock)
		}
	}

	return reasoningBlocks
}

// convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks converts Bifrost content to Bedrock content blocks.
// The ctx is propagated to URL fetches inside image blocks.
func convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(ctx context.Context, content schemas.ResponsesMessageContent) ([]BedrockContentBlock, error) {
	var blocks []BedrockContentBlock

	if content.ContentStr != nil {
		blocks = append(blocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		for _, block := range content.ContentBlocks {

			bedrockBlock := BedrockContentBlock{}
			switch block.Type {
			case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText:
				if block.Text == nil || *block.Text == "" {
					continue
				}
				bedrockBlock.Text = block.Text
			case schemas.ResponsesInputMessageContentBlockTypeImage:
				if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
					imageSource, err := convertImageToBedrockSource(ctx, *block.ResponsesInputMessageContentBlockImage.ImageURL)
					if err != nil {
						return nil, fmt.Errorf("failed to convert image in responses content block: %w", err)
					}
					bedrockBlock.Image = imageSource
				}
			case schemas.ResponsesOutputMessageContentTypeReasoning:
				if block.Text != nil {
					bedrockBlock.ReasoningContent = &BedrockReasoningContent{
						ReasoningText: &BedrockReasoningContentText{
							Text:      block.Text,
							Signature: reasoningSignatureForBedrock(block.Signature),
						},
					}
				}
			case schemas.ResponsesOutputMessageContentTypeCompaction:
				// Convert compaction to text block for Bedrock (compaction is Anthropic-specific)
				if block.ResponsesOutputMessageContentCompaction != nil {
					bedrockBlock.Text = &block.ResponsesOutputMessageContentCompaction.Summary
				}
			case schemas.ResponsesInputMessageContentBlockTypeFile:
				if block.ResponsesInputMessageContentBlockFile != nil {
					doc := &BedrockDocumentSource{
						Name:   "document", // Default
						Format: "pdf",      // Default
						Source: &BedrockDocumentSourceData{},
					}

					// Set filename (normalized for Bedrock)
					if block.ResponsesInputMessageContentBlockFile.Filename != nil {
						doc.Name = normalizeBedrockFilename(*block.ResponsesInputMessageContentBlockFile.Filename)
					}

					// Determine format: text or PDF based on FileType
					isTextFile := false
					if block.ResponsesInputMessageContentBlockFile.FileType != nil {
						fileType := *block.ResponsesInputMessageContentBlockFile.FileType
						// Check if it's a text type
						if fileType == "text/markdown" || fileType == "md" {
							doc.Format = "md"
							isTextFile = true
						} else if fileType == "text/html" || fileType == "html" {
							doc.Format = "html"
							isTextFile = true
						} else if fileType == "text/csv" || fileType == "csv" {
							doc.Format = "csv"
							isTextFile = true
						} else if strings.HasPrefix(fileType, "text/") || fileType == "txt" {
							doc.Format = "txt"
							isTextFile = true
						} else if strings.Contains(fileType, "pdf") || fileType == "pdf" {
							doc.Format = "pdf"
						} else if strings.Contains(fileType, "spreadsheetml") || fileType == "xlsx" {
							doc.Format = "xlsx"
						} else if fileType == "application/vnd.ms-excel" || fileType == "xls" {
							doc.Format = "xls"
						} else if strings.Contains(fileType, "wordprocessingml") || fileType == "docx" {
							doc.Format = "docx"
						} else if fileType == "application/msword" || fileType == "doc" {
							doc.Format = "doc"
						}
					}

					// Handle file data
					if block.ResponsesInputMessageContentBlockFile.FileData != nil {
						fileData := *block.ResponsesInputMessageContentBlockFile.FileData

						// Check if it's a data URL (e.g., "data:application/pdf;base64,...")
						if strings.HasPrefix(fileData, "data:") {
							urlInfo := schemas.ExtractURLTypeInfo(fileData)
							if urlInfo.DataURLWithoutPrefix != nil {
								// PDF or other binary - keep as base64
								doc.Source.Bytes = urlInfo.DataURLWithoutPrefix
								bedrockBlock.Document = doc
								break
							}
						}

						// Not a data URL - use as-is
						if isTextFile {
							// bytes is necessary for bedrock
							// base64 string of the text
							doc.Source.Text = &fileData
							encoded := base64.StdEncoding.EncodeToString([]byte(fileData))
							doc.Source.Bytes = &encoded
						} else {
							doc.Source.Bytes = &fileData
						}

						bedrockBlock.Document = doc

					}
				}
			default:
				// Don't add anything for unknown types
				continue
			}

			// Only append if at least one required field is set
			if bedrockBlock.Text != nil ||
				bedrockBlock.Image != nil ||
				bedrockBlock.Document != nil ||
				bedrockBlock.ToolUse != nil ||
				bedrockBlock.ToolResult != nil ||
				bedrockBlock.ReasoningContent != nil ||
				bedrockBlock.CachePoint != nil ||
				bedrockBlock.JSON != nil ||
				bedrockBlock.GuardContent != nil {
				blocks = append(blocks, bedrockBlock)
			}

			// For text blocks: emit a citationsContent block per url_citation annotation,
			// reconstructing the interleaved text+citation structure Bedrock uses.
			if bedrockBlock.Text != nil && block.ResponsesOutputMessageContentText != nil {
				for _, annotation := range block.ResponsesOutputMessageContentText.Annotations {
					if annotation.Type != "url_citation" || annotation.URL == nil {
						continue
					}
					domain := ""
					if annotation.Title != nil {
						domain = *annotation.Title
					}
					blocks = append(blocks, BedrockContentBlock{
						CitationsContent: &BedrockCitationsContent{
							Citations: []BedrockCitation{{
								Location: BedrockCitationLocation{
									Web: &BedrockWebCitationLocation{
										URL:    *annotation.URL,
										Domain: domain,
									},
								},
							}},
						},
					})
				}
			}

			if block.CacheControl != nil {
				blocks = append(blocks, BedrockContentBlock{
					CachePoint: newBedrockCachePoint(block.CacheControl.TTL),
				})
			}
		}
	}

	return blocks, nil
}
