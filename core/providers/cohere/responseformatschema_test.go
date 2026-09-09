package cohere

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for Cohere structured output being dropped and then malformed.
//
// Cohere v2 spells structured output as
//
//	{"type": "json_object", "json_schema": {<raw JSON schema>}}
//
// (https://docs.cohere.com/reference/chat - "The model can be forced into outputting JSON
// objects by setting {\"type\": \"json_object\"}. A JSON Schema can optionally be provided").
//
// Two defects stacked on that path, both caught by harness cells 48.1.C / 48.2.C / 48.3.C /
// 48.5.C (the /langchain, /litellm, /pydanticai and /cohere drop-ins all route /v2/chat to the
// Cohere surface):
//
//  1. CohereResponseFormat tagged the field `schema`, not `json_schema`, so a real Cohere request
//     lost its schema silently. anthropic/claude-haiku-4-5 then answered with markdown-fenced
//     JSON - no schema was ever in force.
//  2. convertCohereResponseFormatToBifrost emitted {"type":"json_schema","json_schema":<raw>},
//     but the canonical form requires json_schema to be a WRAPPER {name, schema}. OpenAI rejected
//     it outright: "Missing required parameter: 'response_format.json_schema.name'".

const rawGreetingSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`

func TestCohereResponseFormat_BindsJSONSchemaField(t *testing.T) {
	var rf CohereResponseFormat
	require.NoError(t, json.Unmarshal([]byte(`{"type":"json_object","json_schema":`+rawGreetingSchema+`}`), &rf))

	assert.Equal(t, CohereResponseFormatType("json_object"), rf.Type)
	require.NotNil(t, rf.JSONSchema,
		"Cohere spells the field json_schema; binding only `schema` drops structured output silently")
}

// The older `schema` spelling must keep working so anything already sending it is unaffected.
func TestCohereResponseFormat_StillAcceptsSchemaAlias(t *testing.T) {
	var rf CohereResponseFormat
	require.NoError(t, json.Unmarshal([]byte(`{"type":"json_object","schema":`+rawGreetingSchema+`}`), &rf))
	require.NotNil(t, rf.JSONSchema, "the `schema` alias must remain accepted")
}

// The canonical Bifrost form wraps the schema; emitting the bare schema is rejected upstream.
func TestConvertCohereResponseFormatToBifrost_WrapsSchema(t *testing.T) {
	var raw interface{}
	require.NoError(t, json.Unmarshal([]byte(rawGreetingSchema), &raw))

	got := convertCohereResponseFormatToBifrost(&CohereResponseFormat{
		Type: CohereResponseFormatType("json_object"), JSONSchema: &raw,
	})
	require.NotNil(t, got)

	// Consumers type-assert this to map[string]interface{} - anthropic/utils.go
	// convertChatResponseFormatToTool silently returns nil on anything else, which meant
	// Anthropic applied no structured output at all and answered with fenced JSON.
	out, isMap := (*got).(map[string]interface{})
	require.True(t, isMap, "ResponseFormat must be a map like every other inbound path, got %T", *got)

	assert.Equal(t, "json_schema", out["type"])
	wrapper, ok := out["json_schema"].(map[string]interface{})
	require.True(t, ok, "json_schema must be a wrapper object, got %T", out["json_schema"])

	assert.NotEmpty(t, wrapper["name"], "OpenAI requires response_format.json_schema.name")
	inner, ok := wrapper["schema"].(map[string]interface{})
	require.True(t, ok, "the raw schema belongs under json_schema.schema")
	assert.Equal(t, "object", inner["type"])
	assert.Contains(t, inner, "properties")
}

// Plain JSON mode (no schema) must stay plain - wrapping nothing would be wrong.
func TestConvertCohereResponseFormatToBifrost_PlainJSONObjectUnchanged(t *testing.T) {
	got := convertCohereResponseFormatToBifrost(&CohereResponseFormat{
		Type: CohereResponseFormatType("json_object"),
	})
	require.NotNil(t, got)

	out, isMap := (*got).(map[string]interface{})
	require.True(t, isMap, "ResponseFormat must be a map, got %T", *got)
	assert.Equal(t, "json_object", out["type"])
	assert.NotContains(t, out, "json_schema")
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	if rm, ok := v.(json.RawMessage); ok {
		return string(rm)
	}
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// An explicit null is a choice, not a missing key. *interface{} decodes `"json_schema": null` to
// nil, so a plain nil check let the legacy `schema` alias silently override the caller's canonical
// spelling. Precedence is now keyed on presence.
func TestCohereResponseFormat_ExplicitNullJSONSchemaBeatsSchemaAlias(t *testing.T) {
	var rf CohereResponseFormat
	require.NoError(t, json.Unmarshal(
		[]byte(`{"type":"json_object","json_schema":null,"schema":`+rawGreetingSchema+`}`), &rf))

	assert.Nil(t, rf.JSONSchema,
		"an explicit json_schema:null must not be filled in from the legacy `schema` alias")
}

// The alias must still win when json_schema is genuinely absent - the fix must not disable it.
func TestCohereResponseFormat_AbsentJSONSchemaStillFallsBackToAlias(t *testing.T) {
	var rf CohereResponseFormat
	require.NoError(t, json.Unmarshal(
		[]byte(`{"type":"json_object","schema":`+rawGreetingSchema+`}`), &rf))

	require.NotNil(t, rf.JSONSchema, "with no json_schema key at all the alias is the only source")
}

// Both present and both real: the canonical spelling wins.
func TestCohereResponseFormat_CanonicalWinsOverAliasWhenBothPresent(t *testing.T) {
	var rf CohereResponseFormat
	require.NoError(t, json.Unmarshal(
		[]byte(`{"type":"json_object","json_schema":{"type":"string"},"schema":`+rawGreetingSchema+`}`), &rf))

	require.NotNil(t, rf.JSONSchema)
	m, ok := (*rf.JSONSchema).(map[string]interface{})
	require.True(t, ok, "the canonical json_schema value should be the one bound")
	assert.Equal(t, "string", m["type"], "json_schema must win over the legacy schema alias")
}
