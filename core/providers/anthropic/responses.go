package anthropic

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// AnthropicResponsesStreamState tracks state during streaming conversion for responses API
type AnthropicResponsesStreamState struct {
	InputJSONBuffers  map[int]string                       // server/tool input_json buffers keyed by Anthropic content block index
	InputJSONPurposes map[int]anthropicInputJSONBufferKind // classifies each buffered content block

	// Computer tool accumulation
	ComputerToolID *string

	// Web search tool accumulation (minimal fields)
	WebSearchToolID      *string                // Tool ID of active web search
	WebSearchOutputIndex *int                   // Output index for this search
	WebSearchResult      *AnthropicContentBlock // Result block when it arrives
	WebSearchQuery       *string                // Query captured from the (pre-populated) server_tool_use input
	WebSearchCaller      *AnthropicToolCaller   // Programmatic-tool-calling caller, if the search was spawned from code execution

	// Web fetch tool accumulation
	WebFetchToolID      *string                // Tool ID of active web fetch
	WebFetchOutputIndex *int                   // Output index for this fetch
	WebFetchURL         *string                // URL captured from the server_tool_use input
	WebFetchResult      *AnthropicContentBlock // Result block when it arrives

	// Advisor tool accumulation
	AdvisorToolID      *string                // Tool ID of active advisor call
	AdvisorOutputIndex *int                   // Output index for this advisor call
	AdvisorResult      *AnthropicContentBlock // advisor_tool_result block when it arrives

	// Tool search (server-side tool_search) accumulation
	ToolSearchToolID      *string                // server_tool_use ID of active tool_search
	ToolSearchToolName    *string                // tool name (tool_search_tool_regex|bm25) — kept so the done item matches the added item
	ToolSearchOutputIndex *int                   // Output index for this tool_search call
	ToolSearchResult      *AnthropicContentBlock // tool_search_tool_result block (carries tool_references) when it arrives

	// Code execution tool accumulation (bash / text_editor / python sub-tools)
	CodeExecToolID      *string                     // server_tool_use id of the active code-execution call
	CodeExecToolName    *string                     // sub-tool name (bash_code_execution, etc.)
	CodeExecOutputIndex *int                        // Output index for this code-execution call
	CodeExecResult      *AnthropicContentBlock      // *_code_execution_tool_result block when it arrives
	CodeExecInput       string                      // verbatim server_tool_use input JSON, kept across nested PTC blocks
	Container           *AnthropicResponseContainer // sandbox container from the final message_delta

	// OpenAI Responses API mapping state
	ContentIndexToOutputIndex map[int]int                       // Maps Anthropic content_index to OpenAI output_index
	ContentIndexToBlockType   map[int]AnthropicContentBlockType // Tracks content block types
	ToolArgumentBuffers       map[int]string                    // Maps output_index to accumulated tool argument JSON
	MCPCallOutputIndices      map[int]bool                      // Tracks which output indices are MCP calls
	ItemIDs                   map[int]string                    // Maps output_index to item ID for stable IDs
	OutputItems               map[int]*schemas.ResponsesMessage // Maps output_index to accumulated output item for response.completed
	ReasoningSignatures       map[int]string                    // Maps output_index to reasoning signature
	TextContentIndices        map[int]bool                      // Tracks which content indices are text blocks
	ReasoningContentIndices   map[int]bool                      // Tracks which content indices are reasoning blocks
	TextBuffers               map[int]*strings.Builder          // Maps output_index to accumulated text content for done events
	ReasoningTextBuffers      map[int]*strings.Builder          // Maps output_index to accumulated reasoning text for done events
	CompactionContentIndices  map[int]*schemas.CacheControl     // Tracks pending compaction blocks with their cache control
	CurrentOutputIndex        int                               // Current output index counter
	MessageID                 *string                           // Message ID from message_start
	Model                     *string                           // Model name from message_start
	StopReason                *string                           // Stop reason for the message
	CreatedAt                 int                               // Timestamp for created_at consistency
	HasEmittedCreated         bool                              // Whether we've emitted response.created
	HasEmittedInProgress      bool                              // Whether we've emitted response.in_progress
	HasEmittedMessageDelta    bool                              // Whether we've emitted message_delta (avoids duplicate from response.completed)
	StructuredOutputToolName  string                            // Name of the structured output tool (if using tool-based SO for Vertex)
	StructuredOutputIndex     *int                              // Output index of the structured output tool call
	UsedStructuredOutputTool  bool                              // True when the SO tool block was actually consumed into text content
	SeenRealToolCall          bool                              // True when any non-SO tool_use/server_tool_use/mcp_tool_use content block was started
}

type anthropicInputJSONBufferKind string

const (
	anthropicInputJSONBufferComputer   anthropicInputJSONBufferKind = "computer"
	anthropicInputJSONBufferWebSearch  anthropicInputJSONBufferKind = "web_search"
	anthropicInputJSONBufferWebFetch   anthropicInputJSONBufferKind = "web_fetch"
	anthropicInputJSONBufferAdvisor    anthropicInputJSONBufferKind = "advisor"
	anthropicInputJSONBufferCodeExec   anthropicInputJSONBufferKind = "code_exec"
	anthropicInputJSONBufferToolSearch anthropicInputJSONBufferKind = "tool_search"
)

func (state *AnthropicResponsesStreamState) beginInputJSONBuffer(index *int, kind anthropicInputJSONBufferKind) {
	if index == nil {
		return
	}
	if state.InputJSONBuffers == nil {
		state.InputJSONBuffers = make(map[int]string)
	}
	if state.InputJSONPurposes == nil {
		state.InputJSONPurposes = make(map[int]anthropicInputJSONBufferKind)
	}
	state.InputJSONBuffers[*index] = ""
	state.InputJSONPurposes[*index] = kind
}

func (state *AnthropicResponsesStreamState) appendInputJSON(index *int, partial string) bool {
	if index == nil || state.InputJSONPurposes == nil {
		return false
	}
	if _, ok := state.InputJSONPurposes[*index]; !ok {
		return false
	}
	state.InputJSONBuffers[*index] += partial
	return true
}

func (state *AnthropicResponsesStreamState) finishInputJSONBuffer(index *int) (string, anthropicInputJSONBufferKind, bool) {
	if index == nil || state.InputJSONPurposes == nil {
		return "", "", false
	}
	kind, ok := state.InputJSONPurposes[*index]
	if !ok {
		return "", "", false
	}
	input := state.InputJSONBuffers[*index]
	delete(state.InputJSONBuffers, *index)
	delete(state.InputJSONPurposes, *index)
	return input, kind, true
}

// anthropicResponsesStreamStatePool provides a pool for Anthropic responses stream state objects.
var anthropicResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicResponsesStreamState{
			InputJSONBuffers:          make(map[int]string),
			InputJSONPurposes:         make(map[int]anthropicInputJSONBufferKind),
			ContentIndexToOutputIndex: make(map[int]int),
			ToolArgumentBuffers:       make(map[int]string),
			MCPCallOutputIndices:      make(map[int]bool),
			ItemIDs:                   make(map[int]string),
			ReasoningSignatures:       make(map[int]string),
			TextContentIndices:        make(map[int]bool),
			ReasoningContentIndices:   make(map[int]bool),
			CompactionContentIndices:  make(map[int]*schemas.CacheControl),
			OutputItems:               make(map[int]*schemas.ResponsesMessage),
			TextBuffers:               make(map[int]*strings.Builder),
			ReasoningTextBuffers:      make(map[int]*strings.Builder),
			CurrentOutputIndex:        0,
			CreatedAt:                 int(time.Now().Unix()),
			HasEmittedCreated:         false,
			HasEmittedInProgress:      false,
		}
	},
}

// anthropicToResponsesStreamState holds per-request state for the Bifrost→Anthropic
// stream conversion direction.
type anthropicToResponsesStreamState struct {
	// webSearchItemIDs tracks item IDs for WebSearch tools so their argument deltas
	// can be skipped and regenerated synthetically (with sanitization) at output_item.done.
	webSearchItemIDs map[string]bool

	// Anthropic content-block index allocation. OpenAI numbers output items while
	// Anthropic numbers content blocks, and one web_search / code_execution call
	// expands to two Anthropic blocks (server_tool_use + *_tool_result) — so the
	// counts differ and the OpenAI indices cannot be reused 1:1 (they collide on
	// programmatic tool calling). Each output item allocates its primary block index
	// at output_item.added; deltas/stops look it up; the second (result) block gets
	// a fresh index.
	nextBlockIndex   int
	blockIndexByItem map[string]int

	// blockIndexMisses records non-empty item keys for which blockIndexFor was
	// asked for an index that allocBlockIndex never assigned — i.e. a
	// content_block_stop/_delta referencing a block whose content_block_start was
	// never registered at output_item.added. This must not happen: every
	// output_item.added allocates its block index (~L2053), and the Anthropic
	// passthrough path routes every output_item.added through this converter so the
	// allocator always runs (see mustConvertInPassthrough in the transport). A miss
	// therefore signals a stream-bookkeeping desync; it is recorded so tests fail
	// loudly instead of the stream silently mis-numbering blocks.
	blockIndexMisses []string

	// passthrough is true when this reverse conversion runs on the Claude Code
	// passthrough path, where verbatim raw upstream frames are interleaved with the
	// converter's own. Only then must the converter consume a content-block index for
	// server-tool result blocks it still collapses (currently resultless web_search)
	// to stay in lockstep with the upstream indices carried by interleaved raw frames.
	// On the all-normalized path (OpenAI-via-Anthropic, curl), every frame is
	// converter-built, so consuming the index would skip a number.
	passthrough bool

	// codeExecToolNameByItem remembers each code_interpreter_call's Anthropic
	// sub-tool name (captured at output_item.added) so the code block's input can be
	// reconstructed and closed on code.done — emitted before the nested web_search
	// blocks, matching Anthropic's sequential ordering. The *_tool_result block then
	// follows at output_item.done.
	codeExecToolNameByItem map[string]string

	// codeExecServerClosedByItem marks code_interpreter_call items whose
	// server_tool_use block was already closed early on code.done (python/bash,
	// where the input reconstructs from the neutral Code and the block must close
	// before nested web_search blocks). text_editor has a multi-key input that
	// Code can't carry and never spawns nested blocks, so it is left open here and
	// closed at output_item.done from the verbatim carry input instead.
	codeExecServerClosedByItem map[string]bool
}

// allocBlockIndex assigns and returns the next Anthropic content-block index. A
// non-empty key is remembered so the block's later delta/stop events resolve to the
// same index via blockIndexFor.
func (s *anthropicToResponsesStreamState) allocBlockIndex(key string) *int {
	idx := s.nextBlockIndex
	s.nextBlockIndex++
	if key != "" {
		if s.blockIndexByItem == nil {
			s.blockIndexByItem = make(map[string]int)
		}
		s.blockIndexByItem[key] = idx
	}
	return &idx
}

// blockIndexFor returns the index previously allocated for key. A non-empty key
// that was never registered by allocBlockIndex means a stop/delta references a
// block whose start we never emitted — a bookkeeping desync — so it is recorded
// in blockIndexMisses before falling back to a fresh allocation (defensive — keeps
// start/stop paired rather than crashing the stream).
func (s *anthropicToResponsesStreamState) blockIndexFor(key string) *int {
	if key != "" && s.blockIndexByItem != nil {
		if idx, ok := s.blockIndexByItem[key]; ok {
			return &idx
		}
	}
	if key != "" {
		s.blockIndexMisses = append(s.blockIndexMisses, key)
	}
	return s.allocBlockIndex(key)
}

// reverseStreamItemKey derives a stable per-item key for content-block index
// allocation, consistent across output_item.added / delta / output_item.done.
func reverseStreamItemKey(resp *schemas.BifrostResponsesStreamResponse) string {
	if resp.Item != nil && resp.Item.ID != nil {
		return *resp.Item.ID
	}
	if resp.ItemID != nil {
		return *resp.ItemID
	}
	if resp.OutputIndex != nil {
		return fmt.Sprintf("oi:%d", *resp.OutputIndex)
	}
	if resp.ContentIndex != nil {
		return fmt.Sprintf("ci:%d", *resp.ContentIndex)
	}
	return ""
}

type anthropicToResponsesStreamStateKeyType struct{}

var anthropicToResponsesStreamStateKey = anthropicToResponsesStreamStateKeyType{}

// getOrCreateAnthropicToResponsesStreamState returns the per-request conversion state,
// creating and storing it in ctx on first access.
func getOrCreateAnthropicToResponsesStreamState(ctx *schemas.BifrostContext) *anthropicToResponsesStreamState {
	if v := ctx.Value(anthropicToResponsesStreamStateKey); v != nil {
		return v.(*anthropicToResponsesStreamState)
	}
	state := &anthropicToResponsesStreamState{}
	ctx.SetValue(anthropicToResponsesStreamStateKey, state)
	return state
}

// SetResponsesStreamPassthrough marks this request's Anthropic reverse stream
// conversion as running on the Claude Code passthrough path (raw upstream frames
// interleaved with converted ones). The converter reads state.passthrough only for
// collapsed server-tool result blocks that must still consume an upstream index.
func SetResponsesStreamPassthrough(ctx *schemas.BifrostContext) {
	getOrCreateAnthropicToResponsesStreamState(ctx).passthrough = true
}

