package cohere

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// CohereResponsesStreamState tracks state during streaming conversion for responses API
type CohereResponsesStreamState struct {
	ContentIndexToOutputIndex     map[int]int    // Maps Cohere content_index to OpenAI output_index
	ToolArgumentBuffers           map[int]string // Maps output_index to accumulated tool argument JSON
	ToolCallNames                 map[int]string // Maps output_index to tool name
	ItemIDs                       map[int]string // Maps output_index to item ID for stable IDs
	ReasoningContentIndices       map[int]bool   // Tracks which content indices are reasoning blocks
	AnnotationIndexToContentIndex map[int]int    // Maps annotation index to content index for citation pairing
	CurrentOutputIndex            int            // Current output index counter
	MessageID                     *string        // Message ID from message_start
	Model                         *string        // Model name from message_start
	CreatedAt                     int            // Timestamp for created_at consistency
	HasEmittedCreated             bool           // Whether we've emitted response.created
	HasEmittedInProgress          bool           // Whether we've emitted response.in_progress
	ToolPlanOutputIndex           *int           // Output index for tool plan text item (if created)
}

// cohereResponsesStreamStatePool provides a pool for Cohere responses stream state objects.
var cohereResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &CohereResponsesStreamState{
			ContentIndexToOutputIndex:     make(map[int]int),
			ToolArgumentBuffers:           make(map[int]string),
			ToolCallNames:                 make(map[int]string),
			ItemIDs:                       make(map[int]string),
			ReasoningContentIndices:       make(map[int]bool),
			AnnotationIndexToContentIndex: make(map[int]int),
			CurrentOutputIndex:            0,
			CreatedAt:                     int(time.Now().Unix()),
			HasEmittedCreated:             false,
			HasEmittedInProgress:          false,
			ToolPlanOutputIndex:           nil,
		}
	},
}

// acquireCohereResponsesStreamState gets a Cohere responses stream state from the pool.
func acquireCohereResponsesStreamState() *CohereResponsesStreamState {
	state := cohereResponsesStreamStatePool.Get().(*CohereResponsesStreamState)
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
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ReasoningContentIndices == nil {
		state.ReasoningContentIndices = make(map[int]bool)
	} else {
		clear(state.ReasoningContentIndices)
	}
	if state.AnnotationIndexToContentIndex == nil {
		state.AnnotationIndexToContentIndex = make(map[int]int)
	} else {
		clear(state.AnnotationIndexToContentIndex)
	}
	// Reset other fields
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.ToolPlanOutputIndex = nil
	return state
}

// releaseCohereResponsesStreamState returns a Cohere responses stream state to the pool.
func releaseCohereResponsesStreamState(state *CohereResponsesStreamState) {
	if state != nil {
		state.flush() // Clean before returning to pool
		cohereResponsesStreamStatePool.Put(state)
	}
}

// flush resets the state of the stream state to its initial values
func (state *CohereResponsesStreamState) flush() {
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
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ReasoningContentIndices == nil {
		state.ReasoningContentIndices = make(map[int]bool)
	} else {
		clear(state.ReasoningContentIndices)
	}
	if state.AnnotationIndexToContentIndex == nil {
		state.AnnotationIndexToContentIndex = make(map[int]int)
	} else {
		clear(state.AnnotationIndexToContentIndex)
	}
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.ToolPlanOutputIndex = nil
}

// getOrCreateOutputIndex returns the output index for a given content index, creating a new one if needed
func (state *CohereResponsesStreamState) getOrCreateOutputIndex(contentIndex *int) int {
	if contentIndex == nil {
		// If no content index, create a new output index
		outputIndex := state.CurrentOutputIndex
		state.CurrentOutputIndex++
		return outputIndex
	}

	if outputIndex, exists := state.ContentIndexToOutputIndex[*contentIndex]; exists {
		return outputIndex
	}

	// Create new output index for this content index
	outputIndex := state.CurrentOutputIndex
	state.CurrentOutputIndex++
	state.ContentIndexToOutputIndex[*contentIndex] = outputIndex
	return outputIndex
}

// convertCohereContentBlockToBifrost converts CohereContentBlock to schemas.ContentBlock for Responses
func convertCohereContentBlockToBifrost(cohereBlock CohereContentBlock) schemas.ResponsesMessageContentBlock {
	switch cohereBlock.Type {
	case CohereContentBlockTypeText:
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesOutputMessageContentTypeText,
			Text: cohereBlock.Text,
			ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
				LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
				Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
			},
		}
	case CohereContentBlockTypeImage:
		// For images, create a text block describing the image (should never happen)
		if cohereBlock.ImageURL == nil {
			// Skip invalid image blocks without ImageURL
			return schemas.ResponsesMessageContentBlock{}
		}
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &cohereBlock.ImageURL.URL,
			},
		}
	case CohereContentBlockTypeThinking:
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesOutputMessageContentTypeReasoning,
			Text: cohereBlock.Thinking,
		}
	default:
		// Fallback to text block
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeText,
			Text: schemas.Ptr(string(cohereBlock.Type)),
			ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
				LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
				Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
			},
		}
	}
}

