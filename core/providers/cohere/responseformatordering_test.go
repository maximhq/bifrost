package cohere

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestResponseFormatOrderSurvivesCohereChat covers Cohere, which flattens
// response_format into {type: json_object, json_schema: <schema>}.
func TestResponseFormatOrderSurvivesCohereChat(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	out, err := ToCohereChatCompletionRequest(&schemas.BifrostChatRequest{
		Provider: schemas.Cohere,
		Model:    "command-r-plus",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.NotNil(t, out.ResponseFormat, "structured output must reach Cohere")
	require.NotNil(t, out.ResponseFormat.JSONSchema)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
	schemaorder.AssertSchemaKeyOrder(t, string(raw), "json_schema")
}

// TestResponseFormatSchemaBytesReachCohereVerbatim: Cohere relocates the schema
// but never rewrites it, so the schema body must survive byte for byte.
func TestResponseFormatSchemaBytesReachCohereVerbatim(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))

	out, err := ToCohereChatCompletionRequest(&schemas.BifrostChatRequest{
		Provider: schemas.Cohere,
		Model:    "command-r-plus",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertSchemaBytes(t, string(raw), "json_schema")
}

// TestResponseFormatOrderSurvivesCohereResponses covers the Responses method.
func TestResponseFormatOrderSurvivesCohereResponses(t *testing.T) {
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))

	out, err := ToCohereResponsesRequest(&schemas.BifrostResponsesRequest{
		Provider: schemas.Cohere,
		Model:    "command-r-plus",
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