// AcquireAnthropicResponsesStreamState gets an Anthropic responses stream state from the pool.
func AcquireAnthropicResponsesStreamState() *AnthropicResponsesStreamState {
	state := anthropicResponsesStreamStatePool.Get().(*AnthropicResponsesStreamState)
	// Clear maps (they're already initialized from New or previous flush)
	// Only initialize if nil (shouldn't happen, but defensive)
	if state.ContentIndexToOutputIndex == nil {
		state.ContentIndexToOutputIndex = make(map[int]int)
	} else {
		clear(state.ContentIndexToOutputIndex)
	}
	if state.InputJSONBuffers == nil {
		state.InputJSONBuffers = make(map[int]string)
	} else {
		clear(state.InputJSONBuffers)
	}
	if state.InputJSONPurposes == nil {
		state.InputJSONPurposes = make(map[int]anthropicInputJSONBufferKind)
	} else {
		clear(state.InputJSONPurposes)
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
	if state.TextBuffers == nil {
		state.TextBuffers = make(map[int]*strings.Builder)
	} else {
		clear(state.TextBuffers)
	}
	if state.ReasoningTextBuffers == nil {
		state.ReasoningTextBuffers = make(map[int]*strings.Builder)
	} else {
		clear(state.ReasoningTextBuffers)
	}
	if state.CompactionContentIndices == nil {
		state.CompactionContentIndices = make(map[int]*schemas.CacheControl)
	} else {
		clear(state.CompactionContentIndices)
	}
	if state.OutputItems == nil {
		state.OutputItems = make(map[int]*schemas.ResponsesMessage)
	} else {
		clear(state.OutputItems)
	}
	// Reset other fields
	state.ComputerToolID = nil
	state.WebSearchToolID = nil
	state.WebSearchOutputIndex = nil
	state.WebSearchResult = nil
	state.WebSearchQuery = nil
	state.WebSearchCaller = nil
	state.WebFetchToolID = nil
	state.WebFetchOutputIndex = nil
	state.WebFetchURL = nil
	state.WebFetchResult = nil
	state.AdvisorToolID = nil
	state.AdvisorOutputIndex = nil
	state.AdvisorResult = nil
	state.ToolSearchToolID = nil
	state.ToolSearchToolName = nil
	state.ToolSearchOutputIndex = nil
	state.ToolSearchResult = nil
	state.CodeExecToolID = nil
	state.CodeExecToolName = nil
	state.CodeExecOutputIndex = nil
	state.CodeExecResult = nil
	state.CodeExecInput = ""
	state.Container = nil
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.StopReason = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.HasEmittedMessageDelta = false
	state.StructuredOutputToolName = ""
	state.StructuredOutputIndex = nil
	state.UsedStructuredOutputTool = false
	state.SeenRealToolCall = false
	return state
}

// ReleaseAnthropicResponsesStreamState returns an Anthropic responses stream state to the pool.
func ReleaseAnthropicResponsesStreamState(state *AnthropicResponsesStreamState) {
	if state != nil {
		state.flush() // Clean before returning to pool
		anthropicResponsesStreamStatePool.Put(state)
	}
}

// flush resets the state of the stream state to its initial values
func (state *AnthropicResponsesStreamState) flush() {
	state.InputJSONBuffers = nil
	state.InputJSONPurposes = nil
	state.ComputerToolID = nil
	state.WebSearchToolID = nil
	state.WebSearchOutputIndex = nil
	state.WebSearchResult = nil
	state.WebSearchQuery = nil
	state.WebSearchCaller = nil
	state.WebFetchToolID = nil
	state.WebFetchOutputIndex = nil
	state.WebFetchURL = nil
	state.WebFetchResult = nil
	state.AdvisorToolID = nil
	state.AdvisorOutputIndex = nil
	state.AdvisorResult = nil
	state.ToolSearchToolID = nil
	state.ToolSearchToolName = nil
	state.ToolSearchOutputIndex = nil
	state.ToolSearchResult = nil
	state.CodeExecToolID = nil
	state.CodeExecToolName = nil
	state.CodeExecOutputIndex = nil
	state.CodeExecResult = nil
	state.CodeExecInput = ""
	state.Container = nil
	state.ContentIndexToOutputIndex = nil
	state.ContentIndexToBlockType = nil
	state.ToolArgumentBuffers = nil
	state.MCPCallOutputIndices = nil
	state.ItemIDs = nil
	state.ReasoningSignatures = nil
	state.TextContentIndices = nil
	state.ReasoningContentIndices = nil
	state.TextBuffers = nil
	state.ReasoningTextBuffers = nil
	state.CompactionContentIndices = nil
	state.OutputItems = nil
	state.CurrentOutputIndex = 0
	state.MessageID = nil
	state.StopReason = nil
	state.Model = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedInProgress = false
	state.HasEmittedMessageDelta = false
	state.StructuredOutputToolName = ""
	state.StructuredOutputIndex = nil
	state.UsedStructuredOutputTool = false
	state.SeenRealToolCall = false
}

// isCompactionItem checks if a ResponsesMessage represents a compaction item
// (a message with a compaction content block as its first content block)
func isCompactionItem(item *schemas.ResponsesMessage) bool {
	return item != nil && item.Type != nil &&
		*item.Type == schemas.ResponsesMessageTypeMessage &&
		item.Content != nil && len(item.Content.ContentBlocks) > 0 &&
		item.Content.ContentBlocks[0].Type == schemas.ResponsesOutputMessageContentTypeCompaction
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
				// Forward cache diagnostics from message_start (cache-diagnosis-2026-04-07).
				if chunk.Message.Diagnostics != nil {
					response.Diagnostics = chunk.Message.Diagnostics
				}
				// Forward input usage from message_start so clients see cache metrics early
				if chunk.Message.Usage != nil {
					response.Usage = &schemas.ResponsesResponseUsage{
						InputTokens:  chunk.Message.Usage.InputTokens,
						OutputTokens: chunk.Message.Usage.OutputTokens,
						TotalTokens:  chunk.Message.Usage.InputTokens + chunk.Message.Usage.OutputTokens,
					}
					if chunk.Message.Usage.CacheReadInputTokens > 0 || chunk.Message.Usage.CacheCreationInputTokens > 0 {
						inputTokensDetails := &schemas.ResponsesResponseInputTokens{
							CachedReadTokens:  chunk.Message.Usage.CacheReadInputTokens,
							CachedWriteTokens: chunk.Message.Usage.CacheCreationInputTokens,
						}
						if chunk.Message.Usage.CacheCreation.Ephemeral5mInputTokens > 0 || chunk.Message.Usage.CacheCreation.Ephemeral1hInputTokens > 0 {
							inputTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{
								CachedWriteTokens5m: chunk.Message.Usage.CacheCreation.Ephemeral5mInputTokens,
								CachedWriteTokens1h: chunk.Message.Usage.CacheCreation.Ephemeral1hInputTokens,
							}
						}
						response.Usage.InputTokensDetails = inputTokensDetails
						// Bifrost convention: InputTokens includes cached tokens
						response.Usage.InputTokens += chunk.Message.Usage.CacheReadInputTokens + chunk.Message.Usage.CacheCreationInputTokens
						response.Usage.TotalTokens += chunk.Message.Usage.CacheReadInputTokens + chunk.Message.Usage.CacheCreationInputTokens
					}
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
			// A data-less redacted_thinking block contributes nothing; skip it
			// before reserving an output index so later items keep their
			// positions in response.completed output.
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeRedactedThinking &&
				(chunk.ContentBlock.Data == nil || *chunk.ContentBlock.Data == "") {
				state.ContentIndexToBlockType[*chunk.Index] = AnthropicContentBlockTypeRedactedThinking
				return nil, nil, false
			}

			outputIndex := state.getOrCreateOutputIndex(chunk.Index)

			if chunk.ContentBlock.Type == AnthropicContentBlockTypeToolUse &&
				chunk.ContentBlock.Name != nil &&
				*chunk.ContentBlock.Name == string(AnthropicToolNameComputer) &&
				chunk.ContentBlock.ID != nil {

				state.SeenRealToolCall = true
				// Start accumulating computer tool
				state.ComputerToolID = chunk.ContentBlock.ID
				state.beginInputJSONBuffer(chunk.Index, anthropicInputJSONBufferComputer)

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

				state.SeenRealToolCall = true
				// web_search server_tool_use usually arrives pre-populated (query in
				// input, not streamed). Keep a buffer anyway so unusual input_json_delta
				// events are swallowed and can be used as a fallback on block stop.
				state.beginInputJSONBuffer(chunk.Index, anthropicInputJSONBufferWebSearch)
				state.WebSearchToolID = chunk.ContentBlock.ID
				state.WebSearchOutputIndex = schemas.Ptr(outputIndex)
				state.WebSearchQuery = nil
				if q := providerUtils.GetJSONField(chunk.ContentBlock.Input, "query"); q.Exists() && q.Type == gjson.String {
					state.WebSearchQuery = schemas.Ptr(q.Str)
				}
				state.WebSearchCaller = chunk.ContentBlock.Caller

				// Store item ID
				state.ItemIDs[outputIndex] = *chunk.ContentBlock.ID

				wsAction := &schemas.ResponsesWebSearchToolCallAction{Type: "search"}
				if state.WebSearchQuery != nil {
					wsAction.Query = state.WebSearchQuery
					wsAction.Queries = []string{*state.WebSearchQuery}
				}
				toolMsg := &schemas.ResponsesToolMessage{
					CallID: chunk.ContentBlock.ID,
					Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: wsAction},
				}
				if state.WebSearchCaller != nil {
					toolMsg.Caller = &schemas.ResponsesToolCaller{
						Type:   string(state.WebSearchCaller.Type),
						ToolID: state.WebSearchCaller.ToolID,
					}
				}

				// Emit output_item.added for web_search_call
				item := &schemas.ResponsesMessage{
					ID:                   chunk.ContentBlock.ID,
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status:               schemas.Ptr("in_progress"),
					ResponsesToolMessage: toolMsg,
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

			// Handle tool_search server_tool_use (server-side tool_search query block).
			// Anthropic runs the search server-side and returns a tool_search_tool_result
			// carrying tool_references to the discovered (deferred) tools; the model then
			// emits a normal tool_use to call one. Mirrors the web_search query path.
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeServerToolUse &&
				chunk.ContentBlock.Name != nil &&
				(*chunk.ContentBlock.Name == string(AnthropicToolNameToolSearchRegex) ||
					*chunk.ContentBlock.Name == string(AnthropicToolNameToolSearchBM25)) &&
				chunk.ContentBlock.ID != nil {

				state.SeenRealToolCall = true
				// Suppress the query input_json deltas via the shared input-buffer path;
				// the search is run server-side, so we only care about the result block.
				state.beginInputJSONBuffer(chunk.Index, anthropicInputJSONBufferToolSearch)
				state.ToolSearchToolID = chunk.ContentBlock.ID
				state.ToolSearchToolName = chunk.ContentBlock.Name
				state.ToolSearchOutputIndex = schemas.Ptr(outputIndex)
				state.ItemIDs[outputIndex] = *chunk.ContentBlock.ID
				// Mark block type so content_block_stop doesn't emit a generic message done
				if chunk.Index != nil {
					state.ContentIndexToBlockType[*chunk.Index] = AnthropicContentBlockTypeServerToolUse
					state.TextContentIndices[*chunk.Index] = false
				}

				// Emit output_item.added for the tool_search_call (completed at result block-stop)
				item := &schemas.ResponsesMessage{
					ID:     chunk.ContentBlock.ID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
					Status: schemas.Ptr("in_progress"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: chunk.ContentBlock.ID,
						Name:   chunk.ContentBlock.Name,
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

			// Handle tool_search_tool_result block (the discovered tool_references arrive).
			// Store it; the tool_search_call output_item.done (carrying tool_references) is
			// emitted on this block's content_block_stop. Mirrors web_search_tool_result.
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeToolSearchToolResult &&
				chunk.ContentBlock.ToolUseID != nil {

				if chunk.Index != nil {
					state.ContentIndexToBlockType[*chunk.Index] = AnthropicContentBlockTypeToolSearchToolResult
				}

				if state.ToolSearchToolID != nil && *state.ToolSearchToolID == *chunk.ContentBlock.ToolUseID {
					// Store the result block (arrives complete with all tool_references)
					state.ToolSearchResult = chunk.ContentBlock
				}

				// Defer the done to content_block_stop (don't drop the block)
				return nil, nil, false
			}

			// Handle web_fetch server_tool_use (fetch block)
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeServerToolUse &&
				chunk.ContentBlock.Name != nil &&
				*chunk.ContentBlock.Name == string(AnthropicToolNameWebFetch) &&
				chunk.ContentBlock.ID != nil {

				state.SeenRealToolCall = true
				state.beginInputJSONBuffer(chunk.Index, anthropicInputJSONBufferWebFetch)
				state.WebFetchToolID = chunk.ContentBlock.ID
				state.WebFetchOutputIndex = schemas.Ptr(outputIndex)
				state.WebFetchURL = nil
				if u := providerUtils.GetJSONField(chunk.ContentBlock.Input, "url"); u.Exists() && u.Type == gjson.String {
					state.WebFetchURL = schemas.Ptr(u.Str)
				}

				state.ItemIDs[outputIndex] = *chunk.ContentBlock.ID

				toolMsg := &schemas.ResponsesToolMessage{
					CallID: chunk.ContentBlock.ID,
				}
				if state.WebFetchURL != nil {
					toolMsg.Action = &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{
							Type: "fetch",
							URL:  *state.WebFetchURL,
						},
					}
				}
				// Preserve the programmatic-tool-calling caller (set when this fetch was
				// spawned from inside the code-execution sandbox), mirroring web_search.
				if chunk.ContentBlock.Caller != nil {
					toolMsg.Caller = &schemas.ResponsesToolCaller{
						Type:   string(chunk.ContentBlock.Caller.Type),
						ToolID: chunk.ContentBlock.Caller.ToolID,
					}
				}
				item := &schemas.ResponsesMessage{
					ID:                   chunk.ContentBlock.ID,
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
					Status:               schemas.Ptr("in_progress"),
					ResponsesToolMessage: toolMsg,
				}

				var responses []*schemas.BifrostResponsesStreamResponse

				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				})

				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeWebFetchCallInProgress,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ItemID:         chunk.ContentBlock.ID,
				})

				responses = append(responses, &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeWebFetchCallFetching,
					SequenceNumber: sequenceNumber + len(responses),
					OutputIndex:    schemas.Ptr(outputIndex),
					ItemID:         chunk.ContentBlock.ID,
				})

				return responses, nil, false
			}

			// Handle web_fetch_tool_result block
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeWebFetchToolResult &&
				chunk.ContentBlock.ToolUseID != nil {

				if chunk.Index != nil {
					state.ContentIndexToBlockType[*chunk.Index] = AnthropicContentBlockTypeWebFetchToolResult
				}

				if state.WebFetchToolID != nil && *state.WebFetchToolID == *chunk.ContentBlock.ToolUseID {
					if chunk.Index != nil {
						delete(state.ContentIndexToBlockType, *chunk.Index)
					}

					// Remember that the result block arrived; its content is not
					// represented in the Responses model (handled server-side), but its
					// presence drives the web_fetch_call output_item.done at the result
					// block's content_block_stop (mirrors web_search).
					state.WebFetchResult = chunk.ContentBlock

					return []*schemas.BifrostResponsesStreamResponse{{
						Type:           schemas.ResponsesStreamResponseTypeWebFetchCallCompleted,
						SequenceNumber: sequenceNumber,
						OutputIndex:    state.WebFetchOutputIndex,
						ItemID:         chunk.ContentBlock.ToolUseID,
					}}, nil, false
				}

				return nil, nil, false
			}

			// Handle advisor server_tool_use (the call; input is always empty).
			// The paired advisor_tool_result is emitted as output_item.done when
			// the result block's content_block_stop arrives (mirrors web_search).
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeServerToolUse &&
				chunk.ContentBlock.Name != nil &&
				*chunk.ContentBlock.Name == string(AnthropicToolNameAdvisor) &&
				chunk.ContentBlock.ID != nil {

				state.SeenRealToolCall = true
				state.beginInputJSONBuffer(chunk.Index, anthropicInputJSONBufferAdvisor) // absorbs the empty advisor input_json_delta
				state.AdvisorToolID = chunk.ContentBlock.ID
				state.AdvisorOutputIndex = schemas.Ptr(outputIndex)
				state.ItemIDs[outputIndex] = *chunk.ContentBlock.ID

				item := &schemas.ResponsesMessage{
					ID:     chunk.ContentBlock.ID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeAdvisorCall),
					Status: schemas.Ptr("in_progress"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: chunk.ContentBlock.ID,
						Name:   schemas.Ptr(string(AnthropicToolNameAdvisor)),
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

			// Handle advisor_tool_result block (arrives complete in one event).
			// Store it; the output_item.done is emitted on its content_block_stop.
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeAdvisorToolResult &&
				chunk.ContentBlock.ToolUseID != nil {

				if chunk.Index != nil {
					state.ContentIndexToBlockType[*chunk.Index] = AnthropicContentBlockTypeAdvisorToolResult
				}
				if state.AdvisorToolID != nil && *state.AdvisorToolID == *chunk.ContentBlock.ToolUseID {
					state.AdvisorResult = chunk.ContentBlock
				}
				return nil, nil, false
			}

			// Handle code-execution server_tool_use (bash / text_editor / python).
			// The input JSON is accumulated; the decoded code is emitted on
			// content_block_stop (Anthropic streams JSON, OpenAI streams raw code).
			if chunk.ContentBlock.Type == AnthropicContentBlockTypeServerToolUse &&
				chunk.ContentBlock.Name != nil &&
				isAnthropicCodeExecutionToolName(*chunk.ContentBlock.Name) &&
				chunk.ContentBlock.ID != nil {

				state.SeenRealToolCall = true
				state.beginInputJSONBuffer(chunk.Index, anthropicInputJSONBufferCodeExec)
				state.CodeExecToolID = chunk.ContentBlock.ID
				state.CodeExecToolName = chunk.ContentBlock.Name
				state.CodeExecOutputIndex = schemas.Ptr(outputIndex)
				state.ItemIDs[outputIndex] = *chunk.ContentBlock.ID

				item := &schemas.ResponsesMessage{
					ID:     chunk.ContentBlock.ID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
					Status: schemas.Ptr("in_progress"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:                           chunk.ContentBlock.ID,
						ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{},
						// Carry the sub-tool name so the reverse converter can
						// reconstruct the correct server_tool_use name at this start.
						ResponsesCodeExecutionCall: &schemas.ResponsesCodeExecutionCall{ToolName: *chunk.ContentBlock.Name},
					},
				}

				return []*schemas.BifrostResponsesStreamResponse{
					{
						Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Item:           item,
					},
					{
						Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallInProgress,
						SequenceNumber: sequenceNumber + 1,
						OutputIndex:    schemas.Ptr(outputIndex),
						ItemID:         chunk.ContentBlock.ID,
					},
				}, nil, false
			}

			// Handle code-execution result block (arrives complete in one event).
			// Store it; the output_item.done is emitted on its content_block_stop.
			if (chunk.ContentBlock.Type == AnthropicContentBlockTypeCodeExecutionToolResult ||
				chunk.ContentBlock.Type == AnthropicContentBlockTypeBashCodeExecutionToolResult ||
				chunk.ContentBlock.Type == AnthropicContentBlockTypeTextEditorCodeExecutionToolResult) &&
				chunk.ContentBlock.ToolUseID != nil {

				if chunk.Index != nil {
					state.ContentIndexToBlockType[*chunk.Index] = chunk.ContentBlock.Type
				}
				if state.CodeExecToolID != nil && *state.CodeExecToolID == *chunk.ContentBlock.ToolUseID {
					state.CodeExecResult = chunk.ContentBlock
				}
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

				state.SeenRealToolCall = true
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

				// Store a cloned copy so later mutations (e.g. setting Arguments/Status
				// to "completed") don't affect the already-emitted output_item.added event.
				clonedItem := *item
				clonedToolMsg := *item.ResponsesToolMessage
				clonedItem.ResponsesToolMessage = &clonedToolMsg
				state.OutputItems[outputIndex] = &clonedItem

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
				state.SeenRealToolCall = true
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

				// Persist into OutputItems so content_block_stop can fold the
				// accumulated text and signature into the matching output_item.done
				// and response.completed carries the completed reasoning item.
				itemCopy := *item
				state.OutputItems[outputIndex] = &itemCopy

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
			case AnthropicContentBlockTypeRedactedThinking:
				// Redacted thinking blocks arrive complete in content_block_start (no
				// deltas follow; data-less blocks were already skipped above, before
				// an output index was reserved). Surface the encrypted payload as a
				// reasoning item with encrypted_content, mirroring the non-streaming
				// converter, so streaming clients can replay it on the next turn;
				// Anthropic rejects tool-use follow-ups whose latest assistant
				// message dropped it.
				messageType := schemas.ResponsesMessageTypeReasoning
				role := schemas.ResponsesInputMessageRoleAssistant

				// Generate stable ID for the reasoning item
				var itemID string
				if state.MessageID == nil {
					itemID = fmt.Sprintf("reasoning_%d", outputIndex)
				} else {
					itemID = fmt.Sprintf("msg_%s_reasoning_%d", *state.MessageID, outputIndex)
				}
				state.ItemIDs[outputIndex] = itemID

				item := &schemas.ResponsesMessage{
					ID:   &itemID,
					Type: &messageType,
					Role: &role,
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary:          []schemas.ResponsesReasoningSummary{},
						EncryptedContent: chunk.ContentBlock.Data,
					},
				}

				// Persist into OutputItems so the block's content_block_stop emits the
				// matching output_item.done and response.completed carries the item.
				itemCopy := *item
				state.OutputItems[outputIndex] = &itemCopy

				return []*schemas.BifrostResponsesStreamResponse{{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(outputIndex),
					ContentIndex:   chunk.Index,
					Item:           item,
				}}, nil, false
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
					// Accumulate text for done events
					if state.TextBuffers[outputIndex] == nil {
						state.TextBuffers[outputIndex] = &strings.Builder{}
					}
					state.TextBuffers[outputIndex].WriteString(*chunk.Delta.Text)

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
					if state.appendInputJSON(chunk.Index, *chunk.Delta.PartialJSON) {
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
					// Accumulate the text so content_block_stop can emit it on
					// reasoning_summary_text.done and the completed reasoning item
					if state.ReasoningTextBuffers == nil {
						state.ReasoningTextBuffers = make(map[int]*strings.Builder)
					}
					if state.ReasoningTextBuffers[outputIndex] == nil {
						state.ReasoningTextBuffers[outputIndex] = &strings.Builder{}
					}
					state.ReasoningTextBuffers[outputIndex].WriteString(*chunk.Delta.Thinking)

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
			// Data-less redacted_thinking: its start reserved no output index, so
			// its stop must not reserve one (or synthesize a done) either.
			if blockType, exists := state.ContentIndexToBlockType[*chunk.Index]; exists &&
				blockType == AnthropicContentBlockTypeRedactedThinking {
				delete(state.ContentIndexToBlockType, *chunk.Index)
				return nil, nil, false
			}

			outputIndex := state.getOrCreateOutputIndex(chunk.Index)

			if inputJSON, inputKind, hasInputBuffer := state.finishInputJSONBuffer(chunk.Index); hasInputBuffer {
				switch inputKind {
				case anthropicInputJSONBufferComputer:
					if state.ComputerToolID == nil {
						return nil, nil, false
					}
					// Parse accumulated JSON and convert to OpenAI format
					var inputMap map[string]interface{}
					var action *schemas.ResponsesComputerToolCallAction

					if inputJSON != "" {
						if err := sonic.Unmarshal([]byte(inputJSON), &inputMap); err == nil {
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

					state.ComputerToolID = nil

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

				case anthropicInputJSONBufferWebSearch:
					if state.WebSearchToolID == nil {
						return nil, nil, false
					}
					if state.WebSearchQuery == nil && inputJSON != "" {
						if q := providerUtils.GetJSONField([]byte(inputJSON), "query"); q.Exists() && q.Type == gjson.String {
							state.WebSearchQuery = schemas.Ptr(q.Str)
						}
					}
					return nil, nil, false

				case anthropicInputJSONBufferWebFetch:
					// Fallback: recover the URL if Anthropic streamed it via
					// input_json_delta instead of inlining it in content_block_start
					// (mirrors the web_search query fallback above).
					if state.WebFetchURL == nil && inputJSON != "" {
						if u := providerUtils.GetJSONField([]byte(inputJSON), "url"); u.Exists() && u.Type == gjson.String {
							state.WebFetchURL = schemas.Ptr(u.Str)
						}
					}
					return nil, nil, false

				case anthropicInputJSONBufferAdvisor:
					return nil, nil, false

				case anthropicInputJSONBufferCodeExec:
					if state.CodeExecToolID == nil {
						return nil, nil, false
					}
					// Code-execution server_tool_use block ended — decode the code from
					// the accumulated input JSON and emit code.delta + code.done +
					// interpreting; the result block is folded in later.
					state.CodeExecInput = inputJSON
					code := ""
					if inputJSON != "" {
						key := "code"
						if state.CodeExecToolName != nil &&
							AnthropicToolName(*state.CodeExecToolName) == AnthropicToolNameBashCodeExecution {
							key = "command"
						}
						if c := providerUtils.GetJSONField([]byte(inputJSON), key); c.Exists() && c.Type == gjson.String {
							code = c.Str
						}
					}
					outIdx := state.CodeExecOutputIndex
					itemID := state.CodeExecToolID

					var responses []*schemas.BifrostResponsesStreamResponse
					if code != "" {
						responses = append(responses, &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta,
							SequenceNumber: sequenceNumber + len(responses),
							OutputIndex:    outIdx,
							ItemID:         itemID,
							Delta:          schemas.Ptr(code),
						})
					}
					// Capture the offset once: both struct literals are evaluated before
					// the variadic append reassigns responses, so reading len(responses)
					// in each would yield the same (duplicate) sequence number.
					codeDoneIdx := sequenceNumber + len(responses)
					responses = append(responses,
						&schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone,
							SequenceNumber: codeDoneIdx,
							OutputIndex:    outIdx,
							ItemID:         itemID,
							Code:           schemas.Ptr(code),
						},
						&schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallInterpreting,
							SequenceNumber: codeDoneIdx + 1,
							OutputIndex:    outIdx,
							ItemID:         itemID,
						},
					)
					return responses, nil, false

				case anthropicInputJSONBufferToolSearch:
					// tool_search server_tool_use query block ended — the search runs
					// server-side, so just wait for the tool_search_tool_result block.
					return nil, nil, false
				}
			}

			// Check if this is the end of a web_search_tool_result block
			if state.WebSearchResult != nil && state.WebSearchToolID != nil {

				// Use the query captured from the server_tool_use input at block start
				var query string
				var queries []string
				if state.WebSearchQuery != nil {
					query = *state.WebSearchQuery
					queries = []string{query}
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

				toolMsg := &schemas.ResponsesToolMessage{
					CallID: state.WebSearchToolID,
					Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: action},
				}
				if state.WebSearchCaller != nil {
					toolMsg.Caller = &schemas.ResponsesToolCaller{
						Type:   string(state.WebSearchCaller.Type),
						ToolID: state.WebSearchCaller.ToolID,
					}
				}
				item := &schemas.ResponsesMessage{
					ID:                   state.WebSearchToolID,
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
					Status:               &statusCompleted,
					ResponsesToolMessage: toolMsg,
				}

				outputIdx := state.WebSearchOutputIndex

				// Clear all web search state
				state.WebSearchToolID = nil
				state.WebSearchOutputIndex = nil
				state.WebSearchResult = nil
				state.WebSearchQuery = nil
				state.WebSearchCaller = nil

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

			// End of a web_fetch_tool_result block — emit the web_fetch_call done with
			// the typed result payload so Anthropic-compatible reverse conversion can
			// faithfully rebuild the server_tool_use + web_fetch_tool_result pair.
			if state.WebFetchResult != nil && state.WebFetchToolID != nil {
				toolMsg := &schemas.ResponsesToolMessage{CallID: state.WebFetchToolID}
				if state.WebFetchURL != nil {
					toolMsg.Action = &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{
							Type: "fetch",
							URL:  *state.WebFetchURL,
						},
					}
				}
				toolMsg.ResponsesWebFetchCall = convertAnthropicWebFetchResultToBifrost(state.WebFetchResult)
				item := &schemas.ResponsesMessage{
					ID:                   state.WebFetchToolID,
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
					Status:               schemas.Ptr("completed"),
					ResponsesToolMessage: toolMsg,
				}
				outputIdx := state.WebFetchOutputIndex

				state.WebFetchToolID = nil
				state.WebFetchOutputIndex = nil
				state.WebFetchURL = nil
				state.WebFetchResult = nil

				if chunk.Index != nil {
					delete(state.ContentIndexToBlockType, *chunk.Index)
				}

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

			// End of an advisor_tool_result block — emit the advisor_call done.
			if state.AdvisorResult != nil && state.AdvisorToolID != nil {
				advisor := &schemas.ResponsesAdvisorCall{}
				// content is a single object on the wire (ContentObj); UnmarshalJSON
				// wraps incoming objects into ContentBlocks, so accept either.
				if c := state.AdvisorResult.Content; c != nil {
					inner := c.ContentObj
					if inner == nil && len(c.ContentBlocks) > 0 {
						inner = &c.ContentBlocks[0]
					}
					if inner != nil {
						advisor.ResultType = string(inner.Type)
						advisor.Text = inner.Text
						advisor.EncryptedContent = inner.EncryptedContent
						advisor.ErrorCode = inner.ErrorCode
						advisor.StopReason = inner.StopReason
					}
				}
				item := &schemas.ResponsesMessage{
					ID:     state.AdvisorToolID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeAdvisorCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:               state.AdvisorToolID,
						Name:                 schemas.Ptr(string(AnthropicToolNameAdvisor)),
						ResponsesAdvisorCall: advisor,
					},
				}
				outputIdx := state.AdvisorOutputIndex
				// Persist into OutputItems so message_stop includes the advisor_call
				// in response.completed (mirrors the web_search_call handling).
				if outputIdx != nil {
					cloned := *item
					clonedToolMsg := *item.ResponsesToolMessage
					cloned.ResponsesToolMessage = &clonedToolMsg
					state.OutputItems[*outputIdx] = &cloned
				}
				state.AdvisorToolID = nil
				state.AdvisorOutputIndex = nil
				state.AdvisorResult = nil
				if chunk.Index != nil {
					delete(state.ContentIndexToBlockType, *chunk.Index)
				}
				return []*schemas.BifrostResponsesStreamResponse{{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: sequenceNumber,
					OutputIndex:    outputIdx,
					ContentIndex:   chunk.Index,
					Item:           item,
				}}, nil, false
			}

			// End of a tool_search_tool_result block — emit the tool_search_call done
			// carrying the discovered tool references (the deferred tools the search found).
			// Mirrors the web_search_call done; the model's subsequent tool_use (calling one
			// of those tools) is forwarded by the generic tool_use path.
			if state.ToolSearchResult != nil && state.ToolSearchToolID != nil {
				var toolRefs []string
				for _, ref := range state.ToolSearchResult.ToolReferences {
					if ref.ToolName != nil {
						toolRefs = append(toolRefs, *ref.ToolName)
					} else if ref.Name != nil {
						toolRefs = append(toolRefs, *ref.Name)
					}
				}
				item := &schemas.ResponsesMessage{
					ID:     state.ToolSearchToolID,
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:                  state.ToolSearchToolID,
						Name:                    state.ToolSearchToolName,
						ResponsesToolSearchCall: &schemas.ResponsesToolSearchCall{ToolReferences: toolRefs},
					},
				}
				outputIdx := state.ToolSearchOutputIndex
				// Persist into OutputItems so message_stop includes the tool_search_call
				// in response.completed (mirrors web_search_call / advisor_call handling).
				if outputIdx != nil {
					cloned := *item
					clonedToolMsg := *item.ResponsesToolMessage
					cloned.ResponsesToolMessage = &clonedToolMsg
					state.OutputItems[*outputIdx] = &cloned
				}
				state.ToolSearchToolID = nil
				state.ToolSearchToolName = nil
				state.ToolSearchOutputIndex = nil
				state.ToolSearchResult = nil
				if chunk.Index != nil {
					delete(state.ContentIndexToBlockType, *chunk.Index)
				}
				return []*schemas.BifrostResponsesStreamResponse{{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
					SequenceNumber: sequenceNumber,
					OutputIndex:    outputIdx,
					ContentIndex:   chunk.Index,
					Item:           item,
				}}, nil, false
			}

			// End of a code-execution result block — emit the code_interpreter_call done.
			if state.CodeExecResult != nil && state.CodeExecToolID != nil {
				// Rebuild the server_tool_use block from accumulated state so the
				// streamed item is identical to the non-streaming converter output.
				serverBlock := AnthropicContentBlock{
					Type: AnthropicContentBlockTypeServerToolUse,
					ID:   state.CodeExecToolID,
					Name: state.CodeExecToolName,
				}
				// Use the input captured when the server_tool_use block closed.
				if state.CodeExecInput != "" {
					serverBlock.Input = json.RawMessage(state.CodeExecInput)
				}
				msgs := []schemas.ResponsesMessage{buildBifrostCodeExecutionCall(serverBlock)}
				attachAnthropicCodeExecutionResult(msgs, *state.CodeExecToolID, *state.CodeExecResult)
				item := &msgs[0]

				outputIdx := state.CodeExecOutputIndex
				itemID := state.CodeExecToolID
				// Persist into OutputItems so response.completed includes the call;
				// the sandbox container is folded in at message_stop.
				if outputIdx != nil {
					cloned := *item
					if item.ResponsesToolMessage != nil {
						clonedTM := *item.ResponsesToolMessage
						cloned.ResponsesToolMessage = &clonedTM
					}
					state.OutputItems[*outputIdx] = &cloned
				}

				state.CodeExecToolID = nil
				state.CodeExecToolName = nil
				state.CodeExecOutputIndex = nil
				state.CodeExecResult = nil
				state.CodeExecInput = ""
				if chunk.Index != nil {
					delete(state.ContentIndexToBlockType, *chunk.Index)
				}

				return []*schemas.BifrostResponsesStreamResponse{
					{
						Type:           schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted,
						SequenceNumber: sequenceNumber,
						OutputIndex:    outputIdx,
						ItemID:         itemID,
					},
					{
						Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
						SequenceNumber: sequenceNumber + 1,
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
					if blockType == AnthropicContentBlockTypeWebSearchToolResult ||
						blockType == AnthropicContentBlockTypeWebFetchToolResult ||
						blockType == AnthropicContentBlockTypeAdvisorToolResult ||
						blockType == AnthropicContentBlockTypeToolSearchToolResult ||
						blockType == AnthropicContentBlockTypeServerToolUse ||
						blockType == AnthropicContentBlockTypeCodeExecutionToolResult ||
						blockType == AnthropicContentBlockTypeBashCodeExecutionToolResult ||
						blockType == AnthropicContentBlockTypeTextEditorCodeExecutionToolResult {
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

			// Capture accumulated text once — shared by output_text.done and output_item.done
			accText := ""
			if buf := state.TextBuffers[outputIndex]; buf != nil {
				accText = buf.String()
			}

			// Check if this content index is a text block
			if chunk.Index != nil {
				if state.TextContentIndices[*chunk.Index] {
					// Emit output_text.done with full accumulated text
					textDoneResponse := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Text:           &accText,
					}
					if itemID != "" {
						textDoneResponse.ItemID = &itemID
					}
					responses = append(responses, textDoneResponse)

					// Emit content_part.done with full accumulated text in Part
					partText := accText
					part := &schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesOutputMessageContentTypeText,
						Text: &partText,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						},
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

					// Clear the text content index tracking
					delete(state.TextContentIndices, *chunk.Index)
				}

				// Check if this content index is a reasoning block
				if state.ReasoningContentIndices[*chunk.Index] {
					// Capture the accumulated reasoning text and signature
					accReasoning := ""
					if buf := state.ReasoningTextBuffers[outputIndex]; buf != nil {
						accReasoning = buf.String()
						delete(state.ReasoningTextBuffers, outputIndex)
					}
					var signature *string
					if sig, ok := state.ReasoningSignatures[outputIndex]; ok && sig != "" {
						sigCopy := sig
						signature = &sigCopy
						delete(state.ReasoningSignatures, outputIndex)
					}

					// Fold them into the stored item with the same shape the
					// non-streaming converter produces (a reasoning_text content
					// block carrying text + signature), so output_item.done and
					// response.completed carry a replayable reasoning item.
					if storedItem, exists := state.OutputItems[outputIndex]; exists {
						textCopy := accReasoning
						storedItem.Content = &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
									Text:      &textCopy,
									Signature: signature,
								},
							},
						}
						storedItem.ResponsesReasoning = nil
					}

					// Emit reasoning_summary_text.done (reasoning equivalent of
					// output_text.done) with the full accumulated text
					doneText := accReasoning
					reasoningDoneResponse := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Text:           &doneText,
					}
					if itemID != "" {
						reasoningDoneResponse.ItemID = &itemID
					}
					responses = append(responses, reasoningDoneResponse)

					// Emit content_part.done for reasoning with the completed part
					partText := accReasoning
					partDoneResponse := &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
						SequenceNumber: sequenceNumber + len(responses),
						OutputIndex:    schemas.Ptr(outputIndex),
						ContentIndex:   chunk.Index,
						Part: &schemas.ResponsesMessageContentBlock{
							Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
							Text:      &partText,
							Signature: signature,
						},
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
				state.UsedStructuredOutputTool = true

				return responses, nil, false
			}

			// Check if this is a tool call (function_call or MCP call)
			// If we have accumulated arguments, emit appropriate arguments.done first
			// Note: we check hasArgs only (not accumulatedArgs != "") to handle zero-arg tool calls
			if accumulatedArgs, hasArgs := state.ToolArgumentBuffers[outputIndex]; hasArgs {
				// Update the stored output item with the final arguments
				if storedItem, exists := state.OutputItems[outputIndex]; exists && storedItem.ResponsesToolMessage != nil {
					storedItem.ResponsesToolMessage.Arguments = &accumulatedArgs
					storedItem.Status = schemas.Ptr("completed")
				}

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
			var doneItem *schemas.ResponsesMessage
			if storedItem, exists := state.OutputItems[outputIndex]; exists {
				copied := *storedItem
				if storedItem.ResponsesToolMessage != nil {
					toolMsgCopy := *storedItem.ResponsesToolMessage
					copied.ResponsesToolMessage = &toolMsgCopy
				}
				doneItem = &copied
			} else {
				// Build content blocks from accumulated text (captured above)
				contentBlocks := []schemas.ResponsesMessageContentBlock{}
				if accText != "" {
					textCopy := accText
					contentBlocks = []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: &textCopy,
							ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
								Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
								LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
							},
						},
					}
				}
				delete(state.TextBuffers, outputIndex)
				doneItem = &schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Status: &statusCompleted,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: contentBlocks,
					},
				}
				if doneItemID != "" {
					doneItem.ID = &doneItemID
				}
				// Only persist synthesized items that actually have text content — reasoning
				// and MCP blocks fall through here without storedItems but must not pollute
				// response.completed with empty assistant message shells.
				if len(contentBlocks) > 0 {
					cloned := *doneItem
					clonedContent := *doneItem.Content
					cloned.Content = &clonedContent
					state.OutputItems[outputIndex] = &cloned
				}
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
		// The sandbox container is delivered here, after all content blocks; fold
		// it onto the code_interpreter_call(s) at message_stop.
		if chunk.Delta.Container != nil {
			state.Container = chunk.Delta.Container
		}
		if chunk.Delta.StopReason != nil {
			mapped := ConvertAnthropicFinishReasonToBifrost(*chunk.Delta.StopReason)
			if state.UsedStructuredOutputTool && !state.SeenRealToolCall &&
				mapped == string(schemas.BifrostFinishReasonToolCalls) {
				mapped = string(schemas.BifrostFinishReasonStop)
			}
			state.StopReason = &mapped
		}
		// Check if integration type in ctx is anthropic
		if ctx.Value(schemas.BifrostContextKeyIntegrationType) == "anthropic" {
			// Convert usage from Anthropic format to Bifrost
			bifrostUsage := ConvertAnthropicUsageToBifrostUsage(chunk.Usage)

			// Use the already-remapped stop reason so SO overrides are preserved.
			var stopReason *string
			if state.StopReason != nil {
				stopReason = state.StopReason
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
				response.Speed = chunk.Usage.Speed
				response.InferenceGeo = chunk.Usage.InferenceGeo
			}
			// Carry the sandbox container on the message_delta event so the reverse
			// converter can re-emit it (Anthropic delivers it here, not earlier).
			if state.Container != nil {
				response.Container = &schemas.ResponsesResponseContainer{
					ID:        state.Container.ID,
					ExpiresAt: state.Container.ExpiresAt,
				}
			}

			// Mark that we already emitted a message_delta so response.completed
			// doesn't synthesize a duplicate one.
			state.HasEmittedMessageDelta = true

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

		// Fold the sandbox container (delivered on the final message_delta) onto
		// every code_interpreter_call so response.completed carries it (mirrors the
		// non-streaming container lift in ToBifrostResponsesResponse). Also expose it
		// at the response level so the reverse converter can re-emit it if it builds
		// the message_delta from this completed event.
		if state.Container != nil {
			response.Container = &schemas.ResponsesResponseContainer{
				ID:        state.Container.ID,
				ExpiresAt: state.Container.ExpiresAt,
			}
			for _, item := range state.OutputItems {
				if item == nil || item.Type == nil ||
					*item.Type != schemas.ResponsesMessageTypeCodeInterpreterCall ||
					item.ResponsesToolMessage == nil {
					continue
				}
				if item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall == nil {
					item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall = &schemas.ResponsesCodeInterpreterToolCall{}
				}
				item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.ContainerID = state.Container.ID
				if state.Container.ExpiresAt != nil {
					if item.ResponsesToolMessage.ResponsesCodeExecutionCall == nil {
						item.ResponsesToolMessage.ResponsesCodeExecutionCall = &schemas.ResponsesCodeExecutionCall{}
					}
					item.ResponsesToolMessage.ResponsesCodeExecutionCall.ContainerExpiresAt = state.Container.ExpiresAt
				}
			}
		}

		// Populate the Output array from accumulated items for response.completed
		// This is needed for clients that check Output for function_call items
		if len(state.OutputItems) > 0 {
			// Sort by output index to maintain order
			response.Output = make([]schemas.ResponsesMessage, 0, len(state.OutputItems))
			for i := 0; i < state.CurrentOutputIndex; i++ {
				if item, exists := state.OutputItems[i]; exists {
					response.Output = append(response.Output, *item)
				}
			}
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
			// Use actual usage if available (forwarded from upstream message_start),
			// otherwise fall back to zeros for non-Anthropic providers
			var messageUsage *AnthropicUsage
			if bifrostResp.Response.Usage != nil {
				messageUsage = ConvertBifrostUsageToAnthropicUsage(bifrostResp.Response.Usage)
			} else {
				messageUsage = &AnthropicUsage{
					InputTokens:              0,
					OutputTokens:             0,
					CacheReadInputTokens:     0,
					CacheCreationInputTokens: 0,
					CacheCreation: AnthropicUsageCacheCreation{
						Ephemeral5mInputTokens: 0,
						Ephemeral1hInputTokens: 0,
					},
				}
			}
			streamMessage := &AnthropicMessageResponse{
				Type:    "message",
				Role:    "assistant",
				Content: []AnthropicContentBlock{}, // Always empty array in message_start
				Usage:   messageUsage,
			}
			if bifrostResp.Response.ID != nil {
				streamMessage.ID = *bifrostResp.Response.ID
			}
			// Prefer Response.Model, then ResolvedModelUsed, then OriginalModelRequested
			if bifrostResp.Response != nil && bifrostResp.Response.Model != "" {
				streamMessage.Model = bifrostResp.Response.Model
			} else if bifrostResp.ExtraFields.ResolvedModelUsed != "" {
				streamMessage.Model = bifrostResp.ExtraFields.ResolvedModelUsed
			} else if bifrostResp.ExtraFields.OriginalModelRequested != "" {
				streamMessage.Model = bifrostResp.ExtraFields.OriginalModelRequested
			}
			// Cache diagnostics arrives on message_start (cache-diagnosis-2026-04-07).
			if bifrostResp.Response.Diagnostics != nil {
				streamMessage.Diagnostics = bifrostResp.Response.Diagnostics
			}
			streamResp.Message = streamMessage
		}
	case schemas.ResponsesStreamResponseTypeInProgress:
		// Skip converting response.in_progress back to avoid duplicate message_start events
		// This is an OpenAI-style lifecycle event that doesn't map directly to Anthropic events
		return nil

	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		// Every output item starts exactly one primary Anthropic content block here;
		// allocate its index once (deltas/stops look it up, result blocks get fresh
		// indices). This is the single source of truth for block numbering.
		addedState := getOrCreateAnthropicToResponsesStreamState(ctx)
		blockIdx := addedState.allocBlockIndex(reverseStreamItemKey(bifrostResp))

		// Check if this is a computer tool call
		if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeComputerCall {

			// Computer tool - emit content_block_start
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			streamResp.Index = blockIdx

			// Build the content_block as tool_use
			// Note: Computer tool calls should not be converted to thinking blocks
			contentBlock := &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   providerUtils.SanitizeAnthropicToolUseIDPtr(bifrostResp.Item.ID), // The tool use ID
				Name: schemas.Ptr(string(AnthropicToolNameComputer)),                   // "computer"
			}

			// Always start with empty input for streaming compatibility
			contentBlock.Input = json.RawMessage("{}")

			streamResp.ContentBlock = contentBlock
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {

			// Web search call - emit content_block_start with server_tool_use
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			streamResp.Index = blockIdx

			// Build the content_block as server_tool_use
			contentBlock := &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeServerToolUse,
				ID:   providerUtils.SanitizeAnthropicToolUseIDPtr(bifrostResp.Item.ID), // The tool use ID
				Name: schemas.Ptr(string(AnthropicToolNameWebSearch)),                  // "web_search"
			}

			// Deliver the query whole in content_block_start (no input_json_delta),
			// matching native: a web_search is spawned atomically (in PTC, by the
			// sandbox), so Anthropic never streams its input. Fall back to empty.
			contentBlock.Input = json.RawMessage("{}")
			if tm := bifrostResp.Item.ResponsesToolMessage; tm != nil && tm.Action != nil &&
				tm.Action.ResponsesWebSearchToolCallAction != nil &&
				tm.Action.ResponsesWebSearchToolCallAction.Query != nil {
				if inputBytes, err := providerUtils.MarshalSorted(map[string]interface{}{"query": *tm.Action.ResponsesWebSearchToolCallAction.Query}); err == nil {
					contentBlock.Input = json.RawMessage(inputBytes)
				}
			}

			// Preserve the caller (set when this search was spawned from inside the
			// code execution sandbox — programmatic tool calling).
			if tm := bifrostResp.Item.ResponsesToolMessage; tm != nil && tm.Caller != nil {
				contentBlock.Caller = &AnthropicToolCaller{
					Type:   AnthropicToolCallerType(tm.Caller.Type),
					ToolID: providerUtils.SanitizeAnthropicToolUseIDPtr(tm.Caller.ToolID),
				}
			}

			streamResp.ContentBlock = contentBlock
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeAdvisorCall {

			// Advisor call - emit content_block_start with server_tool_use (input is always empty)
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			streamResp.Index = blockIdx
			toolUseID := bifrostResp.Item.ID
			if bifrostResp.Item.ResponsesToolMessage != nil && bifrostResp.Item.ResponsesToolMessage.CallID != nil {
				toolUseID = bifrostResp.Item.ResponsesToolMessage.CallID
			}
			toolUseID = providerUtils.SanitizeAnthropicToolUseIDPtr(toolUseID)
			streamResp.ContentBlock = &AnthropicContentBlock{
				Type:  AnthropicContentBlockTypeServerToolUse,
				ID:    toolUseID,
				Name:  schemas.Ptr(string(AnthropicToolNameAdvisor)),
				Input: json.RawMessage("{}"),
			}
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeCodeInterpreterCall {

			// Code interpreter call - emit content_block_start with server_tool_use.
			// Input is empty here; the verbatim input is streamed at output_item.done.
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			streamResp.Index = blockIdx
			toolUseID := bifrostResp.Item.ID
			toolName := string(AnthropicToolNameCodeExecution)
			if tm := bifrostResp.Item.ResponsesToolMessage; tm != nil {
				if tm.CallID != nil {
					toolUseID = tm.CallID
				}
				if tm.ResponsesCodeExecutionCall != nil && tm.ResponsesCodeExecutionCall.ToolName != "" {
					toolName = tm.ResponsesCodeExecutionCall.ToolName
				}
			}
			// Remember the sub-tool so code.done can rebuild {"code"|"command": …}.
			if addedState.codeExecToolNameByItem == nil {
				addedState.codeExecToolNameByItem = make(map[string]string)
			}
			addedState.codeExecToolNameByItem[reverseStreamItemKey(bifrostResp)] = toolName
			streamResp.ContentBlock = &AnthropicContentBlock{
				Type:  AnthropicContentBlockTypeServerToolUse,
				ID:    providerUtils.SanitizeAnthropicToolUseIDPtr(toolUseID),
				Name:  schemas.Ptr(toolName),
				Input: json.RawMessage("{}"),
			}
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebFetchCall {

			// Web fetch call - emit content_block_start with server_tool_use. The
			// paired web_fetch_tool_result block is emitted at output_item.done when
			// the typed result payload is available.
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			streamResp.Index = blockIdx
			if blocks := convertBifrostWebFetchCallToAnthropicBlocks(bifrostResp.Item); len(blocks) > 0 {
				streamResp.ContentBlock = &blocks[0]
			}
		} else {
			// Text or other content blocks - emit content_block_start
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			streamResp.Index = blockIdx

			// Build content_block based on item type
			if bifrostResp.Item != nil {
				contentBlock := &AnthropicContentBlock{}

				// Check if this is a compaction item (message with compaction content block)
				if isCompactionItem(bifrostResp.Item) {
					contentBlock.Type = AnthropicContentBlockTypeCompaction
					contentBlock.Content = &AnthropicContent{ContentStr: schemas.Ptr("")}
					if bifrostResp.Item.Content.ContentBlocks[0].CacheControl != nil {
						contentBlock.CacheControl = bifrostResp.Item.Content.ContentBlocks[0].CacheControl
					}
				} else if bifrostResp.Item.Type != nil {
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
									contentBlock.ID = providerUtils.SanitizeAnthropicToolUseIDPtr(bifrostResp.Item.ResponsesToolMessage.CallID)
									contentBlock.Name = bifrostResp.Item.ResponsesToolMessage.Name
									// Always start with empty input for streaming compatibility
									contentBlock.Input = json.RawMessage("{}")

									// Track WebSearch tools so we can skip their argument deltas
									// and regenerate them synthetically (with sanitization) at output_item.done
									if bifrostResp.Item.ResponsesToolMessage.Name != nil &&
										*bifrostResp.Item.ResponsesToolMessage.Name == "WebSearch" &&
										bifrostResp.Item.ID != nil {
										streamState := getOrCreateAnthropicToResponsesStreamState(ctx)
										if streamState.webSearchItemIDs == nil {
											streamState.webSearchItemIDs = make(map[string]bool)
										}
										streamState.webSearchItemIDs[*bifrostResp.Item.ID] = true
									}
								}
							}
						}
					case schemas.ResponsesMessageTypeMCPCall:
						contentBlock.Type = AnthropicContentBlockTypeMCPToolUse
						if bifrostResp.Item.ResponsesToolMessage != nil {
							contentBlock.ID = providerUtils.SanitizeAnthropicToolUseIDPtr(bifrostResp.Item.ID)
							contentBlock.Name = bifrostResp.Item.ResponsesToolMessage.Name
							if bifrostResp.Item.ResponsesToolMessage.ResponsesMCPToolCall != nil {
								contentBlock.ServerName = &bifrostResp.Item.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
							}
							// Always start with empty input for streaming compatibility
							contentBlock.Input = json.RawMessage("{}")
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

		// Generate compaction_delta event for compaction items
		if isCompactionItem(bifrostResp.Item) {
			block := bifrostResp.Item.Content.ContentBlocks[0]
			if block.ResponsesOutputMessageContentCompaction != nil {
				events = append(events, &AnthropicStreamEvent{
					Type:  AnthropicStreamEventTypeContentBlockDelta,
					Index: blockIdx,
					Delta: &AnthropicStreamDelta{
						Type:    AnthropicStreamDeltaTypeCompaction,
						Content: &block.ResponsesOutputMessageContentCompaction.Summary,
					},
				})
			}
		}

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
					if jsonBytes, err := providerUtils.MarshalSorted(actionInput); err == nil {
						argumentsJSON = string(jsonBytes)
						shouldGenerateDeltas = true
					}
				}
			}
			if shouldGenerateDeltas && argumentsJSON != "" {
				// Generate synthetic input_json_delta events by chunking the JSON.
				deltaEvents := generateSyntheticInputJSONDeltas(argumentsJSON, blockIdx)
				events = append(events, deltaEvents...)
			}
		}

		return events
	case schemas.ResponsesStreamResponseTypeContentPartAdded:
		return nil

	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))
		if bifrostResp.Delta != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type: AnthropicStreamDeltaTypeText,
				Text: bifrostResp.Delta,
			}
		}

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		// Skip WebSearch tool argument deltas - they will be sent synthetically in output_item.done
		if bifrostResp.ItemID != nil {
			streamState := getOrCreateAnthropicToResponsesStreamState(ctx)
			if streamState.webSearchItemIDs[*bifrostResp.ItemID] {
				return nil
			}
		}

		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))
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
		streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))

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
			streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))

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

			indexToUse := getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))
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
				streamState := getOrCreateAnthropicToResponsesStreamState(ctx)
				delete(streamState.webSearchItemIDs, *bifrostResp.Item.ID)
			}

			return events
		}

		if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeComputerCall {

			// Computer tool complete - emit content_block_delta with the action, then stop
			// Note: We're sending the complete action JSON in one delta
			streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
			streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))

			// Convert the action to Anthropic format and marshal to JSON
			if bifrostResp.Item.ResponsesToolMessage != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action != nil &&
				bifrostResp.Item.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {

				actionInput := convertResponsesToAnthropicComputerAction(
					bifrostResp.Item.ResponsesToolMessage.Action.ResponsesComputerToolCallAction,
				)

				// Marshal the action to JSON string
				if jsonBytes, err := providerUtils.MarshalSorted(actionInput); err == nil {
					jsonStr := string(jsonBytes)
					streamResp.Delta = &AnthropicStreamDelta{
						Type:        AnthropicStreamDeltaTypeInputJSON,
						PartialJSON: &jsonStr,
					}
				}
			}
			return []*AnthropicStreamEvent{
				streamResp,
				{
					Type:  AnthropicStreamEventTypeContentBlockStop,
					Index: streamResp.Index,
				},
			}
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {

			// Web search call complete - generate synthetic input_json_delta events, then emit content_block_stop
			var events []*AnthropicStreamEvent
			state := getOrCreateAnthropicToResponsesStreamState(ctx)
			serverIdx := state.blockIndexFor(reverseStreamItemKey(bifrostResp))

			tm := bifrostResp.Item.ResponsesToolMessage
			wsAction := (*schemas.ResponsesWebSearchToolCallAction)(nil)
			if tm != nil && tm.Action != nil {
				wsAction = tm.Action.ResponsesWebSearchToolCallAction
			}

			// 1. Stop the server_tool_use. The query was already delivered whole in
			// content_block_start (no input_json_delta), matching native.
			events = append(events, &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeContentBlockStop,
				Index: serverIdx,
			})

			// 2. Emit the paired web_search_tool_result block at a fresh index.
			if wsAction != nil && len(wsAction.Sources) > 0 {
				resultIndex := state.allocBlockIndex("")

				var resultContentBlocks []AnthropicContentBlock
				for _, source := range wsAction.Sources {
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

				resultBlock := &AnthropicContentBlock{
					Type:      AnthropicContentBlockTypeWebSearchToolResult,
					ToolUseID: providerUtils.SanitizeAnthropicToolUseIDPtr(bifrostResp.Item.ID), // Link to the server_tool_use block
					Content:   &AnthropicContent{ContentBlocks: resultContentBlocks},
				}
				// Carry the programmatic-tool-calling caller onto the result too.
				if tm != nil && tm.Caller != nil {
					resultBlock.Caller = &AnthropicToolCaller{
						Type:   AnthropicToolCallerType(tm.Caller.Type),
						ToolID: providerUtils.SanitizeAnthropicToolUseIDPtr(tm.Caller.ToolID),
					}
				}
				events = append(events,
					&AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStart, Index: resultIndex, ContentBlock: resultBlock},
					&AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStop, Index: resultIndex},
				)
			} else if state.passthrough {
				// A resultless or error web_search (no sources, max_uses_exceeded,
				// web_search_tool_result_error, etc.) still consumed a content-block
				// index upstream. On the passthrough path, later non-server-tool frames
				// may be forwarded raw, so consume that hidden index to keep the
				// converter's next allocated index in lockstep with upstream.
				_ = state.allocBlockIndex("")
			}

			return events
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebFetchCall {

			// Web fetch call complete - close the server_tool_use block, then emit
			// the paired web_fetch_tool_result block when present.
			state := getOrCreateAnthropicToResponsesStreamState(ctx)
			serverIdx := state.blockIndexFor(reverseStreamItemKey(bifrostResp))
			events := []*AnthropicStreamEvent{{
				Type:  AnthropicStreamEventTypeContentBlockStop,
				Index: serverIdx,
			}}
			if blocks := convertBifrostWebFetchCallToAnthropicBlocks(bifrostResp.Item); len(blocks) > 1 {
				resultIdx := state.allocBlockIndex("")
				resultBlock := blocks[1]
				events = append(events,
					&AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStart, Index: resultIdx, ContentBlock: &resultBlock},
					&AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStop, Index: resultIdx},
				)
			} else if state.passthrough {
				// Older/partial replay items can lack the typed web_fetch result even
				// though upstream consumed a result-block index. Keep later raw
				// passthrough frames aligned with the converter's allocator.
				_ = state.allocBlockIndex("")
			}
			return events
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeCodeInterpreterCall {

			// Code interpreter call complete.
			var events []*AnthropicStreamEvent
			blocks := convertBifrostCodeExecCallToAnthropicBlocks(bifrostResp.Item)
			state := getOrCreateAnthropicToResponsesStreamState(ctx)

			// For python/bash the server_tool_use block was already closed early on
			// code.done. text_editor (and any item not closed there) is still open —
			// close it now from the verbatim carry input, which carries its full
			// multi-key payload (command/path/file_text/…) that Code can't.
			if !state.codeExecServerClosedByItem[reverseStreamItemKey(bifrostResp)] {
				serverIdx := state.blockIndexFor(reverseStreamItemKey(bifrostResp))
				if len(blocks) > 0 && len(blocks[0].Input) > 0 {
					events = append(events, generateSyntheticInputJSONDeltas(string(blocks[0].Input), serverIdx)...)
				}
				events = append(events, &AnthropicStreamEvent{
					Type:  AnthropicStreamEventTypeContentBlockStop,
					Index: serverIdx,
				})
			}

			if len(blocks) > 1 {
				resultIdx := state.allocBlockIndex("")
				resultBlock := blocks[1]
				events = append(events,
					&AnthropicStreamEvent{
						Type:         AnthropicStreamEventTypeContentBlockStart,
						Index:        resultIdx,
						ContentBlock: &resultBlock,
					},
					&AnthropicStreamEvent{
						Type:  AnthropicStreamEventTypeContentBlockStop,
						Index: resultIdx,
					},
				)
			}

			return events
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			*bifrostResp.Item.Type == schemas.ResponsesMessageTypeAdvisorCall {

			// Advisor call complete - emit content_block_stop for the server_tool_use
			// block, then the advisor_tool_result block (start + stop) at a fresh index.
			var events []*AnthropicStreamEvent
			state := getOrCreateAnthropicToResponsesStreamState(ctx)

			toolUseID := bifrostResp.Item.ID
			if bifrostResp.Item.ResponsesToolMessage != nil && bifrostResp.Item.ResponsesToolMessage.CallID != nil {
				toolUseID = bifrostResp.Item.ResponsesToolMessage.CallID
			}
			toolUseID = providerUtils.SanitizeAnthropicToolUseIDPtr(toolUseID)

			serverIdx := state.blockIndexFor(reverseStreamItemKey(bifrostResp))

			// 1. content_block_stop for the server_tool_use block.
			events = append(events, &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeContentBlockStop,
				Index: serverIdx,
			})

			// 2. content_block_start for advisor_tool_result at a fresh index.
			resultIdx := state.allocBlockIndex("")
			resultBlock := &AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeAdvisorToolResult,
				ToolUseID: toolUseID,
			}
			if bifrostResp.Item.ResponsesToolMessage != nil && bifrostResp.Item.ResponsesToolMessage.ResponsesAdvisorCall != nil {
				adv := bifrostResp.Item.ResponsesToolMessage.ResponsesAdvisorCall
				resultType := adv.ResultType
				if resultType == "" {
					resultType = "advisor_result"
				}
				resultBlock.Content = &AnthropicContent{
					ContentObj: &AnthropicContentBlock{
						Type:             AnthropicContentBlockType(resultType),
						Text:             adv.Text,
						EncryptedContent: adv.EncryptedContent,
						ErrorCode:        adv.ErrorCode,
						StopReason:       adv.StopReason,
					},
				}
			}
			events = append(events, &AnthropicStreamEvent{
				Type:         AnthropicStreamEventTypeContentBlockStart,
				Index:        resultIdx,
				ContentBlock: resultBlock,
			})

			// 3. content_block_stop for the advisor_tool_result block.
			events = append(events, &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeContentBlockStop,
				Index: resultIdx,
			})

			return events
		} else if bifrostResp.Item != nil &&
			bifrostResp.Item.Type != nil &&
			(*bifrostResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall ||
				*bifrostResp.Item.Type == schemas.ResponsesMessageTypeMCPCall) {

			// Function call or MCP call complete - just emit content_block_stop
			streamResp.Type = AnthropicStreamEventTypeContentBlockStop
			streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))
		} else {
			// For text blocks and other content blocks, emit content_block_stop
			streamResp.Type = AnthropicStreamEventTypeContentBlockStop
			streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))
		}
	case schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		schemas.ResponsesStreamResponseTypeWebSearchCallCompleted:
		// Web search lifecycle events - these are OpenAI-style events that don't have Anthropic equivalents
		// Skip them to avoid cluttering the stream
		return nil

	case schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone:
		// Close the code server_tool_use block early — before any nested web_search
		// blocks — only for python/bash, whose entire input reconstructs from the
		// neutral Code (`{"code"|"command": …}`) and which can spawn nested blocks
		// (programmatic tool calling), so sequential ordering requires the early
		// close. text_editor has a multi-key input that Code can't carry (Code is
		// empty) and never spawns nested blocks, so leave its block open and let
		// output_item.done close it from the verbatim carry input.
		if bifrostResp.Code == nil || *bifrostResp.Code == "" {
			return nil
		}
		state := getOrCreateAnthropicToResponsesStreamState(ctx)
		key := reverseStreamItemKey(bifrostResp)
		idx := state.blockIndexFor(key)
		inputKey := "code"
		if AnthropicToolName(state.codeExecToolNameByItem[key]) == AnthropicToolNameBashCodeExecution {
			inputKey = "command"
		}
		var events []*AnthropicStreamEvent
		if inputBytes, err := providerUtils.MarshalSorted(map[string]interface{}{inputKey: *bifrostResp.Code}); err == nil {
			events = append(events, generateSyntheticInputJSONDeltas(string(inputBytes), idx)...)
		}
		events = append(events, &AnthropicStreamEvent{Type: AnthropicStreamEventTypeContentBlockStop, Index: idx})
		if state.codeExecServerClosedByItem == nil {
			state.codeExecServerClosedByItem = make(map[string]bool)
		}
		state.codeExecServerClosedByItem[key] = true
		return events

	case schemas.ResponsesStreamResponseTypeCodeInterpreterCallInProgress,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallInterpreting,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted:
		// Other code interpreter lifecycle events have no Anthropic equivalent.
		return nil

	case schemas.ResponsesStreamResponseTypePing:
		streamResp.Type = AnthropicStreamEventTypePing

	case schemas.ResponsesStreamResponseTypeCompleted:
		streamResp.Type = AnthropicStreamEventTypeMessageStop
		// If a message_delta was already emitted from the upstream event, only emit message_stop
		// to avoid sending a duplicate message_delta to the client.
		if alreadyEmitted, ok := ctx.Value(schemas.BifrostContextKeyHasEmittedMessageDelta).(bool); ok && alreadyEmitted {
			return []*AnthropicStreamEvent{streamResp}
		}
		stopReason := AnthropicStopReasonEndTurn
		if bifrostResp.Response != nil {
			stopReason = inferAnthropicStopReasonFromBifrostResponse(bifrostResp.Response, nil)
		}
		anthropicContentDeltaEvent := &AnthropicStreamEvent{
			Type: AnthropicStreamEventTypeMessageDelta,
			Delta: &AnthropicStreamDelta{
				StopReason: &stopReason,
			},
		}
		// Convert usage from Bifrost to Anthropic
		if bifrostResp.Response != nil {
			anthropicContentDeltaEvent.Usage = ConvertBifrostUsageToAnthropicUsage(bifrostResp.Response.Usage)
			// Re-emit the code-execution sandbox container on the message_delta.
			if bifrostResp.Response.Container != nil {
				if anthropicContentDeltaEvent.Delta == nil {
					anthropicContentDeltaEvent.Delta = &AnthropicStreamDelta{}
				}
				anthropicContentDeltaEvent.Delta.Container = &AnthropicResponseContainer{
					ID:        bifrostResp.Response.Container.ID,
					ExpiresAt: bifrostResp.Response.Container.ExpiresAt,
				}
			}
		}
		return []*AnthropicStreamEvent{anthropicContentDeltaEvent, streamResp}

	case schemas.ResponsesStreamResponseTypeMCPCallArgumentsDelta:
		// MCP call arguments delta - convert to content_block_delta with input_json
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))
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
		streamResp.Index = getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexFor(reverseStreamItemKey(bifrostResp))

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

			// Re-emit the code-execution sandbox container on message_delta (read
			// straight off the event — Anthropic delivers it here natively).
			if bifrostResp.Response != nil && bifrostResp.Response.Container != nil {
				if streamResp.Delta == nil {
					streamResp.Delta = &AnthropicStreamDelta{}
				}
				streamResp.Delta.Container = &AnthropicResponseContainer{
					ID:        bifrostResp.Response.Container.ID,
					ExpiresAt: bifrostResp.Response.Container.ExpiresAt,
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
	provider, model := schemas.ParseModelString(req.Model, "")

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
		key := strings.TrimSpace(*req.Metadata.UserID)
		if key != "" {
			if len(key) > 64 {
				sum := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
				key = sum
			}
			params.PromptCacheKey = &key
		}
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
	if req.Diagnostics != nil {
		params.ExtraParams["diagnostics"] = req.Diagnostics
	}
	if req.TopK != nil {
		params.ExtraParams["top_k"] = *req.TopK
	}
	if req.Speed != nil {
		params.ExtraParams["speed"] = *req.Speed
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
	if req.OutputConfig != nil && req.OutputConfig.TaskBudget != nil {
		params.ExtraParams["task_budget"] = req.OutputConfig.TaskBudget
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
			// If the request was sent with display:"omitted"
			if req.Thinking.Display != nil && *req.Thinking.Display == "omitted" {
				summary = schemas.Ptr("none")
			}
			if req.OutputConfig != nil && req.OutputConfig.Effort != nil {
				// Native effort present — map to Bifrost enum (e.g., "max" → "high")
				params.Reasoning = &schemas.ResponsesParametersReasoning{
					Effort:    schemas.Ptr(*req.OutputConfig.Effort),
					MaxTokens: req.Thinking.BudgetTokens,
					Summary:   summary,
				}
			} else if req.Thinking.BudgetTokens != nil {
				// Fallback: convert budget_tokens to effort
				params.Reasoning = &schemas.ResponsesParametersReasoning{
					Effort:    schemas.Ptr(providerUtils.GetReasoningEffortFromBudgetTokens(*req.Thinking.BudgetTokens, MinimumReasoningMaxTokens, providerUtils.GetMaxOutputTokensOrDefault(req.Model, AnthropicDefaultMaxTokens))),
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
	if req.ServiceTier != nil {
		mapped := MapAnthropicRequestServiceTierToBifrost(*req.ServiceTier)
		params.ServiceTier = &mapped
	}

	// Add truncation parameter if computer tool is being used
	if provider == schemas.OpenAI && req.Tools != nil {
		for _, tool := range req.Tools {
			if tool.Type == nil {
				continue
			}
			switch *tool.Type {
			case AnthropicToolTypeComputer20250124, AnthropicToolTypeComputer20251124:
				params.Truncation = schemas.Ptr("auto")
			case AnthropicToolTypeWebSearch20250305, AnthropicToolTypeWebSearch20260209:
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
				applyAnthropicToolFlagsToResponsesTool(&tool, bifrostTool)
				bifrostTools = append(bifrostTools, *bifrostTool)
			}
		}
		if len(bifrostTools) > 0 {
			bifrostReq.Params.Tools = bifrostTools
		}
	}

	if req.MCPServers != nil {
		// Build a map of mcp_toolset entries from tools[] keyed by mcp_server_name.
		// Stores the full *AnthropicTool (not just *AnthropicMCPToolsetTool) so
		// top-level Anthropic tool flags (DeferLoading, AllowedCallers,
		// InputExamples, EagerInputStreaming) survive the mcp_servers merge path —
		// without this, mcp_toolset tools bypass applyAnthropicToolFlagsToResponsesTool
		// because convertAnthropicToolToBifrost skips them.
		toolsetByServer := make(map[string]*AnthropicTool)
		if req.Tools != nil {
			for i := range req.Tools {
				if req.Tools[i].MCPToolset != nil {
					toolsetByServer[req.Tools[i].MCPToolset.MCPServerName] = &req.Tools[i]
				}
			}
		}

		var bifrostMCPTools []schemas.ResponsesTool
		for _, mcpServer := range req.MCPServers {
			bifrostMCPTool := convertAnthropicMCPServerV2ToBifrostTool(&mcpServer)
			if bifrostMCPTool != nil {
				// Merge mcp_toolset configs (allowed tools) + Anthropic tool flags if present
				if toolWithFlags, ok := toolsetByServer[mcpServer.Name]; ok {
					applyMCPToolsetConfigToBifrostTool(bifrostMCPTool, toolWithFlags.MCPToolset)
					applyAnthropicToolFlagsToResponsesTool(toolWithFlags, bifrostMCPTool)
				}
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
		MaxTokens: providerUtils.GetMaxOutputTokensOrDefault(bifrostReq.Model, AnthropicDefaultMaxTokens),
	}

	// capModel is the canonical model string used only for capability/version
	capModel := schemas.ResolveCanonicalModel(ctx, bifrostReq.Model)

	// Convert basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxOutputTokens != nil {
			anthropicReq.MaxTokens = *bifrostReq.Params.MaxOutputTokens
		}
		// Opus 4.7+ and the Fable/Mythos family reject temperature, top_p, and
		// top_k with a 400 error.
		if !IsAdaptiveOnlyThinkingModel(capModel) {
			// Anthropic doesn't allow both temperature and top_p to be specified.
			// If both are present, prefer temperature (more commonly used).
			if bifrostReq.Params.Temperature != nil {
				anthropicReq.Temperature = bifrostReq.Params.Temperature
			} else if bifrostReq.Params.TopP != nil {
				anthropicReq.TopP = bifrostReq.Params.TopP
			}
		}
		if bifrostReq.Params.User != nil {
			anthropicReq.Metadata = &AnthropicMetaData{
				UserID: bifrostReq.Params.User,
			}
		}
		if bifrostReq.Params.Text != nil {
			// Vertex and Bedrock Mantle don't accept native structured outputs
			// (output_config.format), so convert to a tool instead.
			if bifrostReq.Provider == schemas.Vertex || bifrostReq.Provider == schemas.BedrockMantle {
				if bifrostReq.Params.Text.Format != nil {
					responseFormatTool := convertResponsesTextFormatToTool(ctx, bifrostReq.Params.Text)
					if responseFormatTool != nil {
						if anthropicReq.Tools == nil {
							anthropicReq.Tools = []AnthropicTool{}
						}
						anthropicReq.Tools = append(anthropicReq.Tools, *responseFormatTool)
						thinkingEnabled := bifrostReq.Params.Reasoning != nil &&
							(bifrostReq.Params.Reasoning.MaxTokens != nil ||
								(bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none"))
						if !thinkingEnabled {
							anthropicReq.ToolChoice = &AnthropicToolChoice{
								Type: "tool",
								Name: responseFormatTool.Name,
							}
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
				if IsAdaptiveOnlyThinkingModel(capModel) {
					// Opus 4.7+ and Fable/Mythos: budget_tokens removed; adaptive thinking is the only thinking-on mode.
					anthropicReq.Thinking = &AnthropicThinking{Type: "adaptive"}
					// Preserve a co-present effort — these models support
					// output_config.effort, and the budget is otherwise dropped.
					if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
						setEffortOnOutputConfig(anthropicReq, MapBifrostEffortToAnthropic(*bifrostReq.Params.Reasoning.Effort))
					}
				} else {
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
				}
			} else {
				if bifrostReq.Params.Reasoning.Effort != nil {
					if *bifrostReq.Params.Reasoning.Effort != "none" {
						effort := MapBifrostEffortToAnthropic(*bifrostReq.Params.Reasoning.Effort)

						if SupportsAdaptiveThinking(capModel) {
							// Opus 4.6+ and Opus 4.7+: adaptive thinking + native effort
							anthropicReq.Thinking = &AnthropicThinking{Type: "adaptive"}
							setEffortOnOutputConfig(anthropicReq, effort)
						} else if SupportsNativeEffort(capModel) {
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
					} else if !IsFableFamily(capModel) {
						// Fable/Mythos reject thinking:{type:"disabled"} with a 400 —
						// adaptive thinking is always on and cannot be disabled. Omit
						// the thinking param entirely for that family; all other
						// models take the explicit disabled path.
						anthropicReq.Thinking = &AnthropicThinking{
							Type: "disabled",
						}
					}
				}
			}
			if anthropicReq.Thinking != nil && anthropicReq.Thinking.Type != "disabled" {
				if bifrostReq.Params.Reasoning != nil &&
					bifrostReq.Params.Reasoning.Summary != nil {
					if *bifrostReq.Params.Reasoning.Summary == "none" {
						anthropicReq.Thinking.Display = schemas.Ptr("omitted")
					} else {
						anthropicReq.Thinking.Display = schemas.Ptr("summarized")
					}
				} else if IsAdaptiveOnlyThinkingModel(capModel) {
					anthropicReq.Thinking.Display = schemas.Ptr("summarized")
				}
			}
		}
		// Convert service tier
		if bifrostReq.Params.ServiceTier != nil {
			mapped := MapBifrostServiceTierToAnthropicRequest(*bifrostReq.Params.ServiceTier)
			anthropicReq.ServiceTier = &mapped
		}

		if bifrostReq.Params.ExtraParams != nil {
			anthropicReq.ExtraParams = make(map[string]interface{}, len(bifrostReq.Params.ExtraParams))
			for k, v := range bifrostReq.Params.ExtraParams {
				anthropicReq.ExtraParams[k] = v
			}
			if cacheControlRaw, exists := anthropicReq.ExtraParams["cache_control"]; exists {
				parsed := false
				switch v := cacheControlRaw.(type) {
				case *schemas.CacheControl:
					anthropicReq.CacheControl = v
					parsed = true
				case schemas.CacheControl:
					anthropicReq.CacheControl = &v
					parsed = true
				default:
					if data, err := providerUtils.MarshalSorted(v); err == nil {
						var cc schemas.CacheControl
						if sonic.Unmarshal(data, &cc) == nil {
							anthropicReq.CacheControl = &cc
							parsed = true
						}
					}
				}
				if parsed {
					delete(anthropicReq.ExtraParams, "cache_control")
				}
			}
			if diagnosticsRaw, exists := anthropicReq.ExtraParams["diagnostics"]; exists {
				parsed := false
				switch v := diagnosticsRaw.(type) {
				case *AnthropicDiagnostics:
					anthropicReq.Diagnostics = v
					parsed = true
				case AnthropicDiagnostics:
					anthropicReq.Diagnostics = &v
					parsed = true
				default:
					if data, err := providerUtils.MarshalSorted(v); err == nil {
						var d AnthropicDiagnostics
						if sonic.Unmarshal(data, &d) == nil {
							anthropicReq.Diagnostics = &d
							parsed = true
						}
					}
				}
				if parsed {
					delete(anthropicReq.ExtraParams, "diagnostics")
				}
			}
			topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"])
			if ok {
				delete(anthropicReq.ExtraParams, "top_k")
				if !IsAdaptiveOnlyThinkingModel(capModel) {
					anthropicReq.TopK = topK
				}
			}
			if speed, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["speed"]); ok {
				delete(anthropicReq.ExtraParams, "speed")
				if SupportsFastMode(capModel) {
					anthropicReq.Speed = speed
				}
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
				} else if data, err := providerUtils.MarshalSorted(cmVal); err == nil {
					var cm ContextManagement
					if sonic.Unmarshal(data, &cm) == nil {
						delete(anthropicReq.ExtraParams, "context_management")
						anthropicReq.ContextManagement = &cm
					}
				}
			}
			if tbVal, exists := bifrostReq.Params.ExtraParams["task_budget"]; exists {
				// Always consume provider-specific key from passthrough extras.
				delete(anthropicReq.ExtraParams, "task_budget")
				var taskBudget *AnthropicTaskBudget
				switch v := tbVal.(type) {
				case *AnthropicTaskBudget:
					taskBudget = v
				case AnthropicTaskBudget:
					taskBudget = &v
				default:
					if data, err := providerUtils.MarshalSorted(v); err == nil {
						var tb AnthropicTaskBudget
						if sonic.Unmarshal(data, &tb) == nil {
							taskBudget = &tb
						}
					}
				}
				if taskBudget == nil {
					return nil, fmt.Errorf("invalid task_budget format for anthropic")
				}
				if anthropicReq.OutputConfig == nil {
					anthropicReq.OutputConfig = &AnthropicOutputConfig{}
				}
				anthropicReq.OutputConfig.TaskBudget = taskBudget
			}
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			anthropicTools, mcpServers := convertBifrostToolsToAnthropic(capModel, bifrostReq.Params.Tools, bifrostReq.Provider)
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

		// DeepSeek rejects a forced tool_choice while thinking is on. Force thinking
		// off when tool_choice pins a specific tool.
		if bifrostReq.Provider == schemas.DeepSeek && anthropicReq.ToolChoice != nil &&
			anthropicReq.ToolChoice.Type == "tool" {
			anthropicReq.Thinking = &AnthropicThinking{Type: "disabled"}
		}
	}

	if bifrostReq.Input != nil {
		anthropicMessages, systemContent := ConvertBifrostMessagesToAnthropicMessages(ctx, bifrostReq.Input, true, bifrostReq.Provider, capModel)

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

	// Handle cache read tokens
	if anthropicUsage.CacheReadInputTokens > 0 {
		if bifrostUsage.InputTokensDetails == nil {
			bifrostUsage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		bifrostUsage.InputTokensDetails.CachedReadTokens = anthropicUsage.CacheReadInputTokens
		bifrostUsage.InputTokens = bifrostUsage.InputTokens + anthropicUsage.CacheReadInputTokens
		bifrostUsage.TotalTokens = bifrostUsage.TotalTokens + anthropicUsage.CacheReadInputTokens
	}

	// Handle cache creation tokens
	if anthropicUsage.CacheCreationInputTokens > 0 {
		if bifrostUsage.InputTokensDetails == nil {
			bifrostUsage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		bifrostUsage.InputTokensDetails.CachedWriteTokens = anthropicUsage.CacheCreationInputTokens
		if anthropicUsage.CacheCreation.Ephemeral5mInputTokens > 0 || anthropicUsage.CacheCreation.Ephemeral1hInputTokens > 0 {
			bifrostUsage.InputTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: anthropicUsage.CacheCreation.Ephemeral5mInputTokens,
				CachedWriteTokens1h: anthropicUsage.CacheCreation.Ephemeral1hInputTokens,
			}
		}
		bifrostUsage.InputTokens = bifrostUsage.InputTokens + anthropicUsage.CacheCreationInputTokens
		bifrostUsage.TotalTokens = bifrostUsage.TotalTokens + anthropicUsage.CacheCreationInputTokens
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

	// Handle cache read tokens
	if bifrostUsage.InputTokensDetails != nil {
		if bifrostUsage.InputTokensDetails.CachedReadTokens > 0 {
			anthropicUsage.CacheReadInputTokens = bifrostUsage.InputTokensDetails.CachedReadTokens
			anthropicUsage.InputTokens = anthropicUsage.InputTokens - bifrostUsage.InputTokensDetails.CachedReadTokens
		}
		if bifrostUsage.InputTokensDetails.CachedWriteTokens > 0 {
			anthropicUsage.CacheCreationInputTokens = bifrostUsage.InputTokensDetails.CachedWriteTokens
			anthropicUsage.InputTokens = anthropicUsage.InputTokens - bifrostUsage.InputTokensDetails.CachedWriteTokens
			if bifrostUsage.InputTokensDetails.CachedWriteTokenDetails != nil {
				anthropicUsage.CacheCreation = AnthropicUsageCacheCreation{
					Ephemeral5mInputTokens: bifrostUsage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m,
					Ephemeral1hInputTokens: bifrostUsage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h,
				}
			}
		}
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
			// Lift the response-level code-execution container onto every
			// code_interpreter_call so it round-trips (id is neutral; expiry is carried).
			if response.Container != nil {
				for i := range outputMessages {
					m := &outputMessages[i]
					if m.Type == nil || *m.Type != schemas.ResponsesMessageTypeCodeInterpreterCall || m.ResponsesToolMessage == nil {
						continue
					}
					if m.ResponsesToolMessage.ResponsesCodeInterpreterToolCall == nil {
						m.ResponsesToolMessage.ResponsesCodeInterpreterToolCall = &schemas.ResponsesCodeInterpreterToolCall{}
					}
					m.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.ContainerID = response.Container.ID
					if response.Container.ExpiresAt != nil {
						if m.ResponsesToolMessage.ResponsesCodeExecutionCall == nil {
							m.ResponsesToolMessage.ResponsesCodeExecutionCall = &schemas.ResponsesCodeExecutionCall{}
						}
						m.ResponsesToolMessage.ResponsesCodeExecutionCall.ContainerExpiresAt = response.Container.ExpiresAt
					}
				}
			}
			bifrostResp.Output = outputMessages
		}
	}

	bifrostResp.Model = response.Model

	if response.StopReason != "" {
		mapped := ConvertAnthropicFinishReasonToBifrost(response.StopReason)
		if mapped == string(schemas.BifrostFinishReasonToolCalls) {
			if soToolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok && soToolName != "" {
				hasRealToolUse := false
				for _, block := range response.Content {
					if block.Type == AnthropicContentBlockTypeServerToolUse ||
						block.Type == AnthropicContentBlockTypeMCPToolUse ||
						(block.Type == AnthropicContentBlockTypeToolUse && block.Name != nil && *block.Name != soToolName) {
						hasRealToolUse = true
						break
					}
				}
				if !hasRealToolUse {
					mapped = string(schemas.BifrostFinishReasonStop)
				}
			}
		}
		bifrostResp.StopReason = &mapped
	}

	if response.Usage != nil && response.Usage.ServiceTier != nil {
		mapped := MapAnthropicServiceTierToBifrost(*response.Usage.ServiceTier)
		bifrostResp.ServiceTier = &mapped
	}

	// Forward the speed actually served (fast mode) — drives fast-mode billing.
	if response.Usage != nil && response.Usage.Speed != nil {
		bifrostResp.Speed = response.Usage.Speed
	}

	// Forward the inference geography served — drives the data-residency multiplier.
	if response.Usage != nil && response.Usage.InferenceGeo != nil {
		bifrostResp.InferenceGeo = response.Usage.InferenceGeo
	}

	// Forward cache diagnostics (cache-diagnosis-2026-04-07) to the client.
	if response.Diagnostics != nil {
		bifrostResp.Diagnostics = response.Diagnostics
	}

	return bifrostResp
}

func bifrostResponsesOutputHasFunctionCall(output []schemas.ResponsesMessage) bool {
	for _, msg := range output {
		// Reasoning-only items can carry the function_call type but convert to
		// thinking blocks, not tool_use — they must not force a tool_use stop reason.
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall &&
			msg.ResponsesReasoning == nil && msg.ResponsesToolMessage != nil {
			return true
		}
	}
	return false
}

func anthropicContentBlocksHaveToolUse(contentBlocks []AnthropicContentBlock) bool {
	for _, block := range contentBlocks {
		if block.Type == AnthropicContentBlockTypeToolUse {
			return true
		}
	}
	return false
}

func inferAnthropicStopReasonFromBifrostResponse(bifrostResp *schemas.BifrostResponsesResponse, contentBlocks []AnthropicContentBlock) AnthropicStopReason {
	if bifrostResp != nil {
		if bifrostResp.StopReason != nil {
			return ConvertBifrostFinishReasonToAnthropic(*bifrostResp.StopReason)
		}
		if bifrostResponsesOutputHasFunctionCall(bifrostResp.Output) {
			return AnthropicStopReasonToolUse
		}
	}
	if anthropicContentBlocksHaveToolUse(contentBlocks) {
		return AnthropicStopReasonToolUse
	}
	return AnthropicStopReasonEndTurn
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
		anthropicMessages, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, bifrostResp.Output, false, "", "")
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

	// Restore the response-level code-execution container from the first
	// code_interpreter_call that carries one.
	for i := range bifrostResp.Output {
		m := &bifrostResp.Output[i]
		if m.Type == nil || *m.Type != schemas.ResponsesMessageTypeCodeInterpreterCall || m.ResponsesToolMessage == nil {
			continue
		}
		ci := m.ResponsesToolMessage.ResponsesCodeInterpreterToolCall
		if ci == nil || ci.ContainerID == "" {
			continue
		}
		container := &AnthropicResponseContainer{ID: ci.ContainerID}
		if cec := m.ResponsesToolMessage.ResponsesCodeExecutionCall; cec != nil {
			container.ExpiresAt = cec.ContainerExpiresAt
		}
		anthropicResp.Container = container
		break
	}

	anthropicResp.StopReason = inferAnthropicStopReasonFromBifrostResponse(bifrostResp, contentBlocks)

	anthropicResp.Model = bifrostResp.Model

	if bifrostResp.ServiceTier != nil {
		if anthropicResp.Usage == nil {
			anthropicResp.Usage = &AnthropicUsage{}
		}
		mapped := MapBifrostServiceTierToAnthropicResponse(*bifrostResp.ServiceTier)
		anthropicResp.Usage.ServiceTier = &mapped
	}

	if bifrostResp.Speed != nil {
		if anthropicResp.Usage == nil {
			anthropicResp.Usage = &AnthropicUsage{}
		}
		anthropicResp.Usage.Speed = bifrostResp.Speed
	}

	if bifrostResp.Diagnostics != nil {
		anthropicResp.Diagnostics = bifrostResp.Diagnostics
	}

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
// This is the main conversion method from Bifrost to Anthropic - handles all message types and returns messages + system content.
// provider and model are used to gate mid-conversation system message support (Anthropic + Opus 4.8+ only).
func ConvertBifrostMessagesToAnthropicMessages(ctx *schemas.BifrostContext, bifrostMessages []schemas.ResponsesMessage, isRequestMessage bool, provider schemas.ModelProvider, model string) ([]AnthropicMessage, *AnthropicContent) {
	// If only a single system message is present, convert it user message (since openai allows it)
	if len(bifrostMessages) == 1 && bifrostMessages[0].Role != nil && (*bifrostMessages[0].Role == schemas.ResponsesInputMessageRoleSystem || *bifrostMessages[0].Role == schemas.ResponsesInputMessageRoleDeveloper) {
		if systemContent := convertBifrostMessageToAnthropicSystemContent(&bifrostMessages[0]); systemContent != nil {
			return []AnthropicMessage{{
				Role:    AnthropicMessageRoleUser,
				Content: *systemContent,
			}}, nil
		}
	}

	// seenConversation tracks whether any user/assistant message has been appended.
	// A system message encountered after the conversation starts is emitted as
	// role:"system" in the messages array when the provider+model supports it.
	seenConversation := false
	midConvSystemSupported := isRequestMessage && SupportsMidConversationSystem(provider, model)

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

	// knownToolUseIDs is the set of tool_use ids that are actually committed to the
	// output as tool_use/server_tool_use blocks. A tool_result whose id is not in
	// this set is "orphaned" (e.g. a function_call_output sent via the OpenAI
	// previous_response_id pattern without its originating function_call) and must
	// NOT be emitted as a tool_result, or Anthropic rejects the whole request.
	knownToolUseIDs := make(map[string]bool)

	// Helper to emit orphaned tool results (no matching tool_use) as a single user
	// text message so their content is preserved without violating Anthropic's
	// requirement that every tool_result have a corresponding tool_use.
	appendOrphanToolResultsAsUserText := func(blocks []AnthropicContentBlock) {
		if len(blocks) == 0 {
			return
		}
		var textBlocks []AnthropicContentBlock
		for i := range blocks {
			text := toolResultBlockToText(&blocks[i])
			if text == "" {
				continue
			}
			textBlocks = append(textBlocks, AnthropicContentBlock{
				Type: AnthropicContentBlockTypeText,
				Text: schemas.Ptr(text),
			})
		}
		if len(textBlocks) > 0 {
			anthropicMessages = append(anthropicMessages, AnthropicMessage{
				Role:    AnthropicMessageRoleUser,
				Content: AnthropicContent{ContentBlocks: textBlocks},
			})
		}
	}

	// Helper to flush pending tool result blocks into user messages.
	// Results are partitioned into those with a real matching tool_use (emitted as
	// tool_result, grouped by their originating assistant turn) and orphans, which
	// are converted to user text. Anthropic rejects a tool_result block that has no
	// corresponding tool_use block in the previous message:
	// https://platform.claude.com/docs/en/agents-and-tools/tool-use/how-tool-use-works
	flushPendingToolResults := func() {
		if len(pendingToolResultBlocks) == 0 {
			return
		}

		var matched []AnthropicContentBlock
		var orphaned []AnthropicContentBlock
		for _, block := range pendingToolResultBlocks {
			if block.ToolUseID != nil && knownToolUseIDs[*block.ToolUseID] {
				matched = append(matched, block)
			} else {
				orphaned = append(orphaned, block)
			}
		}
		pendingToolResultBlocks = nil

		// Group matched tool results by their corresponding (not-yet-flushed) tool
		// call group so each assistant turn's results land in their own user message.
		remaining := matched
		for i := range toolCallGroups {
			if toolCallGroups[i].flushed {
				continue
			}

			var groupResults []AnthropicContentBlock
			var rest []AnthropicContentBlock
			for _, block := range remaining {
				if block.ToolUseID != nil && toolCallGroups[i].toolCallIDs[*block.ToolUseID] {
					groupResults = append(groupResults, block)
				} else {
					rest = append(rest, block)
				}
			}

			if len(groupResults) > 0 {
				anthropicMessages = append(anthropicMessages, AnthropicMessage{
					Role:    AnthropicMessageRoleUser,
					Content: AnthropicContent{ContentBlocks: groupResults},
				})
				toolCallGroups[i].flushed = true
				remaining = rest
			}
		}

		// Matched results whose group was already flushed in an earlier call still
		// have a valid tool_use, so emit them as a standalone tool_result message.
		if len(remaining) > 0 {
			anthropicMessages = append(anthropicMessages, AnthropicMessage{
				Role:    AnthropicMessageRoleUser,
				Content: AnthropicContent{ContentBlocks: remaining},
			})
		}

		// Orphans (no matching tool_use anywhere) become user text.
		appendOrphanToolResultsAsUserText(orphaned)
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
				// These ids are now committed as tool_use blocks in the output, so
				// any later tool_result referencing them is legitimate.
				for id := range currentToolCallIDs {
					knownToolUseIDs[id] = true
				}
				currentToolCallIDs = nil
			}

			pendingToolCalls = nil
			currentAssistantMessage = nil
		}
	}

	trimIndex := len(bifrostMessages)
	if isRequestMessage && ctx.Value(schemas.BifrostContextKeySupportsAssistantPrefill) == false {
		for trimIndex > 0 {
			m := bifrostMessages[trimIndex-1]
			if m.Role != nil && *m.Role == schemas.ResponsesInputMessageRoleAssistant {
				trimIndex--
			} else {
				break
			}
		}
	}

	for i, msg := range bifrostMessages {
		// Handle nil Type as regular message
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}

		// Skip trailing assistant messages.
		if isRequestMessage && i >= trimIndex {
			continue
		}

		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Flush any pending tool results before processing other message types
			flushPendingToolResults()

			// Flush any pending tool calls first (with tracking for tool call groups)
			flushPendingToolCallsWithTracking()

			// Handle system messages
			if msg.Role != nil && (*msg.Role == schemas.ResponsesInputMessageRoleSystem || *msg.Role == schemas.ResponsesInputMessageRoleDeveloper) {
				// Flush any pending reasoning blocks into an assistant message first so
				// they are not reordered or lost when a system message interrupts the
				// reasoning → system → user sequence.
				if len(pendingReasoningContentBlocks) > 0 {
					copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
					copy(copied, pendingReasoningContentBlocks)
					anthropicMessages = append(anthropicMessages, AnthropicMessage{
						Role:    AnthropicMessageRoleAssistant,
						Content: AnthropicContent{ContentBlocks: copied},
					})
					pendingReasoningContentBlocks = nil
				}
				if !seenConversation && (len(anthropicMessages) > 0 ||
					len(pendingToolCalls) > 0 ||
					len(pendingToolResultBlocks) > 0 ||
					currentAssistantMessage != nil) {
					seenConversation = true
				}
				if content := convertBifrostMessageToAnthropicSystemContent(&msg); content != nil {
					if seenConversation && midConvSystemSupported {
						// Mid-conversation system message — emit as role:"system" in messages array.
						anthropicMessages = append(anthropicMessages, AnthropicMessage{
							Role:    AnthropicMessageRoleSystem,
							Content: *content,
						})
					} else {
						systemContent = appendToSystemContent(systemContent, *content)
					}
				}
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
				// Register any tool_use ids carried on a regular assistant message so
				// later tool_result blocks referencing them are not treated as orphans.
				for _, b := range anthropicMsg.Content.ContentBlocks {
					if (b.Type == AnthropicContentBlockTypeToolUse || b.Type == AnthropicContentBlockTypeServerToolUse) && b.ID != nil {
						knownToolUseIDs[*b.ID] = true
					}
				}
				seenConversation = true
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

		case schemas.ResponsesMessageTypeAdvisorCall:
			// Advisor calls, like web search, emit a server_tool_use + result
			// pair that lives inside the assistant message.
			flushPendingToolResults()
			advisorBlocks := convertBifrostAdvisorCallToAnthropicBlocks(&msg)
			if len(advisorBlocks) > 0 {
				if currentAssistantMessage == nil {
					currentAssistantMessage = &AnthropicMessage{
						Role: AnthropicMessageRoleAssistant,
					}
				}
				if len(pendingReasoningContentBlocks) > 0 {
					copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
					copy(copied, pendingReasoningContentBlocks)
					pendingToolCalls = append(copied, pendingToolCalls...)
					pendingReasoningContentBlocks = nil
				}
				pendingToolCalls = append(pendingToolCalls, advisorBlocks...)
				if advisorBlocks[0].ID != nil {
					if currentToolCallIDs == nil {
						currentToolCallIDs = make(map[string]bool)
					}
					currentToolCallIDs[*advisorBlocks[0].ID] = true
				}
			}

		case schemas.ResponsesMessageTypeToolSearchCall:
			// tool_search calls, like web search/advisor, emit a server_tool_use +
			// result pair (carrying the discovered tool_references) that lives inside
			// the assistant message, so a follow-up turn keeps the search context.
			flushPendingToolResults()
			toolSearchBlocks := convertBifrostToolSearchCallToAnthropicBlocks(&msg)
			if len(toolSearchBlocks) > 0 {
				if currentAssistantMessage == nil {
					currentAssistantMessage = &AnthropicMessage{
						Role: AnthropicMessageRoleAssistant,
					}
				}
				if len(pendingReasoningContentBlocks) > 0 {
					copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
					copy(copied, pendingReasoningContentBlocks)
					pendingToolCalls = append(copied, pendingToolCalls...)
					pendingReasoningContentBlocks = nil
				}
				pendingToolCalls = append(pendingToolCalls, toolSearchBlocks...)
				if toolSearchBlocks[0].ID != nil {
					if currentToolCallIDs == nil {
						currentToolCallIDs = make(map[string]bool)
					}
					currentToolCallIDs[*toolSearchBlocks[0].ID] = true
				}
			}

		case schemas.ResponsesMessageTypeCodeInterpreterCall:
			// Code execution calls, like web search/advisor, emit a server_tool_use +
			// *_code_execution_tool_result pair inside the assistant message.
			flushPendingToolResults()
			codeExecBlocks := convertBifrostCodeExecCallToAnthropicBlocks(&msg)
			if len(codeExecBlocks) > 0 {
				if currentAssistantMessage == nil {
					currentAssistantMessage = &AnthropicMessage{
						Role: AnthropicMessageRoleAssistant,
					}
				}
				if len(pendingReasoningContentBlocks) > 0 {
					copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
					copy(copied, pendingReasoningContentBlocks)
					pendingToolCalls = append(copied, pendingToolCalls...)
					pendingReasoningContentBlocks = nil
				}
				pendingToolCalls = append(pendingToolCalls, codeExecBlocks...)
				if codeExecBlocks[0].ID != nil {
					if currentToolCallIDs == nil {
						currentToolCallIDs = make(map[string]bool)
					}
					currentToolCallIDs[*codeExecBlocks[0].ID] = true
				}
			}

		case schemas.ResponsesMessageTypeWebFetchCall:
			flushPendingToolResults()
			webFetchBlocks := convertBifrostWebFetchCallToAnthropicBlocks(&msg)
			if len(webFetchBlocks) > 0 {
				if currentAssistantMessage == nil {
					currentAssistantMessage = &AnthropicMessage{
						Role: AnthropicMessageRoleAssistant,
					}
				}
				if len(pendingReasoningContentBlocks) > 0 {
					copied := make([]AnthropicContentBlock, len(pendingReasoningContentBlocks))
					copy(copied, pendingReasoningContentBlocks)
					pendingToolCalls = append(copied, pendingToolCalls...)
					pendingReasoningContentBlocks = nil
				}
				pendingToolCalls = append(pendingToolCalls, webFetchBlocks...)
				if webFetchBlocks[0].ID != nil {
					if currentToolCallIDs == nil {
						currentToolCallIDs = make(map[string]bool)
					}
					currentToolCallIDs[*webFetchBlocks[0].ID] = true
				}
			}

		// Handle other tool call types that are not natively supported by Anthropic
		case schemas.ResponsesMessageTypeFileSearchCall,
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

	// Trim trailing whitespace from the last assistant message
	// ContentStr is converted to a single text ContentBlock during message conversion
	// so we trim the text of that block instead.
	lastMsgIndex := len(anthropicMessages) - 1
	if isRequestMessage && lastMsgIndex >= 0 && anthropicMessages[lastMsgIndex].Role == AnthropicMessageRoleAssistant {
		blocks := anthropicMessages[lastMsgIndex].Content.ContentBlocks
		for j := len(blocks) - 1; j >= 0; j-- {
			if blocks[j].Type == AnthropicContentBlockTypeText && blocks[j].Text != nil {
				anthropicMessages[lastMsgIndex].Content.ContentBlocks[j].Text = schemas.Ptr(strings.TrimRight(*blocks[j].Text, " \n\r\t"))
				break
			}
		}
	}

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
			if block.Source != nil && block.Source.SourceObj != nil {
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
			if block.Source != nil && block.Source.SourceObj != nil {
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

		case AnthropicContentBlockTypeContainerUpload:
			if block.FileID != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{block.toBifrostResponsesContainerUploadBlock()},
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
						Type:         schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Status:       schemas.Ptr("completed"),
						CacheControl: block.CacheControl,
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
								if contentBlock.Source != nil && contentBlock.Source.SourceObj != nil {
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
				Type:         schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status:       schemas.Ptr("completed"),
				CacheControl: toolBlock.CacheControl,
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
				var inputMap map[string]interface{}
				if err := sonic.Unmarshal(toolBlock.Input, &inputMap); err == nil {
					bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
						ResponsesComputerToolCallAction: convertAnthropicToResponsesComputerAction(inputMap),
					}
				}
			} else if toolBlock.Name != nil && *toolBlock.Name == string(AnthropicToolNameWebSearch) {
				bifrostMsg.Type = schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall)
				bifrostMsg.ResponsesToolMessage.Name = nil
				if q := providerUtils.GetJSONField(toolBlock.Input, "query"); q.Exists() && q.Type == gjson.String {
					query := q.Str
					bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
							Type:    "search",
							Query:   schemas.Ptr(query),
							Queries: []string{query},
						},
					}
				}
			} else if toolBlock.Name != nil && *toolBlock.Name == string(AnthropicToolNameWebFetch) {
				bifrostMsg.Type = schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall)
				bifrostMsg.ResponsesToolMessage.Name = nil
				if u := providerUtils.GetJSONField(toolBlock.Input, "url"); u.Exists() && u.Type == gjson.String {
					bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{
							URL: u.Str,
						},
					}
				}
			} else {
				if len(toolBlock.Input) > 0 {
					bifrostMsg.ResponsesToolMessage.Arguments = schemas.Ptr(string(toolBlock.Input))
				}
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
			if block.Source != nil && block.Source.SourceObj != nil {
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
			if block.Source != nil && block.Source.SourceObj != nil {
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
		case AnthropicContentBlockTypeContainerUpload:
			if block.FileID != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{block.toBifrostResponsesContainerUploadBlock()},
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
					jsonStr = string(block.Input)
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
						Type:         schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status:       schemas.Ptr("completed"),
						CacheControl: block.CacheControl,
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
						var inputMap map[string]interface{}
						if err := sonic.Unmarshal(block.Input, &inputMap); err == nil {
							bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
								ResponsesComputerToolCallAction: convertAnthropicToResponsesComputerAction(inputMap),
							}
						}
					} else if len(block.Input) > 0 {
						bifrostMsg.ResponsesToolMessage.Arguments = schemas.Ptr(string(block.Input))
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			}
		case AnthropicContentBlockTypeToolResult:
			// Convert tool result to function call output message
			if block.ToolUseID != nil {
				if block.Content != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type:         schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						Status:       schemas.Ptr("completed"),
						CacheControl: block.CacheControl,
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
								if contentBlock.Source != nil && contentBlock.Source.SourceObj != nil {
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

				// Preserve the caller (set when this search was spawned from inside
				// the code execution sandbox — programmatic tool calling).
				if block.Caller != nil {
					bifrostMsg.ResponsesToolMessage.Caller = &schemas.ResponsesToolCaller{
						Type:   string(block.Caller.Type),
						ToolID: block.Caller.ToolID,
					}
				}

				// Extract query from input
				if block.Input != nil {
					if q := providerUtils.GetJSONField(block.Input, "query"); q.Exists() && q.Type == gjson.String {
						query := q.Str
						bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
								Type:    "search",
								Query:   schemas.Ptr(query),
								Queries: []string{query}, // Anthropic uses single query
							},
						}
					}
				}

				if isOutputMessage {
					bifrostMsg.ID = block.ID
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			} else if block.Name != nil && *block.Name == string(AnthropicToolNameWebFetch) {
				bifrostMsg := schemas.ResponsesMessage{
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
					Status:               schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: block.ID},
				}

				if block.Caller != nil {
					bifrostMsg.ResponsesToolMessage.Caller = &schemas.ResponsesToolCaller{
						Type:   string(block.Caller.Type),
						ToolID: block.Caller.ToolID,
					}
				}

				if block.Input != nil {
					if u := providerUtils.GetJSONField(block.Input, "url"); u.Exists() && u.Type == gjson.String {
						bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{
								URL: u.Str,
							},
						}
					}
				}

				if isOutputMessage {
					bifrostMsg.ID = block.ID
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			} else if block.Name != nil && *block.Name == string(AnthropicToolNameAdvisor) {
				// advisor server_tool_use — the paired advisor_tool_result is
				// attached onto this message when it is encountered below.
				bifrostMsg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeAdvisorCall),
					ID:     block.ID,
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Name:   schemas.Ptr(string(AnthropicToolNameAdvisor)),
						CallID: block.ID,
					},
				}
				if isOutputMessage {
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			} else if block.Name != nil && isAnthropicCodeExecutionToolName(*block.Name) {
				// code_execution / bash_code_execution / text_editor_code_execution
				// server_tool_use — the paired *_tool_result is attached below.
				if isOutputMessage {
					bifrostMessages = append(bifrostMessages, buildBifrostCodeExecutionCall(block))
				}
			}

		case AnthropicContentBlockTypeCodeExecutionToolResult,
			AnthropicContentBlockTypeBashCodeExecutionToolResult,
			AnthropicContentBlockTypeTextEditorCodeExecutionToolResult:
			// Fold the code-execution result onto the matching code_interpreter_call.
			if block.ToolUseID != nil {
				attachAnthropicCodeExecutionResult(bifrostMessages, *block.ToolUseID, block)
			}

		case AnthropicContentBlockTypeAdvisorToolResult:
			// Attach the advisor result onto the matching advisor_call.
			if block.ToolUseID != nil {
				for i := len(bifrostMessages) - 1; i >= 0; i-- {
					msg := &bifrostMessages[i]
					if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeAdvisorCall {
						continue
					}
					if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.CallID == nil ||
						*msg.ResponsesToolMessage.CallID != *block.ToolUseID {
						continue
					}
					advisor := &schemas.ResponsesAdvisorCall{}
					// content is a single object on the wire (ContentObj); UnmarshalJSON
					// wraps incoming objects into ContentBlocks, so accept either.
					if c := block.Content; c != nil {
						inner := c.ContentObj
						if inner == nil && len(c.ContentBlocks) > 0 {
							inner = &c.ContentBlocks[0]
						}
						if inner != nil {
							advisor.ResultType = string(inner.Type)
							advisor.Text = inner.Text
							advisor.EncryptedContent = inner.EncryptedContent
							advisor.ErrorCode = inner.ErrorCode
							advisor.StopReason = inner.StopReason
						}
					}
					msg.ResponsesToolMessage.ResponsesAdvisorCall = advisor
					break
				}
			}

		case AnthropicContentBlockTypeWebSearchToolResult:
			// Find the corresponding web_search_call by tool_use_id
			if block.ToolUseID != nil {
				attachWebSearchSourcesToCall(bifrostMessages, *block.ToolUseID, block, true)
			}

		case AnthropicContentBlockTypeWebFetchToolResult:
			if block.ToolUseID != nil {
				attachAnthropicWebFetchResult(bifrostMessages, *block.ToolUseID, block)
			}

		case AnthropicContentBlockTypeWebSearchToolResultError:
			// Handle web search errors — find matching web_search_call and mark as failed
			if block.ToolUseID != nil {
				for i := len(bifrostMessages) - 1; i >= 0; i-- {
					msg := &bifrostMessages[i]
					if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeWebSearchCall &&
						msg.ID != nil && *msg.ID == *block.ToolUseID {
						msg.Status = schemas.Ptr("failed")
						break
					}
				}
			}

		case AnthropicContentBlockTypeMCPToolUse:
			// Convert MCP tool use to MCP call (assistant's tool call)
			if block.ID != nil && block.Name != nil {
				bifrostMsg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
					ID:   block.ID,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Name: block.Name,
					},
				}
				if len(block.Input) > 0 {
					bifrostMsg.ResponsesToolMessage.Arguments = schemas.Ptr(string(block.Input))
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
					ContentBlocks: []AnthropicContentBlock{{
						Type: AnthropicContentBlockTypeText,
						Text: msg.Content.ContentStr,
					}},
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
				// signature is required by the Agent SDK; converted (non-Anthropic) reasoning
				// has none, so default to empty rather than omitting the field.
				signature := block.Signature
				if signature == nil {
					signature = schemas.Ptr("")
				}
				thinkingBlock := AnthropicContentBlock{
					Type:      AnthropicContentBlockTypeThinking,
					Thinking:  block.Text,
					Signature: signature,
				}
				thinkingBlocks = append(thinkingBlocks, thinkingBlock)
			}
		}
	} else if msg.ResponsesReasoning != nil {
		// Redacted-only reasoning items carry an EMPTY (non-nil) summary list next
		// to encrypted_content, in both the streaming and non-streaming converters,
		// so gate on the list having entries rather than on nil: a nil-check sends
		// such items through the summary loop, emits nothing, and drops the
		// encrypted payload from the replayed request.
		if len(msg.ResponsesReasoning.Summary) > 0 {
			for _, reasoningContent := range msg.ResponsesReasoning.Summary {
				thinkingBlock := AnthropicContentBlock{
					Type:      AnthropicContentBlockTypeThinking,
					Thinking:  &reasoningContent.Text,
					Signature: schemas.Ptr(""), // required by the Agent SDK; converted reasoning has no signature
				}
				thinkingBlocks = append(thinkingBlocks, thinkingBlock)
			}
		} else if msg.ResponsesReasoning.EncryptedContent != nil && *msg.ResponsesReasoning.EncryptedContent != "" {
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
			Type:         AnthropicContentBlockTypeToolUse,
			CacheControl: msg.CacheControl,
		}

		if msg.ResponsesToolMessage.CallID != nil {
			toolUseBlock.ID = providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ResponsesToolMessage.CallID)
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
		} else {
			// Anthropic requires input to always be present on tool_use blocks;
			// default to an empty object for tools that take no arguments.
			toolUseBlock.Input = json.RawMessage("{}")
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
			Type:         AnthropicContentBlockTypeToolResult,
			ToolUseID:    providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ResponsesToolMessage.CallID),
			CacheControl: msg.CacheControl,
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

// toolResultBlockToText renders a tool_result content block as plain text. It is
// used to salvage an orphaned tool_result (one whose tool_use_id has no matching
// tool_use block in the converted output) by converting it into a user text
// message instead of an invalid tool_result block, which Anthropic rejects:
// https://platform.claude.com/docs/en/agents-and-tools/tool-use/how-tool-use-works
// Returns "" when the block carries no renderable text.
func toolResultBlockToText(block *AnthropicContentBlock) string {
	if block == nil || block.Content == nil {
		return ""
	}
	if block.Content.ContentStr != nil && *block.Content.ContentStr != "" {
		return "Tool result: " + *block.Content.ContentStr
	}
	var sb strings.Builder
	for _, cb := range block.Content.ContentBlocks {
		if cb.Text != nil {
			sb.WriteString(*cb.Text)
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	return "Tool result: " + sb.String()
}

// convertBifrostComputerCallOutputToAnthropicToolResultBlock converts a Bifrost computer call output to a single tool result block
// This is used to accumulate multiple tool results into a single user message
func convertBifrostComputerCallOutputToAnthropicToolResultBlock(msg *schemas.ResponsesMessage) *AnthropicContentBlock {
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
		toolResultBlock := AnthropicContentBlock{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ResponsesToolMessage.CallID),
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
			ToolUseID: providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ResponsesToolMessage.CallID),
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
			ContentBlocks: []AnthropicContentBlock{{
				Type: AnthropicContentBlockTypeText,
				Text: msg.Content.ContentStr,
			}},
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
			toolUseBlock.ID = providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ResponsesToolMessage.CallID)
		}
		if msg.ResponsesToolMessage.Name != nil {
			toolUseBlock.Name = msg.ResponsesToolMessage.Name
		}

		if msg.ResponsesToolMessage.Action != nil && msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
			inputMap := convertResponsesToAnthropicComputerAction(msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction)
			if inputBytes, err := providerUtils.MarshalSorted(inputMap); err == nil {
				toolUseBlock.Input = json.RawMessage(inputBytes)
			}
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
			toolUseBlock.ID = providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ID)
		}
		toolUseBlock.Name = msg.ResponsesToolMessage.Name

		// Set server name if present
		if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
			toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
		}

		// Parse arguments as JSON input
		if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
			toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
		} else {
			// Anthropic requires input to always be present on tool_use blocks;
			// default to an empty object for tools that take no arguments.
			toolUseBlock.Input = json.RawMessage("{}")
		}

		return &toolUseBlock
	}
	return nil
}

