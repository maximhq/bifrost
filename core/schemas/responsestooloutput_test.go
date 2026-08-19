package schemas

import (
	"testing"
)

// TestResponsesToolMessageOutputStructMarshalEmpty verifies that an output
// struct with all variants nil serializes as an empty string instead of
// erroring. An error here would abort marshaling of any enclosing structure
// (conversation histories, log rows), silently dropping data downstream.
func TestResponsesToolMessageOutputStructMarshalEmpty(t *testing.T) {
	data, err := MarshalSorted(ResponsesToolMessageOutputStruct{})
	if err != nil {
		t.Fatalf("empty output struct must marshal, got error: %v", err)
	}
	if string(data) != `""` {
		t.Fatalf("empty output struct should marshal as empty string, got %s", data)
	}
}

// TestResponsesToolMessageOutputStructRoundTripEmpty verifies the marshaled
// empty output unmarshals back into the string variant.
func TestResponsesToolMessageOutputStructRoundTripEmpty(t *testing.T) {
	data, err := MarshalSorted(ResponsesToolMessageOutputStruct{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ResponsesToolMessageOutputStruct
	if err := Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ResponsesToolCallOutputStr == nil || *out.ResponsesToolCallOutputStr != "" {
		t.Fatalf("round trip should yield empty output string, got %+v", out)
	}
}

// TestResponsesMessageMarshalWithEmptyToolOutput verifies a full message
// containing an empty tool output (the shape produced by an Anthropic
// tool_result with content: []) serializes cleanly inside a slice, matching
// how conversation histories are stored.
func TestResponsesMessageMarshalWithEmptyToolOutput(t *testing.T) {
	msgs := []ResponsesMessage{
		{
			Type:   Ptr(ResponsesMessageTypeFunctionCallOutput),
			Status: Ptr("completed"),
			ResponsesToolMessage: &ResponsesToolMessage{
				CallID: Ptr("toolu_empty"),
				Output: &ResponsesToolMessageOutputStruct{},
			},
		},
	}
	if _, err := MarshalSorted(msgs); err != nil {
		t.Fatalf("history containing empty tool output must marshal, got: %v", err)
	}
}
