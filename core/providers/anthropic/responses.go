package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// AnthropicResponsesStreamState tracks state during streaming conversion for responses API
type AnthropicResponsesStreamState struct {
	ChunkIndex      *int   // index of the chunk in the stream (reused for computer AND web search)
	AccumulatedJSON string // deltas of any event (reused for computer AND web search)

	// Computer tool accumulation
	ComputerToolID *string

	// Web search tool accumulation (minimal fields)
	WebSearchToolID      *string                // Tool ID of active web search
	WebSearchOutputIndex *int                   // Output index for this search
	WebSearchResult      *AnthropicContentBlock // Result block when it arrives

	// OpenAI Responses API mapping state
	ContentIndexToOutputIndex map[int]int                       // Maps Anthropic content_index to OpenAI output_index
	ContentIndexToBlockType   map[int]AnthropicContentBlockType // Tracks content block types
	ToolArgumentBuffers       map[int]string                    // Maps output_index to accumulated tool argument JSON
	MCPCallOutputIndices      map[int]bool                      // Tracks which output indices are MCP calls
	ItemIDs                   map[int]string                    // Maps output_index to item ID for stable IDs
	ReasoningSignatures       map[int]string                    // Maps output_index to reasoning signature
	TextContentIndices        map[int]bool                      // Tracks which content indices are text blocks
	ReasoningContentIndices   map[int]bool                      // Tracks which content indices are reasoning blocks
	CompactionContentIndices  map[int]*schemas.CacheControl     // Tracks pending compaction blocks with their cache control
	CurrentOutputIndex        int                               // Current output index counter
	MessageID                 *string                           // Message ID from message_start
	Model                     *string                           // Model name from message_start
	StopReason                *string                           // Stop reason for the message
	CreatedAt                 int                               // Timestamp for created_at consistency
	HasEmittedCreated         bool                              // Whether we've emitted response.created
	HasEmittedInProgress      bool                              // Whether we've emitted response.in_progress
	StructuredOutputToolName  string                            // Name of the structured output tool (if using tool-based SO for Vertex)
	StructuredOutputIndex     *int                              // Output index of the structured output tool call
}

// anthropicResponsesStreamStatePool provides a pool for Anthropic responses stream state objects.
var anthropicResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicResponsesStreamState{
			ContentIndexToOutputIndex: make(map[int]int),
			ToolArgumentBuffers:       make(map[int]string),
			MCPCallOutputIndices:      make(map[int]bool),
			ItemIDs:                   make(map[int]string),
			ReasoningSignatures:       make(map[int]string),
			TextContentIndices:        make(map[int]bool),
			ReasoningContentIndices:   make(map[int]bool),
			CompactionContentIndices:  make(map[int]*schemas.CacheControl),
			CurrentOutputIndex:        0,
			CreatedAt:                 int(time.Now().Unix()),
			HasEmittedCreated:         false,
			HasEmittedInProgress:      false,
		}
	},
}

// webSearchItemIDs tracks item IDs for WebSearch tools to skip their argument deltas
// Maps item_id (string) -> true for WebSearch tools that need delta skipping
var webSearchItemIDs sync.Map

// acquireAnthropicResponsesStreamState gets an Anthropic responses stream state from the pool.
func acquireAnthropicResponsesStreamState() *AnthropicResponsesStreamState {
	state := anthropicResponsesStreamStatePool.Get().(*AnthropicResponsesStreamState)
	// Clear maps (they're already initialized from New or previous flush)
	// Only initialize if nil (shouldn't happen, but defensive)
	if state.ContentIndexToOutputIndex == nil {
		state.ContentIndexToOutputIndex = make(map[int]int)
	} else {
		clear(state.ContentIndexToOutputIndex)
	}
	if state.ContentIndexToBlockType == nil {
		state.ContentIndexToBlockType = make(map[int]AnthropicContentBlockType)
	} else {
		clear(state.ContentIndexToBlockType)
	}
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[int]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	if state.MCPCallOutputIndices == nil {
		state.MCPCallOutputIndices = make(map[int]bool)
	} else {
		clear(state.MCPCallOutputIndices)
	}
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ReasoningSignatures == nil {
		state.ReasoningSignatures = make(map[int]string)
	} else {
		clear(state.ReasoningSignatures)
	}
	if state.TextContentIndices == nil {
		state.TextContentIndices = make(map[int]bool)
	} else {
		clear(state.TextContentIndices)
	}
	if state.ReasoningContentIndices == nil {
		state.ReasoningContentIndices = make(map[int]bool)
	} else {
		clear(state.ReasoningContentIndices)
	}
	if state.CompactionContentIndices == nil {
		state.CompactionContentIndices = make(map[int]*schemas.CacheControl)
	} else {
		clear(state.CompactionContentIndices)
	}
	// Reset other fields
	state.ChunkIndex = nil
	state.AccumulatedJSON = ""
	state.ComputerToolID = nil
	state.WebSearchToolID = nil
	state.WebSearchOutputIndex = nil
	state.WebSearchResult = nil
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.StopReason = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.StructuredOutputToolName = ""
	state.StructuredOutputIndex = nil
	return state
}

// releaseAnthropicResponsesStreamState returns an Anthropic responses stream state to the pool.
func releaseAnthropicResponsesStreamState(state *AnthropicResponsesStreamState) {
	if state != nil {
		state.flush() // Clean before returning to pool
		anthropicResponsesStreamStatePool.Put(state)
	}
}

// flush resets the state of the stream state to its initial values
func (state *AnthropicResponsesStreamState) flush() {
	state.ChunkIndex = nil
	state.AccumulatedJSON = ""
	state.ComputerToolID = nil
	state.WebSearchToolID = nil
	state.WebSearchOutputIndex = nil
	state.WebSearchResult = nil
	state.ContentIndexToOutputIndex = make(map[int]int)
	state.ContentIndexToBlockType = make(map[int]AnthropicContentBlockType)
	state.ToolArgumentBuffers = make(map[int]string)
	state.MCPCallOutputIndices = make(map[int]bool)
	state.ItemIDs = make(map[int]string)
	state.ReasoningSignatures = make(map[int]string)
	state.TextContentIndices = make(map[int]bool)
	state.ReasoningContentIndices = make(map[int]bool)
	state.CompactionContentIndices = make(map[int]*schemas.CacheControl)
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.StopReason = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.StructuredOutputToolName = ""
	state.StructuredOutputIndex = nil
}

// getOrCreateOutputIndex returns the output index for a given content index, creating a new one if needed
func (state *AnthropicResponsesStreamState) getOrCreateOutputIndex(contentIndex *int) int {
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

// ToBifrostResponsesStream converts an Anthropic stream event to a Bifrost Responses Stream response
// It maintains state via the state for handling multi-chunk conversions like computer tools
// Returns a slice of responses to support cases where a single event produces multiple responses
func (chunk *AnthropicStreamEvent) ToBifrostResponsesStream(ctx context.Context, sequenceNumber int, state *AnthropicResponsesStreamState) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError, bool) {
	switch chunk.Type {
	case AnthropicStreamEventTypeMessageStart:
		// Message start - emit response.created and response.in_progress (OpenAI-style lifecycle)
		if chunk.Message != nil {
			state.MessageID = &chunk.Message.ID
			state.Model = &chunk.Message.Model
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

	case AnthropicStreamEventTypeContentBlockStart:
		// Content block start - emit output_item.added (OpenAI-style)
		if chunk.ContentBlock != nil && chunk.Index != nil {
			outputIndex := state.getOrCreateOutputIndex(chunk.Index)

			if chunk.ContentBlock.Type == AnthropicContentBlockTypeToolUse &&
				chunk.ContentBlock.Name != nil &&
				*chunk.ContentBlock.Name == string(AnthropicToolNameComputer) &&
				chunk.ContentBlock.ID != nil {

				// Start accumulating computer tool
				state.ComputerToolID = chunk.ContentBlock.ID
				state.ChunkIndex = chunk.Index
				state.AccumulatedJSON = ""

				// Emit output_item.added for computer_call
				item := &schemas.ResponsesMessage{
					ID:   chunk.ContentBlock.ID,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeComputerCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: chunk.ContentBlock.ID,
					},
				}

				return []*schemas.BifrostResponsesStreamResponse{{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				}}, nil, false
			}

			// Handle web_search server_tool_use (query block)
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeServerToolUse &&
				chunk.ContentBlock.Name != nil &&
				*chunk.ContentBlock.Name == string(AnthropicToolNameWebSearch) &&
				chunk.ContentBlock.ID != nil {

				// Start accumulating web search query (reuse shared accumulation fields)
				state.ChunkIndex = chunk.Index
				state.AccumulatedJSON = ""
				state.WebSearchToolID = chunk.ContentBlock.ID
				// Store output index value (allocate new int to avoid pointer-to-local-variable issue)
				state.WebSearchOutputIndex = schemas.Ptr(outputIndex)

				// Store item ID
				state.ItemIDs[outputIndex] = *chunk.ContentBlock.ID

				// Emit output_item.added for web_search_call
				item := &schemas.ResponsesMessage{
					ID:     chunk.ContentBlock.ID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status: schemas.Ptr("in_progress"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: chunk.ContentBlock.ID,
						Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
								Type: "search",
							},
						},
					},
				}

				var responses []*schemas.BifrostResponsesStreamResponse

				// Emit output_item.added
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				})

				// Emit web_search_call.in_progress
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ItemID:         chunk.ContentBlock.ID,
				})

				// Emit web_search_call.searching
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ItemID:         chunk.ContentBlock.ID,
				})

				return responses, nil, false
			}

			// Handle web_search_tool_result block (results arrive)
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeWebSearchToolResult &&
				chunk.ContentBlock.ToolUseID != nil {

				// Track that this content index is a web search result block
				if chunk.Index != nil {
					state.ContentIndexToBlockType[*chunk.Index] = AnthropicContentBlockTypeWebSearchToolResult
				}

				// Check if this matches our active web search
				if state.WebSearchToolID != nil && *state.WebSearchToolID == *chunk.ContentBlock.ToolUseID {

					// Store the result block (arrives complete with all sources)
					state.WebSearchResult = chunk.ContentBlock

					if chunk.Index != nil {
						delete(state.ContentIndexToBlockType, *chunk.Index)
					}

					// Emit web_search_call.completed
					return []*schemas.BifrostResponsesStreamResponse{{
						Type:           schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
						SequenceNumber: sequenceNumber,
						OutputIndex:    state.WebSearchOutputIndex,
						ItemID:         chunk.ContentBlock.ToolUseID,
					}}, nil, false
				}

				// If no matching tool ID, skip (shouldn't happen in normal flow)
				return nil, nil, false
			}

			switch chunk.ContentBlock.Type {
			case AnthropicContentBlockTypeCompaction:
				// Compaction block - track it but don't emit yet (summary arrives in delta)
				itemID := fmt.Sprintf("cmp_%d", outputIndex)
				state.ItemIDs[outputIndex] = itemID

				// Store cache control for later use when delta arrives
				state.CompactionContentIndices[outputIndex] = chunk.ContentBlock.CacheControl

				// Track in ContentIndexToBlockType so content_block_stop skips generic done
				if chunk.Index != nil {
					state.ContentIndexToBlockType[*chunk.Index] = AnthropicContentBlockTypeCompaction
				}

				// Don't emit output_item.added yet - wait for the delta with actual summary
				return nil, nil, false
			case AnthropicContentBlockTypeText:
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
					ID:     schemas.Ptr(itemID),
					Status: schemas.Ptr("in_progress"),
					Type:   &messageType,
					Role:   &role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{}, // Empty blocks slice for mutation support
					},
				}

				// Track that this content index is a text block
				if chunk.Index != nil {
					state.TextContentIndices[*chunk.Index] = true
				}

				var responses []*schemas.BifrostResponsesStreamResponse

				// Emit output_item.added
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
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
			case AnthropicContentBlockTypeToolUse:
				// Check if this is the structured output tool - if so, skip emitting tool call
				if state.StructuredOutputToolName != "" && chunk.ContentBlock.Name != nil && *chunk.ContentBlock.Name == state.StructuredOutputToolName {
					// Mark this output index for structured output handling
					state.StructuredOutputIndex = &outputIndex

					// Initialize argument buffer for accumulating the JSON
					state.ToolArgumentBuffers[outputIndex] = ""

					// Mark tool use blocks to prevent synthetic content_part.added events
					if chunk.Index != nil {
						state.TextContentIndices[*chunk.Index] = false
					}

					// Store item ID for this structured output
					if chunk.ContentBlock.ID != nil {
						state.ItemIDs[outputIndex] = *chunk.ContentBlock.ID
					}

					return nil, nil, false
				}

				// Function call starting - emit output_item.added with type "function_call" and status "in_progress"
				statusInProgress := "in_progress"
				itemID := ""
				if chunk.ContentBlock.ID != nil {
					itemID = *chunk.ContentBlock.ID
					state.ItemIDs[outputIndex] = itemID
				}
				item := &schemas.ResponsesMessage{
					ID:     chunk.ContentBlock.ID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status: &statusInProgress,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    chunk.ContentBlock.ID,
						Name:      chunk.ContentBlock.Name,
						Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
					},
				}

				// Initialize argument buffer for this tool call
				state.ToolArgumentBuffers[outputIndex] = ""

				// Mark tool use blocks to prevent synthetic content_part.added events
				// This prevents extra content_block_stop events for tools like web_search
				if chunk.Index != nil {
					state.TextContentIndices[*chunk.Index] = false
				}

				return []*schemas.BifrostResponsesStreamResponse{{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				}}, nil, false
			case AnthropicContentBlockTypeMCPToolUse:
				// MCP tool call starting - emit output_item.added
				itemID := ""
				if chunk.ContentBlock.ID != nil {
					itemID = *chunk.ContentBlock.ID
					state.ItemIDs[outputIndex] = itemID
				}
				item := &schemas.ResponsesMessage{
					ID:   chunk.ContentBlock.ID,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Name:      chunk.ContentBlock.Name,
						Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
					},
				}

				// Set server name if present
				if chunk.ContentBlock.ServerName != nil {
					item.ResponsesToolMessage.ResponsesMCPToolCall = &schemas.ResponsesMCPToolCall{
						ServerLabel: *chunk.ContentBlock.ServerName,
					}
				}

				// Initialize argument buffer for this MCP call and mark as MCP
				state.ToolArgumentBuffers[outputIndex] = ""
				state.MCPCallOutputIndices[outputIndex] = true

				// Mark MCP tool use blocks to prevent synthetic content_part.added events
				if chunk.Index != nil {
					state.TextContentIndices[*chunk.Index] = false
				}

				return []*schemas.BifrostResponsesStreamResponse{{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				}}, nil, false
			case AnthropicContentBlockTypeThinking:
				// Thinking/reasoning block - emit output_item.added with type "reasoning"
				messageType := schemas.ResponsesMessageTypeReasoning
				role := schemas.ResponsesInputMessageRoleAssistant

				// Generate stable ID for reasoning item
				var itemID string
				if state.MessageID == nil {
					itemID = fmt.Sprintf("reasoning_%d", outputIndex)
				} else {
					itemID = fmt.Sprintf("msg_%s_reasoning_%d", *state.MessageID, outputIndex)
				}
				state.ItemIDs[outputIndex] = itemID

				// Initialize reasoning structure
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

				var responses []*schemas.BifrostResponsesStreamResponse

				// Emit output_item.added
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
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
				// Preserve signature in the content part if present
				if chunk.ContentBlock.Signature != nil {
					part.Signature = chunk.ContentBlock.Signature
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
			default:
				// Send down an empty response only when integration type is anthropic
				if ctx.Value(schemas.BifrostContextKeyIntegrationType) == "anthropic" {
					return []*schemas.BifrostResponsesStreamResponse{{
						Type:           "",
						SequenceNumber: sequenceNumber,
					}}, nil, false
				}
				return nil, nil, false
			}
		}

	case AnthropicStreamEventTypeContentBlockDelta:
		if chunk.Index != nil && chunk.Delta != nil {
			outputIndex := state.getOrCreateOutputIndex(chunk.Index)

			// Handle different delta types
			switch chunk.Delta.Type {
			case AnthropicStreamDeltaTypeCompaction:
				if chunk.Delta.Content != nil {
					// Compaction summary arrives - emit both output_item.added and output_item.done
					itemID := state.ItemIDs[outputIndex]
					messageType := schemas.ResponsesMessageTypeMessage
					role := schemas.ResponsesInputMessageRoleAssistant

					// Retrieve cache control stored from content_block_start
					cacheControl := state.CompactionContentIndices[outputIndex]

					item := &schemas.ResponsesMessage{
						ID:     &itemID,
						Status: schemas.Ptr("completed"),
						Type:   &messageType,
						Role:   &role,
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeCompaction,
									ResponsesOutputMessageContentCompaction: &schemas.ResponsesOutputMessageContentCompaction{
										Summary: *chunk.Delta.Content,
									},
									CacheControl: cacheControl,
								},
							},
						},
					}

					// Emit both output_item.added (with summary) and output_item.done
					return []*schemas.BifrostResponsesStreamResponse{
						{
							Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
							SequenceNumber: sequenceNumber,
							OutputIndex:    schemas.Ptr(outputIndex),
							ContentIndex:   chunk.Index,
							Item:           item,
						},
						{
							Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
							SequenceNumber: sequenceNumber + 1,
							OutputIndex:    schemas.Ptr(outputIndex),
							ItemID:         schemas.Ptr(itemID),
							Item:           item,
						},
					}, nil, false
				}
			case AnthropicStreamDeltaTypeText:
				if chunk.Delta.Text != nil && *chunk.Delta.Text != "" {
					// Text content delta - emit output_text.delta with item ID
					itemID := state.ItemIDs[outputIndex]
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Delta:          chunk.Delta.Text,
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
				}

			case AnthropicStreamDeltaTypeInputJSON:
				// Function call arguments delta
				if chunk.Delta.PartialJSON != nil {
					// Check if we're accumulating any tool (computer or web search)
					// Both use the shared ChunkIndex and AccumulatedJSON fields
					if state.ChunkIndex != nil && *state.ChunkIndex == *chunk.Index {
						// Accumulate the JSON and don't emit anything
						state.AccumulatedJSON += *chunk.Delta.PartialJSON
						return nil, nil, false
					}

					// Accumulate tool arguments in buffer
					if _, exists := state.ToolArgumentBuffers[outputIndex]; !exists {
						state.ToolArgumentBuffers[outputIndex] = ""
					}
					state.ToolArgumentBuffers[outputIndex] += *chunk.Delta.PartialJSON

					// Check if this is the structured output tool - if so, just accumulate without emitting
					if state.StructuredOutputIndex != nil && *state.StructuredOutputIndex == outputIndex {
						// This is the structured output tool - accumulate without emitting delta events
						return nil, nil, false
					}

					// Emit appropriate delta type based on whether this is an MCP call
					var deltaType schemas.ResponsesStreamResponseType
					if state.MCPCallOutputIndices[outputIndex] {
						deltaType = schemas.ResponsesStreamResponseTypeMCPCallArgumentsDelta
					} else {
						deltaType = schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta
					}

					itemID := state.ItemIDs[outputIndex]
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           deltaType,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Delta:          chunk.Delta.PartialJSON,
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
				}

			case AnthropicStreamDeltaTypeThinking:
				// Reasoning/thinking content delta
				if chunk.Delta.Thinking != nil && *chunk.Delta.Thinking != "" {
					itemID := state.ItemIDs[outputIndex]
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Delta:          chunk.Delta.Thinking,
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
				}

			case AnthropicStreamDeltaTypeSignature:
				// Handle signature verification for thinking content
				// Store the signature in state for the reasoning item
				if chunk.Delta.Signature != nil && *chunk.Delta.Signature != "" {
					state.ReasoningSignatures[outputIndex] = *chunk.Delta.Signature
					// Emit signature_delta event using the signature field
					itemID := state.ItemIDs[outputIndex]
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Signature:      chunk.Delta.Signature, // Use signature field instead of delta
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
				}
				return nil, nil, false

			case AnthropicStreamDeltaTypeCitations:
				// Handle citations delta - convert Anthropic citation to OpenAI annotation
				if chunk.Delta.Citation != nil {
					// For streaming, we don't compute indices yet (pass empty string)
					annotation := convertAnthropicCitationToAnnotation(*chunk.Delta.Citation, "")

					// Emit output_text.annotation.added event
					itemID := state.ItemIDs[outputIndex]
					response := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Annotation:     &annotation,
					}
					if itemID != "" {
						response.ItemID = &itemID
					}
					return []*schemas.BifrostResponsesStreamResponse{response}, nil, false
				}
				return nil, nil, false
			}
		}

	case AnthropicStreamEventTypeContentBlockStop:
		// Content block is complete - emit output_item.done (OpenAI-style)
		if chunk.Index != nil {
			outputIndex := state.getOrCreateOutputIndex(chunk.Index)

			// Check if this is the end of a tool accumulation (computer or web search query)
			if state.ChunkIndex != nil && *state.ChunkIndex == *chunk.Index {

				// Computer tool completion
				if state.ComputerToolID != nil {
					// Parse accumulated JSON and convert to OpenAI format
					var inputMap map[string]interface{}
					var action *schemas.ResponsesComputerToolCallAction

					if state.AccumulatedJSON != "" {
						if err := json.Unmarshal([]byte(state.AccumulatedJSON), &inputMap); err == nil {
							action = convertAnthropicToResponsesComputerAction(inputMap)
						}
					}

					// Create computer_call item with action
					statusCompleted := "completed"
					item := &schemas.ResponsesMessage{
						ID:     state.ComputerToolID,
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeComputerCall),
						Status: &statusCompleted,
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: state.ComputerToolID,
							ResponsesComputerToolCall: &schemas.ResponsesComputerToolCall{
								PendingSafetyChecks: []schemas.ResponsesComputerToolCallPendingSafetyCheck{},
							},
						},
					}

					// Add action if we successfully parsed it
					if action != nil {
						item.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
							ResponsesComputerToolCallAction: action,
						}
					}

					// Clear computer tool state
					state.ComputerToolID = nil
					state.ChunkIndex = nil
					state.AccumulatedJSON = ""

					// Return output_item.done
					return []*schemas.BifrostResponsesStreamResponse{
						{
							Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
							SequenceNumber: sequenceNumber,
							OutputIndex:    schemas.Ptr(outputIndex),
							ContentIndex:   chunk.Index,
							Item:           item,
						},
					}, nil, false
				}

				// Web search query block ended (don't emit output_item.done yet - wait for result)
				if state.WebSearchToolID != nil {
					// Clear ChunkIndex (done accumulating query)
					// Keep WebSearchToolID, WebSearchOutputIndex, and AccumulatedJSON (need them for final item)
					state.ChunkIndex = nil
					return nil, nil, false
				}
			}

			// Check if this is the end of a web_search_tool_result block
			if state.WebSearchResult != nil && state.WebSearchToolID != nil {

				// Parse the query from AccumulatedJSON
				var query string
				var queries []string
				if state.AccumulatedJSON != "" {
					var inputMap map[string]interface{}
					if err := json.Unmarshal([]byte(state.AccumulatedJSON), &inputMap); err == nil {
						if q, ok := inputMap["query"].(string); ok {
							query = q
							queries = []string{q}
						}
					}
				}

				// Extract sources from the result block
				var sources []schemas.ResponsesWebSearchToolCallActionSearchSource
				if state.WebSearchResult.Content != nil && len(state.WebSearchResult.Content.ContentBlocks) > 0 {
					for _, resultBlock := range state.WebSearchResult.Content.ContentBlocks {
						if resultBlock.Type == AnthropicContentBlockTypeWebSearchResult && resultBlock.URL != nil {
							sources = append(sources, schemas.ResponsesWebSearchToolCallActionSearchSource{
								Type:             "url",
								URL:              *resultBlock.URL,
								Title:            resultBlock.Title,
								EncryptedContent: resultBlock.EncryptedContent,
								PageAge:          resultBlock.PageAge,
							})
						}
					}
				}

				// Create complete web_search_call item with action including query and sources
				statusCompleted := "completed"
				action := &schemas.ResponsesWebSearchToolCallAction{
					Type:    "search",
					Sources: sources,
				}
				// Only set query fields if query is not empty
				if query != "" {
					action.Query = &query
					action.Queries = queries
				}

				item := &schemas.ResponsesMessage{
					ID:     state.WebSearchToolID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status: &statusCompleted,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: state.WebSearchToolID,
						Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: action,
						},
					},
				}

				outputIdx := state.WebSearchOutputIndex

				// Clear all web search state
				state.WebSearchToolID = nil
				state.WebSearchOutputIndex = nil
				state.WebSearchResult = nil
				state.AccumulatedJSON = ""

				if chunk.Index != nil {
					delete(state.ContentIndexToBlockType, *chunk.Index)
				}

				// Return output_item.done for the web_search_call (not the result block)
				return []*schemas.BifrostResponsesStreamResponse{
					{
						Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
						SequenceNumber: sequenceNumber,
						OutputIndex:    outputIdx,
						ContentIndex:   chunk.Index,
						Item:           item,
					},
				}, nil, false
			}

			// Skip generic output_item.done if this is a web_search_tool_result or compaction block
			// (their handlers already emitted the proper done event)
			if chunk.Index != nil {
				if blockType, exists := state.ContentIndexToBlockType[*chunk.Index]; exists {
					if blockType == AnthropicContentBlockTypeWebSearchToolResult {
						// Clean up the tracking
						delete(state.ContentIndexToBlockType, *chunk.Index)
						return nil, nil, false
					}
					if blockType == AnthropicContentBlockTypeCompaction {
						// Clean up the tracking
						delete(state.ContentIndexToBlockType, *chunk.Index)
						return nil, nil, false
					}
				}
			}

			// Check if this is a text block - emit output_text.done and content_part.done
			var responses []*schemas.BifrostResponsesStreamResponse
			itemID := state.ItemIDs[outputIndex]

			// Check if this content index is a text block
			if chunk.Index != nil {
				if state.TextContentIndices[*chunk.Index] {
					// Emit output_text.done (without accumulated text, just the event)
					emptyText := ""
					textDoneResponse := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Text:           &emptyText,
					}
					if itemID != "" {
						textDoneResponse.ItemID = &itemID
					}
					responses = append(responses, textDoneResponse)

					// Emit content_part.done
					partDoneResponse := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
					}
					if itemID != "" {
						partDoneResponse.ItemID = &itemID
					}
					responses = append(responses, partDoneResponse)

					// Clear the text content index tracking
					delete(state.TextContentIndices, *chunk.Index)
				}

				// Check if this content index is a reasoning block
				if state.ReasoningContentIndices[*chunk.Index] {
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
					partDoneResponse := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
					}
					if itemID != "" {
						partDoneResponse.ItemID = &itemID
					}
					responses = append(responses, partDoneResponse)

					// Clear the reasoning content index tracking
					delete(state.ReasoningContentIndices, *chunk.Index)
				}
			}

			// Check if this is a structured output tool call
			if accumulatedArgs, hasArgs := state.ToolArgumentBuffers[outputIndex]; hasArgs && state.StructuredOutputIndex != nil && *state.StructuredOutputIndex == outputIndex {
				// This was a structured output tool - emit as text message instead
				textContent := accumulatedArgs
				if textContent == "" {
					textContent = "{}"
				}

				// Create ContentBlocks with output_text type instead of ContentStr
				contentBlock := schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &textContent,
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					},
				}

				item := &schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{contentBlock},
					},
				}
				if itemID != "" {
					item.ID = &itemID
				}

				// Emit output_item.added for the text message
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				})

				// Emit output_item.done
				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				})

				// Clear the buffer and tracking
				delete(state.ToolArgumentBuffers, outputIndex)
				state.StructuredOutputIndex = nil

				return responses, nil, false
			}

			// Check if this is a tool call (function_call or MCP call)
			// If we have accumulated arguments, emit appropriate arguments.done first
			if accumulatedArgs, hasArgs := state.ToolArgumentBuffers[outputIndex]; hasArgs && accumulatedArgs != "" {
				// Emit appropriate arguments.done based on whether this is an MCP call
				var doneType schemas.ResponsesStreamResponseType
				if state.MCPCallOutputIndices[outputIndex] {
					doneType = schemas.ResponsesStreamResponseTypeMCPCallArgumentsDone
				} else {
					doneType = schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone
				}

				response := &schemas.BifrostResponsesStreamResponse{
					Type:           doneType,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Arguments:      &accumulatedArgs,
				}
				if itemID != "" {
					response.ItemID = &itemID
				}
				responses = append(responses, response)
				// Clear the buffer and MCP tracking
				delete(state.ToolArgumentBuffers, outputIndex)
				delete(state.MCPCallOutputIndices, outputIndex)
			}

			// Emit output_item.done for all content blocks (text, tool, etc.)
			statusCompleted := "completed"
			doneItemID := state.ItemIDs[outputIndex]
			doneItem := &schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Status: &statusCompleted,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{},
				},
			}
			if doneItemID != "" {
				doneItem.ID = &doneItemID
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

	case AnthropicStreamEventTypeMessageDelta:
		if chunk.Delta.StopReason != nil {
			state.StopReason = schemas.Ptr(ConvertAnthropicFinishReasonToBifrost(*chunk.Delta.StopReason))
		}
		// Check if integration type in ctx is anthropic
		if ctx.Value(schemas.BifrostContextKeyIntegrationType) == "anthropic" {
			// Convert usage from Anthropic format to Bifrost
			bifrostUsage := ConvertAnthropicUsageToBifrostUsage(chunk.Usage)

			// Convert stop reason if present
			var stopReason *string
			if chunk.Delta != nil && chunk.Delta.StopReason != nil {
				converted := ConvertAnthropicFinishReasonToBifrost(*chunk.Delta.StopReason)
				stopReason = &converted
			}

			// Create response object with usage and stop reason
			response := &schemas.BifrostResponsesResponse{
				CreatedAt: state.CreatedAt,
			}
			if state.MessageID != nil {
				response.ID = state.MessageID
			}
			if state.Model != nil {
				response.Model = *state.Model
			}
			if stopReason != nil {
				response.StopReason = stopReason
			}
			if bifrostUsage != nil {
				response.Usage = bifrostUsage
			}

			return []*schemas.BifrostResponsesStreamResponse{{
				Type:           "message_delta",
				SequenceNumber: sequenceNumber,
				Response:       response,
			}}, nil, false
		}
		// Message-level updates (like stop reason, usage, etc.)
		// Note: We don't emit output_item.done here because items are already closed
		// by content_block_stop. This event is informational only.
		return nil, nil, false

	case AnthropicStreamEventTypeMessageStop:
		// Message stop - emit response.completed (OpenAI-style)
		response := &schemas.BifrostResponsesResponse{
			CreatedAt: state.CreatedAt,
		}
		if state.MessageID != nil {
			response.ID = state.MessageID
		}
		if state.Model != nil {
			response.Model = *state.Model
		}
		if state.StopReason != nil {
			response.StopReason = state.StopReason
		}

		return []*schemas.BifrostResponsesStreamResponse{{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: sequenceNumber,
			Response:       response,
		}}, nil, true // Indicate stream is complete

	case AnthropicStreamEventTypePing:
		return []*schemas.BifrostResponsesStreamResponse{{
			Type:           schemas.ResponsesStreamResponseTypePing,
			SequenceNumber: sequenceNumber,
		}}, nil, false

	case AnthropicStreamEventTypeError:
		if chunk.Error != nil {
			// Send error event
			bifrostErr := &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    &chunk.Error.Type,
					Message: chunk.Error.Message,
				},
			}

			return []*schemas.BifrostResponsesStreamResponse{{
				Type:           schemas.ResponsesStreamResponseTypeError,
				SequenceNumber: sequenceNumber,
				Message:        &chunk.Error.Message,
			}}, bifrostErr, false
		}
	}

	return nil, nil, false
}

