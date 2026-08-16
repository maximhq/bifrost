package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

func imageGenerationResponsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.6-luna",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Generate a small orange cat in a coffee shop.")},
		}},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type: schemas.ResponsesToolTypeImageGeneration,
				ResponsesToolImageGeneration: &schemas.ResponsesToolImageGeneration{
					Action: schemas.Ptr(schemas.ResponsesImageGenerationActionGenerate),
				},
			}},
		},
	}
}

func TestResponsesImageGenerationUnaryProviderIntegration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		if !strings.Contains(string(body), `"type": "image_generation"`) {
			t.Errorf("request did not contain image_generation tool: %s", body)
		}
		if !strings.Contains(string(body), `"action": "generate"`) {
			t.Errorf("request did not contain generate action: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_unary","object":"response","status":"completed","output":[{"type":"image_generation_call","id":"ig_unary","status":"completed","action":"generate","result":"aGVsbG8="}]}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	response, bifrostErr := provider.Responses(newStreamTestContext(), testKey(), imageGenerationResponsesRequest())
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)
	require.Len(t, response.Output, 1)
	require.NotNil(t, response.Output[0].ResponsesImageGenerationCall)
	require.Equal(t, "aGVsbG8=", response.Output[0].ResponsesImageGenerationCall.Result)
	require.NotNil(t, response.Output[0].Action)
	require.NotNil(t, response.Output[0].Action.ResponsesImageGenerationToolCallAction)
	require.Equal(t, schemas.ResponsesImageGenerationActionGenerate, response.Output[0].Action.ResponsesImageGenerationToolCallAction.Type)
	encodedResponse, err := json.Marshal(response)
	require.NoError(t, err)
	require.Contains(t, string(encodedResponse), `"action":"generate"`)
}

func TestResponsesImageGenerationStreamingProviderIntegration(t *testing.T) {
	streamBody := "event: response.image_generation_call.in_progress\n" +
		`data: {"type":"response.image_generation_call.in_progress","sequence_number":1,"item":{"type":"image_generation_call","id":"ig_stream","status":"in_progress","action":"generate"}}` + "\n\n" +
		"event: response.image_generation_call.generating\n" +
		`data: {"type":"response.image_generation_call.generating","sequence_number":2,"item":{"type":"image_generation_call","id":"ig_stream","status":"generating","action":"generate"}}` + "\n\n" +
		"event: response.image_generation_call.partial_image\n" +
		`data: {"type":"response.image_generation_call.partial_image","sequence_number":3,"item_id":"ig_stream","partial_image_b64":"aGk=","partial_image_index":0}` + "\n\n" +
		// OpenAI's live stream returns the final base64 image in output_item.done,
		// rather than emitting a response.image_generation_call.completed event.
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","sequence_number":4,"item":{"type":"image_generation_call","id":"ig_stream","status":"generating","action":"generate","result":"aGVsbG8="}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":5,"response":{"id":"resp_stream","object":"response","status":"completed","output":[{"type":"image_generation_call","id":"ig_stream","status":"generating","action":"generate","result":"aGVsbG8="}]}}` + "\n\n"

	server := completeSSEServer(t, streamBody)
	defer server.Close()
	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), imageGenerationResponsesRequest())
	require.Nil(t, bifrostErr)

	wantedEvents := map[schemas.ResponsesStreamResponseType]bool{
		schemas.ResponsesStreamResponseTypeImageGenerationCallInProgress:   false,
		schemas.ResponsesStreamResponseTypeImageGenerationCallGenerating:   false,
		schemas.ResponsesStreamResponseTypeImageGenerationCallPartialImage: false,
		schemas.ResponsesStreamResponseTypeOutputItemDone:                  false,
	}
	var completed *schemas.BifrostResponsesStreamResponse
	for _, chunk := range collectChunks(t, stream) {
		require.Nil(t, chunk.BifrostError)
		if chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		response := chunk.BifrostResponsesStreamResponse
		if _, ok := wantedEvents[response.Type]; ok {
			wantedEvents[response.Type] = true
		}
		if response.Type == schemas.ResponsesStreamResponseTypeImageGenerationCallInProgress ||
			response.Type == schemas.ResponsesStreamResponseTypeImageGenerationCallGenerating ||
			response.Type == schemas.ResponsesStreamResponseTypeOutputItemDone {
			require.NotNil(t, response.Item)
			require.NotNil(t, response.Item.Action)
			require.NotNil(t, response.Item.Action.ResponsesImageGenerationToolCallAction)
			encodedEvent, err := json.Marshal(response)
			require.NoError(t, err)
			require.Contains(t, string(encodedEvent), `"action":"generate"`)
			if response.Type == schemas.ResponsesStreamResponseTypeOutputItemDone {
				require.Equal(t, "aGVsbG8=", response.Item.ResponsesImageGenerationCall.Result)
			}
		}
		if response.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completed = response
		}
	}

	for eventType, seen := range wantedEvents {
		require.True(t, seen, "missing image-generation stream event %s", eventType)
	}
	require.NotNil(t, completed)
	require.NotNil(t, completed.Response)
	require.Len(t, completed.Response.Output, 1)
	require.Equal(t, "aGVsbG8=", completed.Response.Output[0].ResponsesImageGenerationCall.Result)
}
