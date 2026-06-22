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
