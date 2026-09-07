package openai

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestResponsesStreamError_NormalizesAzureShapeAndEmptyDetails(t *testing.T) {
	cases := []struct {
		wire, wantMessage string
	}{
		{`{"type":"error","error":{"type":"too_many_requests","code":"no_capacity","message":"capacity"}}`, "capacity"},
		{`{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"input is too large"}}}`, "input is too large"},
		{`{"type":"error","error":{}}`, "provider stream error (error)"},
	}
	for _, tc := range cases {
		var response schemas.BifrostResponsesStreamResponse
		if err := schemas.Unmarshal([]byte(tc.wire), &response); err != nil {
			t.Fatalf("unmarshal stream error: %v", err)
		}
		got := responsesStreamError(&response)
		if got == nil || got.Error == nil || got.Error.Message != tc.wantMessage {
			t.Fatalf("normalized error = %v, want message %q", got, tc.wantMessage)
		}
	}
}

func TestParseOpenAIError_FallbackMessageWhenProviderBodyIsNonOpenAIShape(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusUnprocessableEntity)
	resp.SetBodyString(`{"detail":[{"loc":["body","messages",0,"role"],"msg":"value is not a valid enumeration member"}]}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message == "" {
		t.Fatal("expected non-empty error message")
	}
	if errResp.Error.Message != "provider API error (status 422)" {
		t.Fatalf("expected fallback message, got %q", errResp.Error.Message)
	}
}

func TestParseOpenAIError_PreservesProviderMessageWhenPresent(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusUnprocessableEntity)
	resp.SetBodyString(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error","param":"messages.0.role","code":"invalid_value"}}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message != "unsupported role: developer" {
		t.Fatalf("expected provider message, got %q", errResp.Error.Message)
	}
}

func TestParseOpenAIError_FallbackMessageWhenBodyIsEmpty(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBody(nil)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	// HandleProviderAPIError returns ErrProviderResponseEmpty with HTTP status for empty bodies.
	expectedMsg := schemas.ErrProviderResponseEmpty + " (HTTP 400)"
	if errResp.Error.Message != expectedMsg {
		t.Fatalf("expected %q, got %q", expectedMsg, errResp.Error.Message)
	}
}

func TestParseOpenAIError_WhitespaceProviderMessageFallsBack(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"error":{"message":"   ","type":"invalid_request_error"}}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message != "provider API error (status 400)" {
		t.Fatalf("expected fallback message, got %q", errResp.Error.Message)
	}
}

func TestParseOpenAIError_DefaultStatusCodeFallsBackWithStatusNumber(t *testing.T) {
	var resp fasthttp.Response
	// fasthttp defaults zero-value response status code to 200.
	resp.SetBodyString(`{"error":{"message":""}}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message != "provider API error (status 200)" {
		t.Fatalf("expected fallback message with default status, got %q", errResp.Error.Message)
	}
}

// A Responses stream must end on a frame the client can dispatch on. Before this,
// an upstream response.failed was flattened into Bifrost's error envelope and its
// nested response object — id, status, usage — was dropped (#6783).
func TestResponsesStreamError_PreservesUpstreamTerminalEvent(t *testing.T) {
	upstream := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeFailed,
		SequenceNumber: 1,
		Response: &schemas.BifrostResponsesResponse{
			ID:     schemas.Ptr("resp_repro"),
			Object: "response",
			Status: schemas.Ptr(schemas.ResponsesResponseStatusFailed),
			Model:  "gpt-5.6-sol",
			Error: &schemas.ResponsesResponseError{
				Code:    "server_error",
				Message: "simulated Azure failure",
			},
		},
	}

	bifrostErr := responsesStreamError(upstream)

	if bifrostErr.ResponsesTerminalEvent == nil {
		t.Fatal("upstream terminal event was dropped")
	}
	if got := bifrostErr.ResponsesTerminalEvent.Type; got != schemas.ResponsesStreamResponseTypeFailed {
		t.Errorf("event type = %q, want response.failed", got)
	}
	if bifrostErr.ResponsesTerminalEvent.Response == nil {
		t.Fatal("nested response object was dropped")
	}
	if got := bifrostErr.ResponsesTerminalEvent.Response.ID; got == nil || *got != "resp_repro" {
		t.Errorf("response id = %v, want resp_repro", got)
	}
	// The flattened fields must still be populated for retries, logging and billing.
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "simulated Azure failure" {
		t.Errorf("flattened error = %+v, want the upstream message", bifrostErr.Error)
	}
}

// A stream that dies mid-flight has no upstream event to replay, so one is
// synthesized from the identity of the last event seen.
func TestNewResponsesTruncationEvent_SynthesizesFailedFromLastSeen(t *testing.T) {
	event := newResponsesTruncationEvent(&schemas.BifrostResponsesResponse{
		ID:     schemas.Ptr("resp_abc"),
		Object: "response",
		Model:  "gpt-5.6-sol",
		Status: schemas.Ptr(schemas.ResponsesResponseStatusInProgress),
	}, 7)

	if event == nil {
		t.Fatal("expected a synthesized event")
	}
	if event.Type != schemas.ResponsesStreamResponseTypeFailed {
		t.Errorf("type = %q, want response.failed", event.Type)
	}
	if event.SequenceNumber != 7 {
		t.Errorf("sequence_number = %d, want 7", event.SequenceNumber)
	}
	if event.Response == nil || event.Response.Status == nil || *event.Response.Status != schemas.ResponsesResponseStatusFailed {
		t.Fatalf("status must be flipped to failed, got %+v", event.Response)
	}
	if got := event.Response.ID; got == nil || *got != "resp_abc" {
		t.Errorf("id = %v, want the id carried by the stream", got)
	}
	if event.Response.Error == nil || event.Response.Error.Message != schemas.ErrProviderStreamTruncated {
		t.Errorf("error = %+v, want the truncation reason", event.Response.Error)
	}
}

// Without an identity there is nothing to build a response.failed around, so the
// renderer must still produce a typed frame — the `error` event needs no identity.
func TestToOpenAIResponsesStreamError_FallsBackToTypedErrorEvent(t *testing.T) {
	if got := newResponsesTruncationEvent(nil, 1); got != nil {
		t.Fatalf("expected no synthesized event without an identity, got %+v", got)
	}

	framed := ToOpenAIResponsesStreamError(&schemas.BifrostError{
		Error: &schemas.ErrorField{
			Code:    schemas.Ptr("provider_connection_failed"),
			Message: schemas.ErrProviderStreamTruncated,
		},
	})

	if !strings.HasPrefix(framed, "event: error\n") {
		t.Errorf("frame must be named for its own type, got %q", framed)
	}
	if !strings.Contains(framed, `"type":"error"`) {
		t.Errorf("frame must carry a dispatchable type, got %q", framed)
	}
	if !strings.Contains(framed, schemas.ErrProviderStreamTruncated) {
		t.Errorf("frame must carry the failure reason, got %q", framed)
	}
}
