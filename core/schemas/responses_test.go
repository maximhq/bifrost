package schemas

import (
	"strings"
	"testing"
)

func TestBifrostResponsesStreamResponsePreservesOpenAIStreamMetadata(t *testing.T) {
	raw := []byte(`{"type":"response.reasoning_summary_text.delta","delta":"thinking","item_id":"rs_123","obfuscation":"opaque","output_index":0,"sequence_number":4,"summary_index":0}`)

	var resp BifrostResponsesStreamResponse
	if err := Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response stream chunk: %v", err)
	}

	if resp.SummaryIndex == nil || *resp.SummaryIndex != 0 {
		t.Fatalf("expected summary_index to survive unmarshal, got %#v", resp.SummaryIndex)
	}
	if resp.Obfuscation == nil || *resp.Obfuscation != "opaque" {
		t.Fatalf("expected obfuscation to survive unmarshal, got %#v", resp.Obfuscation)
	}

	defaulted := resp.WithDefaults()
	if defaulted.SummaryIndex == nil || *defaulted.SummaryIndex != 0 {
		t.Fatalf("expected summary_index to survive WithDefaults, got %#v", defaulted.SummaryIndex)
	}
	if defaulted.Obfuscation == nil || *defaulted.Obfuscation != "opaque" {
		t.Fatalf("expected obfuscation to survive WithDefaults, got %#v", defaulted.Obfuscation)
	}

	encoded, err := MarshalSorted(defaulted)
	if err != nil {
		t.Fatalf("marshal defaulted response stream chunk: %v", err)
	}
	if !strings.Contains(string(encoded), `"summary_index":0`) {
		t.Fatalf("expected encoded chunk to contain summary_index, got %s", encoded)
	}
	if !strings.Contains(string(encoded), `"obfuscation":"opaque"`) {
		t.Fatalf("expected encoded chunk to contain obfuscation, got %s", encoded)
	}

	encodedChunk, err := MarshalSorted(BifrostStreamChunk{BifrostResponsesStreamResponse: defaulted})
	if err != nil {
		t.Fatalf("marshal response stream chunk wrapper: %v", err)
	}
	if !strings.Contains(string(encodedChunk), `"summary_index":0`) {
		t.Fatalf("expected encoded stream chunk to contain summary_index, got %s", encodedChunk)
	}
	if !strings.Contains(string(encodedChunk), `"obfuscation":"opaque"`) {
		t.Fatalf("expected encoded stream chunk to contain obfuscation, got %s", encodedChunk)
	}
}

