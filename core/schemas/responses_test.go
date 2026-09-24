package schemas

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicBillingHeaderExtraction(t *testing.T) {
	header := "x-anthropic-billing-header: cc_version=2.1.270.42c; cc_entrypoint=cli; cch=12345;"
	for _, tc := range []struct {
		name, text string
		strip      bool
	}{
		{"metadata", header, true},
		{"whitespace", " \n" + header + "\n", true},
		{"future field", "x-anthropic-billing-header: cc_version=2.1.270.abc; future=value;", true},
		{"ordinary text", "Keep these instructions.", false},
		{"quoted header", "Discuss " + header, false},
		{"mixed multiline", header + "\nKeep these instructions.", false},
		{"mixed same line", header + " Keep these instructions.", false},
	} {
		for _, shape := range []string{"string", "blocks"} {
			t.Run(tc.name+"/"+shape, func(t *testing.T) {
				content := &ResponsesMessageContent{ContentStr: &tc.text}
				if shape == "blocks" {
					content = &ResponsesMessageContent{ContentBlocks: []ResponsesMessageContentBlock{{Type: ResponsesInputMessageContentBlockTypeText, Text: &tc.text}}}
				}
				r := &BifrostResponsesRequest{Input: []ResponsesMessage{
					{Role: Ptr(ResponsesInputMessageRoleSystem), Content: content},
					{Role: Ptr(ResponsesInputMessageRoleUser), Content: &ResponsesMessageContent{ContentStr: &tc.text}},
				}, RawRequestBody: []byte("original raw body")}
				before, err := MarshalSorted(r)
				require.NoError(t, err)
				r.ExtractAnthropicBillingHeader()
				r.ExtractAnthropicBillingHeader() // Repeated normalization is harmless.
				if tc.strip {
					require.Len(t, r.Input, 1)
					assert.Equal(t, ResponsesInputMessageRoleUser, *r.Input[0].Role)
					assert.Equal(t, tc.text, *r.Input[0].Content.ContentStr)
				} else {
					assert.Nil(t, r.anthropicBillingHeader)
					require.Len(t, r.Input, 2)
				}
				assert.Equal(t, "original raw body", string(r.RawRequestBody))
				restored := r.WithAnthropicBillingHeader()
				after, err := MarshalSorted(restored)
				require.NoError(t, err)
				assert.Equal(t, string(before), string(after))
				assert.Same(t, restored, restored.WithAnthropicBillingHeader())
			})
		}
	}
}

func TestAnthropicBillingHeaderRestoresPositionsAndCacheMarkers(t *testing.T) {
	header := "x-anthropic-billing-header: cc_version=2.1.270.42c;"
	block := func(text string) ResponsesMessageContentBlock {
		return ResponsesMessageContentBlock{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr(text)}
	}
	for _, onlyHeaders := range []bool{false, true} {
		blocks := []ResponsesMessageContentBlock{block(header), block(header)}
		if !onlyHeaders {
			blocks = []ResponsesMessageContentBlock{block("first"), block(header), block("last"), block(header)}
		}
		blocks[1].CacheControl = &CacheControl{Type: CacheControlTypeEphemeral}
		r := &BifrostResponsesRequest{Input: []ResponsesMessage{
			{Role: Ptr(ResponsesInputMessageRoleSystem), Content: &ResponsesMessageContent{ContentBlocks: blocks}},
			{Role: Ptr(ResponsesInputMessageRoleUser), Content: &ResponsesMessageContent{ContentStr: Ptr("hello")}},
		}}
		before, err := MarshalSorted(r)
		require.NoError(t, err)
		r.ExtractAnthropicBillingHeader()
		normalized, err := MarshalSorted(r)
		require.NoError(t, err)
		assert.NotContains(t, string(normalized), "x-anthropic-billing-header:")
		// Core fallbacks shallow-copy the request; the private metadata must survive.
		fallback := *r
		restored := fallback.WithAnthropicBillingHeader()
		after, err := MarshalSorted(restored)
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after))
		unchanged, err := MarshalSorted(r)
		require.NoError(t, err)
		assert.Equal(t, string(normalized), string(unchanged))
	}
}