func (chunk *CohereStreamEvent) ToBifrostResponsesStream(sequenceNumber int, state *CohereResponsesStreamState) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError, bool) {
	switch chunk.Type {
	case StreamEventMessageStart:
		// Message start - emit response.created and response.in_progress (OpenAI-style lifecycle)
		if chunk.ID != nil {
			state.MessageID = chunk.ID
			// Use the state's CreatedAt for consistency
			if state.CreatedAt == 0 {
				state.CreatedAt = int(time.Now().Unix())
			}

			var responses []*schemas.BifrostResponsesStreamResponse

			// Emit response.created
			if !state.HasEmittedCreated {
				response := &schemas.BifrostResponsesResponse{
					ID:        state.MessageID,
					CreatedAt: state.CreatedAt,
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
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeInProgress,
					SequenceNumber: sequenceNumber + len(responses),
					Response:       response,
				})
				state.HasEmittedInProgress = true
			}

			if len(responses) > 0 {
				return responses, nil, false
			}
		}
	case StreamEventContentStart:
		// Content block start - emit output_item.added (OpenAI-style)
		// First, close tool plan message item if it's still open
		var responses []*schemas.BifrostResponsesStreamResponse
		if state.ToolPlanOutputIndex != nil {
			outputIndex := *state.ToolPlanOutputIndex
			itemID := state.ItemIDs[outputIndex]

			// Emit output_text.done (without accumulated text, just the event)
			emptyText := ""
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   schemas.Ptr(0),
				ItemID:         &itemID,
				Text:           &emptyText,
				LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
			})

			// Emit content_part.done
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
				ContentIndex:   schemas.Ptr(0),
				ItemID:         &itemID,
				Part:           part,
			})

			// Emit output_item.done
			statusCompleted := "completed"
			messageType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant
			doneItem := &schemas.ResponsesMessage{
				Type:   &messageType,
				Role:   &role,
				Status: &statusCompleted,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{},
				},
			}
			if itemID != "" {
				doneItem.ID = &itemID
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   schemas.Ptr(0),
				Item:           doneItem,
			})
			state.ToolPlanOutputIndex = nil // Mark as closed
		}

		if chunk.Delta != nil && chunk.Index != nil && chunk.Delta.Message != nil && chunk.Delta.Message.Content != nil && chunk.Delta.Message.Content.CohereStreamContentObject != nil {
			outputIndex := state.getOrCreateOutputIndex(chunk.Index)

			switch chunk.Delta.Message.Content.CohereStreamContentObject.Type {
			case CohereContentBlockTypeText:
				// Text block - emit output_item.added with type "message"
				messageType := schemas.ResponsesMessageTypeMessage
				role := schemas.ResponsesInputMessageRoleAssistant

				// Generate stable ID for text item
				var itemID string
				if state.MessageID == nil {
					itemID = fmt.Sprintf("item_%d", outputIndex)
				} else {
					itemID = fmt.Sprintf("msg_%s_item_%d", *state.MessageID, outputIndex)
				}
				state.ItemIDs[outputIndex] = itemID

				item := &schemas.ResponsesMessage{
					ID:   &itemID,
					Type: &messageType,
					Role: &role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{}, // Empty blocks slice for mutation support
					},
				}

				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
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
					ContentIndex:   chunk.Index,
					ItemID:         &itemID,
					Part:           part,
				})
				return responses, nil, false
			case CohereContentBlockTypeThinking:
				// Thinking/reasoning content - emit as reasoning item
				messageType := schemas.ResponsesMessageTypeReasoning
				role := schemas.ResponsesInputMessageRoleAssistant

				// Generate stable ID for reasoning item
				itemID := "rs_" + providerUtils.GetRandomString(50)
				state.ItemIDs[outputIndex] = itemID

				item := &schemas.ResponsesMessage{
					ID:   &itemID,
					Type: &messageType,
					Role: &role,
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningSummary{},
					},
				}

				// Track that this content index is a reasoning block
				if chunk.Index != nil {
					state.ReasoningContentIndices[*chunk.Index] = true
				}

				// Emit output_item.added
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				})

				// Emit content_part.added with empty reasoning_text part
				emptyText := ""
				part := &schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeReasoning,
					Text: &emptyText,
				}
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					ItemID:         &itemID,
					Part:           part,
				})

				return responses, nil, false
			}
		}
		if len(responses) > 0 {
			return responses, nil, false
		}
	case StreamEventContentDelta:
		if chunk.Index != nil && chunk.Delta != nil {
			outputIndex := state.getOrCreateOutputIndex(chunk.Index)

			// Handle text content delta
			if chunk.Delta.Message != nil && chunk.Delta.Message.Content != nil && chunk.Delta.Message.Content.CohereStreamContentObject != nil && chunk.Delta.Message.Content.CohereStreamContentObject.Text != nil && *chunk.Delta.Message.Content.CohereStreamContentObject.Text != "" {
				// Emit output_text.delta (not reasoning_summary_text.delta for regular text)
				itemID := state.ItemIDs[outputIndex]
				response := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Delta:          chunk.Delta.Message.Content.CohereStreamContentObject.Text,
					LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}

			// Handle thinking content delta
			if chunk.Delta.Message != nil && chunk.Delta.Message.Content != nil && chunk.Delta.Message.Content.CohereStreamContentObject != nil && chunk.Delta.Message.Content.CohereStreamContentObject.Thinking != nil && *chunk.Delta.Message.Content.CohereStreamContentObject.Thinking != "" {
				// Emit reasoning_summary_text.delta for thinking content
				itemID := state.ItemIDs[outputIndex]
				response := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Delta:          chunk.Delta.Message.Content.CohereStreamContentObject.Thinking,
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}
		}
		return nil, nil, false
	case StreamEventContentEnd:
		// Content block is complete - emit output_text.done, content_part.done, and output_item.done (OpenAI-style)
		if chunk.Index != nil {
			outputIndex := state.getOrCreateOutputIndex(chunk.Index)
			itemID := state.ItemIDs[outputIndex]
			var responses []*schemas.BifrostResponsesStreamResponse
			isReasoning := state.ReasoningContentIndices[*chunk.Index]

			// Check if this content index is a reasoning block
			if isReasoning {
				// Emit reasoning_summary_text.done (reasoning equivalent of output_text.done)
				emptyText := ""
				reasoningDoneResponse := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
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
					ContentIndex:   chunk.Index,
					Part:           part,
				}
				if itemID != "" {
					partDoneResponse.ItemID = &itemID
				}
				responses = append(responses, partDoneResponse)

				// Clear the reasoning content index tracking
				delete(state.ReasoningContentIndices, *chunk.Index)
			} else {
				// Regular text block - emit output_text.done (without accumulated text, just the event)
				emptyText := ""
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					ItemID:         &itemID,
					Text:           &emptyText,
					LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
				})

				// Emit content_part.done
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
					ContentIndex:   chunk.Index,
					ItemID:         &itemID,
					Part:           part,
				})
			}

			// Emit output_item.done for all content blocks (text, reasoning, etc.)
			statusCompleted := "completed"
			var doneItem *schemas.ResponsesMessage
			if isReasoning {
				messageType := schemas.ResponsesMessageTypeReasoning
				role := schemas.ResponsesInputMessageRoleAssistant
				doneItem = &schemas.ResponsesMessage{
					Type:   &messageType,
					Role:   &role,
					Status: &statusCompleted,
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningSummary{},
					},
				}
			} else {
				messageType := schemas.ResponsesMessageTypeMessage
				role := schemas.ResponsesInputMessageRoleAssistant
				doneItem = &schemas.ResponsesMessage{
					Type:   &messageType,
					Role:   &role,
					Status: &statusCompleted,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{},
					},
				}
			}
			if itemID != "" {
				doneItem.ID = &itemID
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   chunk.Index,
				Item:           doneItem,
			})
			return responses, nil, false
		}
	case StreamEventToolPlanDelta:
		if chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolPlan != nil && *chunk.Delta.Message.ToolPlan != "" {
			// Tool plan delta - treat as normal text (Option A)
			// Use output_index 0 for text message if it exists, otherwise create new
			outputIndex := 0
			var responses []*schemas.BifrostResponsesStreamResponse

			if state.ToolPlanOutputIndex != nil {
				outputIndex = *state.ToolPlanOutputIndex
			} else {
				// Create message item first if it doesn't exist
				outputIndex = 0
				state.ToolPlanOutputIndex = &outputIndex
				state.ContentIndexToOutputIndex[0] = outputIndex

				// Generate stable ID for text item
				// Generate stable ID for text item
				var itemID string
				if state.MessageID == nil {
					itemID = fmt.Sprintf("item_%d", outputIndex)
				} else {
					itemID = fmt.Sprintf("msg_%s_item_%d", *state.MessageID, outputIndex)
				}
				state.ItemIDs[outputIndex] = itemID

				messageType := schemas.ResponsesMessageTypeMessage
				role := schemas.ResponsesInputMessageRoleAssistant

				item := &schemas.ResponsesMessage{
					ID:   &itemID,
					Type: &messageType,
					Role: &role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{},
					},
				}

				// Emit output_item.added for text message
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   schemas.Ptr(0),
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
					ContentIndex:   schemas.Ptr(0),
					ItemID:         &itemID,
					Part:           part,
				})
			}

			// Emit output_text.delta (not reasoning_summary_text.delta)
			itemID := state.ItemIDs[outputIndex]
			response := &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   schemas.Ptr(0), // Tool plan is typically at index 0
				Delta:          chunk.Delta.Message.ToolPlan,
				LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
			}
			if itemID != "" {
				response.ItemID = &itemID
			}
			responses = append(responses, response)
			return responses, nil, false
		}
		return nil, nil, false
	case StreamEventToolCallStart:
		// First, close tool plan message item if it's still open
		var responses []*schemas.BifrostResponsesStreamResponse
		if state.ToolPlanOutputIndex != nil {
			outputIndex := *state.ToolPlanOutputIndex
			itemID := state.ItemIDs[outputIndex]

			// Emit output_text.done (without accumulated text, just the event)
			emptyText := ""
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   schemas.Ptr(0),
				ItemID:         &itemID,
				Text:           &emptyText,
				LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
			})

			// Emit content_part.done
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
				ContentIndex:   schemas.Ptr(0),
				ItemID:         &itemID,
				Part:           part,
			})

			// Emit output_item.done
			statusCompleted := "completed"
			messageType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant
			doneItem := &schemas.ResponsesMessage{
				Type:   &messageType,
				Role:   &role,
				Status: &statusCompleted,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{},
				},
			}
			if itemID != "" {
				doneItem.ID = &itemID
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   schemas.Ptr(0),
				Item:           doneItem,
			})
			state.ToolPlanOutputIndex = nil // Mark as closed
		}

		if chunk.Index != nil && chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolCalls != nil && chunk.Delta.Message.ToolCalls.CohereToolCallObject != nil {
			// Tool call start - emit output_item.added with type "function_call" and status "in_progress"
			toolCall := chunk.Delta.Message.ToolCalls.CohereToolCallObject
			if toolCall.Function != nil && toolCall.Function.Name != nil {
				// Always use a new output index for tool calls to avoid collision with text items
				// Use output_index 1 (or next available) to avoid collision with text at index 0
				outputIndex := state.CurrentOutputIndex
				if outputIndex == 0 {
					outputIndex = 1 // Skip 0 if it's used for text
				}
				state.CurrentOutputIndex = outputIndex + 1
				// Optionally map the content index if provided
				if chunk.Index != nil {
					state.ContentIndexToOutputIndex[*chunk.Index] = outputIndex
				}

				statusInProgress := "in_progress"
				itemID := ""
				if toolCall.ID != nil {
					itemID = *toolCall.ID
					state.ItemIDs[outputIndex] = itemID
				}
				if toolCall.Function.Name != nil {
					state.ToolCallNames[outputIndex] = *toolCall.Function.Name
				}

				item := &schemas.ResponsesMessage{
					ID:     toolCall.ID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status: &statusInProgress,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    toolCall.ID,
						Name:      toolCall.Function.Name,
						Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
					},
				}

				// Initialize argument buffer for this tool call
				state.ToolArgumentBuffers[outputIndex] = ""

				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					Item:           item,
				})
				return responses, nil, false
			}
		}
		if len(responses) > 0 {
			return responses, nil, false
		}
		return nil, nil, false
	case StreamEventToolCallDelta:
		if chunk.Index != nil && chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolCalls != nil && chunk.Delta.Message.ToolCalls.CohereToolCallObject != nil {
			// Tool call delta - handle function arguments streaming
			toolCall := chunk.Delta.Message.ToolCalls.CohereToolCallObject
			if toolCall.Function != nil {
				outputIndex := state.getOrCreateOutputIndex(chunk.Index)

				// Accumulate tool arguments in buffer
				if _, exists := state.ToolArgumentBuffers[outputIndex]; !exists {
					state.ToolArgumentBuffers[outputIndex] = ""
				}
				state.ToolArgumentBuffers[outputIndex] += toolCall.Function.Arguments

				// Emit function_call_arguments.delta
				itemID := state.ItemIDs[outputIndex]
				response := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
					SequenceNumber: sequenceNumber,
					ContentIndex:   chunk.Index,
					OutputIndex:    schemas.Ptr(outputIndex),
					Delta:          schemas.Ptr(toolCall.Function.Arguments),
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
			}
		}
		return nil, nil, false
	case StreamEventToolCallEnd:
		if chunk.Index != nil {
			// Tool call end - emit function_call_arguments.done then output_item.done
			outputIndex := state.getOrCreateOutputIndex(chunk.Index)
			var responses []*schemas.BifrostResponsesStreamResponse
			argsValue := ""

			// Emit function_call_arguments.done with full accumulated JSON
			if accumulatedArgs, hasArgs := state.ToolArgumentBuffers[outputIndex]; hasArgs && accumulatedArgs != "" {
				argsValue = accumulatedArgs
				itemID := state.ItemIDs[outputIndex]
				response := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Arguments:      &argsValue,
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				responses = append(responses, response)
				// Clear the buffer
				delete(state.ToolArgumentBuffers, outputIndex)
			}

			// Emit output_item.done for the function call
			statusCompleted := "completed"
			itemID := state.ItemIDs[outputIndex]
			callName, hasName := state.ToolCallNames[outputIndex]
			var callNamePtr *string
			if hasName && callName != "" {
				callNamePtr = &callName
			}
			doneItem := &schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: &statusCompleted,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &itemID,
					Name:      callNamePtr,
					Arguments: &argsValue,
				},
			}
			if itemID != "" {
				doneItem.ID = &itemID
			}
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber + len(responses),
				OutputIndex:    schemas.Ptr(outputIndex),
				ContentIndex:   chunk.Index,
				Item:           doneItem,
			})

			return responses, nil, false
		}
		return nil, nil, false
	case StreamEventCitationStart:
		if chunk.Index != nil && chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.Citations != nil {
			// Citation start - create annotation for the citation
			citation := chunk.Delta.Message.Citations.CohereStreamCitationObject

			// Map Cohere citation to ResponsesOutputMessageContentTextAnnotation
			annotation := &schemas.ResponsesOutputMessageContentTextAnnotation{
				Type:       "file_citation", // Default to file_citation
				StartIndex: schemas.Ptr(citation.Start),
				EndIndex:   schemas.Ptr(citation.End),
			}

			// Set annotation type and metadata
			if len(citation.Sources) > 0 {
				source := citation.Sources[0]

				if source.ID != nil {
					annotation.FileID = source.ID
				}

				if source.Document != nil {
					if title, ok := (*source.Document)["title"].(string); ok {
						annotation.Title = &title
					}
					if id, ok := (*source.Document)["id"].(string); ok && annotation.FileID == nil {
						annotation.FileID = &id
					}
					if snippet, ok := (*source.Document)["snippet"].(string); ok {
						annotation.Text = &snippet
					}
					if url, ok := (*source.Document)["url"].(string); ok {
						annotation.URL = &url
					}
				}
			}

			// Use output_index based on content index for citations (they're part of the text item)
			outputIndex := 0
			if citation.ContentIndex >= 0 {
				contentIndexPtr := &citation.ContentIndex
				outputIndex = state.getOrCreateOutputIndex(contentIndexPtr)
			}

			// Record mapping from annotation index to content index for citation pairing
			if chunk.Index != nil && citation.ContentIndex >= 0 {
				state.AnnotationIndexToContentIndex[*chunk.Index] = citation.ContentIndex
			}

			return []*schemas.BifrostResponsesStreamResponse{{
				Type:            schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
				SequenceNumber:  sequenceNumber,
				ContentIndex:    schemas.Ptr(citation.ContentIndex),
				Annotation:      annotation,
				OutputIndex:     schemas.Ptr(outputIndex),
				AnnotationIndex: chunk.Index,
			}}, nil, false
		}
		return nil, nil, false
	case StreamEventCitationEnd:
		if chunk.Index != nil {
			// Citation end - indicate annotation is complete
			// Look up the original content index from state using the annotation index
			contentIndex, exists := state.AnnotationIndexToContentIndex[*chunk.Index]
			if !exists {
				// Fallback: if mapping not found, use annotation index (shouldn't happen in normal flow)
				contentIndex = *chunk.Index
			}

			// Derive outputIndex from the content index
			contentIndexPtr := &contentIndex
			outputIndex := state.getOrCreateOutputIndex(contentIndexPtr)

			return []*schemas.BifrostResponsesStreamResponse{{
				Type:            schemas.ResponsesStreamResponseTypeOutputTextAnnotationDone,
				SequenceNumber:  sequenceNumber,
				ContentIndex:    &contentIndex,
				OutputIndex:     schemas.Ptr(outputIndex),
				AnnotationIndex: chunk.Index,
			}}, nil, false
		}
		return nil, nil, false
	case StreamEventMessageEnd:
		// Message end - emit response.completed (OpenAI-style)
		response := &schemas.BifrostResponsesResponse{
			CreatedAt: state.CreatedAt,
		}
		if state.MessageID != nil {
			response.ID = state.MessageID
		}
		if state.Model != nil {
			response.Model = *state.Model
		}

		if chunk.Delta != nil {
			if chunk.Delta.Usage != nil {
				usage := &schemas.ResponsesResponseUsage{}

				if chunk.Delta.Usage.Tokens != nil {
					if chunk.Delta.Usage.Tokens.InputTokens != nil {
						usage.InputTokens = *chunk.Delta.Usage.Tokens.InputTokens
					}
					if chunk.Delta.Usage.Tokens.OutputTokens != nil {
						usage.OutputTokens = *chunk.Delta.Usage.Tokens.OutputTokens
					}
					usage.TotalTokens = usage.InputTokens + usage.OutputTokens
				}

				if chunk.Delta.Usage.CachedTokens != nil {
					usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{
						CachedTokens: *chunk.Delta.Usage.CachedTokens,
					}
				}
				response.Usage = usage
			}
		}

		return []*schemas.BifrostResponsesStreamResponse{{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: sequenceNumber,
			Response:       response,
		}}, nil, true
	case StreamEventDebug:
		return nil, nil, false
	}
	return nil, nil, false
}

