package perplexity

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestResponseFormatOrderSurvivesPerplexityChat covers the pass-through
// providers: response_format is forwarded untouched, so the only thing that can
// corrupt it is the neutral decode/encode.
func TestResponseFormatOrderSurvivesPerplexityChat(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	out := ToPerplexityChatCompletionRequest(&schemas.BifrostChatRequest{
		Provider: schemas.Perplexity,
		Model:    "sonar",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NotNil(t, out)
	require.NotNil(t, out.ResponseFormat)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
	schemaorder.AssertSchemaKeyOrder(t, string(raw), "schema")
}

// TestResponseFormatSchemaBytesReachPerplexityVerbatim covers the pass-through
// providers: nothing may be re-encoded on a route that only forwards.
func TestResponseFormatSchemaBytesReachPerplexityVerbatim(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))

	out := ToPerplexityChatCompletionRequest(&schemas.BifrostChatRequest{
		Provider: schemas.Perplexity,
		Model:    "sonar",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NotNil(t, out)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertSchemaBytes(t, string(raw), "schema")
}

// TestResponseFormatOrderSurvivesPerplexityResponses covers the Responses method.
func TestResponseFormatOrderSurvivesPerplexityResponses(t *testing.T) {
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))

	out := ToPerplexityResponsesRequest(&schemas.BifrostResponsesRequest{
		Provider: schemas.Perplexity,
		Model:    "sonar",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NotNil(t, out)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}
