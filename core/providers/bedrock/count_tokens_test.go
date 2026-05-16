package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestIsCountTokensUnsupported(t *testing.T) {
	tests := []struct {
		name     string
		err      *schemas.BifrostError
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "nil error field",
			err:      &schemas.BifrostError{},
			expected: false,
		},
		{
			name: "matching bedrock error message",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "The provided model doesn't support counting tokens.",
				},
			},
			expected: true,
		},
		{
			name: "matching message with different casing",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "the provided model DOESN'T SUPPORT COUNTING TOKENS.",
				},
			},
			expected: true,
		},
		{
			name: "unrelated error message",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "access denied",
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, isCountTokensUnsupported(tc.err))
		})
	}
}

func TestEstimateTokenCount(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected int
	}{
		{
			name:     "empty input",
			input:    []byte{},
			expected: 0,
		},
		{
			name:     "nil input",
			input:    nil,
			expected: 0,
		},
		{
			name:     "exact multiple of 4",
			input:    make([]byte, 100),
			expected: 25,
		},
		{
			name:     "rounds up",
			input:    make([]byte, 101),
			expected: 26,
		},
		{
			name:     "single byte",
			input:    []byte("x"),
			expected: 1,
		},
		{
			name:     "realistic json body",
			input:    []byte(`{"messages":[{"role":"user","content":"Hello, how are you today?"}],"model":"us.anthropic.claude-sonnet-4-6"}`),
			expected: 28,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, estimateTokenCount(tc.input))
		})
	}
}