// ToAnthropicResponsesStreamResponse converts a Bifrost Responses stream response to Anthropic SSE string format
func ToAnthropicResponsesStreamResponse(ctx *schemas.BifrostContext, bifrostResp *schemas.BifrostResponsesStreamResponse) []*AnthropicStreamEvent {
	if bifrostResp == nil {
		return nil
	}

	streamResp := &AnthropicStreamEvent{}

	// Map ResponsesStreamResponse types to Anthropic stream events
	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypeCreated:
		// Only convert response.created back to message_start (not response.in_progress to avoid duplicates)
		streamResp.Type = AnthropicStreamEventTypeMessageStart
		if bifrostResp.Response != nil {
			streamMessage := &AnthropicMessageResponse{
				Type:    "message",
				Role:    "assistant",
				Content: []AnthropicContentBlock{}, // Always empty array in message_start
				Usage: &AnthropicUsage{
					InputTokens:              0,
					OutputTokens:             0,
					CacheReadInputTokens:     0,
					CacheCreationInputTokens: 0,
					CacheCreation: AnthropicUsageCacheCreation{
						Ephemeral5mInputTokens: 0,
						Ephemeral1hInputTokens: 0,
					},
				},
			}
			if bifrostResp.Response.ID != nil {
				streamMessage.ID = *bifrostResp.Response.ID
			}
			// Preserve model from Response if available, otherwise use ExtraFields
			if bifrostResp.ExtraFields.ModelRequested != "" {
				if bifrostResp.Response != nil && bifrostResp.Response.Model != "" {
					streamMessage.Model = bifrostResp.Response.Model
				} else {
					streamMessage.Model = bifrostResp.ExtraFields.ModelRequested
				}
			}
			streamResp.Message = streamMessage
		}
	case schemas.ResponsesStreamResponseTypeInProgress:
		// Skip converting response.in_progress back to avoid duplicate message_start events
		// This is an OpenAI-style lifecycle event that doesn't map directly to Anthropic events
		return nil

	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		// Check if this is a computer tool call
		if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeComputerCall {

			// Computer tool - emit content_block_start
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart

			if bifrostResp.OutputIndex != nil {
				streamResp.Index = bifrostResp.OutputIndex
			} else if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			}

			// Build the content_block as tool_use
			// Note: Computer tool calls should not be converted to thinking blocks
			contentBlock := &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   bifrostResp.Item.ID,                            // The tool use ID
				Name: schemas.Ptr(string(AnthropicToolNameComputer)), // "computer"
			}

			// Always start with empty input for streaming compatibility
			contentBlock.Input = map[string]interface{}{}

			streamResp.ContentBlock = contentBlock
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {

			// Web search call - emit content_block_start with server_tool_use
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart

			if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			} else if bifrostResp.OutputIndex != nil {
				streamResp.Index = bifrostResp.OutputIndex
			}

			// Build the content_block as server_tool_use
			contentBlock := &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeServerToolUse,
				ID:   bifrostResp.Item.ID,                             // The tool use ID
				Name: schemas.Ptr(string(AnthropicToolNameWebSearch)), // "web_search"
			}

			// Start with empty input for streaming compatibility
			contentBlock.Input = map[string]interface{}{}

			streamResp.ContentBlock = contentBlock
		} else {
			// Text or other content blocks - emit content_block_start
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			// Use OutputIndex for global Anthropic indexing
			if bifrostResp.OutputIndex != nil {
				streamResp.Index = bifrostResp.OutputIndex
			} else if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			}

			// Build content_block based on item type
			if bifrostResp.Item != nil {
				contentBlock := &AnthropicContentBlock{}
				if bifrostResp.Item.Type != nil {
					switch *bifrostResp.Item.Type {
					case schemas.ResponsesMessageTypeMessage:
						contentBlock.Type = AnthropicContentBlockTypeText
						contentBlock.Text = schemas.Ptr("")
					case schemas.ResponsesMessageTypeReasoning:
						contentBlock.Type = AnthropicContentBlockTypeThinking
						contentBlock.Thinking = schemas.Ptr("")
						contentBlock.Signature = schemas.Ptr("")
						// Preserve signature if present
						if bifrostResp.Item.ResponsesReasoning != nil && bifrostResp.Item.ResponsesReasoning.EncryptedContent != nil && *bifrostResp.Item.ResponsesReasoning.EncryptedContent != "" {
							contentBlock.Data = bifrostResp.Item.ResponsesReasoning.EncryptedContent
							// When signature is present but thinking content is empty, use redacted_thinking
							if contentBlock.Thinking != nil && *contentBlock.Thinking == "" {
								contentBlock.Type = AnthropicContentBlockTypeRedactedThinking
							}
						}
					case schemas.ResponsesMessageTypeFunctionCall:
						// Check if this item actually has reasoning content (misclassified)
						// When thinking is enabled, reasoning content might be incorrectly classified as FunctionCall
						if bifrostResp.Item.ResponsesReasoning != nil {
							// This is actually reasoning content, not a function call
							contentBlock.Type = AnthropicContentBlockTypeThinking
							contentBlock.Thinking = schemas.Ptr("")
							contentBlock.Signature = schemas.Ptr("")
							// Check if there's encrypted content for redacted_thinking
							if bifrostResp.Item.ResponsesReasoning.EncryptedContent != nil && *bifrostResp.Item.ResponsesReasoning.EncryptedContent != "" {
								contentBlock.Type = AnthropicContentBlockTypeRedactedThinking
								contentBlock.Data = bifrostResp.Item.ResponsesReasoning.EncryptedContent
							}
						} else {
							// Regular function call - check if ContentIndex is 0 and thinking might be enabled
							// If ContentIndex is 0, we need to check if there's reasoning content in the response
							contentIndex := 0
							if bifrostResp.ContentIndex != nil {
								contentIndex = *bifrostResp.ContentIndex
							}
							isFirstBlock := contentIndex == 0

							// Check if response has reasoning content (indicating thinking is enabled)
							hasReasoningInResponse := false
							if bifrostResp.Response != nil && bifrostResp.Response.Output != nil {
								for _, msg := range bifrostResp.Response.Output {
									if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning {
										hasReasoningInResponse = true
										break
									}
								}
							}

							// When thinking is enabled and this is the first block, use thinking/redacted_thinking
							if isFirstBlock && hasReasoningInResponse {
								contentBlock.Type = AnthropicContentBlockTypeThinking
								contentBlock.Thinking = schemas.Ptr("")
								contentBlock.Signature = schemas.Ptr("")
							} else {
								contentBlock.Type = AnthropicContentBlockTypeToolUse
								if bifrostResp.Item.ResponsesToolMessage != nil {
									contentBlock.ID = bifrostResp.Item.ResponsesToolMessage.CallID
									contentBlock.Name = bifrostResp.Item.ResponsesToolMessage.Name
									// Always start with empty input for streaming compatibility
									contentBlock.Input = map[string]interface{}{}

									// Track WebSearch tools so we can skip their argument deltas
									if bifrostResp.Item.ResponsesToolMessage.Name != nil &&
										*bifrostResp.Item.ResponsesToolMessage.Name == "WebSearch" &&
										bifrostResp.Item.ID != nil {
										webSearchItemIDs.Store(*bifrostResp.Item.ID, true)
									}
								}
							}
						}
					case schemas.ResponsesMessageTypeMCPCall:
						contentBlock.Type = AnthropicContentBlockTypeMCPToolUse
						if bifrostResp.Item.ResponsesToolMessage != nil {
							contentBlock.ID = bifrostResp.Item.ID
							contentBlock.Name = bifrostResp.Item.ResponsesToolMessage.Name
							if bifrostResp.Item.ResponsesToolMessage.ResponsesMCPToolCall != nil {
								contentBlock.ServerName = &bifrostResp.Item.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
							}
							// Always start with empty input for streaming compatibility
							contentBlock.Input = map[string]interface{}{}
						}
					}
				}
				if contentBlock.Type != "" {
					streamResp.ContentBlock = contentBlock
				}
			}
		}

		// Generate synthetic input_json_delta events for tool calls with arguments
		var events []*AnthropicStreamEvent
		events = append(events, streamResp)

		// Check if this is a tool call with arguments that need to be streamed
		if bifrostResp.Item != nil && bifrostResp.Item.ResponsesToolMessage != nil {
			var argumentsJSON string
			var shouldGenerateDeltas bool

			switch *bifrostResp.Item.Type {
			case schemas.ResponsesMessageTypeFunctionCall:
				if bifrostResp.Item.ResponsesToolMessage.Arguments != nil && *bifrostResp.Item.ResponsesToolMessage.Arguments != "" {
					argumentsJSON = *bifrostResp.Item.ResponsesToolMessage.Arguments
					shouldGenerateDeltas = true
				}
			case schemas.ResponsesMessageTypeMCPCall:
				if bifrostResp.Item.ResponsesToolMessage.Arguments != nil && *bifrostResp.Item.ResponsesToolMessage.Arguments != "" {
					argumentsJSON = *bifrostResp.Item.ResponsesToolMessage.Arguments
					shouldGenerateDeltas = true
				}
			case schemas.ResponsesMessageTypeComputerCall:
				if bifrostResp.Item.ResponsesToolMessage.Action != nil && bifrostResp.Item.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
					actionInput := convertResponsesToAnthropicComputerAction(bifrostResp.Item.ResponsesToolMessage.Action.ResponsesComputerToolCallAction)
					if jsonBytes, err := json.Marshal(actionInput); err == nil {
						argumentsJSON = string(jsonBytes)
						shouldGenerateDeltas = true
					}
				}
			}
			if shouldGenerateDeltas && argumentsJSON != "" {
				// Generate synthetic input_json_delta events by chunking the JSON
				var indexToUse *int
				if bifrostResp.OutputIndex != nil {
					indexToUse = bifrostResp.OutputIndex
				} else if bifrostResp.ContentIndex != nil {
					indexToUse = bifrostResp.ContentIndex
				}
				deltaEvents := generateSyntheticInputJSONDeltas(argumentsJSON, indexToUse)
				events = append(events, deltaEvents...)
			}
		}

		return events
	case schemas.ResponsesStreamResponseTypeContentPartAdded:
		return nil

	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		// Use OutputIndex instead of ContentIndex for global Anthropic indexing
		if bifrostResp.OutputIndex != nil {
			streamResp.Index = bifrostResp.OutputIndex
		} else if bifrostResp.ContentIndex != nil {
			// Fallback to ContentIndex if OutputIndex not available
			streamResp.Index = bifrostResp.ContentIndex
		}
		if bifrostResp.Delta != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type: AnthropicStreamDeltaTypeText,
				Text: bifrostResp.Delta,
			}
		}

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		// Skip WebSearch tool argument deltas - they will be sent synthetically in output_item.done
		// after sanitization to remove conflicting allowed_domains and blocked_domains
		if bifrostResp.ItemID != nil {
			if _, isWebSearch := webSearchItemIDs.Load(*bifrostResp.ItemID); isWebSearch {
				return nil
			}
		}

		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		// Use OutputIndex for global Anthropic indexing
		if bifrostResp.OutputIndex != nil {
			streamResp.Index = bifrostResp.OutputIndex
		} else if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}
		if bifrostResp.Arguments != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: bifrostResp.Arguments,
			}
		} else if bifrostResp.Delta != nil {
			// Handle cases where Delta field is used instead of Arguments
			streamResp.Delta = &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: bifrostResp.Delta,
			}
		}

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		// Use OutputIndex for global Anthropic indexing
		if bifrostResp.OutputIndex != nil {
			streamResp.Index = bifrostResp.OutputIndex
		} else if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}

		// Check if this is a signature delta or text delta
		if bifrostResp.Signature != nil {
			// This is a signature_delta
			streamResp.Delta = &AnthropicStreamDelta{
				Type:      AnthropicStreamDeltaTypeSignature,
				Signature: bifrostResp.Signature,
			}
		} else if bifrostResp.Delta != nil {
			// This is a thinking_delta
			streamResp.Delta = &AnthropicStreamDelta{
				Type:     AnthropicStreamDeltaTypeThinking,
				Thinking: bifrostResp.Delta,
			}
		}

	case schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded:
		// Convert OpenAI annotation to Anthropic citation
		if bifrostResp.Annotation != nil {
			streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
			if bifrostResp.OutputIndex != nil {
				streamResp.Index = bifrostResp.OutputIndex
			} else if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			}

			citation := convertAnnotationToAnthropicCitation(*bifrostResp.Annotation)

			streamResp.Delta = &AnthropicStreamDelta{
				Type:     AnthropicStreamDeltaTypeCitations,
				Citation: &citation,
			}
		}

	case schemas.ResponsesStreamResponseTypeContentPartDone:
		return nil

	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		// Handle WebSearch tool completion with sanitization and synthetic delta generation

		// check for claude-cli user agent
		if ctx != nil {
			if IsClaudeCodeRequest(ctx) {
				// check for WebSearch tool
				if bifrostResp.Item != nil &&
					bifrostResp.Item.Type != nil &&
					*bifrostResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall &&
					bifrostResp.Item.ResponsesToolMessage != nil &&
					bifrostResp.Item.ResponsesToolMessage.Name != nil &&
					*bifrostResp.Item.ResponsesToolMessage.Name == "WebSearch" &&
					bifrostResp.Item.ResponsesToolMessage.Arguments != nil {

					argumentsJSON := sanitizeWebSearchArguments(*bifrostResp.Item.ResponsesToolMessage.Arguments)
					bifrostResp.Item.ResponsesToolMessage.Arguments = &argumentsJSON

					// Generate synthetic input_json_delta events for the sanitized WebSearch arguments
					// This replaces the delta events that were skipped earlier
					var events []*AnthropicStreamEvent

					// Use OutputIndex for proper Anthropic indexing, fallback to ContentIndex
					var indexToUse *int
					if bifrostResp.OutputIndex != nil {
						indexToUse = bifrostResp.OutputIndex
					} else if bifrostResp.ContentIndex != nil {
						indexToUse = bifrostResp.ContentIndex
					}

					deltaEvents := generateSyntheticInputJSONDeltas(argumentsJSON, indexToUse)
					events = append(events, deltaEvents...)

					// Add the content_block_stop event at the end
					stopEvent := &AnthropicStreamEvent{
						Type:  AnthropicStreamEventTypeContentBlockStop,
						Index: indexToUse,
					}
					events = append(events, stopEvent)

					// Clean up the tracking for this WebSearch item
					if bifrostResp.Item.ID != nil {
						webSearchItemIDs.Delete(*bifrostResp.Item.ID)
					}

					return events
				}
			}
		}

		if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeComputerCall {

			// Computer tool complete - emit content_block_delta with the action, then stop
			// Note: We're sending the complete action JSON in one delta
			streamResp.Type = AnthropicStreamEventTypeContentBlockDelta

			// Use OutputIndex for global Anthropic indexing
			if bifrostResp.OutputIndex != nil {
				streamResp.Index = bifrostResp.OutputIndex
			} else if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			}

			// Convert the action to Anthropic format and marshal to JSON
			if bifrostResp.Item.ResponsesToolMessage != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {

				actionInput := convertResponsesToAnthropicComputerAction(
					bifrostResp.Item.ResponsesToolMessage.Action.ResponsesComputerToolCallAction,
				)

				// Marshal the action to JSON string
				if jsonBytes, err := json.Marshal(actionInput); err == nil {
					jsonStr := string(jsonBytes)
					streamResp.Delta = &AnthropicStreamDelta{
						Type:        AnthropicStreamDeltaTypeInputJSON,
						PartialJSON: &jsonStr,
					}
				}
			}
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {

			// Web search call complete - generate synthetic input_json_delta events, then emit content_block_stop
			var events []*AnthropicStreamEvent

			// Extract query from web search action for synthetic delta generation
			var queryJSON string
			if bifrostResp.Item.ResponsesToolMessage != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Query != nil {

				// Create input map with query
				inputMap := map[string]interface{}{
					"query": *bifrostResp.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Query,
				}
				if jsonBytes, err := json.Marshal(inputMap); err == nil {
					queryJSON = string(jsonBytes)
				}
			}

			// Generate synthetic input_json_delta events if we have a query
			if queryJSON != "" {
				var indexToUse *int
				if bifrostResp.OutputIndex != nil {
					indexToUse = bifrostResp.OutputIndex
				} else if bifrostResp.ContentIndex != nil {
					indexToUse = bifrostResp.ContentIndex
				}
				deltaEvents := generateSyntheticInputJSONDeltas(queryJSON, indexToUse)
				events = append(events, deltaEvents...)
			}

			// 1. Emit content_block_stop for the query block (server_tool_use)
			stopEvent := &AnthropicStreamEvent{
				Type: AnthropicStreamEventTypeContentBlockStop,
			}
			if bifrostResp.ContentIndex != nil {
				stopEvent.Index = bifrostResp.ContentIndex
			} else if bifrostResp.OutputIndex != nil {
				stopEvent.Index = bifrostResp.OutputIndex
			}
			events = append(events, stopEvent)

			// 2. Extract sources and create web_search_tool_result block if sources exist
			if bifrostResp.Item.ResponsesToolMessage != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil &&
				len(bifrostResp.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources) > 0 {

				// Calculate next index for result block
				var resultIndex *int
				if bifrostResp.OutputIndex != nil {
					nextIdx := *bifrostResp.OutputIndex + 1
					resultIndex = &nextIdx
				} else if bifrostResp.ContentIndex != nil {
					nextIdx := *bifrostResp.ContentIndex + 1
					resultIndex = &nextIdx
				}

				// Create content blocks for each source
				var resultContentBlocks []AnthropicContentBlock
				for _, source := range bifrostResp.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources {
					block := AnthropicContentBlock{
						Type:             AnthropicContentBlockTypeWebSearchResult,
						URL:              &source.URL,
						EncryptedContent: source.EncryptedContent,
						PageAge:          source.PageAge,
					}
					if source.Title != nil {
						block.Title = source.Title
					} else if source.URL != "" {
						block.Title = schemas.Ptr(source.URL)
					}
					resultContentBlocks = append(resultContentBlocks, block)
				}

				// Emit content_block_start for web_search_tool_result
				resultStartEvent := &AnthropicStreamEvent{
					Type:  AnthropicStreamEventTypeContentBlockStart,
					Index: resultIndex,
					ContentBlock: &AnthropicContentBlock{
						Type:      AnthropicContentBlockTypeWebSearchToolResult,
						ToolUseID: bifrostResp.Item.ID, // Link to the server_tool_use block
						Content: &AnthropicContent{
							ContentBlocks: resultContentBlocks,
						},
					},
				}
				events = append(events, resultStartEvent)

				// Emit content_block_stop for the result block
				resultStopEvent := &AnthropicStreamEvent{
					Type:  AnthropicStreamEventTypeContentBlockStop,
					Index: resultIndex,
				}
				events = append(events, resultStopEvent)
			}

			return events
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			(*bifrostResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall ||
				*bifrostResp.Item.Type == schemas.ResponsesMessageTypeMCPCall) {

			// Function call or MCP call complete - just emit content_block_stop
			streamResp.Type = AnthropicStreamEventTypeContentBlockStop
			if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			} else if bifrostResp.OutputIndex != nil {
				streamResp.Index = bifrostResp.OutputIndex
			}
		} else {
			// For text blocks and other content blocks, emit content_block_stop
			streamResp.Type = AnthropicStreamEventTypeContentBlockStop
			// Use OutputIndex for global Anthropic indexing
			if bifrostResp.OutputIndex != nil {
				streamResp.Index = bifrostResp.OutputIndex
			} else if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			}
		}
	case schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		schemas.ResponsesStreamResponseTypeWebSearchCallCompleted:
		// Web search lifecycle events - these are OpenAI-style events that don't have Anthropic equivalents
		// Skip them to avoid cluttering the stream
		return nil

	case schemas.ResponsesStreamResponseTypePing:
		streamResp.Type = AnthropicStreamEventTypePing

	case schemas.ResponsesStreamResponseTypeCompleted:
		streamResp.Type = AnthropicStreamEventTypeMessageStop
		anthropicContentDeltaEvent := &AnthropicStreamEvent{
			Type: AnthropicStreamEventTypeMessageDelta,
			Delta: &AnthropicStreamDelta{
				StopReason:   schemas.Ptr(AnthropicStopReasonEndTurn),
				StopSequence: schemas.Ptr(""),
			},
		}
		// Convert usage from Bifrost to Anthropic
		if bifrostResp.Response != nil {
			anthropicContentDeltaEvent.Usage = ConvertBifrostUsageToAnthropicUsage(bifrostResp.Response.Usage)
			if bifrostResp.Response.StopReason != nil {
				anthropicContentDeltaEvent.Delta = &AnthropicStreamDelta{
					StopReason:   schemas.Ptr(ConvertBifrostFinishReasonToAnthropic(*bifrostResp.Response.StopReason)),
					StopSequence: nil,
				}
			}
		}
		return []*AnthropicStreamEvent{anthropicContentDeltaEvent, streamResp}

	case schemas.ResponsesStreamResponseTypeMCPCallArgumentsDelta:
		// MCP call arguments delta - convert to content_block_delta with input_json
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		// Use OutputIndex for global Anthropic indexing
		if bifrostResp.OutputIndex != nil {
			streamResp.Index = bifrostResp.OutputIndex
		} else if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}
		if bifrostResp.Delta != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: bifrostResp.Delta,
			}
		} else if bifrostResp.Arguments != nil {
			// Handle cases where Arguments field is used instead of Delta
			streamResp.Delta = &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: bifrostResp.Arguments,
			}
		}

	case schemas.ResponsesStreamResponseTypeMCPCallCompleted:
		// MCP call completed - emit content_block_stop
		streamResp.Type = AnthropicStreamEventTypeContentBlockStop
		// Use OutputIndex for global Anthropic indexing
		if bifrostResp.OutputIndex != nil {
			streamResp.Index = bifrostResp.OutputIndex
		} else if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}

	case schemas.ResponsesStreamResponseTypeMCPCallFailed:
		// MCP call failed - emit error event
		streamResp.Type = AnthropicStreamEventTypeError
		errorMsg := "MCP call failed"
		if bifrostResp.Message != nil {
			errorMsg = *bifrostResp.Message
		}
		streamResp.Error = &AnthropicStreamError{
			Type:    "error",
			Message: errorMsg,
		}

	case "message_delta":
		// Check if integration type in ctx is anthropic
		if ctx.Value(schemas.BifrostContextKeyIntegrationType) == "anthropic" {
			streamResp.Type = AnthropicStreamEventTypeMessageDelta

			// Convert usage from Bifrost format to Anthropic format using common converter
			if bifrostResp.Response != nil {
				streamResp.Usage = ConvertBifrostUsageToAnthropicUsage(bifrostResp.Response.Usage)
			}

			// Convert stop reason from Bifrost format to Anthropic format
			if bifrostResp.Response != nil && bifrostResp.Response.StopReason != nil {
				streamResp.Delta = &AnthropicStreamDelta{
					StopReason: schemas.Ptr(ConvertBifrostFinishReasonToAnthropic(*bifrostResp.Response.StopReason)),
				}
			} else if bifrostResp.Delta != nil {
				// Handle text delta if present
				streamResp.Delta = &AnthropicStreamDelta{
					Type: AnthropicStreamDeltaTypeText,
					Text: bifrostResp.Delta,
				}
			}
		}

	case schemas.ResponsesStreamResponseTypeError:
		streamResp.Type = AnthropicStreamEventTypeError
		if bifrostResp.Message != nil {
			streamResp.Error = &AnthropicStreamError{
				Type:    "error",
				Message: *bifrostResp.Message,
			}
		}

	default:
		// Unknown event type, return empty
		return nil
	}

	return []*AnthropicStreamEvent{streamResp}
}

