package bedrock

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestResponseFormatOrderSurvivesBedrockConverse covers Bedrock, where
// structured output is rewritten into a synthetic tool's input schema.
func TestResponseFormatOrderSurvivesBedrockConverse(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToBedrockChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-20250514-v1:0",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.NotNil(t, out.ToolConfig, "structured output must survive as a tool")

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}

// TestResponseFormatSchemaBytesReachBedrockVerbatim: the schema becomes a tool's
// input schema, which Bedrock carries as raw JSON, so no re-encoding is needed.
func TestResponseFormatSchemaBytesReachBedrockVerbatim(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToBedrockChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-20250514-v1:0",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertSchemaBytes(t, string(raw), "json")
}

// TestResponseFormatOrderSurvivesBedrockResponses covers the Responses method.
func TestResponseFormatOrderSurvivesBedrockResponses(t *testing.T) {
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToBedrockResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-20250514-v1:0",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}

// TestResponseFormatSchemaBytesReachBedrockNativeVerbatim: on the native path the schema
// travels as outputConfig's JSON string, still byte for byte when no rewrite is needed.
func TestResponseFormatSchemaBytesReachBedrockNativeVerbatim(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToBedrockChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "us.openai.gpt-6-luna",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.Nil(t, out.ToolConfig, "native structured output must not add the synthetic tool")
	require.Equal(t, schemaorder.ByteExactSchema, gjson.GetBytes(out.OutputConfig, "textFormat.structure.jsonSchema.schema").String())
}

// TestResponseFormatOrderSurvivesBedrockNative covers both methods on a Claude model that takes
// the schema natively.
func TestResponseFormatOrderSurvivesBedrockNative(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	var chatParams schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &chatParams))
	chatOut, err := ToBedrockChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &chatParams,
	})
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, gjson.GetBytes(chatOut.OutputConfig, "textFormat.structure.jsonSchema.schema").String())

	var responsesParams schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &responsesParams))
	responsesOut, err := ToBedrockResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &responsesParams,
	})
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, gjson.GetBytes(responsesOut.OutputConfig, "textFormat.structure.jsonSchema.schema").String())
}