// ConvertResponsesTextFormatToCohere converts Bifrost Responses Text.Format to Cohere's typed format
// Responses format: Text.Format with type "json_schema", "json_object", or "text"
// Cohere format: { type: "json_object", json_schema: {...} }
func convertResponsesTextFormatToCohere(textFormat *schemas.ResponsesTextConfigFormat) *CohereResponseFormat {
	if textFormat == nil {
		return nil
	}

	cohereFormat := &CohereResponseFormat{}

	// Convert type
	switch textFormat.Type {
	case "text":
		cohereFormat.Type = ResponseFormatTypeText
	case "json_object":
		cohereFormat.Type = ResponseFormatTypeJSONObject
	case "json_schema":
		cohereFormat.Type = ResponseFormatTypeJSONObject

		// If schema is provided, extract it
		if textFormat.JSONSchema != nil {
			// Build schema map
			schema := make(map[string]interface{})
			if textFormat.JSONSchema.Type != nil {
				schema["type"] = *textFormat.JSONSchema.Type
			}
			if textFormat.JSONSchema.Properties != nil {
				schema["properties"] = *textFormat.JSONSchema.Properties
			}
			if len(textFormat.JSONSchema.Required) > 0 {
				schema["required"] = textFormat.JSONSchema.Required
			}
			if textFormat.JSONSchema.AdditionalProperties != nil {
				schema["additionalProperties"] = *textFormat.JSONSchema.AdditionalProperties
			}

			var schemaInterface interface{} = schema
			cohereFormat.JSONSchema = &schemaInterface
		}
	default:
		cohereFormat.Type = ResponseFormatTypeJSONObject
	}

	return cohereFormat
}

