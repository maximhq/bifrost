package replicate

import (
	"testing"

	"github.com/maximhq/bifrost/core/internal/schemaorder"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

const gpt5StructuredModel = "openai/gpt-5-structured"

// TestResponseFormatReachesReplicateChat pins that a chat request keeps its
// structured output: gpt-5-structured takes the schema as the `json_schema`
// input, and the chat path used to drop response_format on the floor while the
// Responses path carried it.
func TestResponseFormatReachesReplicateChat(t *testing.T) {
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ChatBody), &params))

	out, err := ToReplicateChatRequest(&schemas.BifrostChatRequest{
		Provider: schemas.Replicate,
		Model:    gpt5StructuredModel,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Input)
	require.NotNil(t, out.Input.JsonSchema, "structured output must reach Replicate")

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}

// TestResponseFormatOrderSurvivesReplicateResponses covers the Responses method.
func TestResponseFormatOrderSurvivesReplicateResponses(t *testing.T) {
	var params schemas.ResponsesParameters
	require.NoError(t, schemas.Unmarshal([]byte(schemaorder.ResponsesBody), &params))

	out, err := ToReplicateResponsesRequest(&schemas.BifrostResponsesRequest{
		Provider: schemas.Replicate,
		Model:    gpt5StructuredModel,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("assign it")},
		}},
		Params: &params,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Input)
	require.NotNil(t, out.Input.JsonSchema)

	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	schemaorder.AssertPropertyOrder(t, string(raw))
}

// ResponsesTextConfigFormatFromChatResponseFormat returns a non-nil format for every
// recognized response_format.type, and for "text"/"json_object" that format carries only
// Type -- no schema. Assigning it anyway sent gpt-5-structured a schema-less structured
// output config (`json_schema: {"format":{"type":"json_object"}}`), which is not a
// structured-output request at all.
func TestSchemalessResponseFormatDoesNotReachReplicateJsonSchema(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"json_object", `{"response_format":{"type":"json_object"}}`},
		{"text", `{"response_format":{"type":"text"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var params schemas.ChatParameters
			require.NoError(t, schemas.Unmarshal([]byte(tc.body), &params))

			out, err := ToReplicateChatRequest(&schemas.BifrostChatRequest{
				Provider: schemas.Replicate,
				Model:    gpt5StructuredModel,
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("assign it")},
				}},
				Params: &params,
			})
			require.NoError(t, err)
			require.NotNil(t, out.Input)
			require.Nil(t, out.Input.JsonSchema,
				"%s carries no schema, so it must not be sent as a json_schema input", tc.name)
		})
	}
}
