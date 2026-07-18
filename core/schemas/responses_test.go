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

// TestResponsesMessageImageGenerationCallNullResultRoundTrip verifies that a
// null "result" (image still generating, per OpenAI's schema) round-trips as
// null rather than being coerced into an empty string.
func TestResponsesMessageImageGenerationCallNullResultRoundTrip(t *testing.T) {
	raw := []byte(`{"id":"ig_1","type":"image_generation_call","status":"in_progress","result":null}`)

	var msg ResponsesMessage
	if err := Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal responses message: %v", err)
	}

	if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.ResponsesImageGenerationCall == nil {
		t.Fatalf("expected ResponsesImageGenerationCall to be populated, got %#v", msg.ResponsesToolMessage)
	}
	if msg.ResponsesToolMessage.ResponsesImageGenerationCall.Result != nil {
		t.Fatalf("expected result to stay nil, got %#v", msg.ResponsesToolMessage.ResponsesImageGenerationCall.Result)
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal responses message: %v", err)
	}
	if !strings.Contains(string(encoded), `"result":null`) {
		t.Fatalf("expected encoded message to preserve null result, got %s", encoded)
	}
	if strings.Contains(string(encoded), `"result":""`) {
		t.Fatalf("null result was coerced into an empty string: %s", encoded)
	}
}

// TestResponsesToolMCPAllowedToolsArrayForm verifies that allowed_tools sent
// as a plain array of tool names (the common form) decodes and re-encodes
// correctly instead of throwing a hard unmarshal error.
func TestResponsesToolMCPAllowedToolsArrayForm(t *testing.T) {
	raw := []byte(`{"type":"mcp","server_label":"srv","server_url":"https://x/mcp","allowed_tools":["t1","t2"]}`)

	var tool ResponsesTool
	if err := Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal responses tool: %v", err)
	}
	if tool.ResponsesToolMCP == nil || tool.ResponsesToolMCP.AllowedTools == nil {
		t.Fatalf("expected AllowedTools to be populated, got %#v", tool.ResponsesToolMCP)
	}
	if len(tool.ResponsesToolMCP.AllowedTools.ToolNames) != 2 {
		t.Fatalf("expected 2 tool names, got %#v", tool.ResponsesToolMCP.AllowedTools.ToolNames)
	}

	encoded, err := MarshalSorted(tool)
	if err != nil {
		t.Fatalf("marshal responses tool: %v", err)
	}
	if !strings.Contains(string(encoded), `"allowed_tools":["t1","t2"]`) {
		t.Fatalf("expected encoded tool to preserve allowed_tools array, got %s", encoded)
	}
}

// TestResponsesToolMCPAllowedToolsFilterForm verifies that allowed_tools sent
// as a filter object ({read_only, tool_names}) round-trips instead of
// silently decoding to an empty object.
func TestResponsesToolMCPAllowedToolsFilterForm(t *testing.T) {
	raw := []byte(`{"type":"mcp","server_label":"srv","server_url":"https://x/mcp","allowed_tools":{"read_only":true,"tool_names":["t1"]}}`)

	var tool ResponsesTool
	if err := Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal responses tool: %v", err)
	}
	if tool.ResponsesToolMCP == nil || tool.ResponsesToolMCP.AllowedTools == nil || tool.ResponsesToolMCP.AllowedTools.Filter == nil {
		t.Fatalf("expected AllowedTools.Filter to be populated, got %#v", tool.ResponsesToolMCP)
	}
	if tool.ResponsesToolMCP.AllowedTools.Filter.ReadOnly == nil || !*tool.ResponsesToolMCP.AllowedTools.Filter.ReadOnly {
		t.Fatalf("expected read_only true, got %#v", tool.ResponsesToolMCP.AllowedTools.Filter)
	}

	encoded, err := MarshalSorted(tool)
	if err != nil {
		t.Fatalf("marshal responses tool: %v", err)
	}
	if !strings.Contains(string(encoded), `"tool_names":["t1"]`) || !strings.Contains(string(encoded), `"read_only":true`) {
		t.Fatalf("expected encoded tool to preserve allowed_tools filter, got %s", encoded)
	}
}

// TestResponsesToolMCPAllowedToolsEmptyArrayMarshalsToEmptyArray verifies that
// a non-nil empty ToolNames slice (Anthropic's "deny all" case, e.g.
// convertAnthropicToolsetToBifrostTool in core/providers/anthropic/responses.go)
// marshals as "[]", not "{}" or "null" — the old bare struct encoding treated
// a zero-length slice as empty via omitempty and dropped it entirely,
// producing "{}", which is neither a valid array nor a valid filter object
// per OpenAI's allowed_tools schema.
func TestResponsesToolMCPAllowedToolsEmptyArrayMarshalsToEmptyArray(t *testing.T) {
	at := ResponsesToolMCPAllowedTools{ToolNames: []string{}}

	encoded, err := MarshalSorted(at)
	if err != nil {
		t.Fatalf("marshal responses tool mcp allowed tools: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("expected empty ToolNames to marshal as [], got %s", encoded)
	}
}

// TestResponsesMessageMCPCallServerLabelAndApprovalRequestIDTogether verifies
// that approval_request_id decodes correctly and isn't clobbered when
// server_label is also present in the same mcp_call item (the common
// production shape). Note: server_label itself does not round-trip on this
// branch — that's a separate, already-tracked bug (server_label sits behind
// two levels of anonymous pointer embedding that the JSON decoder doesn't
// auto-promote through) fixed independently in maximhq/bifrost#4844. This
// test only asserts that approval_request_id's manual routing doesn't
// misbehave when ResponsesMCPToolCall is (or isn't) already allocated by
// other decode paths.
func TestResponsesMessageMCPCallServerLabelAndApprovalRequestIDTogether(t *testing.T) {
	raw := []byte(`{"id":"mcp_1","type":"mcp_call","server_label":"my-srv","name":"tool1","arguments":"{}","status":"completed","approval_request_id":"apr_1"}`)

	var msg ResponsesMessage
	if err := Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal responses message: %v", err)
	}
	if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.ResponsesMCPToolCall == nil {
		t.Fatalf("expected ResponsesMCPToolCall to be populated, got %#v", msg.ResponsesToolMessage)
	}
	if msg.ResponsesToolMessage.ResponsesMCPToolCall.ApprovalRequestID == nil || *msg.ResponsesToolMessage.ResponsesMCPToolCall.ApprovalRequestID != "apr_1" {
		t.Fatalf("expected approval_request_id to survive unmarshal even with server_label present, got %#v", msg.ResponsesToolMessage.ResponsesMCPToolCall.ApprovalRequestID)
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal responses message: %v", err)
	}
	if !strings.Contains(string(encoded), `"approval_request_id":"apr_1"`) {
		t.Fatalf("expected encoded message to preserve approval_request_id, got %s", encoded)
	}
}