func TestBifrostResponsesResponseUnmarshalTimestamps(t *testing.T) {
	t.Run("float created_at is truncated to int", func(t *testing.T) {
		raw := []byte(`{"object":"response","model":"m","created_at":1716000000.5,"output":[]}`)
		var r BifrostResponsesResponse
		if err := Unmarshal(raw, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.CreatedAt != 1716000000 {
			t.Fatalf("expected CreatedAt 1716000000, got %d", r.CreatedAt)
		}
		if r.CompletedAt != nil {
			t.Fatalf("expected CompletedAt nil, got %v", r.CompletedAt)
		}
	})

	t.Run("integer created_at is preserved", func(t *testing.T) {
		raw := []byte(`{"object":"response","model":"m","created_at":1716000000,"output":[]}`)
		var r BifrostResponsesResponse
		if err := Unmarshal(raw, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.CreatedAt != 1716000000 {
			t.Fatalf("expected CreatedAt 1716000000, got %d", r.CreatedAt)
		}
	})

	t.Run("null completed_at leaves field nil", func(t *testing.T) {
		raw := []byte(`{"object":"response","model":"m","created_at":1716000000,"completed_at":null,"output":[]}`)
		var r BifrostResponsesResponse
		if err := Unmarshal(raw, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.CompletedAt != nil {
			t.Fatalf("expected CompletedAt nil, got %v", r.CompletedAt)
		}
	})

	t.Run("absent completed_at leaves field nil", func(t *testing.T) {
		raw := []byte(`{"object":"response","model":"m","created_at":1716000000,"output":[]}`)
		var r BifrostResponsesResponse
		if err := Unmarshal(raw, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.CompletedAt != nil {
			t.Fatalf("expected CompletedAt nil, got %v", r.CompletedAt)
		}
	})

	t.Run("float completed_at is truncated to int", func(t *testing.T) {
		raw := []byte(`{"object":"response","model":"m","created_at":1716000000,"completed_at":1716000099.9,"output":[]}`)
		var r BifrostResponsesResponse
		if err := Unmarshal(raw, &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.CompletedAt == nil || *r.CompletedAt != 1716000099 {
			t.Fatalf("expected CompletedAt 1716000099, got %v", r.CompletedAt)
		}
	})
}

// TestResponsesMessageContentEmptyMarshalsToEmptyString verifies that empty
// content serializes as "" rather than null, since the OpenAI Responses API
// rejects null content.
func TestResponsesMessageContentEmptyMarshalsToEmptyString(t *testing.T) {
	encoded, err := MarshalSorted(ResponsesMessageContent{})
	if err != nil {
		t.Fatalf("marshal empty content: %v", err)
	}
	if string(encoded) != `""` {
		t.Fatalf("expected empty content to marshal to \"\", got %s", encoded)
	}

	str := "hello"
	encodedStr, err := MarshalSorted(ResponsesMessageContent{ContentStr: &str})
	if err != nil {
		t.Fatalf("marshal string content: %v", err)
	}
	if string(encodedStr) != `"hello"` {
		t.Fatalf("expected string content to round-trip, got %s", encodedStr)
	}

	role := ResponsesInputMessageRoleUser
	msg := ResponsesMessage{Role: &role, Content: &ResponsesMessageContent{}}
	encodedMsg, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal message with empty content: %v", err)
	}
	if strings.Contains(string(encodedMsg), `"content":null`) {
		t.Fatalf("expected no null content in message, got %s", encodedMsg)
	}
	if !strings.Contains(string(encodedMsg), `"content":""`) {
		t.Fatalf("expected empty-string content in message, got %s", encodedMsg)
	}
}

// TestResponsesMessageToolCallArguments verifies that function/tool-call
// `arguments` parse whether the provider serializes them as a JSON string
// (`function_call` items) or as a JSON object (`tool_search_call` items, emitted
// when the request enables OpenAI's `tool_search` tool — captured live from
// api.openai.com). The object form previously failed with "Mismatch type string
// with value object", silently dropping the item mid-stream and hanging the
// client.
func TestResponsesMessageToolCallArguments(t *testing.T) {
	t.Run("string arguments are preserved", func(t *testing.T) {
		raw := []byte(`{"id":"fc_1","type":"function_call","status":"completed","name":"grafana","call_id":"call_123","arguments":"{\"query\":\"observability\"}"}`)

		var msg ResponsesMessage
		if err := Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal function_call item: %v", err)
		}
		if msg.ResponsesToolMessage == nil || msg.Arguments == nil {
			t.Fatalf("expected arguments to be set, got %#v", msg.ResponsesToolMessage)
		}
		if *msg.Arguments != `{"query":"observability"}` {
			t.Fatalf("expected stringified arguments, got %q", *msg.Arguments)
		}
		if msg.CallID == nil || *msg.CallID != "call_123" {
			t.Fatalf("expected call_id to survive, got %#v", msg.CallID)
		}
		if msg.Name == nil || *msg.Name != "grafana" {
			t.Fatalf("expected name to survive, got %#v", msg.Name)
		}
	})

	t.Run("object arguments are normalized to stringified json", func(t *testing.T) {
		raw := []byte(`{"id":"fc_1","type":"function_call","status":"completed","name":"grafana","call_id":"call_123","arguments":{"query":"observability"}}`)

		var msg ResponsesMessage
		if err := Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal function_call item with object arguments: %v", err)
		}
		if msg.ResponsesToolMessage == nil || msg.Arguments == nil {
			t.Fatalf("expected arguments to be set, got %#v", msg.ResponsesToolMessage)
		}
		if *msg.Arguments != `{"query":"observability"}` {
			t.Fatalf("expected object arguments to normalize to stringified json, got %q", *msg.Arguments)
		}
		if msg.CallID == nil || *msg.CallID != "call_123" {
			t.Fatalf("expected call_id to survive object-argument decode, got %#v", msg.CallID)
		}

		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal normalized message: %v", err)
		}
		if !strings.Contains(string(encoded), `"arguments":"{\"query\":\"observability\"}"`) {
			t.Fatalf("expected arguments to round-trip as a string, got %s", encoded)
		}
	})

	t.Run("empty object arguments", func(t *testing.T) {
		raw := []byte(`{"id":"fc_1","type":"function_call","name":"grafana","call_id":"call_123","arguments":{}}`)

		var msg ResponsesMessage
		if err := Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal function_call item with empty object arguments: %v", err)
		}
		if msg.Arguments == nil || *msg.Arguments != `{}` {
			t.Fatalf("expected empty object arguments to normalize to %q, got %#v", `{}`, msg.Arguments)
		}
	})

	t.Run("object arguments inside a streamed output_item.done event", func(t *testing.T) {
		raw := []byte(`{"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","name":"grafana","call_id":"call_123","arguments":{"query":"observability"}}}`)

		var resp BifrostResponsesStreamResponse
		if err := Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal output_item.done with object arguments: %v", err)
		}
		if resp.Item == nil || resp.Item.ResponsesToolMessage == nil || resp.Item.Arguments == nil {
			t.Fatalf("expected streamed item arguments to be set, got %#v", resp.Item)
		}
		if *resp.Item.Arguments != `{"query":"observability"}` {
			t.Fatalf("expected streamed object arguments to normalize, got %q", *resp.Item.Arguments)
		}
	})

	// Real tool_search_call frames captured from api.openai.com by replaying
	// Codex's request (which enables the `tool_search` tool). These are the exact
	// frames that triggered the production "Mismatch type string with value
	// object" failure.
	t.Run("real tool_search_call frames from openai", func(t *testing.T) {
		frames := map[string]string{
			"in_progress (empty object)":   `{"type":"response.output_item.added","output_index":1,"sequence_number":4,"item":{"id":"tsc_01429bcd111d3db1016a3abc8e12948191a9efb0edcbd7f68a","type":"tool_search_call","status":"in_progress","arguments":{},"call_id":"call_OYgDGFxcFL8POxRYssDHUsaM","execution":"client"}}`,
			"completed (populated object)": `{"type":"response.output_item.done","output_index":1,"sequence_number":5,"item":{"id":"tsc_01429bcd111d3db1016a3abc8e12948191a9efb0edcbd7f68a","type":"tool_search_call","status":"completed","arguments":{"query":"observability_repro sentry grafana websocket responses","limit":10},"call_id":"call_OYgDGFxcFL8POxRYssDHUsaM","execution":"client"}}`,
		}
		want := map[string]string{
			"in_progress (empty object)":   `{}`,
			"completed (populated object)": `{"query":"observability_repro sentry grafana websocket responses","limit":10}`,
		}
		for name, raw := range frames {
			var resp BifrostResponsesStreamResponse
			if err := Unmarshal([]byte(raw), &resp); err != nil {
				t.Fatalf("[%s] unmarshal tool_search_call frame: %v", name, err)
			}
			if resp.Item == nil || resp.Item.ResponsesToolMessage == nil || resp.Item.Arguments == nil {
				t.Fatalf("[%s] expected tool_search_call arguments to be set, got %#v", name, resp.Item)
			}
			if *resp.Item.Arguments != want[name] {
				t.Fatalf("[%s] expected arguments %q, got %q", name, want[name], *resp.Item.Arguments)
			}
		}
	})
}

func TestResponsesMessagePreservesOpenAIPhase(t *testing.T) {
	raw := []byte(`{"id":"msg_123","type":"message","status":"in_progress","content":[],"phase":"final_answer","role":"assistant"}`)

	var msg ResponsesMessage
	if err := Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal responses message: %v", err)
	}

	if msg.Phase == nil || *msg.Phase != "final_answer" {
		t.Fatalf("expected phase to survive unmarshal, got %#v", msg.Phase)
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal responses message: %v", err)
	}
	if !strings.Contains(string(encoded), `"phase":"final_answer"`) {
		t.Fatalf("expected encoded message to contain phase, got %s", encoded)
	}
}