// TestBifrostResponsesStreamResponseOmitsEmptyItem verifies that events without
// an item object (response.created, output_text.delta, response.completed, ...)
// do not serialize "item": null. Strict Responses API clients (e.g. opencode's
// open-responses protocol) reject events where "item" is present but null —
// the field only belongs on output_item.added / output_item.done.
func TestBifrostResponsesStreamResponseOmitsEmptyItem(t *testing.T) {
	for _, typ := range []ResponsesStreamResponseType{
		ResponsesStreamResponseTypeCreated,
		ResponsesStreamResponseTypeInProgress,
		ResponsesStreamResponseTypeOutputTextDelta,
		ResponsesStreamResponseTypeContentPartAdded,
		ResponsesStreamResponseTypeCompleted,
	} {
		ev := &BifrostResponsesStreamResponse{Type: typ, SequenceNumber: 0}
		encoded, err := MarshalSorted(ev)
		if err != nil {
			t.Fatalf("%s: marshal: %v", typ, err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("%s: unmarshal encoded event: %v", typ, err)
		}
		if _, ok := decoded["item"]; ok {
			t.Errorf("%s: event without item serializes an item field:\n%s", typ, encoded)
		}
	}

	for _, typ := range []ResponsesStreamResponseType{
		ResponsesStreamResponseTypeOutputItemAdded,
		ResponsesStreamResponseTypeOutputItemDone,
	} {
		withItem := &BifrostResponsesStreamResponse{
			Type: typ,
			Item: &ResponsesMessage{Type: Ptr(ResponsesMessageTypeMessage), ID: Ptr("msg_1")},
		}
		encoded, err := MarshalSorted(withItem)
		if err != nil {
			t.Fatalf("%s: marshal: %v", typ, err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("%s: unmarshal encoded event: %v", typ, err)
		}
		itemJSON, ok := decoded["item"]
		if !ok {
			t.Errorf("%s: lost item object:\n%s", typ, encoded)
			continue
		}
		var item ResponsesMessage
		if err := json.Unmarshal(itemJSON, &item); err != nil {
			t.Fatalf("%s: unmarshal item: %v", typ, err)
		}
		if item.ID == nil || *item.ID != "msg_1" {
			t.Errorf("%s: unexpected item payload: %#v", typ, item)
		}
	}
}

func TestBifrostResponsesStreamResponseLogProbsScopedToApplicableEvents(t *testing.T) {
	created := &BifrostResponsesStreamResponse{Type: ResponsesStreamResponseTypeCreated, SequenceNumber: 0}
	encoded, err := MarshalSorted(created)
	if err != nil {
		t.Fatalf("created: marshal: %v", err)
	}
	var createdDecoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &createdDecoded); err != nil {
		t.Fatalf("created: unmarshal encoded event: %v", err)
	}
	if _, ok := createdDecoded["logprobs"]; ok {
		t.Fatalf("created: unexpected logprobs field: %s", encoded)
	}

	delta := (&BifrostResponsesStreamResponse{Type: ResponsesStreamResponseTypeOutputTextDelta}).WithDefaults()
	encoded, err = MarshalSorted(delta)
	if err != nil {
		t.Fatalf("output_text.delta: marshal: %v", err)
	}
	var deltaDecoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &deltaDecoded); err != nil {
		t.Fatalf("output_text.delta: unmarshal encoded event: %v", err)
	}
	logprobsJSON, ok := deltaDecoded["logprobs"]
	if !ok {
		t.Fatalf("output_text.delta: missing logprobs field: %s", encoded)
	}
	var logprobs []ResponsesOutputMessageContentTextLogProb
	if err := json.Unmarshal(logprobsJSON, &logprobs); err != nil {
		t.Fatalf("output_text.delta: unmarshal logprobs: %v", err)
	}
	if logprobs == nil || len(logprobs) != 0 {
		t.Fatalf("output_text.delta: expected empty logprobs array, got %#v", logprobs)
	}
}

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

func TestBifrostResponsesResponseWithDefaultsPreservesUltrafastServiceTier(t *testing.T) {
	tier := BifrostServiceTierUltrafast
	got := (&BifrostResponsesResponse{ServiceTier: &tier}).WithDefaults()
	if got.ServiceTier == nil || *got.ServiceTier != BifrostServiceTierUltrafast {
		t.Fatalf("service tier = %v, want ultrafast", got.ServiceTier)
	}
}

