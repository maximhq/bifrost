package openai_test

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// End-to-end regression coverage for the two-stage error normalization
// design. OpenAI is the
// canonical vocabulary by design (Bifrost's IR is OpenAI's own Responses API
// schema — core/schemas/responses.go), so Stage 1 for OpenAI is expected to
// be near-identity: whatever OpenAI's own error body says should pass through
// unchanged, just with StatusCode always populated.
func TestOpenAIErrorNormalization_E2E_NonStreaming(t *testing.T) {
	server := llmtests.NewMockErrorServer(429, `{"error":{"type":"rate_limit_exceeded","code":"rate_limit_exceeded","message":"You exceeded your current quota"}}`, "application/json")
	defer server.Close()

	provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:             server.URL,
			AllowPrivateNetwork: true,
		},
	}, &llmtests.NoOpTestLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	s := "hi"
	_, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{}, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &s}}},
	})

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Type)
	require.Equal(t, "rate_limit_exceeded", *bifrostErr.Error.Type, "OpenAI's own type must pass through unchanged (identity Stage 1)")
	require.NotNil(t, bifrostErr.StatusCode)
	require.Equal(t, 429, *bifrostErr.StatusCode)
}

// Regression test for issue #5040: a mid-stream OpenAI Responses API `error`
// event previously left BifrostError.StatusCode nil, which the transport
// layer (transports/bifrost-http/integrations/utils.go sendStreamError) then
// defaulted to 500 instead of the correct 429. Drives a real mock SSE stream
// through provider.ResponsesStream to verify the fix
// (core/providers/openai/errors.go StatusCodeForResponsesStreamErrorCode,
// wired into openai.go) end-to-end rather than just unit-testing the helper.
func TestOpenAIErrorNormalization_E2E_StreamingIssue5040(t *testing.T) {
	tests := []struct {
		name           string
		sseBody        string
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "mid-stream rate_limit_exceeded error event sets StatusCode 429, not nil/500",
			sseBody:        "data: {\"type\":\"error\",\"sequence_number\":1,\"code\":\"rate_limit_exceeded\",\"message\":\"Rate limit reached\"}\n\n",
			expectedStatus: 429,
			expectedCode:   "rate_limit_exceeded",
		},
		{
			name:           "mid-stream response.failed with server_error code sets StatusCode 500",
			sseBody:        "data: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"Internal error\"}}}\n\n",
			expectedStatus: 500,
			expectedCode:   "server_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := llmtests.NewMockErrorServer(200, tt.sseBody, "text/event-stream")
			defer server.Close()

			provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:             server.URL,
					AllowPrivateNetwork: true,
				},
			}, &llmtests.NoOpTestLogger{})

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()

			postHookRunner := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return result, err
			}

			s := "hi"
			responseChan, initErr := provider.ResponsesStream(ctx, postHookRunner, func(context.Context) {}, schemas.Key{}, &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
				Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &s}}},
			})
			require.Nil(t, initErr)
			require.NotNil(t, responseChan)

			var gotErr *schemas.BifrostError
			for chunk := range responseChan {
				if chunk.BifrostError != nil {
					gotErr = chunk.BifrostError
					break
				}
			}

			require.NotNil(t, gotErr, "expected a BifrostError chunk from the mock SSE error stream")
			require.NotNil(t, gotErr.StatusCode, "issue #5040 regression: StatusCode must not be nil for mid-stream errors")
			require.Equal(t, tt.expectedStatus, *gotErr.StatusCode)
			require.NotNil(t, gotErr.Error.Code)
			require.Equal(t, tt.expectedCode, *gotErr.Error.Code)
		})
	}
}