// ToBifrostResponsesRequest converts an Anthropic message request to Bifrost format
func (req *AnthropicMessageRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	provider, model := schemas.ParseModelString(req.Model, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Anthropic))

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider:  provider,
		Model:     model,
		Fallbacks: schemas.ParseFallbacks(req.Fallbacks),
	}

	// Convert basic parameters
	params := &schemas.ResponsesParameters{
		ExtraParams: make(map[string]interface{}),
	}

	if req.MaxTokens > 0 {
		params.MaxOutputTokens = &req.MaxTokens
	}
	if req.Temperature != nil {
		params.Temperature = req.Temperature
	}
	if req.TopP != nil {
		params.TopP = req.TopP
	}
	if req.Metadata != nil && req.Metadata.UserID != nil {
		params.User = req.Metadata.UserID
	}
	if req.ContextManagement != nil {
		params.ExtraParams["context_management"] = req.ContextManagement
	}
	if req.InferenceGeo != nil {
		params.ExtraParams["inference_geo"] = *req.InferenceGeo
	}
	if req.CacheControl != nil {
		params.ExtraParams["cache_control"] = req.CacheControl
	}
	if req.TopK != nil {
		params.ExtraParams["top_k"] = *req.TopK
	}
	if req.StopSequences != nil {
		params.ExtraParams["stop"] = req.StopSequences
	}
	if req.OutputFormat != nil {
		params.Text = convertAnthropicOutputFormatToResponsesTextConfig(req.OutputFormat)
	} else if req.OutputConfig != nil && req.OutputConfig.Format != nil {
		// GA structured outputs - OutputConfig.Format has same structure as OutputFormat
		params.Text = convertAnthropicOutputFormatToResponsesTextConfig(req.OutputConfig.Format)
	}
	if req.Thinking != nil {
		if req.Thinking.Type == "enabled" || req.Thinking.Type == "adaptive" {
			var summary *string
			if summaryValue, ok := schemas.SafeExtractStringPointer(req.ExtraParams["reasoning_summary"]); ok {
				summary = summaryValue
			}
			// check if user agent in ctx is claude-cli
			if ctx != nil {
				if IsClaudeCodeRequest(ctx) {
					summary = schemas.Ptr("detailed")
				}
			}
			if req.OutputConfig != nil && req.OutputConfig.Effort != nil {
				// Native effort present — map to Bifrost enum (e.g., "max" → "high")
				params.Reasoning = &schemas.ResponsesParametersReasoning{
					Effort:    schemas.Ptr(MapAnthropicEffortToBifrost(*req.OutputConfig.Effort)),
					MaxTokens: req.Thinking.BudgetTokens,
					Summary:   summary,
				}
			} else if req.Thinking.BudgetTokens != nil {
				// Fallback: convert budget_tokens to effort
				params.Reasoning = &schemas.ResponsesParametersReasoning{
					Effort:    schemas.Ptr(providerUtils.GetReasoningEffortFromBudgetTokens(*req.Thinking.BudgetTokens, MinimumReasoningMaxTokens, AnthropicDefaultMaxTokens)),
					MaxTokens: req.Thinking.BudgetTokens,
					Summary:   summary,
				}
			} else {
				// Adaptive with no explicit effort — default to "high"
				params.Reasoning = &schemas.ResponsesParametersReasoning{
					Effort:  schemas.Ptr("high"),
					Summary: summary,
				}
			}
		} else {
			params.Reasoning = &schemas.ResponsesParametersReasoning{
				Effort: schemas.Ptr("none"),
			}
		}
	}
	if include, ok := schemas.SafeExtractStringSlice(req.ExtraParams["include"]); ok {
		params.Include = include
	}

	// Add trucation parameter if computer tool is being used
	if provider == schemas.OpenAI && req.Tools != nil {
		for _, tool := range req.Tools {
			if tool.Type != nil && (*tool.Type == AnthropicToolTypeComputer20250124 || *tool.Type == AnthropicToolTypeComputer20251124) {
				params.Truncation = schemas.Ptr("auto")
			} else if tool.Type != nil && (*tool.Type == AnthropicToolTypeWebSearch20250305) {
				params.Include = []string{"web_search_call.action.sources"}
			}
		}

	}

	bifrostReq.Params = params

	// Convert messages directly to ChatMessage format
	var bifrostMessages []schemas.ResponsesMessage

	// Convert regular messages using the new conversion method
	convertedMessages := ConvertAnthropicMessagesToBifrostMessages(ctx, req.Messages, req.System, false, provider == schemas.Bedrock)
	bifrostMessages = append(bifrostMessages, convertedMessages...)

	// Convert tools if present
	if req.Tools != nil {
		var bifrostTools []schemas.ResponsesTool
		for _, tool := range req.Tools {
			bifrostTool := convertAnthropicToolToBifrost(&tool)
			if bifrostTool != nil {
				bifrostTools = append(bifrostTools, *bifrostTool)
			}
		}
		if len(bifrostTools) > 0 {
			bifrostReq.Params.Tools = bifrostTools
		}
	}

	if req.MCPServers != nil {
		var bifrostMCPTools []schemas.ResponsesTool
		for _, mcpServer := range req.MCPServers {
			bifrostMCPTool := convertAnthropicMCPServerToBifrostTool(&mcpServer)
			if bifrostMCPTool != nil {
				bifrostMCPTools = append(bifrostMCPTools, *bifrostMCPTool)
			}
		}
		if len(bifrostMCPTools) > 0 {
			bifrostReq.Params.Tools = append(bifrostReq.Params.Tools, bifrostMCPTools...)
		}
	}

	// Convert tool choice if present
	if req.ToolChoice != nil {
		bifrostToolChoice := convertAnthropicToolChoiceToBifrost(req.ToolChoice)
		if bifrostToolChoice != nil {
			bifrostReq.Params.ToolChoice = bifrostToolChoice
		}
	}

	// Set the converted messages
	if len(bifrostMessages) > 0 {
		bifrostReq.Input = bifrostMessages
	}

	return bifrostReq
}

