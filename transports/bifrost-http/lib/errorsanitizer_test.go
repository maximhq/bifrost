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

// A fallback's message is copied onto the primary's error when the chain runs out, so it reaches
// the client through the same response and must pass the same check (#3261).
func TestSanitizeBifrostErrorForClientHidesFallbackAttemptDetails(t *testing.T) {
	statusCode := fasthttp.StatusTooManyRequests
	fallbackStatus := fasthttp.StatusInternalServerError
	refusedStatus := fasthttp.StatusForbidden
	err := &schemas.BifrostError{
		StatusCode: &statusCode,
		Error:      &schemas.ErrorField{Message: "rate limited"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			FallbackAttempts: []schemas.FallbackAttempt{
				{Provider: "prov-b", Model: "m-b", StatusCode: &fallbackStatus, Message: "Traceback (most recent call last):\n  File \"/srv/app.py\", line 12"},
				{Provider: "prov-c", Model: "m-c", StatusCode: &refusedStatus, Type: "provider_blocked", Message: "provider prov-c is not allowed"},
			},
		},
	}

	sanitized := SanitizeBifrostErrorForClient(err)

	attempts := sanitized.ExtraFields.FallbackAttempts
	if len(attempts) != 2 {
		t.Fatalf("expected 2 fallback attempts, got %d", len(attempts))
	}
	if attempts[0].Message != ClientSafeInternalErrorMessage {
		t.Fatalf("expected fallback traceback to be hidden, got %q", attempts[0].Message)
	}
	if attempts[0].StatusCode == nil || *attempts[0].StatusCode != fallbackStatus {
		t.Fatalf("expected fallback status to be preserved, got %v", attempts[0].StatusCode)
	}
	if attempts[1].Message != "provider prov-c is not allowed" {
		t.Fatalf("expected non-sensitive fallback message to be preserved, got %q", attempts[1].Message)
	}
	if err.ExtraFields.FallbackAttempts[0].Message == ClientSafeInternalErrorMessage {
		t.Fatal("expected original fallback attempts to remain unchanged")
	}
}