// convertBifrostMCPCallOutputToAnthropicMessage converts a Bifrost MCP call output to Anthropic message
func convertBifrostMCPCallOutputToAnthropicMessage(msg *schemas.ResponsesMessage) *AnthropicMessage {
	toolResultBlock := AnthropicContentBlock{
		Type: AnthropicContentBlockTypeMCPToolResult,
		ID:   providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ResponsesToolMessage.CallID),
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
			toolUseBlock.ID = providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ID)
		}
		toolUseBlock.Name = msg.ResponsesToolMessage.Name

		// Set server name if present
		if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
			toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
		}

		// Parse arguments as JSON input
		if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
			toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
		} else {
			// Anthropic requires input to always be present on tool_use blocks;
			// default to an empty object for tools that take no arguments.
			toolUseBlock.Input = json.RawMessage("{}")
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

	// The caller (set when this search was spawned from inside the code execution
	// sandbox) must be re-emitted on both the server_tool_use and the result block.
	var caller *AnthropicToolCaller
	if c := msg.ResponsesToolMessage.Caller; c != nil {
		caller = &AnthropicToolCaller{Type: AnthropicToolCallerType(c.Type), ToolID: providerUtils.SanitizeAnthropicToolUseIDPtr(c.ToolID)}
	}

	// 1. Create server_tool_use block for the web search
	serverToolUseBlock := AnthropicContentBlock{
		Type:   AnthropicContentBlockTypeServerToolUse,
		Name:   schemas.Ptr("web_search"),
		Caller: caller,
	}

	if msg.ID != nil {
		serverToolUseBlock.ID = providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ID)
	}

	// Extract the query from the action
	if action.Query != nil {
		inputBytes, err := providerUtils.MarshalSorted(map[string]interface{}{
			"query": *action.Query,
		})
		if err == nil {
			serverToolUseBlock.Input = json.RawMessage(inputBytes)
		}
	}

	blocks = append(blocks, serverToolUseBlock)

	// 2. Always create web_search_tool_result block — Anthropic requires it alongside every server_tool_use.
	// Without this block, the API returns: "web_search tool use was found without a corresponding web_search_tool_result block"
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
	// Determine the tool use ID - prefer CallID (authoritative), fall back to msg.ID
	var toolUseID *string
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
		toolUseID = msg.ResponsesToolMessage.CallID
	} else {
		toolUseID = msg.ID
	}
	toolUseID = providerUtils.SanitizeAnthropicToolUseIDPtr(toolUseID)
	webSearchResultBlock := AnthropicContentBlock{
		Type:      AnthropicContentBlockTypeWebSearchToolResult,
		ToolUseID: toolUseID,
		Caller:    caller,
		Content: &AnthropicContent{
			ContentBlocks: resultBlocks,
		},
	}
	blocks = append(blocks, webSearchResultBlock)

	return blocks
}