// ToAnthropicResponsesRequest converts a BifrostRequest with Responses structure back to AnthropicMessageRequest
func ToAnthropicResponsesRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostResponsesRequest) (*AnthropicMessageRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	anthropicReq := &AnthropicMessageRequest{
		Model:     bifrostReq.Model,
		MaxTokens: AnthropicDefaultMaxTokens,
	}

	// Convert basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxOutputTokens != nil {
			anthropicReq.MaxTokens = *bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			anthropicReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			anthropicReq.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.User != nil {
			anthropicReq.Metadata = &AnthropicMetaData{
				UserID: bifrostReq.Params.User,
			}
		}
		if bifrostReq.Params.Text != nil {
			// Vertex doesn't support native structured outputs, so convert to tool
			if bifrostReq.Provider == schemas.Vertex {
				if bifrostReq.Params.Text.Format != nil {
					responseFormatTool := convertResponsesTextFormatToTool(ctx, bifrostReq.Params.Text)
					if responseFormatTool != nil {
						if anthropicReq.Tools == nil {
							anthropicReq.Tools = []AnthropicTool{}
						}
						anthropicReq.Tools = append(anthropicReq.Tools, *responseFormatTool)
						// Force the model to use this specific tool
						anthropicReq.ToolChoice = &AnthropicToolChoice{
							Type: "tool",
							Name: responseFormatTool.Name,
						}
					}
				}
			} else {
				// Citations cannot be used together with Structured Outputs in anthropic.
				hasCitationsEnabled := false
				// loop over input messages and check if any message has citations enabled
				for _, message := range bifrostReq.Input {
					if message.Content == nil || message.Content.ContentBlocks == nil {
						continue
					}
					if message.Content.ContentBlocks != nil {
						for _, block := range message.Content.ContentBlocks {
							if block.Type == schemas.ResponsesInputMessageContentBlockTypeFile &&
								block.Citations != nil &&
								block.Citations.Enabled != nil &&
								*block.Citations.Enabled {
								hasCitationsEnabled = true
								break
							}
						}
					}
					if hasCitationsEnabled {
						break
					}
				}
				if !hasCitationsEnabled {
					// Use GA structured outputs (output_config.format) instead of beta (output_format)
					outputFormat := convertResponsesTextConfigToAnthropicOutputFormat(bifrostReq.Params.Text)
					if outputFormat != nil {
						anthropicReq.OutputConfig = &AnthropicOutputConfig{
							Format: outputFormat,
						}
					}
				}
			}
		}
		if bifrostReq.Params.Reasoning != nil {
			if bifrostReq.Params.Reasoning.MaxTokens != nil {
				budgetTokens := *bifrostReq.Params.Reasoning.MaxTokens
				if *bifrostReq.Params.Reasoning.MaxTokens == -1 {
					// anthropic does not support dynamic reasoning budget like gemini
					// setting it to default max tokens
					budgetTokens = MinimumReasoningMaxTokens
				}
				if budgetTokens < MinimumReasoningMaxTokens {
					return nil, fmt.Errorf("reasoning.max_tokens must be >= %d for anthropic", MinimumReasoningMaxTokens)
				}
				anthropicReq.Thinking = &AnthropicThinking{
					Type:         "enabled",
					BudgetTokens: schemas.Ptr(budgetTokens),
				}
			} else {
				if bifrostReq.Params.Reasoning.Effort != nil {
					if *bifrostReq.Params.Reasoning.Effort != "none" {
						effort := MapBifrostEffortToAnthropic(*bifrostReq.Params.Reasoning.Effort)

						if SupportsAdaptiveThinking(bifrostReq.Model) {
							// Opus 4.6+: adaptive thinking + native effort
							anthropicReq.Thinking = &AnthropicThinking{Type: "adaptive"}
							setEffortOnOutputConfig(anthropicReq, effort)
						} else if SupportsNativeEffort(bifrostReq.Model) {
							// Opus 4.5: native effort + budget_tokens thinking
							setEffortOnOutputConfig(anthropicReq, effort)
							budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(effort, MinimumReasoningMaxTokens, anthropicReq.MaxTokens)
							if err != nil {
								return nil, err
							}
							anthropicReq.Thinking = &AnthropicThinking{
								Type:         "enabled",
								BudgetTokens: schemas.Ptr(budgetTokens),
							}
						} else {
							// Older models: budget_tokens only
							budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(effort, MinimumReasoningMaxTokens, anthropicReq.MaxTokens)
							if err != nil {
								return nil, err
							}
							anthropicReq.Thinking = &AnthropicThinking{
								Type:         "enabled",
								BudgetTokens: schemas.Ptr(budgetTokens),
							}
						}
					} else {
						anthropicReq.Thinking = &AnthropicThinking{
							Type: "disabled",
						}
					}
				}
			}
		}
		// Convert service tier
		anthropicReq.ServiceTier = bifrostReq.Params.ServiceTier

		if bifrostReq.Params.ExtraParams != nil {
			anthropicReq.ExtraParams = bifrostReq.Params.ExtraParams
			if cacheControlRaw, exists := bifrostReq.Params.ExtraParams["cache_control"]; exists {
				switch v := cacheControlRaw.(type) {
				case *schemas.CacheControl:
					anthropicReq.CacheControl = v
				case schemas.CacheControl:
					anthropicReq.CacheControl = &v
				default:
					if data, err := json.Marshal(v); err == nil {
						var parsed schemas.CacheControl
						if json.Unmarshal(data, &parsed) == nil {
							anthropicReq.CacheControl = &parsed
						}
					}
				}
				delete(bifrostReq.Params.ExtraParams, "cache_control")
			}
			topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"])
			if ok {
				delete(anthropicReq.ExtraParams, "top_k")
				anthropicReq.TopK = topK
			}
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				delete(anthropicReq.ExtraParams, "stop")
				anthropicReq.StopSequences = stop
			}
			if inferenceGeo, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["inference_geo"]); ok {
				delete(anthropicReq.ExtraParams, "inference_geo")
				anthropicReq.InferenceGeo = inferenceGeo
			}
			if cmVal := bifrostReq.Params.ExtraParams["context_management"]; cmVal != nil {
				if cm, ok := cmVal.(*ContextManagement); ok && cm != nil {
					delete(anthropicReq.ExtraParams, "context_management")
					anthropicReq.ContextManagement = cm
				} else if data, err := json.Marshal(cmVal); err == nil {
					var cm ContextManagement
					if json.Unmarshal(data, &cm) == nil {
						delete(anthropicReq.ExtraParams, "context_management")
						anthropicReq.ContextManagement = &cm
					}
				}
			}
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			anthropicTools := []AnthropicTool{}
			mcpServers := []AnthropicMCPServer{}
			for _, tool := range bifrostReq.Params.Tools {
				// handle mcp tool differently
				if tool.Type == schemas.ResponsesToolTypeMCP && tool.ResponsesToolMCP != nil {
					mcpServer := convertBifrostMCPToolToAnthropicServer(&tool)
					if mcpServer != nil {
						mcpServers = append(mcpServers, *mcpServer)
					}
					continue // Skip converting MCP tools to anthropicTools since they're handled separately
				}
				anthropicTool := convertBifrostToolToAnthropic(bifrostReq.Model, &tool)
				if anthropicTool != nil {
					anthropicTools = append(anthropicTools, *anthropicTool)
				}
			}
			if len(anthropicTools) > 0 {
				if anthropicReq.Tools == nil {
					anthropicReq.Tools = anthropicTools
				} else {
					anthropicReq.Tools = append(anthropicReq.Tools, anthropicTools...)
				}
			}
			if len(mcpServers) > 0 {
				anthropicReq.MCPServers = mcpServers
			}
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			anthropicToolChoice := convertResponsesToolChoiceToAnthropic(bifrostReq.Params.ToolChoice)
			if anthropicToolChoice != nil {
				anthropicReq.ToolChoice = anthropicToolChoice
			}
		}
	}

	if bifrostReq.Input != nil {
		anthropicMessages, systemContent := ConvertBifrostMessagesToAnthropicMessages(ctx, bifrostReq.Input)

		// Set system message if present
		if systemContent != nil {
			anthropicReq.System = systemContent
		} else if bifrostReq.Params != nil && bifrostReq.Params.Instructions != nil && *bifrostReq.Params.Instructions != "" {
			// if no system content, check if instructions are present
			// system messages take precedence over instructions
			anthropicReq.System = &AnthropicContent{
				ContentBlocks: []AnthropicContentBlock{
					{
						Type: AnthropicContentBlockTypeText,
						Text: bifrostReq.Params.Instructions,
					},
				},
			}
		}

		// Set regular messages
		anthropicReq.Messages = anthropicMessages
	}

	return anthropicReq, nil
}

// ConvertAnthropicUsageToBifrostUsage converts Anthropic usage format to Bifrost usage format
// Handles iterations recursively
func ConvertAnthropicUsageToBifrostUsage(anthropicUsage *AnthropicUsage) *schemas.ResponsesResponseUsage {
	if anthropicUsage == nil {
		return nil
	}

	bifrostUsage := &schemas.ResponsesResponseUsage{
		Type:         anthropicUsage.Type,
		InputTokens:  anthropicUsage.InputTokens,
		OutputTokens: anthropicUsage.OutputTokens,
		TotalTokens:  anthropicUsage.InputTokens + anthropicUsage.OutputTokens,
	}

	// Handle cache read tokens (input side)
	if anthropicUsage.CacheReadInputTokens > 0 {
		if bifrostUsage.InputTokensDetails == nil {
			bifrostUsage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		bifrostUsage.InputTokensDetails.CachedTokens = anthropicUsage.CacheReadInputTokens
	}

	// Handle cache creation tokens (output side)
	if anthropicUsage.CacheCreationInputTokens > 0 {
		if bifrostUsage.OutputTokensDetails == nil {
			bifrostUsage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{}
		}
		bifrostUsage.OutputTokensDetails.CachedTokens = anthropicUsage.CacheCreationInputTokens
	}

	// Propagate server tool use (web search) counts
	if anthropicUsage.ServerToolUse != nil && anthropicUsage.ServerToolUse.WebSearchRequests > 0 {
		if bifrostUsage.OutputTokensDetails == nil {
			bifrostUsage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{}
		}
		bifrostUsage.OutputTokensDetails.NumSearchQueries = schemas.Ptr(anthropicUsage.ServerToolUse.WebSearchRequests)
	}

	// Recursively convert iterations
	if len(anthropicUsage.Iterations) > 0 {
		bifrostUsage.Iterations = make([]schemas.ResponsesResponseUsage, len(anthropicUsage.Iterations))
		for i, iteration := range anthropicUsage.Iterations {
			if converted := ConvertAnthropicUsageToBifrostUsage(&iteration); converted != nil {
				bifrostUsage.Iterations[i] = *converted
			}
		}
	}

	return bifrostUsage
}

// ConvertBifrostUsageToAnthropicUsage converts Bifrost usage format to Anthropic usage format
// Handles iterations recursively
func ConvertBifrostUsageToAnthropicUsage(bifrostUsage *schemas.ResponsesResponseUsage) *AnthropicUsage {
	if bifrostUsage == nil {
		return nil
	}

	anthropicUsage := &AnthropicUsage{
		Type:         bifrostUsage.Type,
		InputTokens:  bifrostUsage.InputTokens,
		OutputTokens: bifrostUsage.OutputTokens,
	}

	// Handle cache read tokens (from input side)
	if bifrostUsage.InputTokensDetails != nil && bifrostUsage.InputTokensDetails.CachedTokens > 0 {
		anthropicUsage.CacheReadInputTokens = bifrostUsage.InputTokensDetails.CachedTokens
	}

	// Handle cache creation tokens (from output side)
	if bifrostUsage.OutputTokensDetails != nil && bifrostUsage.OutputTokensDetails.CachedTokens > 0 {
		anthropicUsage.CacheCreationInputTokens = bifrostUsage.OutputTokensDetails.CachedTokens
	}

	// Handle server tool use statistics (e.g., web search)
	if bifrostUsage.OutputTokensDetails != nil && bifrostUsage.OutputTokensDetails.NumSearchQueries != nil && *bifrostUsage.OutputTokensDetails.NumSearchQueries > 0 {
		anthropicUsage.ServerToolUse = &AnthropicServerToolUseUsage{
			WebSearchRequests: *bifrostUsage.OutputTokensDetails.NumSearchQueries,
		}
	}

	// Recursively convert iterations
	if len(bifrostUsage.Iterations) > 0 {
		anthropicUsage.Iterations = make([]AnthropicUsage, len(bifrostUsage.Iterations))
		for i, iteration := range bifrostUsage.Iterations {
			if converted := ConvertBifrostUsageToAnthropicUsage(&iteration); converted != nil {
				anthropicUsage.Iterations[i] = *converted
			}
		}
	}

	return anthropicUsage
}

// ToBifrostResponsesResponse converts an Anthropic response to BifrostResponse with Responses structure
func (response *AnthropicMessageResponse) ToBifrostResponsesResponse(ctx *schemas.BifrostContext) *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr(response.ID),
		CreatedAt: int(time.Now().Unix()),
	}

	// Convert usage information using common converter (handles iterations recursively)
	bifrostResp.Usage = ConvertAnthropicUsageToBifrostUsage(response.Usage)

	// Convert content to Responses output messages using the new conversion method
	if len(response.Content) > 0 {
		// Create a temporary message to use the conversion method
		tempMsg := AnthropicMessage{
			Role: AnthropicMessageRoleAssistant,
			Content: AnthropicContent{
				ContentBlocks: response.Content,
			},
		}
		outputMessages := ConvertAnthropicMessagesToBifrostMessages(ctx, []AnthropicMessage{tempMsg}, nil, true, false)
		if len(outputMessages) > 0 {
			bifrostResp.Output = outputMessages
		}
	}

	bifrostResp.Model = response.Model

	return bifrostResp
}

// ToAnthropicResponsesResponse converts a BifrostResponse with Responses structure back to AnthropicMessageResponse
func ToAnthropicResponsesResponse(ctx *schemas.BifrostContext, bifrostResp *schemas.BifrostResponsesResponse) *AnthropicMessageResponse {
	anthropicResp := &AnthropicMessageResponse{
		Type: "message",
		Role: "assistant",
	}
	if bifrostResp.ID != nil {
		anthropicResp.ID = *bifrostResp.ID
	}

	// Convert usage information using common converter (handles iterations recursively)
	anthropicResp.Usage = ConvertBifrostUsageToAnthropicUsage(bifrostResp.Usage)

	// Convert output messages to Anthropic content blocks using the new conversion method
	var contentBlocks []AnthropicContentBlock
	if bifrostResp.Output != nil {
		anthropicMessages, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, bifrostResp.Output)
		// Extract content blocks from the converted messages
		for _, msg := range anthropicMessages {
			if msg.Content.ContentBlocks != nil {
				contentBlocks = append(contentBlocks, msg.Content.ContentBlocks...)
			} else if msg.Content.ContentStr != nil {
				contentBlocks = append(contentBlocks, AnthropicContentBlock{
					Type: AnthropicContentBlockTypeText,
					Text: msg.Content.ContentStr,
				})
			}
		}
	}

	if len(contentBlocks) > 0 {
		anthropicResp.Content = contentBlocks
	} else {
		anthropicResp.Content = []AnthropicContentBlock{}
	}

	// Set default stop reason - could be enhanced based on additional context
	anthropicResp.StopReason = AnthropicStopReasonEndTurn

	// Check if there are tool calls to set appropriate stop reason
	for _, block := range contentBlocks {
		if block.Type == AnthropicContentBlockTypeToolUse {
			anthropicResp.StopReason = AnthropicStopReasonToolUse
			break
		}
	}

	anthropicResp.Model = bifrostResp.Model

	return anthropicResp
}

// ConvertAnthropicMessagesToBifrostMessages converts an array of Anthropic messages to Bifrost ResponsesMessage format
func ConvertAnthropicMessagesToBifrostMessages(ctx *schemas.BifrostContext, anthropicMessages []AnthropicMessage, systemContent *AnthropicContent, isOutputMessage bool, keepToolsGrouped bool) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage

	// Get structured output tool name from context if present
	var structuredOutputToolName string
	if ctx != nil {
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}
	}

	// Handle system message first if present
	if systemContent != nil {
		systemMessages := convertAnthropicSystemToBifrostMessages(systemContent)
		bifrostMessages = append(bifrostMessages, systemMessages...)
	}

	// Convert regular messages
	for _, msg := range anthropicMessages {
		var convertedMessages []schemas.ResponsesMessage
		if keepToolsGrouped {
			convertedMessages = convertSingleAnthropicMessageToBifrostMessagesGrouped(&msg, isOutputMessage, structuredOutputToolName)
		} else {
			convertedMessages = convertSingleAnthropicMessageToBifrostMessages(ctx, &msg, isOutputMessage, structuredOutputToolName)
		}
		bifrostMessages = append(bifrostMessages, convertedMessages...)
	}

	return bifrostMessages
}

