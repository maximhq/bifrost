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

// TestResponsesToolMessageOutputStructUnmarshalObjectOutput: a function_call_output
// whose output is a plain JSON object (Gemini-shaped history replayed through
// OpenAI) is neither a string, a content-part list, nor a computer screenshot.
// It used to be decoded as ResponsesComputerToolCallOutputData and re-emitted as
// {"type":""}; it must instead become its JSON text, which OpenAI accepts.
func TestResponsesToolMessageOutputStructUnmarshalObjectOutput(t *testing.T) {
	var out ResponsesToolMessageOutputStruct
	if err := Unmarshal([]byte(`{"temperature": 72, "unit": "F"}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ResponsesComputerToolCallOutput != nil {
		t.Fatalf("plain object must not decode as a computer screenshot: %+v", out.ResponsesComputerToolCallOutput)
	}
	if out.ResponsesToolCallOutputStr == nil {
		t.Fatal("plain object must decode to its JSON text")
	}
	var back map[string]any
	if err := Unmarshal([]byte(*out.ResponsesToolCallOutputStr), &back); err != nil || back["temperature"] != float64(72) || back["unit"] != "F" {
		t.Fatalf("stringified output should round-trip the object, got %q (err=%v)", *out.ResponsesToolCallOutputStr, err)
	}
	wire, err := MarshalSorted(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var s string
	if err := Unmarshal(wire, &s); err != nil {
		t.Fatalf("wire form must be a JSON string, got %s", wire)
	}
}

// Computer screenshots keep decoding into the typed variant, with or without a
// file reference, so the object fallback above never captures them.
func TestResponsesToolMessageOutputStructUnmarshalComputerScreenshot(t *testing.T) {
	for _, raw := range []string{
		`{"type":"computer_screenshot","image_url":"data:image/png;base64,iVBORw0KGgo="}`,
		`{"type":"computer_screenshot","file_id":"file_123"}`,
		`{"type":"computer_screenshot"}`,
	} {
		var out ResponsesToolMessageOutputStruct
		if err := Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if out.ResponsesComputerToolCallOutput == nil || out.ResponsesComputerToolCallOutput.Type != "computer_screenshot" {
			t.Fatalf("%s must decode as a computer screenshot, got %+v", raw, out)
		}
		if out.ResponsesToolCallOutputStr != nil {
			t.Fatalf("%s must not also populate the string variant", raw)
		}
	}
}