// TestPrepareClaudeNativeSchema pins what Claude structured output accepts on Bedrock (each
// rejected case returned a 400 live), so a schema Claude would refuse keeps the synthetic tool.
func TestPrepareClaudeNativeSchema(t *testing.T) {
	closed := func(props string) string {
		return `{"type":"object","properties":` + props + `,"required":[],"additionalProperties":false}`
	}
	optional := func(n int) string {
		props := make([]string, n)
		for i := range props {
			props[i] = `"p` + strconv.Itoa(i) + `":{"type":"string"}`
		}
		return closed("{" + strings.Join(props, ",") + "}")
	}
	nullable := func(n int) string {
		props := make([]string, n)
		required := make([]string, n)
		for i := range props {
			props[i] = `"p` + strconv.Itoa(i) + `":{"type":["string","null"]}`
			required[i] = `"p` + strconv.Itoa(i) + `"`
		}
		return `{"type":"object","properties":{` + strings.Join(props, ",") + `},"required":[` + strings.Join(required, ",") + `],"additionalProperties":false}`
	}

	accepted := map[string]string{
		"closed object":                    closed(`{"a":{"type":"string"}}`),
		"string lengths":                   closed(`{"a":{"type":"string","minLength":1,"maxLength":9}}`),
		"enum, const, format":              closed(`{"a":{"enum":["x",1,null]},"b":{"const":"x"},"c":{"type":"string","format":"date-time"}}`),
		"pattern":                          closed(`{"a":{"type":"string","pattern":"^[a-z]+$"}}`),
		"minItems 1":                       closed(`{"a":{"type":"array","items":{"type":"string"},"minItems":1}}`),
		"nullable unions":                  closed(`{"a":{"type":["string","null"]},"b":{"anyOf":[{"type":"string"},{"type":"null"}]}}`),
		"allOf and if":                     closed(`{"a":{"allOf":[{"type":"string"}],"if":{"const":"x"},"then":{"const":"x"}}}`),
		"local ref":                        `{"type":"object","properties":{"a":{"$ref":"#/$defs/X"}},"required":["a"],"additionalProperties":false,"$defs":{"X":` + closed(`{"x":{"type":"string"}}`) + `}}`,
		"empty closed object":              closed(`{"a":{"type":"object","properties":{},"additionalProperties":false}}`),
		"top-level array":                  `{"type":"array","items":{"type":"string"}}`,
		"10 optional":                      optional(10),
		"allOf with a single $ref":         `{"type":"object","properties":{"a":{"allOf":[{"$ref":"#/$defs/X"}],"description":"pydantic v1"}},"required":["a"],"additionalProperties":false,"$defs":{"X":` + closed(`{"x":{"type":"string"}}`) + `}}`,
		"allOf without sibling properties": `{"allOf":[` + closed(`{"a":{"type":"string"}}`) + `,` + closed(`{"b":{"type":"string"}}`) + `]}`,
		"string-typed if":                  closed(`{"a":{"type":"string","if":{"const":"x"},"then":{"const":"x"}}}`),
		"definitions ref":                  `{"type":"object","properties":{"a":{"$ref":"#/definitions/X"}},"required":["a"],"additionalProperties":false,"definitions":{"X":` + closed(`{"x":{"type":"string"}}`) + `}}`,
		"dotted def name":                  `{"type":"object","properties":{"a":{"$ref":"#/$defs/x.y"}},"required":["a"],"additionalProperties":false,"$defs":{"x.y":` + closed(`{"x":{"type":"string"}}`) + `}}`,
		"16 unions":                        nullable(16),
	}
	for name, schema := range accepted {
		t.Run("accepted/"+name, func(t *testing.T) {
			out, ok := prepareClaudeNativeSchema([]byte(schema), claudeSchemaLoad{})
			require.True(t, ok)
			require.Equal(t, schema, string(out), "a schema that needs no closing must pass through untouched")
		})
	}

	rejected := map[string]string{
		"additionalProperties true":   `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":true}`,
		"additionalProperties schema": closed(`{"a":{"type":"object","additionalProperties":{"type":"string"}}}`),
		"free-form object":            closed(`{"a":{"type":"object"}}`),
		"minimum":                     closed(`{"a":{"type":"integer","minimum":1}}`),
		"exclusiveMinimum":            closed(`{"a":{"type":"number","exclusiveMinimum":0}}`),
		"multipleOf":                  closed(`{"a":{"type":"integer","multipleOf":2}}`),
		"maxItems":                    closed(`{"a":{"type":"array","items":{"type":"string"},"maxItems":3}}`),
		"minItems 2":                  closed(`{"a":{"type":"array","items":{"type":"string"},"minItems":2}}`),
		"uniqueItems":                 closed(`{"a":{"type":"array","items":{"type":"string"},"uniqueItems":true}}`),
		"tuple items":                 closed(`{"a":{"type":"array","items":[{"type":"string"}]}}`),
		"oneOf":                       closed(`{"a":{"oneOf":[{"type":"string"},{"type":"integer"}]}}`),
		"not":                         closed(`{"a":{"type":"string","not":{"const":"x"}}}`),
		"unknown format":              closed(`{"a":{"type":"string","format":"phone"}}`),
		"lookahead pattern":           closed(`{"a":{"type":"string","pattern":"^(?=a)[a-z]+$"}}`),
		"object enum":                 closed(`{"a":{"enum":[{"x":1}]}}`),
		"minProperties":               `{"type":"object","properties":{"a":{"type":"string"}},"minProperties":1,"additionalProperties":false}`,
		"recursive ref":               `{"type":"object","properties":{"a":{"$ref":"#/$defs/N"}},"additionalProperties":false,"$defs":{"N":{"type":"object","properties":{"kids":{"type":"array","items":{"$ref":"#/$defs/N"}}},"additionalProperties":false}}}`,
		"root ref":                    closed(`{"self":{"$ref":"#"}}`),
		"11 optional":                 optional(11),
		// Live: "Cannot merge allOf schemas" when a branch sits beside the node's own properties.
		"allOf beside properties": `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"additionalProperties":false,"allOf":[{"type":"object","properties":{"a":{"type":"string"}}}]}`,
		// Live: "Cannot merge allOf schemas: schema 1 contains $ref" whenever several branches merge.
		"allOf merging a $ref": `{"type":"object","properties":{"a":{"allOf":[` + closed(`{"y":{"type":"string"}}`) + `,{"$ref":"#/$defs/X"}]}},"required":["a"],"additionalProperties":false,"$defs":{"X":` + closed(`{"x":{"type":"string"}}`) + `}}`,
		// Live: "For 'object' type, property 'if'/'then' is not supported".
		"object if":   `{"type":"object","properties":{"k":{"type":"string"}},"required":["k"],"additionalProperties":false,"if":{"properties":{"k":{"const":"x"}}},"then":{"required":["k"]}}`,
		"object then": `{"type":"object","properties":{"k":{"type":"string"}},"required":["k"],"additionalProperties":false,"then":` + closed(`{"k":{"type":"string"}}`) + `}`,
		// Live: refs must name a $defs/definitions entry literally.
		"ref into properties":  closed(`{"node":{"type":"object","properties":{"next":{"anyOf":[{"$ref":"#/properties/node"},{"type":"null"}]}},"required":["next"],"additionalProperties":false}}`),
		"escaped def name":     `{"type":"object","properties":{"a":{"$ref":"#/$defs/A~1B"}},"required":["a"],"additionalProperties":false,"$defs":{"A/B":` + closed(`{"x":{"type":"string"}}`) + `}}`,
		"pointer into a def":   `{"type":"object","properties":{"a":{"$ref":"#/$defs/D/properties/x"}},"required":["a"],"additionalProperties":false,"$defs":{"D":` + closed(`{"x":{"type":"string"}}`) + `}}`,
		"ref to a missing def": `{"type":"object","properties":{"a":{"$ref":"#/$defs/Missing"}},"required":["a"],"additionalProperties":false}`,
		"external ref":         `{"type":"object","properties":{"a":{"$ref":"https://example.com/s.json"}},"required":["a"],"additionalProperties":false}`,
		"17 unions":            nullable(17),
	}
	for name, schema := range rejected {
		t.Run("rejected/"+name, func(t *testing.T) {
			_, ok := prepareClaudeNativeSchema([]byte(schema), claudeSchemaLoad{})
			require.False(t, ok)
		})
	}

	t.Run("closes every unset object", func(t *testing.T) {
		in := `{"type":"object","properties":{"n":{"type":"object","properties":{"x":{"type":"string"}}},"list":{"type":"array","items":{"type":"object","properties":{"y":{"type":"integer"}}}},"pick":{"anyOf":[{"type":"object","properties":{"z":{"type":"string"}}},{"type":"null"}]},"ref":{"$ref":"#/$defs/D"},"a.b":{"type":"object","properties":{"k":{"type":"string"}}}},"$defs":{"D":{"type":"object","properties":{"d":{"type":"string"}}}}}`
		out, ok := prepareClaudeNativeSchema([]byte(in), claudeSchemaLoad{})
		require.True(t, ok)
		for _, path := range []string{"", "properties.n.", "properties.list.items.", "properties.pick.anyOf.0.", `properties.a\.b.`, `\$defs.D.`} {
			require.Equal(t, gjson.False, gjson.GetBytes(out, path+"additionalProperties").Type, "object at %q was not closed: %s", path, out)
		}
		require.Equal(t, `{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":false}`, gjson.GetBytes(out, "properties.n").Raw, "closing must append the key and leave the rest untouched")
	})
}

