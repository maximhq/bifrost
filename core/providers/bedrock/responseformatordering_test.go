package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
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
