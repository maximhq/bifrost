package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Issue #5279 — tool_search_tool_* must normalize to the canonical tool_search
// =============================================================================

// TestResponsesToolUnmarshalNormalizesToolSearchType locks in the fix for #5279.
// A Responses request declaring Anthropic's tool-search meta-tool by its dated
// type (tool_search_tool_regex_20251119 / tool_search_tool_bm25_20251119) used
// to fall through normalizeResponsesToolType unchanged, so every downstream
// switch comparing against ResponsesToolTypeToolSearch missed and the tool was
// downcast to a plain custom tool. Anthropic then returned a bare tool_use and
// never ran the server-side search.
//
// Cite: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool
// ("Tool definition" — the two variants are tool_search_tool_regex_20251119 and
// tool_search_tool_bm25_20251119, each declared with a matching `name`).
func TestResponsesToolUnmarshalNormalizesToolSearchType(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string // the variant discriminator convertBifrostToolToAnthropic reads
	}{
		{
			name:     "regex dated variant with name (doc canonical shape)",
			input:    `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}`,
			wantName: "tool_search_tool_regex",
		},
		{
			name:     "bm25 dated variant with name (doc canonical shape)",
			input:    `{"type":"tool_search_tool_bm25_20251119","name":"tool_search_tool_bm25"}`,
			wantName: "tool_search_tool_bm25",
		},
		{
			// Anthropic's own Go and C# SDK examples construct the tool with a
			// type and no name. Without a Name backfill the variant is lost and
			// the request silently downgrades regex -> bm25, which per the doc
			// means the model is handed a natural-language query grammar when it
			// expects Python re.search() patterns.
			name:     "regex dated variant without name backfills the variant",
			input:    `{"type":"tool_search_tool_regex_20251119"}`,
			wantName: "tool_search_tool_regex",
		},
		{
			name:     "bm25 dated variant without name backfills the variant",
			input:    `{"type":"tool_search_tool_bm25_20251119"}`,
			wantName: "tool_search_tool_bm25",
		},
		{
			name:     "undated regex variant",
			input:    `{"type":"tool_search_tool_regex","name":"tool_search_tool_regex"}`,
			wantName: "tool_search_tool_regex",
		},
		{
			name:     "undated bm25 variant",
			input:    `{"type":"tool_search_tool_bm25","name":"tool_search_tool_bm25"}`,
			wantName: "tool_search_tool_bm25",
		},
		{
			// A future dated variant must still normalize rather than silently
			// becoming a custom tool — that forward-compat gap is the bug.
			name:     "unknown future dated variant still normalizes",
			input:    `{"type":"tool_search_tool_regex_20991231"}`,
			wantName: "tool_search_tool_regex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tool ResponsesTool
			require.NoError(t, Unmarshal([]byte(tt.input), &tool))

			assert.Equal(t, ResponsesToolTypeToolSearch, tool.Type,
				"dated tool_search type must normalize to the canonical tool_search")
			require.NotNil(t, tool.Name, "name must be present so the regex/bm25 variant survives")
			assert.Equal(t, tt.wantName, *tool.Name)
			assert.NotNil(t, tool.ResponsesToolToolSearch,
				"normalizing must also populate the embedded tool_search struct")
		})
	}
}

// TestResponsesToolUnmarshalToolSearchExplicitNameWins guards the Name backfill
// from clobbering a name the caller actually sent.
func TestResponsesToolUnmarshalToolSearchExplicitNameWins(t *testing.T) {
	var tool ResponsesTool
	require.NoError(t, Unmarshal(
		[]byte(`{"type":"tool_search_tool_bm25_20251119","name":"my_custom_search_name"}`), &tool))

	assert.Equal(t, ResponsesToolTypeToolSearch, tool.Type)
	require.NotNil(t, tool.Name)
	assert.Equal(t, "my_custom_search_name", *tool.Name,
		"an explicit name must never be overwritten by the variant backfill")
}

// TestResponsesToolUnmarshalCanonicalToolSearchUnchanged verifies the canonical
// OpenAI-shaped tool_search (execution/parameters) is untouched by the
// normalization guard — it must not be re-normalized or gain a backfilled name.
func TestResponsesToolUnmarshalCanonicalToolSearchUnchanged(t *testing.T) {
	var tool ResponsesTool
	require.NoError(t, Unmarshal(
		[]byte(`{"type":"tool_search","execution":"client","parameters":{"type":"object"}}`), &tool))

	assert.Equal(t, ResponsesToolTypeToolSearch, tool.Type)
	assert.Nil(t, tool.Name, "canonical tool_search carries no variant to backfill")
	require.NotNil(t, tool.ResponsesToolToolSearch)
	require.NotNil(t, tool.ResponsesToolToolSearch.Execution)
	assert.Equal(t, "client", *tool.ResponsesToolToolSearch.Execution)
	assert.NotNil(t, tool.ResponsesToolToolSearch.Parameters)
}

