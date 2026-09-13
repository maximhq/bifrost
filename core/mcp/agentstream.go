package mcp

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ChatStreamStarter opens one streaming iteration of the agent loop. It is the streaming twin of the
// makeReq callback the blocking loop takes, and routes through the caller's full request pipeline
// so every iteration is logged, priced and governed like any other request.
type ChatStreamStarter func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError)

// chatIteration is the outcome of relaying one iteration's chunks.
type chatIteration struct {
	// held carries the chunks withheld from the client: everything from the first tool-call
	// delta onward, plus any usage-bearing chunk so totals can be merged before delivery.
	held []*schemas.BifrostStreamChunk
	// aborted means the client is gone; nothing further may be sent.
	aborted bool
	// failed means the iteration carried an error, so the hold is released rather than acted on.
	failed bool
}

// CheckAndExecuteAgentForChatStream wraps a chat stream so tool calls Bifrost owns are executed
// without the client ever seeing them, with the model's follow-up continuing in the same stream.
// A stream carrying no auto-executable tool is returned untouched, so the common case keeps the
// provider's channel and pays nothing.
//
// The relay forwards chunks as they arrive until the first tool-call delta and then holds
// everything. A turn's tool calls are its tail, so the hold stays small and ordering is exact.
// When the iteration's channel closes the held calls are reassembled and either released to the
// client, or dropped and executed so the next iteration can continue the same stream.
func (m *MCPManager) CheckAndExecuteAgentForChatStream(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostChatRequest,
	stream chan *schemas.BifrostStreamChunk,
	startStream ChatStreamStarter,
) chan *schemas.BifrostStreamChunk {
	if m.toolsManager == nil {
		return stream
	}
	return m.toolsManager.ExecuteAgentForChatStream(ctx, req, stream, startStream, m.executeToolForAgent)
}

// ExecuteAgentForChatStream is the streaming twin of ExecuteAgentForChatRequest. It returns the
// stream unchanged unless the request carries a tool this manager would execute itself.
func (t *ToolsManager) ExecuteAgentForChatStream(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostChatRequest,
	stream chan *schemas.BifrostStreamChunk,
	startStream ChatStreamStarter,
	executeTool MCPToolExecutor,
) chan *schemas.BifrostStreamChunk {
	if ctx == nil || req == nil || stream == nil || startStream == nil || executeTool == nil {
		return stream
	}
	if !t.hasAutoExecutableTools(req) {
		return stream
	}
	out := make(chan *schemas.BifrostStreamChunk)
	go t.relayChatStream(ctx, req, stream, startStream, executeTool, out)
	return out
}

// relayChatStream owns the client-facing channel for the whole turn, across every iteration.
func (t *ToolsManager) relayChatStream(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostChatRequest,
	stream chan *schemas.BifrostStreamChunk,
	startStream ChatStreamStarter,
	executeTool MCPToolExecutor,
	out chan *schemas.BifrostStreamChunk,
) {
	defer close(out)

	conversation := append([]schemas.ChatMessage(nil), req.Input...)
	maxDepth := int(t.maxAgentDepth.Load())
	if maxDepth <= 0 {
		maxDepth = schemas.DefaultMaxAgentDepth
	}

	var carriedUsage *schemas.BifrostLLMUsage
	current := stream

	for depth := 1; ; depth++ {
		iteration := pumpChatIteration(ctx, current, out, depth > 1)
		if iteration.aborted {
			return
		}
		if len(iteration.held) == 0 {
			return
		}

		var toolCalls []schemas.ChatAssistantMessageToolCall
		if !iteration.failed {
			toolCalls = reassembleChatToolCalls(iteration.held)
		}
		autoExecutable, pending := t.agentModeExecutor.partitionToolCalls(ctx, toolCalls, t.clientManager)

		// A plugin holding this stream behind the pause gate has already decided what the client
		// may see, so running a tool underneath that decision could act on a blocked response.
		gated, _ := ctx.Value(schemas.BifrostContextKeyStreamGated).(bool)
		if iteration.failed || len(autoExecutable) == 0 || len(pending) > 0 || depth > maxDepth || gated {
			flushChatHold(ctx, out, iteration.held, carriedUsage)
			return
		}

		carriedUsage = schemas.MergeBifrostLLMUsage(carriedUsage, chatHoldUsage(iteration.held))

		results, execErr := t.agentModeExecutor.executeToolCallsInParallel(ctx, autoExecutable, executeTool)
		if execErr != nil {
			sendStreamChunk(ctx, out, &schemas.BifrostStreamChunk{BifrostError: execErr})
			return
		}

		conversation = append(conversation, assistantToolCallMessage(autoExecutable))
		for _, result := range results {
			if result != nil {
				conversation = append(conversation, *result)
			}
		}

		if t.fetchNewRequestIDFunc != nil {
			if newID := t.fetchNewRequestIDFunc(ctx); newID != "" {
				ctx.SetValue(schemas.BifrostContextKeyRequestID, newID)
			}
		}

		next, startErr := startStream(ctx, &schemas.BifrostChatRequest{
			Provider:  req.Provider,
			Model:     req.Model,
			Fallbacks: req.Fallbacks,
			Params:    req.Params,
			Input:     conversation,
		})
		if startErr != nil {
			sendStreamChunk(ctx, out, &schemas.BifrostStreamChunk{BifrostError: startErr})
			return
		}
		current = next
	}
}

