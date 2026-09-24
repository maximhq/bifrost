package handlers

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// On native /v1/responses the SSE event name used to be hardcoded to "error"
// regardless of the payload, so a response.failed body went out under an "error"
// event name — a frame that contradicts itself (#6783).

// streamNativeResponsesError runs one error chunk through the native streaming
// handler and returns the event name and decoded payload of the final frame.
func streamNativeResponsesError(t *testing.T, bifrostErr *schemas.BifrostError) (string, map[string]interface{}) {
	t.Helper()

	stream := make(chan *schemas.BifrostStreamChunk, 1)
	stream <- &schemas.BifrostStreamChunk{BifrostError: bifrostErr}
	close(stream)

	handler := &CompletionHandler{config: &lib.Config{}}
	ctx := &fasthttp.RequestCtx{}
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	handler.handleStreamingResponse(ctx, bifrostCtx, schemas.ResponsesStreamRequest,
		func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) { return stream, nil },
		func() {})

	body, err := io.ReadAll(ctx.Response.BodyStream())
	require.NoError(t, err)

	frame := strings.TrimSpace(string(body))
	require.NotEmpty(t, frame, "handler emitted no terminal frame")
	require.NotContains(t, frame, "[DONE]", "responses streams must not append the done marker")

	eventName, data, found := strings.Cut(frame, "\n")
	require.True(t, found, "frame must carry both an event line and a data line: %q", frame)
	eventName = strings.TrimPrefix(eventName, "event: ")

	var payload map[string]interface{}
	require.NoError(t, sonic.UnmarshalString(strings.TrimPrefix(data, "data: "), &payload))
	return eventName, payload
}

func Test_NativeResponsesStreamErrorEventNameMatchesPayload(t *testing.T) {
	eventName, payload := streamNativeResponsesError(t, &schemas.BifrostError{
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

	require.Equal(t, "response.failed", eventName, "event name must follow the payload, not a hardcoded 'error'")
	require.Equal(t, eventName, payload["type"], "event name and data.type must agree")

	response, ok := payload["response"].(map[string]interface{})
	require.True(t, ok, "response.failed must carry its response object: %v", payload)
	require.Equal(t, "resp_repro", response["id"])
	require.Equal(t, "failed", response["status"])
}

// A failure carrying no Responses event still needs a dispatchable frame, and its
// name must match the `error` type it falls back to.
func Test_NativeResponsesStreamFallsBackToTypedErrorEvent(t *testing.T) {
	eventName, payload := streamNativeResponsesError(t, &schemas.BifrostError{
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