// =============================================================================
// Decode-path guard for the gjson refactor of ResponsesTool.UnmarshalJSON
// =============================================================================

// TestResponsesToolUnmarshalPreservesCommonFields pins every common field the
// decoder reads. ResponsesTool.UnmarshalJSON used to decode into a
// map[string]interface{} and then re-marshal cache_control / allowed_callers /
// input_examples back to bytes to convert them into their typed form; it now
// reads them with gjson directly. This test is the behavioural contract that
// refactor must not change.
func TestResponsesToolUnmarshalPreservesCommonFields(t *testing.T) {
	raw := []byte(`{
		"type": "function",
		"name": "get_weather",
		"description": "Get the weather",
		"cache_control": {"type": "ephemeral", "ttl": "1h"},
		"defer_loading": true,
		"allowed_callers": ["direct", "code_execution_20250825"],
		"input_examples": [
			{"input": {"location": "SF"}, "description": "basic lookup"},
			{"input": {"location": "NYC", "unit": "celsius"}}
		],
		"eager_input_streaming": true,
		"parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]},
		"strict": true
	}`)

	var tool ResponsesTool
	require.NoError(t, Unmarshal(raw, &tool))

	assert.Equal(t, ResponsesToolTypeFunction, tool.Type)
	require.NotNil(t, tool.Name)
	assert.Equal(t, "get_weather", *tool.Name)
	require.NotNil(t, tool.Description)
	assert.Equal(t, "Get the weather", *tool.Description)

	require.NotNil(t, tool.CacheControl)
	assert.Equal(t, CacheControlType("ephemeral"), tool.CacheControl.Type)
	require.NotNil(t, tool.CacheControl.TTL)
	assert.Equal(t, "1h", *tool.CacheControl.TTL)

	require.NotNil(t, tool.DeferLoading)
	assert.True(t, *tool.DeferLoading)

	assert.Equal(t, []string{"direct", "code_execution_20250825"}, tool.AllowedCallers)

	require.Len(t, tool.InputExamples, 2)
	assert.JSONEq(t, `{"location":"SF"}`, string(tool.InputExamples[0].Input))
	require.NotNil(t, tool.InputExamples[0].Description)
	assert.Equal(t, "basic lookup", *tool.InputExamples[0].Description)
	assert.JSONEq(t, `{"location":"NYC","unit":"celsius"}`, string(tool.InputExamples[1].Input))
	assert.Nil(t, tool.InputExamples[1].Description)

	require.NotNil(t, tool.EagerInputStreaming)
	assert.True(t, *tool.EagerInputStreaming)

	require.NotNil(t, tool.ResponsesToolFunction)
	require.NotNil(t, tool.ResponsesToolFunction.Parameters)
	assert.Equal(t, []string{"location"}, tool.ResponsesToolFunction.Parameters.Required)
	require.NotNil(t, tool.ResponsesToolFunction.Strict)
	assert.True(t, *tool.ResponsesToolFunction.Strict)
}

// TestResponsesToolUnmarshalOmitsAbsentCommonFields verifies absent optional
// fields stay nil rather than being materialized as zero values.
func TestResponsesToolUnmarshalOmitsAbsentCommonFields(t *testing.T) {
	var tool ResponsesTool
	require.NoError(t, Unmarshal([]byte(`{"type":"function","name":"noop"}`), &tool))

	assert.Nil(t, tool.Description)
	assert.Nil(t, tool.CacheControl)
	assert.Nil(t, tool.DeferLoading)
	assert.Nil(t, tool.AllowedCallers)
	assert.Nil(t, tool.InputExamples)
	assert.Nil(t, tool.EagerInputStreaming)
}

// TestResponsesToolUnmarshalIgnoresWrongTypedScalars pins the tolerant reads the
// map-based decoder had: `raw["name"].(string)` and `raw["defer_loading"].(bool)`
// silently skipped values of the wrong JSON type rather than erroring. Clients in
// the wild send `"defer_loading": "true"`; that must stay a no-op, not a 400.
func TestResponsesToolUnmarshalIgnoresWrongTypedScalars(t *testing.T) {
	raw := []byte(`{
		"type": "function",
		"name": 42,
		"description": false,
		"defer_loading": "true",
		"eager_input_streaming": 1
	}`)

	var tool ResponsesTool
	require.NoError(t, Unmarshal(raw, &tool), "wrong-typed scalars must be skipped, not rejected")

	assert.Nil(t, tool.Name, "non-string name must be ignored")
	assert.Nil(t, tool.Description, "non-string description must be ignored")
	assert.Nil(t, tool.DeferLoading, "non-bool defer_loading must be ignored")
	assert.Nil(t, tool.EagerInputStreaming, "non-bool eager_input_streaming must be ignored")
}