// ToCohereResponsesRequest converts a BifrostRequest (Responses structure) to CohereChatRequest
func ToCohereResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) (*CohereChatRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	cohereReq := &CohereChatRequest{
		Model: bifrostReq.Model,
	}

	// Map basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxOutputTokens != nil {
			cohereReq.MaxTokens = bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			cohereReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			cohereReq.P = bifrostReq.Params.TopP
		}

		// Convert reasoning
		if bifrostReq.Params.Reasoning != nil {
			if bifrostReq.Params.Reasoning.MaxTokens != nil {
				thinking := &CohereThinking{
					Type: ThinkingTypeEnabled,
				}
				if *bifrostReq.Params.Reasoning.MaxTokens == -1 {
					// cohere does not support dynamic reasoning budget like gemini
					// setting it to minimum reasoning budget
					thinking.TokenBudget = schemas.Ptr(anthropic.MinimumReasoningMaxTokens)
				} else {
					thinking.TokenBudget = bifrostReq.Params.Reasoning.MaxTokens
				}
				cohereReq.Thinking = thinking
			} else {
				if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
					maxOutputTokens := DefaultCompletionMaxTokens
					if bifrostReq.Params.MaxOutputTokens != nil {
						maxOutputTokens = *bifrostReq.Params.MaxOutputTokens
					}
					budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, MinimumReasoningMaxTokens, maxOutputTokens)
					if err != nil {
						return nil, err
					}
					cohereReq.Thinking = &CohereThinking{
						Type:        ThinkingTypeEnabled,
						TokenBudget: schemas.Ptr(budgetTokens),
					}
				} else {
					cohereReq.Thinking = &CohereThinking{
						Type: ThinkingTypeDisabled,
					}
				}
			}
		}

		if bifrostReq.Params.Text != nil && bifrostReq.Params.Text.Format != nil {
			cohereReq.ResponseFormat = convertResponsesTextFormatToCohere(bifrostReq.Params.Text.Format)
		}
		if bifrostReq.Params.ExtraParams != nil {
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				cohereReq.K = topK
			}
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				cohereReq.StopSequences = stop
			}
			if frequencyPenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["frequency_penalty"]); ok {
				cohereReq.FrequencyPenalty = frequencyPenalty
			}
			if presencePenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["presence_penalty"]); ok {
				cohereReq.PresencePenalty = presencePenalty
			}
			if thinkingParam, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "thinking"); ok {
				if thinkingMap, ok := thinkingParam.(map[string]interface{}); ok {
					thinking := &CohereThinking{}
					if typeStr, ok := schemas.SafeExtractString(thinkingMap["type"]); ok {
						thinking.Type = CohereThinkingType(typeStr)
					}
					if tokenBudget, ok := schemas.SafeExtractIntPointer(thinkingMap["token_budget"]); ok {
						thinking.TokenBudget = tokenBudget
					}
					cohereReq.Thinking = thinking
				}
			}
		}
	}

	// Convert tools
	if bifrostReq.Params != nil && bifrostReq.Params.Tools != nil {
		var cohereTools []CohereChatRequestTool
		for _, tool := range bifrostReq.Params.Tools {
			if tool.ResponsesToolFunction != nil && tool.Name != nil {
				cohereTool := CohereChatRequestTool{
					Type: "function",
					Function: CohereChatRequestFunction{
						Name:        *tool.Name,
						Description: tool.Description,
						Parameters:  tool.ResponsesToolFunction.Parameters,
					},
				}
				cohereTools = append(cohereTools, cohereTool)
			}
		}

		if len(cohereTools) > 0 {
			cohereReq.Tools = cohereTools
		}
	}

	// Convert tool choice
	if bifrostReq.Params != nil && bifrostReq.Params.ToolChoice != nil {
		cohereReq.ToolChoice = convertBifrostToolChoiceToCohereToolChoice(*bifrostReq.Params.ToolChoice)
	}

	// Process ResponsesInput (which contains the Responses items)
	if bifrostReq.Input != nil {
		cohereReq.Messages = ConvertBifrostMessagesToCohereMessages(bifrostReq.Input, bifrostReq.Params)
	}

	return cohereReq, nil
}

