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

// output is a required array in the Responses API. A response with no items (an empty
// Claude turn, or the response.created / response.in_progress lifecycle events providers
// build without items) must send [] rather than null: clients iterate it on terminal events.
func TestBifrostResponsesResponseNilOutputMarshalsAsEmptyArray(t *testing.T) {
	outputOf := func(t *testing.T, data []byte, path ...string) json.RawMessage {
		t.Helper()
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		for _, key := range path {
			if err := json.Unmarshal(obj[key], &obj); err != nil {
				t.Fatalf("unmarshal %q of %s: %v", key, data, err)
			}
		}
		return obj["output"]
	}

	response := BifrostResponsesResponse{ID: new("msg_1"), Object: "response", CreatedAt: 1700000000, Model: "claude-haiku-4-5"}
	marshalers := map[string]func(any) ([]byte, error){"sonic": Marshal, "encoding/json": json.Marshal}
	for name, marshal := range marshalers {
		data, err := marshal(response)
		if err != nil {
			t.Fatalf("%s: marshal response: %v", name, err)
		}
		if got := string(outputOf(t, data)); got != "[]" {
			t.Fatalf("%s: response output = %s, want []: %s", name, got, data)
		}
		for _, eventType := range []ResponsesStreamResponseType{ResponsesStreamResponseTypeCreated, ResponsesStreamResponseTypeInProgress, ResponsesStreamResponseTypeCompleted} {
			data, err := marshal(BifrostResponsesStreamResponse{Type: eventType, Response: &response})
			if err != nil {
				t.Fatalf("%s: marshal %s: %v", name, eventType, err)
			}
			if got := string(outputOf(t, data, "response")); got != "[]" {
				t.Fatalf("%s: %s response.output = %s, want []: %s", name, eventType, got, data)
			}
		}
	}
	if response.Output != nil {
		t.Fatalf("marshal mutated the caller's response: Output = %#v", response.Output)
	}

	populated := BifrostResponsesResponse{Output: []ResponsesMessage{{ID: new("msg_1"), Type: new(ResponsesMessageTypeMessage)}}}
	data, err := Marshal(populated)
	if err != nil {
		t.Fatalf("marshal populated response: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(outputOf(t, data), &items); err != nil || len(items) != 1 || items[0]["id"] != "msg_1" {
		t.Fatalf("populated output changed: %s (err %v)", data, err)
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