// ConvertBifrostMessagesToAnthropicMessages converts an array of Bifrost ResponsesMessage to Anthropic message format
// This is the main conversion method from Bifrost to Anthropic - handles all message types and returns messages + system content
func ConvertBifrostMessagesToAnthropicMessages(ctx *schemas.BifrostContext, bifrostMessages []schemas.ResponsesMessage) ([]AnthropicMessage, *AnthropicContent) {
	var anthropicMessages []AnthropicMessage
	var systemContent *AnthropicContent
	var pendingToolCalls []AnthropicContentBlock
	var pendingToolResultBlocks []AnthropicContentBlock
	var pendingReasoningContentBlocks []AnthropicContentBlock
	var currentAssistantMessage *AnthropicMessage

	// Track tool call IDs for each assistant turn to properly match tool results
	// Each assistant turn that contains tool_use blocks should have its tool results
	// grouped in a corresponding user message
	type toolCallGroup struct {
		toolCallIDs map[string]bool // Set of tool call IDs in this group
		flushed     bool            // Whether the tool results for this group have been flushed
	}
	var toolCallGroups []toolCallGroup
	var currentToolCallIDs map[string]bool // IDs of tool calls in the current pending batch

	// Helper to flush pending tool result blocks into user messages
	// This now matches tool results to their corresponding tool call groups
	flushPendingToolResults := func() {
		if len(pendingToolResultBlocks) == 0 {
			return
		}

		// If there are no tool call groups, just flush all results together
		if len(toolCallGroups) == 0 {
			anthropicMessages = append(anthropicMessages, AnthropicMessage{
				Role: AnthropicMessageRoleUser,
				Content: AnthropicContent{
					ContentBlocks: pendingToolResultBlocks,
				},
			})
			pendingToolResultBlocks = nil
			return
		}

		// Group tool results by their corresponding tool call group
		// Each group should be flushed as a separate user message
		for i := range toolCallGroups {
			if toolCallGroups[i].flushed {
				continue
			}

			var groupResults []AnthropicContentBlock
			var remainingResults []AnthropicContentBlock

			for _, block := range pendingToolResultBlocks {
				if block.ToolUseID != nil && toolCallGroups[i].toolCallIDs[*block.ToolUseID] {
					groupResults = append(groupResults, block)
				} else {
					remainingResults = append(remainingResults, block)
				}
			}

			if len(groupResults) > 0 {
				anthropicMessages = append(anthropicMessages, AnthropicMessage{
					Role: AnthropicMessageRoleUser,
					Content: AnthropicContent{
						ContentBlocks: groupResults,
					},
				})
				toolCallGroups[i].flushed = true
				pendingToolResultBlocks = remainingResults
			}
		}

		// Flush any remaining tool results that didn't match any group
		if len(pendingToolResultBlocks) > 0 {
			anthropicMessages = append(anthropicMessages, AnthropicMessage{
				Role: AnthropicMessageRoleUser,
				Content: AnthropicContent{
					ContentBlocks: pendingToolResultBlocks,
				},
			})
			pendingToolResultBlocks = nil
		}
	}

	// Helper to flush pending tool calls with tool call ID tracking
	flushPendingToolCallsWithTracking := func() {
		if len(pendingToolCalls) > 0 && currentAssistantMessage != nil {
			// Copy the slice to avoid aliasing issues
			copied := make([]AnthropicContentBlock, len(pendingToolCalls))
			copy(copied, pendingToolCalls)
			currentAssistantMessage.Content = AnthropicContent{
				ContentBlocks: copied,
			}
			anthropicMessages = append(anthropicMessages, *currentAssistantMessage)

			// Record this tool call group for matching with tool results
			if len(currentToolCallIDs) > 0 {
				toolCallGroups = append(toolCallGroups, toolCallGroup{
					toolCallIDs: currentToolCallIDs,
					flushed:     false,
				})
				currentToolCallIDs = nil
			}

			pendingToolCalls = nil
			currentAssistantMessage = nil
		}
	}

	for _, msg := range bifrostMessages {
		// Handle nil Type as regular message
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}

		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Flush any pending tool results before processing other message types
			flushPendingToolResults()

			// Flush any pending tool calls first (with tracking for tool call groups)
			flushPendingToolCallsWithTracking()

			// Handle system messages separately
			if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
				systemContent = convertBifrostMessageToAnthropicSystemContent(&msg)
				continue
			}

			// If there are pending reasoning blocks and this is a user message,
			// flush them into a separate assistant message first
			// (thinking blocks can only appear in assistant messages in Anthropic)
			if len(pendingReasoningContentBlocks) > 0 && (msg.Role == nil || *msg.Role == schemas.ResponsesInputMessageRoleUser) {
				// Copy the pending reasoning content blocks
				copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
				copy(copied, pendingReasoningContentBlocks)
				assistantReasoningMsg := AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
					Content: AnthropicContent{
						ContentBlocks: copied,
					},
				}
				anthropicMessages = append(anthropicMessages, assistantReasoningMsg)
				pendingReasoningContentBlocks = nil
			}

			// Regular user/assistant message
			anthropicMsg := convertBifrostMessageToAnthropicMessage(&msg, &pendingReasoningContentBlocks)
			if anthropicMsg != nil {
				anthropicMessages = append(anthropicMessages, *anthropicMsg)
			}

		case schemas.ResponsesMessageTypeReasoning:
			// Flush any pending tool results before processing reasoning
			flushPendingToolResults()

			// Handle reasoning as thinking content
			reasoningBlocks := convertBifrostReasoningToAnthropicThinking(&msg)
			pendingReasoningContentBlocks = append(pendingReasoningContentBlocks, reasoningBlocks...)

		case schemas.ResponsesMessageTypeFunctionCall:
			// Flush any pending tool results before processing function calls
			flushPendingToolResults()

			// When thinking blocks exist, they MUST come first before tool_use blocks
			// If we have pending reasoning blocks, we need to prepend them to the assistant message
			if currentAssistantMessage == nil {
				currentAssistantMessage = &AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
				}
			}

			// Prepend any pending reasoning blocks to ensure they come BEFORE tool_use blocks
			// This is required by Anthropic/Bedrock API: if an assistant message contains thinking blocks,
			// the first block must be thinking or redacted_thinking, NOT tool_use
			if len(pendingReasoningContentBlocks) > 0 {
				copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
				copy(copied, pendingReasoningContentBlocks)
				pendingToolCalls = append(copied, pendingToolCalls...)
				pendingReasoningContentBlocks = nil
			}

			toolUseBlock := convertBifrostFunctionCallToAnthropicToolUse(ctx, &msg)
			if toolUseBlock != nil {
				// If there was a previous assistant message (text only) that was just added,
				// and we have no pending tool calls yet, we should merge the tool call into it.
				// This handles the case where an assistant text message precedes tool calls.
				if len(pendingToolCalls) == 0 && len(anthropicMessages) > 0 {
					lastMsgIdx := len(anthropicMessages) - 1
					lastMsg := &anthropicMessages[lastMsgIdx]

					// Check if the last message is an assistant message that could have text
					if lastMsg.Role == AnthropicMessageRoleAssistant {
						hasToolUse := false
						for _, block := range lastMsg.Content.ContentBlocks {
							if block.Type == AnthropicContentBlockTypeToolUse {
								hasToolUse = true
								break
							}
						}
						// If the last assistant message has no tool_use blocks, merge the tool call into it
						if !hasToolUse {
							// Copy existing content blocks and append the tool_use
							existingBlocks := lastMsg.Content.ContentBlocks
							existingBlocks = append(existingBlocks, *toolUseBlock)
							lastMsg.Content = AnthropicContent{
								ContentBlocks: existingBlocks,
							}
							// Track the tool call ID
							if currentToolCallIDs == nil {
								currentToolCallIDs = make(map[string]bool)
							}
							if toolUseBlock.ID != nil {
								currentToolCallIDs[*toolUseBlock.ID] = true
							}
							// Use this message as the current one for subsequent tool calls
							pendingToolCalls = lastMsg.Content.ContentBlocks
							anthropicMessages = anthropicMessages[:lastMsgIdx] // Remove it, will be re-added on flush
							currentAssistantMessage = lastMsg
							continue
						}
					}
				}

				pendingToolCalls = append(pendingToolCalls, *toolUseBlock)

				// Track the tool call ID for matching with tool results
				if currentToolCallIDs == nil {
					currentToolCallIDs = make(map[string]bool)
				}
				if toolUseBlock.ID != nil {
					currentToolCallIDs[*toolUseBlock.ID] = true
				}
			}

		case schemas.ResponsesMessageTypeFunctionCallOutput:
			// Flush any pending tool calls first before processing tool results (with tracking)
			flushPendingToolCallsWithTracking()

			// Accumulate tool result blocks - they will be merged into a single user message
			// This is required because Anthropic/Bedrock expect all tool results for parallel
			// tool calls to be in the same user message, in the same order as the tool calls
			toolResultBlock := convertBifrostFunctionCallOutputToAnthropicToolResultBlock(&msg)
			if toolResultBlock != nil {
				pendingToolResultBlocks = append(pendingToolResultBlocks, *toolResultBlock)
			}

		case schemas.ResponsesMessageTypeItemReference:
			// Flush any pending tool results before processing item reference
			flushPendingToolResults()

			// Handle item reference as regular text message
			referenceMsg := convertBifrostItemReferenceToAnthropicMessage(&msg)
			if referenceMsg != nil {
				anthropicMessages = append(anthropicMessages, *referenceMsg)
			}

		case schemas.ResponsesMessageTypeComputerCall:
			// Flush any pending tool results before processing computer calls
			flushPendingToolResults()

			// Start accumulating computer tool calls for assistant message
			if currentAssistantMessage == nil {
				currentAssistantMessage = &AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
				}
			}

			// Prepend any pending reasoning blocks to ensure they come BEFORE tool_use blocks
			if len(pendingReasoningContentBlocks) > 0 {
				copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
				copy(copied, pendingReasoningContentBlocks)
				pendingToolCalls = append(copied, pendingToolCalls...)
				pendingReasoningContentBlocks = nil
			}

			computerToolUseBlock := convertBifrostComputerCallToAnthropicToolUse(&msg)
			if computerToolUseBlock != nil {
				pendingToolCalls = append(pendingToolCalls, *computerToolUseBlock)

				// Track the tool call ID for matching with tool results
				if currentToolCallIDs == nil {
					currentToolCallIDs = make(map[string]bool)
				}
				if computerToolUseBlock.ID != nil {
					currentToolCallIDs[*computerToolUseBlock.ID] = true
				}
			}

		case schemas.ResponsesMessageTypeMCPCall:
			// Check if this is a tool use (from assistant) or tool result (from user)
			if msg.ResponsesToolMessage != nil {
				if msg.ResponsesToolMessage.Name != nil {
					// Flush any pending tool results before processing MCP calls
					flushPendingToolResults()

					// This is a tool use call (assistant calling a tool)
					if currentAssistantMessage == nil {
						currentAssistantMessage = &AnthropicMessage{
							Role: AnthropicMessageRoleAssistant,
						}
					}

					// Prepend any pending reasoning blocks to ensure they come BEFORE tool_use blocks
					if len(pendingReasoningContentBlocks) > 0 {
						copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
						copy(copied, pendingReasoningContentBlocks)
						pendingToolCalls = append(copied, pendingToolCalls...)
						pendingReasoningContentBlocks = nil
					}

					mcpToolUseBlock := convertBifrostMCPCallToAnthropicToolUse(&msg)
					if mcpToolUseBlock != nil {
						pendingToolCalls = append(pendingToolCalls, *mcpToolUseBlock)

						// Track the tool call ID for matching with tool results
						if currentToolCallIDs == nil {
							currentToolCallIDs = make(map[string]bool)
						}
						if mcpToolUseBlock.ID != nil {
							currentToolCallIDs[*mcpToolUseBlock.ID] = true
						}
					}
				} else if msg.ResponsesToolMessage.CallID != nil {
					// This is a tool result (user providing result of tool execution)
					// Accumulate with other tool results
					mcpToolResultBlock := convertBifrostMCPCallOutputToAnthropicToolResultBlock(&msg)
					if mcpToolResultBlock != nil {
						pendingToolResultBlocks = append(pendingToolResultBlocks, *mcpToolResultBlock)
					}
				}
			}

		case schemas.ResponsesMessageTypeMCPApprovalRequest:
			// Flush any pending tool results before processing MCP approval requests
			flushPendingToolResults()

			// MCP approval request is OpenAI-specific for human-in-the-loop workflows
			// Convert to Anthropic's mcp_tool_use format (same as regular MCP calls)
			if currentAssistantMessage == nil {
				currentAssistantMessage = &AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
				}
			}

			// Prepend any pending reasoning blocks to ensure they come BEFORE tool_use blocks
			if len(pendingReasoningContentBlocks) > 0 {
				copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
				copy(copied, pendingReasoningContentBlocks)
				pendingToolCalls = append(copied, pendingToolCalls...)
				pendingReasoningContentBlocks = nil
			}

			mcpApprovalBlock := convertBifrostMCPApprovalToAnthropicToolUse(&msg)
			if mcpApprovalBlock != nil {
				pendingToolCalls = append(pendingToolCalls, *mcpApprovalBlock)

				// Track the tool call ID for matching with tool results
				if currentToolCallIDs == nil {
					currentToolCallIDs = make(map[string]bool)
				}
				if mcpApprovalBlock.ID != nil {
					currentToolCallIDs[*mcpApprovalBlock.ID] = true
				}
			}

		case schemas.ResponsesMessageTypeWebSearchCall:
			// Flush any pending tool results before processing web search calls
			flushPendingToolResults()

			// Web search calls need special handling: create server_tool_use + web_search_tool_result blocks
			webSearchBlocks := convertBifrostWebSearchCallToAnthropicBlocks(&msg)
			if len(webSearchBlocks) > 0 {
				// For web search, we create both server_tool_use and web_search_tool_result
				// These should appear in an assistant message
				if currentAssistantMessage == nil {
					currentAssistantMessage = &AnthropicMessage{
						Role: AnthropicMessageRoleAssistant,
					}
				}

				// Prepend any pending reasoning blocks to ensure they come BEFORE tool blocks
				if len(pendingReasoningContentBlocks) > 0 {
					copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
					copy(copied, pendingReasoningContentBlocks)
					pendingToolCalls = append(copied, pendingToolCalls...)
					pendingReasoningContentBlocks = nil
				}

				// Add the web search blocks (server_tool_use + web_search_tool_result)
				pendingToolCalls = append(pendingToolCalls, webSearchBlocks...)

				// Track the tool call ID for the server_tool_use block (first block)
				if len(webSearchBlocks) > 0 && webSearchBlocks[0].ID != nil {
					if currentToolCallIDs == nil {
						currentToolCallIDs = make(map[string]bool)
					}
					currentToolCallIDs[*webSearchBlocks[0].ID] = true
				}
			}

		// Handle other tool call types that are not natively supported by Anthropic
		case schemas.ResponsesMessageTypeFileSearchCall,
			schemas.ResponsesMessageTypeCodeInterpreterCall,
			schemas.ResponsesMessageTypeLocalShellCall,
			schemas.ResponsesMessageTypeCustomToolCall,
			schemas.ResponsesMessageTypeImageGenerationCall:
			// Flush any pending tool results before processing unsupported tool calls
			flushPendingToolResults()

			// Convert unsupported tool calls to regular text messages
			unsupportedToolMsg := convertBifrostUnsupportedToolCallToAnthropicMessage(&msg, msgType)
			if unsupportedToolMsg != nil {
				anthropicMessages = append(anthropicMessages, *unsupportedToolMsg)
			}

		case schemas.ResponsesMessageTypeComputerCallOutput:
			// Flush any pending tool calls first before processing tool results (with tracking)
			flushPendingToolCallsWithTracking()

			// Accumulate computer call output with other tool results
			computerResultBlock := convertBifrostComputerCallOutputToAnthropicToolResultBlock(&msg)
			if computerResultBlock != nil {
				pendingToolResultBlocks = append(pendingToolResultBlocks, *computerResultBlock)
			}

		case schemas.ResponsesMessageTypeLocalShellCallOutput,
			schemas.ResponsesMessageTypeCustomToolCallOutput:
			// Handle tool outputs as user messages
			toolOutputMsg := convertBifrostToolOutputToAnthropicMessage(&msg)
			if toolOutputMsg != nil {
				anthropicMessages = append(anthropicMessages, *toolOutputMsg)
			}

		default:
			// Skip unknown message types or log them for debugging
			continue
		}
	}

	// Flush any remaining pending tool results
	flushPendingToolResults()

	// Flush any remaining pending tool calls (with tracking)
	flushPendingToolCallsWithTracking()

	return anthropicMessages, systemContent
}

// Helper function to convert Anthropic system content to Bifrost messages
func convertAnthropicSystemToBifrostMessages(systemContent *AnthropicContent) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage

	if systemContent.ContentStr != nil && *systemContent.ContentStr != "" {
		bifrostMessages = append(bifrostMessages, schemas.ResponsesMessage{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: systemContent.ContentStr,
			},
		})
	} else if systemContent.ContentBlocks != nil {
		contentBlocks := []schemas.ResponsesMessageContentBlock{}
		for _, block := range systemContent.ContentBlocks {
			if block.Text != nil { // System messages will only have text content
				contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
					Type:         schemas.ResponsesInputMessageContentBlockTypeText,
					Text:         block.Text,
					CacheControl: block.CacheControl,
				})
			}
		}
		if len(contentBlocks) > 0 {
			bifrostMessages = append(bifrostMessages, schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: contentBlocks,
				},
			})
		}
	}

	return bifrostMessages
}

// Helper function to convert a single Anthropic message to Bifrost messages
func convertSingleAnthropicMessageToBifrostMessages(ctx *schemas.BifrostContext, msg *AnthropicMessage, isOutputMessage bool, structuredOutputToolName string) []schemas.ResponsesMessage {
	// Determine if this message should use output types based on role
	// Assistant messages in conversation history should use output_text
	isOutput := isOutputMessage || msg.Role == AnthropicMessageRoleAssistant

	// Handle text content (simple case)
	if msg.Content.ContentStr != nil {
		roleVal := schemas.ResponsesMessageRoleType(msg.Role)
		return []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: &roleVal,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: msg.Content.ContentStr,
				},
			},
		}
	}

	// Handle content blocks
	if msg.Content.ContentBlocks != nil {
		roleVal := schemas.ResponsesMessageRoleType(msg.Role)
		return convertAnthropicContentBlocksToResponsesMessages(ctx, msg.Content.ContentBlocks, &roleVal, isOutput, structuredOutputToolName)
	}

	return []schemas.ResponsesMessage{}
}

// Helper function to convert a single Anthropic message to Bifrost messages, grouping text and tool calls
// This keeps assistant messages with mixed text and tool_use blocks together
func convertSingleAnthropicMessageToBifrostMessagesGrouped(msg *AnthropicMessage, isOutputMessage bool, structuredOutputToolName string) []schemas.ResponsesMessage {
	// Determine if this message should use output types based on role
	// Assistant messages in conversation history should use output_text
	isOutput := isOutputMessage || msg.Role == AnthropicMessageRoleAssistant

	// Handle text content (simple case)
	if msg.Content.ContentStr != nil {
		roleVal := schemas.ResponsesMessageRoleType(msg.Role)
		return []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: &roleVal,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: msg.Content.ContentStr,
				},
			},
		}
	}

	// Handle content blocks with grouping for text and tool calls
	if msg.Content.ContentBlocks != nil {
		roleVal := schemas.ResponsesMessageRoleType(msg.Role)
		return convertAnthropicContentBlocksToResponsesMessagesGrouped(msg.Content.ContentBlocks, &roleVal, isOutput)
	}

	return []schemas.ResponsesMessage{}
}

// Helper function to convert Anthropic content blocks to Bifrost ResponsesMessages, grouping text and tool_use blocks
func convertAnthropicContentBlocksToResponsesMessagesGrouped(contentBlocks []AnthropicContentBlock, role *schemas.ResponsesMessageRoleType, isOutputMessage bool) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage
	var accumulatedTextContent []schemas.ResponsesMessageContentBlock
	var pendingToolUseBlocks []*AnthropicContentBlock // Accumulate tool_use blocks

	// Process content blocks
	for _, block := range contentBlocks {
		switch block.Type {
		case AnthropicContentBlockTypeText:
			if block.Text != nil {
				if isOutputMessage {
					// For output messages, accumulate text blocks (don't emit immediately)
					accumulatedTextContent = append(accumulatedTextContent, schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: block.Text,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						},
					})
				} else {
					// For input messages, emit text immediately as separate message
					bifrostMsg := schemas.ResponsesMessage{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: role,
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type:         schemas.ResponsesOutputMessageContentTypeText,
									Text:         block.Text,
									CacheControl: block.CacheControl,
									ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
										LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
										Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
									},
								},
							},
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			}

		case AnthropicContentBlockTypeImage:
			// Don't emit accumulated text or tool_use blocks for images
			if block.Source != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{block.toBifrostResponsesImageBlock()},
					},
				}
				if isOutputMessage {
					bifrostMsg.ID = schemas.Ptr("msg_" + providerUtils.GetRandomString(50))
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}

		case AnthropicContentBlockTypeDocument:
			// Handle document blocks similar to images
			if block.Source != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{block.toBifrostResponsesDocumentBlock()},
					},
				}
				if isOutputMessage {
					bifrostMsg.ID = schemas.Ptr("msg_" + providerUtils.GetRandomString(50))
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}

		case AnthropicContentBlockTypeThinking:
			if block.Thinking != nil {
				bifrostMsg := schemas.ResponsesMessage{
					ID:   schemas.Ptr("rs_" + providerUtils.GetRandomString(50)),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					Role: role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
								Text:      block.Thinking,
								Signature: block.Signature,
							},
						},
					},
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}

		case AnthropicContentBlockTypeRedactedThinking:
			// Handle redacted thinking (encrypted content)
			if block.Data != nil {
				bifrostMsg := schemas.ResponsesMessage{
					ID:   schemas.Ptr("rs_" + providerUtils.GetRandomString(50)),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary:          []schemas.ResponsesReasoningSummary{},
						EncryptedContent: block.Data,
					},
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}

		case AnthropicContentBlockTypeToolUse:
			// Accumulate tool_use blocks to group them together
			if block.ID != nil && block.Name != nil {
				blockCopy := block
				pendingToolUseBlocks = append(pendingToolUseBlocks, &blockCopy)
			}

		case AnthropicContentBlockTypeToolResult:
			// Convert tool result to function call output message
			if block.ToolUseID != nil {
				if block.Content != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: block.ToolUseID,
						},
					}
					// Initialize the nested struct before any writes
					bifrostMsg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}

					if block.Content.ContentStr != nil {
						bifrostMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = block.Content.ContentStr
					} else if block.Content.ContentBlocks != nil {
						var toolMsgContentBlocks []schemas.ResponsesMessageContentBlock
						for _, contentBlock := range block.Content.ContentBlocks {
							switch contentBlock.Type {
							case AnthropicContentBlockTypeText:
								if contentBlock.Text != nil {
									var blockType schemas.ResponsesMessageContentBlockType
									if isOutputMessage {
										blockType = schemas.ResponsesOutputMessageContentTypeText
									} else {
										blockType = schemas.ResponsesInputMessageContentBlockTypeText
									}
									toolMsgContentBlocks = append(toolMsgContentBlocks, schemas.ResponsesMessageContentBlock{
										Type:         blockType,
										Text:         contentBlock.Text,
										CacheControl: contentBlock.CacheControl,
									})
								}
							case AnthropicContentBlockTypeImage:
								if contentBlock.Source != nil {
									toolMsgContentBlocks = append(toolMsgContentBlocks, contentBlock.toBifrostResponsesImageBlock())
								}
							}
						}
						bifrostMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = toolMsgContentBlocks
					}

					// Handle is_error from Anthropic
					if block.IsError != nil && *block.IsError {
						bifrostMsg.Status = schemas.Ptr("incomplete")
					}

					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			}

		case AnthropicContentBlockTypeServerToolUse:
			// Accumulate server tool use blocks
			if block.ID != nil && block.Name != nil {
				blockCopy := block
				pendingToolUseBlocks = append(pendingToolUseBlocks, &blockCopy)
			}

		case AnthropicContentBlockTypeMCPToolUse:
			// Accumulate MCP tool use blocks
			if block.ID != nil && block.Name != nil {
				blockCopy := block
				pendingToolUseBlocks = append(pendingToolUseBlocks, &blockCopy)
			}

		case AnthropicContentBlockTypeMCPToolResult:
			// Handle MCP tool results directly without flushing other blocks
			// MCP results will be emitted as separate messages

		case AnthropicContentBlockTypeWebSearchResult:
			// Find the corresponding web_search_call by tool_use_id and attach sources
			if block.ToolUseID != nil {
				attachWebSearchSourcesToCall(bifrostMessages, *block.ToolUseID, block, true)
			}
		}
	}

	// Flush any remaining pending blocks
	if len(accumulatedTextContent) > 0 {
		bifrostMsg := schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: role,
		}
		if isOutputMessage {
			bifrostMsg.ID = schemas.Ptr("msg_" + providerUtils.GetRandomString(50))
			bifrostMsg.Content = &schemas.ResponsesMessageContent{
				ContentBlocks: accumulatedTextContent,
			}
			bifrostMessages = append(bifrostMessages, bifrostMsg)
		}
	}

	// Emit any accumulated tool_use blocks as function_calls
	if len(pendingToolUseBlocks) > 0 {
		for _, toolBlock := range pendingToolUseBlocks {
			bifrostMsg := schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: toolBlock.ID,
					Name:   toolBlock.Name,
				},
			}
			if isOutputMessage {
				bifrostMsg.ID = schemas.Ptr("fc_" + providerUtils.GetRandomString(50))
			}

			// Check for computer tool use
			if toolBlock.Name != nil && *toolBlock.Name == string(AnthropicToolNameComputer) {
				bifrostMsg.Type = schemas.Ptr(schemas.ResponsesMessageTypeComputerCall)
				bifrostMsg.ResponsesToolMessage.Name = nil
				if inputMap, ok := toolBlock.Input.(map[string]interface{}); ok {
					bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
						ResponsesComputerToolCallAction: convertAnthropicToResponsesComputerAction(inputMap),
					}
				}
			} else if toolBlock.Name != nil && *toolBlock.Name == string(AnthropicToolNameWebSearch) {
				// Handle web_search tool
				bifrostMsg.Type = schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall)
				bifrostMsg.ResponsesToolMessage.Name = nil
				if inputMap, ok := toolBlock.Input.(map[string]interface{}); ok {
					if query, ok := inputMap["query"].(string); ok {
						bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
								Type:    "search",
								Query:   schemas.Ptr(query),
								Queries: []string{query}, // Anthropic uses single query
							},
						}
					}
				}
			} else {
				bifrostMsg.ResponsesToolMessage.Arguments = schemas.Ptr(schemas.JsonifyInput(toolBlock.Input))
			}

			bifrostMessages = append(bifrostMessages, bifrostMsg)
		}
	}

	return bifrostMessages
}