func getAnthropicContentObject(content *AnthropicContent) *AnthropicContentBlock {
	if content == nil {
		return nil
	}
	if content.ContentObj != nil {
		return content.ContentObj
	}
	if len(content.ContentBlocks) > 0 {
		return &content.ContentBlocks[0]
	}
	return nil
}

func convertAnthropicWebFetchResultToBifrost(block *AnthropicContentBlock) *schemas.ResponsesWebFetchCall {
	if block == nil {
		return nil
	}
	result := &schemas.ResponsesWebFetchCall{}
	inner := getAnthropicContentObject(block.Content)
	if inner == nil {
		return result
	}

	result.ResultType = string(inner.Type)
	result.URL = inner.URL
	result.RetrievedAt = inner.RetrievedAt
	result.ErrorCode = inner.ErrorCode

	doc := getAnthropicContentObject(inner.Content)
	if doc != nil {
		result.Document = &schemas.ResponsesWebFetchDocument{
			Type:    string(doc.Type),
			Text:    doc.Text,
			Title:   doc.Title,
			Context: doc.Context,
		}
		if doc.Citations != nil && doc.Citations.Config != nil {
			result.Document.Citations = doc.Citations.Config
		}
		if doc.Source != nil && doc.Source.SourceObj != nil {
			src := doc.Source.SourceObj
			result.Document.Source = &schemas.ResponsesWebFetchSource{
				Type:      src.Type,
				MediaType: src.MediaType,
				Data:      src.Data,
				URL:       src.URL,
				FileID:    src.FileID,
			}
		}
	}

	return result
}

