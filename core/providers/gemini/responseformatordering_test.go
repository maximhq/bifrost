package gemini

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestResponseFormatOrderSurvivesGeminiChat covers response_format ->
// generationConfig.responseJsonSchema, Gemini's structured output field.
func TestResponseFormatOrderSurvivesGeminiChat(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToGeminiChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.NotNil(t, out.GenerationConfig)
	require.NotNil(t, out.GenerationConfig.ResponseJSONSchema, "structured output must reach Gemini")

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}

// TestResponseFormatSchemaBytesReachGeminiVerbatim: Gemini's normalizer only has
// work to do for union type arrays. This schema has none, so normalization is an
// identity transform and the bytes must pass through untouched.
func TestResponseFormatSchemaBytesReachGeminiVerbatim(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToGeminiChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertSchemaBytes(t, string(raw), "responseJsonSchema")
}

// TestResponseFormatOrderSurvivesGeminiResponses covers the Responses method.
func TestResponseFormatOrderSurvivesGeminiResponses(t *testing.T) {
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToGeminiResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
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