// Helper function to convert Anthropic content blocks to Bifrost ResponsesMessages
func convertAnthropicContentBlocksToResponsesMessages(ctx *schemas.BifrostContext, contentBlocks []AnthropicContentBlock, role *schemas.ResponsesMessageRoleType, isOutputMessage bool, structuredOutputToolName string) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage
	var reasoningContentBlocks []schemas.ResponsesMessageContentBlock

	// Process content blocks
	for _, block := range contentBlocks {
		switch block.Type {
		case AnthropicContentBlockTypeCompaction:
			if block.Content != nil {
				var summaryText string
				if block.Content.ContentStr != nil {
					summaryText = *block.Content.ContentStr
				}

				bifrostMsg := schemas.ResponsesMessage{
					ID:     schemas.Ptr("cmp_" + providerUtils.GetRandomString(50)),
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role:   role,
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type:         schemas.ResponsesOutputMessageContentTypeCompaction,
								CacheControl: block.CacheControl,
								ResponsesOutputMessageContentCompaction: &schemas.ResponsesOutputMessageContentCompaction{
									Summary: summaryText,
								},
							},
						},
					},
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}
		case AnthropicContentBlockTypeText:
			if block.Text != nil {
				var bifrostMsg schemas.ResponsesMessage
				if isOutputMessage {
					// For output messages, use ContentBlocks with ResponsesOutputMessageContentTypeText
					contentBlock := schemas.ResponsesMessageContentBlock{
						Type:         schemas.ResponsesOutputMessageContentTypeText,
						Text:         block.Text,
						CacheControl: block.CacheControl,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
						},
					}

					// Convert Anthropic citations to OpenAI annotations
					if block.Citations != nil && len(block.Citations.TextCitations) > 0 {
						annotations := make([]schemas.ResponsesOutputMessageContentTextAnnotation, len(block.Citations.TextCitations))
						fullText := ""
						if block.Text != nil {
							fullText = *block.Text
						}
						for i, citation := range block.Citations.TextCitations {
							annotations[i] = convertAnthropicCitationToAnnotation(citation, fullText)
						}

						contentBlock.ResponsesOutputMessageContentText = &schemas.ResponsesOutputMessageContentText{
							Annotations: annotations,
						}
					}

					bifrostMsg = schemas.ResponsesMessage{
						ID:     schemas.Ptr("msg_" + providerUtils.GetRandomString(50)),
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role:   role,
						Status: schemas.Ptr("completed"),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{contentBlock},
						},
					}
				} else {
					// For input messages, use ContentStr
					bifrostMsg = schemas.ResponsesMessage{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: role,
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type:         schemas.ResponsesInputMessageContentBlockTypeText,
									Text:         block.Text,
									CacheControl: block.CacheControl,
								},
							},
						},
					}
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}
		case AnthropicContentBlockTypeImage:
			if block.Source != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{block.toBifrostResponsesImageBlock()},
					},
				}
				if isOutputMessage {
					bifrostMsg.ID = schemas.Ptr("msg_" + providerUtils.GetRandomString(50))
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}
		case AnthropicContentBlockTypeDocument:
			if block.Source != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{block.toBifrostResponsesDocumentBlock()},
					},
				}
				if isOutputMessage {
					bifrostMsg.ID = schemas.Ptr("msg_" + providerUtils.GetRandomString(50))
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}
		case AnthropicContentBlockTypeThinking:
			if block.Thinking != nil {
				// Collect reasoning blocks to create a single reasoning message
				reasoningContentBlocks = append(reasoningContentBlocks, schemas.ResponsesMessageContentBlock{
					Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
					Text:      block.Thinking,
					Signature: block.Signature,
				})
			}
		case AnthropicContentBlockTypeRedactedThinking:
			if block.Data != nil {
				bifrostMsg := schemas.ResponsesMessage{
					ID:   schemas.Ptr("rs_" + providerUtils.GetRandomString(50)),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary:          []schemas.ResponsesReasoningSummary{},
						EncryptedContent: block.Data,
					},
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}
		case AnthropicContentBlockTypeToolUse:
			// Check if this is the structured output tool - if so, convert to text content
			if structuredOutputToolName != "" && block.Name != nil && *block.Name == structuredOutputToolName {
				// This is a structured output tool - convert to text message
				var jsonStr string
				if block.Input != nil {
					jsonStr = schemas.JsonifyInput(block.Input)
				} else {
					jsonStr = "{}"
				}

				contentBlock := schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &jsonStr,
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
					},
				}

				bifrostMsg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role:   role,
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{contentBlock},
					},
				}
				if isOutputMessage {
					bifrostMsg.ID = schemas.Ptr("msg_" + providerUtils.GetRandomString(50))
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			} else {
				// Convert tool use to function call message
				if block.ID != nil && block.Name != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: block.ID,
							Name:   block.Name,
						},
					}
					if isOutputMessage {
						bifrostMsg.ID = schemas.Ptr("fc_" + providerUtils.GetRandomString(50))
					}

					// here need to check for computer tool use
					if block.Name != nil && *block.Name == string(AnthropicToolNameComputer) {
						bifrostMsg.Type = schemas.Ptr(schemas.ResponsesMessageTypeComputerCall)
						bifrostMsg.ResponsesToolMessage.Name = nil
						if inputMap, ok := block.Input.(map[string]interface{}); ok {
							bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
								ResponsesComputerToolCallAction: convertAnthropicToResponsesComputerAction(inputMap),
							}
						}
					} else {
						bifrostMsg.ResponsesToolMessage.Arguments = schemas.Ptr(schemas.JsonifyInput(block.Input))
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			}
		case AnthropicContentBlockTypeToolResult:
			// Convert tool result to function call output message
			if block.ToolUseID != nil {
				if block.Content != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: block.ToolUseID,
						},
					}
					// Initialize the nested struct before any writes
					bifrostMsg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}

					if block.Content.ContentStr != nil {
						bifrostMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = block.Content.ContentStr
					} else if block.Content.ContentBlocks != nil {
						var toolMsgContentBlocks []schemas.ResponsesMessageContentBlock
						for _, contentBlock := range block.Content.ContentBlocks {
							switch contentBlock.Type {
							case AnthropicContentBlockTypeText:
								if contentBlock.Text != nil {
									var blockType schemas.ResponsesMessageContentBlockType
									if isOutputMessage {
										blockType = schemas.ResponsesOutputMessageContentTypeText
									} else {
										blockType = schemas.ResponsesInputMessageContentBlockTypeText
									}
									toolMsgContentBlocks = append(toolMsgContentBlocks, schemas.ResponsesMessageContentBlock{
										Type:         blockType,
										Text:         contentBlock.Text,
										CacheControl: contentBlock.CacheControl,
									})
								}
							case AnthropicContentBlockTypeImage:
								if contentBlock.Source != nil {
									toolMsgContentBlocks = append(toolMsgContentBlocks, contentBlock.toBifrostResponsesImageBlock())
								}
							}
						}
						bifrostMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = toolMsgContentBlocks
					}

					// Handle is_error from Anthropic
					if block.IsError != nil && *block.IsError {
						bifrostMsg.Status = schemas.Ptr("incomplete")
					}

					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			}

		case AnthropicContentBlockTypeServerToolUse:
			// Check if it's a web_search tool
			if block.Name != nil && *block.Name == string(AnthropicToolNameWebSearch) {
				bifrostMsg := schemas.ResponsesMessage{
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status:               schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{},
				}

				// Extract query from input
				if block.Input != nil {
					if inputMap, ok := block.Input.(map[string]interface{}); ok {
						if query, ok := inputMap["query"].(string); ok {
							bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
								ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
									Type:    "search",
									Query:   schemas.Ptr(query),
									Queries: []string{query}, // Anthropic uses single query
								},
							}
						}
					}
				}

				if isOutputMessage {
					bifrostMsg.ID = block.ID
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			}

		case AnthropicContentBlockTypeWebSearchToolResult:
			// Find the corresponding web_search_call by tool_use_id
			if block.ToolUseID != nil {
				attachWebSearchSourcesToCall(bifrostMessages, *block.ToolUseID, block, true)
			}
		case AnthropicContentBlockTypeMCPToolUse:
			// Convert MCP tool use to MCP call (assistant's tool call)
			if block.ID != nil && block.Name != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
					ID:   block.ID,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Name:      block.Name,
						Arguments: schemas.Ptr(schemas.JsonifyInput(block.Input)),
					},
				}
				if block.ServerName != nil {
					bifrostMsg.ResponsesToolMessage.ResponsesMCPToolCall = &schemas.ResponsesMCPToolCall{
						ServerLabel: *block.ServerName,
					}
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}
		case AnthropicContentBlockTypeMCPToolResult:
			// Convert MCP tool result to MCP call (user's tool result)
			if block.ToolUseID != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: block.ToolUseID,
					},
				}
				if isOutputMessage {
					bifrostMsg.ID = schemas.Ptr("msg_" + providerUtils.GetRandomString(50))
				}
				// Initialize the nested struct before any writes
				bifrostMsg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}

				if block.Content != nil {
					if block.Content.ContentStr != nil {
						bifrostMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = block.Content.ContentStr
					} else if block.Content.ContentBlocks != nil {
						var toolMsgContentBlocks []schemas.ResponsesMessageContentBlock
						for _, contentBlock := range block.Content.ContentBlocks {
							if contentBlock.Type == AnthropicContentBlockTypeText {
								if contentBlock.Text != nil {
									var blockType schemas.ResponsesMessageContentBlockType
									if isOutputMessage {
										blockType = schemas.ResponsesOutputMessageContentTypeText
									} else {
										blockType = schemas.ResponsesInputMessageContentBlockTypeText
									}
									toolMsgContentBlocks = append(toolMsgContentBlocks, schemas.ResponsesMessageContentBlock{
										Type:         blockType,
										Text:         contentBlock.Text,
										CacheControl: contentBlock.CacheControl,
									})
								}
							}
						}
						bifrostMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = toolMsgContentBlocks
					}
				}
				bifrostMessages = append(bifrostMessages, bifrostMsg)
			}
		default:
			// Handle other block types if needed
		}
	}

	// Handle reasoning blocks - prepend reasoning message if we collected any
	// This ensures reasoning comes before any text/tool blocks (Bedrock compatibility)
	if len(reasoningContentBlocks) > 0 {
		reasoningMessage := schemas.ResponsesMessage{
			ID:   schemas.Ptr("rs_" + providerUtils.GetRandomString(50)),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			},
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: reasoningContentBlocks,
			},
		}
		// Prepend the reasoning message to the start of the messages list
		// This ensures reasoning comes before text/tool responses
		bifrostMessages = append([]schemas.ResponsesMessage{reasoningMessage}, bifrostMessages...)
	}

	return bifrostMessages
}

// Helper functions for converting individual Bifrost message types to Anthropic messages
// convertBifrostMessageToAnthropicSystemContent converts a Bifrost system message to Anthropic system content
func convertBifrostMessageToAnthropicSystemContent(msg *schemas.ResponsesMessage) *AnthropicContent {
	if msg.Content != nil {
		if msg.Content.ContentStr != nil {
			return &AnthropicContent{
				ContentStr: msg.Content.ContentStr,
			}
		} else if msg.Content.ContentBlocks != nil {
			contentBlocks := convertBifrostContentBlocksToAnthropic(msg.Content.ContentBlocks)
			if len(contentBlocks) > 0 {
				return &AnthropicContent{
					ContentBlocks: contentBlocks,
				}
			}
		}
	}
	return nil
}

// convertBifrostMessageToAnthropicMessage converts a regular Bifrost message to Anthropic message
func convertBifrostMessageToAnthropicMessage(msg *schemas.ResponsesMessage, pendingReasoningContentBlocks *[]AnthropicContentBlock) *AnthropicMessage {
	anthropicMsg := AnthropicMessage{}

	// Set role
	if msg.Role != nil {
		switch *msg.Role {
		case schemas.ResponsesInputMessageRoleUser:
			anthropicMsg.Role = AnthropicMessageRoleUser
		case schemas.ResponsesInputMessageRoleAssistant:
			anthropicMsg.Role = AnthropicMessageRoleAssistant
		default:
			anthropicMsg.Role = AnthropicMessageRoleUser // Default fallback
		}
	} else {
		anthropicMsg.Role = AnthropicMessageRoleUser // Default fallback
	}

	// Add any pending reasoning content blocks to the message
	// Only add reasoning blocks to assistant messages (thinking blocks can only appear in assistant messages in Anthropic)
	if len(*pendingReasoningContentBlocks) > 0 && anthropicMsg.Role == AnthropicMessageRoleAssistant {
		// copy the pending reasoning content blocks
		copied := make([]AnthropicContentBlock, len(*pendingReasoningContentBlocks))
		copy(copied, *pendingReasoningContentBlocks)
		contentBlocks := copied
		*pendingReasoningContentBlocks = nil
		// Add content blocks after pending reasoning content blocks are added
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				contentBlocks = append(contentBlocks, AnthropicContentBlock{
					Type: AnthropicContentBlockTypeText,
					Text: msg.Content.ContentStr,
				})
			} else if msg.Content.ContentBlocks != nil {
				contentBlocks = append(contentBlocks, convertBifrostContentBlocksToAnthropic(msg.Content.ContentBlocks)...)
			}
		}
		anthropicMsg.Content = AnthropicContent{
			ContentBlocks: contentBlocks,
		}
	} else {
		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				anthropicMsg.Content = AnthropicContent{
					ContentStr: msg.Content.ContentStr,
				}
			} else if msg.Content.ContentBlocks != nil {
				contentBlocks := convertBifrostContentBlocksToAnthropic(msg.Content.ContentBlocks)
				if len(contentBlocks) > 0 {
					anthropicMsg.Content = AnthropicContent{
						ContentBlocks: contentBlocks,
					}
				}
			}
		}
	}

	return &anthropicMsg
}

// convertBifrostReasoningToAnthropicThinking converts a Bifrost reasoning message to Anthropic thinking blocks
func convertBifrostReasoningToAnthropicThinking(msg *schemas.ResponsesMessage) []AnthropicContentBlock {
	var thinkingBlocks []AnthropicContentBlock

	if msg.Content != nil && msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == schemas.ResponsesOutputMessageContentTypeReasoning && block.Text != nil {
				thinkingBlock := AnthropicContentBlock{
					Type:      AnthropicContentBlockTypeThinking,
					Thinking:  block.Text,
					Signature: block.Signature,
				}
				thinkingBlocks = append(thinkingBlocks, thinkingBlock)
			}
		}
	} else if msg.ResponsesReasoning != nil {
		if msg.ResponsesReasoning.Summary != nil {
			for _, reasoningContent := range msg.ResponsesReasoning.Summary {
				thinkingBlock := AnthropicContentBlock{
					Type:     AnthropicContentBlockTypeThinking,
					Thinking: &reasoningContent.Text,
				}
				thinkingBlocks = append(thinkingBlocks, thinkingBlock)
			}
		} else if msg.ResponsesReasoning.EncryptedContent != nil {
			thinkingBlock := AnthropicContentBlock{
				Type: AnthropicContentBlockTypeRedactedThinking,
				Data: msg.ResponsesReasoning.EncryptedContent,
			}
			thinkingBlocks = append(thinkingBlocks, thinkingBlock)
		}
	}

	return thinkingBlocks
}

// convertBifrostFunctionCallToAnthropicToolUse converts a Bifrost function call to Anthropic tool use
func convertBifrostFunctionCallToAnthropicToolUse(ctx *schemas.BifrostContext, msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil {
		toolUseBlock := AnthropicContentBlock{
			Type: AnthropicContentBlockTypeToolUse,
		}

		if msg.ResponsesToolMessage.CallID != nil {
			toolUseBlock.ID = msg.ResponsesToolMessage.CallID
		}
		if msg.ResponsesToolMessage.Name != nil {
			toolUseBlock.Name = msg.ResponsesToolMessage.Name
		}

		// Parse arguments as JSON input
		if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
			argumentsJSON := *msg.ResponsesToolMessage.Arguments

			// Sanitize WebSearch tool arguments to remove both allowed_domains and blocked_domains
			// Anthropic only allows one or the other, not both
			// Only do this for Claude CLI
			if ctx != nil {
				if IsClaudeCodeRequest(ctx) {
					if msg.ResponsesToolMessage.Name != nil && *msg.ResponsesToolMessage.Name == "WebSearch" {
						argumentsJSON = sanitizeWebSearchArguments(argumentsJSON)
					}
				}
			}
			toolUseBlock.Input = parseJSONInput(argumentsJSON)
		}

		return &toolUseBlock
	}
	return nil
}

// convertBifrostFunctionCallOutputToAnthropicToolResultBlock converts a Bifrost function call output to a single tool result block
// This is used to accumulate multiple tool results into a single user message
func convertBifrostFunctionCallOutputToAnthropicToolResultBlock(msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil {
		toolResultBlock := AnthropicContentBlock{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: msg.ResponsesToolMessage.CallID,
		}

		if msg.ResponsesToolMessage.Output != nil {
			toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
		}

		// Set is_error if there's an error message or the status indicates an error
		if msg.ResponsesToolMessage.Error != nil && *msg.ResponsesToolMessage.Error != "" {
			toolResultBlock.IsError = schemas.Ptr(true)
			if toolResultBlock.Content == nil {
				toolResultBlock.Content = &AnthropicContent{
					ContentStr: msg.ResponsesToolMessage.Error,
				}
			}
		} else if msg.Status != nil && *msg.Status == "incomplete" {
			toolResultBlock.IsError = schemas.Ptr(true)
		}

		return &toolResultBlock
	}
	return nil
}

// convertBifrostComputerCallOutputToAnthropicToolResultBlock converts a Bifrost computer call output to a single tool result block
// This is used to accumulate multiple tool results into a single user message
func convertBifrostComputerCallOutputToAnthropicToolResultBlock(msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
		toolResultBlock := AnthropicContentBlock{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: msg.ResponsesToolMessage.CallID,
		}

		// Handle output
		if msg.ResponsesToolMessage.Output != nil {
			toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
		}

		// Set is_error if there's an error message or the status indicates an error
		if msg.ResponsesToolMessage.Error != nil && *msg.ResponsesToolMessage.Error != "" {
			toolResultBlock.IsError = schemas.Ptr(true)
			if toolResultBlock.Content == nil {
				toolResultBlock.Content = &AnthropicContent{
					ContentStr: msg.ResponsesToolMessage.Error,
				}
			}
		} else if msg.Status != nil && *msg.Status == "incomplete" {
			toolResultBlock.IsError = schemas.Ptr(true)
		}

		return &toolResultBlock
	}
	return nil
}

