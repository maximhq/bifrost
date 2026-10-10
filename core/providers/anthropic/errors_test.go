package anthropic

import (
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestToAnthropicChatCompletionError checks that the envelope's error.type is always a
// documented Anthropic type (or a Bifrost wire-level type) that agrees with the status.
func TestToAnthropicChatCompletionError(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	intPtr := func(i int) *int { return &i }

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
			name: "nil Type on internal error without status is api_error",
			input: &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Type:    nil,
					Message: "connection failed",
				},
			},
			expectedType: "api_error",
		},
		{
			name: "empty Type on internal error without status is api_error",
			input: &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Type:    strPtr(""),
					Message: "boom",
				},
			},
			expectedType: "api_error",
		},
		{
			name:         "nil Error field on internal error is api_error",
			input:        &schemas.BifrostError{IsBifrostError: true, Error: nil},
			expectedType: "api_error",
		},
		{
			name:         "nil Error field on non-bifrost error follows its 400 status",
			input:        &schemas.BifrostError{Error: nil},
			expectedType: "invalid_request_error",
		},
		{
			name: "valid Type is preserved",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    strPtr("rate_limit_error"),
					Message: "rate limited",
				},
			},
			expectedType: "rate_limit_error",
		},
		{
			name: "valid Type wins over status",
			input: &schemas.BifrostError{
				StatusCode: intPtr(529),
				Error:      &schemas.ErrorField{Type: strPtr("overloaded_error"), Message: "overloaded"},
			},
			expectedType: "overloaded_error",
		},
		{
			name: "bifrost wire Type is preserved",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    strPtr("request_cancelled"),
					Message: "cancelled",
				},
			},
			expectedType: "request_cancelled",
		},
		{
			name: "validation error without Type is invalid_request_error",
			input: &schemas.BifrostError{
				StatusCode: intPtr(400),
				Error:      &schemas.ErrorField{Message: "Invalid JSON"},
			},
			expectedType: "invalid_request_error",
		},
		{
			name:         "unsupported operation is invalid_request_error",
			input:        providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, schemas.Ollama),
			expectedType: "invalid_request_error",
		},
		{
			name: "gRPC INVALID_ARGUMENT maps to invalid_request_error",
			input: &schemas.BifrostError{
				StatusCode: intPtr(400),
				Error:      &schemas.ErrorField{Type: strPtr("INVALID_ARGUMENT"), Message: "bad"},
			},
			expectedType: "invalid_request_error",
		},
		{
			name:         "gRPC FAILED_PRECONDITION maps to invalid_request_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(400), Error: &schemas.ErrorField{Type: strPtr("FAILED_PRECONDITION")}},
			expectedType: "invalid_request_error",
		},
		{
			name:         "gRPC UNAUTHENTICATED maps to authentication_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(401), Error: &schemas.ErrorField{Type: strPtr("UNAUTHENTICATED")}},
			expectedType: "authentication_error",
		},
		{
			name:         "gRPC PERMISSION_DENIED maps to permission_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(403), Error: &schemas.ErrorField{Type: strPtr("PERMISSION_DENIED")}},
			expectedType: "permission_error",
		},
		{
			name:         "gRPC NOT_FOUND maps to not_found_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(404), Error: &schemas.ErrorField{Type: strPtr("NOT_FOUND")}},
			expectedType: "not_found_error",
		},
		{
			name:         "gRPC RESOURCE_EXHAUSTED maps to rate_limit_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(429), Error: &schemas.ErrorField{Type: strPtr("RESOURCE_EXHAUSTED")}},
			expectedType: "rate_limit_error",
		},
		{
			name:         "gRPC UNAVAILABLE maps to overloaded_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(503), Error: &schemas.ErrorField{Type: strPtr("UNAVAILABLE")}},
			expectedType: "overloaded_error",
		},
		{
			name:         "gRPC INTERNAL maps to api_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(500), Error: &schemas.ErrorField{Type: strPtr("INTERNAL")}},
			expectedType: "api_error",
		},
		{
			name:         "gRPC UNAVAILABLE without status maps to overloaded_error",
			input:        &schemas.BifrostError{Error: &schemas.ErrorField{Type: strPtr("UNAVAILABLE")}},
			expectedType: "overloaded_error",
		},
		{
			name:         "gRPC DEADLINE_EXCEEDED without status maps to api_error",
			input:        &schemas.BifrostError{Error: &schemas.ErrorField{Type: strPtr("DEADLINE_EXCEEDED")}},
			expectedType: "api_error",
		},
		{
			name:         "gRPC DEADLINE_EXCEEDED with 504 follows status to timeout_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(504), Error: &schemas.ErrorField{Type: strPtr("DEADLINE_EXCEEDED")}},
			expectedType: "timeout_error",
		},
		{
			name:         "gRPC UNAVAILABLE with 429 follows status to rate_limit_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(429), Error: &schemas.ErrorField{Type: strPtr("UNAVAILABLE")}},
			expectedType: "rate_limit_error",
		},
		{
			name:         "gRPC INVALID_ARGUMENT with 502 follows status class to api_error",
			input:        &schemas.BifrostError{StatusCode: intPtr(502), Error: &schemas.ErrorField{Type: strPtr("INVALID_ARGUMENT")}},
			expectedType: "api_error",
		},
		{
			name:         "documented Type in the other status class follows status",
			input:        &schemas.BifrostError{StatusCode: intPtr(500), Error: &schemas.ErrorField{Type: strPtr("rate_limit_error")}},
			expectedType: "api_error",
		},
		{
			name:         "documented server Type with 4xx status follows status",
			input:        &schemas.BifrostError{StatusCode: intPtr(429), Error: &schemas.ErrorField{Type: strPtr("api_error")}},
			expectedType: "rate_limit_error",
		},
		{
			name:         "documented Type in the same status class is preserved",
			input:        &schemas.BifrostError{StatusCode: intPtr(500), Error: &schemas.ErrorField{Type: strPtr("overloaded_error")}},
			expectedType: "overloaded_error",
		},
		{
			name:         "bifrost wire Type is preserved despite status",
			input:        &schemas.BifrostError{StatusCode: intPtr(504), Error: &schemas.ErrorField{Type: strPtr("request_timed_out")}},
			expectedType: "request_timed_out",
		},
		{
			name: "unknown Type falls back to status",
			input: &schemas.BifrostError{
				StatusCode: intPtr(403),
				Error:      &schemas.ErrorField{Type: strPtr("gemini_api_error"), Message: "denied"},
			},
			expectedType: "permission_error",
		},
		{
			name:         "unknown Type without status is api_error",
			input:        &schemas.BifrostError{Error: &schemas.ErrorField{Type: strPtr("server_error")}},
			expectedType: "api_error",
		},
		{name: "status 401", input: &schemas.BifrostError{StatusCode: intPtr(401)}, expectedType: "authentication_error"},
		{name: "status 404", input: &schemas.BifrostError{StatusCode: intPtr(404)}, expectedType: "not_found_error"},
		{name: "status 413", input: &schemas.BifrostError{StatusCode: intPtr(413)}, expectedType: "request_too_large"},
		{name: "status 422", input: &schemas.BifrostError{StatusCode: intPtr(422)}, expectedType: "invalid_request_error"},
		{name: "status 429", input: &schemas.BifrostError{StatusCode: intPtr(429)}, expectedType: "rate_limit_error"},
		{name: "status 502", input: &schemas.BifrostError{StatusCode: intPtr(502)}, expectedType: "api_error"},
		{name: "status 529", input: &schemas.BifrostError{StatusCode: intPtr(529)}, expectedType: "overloaded_error"},
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

