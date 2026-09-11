package integrations

import (
	"io"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Responses streams must end on a typed Responses event. Before #6783 the route
// wrote Bifrost's internal error envelope as a bare `data:` frame, which carries no
// Responses `type`, so clients discarded it and reported a dropped stream.

func responsesRouteConfig(t *testing.T) RouteConfig {
	t.Helper()
	for _, config := range CreateOpenAIRouteConfigs("", &mockHandlerStore{}) {
		if strings.HasSuffix(config.Path, "/v1/responses") && config.StreamConfig != nil &&
			config.StreamConfig.ResponsesStreamResponseConverter != nil {
			return config
		}
	}
	t.Fatal("no /v1/responses route found")
	return RouteConfig{}
}

// streamResponsesError runs a single error chunk through the route and returns the
// SSE event name and the decoded data payload of the final frame.
func streamResponsesError(t *testing.T, bifrostErr *schemas.BifrostError) (string, map[string]interface{}) {
	t.Helper()

	stream := make(chan *schemas.BifrostStreamChunk, 1)
	stream <- &schemas.BifrostStreamChunk{BifrostError: bifrostErr}
	close(stream)

	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())
	ctx := &fasthttp.RequestCtx{}
	router.handleStreaming(ctx, nil, responsesRouteConfig(t), stream, func() {})

	body, err := io.ReadAll(ctx.Response.BodyStream())
	require.NoError(t, err)

	frame := strings.TrimSpace(string(body))
	require.NotEmpty(t, frame, "route emitted no terminal frame")
	require.NotContains(t, frame, "[DONE]", "responses streams must not append the done marker")

	eventName, data, found := strings.Cut(frame, "\n")
	require.True(t, found, "frame must carry both an event line and a data line: %q", frame)
	eventName = strings.TrimPrefix(eventName, "event: ")

	var payload map[string]interface{}
	require.NoError(t, sonic.UnmarshalString(strings.TrimPrefix(data, "data: "), &payload))
	return eventName, payload
}

// A connection that dies mid-stream is reported as a well-formed response.failed
// built from the identity the stream already carried.
func Test_ResponsesStreamTruncationEmitsTypedFailureEvent(t *testing.T) {
	statusCode := 502
	eventName, payload := streamResponsesError(t, &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Type:    schemas.Ptr(schemas.ProviderConnectionFailed),
			Message: schemas.ErrProviderStreamTruncated,
		},
		ResponsesTerminalEvent: &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeFailed,
			SequenceNumber: 7,
			Response: &schemas.BifrostResponsesResponse{
				ID:     schemas.Ptr("resp_abc"),
				Object: "response",
				Model:  "gpt-5.6-sol",
				Status: schemas.Ptr(schemas.ResponsesResponseStatusFailed),
				Error: &schemas.ResponsesResponseError{
					Code:    string(schemas.ProviderConnectionFailed),
					Message: schemas.ErrProviderStreamTruncated,
				},
			},
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.ResponsesStreamRequest,
			Provider:    schemas.Azure,
		},
	})

	require.Equal(t, "response.failed", eventName)
	require.Equal(t, "response.failed", payload["type"], "clients dispatch on data.type")
	require.Equal(t, eventName, payload["type"], "event name and payload type must agree")

	response, ok := payload["response"].(map[string]interface{})
	require.True(t, ok, "response.failed must carry its response object: %v", payload)
	require.Equal(t, "resp_abc", response["id"])
	require.Equal(t, "failed", response["status"])
}

// An upstream response.failed is replayed with its own payload intact rather than
// being flattened into Bifrost's envelope.
func Test_ResponsesStreamUpstreamFailureIsReplayedIntact(t *testing.T) {
	eventName, payload := streamResponsesError(t, &schemas.BifrostError{
		Type:           schemas.Ptr("response.failed"),
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Code:    schemas.Ptr("server_error"),
			Message: "simulated Azure failure",
		},
		ResponsesTerminalEvent: &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeFailed,
			SequenceNumber: 1,
			Response: &schemas.BifrostResponsesResponse{
				ID:     schemas.Ptr("resp_repro"),
				Object: "response",
				Model:  "gpt-5.6-sol",
				Status: schemas.Ptr(schemas.ResponsesResponseStatusFailed),
				Usage:  &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
				Error: &schemas.ResponsesResponseError{
					Code:    "server_error",
					Message: "simulated Azure failure",
				},
			},
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.ResponsesStreamRequest,
			Provider:    schemas.Azure,
		},
	})

	require.Equal(t, "response.failed", eventName)
	response, ok := payload["response"].(map[string]interface{})
	require.True(t, ok, "the upstream response object must survive: %v", payload)
	require.Equal(t, "resp_repro", response["id"])

	usage, ok := response["usage"].(map[string]interface{})
	require.True(t, ok, "usage must survive the replay: %v", response)
	require.EqualValues(t, 12, usage["total_tokens"])

	failure, ok := response["error"].(map[string]interface{})
	require.True(t, ok, "the provider's reason must survive: %v", response)
	require.Equal(t, "simulated Azure failure", failure["message"])
}

// A failure with no response identity still has to produce a dispatchable frame;
// the top-level `error` event needs none.
func Test_ResponsesStreamWithoutIdentityEmitsTypedErrorEvent(t *testing.T) {
	eventName, payload := streamResponsesError(t, &schemas.BifrostError{
		Error: &schemas.ErrorField{
			Code:    schemas.Ptr("provider_connection_failed"),
			Message: schemas.ErrProviderStreamTruncated,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType: schemas.ResponsesStreamRequest,
		},
	})

	require.Equal(t, "error", eventName)
	require.Equal(t, "error", payload["type"])
	require.Equal(t, schemas.ErrProviderStreamTruncated, payload["message"])
}