// ToBifrostResponsesResponse converts CohereChatResponse to BifrostResponse (Responses structure)
func (response *CohereChatResponse) ToBifrostResponsesResponse() *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr(response.ID),
		CreatedAt: int(time.Now().Unix()), // Set current timestamp
	}

	// Convert usage information
	if response.Usage != nil {
		usage := &schemas.ResponsesResponseUsage{}

		if response.Usage.Tokens != nil {
			if response.Usage.Tokens.InputTokens != nil {
				usage.InputTokens = *response.Usage.Tokens.InputTokens
			}
			if response.Usage.Tokens.OutputTokens != nil {
				usage.OutputTokens = *response.Usage.Tokens.OutputTokens
			}
			usage.TotalTokens = usage.InputTokens + usage.OutputTokens
		}

		if response.Usage.CachedTokens != nil {
			usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{
				CachedTokens: *response.Usage.CachedTokens,
			}
		}

		bifrostResp.Usage = usage
	}

	// Convert output message to Responses format
	if response.Message != nil {
		outputMessages := ConvertCohereMessagesToBifrostMessages([]CohereMessage{*response.Message}, true)
		bifrostResp.Output = outputMessages
	}

	return bifrostResp
}

// ConvertBifrostMessagesToCohereMessages converts an array of Bifrost ResponsesMessage to Cohere message format
// This is the main conversion method from Bifrost to Cohere - handles all message types and returns messages
func ConvertBifrostMessagesToCohereMessages(bifrostMessages []schemas.ResponsesMessage, params *schemas.ResponsesParameters) []CohereMessage {
	var cohereMessages []CohereMessage
	var systemContent []string
	var pendingReasoningContentBlocks []CohereContentBlock
	var currentAssistantMessage *CohereMessage

	for _, msg := range bifrostMessages {
		// Handle nil Type with default
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}

		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Handle nil Role with default
			role := "user"
			if msg.Role != nil {
				role = string(*msg.Role)
			}

			if role == "system" {
				// Collect system messages separately for Cohere
				systemMsgs := convertBifrostMessageToCohereSystemContent(&msg)
				systemContent = append(systemContent, systemMsgs...)
			} else {
				// Convert regular message
				cohereMsg := convertBifrostMessageToCohereMessage(&msg)
				if cohereMsg != nil {
					if role == "assistant" {
						// Add any pending reasoning content blocks to the message
						if len(pendingReasoningContentBlocks) > 0 {
							// copy the pending reasoning content blocks
							copied := make([]CohereContentBlock, len(pendingReasoningContentBlocks))
							copy(copied, pendingReasoningContentBlocks)
							contentBlocks := copied
							pendingReasoningContentBlocks = nil
							// Add content blocks after pending reasoning content blocks are added
							if msg.Content != nil {
								if msg.Content.ContentStr != nil {
									contentBlocks = append(contentBlocks, CohereContentBlock{
										Type: CohereContentBlockTypeText,
										Text: msg.Content.ContentStr,
									})
								} else if msg.Content.ContentBlocks != nil {
									contentBlocks = append(contentBlocks, convertResponsesMessageContentBlocksToCohere(msg.Content.ContentBlocks)...)
								}
							}
							cohereMsg.Content = NewBlocksContent(contentBlocks)
						}
						// Store assistant message for potential reasoning blocks
						currentAssistantMessage = cohereMsg
					} else {
						// Flush any pending assistant message first for non-assistant messages
						if currentAssistantMessage != nil {
							if len(pendingReasoningContentBlocks) > 0 {
								if currentAssistantMessage.Content == nil {
									currentAssistantMessage.Content = NewBlocksContent(pendingReasoningContentBlocks)
								} else if currentAssistantMessage.Content.BlocksContent != nil {
									currentAssistantMessage.Content.BlocksContent = append(currentAssistantMessage.Content.BlocksContent, pendingReasoningContentBlocks...)
								}
								pendingReasoningContentBlocks = nil
							}
							cohereMessages = append(cohereMessages, *currentAssistantMessage)
							currentAssistantMessage = nil
						}
						cohereMessages = append(cohereMessages, *cohereMsg)
					}
				}
			}

		case schemas.ResponsesMessageTypeReasoning:
			// Handle reasoning as thinking content blocks
			reasoningBlocks := convertBifrostReasoningToCohereThinking(&msg)
			if len(reasoningBlocks) > 0 {
				if currentAssistantMessage == nil {
					currentAssistantMessage = &CohereMessage{
						Role: "assistant",
					}
				}
				pendingReasoningContentBlocks = append(pendingReasoningContentBlocks, reasoningBlocks...)
			}

		case schemas.ResponsesMessageTypeFunctionCall:
			// Flush any pending reasoning blocks first
			if len(pendingReasoningContentBlocks) > 0 && currentAssistantMessage != nil {
				if currentAssistantMessage.Content == nil {
					currentAssistantMessage.Content = NewBlocksContent(pendingReasoningContentBlocks)
				} else if currentAssistantMessage.Content.BlocksContent != nil {
					currentAssistantMessage.Content.BlocksContent = append(currentAssistantMessage.Content.BlocksContent, pendingReasoningContentBlocks...)
				}
				cohereMessages = append(cohereMessages, *currentAssistantMessage)
				pendingReasoningContentBlocks = nil
				currentAssistantMessage = nil
			}

			// Handle function calls from Responses
			assistantMsg := convertBifrostFunctionCallToCohereMessage(&msg)
			if assistantMsg != nil {
				cohereMessages = append(cohereMessages, *assistantMsg)
			}

		case schemas.ResponsesMessageTypeFunctionCallOutput:
			// Handle function call outputs
			toolMsg := convertBifrostFunctionCallOutputToCohereMessage(&msg)
			if toolMsg != nil {
				cohereMessages = append(cohereMessages, *toolMsg)
			}
		}
	}

	// Flush any remaining pending reasoning blocks
	if len(pendingReasoningContentBlocks) > 0 && currentAssistantMessage != nil {
		if currentAssistantMessage.Content == nil {
			currentAssistantMessage.Content = NewBlocksContent(pendingReasoningContentBlocks)
		} else if currentAssistantMessage.Content.BlocksContent != nil {
			currentAssistantMessage.Content.BlocksContent = append(currentAssistantMessage.Content.BlocksContent, pendingReasoningContentBlocks...)
		}
		cohereMessages = append(cohereMessages, *currentAssistantMessage)
	} else if currentAssistantMessage != nil {
		cohereMessages = append(cohereMessages, *currentAssistantMessage)
	}

	// Prepend system messages if any
	if len(systemContent) > 0 {
		systemMsg := CohereMessage{
			Role:    "system",
			Content: NewStringContent(strings.Join(systemContent, "\n")),
		}
		cohereMessages = append([]CohereMessage{systemMsg}, cohereMessages...)
	} else if params != nil && params.Instructions != nil {
		// if no system messages, check if instructions are present
		systemMsg := CohereMessage{
			Role:    "system",
			Content: NewStringContent(*params.Instructions),
		}
		cohereMessages = append([]CohereMessage{systemMsg}, cohereMessages...)
	}

	return cohereMessages
}