// TestResponsesMessageMCPCallApprovalRequestIDRoundTrip verifies that
// mcp_call's approval_request_id (which links the call to a later
// mcp_approval_response) survives decode/re-encode.
func TestResponsesMessageMCPCallApprovalRequestIDRoundTrip(t *testing.T) {
	raw := []byte(`{"id":"mcp_1","type":"mcp_call","name":"tool1","arguments":"{}","status":"completed","approval_request_id":"apr_1"}`)

	var msg ResponsesMessage
	if err := Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal responses message: %v", err)
	}
	if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.ResponsesMCPToolCall == nil {
		t.Fatalf("expected ResponsesMCPToolCall to be populated, got %#v", msg.ResponsesToolMessage)
	}
	if msg.ResponsesToolMessage.ResponsesMCPToolCall.ApprovalRequestID == nil || *msg.ResponsesToolMessage.ResponsesMCPToolCall.ApprovalRequestID != "apr_1" {
		t.Fatalf("expected approval_request_id to survive unmarshal, got %#v", msg.ResponsesToolMessage.ResponsesMCPToolCall.ApprovalRequestID)
	}

	encoded, err := MarshalSorted(msg)
	if err != nil {
		t.Fatalf("marshal responses message: %v", err)
	}
	if !strings.Contains(string(encoded), `"approval_request_id":"apr_1"`) {
		t.Fatalf("expected encoded message to preserve approval_request_id, got %s", encoded)
	}
}

// TestResponsesToolFileSearchHybridSearchRoundTrip verifies that
// ranking_options.hybrid_search (reciprocal-rank-fusion weighting) survives
// decode/re-encode on a file_search tool declaration.
func TestResponsesToolFileSearchHybridSearchRoundTrip(t *testing.T) {
	raw := []byte(`{"type":"file_search","vector_store_ids":["vs_1"],"ranking_options":{"ranker":"auto","hybrid_search":{"embedding_weight":0.7,"text_weight":0.3}}}`)

	var tool ResponsesTool
	if err := Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal responses tool: %v", err)
	}
	if tool.ResponsesToolFileSearch == nil || tool.ResponsesToolFileSearch.RankingOptions == nil || tool.ResponsesToolFileSearch.RankingOptions.HybridSearch == nil {
		t.Fatalf("expected HybridSearch to be populated, got %#v", tool.ResponsesToolFileSearch)
	}
	if tool.ResponsesToolFileSearch.RankingOptions.HybridSearch.EmbeddingWeight != 0.7 || tool.ResponsesToolFileSearch.RankingOptions.HybridSearch.TextWeight != 0.3 {
		t.Fatalf("expected hybrid_search weights to survive unmarshal, got %#v", tool.ResponsesToolFileSearch.RankingOptions.HybridSearch)
	}

	encoded, err := MarshalSorted(tool)
	if err != nil {
		t.Fatalf("marshal responses tool: %v", err)
	}
	if !strings.Contains(string(encoded), `"hybrid_search":{"embedding_weight":0.7,"text_weight":0.3}`) {
		t.Fatalf("expected encoded tool to preserve hybrid_search, got %s", encoded)
	}
}

// TestResponsesToolWebSearchPreviewSearchContentTypesRoundTrip verifies that
// search_content_types survives decode/re-encode on the web_search_preview
// tool variant specifically (it was already modeled correctly on the plain
// web_search variant).
func TestResponsesToolWebSearchPreviewSearchContentTypesRoundTrip(t *testing.T) {
	raw := []byte(`{"type":"web_search_preview","search_content_types":["text","image"]}`)

	var tool ResponsesTool
	if err := Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal responses tool: %v", err)
	}
	if tool.ResponsesToolWebSearchPreview == nil || len(tool.ResponsesToolWebSearchPreview.SearchContentTypes) != 2 {
		t.Fatalf("expected SearchContentTypes to be populated, got %#v", tool.ResponsesToolWebSearchPreview)
	}

	encoded, err := MarshalSorted(tool)
	if err != nil {
		t.Fatalf("marshal responses tool: %v", err)
	}
	if !strings.Contains(string(encoded), `"search_content_types":["text","image"]`) {
		t.Fatalf("expected encoded tool to preserve search_content_types, got %s", encoded)
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