// convertBifrostMCPCallOutputToAnthropicToolResultBlock converts a Bifrost MCP call output to a single tool result block
// This is used to accumulate multiple tool results into a single user message
func convertBifrostMCPCallOutputToAnthropicToolResultBlock(msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
		toolResultBlock := AnthropicContentBlock{
			Type:      AnthropicContentBlockTypeMCPToolResult,
			ToolUseID: msg.ResponsesToolMessage.CallID,
		}

		// Handle output
		if msg.ResponsesToolMessage.Output != nil {
			toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
		}

		// Set is_error if there's an error message or the status indicates an error
		if msg.ResponsesToolMessage.Error != nil && *msg.ResponsesToolMessage.Error != "" {
			toolResultBlock.IsError = schemas.Ptr(true)
			if toolResultBlock.Content == nil {
				toolResultBlock.Content = &AnthropicContent{
					ContentStr: msg.ResponsesToolMessage.Error,
				}
			}
		} else if msg.Status != nil && *msg.Status == "incomplete" {
			toolResultBlock.IsError = schemas.Ptr(true)
		}

		return &toolResultBlock
	}
	return nil
}

// convertBifrostItemReferenceToAnthropicMessage converts a Bifrost item reference to Anthropic message
func convertBifrostItemReferenceToAnthropicMessage(msg *schemas.ResponsesMessage) *AnthropicMessage {
	if msg.Content != nil && msg.Content.ContentStr != nil {
		referenceMsg := AnthropicMessage{
			Role: AnthropicMessageRoleUser, // Default to user for references
		}
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleAssistant {
			referenceMsg.Role = AnthropicMessageRoleAssistant
		}

		referenceMsg.Content = AnthropicContent{
			ContentStr: msg.Content.ContentStr,
		}

		return &referenceMsg
	}
	return nil
}

// convertBifrostComputerCallToAnthropicToolUse converts a Bifrost computer call to Anthropic tool use
func convertBifrostComputerCallToAnthropicToolUse(msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil {
		toolUseBlock := AnthropicContentBlock{
			Type: AnthropicContentBlockTypeToolUse,
			Name: schemas.Ptr(string(AnthropicToolNameComputer)),
		}
		if msg.ResponsesToolMessage.CallID != nil {
			toolUseBlock.ID = msg.ResponsesToolMessage.CallID
		}
		if msg.ResponsesToolMessage.Name != nil {
			toolUseBlock.Name = msg.ResponsesToolMessage.Name
		}

		if msg.ResponsesToolMessage.Action != nil && msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
			toolUseBlock.Input = convertResponsesToAnthropicComputerAction(msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction)
		}

		return &toolUseBlock
	}
	return nil
}

// convertBifrostMCPCallToAnthropicToolUse converts a Bifrost MCP call to Anthropic tool use
func convertBifrostMCPCallToAnthropicToolUse(msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
		toolUseBlock := AnthropicContentBlock{
			Type: AnthropicContentBlockTypeMCPToolUse,
		}

		if msg.ID != nil {
			toolUseBlock.ID = msg.ID
		}
		toolUseBlock.Name = msg.ResponsesToolMessage.Name

		// Set server name if present
		if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
			toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
		}

		// Parse arguments as JSON input
		if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
			toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
		}

		return &toolUseBlock
	}
	return nil
}

// convertBifrostMCPCallOutputToAnthropicMessage converts a Bifrost MCP call output to Anthropic message
func convertBifrostMCPCallOutputToAnthropicMessage(msg *schemas.ResponsesMessage) *AnthropicMessage {
	toolResultBlock := AnthropicContentBlock{
		Type: AnthropicContentBlockTypeMCPToolResult,
		ID:   msg.ResponsesToolMessage.CallID,
	}

	if msg.ResponsesToolMessage.Output != nil {
		toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
	}

	return &AnthropicMessage{
		Role: AnthropicMessageRoleUser,
		Content: AnthropicContent{
			ContentBlocks: []AnthropicContentBlock{toolResultBlock},
		},
	}
}

// convertBifrostMCPApprovalToAnthropicToolUse converts a Bifrost MCP approval request to Anthropic tool use
func convertBifrostMCPApprovalToAnthropicToolUse(msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
		toolUseBlock := AnthropicContentBlock{
			Type: AnthropicContentBlockTypeMCPToolUse,
		}

		if msg.ID != nil {
			toolUseBlock.ID = msg.ID
		}
		toolUseBlock.Name = msg.ResponsesToolMessage.Name

		// Set server name if present
		if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
			toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
		}

		// Parse arguments as JSON input
		if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
			toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
		}

		return &toolUseBlock
	}
	return nil
}

// convertBifrostWebSearchCallToAnthropicBlocks converts a Bifrost web_search_call to Anthropic server_tool_use and web_search_tool_result blocks
func convertBifrostWebSearchCallToAnthropicBlocks(msg *schemas.ResponsesMessage) []AnthropicContentBlock {
	if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.Action == nil || msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction == nil {
		return nil
	}

	var blocks []AnthropicContentBlock
	action := msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction

	// 1. Create server_tool_use block for the web search
	serverToolUseBlock := AnthropicContentBlock{
		Type: AnthropicContentBlockTypeServerToolUse,
		Name: schemas.Ptr("web_search"),
	}

	if msg.ID != nil {
		serverToolUseBlock.ID = msg.ID
	}

	// Extract the query from the action
	if action.Query != nil {
		input := map[string]interface{}{
			"query": *action.Query,
		}
		serverToolUseBlock.Input = input
	}

	blocks = append(blocks, serverToolUseBlock)

	// 2. Create web_search_tool_result block if sources are present
	if len(action.Sources) > 0 {
		var resultBlocks []AnthropicContentBlock
		for _, source := range action.Sources {
			if source.URL != "" {
				resultBlock := AnthropicContentBlock{
					Type:             AnthropicContentBlockTypeWebSearchResult,
					URL:              schemas.Ptr(source.URL),
					EncryptedContent: source.EncryptedContent,
					PageAge:          source.PageAge,
				}
				if source.Title != nil {
					resultBlock.Title = source.Title
				} else if source.URL != "" {
					resultBlock.Title = schemas.Ptr(source.URL)
				}
				resultBlocks = append(resultBlocks, resultBlock)
			}
		}

		if len(resultBlocks) > 0 {
			webSearchResultBlock := AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeWebSearchToolResult,
				ToolUseID: msg.ID,
				Content: &AnthropicContent{
					ContentBlocks: resultBlocks,
				},
			}
			blocks = append(blocks, webSearchResultBlock)
		}
	}

	return blocks
}

// convertBifrostUnsupportedToolCallToAnthropicMessage converts unsupported tool calls to text messages
func convertBifrostUnsupportedToolCallToAnthropicMessage(msg *schemas.ResponsesMessage, msgType schemas.ResponsesMessageType) *AnthropicMessage {
	if msg.ResponsesToolMessage != nil {
		var description string
		if msg.ResponsesToolMessage.Name != nil {
			description = fmt.Sprintf("Tool call: %s", *msg.ResponsesToolMessage.Name)
			if msg.ResponsesToolMessage.Arguments != nil {
				description += fmt.Sprintf(" with arguments: %s", *msg.ResponsesToolMessage.Arguments)
			}
		} else {
			description = fmt.Sprintf("Tool call of type: %s", msgType)
		}

		return &AnthropicMessage{
			Role: AnthropicMessageRoleAssistant,
			Content: AnthropicContent{
				ContentStr: &description,
			},
		}
	}
	return nil
}

// convertBifrostComputerCallOutputToAnthropicMessage converts a Bifrost computer call output to Anthropic message
func convertBifrostComputerCallOutputToAnthropicMessage(msg *schemas.ResponsesMessage) *AnthropicMessage {
	if msg.ResponsesToolMessage != nil {
		toolResultBlock := AnthropicContentBlock{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: msg.ResponsesToolMessage.CallID,
		}

		if msg.ResponsesToolMessage.Output != nil {
			toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
		}

		return &AnthropicMessage{
			Role: AnthropicMessageRoleUser,
			Content: AnthropicContent{
				ContentBlocks: []AnthropicContentBlock{toolResultBlock},
			},
		}
	}
	return nil
}

// convertBifrostToolOutputToAnthropicMessage converts tool outputs to user messages
func convertBifrostToolOutputToAnthropicMessage(msg *schemas.ResponsesMessage) *AnthropicMessage {
	if msg.ResponsesToolMessage != nil {
		var outputText string
		// Try to extract output text based on tool type
		if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
			outputText = *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
		}

		if outputText != "" {
			return &AnthropicMessage{
				Role: AnthropicMessageRoleUser,
				Content: AnthropicContent{
					ContentStr: &outputText,
				},
			}
		}
	}
	return nil
}

// convertAnthropicToolToBifrost converts AnthropicTool to schemas.Tool
func convertAnthropicToolToBifrost(tool *AnthropicTool) *schemas.ResponsesTool {
	if tool == nil {
		return nil
	}

	// Handle special tool types first
	if tool.Type != nil {
		switch *tool.Type {
		case AnthropicToolTypeComputer20250124, AnthropicToolTypeComputer20251124:
			bifrostTool := &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeComputerUsePreview,
			}
			if tool.AnthropicToolComputerUse != nil {
				bifrostTool.ResponsesToolComputerUsePreview = &schemas.ResponsesToolComputerUsePreview{
					Environment: "browser", // Default environment
				}
				if tool.AnthropicToolComputerUse.DisplayWidthPx != nil {
					bifrostTool.ResponsesToolComputerUsePreview.DisplayWidth = *tool.AnthropicToolComputerUse.DisplayWidthPx
				}
				if tool.AnthropicToolComputerUse.DisplayHeightPx != nil {
					bifrostTool.ResponsesToolComputerUsePreview.DisplayHeight = *tool.AnthropicToolComputerUse.DisplayHeightPx
				}
				if tool.AnthropicToolComputerUse.EnableZoom != nil {
					bifrostTool.ResponsesToolComputerUsePreview.EnableZoom = tool.AnthropicToolComputerUse.EnableZoom
				}
			}
			return bifrostTool

		case AnthropicToolTypeWebSearch20250305:
			bifrostTool := &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeWebSearch,
			}
			if tool.AnthropicToolWebSearch != nil {
				bifrostTool.ResponsesToolWebSearch = &schemas.ResponsesToolWebSearch{
					Filters: &schemas.ResponsesToolWebSearchFilters{
						AllowedDomains: tool.AnthropicToolWebSearch.AllowedDomains,
						BlockedDomains: tool.AnthropicToolWebSearch.BlockedDomains,
					},
				}
				if tool.AnthropicToolWebSearch.MaxUses != nil {
					bifrostTool.ResponsesToolWebSearch.MaxUses = tool.AnthropicToolWebSearch.MaxUses
				}
				if tool.AnthropicToolWebSearch.UserLocation != nil {
					bifrostTool.ResponsesToolWebSearch.UserLocation = &schemas.ResponsesToolWebSearchUserLocation{
						Type:     tool.AnthropicToolWebSearch.UserLocation.Type,
						City:     tool.AnthropicToolWebSearch.UserLocation.City,
						Country:  tool.AnthropicToolWebSearch.UserLocation.Country,
						Timezone: tool.AnthropicToolWebSearch.UserLocation.Timezone,
					}
				}
			}

			return bifrostTool

		case AnthropicToolTypeBash20250124:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeLocalShell,
			}

		case AnthropicToolTypeTextEditor20250124:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250124),
				Name: &tool.Name,
			}
		case AnthropicToolTypeTextEditor20250429:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250429),
				Name: &tool.Name,
			}
		case AnthropicToolTypeTextEditor20250728:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250728),
				Name: &tool.Name,
			}
		}
	}

	// Handle custom/default tool type (function)
	bifrostTool := &schemas.ResponsesTool{
		Type:        schemas.ResponsesToolTypeFunction,
		Name:        &tool.Name,
		Description: tool.Description,
	}

	if tool.InputSchema != nil || tool.Strict != nil {
		bifrostTool.ResponsesToolFunction = &schemas.ResponsesToolFunction{
			Parameters: tool.InputSchema,
			Strict:     tool.Strict,
		}
	}

	if tool.CacheControl != nil {
		bifrostTool.CacheControl = tool.CacheControl
	}

	return bifrostTool
}

// convertAnthropicToolChoiceToBifrost converts AnthropicToolChoice to schemas.ToolChoice
func convertAnthropicToolChoiceToBifrost(toolChoice *AnthropicToolChoice) *schemas.ResponsesToolChoice {
	if toolChoice == nil {
		return nil
	}

	bifrostToolChoice := &schemas.ResponsesToolChoice{}

	// Handle string format
	if toolChoice.Type != "" {
		switch toolChoice.Type {
		case "auto":
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeAuto))
		case "any":
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeAny))
		case "none":
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeNone))
		case "tool":
			// Handle forced tool choice with specific function name
			bifrostToolChoice.ResponsesToolChoiceStruct = &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeFunction,
				Name: &toolChoice.Name,
			}
			return bifrostToolChoice
		default:
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeAuto))
		}
	}

	return bifrostToolChoice
}

// flushPendingContentBlocks is a helper that flushes accumulated content blocks into an assistant message
func flushPendingContentBlocks(
	pendingContentBlocks []AnthropicContentBlock,
	currentAssistantMessage *AnthropicMessage,
	anthropicMessages []AnthropicMessage,
) ([]AnthropicContentBlock, *AnthropicMessage, []AnthropicMessage) {
	if len(pendingContentBlocks) > 0 && currentAssistantMessage != nil {
		// Copy the slice to avoid aliasing issues
		copied := make([]AnthropicContentBlock, len(pendingContentBlocks))
		copy(copied, pendingContentBlocks)
		currentAssistantMessage.Content = AnthropicContent{
			ContentBlocks: copied,
		}
		anthropicMessages = append(anthropicMessages, *currentAssistantMessage)
		// Return nil values to indicate flushed state
		return nil, nil, anthropicMessages
	}
	// Return unchanged values if no flush was needed
	return pendingContentBlocks, currentAssistantMessage, anthropicMessages
}

// convertToolOutputToAnthropicContent converts tool output to Anthropic content format
func convertToolOutputToAnthropicContent(output *schemas.ResponsesToolMessageOutputStruct) *AnthropicContent {
	if output == nil {
		return nil
	}

	if output.ResponsesToolCallOutputStr != nil {
		return &AnthropicContent{
			ContentStr: output.ResponsesToolCallOutputStr,
		}
	}

	if output.ResponsesFunctionToolCallOutputBlocks != nil {
		var resultBlocks []AnthropicContentBlock
		for _, block := range output.ResponsesFunctionToolCallOutputBlocks {
			if converted := convertContentBlockToAnthropic(block); converted != nil {
				resultBlocks = append(resultBlocks, *converted)
			}
		}
		if len(resultBlocks) > 0 {
			return &AnthropicContent{
				ContentBlocks: resultBlocks,
			}
		}
	}

	if output.ResponsesComputerToolCallOutput != nil && output.ResponsesComputerToolCallOutput.ImageURL != nil {
		imgBlock := ConvertToAnthropicImageBlock(schemas.ChatContentBlock{
			Type: schemas.ChatContentBlockTypeImage,
			ImageURLStruct: &schemas.ChatInputImage{
				URL: *output.ResponsesComputerToolCallOutput.ImageURL,
			},
		})
		return &AnthropicContent{
			ContentBlocks: []AnthropicContentBlock{imgBlock},
		}
	}

	return nil
}

// Helper function to convert Tool back to AnthropicTool
func convertBifrostToolToAnthropic(model string, tool *schemas.ResponsesTool) *AnthropicTool {
	if tool == nil {
		return nil
	}

	switch tool.Type {
	case schemas.ResponsesToolTypeComputerUsePreview:
		if tool.ResponsesToolComputerUsePreview != nil {
			computerToolType := AnthropicToolTypeComputer20250124
			if strings.Contains(model, "claude") && strings.Contains(model, "opus") && (strings.Contains(model, "4.5") || strings.Contains(model, "4-5")) {
				computerToolType = AnthropicToolTypeComputer20251124
			}
			return &AnthropicTool{
				Type: schemas.Ptr(computerToolType),
				Name: string(AnthropicToolNameComputer),
				AnthropicToolComputerUse: &AnthropicToolComputerUse{
					DisplayWidthPx:  schemas.Ptr(tool.ResponsesToolComputerUsePreview.DisplayWidth),
					DisplayHeightPx: schemas.Ptr(tool.ResponsesToolComputerUsePreview.DisplayHeight),
					DisplayNumber:   schemas.Ptr(1),
					EnableZoom:      tool.ResponsesToolComputerUsePreview.EnableZoom,
				},
			}
		}
	case schemas.ResponsesToolTypeWebSearch:
		anthropicTool := &AnthropicTool{
			Type:                   schemas.Ptr(AnthropicToolTypeWebSearch20250305),
			Name:                   string(AnthropicToolNameWebSearch),
			AnthropicToolWebSearch: &AnthropicToolWebSearch{},
		}
		if tool.ResponsesToolWebSearch != nil {
			if tool.ResponsesToolWebSearch.MaxUses != nil {
				anthropicTool.AnthropicToolWebSearch.MaxUses = tool.ResponsesToolWebSearch.MaxUses
			}
			if tool.ResponsesToolWebSearch.Filters != nil {
				anthropicTool.AnthropicToolWebSearch.AllowedDomains = tool.ResponsesToolWebSearch.Filters.AllowedDomains
				anthropicTool.AnthropicToolWebSearch.BlockedDomains = tool.ResponsesToolWebSearch.Filters.BlockedDomains
			}
			if tool.ResponsesToolWebSearch.UserLocation != nil {
				anthropicTool.AnthropicToolWebSearch.UserLocation = &AnthropicToolWebSearchUserLocation{
					Type:     tool.ResponsesToolWebSearch.UserLocation.Type,
					City:     tool.ResponsesToolWebSearch.UserLocation.City,
					Country:  tool.ResponsesToolWebSearch.UserLocation.Country,
					Timezone: tool.ResponsesToolWebSearch.UserLocation.Timezone,
				}
			}
		}

		return anthropicTool
	case schemas.ResponsesToolTypeLocalShell:
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeBash20250124),
			Name: string(AnthropicToolNameBash),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250124):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250124),
			Name: string(AnthropicToolNameTextEditor),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250429):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250429),
			Name: string(AnthropicToolNameTextEditor),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250728):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250728),
			Name: string(AnthropicToolNameTextEditor),
		}
	}

	anthropicTool := &AnthropicTool{}

	if tool.Name != nil {
		anthropicTool.Name = *tool.Name
	}

	if tool.Description != nil {
		anthropicTool.Description = tool.Description
	}

	// Convert parameters and strict from ToolFunction
	if tool.ResponsesToolFunction != nil {
		anthropicTool.InputSchema = tool.ResponsesToolFunction.Parameters
		anthropicTool.Strict = tool.ResponsesToolFunction.Strict
	}

	if tool.CacheControl != nil {
		anthropicTool.CacheControl = tool.CacheControl
	}

	return anthropicTool
}

// Helper function to convert ResponsesToolChoice back to AnthropicToolChoice
func convertResponsesToolChoiceToAnthropic(toolChoice *schemas.ResponsesToolChoice) *AnthropicToolChoice {
	if toolChoice == nil {
		return nil
	}
	// String-form choices (auto/any/none/required) have no struct payload.
	if toolChoice.ResponsesToolChoiceStruct == nil && toolChoice.ResponsesToolChoiceStr != nil {
		switch schemas.ResponsesToolChoiceType(*toolChoice.ResponsesToolChoiceStr) {
		case schemas.ResponsesToolChoiceTypeAuto:
			return &AnthropicToolChoice{Type: "auto"}
		case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &AnthropicToolChoice{Type: "any"}
		case schemas.ResponsesToolChoiceTypeNone:
			return &AnthropicToolChoice{Type: "none"}
		default:
			return nil
		}
	}

	if toolChoice.ResponsesToolChoiceStruct == nil {
		return nil
	}

	anthropicChoice := &AnthropicToolChoice{}

	var toolChoiceType *string
	if toolChoice.ResponsesToolChoiceStruct != nil {
		toolChoiceType = schemas.Ptr(string(toolChoice.ResponsesToolChoiceStruct.Type))
	} else {
		toolChoiceType = toolChoice.ResponsesToolChoiceStr
	}

	switch *toolChoiceType {
	case "auto":
		anthropicChoice.Type = "auto"
	case "required":
		anthropicChoice.Type = "any"
	case "function":
		// Handle function type - set as "tool" with specific function name
		if toolChoice.ResponsesToolChoiceStruct != nil && toolChoice.ResponsesToolChoiceStruct.Name != nil {
			anthropicChoice.Type = "tool"
			anthropicChoice.Name = *toolChoice.ResponsesToolChoiceStruct.Name
		}
		return anthropicChoice
	}

	// Legacy fallback: also check for Name field (for backward compatibility)
	if toolChoice.ResponsesToolChoiceStruct != nil && toolChoice.ResponsesToolChoiceStruct.Name != nil {
		anthropicChoice.Type = "tool"
		anthropicChoice.Name = *toolChoice.ResponsesToolChoiceStruct.Name
	}

	return anthropicChoice
}

