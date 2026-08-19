package schemas_test

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChatParametersResponseFormatDecodeKeepsClientBytes checks the decoded
// in-memory representation, so a failure points at the decoder rather than at
// the encoder. response_format is held as raw JSON: the gateway has no reason to
// re-encode a schema it only forwards, and re-encoding is what loses key order
// and numeric precision.
func TestChatParametersResponseFormatDecodeKeepsClientBytes(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))
	require.NotNil(t, params.ResponseFormat)

	raw, ok := (*params.ResponseFormat).(json.RawMessage)
	require.True(t, ok, "response_format must decode to raw JSON, got %T", *params.ResponseFormat)

	rf, ok := schemas.ParseChatResponseFormat(params.ResponseFormat)
	require.True(t, ok)
	assert.Equal(t, "json_schema", rf.Type)
	name, ok := rf.Name()
	require.True(t, ok)
	assert.Equal(t, "AssignmentResult", name)
	require.NotNil(t, rf.Strict())
	assert.True(t, *rf.Strict())

	assert.Contains(t, string(raw), schemaorder.ByteExactSchema, "the client's response_format bytes must be kept verbatim")
	assert.Equal(t, schemaorder.ByteExactSchema, string(rf.RawSchema()), "schema bytes must survive the decode untouched")
}

// TestChatParametersResponseFormatParsesOnDemand covers the structural view the
// rewriting providers (Gemini normalization, the Anthropic tool fallback) rely
// on: parsing the raw bytes must yield the client's key order at every level.
func TestChatParametersResponseFormatParsesOnDemand(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	rf, ok := schemas.ParseChatResponseFormat(params.ResponseFormat)
	require.True(t, ok)

	schema, ok := rf.SchemaMap()
	require.True(t, ok)
	assert.Equal(t, schemaorder.SchemaKeyOrder, schema.Keys())

	props, ok := schemas.SafeExtractOrderedMap(mustGet(t, schema, "properties"))
	require.True(t, ok)
	assert.Equal(t, schemaorder.PropertyOrder, props.Keys())
}

// TestChatParametersResponseFormatRoundTripsInOrder pins the encode side: an
// unmarshal/marshal round-trip must return the schema in the declared order.
func TestChatParametersResponseFormatRoundTripsInOrder(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	out, err := schemas.MarshalSorted(params)
	require.NoError(t, err)

	schemaorder.AssertPropertyOrder(t, string(out))
	schemaorder.AssertSchemaKeyOrder(t, string(out), "schema")
}

// TestChatParametersResponseFormatAcceptsPlainMaps guards backwards
// compatibility: plugins and Go callers build response_format as a plain map,
// which has no order to preserve but must still serialize and convert.
func TestChatParametersResponseFormatAcceptsPlainMaps(t *testing.T) {
	var rf interface{} = map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name": "AssignmentResult",
			"schema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"reasoning": map[string]interface{}{"type": "string"}},
			},
		},
	}
	params := schemas.ChatParameters{ResponseFormat: &rf}

	out, err := schemas.MarshalSorted(params)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"reasoning"`)

	cr := chatRequestWith(&params)
	rr := cr.ToResponsesRequest()
	require.NotNil(t, rr.Params)
	require.NotNil(t, rr.Params.Text)
	require.NotNil(t, rr.Params.Text.Format)
	assert.Equal(t, "json_schema", rr.Params.Text.Format.Type)
}

// TestResponseFormatChatToResponsesKeepsOrder guards the conversion path: mux
// reads response_format by type-asserting the decoded value, so an
// order-preserving representation must not break json_schema extraction.
func TestResponseFormatChatToResponsesKeepsOrder(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	rr := chatRequestWith(&params).ToResponsesRequest()
	require.NotNil(t, rr.Params)
	require.NotNil(t, rr.Params.Text)
	require.NotNil(t, rr.Params.Text.Format)
	assert.Equal(t, "json_schema", rr.Params.Text.Format.Type)
	require.NotNil(t, rr.Params.Text.Format.Name)
	assert.Equal(t, "AssignmentResult", *rr.Params.Text.Format.Name)

	out, err := schemas.MarshalSorted(rr.Params.Text.Format)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(out))
}

// TestResponseFormatResponsesToChatKeepsOrder covers the reverse conversion,
// used when a Responses request is routed to a chat-completions provider.
func TestResponseFormatResponsesToChatKeepsOrder(t *testing.T) {
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))
	require.NotNil(t, params.Text)

	rr := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4-nano",
		Input:    []schemas.ResponsesMessage{},
		Params:   &params,
	}

	cr := rr.ToChatRequest()
	require.NotNil(t, cr.Params)
	require.NotNil(t, cr.Params.ResponseFormat)

	out, err := schemas.MarshalSorted(cr.Params)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(out))
	schemaorder.AssertSchemaKeyOrder(t, string(out), "schema")
}

// TestResponsesSchemaToMapDoesNotMutateSource guards a sharp edge: OrderedMap
// key sorting recurses into nested maps and mutates them in place, so a
// conversion that sorts its own keys must not reach into the request's schema.
func TestResponsesSchemaToMapDoesNotMutateSource(t *testing.T) {
	props := schemas.NewOrderedMapFromPairs(
		schemas.KV("reasoning", map[string]any{"type": "string"}),
		schemas.KV("assigned_group_id", map[string]any{"type": "integer"}),
	)
	schema := &schemas.ResponsesTextConfigFormatJSONSchema{
		Type:       schemas.Ptr("object"),
		Properties: props,
		Required:   []string{"reasoning", "assigned_group_id"},
	}

	converted := schema.ToMap()
	require.NotNil(t, converted)

	assert.Equal(t, schemaorder.PropertyOrder, props.Keys(), "ToMap must not reorder the source properties")
	assert.Equal(t, schemaorder.PropertyOrder, schema.Properties.Keys())

	out, err := schemas.MarshalSorted(converted)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(out))
}

func chatRequestWith(params *schemas.ChatParameters) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.4-nano",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: params,
	}
}

func mustGet(t *testing.T, om *schemas.OrderedMap, key string) interface{} {
	t.Helper()
	v, ok := om.Get(key)
	require.True(t, ok, "expected key %q", key)
	return v
}