func attachAnthropicWebFetchResult(messages []schemas.ResponsesMessage, toolUseID string, block AnthropicContentBlock) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := &messages[i]
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeWebFetchCall {
			continue
		}
		if msg.ID == nil || *msg.ID != toolUseID {
			continue
		}
		if msg.ResponsesToolMessage == nil {
			msg.ResponsesToolMessage = &schemas.ResponsesToolMessage{}
		}
		msg.ResponsesToolMessage.CallID = &toolUseID
		msg.ResponsesToolMessage.ResponsesWebFetchCall = convertAnthropicWebFetchResultToBifrost(&block)
		break
	}
}

func convertBifrostWebFetchCallToAnthropicBlocks(msg *schemas.ResponsesMessage) []AnthropicContentBlock {
	if msg == nil || msg.ResponsesToolMessage == nil {
		return nil
	}
	tm := msg.ResponsesToolMessage

	var toolUseID *string
	if tm.CallID != nil {
		toolUseID = tm.CallID
	} else {
		toolUseID = msg.ID
	}
	toolUseID = providerUtils.SanitizeAnthropicToolUseIDPtr(toolUseID)

	var caller *AnthropicToolCaller
	if tm.Caller != nil {
		caller = &AnthropicToolCaller{Type: AnthropicToolCallerType(tm.Caller.Type), ToolID: providerUtils.SanitizeAnthropicToolUseIDPtr(tm.Caller.ToolID)}
	}

	serverToolUseBlock := AnthropicContentBlock{
		Type:   AnthropicContentBlockTypeServerToolUse,
		ID:     toolUseID,
		Name:   schemas.Ptr(string(AnthropicToolNameWebFetch)),
		Caller: caller,
		Input:  json.RawMessage("{}"),
	}
	if tm.Action != nil && tm.Action.ResponsesWebFetchToolCallAction != nil &&
		tm.Action.ResponsesWebFetchToolCallAction.URL != "" {
		if inputBytes, err := providerUtils.MarshalSorted(map[string]interface{}{"url": tm.Action.ResponsesWebFetchToolCallAction.URL}); err == nil {
			serverToolUseBlock.Input = json.RawMessage(inputBytes)
		}
	}

	blocks := []AnthropicContentBlock{serverToolUseBlock}
	if tm.ResponsesWebFetchCall == nil {
		return blocks
	}

	wf := tm.ResponsesWebFetchCall
	resultType := wf.ResultType
	if resultType == "" {
		resultType = "web_fetch_result"
	}
	inner := AnthropicContentBlock{
		Type:        AnthropicContentBlockType(resultType),
		URL:         wf.URL,
		RetrievedAt: wf.RetrievedAt,
		ErrorCode:   wf.ErrorCode,
	}
	if wf.Document != nil {
		doc := &AnthropicContentBlock{
			Type:    AnthropicContentBlockType(wf.Document.Type),
			Text:    wf.Document.Text,
			Title:   wf.Document.Title,
			Context: wf.Document.Context,
		}
		if doc.Type == "" {
			doc.Type = AnthropicContentBlockTypeDocument
		}
		if wf.Document.Citations != nil {
			doc.Citations = &AnthropicCitations{Config: wf.Document.Citations}
		}
		if wf.Document.Source != nil {
			src := wf.Document.Source
			doc.Source = &AnthropicBlockSource{SourceObj: &AnthropicSource{
				Type:      src.Type,
				MediaType: src.MediaType,
				Data:      src.Data,
				URL:       src.URL,
				FileID:    src.FileID,
			}}
		}
		inner.Content = &AnthropicContent{ContentObj: doc}
	}

	blocks = append(blocks, AnthropicContentBlock{
		Type:      AnthropicContentBlockTypeWebFetchToolResult,
		ToolUseID: toolUseID,
		Caller:    caller,
		Content:   &AnthropicContent{ContentObj: &inner},
	})

	return blocks
}