// Helper function to convert ContentBlock to AnthropicContentBlock
func convertContentBlockToAnthropic(block schemas.ResponsesMessageContentBlock) *AnthropicContentBlock {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText:
		anthropicBlock := AnthropicContentBlock{}
		if block.Text != nil {
			anthropicBlock = AnthropicContentBlock{
				Type:         AnthropicContentBlockTypeText,
				Text:         block.Text,
				CacheControl: block.CacheControl,
			}
			if block.ResponsesOutputMessageContentText != nil && len(block.ResponsesOutputMessageContentText.Annotations) > 0 {
				anthropicBlock.Citations = &AnthropicCitations{
					TextCitations: make([]AnthropicTextCitation, len(block.ResponsesOutputMessageContentText.Annotations)),
				}
				for i, annotation := range block.ResponsesOutputMessageContentText.Annotations {
					anthropicBlock.Citations.TextCitations[i] = convertAnnotationToAnthropicCitation(annotation)
				}
			}
			return &anthropicBlock
		}
	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
			// Convert using the same logic as ConvertToAnthropicImageBlock
			chatBlock := schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeImage,
				ImageURLStruct: &schemas.ChatInputImage{
					URL: *block.ResponsesInputMessageContentBlockImage.ImageURL,
				},
				CacheControl: block.CacheControl,
			}
			anthropicBlock := ConvertToAnthropicImageBlock(chatBlock)
			return &anthropicBlock
		}
	case schemas.ResponsesOutputMessageContentTypeCompaction:
		if block.ResponsesOutputMessageContentCompaction != nil {
			return &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeCompaction,
				Content: &AnthropicContent{
					ContentStr: &block.ResponsesOutputMessageContentCompaction.Summary,
				},
				CacheControl: block.CacheControl,
			}
		}
	case schemas.ResponsesInputMessageContentBlockTypeFile:
		if block.ResponsesInputMessageContentBlockFile != nil {
			// Direct conversion without intermediate ChatContentBlock
			anthropicBlock := ConvertResponsesFileBlockToAnthropic(
				block.ResponsesInputMessageContentBlockFile,
				block.CacheControl,
				block.Citations,
			)
			return &anthropicBlock
		}
	case schemas.ResponsesOutputMessageContentTypeReasoning:
		if block.Text != nil {
			return &AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeThinking,
				Thinking:  block.Text,
				Signature: block.Signature,
			}
		}
	}
	return nil
}

// Helper to convert Bifrost content blocks slice to Anthropic content blocks
func convertBifrostContentBlocksToAnthropic(blocks []schemas.ResponsesMessageContentBlock) []AnthropicContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	var result []AnthropicContentBlock
	for _, block := range blocks {
		if converted := convertContentBlockToAnthropic(block); converted != nil {
			result = append(result, *converted)
		}
	}
	if len(result) > 0 {
		return result
	}
	return nil
}

func (block AnthropicContentBlock) toBifrostResponsesImageBlock() schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeImage,
		ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
			ImageURL: schemas.Ptr(getImageURLFromBlock(block)),
		},
		CacheControl: block.CacheControl,
	}
}

func (block AnthropicContentBlock) toBifrostResponsesDocumentBlock() schemas.ResponsesMessageContentBlock {
	resultBlock := schemas.ResponsesMessageContentBlock{
		Type:                                  schemas.ResponsesInputMessageContentBlockTypeFile,
		CacheControl:                          block.CacheControl,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{},
	}

	if block.Citations != nil && block.Citations.Config != nil {
		resultBlock.Citations = block.Citations.Config
	}

	// Set filename from title if available
	if block.Title != nil {
		resultBlock.ResponsesInputMessageContentBlockFile.Filename = block.Title
	}

	if block.Source == nil {
		return resultBlock
	}

	// Handle different source types
	switch block.Source.Type {
	case "url":
		// URL source
		if block.Source.URL != nil {
			resultBlock.ResponsesInputMessageContentBlockFile.FileURL = block.Source.URL
		}
	case "base64":
		// Base64 encoded data
		if block.Source.Data != nil {
			// Construct data URL with media type
			mediaType := "application/pdf"
			if block.Source.MediaType != nil {
				mediaType = *block.Source.MediaType
			}
			dataURL := *block.Source.Data
			if !strings.HasPrefix(dataURL, "data:") {
				dataURL = "data:" + mediaType + ";base64," + *block.Source.Data
			}
			resultBlock.ResponsesInputMessageContentBlockFile.FileData = &dataURL
		}
	case "text":
		// Plain text source
		if block.Source.Data != nil {
			resultBlock.ResponsesInputMessageContentBlockFile.FileType = schemas.Ptr("text/plain")
			resultBlock.ResponsesInputMessageContentBlockFile.FileData = block.Source.Data
		}
	}

	return resultBlock
}

// Helper functions for MCP tool/server conversion
// convertAnthropicMCPServerToBifrostTool converts a single Anthropic MCP server to a Bifrost ResponsesTool
func convertAnthropicMCPServerToBifrostTool(mcpServer *AnthropicMCPServer) *schemas.ResponsesTool {
	if mcpServer == nil {
		return nil
	}

	bifrostTool := &schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeMCP,
		ResponsesToolMCP: &schemas.ResponsesToolMCP{
			ServerLabel: mcpServer.Name,
		},
	}

	// Set server URL if present
	if mcpServer.URL != "" {
		bifrostTool.ResponsesToolMCP.ServerURL = schemas.Ptr(mcpServer.URL)
	}

	// Set authorization token if present
	if mcpServer.AuthorizationToken != nil {
		bifrostTool.ResponsesToolMCP.Authorization = mcpServer.AuthorizationToken
	}

	// Set allowed tools from tool configuration
	if mcpServer.ToolConfiguration != nil && len(mcpServer.ToolConfiguration.AllowedTools) > 0 {
		bifrostTool.ResponsesToolMCP.AllowedTools = &schemas.ResponsesToolMCPAllowedTools{
			ToolNames: mcpServer.ToolConfiguration.AllowedTools,
		}
	}

	return bifrostTool
}

// convertBifrostMCPToolToAnthropicServer converts a Bifrost MCP tool back to an Anthropic MCP server
func convertBifrostMCPToolToAnthropicServer(tool *schemas.ResponsesTool) *AnthropicMCPServer {
	if tool == nil || tool.Type != schemas.ResponsesToolTypeMCP || tool.ResponsesToolMCP == nil {
		return nil
	}

	mcpServer := &AnthropicMCPServer{
		Type: "url",
		Name: tool.ResponsesToolMCP.ServerLabel,
		ToolConfiguration: &AnthropicMCPToolConfig{
			Enabled: true,
		},
	}

	// Set server URL if present
	if tool.ResponsesToolMCP.ServerURL != nil {
		mcpServer.URL = *tool.ResponsesToolMCP.ServerURL
	}

	// Set allowed tools if present
	if tool.ResponsesToolMCP.AllowedTools != nil && len(tool.ResponsesToolMCP.AllowedTools.ToolNames) > 0 {
		mcpServer.ToolConfiguration.AllowedTools = tool.ResponsesToolMCP.AllowedTools.ToolNames
	}

	// Set authorization token if present
	if tool.ResponsesToolMCP.Authorization != nil {
		mcpServer.AuthorizationToken = tool.ResponsesToolMCP.Authorization
	}

	return mcpServer
}

// convertAnthropicCitationToAnnotation converts an Anthropic citation to an OpenAI annotation
// fullText is the complete text content of the message block, used to compute citation indices for web search results
func convertAnthropicCitationToAnnotation(citation AnthropicTextCitation, fullText string) schemas.ResponsesOutputMessageContentTextAnnotation {
	annotation := schemas.ResponsesOutputMessageContentTextAnnotation{
		Type:  string(citation.Type),
		Index: citation.DocumentIndex,
		Text:  schemas.Ptr(citation.CitedText),
	}

	// Map type-specific fields based on citation type
	switch citation.Type {
	case AnthropicCitationTypeCharLocation:
		// Character location fields
		annotation.StartCharIndex = citation.StartCharIndex
		annotation.EndCharIndex = citation.EndCharIndex
		annotation.Filename = citation.DocumentTitle
		annotation.FileID = citation.FileID

	case AnthropicCitationTypePageLocation:
		// Page location fields
		annotation.StartPageNumber = citation.StartPageNumber
		annotation.EndPageNumber = citation.EndPageNumber
		annotation.Filename = citation.DocumentTitle
		annotation.FileID = citation.FileID

	case AnthropicCitationTypeContentBlockLocation:
		// Content block location fields
		annotation.StartBlockIndex = citation.StartBlockIndex
		annotation.EndBlockIndex = citation.EndBlockIndex
		annotation.Filename = citation.DocumentTitle
		annotation.FileID = citation.FileID

	case AnthropicCitationTypeWebSearchResultLocation:
		// Web search result fields - map to OpenAI url_citation format
		annotation.Type = "url_citation"
		annotation.Title = citation.Title
		annotation.URL = citation.URL
		annotation.EncryptedIndex = citation.EncryptedIndex

		// Compute start_index and end_index by findin
		if fullText != "" && citation.URL != nil && *citation.URL != "" {
			startIdx := strings.Index(fullText, *citation.URL)
			if startIdx != -1 {
				endIdx := startIdx + len(*citation.URL)
				annotation.StartIndex = schemas.Ptr(startIdx)
				annotation.EndIndex = schemas.Ptr(endIdx)
			} else {
				// assign start_index and end_index to the entire text
				annotation.StartIndex = schemas.Ptr(0)
				annotation.EndIndex = schemas.Ptr(len(fullText))
			}
		}

	case AnthropicCitationTypeSearchResultLocation:
		// Search result location fields
		annotation.StartBlockIndex = citation.StartBlockIndex
		annotation.EndBlockIndex = citation.EndBlockIndex
		annotation.Title = citation.Title
		annotation.Source = citation.Source
	}

	return annotation
}

// convertAnnotationToAnthropicCitation converts an OpenAI annotation to an Anthropic citation
func convertAnnotationToAnthropicCitation(annotation schemas.ResponsesOutputMessageContentTextAnnotation) AnthropicTextCitation {
	citation := AnthropicTextCitation{
		Type:      AnthropicCitationType(annotation.Type),
		CitedText: "",
	}

	// Map common fields
	if annotation.Text != nil {
		citation.CitedText = *annotation.Text
	}

	// Map type-specific fields based on annotation type
	switch annotation.Type {
	case string(AnthropicCitationTypeCharLocation):
		// Character location
		citation.StartCharIndex = annotation.StartCharIndex
		citation.EndCharIndex = annotation.EndCharIndex
		citation.DocumentTitle = annotation.Filename
		citation.DocumentIndex = annotation.Index
		citation.FileID = annotation.FileID

	case string(AnthropicCitationTypePageLocation):
		// Page location
		citation.StartPageNumber = annotation.StartPageNumber
		citation.EndPageNumber = annotation.EndPageNumber
		citation.DocumentTitle = annotation.Filename
		citation.DocumentIndex = annotation.Index
		citation.FileID = annotation.FileID

	case string(AnthropicCitationTypeContentBlockLocation):
		// Content block location
		citation.StartBlockIndex = annotation.StartBlockIndex
		citation.EndBlockIndex = annotation.EndBlockIndex
		citation.DocumentTitle = annotation.Filename
		citation.DocumentIndex = annotation.Index
		citation.FileID = annotation.FileID

	case string(AnthropicCitationTypeWebSearchResultLocation):
		// Web search result
		citation.Title = annotation.Title
		citation.URL = annotation.URL
		citation.EncryptedIndex = annotation.EncryptedIndex

	case string(AnthropicCitationTypeSearchResultLocation):
		// Search result location
		citation.StartBlockIndex = annotation.StartBlockIndex
		citation.EndBlockIndex = annotation.EndBlockIndex
		citation.Title = annotation.Title
		citation.Source = annotation.Source

	case "url_citation":
		citation.Type = AnthropicCitationTypeWebSearchResultLocation
		citation.URL = annotation.URL
		citation.Title = annotation.Title
		citation.EncryptedIndex = annotation.EncryptedIndex

	case "file_citation", "container_file_citation", "file_path", "text_annotation":
		// OpenAI native types - map to char_location
		citation.Type = "char_location"
		citation.StartCharIndex = annotation.StartIndex
		citation.EndCharIndex = annotation.EndIndex
		citation.DocumentTitle = annotation.Filename
		citation.Title = annotation.Title
		citation.FileID = annotation.FileID
	}

	return citation
}

// convertResponsesToAnthropicComputerAction converts ResponsesComputerToolCallAction to Anthropic input map
func convertResponsesToAnthropicComputerAction(action *schemas.ResponsesComputerToolCallAction) map[string]any {
	input := map[string]any{}
	var actionStr string

	// Map action type from OpenAI to Anthropic format
	switch action.Type {
	case "screenshot":
		actionStr = "screenshot"

	case "click":
		// Map click with button variants
		if action.Button != nil {
			switch *action.Button {
			case "right":
				actionStr = "right_click"
			case "wheel":
				actionStr = "middle_click"
			default: // "left", "back", "forward" or others
				actionStr = "left_click"
			}
		} else {
			actionStr = "left_click"
		}
		// Add coordinates
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

	case "double_click":
		actionStr = "double_click"
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

	case "move":
		actionStr = "mouse_move"
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

	case "type":
		actionStr = "type"
		if action.Text != nil {
			input["text"] = *action.Text
		}

	case "keypress":
		actionStr = "key"
		if len(action.Keys) > 0 {
			// Convert array of keys to "key1+key2+..." format
			text := ""
			for i, key := range action.Keys {
				if i > 0 {
					text += "+"
				}
				text += key
			}
			input["text"] = text
		}

	case "scroll":
		actionStr = "scroll"
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

		// Handle scroll direction - Anthropic supports one direction at a time
		// If both ScrollX and ScrollY are present, use the one with larger absolute value
		scrollX := 0
		scrollY := 0
		if action.ScrollX != nil {
			scrollX = *action.ScrollX
		}
		if action.ScrollY != nil {
			scrollY = *action.ScrollY
		}

		if math.Abs(float64(scrollY)) >= math.Abs(float64(scrollX)) && scrollY != 0 {
			// Vertical scroll is dominant or only one present
			if scrollY > 0 {
				input["scroll_direction"] = "down"
				input["scroll_amount"] = scrollY / 100
			} else {
				input["scroll_direction"] = "up"
				input["scroll_amount"] = (-scrollY) / 100
			}
		} else if scrollX != 0 {
			// Horizontal scroll is dominant or only one present
			if scrollX > 0 {
				input["scroll_direction"] = "right"
				input["scroll_amount"] = scrollX / 100
			} else {
				input["scroll_direction"] = "left"
				input["scroll_amount"] = (-scrollX) / 100
			}
		}

	case "drag":
		actionStr = "left_click_drag"
		if len(action.Path) >= 2 {
			// Map first and last points as start and end coordinates
			input["start_coordinate"] = []int{action.Path[0].X, action.Path[0].Y}
			input["end_coordinate"] = []int{action.Path[len(action.Path)-1].X, action.Path[len(action.Path)-1].Y}
		}

	case "wait":
		actionStr = "wait"
		input["duration"] = 2

	case "zoom":
		actionStr = "zoom"
		// Anthropic zoom action expects region as [x1, y1, x2, y2]
		if len(action.Region) == 4 {
			input["region"] = action.Region
		}

	default:
		// Pass through any unknown action types
		actionStr = action.Type
	}

	input["action"] = actionStr

	return input
}

// convertAnthropicToResponsesComputerAction converts Anthropic input map to ResponsesComputerToolCallAction
func convertAnthropicToResponsesComputerAction(inputMap map[string]interface{}) *schemas.ResponsesComputerToolCallAction {
	action := &schemas.ResponsesComputerToolCallAction{}

	// Extract action type
	actionStr, ok := inputMap["action"].(string)
	if !ok {
		return action
	}

	// Map action type from Anthropic to OpenAI format
	switch actionStr {
	case "screenshot":
		action.Type = "screenshot"

	case "left_click":
		action.Type = "click"
		action.Button = schemas.Ptr("left")

	case "right_click":
		action.Type = "click"
		action.Button = schemas.Ptr("right")

	case "middle_click":
		action.Type = "click"
		action.Button = schemas.Ptr("wheel")

	case "double_click":
		action.Type = "double_click"

	case "mouse_move":
		action.Type = "move"

	case "type":
		action.Type = "type"
		if text, ok := inputMap["text"].(string); ok {
			action.Text = schemas.Ptr(text)
		}

	case "key":
		action.Type = "keypress"
		if text, ok := inputMap["text"].(string); ok {
			// Convert "key1+key2+..." format to array of keys
			keys := strings.Split(text, "+")
			action.Keys = keys
		}

	case "scroll":
		action.Type = "scroll"
		// Convert scroll_direction and scroll_amount to pixel values
		if direction, ok := inputMap["scroll_direction"].(string); ok {
			amount := 100 // Default scroll amount in pixels
			if scrollAmount, ok := inputMap["scroll_amount"].(float64); ok {
				amount = int(scrollAmount) * 100 // Convert scroll units to pixels
			}
			switch direction {
			case "down":
				action.ScrollY = schemas.Ptr(amount)
				action.ScrollX = schemas.Ptr(0)
			case "up":
				action.ScrollY = schemas.Ptr(-amount)
				action.ScrollX = schemas.Ptr(0)
			case "right":
				action.ScrollX = schemas.Ptr(amount)
				action.ScrollY = schemas.Ptr(0)
			case "left":
				action.ScrollX = schemas.Ptr(-amount)
				action.ScrollY = schemas.Ptr(0)
			}
		}

	case "left_click_drag":
		action.Type = "drag"
		// Extract start and end coordinates
		if startCoord, ok := inputMap["start_coordinate"].([]interface{}); ok && len(startCoord) == 2 {
			if endCoord, ok := inputMap["end_coordinate"].([]interface{}); ok && len(endCoord) == 2 {
				// JSON unmarshaling produces float64 for numbers, so convert them
				startX, startXOk := startCoord[0].(float64)
				startY, startYOk := startCoord[1].(float64)
				endX, endXOk := endCoord[0].(float64)
				endY, endYOk := endCoord[1].(float64)
				if startXOk && startYOk && endXOk && endYOk {
					action.Path = []schemas.ResponsesComputerToolCallActionPath{
						{X: int(startX), Y: int(startY)},
						{X: int(endX), Y: int(endY)},
					}
				}
			}
		}

	case "wait":
		action.Type = "wait"

	case "zoom":
		action.Type = "zoom"
		// Extract region [x1, y1, x2, y2] for zoom action
		if region, ok := inputMap["region"].([]interface{}); ok && len(region) == 4 {
			// JSON unmarshaling produces float64 for numbers, so convert them
			x1, x1Ok := region[0].(float64)
			y1, y1Ok := region[1].(float64)
			x2, x2Ok := region[2].(float64)
			y2, y2Ok := region[3].(float64)
			if x1Ok && y1Ok && x2Ok && y2Ok {
				action.Region = []int{int(x1), int(y1), int(x2), int(y2)}
			}
		}

	default:
		// Pass through any unknown action types
		action.Type = actionStr
	}

	// Extract coordinates for all actions that use them (click, double_click, move, scroll, etc.)
	if coordinate, ok := inputMap["coordinate"].([]interface{}); ok && len(coordinate) == 2 {
		// JSON unmarshaling produces float64 for numbers, so convert them
		if x, xOk := coordinate[0].(float64); xOk {
			if y, yOk := coordinate[1].(float64); yOk {
				action.X = schemas.Ptr(int(x))
				action.Y = schemas.Ptr(int(y))
			}
		}
	}

	return action
}

// generateSyntheticInputJSONDeltas creates synthetic input_json_delta events from complete JSON arguments
// This simulates the streaming behavior that Anthropic provides natively
func generateSyntheticInputJSONDeltas(argumentsJSON string, contentIndex *int) []*AnthropicStreamEvent {
	var events []*AnthropicStreamEvent

	// Chunk size for synthetic streaming (similar to how Anthropic chunks arguments)
	chunkSize := 8 // Small chunks to simulate realistic streaming

	// Start with empty delta to match Anthropic's behavior
	events = append(events, &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockDelta,
		Index: contentIndex,
		Delta: &AnthropicStreamDelta{
			Type:        AnthropicStreamDeltaTypeInputJSON,
			PartialJSON: schemas.Ptr(""),
		},
	})

	// Break the JSON into chunks
	for i := 0; i < len(argumentsJSON); i += chunkSize {
		end := min(i+chunkSize, len(argumentsJSON))

		chunk := argumentsJSON[i:end]
		events = append(events, &AnthropicStreamEvent{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: contentIndex,
			Delta: &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: &chunk,
			},
		})
	}

	return events
}
