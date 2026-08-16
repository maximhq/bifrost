package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesImageGenerationActionRequestRoundTrip(t *testing.T) {
	for _, want := range []ResponsesImageGenerationAction{
		ResponsesImageGenerationActionGenerate,
		ResponsesImageGenerationActionEdit,
		ResponsesImageGenerationActionAuto,
	} {
		t.Run(string(want), func(t *testing.T) {
			tool := ResponsesTool{
				Type: ResponsesToolTypeImageGeneration,
				ResponsesToolImageGeneration: &ResponsesToolImageGeneration{
					Action: &want,
				},
			}

			encoded, err := json.Marshal(tool)
			require.NoError(t, err)
			require.JSONEq(t, `{"type":"image_generation","action":"`+string(want)+`"}`, string(encoded))

			var decoded ResponsesTool
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			require.NotNil(t, decoded.ResponsesToolImageGeneration)
			require.NotNil(t, decoded.ResponsesToolImageGeneration.Action)
			require.Equal(t, want, *decoded.ResponsesToolImageGeneration.Action)
		})
	}
}

func TestResponsesImageGenerationActionUnionAcceptsScalarAndObject(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		scalar bool
	}{
		{name: "scalar", input: `"generate"`, scalar: true},
		{name: "object", input: `{"type":"generate"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var action ResponsesToolMessageActionStruct
			require.NoError(t, json.Unmarshal([]byte(tt.input), &action))
			require.NotNil(t, action.ResponsesImageGenerationToolCallAction)
			require.Equal(t, ResponsesImageGenerationActionGenerate, action.ResponsesImageGenerationToolCallAction.Type)
			require.Equal(t, tt.scalar, action.ResponsesImageGenerationToolCallAction.Scalar)
			encoded, err := json.Marshal(action)
			require.NoError(t, err)
			require.JSONEq(t, tt.input, string(encoded))
		})
	}
}

func TestResponsesImageGenerationActionUnionPreservesKnownObjectExtensions(t *testing.T) {
	input := `{"type":"generate","vendor_field":true}`
	var action ResponsesToolMessageActionStruct
	require.NoError(t, json.Unmarshal([]byte(input), &action))
	require.NotNil(t, action.ResponsesImageGenerationToolCallAction)
	require.Equal(t, ResponsesImageGenerationActionGenerate, action.ResponsesImageGenerationToolCallAction.Type)
	require.JSONEq(t, input, string(action.ResponsesImageGenerationToolCallAction.Raw))

	encoded, err := json.Marshal(action)
	require.NoError(t, err)
	require.JSONEq(t, input, string(encoded))

	message := ResponsesMessage{
		Type:                 Ptr(ResponsesMessageTypeImageGenerationCall),
		ResponsesToolMessage: &ResponsesToolMessage{Action: &action},
	}
	copied := DeepCopyResponsesMessage(message)
	require.NotNil(t, copied.ResponsesToolMessage.Action.ResponsesImageGenerationToolCallAction)
	copied.ResponsesToolMessage.Action.ResponsesImageGenerationToolCallAction.Raw[0] = '['
	require.Equal(t, byte('{'), message.ResponsesToolMessage.Action.ResponsesImageGenerationToolCallAction.Raw[0])
}

func TestResponsesImageGenerationActionUnionPreservesUnknownValues(t *testing.T) {
	for _, input := range []string{`"future_image_action"`, `{"type":"future_image_action","vendor_field":true}`} {
		t.Run(input, func(t *testing.T) {
			var action ResponsesToolMessageActionStruct
			require.NoError(t, json.Unmarshal([]byte(input), &action))
			require.Nil(t, action.ResponsesComputerToolCallAction)
			require.JSONEq(t, input, string(action.Raw))

			encoded, err := json.Marshal(action)
			require.NoError(t, err)
			require.JSONEq(t, input, string(encoded))
		})
	}
}

func TestResponsesImageGenerationActionUnionRejectsMalformedJSON(t *testing.T) {
	var action ResponsesToolMessageActionStruct
	require.Error(t, json.Unmarshal([]byte(`{"type":"generate"`), &action))
}

func TestResponsesImageGenerationActionUnionPreservesNull(t *testing.T) {
	var action ResponsesToolMessageActionStruct
	require.NoError(t, json.Unmarshal([]byte(`null`), &action))
	encoded, err := json.Marshal(action)
	require.NoError(t, err)
	require.Equal(t, `null`, string(encoded))
}

func TestResponsesImageGenerationCallDecodesUnaryAndStreamingFixtures(t *testing.T) {
	var response BifrostResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"resp_123",
		"object":"response",
		"status":"completed",
		"output":[{
			"type":"image_generation_call",
			"id":"ig_123",
			"status":"completed",
			"action":"generate",
			"result":"aGVsbG8="
		}]
	}`), &response))
	require.Len(t, response.Output, 1)
	require.NotNil(t, response.Output[0].ResponsesImageGenerationCall)
	require.Equal(t, "aGVsbG8=", response.Output[0].ResponsesImageGenerationCall.Result)
	require.NotNil(t, response.Output[0].Action)
	require.NotNil(t, response.Output[0].Action.ResponsesImageGenerationToolCallAction)
	require.Equal(t, ResponsesImageGenerationActionGenerate, response.Output[0].Action.ResponsesImageGenerationToolCallAction.Type)

	events := []ResponsesStreamResponseType{
		ResponsesStreamResponseTypeImageGenerationCallInProgress,
		ResponsesStreamResponseTypeImageGenerationCallGenerating,
		ResponsesStreamResponseTypeImageGenerationCallPartialImage,
		ResponsesStreamResponseTypeImageGenerationCallCompleted,
	}
	for i, eventType := range events {
		fixture := map[string]any{
			"type":            eventType,
			"sequence_number": i + 1,
			"item": map[string]any{
				"type":   "image_generation_call",
				"id":     "ig_123",
				"status": "in_progress",
				"action": "generate",
			},
		}
		encoded, err := json.Marshal(fixture)
		require.NoError(t, err)

		var stream BifrostResponsesStreamResponse
		require.NoError(t, json.Unmarshal(encoded, &stream), string(encoded))
		require.Equal(t, eventType, stream.Type)
		require.NotNil(t, stream.Item)
		require.NotNil(t, stream.Item.Action)
		require.NotNil(t, stream.Item.Action.ResponsesImageGenerationToolCallAction)
	}
}