// convertBifrostAdvisorCallToAnthropicBlocks rebuilds the advisor server_tool_use
// block and its paired advisor_tool_result block from a neutral advisor_call.
// Anthropic requires both blocks to appear together in the assistant message.
func convertBifrostAdvisorCallToAnthropicBlocks(msg *schemas.ResponsesMessage) []AnthropicContentBlock {
	// Resolve the tool-use id (server_tool_use.id == advisor_tool_result.tool_use_id).
	var toolUseID *string
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
		toolUseID = msg.ResponsesToolMessage.CallID
	} else {
		toolUseID = msg.ID
	}
	toolUseID = providerUtils.SanitizeAnthropicToolUseIDPtr(toolUseID)

	// 1. server_tool_use block — advisor input is always empty.
	serverToolUseBlock := AnthropicContentBlock{
		Type:  AnthropicContentBlockTypeServerToolUse,
		ID:    toolUseID,
		Name:  schemas.Ptr(string(AnthropicToolNameAdvisor)),
		Input: json.RawMessage("{}"),
	}

	// 2. advisor_tool_result block carrying the advisor's result content.
	resultBlock := AnthropicContentBlock{
		Type:      AnthropicContentBlockTypeAdvisorToolResult,
		ToolUseID: toolUseID,
	}
	if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.ResponsesAdvisorCall != nil {
		adv := msg.ResponsesToolMessage.ResponsesAdvisorCall
		resultType := adv.ResultType
		if resultType == "" {
			resultType = "advisor_result"
		}
		resultBlock.Content = &AnthropicContent{
			ContentObj: &AnthropicContentBlock{
				Type:             AnthropicContentBlockType(resultType),
				Text:             adv.Text,
				EncryptedContent: adv.EncryptedContent,
				ErrorCode:        adv.ErrorCode,
				StopReason:       adv.StopReason,
			},
		}
	}

	return []AnthropicContentBlock{serverToolUseBlock, resultBlock}
}

