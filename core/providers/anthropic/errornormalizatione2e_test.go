package anthropic_test

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// End-to-end regression coverage for the two-stage error normalization
// design: drives a real
// provider.ChatCompletion call against a mock Anthropic backend
// (llmtests.NewMockErrorServer) that returns Anthropic's genuine raw error
// envelope, then verifies Stage 1 (normalize-to-OpenAI, in errors.go
// parseAnthropicError) produces the canonical OpenAI type/status — and that
// re-encoding through Stage 2 (ToAnthropicChatCompletionError) round-trips
// back to Anthropic's own vocabulary, exactly as a client hitting
// /anthropic/v1/messages backed by Anthropic itself should see.
func TestAnthropicErrorNormalization_E2E(t *testing.T) {
	tests := []struct {
		name                string
		httpStatus          int
		anthropicBody       string
		expectCanonicalType string
		expectStatusCode    int
		expectAnthropicType string // after Stage 2 round trip
	}{
		{
			name:                "rate_limit_error normalizes to rate_limit_exceeded/429 and round-trips",
			httpStatus:          429,
			anthropicBody:       `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`,
			expectCanonicalType: "rate_limit_exceeded",
			expectStatusCode:    429,
			expectAnthropicType: "rate_limit_error",
		},
		{
			// The key regression this whole design exists for: billing_error
			// must NOT collapse into rate_limit_exceeded/rate_limit_error.
			// Status is 402 (permanent per-key failure classification in
			// core/utils.go), matching governance's identical, deliberate 402
			// for the same canonical insufficient_quota type.
			name:                "billing_error normalizes to insufficient_quota/402 and round-trips to billing_error, NOT rate_limit_error",
			httpStatus:          400,
			anthropicBody:       `{"type":"error","error":{"type":"billing_error","message":"Your account has insufficient credits"}}`,
			expectCanonicalType: "insufficient_quota",
			expectStatusCode:    402,
			expectAnthropicType: "billing_error",
		},
		{
			// overloaded_error is documented at HTTP 529 by Anthropic — verifies
			// Stage 1 keeps the real native 529 status (not canonicalized to 503)
			// so anthropic-python's status-code-based SDK dispatch still raises
			// OverloadedError, while the canonical *type* is still
			// service_unavailable and never misclassified as a rate limit.
			name:                "overloaded_error (raw 529) normalizes to service_unavailable/529, not rate_limit_exceeded",
			httpStatus:          529,
			anthropicBody:       `{"type":"error","error":{"type":"overloaded_error","message":"Anthropic's API is temporarily overloaded"}}`,
			expectCanonicalType: "service_unavailable",
			expectStatusCode:    529,
			expectAnthropicType: "overloaded_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := llmtests.NewMockErrorServer(tt.httpStatus, tt.anthropicBody, "application/json")
			defer server.Close()

			provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:             server.URL,
					AllowPrivateNetwork: true,
				},
			}, &llmtests.NoOpTestLogger{})

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()

			_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{}, &schemas.BifrostChatRequest{
				Provider: schemas.Anthropic,
				Model:    "claude-3-5-sonnet-20241022",
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtrE2E("hi")}}},
			})

			require.NotNil(t, bifrostErr, "expected a BifrostError from the mock error backend")
			require.NotNil(t, bifrostErr.Error, "expected ErrorField to be populated")
			require.NotNil(t, bifrostErr.Error.Type, "expected Stage 1 to set a canonical error type")

			// Stage 1 assertions: canonical OpenAI vocabulary + corrected status
			require.Equal(t, tt.expectCanonicalType, *bifrostErr.Error.Type, "Stage 1 canonical type mismatch")
			require.NotNil(t, bifrostErr.StatusCode, "expected Stage 1 to set a canonical StatusCode")
			require.Equal(t, tt.expectStatusCode, *bifrostErr.StatusCode, "Stage 1 canonical status mismatch")

			// Stage 2 round trip: same-provider passthrough (Case A) must
			// reconstruct Anthropic's own original vocabulary, not leak the
			// intermediate OpenAI-canonical value.
			anthropicErr := anthropic.ToAnthropicChatCompletionError(bifrostErr)
			require.NotNil(t, anthropicErr)
			require.Equal(t, tt.expectAnthropicType, anthropicErr.Error.Type, "Stage 2 round-trip type mismatch")
		})
	}
}

func strPtrE2E(s string) *string { return &s }
