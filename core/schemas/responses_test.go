package schemas

import (
	"encoding/json"
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
	// object" failure. tool_search items are preserved verbatim (see
	// rawPreserved), so the item must decode without error and re-encode
	// byte-identically, object-form arguments included.
	t.Run("real tool_search_call frames from openai", func(t *testing.T) {
		items := map[string]string{
			"in_progress (empty object)":   `{"id":"tsc_01429bcd111d3db1016a3abc8e12948191a9efb0edcbd7f68a","type":"tool_search_call","status":"in_progress","arguments":{},"call_id":"call_OYgDGFxcFL8POxRYssDHUsaM","execution":"client"}`,
			"completed (populated object)": `{"id":"tsc_01429bcd111d3db1016a3abc8e12948191a9efb0edcbd7f68a","type":"tool_search_call","status":"completed","arguments":{"query":"observability_repro sentry grafana websocket responses","limit":10},"call_id":"call_OYgDGFxcFL8POxRYssDHUsaM","execution":"client"}`,
		}
		events := map[string]string{
			"in_progress (empty object)":   `{"type":"response.output_item.added","output_index":1,"sequence_number":4,"item":` + items["in_progress (empty object)"] + `}`,
			"completed (populated object)": `{"type":"response.output_item.done","output_index":1,"sequence_number":5,"item":` + items["completed (populated object)"] + `}`,
		}
		for name, raw := range events {
			var resp BifrostResponsesStreamResponse
			if err := Unmarshal([]byte(raw), &resp); err != nil {
				t.Fatalf("[%s] unmarshal tool_search_call frame: %v", name, err)
			}
			if resp.Item == nil || resp.Item.Type == nil || *resp.Item.Type != ResponsesMessageTypeToolSearchCall {
				t.Fatalf("[%s] expected tool_search_call item, got %#v", name, resp.Item)
			}
			encoded, err := MarshalSorted(resp.Item)
			if err != nil {
				t.Fatalf("[%s] marshal preserved tool_search_call item: %v", name, err)
			}
			if string(encoded) != items[name] {
				t.Fatalf("[%s] expected item to round-trip verbatim\nwant: %s\ngot:  %s", name, items[name], encoded)
			}
		}
	})
}