// ConvertCohereMessagesToBifrostMessages converts an array of Cohere messages to Bifrost ResponsesMessage format
// This is the main conversion method from Cohere to Bifrost - handles all message types and content blocks
func ConvertCohereMessagesToBifrostMessages(cohereMessages []CohereMessage, isOutputMessage bool) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage

	for _, msg := range cohereMessages {
		convertedMessages := convertSingleCohereMessageToBifrostMessages(&msg, isOutputMessage)
		bifrostMessages = append(bifrostMessages, convertedMessages...)
	}

	return bifrostMessages
}

// convertBifrostToolChoiceToCohere converts schemas.ToolChoice to CohereToolChoice
func convertBifrostToolChoiceToCohereToolChoice(toolChoice schemas.ResponsesToolChoice) *CohereToolChoice {
	toolChoiceString := toolChoice.ResponsesToolChoiceStr

	if toolChoiceString != nil {
		switch *toolChoiceString {
		case "none":
			choice := ToolChoiceNone
			return &choice
		case "required", "function":
			choice := ToolChoiceRequired
			return &choice
		case "auto":
			choice := ToolChoiceAuto
			return &choice
		default:
			choice := ToolChoiceRequired
			return &choice
		}
	}

	return nil
}

// Helper functions for converting individual Cohere message types