// Cursor (and other Chat Completions clients) send function tools nested under
// a "function" wrapper. The unmarshal must lift name/description/parameters so
// providers that require a top-level name (e.g. Bedrock) don't reject the tool.
func TestResponsesToolUnmarshalLiftsChatCompletionsFunctionWrapper(t *testing.T) {
	raw := []byte(`{
		"type": "function",
		"function": {
			"name": "read_file",
			"description": "Reads a file",
			"parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]},
			"strict": true
		}
	}`)

	var tool ResponsesTool
	if err := Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal chat-completions-format tool: %v", err)
	}

	if tool.Name == nil || *tool.Name != "read_file" {
		t.Fatalf("expected name lifted from function wrapper, got %#v", tool.Name)
	}
	if tool.Description == nil || *tool.Description != "Reads a file" {
		t.Fatalf("expected description lifted from function wrapper, got %#v", tool.Description)
	}
	if tool.ResponsesToolFunction == nil || tool.ResponsesToolFunction.Parameters == nil {
		t.Fatalf("expected parameters lifted from function wrapper, got %#v", tool.ResponsesToolFunction)
	}
	if len(tool.ResponsesToolFunction.Parameters.Required) != 1 || tool.ResponsesToolFunction.Parameters.Required[0] != "path" {
		t.Fatalf("expected parameters schema to survive, got %#v", tool.ResponsesToolFunction.Parameters)
	}
	if tool.ResponsesToolFunction.Strict == nil || !*tool.ResponsesToolFunction.Strict {
		t.Fatalf("expected strict lifted from function wrapper, got %#v", tool.ResponsesToolFunction.Strict)
	}
}

func TestResponsesToolUnmarshalTopLevelFieldsWinOverFunctionWrapper(t *testing.T) {
	tests := []struct {
		name            string
		raw             string
		wantName        string
		wantDescription string
		wantStrict      *bool
		wantParamKey    string
	}{
		{
			name: "name_and_parameters",
			raw: `{
				"type": "function",
				"name": "top_level_name",
				"parameters": {"type": "object", "properties": {"a": {"type": "string"}}},
				"function": {
					"name": "nested_name",
					"parameters": {"type": "object", "properties": {"b": {"type": "string"}}}
				}
			}`,
			wantName:     "top_level_name",
			wantParamKey: "a",
		},
		{
			name: "description",
			raw: `{
				"type": "function",
				"name": "top_level_name",
				"description": "top-level description",
				"function": {"name": "nested_name", "description": "nested description"}
			}`,
			wantName:        "top_level_name",
			wantDescription: "top-level description",
		},
		{
			name: "explicit_strict_false",
			raw: `{
				"type": "function",
				"strict": false,
				"function": {"name": "nested_name", "strict": true}
			}`,
			// Name is still lifted from the wrapper; the explicit top-level
			// strict:false must not be overwritten by the nested strict:true.
			wantName:   "nested_name",
			wantStrict: Ptr(false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tool ResponsesTool
			if err := Unmarshal([]byte(tt.raw), &tool); err != nil {
				t.Fatalf("unmarshal mixed-format tool: %v", err)
			}

			if tool.Name == nil || *tool.Name != tt.wantName {
				t.Fatalf("expected name %q, got %#v", tt.wantName, tool.Name)
			}
			if tool.ResponsesToolFunction == nil {
				t.Fatalf("expected function tool payload, got nil")
			}
			if tt.wantDescription != "" && (tool.Description == nil || *tool.Description != tt.wantDescription) {
				t.Fatalf("expected description %q, got %#v", tt.wantDescription, tool.Description)
			}
			if tt.wantStrict != nil {
				if tool.ResponsesToolFunction.Strict == nil || *tool.ResponsesToolFunction.Strict != *tt.wantStrict {
					t.Fatalf("expected strict %v, got %#v", *tt.wantStrict, tool.ResponsesToolFunction.Strict)
				}
			}
			if tt.wantParamKey != "" {
				if tool.ResponsesToolFunction.Parameters == nil || tool.ResponsesToolFunction.Parameters.Properties == nil {
					t.Fatalf("expected parameters present, got %#v", tool.ResponsesToolFunction)
				}
				if _, ok := tool.ResponsesToolFunction.Parameters.Properties.Get(tt.wantParamKey); !ok {
					t.Fatalf("expected top-level parameters to win, got %#v", tool.ResponsesToolFunction.Parameters)
				}
			}
		})
	}
}