// TestResponsesToolUnmarshalTypeFieldErrors pins the two type-field error paths
// and the malformed-JSON path. gjson does not validate its input, so dropping
// the map decode must not turn malformed JSON into a silent success.
func TestResponsesToolUnmarshalTypeFieldErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"missing type", `{"name":"foo"}`, "missing required 'type' field"},
		{"numeric type", `{"type":123}`, "'type' field must be a string"},
		{"null type", `{"type":null}`, "'type' field must be a string"},
		{"object type", `{"type":{"a":1}}`, "'type' field must be a string"},
		{"malformed json", `{"type":"function"`, ""},
		{"not an object", `"just a string"`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tool ResponsesTool
			err := tool.UnmarshalJSON([]byte(tt.input))
			require.Error(t, err, "expected an error for input %s", tt.input)
			if tt.wantErr != "" {
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

// TestResponsesToolUnmarshalNullFunctionWrapper pins a subtle behaviour of the
// map decoder: `{"function": null}` has the key present but decodes to a nil
// wrapper, so it lifts nothing and does NOT error. Only a non-null,
// non-object `function` is malformed (see
// TestResponsesToolUnmarshalRejectsMalformedFunctionWrapper).
func TestResponsesToolUnmarshalNullFunctionWrapper(t *testing.T) {
	var tool ResponsesTool
	require.NoError(t, tool.UnmarshalJSON([]byte(`{"type":"function","name":"top","function":null}`)))

	assert.Equal(t, ResponsesToolTypeFunction, tool.Type)
	require.NotNil(t, tool.Name)
	assert.Equal(t, "top", *tool.Name)
	assert.NotNil(t, tool.ResponsesToolFunction)
}

// TestResponsesToolMarshalUnmarshalRoundTrip walks every tool type through
// Unmarshal -> MarshalSorted and asserts the JSON is preserved. This is the
// broadest guard on the decoder refactor: if any type's embedded struct stops
// being populated, its fields vanish from the re-marshaled output.
func TestResponsesToolMarshalUnmarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" means: must equal `in`
	}{
		{
			name: "function",
			in:   `{"type":"function","name":"f","parameters":{"type":"object"},"strict":true}`,
			// ToolFunctionParameters always emits an (empty) properties object;
			// pre-existing behaviour, unrelated to the decoder.
			want: `{"type":"function","name":"f","parameters":{"type":"object","properties":{}},"strict":true}`,
		},
		{name: "custom", in: `{"type":"custom","name":"c","description":"d"}`},
		{name: "file_search", in: `{"type":"file_search","vector_store_ids":["vs_1"]}`},
		{name: "mcp", in: `{"type":"mcp","server_label":"srv","server_url":"https://example.com"}`},
		{name: "local_shell", in: `{"type":"local_shell"}`},
		{name: "image_generation", in: `{"type":"image_generation"}`},
		{name: "web_search canonical", in: `{"type":"web_search","max_uses":3}`},
		{
			name: "web_search dated normalizes",
			in:   `{"type":"web_search_20250305","max_uses":3}`,
			want: `{"type":"web_search","max_uses":3}`,
		},
		{
			name: "memory dated normalizes",
			in:   `{"type":"memory_20250818","name":"memory"}`,
			want: `{"type":"memory","name":"memory"}`,
		},
		{
			name: "tool_search dated normalizes",
			in:   `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}`,
			want: `{"type":"tool_search","name":"tool_search_tool_regex"}`,
		},
		{
			name: "anthropic flags survive",
			in:   `{"type":"function","name":"f","defer_loading":true,"allowed_callers":["direct"],"eager_input_streaming":true,"parameters":{"type":"object"},"strict":false}`,
			want: `{"type":"function","name":"f","defer_loading":true,"allowed_callers":["direct"],"eager_input_streaming":true,"parameters":{"type":"object","properties":{}},"strict":false}`,
		},
		{
			name: "cache_control survives",
			in:   `{"type":"custom","name":"c","cache_control":{"type":"ephemeral"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tool ResponsesTool
			require.NoError(t, Unmarshal([]byte(tt.in), &tool))

			out, err := MarshalSorted(tool)
			require.NoError(t, err)

			want := tt.want
			if want == "" {
				want = tt.in
			}
			assert.JSONEq(t, want, string(out))
		})
	}
}