// convertBifrostToolSearchCallToAnthropicBlocks rebuilds the tool_search
// server_tool_use block and its paired tool_search_tool_result block (carrying
// the discovered tool_references) from a neutral tool_search_call. Anthropic
// requires the server_tool_use to be followed by its result block in the
// assistant message, so a follow-up turn that references a discovered tool keeps
// the search context. Mirrors convertBifrostWebSearchCall/AdvisorCall.
func convertBifrostToolSearchCallToAnthropicBlocks(msg *schemas.ResponsesMessage) []AnthropicContentBlock {
	if msg.ResponsesToolMessage == nil {
		return nil
	}

	// Resolve the tool-use id (server_tool_use.id == tool_search_tool_result.tool_use_id).
	var toolUseID *string
	if msg.ResponsesToolMessage.CallID != nil {
		toolUseID = msg.ResponsesToolMessage.CallID
	} else {
		toolUseID = msg.ID
	}
	// A JSON-decoded tool_search_call input item carries no CallID/ID (only the
	// preserved raw bytes + arguments), so toolUseID can be nil. Anthropic rejects
	// server_tool_use / tool_search_tool_result blocks with a nil id — skip rather
	// than emit an invalid pair, consistent with the nil-ResponsesToolMessage guard.
	if toolUseID == nil {
		return nil
	}

	// 1. server_tool_use block. Preserve the search variant name (regex/bm25);
	// the query is not retained on the neutral item, so send an empty input.
	name := string(AnthropicToolNameToolSearchRegex)
	if msg.ResponsesToolMessage.Name != nil && *msg.ResponsesToolMessage.Name != "" {
		name = *msg.ResponsesToolMessage.Name
	}
	serverToolUseBlock := AnthropicContentBlock{
		Type:  AnthropicContentBlockTypeServerToolUse,
		ID:    toolUseID,
		Name:  schemas.Ptr(name),
		Input: json.RawMessage("{}"),
	}

	// 2. tool_search_tool_result block reconstructed from the discovered tool names.
	var toolReferences []AnthropicContentBlock
	if msg.ResponsesToolMessage.ResponsesToolSearchCall != nil {
		for _, toolName := range msg.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences {
			toolReferences = append(toolReferences, AnthropicContentBlock{
				Type:     AnthropicContentBlockTypeToolReference,
				ToolName: schemas.Ptr(toolName),
			})
		}
	}
	resultBlock := AnthropicContentBlock{
		Type:           AnthropicContentBlockTypeToolSearchToolResult,
		ToolUseID:      toolUseID,
		ToolReferences: toolReferences,
	}

	return []AnthropicContentBlock{serverToolUseBlock, resultBlock}
}

// isAnthropicCodeExecutionToolName reports whether name is one of the code
// execution sub-tools (code_execution_20250825+ surfaces bash + text_editor;
// code_execution is the legacy Python sub-tool).
func isAnthropicCodeExecutionToolName(name string) bool {
	switch AnthropicToolName(name) {
	case AnthropicToolNameCodeExecution, AnthropicToolNameBashCodeExecution, AnthropicToolNameTextEditorCodeExecution:
		return true
	default:
		return false
	}
}

// anthropicCodeExecResultBlockType maps a code-execution sub-tool name to its
// outer *_tool_result block type.
func anthropicCodeExecResultBlockType(toolName string) AnthropicContentBlockType {
	switch AnthropicToolName(toolName) {
	case AnthropicToolNameBashCodeExecution:
		return AnthropicContentBlockTypeBashCodeExecutionToolResult
	case AnthropicToolNameTextEditorCodeExecution:
		return AnthropicContentBlockTypeTextEditorCodeExecutionToolResult
	default:
		return AnthropicContentBlockTypeCodeExecutionToolResult
	}
}

// anthropicCodeExecInnerResultType maps a code-execution sub-tool name to its
// inner result-content discriminator (used when the stored result type is absent).
func anthropicCodeExecInnerResultType(toolName string) AnthropicContentBlockType {
	switch AnthropicToolName(toolName) {
	case AnthropicToolNameBashCodeExecution:
		return AnthropicContentBlockTypeBashCodeExecutionResult
	case AnthropicToolNameTextEditorCodeExecution:
		return AnthropicContentBlockTypeTextEditorCodeExecutionResult
	default:
		return AnthropicContentBlockTypeCodeExecutionResult
	}
}

// anthropicCodeExecOutputBlockType maps a code-execution sub-tool name to the
// file-output block type emitted inside a result's content array.
func anthropicCodeExecOutputBlockType(toolName string) AnthropicContentBlockType {
	if AnthropicToolName(toolName) == AnthropicToolNameBashCodeExecution {
		return AnthropicContentBlockTypeBashCodeExecutionOutput
	}
	return AnthropicContentBlockTypeCodeExecutionOutput
}

// buildBifrostCodeExecutionCall converts a code-execution server_tool_use block
// into a neutral code_interpreter_call message that also carries the Anthropic
// fidelity (sub-tool name, verbatim input, caller). The paired *_tool_result is
// attached later by attachAnthropicCodeExecutionResult.
func buildBifrostCodeExecutionCall(block AnthropicContentBlock) schemas.ResponsesMessage {
	carry := &schemas.ResponsesCodeExecutionCall{}
	if block.Name != nil {
		carry.ToolName = *block.Name
	}
	ci := &schemas.ResponsesCodeInterpreterToolCall{}

	if len(block.Input) > 0 {
		raw := string(block.Input)
		carry.Input = &raw
		// Populate the neutral "code" for OpenAI-compatible consumers: Python uses
		// "code", bash uses "command". text_editor verbs are not runnable code, so
		// the neutral code stays empty and only the carry input round-trips them.
		switch AnthropicToolName(carry.ToolName) {
		case AnthropicToolNameCodeExecution:
			if c := providerUtils.GetJSONField(block.Input, "code"); c.Exists() && c.Type == gjson.String {
				ci.Code = schemas.Ptr(c.Str)
			}
		case AnthropicToolNameBashCodeExecution:
			if c := providerUtils.GetJSONField(block.Input, "command"); c.Exists() && c.Type == gjson.String {
				ci.Code = schemas.Ptr(c.Str)
			}
		}
	}

	if block.Caller != nil {
		carry.Caller = &schemas.ResponsesToolCaller{
			Type:   string(block.Caller.Type),
			ToolID: block.Caller.ToolID,
		}
	}

	return schemas.ResponsesMessage{
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
		ID:     block.ID,
		Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                           block.ID,
			ResponsesCodeInterpreterToolCall: ci,
			ResponsesCodeExecutionCall:       carry,
		},
	}
}

// attachAnthropicCodeExecutionResult finds the most recent code_interpreter_call
// matching toolUseID and folds the *_tool_result block onto it — both the
// Anthropic fidelity carry and the neutral code_interpreter outputs.
func attachAnthropicCodeExecutionResult(msgs []schemas.ResponsesMessage, toolUseID string, block AnthropicContentBlock) {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := &msgs[i]
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeCodeInterpreterCall {
			continue
		}
		if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.CallID == nil ||
			*msg.ResponsesToolMessage.CallID != toolUseID {
			continue
		}

		tm := msg.ResponsesToolMessage
		if tm.ResponsesCodeExecutionCall == nil {
			tm.ResponsesCodeExecutionCall = &schemas.ResponsesCodeExecutionCall{}
		}
		if tm.ResponsesCodeInterpreterToolCall == nil {
			tm.ResponsesCodeInterpreterToolCall = &schemas.ResponsesCodeInterpreterToolCall{}
		}
		carry := tm.ResponsesCodeExecutionCall
		ci := tm.ResponsesCodeInterpreterToolCall

		// content is a single object on the wire (ContentObj); UnmarshalJSON may
		// wrap it into ContentBlocks instead, so accept either.
		var inner *AnthropicContentBlock
		if c := block.Content; c != nil {
			inner = c.ContentObj
			if inner == nil && len(c.ContentBlocks) > 0 {
				inner = &c.ContentBlocks[0]
			}
		}
		if inner != nil {
			carry.ResultType = string(inner.Type)
			carry.Stdout = inner.Stdout
			carry.Stderr = inner.Stderr
			carry.ReturnCode = inner.ReturnCode
			carry.EncryptedStdout = inner.EncryptedStdout
			carry.FileType = inner.FileType
			carry.StartLine = inner.StartLine
			carry.NumLines = inner.NumLines
			carry.TotalLines = inner.TotalLines
			carry.IsFileUpdate = inner.IsFileUpdate
			carry.OldStart = inner.OldStart
			carry.OldLines = inner.OldLines
			carry.NewStart = inner.NewStart
			carry.NewLines = inner.NewLines
			carry.Lines = inner.Lines
			carry.ErrorCode = inner.ErrorCode

			// The inner result's own "content" is either a string (text_editor view
			// file contents) or an array of file-output blocks (bash/python files).
			if ic := inner.Content; ic != nil {
				if ic.ContentStr != nil {
					carry.FileContent = ic.ContentStr
				}
				for _, fb := range ic.ContentBlocks {
					if fb.FileID != nil {
						carry.Files = append(carry.Files, schemas.ResponsesCodeExecutionFileOutput{FileID: *fb.FileID})
					}
				}
			}

			// Neutral OpenAI-compatible view: stdout becomes a logs output.
			if inner.Stdout != nil && *inner.Stdout != "" {
				ci.Outputs = append(ci.Outputs, schemas.ResponsesCodeInterpreterOutput{
					ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{
						Type: "logs",
						Logs: *inner.Stdout,
					},
				})
			}
		}

		// The caller union may also appear on the result block.
		if block.Caller != nil && carry.Caller == nil {
			carry.Caller = &schemas.ResponsesToolCaller{
				Type:   string(block.Caller.Type),
				ToolID: block.Caller.ToolID,
			}
		}

		if carry.ErrorCode != nil {
			msg.Status = schemas.Ptr("failed")
		}
		return
	}
}

// convertBifrostCodeExecCallToAnthropicBlocks rebuilds the Anthropic
// server_tool_use + *_code_execution_tool_result block pair from a neutral
// code_interpreter_call carrying ResponsesCodeExecutionCall. Anthropic requires
// both blocks to appear together in the assistant message.
func convertBifrostCodeExecCallToAnthropicBlocks(msg *schemas.ResponsesMessage) []AnthropicContentBlock {
	tm := msg.ResponsesToolMessage
	if tm == nil {
		return nil
	}
	cec := tm.ResponsesCodeExecutionCall
	ci := tm.ResponsesCodeInterpreterToolCall
	if cec == nil && ci == nil {
		return nil
	}
	if cec == nil {
		// OpenAI-origin code_interpreter_call without the Anthropic carry: synthesize
		// the fidelity from the neutral fields so it still round-trips to Anthropic.
		cec = &schemas.ResponsesCodeExecutionCall{}
	}

	var toolUseID *string
	if tm.CallID != nil {
		toolUseID = tm.CallID
	} else {
		toolUseID = msg.ID
	}
	toolUseID = providerUtils.SanitizeAnthropicToolUseIDPtr(toolUseID)

	toolName := cec.ToolName
	if toolName == "" {
		toolName = string(AnthropicToolNameCodeExecution)
	}

	// 1. server_tool_use block.
	serverToolUse := AnthropicContentBlock{
		Type: AnthropicContentBlockTypeServerToolUse,
		ID:   toolUseID,
		Name: schemas.Ptr(toolName),
	}
	if cec.Input != nil {
		serverToolUse.Input = json.RawMessage(*cec.Input)
	} else if ci != nil && ci.Code != nil {
		// Reconstruct a minimal input from the neutral code field.
		key := "code"
		if AnthropicToolName(toolName) == AnthropicToolNameBashCodeExecution {
			key = "command"
		}
		if inputBytes, err := providerUtils.MarshalSorted(map[string]interface{}{key: *ci.Code}); err == nil {
			serverToolUse.Input = json.RawMessage(inputBytes)
		}
	}
	if cec.Caller != nil {
		serverToolUse.Caller = &AnthropicToolCaller{
			Type:   AnthropicToolCallerType(cec.Caller.Type),
			ToolID: providerUtils.SanitizeAnthropicToolUseIDPtr(cec.Caller.ToolID),
		}
	}

	// 2. inner result-content object.
	stdout := cec.Stdout
	if stdout == nil && ci != nil {
		// OpenAI-origin: fold the first logs output back into stdout.
		for _, o := range ci.Outputs {
			if o.ResponsesCodeInterpreterOutputLogs != nil {
				logs := o.ResponsesCodeInterpreterOutputLogs.Logs
				stdout = &logs
				break
			}
		}
	}
	inner := AnthropicContentBlock{
		Type:            AnthropicContentBlockType(cec.ResultType),
		Stdout:          stdout,
		Stderr:          cec.Stderr,
		ReturnCode:      cec.ReturnCode,
		EncryptedStdout: cec.EncryptedStdout,
		FileType:        cec.FileType,
		StartLine:       cec.StartLine,
		NumLines:        cec.NumLines,
		TotalLines:      cec.TotalLines,
		IsFileUpdate:    cec.IsFileUpdate,
		OldStart:        cec.OldStart,
		OldLines:        cec.OldLines,
		NewStart:        cec.NewStart,
		NewLines:        cec.NewLines,
		Lines:           cec.Lines,
		ErrorCode:       cec.ErrorCode,
	}
	if inner.Type == "" {
		inner.Type = anthropicCodeExecInnerResultType(toolName)
	}

	// Rebuild the inner result's own "content": text_editor view file contents
	// (string) and/or generated file outputs (array of *_code_execution_output).
	var innerContent *AnthropicContent
	if cec.FileContent != nil {
		innerContent = &AnthropicContent{ContentStr: cec.FileContent}
	}
	if len(cec.Files) > 0 {
		outputType := anthropicCodeExecOutputBlockType(toolName)
		fileBlocks := make([]AnthropicContentBlock, 0, len(cec.Files))
		for _, f := range cec.Files {
			fileID := f.FileID
			fileBlocks = append(fileBlocks, AnthropicContentBlock{Type: outputType, FileID: &fileID})
		}
		if innerContent == nil {
			innerContent = &AnthropicContent{}
		}
		innerContent.ContentBlocks = fileBlocks
	}

	if innerContent == nil {
		switch inner.Type {
		case AnthropicContentBlockTypeCodeExecutionResult,
			AnthropicContentBlockTypeBashCodeExecutionResult,
			AnthropicContentBlockTypeEncryptedCodeExecutionResult:
			innerContent = &AnthropicContent{ContentBlocks: []AnthropicContentBlock{}}
		}
	}
	inner.Content = innerContent

	// 3. outer *_tool_result block wrapping the inner content object.
	resultBlock := AnthropicContentBlock{
		Type:      anthropicCodeExecResultBlockType(toolName),
		ToolUseID: toolUseID,
		Content:   &AnthropicContent{ContentObj: &inner},
	}
	if cec.Caller != nil {
		resultBlock.Caller = &AnthropicToolCaller{
			Type:   AnthropicToolCallerType(cec.Caller.Type),
			ToolID: providerUtils.SanitizeAnthropicToolUseIDPtr(cec.Caller.ToolID),
		}
	}

	return []AnthropicContentBlock{serverToolUse, resultBlock}
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
				ContentBlocks: []AnthropicContentBlock{{
					Type: AnthropicContentBlockTypeText,
					Text: &description,
				}},
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
			ToolUseID: providerUtils.SanitizeAnthropicToolUseIDPtr(msg.ResponsesToolMessage.CallID),
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
					ContentBlocks: []AnthropicContentBlock{{
						Type: AnthropicContentBlockTypeText,
						Text: &outputText,
					}},
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

	// Skip mcp_toolset entries — these are merged with mcp_servers in ToBifrostResponsesRequest
	if tool.MCPToolset != nil {
		return nil
	}

	// Handle special tool types first
	if tool.Type != nil {
		// Version-dated server search tools ship new versions regularly; match them by
		// prefix so a newer version (e.g. web_fetch_20260318) is recognized as a server
		// tool instead of falling through to the client-function default at the end of
		// this function. Mirrors applySharedServerToolFields and the chat path. Tools
		// that must round-trip their exact version (code_execution, text_editor) stay in
		// the exact switch below.
		switch typeStr := string(*tool.Type); {
		case strings.HasPrefix(typeStr, "web_search_"):
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

		case strings.HasPrefix(typeStr, "web_fetch_"):
			bifrostTool := &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeWebFetch,
			}
			if tool.AnthropicToolWebFetch != nil {
				bifrostTool.ResponsesToolWebFetch = &schemas.ResponsesToolWebFetch{
					MaxUses:           tool.AnthropicToolWebFetch.MaxUses,
					MaxContentTokens:  tool.AnthropicToolWebFetch.MaxContentTokens,
					UseCache:          tool.AnthropicToolWebFetch.UseCache,
					ResponseInclusion: tool.AnthropicToolWebFetch.ResponseInclusion,
				}
				if len(tool.AnthropicToolWebFetch.AllowedDomains) > 0 || len(tool.AnthropicToolWebFetch.BlockedDomains) > 0 {
					bifrostTool.ResponsesToolWebFetch.Filters = &schemas.ResponsesToolWebSearchFilters{
						AllowedDomains: tool.AnthropicToolWebFetch.AllowedDomains,
						BlockedDomains: tool.AnthropicToolWebFetch.BlockedDomains,
					}
				}
			}
			return bifrostTool
		}

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

		case AnthropicToolTypeCodeExecution20250522, AnthropicToolTypeCodeExecution,
			AnthropicToolTypeCodeExecution20260120, AnthropicToolTypeCodeExecution20260521:
			// Preserve the exact requested version so its capability tier
			// (bash/file vs. PTC+REPL vs. disclosed time limit) round-trips.
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{
					Version: schemas.Ptr(string(*tool.Type)),
				},
			}

		case AnthropicToolTypeMemory20250818:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeMemory,
				Name: &tool.Name,
			}

		case AnthropicToolTypeToolSearchBM25, AnthropicToolTypeToolSearchBM2520251119,
			AnthropicToolTypeToolSearchRegex, AnthropicToolTypeToolSearchRegex20251119:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeToolSearch,
				Name: &tool.Name,
			}

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

		case AnthropicToolTypeAdvisor20260301:
			bifrostTool := &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeAdvisor,
			}
			if tool.AnthropicToolAdvisor != nil {
				bifrostTool.ResponsesToolAdvisor = &schemas.ResponsesToolAdvisor{
					Model:     tool.AnthropicToolAdvisor.Model,
					MaxUses:   tool.AnthropicToolAdvisor.MaxUses,
					MaxTokens: tool.AnthropicToolAdvisor.MaxTokens,
				}
				if tool.AnthropicToolAdvisor.Caching != nil {
					bifrostTool.ResponsesToolAdvisor.Caching = &schemas.ResponsesToolAdvisorCaching{
						Type: tool.AnthropicToolAdvisor.Caching.Type,
						TTL:  tool.AnthropicToolAdvisor.Caching.TTL,
					}
				}
			}
			return bifrostTool
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