// pumpChatIteration forwards one iteration's chunks until its channel closes, holding everything from the
// first tool-call delta onward so the caller can decide whether the client should ever see it.
// suppressRole drops the opening role-only delta of a later iteration, which would otherwise read as
// a second assistant message inside one response.
func pumpChatIteration(
	ctx *schemas.BifrostContext,
	in chan *schemas.BifrostStreamChunk,
	out chan *schemas.BifrostStreamChunk,
	suppressRole bool,
) chatIteration {
	iteration := chatIteration{}
	holding := false
	for chunk := range in {
		if !holding && (chatChunkHasToolCall(chunk) || chatChunkHasUsage(chunk)) {
			holding = true
		}
		if holding {
			if chunk != nil && chunk.BifrostError != nil {
				iteration.failed = true
			}
			iteration.held = append(iteration.held, chunk)
			continue
		}
		if suppressRole && chatChunkIsRoleOnly(chunk) {
			continue
		}
		if !sendStreamChunk(ctx, out, chunk) {
			iteration.aborted = true
			// Release the producer now that nothing will read its remaining chunks.
			go func() {
				for range in {
				}
			}()
			return iteration
		}
	}
	return iteration
}

// flushChatHold releases held chunks to the client, merging usage carried from earlier iterations
// into the first chunk that reports usage so the turn's totals stay whole.
func flushChatHold(
	ctx *schemas.BifrostContext,
	out chan *schemas.BifrostStreamChunk,
	held []*schemas.BifrostStreamChunk,
	carriedUsage *schemas.BifrostLLMUsage,
) {
	merged := carriedUsage != nil
	for _, chunk := range held {
		if merged && chatChunkHasUsage(chunk) {
			chunk = withMergedChatUsage(chunk, carriedUsage)
			merged = false
		}
		if !sendStreamChunk(ctx, out, chunk) {
			return
		}
	}
}

// reassembleChatToolCalls merges the tool-call deltas held for one iteration into whole calls. Chat
// streams split a call across chunks: the first carries the id and name, later ones carry
// argument fragments, all tied together by index.
func reassembleChatToolCalls(held []*schemas.BifrostStreamChunk) []schemas.ChatAssistantMessageToolCall {
	byIndex := make(map[uint16]*schemas.ChatAssistantMessageToolCall)
	order := make([]uint16, 0, len(held))
	for _, chunk := range held {
		if chunk == nil || chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
				continue
			}
			for _, delta := range choice.ChatStreamResponseChoice.Delta.ToolCalls {
				call, seen := byIndex[delta.Index]
				if !seen {
					started := delta
					started.Function.Arguments = ""
					byIndex[delta.Index] = &started
					order = append(order, delta.Index)
					call = &started
				}
				if delta.ID != nil {
					call.ID = delta.ID
				}
				if delta.Type != nil {
					call.Type = delta.Type
				}
				if delta.Function.Name != nil {
					call.Function.Name = delta.Function.Name
				}
				call.Function.Arguments += delta.Function.Arguments
			}
		}
	}
	calls := make([]schemas.ChatAssistantMessageToolCall, 0, len(order))
	for _, index := range order {
		calls = append(calls, *byIndex[index])
	}
	return calls
}