// convertBifrostMessageToCohereSystemContent converts a Bifrost system message to Cohere system content
func convertBifrostMessageToCohereSystemContent(msg *schemas.ResponsesMessage) []string {
	var systemContent []string

	if msg.Content != nil {
		if msg.Content.ContentStr != nil {
			systemContent = append(systemContent, *msg.Content.ContentStr)
		} else if msg.Content.ContentBlocks != nil {
			for _, block := range msg.Content.ContentBlocks {
				if block.Text != nil {
					systemContent = append(systemContent, *block.Text)
				}
			}
		}
	}

	return systemContent
}

// convertBifrostMessageToCohereMessage converts a regular Bifrost message to Cohere message
func convertBifrostMessageToCohereMessage(msg *schemas.ResponsesMessage) *CohereMessage {
	role := "user"
	if msg.Role != nil {
		role = string(*msg.Role)
	}

	cohereMsg := CohereMessage{
		Role: role,
	}

	// Convert content - only if Content is not nil
	if msg.Content != nil {
		if msg.Content.ContentStr != nil {
			cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
		} else if msg.Content.ContentBlocks != nil {
			contentBlocks := convertResponsesMessageContentBlocksToCohere(msg.Content.ContentBlocks)
			cohereMsg.Content = NewBlocksContent(contentBlocks)
		}
	}

	return &cohereMsg
}

// convertBifrostReasoningToCohereThinking converts a Bifrost reasoning message to Cohere thinking blocks
func convertBifrostReasoningToCohereThinking(msg *schemas.ResponsesMessage) []CohereContentBlock {
	var thinkingBlocks []CohereContentBlock

	if msg.Content != nil && msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == schemas.ResponsesOutputMessageContentTypeReasoning && block.Text != nil {
				thinkingBlock := CohereContentBlock{
					Type:     CohereContentBlockTypeThinking,
					Thinking: block.Text,
				}
				thinkingBlocks = append(thinkingBlocks, thinkingBlock)
			}
		}
	} else if msg.ResponsesReasoning != nil {
		if msg.ResponsesReasoning.Summary != nil {
			for _, reasoningContent := range msg.ResponsesReasoning.Summary {
				thinkingBlock := CohereContentBlock{
					Type:     CohereContentBlockTypeThinking,
					Thinking: &reasoningContent.Text,
				}
				thinkingBlocks = append(thinkingBlocks, thinkingBlock)
			}
		} else if msg.ResponsesReasoning.EncryptedContent != nil {
			// Cohere doesn't have a direct equivalent to encrypted content,
			// so we'll store it as a regular thinking block with a special marker
			encryptedText := fmt.Sprintf("[ENCRYPTED_REASONING: %s]", *msg.ResponsesReasoning.EncryptedContent)
			thinkingBlock := CohereContentBlock{
				Type:     CohereContentBlockTypeThinking,
				Thinking: &encryptedText,
			}
			thinkingBlocks = append(thinkingBlocks, thinkingBlock)
		}
	}

	return thinkingBlocks
}

// convertBifrostFunctionCallToCohereMessage converts a Bifrost function call to Cohere message
func convertBifrostFunctionCallToCohereMessage(msg *schemas.ResponsesMessage) *CohereMessage {
	assistantMsg := CohereMessage{
		Role: "assistant",
	}

	// Extract function call details
	var cohereToolCalls []CohereToolCall
	toolCall := CohereToolCall{
		Type:     "function",
		Function: &CohereFunction{},
	}

	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
		toolCall.ID = msg.CallID
	}

	// Get function details from AssistantMessage
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Arguments != nil {
		toolCall.Function.Arguments = *msg.ResponsesToolMessage.Arguments
	}

	// Get name from ToolMessage if available
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
		toolCall.Function.Name = msg.ResponsesToolMessage.Name
	}

	cohereToolCalls = append(cohereToolCalls, toolCall)

	if len(cohereToolCalls) > 0 {
		assistantMsg.ToolCalls = cohereToolCalls
	}

	return &assistantMsg
}