func TestResponsesMessageMarshalsToolSearchArgumentsAsObject(t *testing.T) {
	toolSearchType := ResponsesMessageTypeToolSearchCall
	functionType := ResponsesMessageTypeFunctionCall
	callID := "call_123"

	t.Run("tool_search_call arguments marshal as a JSON object", func(t *testing.T) {
		args := `{"query":"observability logs","limit":10}`
		msg := ResponsesMessage{
			Type:                 &toolSearchType,
			ResponsesToolMessage: &ResponsesToolMessage{CallID: &callID, Arguments: &args},
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal tool_search_call: %v", err)
		}
		if !strings.Contains(string(encoded), `"arguments":{"query":"observability logs","limit":10}`) {
			t.Fatalf("expected object-valued arguments, got %s", encoded)
		}
		if strings.Contains(string(encoded), `"arguments":"`) {
			t.Fatalf("tool_search_call arguments must not be stringified, got %s", encoded)
		}
	})

	t.Run("tool_search_call empty arguments marshal as an empty object", func(t *testing.T) {
		args := `{}`
		msg := ResponsesMessage{
			Type:                 &toolSearchType,
			ResponsesToolMessage: &ResponsesToolMessage{CallID: &callID, Arguments: &args},
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal tool_search_call: %v", err)
		}
		if !strings.Contains(string(encoded), `"arguments":{}`) {
			t.Fatalf("expected empty object arguments, got %s", encoded)
		}
	})

	t.Run("function_call arguments stay a JSON string", func(t *testing.T) {
		args := `{"city":"Paris"}`
		msg := ResponsesMessage{
			Type:                 &functionType,
			ResponsesToolMessage: &ResponsesToolMessage{CallID: &callID, Arguments: &args},
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal function_call: %v", err)
		}
		if !strings.Contains(string(encoded), `"arguments":"{\"city\":\"Paris\"}"`) {
			t.Fatalf("expected stringified arguments, got %s", encoded)
		}
	})

	t.Run("real tool_search_call frame round-trips object -> string -> object", func(t *testing.T) {
		raw := []byte(`{"type":"response.output_item.done","output_index":1,"sequence_number":5,"item":{"id":"tsc_1","type":"tool_search_call","status":"completed","arguments":{"query":"observability logs","limit":10},"call_id":"call_1","execution":"client"}}`)

		var resp BifrostResponsesStreamResponse
		if err := Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal tool_search_call frame: %v", err)
		}
		if resp.Item == nil || resp.Item.Arguments == nil {
			t.Fatalf("expected parsed item arguments, got %#v", resp.Item)
		}
		if *resp.Item.Arguments != `{"query":"observability logs","limit":10}` {
			t.Fatalf("expected stringified internal arguments, got %q", *resp.Item.Arguments)
		}

		encoded, err := MarshalSorted(resp.Item)
		if err != nil {
			t.Fatalf("marshal parsed item: %v", err)
		}
		if !strings.Contains(string(encoded), `"arguments":{"query":"observability logs","limit":10}`) {
			t.Fatalf("expected re-emitted object arguments, got %s", encoded)
		}
		if strings.Contains(string(encoded), `"arguments":"`) {
			t.Fatalf("tool_search_call arguments must round-trip as an object, got %s", encoded)
		}
	})

	t.Run("non-tool item without arguments marshals without panicking", func(t *testing.T) {
		reasoningType := ResponsesMessageTypeReasoning
		msg := ResponsesMessage{Type: &reasoningType}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal reasoning item: %v", err)
		}
		if strings.Contains(string(encoded), `"arguments"`) {
			t.Fatalf("did not expect arguments key, got %s", encoded)
		}
	})
}

func TestResponsesMessagePreservesToolSearchExecution(t *testing.T) {
	raw := []byte(`{"id":"tsc_1","type":"tool_search_call","status":"completed","arguments":{"query":"loki"},"call_id":"call_1","execution":"client"}`)

	var msg ResponsesMessage
	if err := Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal tool_search_call: %v", err)
	}
	if msg.ResponsesToolMessage == nil || msg.Execution == nil || *msg.Execution != "client" {
		t.Fatalf("expected execution=client to survive unmarshal, got %#v", msg.ResponsesToolMessage)
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal tool_search_call: %v", err)
	}
	if !strings.Contains(string(encoded), `"execution":"client"`) {
		t.Fatalf("expected execution to round-trip, got %s", encoded)
	}
}

func TestResponsesMessageRoundTripsToolSearchOutputTools(t *testing.T) {
	raw := []byte(`{"id":"tso_1","type":"tool_search_output","call_id":"call_1","tools":[{"type":"namespace","name":"telemetry","tools":[{"type":"function","name":"query_loki_logs","description":"query loki","parameters":{"type":"object","properties":{"run_id":{"type":"string"}}}}]}]}`)

	var msg ResponsesMessage
	if err := Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal tool_search_output: %v", err)
	}
	if msg.Type == nil || *msg.Type != ResponsesMessageTypeToolSearchOutput {
		t.Fatalf("expected tool_search_output type, got %#v", msg.Type)
	}
	if len(msg.ToolSearchOutputTools) == 0 {
		t.Fatalf("expected raw tools to be captured, got none")
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal tool_search_output: %v", err)
	}
	for _, want := range []string{`"type":"namespace"`, `"type":"function"`, `"name":"query_loki_logs"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("expected re-emitted tools to contain %s, got %s", want, encoded)
		}
	}
}

func TestResponsesMessageMarshalsToolSearchOutputArgumentsAsObject(t *testing.T) {
	toolSearchOutputType := ResponsesMessageTypeToolSearchOutput
	callID := "call_1"
	args := `{"query":"loki"}`
	tools := json.RawMessage(`[{"type":"namespace","name":"telemetry","tools":[{"type":"function","name":"query_loki_logs"}]}]`)
	msg := ResponsesMessage{
		Type:                  &toolSearchOutputType,
		ToolSearchOutputTools: tools,
		ResponsesToolMessage:  &ResponsesToolMessage{CallID: &callID, Arguments: &args},
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal tool_search_output: %v", err)
	}
	if !strings.Contains(string(encoded), `"arguments":{"query":"loki"}`) {
		t.Fatalf("expected object-valued arguments, got %s", encoded)
	}
	if strings.Contains(string(encoded), `"arguments":"`) {
		t.Fatalf("tool_search_output arguments must not be stringified, got %s", encoded)
	}
}

