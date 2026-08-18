package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func orderingChatRequest(t *testing.T, provider schemas.ModelProvider) *schemas.BifrostChatRequest {
	t.Helper()
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    "claude-opus-4-5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	}
}

// TestResponseFormatOrderNativeStructuredOutputs covers the Anthropic-native
// path, where response_format becomes output_config.format.
func TestResponseFormatOrderNativeStructuredOutputs(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToAnthropicChatRequest(ctx, orderingChatRequest(t, schemas.Anthropic))
	require.NoError(t, err)
	require.NotNil(t, out.OutputConfig)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
	schemaorder.AssertSchemaKeyOrder(t, string(raw), "schema")
}

// TestResponseFormatOrderToolFallback covers Vertex/Bedrock Mantle/Azure, where
// the schema is rewritten into a forced tool's input_schema.
func TestResponseFormatOrderToolFallback(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Vertex, schemas.BedrockMantle, schemas.Azure} {
		t.Run(string(provider), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			out, err := ToAnthropicChatRequest(ctx, orderingChatRequest(t, provider))
			require.NoError(t, err)
			require.NotEmpty(t, out.Tools, "structured output must survive as a tool")

			raw, err := providerUtils.MarshalSorted(out)
			require.NoError(t, err)
			schemaorder.AssertPropertyOrder(t, string(raw))
		})
	}
}

// TestResponseFormatSchemaBytesReachAnthropicVerbatim: Anthropic's normalizer
// only rewrites union type arrays, of which this schema has none, so
// output_config.format.schema must carry the client's bytes unchanged.
//
// The Vertex/Mantle tool fallback is deliberately not byte-checked: there the
// schema becomes a tool's typed input_schema, so re-encoding is inherent to the
// rewrite. Key order on that path is covered by TestResponseFormatOrderToolFallback.
func TestResponseFormatSchemaBytesReachAnthropicVerbatim(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ByteExactChatBody), &params))

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToAnthropicChatRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.NotNil(t, out.OutputConfig)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertSchemaBytes(t, string(raw), "schema")
}

func orderingResponsesRequest(t *testing.T, provider schemas.ModelProvider) *schemas.BifrostResponsesRequest {
	t.Helper()
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    "claude-opus-4-5",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	}
}

// TestResponseFormatOrderSurvivesAnthropicResponses covers the Responses method,
// where the schema arrives under text.format rather than response_format.
func TestResponseFormatOrderSurvivesAnthropicResponses(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	out, err := ToAnthropicResponsesRequest(ctx, orderingResponsesRequest(t, schemas.Anthropic))
	require.NoError(t, err)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}
