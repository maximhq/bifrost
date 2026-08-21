package schemas

import (
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ChatResponseFormat is a reader over an OpenAI-style chat `response_format`
// value.
//
// The value is carried as raw JSON wherever possible (see
// ChatParameters.UnmarshalJSON): a gateway that forwards a schema unchanged
// should forward the client's bytes, not its own re-encoding of them. Only the
// providers that genuinely rewrite the schema pay for parsing, and they do it
// here, on demand.
//
// ChatParameters.ResponseFormat is an untyped interface{}, so the value may also
// be an *OrderedMap (built by the Responses to Chat conversion) or a plain
// map[string]interface{} (built by Go callers and plugins). All three are
// accepted; RawSchema is only byte-exact for the raw case, which is the case
// that comes off the wire.
type ChatResponseFormat struct {
	// Type is response_format.type: "text" | "json_object" | "json_schema".
	Type string

	// raw is the whole response_format object, empty unless it came off the wire.
	raw json.RawMessage
	// jsonSchema is the response_format.json_schema wrapper, parsed lazily for
	// the non-raw representations.
	jsonSchema *OrderedMap
}

// Name returns json_schema.name. The second return is false when the key is
// absent, which is distinct from an explicitly empty name.
func (f *ChatResponseFormat) Name() (string, bool) {
	return f.stringField("name")
}

// Description returns json_schema.description. The second return is false when
// the key is absent, which is distinct from an explicitly empty description.
func (f *ChatResponseFormat) Description() (string, bool) {
	return f.stringField("description")
}

// Strict returns json_schema.strict, or nil when absent.
func (f *ChatResponseFormat) Strict() *bool {
	if f == nil {
		return nil
	}
	if len(f.raw) > 0 {
		if result := gjson.GetBytes(f.raw, "json_schema.strict"); result.Exists() && result.IsBool() {
			return Ptr(result.Bool())
		}
		return nil
	}
	if f.jsonSchema == nil {
		return nil
	}
	if value, ok := f.jsonSchema.Get("strict"); ok {
		if strict, ok := value.(bool); ok {
			return &strict
		}
	}
	return nil
}

// HasJSONSchema reports whether a json_schema wrapper is present.
func (f *ChatResponseFormat) HasJSONSchema() bool {
	if f == nil {
		return false
	}
	if len(f.raw) > 0 {
		return gjson.GetBytes(f.raw, "json_schema").IsObject()
	}
	return f.jsonSchema != nil
}

// RawJSONSchema returns the response_format.json_schema wrapper as raw JSON, or
// nil when it is absent. The bytes are the client's own when the value came off
// the wire; otherwise they are re-encoded from the in-memory representation.
func (f *ChatResponseFormat) RawJSONSchema() json.RawMessage {
	if f == nil {
		return nil
	}
	if len(f.raw) > 0 {
		if result := gjson.GetBytes(f.raw, "json_schema"); result.IsObject() {
			return json.RawMessage(result.Raw)
		}
		return nil
	}
	if f.jsonSchema == nil {
		return nil
	}
	encoded, err := MarshalSorted(f.jsonSchema)
	if err != nil {
		return nil
	}
	return encoded
}

// RawSchema returns response_format.json_schema.schema as raw JSON, or nil when
// it is absent. Providers that forward the schema untouched should use this:
// it preserves key order and numeric literals exactly, which re-encoding cannot.
func (f *ChatResponseFormat) RawSchema() json.RawMessage {
	if f == nil {
		return nil
	}
	if len(f.raw) > 0 {
		if result := gjson.GetBytes(f.raw, "json_schema.schema"); result.Exists() {
			return json.RawMessage(result.Raw)
		}
		return nil
	}
	schema, ok := f.Schema()
	if !ok {
		return nil
	}
	encoded, err := MarshalSorted(schema)
	if err != nil {
		return nil
	}
	return encoded
}

// Schema returns response_format.json_schema.schema as a Go value. Prefer
// RawSchema unless the schema has to be walked or rebuilt.
func (f *ChatResponseFormat) Schema() (interface{}, bool) {
	if f == nil {
		return nil, false
	}
	if wrapper := f.wrapper(); wrapper != nil {
		return wrapper.Get("schema")
	}
	return nil, false
}

// SchemaMap returns response_format.json_schema.schema as an OrderedMap, parsing
// the raw bytes if needed. Boolean and missing schemas return false. Only the
// providers that rewrite the schema (Gemini normalization, the Anthropic tool
// fallback) should need this.
func (f *ChatResponseFormat) SchemaMap() (*OrderedMap, bool) {
	schema, ok := f.Schema()
	if !ok {
		return nil, false
	}
	return SafeExtractOrderedMap(schema)
}

// wrapper returns the json_schema object as an OrderedMap, decoding the raw form
// on first use.
func (f *ChatResponseFormat) wrapper() *OrderedMap {
	if f == nil {
		return nil
	}
	if f.jsonSchema == nil && len(f.raw) > 0 {
		result := gjson.GetBytes(f.raw, "json_schema")
		if !result.IsObject() {
			return nil
		}
		decoded := NewOrderedMap()
		if err := decoded.UnmarshalJSON([]byte(result.Raw)); err != nil {
			return nil
		}
		f.jsonSchema = decoded
	}
	return f.jsonSchema
}

func (f *ChatResponseFormat) stringField(key string) (string, bool) {
	if f == nil {
		return "", false
	}
	if len(f.raw) > 0 {
		result := gjson.GetBytes(f.raw, "json_schema."+key)
		if !result.Exists() || result.Type != gjson.String {
			return "", false
		}
		return result.String(), true
	}
	if f.jsonSchema == nil {
		return "", false
	}
	if value, ok := f.jsonSchema.Get(key); ok {
		if str, ok := SafeExtractString(value); ok {
			return str, true
		}
	}
	return "", false
}

// ParseChatResponseFormat reads ChatParameters.ResponseFormat, accepting the raw
// JSON produced by the wire decode as well as the *OrderedMap and plain map
// forms built in Go. It returns false when the value is absent or is not an
// object with a string `type`.
func ParseChatResponseFormat(responseFormat *interface{}) (*ChatResponseFormat, bool) {
	if responseFormat == nil || *responseFormat == nil {
		return nil, false
	}

	if raw, ok := rawJSONBytes(*responseFormat); ok {
		result := gjson.GetBytes(raw, "type")
		if result.Type != gjson.String {
			return nil, false
		}
		return &ChatResponseFormat{Type: result.String(), raw: raw}, true
	}

	formatMap, ok := SafeExtractOrderedMap(*responseFormat)
	if !ok || formatMap == nil {
		return nil, false
	}
	typeRaw, ok := formatMap.Get("type")
	if !ok {
		return nil, false
	}
	formatType, ok := SafeExtractString(typeRaw)
	if !ok {
		return nil, false
	}

	parsed := &ChatResponseFormat{Type: formatType}
	if jsonSchemaRaw, ok := formatMap.Get("json_schema"); ok {
		if jsonSchema, ok := SafeExtractOrderedMap(jsonSchemaRaw); ok {
			parsed.jsonSchema = jsonSchema
		}
	}
	return parsed, true
}

// rawJSONBytes reports whether the value is raw JSON carrying an object.
func rawJSONBytes(value interface{}) (json.RawMessage, bool) {
	var raw []byte
	switch typed := value.(type) {
	case json.RawMessage:
		raw = typed
	case *json.RawMessage:
		if typed == nil {
			return nil, false
		}
		raw = *typed
	case []byte:
		raw = typed
	default:
		return nil, false
	}
	if !gjson.ParseBytes(raw).IsObject() {
		return nil, false
	}
	return raw, true
}

// GetFormat returns the text format config, or nil when unset. Safe on a nil
// receiver so callers can chain it off optional parameters.
func (t *ResponsesTextConfig) GetFormat() *ResponsesTextConfigFormat {
	if t == nil {
		return nil
	}
	return t.Format
}

// ChatResponseFormatFromResponsesFormat converts a Responses API text.format
// config into the chat-completions `response_format` value, so a Responses
// request routed to a chat-completions provider keeps its structured output.
// Returns nil when there is no format to carry.
//
// The result is raw JSON, matching what the chat wire decode produces, so
// providers downstream forward one representation rather than two. The schema
// body is spliced in as bytes (sjson.SetRawBytes), which keeps its key order
// without a map round-trip.
func ChatResponseFormatFromResponsesFormat(format *ResponsesTextConfigFormat) *interface{} {
	if format == nil {
		return nil
	}

	body, err := sjson.SetBytes([]byte("{}"), "type", format.Type)
	if err != nil {
		return nil
	}

	if format.Type == "json_schema" {
		// Written in the order OpenAI documents for response_format.json_schema.
		wrapper := []byte("{}")
		if format.Name != nil {
			wrapper, _ = sjson.SetBytes(wrapper, "name", *format.Name)
		}
		if format.Description != nil {
			wrapper, _ = sjson.SetBytes(wrapper, "description", *format.Description)
		}
		if schema := format.JSONSchema.RawSchemaJSON(); len(schema) > 0 {
			wrapper, _ = sjson.SetRawBytes(wrapper, "schema", schema)
		}
		if format.Strict != nil {
			wrapper, _ = sjson.SetBytes(wrapper, "strict", *format.Strict)
		}
		body, _ = sjson.SetRawBytes(body, "json_schema", wrapper)
	}

	var responseFormat interface{} = json.RawMessage(body)
	return &responseFormat
}

// ResponsesTextConfigFormatFromChatResponseFormat converts a chat-completions
// `response_format` into the Responses API text.format config. It is the inverse
// of ChatResponseFormatFromResponsesFormat, and providers that speak the
// Responses shape use it so a chat request keeps its structured output.
// Returns nil when there is nothing usable to carry.
func ResponsesTextConfigFormatFromChatResponseFormat(responseFormat *interface{}) *ResponsesTextConfigFormat {
	rf, ok := ParseChatResponseFormat(responseFormat)
	if !ok {
		return nil
	}

	format := &ResponsesTextConfigFormat{Type: rf.Type}
	if rf.Type != "json_schema" {
		return format
	}

	if !rf.HasJSONSchema() {
		return nil
	}
	if name, ok := rf.Name(); ok {
		format.Name = &name
	}
	if description, ok := rf.Description(); ok {
		format.Description = &description
	}
	format.Strict = rf.Strict()
	if schema, ok := rf.Schema(); ok {
		format.JSONSchema = JSONSchemaFromMap(schema)
	}
	return format
}