func TestDeepCopyResponsesMessagePreservesToolSearchFields(t *testing.T) {
	toolSearchOutputType := ResponsesMessageTypeToolSearchOutput
	callID := "call_1"
	name := "query_loki_logs"
	namespace := "telemetry"
	args := `{"query":"loki"}`
	execution := "client"
	tools := json.RawMessage(`[{"type":"namespace","name":"telemetry","tools":[{"type":"function","name":"query_loki_logs"}]}]`)

	copied := DeepCopyResponsesMessage(ResponsesMessage{
		Type:                  &toolSearchOutputType,
		ToolSearchOutputTools: tools,
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    &callID,
			Name:      &name,
			Namespace: &namespace,
			Arguments: &args,
			Execution: &execution,
		},
	})

	if copied.ToolSearchOutputTools == nil || string(copied.ToolSearchOutputTools) != string(tools) {
		t.Fatalf("expected raw tool_search_output tools to survive copy, got %s", copied.ToolSearchOutputTools)
	}
	if copied.ResponsesToolMessage == nil || copied.Namespace == nil || *copied.Namespace != namespace {
		t.Fatalf("expected namespace to survive copy, got %#v", copied.ResponsesToolMessage)
	}
	if copied.Execution == nil || *copied.Execution != execution {
		t.Fatalf("expected execution to survive copy, got %#v", copied.ResponsesToolMessage)
=======
// TestResponsesMessagePreservesAdditionalTools verifies that codex
// `additional_tools` input items (sent for code-mode models such as
// gpt-5.6-sol) round-trip byte-identically. These items carry a `tools` array
// whose entries have their own `type` discriminators (custom / function /
// namespace with nested tool lists); a typed decode promotes the array into
// the embedded mcp_list_tools fields and strips `type`, making OpenAI reject
// the forwarded request with "Missing required parameter:
// 'input[0].tools[0].type'".
func TestResponsesMessagePreservesAdditionalTools(t *testing.T) {
	raw := `{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"apply_patch","description":"Apply a patch"},{"type":"function","name":"shell","description":"Runs a shell command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},{"type":"namespace","name":"repo_tools","description":"Repository helper tools","tools":[{"type":"function","name":"open_file","description":"Open a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}]}`

	var msg ResponsesMessage
	if err := Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal additional_tools item: %v", err)
	}
	if msg.Type == nil || *msg.Type != ResponsesMessageTypeAdditionalTools {
		t.Fatalf("expected additional_tools item, got %#v", msg.Type)
	}
	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal preserved additional_tools item: %v", err)
	}
	if string(encoded) != raw {
		t.Fatalf("expected item to round-trip verbatim\nwant: %s\ngot:  %s", raw, encoded)
	}

	// A reused receiver must not leak preserved bytes into the next decode.
	if err := Unmarshal([]byte(`{"type":"message","role":"user","content":"hi"}`), &msg); err != nil {
		t.Fatalf("unmarshal follow-up message: %v", err)
	}
	encoded, err = MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal follow-up message: %v", err)
	}
	if strings.Contains(string(encoded), "additional_tools") {
		t.Fatalf("expected reused receiver to drop preserved bytes, got %s", encoded)
	}
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

