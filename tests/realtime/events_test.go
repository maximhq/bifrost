package realtime

// Realtime API events, one builder per event type, named after it. Shapes follow OpenAI's GA
// Realtime API; only the fields the gateway reads or a test asserts on are filled in.

type event map[string]any

// tokens is a response's usage; the fake bills everything as text tokens so costs are exact.
type tokens struct{ input, output int }

// Client events.

func sessionUpdate(session map[string]any) event {
	return event{"type": "session.update", "session": session}
}

func userMessage(text string) event {
	return event{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}}
}

func functionCallOutput(callID, output string) event {
	return event{"type": "conversation.item.create", "item": map[string]any{
		"type": "function_call_output", "call_id": callID, "output": output,
	}}
}

func responseCreate() event { return event{"type": "response.create"} }

// inputAudioClear is harmless to send at any point, which makes it a marker: a test sends it after
// something the gateway must not forward, and expects it to be the next frame upstream.
func inputAudioClear() event { return event{"type": "input_audio_buffer.clear"} }

// Provider events.

func sessionCreated(sessionID, model string) event {
	return event{"type": "session.created", "session": map[string]any{
		"id": sessionID, "object": "realtime.session", "type": "realtime", "model": model,
	}}
}

func responseCreated(responseID string) event {
	return event{"type": "response.created", "response": map[string]any{
		"id": responseID, "object": "realtime.response", "status": "in_progress", "output": []any{},
	}}
}

func outputTextDelta(responseID, itemID, delta string) event {
	return event{"type": "response.output_text.delta", "response_id": responseID, "item_id": itemID,
		"output_index": 0, "content_index": 0, "delta": delta}
}

func responseDone(responseID, status string, usage tokens, output ...map[string]any) event {
	if output == nil {
		output = []map[string]any{}
	}
	return event{"type": "response.done", "response": map[string]any{
		"id": responseID, "object": "realtime.response", "status": status, "output": output,
		"usage": map[string]any{
			"total_tokens":         usage.input + usage.output,
			"input_tokens":         usage.input,
			"output_tokens":        usage.output,
			"input_token_details":  map[string]any{"text_tokens": usage.input, "audio_tokens": 0, "cached_tokens": 0},
			"output_token_details": map[string]any{"text_tokens": usage.output, "audio_tokens": 0},
		},
	}}
}

func inputAudioCommitted(itemID string) event {
	return event{"type": "input_audio_buffer.committed", "item_id": itemID}
}

func transcriptionCompleted(itemID, transcript string) event {
	return event{"type": "conversation.item.input_audio_transcription.completed", "item_id": itemID,
		"content_index": 0, "transcript": transcript}
}

func errorEvent(errorType, code, message string) event {
	return event{"type": "error", "error": map[string]any{"type": errorType, "code": code, "message": message}}
}

// Output items carried by response.done.

func messageItem(itemID, text string) map[string]any {
	return map[string]any{"id": itemID, "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text}}}
}

func functionCallItem(itemID, callID, name, arguments string) map[string]any {
	return map[string]any{"id": itemID, "type": "function_call", "status": "completed",
		"call_id": callID, "name": name, "arguments": arguments}
}

// Whole provider responses, as the sequence of events OpenAI streams for one.

// textReply is a completed response that says text.
func textReply(responseID, text string, usage tokens) []event {
	itemID := "item_" + responseID
	return []event{
		responseCreated(responseID),
		outputTextDelta(responseID, itemID, text),
		responseDone(responseID, "completed", usage, messageItem(itemID, text)),
	}
}

// toolCallReply is a completed response that calls one function.
func toolCallReply(responseID, callID, name, arguments string, usage tokens) []event {
	return []event{
		responseCreated(responseID),
		responseDone(responseID, "completed", usage, functionCallItem("item_"+responseID, callID, name, arguments)),
	}
}
