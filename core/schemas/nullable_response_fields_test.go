package schemas

import (
	"encoding/json"
	"testing"
)

func TestBifrostChatResponseRequiredNullableFieldsMarshalAsNull(t *testing.T) {
	response := BifrostChatResponse{
		ID:      "chatcmpl-1",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4o",
		Choices: []BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &ChatNonStreamResponseChoice{
					Message: &ChatMessage{
						Role:                 ChatMessageRoleAssistant,
						Content:              nil,
						ChatAssistantMessage: &ChatAssistantMessage{},
					},
				},
			},
		},
	}

	var got map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, response), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	choices := got["choices"].([]any)
	choice := choices[0].(map[string]any)
	if _, ok := choice["finish_reason"]; !ok {
		t.Fatalf("finish_reason key missing from choice: %#v", choice)
	}
	if choice["finish_reason"] != nil {
		t.Fatalf("finish_reason = %#v, want nil", choice["finish_reason"])
	}
	if _, ok := choice["logprobs"]; !ok {
		t.Fatalf("logprobs key missing from choice: %#v", choice)
	}
	if choice["logprobs"] != nil {
		t.Fatalf("logprobs = %#v, want nil", choice["logprobs"])
	}

	message := choice["message"].(map[string]any)
	if _, ok := message["content"]; !ok {
		t.Fatalf("content key missing from message: %#v", message)
	}
	if message["content"] != nil {
		t.Fatalf("content = %#v, want nil", message["content"])
	}
	if _, ok := message["refusal"]; !ok {
		t.Fatalf("refusal key missing from message: %#v", message)
	}
	if message["refusal"] != nil {
		t.Fatalf("refusal = %#v, want nil", message["refusal"])
	}
}

func TestBifrostResponsesResponseRequiredNullableMetadataMarshalsAsNull(t *testing.T) {
	response := BifrostResponsesResponse{
		Object:    "response",
		CreatedAt: 1700000000,
		Model:     "gpt-4o",
		Output:    []ResponsesMessage{},
	}

	var got map[string]any
	if err := json.Unmarshal(mustMarshalJSON(t, response), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, ok := got["metadata"]; !ok {
		t.Fatalf("metadata key missing from response: %#v", got)
	}
	if got["metadata"] != nil {
		t.Fatalf("metadata = %#v, want nil", got["metadata"])
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := MarshalSorted(v)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return data
}