// hasAutoExecutableTools reports whether the request carries any tool Bifrost would run itself.
// Streams without one skip the relay and keep the provider's channel untouched.
func (t *ToolsManager) hasAutoExecutableTools(req *schemas.BifrostChatRequest) bool {
	if req == nil || req.Params == nil {
		return false
	}
	for _, tool := range req.Params.Tools {
		if tool.Function == nil || tool.Function.Name == "" {
			continue
		}
		name := tool.Function.Name
		if name == ToolTypeListToolFiles || name == ToolTypeReadToolFile ||
			name == ToolTypeGetToolDocs || name == ToolTypeExecuteToolCode {
			return true
		}
		if client := t.clientManager.GetClientForTool(name); client != nil && canAutoExecuteTool(name, client.ExecutionConfig) {
			return true
		}
	}
	return false
}

// assistantToolCallMessage rebuilds the assistant turn that requested the executed tools, so the
// next iteration sees the same history the blocking loop would have produced.
func assistantToolCallMessage(calls []schemas.ChatAssistantMessageToolCall) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role:                 schemas.ChatMessageRoleAssistant,
		ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: calls},
	}
}

// chatHoldUsage returns the usage reported by a held iteration, if any.
func chatHoldUsage(held []*schemas.BifrostStreamChunk) *schemas.BifrostLLMUsage {
	for _, chunk := range held {
		if chatChunkHasUsage(chunk) {
			return chunk.BifrostChatResponse.Usage
		}
	}
	return nil
}

// withMergedChatUsage copies chunk with carried usage merged in, leaving the provider's chunk
// untouched for anything that already observed it.
func withMergedChatUsage(chunk *schemas.BifrostStreamChunk, carried *schemas.BifrostLLMUsage) *schemas.BifrostStreamChunk {
	response := *chunk.BifrostChatResponse
	response.Usage = schemas.MergeBifrostLLMUsage(carried, response.Usage)
	copied := *chunk
	copied.BifrostChatResponse = &response
	return &copied
}

func chatChunkHasToolCall(chunk *schemas.BifrostStreamChunk) bool {
	if chunk == nil || chunk.BifrostChatResponse == nil {
		return false
	}
	for _, choice := range chunk.BifrostChatResponse.Choices {
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil &&
			len(choice.ChatStreamResponseChoice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func chatChunkHasUsage(chunk *schemas.BifrostStreamChunk) bool {
	return chunk != nil && chunk.BifrostChatResponse != nil && chunk.BifrostChatResponse.Usage != nil
}

// chatChunkIsRoleOnly reports whether a chunk carries nothing but the opening assistant role.
func chatChunkIsRoleOnly(chunk *schemas.BifrostStreamChunk) bool {
	if chunk == nil || chunk.BifrostChatResponse == nil || chunk.BifrostError != nil {
		return false
	}
	if chunk.BifrostChatResponse.Usage != nil || len(chunk.BifrostChatResponse.Choices) == 0 {
		return false
	}
	for _, choice := range chunk.BifrostChatResponse.Choices {
		if choice.FinishReason != nil {
			return false
		}
		delta := choice.ChatStreamResponseChoice
		if delta == nil || delta.Delta == nil || delta.Delta.Role == nil {
			return false
		}
		if delta.Delta.Content != nil || delta.Delta.Refusal != nil || delta.Delta.Reasoning != nil ||
			len(delta.Delta.ToolCalls) > 0 || len(delta.Delta.Annotations) > 0 {
			return false
		}
	}
	return true
}

func sendStreamChunk(ctx *schemas.BifrostContext, out chan *schemas.BifrostStreamChunk, chunk *schemas.BifrostStreamChunk) bool {
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}