// BenchmarkConverseStructuredOutput measures Chat conversion without a response_format (the
// common case) and with a json_schema one on each structured-output path.
func BenchmarkConverseStructuredOutput(b *testing.B) {
	const closedSchema = `{"type":"json_schema","json_schema":{"name":"g","schema":{"type":"object","properties":{"greeting":{"type":"string"},"language":{"type":"string"}},"required":["greeting","language"],"additionalProperties":false}}}`
	const openSchema = `{"type":"json_schema","json_schema":{"name":"g","schema":{"type":"object","properties":{"greeting":{"type":"string"},"meta":{"type":"object","properties":{"lang":{"type":"string"}}}},"required":["greeting"]}}}`
	const rejectedSchema = `{"type":"json_schema","json_schema":{"name":"g","schema":{"type":"object","properties":{"n":{"type":"integer","minimum":1}},"required":["n"],"additionalProperties":false}}}`
	for _, bc := range []struct {
		name, model, responseFormat string
	}{
		{"no_response_format", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", ""},
		{"claude_native_closed", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", closedSchema},
		{"claude_native_closes_objects", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", openSchema},
		{"claude_rejected_falls_back", "us.anthropic.claude-sonnet-4-5-20250929-v1:0", rejectedSchema},
		{"gpt_native", "us.openai.gpt-6-luna", closedSchema},
		{"ungated_synthetic_tool", "us.meta.llama4-maverick-17b-instruct-v1:0", closedSchema},
	} {
		b.Run(bc.name, func(b *testing.B) {
			params := &schemas.ChatParameters{}
			if bc.responseFormat != "" {
				require.NoError(b, schemas.Unmarshal([]byte(`{"response_format":`+bc.responseFormat+`}`), params))
			}
			req := &schemas.BifrostChatRequest{
				Provider: schemas.Bedrock,
				Model:    bc.model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Say hello.")}}},
				Params:   params,
			}
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				if _, err := ToBedrockChatCompletionRequest(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