// TestWithDefaultsStripsCodeExecutionCarry verifies that WithDefaults() (the
// normalized provider-format converters, e.g. openai/v1/responses) drops the
// Anthropic-only code-execution fidelity carry while keeping the neutral
// code_interpreter_call view — and does not mutate the source response (the raw
// Bifrost superset path keeps the carry).
func TestWithDefaultsStripsCodeExecutionCarry(t *testing.T) {
	code := "print(1)"
	resp := &BifrostResponsesResponse{
		ID: Ptr("resp_1"),
		Output: []ResponsesMessage{
			{
				Type: Ptr(ResponsesMessageTypeCodeInterpreterCall),
				ID:   Ptr("ci_1"),
				ResponsesToolMessage: &ResponsesToolMessage{
					CallID:                           Ptr("ci_1"),
					ResponsesCodeInterpreterToolCall: &ResponsesCodeInterpreterToolCall{Code: &code, ContainerID: "cntr_1"},
					ResponsesCodeExecutionCall:       &ResponsesCodeExecutionCall{ToolName: "bash_code_execution", Stdout: Ptr("hi\n")},
				},
			},
		},
	}

	normalized := resp.WithDefaults()

	// Normalized output: carry gone, neutral view intact.
	tm := normalized.Output[0].ResponsesToolMessage
	if tm.ResponsesCodeExecutionCall != nil {
		t.Error("WithDefaults leaked the code-execution carry into normalized output")
	}
	if tm.ResponsesCodeInterpreterToolCall == nil || tm.ResponsesCodeInterpreterToolCall.ContainerID != "cntr_1" {
		t.Error("WithDefaults dropped the neutral code_interpreter_call view")
	}

	// Source response (raw superset) must be untouched.
	if resp.Output[0].ResponsesToolMessage.ResponsesCodeExecutionCall == nil {
		t.Error("WithDefaults mutated the source response — superset lost the carry")
	}

	encoded, err := Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "code_execution_") {
		t.Errorf("normalized JSON still contains code_execution_* fields:\n%s", encoded)
	}
}

// TestStreamWithDefaultsStripsCodeExecutionCarry verifies the streaming converter
// drops the code-execution carry from output_item.added / output_item.done items
// (the streaming analog of the non-streaming Output strip).
func TestStreamWithDefaultsStripsCodeExecutionCarry(t *testing.T) {
	mkItem := func() *ResponsesMessage {
		return &ResponsesMessage{
			Type: Ptr(ResponsesMessageTypeCodeInterpreterCall),
			ID:   Ptr("ci_1"),
			ResponsesToolMessage: &ResponsesToolMessage{
				CallID:                           Ptr("ci_1"),
				ResponsesCodeInterpreterToolCall: &ResponsesCodeInterpreterToolCall{ContainerID: "cntr_1"},
				ResponsesCodeExecutionCall:       &ResponsesCodeExecutionCall{ToolName: "bash_code_execution"},
			},
		}
	}

	for _, typ := range []ResponsesStreamResponseType{
		ResponsesStreamResponseTypeOutputItemAdded,
		ResponsesStreamResponseTypeOutputItemDone,
	} {
		src := &BifrostResponsesStreamResponse{Type: typ, Item: mkItem()}
		out := src.WithDefaults()

		if out.Item.ResponsesToolMessage.ResponsesCodeExecutionCall != nil {
			t.Errorf("%s: leaked code-execution carry on streamed item", typ)
		}
		if out.Item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall == nil {
			t.Errorf("%s: dropped neutral code_interpreter_call view", typ)
		}
		if src.Item.ResponsesToolMessage.ResponsesCodeExecutionCall == nil {
			t.Errorf("%s: mutated source item — superset stream lost the carry", typ)
		}

		encoded, err := Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(encoded), "code_execution_") {
			t.Errorf("%s: normalized stream JSON still has code_execution_*:\n%s", typ, encoded)
		}
	}
}