func TestToAnthropicChatCompletionErrorNeverEmitsEmptyMessage(t *testing.T) {
	statusBadRequest := 400
	tests := []struct {
		name     string
		input    *schemas.BifrostError
		expected string
	}{
		{
			name:     "missing error field",
			input:    &schemas.BifrostError{},
			expected: "unknown error",
		},
		{
			name: "empty provider message falls back to status",
			input: &schemas.BifrostError{
				StatusCode: &statusBadRequest,
				Error:      &schemas.ErrorField{},
			},
			expected: "HTTP 400 error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToAnthropicChatCompletionError(tt.input)
			if result.Error.Message != tt.expected {
				t.Fatalf("expected message %q, got %q", tt.expected, result.Error.Message)
			}
		})
	}
}

func TestAnthropicMessageErrorDetailsMarshal(t *testing.T) {
	withDetails := &AnthropicMessageError{
		Type: "error",
		Error: AnthropicMessageErrorStruct{
			Type:    "invalid_request_error",
			Message: "no thread state",
			Details: &AnthropicMessageErrorDetails{ErrorCode: "thread_unsupported_request"},
		},
	}
	data, err := providerUtils.MarshalSorted(withDetails)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	if !strings.Contains(string(data), `"error_code":"thread_unsupported_request"`) {
		t.Errorf("expected details.error_code in output, got %s", data)
	}

	withoutDetails := &AnthropicMessageError{
		Type:  "error",
		Error: AnthropicMessageErrorStruct{Type: "api_error", Message: "boom"},
	}
	data, err = providerUtils.MarshalSorted(withoutDetails)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	if strings.Contains(string(data), "details") {
		t.Errorf("details must be omitted when nil so existing errors stay byte-identical, got %s", data)
	}
}
