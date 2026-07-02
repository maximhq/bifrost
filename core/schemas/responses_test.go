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
			// execution field must survive unmarshal
			if resp.Item.ResponsesToolMessage.Execution == nil || *resp.Item.ResponsesToolMessage.Execution != "client" {
				t.Fatalf("[%s] expected execution=\"client\" after unmarshal, got %#v", name, resp.Item.ResponsesToolMessage.Execution)
			}
		}
	})
}

// TestResponsesMessageToolSearchCallMarshal verifies the round-trip wire format
// for tool_search_call items. OpenAI sends arguments as a JSON object; after
// Bifrost normalizes them into a *string on unmarshal, MarshalJSON must expand
// them back to an object so the client receives the original wire format.
// It also verifies that the required "execution" field is always emitted
// (defaulting to "client" when not set), which Codex requires to execute
// the tool search.
func TestResponsesMessageToolSearchCallMarshal(t *testing.T) {
	msgType := ResponsesMessageTypeToolSearchCall

	t.Run("completed frame marshals arguments as json object", func(t *testing.T) {
		args := `{"query":"observability","limit":10}`
		msg := ResponsesMessage{
			Type: &msgType,
			ResponsesToolMessage: &ResponsesToolMessage{
				Arguments: &args,
			},
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal tool_search_call message: %v", err)
		}
		// arguments must be a JSON object, not a quoted string
		if strings.Contains(string(encoded), `"arguments":"`) {
			t.Fatalf("expected arguments as object, got string form: %s", encoded)
		}
		if !strings.Contains(string(encoded), `"arguments":{"query":"observability","limit":10}`) {
			t.Fatalf("expected arguments object in output, got: %s", encoded)
		}
		// execution must always be present (codex requires it)
		if !strings.Contains(string(encoded), `"execution":"client"`) {
			t.Fatalf("expected execution=client in output, got: %s", encoded)
		}
	})

	t.Run("full round-trip from raw openai frame", func(t *testing.T) {
		raw := []byte(`{"id":"tsc_abc","type":"tool_search_call","status":"completed","call_id":"call_xyz","execution":"client","arguments":{"query":"sentry grafana","limit":10}}`)
		var msg ResponsesMessage
		if err := Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal tool_search_call: %v", err)
		}
		if msg.Arguments == nil || *msg.Arguments != `{"query":"sentry grafana","limit":10}` {
			t.Fatalf("unexpected arguments after unmarshal: %v", msg.Arguments)
		}
		if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.Execution == nil || *msg.ResponsesToolMessage.Execution != "client" {
			t.Fatalf("expected execution=client after unmarshal, got %#v", msg.ResponsesToolMessage)
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal tool_search_call after round-trip: %v", err)
		}
		if strings.Contains(string(encoded), `"arguments":"`) {
			t.Fatalf("expected object form after round-trip, got string: %s", encoded)
		}
		if !strings.Contains(string(encoded), `"arguments":{"`) {
			t.Fatalf("expected arguments object after round-trip, got: %s", encoded)
		}
		if !strings.Contains(string(encoded), `"execution":"client"`) {
			t.Fatalf("expected execution=client preserved in round-trip, got: %s", encoded)
		}
	})

	t.Run("execution is preserved when set to non-default value", func(t *testing.T) {
		args := `{"query":"test"}`
		exec := "server"
		msg := ResponsesMessage{
			Type: &msgType,
			ResponsesToolMessage: &ResponsesToolMessage{
				Arguments: &args,
				Execution: &exec,
			},
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal tool_search_call with custom execution: %v", err)
		}
		if !strings.Contains(string(encoded), `"execution":"server"`) {
			t.Fatalf("expected execution=server to be preserved, got: %s", encoded)
		}
	})

	t.Run("invalid non-empty tool_search_call arguments return error", func(t *testing.T) {
		for _, args := range []string{`{"query":`, `[]`, `42`, `null`, `"query"`} {
			msg := ResponsesMessage{
				Type: &msgType,
				ResponsesToolMessage: &ResponsesToolMessage{
					Arguments: &args,
				},
			}
			_, err := MarshalSorted(msg)
			if err == nil {
				t.Fatalf("expected invalid tool_search_call arguments %q to return error", args)
			}
			if !strings.Contains(err.Error(), "tool_search_call arguments must be valid JSON object text") {
				t.Fatalf("unexpected error for %q: %v", args, err)
			}
		}
	})

	t.Run("function_call still marshals arguments as json string", func(t *testing.T) {
		fcType := ResponsesMessageTypeFunctionCall
		args := `{"param":"value"}`
		msg := ResponsesMessage{
			Type: &fcType,
			ResponsesToolMessage: &ResponsesToolMessage{
				Arguments: &args,
			},
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal function_call message: %v", err)
		}
		// function_call arguments must remain a JSON string
		if !strings.Contains(string(encoded), `"arguments":"{`) {
			t.Fatalf("expected function_call arguments as string, got: %s", encoded)
		}
		// function_call must not emit execution
		if strings.Contains(string(encoded), `"execution"`) {
			t.Fatalf("execution must not appear in function_call output, got: %s", encoded)
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

func TestResponsesMessageToolSearchOutputRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantFields []string
		wantAbsent []string
	}{
		{
			name: "namespace tool type is preserved",
			raw: `{
				"type": "tool_search_output",
				"call_id": "search-1",
				"tools": [
					{
						"type": "namespace",
						"name": "mcp__codexself",
						"description": "Codex self-tools",
						"tools": [
							{"type": "function", "name": "codex_reply", "description": "Reply with a message"}
						]
					}
				]
			}`,
			wantFields: []string{
				`"type":"tool_search_output"`,
				`"call_id":"search-1"`,
				`"type":"namespace"`,
				`"name":"mcp__codexself"`,
				`"type":"function"`,
				`"name":"codex_reply"`,
			},
		},
		{
			name: "empty tools array round-trips cleanly",
			raw:  `{"type":"tool_search_output","call_id":"search-3","tools":[]}`,
			wantFields: []string{
				`"type":"tool_search_output"`,
				`"call_id":"search-3"`,
			},
		},
		{
			name: "tool_search_output emits execution field (codex requires it)",
			raw: `{
				"type": "tool_search_output",
				"call_id": "search-4",
				"tools": [{"type": "function", "name": "shell"}]
			}`,
			wantFields: []string{
				`"type":"tool_search_output"`,
				`"execution":"client"`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var msg ResponsesMessage
			if err := Unmarshal([]byte(tc.raw), &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			// Verify the type constant parsed correctly.
			if msg.Type == nil || *msg.Type != ResponsesMessageTypeToolSearchOutput {
				t.Fatalf("expected type tool_search_output, got %v", msg.Type)
			}

			// Tools must be non-nil for any non-empty tools array.
			// (empty-array case is allowed to be nil or empty slice after round-trip)

			encoded, err := MarshalSorted(msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(encoded)

			for _, want := range tc.wantFields {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in encoded output:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("unexpected %q present in encoded output:\n%s", absent, got)
				}
			}
		})
	}
}

// TestResponsesMessageMCPListToolsRoundTrip guards against a field collision:
// ResponsesToolMessage.Tools (tool_search_output) and the embedded
// ResponsesMCPListTools.Tools (mcp_list_tools) both use the wire key "tools".
// If either field were left to struct-tag-based encoding, the shallower one
// would silently win for every message type and the other would never survive
// marshal/unmarshal.
//
// Note: this only asserts the "tools" field, which is what UnmarshalJSON/
// MarshalJSON route manually. ResponsesMCPListTools.ServerLabel is a separate,
// pre-existing bug (reproduces identically on a clean upstream/dev checkout,
// unrelated to this collision or to tool_search at all): the JSON decoder
// never auto-allocates the doubly-nested anonymous pointer chain
// ResponsesMessage -> *ResponsesToolMessage -> *ResponsesMCPListTools, so no
// field on ResponsesMCPListTools reaches the wire on unmarshal unless
// something else (like this Tools routing) has already allocated the struct.
func TestResponsesMessageMCPListToolsRoundTrip(t *testing.T) {
	raw := `{"type":"mcp_list_tools","tools":[{"name":"query_prometheus","input_schema":{"type":"object"}}]}`

	var msg ResponsesMessage
	if err := Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal mcp_list_tools: %v", err)
	}

	if msg.ResponsesToolMessage == nil || msg.ResponsesMCPListTools == nil {
		t.Fatalf("expected ResponsesMCPListTools to be populated, got %#v", msg.ResponsesToolMessage)
	}
	if len(msg.ResponsesMCPListTools.Tools) != 1 || msg.ResponsesMCPListTools.Tools[0].Name != "query_prometheus" {
		t.Fatalf("expected mcp_list_tools.tools to survive unmarshal, got %#v", msg.ResponsesMCPListTools.Tools)
	}
	// The unrelated tool_search_output field must stay untouched.
	if msg.ResponsesToolMessage.Tools != nil {
		t.Fatalf("expected ResponsesToolMessage.Tools to stay nil for mcp_list_tools, got %#v", msg.ResponsesToolMessage.Tools)
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal mcp_list_tools: %v", err)
	}
	got := string(encoded)
	if !strings.Contains(got, `"name":"query_prometheus"`) {
		t.Fatalf("expected mcp_list_tools.tools to survive marshal, got: %s", got)
	}
}

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