// convertBifrostFunctionCallOutputToCohereMessage converts a Bifrost function call output to Cohere message
func convertBifrostFunctionCallOutputToCohereMessage(msg *schemas.ResponsesMessage) *CohereMessage {
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
		toolMsg := CohereMessage{
			Role: "tool",
		}

		// Extract content from ResponsesFunctionToolCallOutput if Content is not set
		// This is needed for OpenAI Responses API which uses an "output" field
		content := msg.Content
		if content == nil && msg.ResponsesToolMessage.Output != nil {
			content = &schemas.ResponsesMessageContent{}
			if msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
				content.ContentStr = msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
			} else if msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
				content.ContentBlocks = msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
			}
		}

		// Convert content - only if Content is not nil
		if content != nil {
			if content.ContentStr != nil {
				toolMsg.Content = NewStringContent(*content.ContentStr)
			} else if content.ContentBlocks != nil {
				contentBlocks := convertResponsesMessageContentBlocksToCohere(content.ContentBlocks)
				toolMsg.Content = NewBlocksContent(contentBlocks)
			}
		}

		toolMsg.ToolCallID = msg.ResponsesToolMessage.CallID

		return &toolMsg
	}
	return nil
}

// convertSingleCohereMessageToBifrostMessages converts a single Cohere message to Bifrost messages
func convertSingleCohereMessageToBifrostMessages(cohereMsg *CohereMessage, isOutputMessage bool) []schemas.ResponsesMessage {
	var outputMessages []schemas.ResponsesMessage
	var reasoningContentBlocks []schemas.ResponsesMessageContentBlock

	// Handle text content first
	if cohereMsg.Content != nil {
		var content schemas.ResponsesMessageContent
		var contentBlocks []schemas.ResponsesMessageContentBlock

		if cohereMsg.Content.StringContent != nil {
			// Determine content block type based on message role and output flag
			blockType := schemas.ResponsesInputMessageContentBlockTypeText
			if isOutputMessage || cohereMsg.Role == "assistant" {
				blockType = schemas.ResponsesOutputMessageContentTypeText
			}

			contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
				Type: blockType,
				Text: cohereMsg.Content.StringContent,
			})
		} else if cohereMsg.Content.BlocksContent != nil {
			// Convert content blocks and separate reasoning blocks
			for _, block := range cohereMsg.Content.BlocksContent {
				if block.Type == CohereContentBlockTypeThinking {
					// Collect reasoning blocks to create a single reasoning message
					reasoningContentBlocks = append(reasoningContentBlocks, schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesOutputMessageContentTypeReasoning,
						Text: block.Thinking,
					})
				} else {
					converted := convertCohereContentBlockToBifrost(block)
					if converted.Type != "" {
						contentBlocks = append(contentBlocks, converted)
					}
				}
			}
		}

		content.ContentBlocks = contentBlocks

		// Create message output if we have content blocks
		if len(contentBlocks) > 0 {
			var role schemas.ResponsesMessageRoleType
			switch cohereMsg.Role {
			case "user":
				role = schemas.ResponsesInputMessageRoleUser
			case "assistant":
				role = schemas.ResponsesInputMessageRoleAssistant
			case "system":
				role = schemas.ResponsesInputMessageRoleSystem
			default:
				role = schemas.ResponsesInputMessageRoleUser
			}

			outputMsg := schemas.ResponsesMessage{
				Role:    &role,
				Content: &content,
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			}

			if isOutputMessage {
				outputMsg.ID = schemas.Ptr("msg_" + fmt.Sprintf("%d", time.Now().UnixNano()))
				outputMsg.Status = schemas.Ptr("completed")
			}

			outputMessages = append(outputMessages, outputMsg)
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

	// Handle tool calls
	if cohereMsg.ToolCalls != nil {
		for _, toolCall := range cohereMsg.ToolCalls {
			// Check if Function is nil to avoid nil pointer dereference
			if toolCall.Function == nil {
				// Skip this tool call if Function is nil
				continue
			}

			// Safely extract function name and arguments
			var functionName *string
			var functionArguments *string

			if toolCall.Function.Name != nil {
				functionName = toolCall.Function.Name
			} else {
				// Use empty string if Name is nil
				functionName = schemas.Ptr("")
			}

			// Arguments is a string, not a pointer, so it's safe to access directly
			functionArguments = schemas.Ptr(toolCall.Function.Arguments)

			toolCallMsg := schemas.ResponsesMessage{
				ID:     toolCall.ID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      functionName,
					CallID:    toolCall.ID,
					Arguments: functionArguments,
				},
			}

			if isOutputMessage {
				role := schemas.ResponsesInputMessageRoleAssistant
				toolCallMsg.Role = &role
			}

			outputMessages = append(outputMessages, toolCallMsg)
		}
	}

	return outputMessages
}

// convertBifrostContentBlocksToCohere converts Bifrost content blocks to Cohere format
func convertResponsesMessageContentBlocksToCohere(blocks []schemas.ResponsesMessageContentBlock) []CohereContentBlock {
	var cohereBlocks []CohereContentBlock

	for _, block := range blocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText:
			// Handle both input_text (user messages) and output_text (assistant messages)
			if block.Text != nil {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: CohereContentBlockTypeText,
					Text: block.Text,
				})
			}
		case schemas.ResponsesInputMessageContentBlockTypeImage:
			if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil && *block.ResponsesInputMessageContentBlockImage.ImageURL != "" {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: CohereContentBlockTypeImage,
					ImageURL: &CohereImageURL{
						URL: *block.ResponsesInputMessageContentBlockImage.ImageURL,
					},
				})
			}
		case schemas.ResponsesOutputMessageContentTypeReasoning:
			if block.Text != nil {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type:     CohereContentBlockTypeThinking,
					Thinking: block.Text,
				})
			}
		}
	}

	return cohereBlocks
}
