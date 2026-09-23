package anthropic

import (
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
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
			name: "internal Type is preserved",
			input: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Type:    strPtr("request_cancelled"),
					Message: "cancelled",
				},
			},
			expectedType: "request_cancelled",
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
