package warp

// fold assembles a turn out of its events.
//
// Both transports run the same loop and differ only in their sink: SSE writes
// each event as a frame, JSON waits for the end. What they share is this
// reduction - deltas concatenated into an answer, tool calls paired start to end,
// the terminal frame recorded - so it lives in one place rather than being
// maintained twice.
type fold struct {
	response ChatResponse
	answer   []byte
	pending  map[string]ChatToolCall
	sawDone  bool
}

func newFold() *fold {
	return &fold{response: ChatResponse{ToolCalls: []ChatToolCall{}}, pending: map[string]ChatToolCall{}}
}

// apply folds one event in.
func (f *fold) apply(event Event) {
	switch event.Type {
	case EventDelta:
		f.answer = append(f.answer, event.Delta...)
	case EventToolCallStart:
		f.pending[event.ToolID] = ChatToolCall{Name: event.ToolName, Arguments: event.Arguments}
	case EventToolCallEnd:
		call := f.pending[event.ToolID]
		call.Name, call.DurationMs, call.Failed = event.ToolName, event.DurationMs, event.Failed
		f.response.ToolCalls = append(f.response.ToolCalls, call)
		delete(f.pending, event.ToolID)
	case EventDone:
		f.sawDone = true
		f.response.FinishReason, f.response.Iterations, f.response.Usage = event.FinishReason, event.Iterations, event.Usage
		// A done frame that already carries the thread id (one replayed from a
		// stream) keeps it, so a fold over frames equals the fold that made them.
		if event.ConversationID != "" {
			f.response.ConversationID = event.ConversationID
		}
	case EventError:
		f.response.Error = &ChatError{Code: event.Code, Message: event.Message}
	}
}

// result returns the turn as assembled so far.
func (f *fold) result() ChatResponse {
	response := f.response
	response.Answer = string(f.answer)
	return response
}
