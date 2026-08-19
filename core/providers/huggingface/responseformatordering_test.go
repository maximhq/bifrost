package huggingface

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestResponseFormatOrderSurvivesHuggingFaceChat covers HuggingFace, which
// re-encodes json_schema into its own typed struct with a raw schema field.
func TestResponseFormatOrderSurvivesHuggingFaceChat(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	out, err := ToHuggingFaceChatCompletionRequest(&schemas.BifrostChatRequest{
		Provider: schemas.HuggingFace,
		Model:    "meta-llama/Llama-3.3-70B-Instruct",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.NotNil(t, out.ResponseFormat, "structured output must reach HuggingFace")
	require.NotNil(t, out.ResponseFormat.JSONSchema)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
	schemaorder.AssertSchemaKeyOrder(t, string(raw), "schema")
}

// TestResponseFormatSchemaBytesReachHuggingFaceVerbatim: HuggingFace re-types the
// wrapper but carries the schema as raw JSON, so it must not be re-encoded.
func TestResponseFormatSchemaBytesReachHuggingFaceVerbatim(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))

	out, err := ToHuggingFaceChatCompletionRequest(&schemas.BifrostChatRequest{
		Provider: schemas.HuggingFace,
		Model:    "meta-llama/Llama-3.3-70B-Instruct",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertSchemaBytes(t, string(raw), "schema")
}

// TestResponseFormatOrderSurvivesHuggingFaceResponses covers the Responses method.
func TestResponseFormatOrderSurvivesHuggingFaceResponses(t *testing.T) {
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))

	out, err := ToHuggingFaceResponsesRequest(&schemas.BifrostResponsesRequest{
		Provider: schemas.HuggingFace,
		Model:    "meta-llama/Llama-3.3-70B-Instruct",
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