// convertBifrostToolsToAnthropic converts all Bifrost tools to Anthropic tools and MCP servers.
// It handles context-dependent conversions like code_interpreter, which must be skipped when
// web_search or web_fetch is present (Anthropic auto-injects code_execution in that case).
func convertBifrostToolsToAnthropic(model string, tools []schemas.ResponsesTool, provider schemas.ModelProvider) ([]AnthropicTool, []AnthropicMCPServerV2) {
	// Check if web search or web fetch is present — when they are, Anthropic
	// auto-injects code_execution so we must skip it to avoid conflicts.
	hasWebSearchOrFetch := false
	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeWebSearch || tool.Type == schemas.ResponsesToolTypeWebFetch {
			hasWebSearchOrFetch = true
			break
		}
	}

	anthropicTools := []AnthropicTool{}
	mcpServers := []AnthropicMCPServerV2{}
	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeMCP && tool.ResponsesToolMCP != nil {
			server, toolset := convertBifrostMCPToolToAnthropicNew(&tool)
			if server != nil {
				mcpServers = append(mcpServers, *server)
			}
			if toolset != nil {
				mcpTool := AnthropicTool{MCPToolset: toolset}
				applyResponsesToolAnthropicFlags(&mcpTool, &tool)
				anthropicTools = append(anthropicTools, mcpTool)
			}
			continue
		}
		anthropicTool := convertBifrostToolToAnthropic(model, &tool, provider, hasWebSearchOrFetch)
		if anthropicTool != nil {
			applyResponsesToolAnthropicFlags(anthropicTool, &tool)
			anthropicTools = append(anthropicTools, *anthropicTool)
		}
	}
	return anthropicTools, mcpServers
}

// applyAnthropicToolFlagsToResponsesTool propagates the Anthropic-native tool
// flags (DeferLoading, AllowedCallers, InputExamples, EagerInputStreaming) in
// the inbound direction: from the incoming AnthropicTool onto the neutral
// ResponsesTool when the native Anthropic /v1/messages endpoint is the entry
// point. Called once per converted tool so every return path inside
// convertAnthropicToolToBifrost benefits.
func applyAnthropicToolFlagsToResponsesTool(at *AnthropicTool, rt *schemas.ResponsesTool) {
	if at == nil || rt == nil {
		return
	}
	if at.DeferLoading != nil {
		rt.DeferLoading = at.DeferLoading
	}
	if len(at.AllowedCallers) > 0 {
		rt.AllowedCallers = at.AllowedCallers
	}
	if len(at.InputExamples) > 0 {
		rt.InputExamples = make([]schemas.ChatToolInputExample, len(at.InputExamples))
		for i, ex := range at.InputExamples {
			rt.InputExamples[i] = schemas.ChatToolInputExample{
				Input:       ex.Input,
				Description: ex.Description,
			}
		}
	}
	if at.EagerInputStreaming != nil {
		rt.EagerInputStreaming = at.EagerInputStreaming
	}
}

// applyResponsesToolAnthropicFlags propagates the Anthropic-native tool flags
// (DeferLoading, AllowedCallers, InputExamples, EagerInputStreaming) from the
// neutral ResponsesTool onto the provider-native AnthropicTool. Called once
// per converted tool so every branch in convertBifrostToolToAnthropic
// benefits without duplicating the logic on each return path.
func applyResponsesToolAnthropicFlags(at *AnthropicTool, rt *schemas.ResponsesTool) {
	if at == nil || rt == nil {
		return
	}
	if rt.DeferLoading != nil {
		at.DeferLoading = rt.DeferLoading
	}
	if len(rt.AllowedCallers) > 0 {
		at.AllowedCallers = rt.AllowedCallers
	}
	if len(rt.InputExamples) > 0 {
		at.InputExamples = make([]AnthropicToolInputExample, len(rt.InputExamples))
		for i, ex := range rt.InputExamples {
			at.InputExamples[i] = AnthropicToolInputExample{
				Input:       ex.Input,
				Description: ex.Description,
			}
		}
	}
	if rt.EagerInputStreaming != nil {
		at.EagerInputStreaming = rt.EagerInputStreaming
	}
}

// Helper function to convert Tool back to AnthropicTool
func convertBifrostToolToAnthropic(model string, tool *schemas.ResponsesTool, provider schemas.ModelProvider, hasWebSearchOrFetch bool) *AnthropicTool {
	if tool == nil {
		return nil
	}

	// Text editor family (text_editor_*): normalize to the model's generation
	// via TextEditorGeneration so old-gen requests against new-gen-only models
	// (e.g. sonnet-4-5+) get auto-upgraded to text_editor_20250728. Scoped to
	// text_editor only — computer-use tools are handled by the case below
	// (which preserves the embedded display_width_px / display_height_px from
	// ResponsesToolComputerUsePreview), and bash falls through to the
	// ResponsesToolTypeLocalShell case (no version variants).
	if baseTool := computerUseBaseTool(string(tool.Type)); baseTool == "text_editor" {
		if wantType, wantName := NormalizedToolSpec(TextEditorGeneration(model), baseTool); wantType != "" {
			anthropicType := AnthropicToolType(wantType)
			return &AnthropicTool{
				Type: &anthropicType,
				Name: wantName,
			}
		}
	}

	switch tool.Type {
	case schemas.ResponsesToolTypeCodeInterpreter:
		if hasWebSearchOrFetch {
			// Skip code execution tools when web search/fetch is present —
			// the Anthropic API auto-injects code_execution in that case.
			// Including it explicitly causes "Auto-injecting tools would conflict" errors.
			return nil
		}
		// When no web search/fetch, explicitly include code_execution. Forward the
		// exact requested version verbatim (its capability tier — bash/file vs.
		// PTC+REPL vs. disclosed time limit — must round-trip). Fall back to
		// 20250825, the only version every model supports, when none was captured
		// (e.g. an OpenAI-origin code_interpreter request).
		codeExecVersion := AnthropicToolTypeCodeExecution
		if tool.ResponsesToolCodeInterpreter != nil &&
			tool.ResponsesToolCodeInterpreter.Version != nil &&
			*tool.ResponsesToolCodeInterpreter.Version != "" {
			codeExecVersion = AnthropicToolType(*tool.ResponsesToolCodeInterpreter.Version)
		}
		return &AnthropicTool{
			Type: schemas.Ptr(codeExecVersion),
			Name: string(AnthropicToolNameCodeExecution),
		}
	case schemas.ResponsesToolTypeComputerUsePreview:
		if tool.ResponsesToolComputerUsePreview != nil {
			computerToolType := AnthropicToolTypeComputer20250124
			if ComputerUseGeneration(model) == ComputerUseGen20251124 {
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
		webSearchType := AnthropicToolTypeWebSearch20250305
		// Dynamic filtering (web_search_20260209) available on Anthropic + Azure for Opus 4.6+, Sonnet 4.6+.
		features, ok := ProviderFeatures[provider]
		if ok && features.WebSearchDynamic &&
			(strings.Contains(model, "4.6") || strings.Contains(model, "4-6") || IsOpus47Plus(model) || IsSonnet5Plus(model) || IsFableFamily(model)) {
			webSearchType = AnthropicToolTypeWebSearch20260209
		}
		anthropicTool := &AnthropicTool{
			Type:                   schemas.Ptr(webSearchType),
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
	case schemas.ResponsesToolTypeWebFetch:
		webFetchType := AnthropicToolTypeWebFetch20250910
		// Dynamic filtering versions only available on Anthropic + Azure
		features, ok := ProviderFeatures[provider]
		if ok && features.WebSearchDynamic &&
			(strings.Contains(model, "4.6") || strings.Contains(model, "4-6")) {
			webFetchType = AnthropicToolTypeWebFetch20260309
		}
		if tool.ResponsesToolWebFetch != nil && tool.ResponsesToolWebFetch.ResponseInclusion != nil {
			webFetchType = AnthropicToolTypeWebFetch20260318
		} else if tool.ResponsesToolWebFetch != nil && tool.ResponsesToolWebFetch.UseCache != nil &&
			webFetchType == AnthropicToolTypeWebFetch20250910 {
			webFetchType = AnthropicToolTypeWebFetch20260309
		}
		anthropicTool := &AnthropicTool{
			Type:                  schemas.Ptr(webFetchType),
			Name:                  string(AnthropicToolNameWebFetch),
			AnthropicToolWebFetch: &AnthropicToolWebFetch{},
		}
		if tool.ResponsesToolWebFetch != nil {
			anthropicTool.AnthropicToolWebFetch.MaxUses = tool.ResponsesToolWebFetch.MaxUses
			anthropicTool.AnthropicToolWebFetch.MaxContentTokens = tool.ResponsesToolWebFetch.MaxContentTokens
			anthropicTool.AnthropicToolWebFetch.UseCache = tool.ResponsesToolWebFetch.UseCache
			anthropicTool.AnthropicToolWebFetch.ResponseInclusion = tool.ResponsesToolWebFetch.ResponseInclusion
			if tool.ResponsesToolWebFetch.Filters != nil {
				anthropicTool.AnthropicToolWebFetch.AllowedDomains = tool.ResponsesToolWebFetch.Filters.AllowedDomains
				anthropicTool.AnthropicToolWebFetch.BlockedDomains = tool.ResponsesToolWebFetch.Filters.BlockedDomains
			}
		}
		return anthropicTool
	case schemas.ResponsesToolTypeMemory:
		anthropicTool := &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeMemory20250818),
			Name: string(AnthropicToolNameMemory),
		}
		return anthropicTool
	case schemas.ResponsesToolTypeToolSearch:
		toolSearchType := AnthropicToolTypeToolSearchBM2520251119
		toolSearchName := AnthropicToolNameToolSearchBM25
		if tool.Name != nil && strings.Contains(*tool.Name, "regex") {
			toolSearchType = AnthropicToolTypeToolSearchRegex20251119
			toolSearchName = AnthropicToolNameToolSearchRegex
		}
		return &AnthropicTool{
			Type: schemas.Ptr(toolSearchType),
			Name: string(toolSearchName),
		}
	case schemas.ResponsesToolTypeLocalShell:
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeBash20250124),
			Name: string(AnthropicToolNameBash),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250124):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250124),
			Name: string(AnthropicToolNameTextEditorLegacy),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250429):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250429),
			Name: string(AnthropicToolNameTextEditorLegacy),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250728):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250728),
			Name: string(AnthropicToolNameTextEditor),
		}
	case schemas.ResponsesToolTypeAdvisor:
		advisor := &AnthropicToolAdvisor{}
		if tool.ResponsesToolAdvisor != nil {
			advisor.Model = tool.ResponsesToolAdvisor.Model
			advisor.MaxUses = tool.ResponsesToolAdvisor.MaxUses
			advisor.MaxTokens = tool.ResponsesToolAdvisor.MaxTokens
			if tool.ResponsesToolAdvisor.Caching != nil {
				advisor.Caching = &AnthropicToolAdvisorCaching{
					Type: tool.ResponsesToolAdvisor.Caching.Type,
					TTL:  tool.ResponsesToolAdvisor.Caching.TTL,
				}
			}
		}
		return &AnthropicTool{
			Type:                 schemas.Ptr(AnthropicToolTypeAdvisor20260301),
			Name:                 string(AnthropicToolNameAdvisor),
			AnthropicToolAdvisor: advisor,
		}
	}

	// Skip tools with no name — Anthropic rejects them
	if tool.Name == nil || *tool.Name == "" {
		return nil
	}

	anthropicTool := &AnthropicTool{}
	anthropicTool.Name = *tool.Name

	if tool.Description != nil {
		anthropicTool.Description = tool.Description
	}

	// Convert parameters and strict from ToolFunction
	if tool.ResponsesToolFunction != nil {
		anthropicTool.Strict = tool.ResponsesToolFunction.Strict
	}
	if tool.ResponsesToolFunction != nil && tool.ResponsesToolFunction.Parameters != nil {
		anthropicTool.InputSchema = tool.ResponsesToolFunction.Parameters
	} else {
		// Anthropic requires input_schema for custom tools, provide empty object schema if missing
		anthropicTool.InputSchema = &schemas.ToolFunctionParameters{
			Type:       "object",
			Properties: &schemas.OrderedMap{},
		}
	}

	// Normalize tool schema key ordering to ensure deterministic serialization.
	// Clients (e.g. Claude Agent SDK) may send non-deterministic property orderings
	// across turns, which breaks Anthropic's prefix-based prompt caching since tool
	// definitions are part of the serialized request prefix.
	// Normalized() returns a shallow copy with sorted key slices, so the
	// caller-owned tool.ResponsesToolFunction.Parameters is never mutated.
	if anthropicTool.InputSchema != nil {
		anthropicTool.InputSchema = anthropicTool.InputSchema.Normalized()
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
		if block.ResponsesInputMessageContentBlockFile != nil || block.FileID != nil {
			// Direct conversion without intermediate ChatContentBlock
			anthropicBlock := ConvertResponsesFileBlockToAnthropic(
				block.ResponsesInputMessageContentBlockFile,
				block.FileID,
				block.CacheControl,
				block.Citations,
			)
			return &anthropicBlock
		}
	case schemas.ResponsesInputMessageContentBlockTypeContainer:
		if block.FileID != nil {
			return &AnthropicContentBlock{
				Type:         AnthropicContentBlockTypeContainerUpload,
				FileID:       block.FileID,
				CacheControl: block.CacheControl,
			}
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

func (block AnthropicContentBlock) toBifrostResponsesContainerUploadBlock() schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{
		Type:         schemas.ResponsesInputMessageContentBlockTypeContainer,
		FileID:       block.FileID,
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

	if block.Source == nil || block.Source.SourceObj == nil {
		// File-block rendering only applies to object-form sources
		// (image / document). String-form sources (search_result) are
		// handled elsewhere.
		return resultBlock
	}
	src := block.Source.SourceObj

	// Handle different source types
	switch src.Type {
	case "url":
		// URL source
		if src.URL != nil {
			resultBlock.ResponsesInputMessageContentBlockFile.FileURL = src.URL
		}
	case "base64":
		// Base64 encoded data
		if src.Data != nil {
			// Construct data URL with media type
			mediaType := "application/pdf"
			if src.MediaType != nil {
				mediaType = *src.MediaType
			}
			dataURL := *src.Data
			if !strings.HasPrefix(dataURL, "data:") {
				dataURL = "data:" + mediaType + ";base64," + *src.Data
			}
			resultBlock.ResponsesInputMessageContentBlockFile.FileData = &dataURL
		}
	case "text":
		// Plain text source
		if src.Data != nil {
			resultBlock.ResponsesInputMessageContentBlockFile.FileType = schemas.Ptr("text/plain")
			resultBlock.ResponsesInputMessageContentBlockFile.FileData = src.Data
		}
	case "file":
		// File ID reference (requires files-api-2025-04-14 beta header)
		if src.FileID != nil {
			resultBlock.FileID = src.FileID
		}
	}

	return resultBlock
}

// Helper functions for MCP tool/server conversion
// convertAnthropicMCPServerV2ToBifrostTool converts a new-format MCP server to a Bifrost ResponsesTool.
func convertAnthropicMCPServerV2ToBifrostTool(mcpServer *AnthropicMCPServerV2) *schemas.ResponsesTool {
	if mcpServer == nil {
		return nil
	}

	bifrostTool := &schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeMCP,
		ResponsesToolMCP: &schemas.ResponsesToolMCP{
			ServerLabel: mcpServer.Name,
		},
	}

	if mcpServer.URL != "" {
		bifrostTool.ResponsesToolMCP.ServerURL = schemas.Ptr(mcpServer.URL)
	}
	if mcpServer.AuthorizationToken != nil {
		bifrostTool.ResponsesToolMCP.Authorization = mcpServer.AuthorizationToken
	}

	return bifrostTool
}

// applyMCPToolsetConfigToBifrostTool merges mcp_toolset tool configs (from tools[]) into a Bifrost MCP tool.
// Extracts the allowlist pattern: tools explicitly enabled in configs while default_config has enabled=false.
func applyMCPToolsetConfigToBifrostTool(bifrostTool *schemas.ResponsesTool, toolset *AnthropicMCPToolsetTool) {
	if bifrostTool == nil || bifrostTool.ResponsesToolMCP == nil || toolset == nil {
		return
	}

	// Extract allowed tools from the allowlist pattern:
	// default_config.enabled=false + individual tools enabled in configs
	if toolset.Configs != nil {
		defaultEnabled := true
		if toolset.DefaultConfig != nil && toolset.DefaultConfig.Enabled != nil {
			defaultEnabled = *toolset.DefaultConfig.Enabled
		}

		if !defaultEnabled {
			// Allowlist pattern: collect explicitly enabled tools.
			// Keep an empty allowlist to preserve the "deny all" case.
			allowedTools := make([]string, 0, len(toolset.Configs))
			for toolName, config := range toolset.Configs {
				if config != nil && config.Enabled != nil && *config.Enabled {
					allowedTools = append(allowedTools, toolName)
				}
			}
			bifrostTool.ResponsesToolMCP.AllowedTools = &schemas.ResponsesToolMCPAllowedTools{
				ToolNames: allowedTools,
			}
		}
	}

	// Apply cache control if present
	if toolset.CacheControl != nil {
		bifrostTool.CacheControl = toolset.CacheControl
	}
}

// convertAnthropicMCPServerToBifrostTool converts a deprecated-format Anthropic MCP server to a Bifrost ResponsesTool.
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

// convertBifrostMCPToolToAnthropicNew converts a Bifrost MCP tool to the new mcp-client-2025-11-20 format.
// Returns both a simplified server entry (for mcp_servers[]) and a toolset entry (for tools[]).
func convertBifrostMCPToolToAnthropicNew(tool *schemas.ResponsesTool) (*AnthropicMCPServerV2, *AnthropicMCPToolsetTool) {
	if tool == nil || tool.Type != schemas.ResponsesToolTypeMCP || tool.ResponsesToolMCP == nil {
		return nil, nil
	}

	// Build simplified server (no tool_configuration)
	server := &AnthropicMCPServerV2{
		Type: "url",
		Name: tool.ResponsesToolMCP.ServerLabel,
	}
	if tool.ResponsesToolMCP.ServerURL != nil {
		server.URL = *tool.ResponsesToolMCP.ServerURL
	}
	if tool.ResponsesToolMCP.Authorization != nil {
		server.AuthorizationToken = tool.ResponsesToolMCP.Authorization
	}

	// Build toolset tool (references server by name)
	toolset := &AnthropicMCPToolsetTool{
		Type:          "mcp_toolset",
		MCPServerName: tool.ResponsesToolMCP.ServerLabel,
		CacheControl:  tool.CacheControl,
	}

	// Convert allowed tools to per-tool configs
	if tool.ResponsesToolMCP.AllowedTools != nil {
		// Allowlist pattern: default disabled, specific tools enabled
		toolset.DefaultConfig = &AnthropicMCPToolsetConfig{Enabled: new(false)}
		if len(tool.ResponsesToolMCP.AllowedTools.ToolNames) > 0 {
			toolset.Configs = make(map[string]*AnthropicMCPToolsetConfig, len(tool.ResponsesToolMCP.AllowedTools.ToolNames))
			for _, toolName := range tool.ResponsesToolMCP.AllowedTools.ToolNames {
				toolset.Configs[toolName] = &AnthropicMCPToolsetConfig{Enabled: schemas.Ptr(true)}
			}
		}
	}

	return server, toolset
}

// convertBifrostMCPToolToAnthropicServer converts a Bifrost MCP tool to the deprecated mcp-client-2025-04-04 format.
// Kept for backward compatibility.
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

	// Break the JSON into chunks on rune boundaries — slicing by bytes would
	// split a multi-byte UTF-8 rune across two deltas, emitting invalid UTF-8 on
	// the wire (the SDK's SSE decoder rejects it). Concatenating the rune chunks
	// still reconstructs the exact JSON for parsing at content_block_stop.
	runes := []rune(argumentsJSON)
	for i := 0; i < len(runes); i += chunkSize {
		end := min(i+chunkSize, len(runes))

		chunk := string(runes[i:end])
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
