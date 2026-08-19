package lib

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestSanitizeBifrostErrorForClientHidesInternalDetails(t *testing.T) {
	statusCode := fasthttp.StatusInternalServerError
	err := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: "failed to create customer: pq: duplicate key value violates unique constraint users_email_key",
			Error:   errors.New("goroutine 1 [running]:\nmain.handler\n\t/app/server.go:42"),
			Param:   "users_email_key",
		},
	}

	sanitized := SanitizeBifrostErrorForClient(err)

	if sanitized == err {
		t.Fatal("expected sanitizer to return a copy")
	}
	if sanitized.Error.Message != ClientSafeInternalErrorMessage {
		t.Fatalf("expected generic message, got %q", sanitized.Error.Message)
	}
	if sanitized.Error.Error != nil {
		t.Fatalf("expected sensitive nested error to be removed, got %v", sanitized.Error.Error)
	}
	if sanitized.Error.Param != nil {
		t.Fatalf("expected param to be removed, got %v", sanitized.Error.Param)
	}
	if err.Error.Message == ClientSafeInternalErrorMessage || err.Error.Error == nil || err.Error.Param == nil {
		t.Fatal("expected original error to remain unchanged")
	}
}

func TestSanitizeBifrostErrorForClientPreservesClientValidationMessage(t *testing.T) {
	statusCode := fasthttp.StatusBadRequest
	err := &schemas.BifrostError{
		StatusCode: &statusCode,
		Error: &schemas.ErrorField{
			Message: "model is required",
			Error:   errors.New("missing model"),
			Param:   "model",
		},
	}

	sanitized := SanitizeBifrostErrorForClient(err)

	if sanitized.Error.Message != "model is required" {
		t.Fatalf("expected validation message to be preserved, got %q", sanitized.Error.Message)
	}
	if sanitized.Error.Param != "model" {
		t.Fatalf("expected param to be preserved, got %v", sanitized.Error.Param)
	}
	if sanitized.Error.Error == nil {
		t.Fatal("expected non-sensitive nested error to be preserved")
	}
}

func TestSanitizeBifrostErrorForClientPreservesNonSensitiveServerMessage(t *testing.T) {
	statusCode := fasthttp.StatusInternalServerError
	err := &schemas.BifrostError{
		StatusCode: &statusCode,
		Error: &schemas.ErrorField{
			Message: "failed to reload config",
		},
	}

	sanitized := SanitizeBifrostErrorForClient(err)

	if sanitized.Error.Message != "failed to reload config" {
		t.Fatalf("expected non-sensitive server message to be preserved, got %q", sanitized.Error.Message)
	}
}

func TestSanitizeBifrostTimeoutErrorKeepsMetadataAndHidesCause(t *testing.T) {
	err := &schemas.BifrostError{
		Error: &schemas.ErrorField{Message: "upstream connection timed out", Error: errors.New("dial tcp secret.internal: timeout"), Param: "secret"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			TimeoutSource:            schemas.TimeoutSourceUpstreamConnection,
			ConfiguredTimeoutSeconds: 600,
			ElapsedMS:                27_000,
			UpstreamResponseReceived: schemas.Ptr(false),
			RawRequest:               "sensitive request",
			RawResponse:              "sensitive upstream response",
		},
	}

	sanitized := SanitizeBifrostErrorForClient(err)
	if sanitized.Error.Error != nil || sanitized.Error.Param != nil {
		t.Fatal("timeout cause and param must not be returned to clients")
	}
	if sanitized.ExtraFields.RawRequest != nil || sanitized.ExtraFields.RawResponse != nil {
		t.Fatal("timeout payloads must not be returned to clients")
	}
	if sanitized.ExtraFields.TimeoutSource != schemas.TimeoutSourceUpstreamConnection || sanitized.ExtraFields.ConfiguredTimeoutSeconds != 600 {
		t.Fatal("safe structured timeout metadata must be preserved")
	}
	if err.Error.Error == nil || err.ExtraFields.RawRequest == nil {
		t.Fatal("sanitizer must not mutate the original error")
	}
}

func TestSanitizeBifrostTimeoutErrorReplacesUpstreamMessage(t *testing.T) {
	err := &schemas.BifrostError{
		Error: &schemas.ErrorField{
			Message: "gateway timeout contacting https://user:secret@proxy.internal?X-Amz-Signature=top-secret",
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			TimeoutSource:            schemas.TimeoutSourceUpstreamHTTP504,
			ConfiguredTimeoutSeconds: 600,
			ElapsedMS:                27_000,
			UpstreamResponseReceived: schemas.Ptr(true),
		},
	}

	sanitized := SanitizeBifrostErrorForClient(err)
	if sanitized.Error.Message != "upstream returned HTTP 504 Gateway Timeout" {
		t.Fatalf("expected canonical timeout message, got %q", sanitized.Error.Message)
	}
	if err.Error.Message == sanitized.Error.Message {
		t.Fatal("sanitizer must not mutate the original error")
	}
}