func TestResponsesToolUnmarshalRejectsMalformedFunctionWrapper(t *testing.T) {
	raw := []byte(`{"type": "function", "function": "not_an_object"}`)

	var tool ResponsesTool
	err := Unmarshal(raw, &tool)
	if err == nil {
		t.Fatalf("expected error for malformed function wrapper, got nil")
	}
	if !strings.Contains(err.Error(), "invalid 'function' object") {
		t.Fatalf("expected contextual error, got %v", err)
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

func TestResponsesMessageContentUnmarshalJSONBoundaries(t *testing.T) {
	tests := []struct {
		name string
		data string
		want ResponsesMessageContent
	}{
		{name: "empty string", data: `""`, want: ResponsesMessageContent{ContentStr: Ptr("")}},
		{name: "string with whitespace", data: " \t\r\n\"hello\" \t\r\n", want: ResponsesMessageContent{ContentStr: Ptr("hello")}},
		{name: "escaped string", data: `"line\n\"quote\"\u4e16\u754c"`, want: ResponsesMessageContent{ContentStr: Ptr("line\n\"quote\"\u4e16\u754c")}},
		{name: "null", data: " \t\r\nnull \t\r\n", want: ResponsesMessageContent{ContentStr: Ptr("")}},
		{name: "empty array", data: " \t\r\n[] \t\r\n", want: ResponsesMessageContent{ContentBlocks: []ResponsesMessageContentBlock{}}},
		{
			name: "content blocks",
			data: `[{"type":"input_text","text":"hello"}]`,
			want: ResponsesMessageContent{ContentBlocks: []ResponsesMessageContentBlock{
				{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("hello")},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ResponsesMessageContent
			require.NoError(t, got.UnmarshalJSON([]byte(tt.data)))
			assert.Equal(t, tt.want, got)
		})
	}

	invalid := []struct {
		name string
		data string
	}{
		{name: "empty", data: ""},
		{name: "whitespace only", data: " \t\r\n"},
		{name: "object", data: `{}`},
		{name: "number", data: `123`},
		{name: "boolean", data: `true`},
		{name: "non JSON whitespace", data: "\vnull"},
		{name: "truncated null", data: `nul`},
		{name: "truncated string", data: `"unterminated`},
		{name: "invalid escape", data: `"\q"`},
		{name: "truncated array", data: `[`},
		{name: "invalid array item", data: `[{"type":"input_text","text":"new"},42]`},
		{name: "trailing comma", data: `[{},]`},
		{name: "trailing value", data: `"text" false`},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			before := ResponsesMessageContent{ContentStr: Ptr("previous")}
			got := before
			err := got.UnmarshalJSON([]byte(tt.data))
			const wantError = "content field is neither a string nor an array of Content blocks"
			require.EqualError(t, err, wantError)
			assert.Equal(t, before, got, "failed decode changed receiver")
		})
	}
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

// TestDeepCopyResponsesMessagePreservesRawPreserved verifies that a raw-preserved
// item survives the copy. rawPreserved is unexported, so a copy that misses it
// re-marshals field-by-field and reduces the item to just its type.
func TestDeepCopyResponsesMessagePreservesRawPreserved(t *testing.T) {
	raw := `{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec","format":{"type":"grammar","syntax":"lark","definition":"start: x"}}]}`

	var msg ResponsesMessage
	if err := msg.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	encoded, err := DeepCopyResponsesMessage(msg).MarshalJSON()
	if err != nil {
		t.Fatalf("marshal copy: %v", err)
	}
	if string(encoded) != raw {
		t.Fatalf("copy did not round-trip verbatim:\n got: %s\nwant: %s", encoded, raw)
	}
}

// TestDeepCopyResponsesMessagePreservesCacheControls verifies cache breakpoints survive copy-on-write request transforms.
func TestDeepCopyResponsesMessagePreservesCacheControls(t *testing.T) {
	ttl := "1h"
	scope := "user"
	original := ResponsesMessage{
		Type:         Ptr(ResponsesMessageTypeFunctionCallOutput),
		CacheControl: &CacheControl{Type: CacheControlTypeEphemeral, TTL: &ttl, Scope: &scope},
		Content: &ResponsesMessageContent{ContentBlocks: []ResponsesMessageContentBlock{{
			Type:         ResponsesInputMessageContentBlockTypeText,
			Text:         Ptr("cacheable content"),
			CacheControl: &CacheControl{Type: CacheControlTypeEphemeral, TTL: &ttl, Scope: &scope},
		}}},
	}

	copied := DeepCopyResponsesMessage(original)
	cacheControls := [][2]*CacheControl{
		{original.CacheControl, copied.CacheControl},
		{original.Content.ContentBlocks[0].CacheControl, copied.Content.ContentBlocks[0].CacheControl},
	}
	for _, pair := range cacheControls {
		originalCacheControl, copiedCacheControl := pair[0], pair[1]
		if copiedCacheControl == nil {
			t.Fatal("deep copy dropped cache control")
		}
		if copiedCacheControl.Type != originalCacheControl.Type || copiedCacheControl.TTL == nil || originalCacheControl.TTL == nil || *copiedCacheControl.TTL != *originalCacheControl.TTL || copiedCacheControl.Scope == nil || originalCacheControl.Scope == nil || *copiedCacheControl.Scope != *originalCacheControl.Scope {
			t.Fatalf("cache control = %#v, want %#v", copiedCacheControl, originalCacheControl)
		}
		if copiedCacheControl == originalCacheControl {
			t.Error("copy aliases the original cache control struct")
		}
		if copiedCacheControl.TTL == originalCacheControl.TTL {
			t.Error("copy aliases the original cache control TTL")
		}
		if copiedCacheControl.Scope == originalCacheControl.Scope {
			t.Error("copy aliases the original cache control scope")
		}
	}
}

// TestDeepCopyResponsesMessagePreservesExtendedFields verifies newly supported Responses fields survive the shared copy path.
func TestDeepCopyResponsesMessagePreservesExtendedFields(t *testing.T) {
	fileType := "application/pdf"
	breakpointMode := "explicit"
	annotationIndex := 1
	annotationPage := 2
	annotationSource := "anthropic"
	annotationEncryptedIndex := "ciphertext"
	category := "safety"
	original := ResponsesMessage{
		ProviderNativeParts: json.RawMessage(`{"thoughtSignature":"opaque"}`),
		Content: &ResponsesMessageContent{ContentBlocks: []ResponsesMessageContentBlock{{
			Type:                                  ResponsesOutputMessageContentTypeText,
			ResponsesInputMessageContentBlockFile: &ResponsesInputMessageContentBlockFile{FileType: &fileType},
			Citations:                             &Citations{Enabled: Ptr(true)},
			PromptCacheBreakpoint:                 &PromptCacheBreakpoint{Mode: &breakpointMode},
			ResponsesOutputMessageContentText: &ResponsesOutputMessageContentText{Annotations: []ResponsesOutputMessageContentTextAnnotation{{
				Index:           &annotationIndex,
				StartCharIndex:  &annotationIndex,
				EndCharIndex:    &annotationIndex,
				StartPageNumber: &annotationPage,
				EndPageNumber:   &annotationPage,
				StartBlockIndex: &annotationIndex,
				EndBlockIndex:   &annotationIndex,
				Source:          &annotationSource,
				EncryptedIndex:  &annotationEncryptedIndex,
			}}},
			ResponsesOutputMessageContentRenderedContent: &ResponsesOutputMessageContentRenderedContent{RenderedContent: "rendered"},
			ResponsesOutputMessageContentCompaction:      &ResponsesOutputMessageContentCompaction{Summary: "summary"},
			ResponsesOutputMessageContentFallback: &ResponsesOutputMessageContentFallback{
				FromModel:       "claude-opus-5",
				ToModel:         "claude-sonnet-5",
				TriggerType:     "refusal",
				TriggerCategory: &category,
			},
		}}},
		ResponsesToolMessage: &ResponsesToolMessage{
			ToolsetName: Ptr("computer"),
			Action:      &ResponsesToolMessageActionStruct{ResponsesToolCallActionStr: Ptr("generate")},
			ResponsesComputerToolCall: &ResponsesComputerToolCall{PendingSafetyChecks: []ResponsesComputerToolCallPendingSafetyCheck{{
				ID: "check_1", Code: "confirm", Message: "confirm action",
			}}},
			ResponsesComputerToolCallOutput: &ResponsesComputerToolCallOutput{AcknowledgedSafetyChecks: []ResponsesComputerToolCallAcknowledgedSafetyCheck{{
				ID: "check_1", Code: Ptr("confirm"), Message: Ptr("approved"),
			}}},
			ResponsesCodeInterpreterToolCall: &ResponsesCodeInterpreterToolCall{
				Code:        Ptr("print('hello')"),
				ContainerID: "container_1",
				Outputs: []ResponsesCodeInterpreterOutput{{
					ResponsesCodeInterpreterOutputLogs: &ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: "hello"},
				}},
			},
			ResponsesMCPToolCall: &ResponsesMCPToolCall{ServerLabel: "repo"},
			ResponsesImageGenerationCall: &ResponsesImageGenerationCall{
				Result:        "image",
				Background:    Ptr("transparent"),
				OutputFormat:  Ptr("png"),
				Quality:       Ptr("high"),
				RevisedPrompt: Ptr("draw a cat"),
				Size:          Ptr("1024x1024"),
			},
			ResponsesMCPListTools: &ResponsesMCPListTools{ServerLabel: "repo", Tools: []ResponsesMCPTool{{
				Name:        "read_file",
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": "string"}},
				Description: Ptr("read a file"),
				Annotations: &map[string]any{"readOnlyHint": true},
			}}},
			ResponsesMCPApprovalResponse: &ResponsesMCPApprovalResponse{
				ApprovalResponseID: "approval_1", Approve: true, Reason: Ptr("allowed"),
			},
			ResponsesAdvisorCall: &ResponsesAdvisorCall{
				ResultType:       "advisor_result",
				Text:             Ptr("advice"),
				EncryptedContent: Ptr("encrypted"),
				ErrorCode:        Ptr("none"),
				StopReason:       Ptr("end_turn"),
			},
			ResponsesToolSearchCall: &ResponsesToolSearchCall{ToolReferences: []string{"read_file"}},
			ResponsesCodeExecutionCall: &ResponsesCodeExecutionCall{
				ToolName: "bash_code_execution",
				Input:    Ptr(`{"command":"pwd"}`),
				Lines:    []string{"line one"},
				Files:    []ResponsesCodeExecutionFileOutput{{FileID: "file_1"}},
				Caller:   &ResponsesToolCaller{Type: "code_execution_20260120", ToolID: Ptr("tool_1")},
			},
		},
	}

	copied := DeepCopyResponsesMessage(original)
	if !reflect.DeepEqual(original, copied) {
		t.Fatalf("deep copy lost Responses fields\noriginal: %#v\ncopied: %#v", original, copied)
	}
	if &original.ProviderNativeParts[0] == &copied.ProviderNativeParts[0] {
		t.Fatal("copy aliases provider native parts")
	}
	if original.Content.ContentBlocks[0].ResponsesInputMessageContentBlockFile.FileType == copied.Content.ContentBlocks[0].ResponsesInputMessageContentBlockFile.FileType {
		t.Fatal("copy aliases file type")
	}
	if original.ResponsesToolMessage.Action.ResponsesToolCallActionStr == copied.ResponsesToolMessage.Action.ResponsesToolCallActionStr {
		t.Fatal("copy aliases bare tool action")
	}
	if original.ResponsesToolMessage.ToolsetName == copied.ResponsesToolMessage.ToolsetName {
		t.Fatal("copy aliases toolset name")
	}

	copied.ProviderNativeParts[0] = '['
	copied.ResponsesToolMessage.ResponsesMCPListTools.Tools[0].InputSchema["type"] = "array"
	copied.ResponsesToolMessage.ResponsesCodeExecutionCall.Lines[0] = "changed"
	if string(original.ProviderNativeParts) != `{"thoughtSignature":"opaque"}` {
		t.Fatal("mutating copied provider native parts changed the original")
	}
	if original.ResponsesToolMessage.ResponsesMCPListTools.Tools[0].InputSchema["type"] != "object" {
		t.Fatal("mutating copied MCP schema changed the original")
	}
	if original.ResponsesToolMessage.ResponsesCodeExecutionCall.Lines[0] != "line one" {
		t.Fatal("mutating copied code execution lines changed the original")
	}
}

// TestDeepCopyResponsesMessagePreservesNilMCPAnnotations verifies a non-nil annotations pointer to a nil map is copied without panicking or aliasing.
func TestDeepCopyResponsesMessagePreservesNilMCPAnnotations(t *testing.T) {
	annotations := map[string]any(nil)
	original := ResponsesMessage{
		ResponsesToolMessage: &ResponsesToolMessage{
			ResponsesMCPListTools: &ResponsesMCPListTools{Tools: []ResponsesMCPTool{{Annotations: &annotations}}},
		},
	}

	copied := DeepCopyResponsesMessage(original)
	copyAnnotations := copied.ResponsesToolMessage.ResponsesMCPListTools.Tools[0].Annotations
	if copyAnnotations == nil {
		t.Fatal("copy dropped annotations pointer")
	}
	if copyAnnotations == original.ResponsesToolMessage.ResponsesMCPListTools.Tools[0].Annotations {
		t.Fatal("copy aliases annotations pointer")
	}
	if *copyAnnotations != nil {
		t.Fatal("copy changed nil annotations map")
	}
}

// TestDeepCopyResponsesMessageCopiesCodeExecutionPointers verifies code-execution pointer fields do not alias the original message.
func TestDeepCopyResponsesMessageCopiesCodeExecutionPointers(t *testing.T) {
	originalCall := &ResponsesCodeExecutionCall{
		Input:              Ptr(`{"command":"pwd"}`),
		Stdout:             Ptr("output"),
		Stderr:             Ptr("error"),
		ReturnCode:         Ptr(1),
		EncryptedStdout:    Ptr("encrypted"),
		FileType:           Ptr("text"),
		FileContent:        Ptr("contents"),
		StartLine:          Ptr(1),
		NumLines:           Ptr(2),
		TotalLines:         Ptr(3),
		IsFileUpdate:       Ptr(true),
		OldStart:           Ptr(4),
		OldLines:           Ptr(5),
		NewStart:           Ptr(6),
		NewLines:           Ptr(7),
		Lines:              []string{"before", "after"},
		ErrorCode:          Ptr("unavailable"),
		Files:              []ResponsesCodeExecutionFileOutput{{FileID: "file_1"}},
		ContainerExpiresAt: Ptr("2026-10-01T00:00:00Z"),
		Caller:             &ResponsesToolCaller{Type: "code_execution_20260120", ToolID: Ptr("tool_1")},
	}
	original := ResponsesMessage{ResponsesToolMessage: &ResponsesToolMessage{ResponsesCodeExecutionCall: originalCall}}

	copied := DeepCopyResponsesMessage(original)
	copiedCall := copied.ResponsesToolMessage.ResponsesCodeExecutionCall
	if !reflect.DeepEqual(originalCall, copiedCall) {
		t.Fatalf("deep copy changed code-execution call\noriginal: %#v\ncopied: %#v", originalCall, copiedCall)
	}

	for _, field := range []string{
		"Input", "Stdout", "Stderr", "ReturnCode", "EncryptedStdout", "FileType", "FileContent",
		"StartLine", "NumLines", "TotalLines", "IsFileUpdate", "OldStart", "OldLines", "NewStart",
		"NewLines", "ErrorCode", "ContainerExpiresAt",
	} {
		originalPointer := reflect.ValueOf(originalCall).Elem().FieldByName(field)
		copiedPointer := reflect.ValueOf(copiedCall).Elem().FieldByName(field)
		if originalPointer.Pointer() == copiedPointer.Pointer() {
			t.Fatalf("copy aliases code-execution %s", field)
		}
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
	}
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

// TestCustomToolInputDoneRoundTrip preserves the terminal input clients compare with streamed custom-tool deltas.
func TestCustomToolInputDoneRoundTrip(t *testing.T) {
	raw := []byte(`{"type":"response.custom_tool_call_input.done","item_id":"tool1","output_index":0,"input":"grep alice@example.com"}`)
	var response BifrostResponsesStreamResponse
	if err := Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(response.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(output, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["input"] != "grep alice@example.com" {
		t.Fatalf("terminal input lost: %s", output)
	}
}

// TestDeepCopyResponsesMessageCustomInput preserves custom input without sharing mutable tool state.
func TestDeepCopyResponsesMessageCustomInput(t *testing.T) {
	for _, input := range []string{"", "grep alice@example.com"} {
		t.Run(input, func(t *testing.T) {
			original := ResponsesMessage{
				Type: Ptr(ResponsesMessageTypeCustomToolCall),
				ResponsesToolMessage: &ResponsesToolMessage{
					Name:                    Ptr("bash"),
					ResponsesCustomToolCall: &ResponsesCustomToolCall{Input: input},
				},
			}
			copied := DeepCopyResponsesMessage(original)
			if copied.ResponsesToolMessage == nil || copied.ResponsesCustomToolCall == nil {
				t.Fatal("copy lost custom tool input")
			}
			if copied.ResponsesCustomToolCall.Input != input {
				t.Fatalf("input = %q, want %q", copied.ResponsesCustomToolCall.Input, input)
			}
			copied.ResponsesCustomToolCall.Input = "redacted"
			if original.ResponsesCustomToolCall.Input != input {
				t.Fatal("changing copied input mutated the original")
			}
		})
	}
}

// A per-part media resolution is replayed to the provider verbatim, so DeepCopyResponsesMessage
// must carry it across -- and must not alias it, since the copy and the original can be sent on
// different attempts of the same request.
func TestDeepCopyResponsesMessagePreservesMediaResolution(t *testing.T) {
	messageType := ResponsesMessageTypeMessage
	role := ResponsesInputMessageRoleUser
	imageURL := "data:image/jpeg;base64,/9j/4AAQSkZJRg=="
	numTokens := int32(512)

	original := ResponsesMessage{
		Type: &messageType,
		Role: &role,
		Content: &ResponsesMessageContent{
			ContentBlocks: []ResponsesMessageContentBlock{{
				Type:                                   ResponsesInputMessageContentBlockTypeImage,
				ResponsesInputMessageContentBlockImage: &ResponsesInputMessageContentBlockImage{ImageURL: &imageURL},
				MediaResolution:                        &MediaResolution{Level: "MEDIA_RESOLUTION_ULTRA_HIGH", NumTokens: &numTokens},
			}},
		},
	}

	copied := DeepCopyResponsesMessage(original)
	got := copied.Content.ContentBlocks[0].MediaResolution
	if got == nil {
		t.Fatal("deep copy dropped the media resolution")
	}
	if got.Level != "MEDIA_RESOLUTION_ULTRA_HIGH" {
		t.Fatalf("level = %q, want MEDIA_RESOLUTION_ULTRA_HIGH", got.Level)
	}
	if got == original.Content.ContentBlocks[0].MediaResolution {
		t.Error("copy aliases the original media resolution struct")
	}
	if got.NumTokens == nil {
		t.Fatal("deep copy dropped numTokens")
	}
	if got.NumTokens == original.Content.ContentBlocks[0].MediaResolution.NumTokens {
		t.Error("copy aliases the original numTokens pointer")
	}
	if *got.NumTokens != 512 {
		t.Fatalf("numTokens = %d, want 512", *got.NumTokens)
	}
}

// TestResponsesWebSearchSourceRoundTrip pins web_search_call action sources
// through a decode -> re-encode cycle. OpenAI's hosted web search can return
// specialized API sources ({"type":"api","name":"oai-weather"}) that carry a
// name and no URL; they must survive the round-trip without losing the name or
// fabricating an empty url.
func TestResponsesWebSearchSourceRoundTrip(t *testing.T) {
	roundTripSource := func(t *testing.T, raw string) map[string]any {
		t.Helper()
		var msg ResponsesMessage
		if err := Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("unmarshal web_search_call: %v", err)
		}
		encoded, err := MarshalSorted(msg)
		if err != nil {
			t.Fatalf("marshal web_search_call: %v", err)
		}
		var out struct {
			Action struct {
				Sources []map[string]any `json:"sources"`
			} `json:"action"`
		}
		if err := json.Unmarshal(encoded, &out); err != nil {
			t.Fatalf("unmarshal encoded web_search_call: %v", err)
		}
		if len(out.Action.Sources) != 1 {
			t.Fatalf("expected 1 source after round-trip, got %d (encoded: %s)", len(out.Action.Sources), encoded)
		}
		return out.Action.Sources[0]
	}

	t.Run("api source keeps name and gains no url", func(t *testing.T) {
		raw := `{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","queries":["weather in paris"],"sources":[{"type":"api","name":"oai-weather"}]}}`

		source := roundTripSource(t, raw)
		if source["type"] != "api" {
			t.Fatalf("expected source type %q, got %v", "api", source["type"])
		}
		if source["name"] != "oai-weather" {
			t.Fatalf("expected source name %q, got %v", "oai-weather", source["name"])
		}
		if _, ok := source["url"]; ok {
			t.Fatalf("expected no url key on an api source, got %v", source["url"])
		}
	})

	t.Run("url source round-trips unchanged", func(t *testing.T) {
		raw := `{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","queries":["weather in paris"],"sources":[{"type":"url","url":"https://example.com"}]}}`

		source := roundTripSource(t, raw)
		if source["type"] != "url" {
			t.Fatalf("expected source type %q, got %v", "url", source["type"])
		}
		if source["url"] != "https://example.com" {
			t.Fatalf("expected source url %q, got %v", "https://example.com", source["url"])
		}
		if _, ok := source["name"]; ok {
			t.Fatalf("expected no name key on a plain url source, got %v", source["name"])
		}
	})
}
