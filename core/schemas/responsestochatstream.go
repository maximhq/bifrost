package schemas

import (
	"bytes"
	"crypto/sha256"
	"hash"
)

// ResponsesToChatStreamState correlates tool events within one Responses stream.
// It must not be shared between streams.
type ResponsesToChatStreamState struct {
	byItemID      map[string]*responsesChatToolCall
	byOutputIndex map[int]*responsesChatToolCall
	current       *responsesChatToolCall
	nextIndex     uint32
}

type responsesChatToolCall struct {
	index        uint16
	itemID       string
	hash         hash.Hash
	argumentSize int
	finished     bool
}

// Convert maps a Responses event to a Chat Completions chunk while retaining
// tool-call indexes and already-emitted argument fragments for this stream.
func (state *ResponsesToChatStreamState) Convert(event *BifrostResponsesStreamResponse) *BifrostChatResponse {
	response := event.ToBifrostChatResponse()
	if response == nil {
		return nil
	}
	var call *responsesChatToolCall
	switch event.Type {
	case ResponsesStreamResponseTypeOutputItemAdded:
		if !isResponsesFunctionCall(event.Item) {
			return response
		}
		if responsesStreamItemID(event) != "" || event.OutputIndex != nil {
			call = state.find(event)
		}
		if call != nil {
			response.Choices[0].Delta.ToolCalls = nil
			return response
		}
		call = state.add(event)
		if call == nil {
			response.Choices[0].Delta.ToolCalls = nil
			return response
		}
		tool := &response.Choices[0].Delta.ToolCalls[0]
		tool.Index = call.index
		if event.Item.Arguments != nil {
			tool.Function.Arguments = *event.Item.Arguments
			call.record(tool.Function.Arguments)
		}
	case ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		call = state.find(event)
		if call == nil || call.finished || event.Delta == nil {
			response.Choices[0].Delta.ToolCalls = nil
			return response
		}
		response.Choices[0].Delta.ToolCalls[0].Index = call.index
		call.record(*event.Delta)
	case ResponsesStreamResponseTypeFunctionCallArgumentsDone:
		call = state.find(event)
		if call != nil && event.Arguments != nil {
			state.emitTerminal(response, call, *event.Arguments)
		}
	case ResponsesStreamResponseTypeOutputItemDone:
		if !isResponsesFunctionCall(event.Item) {
			return response
		}
		call = state.find(event)
		if call == nil {
			call = state.add(event)
			if call == nil {
				return response
			}
			functionType := "function"
			response.Choices[0].Delta.ToolCalls = []ChatAssistantMessageToolCall{{
				Index: call.index, ID: event.Item.CallID, Type: &functionType,
				Function: ChatAssistantMessageToolCallFunction{Name: event.Item.Name},
			}}
			if event.Item.Arguments != nil {
				response.Choices[0].Delta.ToolCalls[0].Function.Arguments = *event.Item.Arguments
				call.finished = true
			}
			return response
		}
		if event.Item.Arguments != nil {
			state.emitTerminal(response, call, *event.Item.Arguments)
		}
	}
	return response
}

// isResponsesFunctionCall identifies output items that contain function-call data.
func isResponsesFunctionCall(item *ResponsesMessage) bool {
	return item != nil && item.Type != nil && *item.Type == ResponsesMessageTypeFunctionCall && item.ResponsesToolMessage != nil
}

// find correlates an event by item ID or output index, using the current call only when neither is present.
func (state *ResponsesToChatStreamState) find(event *BifrostResponsesStreamResponse) *responsesChatToolCall {
	itemID := responsesStreamItemID(event)
	if itemID != "" {
		if call := state.byItemID[itemID]; call != nil {
			return call
		}
	}
	if event.OutputIndex != nil {
		call := state.byOutputIndex[*event.OutputIndex]
		if call == nil || (itemID != "" && call.itemID != "" && call.itemID != itemID) {
			return nil
		}
		if itemID != "" && call.itemID == "" {
			call.itemID = itemID
			state.byItemID[itemID] = call
		}
		return call
	}
	if itemID != "" {
		return nil
	}
	return state.current
}

// add assigns the next Chat index and records the Responses identifiers for a new call.
func (state *ResponsesToChatStreamState) add(event *BifrostResponsesStreamResponse) *responsesChatToolCall {
	if state.nextIndex > uint32(^uint16(0)) {
		return nil
	}
	if state.byItemID == nil {
		state.byItemID = make(map[string]*responsesChatToolCall)
		state.byOutputIndex = make(map[int]*responsesChatToolCall)
	}
	call := &responsesChatToolCall{index: uint16(state.nextIndex), itemID: responsesStreamItemID(event)}
	state.nextIndex++
	if call.itemID != "" {
		state.byItemID[call.itemID] = call
	}
	if event.OutputIndex != nil {
		state.byOutputIndex[*event.OutputIndex] = call
	}
	state.current = call
	return call
}

// responsesStreamItemID returns the item identifier from either event representation.
func responsesStreamItemID(event *BifrostResponsesStreamResponse) string {
	if event.ItemID != nil {
		return *event.ItemID
	}
	if event.Item != nil && event.Item.ID != nil {
		return *event.Item.ID
	}
	return ""
}

// record tracks emitted argument bytes without retaining their contents.
func (call *responsesChatToolCall) record(fragment string) {
	if fragment == "" {
		return
	}
	if call.hash == nil {
		call.hash = sha256.New()
	}
	// hash.Hash.Write never returns an error.
	_, _ = call.hash.Write([]byte(fragment))
	call.argumentSize += len(fragment)
}

// emitTerminal emits only the unseen suffix when the final arguments extend the emitted prefix.
func (state *ResponsesToChatStreamState) emitTerminal(response *BifrostChatResponse, call *responsesChatToolCall, arguments string) {
	if call.finished || len(arguments) < call.argumentSize {
		return
	}
	if call.hash != nil {
		prefix := sha256.Sum256([]byte(arguments[:call.argumentSize]))
		var digest [sha256.Size]byte
		if !bytes.Equal(call.hash.Sum(digest[:0]), prefix[:]) {
			return
		}
	}
	call.finished = true
	call.hash = nil
	if suffix := arguments[call.argumentSize:]; suffix != "" {
		response.Choices[0].Delta.ToolCalls = []ChatAssistantMessageToolCall{{
			Index: call.index, Function: ChatAssistantMessageToolCallFunction{Arguments: suffix},
		}}
	}
}