// TestOpenAIErrorNormalization_E2E_ResponsesStreamTypeOnlyFallback is a
// regression test (found via greptile review): the Responses-stream
// `response.failed`/`error` branches only ever copied `.code`/`.message`
// from the nested error, never `.type`, so a backend emitting only
// `error.type` (no `error.code`) on an in-body SSE error fell through to
// the generic 500 fallback in StatusCodeForResponsesStreamErrorCode instead
// of correctly resolving via the Error.Type fallback path.
func TestOpenAIErrorNormalization_E2E_ResponsesStreamTypeOnlyFallback(t *testing.T) {
	tests := []struct {
		name           string
		sseBody        string
		expectedStatus int
		expectedType   string
	}{
		{
			name:           "mid-stream error event with only type (no code) resolves via Error.Type fallback",
			sseBody:        "data: {\"type\":\"error\",\"sequence_number\":1,\"error\":{\"type\":\"context_length_exceeded\",\"message\":\"maximum context length exceeded\"}}\n\n",
			expectedStatus: 400,
			expectedType:   "context_length_exceeded",
		},
		{
			name:           "mid-stream response.failed event with only type (no code) resolves via Error.Type fallback",
			sseBody:        "data: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"error\":{\"type\":\"content_policy_violation\",\"message\":\"content policy violation\"}}}\n\n",
			expectedStatus: 400,
			expectedType:   "content_policy_violation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := llmtests.NewMockErrorServer(200, tt.sseBody, "text/event-stream")
			defer server.Close()

			provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:             server.URL,
					AllowPrivateNetwork: true,
				},
			}, &llmtests.NoOpTestLogger{})

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()

			postHookRunner := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return result, err
			}

			s := "hi"
			responseChan, initErr := provider.ResponsesStream(ctx, postHookRunner, func(context.Context) {}, schemas.Key{}, &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
				Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &s}}},
			})
			require.Nil(t, initErr)
			require.NotNil(t, responseChan)

			var gotErr *schemas.BifrostError
			for chunk := range responseChan {
				if chunk.BifrostError != nil {
					gotErr = chunk.BifrostError
					break
				}
			}

			require.NotNil(t, gotErr, "expected a BifrostError chunk from the mock SSE error stream")
			require.NotNil(t, gotErr.Error.Type, "Error.Type must be populated from the nested error.type field")
			require.Equal(t, tt.expectedType, *gotErr.Error.Type)
			require.NotNil(t, gotErr.StatusCode, "StatusCode must resolve via the Error.Type fallback, not default to nil/500")
			require.Equal(t, tt.expectedStatus, *gotErr.StatusCode)
		})
	}
}

// Regression test for the same #5040-pattern bug found in the Chat Completions
// streaming path (HandleOpenAIChatCompletionStreaming) — the shared streaming
// handler reused by all 19 OpenAI-compatible providers (vLLM, sglang, Ollama,
// Groq, etc.), not just native OpenAI. Self-hosted OpenAI-compatible backends
// commonly send an in-body `{"error": {...}}` chunk on an otherwise-200 SSE
// stream with no explicit status_code field — this previously left
// BifrostError.StatusCode nil, defaulting to 500 downstream, exactly like the
// Responses-stream case, just undetected until a follow-up audit.
func TestOpenAIErrorNormalization_E2E_ChatCompletionStreamingInBodyError(t *testing.T) {
	// vLLM/sglang-style in-body error chunk: no top-level status_code field,
	// only a nested error object — mirrors what self-hosted OpenAI-compatible
	// backends actually send.
	sseBody := "data: {\"error\":{\"message\":\"model is overloaded\",\"code\":\"server_error\"}}\n\n"
	server := llmtests.NewMockErrorServer(200, sseBody, "text/event-stream")
	defer server.Close()

	provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:             server.URL,
			AllowPrivateNetwork: true,
		},
	}, &llmtests.NoOpTestLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	postHookRunner := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}

	s := "hi"
	responseChan, initErr := provider.ChatCompletionStream(ctx, postHookRunner, func(context.Context) {}, schemas.Key{}, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &s}}},
	})
	require.Nil(t, initErr)
	require.NotNil(t, responseChan)

	var gotErr *schemas.BifrostError
	for chunk := range responseChan {
		if chunk.BifrostError != nil {
			gotErr = chunk.BifrostError
			break
		}
	}

	require.NotNil(t, gotErr, "expected a BifrostError chunk from the mock in-body-error SSE stream")
	require.NotNil(t, gotErr.StatusCode, "StatusCode must not be nil for in-body chat-completion-stream errors with no explicit status_code")
	require.Equal(t, 500, *gotErr.StatusCode)
}
