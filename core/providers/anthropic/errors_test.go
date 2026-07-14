package anthropic

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestToAnthropicChatCompletionError(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	tests := []struct {
		name         string
		input        *schemas.BifrostError
		expectNil    bool
		expectedType string
	}{
		{
			name:      "nil BifrostError returns nil",
			input:     nil,
			expectNil: true,
		},
		{
			name: "nil ErrorField.Type defaults to api_error",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    nil,
					Message: "connection failed",
				},
			},
			expectedType: "api_error",
		},
		{
			name: "empty string Type defaults to api_error",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    strPtr(""),
					Message: "rate limited",
				},
			},
			expectedType: "api_error",
		},
		{
			// Stage 2: canonical OpenAI type -> Anthropic-native type
			// (openAIToAnthropicType). By the time ToAnthropicChatCompletionError
			// runs, .Error.Type is expected to already be OpenAI-canonical
			// (set by Stage 1, e.g. parseAnthropicError/normalizeAnthropicErrorType),
			// not Anthropic's own raw vocabulary.
			name: "canonical rate_limit_exceeded translates to Anthropic rate_limit_error",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    strPtr("rate_limit_exceeded"),
					Message: "rate limited",
				},
			},
			expectedType: "rate_limit_error",
		},
		{
			name: "canonical insufficient_quota translates to Anthropic billing_error, not rate_limit_error",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    strPtr("insufficient_quota"),
					Message: "budget exceeded",
				},
			},
			expectedType: "billing_error",
		},
		{
			name: "unmapped/unrecognized Type falls back to api_error (never leaks foreign vocabulary)",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    strPtr("request_cancelled"),
					Message: "cancelled",
				},
			},
			expectedType: "api_error",
		},
		{
			name: "nil Error field defaults to api_error",
			input: &schemas.BifrostError{
				Error: nil,
			},
			expectedType: "api_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToAnthropicChatCompletionError(tt.input)

			if tt.expectNil {
				if result != nil {
					t.Fatalf("expected nil, got %+v", result)
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil result")
			}

			if result.Type != "error" {
				t.Errorf("expected top-level Type %q, got %q", "error", result.Type)
			}

			if result.Error.Type != tt.expectedType {
				t.Errorf("expected error Type %q, got %q", tt.expectedType, result.Error.Type)
			}
		})
	}
}

func TestNormalizeAnthropicErrorType(t *testing.T) {
	tests := []struct {
		name               string
		anthropicType      string
		expectedCanonical  string
		expectedStatusCode int
	}{
		{"invalid_request_error", "invalid_request_error", "invalid_request_error", 400},
		{"authentication_error", "authentication_error", "authentication_error", 401},
		{"permission_error", "permission_error", "permission_denied", 403},
		{"not_found_error", "not_found_error", "not_found_error", 404},
		{"rate_limit_error", "rate_limit_error", "rate_limit_exceeded", 429},
		{
			// billing_error must NOT collapse into rate_limit_exceeded — Anthropic
			// has a genuinely distinct budget/quota signal, confirmed via the
			// official schema (BetaErrorType enum). Status is 402, not 429:
			// deterministic account exhaustion is a PERMANENT per-key failure in
			// core/utils.go's perKeyFailureStatusCodes classification, matching
			// the identical, deliberate 402 in governance's DecisionBudgetExceeded
			// for the same canonical Error.Type.
			name:               "billing_error maps to insufficient_quota, status 402 (permanent, not transient retry)",
			anthropicType:      "billing_error",
			expectedCanonical:  "insufficient_quota",
			expectedStatusCode: 402,
		},
		{
			// overloaded_error (HTTP 529) is Anthropic's own capacity being
			// overloaded, not the caller's rate — must NOT be classified as a
			// rate limit. StatusCode is kept at Anthropic's real native 529
			// (not canonicalized to 503) so anthropic-python's status-code-based
			// SDK dispatch raises OverloadedError instead of a generic
			// InternalServerError.
			name:               "overloaded_error maps to service_unavailable, not rate_limit_exceeded",
			anthropicType:      "overloaded_error",
			expectedCanonical:  "service_unavailable",
			expectedStatusCode: 529,
		},
		{"timeout_error", "timeout_error", "request_timeout", 504},
		{"api_error", "api_error", "server_error", 500},
		{
			name:               "unrecognized type falls back to server_error/500",
			anthropicType:      "some_future_anthropic_error_type",
			expectedCanonical:  "server_error",
			expectedStatusCode: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical, status, _ := normalizeAnthropicErrorType(tt.anthropicType)
			if canonical != tt.expectedCanonical {
				t.Errorf("expected canonical type %q, got %q", tt.expectedCanonical, canonical)
			}
			if status != tt.expectedStatusCode {
				t.Errorf("expected status %d, got %d", tt.expectedStatusCode, status)
			}
		})
	}
}

func TestStage1Stage2RoundTrip(t *testing.T) {
	// Every Anthropic-native error.type should survive a Stage1 (normalize-to-OpenAI)
	// -> Stage2 (translate-from-OpenAI) round trip unchanged, when the route
	// provider matches the actual serving provider (Anthropic -> /anthropic).
	// This is the same-provider passthrough case from the design (Case A) —
	// a client hitting /anthropic backed by Anthropic itself must see
	// Anthropic's own vocabulary, not a foreign or drifted value.
	anthropicTypes := []string{
		"invalid_request_error", "authentication_error", "permission_error",
		"not_found_error", "rate_limit_error", "billing_error",
		"overloaded_error", "timeout_error", "api_error",
	}

	for _, original := range anthropicTypes {
		t.Run(original, func(t *testing.T) {
			canonical, _, _ := normalizeAnthropicErrorType(original)
			bifrostErr := &schemas.BifrostError{
				Error: &schemas.ErrorField{Type: &canonical, Message: "test"},
			}
			result := ToAnthropicChatCompletionError(bifrostErr)
			if result.Error.Type != original {
				t.Errorf("round trip broke: %q -> canonical %q -> %q, want %q",
					original, canonical, result.Error.Type, original)
			}
		})
	}
}

// TestParseAnthropicError_UnrecognizedTypePreservesRealStatusCode is a
// regression test (found via codex review) for a fallback-path bug: Stage 1
// normalization must NOT clobber an already-correct, real HTTP status with
// normalizeAnthropicErrorType's generic 500 fallback when Anthropic sends an
// error.type this table doesn't recognize yet (schema drift / a future
// Anthropic error type). The real status HandleProviderAPIError already
// extracted from the HTTP response (e.g. a genuine 403) must survive
// unchanged — downgrading it to 500 would make core/utils.go's
// transientServerStatusCodes misclassify a permanent failure as retryable.
func TestParseAnthropicError_UnrecognizedTypePreservesRealStatusCode(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"some_future_anthropic_error_type","message":"a new error type we don't know about yet"}}`)

	// Build the response the way HandleProviderAPIError would see it: a real
	// 403 status with an unrecognized error.type in the body.
	resp := &fasthttp.Response{}
	resp.SetStatusCode(403)
	resp.SetBody(body)

	result := parseAnthropicError(resp)

	require.NotNil(t, result)
	require.NotNil(t, result.StatusCode, "StatusCode must still be set")
	assert.Equal(t, 403, *result.StatusCode, "real HTTP status must survive an unrecognized error.type, not be downgraded to the generic 500 fallback")
	require.NotNil(t, result.Error)
	require.NotNil(t, result.Error.Type)
	assert.Equal(t, "server_error", *result.Error.Type, "canonical type still falls back to server_error for unrecognized values — only the status must be preserved")
}
