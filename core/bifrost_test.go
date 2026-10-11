package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/internal/memtest"
	"github.com/maximhq/bifrost/core/keyselectors"
	mistralprovider "github.com/maximhq/bifrost/core/providers/mistral"
	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Mock time.Sleep to avoid real delays in tests
var mockSleep func(time.Duration)

// Override time.Sleep in tests and setup logger
func init() {
	mockSleep = func(d time.Duration) {
		// Do nothing in tests to avoid real delays
	}
}

// Helper function to create test config with specific retry settings
func createTestConfig(maxRetries int, initialBackoff, maxBackoff time.Duration) *schemas.ProviderConfig {
	return &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			MaxRetries:          maxRetries,
			RetryBackoffInitial: initialBackoff,
			RetryBackoffMax:     maxBackoff,
		},
	}
}

// Helper function to create a BifrostError
func createBifrostError(message string, statusCode *int, errorType *string, isBifrostError bool) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: isBifrostError,
		StatusCode:     statusCode,
		Error: &schemas.ErrorField{
			Message: message,
			Type:    errorType,
		},
	}
}

// Test executeRequestWithRetries - success scenarios
func TestExecuteRequestWithRetries_SuccessScenarios(t *testing.T) {
	config := createTestConfig(3, 100*time.Millisecond, 1*time.Second)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	logger := NewDefaultLogger(schemas.LogLevelError)
	// Adding dummy tracer to the context
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	// Test immediate success
	t.Run("ImmediateSuccess", func(t *testing.T) {
		callCount := 0
		handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
			callCount++
			return "success", nil
		}

		result, err := executeRequestWithRetries(
			ctx,
			config,
			handler,
			nil,
			schemas.ChatCompletionRequest,
			schemas.OpenAI,
			"gpt-4",
			nil,
			logger,
		)

		if callCount != 1 {
			t.Errorf("Expected 1 call, got %d", callCount)
		}
		if result != "success" {
			t.Errorf("Expected 'success', got %s", result)
		}
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
	})

	// Test success after retries
	t.Run("SuccessAfterRetries", func(t *testing.T) {
		callCount := 0
		handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
			callCount++
			if callCount <= 2 {
				// First two calls fail with retryable error
				return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
			}
			// Third call succeeds
			return "success", nil
		}

		result, err := executeRequestWithRetries(
			ctx,
			config,
			handler,
			nil,
			schemas.ChatCompletionRequest,
			schemas.OpenAI,
			"gpt-4",
			nil,
			logger,
		)

		if callCount != 3 {
			t.Errorf("Expected 3 calls, got %d", callCount)
		}
		if result != "success" {
			t.Errorf("Expected 'success', got %s", result)
		}
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
	})
}

// Test executeRequestWithRetries - retry limits
func TestExecuteRequestWithRetries_RetryLimits(t *testing.T) {
	config := createTestConfig(2, 100*time.Millisecond, 1*time.Second)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)
	t.Run("ExceedsMaxRetries", func(t *testing.T) {
		callCount := 0
		handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
			callCount++
			// Always fail with retryable error
			return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
		}

		result, err := executeRequestWithRetries(
			ctx,
			config,
			handler,
			nil,
			schemas.ChatCompletionRequest,
			schemas.OpenAI,
			"gpt-4",
			nil,
			logger,
		)

		// Should try: initial + 2 retries = 3 total attempts
		if callCount != 3 {
			t.Errorf("Expected 3 calls (initial + 2 retries), got %d", callCount)
		}
		if result != "" {
			t.Errorf("Expected empty result, got %s", result)
		}
		if err == nil {
			t.Fatal("Expected error after exceeding max retries")
		}
		if err.Error == nil {
			t.Fatal("Expected error structure, got nil")
		}
		if err.Error.Message != "rate limit exceeded" {
			t.Errorf("Expected rate limit error, got %s", err.Error.Message)
		}
	})
}

// Test executeRequestWithRetries - non-retryable errors
func TestExecuteRequestWithRetries_NonRetryableErrors(t *testing.T) {
	config := createTestConfig(3, 100*time.Millisecond, 1*time.Second)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	testCases := []struct {
		name  string
		error *schemas.BifrostError
	}{
		{
			name:  "BifrostError",
			error: createBifrostError("validation error", nil, nil, true),
		},
		{
			name:  "RequestCancelled",
			error: createBifrostError("request cancelled", nil, Ptr(schemas.ErrRequestCancelled), false),
		},
		{
			name:  "Non-retryable status code",
			error: createBifrostError("bad request", Ptr(400), nil, false),
		},
		{
			name:  "Non-retryable error message",
			error: createBifrostError("invalid model", nil, nil, false),
		},
	}
	logger := NewDefaultLogger(schemas.LogLevelError)
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			callCount := 0
			handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
				callCount++
				return "", tc.error
			}

			result, err := executeRequestWithRetries(
				ctx,
				config,
				handler,
				nil,
				schemas.ChatCompletionRequest,
				schemas.OpenAI,
				"gpt-4",
				nil,
				logger,
			)

			if callCount != 1 {
				t.Errorf("Expected 1 call (no retries), got %d", callCount)
			}
			if result != "" {
				t.Errorf("Expected empty result, got %s", result)
			}
			if err != tc.error {
				t.Error("Expected original error to be returned")
			}
		})
	}
}

// Test executeRequestWithRetries - retryable conditions
func TestExecuteRequestWithRetries_RetryableConditions(t *testing.T) {
	config := createTestConfig(1, 100*time.Millisecond, 1*time.Second)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	testCases := []struct {
		name  string
		error *schemas.BifrostError
	}{
		{
			name:  "StatusCode_500",
			error: createBifrostError("internal server error", Ptr(500), nil, false),
		},
		{
			name:  "StatusCode_502",
			error: createBifrostError("bad gateway", Ptr(502), nil, false),
		},
		{
			name:  "StatusCode_503",
			error: createBifrostError("service unavailable", Ptr(503), nil, false),
		},
		{
			name:  "StatusCode_504",
			error: createBifrostError("gateway timeout", Ptr(504), nil, false),
		},
		{
			name:  "StatusCode_429",
			error: createBifrostError("too many requests", Ptr(429), nil, false),
		},
		{
			name:  "ErrProviderDoRequest",
			error: createBifrostError(schemas.ErrProviderDoRequest, nil, nil, false),
		},
		{
			name:  "RateLimitMessage",
			error: createBifrostError("rate limit exceeded", nil, nil, false),
		},
		{
			name:  "RateLimitType",
			error: createBifrostError("some error", nil, Ptr("rate_limit"), false),
		},
	}
	logger := NewDefaultLogger(schemas.LogLevelError)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			callCount := 0
			handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
				callCount++
				return "", tc.error
			}

			result, err := executeRequestWithRetries(
				ctx,
				config,
				handler,
				nil,
				schemas.ChatCompletionRequest,
				schemas.OpenAI,
				"gpt-4",
				nil,
				logger,
			)

			// Should try: initial + 1 retry = 2 total attempts
			if callCount != 2 {
				t.Errorf("Expected 2 calls (initial + 1 retry), got %d", callCount)
			}
			if result != "" {
				t.Errorf("Expected empty result, got %s", result)
			}
			if err != tc.error {
				t.Error("Expected original error to be returned")
			}
		})
	}
}

// Test calculateBackoff - exponential growth (base calculations without jitter)
func TestCalculateBackoff_ExponentialGrowth(t *testing.T) {
	config := createTestConfig(5, 100*time.Millisecond, 5*time.Second)

	// Test the base exponential calculation by checking that results fall within expected ranges
	// Since we can't easily mock rand.Float64, we'll test the bounds instead
	testCases := []struct {
		attempt     int
		minExpected time.Duration
		maxExpected time.Duration
	}{
		{0, 80 * time.Millisecond, 120 * time.Millisecond},    // 100ms ± 20%
		{1, 160 * time.Millisecond, 240 * time.Millisecond},   // 200ms ± 20%
		{2, 320 * time.Millisecond, 480 * time.Millisecond},   // 400ms ± 20%
		{3, 640 * time.Millisecond, 960 * time.Millisecond},   // 800ms ± 20%
		{4, 1280 * time.Millisecond, 1920 * time.Millisecond}, // 1600ms ± 20%
		{5, 2560 * time.Millisecond, 3840 * time.Millisecond}, // 3200ms ± 20%
		{10, 4 * time.Second, 6 * time.Second},                // should be capped at max (5s) ± 20%
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Attempt_%d", tc.attempt), func(t *testing.T) {
			backoff := calculateBackoff(tc.attempt, config)
			if backoff < tc.minExpected || backoff > tc.maxExpected {
				t.Errorf("Backoff %v outside expected range [%v, %v]", backoff, tc.minExpected, tc.maxExpected)
			}
		})
	}
}

// Test calculateBackoff - jitter bounds
func TestCalculateBackoff_JitterBounds(t *testing.T) {
	config := createTestConfig(3, 100*time.Millisecond, 5*time.Second)

	// Test jitter bounds for multiple attempts
	for attempt := 0; attempt < 3; attempt++ {
		t.Run(fmt.Sprintf("Attempt_%d_JitterBounds", attempt), func(t *testing.T) {
			// Calculate expected base backoff
			baseBackoff := config.NetworkConfig.RetryBackoffInitial * time.Duration(1<<uint(attempt))
			if baseBackoff > config.NetworkConfig.RetryBackoffMax {
				baseBackoff = config.NetworkConfig.RetryBackoffMax
			}

			// Test multiple samples to verify jitter bounds
			for i := 0; i < 100; i++ {
				backoff := calculateBackoff(attempt, config)

				// Jitter should be ±20% (0.8 to 1.2 multiplier), but capped at configured max
				minExpected := time.Duration(float64(baseBackoff) * 0.8)
				maxExpected := min(time.Duration(float64(baseBackoff)*1.2), config.NetworkConfig.RetryBackoffMax)

				if backoff < minExpected || backoff > maxExpected {
					t.Errorf("Backoff %v outside expected range [%v, %v] for attempt %d",
						backoff, minExpected, maxExpected, attempt)
				}
			}
		})
	}
}

// Test calculateBackoff - max backoff cap
func TestCalculateBackoff_MaxBackoffCap(t *testing.T) {
	config := createTestConfig(10, 100*time.Millisecond, 500*time.Millisecond)

	// High attempt numbers should be capped at max backoff
	for attempt := 5; attempt < 10; attempt++ {
		backoff := calculateBackoff(attempt, config)

		// Jitter should never exceed the configured maximum
		if backoff > config.NetworkConfig.RetryBackoffMax {
			t.Errorf("Backoff %v exceeds configured max %v for attempt %d",
				backoff, config.NetworkConfig.RetryBackoffMax, attempt)
		}
	}
}

// Test IsRateLimitErrorMessage - all patterns
func TestIsRateLimitError_AllPatterns(t *testing.T) {
	// Test all patterns from rateLimitPatterns
	patterns := []string{
		"rate limit",
		"rate_limit",
		"ratelimit",
		"too many requests",
		"quota exceeded",
		"quota_exceeded",
		"request limit",
		"throttled",
		"throttling",
		"rate exceeded",
		"limit exceeded",
		"requests per",
		"rpm exceeded",
		"tpm exceeded",
		"tokens per minute",
		"requests per minute",
		"requests per second",
		"api rate limit",
		"usage limit",
		"concurrent requests limit",
		"burst_rate",
		"rate increased",
	}

	for _, pattern := range patterns {
		t.Run(fmt.Sprintf("Pattern_%s", strings.ReplaceAll(pattern, " ", "_")), func(t *testing.T) {
			// Test exact match
			if !IsRateLimitErrorMessage(pattern) {
				t.Errorf("Pattern '%s' should be detected as rate limit error", pattern)
			}

			// Test case insensitive - uppercase
			if !IsRateLimitErrorMessage(strings.ToUpper(pattern)) {
				t.Errorf("Uppercase pattern '%s' should be detected as rate limit error", strings.ToUpper(pattern))
			}

			// Test case insensitive - mixed case
			if !IsRateLimitErrorMessage(cases.Title(language.English).String(pattern)) {
				t.Errorf("Title case pattern '%s' should be detected as rate limit error", cases.Title(language.English).String(pattern))
			}

			// Test as part of larger message
			message := fmt.Sprintf("Error: %s occurred", pattern)
			if !IsRateLimitErrorMessage(message) {
				t.Errorf("Pattern '%s' in message '%s' should be detected", pattern, message)
			}

			// Test with prefix and suffix
			message = fmt.Sprintf("API call failed due to %s - please retry later", pattern)
			if !IsRateLimitErrorMessage(message) {
				t.Errorf("Pattern '%s' in complex message should be detected", pattern)
			}
		})
	}
}

// Test IsRateLimitErrorMessage - negative cases
func TestIsRateLimitError_NegativeCases(t *testing.T) {
	negativeCases := []string{
		"",
		"invalid request",
		"authentication failed",
		"model not found",
		"internal server error",
		"bad gateway",
		"service unavailable",
		"timeout",
		"connection refused",
		"rate",     // partial match shouldn't trigger
		"limit",    // partial match shouldn't trigger
		"quota",    // partial match shouldn't trigger
		"throttle", // partial match shouldn't trigger (need 'throttled' or 'throttling')
	}

	for _, testCase := range negativeCases {
		t.Run(fmt.Sprintf("Negative_%s", strings.ReplaceAll(testCase, " ", "_")), func(t *testing.T) {
			if IsRateLimitErrorMessage(testCase) {
				t.Errorf("Message '%s' should NOT be detected as rate limit error", testCase)
			}
		})
	}
}

// Test IsRateLimitErrorMessage - edge cases
func TestIsRateLimitError_EdgeCases(t *testing.T) {
	t.Run("EmptyString", func(t *testing.T) {
		if IsRateLimitErrorMessage("") {
			t.Error("Empty string should not be detected as rate limit error")
		}
	})

	t.Run("OnlyWhitespace", func(t *testing.T) {
		if IsRateLimitErrorMessage("   \t\n  ") {
			t.Error("Whitespace-only string should not be detected as rate limit error")
		}
	})

	t.Run("UnicodeCharacters", func(t *testing.T) {
		// Test with unicode characters that might affect case conversion
		message := "RATE LIMIT exceeded 🚫"
		if !IsRateLimitErrorMessage(message) {
			t.Error("Message with unicode should still detect rate limit pattern")
		}
	})

	t.Run("DashScopeErrorCode", func(t *testing.T) {
		// DashScope returns "limit_burst_rate" as the error code
		if !IsRateLimitErrorMessage("limit_burst_rate") {
			t.Error("DashScope error code 'limit_burst_rate' should be detected as rate limit error")
		}
	})

	t.Run("DashScopeErrorMessage", func(t *testing.T) {
		// DashScope returns this as the error message
		if !IsRateLimitErrorMessage("Request rate increased too quickly, please slow down and try again") {
			t.Error("DashScope error message should be detected as rate limit error")
		}
	})
}

// Test retry logging and attempt counting
func TestExecuteRequestWithRetries_LoggingAndCounting(t *testing.T) {
	config := createTestConfig(2, 50*time.Millisecond, 1*time.Second)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	// Capture calls and timing for verification
	var attemptCounts []int
	callCount := 0

	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		attemptCounts = append(attemptCounts, callCount)

		if callCount <= 2 {
			// First two calls fail with retryable error
			return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
		}
		// Third call succeeds
		return "success", nil
	}
	logger := NewDefaultLogger(schemas.LogLevelError)

	result, err := executeRequestWithRetries(
		ctx,
		config,
		handler,
		nil,
		schemas.ChatCompletionRequest,
		schemas.OpenAI,
		"gpt-4",
		nil,
		logger,
	)

	// Verify call progression
	if len(attemptCounts) != 3 {
		t.Errorf("Expected 3 attempts, got %d", len(attemptCounts))
	}

	for i, count := range attemptCounts {
		if count != i+1 {
			t.Errorf("Attempt %d should have call count %d, got %d", i, i+1, count)
		}
	}

	if result != "success" {
		t.Errorf("Expected success result, got %s", result)
	}

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

// TestCreateBaseProvider_CustomTypesafeBase pins that typesafe is accepted as a
// custom provider base and that the provider answers to the custom name, while
// a base outside SupportedBaseProviders is still refused.
func TestCreateBaseProvider_CustomTypesafeBase(t *testing.T) {
	bifrost := &Bifrost{logger: NewDefaultLogger(schemas.LogLevelError)}
	provider, err := bifrost.createBaseProvider("my-typesafe", &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1", DefaultRequestTimeoutInSeconds: 1},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.Typesafe,
		},
	})
	if err != nil {
		t.Fatalf("typesafe must be a supported base provider: %v", err)
	}
	if provider.GetProviderKey() != schemas.ModelProvider("my-typesafe") {
		t.Fatalf("expected custom provider key my-typesafe, got %q", provider.GetProviderKey())
	}

	_, err = bifrost.createBaseProvider("my-vertex", &schemas.ProviderConfig{
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Vertex},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported base provider type") {
		t.Fatalf("vertex is not a supported base provider, got err=%v", err)
	}
}

func TestHandleProviderRequest_OCROperationNotAllowed(t *testing.T) {
	providerConfig := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        "http://127.0.0.1:1",
			DefaultRequestTimeoutInSeconds: 1,
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "custom-mistral",
			BaseProviderType:  schemas.Mistral,
			AllowedRequests:   &schemas.AllowedRequests{},
		},
	}
	provider := mistralprovider.NewMistralProvider(providerConfig, NewDefaultLogger(schemas.LogLevelError))
	if provider.GetProviderKey() != schemas.ModelProvider("custom-mistral") {
		t.Fatalf("expected custom provider key, got %q", provider.GetProviderKey())
	}
	bifrost := &Bifrost{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	request := &ChannelMessage{
		Context: ctx,
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.OCRRequest,
			OCRRequest: &schemas.BifrostOCRRequest{
				Model: "custom-mistral/mistral-ocr-latest",
				Document: schemas.OCRDocument{
					Type:        schemas.OCRDocumentTypeDocumentURL,
					DocumentURL: Ptr("https://example.com/doc.pdf"),
				},
			},
		},
	}

	response, err := bifrost.handleProviderRequest(provider, providerConfig, request, schemas.Key{}, nil)
	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if err == nil {
		t.Fatal("expected unsupported operation error, got nil")
	}
	if err.Error == nil {
		t.Fatal("expected detailed error, got nil")
	}
	if err.Error.Code == nil || *err.Error.Code != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation code, got %#v", err.Error.Code)
	}
	if err.ExtraFields.Provider != schemas.ModelProvider("custom-mistral") {
		t.Fatalf("expected custom provider name, got %q", err.ExtraFields.Provider)
	}
	if err.ExtraFields.RequestType != schemas.OCRRequest {
		t.Fatalf("expected OCR request type, got %q", err.ExtraFields.RequestType)
	}
	if err.ExtraFields.OriginalModelRequested != "custom-mistral/mistral-ocr-latest" {
		t.Fatalf("expected model to be preserved, got %q", err.ExtraFields.OriginalModelRequested)
	}
}

// https://github.com/maximhq/bifrost/issues/6784: an OpenAI-compatible upstream that ends
// generation on finish_reason, never sends [DONE] and then parks the connection behind SSE
// heartbeats holds the stream open indefinitely — heartbeat bytes reset the idle timer without
// making semantic progress. custom_provider_config.does_not_send_done_marker is the operator's
// declaration that finish_reason is terminal for this upstream; this pins the whole path, from
// the account config through the per-attempt context stamp to the provider's read loop.
func TestCustomProviderDoesNotSendDoneMarkerEndsParkedStream(t *testing.T) {
	const chunk = `data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-repro","object":"chat.completion.chunk","created":1,"model":"repro-model",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			return
		}
		if _, err := w.Write([]byte(chunk)); err != nil {
			return
		}
		fl.Flush()

		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				fl.Flush()
			}
		}
	}))
	defer server.Close()

	const customProvider = schemas.ModelProvider("custom-openai")
	account := NewMockAccount()
	account.AddProviderWithBaseURL(customProvider, 1, 1, server.URL)
	account.configs[customProvider].NetworkConfig.MaxRetries = 0
	account.SetCustomProviderConfig(customProvider, &schemas.CustomProviderConfig{
		BaseProviderType:      schemas.OpenAI,
		DoesNotSendDoneMarker: true,
	})
	account.SetKeysForProvider(customProvider, []schemas.Key{
		{ID: "custom-key", Value: *schemas.NewSecretVar("sk-custom"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})

	client := newStreamTestClient(t, account)
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: customProvider,
		Model:    "repro-model",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: Ptr("hi")},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("stream request failed: %v", bifrostErr)
	}

	type drained struct {
		content string
		errs    []string
	}
	done := make(chan drained, 1)
	go func() {
		content, errs := drainChatStream(stream)
		done <- drained{content: content, errs: errs}
	}()

	select {
	case result := <-done:
		if len(result.errs) > 0 {
			t.Fatalf("stream carried errors: %v", result.errs)
		}
		if result.content != "hello" {
			t.Errorf("expected the streamed content to survive the early break, got %q", result.content)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stream never terminated: does_not_send_done_marker did not reach the provider read loop")
	}
}

// TestExecuteRequestWithRetries_529RetriesSameKeyWithoutRotation pins the behavior behind the
// transient classification. TestClassifyFailure_StatusOnly only proves 529 is *classified* as
// transient; this proves executeRequestWithRetries acts on that classification (retry on the
// same key, with no rotation), which is the part that would actually regress.
//
// 529 is Anthropic's overloaded_error: "the API experiences high traffic across all users"
// (platform.claude.com/docs/en/api/errors). It says nothing about this credential, so rotating
// would burn every key in the pool on a condition none of them can avoid.
func TestExecuteRequestWithRetries_529RetriesSameKeyWithoutRotation(t *testing.T) {
	config := createTestConfig(2, 0, 0)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	keyA := schemas.Key{ID: "key-a", Name: "Key A", Value: *schemas.NewSecretVar("sk-a"), Weight: 1}
	keyB := schemas.Key{ID: "key-b", Name: "Key B", Value: *schemas.NewSecretVar("sk-b"), Weight: 1}

	// Rotation-capable provider: it hands out key-b the moment key-a is marked used or dead.
	// A nil keyProvider would make this test vacuous — the rotation path is what's under test.
	keyProvider := func(usedKeyIDs, deadKeyIDs map[string]bool) (schemas.Key, error) {
		if usedKeyIDs[keyA.ID] || deadKeyIDs[keyA.ID] {
			return keyB, nil
		}
		return keyA, nil
	}

	var seenKeyIDs []string
	callCount := 0
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		seenKeyIDs = append(seenKeyIDs, k.ID)
		callCount++
		if callCount == 1 {
			return "", createBifrostError("overloaded_error", Ptr(529), Ptr("overloaded_error"), false)
		}
		return "recovered", nil
	}

	result, err := executeRequestWithRetries(ctx, config, handler, keyProvider,
		schemas.ChatCompletionRequest, schemas.Anthropic, "claude-sonnet-4-5", nil, logger)

	if err != nil {
		t.Fatalf("expected 529 to be retried to success, got error: %v", err)
	}
	if result != "recovered" {
		t.Errorf("expected 'recovered', got %q", result)
	}
	if len(seenKeyIDs) != 2 {
		t.Fatalf("expected 2 attempts (529 then success), got %d: %v", len(seenKeyIDs), seenKeyIDs)
	}
	for i, id := range seenKeyIDs {
		if id != keyA.ID {
			t.Errorf("attempt %d used %s, expected the same key %s throughout (sequence: %v); "+
				"529 is capacity-wide and must not rotate credentials", i+1, id, keyA.ID, seenKeyIDs)
		}
	}
}

// Benchmark calculateBackoff performance
func BenchmarkCalculateBackoff(b *testing.B) {
	config := createTestConfig(10, 100*time.Millisecond, 5*time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculateBackoff(i%10, config)
	}
}

// Benchmark IsRateLimitErrorMessage performance
func BenchmarkIsRateLimitError(b *testing.B) {
	messages := []string{
		"rate limit exceeded",
		"too many requests",
		"quota exceeded",
		"throttled by provider",
		"API rate limit reached",
		"not a rate limit error",
		"authentication failed",
		"model not found",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IsRateLimitErrorMessage(messages[i%len(messages)])
	}
}

// Mock Account implementation for testing UpdateProvider
type MockAccount struct {
	mu      sync.RWMutex
	configs map[schemas.ModelProvider]*schemas.ProviderConfig
	keys    map[schemas.ModelProvider][]schemas.Key
	// keyLookups counts GetKeysForProvider calls per provider
	keyLookups sync.Map
}

func NewMockAccount() *MockAccount {
	return &MockAccount{
		configs: make(map[schemas.ModelProvider]*schemas.ProviderConfig),
		keys:    make(map[schemas.ModelProvider][]schemas.Key),
	}
}

func (ma *MockAccount) AddProvider(provider schemas.ModelProvider, concurrency int, bufferSize int) {
	ma.AddProviderWithBaseURL(provider, concurrency, bufferSize, "")
}

func (ma *MockAccount) AddProviderWithBaseURL(provider schemas.ModelProvider, concurrency int, bufferSize int, baseURL string) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	ma.configs[provider] = &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 300,
			MaxRetries:                     3,
			RetryBackoffInitial:            500 * time.Millisecond,
			RetryBackoffMax:                5 * time.Second,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: concurrency,
			BufferSize:  bufferSize,
		},
	}

	ma.keys[provider] = []schemas.Key{
		{
			ID:     fmt.Sprintf("test-key-%s", provider),
			Value:  *schemas.NewSecretVar(fmt.Sprintf("sk-test-%s", provider)),
			Weight: 100,
		},
	}
}

func (ma *MockAccount) UpdateProviderConfig(provider schemas.ModelProvider, concurrency int, bufferSize int) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	if config, exists := ma.configs[provider]; exists {
		config.ConcurrencyAndBufferSize.Concurrency = concurrency
		config.ConcurrencyAndBufferSize.BufferSize = bufferSize
	}
}

func (ma *MockAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	providers := make([]schemas.ModelProvider, 0, len(ma.configs))
	for provider := range ma.configs {
		providers = append(providers, provider)
	}
	return providers, nil
}

func (ma *MockAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if config, exists := ma.configs[provider]; exists {
		// Return a copy to simulate real behavior
		configCopy := *config
		return &configCopy, nil
	}
	return nil, fmt.Errorf("provider %s not configured", provider)
}

func (ma *MockAccount) GetKeysForProvider(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
	counter, _ := ma.keyLookups.LoadOrStore(provider, &atomic.Int64{})
	counter.(*atomic.Int64).Add(1)

	ma.mu.RLock()
	defer ma.mu.RUnlock()
	if keys, exists := ma.keys[provider]; exists {
		return keys, nil
	}
	return nil, fmt.Errorf("no keys for provider %s", provider)
}

func (ma *MockAccount) SetKeysForProvider(provider schemas.ModelProvider, keys []schemas.Key) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	ma.keys[provider] = keys
}

func (ma *MockAccount) SetCustomProviderConfig(provider schemas.ModelProvider, customConfig *schemas.CustomProviderConfig) {
	ma.mu.Lock()
	defer ma.mu.Unlock()
	if config, exists := ma.configs[provider]; exists {
		config.CustomProviderConfig = customConfig
	}
}

func (ma *MockAccount) KeyLookupCount(provider schemas.ModelProvider) int64 {
	if counter, ok := ma.keyLookups.Load(provider); ok {
		return counter.(*atomic.Int64).Load()
	}
	return 0
}

type countingTracer struct {
	schemas.NoOpTracer
	flushed atomic.Int32
}

func (t *countingTracer) CreateTrace(_ string, _ ...string) string {
	return "trace-ws-final"
}

func (t *countingTracer) CompleteAndFlushTrace(_ string) {
	t.flushed.Add(1)
}

func TestFilterProvidersByContext(t *testing.T) {
	providers := []schemas.ModelProvider{
		schemas.OpenAI,
		schemas.Anthropic,
		schemas.Mistral,
	}

	t.Run("no context filter keeps all providers", func(t *testing.T) {
		filtered := filterProvidersByContext(nil, providers)
		if len(filtered) != len(providers) {
			t.Fatalf("expected all providers, got %v", filtered)
		}
	})

	t.Run("available providers restrict list models fanout", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyAvailableProviders, []schemas.ModelProvider{schemas.Anthropic})

		filtered := filterProvidersByContext(ctx, providers)
		if len(filtered) != 1 || filtered[0] != schemas.Anthropic {
			t.Fatalf("expected only anthropic, got %v", filtered)
		}
	})

	t.Run("empty available providers denies all providers", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyAvailableProviders, []schemas.ModelProvider{})

		filtered := filterProvidersByContext(ctx, providers)
		if len(filtered) != 0 {
			t.Fatalf("expected no providers, got %v", filtered)
		}
	})

	t.Run("malformed available providers fails closed", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyAvailableProviders, "openai")

		filtered := filterProvidersByContext(ctx, providers)
		if len(filtered) != 0 {
			t.Fatalf("expected no providers for malformed context value, got %v", filtered)
		}
	})
}

// TestListAllModels_CustomProviderAllowedRequestsGate covers the fan-out gate:
// a custom provider that disables list_models via allowed_requests must not be
// dispatched at all, while providers that allow it — explicitly or by leaving
// AllowedRequests nil, which permits every operation — still report their models.
// The key lookup count is what separates "skipped" from "dispatched and bounced":
// both end up contributing no models to the aggregated response.
func TestListAllModels_CustomProviderAllowedRequestsGate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"model-a","object":"model","created":0,"owned_by":"test"}]}`)
	}))
	defer server.Close()

	tests := []struct {
		name            string
		provider        schemas.ModelProvider
		allowedRequests *schemas.AllowedRequests
		wantDispatched  bool
	}{
		{
			name:            "nil allowed requests allows all operations",
			provider:        schemas.ModelProvider("custom-openai-nil"),
			allowedRequests: nil,
			wantDispatched:  true,
		},
		{
			name:            "list models explicitly allowed",
			provider:        schemas.ModelProvider("custom-openai-allowed"),
			allowedRequests: &schemas.AllowedRequests{ListModels: true},
			wantDispatched:  true,
		},
		{
			name:            "list models explicitly disallowed",
			provider:        schemas.ModelProvider("custom-openai-gated"),
			allowedRequests: &schemas.AllowedRequests{ListModels: false},
			wantDispatched:  false,
		},
	}

	// All cases share one fan-out so the gate is exercised the way it runs in
	// production: a mix of gated and ungated providers in a single pass.
	account := NewMockAccount()
	for _, tt := range tests {
		account.AddProviderWithBaseURL(tt.provider, 1, 1, server.URL)
		account.SetKeysForProvider(tt.provider, []schemas.Key{
			{
				ID:     fmt.Sprintf("test-key-%s", tt.provider),
				Value:  *schemas.NewSecretVar(fmt.Sprintf("sk-test-%s", tt.provider)),
				Models: schemas.WhiteList{"*"},
				Weight: 100,
			},
		})
		account.SetCustomProviderConfig(tt.provider, &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
			AllowedRequests:  tt.allowedRequests,
		})
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	response, bifrostErr := client.ListAllModels(ctx, &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatalf("ListAllModels returned error: %v", bifrostErr)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyLookups := account.KeyLookupCount(tt.provider)
			foundModels := false
			for _, model := range response.Data {
				if strings.HasPrefix(model.ID, string(tt.provider)+"/") {
					foundModels = true
					break
				}
			}

			if tt.wantDispatched {
				if keyLookups == 0 {
					t.Error("expected provider to be dispatched, got 0 key lookups")
				}
				if !foundModels {
					t.Errorf("expected models from provider, got %+v", response.Data)
				}
				return
			}

			if keyLookups != 0 {
				t.Errorf("expected provider to be skipped before key selection, got %d key lookups", keyLookups)
			}
			if foundModels {
				t.Errorf("expected no models from skipped provider, got %+v", response.Data)
			}
		})
	}
}

func TestRunStreamPreHooks_FinalChunkFlushesTrace(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	account := NewMockAccount()
	tracer := &countingTracer{}

	client, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Tracer:  tracer,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	hooks, bifrostErr := client.RunStreamPreHooks(ctx, &schemas.BifrostRequest{
		RequestType: schemas.WebSocketResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
		},
	})
	if bifrostErr != nil {
		t.Fatalf("RunStreamPreHooks returned error: %v", bifrostErr)
	}
	defer hooks.Cleanup()

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	_, bifrostErr = hooks.PostHookRunner(ctx, &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Object:    "response",
			CreatedAt: int(time.Now().Unix()),
			Model:     "gpt-4o-mini",
		},
	}, nil)
	if bifrostErr != nil {
		t.Fatalf("PostHookRunner returned error: %v", bifrostErr)
	}

	if tracer.flushed.Load() != 1 {
		t.Fatalf("expected trace flush count 1, got %d", tracer.flushed.Load())
	}
}

// mockKVStore implements schemas.KVStore for session stickiness tests.
type mockKVStore struct {
	mu   sync.RWMutex
	data map[string]struct {
		value any
		ttl   time.Duration
	}
}

func newMockKVStore() *mockKVStore {
	return &mockKVStore{data: make(map[string]struct {
		value any
		ttl   time.Duration
	})}
}

func (m *mockKVStore) Get(key string) (any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.data[key]; ok {
		return e.value, nil
	}
	return nil, fmt.Errorf("key not found")
}

func (m *mockKVStore) SetWithTTL(key string, value any, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = struct {
		value any
		ttl   time.Duration
	}{value: value, ttl: ttl}
	return nil
}

func (m *mockKVStore) SetNXWithTTL(key string, value any, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return false, nil
	}
	m.data[key] = struct {
		value any
		ttl   time.Duration
	}{value: value, ttl: ttl}
	return true, nil
}

func (m *mockKVStore) Delete(key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		delete(m.data, key)
		return true, nil
	}
	return false, nil
}

// Test selectKeyFromProviderForModelWithPool with session stickiness: nothing is bound until a
// request is served, and a bound key then comes back alone with rotation off.
func TestSelectKeyFromProviderForModel_SessionStickiness(t *testing.T) {
	kvStore := newMockKVStore()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	// Use 2 keys so the pool can rotate (single key returns early)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})

	var keySelectorCalls int
	deterministicSelector := func(ctx *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
		keySelectorCalls++
		return keys[0], nil // always return first key
	}

	ctx := context.Background()
	bifrost, err := Init(ctx, schemas.BifrostConfig{
		Account:     account,
		Logger:      NewDefaultLogger(schemas.LogLevelError),
		KVStore:     kvStore,
		KeySelector: deterministicSelector,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeySessionID, "sess-123")

	// First request: nothing bound, so the whole pool comes back with rotation allowed and the
	// pool builder neither selects nor writes.
	keys1, canRotate1, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatalf("first selectKeyFromProviderForModelWithPool: %v", err)
	}
	if !canRotate1 || len(keys1) != 2 {
		t.Fatalf("first call: got %d keys canRotate=%v, want the full pool with rotation", len(keys1), canRotate1)
	}
	if keySelectorCalls != 0 {
		t.Errorf("first call: the pool builder should not select, got %d keySelector calls", keySelectorCalls)
	}
	kvKey := SessionStateKey(bfCtx, SessionStateKindKey, string(schemas.OpenAI), "gpt-4")
	if _, err := kvStore.Get(kvKey); err == nil {
		t.Error("session bound before anything served it")
	}

	// The request is served by key-a, which binds the session to it.
	bfCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-a")
	route := schemas.Route{Provider: schemas.OpenAI, Model: "gpt-4"}
	bifrost.observeSessionOutcome(bfCtx, route, &route, false, false, nil)
	if raw, err := kvStore.Get(kvKey); err != nil || raw != "key-a" {
		t.Errorf("kvstore after the request served: expected key-a, got %v (err=%v)", raw, err)
	}

	// Second request: the bound key comes first with the rest of the pool behind it, rotation off,
	// selector not consulted.
	keys2, canRotate2, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatalf("second selectKeyFromProviderForModelWithPool: %v", err)
	}
	if canRotate2 {
		t.Error("second call: canRotate should be false for a session-bound key")
	}
	if len(keys2) != 2 || keys2[0].ID != "key-a" || keys2[1].ID != "key-b" {
		t.Errorf("second call: expected [key-a key-b] (bound first), got %v", keys2)
	}
	if keySelectorCalls != 0 {
		t.Errorf("second call: keySelector should not run, got %d calls", keySelectorCalls)
	}
}

// Test selectKeyFromProviderForModelWithPool - no stickiness when session ID absent
func TestSelectKeyFromProviderForModel_NoStickinessWithoutSessionID(t *testing.T) {
	kvStore := newMockKVStore()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})

	var keySelectorCalls int
	deterministicSelector := func(ctx *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
		keySelectorCalls++
		return keys[0], nil
	}

	ctx := context.Background()
	bifrost, err := Init(ctx, schemas.BifrostConfig{
		Account:     account,
		Logger:      NewDefaultLogger(schemas.LogLevelError),
		KVStore:     kvStore,
		KeySelector: deterministicSelector,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	// No session ID set — pool is returned with canRotate=true; keySelector is called each time.

	for i := 0; i < 2; i++ {
		pool, canRotate, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err != nil {
			t.Fatalf("selectKeyFromProviderForModelWithPool call %d: %v", i+1, err)
		}
		if !canRotate {
			t.Fatalf("call %d: canRotate should be true without a session id", i+1)
		}
		if len(pool) == 0 {
			t.Fatalf("call %d: expected non-empty pool", i+1)
		}
	}
	if keySelectorCalls != 0 {
		t.Errorf("expected 0 keySelector calls from pool building (no session id), got %d", keySelectorCalls)
	}
	// KVStore should not have a sticky entry for an empty session id
	kvStore.mu.RLock()
	entries := len(kvStore.data)
	kvStore.mu.RUnlock()
	if entries != 0 {
		t.Errorf("kvstore should not have a sticky entry for an empty session id, got %d entries", entries)
	}
}

// TestSelectKeyFromProviderForModel_SessionStickinessNoRotation verifies that once a session
// is bound to a key, rate-limit retries reuse that key rather than rotating to another.
func TestSelectKeyFromProviderForModel_SessionStickinessNoRotation(t *testing.T) {
	kvStore := newMockKVStore()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})

	deterministicSelector := func(ctx *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
		return keys[0], nil // always picks key-a when pool includes it
	}

	ctx := context.Background()
	bifrost, err := Init(ctx, schemas.BifrostConfig{
		Account:     account,
		Logger:      NewDefaultLogger(schemas.LogLevelError),
		KVStore:     kvStore,
		KeySelector: deterministicSelector,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeySessionID, "sess-sticky")
	bfCtx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})

	// An earlier request served by key-a bound the session to it.
	bfCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-a")
	route := schemas.Route{Provider: schemas.OpenAI, Model: "gpt-4"}
	bifrost.observeSessionOutcome(bfCtx, route, &route, false, false, nil)

	config := createTestConfig(3, 0, 0)
	logger := NewDefaultLogger(schemas.LogLevelError)

	// Build keyProvider the same way requestWorker does.
	pool, canRotate, poolErr := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if poolErr != nil {
		t.Fatalf("pool build failed: %v", poolErr)
	}
	if canRotate {
		t.Fatal("expected canRotate=false for session-sticky request")
	}
	if len(pool) == 0 || pool[0].ID != "key-a" {
		t.Fatalf("expected sticky pool led by key-a, got %v", pool)
	}

	fixedKey := pool[0]
	keyProvider := func(_, _ map[string]bool) (schemas.Key, error) { return fixedKey, nil }

	// Simulate 3 rate-limit failures then success; all attempts must use key-a.
	var usedKeyIDs []string
	callCount := 0
	handler := func(k schemas.Key) (string, *schemas.BifrostError) {
		usedKeyIDs = append(usedKeyIDs, k.ID)
		callCount++
		if callCount <= 3 {
			return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
		}
		return "ok", nil
	}

	result, retryErr := executeRequestWithRetries(bfCtx, config, handler, keyProvider,
		schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, logger)

	if retryErr != nil {
		t.Fatalf("expected success, got error: %v", retryErr)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %s", result)
	}
	for i, id := range usedKeyIDs {
		if id != "key-a" {
			t.Errorf("attempt %d: expected sticky key-a, got %s (full sequence: %v)", i, id, usedKeyIDs)
		}
	}
}

func TestSelectKeyFromProviderForModel_BlacklistedModels(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)

	ctx := context.Background()
	bifrost, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Each key that blacklists the model allows every model otherwise: a key with no models list
	// allows none, which would exclude it whatever its blacklist said.
	t.Run("all keys blacklist model", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{ID: "k1", Name: "K1", Value: *schemas.NewSecretVar("sk-1"), Weight: 1, Models: []string{"*"}, BlacklistedModels: []string{"gpt-4"}},
		})
		_, _, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err == nil {
			t.Fatal("expected error when model is only blacklisted")
		}
		if !strings.Contains(err.Error(), "no keys found that support model") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("blacklist wins over models allow list", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{
				ID: "k1", Name: "K1", Value: *schemas.NewSecretVar("sk-1"), Weight: 1,
				Models:            []string{"gpt-4"},
				BlacklistedModels: []string{"gpt-4"},
			},
		})
		_, _, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err == nil {
			t.Fatal("expected error when model is both allowed and blacklisted")
		}
	})

	t.Run("second key used when first blacklists", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{ID: "k1", Name: "K1", Value: *schemas.NewSecretVar("sk-1"), Weight: 1, Models: []string{"*"}, BlacklistedModels: []string{"gpt-4"}},
			{ID: "k2", Name: "K2", Value: *schemas.NewSecretVar("sk-2"), Weight: 1, Models: []string{"*"}},
		})
		pool, canRotate, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// After filtering, only k2 remains — single key returns canRotate=false.
		if canRotate {
			t.Fatal("expected canRotate=false for single-key pool after filtering")
		}
		if len(pool) != 1 || pool[0].ID != "k2" {
			t.Fatalf("expected pool=[k2], got %v", pool)
		}
	})
}

func TestSelectKeyFromProviderForModel_VLLMAliasResolution(t *testing.T) {
	account := NewMockAccount()
	bifrost := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	newVLLMKey := func(id, modelName string, models schemas.WhiteList, aliases schemas.KeyAliases) schemas.Key {
		return schemas.Key{
			ID:      id,
			Name:    id,
			Value:   *schemas.NewSecretVar("test-key"),
			Models:  models,
			Aliases: aliases,
			Weight:  1,
			VLLMKeyConfig: &schemas.VLLMKeyConfig{
				URL:       *schemas.NewSecretVar("http://localhost:8000"),
				ModelName: modelName,
			},
		}
	}

	t.Run("resolves alias independently for each key", func(t *testing.T) {
		account.SetKeysForProvider(schemas.VLLM, []schemas.Key{
			newVLLMKey("vllm-a", "served-model-a", schemas.WhiteList{"chat-model"}, schemas.KeyAliases{
				"chat-model": {ModelID: "served-model-a"},
			}),
			newVLLMKey("vllm-b", "served-model-b", schemas.WhiteList{"chat-model"}, schemas.KeyAliases{
				"chat-model": {ModelID: "served-model-b"},
			}),
		})

		keys, canRotate, err := bifrost.selectKeyFromProviderForModelWithPool(ctx, schemas.ChatCompletionRequest, schemas.VLLM, "chat-model", schemas.VLLM)
		if err != nil {
			t.Fatalf("selectKeyFromProviderForModelWithPool: %v", err)
		}
		if !canRotate {
			t.Fatal("canRotate = false, want true for two matching keys")
		}
		if len(keys) != 2 || keys[0].ID != "vllm-a" || keys[1].ID != "vllm-b" {
			t.Fatalf("got keys %v, want [vllm-a vllm-b]", keys)
		}
	})

	t.Run("keeps allowlist checks on requested alias", func(t *testing.T) {
		account.SetKeysForProvider(schemas.VLLM, []schemas.Key{
			newVLLMKey("vllm-a", "served-model-a", schemas.WhiteList{"chat-model"}, schemas.KeyAliases{
				"chat-model": {ModelID: "served-model-a"},
			}),
		})

		_, _, err := bifrost.selectKeyFromProviderForModelWithPool(ctx, schemas.ChatCompletionRequest, schemas.VLLM, "served-model-a", schemas.VLLM)
		if err == nil {
			t.Fatal("expected direct model request to be rejected when only the alias is allowlisted")
		}
	})

	t.Run("still supports direct model names", func(t *testing.T) {
		account.SetKeysForProvider(schemas.VLLM, []schemas.Key{
			newVLLMKey("vllm-a", "served-model-a", schemas.WhiteList{"served-model-a"}, nil),
		})

		keys, canRotate, err := bifrost.selectKeyFromProviderForModelWithPool(ctx, schemas.ChatCompletionRequest, schemas.VLLM, "served-model-a", schemas.VLLM)
		if err != nil {
			t.Fatalf("selectKeyFromProviderForModelWithPool: %v", err)
		}
		if canRotate {
			t.Fatal("canRotate = true, want false for one matching key")
		}
		if len(keys) != 1 || keys[0].ID != "vllm-a" {
			t.Fatalf("got keys %v, want [vllm-a]", keys)
		}
	})
}

// affinityPrefers is a session policy that keeps a session on one key whenever that key is offered.
type affinityPrefers struct{ id string }

func (affinityPrefers) ResolveRoute(_ *schemas.BifrostContext, _ schemas.Route, chain []schemas.Route) []schemas.Route {
	return chain
}

func (a affinityPrefers) ResolveKey(_ *schemas.BifrostContext, _ schemas.ModelProvider, _ string, eligible []schemas.Key) (schemas.Key, bool) {
	for _, key := range eligible {
		if key.ID == a.id {
			return key, true
		}
	}
	return schemas.Key{}, false
}

func (affinityPrefers) Observe(*schemas.BifrostContext, schemas.Route, schemas.RouteOutcome) {}

func TestSelectKeyForProviderRequestType_AdditionalModels(t *testing.T) {
	account := NewMockAccount()
	bifrost := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError), keySelector: keyselectors.WeightedRandom}
	newKey := func(id string, models schemas.WhiteList, blacklisted schemas.BlackList) schemas.Key {
		return schemas.Key{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-" + id), Weight: 1, Models: models, BlacklistedModels: blacklisted}
	}
	voiceOnly := newKey("voice-only", schemas.WhiteList{"gpt-live-1"}, nil)
	both := newKey("both", schemas.WhiteList{"gpt-live-1", "gpt-5.6-luna"}, nil)
	backendOnly := newKey("backend-only", schemas.WhiteList{"gpt-5.6-luna"}, nil)
	wildcardBlocked := newKey("wildcard-blocked", schemas.WhiteList{"*"}, schemas.BlackList{"gpt-5.6-luna"})

	t.Run("selects only keys that serve every model", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{voiceOnly, both, backendOnly, wildcardBlocked})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		for range 20 {
			key, err := bifrost.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, schemas.OpenAI, "gpt-live-1", "gpt-5.6-luna")
			if err != nil {
				t.Fatalf("SelectKeyForProviderRequestType: %v", err)
			}
			if key.ID != "both" {
				t.Fatalf("selected %q, want both", key.ID)
			}
		}
	})

	t.Run("errors when no key serves every model", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{voiceOnly, backendOnly, wildcardBlocked})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, err := bifrost.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, schemas.OpenAI, "gpt-live-1", "gpt-5.6-luna")
		if err == nil || !strings.Contains(err.Error(), "gpt-live-1") || !strings.Contains(err.Error(), "gpt-5.6-luna") {
			t.Fatalf("err = %v, want an error naming both models", err)
		}
	})

	t.Run("pinned key must serve every model", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{voiceOnly, both})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, "both")
		if key, err := bifrost.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, schemas.OpenAI, "gpt-live-1", "gpt-5.6-luna"); err != nil || key.ID != "both" {
			t.Fatalf("pinned both: key=%q err=%v", key.ID, err)
		}
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, "voice-only")
		if key, err := bifrost.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, schemas.OpenAI, "gpt-live-1", "gpt-5.6-luna"); err == nil {
			t.Fatalf("pinned voice-only: selected %q, want error", key.ID)
		}
	})

	t.Run("session affinity picks among keys that serve every model", func(t *testing.T) {
		// The session's last key serves only the voice model; another key serves both. Affinity
		// must choose from the pool narrowed by every model, not rebind to the voice-only key.
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{voiceOnly, both})
		sticky := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError), keySelector: keyselectors.WeightedRandom, sessionAffinity: affinityPrefers{id: "voice-only"}}
		key, err := sticky.SelectKeyForProviderRequestType(sessionCtx("live-1"), schemas.LiveRequest, schemas.OpenAI, "gpt-live-1", "gpt-5.6-luna")
		if err != nil || key.ID != "both" {
			t.Fatalf("key=%q err=%v, want both", key.ID, err)
		}
	})

	t.Run("empty additional model and no additional models keep single-model selection", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{voiceOnly})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		for _, extra := range [][]string{nil, {""}} {
			key, err := bifrost.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, schemas.OpenAI, "gpt-live-1", extra...)
			if err != nil || key.ID != "voice-only" {
				t.Fatalf("additional=%v: key=%q err=%v", extra, key.ID, err)
			}
		}
	})

	t.Run("KeySupportsModel applies the same rules to a pinned session key", func(t *testing.T) {
		if !bifrost.KeySupportsModel(schemas.OpenAI, both, "gpt-5.6-luna") {
			t.Fatal("key listing the model should support it")
		}
		if bifrost.KeySupportsModel(schemas.OpenAI, voiceOnly, "gpt-5.6-luna") {
			t.Fatal("key not listing the model should not support it")
		}
		if bifrost.KeySupportsModel(schemas.OpenAI, wildcardBlocked, "gpt-5.6-luna") {
			t.Fatal("deny list must win over a wildcard allow list")
		}
	})

	t.Run("direct key bypasses model lists", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{voiceOnly})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{ID: "direct", Value: *schemas.NewSecretVar("sk-direct")})
		key, err := bifrost.SelectKeyForProviderRequestType(ctx, schemas.LiveRequest, schemas.OpenAI, "gpt-live-1", "gpt-5.6-luna")
		if err != nil || key.ID != "direct" {
			t.Fatalf("key=%q err=%v, want direct", key.ID, err)
		}
	})
}

// Test key rotation in executeRequestWithRetries on rate-limit errors
func TestExecuteRequestWithRetries_KeyRotation(t *testing.T) {
	config := createTestConfig(3, 0, 0)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	keys := []schemas.Key{
		{ID: "k1", Name: "K1"},
		{ID: "k2", Name: "K2"},
		{ID: "k3", Name: "K3"},
	}

	t.Run("RotatesKeyOnRateLimitRetry", func(t *testing.T) {
		var selectedKeyIDs []string
		keyProvider := func(usedKeyIDs, _ map[string]bool) (schemas.Key, error) {
			for _, k := range keys {
				if !usedKeyIDs[k.ID] {
					return k, nil
				}
			}
			// Fresh round
			for id := range usedKeyIDs {
				delete(usedKeyIDs, id)
			}
			return keys[0], nil
		}

		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			selectedKeyIDs = append(selectedKeyIDs, k.ID)
			// First two calls rate-limit, third succeeds
			if len(selectedKeyIDs) <= 2 {
				return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
			}
			return "success", nil
		}

		result, err := executeRequestWithRetries(ctx, config, handler, keyProvider,
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, logger)

		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if result != "success" {
			t.Errorf("expected 'success', got %s", result)
		}
		if len(selectedKeyIDs) != 3 {
			t.Fatalf("expected 3 attempts, got %d", len(selectedKeyIDs))
		}
		// Each attempt should use a different key
		seen := map[string]struct{}{}
		for _, id := range selectedKeyIDs {
			seen[id] = struct{}{}
		}
		if len(seen) != len(selectedKeyIDs) {
			t.Errorf("expected distinct keys per rate-limit retry, got %v", selectedKeyIDs)
		}
	})

	t.Run("SameKeyOnNetworkError", func(t *testing.T) {
		var selectedKeyIDs []string
		keyProviderCalls := 0
		keyProvider := func(usedKeyIDs, _ map[string]bool) (schemas.Key, error) {
			keyProviderCalls++
			for _, k := range keys {
				if !usedKeyIDs[k.ID] {
					return k, nil
				}
			}
			for id := range usedKeyIDs {
				delete(usedKeyIDs, id)
			}
			return keys[0], nil
		}

		callCount := 0
		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			selectedKeyIDs = append(selectedKeyIDs, k.ID)
			callCount++
			if callCount <= 2 {
				return "", createBifrostError(schemas.ErrProviderDoRequest, nil, nil, false)
			}
			return "success", nil
		}

		result, err := executeRequestWithRetries(ctx, config, handler, keyProvider,
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, logger)

		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if result != "success" {
			t.Errorf("expected 'success', got %s", result)
		}
		if len(selectedKeyIDs) != 3 {
			t.Fatalf("expected 3 attempts, got %d", len(selectedKeyIDs))
		}
		if keyProviderCalls != 1 {
			t.Fatalf("expected keyProvider to be called once for network retries, got %d", keyProviderCalls)
		}
		// All attempts should use the same key (network error = same key)
		for i := 1; i < len(selectedKeyIDs); i++ {
			if selectedKeyIDs[i] != selectedKeyIDs[0] {
				t.Errorf("expected same key for all network-error retries, got %v", selectedKeyIDs)
			}
		}
	})

	t.Run("CyclesFreshRoundWhenPoolExhausted", func(t *testing.T) {
		var selectedKeyIDs []string
		// 3 keys, 6 retries — should cycle through all 3 keys twice
		config6 := createTestConfig(5, 0, 0) // 5 retries = 6 total attempts
		keyProvider := func(usedKeyIDs, _ map[string]bool) (schemas.Key, error) {
			available := make([]schemas.Key, 0)
			for _, k := range keys {
				if !usedKeyIDs[k.ID] {
					available = append(available, k)
				}
			}
			if len(available) == 0 {
				for id := range usedKeyIDs {
					delete(usedKeyIDs, id)
				}
				available = keys
			}
			return available[0], nil
		}

		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			selectedKeyIDs = append(selectedKeyIDs, k.ID)
			return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
		}

		executeRequestWithRetries(ctx, config6, handler, keyProvider,
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, logger)

		if len(selectedKeyIDs) != 6 {
			t.Fatalf("expected 6 attempts (1 initial + 5 retries), got %d", len(selectedKeyIDs))
		}
		// First cycle: k1, k2, k3; second cycle: k1, k2, k3
		expected := []string{"k1", "k2", "k3", "k1", "k2", "k3"}
		for i, id := range selectedKeyIDs {
			if id != expected[i] {
				t.Errorf("attempt %d: expected key %s, got %s (full sequence: %v)", i, expected[i], id, selectedKeyIDs)
			}
		}
	})

	t.Run("NilKeyProviderUsesZeroKey", func(t *testing.T) {
		cleanCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		cleanCtx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})

		var receivedKey schemas.Key
		handler := func(k schemas.Key) (string, *schemas.BifrostError) {
			receivedKey = k
			return "ok", nil
		}

		result, err := executeRequestWithRetries(cleanCtx, config, handler, nil,
			schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, logger)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "ok" {
			t.Errorf("expected 'ok', got %s", result)
		}
		if receivedKey.ID != "" {
			t.Errorf("expected zero Key when keyProvider is nil, got ID=%s", receivedKey.ID)
		}
		if trail, ok := cleanCtx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord); ok && len(trail) > 0 {
			t.Fatalf("expected no attempt trail for nil keyProvider, got %v", trail)
		}
		if selectedID, _ := cleanCtx.Value(schemas.BifrostContextKeySelectedKeyID).(string); selectedID != "" {
			t.Fatalf("expected empty selected key id, got %q", selectedID)
		}
		if selectedName, _ := cleanCtx.Value(schemas.BifrostContextKeySelectedKeyName).(string); selectedName != "" {
			t.Fatalf("expected empty selected key name, got %q", selectedName)
		}
	})
}

// Test UpdateProvider functionality
func TestUpdateProvider(t *testing.T) {
	t.Run("SuccessfulUpdate", func(t *testing.T) {
		// Setup mock account with initial configuration
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 5, 1000)

		// Initialize Bifrost
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bifrost, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError), // Keep tests quiet
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Verify initial provider exists
		initialProvider := bifrost.getProviderByKey(schemas.OpenAI)
		if initialProvider == nil {
			t.Fatalf("Initial provider not found")
		}

		// Update configuration
		account.UpdateProviderConfig(schemas.OpenAI, 10, 2000)

		// Perform update
		err = bifrost.UpdateProvider(schemas.OpenAI)
		if err != nil {
			t.Fatalf("UpdateProvider failed: %v", err)
		}

		// Verify provider was replaced
		updatedProvider := bifrost.getProviderByKey(schemas.OpenAI)
		if updatedProvider == nil {
			t.Fatalf("Updated provider not found")
		}

		// Verify it's a different instance (provider should have been recreated)
		if initialProvider == updatedProvider {
			t.Errorf("Provider instance was not replaced - same memory address")
		}

		// Verify provider key is still correct
		if updatedProvider.GetProviderKey() != schemas.OpenAI {
			t.Errorf("Updated provider has wrong key: got %s, want %s",
				updatedProvider.GetProviderKey(), schemas.OpenAI)
		}
	})

	t.Run("UpdateNonExistentProvider", func(t *testing.T) {
		// Setup account without the provider we'll try to update
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 5, 1000)

		ctx := context.Background()
		bifrost, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Try to update a provider not in the account
		err = bifrost.UpdateProvider(schemas.Anthropic)
		if err == nil {
			t.Errorf("Expected error when updating non-existent provider, got nil")
		}

		// Verify error message
		expectedErrMsg := "failed to get updated config for provider anthropic"
		if err != nil && !strings.Contains(err.Error(), expectedErrMsg) {
			t.Errorf("Expected error containing '%s', got: %v", expectedErrMsg, err)
		}
	})

	t.Run("UpdateInactiveProvider", func(t *testing.T) {
		// Setup account with provider but don't initialize it in Bifrost
		account := NewMockAccount()

		ctx := context.Background()
		bifrost, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Verify provider doesn't exist initially
		// Note: Use Ollama (not in dynamicallyConfigurableProviders) to test truly inactive provider
		if bifrost.getProviderByKey(schemas.Ollama) != nil {
			t.Fatal("Provider should not exist initially")
		}

		// Add provider to account after bifrost initialization
		// Note: Ollama requires a BaseURL
		account.AddProviderWithBaseURL(schemas.Ollama, 3, 500, "http://localhost:11434")

		// Update should succeed and initialize the provider
		err = bifrost.UpdateProvider(schemas.Ollama)
		if err != nil {
			t.Fatalf("UpdateProvider should succeed for inactive provider: %v", err)
		}

		// Verify provider now exists
		provider := bifrost.getProviderByKey(schemas.Ollama)
		if provider == nil {
			t.Fatal("Provider should exist after update")
		}

		if provider.GetProviderKey() != schemas.Ollama {
			t.Errorf("Provider has wrong key: got %s, want %s",
				provider.GetProviderKey(), schemas.Ollama)
		}
	})

	t.Run("MultipleProviderUpdates", func(t *testing.T) {
		// Test updating multiple different providers
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 5, 1000)
		account.AddProvider(schemas.Anthropic, 3, 500)
		account.AddProvider(schemas.Cohere, 2, 200)

		ctx := context.Background()
		bifrost, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Get initial provider references
		initialOpenAI := bifrost.getProviderByKey(schemas.OpenAI)
		initialAnthropic := bifrost.getProviderByKey(schemas.Anthropic)
		initialCohere := bifrost.getProviderByKey(schemas.Cohere)

		// Update configurations
		account.UpdateProviderConfig(schemas.OpenAI, 10, 2000)
		account.UpdateProviderConfig(schemas.Anthropic, 6, 1000)
		account.UpdateProviderConfig(schemas.Cohere, 4, 400)

		// Update all providers
		providers := []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic, schemas.Cohere}
		for _, provider := range providers {
			err = bifrost.UpdateProvider(provider)
			if err != nil {
				t.Fatalf("Failed to update provider %s: %v", provider, err)
			}
		}

		// Verify all providers were replaced
		newOpenAI := bifrost.getProviderByKey(schemas.OpenAI)
		newAnthropic := bifrost.getProviderByKey(schemas.Anthropic)
		newCohere := bifrost.getProviderByKey(schemas.Cohere)

		if initialOpenAI == newOpenAI {
			t.Error("OpenAI provider was not replaced")
		}
		if initialAnthropic == newAnthropic {
			t.Error("Anthropic provider was not replaced")
		}
		if initialCohere == newCohere {
			t.Error("Cohere provider was not replaced")
		}

		// Verify all providers still have correct keys
		if newOpenAI.GetProviderKey() != schemas.OpenAI {
			t.Error("OpenAI provider has wrong key after update")
		}
		if newAnthropic.GetProviderKey() != schemas.Anthropic {
			t.Error("Anthropic provider has wrong key after update")
		}
		if newCohere.GetProviderKey() != schemas.Cohere {
			t.Error("Cohere provider has wrong key after update")
		}
	})

	t.Run("ConcurrentProviderUpdates", func(t *testing.T) {
		// Test updating the same provider concurrently (should be serialized by mutex)
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 5, 1000)

		ctx := context.Background()
		bifrost, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Launch concurrent updates
		const numConcurrentUpdates = 5
		errChan := make(chan error, numConcurrentUpdates)

		for i := 0; i < numConcurrentUpdates; i++ {
			go func(updateNum int) {
				// Update with slightly different config each time
				account.UpdateProviderConfig(schemas.OpenAI, 5+updateNum, 1000+updateNum*100)
				err := bifrost.UpdateProvider(schemas.OpenAI)
				errChan <- err
			}(i)
		}

		// Collect results
		var errors []error
		for i := 0; i < numConcurrentUpdates; i++ {
			if err := <-errChan; err != nil {
				errors = append(errors, err)
			}
		}

		// All updates should succeed (mutex should serialize them)
		if len(errors) > 0 {
			t.Fatalf("Expected no errors from concurrent updates, got: %v", errors)
		}

		// Verify provider still exists and has correct key
		provider := bifrost.getProviderByKey(schemas.OpenAI)
		if provider == nil {
			t.Fatal("Provider should exist after concurrent updates")
		}
		if provider.GetProviderKey() != schemas.OpenAI {
			t.Error("Provider has wrong key after concurrent updates")
		}
	})
}

// Test provider slice management during updates
func TestUpdateProvider_ProviderSliceIntegrity(t *testing.T) {
	t.Run("ProviderSliceConsistency", func(t *testing.T) {
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 5, 1000)
		account.AddProvider(schemas.Anthropic, 3, 500)

		ctx := context.Background()
		bifrost, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Get initial provider count
		initialProviders := bifrost.providers.Load()
		initialCount := len(*initialProviders)

		// Update one provider
		account.UpdateProviderConfig(schemas.OpenAI, 10, 2000)
		err = bifrost.UpdateProvider(schemas.OpenAI)
		if err != nil {
			t.Fatalf("UpdateProvider failed: %v", err)
		}

		// Verify provider count is the same (replacement, not addition)
		updatedProviders := bifrost.providers.Load()
		updatedCount := len(*updatedProviders)

		if initialCount != updatedCount {
			t.Errorf("Provider count changed: initial=%d, updated=%d", initialCount, updatedCount)
		}

		// Verify both providers still exist with correct keys
		foundOpenAI := false
		foundAnthropic := false

		for _, provider := range *updatedProviders {
			switch provider.GetProviderKey() {
			case schemas.OpenAI:
				foundOpenAI = true
			case schemas.Anthropic:
				foundAnthropic = true
			}
		}

		if !foundOpenAI {
			t.Error("OpenAI provider not found in providers slice after update")
		}
		if !foundAnthropic {
			t.Error("Anthropic provider not found in providers slice after update")
		}
	})

	t.Run("ProviderSliceNoMemoryLeaks", func(t *testing.T) {
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 5, 1000)

		ctx := context.Background()
		bifrost, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Failed to initialize Bifrost: %v", err)
		}

		// Perform multiple updates to ensure no memory leaks in provider slice
		for i := 0; i < 10; i++ {
			account.UpdateProviderConfig(schemas.OpenAI, 5+i, 1000+i*100)
			err = bifrost.UpdateProvider(schemas.OpenAI)
			if err != nil {
				t.Fatalf("UpdateProvider failed on iteration %d: %v", i, err)
			}

			// Verify only one OpenAI provider exists
			providers := bifrost.providers.Load()
			openAICount := 0
			for _, provider := range *providers {
				if provider.GetProviderKey() == schemas.OpenAI {
					openAICount++
				}
			}

			if openAICount != 1 {
				t.Fatalf("Expected exactly 1 OpenAI provider, found %d on iteration %d", openAICount, i)
			}
		}
	})
}

// TestProviderQueue_SendOnClosedChannel_Race demonstrates the TOCTOU race that
// caused the "send on closed channel" production panic in the OLD code.
//
// The old code called close(pq.queue) during provider shutdown. The sequence:
//  1. Producer calls isClosing() → false  (queue is still open)
//  2. Concurrently: shutdown calls signalClosing() then close(pq.queue)
//  3. Producer enters select { case pq.queue <- msg: ... case <-pq.done: ... }
//     → PANIC: Go's selectgo iterates cases in a randomised pollorder. When the
//     closed-channel send case is checked first, it immediately panics via
//     goto sclose — before it can reach the done case.
//     The case <-pq.done: guard only saves you when done happens to be checked
//     first in that random ordering (≈50 % of the time with two cases).
//
// THE FIX: pq.queue is never closed. See the ProviderQueue struct comment for
// the full explanation. This test is kept as a proof-of-concept showing why
// closing pq.queue is unsafe; the fix is validated by TestProviderQueue_NoPanicWithoutCloseQueue.
//
// We run many iterations so that the panic is statistically certain to surface
// at least once, confirming the hypothesis.
func TestProviderQueue_SendOnClosedChannel_Race(t *testing.T) {
	// With two select cases each iteration has a ~50 % chance of panicking.
	// The probability of never panicking in 200 iterations is (0.5)^200 ≈ 0.
	const iterations = 200
	panicCount := 0

	for i := 0; i < iterations; i++ {
		func() {
			pq := &ProviderQueue{
				queue:      make(chan *ChannelMessage, 10),
				done:       make(chan struct{}),
				signalOnce: sync.Once{},
			}

			// Synchronization barriers to force the exact race interleaving.
			passedIsClosingCheck := make(chan struct{})
			queueClosed := make(chan struct{})

			var panicked bool
			var wg sync.WaitGroup
			wg.Add(1)

			// Producer — mirrors the hot path in tryRequest.
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil && fmt.Sprint(r) == "send on closed channel" {
						panicked = true
					}
				}()

				// Step 1: isClosing() passes — queue is open.
				if pq.isClosing() {
					return
				}

				// Signal: past the isClosing() gate.
				close(passedIsClosingCheck)

				// Wait for the queue to be closed. This represents the real work
				// tryRequest does between the isClosing() check and the select
				// (MCP setup, tracer lookup, plugin pipeline acquisition).
				<-queueClosed

				// Step 2: enter the exact select guard used in production.
				// pq.queue is closed AND pq.done is closed.
				// When selectgo picks the send case first in its random pollorder
				// it hits goto sclose and panics — the done case cannot save it.
				msg := &ChannelMessage{}
				select {
				case pq.queue <- msg: // panics ~50 % of iterations
				case <-pq.done: // selected the other ~50 %
				}
			}()

			// Closer — mirrors UpdateProvider / RemoveProvider.
			go func() {
				<-passedIsClosingCheck
				pq.signalClosing() // closes done, sets closing = 1
				close(pq.queue)
				close(queueClosed) // release the producer into the select
			}()

			wg.Wait()
			if panicked {
				panicCount++
			}
		}()
	}

	if panicCount == 0 {
		t.Fatalf("expected at least one 'send on closed channel' panic across %d iterations, got none", iterations)
	}
	t.Logf("confirmed: panic triggered in %d / %d iterations — hypothesis is correct", panicCount, iterations)
}

// =============================================================================
// ProviderQueue Unit Tests
//
// These tests exercise the ProviderQueue lifecycle in isolation — no full
// Bifrost instance required. They validate the core safety invariants that
// prevent the "send on closed channel" panic.
// =============================================================================

// newTestChannelMessage creates a minimal ChannelMessage suitable for drain tests.
// The Err channel is buffered (size 1) so the worker can send without blocking.
func newTestChannelMessage(ctx *schemas.BifrostContext) *ChannelMessage {
	return &ChannelMessage{
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4",
			},
		},
		Context:  ctx,
		Response: make(chan *schemas.BifrostResponse, 1),
		Err:      make(chan schemas.BifrostError, 1),
	}
}

// TestProviderQueue_IsClosingStateTransition verifies the atomic state flag:
// isClosing() must return false before signalClosing() and true after.
func TestProviderQueue_IsClosingStateTransition(t *testing.T) {
	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, 10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	if pq.isClosing() {
		t.Fatal("isClosing() must be false before signalClosing() is called")
	}

	pq.signalClosing()

	if !pq.isClosing() {
		t.Fatal("isClosing() must be true after signalClosing() is called")
	}

	// done channel must also be closed
	select {
	case <-pq.done:
		// correct: done is closed
	default:
		t.Fatal("pq.done must be closed after signalClosing()")
	}

	// queue channel must remain OPEN — this is the core of the fix
	// (sending should not panic even though done is closed)
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		select {
		case pq.queue <- &ChannelMessage{}:
		case <-pq.done: // done is closed so this is always ready — no panic
		}
	}()
	if panicked {
		t.Fatal("queue channel must stay open after signalClosing() — sending to it must not panic")
	}
}

// TestProviderQueue_SignalOnceIdempotent verifies that calling signalClosing()
// multiple times is safe. sync.Once ensures done is only closed once and the
// atomic store only happens once — no "close of closed channel" panic.
func TestProviderQueue_SignalOnceIdempotent(t *testing.T) {
	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, 10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic from multiple signalClosing() calls: %v", r)
		}
	}()

	pq.signalClosing()
	pq.signalClosing()
	pq.signalClosing()

	if !pq.isClosing() {
		t.Fatal("isClosing() must be true after multiple signalClosing() calls")
	}
}

// TestProviderQueue_WorkerExitsViaDone verifies that a worker running the
// fixed select loop exits cleanly after signalClosing() without closeQueue().
// Before the fix, workers used `for req := range pq.queue` which required
// the channel to be closed. After the fix, done is the exit signal.
func TestProviderQueue_WorkerExitsViaDone(t *testing.T) {
	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, 10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	workerExited := make(chan struct{})

	// Minimal worker loop — mirrors the exact select pattern in requestWorker
	go func() {
		defer close(workerExited)
		for {
			select {
			case r, ok := <-pq.queue:
				if !ok {
					return
				}
				_ = r // process (no-op in this test)
			case <-pq.done:
				// Drain remaining buffered items (queue is empty here)
				for {
					select {
					case <-pq.queue:
					default:
						return
					}
				}
			}
		}
	}()

	// Worker is now blocked on the select. Signal shutdown WITHOUT closing queue.
	pq.signalClosing()

	select {
	case <-workerExited:
		// correct: worker exited via done
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after signalClosing() — it may be stuck on range over unclosed channel")
	}
}

// TestProviderQueue_WorkerDrainSendsErrors verifies the drain behaviour when
// done fires while items are still buffered: every buffered ChannelMessage must
// receive a "provider is shutting down" error on its Err channel. No client
// should be left blocked waiting for a response that will never come.
//
// This test exercises the drain path directly — same code as requestWorker's
// case <-pq.done: branch — to avoid a non-deterministic select race between the
// normal processing path and the done path.
func TestProviderQueue_WorkerDrainSendsErrors(t *testing.T) {
	const numBuffered = 5

	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, numBuffered+2),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Pre-fill queue — simulates requests buffered when done fires
	msgs := make([]*ChannelMessage, numBuffered)
	for i := 0; i < numBuffered; i++ {
		msgs[i] = newTestChannelMessage(ctx)
		pq.queue <- msgs[i]
	}

	// Signal closing: done is now closed
	pq.signalClosing()

	// Execute the drain path synchronously — exactly what requestWorker does in
	// the case <-pq.done: branch. This is deterministic: we know done is closed
	// and the queue has numBuffered items.
	<-pq.done // fires immediately since signalClosing was already called
drainLoop:
	for {
		select {
		case r := <-pq.queue:
			provKey, mod, _ := r.GetRequestFields()
			r.Err <- schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: "provider is shutting down",
				},
				ExtraFields: schemas.BifrostErrorExtraFields{
					RequestType:            r.RequestType,
					Provider:               provKey,
					OriginalModelRequested: mod,
				},
			}
		default:
			break drainLoop
		}
	}

	// Verify every message received a shutdown error
	for i, msg := range msgs {
		select {
		case bifrostErr := <-msg.Err:
			if bifrostErr.Error == nil {
				t.Errorf("message %d: received nil Error field", i)
				continue
			}
			if bifrostErr.Error.Message != "provider is shutting down" {
				t.Errorf("message %d: expected 'provider is shutting down', got %q",
					i, bifrostErr.Error.Message)
			}
			if bifrostErr.ExtraFields.Provider != schemas.OpenAI {
				t.Errorf("message %d: expected provider %s, got %s",
					i, schemas.OpenAI, bifrostErr.ExtraFields.Provider)
			}
			if bifrostErr.ExtraFields.RequestType != schemas.ChatCompletionRequest {
				t.Errorf("message %d: expected requestType %v, got %v",
					i, schemas.ChatCompletionRequest, bifrostErr.ExtraFields.RequestType)
			}
		default:
			t.Errorf("message %d: no error received — client would be left hanging indefinitely", i)
		}
	}
}

// TestProviderQueue_NoPanicWithoutCloseQueue verifies that the fixed hot path
// — select { case pq.queue <- msg | case <-pq.done } — never panics when
// signalClosing() fires but the queue channel is NOT closed.
//
// This is the direct inverse of TestProviderQueue_SendOnClosedChannel_Race:
// that test proves the old code panics ~50% of the time; this test proves
// the fixed code panics 0% of the time.
func TestProviderQueue_NoPanicWithoutCloseQueue(t *testing.T) {
	const iterations = 500

	for i := 0; i < iterations; i++ {
		func() {
			pq := &ProviderQueue{
				queue:      make(chan *ChannelMessage, 10),
				done:       make(chan struct{}),
				signalOnce: sync.Once{},
			}

			passedIsClosingCheck := make(chan struct{})
			shutdownDone := make(chan struct{})

			var panicked bool
			var wg sync.WaitGroup
			wg.Add(1)

			// Producer: mirrors the tryRequest hot path after the fix.
			// Passes isClosing(), waits for signalClosing, then sends.
			// The queue channel is NEVER closed — only done is closed.
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()

				if pq.isClosing() {
					return
				}
				close(passedIsClosingCheck)
				<-shutdownDone

				msg := &ChannelMessage{}
				select {
				case pq.queue <- msg: // queue is open → safe to send
				case <-pq.done: // done is closed → selected immediately
				}
			}()

			// Closer: signal shutdown but never close the queue channel
			go func() {
				<-passedIsClosingCheck
				pq.signalClosing() // closes done; does NOT close queue
				close(shutdownDone)
			}()

			wg.Wait()

			if panicked {
				t.Errorf("iteration %d: unexpected panic — queue must not be closed in the fixed path", i)
			}
		}()

		if t.Failed() {
			return
		}
	}

	t.Logf("confirmed: zero panics in %d iterations with the fix applied", iterations)
}

// =============================================================================
// UpdateProvider Lifecycle Tests
//
// These tests verify the three key invariants of the UpdateProvider fix:
//   1. New queue is stored BEFORE signalClosing fires (stale producers re-route)
//   2. Transfer happens BEFORE signalClosing (items go to new workers, not errored)
//   3. Concurrent producers + UpdateProvider produce zero panics
// =============================================================================

// TestUpdateProvider_StaleProducerReroutes verifies that a "stale producer" —
// a goroutine that fetched oldPq before UpdateProvider atomically replaced it —
// can transparently re-route to newPq when it later detects isClosing().
//
// The re-routing logic in tryRequest is:
//
//	if pq.isClosing() {
//	    if newPq, err := bifrost.getProviderQueue(provider); err == nil && newPq != pq {
//	        pq = newPq   // transparent re-route
//	    }
//	}
//
// This test exercises that exact sequence without a full Bifrost instance.
func TestUpdateProvider_StaleProducerReroutes(t *testing.T) {
	var requestQueues sync.Map
	provider := schemas.OpenAI

	oldPq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, 10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}
	newPq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, 10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	// Initial state: requestQueues holds oldPq
	requestQueues.Store(provider, oldPq)

	// Stale producer: fetched its reference before UpdateProvider ran
	stalePq := oldPq

	// Simulate UpdateProvider steps 2 + 4:
	// Step 2: atomically replace — new producers now get newPq
	requestQueues.Store(provider, newPq)
	// Step 4: signal old closing — stale producers will detect this
	oldPq.signalClosing()

	// --- Stale producer detects isClosing and attempts re-route ---
	var reroutedPq *ProviderQueue
	if stalePq.isClosing() {
		if val, ok := requestQueues.Load(provider); ok {
			candidate := val.(*ProviderQueue)
			if candidate != stalePq {
				reroutedPq = candidate
			}
		}
	}

	if reroutedPq == nil {
		t.Fatal("stale producer failed to re-route: re-route returned nil (check step ordering)")
	}
	if reroutedPq != newPq {
		t.Fatal("stale producer re-routed to wrong queue: expected newPq")
	}
	if reroutedPq.isClosing() {
		t.Fatal("re-routed queue is already closing — re-route is useless (newPq must be fresh)")
	}

	// Verify: sending to re-routed queue succeeds without panic
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		msg := &ChannelMessage{}
		select {
		case reroutedPq.queue <- msg:
		case <-reroutedPq.done:
			t.Error("newPq.done fired — newPq should be open")
		}
	}()
	if panicked {
		t.Fatal("panic while sending to re-routed queue — queue must not be closed")
	}
}

// TestUpdateProvider_TransferOrdering verifies the ordering invariant:
// items are moved from oldPq to newPq BEFORE signalClosing(oldPq) is called.
//
// Observable consequence: during the entire transfer loop, oldPq.isClosing()
// must remain false. Only after transfer completes does signalClosing fire.
func TestUpdateProvider_TransferOrdering(t *testing.T) {
	const numMessages = 8

	oldPq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, numMessages+2),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}
	newPq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, numMessages+2),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	// Pre-fill oldPq — simulates buffered requests at the moment UpdateProvider runs
	for i := 0; i < numMessages; i++ {
		oldPq.queue <- &ChannelMessage{}
	}

	// Invariant check before transfer begins
	if oldPq.isClosing() {
		t.Fatal("invariant violated: oldPq already closing before transfer begins")
	}

	// Perform transfer, mirroring UpdateProvider step 3.
	// Record whether isClosing() ever fired during the loop.
	closingDuringTransfer := false
	transferred := 0
	for {
		select {
		case msg := <-oldPq.queue:
			if oldPq.isClosing() {
				closingDuringTransfer = true
			}
			newPq.queue <- msg
			transferred++
		default:
			goto transferComplete
		}
	}
transferComplete:

	if closingDuringTransfer {
		t.Error("invariant violated: oldPq was already closing during transfer — " +
			"signalClosing must fire AFTER the transfer loop completes")
	}

	// NOW signal closing, mirroring UpdateProvider step 4
	oldPq.signalClosing()

	if !oldPq.isClosing() {
		t.Error("expected isClosing() == true after signalClosing()")
	}

	// All messages must have moved to newPq
	if transferred != numMessages {
		t.Errorf("expected %d messages transferred, got %d", numMessages, transferred)
	}
	if len(newPq.queue) != numMessages {
		t.Errorf("expected %d messages in newPq after transfer, got %d", numMessages, len(newPq.queue))
	}
	if len(oldPq.queue) != 0 {
		t.Errorf("expected 0 messages remaining in oldPq after transfer, got %d", len(oldPq.queue))
	}
}

// TestUpdateProvider_NoPanicConcurrentAccess verifies that concurrent producers
// sending to a queue that is being replaced (UpdateProvider-style) never cause
// a "send on closed channel" panic.
//
// This test directly models the production scenario that triggered the bug:
// many goroutines continuously send to a ProviderQueue while UpdateProvider
// atomically swaps the queue and signals the old one closing. With the fix
// (queue channel is never closed), the select in producers is always safe.
func TestUpdateProvider_NoPanicConcurrentAccess(t *testing.T) {
	const (
		numProducers    = 10
		numUpdates      = 30
		producerRunTime = 300 * time.Millisecond
	)

	var requestQueues sync.Map
	provider := schemas.OpenAI

	makePq := func() *ProviderQueue {
		return &ProviderQueue{
			queue:      make(chan *ChannelMessage, 200),
			done:       make(chan struct{}),
			signalOnce: sync.Once{},
		}
	}

	initialPq := makePq()
	requestQueues.Store(provider, initialPq)

	var panicCount int64
	var transferDropCount int64

	stop := make(chan struct{})
	var producerWg sync.WaitGroup

	// Drainer: continuously empties queues so producers never block on a full queue
	drainStop := make(chan struct{})
	go func() {
		for {
			select {
			case <-drainStop:
				return
			default:
				if val, ok := requestQueues.Load(provider); ok {
					pq := val.(*ProviderQueue)
					select {
					case <-pq.queue:
					default:
					}
				}
				runtime.Gosched()
			}
		}
	}()

	// Producers: continuously simulate the tryRequest hot path
	for i := 0; i < numProducers; i++ {
		producerWg.Add(1)
		go func() {
			defer producerWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}

				val, ok := requestQueues.Load(provider)
				if !ok {
					runtime.Gosched()
					continue
				}
				pq := val.(*ProviderQueue)

				func() {
					defer func() {
						if r := recover(); r != nil {
							atomic.AddInt64(&panicCount, 1)
						}
					}()

					// Re-route check (mirrors tryRequest)
					if pq.isClosing() {
						if newVal, ok2 := requestQueues.Load(provider); ok2 {
							if candidate := newVal.(*ProviderQueue); candidate != pq {
								pq = candidate
							}
						}
						// If still closing (RemoveProvider path), just return
						if pq.isClosing() {
							return
						}
					}

					msg := &ChannelMessage{}
					select {
					case pq.queue <- msg:
					case <-pq.done:
					case <-stop: // unblock immediately when the test signals stop
					}
				}()

				runtime.Gosched()
			}
		}()
	}

	// Updater: repeatedly performs UpdateProvider-style queue replacements
	var updaterWg sync.WaitGroup
	updaterWg.Add(1)
	go func() {
		defer updaterWg.Done()
		for i := 0; i < numUpdates; i++ {
			val, ok := requestQueues.Load(provider)
			if !ok {
				continue
			}
			oldPq := val.(*ProviderQueue)
			newPq := makePq()

			// Mirror production UpdateProvider step order exactly:
			// Step 2: expose newPq first so stale producers can re-route to it
			// once they see oldPq is closing.
			requestQueues.Store(provider, newPq)

			// Step 3: transfer buffered messages oldPq → newPq.
		drain:
			for {
				select {
				case msg := <-oldPq.queue:
					select {
					case newPq.queue <- msg:
					default:
						// newPq full during transfer — mirrors production cancel path.
						atomic.AddInt64(&transferDropCount, 1)
					}
				default:
					break drain
				}
			}

			// Step 4: signal closing — producers holding a stale oldPq ref now
			// re-route to newPq (already in the map from step 2).
			oldPq.signalClosing()

			time.Sleep(5 * time.Millisecond)
		}
	}()

	time.Sleep(producerRunTime)
	close(stop)
	close(drainStop)
	producerWg.Wait()
	updaterWg.Wait()

	if n := atomic.LoadInt64(&panicCount); n > 0 {
		t.Errorf("detected %d panic(s) — fix did not eliminate the concurrent-access race", n)
	} else {
		t.Logf("confirmed: zero panics across %d producers + %d queue replacements over %v",
			numProducers, numUpdates, producerRunTime)
	}
	if drops := atomic.LoadInt64(&transferDropCount); drops > 0 {
		t.Logf("note: %d message(s) dropped during transfer (oldPq had >200 buffered items) — does not affect panic correctness", drops)
	}
}

// =============================================================================
// RemoveProvider Lifecycle Tests
//
// These tests verify the behavioral contract of RemoveProvider:
//   1. signalClosing() blocks new producers (isClosing() → true)
//   2. Buffered items in the queue get "provider is shutting down" errors
//   3. Workers exit cleanly and the WaitGroup reaches zero
// =============================================================================

// TestRemoveProvider_BlocksNewProducers verifies that after signalClosing(),
// isClosing() returns true. Producers check this flag before sending and return
// a "provider is shutting down" error rather than trying to enqueue.
func TestRemoveProvider_BlocksNewProducers(t *testing.T) {
	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, 10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	// Sanity: before shutdown, producers can proceed
	if pq.isClosing() {
		t.Fatal("isClosing() must be false before RemoveProvider runs")
	}

	// RemoveProvider step 2: signal closing
	pq.signalClosing()

	// New producers must see isClosing() == true and abort
	if !pq.isClosing() {
		t.Fatal("isClosing() must be true after signalClosing() (RemoveProvider)")
	}

	// done must be closed so any producer blocked in the select unblocks immediately
	select {
	case <-pq.done:
		// correct
	default:
		t.Fatal("pq.done must be closed after signalClosing() so blocking producers unblock")
	}

	// CRITICAL: queue channel must remain OPEN — closing it would cause panics in
	// any producer that entered the select before seeing isClosing().
	// With the fix, we NEVER close the queue channel.
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		// A select with done closed always takes the done case — safe, no panic
		select {
		case pq.queue <- &ChannelMessage{}:
		case <-pq.done:
		}
	}()
	if panicked {
		t.Fatal("queue channel must stay open after signalClosing() — closing it causes panics")
	}
}

// TestRemoveProvider_BufferedRequestsGetErrors verifies the drain contract:
// items queued BEFORE signalClosing fires must each receive a
// "provider is shutting down" error on their Err channel. No client should be
// left hanging.
//
// This test exercises the drain logic directly — the same code path that
// requestWorker executes in its case <-pq.done: branch — to avoid the
// non-deterministic select race where the normal processing path can pick up
// items before done fires.
func TestRemoveProvider_BufferedRequestsGetErrors(t *testing.T) {
	const numBuffered = 8

	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, numBuffered+5),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Buffer requests — simulates requests already queued when RemoveProvider runs
	msgs := make([]*ChannelMessage, numBuffered)
	for i := 0; i < numBuffered; i++ {
		msgs[i] = newTestChannelMessage(ctx)
		pq.queue <- msgs[i]
	}

	// RemoveProvider step 2: signal closing
	pq.signalClosing()

	// Execute the drain path — exactly what requestWorker does in case <-pq.done:
	<-pq.done // fires immediately since signalClosing was already called
drainLoop:
	for {
		select {
		case r := <-pq.queue:
			provKey, mod, _ := r.GetRequestFields()
			r.Err <- schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: "provider is shutting down",
				},
				ExtraFields: schemas.BifrostErrorExtraFields{
					RequestType:            r.RequestType,
					Provider:               provKey,
					OriginalModelRequested: mod,
				},
			}
		default:
			break drainLoop
		}
	}

	// Every buffered message must have received a shutdown error
	for i, msg := range msgs {
		select {
		case bifrostErr := <-msg.Err:
			if bifrostErr.Error == nil {
				t.Errorf("message %d: got nil Error field in BifrostError", i)
				continue
			}
			if bifrostErr.Error.Message != "provider is shutting down" {
				t.Errorf("message %d: expected 'provider is shutting down', got %q",
					i, bifrostErr.Error.Message)
			}
			if bifrostErr.ExtraFields.Provider != schemas.OpenAI {
				t.Errorf("message %d: expected provider %s, got %s",
					i, schemas.OpenAI, bifrostErr.ExtraFields.Provider)
			}
			if bifrostErr.ExtraFields.RequestType != schemas.ChatCompletionRequest {
				t.Errorf("message %d: expected requestType %v, got %v",
					i, schemas.ChatCompletionRequest, bifrostErr.ExtraFields.RequestType)
			}
		default:
			t.Errorf("message %d: no error received — client would be left hanging indefinitely", i)
		}
	}
}

// TestRemoveProvider_WorkerWaitGroupCompletes verifies that after signalClosing(),
// the worker goroutine decrements the WaitGroup and wg.Wait() returns promptly.
// This mirrors what RemoveProvider does: signal, then Wait() before cleanup.
func TestRemoveProvider_WorkerWaitGroupCompletes(t *testing.T) {
	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, 10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	var wg sync.WaitGroup
	wg.Add(1)

	// Worker goroutine — mirrors requestWorker's WaitGroup contract
	go func() {
		defer wg.Done()
		for {
			select {
			case r, ok := <-pq.queue:
				if !ok {
					return
				}
				_ = r
			case <-pq.done:
				// Drain remaining (empty in this test)
				for {
					select {
					case <-pq.queue:
					default:
						return
					}
				}
			}
		}
	}()

	// Tiny sleep to ensure worker is parked on select before we signal
	time.Sleep(10 * time.Millisecond)

	// RemoveProvider step 2: signal closing
	pq.signalClosing()

	// RemoveProvider step 3: wait for workers — must complete promptly
	waitReturned := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitReturned)
	}()

	select {
	case <-waitReturned:
		// correct: WaitGroup reached zero after signalClosing()
	case <-time.After(2 * time.Second):
		t.Fatal("wg.Wait() did not return after signalClosing() — worker is stuck (would deadlock RemoveProvider)")
	}
}

// TestRemoveProvider_ConcurrentNewProducersDuringShutdown verifies that
// concurrent producers trying to enqueue after RemoveProvider calls
// signalClosing() all get safe "provider is shutting down" errors — none panic.
// This tests the TOCTOU window: producer passes isClosing() check, then done fires.
func TestRemoveProvider_ConcurrentNewProducersDuringShutdown(t *testing.T) {
	const numProducers = 50

	pq := &ProviderQueue{
		queue:      make(chan *ChannelMessage, numProducers+10),
		done:       make(chan struct{}),
		signalOnce: sync.Once{},
	}

	var panicCount int64
	var shutdownErrors int64
	var successfulSends int64

	// Gate: all producers start together after isClosing() passes
	passedGate := make(chan struct{})
	var gateOnce sync.Once
	shutdownFired := make(chan struct{})

	var producerWg sync.WaitGroup

	for i := 0; i < numProducers; i++ {
		producerWg.Add(1)
		go func() {
			defer producerWg.Done()
			defer func() {
				if r := recover(); r != nil {
					atomic.AddInt64(&panicCount, 1)
				}
			}()

			// Each producer checks isClosing() first (mirrors tryRequest)
			if pq.isClosing() {
				atomic.AddInt64(&shutdownErrors, 1)
				return
			}

			// Signal that at least one producer passed the isClosing() check
			gateOnce.Do(func() { close(passedGate) })

			// Wait for shutdown to be signaled (the TOCTOU window)
			<-shutdownFired

			// Producers now enter the select — with the fix, done is closed but
			// queue is NOT closed, so this select is always safe (no panic)
			msg := &ChannelMessage{}
			select {
			case pq.queue <- msg:
				atomic.AddInt64(&successfulSends, 1)
			case <-pq.done:
				atomic.AddInt64(&shutdownErrors, 1)
			}
		}()
	}

	// Wait for at least one producer to pass the isClosing() gate
	select {
	case <-passedGate:
	case <-time.After(2 * time.Second):
		t.Fatal("no producer passed the isClosing() check within timeout")
	}

	// Signal shutdown (RemoveProvider step 2) — this is the TOCTOU race
	pq.signalClosing()
	close(shutdownFired)

	producerWg.Wait()

	if n := atomic.LoadInt64(&panicCount); n > 0 {
		t.Errorf("detected %d panic(s) — queue must not be closed during concurrent shutdown", n)
	}

	t.Logf("result: %d successful sends, %d shutdown errors, %d panics across %d producers",
		atomic.LoadInt64(&successfulSends),
		atomic.LoadInt64(&shutdownErrors),
		atomic.LoadInt64(&panicCount),
		numProducers)
}

// TestPluginPipelineStreamingRace reproduces the production panic:
//
//	fatal error: concurrent map read and map write
//	(*PluginPipeline).FinalizeStreamingPostHookSpans
//
// It hammers accumulatePluginTiming (per-chunk writer) concurrently with
// FinalizeStreamingPostHookSpans (end-of-stream reader) and resetPluginPipeline
// (pool-release writer). Before the streamingMu fix these three paths had no
// synchronisation and the -race detector / runtime map check would trip
// immediately. Run with: go test -race -run PluginPipelineStreamingRace
func TestPluginPipelineStreamingRace(t *testing.T) {
	p := &PluginPipeline{
		logger: NewDefaultLogger(schemas.LogLevelError),
		tracer: &schemas.NoOpTracer{},
	}

	const writers = 8
	const iterations = 2000

	var wg sync.WaitGroup

	// Per-chunk accumulator writers — simulate multiple plugins accumulating
	// timing for every streamed chunk.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pluginName := fmt.Sprintf("plugin-%d", id%3) // a few distinct plugin keys
			for i := 0; i < iterations; i++ {
				p.accumulatePluginTiming(pluginName, time.Microsecond, i%17 == 0)
			}
		}(w)
	}

	// End-of-stream finalizer racing with writers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := context.Background()
		for i := 0; i < iterations/10; i++ {
			p.FinalizeStreamingPostHookSpans(ctx)
		}
	}()

	// resetPluginPipeline racing with writers — simulates the pool returning
	// the pipeline to another request mid-flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations/10; i++ {
			p.resetPluginPipeline()
		}
	}()

	// Concurrent GetChunkCount readers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = p.GetChunkCount()
		}
	}()

	wg.Wait()
}

// TestFilterKeysByID covers the KeyID scoping path for ListModels requests:
// a hit returns the single matching key, a miss returns an empty slice
// (which the caller surfaces as "no key found"), and the input slice must
// not be mutated.
func TestFilterKeysByID(t *testing.T) {
	keys := []schemas.Key{
		{ID: "k1"},
		{ID: "k2"},
		{ID: "k3"},
	}

	t.Run("match returns single key", func(t *testing.T) {
		got := filterKeysByID(keys, "k2")
		if len(got) != 1 || got[0].ID != "k2" {
			t.Fatalf("filterKeysByID(_, k2) = %+v, want one key with ID=k2", got)
		}
	})

	t.Run("no match returns empty slice", func(t *testing.T) {
		got := filterKeysByID(keys, "does-not-exist")
		if len(got) != 0 {
			t.Fatalf("filterKeysByID(_, missing) = %+v, want empty", got)
		}
	})

	t.Run("empty target returns empty slice", func(t *testing.T) {
		got := filterKeysByID(keys, "")
		if len(got) != 0 {
			t.Fatalf("filterKeysByID(_, \"\") = %+v, want empty", got)
		}
	})

	t.Run("input slice is not mutated", func(t *testing.T) {
		before := make([]schemas.Key, len(keys))
		copy(before, keys)
		_ = filterKeysByID(keys, "k1")
		for i := range keys {
			if keys[i].ID != before[i].ID {
				t.Fatalf("input mutated at index %d: got %q, want %q", i, keys[i].ID, before[i].ID)
			}
		}
	})
}

// fakeRoutingPlugin is a minimal LLMPlugin whose PreRequestHook writes a routing key pin to the
// non-reserved BifrostContextKeyRoutingPinnedAPIKeyID, mirroring what the governance routing
// engine does. It exists to exercise the commit step in PluginPipeline.RunPreRequestHooks.
type fakeRoutingPlugin struct {
	name     string
	pinKeyID string // written to BifrostContextKeyRoutingPinnedAPIKeyID when non-empty
}

func (f *fakeRoutingPlugin) GetName() string { return f.name }
func (f *fakeRoutingPlugin) Cleanup() error  { return nil }
func (f *fakeRoutingPlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if f.pinKeyID != "" {
		// A direct write to the reserved BifrostContextKeyAPIKeyID here would be dropped by the
		// restricted-write block; routing must use the non-reserved key.
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, f.pinKeyID)
	}
	return nil
}
func (f *fakeRoutingPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (f *fakeRoutingPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

type postHookResponsePreservingPlugin struct {
	fakeRoutingPlugin
	block                 bool
	seenResponseWithError bool
}

func (p *postHookResponsePreservingPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if p.block {
		statusCode := 400
		return resp, &schemas.BifrostError{
			StatusCode: &statusCode,
			Error:      &schemas.ErrorField{Message: "blocked"},
		}, nil
	}
	p.seenResponseWithError = resp != nil && bifrostErr != nil
	return resp, bifrostErr, nil
}

func TestRunPostLLMHooksPreservesProviderResponseWithGuardrailError(t *testing.T) {
	t.Parallel()

	observer := &postHookResponsePreservingPlugin{fakeRoutingPlugin: fakeRoutingPlugin{name: "observer"}}
	guardrail := &postHookResponsePreservingPlugin{fakeRoutingPlugin: fakeRoutingPlugin{name: "guardrail"}, block: true}
	pipeline := newRoutingCommitPipeline(observer, guardrail)
	resp := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Model: "gpt-4o-transcribe"}}

	gotResp, gotErr := pipeline.RunPostLLMHooks(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), resp, nil, 2)
	if gotResp != resp {
		t.Fatalf("response = %#v, want original provider response", gotResp)
	}
	if gotErr == nil || gotErr.Error == nil || gotErr.Error.Message != "blocked" {
		t.Fatalf("error = %#v, want guardrail block", gotErr)
	}
	if !observer.seenResponseWithError {
		t.Fatal("downstream post-hook did not receive both the provider response and guardrail error")
	}
}

func newRoutingCommitPipeline(plugins ...schemas.LLMPlugin) *PluginPipeline {
	return &PluginPipeline{
		logger:     NewDefaultLogger(schemas.LogLevelError),
		tracer:     &schemas.NoOpTracer{},
		llmPlugins: plugins,
	}
}

// TestRunPreRequestHooks_CommitsRoutingPinnedKey verifies that the pinned key a routing rule
// writes to the non-reserved BifrostContextKeyRoutingPinnedAPIKeyID (during the blocked
// PreRequestHook phase) is committed by core into the reserved BifrostContextKeyAPIKeyID that
// key selection reads — and that the routing pin's precedence over a caller-supplied pin holds.
func TestRunPreRequestHooks_CommitsRoutingPinnedKey(t *testing.T) {
	const pinned = "routing-pinned-key-id"

	t.Run("routing pin is committed to reserved api-key-id", func(t *testing.T) {
		p := newRoutingCommitPipeline(&fakeRoutingPlugin{name: "gov", pinKeyID: pinned})
		ctx := schemas.NewBifrostContext(context.Background(), time.Now())
		p.RunPreRequestHooks(ctx, &schemas.BifrostRequest{})
		if got, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); got != pinned {
			t.Fatalf("APIKeyID = %q, want %q", got, pinned)
		}
	})

	t.Run("routing pin overrides a caller-supplied api-key-id", func(t *testing.T) {
		p := newRoutingCommitPipeline(&fakeRoutingPlugin{name: "gov", pinKeyID: pinned})
		ctx := schemas.NewBifrostContext(context.Background(), time.Now())
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "caller-pin")
		p.RunPreRequestHooks(ctx, &schemas.BifrostRequest{})
		if got, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); got != pinned {
			t.Fatalf("APIKeyID = %q, want %q (routing pin must override caller pin)", got, pinned)
		}
	})

	t.Run("caller api-key-id preserved when no routing pin", func(t *testing.T) {
		p := newRoutingCommitPipeline(&fakeRoutingPlugin{name: "noop"})
		ctx := schemas.NewBifrostContext(context.Background(), time.Now())
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "caller-pin")
		p.RunPreRequestHooks(ctx, &schemas.BifrostRequest{})
		if got, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); got != "caller-pin" {
			t.Fatalf("APIKeyID = %q, want %q (no routing pin must not clobber caller pin)", got, "caller-pin")
		}
	})
}

// modelRewritingPlugin rewrites the model in PreRequestHook the way a routing rule does, and
// records what its PreLLMHook then reads for the caller's original route.
type modelRewritingPlugin struct {
	routedModel string

	mu              sync.Mutex
	sawProvider     schemas.ModelProvider
	sawModel        string
	sawRoutedModel  string
	preLLMHookCalls int
}

func (p *modelRewritingPlugin) GetName() string { return "model-rewriting" }
func (p *modelRewritingPlugin) Cleanup() error  { return nil }
func (p *modelRewritingPlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	// The requested route is reserved, so a plugin cannot overwrite what the caller sent.
	ctx.SetValue(schemas.BifrostContextKeyRequestedModel, "forged-by-plugin")
	req.SetModel(p.routedModel)
	return nil
}
func (p *modelRewritingPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preLLMHookCalls++
	p.sawProvider, _ = ctx.Value(schemas.BifrostContextKeyRequestedProvider).(schemas.ModelProvider)
	p.sawModel, _ = ctx.Value(schemas.BifrostContextKeyRequestedModel).(string)
	_, p.sawRoutedModel, _ = req.GetRequestFields()
	return req, nil, nil
}
func (p *modelRewritingPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// TestRequestedRouteSurvivesPreRequestRewrite pins that the provider/model the caller sent stay
// readable after a PreRequestHook rewrites the model: on the context for a later plugin's
// PreLLMHook, and on RoutingInfo next to the routed model, for JSON and streaming responses.
func TestRequestedRouteSurvivesPreRequestRewrite(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			server, _ := openAICompatFallbackServer(t)
			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, server.URL)
			account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
			account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
				{ID: "openai-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100},
			})
			plugin := &modelRewritingPlugin{routedModel: "gpt-4o"}
			client, initErr := Init(context.Background(), schemas.BifrostConfig{
				Account:    account,
				Logger:     NewNoOpLogger(),
				LLMPlugins: []schemas.LLMPlugin{plugin},
			})
			if initErr != nil {
				t.Fatalf("Init failed: %v", initErr)
			}
			t.Cleanup(client.Shutdown)

			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
			defer ctx.Cancel()
			req := &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
				},
			}

			var got schemas.RoutingInfo
			if !stream {
				resp, bifrostErr := client.ChatCompletionRequest(ctx, req)
				if bifrostErr != nil {
					t.Fatalf("request failed: %s", bifrostErr.Error.Message)
				}
				got = resp.ExtraFields.RoutingInfo
			} else {
				ch, bifrostErr := client.ChatCompletionStreamRequest(ctx, req)
				if bifrostErr != nil {
					t.Fatalf("stream failed: %s", bifrostErr.Error.Message)
				}
				for chunk := range ch {
					if chunk.BifrostError != nil && chunk.BifrostError.Error != nil {
						t.Fatalf("stream emitted an error chunk: %s", chunk.BifrostError.Error.Message)
					}
					if chunk.BifrostChatResponse != nil {
						got = chunk.BifrostChatResponse.ExtraFields.RoutingInfo
					}
				}
				// The transport writes stream response headers from this snapshot before the
				// first chunk, so it must carry the requested route too.
				snapshot, ok := ctx.Value(schemas.BifrostContextKeyRoutingInfo).(schemas.RoutingInfo)
				if !ok {
					t.Fatal("no RoutingInfo snapshot on the context after the stream")
				}
				if snapshot.RequestedProvider != schemas.OpenAI || snapshot.RequestedModel != "gpt-4o-mini" {
					t.Errorf("snapshot requested route = (%q, %q), want (openai, gpt-4o-mini)", snapshot.RequestedProvider, snapshot.RequestedModel)
				}
			}

			plugin.mu.Lock()
			defer plugin.mu.Unlock()
			if plugin.preLLMHookCalls == 0 {
				t.Fatal("PreLLMHook never ran")
			}
			if plugin.sawProvider != schemas.OpenAI || plugin.sawModel != "gpt-4o-mini" {
				t.Errorf("PreLLMHook read requested route (%q, %q), want (openai, gpt-4o-mini)", plugin.sawProvider, plugin.sawModel)
			}
			if plugin.sawRoutedModel != "gpt-4o" {
				t.Errorf("PreLLMHook request model = %q, want the rewritten gpt-4o", plugin.sawRoutedModel)
			}
			if got.Model != "gpt-4o" {
				t.Errorf("RoutingInfo.Model = %q, want the rewritten gpt-4o", got.Model)
			}
			if got.RequestedProvider != schemas.OpenAI || got.RequestedModel != "gpt-4o-mini" {
				t.Errorf("RoutingInfo requested route = (%q, %q), want (openai, gpt-4o-mini)", got.RequestedProvider, got.RequestedModel)
			}
		})
	}
}

// TestClearAnthropicPassthroughForNonNativeProvider verifies that Anthropic raw-body
// passthrough flags are cleared only when an Anthropic-integration request resolves to a
// provider/model pair that doesn't speak the Anthropic Messages API natively (e.g. Bedrock).
// This guards the fix for Claude-via-Bedrock tool calls breaking when the model is routed to
// Bedrock through a key alias (so the catalog-time guard never fires), and the fix for a
// routing rule retargeting a Claude Code request to a non-Claude model on a multi-family
// provider (Vertex/Azure/Bedrock Mantle), which sent the raw Anthropic body to that provider's
// OpenAI/Gemini surface.
func TestClearAnthropicPassthroughForNonNativeProvider(t *testing.T) {
	flagKeys := []schemas.BifrostContextKey{
		schemas.BifrostContextKeyUseRawRequestBody,
		schemas.BifrostContextKeySendBackRawResponse,
		schemas.BifrostContextKeyPassthroughOverridesPresent,
	}

	tests := []struct {
		name            string
		integrationType string
		baseProvider    schemas.ModelProvider
		model           string
		alias           *schemas.ResolvedAlias
		wantCleared     bool
	}{
		{"anthropic integration to bedrock clears", "anthropic", schemas.Bedrock, "anthropic.claude-sonnet-4-20250514-v1:0", nil, true},
		{"anthropic integration to anthropic preserved", "anthropic", schemas.Anthropic, "claude-sonnet-4-20250514", nil, false},
		{"anthropic integration to vertex preserved", "anthropic", schemas.Vertex, "claude-sonnet-4@20250514", nil, false},
		{"anthropic integration to azure preserved", "anthropic", schemas.Azure, "claude-sonnet-4-20250514", nil, false},
		{"anthropic integration to bedrock mantle preserved", "anthropic", schemas.BedrockMantle, "claude-sonnet-4-20250514", nil, false},
		{"anthropic integration to bedrock mantle openai model clears", "anthropic", schemas.BedrockMantle, "gpt-5-6-luna", nil, true},
		{"anthropic integration to azure openai model clears", "anthropic", schemas.Azure, "gpt-5", nil, true},
		{"anthropic integration to vertex gemini model clears", "anthropic", schemas.Vertex, "gemini-2.5-pro", nil, true},
		{
			name:            "anthropic integration to bedrock mantle claude alias preserved",
			integrationType: "anthropic",
			baseProvider:    schemas.BedrockMantle,
			model:           "fast-model",
			alias: &schemas.ResolvedAlias{
				Key:    "fast-model",
				Config: &schemas.AliasConfig{ModelID: "anthropic.claude-sonnet-4-20250514-v1:0"},
			},
			wantCleared: false,
		},
		{"non-anthropic integration to bedrock preserved", "openai", schemas.Bedrock, "claude-sonnet-4-20250514", nil, false},
		{"no integration type to bedrock preserved", "", schemas.Bedrock, "claude-sonnet-4-20250514", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.integrationType != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tt.integrationType)
			}
			if tt.alias != nil {
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, tt.alias)
			}
			for _, k := range flagKeys {
				ctx.SetValue(k, true)
			}

			ctx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
			ctx.SetValue(schemas.BifrostContextKeyURLPath, "/v1/messages")

			clearAnthropicPassthroughForNonNativeProvider(ctx, tt.baseProvider, tt.model)

			for _, k := range flagKeys {
				got, _ := ctx.Value(k).(bool)
				want := !tt.wantCleared // flags start true; cleared means false
				if got != want {
					t.Errorf("flag %v = %v, want %v", k, got, want)
				}
			}

			// The caller's captured path goes with the raw body: a converted request must land on
			// the provider's own endpoint, not Anthropic's /v1/messages.
			callerPath, hasCallerPath := ctx.Value(schemas.BifrostContextKeyURLPath).(string)
			if tt.wantCleared && hasCallerPath {
				t.Errorf("URLPath = %q, want it cleared for a non-native provider", callerPath)
			}
			if !tt.wantCleared && callerPath != "/v1/messages" {
				t.Errorf("URLPath = %q, want %q preserved", callerPath, "/v1/messages")
			}

			// SkipKeySelection must survive: it also drives IsClaudeCodeMaxMode, which suppresses
			// x-api-key on the Anthropic provider. Clearing it here would make an Anthropic
			// fallback after a non-native attempt send the account key alongside the caller's
			// OAuth token. The flag is gated at the key-selection read site instead —
			// see isKeySkippingAllowed.
			if skip, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); !skip {
				t.Error("SkipKeySelection was cleared; it must be gated at the read site, not mutated per attempt")
			}
		})
	}
}

// TestClearAnthropicPassthroughForUnsupportedStructuredOutput covers a Claude Code session-title
// call (a one-field JSON schema in output_config.format) that a routing rule retargets to Bedrock
// Mantle. The ingress guard in the Anthropic integration only sees a provider spelled out in the
// caller's model string, so an alias or routing rule hides it and the raw body used to reach the
// Mantle Messages API with output_config.format intact — 400 "Extra inputs are not permitted".
// Only the raw request body is dropped: the raw response flags stay on, and the response path
// keys off the synthetic bf_so_* tool name instead. Vertex and Azure take the same route.
func TestClearAnthropicPassthroughForUnsupportedStructuredOutput(t *testing.T) {
	const outputConfigBody = `{"model":"claude-sonnet-5","output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}}}}}}`
	// Legacy beta shape: top-level output_format instead of output_config.format.
	const outputFormatBody = `{"model":"claude-sonnet-5","output_format":{"type":"json_schema","schema":{"type":"object"}}}`
	const noFormatBody = `{"model":"claude-sonnet-5","messages":[]}`

	responsesRequest := func(rawBody string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			ResponsesRequest: &schemas.BifrostResponsesRequest{RawRequestBody: []byte(rawBody)},
		}
	}

	tests := []struct {
		name            string
		integrationType string
		baseProvider    schemas.ModelProvider
		useRawBody      bool
		req             *schemas.BifrostRequest
		wantCleared     bool
	}{
		{"bedrock mantle with output_config.format clears", "anthropic", schemas.BedrockMantle, true, responsesRequest(outputConfigBody), true},
		{"bedrock mantle with legacy output_format clears", "anthropic", schemas.BedrockMantle, true, responsesRequest(outputFormatBody), true},
		{"vertex with output_config.format clears", "anthropic", schemas.Vertex, true, responsesRequest(outputConfigBody), true},
		{"azure with output_config.format clears", "anthropic", schemas.Azure, true, responsesRequest(outputConfigBody), true},
		// Anthropic and Bedrock Converse serve the schema natively; nothing to rewrite.
		{"anthropic with output_config.format preserved", "anthropic", schemas.Anthropic, true, responsesRequest(outputConfigBody), false},
		{"bedrock with output_config.format preserved", "anthropic", schemas.Bedrock, true, responsesRequest(outputConfigBody), false},
		{"bedrock mantle without a format preserved", "anthropic", schemas.BedrockMantle, true, responsesRequest(noFormatBody), false},
		// Typed conversion is already in force — the raw body is never consulted.
		{"passthrough already off stays off", "anthropic", schemas.BedrockMantle, false, responsesRequest(outputConfigBody), false},
		{"non-anthropic integration preserved", "openai", schemas.BedrockMantle, true, responsesRequest(outputConfigBody), false},
		{"no integration type preserved", "", schemas.BedrockMantle, true, responsesRequest(outputConfigBody), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.integrationType != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tt.integrationType)
			}
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, tt.useRawBody)
			ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
			ctx.SetValue(schemas.BifrostContextKeyPassthroughOverridesPresent, true)

			clearAnthropicPassthroughForUnsupportedStructuredOutput(ctx, tt.baseProvider, tt.req)

			useRawBody, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			if want := tt.useRawBody && !tt.wantCleared; useRawBody != want {
				t.Errorf("UseRawRequestBody = %v, want %v", useRawBody, want)
			}

			// The raw-response side is untouched: the Anthropic integration skips passthrough on
			// the way back when a structured-output tool name is set, so clearing these here would
			// only lose the caller's raw-response opt-in.
			for _, k := range []schemas.BifrostContextKey{
				schemas.BifrostContextKeySendBackRawResponse,
				schemas.BifrostContextKeyPassthroughOverridesPresent,
			} {
				if flag, _ := ctx.Value(k).(bool); !flag {
					t.Errorf("flag %v was cleared, want it preserved", k)
				}
			}
		})
	}
}

// TestClearCtxForFallback_DropsCallerSuppliedKey verifies that a raw key supplied via
// x-bf-direct-key does not ride a fallback onto a different provider. Key selection resolves the
// direct key before it ever reaches the provider's own pool, so an uncleared value would send the
// caller's credential for provider A to provider B.
func TestClearCtxForFallback_DropsCallerSuppliedKey(t *testing.T) {
	account := NewMockAccount()
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "anthropic-configured", Name: "anthropic-configured", Value: schemas.SecretVar{Val: "sk-ant-real"}, Models: []string{"*"}, Weight: 1.0},
	})
	bifrost := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{
		ID: "header-provided", Name: "header-provided",
		Value: schemas.SecretVar{Val: "sk-caller-supplied"}, Weight: 1.0,
	})

	// A routing rule's key pin is scoped to the provider whose pool it was resolved against.
	ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, "primary-provider-key")

	clearCtxForFallback(ctx)

	if _, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		t.Fatal("DirectKey survived clearCtxForFallback")
	}
	if pin, ok := ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string); ok {
		t.Fatalf("RoutingPinnedAPIKeyID survived clearCtxForFallback: %q", pin)
	}

	// #6973: provider response headers belong to the provider that produced
	// them. If a fallback attempt fails pre-flight, the previous provider's
	// headers must not survive on the context and be forwarded with the
	// fallback's error response.
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, map[string]string{
		"retry-after":                  "60",
		"x-ratelimit-remaining-tokens": "0",
	})
	clearCtxForFallback(ctx)
	if headers, ok := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string); ok {
		t.Fatalf("ProviderResponseHeaders survived clearCtxForFallback: %v", headers)
	}

	keys, _, err := bifrost.selectKeyFromProviderForModelWithPool(ctx, schemas.ChatCompletionRequest, schemas.Anthropic, "claude-opus-4-5", schemas.Anthropic)
	if err != nil {
		t.Fatalf("selectKeyFromProviderForModelWithPool: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != "anthropic-configured" {
		t.Fatalf("got %v, want the fallback provider's own key", keys)
	}
}

func TestShouldContinueWithFallbacksHandlesIncompleteBifrostError(t *testing.T) {
	statusCode := http.StatusServiceUnavailable
	bifrost := &Bifrost{logger: NewDefaultLogger(schemas.LogLevelError)}
	fallback := schemas.Fallback{Provider: schemas.Anthropic}
	fallbackErr := &schemas.BifrostError{StatusCode: &statusCode}

	if !bifrost.shouldContinueWithFallbacks(fallback, fallbackErr) {
		t.Fatal("incomplete plugin error should allow the next fallback")
	}
}

func TestShouldContinueWithFallbacksStopsOnNilError(t *testing.T) {
	bifrost := &Bifrost{logger: NewDefaultLogger(schemas.LogLevelError)}
	fallback := schemas.Fallback{Provider: schemas.Anthropic}
	if bifrost.shouldContinueWithFallbacks(fallback, nil) {
		t.Fatal("nil error should stop fallback processing")
	}
}

// TestSelectKeyFromProviderForModelWithPool_SkipKeySelectionGatedOnBaseProvider verifies that the
// Claude Code OAuth key-selection skip applies only when the attempt resolved to Anthropic. A
// governance routing rule can rewrite provider/model after the transport set the flag, and every
// non-Anthropic provider authenticates with its own configured key — so it must still get one.
func TestSelectKeyFromProviderForModelWithPool_SkipKeySelectionGatedOnBaseProvider(t *testing.T) {
	account := NewMockAccount()
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "anthropic-key", Name: "anthropic-key", Value: schemas.SecretVar{Val: "sk-ant"}, Models: []string{"*"}, Weight: 1.0},
	})
	account.SetKeysForProvider(schemas.Fireworks, []schemas.Key{
		{ID: "fireworks-key", Name: "fireworks-key", Value: schemas.SecretVar{Val: "fw-key"}, Models: []string{"*"}, Weight: 1.0, UseAnthropicEndpoints: schemas.Ptr(true)},
	})
	bifrost := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}

	tests := []struct {
		name      string
		provider  schemas.ModelProvider
		model     string
		wantKeyID string // "" means the pool must be empty (key selection skipped)
	}{
		{"anthropic keeps the skip", schemas.Anthropic, "claude-opus-4-5", ""},
		{"fireworks selects its own key", schemas.Fireworks, "accounts/fireworks/models/kimi-k2p7-code", "fireworks-key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)

			keys, canRotate, err := bifrost.selectKeyFromProviderForModelWithPool(ctx, schemas.ResponsesRequest, tt.provider, tt.model, tt.provider)
			if err != nil {
				t.Fatalf("selectKeyFromProviderForModelWithPool: %v", err)
			}
			if canRotate {
				t.Error("canRotate = true, want false")
			}
			if tt.wantKeyID == "" {
				if len(keys) != 0 {
					t.Fatalf("got %d keys, want an empty pool (key selection skipped)", len(keys))
				}
				return
			}
			if len(keys) != 1 || keys[0].ID != tt.wantKeyID {
				t.Fatalf("got %v, want a single key %q", keys, tt.wantKeyID)
			}
			// The regression: without a key, UseAnthropicEndpoints is unreadable and the request
			// is built with the OpenAI schema, which Fireworks rejects for missing max_tokens.
			if keys[0].UseAnthropicEndpoints == nil || !*keys[0].UseAnthropicEndpoints {
				t.Error("selected key lost UseAnthropicEndpoints")
			}
		})
	}
}

// Test that releaseChannelMessage clears all request-scoped references so an
// idle pooled ChannelMessage cannot pin the parsed request body, the request
// context, or an undelivered response/error.
func TestReleaseChannelMessage_ClearsPooledReferences(t *testing.T) {
	b := &Bifrost{
		channelMessagePool: sync.Pool{New: func() interface{} { return &ChannelMessage{} }},
		responseChannelPool: sync.Pool{New: func() interface{} {
			return make(chan *schemas.BifrostResponse, 1)
		}},
		errorChannelPool: sync.Pool{New: func() interface{} {
			return make(chan schemas.BifrostError, 1)
		}},
		responseStreamPool: sync.Pool{New: func() interface{} {
			return make(chan chan *schemas.BifrostStreamChunk, 1)
		}},
	}

	req := schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Model: "test-model",
			Input: []schemas.ChatMessage{{}},
		},
	}
	msg := b.getChannelMessage(req)
	msg.Context = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Simulate an undelivered response and error sitting in the channels.
	respCh := msg.Response
	errCh := msg.Err
	respCh <- &schemas.BifrostResponse{}
	errCh <- schemas.BifrostError{}

	b.releaseChannelMessage(msg)

	if msg.ChatRequest != nil || msg.RequestType != "" {
		t.Error("releaseChannelMessage should zero the embedded BifrostRequest")
	}
	if msg.Context != nil {
		t.Error("releaseChannelMessage should clear the Context reference")
	}
	select {
	case <-respCh:
		t.Error("pooled response channel should be drained before Put")
	default:
	}
	select {
	case <-errCh:
		t.Error("pooled error channel should be drained before Put")
	default:
	}
}

// Streaming variant: releaseChannelMessage must also drain and clear
// ResponseStream, which is only allocated for stream request types.
func TestReleaseChannelMessage_ClearsPooledReferences_Streaming(t *testing.T) {
	b := &Bifrost{
		channelMessagePool: sync.Pool{New: func() interface{} { return &ChannelMessage{} }},
		responseChannelPool: sync.Pool{New: func() interface{} {
			return make(chan *schemas.BifrostResponse, 1)
		}},
		errorChannelPool: sync.Pool{New: func() interface{} {
			return make(chan schemas.BifrostError, 1)
		}},
		responseStreamPool: sync.Pool{New: func() interface{} {
			return make(chan chan *schemas.BifrostStreamChunk, 1)
		}},
	}

	req := schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Model: "test-model",
			Input: []schemas.ChatMessage{{}},
		},
	}
	msg := b.getChannelMessage(req)
	msg.Context = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	if msg.ResponseStream == nil {
		t.Fatal("getChannelMessage should allocate ResponseStream for stream request types")
	}

	// Simulate an undelivered stream handoff sitting in the channel.
	streamCh := msg.ResponseStream
	streamCh <- make(chan *schemas.BifrostStreamChunk)

	b.releaseChannelMessage(msg)

	if msg.ChatRequest != nil || msg.RequestType != "" {
		t.Error("releaseChannelMessage should zero the embedded BifrostRequest")
	}
	if msg.Context != nil {
		t.Error("releaseChannelMessage should clear the Context reference")
	}
	if msg.ResponseStream != nil {
		t.Error("releaseChannelMessage should clear the ResponseStream reference")
	}
	select {
	case <-streamCh:
		t.Error("pooled response stream channel should be drained before Put")
	default:
	}
}

// TestExecuteRequestWithRetries_EmptyStreamReturnsClosedChannel pins the public
// streaming contract for zero-chunk streams: when the provider's channel closes
// before the first chunk, the caller must receive a NON-nil, closed channel with
// a nil error — not (nil, nil). A nil channel with a nil error makes integrators
// that range/receive on the result block forever, since a receive from a nil
// channel never returns.
func TestExecuteRequestWithRetries_EmptyStreamReturnsClosedChannel(t *testing.T) {
	config := createTestConfig(1, 10*time.Millisecond, 100*time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	logger := NewDefaultLogger(schemas.LogLevelError)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})

	handler := func(_ schemas.Key) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		ch := make(chan *schemas.BifrostStreamChunk)
		close(ch) // provider stream ends before emitting any chunk
		return ch, nil
	}

	stream, err := executeRequestWithRetries(
		ctx,
		config,
		handler,
		nil,
		schemas.ChatCompletionStreamRequest,
		schemas.OpenAI,
		"gpt-4",
		nil,
		logger,
	)

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if stream == nil {
		t.Fatal("Expected non-nil closed channel for an empty stream; a nil channel with a nil error hangs consumers on a nil-channel receive")
	}
	select {
	case _, ok := <-stream:
		if ok {
			t.Error("Expected zero chunks from an empty stream")
		}
	case <-time.After(time.Second):
		t.Fatal("Receive on the returned channel blocked; expected a closed channel")
	}
	count := 0
	for range stream {
		count++
	}
	if count != 0 {
		t.Errorf("Expected range over empty stream to yield 0 chunks, got %d", count)
	}
}

// TestApplyRawCaptureSignals_RunsAfterPassthroughClear pins the ordering between
// clearAnthropicPassthroughForNonNativeProvider and applyRawCaptureSignals. The derivation reads
// the very override keys the clear drops, so deriving first leaves a converted provider response
// captured and unstripped on a request that no longer passes anything through.
func TestApplyRawCaptureSignals_RunsAfterPassthroughClear(t *testing.T) {
	// Provider config asks for nothing: any capture must come from the passthrough override.
	config := &schemas.ProviderConfig{}

	tests := []struct {
		name            string
		model           string
		wantCaptureResp bool
	}{
		{"non-native model drops the override", "gpt-5-6-luna", false},
		{"claude model keeps the override", "claude-sonnet-4-20250514", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
			ctx.SetValue(schemas.BifrostContextKeyPassthroughOverridesPresent, true)
			ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)

			clearAnthropicPassthroughForNonNativeProvider(ctx, schemas.BedrockMantle, tt.model)
			applyRawCaptureSignals(ctx, config)

			captureResp, _ := ctx.Value(schemas.BifrostContextKeyCaptureRawResponse).(bool)
			if captureResp != tt.wantCaptureResp {
				t.Errorf("CaptureRawResponse = %v, want %v", captureResp, tt.wantCaptureResp)
			}
			// Nothing is stored, so the client-strip flag stays off either way.
			if dropResp, _ := ctx.Value(schemas.BifrostContextKeyDropRawResponseFromClient).(bool); dropResp {
				t.Error("DropRawResponseFromClient = true, want false (store is off)")
			}
		})
	}
}

// TestApplyProviderProxySignal_RewritesPerAttempt pins that each attempt publishes its
// own provider's proxy, so a fallback from a proxied provider (Vertex behind a corporate
// proxy) to a directly reachable one (Bedrock over a VPC endpoint) does not send the
// fallback's URL fetches through the first provider's proxy.
func TestApplyProviderProxySignal_RewritesPerAttempt(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	vertexProxy := &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://10.0.0.9:3128")}

	applyProviderProxySignal(ctx, &schemas.ProviderConfig{ProxyConfig: vertexProxy})
	if got, _ := ctx.Value(schemas.BifrostContextKeyProviderProxyConfig).(*schemas.ProxyConfig); got != vertexProxy {
		t.Fatalf("primary attempt: proxy = %v, want the Vertex proxy", got)
	}

	applyProviderProxySignal(ctx, &schemas.ProviderConfig{})
	if got, _ := ctx.Value(schemas.BifrostContextKeyProviderProxyConfig).(*schemas.ProxyConfig); got != nil {
		t.Fatalf("fallback attempt: proxy = %v, want nil (the fallback has no proxy)", got)
	}

	ctx.BlockRestrictedWrites()
	ctx.SetValue(schemas.BifrostContextKeyProviderProxyConfig, vertexProxy)
	ctx.UnblockRestrictedWrites()
	if got, _ := ctx.Value(schemas.BifrostContextKeyProviderProxyConfig).(*schemas.ProxyConfig); got != nil {
		t.Fatal("a plugin write must not be able to redirect provider fetches through another proxy")
	}
}

// https://github.com/maximhq/bifrost/issues/6966: prepareFallbackRequest enumerates sub-request
// types by hand, and the shallow copy shares any pointer it does not re-target with the
// original request. A type it forgets therefore keeps the primary provider and model, so the
// "fallback" attempt is routed back to the primary while RoutingInfo reports it as a fallback.
// Deriving the cases from the schema (every sub-request that declares Fallbacks) pins every
// fallback-capable type today and fails loudly for any type added later without an arm.
func TestPrepareFallbackRequestRetargetsEveryFallbackCapableType(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.AddProvider(schemas.Azure, 1, 1)
	bifrost := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}
	fallback := schemas.Fallback{Provider: schemas.Azure, Model: "fallback-model"}

	reqType := reflect.TypeOf(schemas.BifrostRequest{})
	cases := 0
	for i := 0; i < reqType.NumField(); i++ {
		field := reqType.Field(i)
		if field.Type.Kind() != reflect.Ptr || field.Type.Elem().Kind() != reflect.Struct {
			continue
		}
		if _, ok := field.Type.Elem().FieldByName("Fallbacks"); !ok {
			continue
		}
		cases++
		t.Run(field.Name, func(t *testing.T) {
			sub := reflect.New(field.Type.Elem())
			sub.Elem().FieldByName("Provider").SetString(string(schemas.OpenAI))
			sub.Elem().FieldByName("Model").SetString("primary-model")
			req := &schemas.BifrostRequest{}
			reflect.ValueOf(req).Elem().Field(i).Set(sub)

			got := bifrost.prepareFallbackRequest(req, fallback)
			if got == nil {
				t.Fatal("prepareFallbackRequest returned nil for a configured fallback provider")
			}
			provider, model, _ := got.GetRequestFields()
			if provider != fallback.Provider || model != fallback.Model {
				t.Errorf("fallback request targets %s/%s, want %s/%s (the attempt would be routed back to the primary)", provider, model, fallback.Provider, fallback.Model)
			}
			origProvider, origModel, _ := req.GetRequestFields()
			if origProvider != schemas.OpenAI || origModel != "primary-model" {
				t.Errorf("original request was mutated to %s/%s", origProvider, origModel)
			}
		})
	}
	if cases == 0 {
		t.Fatal("no fallback-capable sub-request types found on BifrostRequest; the reflection walk is broken")
	}
}

// TestShutdown_RetentionNoGoroutineLeak is the core-side retention assertion.
//
// Bifrost runs a worker pool per provider, and a production profile showed 5120 of those
// goroutines on a single pod. Each holds channel references and, while serving, the
// request-scoped state that flows through them. A Shutdown that returns while workers are
// still parked leaks the whole pool plus everything it can still reach, and nothing in the
// suite asserted otherwise: the existing tests all `defer client.Shutdown()` and never
// check it did anything.
//
// This is the same class as the client-disconnect watcher leak that pinned roughly 2.0 GB
// of a 2.66 GB heap while GC ran perfectly, because every byte of it was reachable.
func TestShutdown_RetentionNoGoroutineLeak(t *testing.T) {
	memtest.AssertNoGoroutineLeak(t, func() {
		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.ModelProvider("custom-openai-shutdown"), 1, 1, "http://127.0.0.1:1")
		account.SetKeysForProvider(schemas.ModelProvider("custom-openai-shutdown"), []schemas.Key{
			{
				ID:     "test-key-shutdown",
				Value:  *schemas.NewSecretVar("sk-test-shutdown"),
				Models: schemas.WhiteList{"*"},
				Weight: 100,
			},
		})
		account.SetCustomProviderConfig(schemas.ModelProvider("custom-openai-shutdown"), &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
		})

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		client, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Error initializing Bifrost: %v", err)
		}
		client.Shutdown()
	})
}

// TestShutdown_RetentionClientReleased complements TestShutdown_RetentionNoGoroutineLeak.
//
// A goroutine count returning to baseline proves nothing is still running; it does not
// prove nothing still holds a reference. A package-level registry, a pool entry or a
// closure captured somewhere can keep the whole client (and its provider queues) alive
// with zero goroutines running. That is precisely the shape that made a production heap
// unreclaimable: not busy, just reachable.
//
// The weak pointer asserts unreachability directly rather than inferring it from a number.
func TestShutdown_RetentionClientReleased(t *testing.T) {
	memtest.AssertReleased(t, "the Bifrost client after Shutdown", func() *Bifrost {
		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.ModelProvider("custom-openai-release"), 1, 1, "http://127.0.0.1:1")
		account.SetKeysForProvider(schemas.ModelProvider("custom-openai-release"), []schemas.Key{
			{
				ID:     "test-key-release",
				Value:  *schemas.NewSecretVar("sk-test-release"),
				Models: schemas.WhiteList{"*"},
				Weight: 100,
			},
		})
		account.SetCustomProviderConfig(schemas.ModelProvider("custom-openai-release"), &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
		})

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		client, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Error initializing Bifrost: %v", err)
		}
		client.Shutdown()
		return client
	})
}

// patternOf reads properties.p.pattern from a tool schema without a JSON round trip.
func patternOf(t *testing.T, params *schemas.ToolFunctionParameters) string {
	t.Helper()
	v, ok := params.Properties.Get("p")
	if !ok {
		t.Fatal("property p missing")
	}
	pm, ok := v.(*schemas.OrderedMap)
	if !ok {
		t.Fatalf("property p is %T, want *OrderedMap", v)
	}
	pat, _ := pm.Get("pattern")
	s, _ := pat.(string)
	return s
}

func lookaroundToolParams() *schemas.ToolFunctionParameters {
	prop := schemas.NewOrderedMap()
	prop.Set("type", "string")
	prop.Set("pattern", `^(?!\.\.?$)[^\0]{1,200}$`)
	props := schemas.NewOrderedMap()
	props.Set("p", prop)
	return &schemas.ToolFunctionParameters{Type: "object", Properties: props}
}

// Only Moonshot and DeepSeek models get their tool-schema patterns rewritten.
// Every other model, on every provider, must reach the wire byte-identical, and
// the no-op path must hand back the caller's request itself so nothing is copied.
func TestToolSchemaPatternRewriteAppliesOnlyToMoonshotAndDeepSeek(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	cases := []struct {
		model     string
		rewritten bool
	}{
		{"gpt-4o-mini", false},
		{"claude-haiku-4-5", false},
		{"global.anthropic.claude-sonnet-5", false},
		{"gemini-3.6-flash", false},
		{"mistral-large-latest", false},
		{"kimina-prover", false}, // contains "kimi" but is not a Moonshot model
		{"global.moonshotai.kimi-k3", true},
		{"kimi-k3", true},
		{"moonshotai/kimi-k2-instruct", true},
		{"deepseek-chat", true},
		{"deepseek-v4.1-flash", true},
		{"us.deepseek.r1-v1:0", true},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			req := &schemas.BifrostResponsesRequest{
				Model: tc.model,
				Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
					Type:                  schemas.ResponsesToolTypeFunction,
					Name:                  schemas.Ptr("probe"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: lookaroundToolParams()},
				}}},
			}
			out := normalizeResponsesToolSchemas(ctx, req)
			got := patternOf(t, out.Params.Tools[0].ResponsesToolFunction.Parameters)
			if !tc.rewritten {
				if out != req {
					t.Fatal("a model outside the relaxed set must get its own request back, not a copy")
				}
				if !strings.Contains(got, `\0`) || !strings.Contains(got, `(?!`) {
					t.Fatalf("pattern was rewritten for a model outside the relaxed set: %q", got)
				}
				return
			}
			if strings.Contains(got, `\0`) || strings.Contains(got, `(?!`) || !strings.Contains(got, `\x00`) {
				t.Fatalf("pattern not fully rewritten for a relaxed-set model: %q", got)
			}
			if orig := patternOf(t, req.Params.Tools[0].ResponsesToolFunction.Parameters); !strings.Contains(orig, `\0`) {
				t.Fatalf("caller's request was mutated in place: %q", orig)
			}
		})
	}
}

// TestSetFallbackPinnedAPIKeyID_SurvivesBlockedWrites covers the streaming race, where a prior attempt's async post-hooks hold blockRestrictedWrites.
func TestSetFallbackPinnedAPIKeyID_SurvivesBlockedWrites(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.BlockRestrictedWrites()
	defer ctx.UnblockRestrictedWrites()

	ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "dropped")
	if pin, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); pin != "" {
		t.Fatalf("SetValue on a reserved key should have been dropped, got %q", pin)
	}

	ctx.SetFallbackPinnedAPIKeyID("fallback-key")
	if pin, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); pin != "fallback-key" {
		t.Fatalf("fallback pin did not land: got %q", pin)
	}
}

// TestClearCtxForFallback_ClearsPreviousFallbackPin keeps one fallback's pinned key from leaking into the next attempt.
func TestClearCtxForFallback_ClearsPreviousFallbackPin(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetFallbackPinnedAPIKeyID("fallback-1-key")

	clearCtxForFallback(ctx)

	if pin, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); ok && pin != "" {
		t.Fatalf("fallback pin survived clearCtxForFallback: %q", pin)
	}
}

// TestStreamFallbackUsesPinnedKey proves the pin, not the weighted selector, picks which credential goes upstream (the unpinned key carries all the weight).
func TestStreamFallbackUsesPinnedKey(t *testing.T) {
	primary := httptest.NewServer(sseHandler(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	defer primary.Close()

	var gotAPIKey atomic.Value
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey.Store(r.Header.Get("x-api-key"))
		anthropicMessagesHandler()(w, r)
	}))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "unpinned-key", Value: *schemas.NewSecretVar("sk-unpinned"), Models: schemas.WhiteList{"*"}, Weight: 100},
		{ID: "pinned-key", Value: *schemas.NewSecretVar("sk-pinned"), Models: schemas.WhiteList{"*"}, Weight: 0},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: []schemas.Fallback{{
			Provider: schemas.Anthropic,
			Model:    "claude-3-5-haiku-20241022",
			KeyID:    "pinned-key",
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("pinned fallback stream failed: %s", bifrostErr.Error.Message)
	}
	if _, errs := drainChatStream(stream); len(errs) > 0 {
		t.Fatalf("pinned fallback stream emitted error chunks: %v", errs)
	}

	if got, _ := gotAPIKey.Load().(string); got != "sk-pinned" {
		t.Fatalf("fallback used api key %q, want %q", got, "sk-pinned")
	}
}

// TestFallbackWithUnknownPinnedKeyIsSkipped covers a pin that names no key in the provider's pool: the attempt is skipped, not load-balanced.
func TestFallbackWithUnknownPinnedKeyIsSkipped(t *testing.T) {
	primary := httptest.NewServer(sseHandler(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	defer primary.Close()

	var hits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		anthropicMessagesHandler()(w, r)
	}))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "anthropic-key", Value: *schemas.NewSecretVar("sk-anthropic"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022", KeyID: "not-in-this-pool"},
			{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("chain did not recover after the mis-pinned fallback: %s", bifrostErr.Error.Message)
	}
	if _, errs := drainChatStream(stream); len(errs) > 0 {
		t.Fatalf("stream emitted error chunks: %v", errs)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("fallback server hits = %d, want 1 (the mis-pinned attempt must not reach upstream)", got)
	}
}

// TestFallbackUsesPinnedKey is the non-streaming twin of TestStreamFallbackUsesPinnedKey; the two orchestrator loops are independent copies.
func TestFallbackUsesPinnedKey(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
	}))
	defer primary.Close()

	var gotAPIKey atomic.Value
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey.Store(r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022",`+
			`"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
		{ID: "unpinned-key", Value: *schemas.NewSecretVar("sk-unpinned"), Models: schemas.WhiteList{"*"}, Weight: 100},
		{ID: "pinned-key", Value: *schemas.NewSecretVar("sk-pinned"), Models: schemas.WhiteList{"*"}, Weight: 0},
	})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: []schemas.Fallback{{
			Provider: schemas.Anthropic,
			Model:    "claude-3-5-haiku-20241022",
			KeyID:    "pinned-key",
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("pinned fallback failed: %s", bifrostErr.Error.Message)
	}

	if got, _ := gotAPIKey.Load().(string); got != "sk-pinned" {
		t.Fatalf("fallback used api key %q, want %q", got, "sk-pinned")
	}
}

// openAICompatFallbackServer answers chat completions in OpenAI shape, JSON or SSE, and records the
// bearer token of every request so a test can tell which provider key served each attempt.
func openAICompatFallbackServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	stream := sseHandler(`{"id":"chatcmpl-fb","object":"chat.completion.chunk","created":1,"model":"fb-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-fb","object":"chat.completion.chunk","created":1,"model":"fb-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			stream(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-fb","object":"chat.completion","created":1,"model":"fb-model",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// failingAnthropicPrimary always answers 429 so every request moves on to its fallbacks.
func failingAnthropicPrimary(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// addFallbackProvider registers a standard OpenAI provider or a custom OpenAI-compatible one at
// baseURL, with an unpinned key that normal selection always picks and a zero-weight pinned key
// that only a pin can reach.
func addFallbackProvider(account *MockAccount, provider schemas.ModelProvider, custom bool, baseURL string) {
	account.AddProviderWithBaseURL(provider, 1, 1, baseURL)
	account.configs[provider].NetworkConfig.MaxRetries = 0
	if custom {
		account.SetCustomProviderConfig(provider, &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI})
	}
	account.SetKeysForProvider(provider, []schemas.Key{
		{ID: "unpinned-key", Value: *schemas.NewSecretVar("sk-unpinned"), Models: schemas.WhiteList{"*"}, Weight: 100},
		{ID: "pinned-key", Value: *schemas.NewSecretVar("sk-pinned"), Models: schemas.WhiteList{"*"}, Weight: 0},
	})
}

// runFallbackChat sends one chat request, JSON or streaming, and fails the test if it does not
// succeed end to end.
func runFallbackChat(t *testing.T, client *Bifrost, stream bool, primary schemas.ModelProvider, fallbacks []schemas.Fallback) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
	req := &schemas.BifrostChatRequest{
		Provider: primary,
		Model:    "claude-3-5-haiku-20241022",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Fallbacks: fallbacks,
	}
	if !stream {
		if _, bifrostErr := client.ChatCompletionRequest(ctx, req); bifrostErr != nil {
			t.Fatalf("request failed: %s", bifrostErr.Error.Message)
		}
		return
	}
	ch, bifrostErr := client.ChatCompletionStreamRequest(ctx, req)
	if bifrostErr != nil {
		t.Fatalf("stream failed: %s", bifrostErr.Error.Message)
	}
	if _, errs := drainChatStream(ch); len(errs) > 0 {
		t.Fatalf("stream emitted error chunks: %v", errs)
	}
}

// TestFallbackKeyPinMatrix covers a routing fallback's key pin on both orchestrator loops, for a
// standard provider and a custom OpenAI-compatible one: a pin reaches its key, an unpinned
// fallback keeps normal selection, and a pin to a key the provider does not have skips only that
// attempt.
func TestFallbackKeyPinMatrix(t *testing.T) {
	providers := []struct {
		name     string
		provider schemas.ModelProvider
		custom   bool
	}{
		{name: "standard", provider: schemas.OpenAI},
		{name: "custom", provider: schemas.ModelProvider("custom-fallback-pin"), custom: true},
	}
	scenarios := []struct {
		name      string
		fallbacks func(p schemas.ModelProvider) []schemas.Fallback
		wantKeys  []string // bearer tokens the fallback upstream must see, in order
	}{
		{
			name: "pinned",
			fallbacks: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "fb-model", KeyID: "pinned-key"}}
			},
			wantKeys: []string{"sk-pinned"},
		},
		{
			name: "unpinned",
			fallbacks: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "fb-model"}}
			},
			wantKeys: []string{"sk-unpinned"},
		},
		{
			name: "pin to a missing key is skipped, next fallback serves",
			fallbacks: func(p schemas.ModelProvider) []schemas.Fallback {
				return []schemas.Fallback{{Provider: p, Model: "fb-model", KeyID: "not-in-this-pool"}, {Provider: p, Model: "fb-model"}}
			},
			wantKeys: []string{"sk-unpinned"},
		},
		{
			name: "pin does not leak into the next fallback",
			fallbacks: func(p schemas.ModelProvider) []schemas.Fallback {
				// The first attempt's model is refused by both keys, so it fails before upstream and
				// the second, unpinned attempt must not inherit the first attempt's pin.
				return []schemas.Fallback{{Provider: p, Model: "blocked-model", KeyID: "pinned-key"}, {Provider: p, Model: "fb-model"}}
			},
			wantKeys: []string{"sk-unpinned"},
		},
	}
	for _, prov := range providers {
		for _, sc := range scenarios {
			for _, stream := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/stream=%t", prov.name, sc.name, stream)
				t.Run(name, func(t *testing.T) {
					primary := failingAnthropicPrimary(t)
					fallback, seen := openAICompatFallbackServer(t)

					account := NewMockAccount()
					account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, primary.URL)
					account.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
					account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
						{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
					})
					addFallbackProvider(account, prov.provider, prov.custom, fallback.URL)
					keys := account.keys[prov.provider]
					for i := range keys {
						keys[i].BlacklistedModels = schemas.BlackList{"blocked-model"}
					}
					client := newStreamTestClient(t, account)

					runFallbackChat(t, client, stream, schemas.Anthropic, sc.fallbacks(prov.provider))
					if got := seen(); !reflect.DeepEqual(got, sc.wantKeys) {
						t.Fatalf("fallback upstream saw keys %v, want %v", got, sc.wantKeys)
					}
				})
			}
		}
	}
}

// allowedKeysAccount mirrors the transport account: it offers only the keys a virtual key allows,
// read from the per-attempt governance context value.
type allowedKeysAccount struct {
	*MockAccount
}

func (a *allowedKeysAccount) GetKeysForProvider(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
	keys, err := a.MockAccount.GetKeysForProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	allowed, ok := ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys).([]string)
	if !ok {
		return keys, nil
	}
	filtered := make([]schemas.Key, 0, len(keys))
	for _, key := range keys {
		if slices.Contains(allowed, key.ID) {
			filtered = append(filtered, key)
		}
	}
	return filtered, nil
}

// allowedKeysPlugin stands in for governance, which publishes a virtual key's allowed keys in
// PreLLMHook on every attempt, after core has cleared the previous attempt's value.
type allowedKeysPlugin struct {
	allowed map[schemas.ModelProvider][]string
}

func (p *allowedKeysPlugin) GetName() string { return "allowed-keys" }
func (p *allowedKeysPlugin) Cleanup() error  { return nil }
func (p *allowedKeysPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p *allowedKeysPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	provider, _, _ := req.GetRequestFields()
	if ids, ok := p.allowed[provider]; ok {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, ids)
	}
	return req, nil, nil
}
func (p *allowedKeysPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// TestFallbackPinOutsideAllowedKeysIsRefused pins that a routing fallback's key pin cannot reach a
// key the virtual key does not allow: the pinned attempt is skipped without touching upstream and
// the next fallback serves on an allowed key, for standard and custom providers on both loops.
func TestFallbackPinOutsideAllowedKeysIsRefused(t *testing.T) {
	providers := []struct {
		name     string
		provider schemas.ModelProvider
		custom   bool
	}{
		{name: "standard", provider: schemas.OpenAI},
		{name: "custom", provider: schemas.ModelProvider("custom-fallback-allowed"), custom: true},
	}
	for _, prov := range providers {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", prov.name, stream), func(t *testing.T) {
				primary := failingAnthropicPrimary(t)
				fallback, seen := openAICompatFallbackServer(t)

				mock := NewMockAccount()
				mock.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, primary.URL)
				mock.configs[schemas.Anthropic].NetworkConfig.MaxRetries = 0
				mock.SetKeysForProvider(schemas.Anthropic, []schemas.Key{
					{ID: "primary-key", Value: *schemas.NewSecretVar("sk-primary"), Models: schemas.WhiteList{"*"}, Weight: 100},
				})
				addFallbackProvider(mock, prov.provider, prov.custom, fallback.URL)

				client, err := Init(context.Background(), schemas.BifrostConfig{
					Account:    &allowedKeysAccount{MockAccount: mock},
					Logger:     NewDefaultLogger(schemas.LogLevelError),
					LLMPlugins: []schemas.LLMPlugin{&allowedKeysPlugin{allowed: map[schemas.ModelProvider][]string{prov.provider: {"unpinned-key"}}}},
				})
				if err != nil {
					t.Fatalf("failed to initialize bifrost: %v", err)
				}
				t.Cleanup(client.Shutdown)

				runFallbackChat(t, client, stream, schemas.Anthropic, []schemas.Fallback{
					{Provider: prov.provider, Model: "fb-model", KeyID: "pinned-key"},
					{Provider: prov.provider, Model: "fb-model"},
				})
				if got := seen(); !reflect.DeepEqual(got, []string{"sk-unpinned"}) {
					t.Fatalf("fallback upstream saw keys %v, want only the allowed key [sk-unpinned]", got)
				}
			})
		}
	}
}

// TestSDKFidelityDecisionRequestNullStateReachesProvider pins #7599 at the core
// entrypoint: a decision request whose state is null (an SDK-valid EntryType)
// must be dispatched to the provider as {"state": null} instead of being
// rejected before dispatch.
func TestSDKFidelityDecisionRequestNullStateReachesProvider(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		if r.URL.Path != "/v1/systemone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.25}},"usage":{"input_tokens":3,"output_tokens":0}}`))
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.Typesafe, 1, 1, server.URL)
	account.SetKeysForProvider(schemas.Typesafe, []schemas.Key{{
		ID:     "test-key-typesafe",
		Value:  *schemas.NewSecretVar("sk-test-typesafe"),
		Models: schemas.WhiteList{"*"},
		Weight: 100,
	}})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}
	defer client.Shutdown()

	resp, bifrostErr := client.DecisionRequest(ctx, &schemas.BifrostDecisionRequest{
		Provider: schemas.Typesafe,
		Model:    "jev-1.13.0",
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("q"), Instructions: schemas.NewDecisionText("Evaluate this state.")},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("null state must reach the provider, got error: %v", bifrostErr)
	}
	if resp == nil || len(resp.Answers) != 1 || resp.Answers[0].Probability == nil || *resp.Answers[0].Probability != 0.25 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"state":null`) {
		t.Errorf("provider must receive state null, got bodies %v", bodies)
	}
}

// An attempt that already ran a provider-injected MCP tool is not retried: a retry would
// restart the injected loop from the original request and run the tool's side effects,
// and bill the finished turns, a second time.
func TestExecuteRequestWithRetries_StopsAfterInjectedToolRan(t *testing.T) {
	config := createTestConfig(2, time.Millisecond, 10*time.Millisecond)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	logger := NewDefaultLogger(schemas.LogLevelError)

	callCount := 0
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		ctx.SetValue(schemas.BifrostContextKeyInjectedToolsExecuted, true)
		return "", createBifrostError("service unavailable", Ptr(503), nil, false)
	}
	_, err := executeRequestWithRetries(ctx, config, handler, nil, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", nil, logger)
	if err == nil {
		t.Fatal("expected the 503 to come back")
	}
	if callCount != 1 {
		t.Fatalf("a retryable error after an injected tool ran ends the request: %d attempts", callCount)
	}
}

// Routing scenarios from the routing test plan, run in-process: real core (key selection, retries,
// fallbacks, session affinity, the caller-pin step) against one OpenAI-compatible upstream that
// answers by bearer token. A test plugin stands in for whatever picked the route (a routing rule, a
// virtual key's weighted pick), including the key a rule's target pins. Each test names the plan
// rows it covers.

const (
	provA schemas.ModelProvider = "prov-a"
	provB schemas.ModelProvider = "prov-b"
	provC schemas.ModelProvider = "prov-c"
)

// scenarioUpstream is the one upstream every provider of a scenario points at. It answers by the
// bearer token a request carries, so each key can be made to fail on its own, and it counts the
// requests by token and by token and model.
type scenarioUpstream struct {
	*httptest.Server
	mu        sync.Mutex
	status    map[string]int           // by "token" or "token|model"; 0 answers 200
	remaining map[string]int           // by the status's match: how many more requests answer it before 200
	replies   map[string]scenarioReply // by "token" or "token|model"; overrides status
	hits      map[string]int           // by "token" and by "token|model"
	arrivals  map[string][]time.Time   // by "token": when each request with it arrived
}

// scenarioReply is an answer spelled out in full, for the failure classes the default bodies do not
// produce: a status with its own body and headers, after an optional stall. midStream answers a
// streamed request with one content chunk and then an error event, the failure after content that
// a stream cannot fall back from.
type scenarioReply struct {
	status    int
	body      string
	header    map[string]string
	stall     time.Duration
	midStream bool
}

// newScenarioUpstream starts an upstream that serves every request until told otherwise.
func newScenarioUpstream(t *testing.T) *scenarioUpstream {
	t.Helper()
	u := &scenarioUpstream{status: map[string]int{}, remaining: map[string]int{}, replies: map[string]scenarioReply{}, hits: map[string]int{}, arrivals: map[string][]time.Time{}}
	u.Server = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.Close)
	return u
}

// serveStream answers a streamed chat completion that succeeds: two content chunks, a final chunk
// with usage, then [DONE], so a caller can tell a stream that ran to its end from one cut short.
func serveStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, payload := range []string{
		fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant","content":"o"}}]}`, model),
		fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{"content":"k"}}]}`, model),
		fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, model),
	} {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// serveMidStreamFailure answers a streamed chat completion with one content chunk and then an
// error event, as a provider that fails after it has started answering does.
func serveMidStreamFailure(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}`, model))
	if flusher != nil {
		flusher.Flush()
	}
	fmt.Fprint(w, "data: {\"error\":{\"message\":\"The server had an error while processing your request.\",\"type\":\"server_error\"}}\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// serve answers one chat completion: the status set for the token and model, else for the token,
// else 200, with an error body shaped like OpenAI's for the failure classes core reads. A streamed
// request that is answered 200 gets an SSE stream.
func (u *scenarioUpstream) serve(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	u.mu.Lock()
	u.hits[token]++
	u.hits[token+"|"+body.Model]++
	u.arrivals[token] = append(u.arrivals[token], time.Now())
	match := token + "|" + body.Model
	status, ok := u.status[match]
	if !ok {
		match = token
		status = u.status[token]
	}
	if left, limited := u.remaining[match]; limited && status != 0 {
		if left <= 1 {
			delete(u.status, match)
			delete(u.remaining, match)
		} else {
			u.remaining[match] = left - 1
		}
	}
	reply, replied := u.replies[token+"|"+body.Model]
	if !replied {
		reply, replied = u.replies[token]
	}
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if replied {
		if reply.stall > 0 {
			select {
			case <-time.After(reply.stall):
			case <-r.Context().Done():
				return
			}
		}
		for name, value := range reply.header {
			w.Header().Set(name, value)
		}
		if reply.midStream && body.Stream {
			serveMidStreamFailure(w, body.Model)
			return
		}
		if reply.status == 0 || reply.status == http.StatusOK {
			if body.Stream {
				serveStream(w, body.Model)
				return
			}
			fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, body.Model)
			return
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
		return
	}
	switch status {
	case 0, http.StatusOK:
		if body.Stream {
			serveStream(w, body.Model)
			return
		}
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, body.Model)
	case http.StatusUnauthorized:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`))
	case http.StatusTooManyRequests:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`))
	default:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"The server had an error while processing your request","type":"server_error"}}`))
	}
}

// answer sets the status the upstream answers for a token, or for "token|model".
func (u *scenarioUpstream) answer(match string, status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status[match] = status
	delete(u.remaining, match)
}

// answerFirst sets the status the upstream answers for a token, or for "token|model", for the next n
// requests only; after them it answers 200 again.
func (u *scenarioUpstream) answerFirst(match string, status, n int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status[match] = status
	u.remaining[match] = n
}

// reply sets the full answer for a token, or for "token|model"; heal removes it.
func (u *scenarioUpstream) reply(match string, r scenarioReply) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.replies[match] = r
}

// heal makes a token, or "token|model", answer 200 again, whatever it was set to answer.
func (u *scenarioUpstream) heal(match string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.replies, match)
	delete(u.status, match)
	delete(u.remaining, match)
}

// count returns how many requests reached the upstream with a token, or with "token|model".
func (u *scenarioUpstream) count(match string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[match]
}

// clearHits forgets the counts, so a turn's hits can be read on their own.
func (u *scenarioUpstream) clearHits() {
	u.mu.Lock()
	defer u.mu.Unlock()
	clear(u.hits)
	clear(u.arrivals)
}

// arrivalsOf returns when each request carrying token reached the upstream, in order.
func (u *scenarioUpstream) arrivalsOf(token string) []time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.arrivals[token])
}

// scenarioKey is a key whose id and name are name and whose value, the upstream's token, is "sk-"+name.
func scenarioKey(name string, weight float64) schemas.Key {
	return schemas.Key{ID: name, Name: name, Value: *schemas.NewSecretVar("sk-" + name), Models: schemas.WhiteList{"*"}, Weight: weight}
}

// scenarioAccount registers each provider as an OpenAI-compatible custom provider at the upstream,
// with two retries and a near-zero backoff, and the given keys.
func scenarioAccount(u *scenarioUpstream, keys map[schemas.ModelProvider][]schemas.Key) *MockAccount {
	account := NewMockAccount()
	for provider, providerKeys := range keys {
		addScenarioProvider(account, u, provider, providerKeys)
	}
	return account
}

// addScenarioProvider registers one provider the way scenarioAccount does, so a test can also add a
// provider back after deleting it, as an operator would.
func addScenarioProvider(account *MockAccount, u *scenarioUpstream, provider schemas.ModelProvider, keys []schemas.Key) {
	account.AddProviderWithBaseURL(provider, 4, 256, u.URL)
	account.mu.Lock()
	network := &account.configs[provider].NetworkConfig
	network.MaxRetries = 2
	network.RetryBackoffInitial = time.Millisecond
	network.RetryBackoffMax = 2 * time.Millisecond
	account.mu.Unlock()
	account.SetCustomProviderConfig(provider, &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI})
	account.SetKeysForProvider(provider, keys)
}

// scenarioRouter stands in for a routing rule or a virtual key's weighted pick: a request that names
// no provider gets the chain route returns, and the primary's KeyID is set as the rule's key pin. It
// also records the attempt trail each provider attempt leaves, since a fallback starts a trail of its
// own and the request's context keeps only the last one.
type scenarioRouter struct {
	mu       sync.Mutex
	route    func() (schemas.Fallback, []schemas.Fallback)
	attempts []scenarioAttempt
}

// scenarioAttempt is what one provider attempt of a request left behind: its attempt trail, one
// record per upstream call, and the number_of_retries it reported.
type scenarioAttempt struct {
	trail   []schemas.KeyAttemptRecord
	retries int
}

// awaitAttempts waits up to two seconds for n attempts to be recorded, since an attempt the caller
// stopped waiting for (a cancel, a deadline) reaches the post-hooks after the request returns, then
// returns them like takeAttempts.
func (r *scenarioRouter) awaitAttempts(t *testing.T, n int) []scenarioAttempt {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		r.mu.Lock()
		got := len(r.attempts)
		r.mu.Unlock()
		if got >= n {
			break
		}
	}
	attempts := r.takeAttempts()
	if len(attempts) < n {
		t.Fatalf("recorded %d provider attempts, want %d", len(attempts), n)
	}
	return attempts
}

// takeAttempts returns the attempts recorded since the last call and forgets them.
func (r *scenarioRouter) takeAttempts() []scenarioAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempts := r.attempts
	r.attempts = nil
	return attempts
}

// fixedRoute always routes to primary, with the given fallbacks.
func fixedRoute(primary schemas.Fallback, fallbacks ...schemas.Fallback) func() (schemas.Fallback, []schemas.Fallback) {
	return func() (schemas.Fallback, []schemas.Fallback) { return primary, fallbacks }
}

// setRoute replaces the router's route, as an operator editing the rule would.
func (r *scenarioRouter) setRoute(route func() (schemas.Fallback, []schemas.Fallback)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.route = route
}

// GetName implements schemas.LLMPlugin.
func (r *scenarioRouter) GetName() string { return "scenario-router" }

// Cleanup implements schemas.LLMPlugin.
func (r *scenarioRouter) Cleanup() error { return nil }

// PreRequestHook routes a request that names no provider, as the routing plugin does.
func (r *scenarioRouter) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if provider, _, _ := req.GetRequestFields(); provider != "" {
		return nil
	}
	r.mu.Lock()
	route := r.route
	r.mu.Unlock()
	primary, fallbacks := route()
	req.SetProvider(primary.Provider)
	req.SetModel(primary.Model)
	req.SetFallbacks(fallbacks)
	if primary.KeyID != "" {
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, primary.KeyID)
	}
	return nil
}

// PreLLMHook implements schemas.LLMPlugin.
func (r *scenarioRouter) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook records the attempt trail and retry count the provider attempt left on the context.
func (r *scenarioRouter) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	trail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	retries, _ := ctx.Value(schemas.BifrostContextKeyNumberOfRetries).(int)
	r.mu.Lock()
	r.attempts = append(r.attempts, scenarioAttempt{trail: slices.Clone(trail), retries: retries})
	r.mu.Unlock()
	return resp, bifrostErr, nil
}

// scenarioClient starts core over the account, with the router when given and session state in kv
// when given.
func scenarioClient(t *testing.T, account *MockAccount, router *scenarioRouter, kv schemas.KVStore) *Bifrost {
	t.Helper()
	config := schemas.BifrostConfig{Account: account, Logger: NewDefaultLogger(schemas.LogLevelError), KVStore: kv}
	if router != nil {
		config.LLMPlugins = []schemas.LLMPlugin{router}
	}
	client, err := Init(context.Background(), config)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(client.Shutdown)
	return client
}

// scenarioChat sends one non-streaming chat request and returns the provider that served it.
func scenarioChat(client *Bifrost, ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, fallbacks ...schemas.Fallback) (schemas.ModelProvider, *schemas.BifrostError) {
	resp, err := client.ChatCompletionRequest(ctx, scenarioRequest(provider, model, fallbacks))
	if err != nil {
		return "", err
	}
	return resp.ExtraFields.RoutingInfo.Provider, nil
}

// scenarioChatInfo sends one non-streaming chat request and returns the routing info of the attempt
// that answered it, the one that served or, when every attempt failed, the one whose error came back.
func scenarioChatInfo(client *Bifrost, ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, fallbacks ...schemas.Fallback) (schemas.RoutingInfo, *schemas.BifrostError) {
	resp, err := client.ChatCompletionRequest(ctx, scenarioRequest(provider, model, fallbacks))
	if err != nil {
		return err.ExtraFields.RoutingInfo, err
	}
	return resp.ExtraFields.RoutingInfo, nil
}

// scenarioRequest is the one-message chat request every scenario sends.
func scenarioRequest(provider schemas.ModelProvider, model string, fallbacks []schemas.Fallback) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider:  provider,
		Model:     model,
		Fallbacks: fallbacks,
		Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}
}

// requireHits fails the test unless each token or "token|model" reached the upstream as often as given.
func requireHits(t *testing.T, u *scenarioUpstream, want map[string]int) {
	t.Helper()
	for match, n := range want {
		if got := u.count(match); got != n {
			t.Errorf("%s reached the upstream %d times, want %d", match, got, n)
		}
	}
}

// requireServed fails the test when the request failed or was served by another provider.
func requireServed(t *testing.T, got schemas.ModelProvider, err *schemas.BifrostError, want schemas.ModelProvider) {
	t.Helper()
	if err != nil {
		t.Fatalf("request failed: %s", err.GetErrorString())
	}
	if got != want {
		t.Fatalf("served by %s, want %s", got, want)
	}
}

// pinnedRule is the rule every explicit-key scenario uses: target A pinned to a1, fallback B pinned to b1.
func pinnedRule() func() (schemas.Fallback, []schemas.Fallback) {
	return fixedRoute(schemas.Fallback{Provider: provA, Model: "m", KeyID: "a1"}, schemas.Fallback{Provider: provB, Model: "m", KeyID: "b1"})
}

// pinnedRuleAccount gives A three keys and B two, all at the same weight, so only a pin explains a
// request landing on one of them every time.
func pinnedRuleAccount(u *scenarioUpstream) *MockAccount {
	return scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1), scenarioKey("a3", 1)},
		provB: {scenarioKey("b1", 1), scenarioKey("b2", 1)},
	})
}

// A rule target pinned to a key never uses another key of its provider, whatever the pinned key
// answers; the request moves to the fallback, uses only the fallback's pinned key, and the fresh
// session binds to the fallback and its key. A transient error and a rate limit are retried on the
// pin, a rejected key is not (plan rows EK-02, EK-03, EK-04; one test, one subtest per row).
func TestScenarioPinnedTargetFailsOverToItsPinnedFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		wantA1 int
	}{
		{name: "EK-02 transient 500", status: http.StatusInternalServerError, wantA1: 3},
		{name: "EK-03 rate limit 429", status: http.StatusTooManyRequests, wantA1: 3},
		{name: "EK-04 rejected key 401", status: http.StatusUnauthorized, wantA1: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newScenarioUpstream(t)
			kv := newMockKVStore()
			client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)
			u.answer("sk-a1", tc.status)

			info, err := scenarioChatInfo(client, sessionCtx("s"), "", "m")
			requireServed(t, info.Provider, err, provB)
			if !info.IsFallback || info.PrimaryProvider == nil || *info.PrimaryProvider != provA || info.Key != "b1" {
				t.Fatalf("B should serve on b1 as the fallback of primary A, routing info %+v", info)
			}
			requireHits(t, u, map[string]int{"sk-a1": tc.wantA1, "sk-a2": 0, "sk-a3": 0, "sk-b1": 1, "sk-b2": 0})
			requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")
			requireSessionState(t, kv, "s", SessionStateKindKey, provB, "m", "b1")
		})
	}
}

// With a session, the request the pinned fallback rescued binds the session to B and b1, so the next
// ten turns start there, with B as the primary, A never tried, and a trail line naming the move and
// the pins; when b1 fails, the demoted A entry runs as a fallback on a1 alone, the session moves back,
// the turn after it has A as the primary with the chain as the rule built it, and B@b1 is still its
// fallback (plan rows EK-05, EK-06; EK-02's rescue sets them up).
func TestScenarioSessionFollowsThePinnedFallbackAndBack(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)
	turn := func() (schemas.RoutingInfo, *schemas.BifrostError) {
		u.clearHits()
		return scenarioChatInfo(client, sessionCtx("s"), "", "m")
	}

	u.answer("sk-a1", http.StatusInternalServerError)
	info, err := turn()
	requireServed(t, info.Provider, err, provB)
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-b1": 1})

	// EK-05: a1 still fails; ten turns start on B with b1 as the primary, and A is not tried. Each
	// turn's trail says the session moved the request to B with B's own pin, and that A's pin stays
	// with A for when A is tried as a fallback.
	const moved = "Session moved the request from prov-a to prov-b, which brings the key pinned for it; the key pinned for prov-a applies if prov-a is tried as a fallback"
	u.clearHits()
	for i := range 10 {
		ctx := sessionCtx("s")
		info, err = scenarioChatInfo(client, ctx, "", "m")
		requireServed(t, info.Provider, err, provB)
		if info.IsFallback || info.Key != "b1" {
			t.Fatalf("turn %d should be served by B on b1 as the primary, routing info %+v", i+2, info)
		}
		if logs := routingLogs(ctx); !strings.Contains(logs, moved) {
			t.Fatalf("turn %d should report the move to B and the pins it carries:\n%s", i+2, logs)
		}
	}
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-b1": 10, "sk-b2": 0})

	// EK-06: a1 heals and b1 fails. The demoted A entry runs as a fallback on a1 alone, and the session
	// moves its route and A's key binding to A and a1.
	u.answer("sk-a1", http.StatusOK)
	u.answer("sk-b1", http.StatusInternalServerError)
	info, err = turn()
	requireServed(t, info.Provider, err, provA)
	if !info.IsFallback || info.Key != "a1" {
		t.Fatalf("A should serve on a1 as a fallback, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-b1": 3, "sk-a1": 1, "sk-a2": 0, "sk-a3": 0})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")

	// The next turn's chain is the rule's own, [A@a1, B@b1], so A is the primary and the session
	// moves nothing.
	ctx := sessionCtx("s")
	u.clearHits()
	info, err = scenarioChatInfo(client, ctx, "", "m")
	requireServed(t, info.Provider, err, provA)
	if info.IsFallback || info.Key != "a1" {
		t.Fatalf("A should serve on a1 as the primary, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-a1": 1, "sk-a2": 0, "sk-a3": 0, "sk-b1": 0})
	if logs := routingLogs(ctx); strings.Contains(logs, "Session moved the request") {
		t.Fatalf("with the session back on the rule's primary the chain should be left as the rule built it:\n%s", logs)
	}

	// B@b1 is still the chain's fallback: with a1 failing again and b1 healed, B serves the next turn
	// as fallback 1 on b1 alone, never b2.
	u.heal("sk-b1")
	u.answer("sk-a1", http.StatusInternalServerError)
	info, err = turn()
	requireServed(t, info.Provider, err, provB)
	if !info.IsFallback || info.Key != "b1" || info.PrimaryProvider == nil || *info.PrimaryProvider != provA {
		t.Fatalf("B should serve on b1 as the fallback of primary A, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0, "sk-b1": 1, "sk-b2": 0})
}

// A session bound to B and b1 after a rescue, with both pinned keys failing: every attempt stays on
// its own pin, the turn returns the error of B, the provider the session put first, and the binding
// it followed is dropped; with both healed the next turn follows the rule to A and a1 (plan row EK-07).
func TestScenarioBothPinnedKeysFailUnderABoundSession(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)
	u.answer("sk-a1", http.StatusInternalServerError)
	info, err := scenarioChatInfo(client, sessionCtx("s"), "", "m")
	requireServed(t, info.Provider, err, provB)
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")

	u.answer("sk-b1", http.StatusInternalServerError)
	u.clearHits()
	info, err = scenarioChatInfo(client, sessionCtx("s"), "", "m")
	if err == nil {
		t.Fatal("a turn whose every attempt failed was served")
	}
	if info.Provider != provB {
		t.Fatalf("the failed turn returned the error of %q, want B's, the head of the chain the session reordered", info.Provider)
	}
	requireHits(t, u, map[string]int{"sk-b1": 3, "sk-a1": 3, "sk-a2": 0, "sk-a3": 0, "sk-b2": 0})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "")

	u.heal("sk-a1")
	u.heal("sk-b1")
	u.clearHits()
	info, err = scenarioChatInfo(client, sessionCtx("s"), "", "m")
	requireServed(t, info.Provider, err, provA)
	if info.Key != "a1" {
		t.Fatalf("the turn after should follow the rule to a1, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-a1": 1, "sk-b1": 0})
}

// keyPinClient is a client over A{a1,a2,a3} at equal weights with a router whose target is A, pinned
// to pin when it is not empty, and a session store.
func keyPinClient(t *testing.T, pin string) (*scenarioUpstream, *mockKVStore, *scenarioRouter, *Bifrost) {
	t.Helper()
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	router := &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m", KeyID: pin})}
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1), scenarioKey("a3", 1)},
	}), router, kv)
	return u, kv, router, client
}

// keyPinTurns sends n turns with a fresh context from ctx each time, after forgetting the upstream's
// counts, and fails unless A serves every one.
func keyPinTurns(t *testing.T, u *scenarioUpstream, client *Bifrost, n int, ctx func() *schemas.BifrostContext) {
	t.Helper()
	u.clearHits()
	for range n {
		served, err := scenarioChat(client, ctx(), "", "m")
		requireServed(t, served, err, provA)
	}
}

// A rule's key pin outranks the session's key binding, and a pinned serve does not replace that
// binding: three pinned turns on a1 bind it, three turns after the pin moves to a2 use a2 with the
// binding still a1, and the turn after the pin is removed is back on a1 (plan row EK-12).
func TestScenarioKeyPinEditedUnderABoundSession(t *testing.T) {
	u, kv, router, client := keyPinClient(t, "a1")
	session := func() *schemas.BifrostContext { return sessionCtx("s") }

	keyPinTurns(t, u, client, 3, session)
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0})
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")

	router.setRoute(fixedRoute(schemas.Fallback{Provider: provA, Model: "m", KeyID: "a2"}))
	keyPinTurns(t, u, client, 3, session)
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 3, "sk-a3": 0})
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")

	router.setRoute(fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}))
	keyPinTurns(t, u, client, 1, session)
	requireHits(t, u, map[string]int{"sk-a1": 1, "sk-a2": 0, "sk-a3": 0})
}

// A pin removed from the target while a session is bound: the session keeps the key its pinned turns
// bound for all ten turns after the edit, and a2 and a3 get traffic only from new sessions (plan row
// EK-11; EK-01's pinned turns set it up).
func TestScenarioKeyPinRemovedUnderABoundSession(t *testing.T) {
	u, kv, router, client := keyPinClient(t, "a1")
	keyPinTurns(t, u, client, 3, func() *schemas.BifrostContext { return sessionCtx("s") })
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")

	router.setRoute(fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}))
	keyPinTurns(t, u, client, 10, func() *schemas.BifrostContext { return sessionCtx("s") })
	requireHits(t, u, map[string]int{"sk-a1": 10, "sk-a2": 0, "sk-a3": 0})

	// Sixty new sessions with neither a2 nor a3 among them happens once in about ten billion runs.
	n := 0
	keyPinTurns(t, u, client, 60, func() *schemas.BifrostContext { n++; return sessionCtx(fmt.Sprintf("new-%d", n)) })
	if u.count("sk-a2") == 0 || u.count("sk-a3") == 0 {
		t.Fatalf("new sessions should rotate across a1 to a3: a1=%d a2=%d a3=%d", u.count("sk-a1"), u.count("sk-a2"), u.count("sk-a3"))
	}
}

// A weighted pick with the other provider as a fallback: the first turn of each session follows the
// weights, and every later turn stays where the first was served. Without fallbacks the pick is rolled
// on every turn, so one session sees both providers (plan rows GV-19, RR-30, RR-29; one subtest each).
func TestScenarioSessionsKeepTheirWeightedPick(t *testing.T) {
	a, b := schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"}
	// roll picks A with probability shareA, else B.
	roll := func(shareA float64) schemas.Fallback {
		if rand.Float64() < shareA {
			return a
		}
		return b
	}
	// virtualKeyPick is a virtual key's weighted pick: the other provider rides as the fallback.
	virtualKeyPick := func(shareA float64) func() (schemas.Fallback, []schemas.Fallback) {
		return func() (schemas.Fallback, []schemas.Fallback) {
			if primary := roll(shareA); primary == a {
				return a, []schemas.Fallback{b}
			}
			return b, []schemas.Fallback{a}
		}
	}
	// rulePick is a rule's weighted targets with the rule's own fallback list.
	rulePick := func(shareA float64, fallbacks ...schemas.Fallback) func() (schemas.Fallback, []schemas.Fallback) {
		return func() (schemas.Fallback, []schemas.Fallback) { return roll(shareA), fallbacks }
	}
	keys := map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 1)}, provB: {scenarioKey("b1", 1)}}

	t.Run("GV-19 with fallbacks the session keeps its first pick", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), &scenarioRouter{route: virtualKeyPick(0.7)}, newMockKVStore())
		const sessions = 500
		first := map[schemas.ModelProvider]int{}
		for i := range sessions {
			id := fmt.Sprintf("s-%d", i)
			var bound schemas.ModelProvider
			for turn := range 3 {
				served, err := scenarioChat(client, sessionCtx(id), "", "m")
				if err != nil {
					t.Fatalf("session %s turn %d failed: %s", id, turn, err.GetErrorString())
				}
				if turn == 0 {
					bound = served
					first[served]++
				} else if served != bound {
					t.Fatalf("session %s moved from %s to %s on turn %d", id, bound, served, turn)
				}
			}
		}
		// Chi-square at p = 1e-6 on one degree of freedom.
		stat := 0.0
		for provider, share := range map[schemas.ModelProvider]float64{provA: 0.7, provB: 0.3} {
			expected := sessions * share
			diff := float64(first[provider]) - expected
			stat += diff * diff / expected
		}
		if stat >= 23.928 {
			t.Fatalf("first turns %v do not fit 70/30 (chi-square %.1f)", first, stat)
		}
	})

	t.Run("RR-30 targets with fallbacks keep the session on its first provider", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), &scenarioRouter{route: rulePick(0.5, a, b)}, newMockKVStore())
		var bound schemas.ModelProvider
		for turn := range 20 {
			ctx := sessionCtx("s")
			served, err := scenarioChat(client, ctx, "", "m")
			if err != nil {
				t.Fatalf("turn %d failed: %s", turn+1, err.GetErrorString())
			}
			if turn == 0 {
				bound = served
				continue
			}
			if served != bound {
				t.Fatalf("turn %d was served by %s, but turn 1 bound %s", turn+1, served, bound)
			}
			if !strings.Contains(routingLogs(ctx), "Session stays on "+string(bound)+"/m") {
				t.Fatalf("turn %d should report the session keeping its binding first:\n%s", turn+1, routingLogs(ctx))
			}
		}
	})

	t.Run("RR-29 targets without fallbacks are rolled every turn", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), &scenarioRouter{route: rulePick(0.5)}, newMockKVStore())
		seen := map[schemas.ModelProvider]bool{}
		dropped := false
		// Twenty turns on one provider happens about twice in a million runs.
		for range 20 {
			ctx := sessionCtx("one")
			served, err := scenarioChat(client, ctx, "", "m")
			if err != nil {
				t.Fatalf("turn failed: %s", err.GetErrorString())
			}
			seen[served] = true
			dropped = dropped || strings.Contains(routingLogs(ctx), "which this request cannot use")
		}
		if len(seen) != 2 {
			t.Fatalf("a one-entry chain cannot be held by a session, yet 20 turns only reached %v", seen)
		}
		if !dropped {
			t.Fatal("a turn whose roll differed from the binding should report dropping it")
		}
	})
}

// With a fixed configuration and a session, identical requests take identical routes: the same
// provider and the same key every time, and from turn 2 on the same routing trail (plan row IX-21).
func TestScenarioIdenticalRequestsTakeIdenticalRoutes(t *testing.T) {
	u := newScenarioUpstream(t)
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1), scenarioKey("a3", 1)},
		provB: {scenarioKey("b1", 1)},
	}), &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"})}, newMockKVStore())
	var trail string
	for turn := range 100 {
		ctx := sessionCtx("s")
		served, err := scenarioChat(client, ctx, "", "m")
		requireServed(t, served, err, provA)
		switch logs := routingLogs(ctx); {
		case turn == 1:
			trail = logs
		case turn > 1 && logs != trail:
			t.Fatalf("turn %d's routing trail differs from turn 2's:\n%s\n--- turn 2 ---\n%s", turn+1, logs, trail)
		}
	}
	used := 0
	for _, token := range []string{"sk-a1", "sk-a2", "sk-a3"} {
		switch u.count(token) {
		case 0:
		case 100:
			used++
		default:
			t.Fatalf("%s served %d of 100 identical requests; one key should serve them all", token, u.count(token))
		}
	}
	if used != 1 {
		t.Fatalf("%d keys served the session, want exactly one", used)
	}
}

// A caller's key pin applies to the attempt on the provider the caller named and to nothing after
// it: a pinned key's transient failure is retried on the pin and then falls back to a free pick, a
// fallback to another model of the same provider picks a key of its own, and a pin naming another
// provider's key fails key selection on the named provider with no upstream call, and is not carried
// to that provider's fallback (plan rows KH-03, KH-06, KH-07, KH-08; one subtest each, KH-07 in two
// parts). A bare model's pin is tried on the provider routing put first, which is never changed for
// it (KH-08).
func TestScenarioCallerPinStaysOffFallbacks(t *testing.T) {
	// The Key-ID header base: A{a1,a2,a3} and B{b1,b2}, every key at the same weight.
	keys := map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1), scenarioKey("a3", 1)},
		provB: {scenarioKey("b1", 1), scenarioKey("b2", 1)},
	}
	t.Run("KH-06 a model fallback on the same provider picks a key of its own", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		u.answer("sk-a2|m", http.StatusInternalServerError)
		for range 10 {
			served, err := scenarioChat(client, pinCtx("", "a2"), provA, "m", schemas.Fallback{Provider: provA, Model: "m2"})
			requireServed(t, served, err, provA)
		}
		requireHits(t, u, map[string]int{"sk-a2|m": 30, "sk-a1|m": 0, "sk-a3|m": 0})
		if got := u.count("sk-a1|m2") + u.count("sk-a2|m2") + u.count("sk-a3|m2"); got != 10 {
			t.Fatalf("the m2 fallback ran %d times, want once per request", got)
		}
		// A carried pin would send all ten to a2; a free pick does so once in about 59,000 runs.
		if u.count("sk-a2|m2") == 10 {
			t.Fatal("every m2 fallback ran on a2, the caller's pin; the fallback should pick freely among a1 to a3")
		}
	})
	// The row sends 10 requests; 20 are sent so that "b1 and b2 both seen" fails by chance only about
	// twice in a million runs rather than once in 512.
	t.Run("KH-03 a pinned key's transient failure falls back to a free pick on B", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		u.answer("sk-a2", http.StatusInternalServerError)
		for range 20 {
			info, err := scenarioChatInfo(client, pinCtx("", "a2"), provA, "m", schemas.Fallback{Provider: provB, Model: "m"})
			requireServed(t, info.Provider, err, provB)
			if !info.IsFallback || info.PrimaryProvider == nil || *info.PrimaryProvider != provA {
				t.Fatalf("B should serve as the fallback of A, routing info %+v", info)
			}
		}
		requireHits(t, u, map[string]int{"sk-a2": 60, "sk-a1": 0, "sk-a3": 0})
		if u.count("sk-b1") == 0 || u.count("sk-b2") == 0 {
			t.Fatalf("the fallback should pick freely on B, b1=%d b2=%d", u.count("sk-b1"), u.count("sk-b2"))
		}
	})
	t.Run("KH-07 a pin on another provider's key, with no fallbacks, is refused on the named provider", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		_, err := scenarioChat(client, pinCtx("", "b1"), provA, "m")
		requireStatus(t, err, http.StatusBadRequest, "")
		if !strings.Contains(err.GetErrorString(), "no supported key found with id") {
			t.Fatalf("the refusal should say no key with the pinned id was found: %s", err.GetErrorString())
		}
		requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0, "sk-a3": 0, "sk-b1": 0, "sk-b2": 0})
	})
	t.Run("KH-07 a pin on another provider's key is not carried to that provider's fallback", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		for range 20 {
			served, err := scenarioChat(client, pinCtx("", "b1"), provA, "m", schemas.Fallback{Provider: provB, Model: "m"})
			requireServed(t, served, err, provB)
		}
		requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0, "sk-a3": 0})
		// A carried pin would send all twenty to b1; a free pick does so about once in a million runs.
		if u.count("sk-b2") == 0 {
			t.Fatal("every B fallback ran on b1, the caller's pin; the fallback should pick freely")
		}
	})
	// A pin is tried on the first attempt only, on whatever provider routing put first: Bifrost never
	// moves the pinned key's provider forward. Routing chose A for a bare model with B as its fallback;
	// the caller's pin on b1 fails key selection on A with no upstream call, and B runs as a fallback,
	// without the pin.
	t.Run("KH-08 routing's choice of provider stands against a pin on another provider's key", func(t *testing.T) {
		u := newScenarioUpstream(t)
		router := &scenarioRouter{}
		router.setRoute(fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"}))
		client := scenarioClient(t, scenarioAccount(u, keys), router, nil)
		for range 20 {
			info, err := scenarioChatInfo(client, pinCtx("", "b1"), "", "m")
			if err != nil {
				t.Fatalf("request failed: %s", err.GetErrorString())
			}
			if info.Provider != provB || !info.IsFallback {
				t.Fatalf("served by %s (fallback %v), want B as the fallback", info.Provider, info.IsFallback)
			}
		}
		requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0, "sk-a3": 0})
		// A carried pin would send all twenty to b1; a free pick does so about once in a million runs.
		if u.count("sk-b2") == 0 {
			t.Fatal("every B fallback ran on b1, the caller's pin; the fallback should pick freely")
		}
	})
}

// directKeyAccount registers A{a1,a2} at uA and B{b1} at uB, the direct-key base, so what each
// provider received can be read on its own upstream.
func directKeyAccount(uA, uB *scenarioUpstream) *MockAccount {
	account := NewMockAccount()
	addScenarioProvider(account, uA, provA, []schemas.Key{scenarioKey("a1", 1), scenarioKey("a2", 1)})
	addScenarioProvider(account, uB, provB, []schemas.Key{scenarioKey("b1", 1)})
	return account
}

// A caller's direct key is used on the first provider of the chain, retries included, and on nothing
// after it: a fallback to another provider, or to another model of the same provider, runs on that
// provider's configured keys. With a bare model the first provider is whatever routing picks, so the
// caller's key can reach a provider it is not for (plan rows DK-01, DK-05, DK-06, DK-07; one subtest
// each).
func TestScenarioDirectKeyGoesOnlyToTheFirstProvider(t *testing.T) {
	directCtx := func() *schemas.BifrostContext {
		ctx := sessionCtx("")
		ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{ID: "header-provided", Name: "header-provided", Value: *schemas.NewSecretVar("sk-caller"), Weight: 1})
		return ctx
	}
	keys := map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1)},
		provB: {scenarioKey("b1", 1)},
	}

	t.Run("DK-01 served on the caller's key", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		for range 5 {
			info, err := scenarioChatInfo(client, directCtx(), provA, "m")
			requireServed(t, info.Provider, err, provA)
			if info.Key != "header-provided" {
				t.Fatalf("routing info should name the caller's key, got %q", info.Key)
			}
		}
		requireHits(t, u, map[string]int{"sk-caller": 5, "sk-a1": 0, "sk-a2": 0})
	})
	t.Run("DK-05 a provider fallback runs on its own keys", func(t *testing.T) {
		uA, uB := newScenarioUpstream(t), newScenarioUpstream(t)
		client := scenarioClient(t, directKeyAccount(uA, uB), nil, nil)
		uA.answer("sk-caller", http.StatusInternalServerError)
		served, err := scenarioChat(client, directCtx(), provA, "m", schemas.Fallback{Provider: provB, Model: "m"})
		requireServed(t, served, err, provB)
		requireHits(t, uA, map[string]int{"sk-caller": 3, "sk-a1": 0, "sk-a2": 0})
		requireHits(t, uB, map[string]int{"sk-caller": 0, "sk-b1": 1})
	})
	t.Run("DK-06 a model fallback on the same provider runs on the pool", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		u.answer("sk-caller", http.StatusUnauthorized)
		served, err := scenarioChat(client, directCtx(), provA, "m", schemas.Fallback{Provider: provA, Model: "m2"})
		requireServed(t, served, err, provA)
		requireHits(t, u, map[string]int{"sk-caller|m": 1, "sk-caller|m2": 0})
		if u.count("sk-a1|m2")+u.count("sk-a2|m2") != 1 {
			t.Fatalf("the model fallback should run once on a pool key, a1=%d a2=%d", u.count("sk-a1|m2"), u.count("sk-a2|m2"))
		}
	})
	t.Run("DK-07 with a bare model the caller's key goes to whichever provider routing picks", func(t *testing.T) {
		uA, uB := newScenarioUpstream(t), newScenarioUpstream(t)
		// The caller's key works on A only; B refuses it.
		uB.answer("sk-caller", http.StatusUnauthorized)
		// vk1 A=1, B=1: either provider is picked, the other rides as its fallback.
		router := &scenarioRouter{route: func() (schemas.Fallback, []schemas.Fallback) {
			a, b := schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"}
			if rand.IntN(2) == 0 {
				a, b = b, a
			}
			return a, []schemas.Fallback{b}
		}}
		client := scenarioClient(t, directKeyAccount(uA, uB), router, nil)
		const n = 40
		for range n {
			if _, err := scenarioChat(client, directCtx(), "", "m"); err != nil {
				t.Fatalf("request failed: %s", err.GetErrorString())
			}
		}
		toB := uB.count("sk-caller")
		if got := uA.count("sk-caller") + toB; got != n {
			t.Fatalf("the caller's key reached a first provider %d times, want %d", got, n)
		}
		if toB == 0 {
			t.Fatal("routing never put B first in 40 requests; the caller's key should have reached it")
		}
		if got := uA.count("sk-a1") + uA.count("sk-a2"); got != toB {
			t.Fatalf("each request B refused should be served by A on a pool key: %d, want %d", got, toB)
		}
		requireHits(t, uB, map[string]int{"sk-b1": 0})
	})
}

// A session's key is used for every retry, so a rate limit does not rotate to a sibling; only the
// fallback provider moves the session, and A's key binding is dropped. The same request without a
// session rotates to a sibling on A (plan row IX-19).
func TestScenarioBoundKeyOnARateLimitDoesNotRotate(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	// a2 and a3 have weight 0: a free pick always lands on a1, a rotation on one of them.
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 0), scenarioKey("a3", 0)},
		provB: {scenarioKey("b1", 1)},
	}), &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"})}, kv)

	served, err := scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provA)

	u.answer("sk-a1", http.StatusTooManyRequests)
	u.clearHits()
	served, err = scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provB)
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0, "sk-b1": 1})
	if entry := kv.data[SessionStateKey(sessionCtx("s"), SessionStateKindRoute, "", "m")]; entry.value != "prov-b/m" {
		t.Fatalf("the session should move to the fallback that served, got %+v", entry)
	}
	if _, bound := kv.data[SessionStateKey(sessionCtx("s"), SessionStateKindKey, string(provA), "m")]; bound {
		t.Fatal("the key binding the turn followed into a failure survived")
	}

	// Without a session the same rate limit rotates to a sibling on A.
	u.clearHits()
	served, err = scenarioChat(client, sessionCtx(""), "", "m")
	requireServed(t, served, err, provA)
	if u.count("sk-a2")+u.count("sk-a3") != 1 || u.count("sk-b1") != 0 {
		t.Fatalf("a request without a session should rotate on A: a2=%d a3=%d b1=%d", u.count("sk-a2"), u.count("sk-a3"), u.count("sk-b1"))
	}
}

// A key bound to the session that starts answering 429, with no fallbacks: the retry policy runs on
// the bound key, never its siblings, the turn returns the 429 and drops the key binding with a trail
// line saying so, and the next turn picks a key normally and binds the one that serves (plan row
// KS-04).
func TestScenarioBoundKeyRateLimitedWithNoFallbacks(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1), scenarioKey("a3", 1)},
	}), nil, kv)
	bindSession(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
	u.answer("sk-a1", http.StatusTooManyRequests)

	ctx := sessionCtx("s")
	_, err := scenarioChat(client, ctx, provA, "m")
	requireStatus(t, err, http.StatusTooManyRequests, "")
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0})
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "")
	if !strings.Contains(routingLogs(ctx), "The key this session followed for prov-a/m failed") {
		t.Fatalf("the turn should report dropping the key binding it followed:\n%s", routingLogs(ctx))
	}

	// a1 still answers 429; the normal pick rotates off it when it lands there, and binds what serves.
	u.clearHits()
	served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
	requireServed(t, served, err, provA)
	bound, _ := sessionState(kv, "s", SessionStateKindKey, provA, "m")
	if bound == "" || bound == "a1" || u.count("sk-"+bound) != 1 {
		t.Fatalf("the session should bind the key that served, holds %q (a1=%d a2=%d a3=%d)", bound, u.count("sk-a1"), u.count("sk-a2"), u.count("sk-a3"))
	}
}

// Before a session has a key, a transient error is retried on the same key and never rotates: a key
// that recovers within the retries serves on the third attempt and binds, and one that does not fails
// the turn with nothing bound, though healthy siblings sit beside it (plan row KS-03; one subtest per
// case). a2 and a3 weigh 0 so that a1 is picked first, as the row sets up.
func TestScenarioTransientErrorStaysOnItsKey(t *testing.T) {
	keys := map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 1), scenarioKey("a2", 0), scenarioKey("a3", 0)}}
	t.Run("(a) a1 recovers on the third attempt", func(t *testing.T) {
		u := newScenarioUpstream(t)
		kv := newMockKVStore()
		client := scenarioClient(t, scenarioAccount(u, keys), nil, kv)
		u.answerFirst("sk-a1", http.StatusInternalServerError, 2)
		served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
		requireServed(t, served, err, provA)
		requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0})
		requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
	})
	t.Run("(b) a1 keeps failing", func(t *testing.T) {
		u := newScenarioUpstream(t)
		kv := newMockKVStore()
		client := scenarioClient(t, scenarioAccount(u, keys), nil, kv)
		u.answer("sk-a1", http.StatusInternalServerError)
		_, err := scenarioChat(client, sessionCtx("s"), provA, "m")
		requireStatus(t, err, http.StatusInternalServerError, "")
		requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0})
		requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "")
	})
}

// When several fallbacks fail before one serves, the session binds the one that served, at fallback
// index 2, and the next turn's chain starts there with the rule's order behind it, [C, A, B]: a turn
// on which C fails is served by A at fallback index 1 with B never reached (plan row PS-04).
func TestScenarioSessionBindsTheFallbackThatServed(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1)}, provB: {scenarioKey("b1", 1)}, provC: {scenarioKey("c1", 1)},
	}), &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"}, schemas.Fallback{Provider: provC, Model: "m"})}, kv)
	u.answer("sk-a1", http.StatusInternalServerError)
	u.answer("sk-b1", http.StatusInternalServerError)

	ctx := sessionCtx("s")
	served, err := scenarioChat(client, ctx, "", "m")
	requireServed(t, served, err, provC)
	if index, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); index != 2 {
		t.Fatalf("C should serve as fallback 2, served at index %d", index)
	}
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-b1": 3, "sk-c1": 1})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-c/m")

	u.clearHits()
	ctx = sessionCtx("s")
	served, err = scenarioChat(client, ctx, "", "m")
	requireServed(t, served, err, provC)
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-b1": 0, "sk-c1": 1})
	if logs := routingLogs(ctx); !strings.Contains(logs, "Session stays on prov-c/m for m; routing proposed prov-a/m") {
		t.Fatalf("turn 2 should put the bound C ahead of the rule's A:\n%s", logs)
	}

	// The rest of the chain keeps the rule's order behind C, [A, B]: with C failing and A and B healed,
	// A serves as fallback 1 and B is never reached.
	u.answer("sk-c1", http.StatusInternalServerError)
	u.heal("sk-a1")
	u.heal("sk-b1")
	u.clearHits()
	ctx = sessionCtx("s")
	served, err = scenarioChat(client, ctx, "", "m")
	requireServed(t, served, err, provA)
	if index, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); index != 1 {
		t.Fatalf("A should serve as fallback 1 behind the bound C, served at index %d", index)
	}
	requireHits(t, u, map[string]int{"sk-c1": 3, "sk-a1": 1, "sk-b1": 0})
}

// Editing the rule while a session is bound: the session keeps its provider while the edited rule
// still offers it, and rebinds when the rule no longer does (plan row DC-06).
func TestScenarioEditedRuleAndABoundSession(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	router := &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"})}
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1)}, provB: {scenarioKey("b1", 1)}, provC: {scenarioKey("c1", 1)},
	}), router, kv)
	served, err := scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provA)

	// (a) The rule now targets B, with A as its fallback: for two turns the session stays on A, and the
	// binding is kept.
	router.setRoute(fixedRoute(schemas.Fallback{Provider: provB, Model: "m"}, schemas.Fallback{Provider: provA, Model: "m"}))
	for range 2 {
		served, err = scenarioChat(client, sessionCtx("s"), "", "m")
		requireServed(t, served, err, provA)
		requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")
	}

	// (b) The rule targets C with B as its fallback, so it no longer offers A: the binding is dropped,
	// C serves and rebinds it, and the second turn stays on C.
	router.setRoute(fixedRoute(schemas.Fallback{Provider: provC, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"}))
	for range 2 {
		served, err = scenarioChat(client, sessionCtx("s"), "", "m")
		requireServed(t, served, err, provC)
		requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-c/m")
	}
}

// A rule target pinned to a1, with a fallback pinned to b1, over fifty turns of one session: every
// turn is served by A on a1, a2, a3 and B are never used, the first turn binds the session's route
// to A/m and its key on A to a1, and every later turn's trail says the session agrees with the rule
// (plan row EK-01).
func TestScenarioPinnedRuleHoldsItsKeyForFiftyTurns(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)
	for turn := 1; turn <= 50; turn++ {
		ctx := sessionCtx("s")
		info, err := scenarioChatInfo(client, ctx, "", "m")
		requireServed(t, info.Provider, err, provA)
		if info.IsFallback || info.Key != "a1" {
			t.Fatalf("turn %d should be served by A on a1 as the primary, routing info %+v", turn, info)
		}
		if turn == 1 {
			requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")
			requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
			continue
		}
		if logs := routingLogs(ctx); !strings.Contains(logs, "Session stays on prov-a/m for m, which routing also proposed") {
			t.Fatalf("turn %d should report the session agreeing with the rule:\n%s", turn, logs)
		}
	}
	requireHits(t, u, map[string]int{"sk-a1": 50, "sk-a2": 0, "sk-a3": 0, "sk-b1": 0, "sk-b2": 0})
}

// The caller's direct key beats the rule's pin: a healthy direct key serves on A with a1 never
// used, binding the route but no key; a direct key answering 500 is retried on itself, then the
// fallback runs on b1 (the direct key does not travel), and a fallback that served without the
// caller's key binds no route (plan row EK-10).
func TestScenarioDirectKeyBeatsTheRulesPin(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)

	info, err := scenarioChatInfo(client, directKeyCtx("healthy"), "", "m")
	requireServed(t, info.Provider, err, provA)
	if info.Key != "header-provided" {
		t.Fatalf("A should serve on the caller's key, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-caller": 1, "sk-a1": 0, "sk-a2": 0, "sk-a3": 0, "sk-b1": 0})
	requireSessionState(t, kv, "healthy", SessionStateKindRoute, "", "m", "prov-a/m")
	requireSessionState(t, kv, "healthy", SessionStateKindKey, provA, "m", "")

	u.answer("sk-caller", http.StatusInternalServerError)
	u.clearHits()
	info, err = scenarioChatInfo(client, directKeyCtx("failing"), "", "m")
	requireServed(t, info.Provider, err, provB)
	if !info.IsFallback || info.Key != "b1" {
		t.Fatalf("B should serve on its pinned b1 as the fallback, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-caller": 3, "sk-a1": 0, "sk-a2": 0, "sk-a3": 0, "sk-b1": 1, "sk-b2": 0})
	requireSessionState(t, kv, "failing", SessionStateKindRoute, "", "m", "")
}

// The rule's fallback pinned to a disabled key: a1 answers 500 and is retried on itself, B's attempt
// fails key selection without reaching the upstream, no further fallback exists, and the request
// returns A's error, the primary's. The session had followed its binding to A, so the binding is
// dropped (plan row EK-16).
func TestScenarioPinnedFallbackKeyDisabled(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := pinnedRuleAccount(u)
	b1 := scenarioKey("b1", 1)
	b1.Enabled = schemas.Ptr(false)
	account.SetKeysForProvider(provB, []schemas.Key{b1, scenarioKey("b2", 1)})
	client := scenarioClient(t, account, &scenarioRouter{route: pinnedRule()}, kv)

	served, err := scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provA)
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")

	u.answer("sk-a1", http.StatusInternalServerError)
	u.clearHits()
	ctx := sessionCtx("s")
	info, err := scenarioChatInfo(client, ctx, "", "m")
	requireStatus(t, err, http.StatusInternalServerError, "")
	if info.Provider != provA {
		t.Fatalf("the error should be A's, the primary's, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0, "sk-b1": 0, "sk-b2": 0})
	logs := routingLogs(ctx)
	for _, want := range []string{"Trying fallback 1/1: prov-b/m", "Fallback 1/1 pinned to provider key b1", "All 1 fallback(s) exhausted; returning primary error"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the trail should show B's attempt on its pinned key failing (%q):\n%s", want, logs)
		}
	}
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "")
	if !strings.Contains(logs, "The provider this session followed for m failed") {
		t.Fatalf("the trail should report dropping the followed binding:\n%s", logs)
	}
}

// streamTurn sends one streamed chat turn and drains it, returning what the stream carried and the
// routing info of the attempt that answered.
func streamTurn(t *testing.T, client *Bifrost, ctx *schemas.BifrostContext) (content string, errs []string, info schemas.RoutingInfo) {
	t.Helper()
	stream, err := client.ChatCompletionStreamRequest(ctx, scenarioRequest("", "m", nil))
	if err != nil {
		t.Fatalf("the stream did not start: %s", err.GetErrorString())
	}
	var builder strings.Builder
	for chunk := range stream {
		if chunk.BifrostError != nil {
			if chunk.BifrostError.Error != nil {
				errs = append(errs, chunk.BifrostError.Error.Message)
			}
			continue
		}
		if chunk.BifrostChatResponse == nil {
			continue
		}
		info = chunk.BifrostChatResponse.ExtraFields.RoutingInfo
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil && choice.ChatStreamResponseChoice.Delta.Content != nil {
				builder.WriteString(*choice.ChatStreamResponseChoice.Delta.Content)
			}
		}
	}
	return builder.String(), errs, info
}

// Streams under the pinned rule. (a) a1 answers 500 before any chunk: it is retried on itself, then
// b1 serves the whole stream, the session binds B/m and b1, and the next streamed turn starts on
// B/b1 with A untouched. (b) a1 fails after its first chunk: nothing falls back, and the error
// reaches the caller after the content already sent (plan row EK-19).
func TestScenarioPinnedRuleOnStreams(t *testing.T) {
	t.Run("(a) a 500 before the first chunk falls back to b1", func(t *testing.T) {
		u := newScenarioUpstream(t)
		kv := newMockKVStore()
		client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)
		u.answer("sk-a1", http.StatusInternalServerError)

		content, errs, info := streamTurn(t, client, sessionCtx("s"))
		if len(errs) > 0 || content != "ok" {
			t.Fatalf("the rescued stream should carry the fallback's whole answer, content %q errors %v", content, errs)
		}
		if info.Provider != provB || info.Key != "b1" || !info.IsFallback {
			t.Fatalf("B should serve the stream on b1 as the fallback, routing info %+v", info)
		}
		requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0, "sk-b1": 1, "sk-b2": 0})
		requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")
		requireSessionState(t, kv, "s", SessionStateKindKey, provB, "m", "b1")

		u.clearHits()
		content, errs, info = streamTurn(t, client, sessionCtx("s"))
		if len(errs) > 0 || content != "ok" {
			t.Fatalf("the second streamed turn should complete, content %q errors %v", content, errs)
		}
		if info.Provider != provB || info.Key != "b1" || info.IsFallback {
			t.Fatalf("the second turn should start on B/b1 as the primary, routing info %+v", info)
		}
		requireHits(t, u, map[string]int{"sk-a1": 0, "sk-b1": 1, "sk-b2": 0})
	})
	t.Run("(b) a failure after the first chunk reaches the caller", func(t *testing.T) {
		u := newScenarioUpstream(t)
		kv := newMockKVStore()
		client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)
		u.reply("sk-a1", scenarioReply{midStream: true})

		content, errs, _ := streamTurn(t, client, sessionCtx("s"))
		if content != "partial" {
			t.Fatalf("the caller should get A's partial content, got %q", content)
		}
		if len(errs) == 0 {
			t.Fatal("the failure after content never reached the caller")
		}
		requireHits(t, u, map[string]int{"sk-a1": 1, "sk-a2": 0, "sk-a3": 0, "sk-b1": 0, "sk-b2": 0})
	})
}

// providerRulePlugin stands in for a routing rule matching provider == A: a request that names A
// keeps A, pinned to a1, with B pinned to b1 as its fallback, as the routing plugin sets them for a
// matched rule.
type providerRulePlugin struct{}

// GetName implements schemas.LLMPlugin.
func (providerRulePlugin) GetName() string { return "provider-rule" }

// Cleanup implements schemas.LLMPlugin.
func (providerRulePlugin) Cleanup() error { return nil }

// PreRequestHook applies the rule to a request that named A.
func (providerRulePlugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if provider, _, _ := req.GetRequestFields(); provider != provA {
		return nil
	}
	req.SetFallbacks([]schemas.Fallback{{Provider: provB, Model: "m", KeyID: "b1"}})
	ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, "a1")
	return nil
}

// PreLLMHook implements schemas.LLMPlugin.
func (providerRulePlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook implements schemas.LLMPlugin.
func (providerRulePlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// A provider-prefixed request against a rule matching that provider: each turn retries a1 on its
// 500 three times and falls back to b1, the caller named A so no route binding is written, and the
// second turn starts at A again (plan row EK-20).
func TestScenarioProviderPrefixedRequestUnderThePinnedRule(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := pinnedRuleAccount(u)
	client, initErr := Init(context.Background(), schemas.BifrostConfig{Account: account, Logger: NewDefaultLogger(schemas.LogLevelError), KVStore: kv, LLMPlugins: []schemas.LLMPlugin{providerRulePlugin{}}})
	if initErr != nil {
		t.Fatalf("Init: %v", initErr)
	}
	t.Cleanup(client.Shutdown)
	u.answer("sk-a1", http.StatusInternalServerError)
	for turn := 1; turn <= 2; turn++ {
		u.clearHits()
		info, err := scenarioChatInfo(client, sessionCtx("s"), provA, "m")
		requireServed(t, info.Provider, err, provB)
		if !info.IsFallback || info.Key != "b1" {
			t.Fatalf("turn %d should be served by B on b1 as the fallback, routing info %+v", turn, info)
		}
		requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0, "sk-b1": 1, "sk-b2": 0})
		requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "")
	}
}

// A caller's pin does not rebind the session's key: unpinned turns bind a1, a turn pinned to a2 is
// served on a2 and leaves the binding on a1, and with the weights moved so a free pick would land on
// a3 the next unpinned turn is back on a1 (plan row KH-12).
func TestScenarioCallerPinLeavesTheSessionsKey(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 1), scenarioKey("a2", 0), scenarioKey("a3", 0)}})
	client := scenarioClient(t, account, nil, kv)
	for range 2 {
		served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
		requireServed(t, served, err, provA)
	}
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")

	u.clearHits()
	info, err := scenarioChatInfo(client, pinCtx("s", "a2"), provA, "m")
	requireServed(t, info.Provider, err, provA)
	if info.Key != "a2" {
		t.Fatalf("the pinned turn should be served on a2, routing info %+v", info)
	}
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 1, "sk-a3": 0})
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")

	// From here a free pick lands on a3; only the binding keeps the session on a1.
	account.SetKeysForProvider(provA, []schemas.Key{scenarioKey("a1", 0), scenarioKey("a2", 0), scenarioKey("a3", 1)})
	u.clearHits()
	ctx := sessionCtx("s")
	served, err := scenarioChat(client, ctx, provA, "m")
	requireServed(t, served, err, provA)
	requireHits(t, u, map[string]int{"sk-a1": 1, "sk-a2": 0, "sk-a3": 0})
	if !strings.Contains(routingLogs(ctx), "Session reused key a1 for prov-a/m") {
		t.Fatalf("the unpinned turn should reuse the session's a1:\n%s", routingLogs(ctx))
	}
}

// A session bound to B, the rule's primary, and a caller pin on a2, a key of A, the rule's fallback:
// the session keeps B first, the pin fails key selection on B with no upstream call, A serves as the
// fallback on a free pick without the pin, and the session drops B and rebinds to A and the key that
// served (plan row KH-14).
func TestScenarioCallerPinOnTheFallbacksKeyUnderABoundSession(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	// a2 weighs 0, so a free pick on A never lands on it; only a carried pin would.
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 0), scenarioKey("a3", 1)},
		provB: {scenarioKey("b1", 1), scenarioKey("b2", 1)},
	}), &scenarioRouter{route: chainOf(provB, provA)}, kv)
	served, err := scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provB)
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")

	u.clearHits()
	ctx := pinCtx("s", "a2")
	info, err := scenarioChatInfo(client, ctx, "", "m")
	requireServed(t, info.Provider, err, provA)
	if !info.IsFallback || info.PrimaryProvider == nil || *info.PrimaryProvider != provB || info.Key == "a2" {
		t.Fatalf("A should serve as B's fallback on a free pick, not the caller's a2; routing info %+v", info)
	}
	if !strings.Contains(routingLogs(ctx), "Session stays on prov-b/m") {
		t.Fatalf("the session should keep B first:\n%s", routingLogs(ctx))
	}
	requireHits(t, u, map[string]int{"sk-b1": 0, "sk-b2": 0, "sk-a2": 0, "sk-" + info.Key: 1})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", info.Key)
}

// A session that followed its route binding to A, then sends the caller's key, which A refuses: each
// turn tries the key on A once, B serves on b1 without the caller's key, the binding the first turn
// followed is dropped, nothing is bound, and the second turn starts on A again (plan row DK-09).
func TestScenarioDirectKeyRescuedByTheFallbackDropsTheBinding(t *testing.T) {
	uA, uB := newScenarioUpstream(t), newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, directKeyAccount(uA, uB), &scenarioRouter{route: chainOf(provA, provB)}, kv)
	served, err := scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provA)
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")

	uA.answer("sk-caller", http.StatusUnauthorized)
	for turn := 1; turn <= 2; turn++ {
		uA.clearHits()
		uB.clearHits()
		ctx := directKeyCtx("s")
		info, err := scenarioChatInfo(client, ctx, "", "m")
		requireServed(t, info.Provider, err, provB)
		if !info.IsFallback || info.Key != "b1" {
			t.Fatalf("turn %d should be served by B on b1 as the fallback, routing info %+v", turn, info)
		}
		requireHits(t, uA, map[string]int{"sk-caller": 1, "sk-a1": 0, "sk-a2": 0})
		requireHits(t, uB, map[string]int{"sk-caller": 0, "sk-b1": 1})
		requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "")
		if !strings.Contains(routingLogs(ctx), "without the caller's own key") {
			t.Fatalf("turn %d should say the fallback served without the caller's key:\n%s", turn, routingLogs(ctx))
		}
	}
}

// Key-level routing scenarios from the routing test plan: pinned keys (a rule's pin, the caller's
// x-bf-api-key-id, a direct key), key stickiness within a provider, and what each failure class does
// to the retry and fallback loops. They run on the scenario upstream, account and router above. Each
// test names the plan rows it covers.

// failureReplies are OpenAI-shaped answers for the failure classes the upstream's default bodies do
// not produce, keyed by the plan's name for each.
var failureReplies = map[string]scenarioReply{
	"402 quota":        {status: http.StatusPaymentRequired, body: `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`},
	"429 quota":        {status: http.StatusTooManyRequests, body: `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`},
	"404 model":        {status: http.StatusNotFound, body: "{\"error\":{\"message\":\"The model `m` does not exist or you do not have access to it.\",\"type\":\"invalid_request_error\",\"code\":\"model_not_found\"}}"},
	"410 retired":      {status: http.StatusGone, body: `{"error":{"message":"The model has been deprecated and is no longer available.","type":"invalid_request_error","code":"model_deprecated"}}`},
	"403 region":       {status: http.StatusForbidden, body: `{"error":{"message":"Country, region, or territory not supported","type":"request_forbidden","code":"unsupported_country_region_territory"}}`},
	"400 caller fault": {status: http.StatusBadRequest, body: `{"error":{"message":"Invalid value for 'temperature': must be at most 2.","type":"invalid_request_error","param":"temperature","code":"invalid_value"}}`},
}

// setMaxRetries sets a provider's max_retries in the account, as an operator editing its network
// config would; a running client picks the change up on UpdateProvider.
func setMaxRetries(account *MockAccount, provider schemas.ModelProvider, n int) {
	account.mu.Lock()
	defer account.mu.Unlock()
	account.configs[provider].NetworkConfig.MaxRetries = n
}

// sessionState reads one of a session's bindings: its route for model (provider "") or its key on
// provider for model.
func sessionState(kv schemas.KVStore, session, kind string, provider schemas.ModelProvider, model string) (string, bool) {
	return SessionStateString(kv, SessionStateKey(sessionCtx(session), kind, string(provider), model))
}

// bindSession writes one of a session's bindings directly, as earlier turns would have left it.
func bindSession(t *testing.T, kv schemas.KVStore, session, kind string, provider schemas.ModelProvider, model, value string) {
	t.Helper()
	if err := kv.SetWithTTL(SessionStateKey(sessionCtx(session), kind, string(provider), model), value, time.Hour); err != nil {
		t.Fatalf("binding the session: %v", err)
	}
}

// requireSessionState fails the test unless the binding holds want, or is absent when want is "".
func requireSessionState(t *testing.T, kv schemas.KVStore, session, kind string, provider schemas.ModelProvider, model, want string) {
	t.Helper()
	got, found := sessionState(kv, session, kind, provider, model)
	if want == "" && found {
		t.Fatalf("the session's %s binding for %s/%s should be gone, holds %q", kind, provider, model, got)
	}
	if want != "" && got != want {
		t.Fatalf("the session's %s binding for %s/%s holds %q, want %q", kind, provider, model, got, want)
	}
}

// pinCtx is a context for session (none when empty) whose caller pins keyID with x-bf-api-key-id.
func pinCtx(session, keyID string) *schemas.BifrostContext {
	ctx := sessionCtx(session)
	ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, keyID)
	return ctx
}

// directKeyCtx is a context for session (none when empty) that carries the caller's own key, whose
// value the upstream sees as the token "sk-caller".
func directKeyCtx(session string) *schemas.BifrostContext {
	ctx := sessionCtx(session)
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{ID: "header-provided", Name: "header-provided", Value: *schemas.NewSecretVar("sk-caller"), Weight: 1})
	return ctx
}

// routingLogs joins the routing engine log messages a request left on ctx, for substring checks.
func routingLogs(ctx *schemas.BifrostContext) string {
	var lines []string
	for _, entry := range ctx.GetRoutingEngineLogs() {
		lines = append(lines, entry.Engine+": "+entry.Message)
	}
	return strings.Join(lines, "\n")
}

// trailKeys lists the key each upstream call of an attempt trail went to.
func trailKeys(trail []schemas.KeyAttemptRecord) []string {
	keys := make([]string, len(trail))
	for i, record := range trail {
		keys[i] = record.KeyID
	}
	return keys
}

// requireClass fails the test unless every record of the trail carries class.
func requireClass(t *testing.T, trail []schemas.KeyAttemptRecord, class schemas.FailureClass) {
	t.Helper()
	for i, record := range trail {
		if record.FailureClass != class {
			t.Fatalf("attempt %d on %s was classed %q, want %q (trail %+v)", i, record.KeyID, record.FailureClass, class, trail)
		}
	}
}

// requireStatus fails the test unless the request failed with status, and, when errType is given,
// with that error type.
func requireStatus(t *testing.T, err *schemas.BifrostError, status int, errType string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the request was served, want a %d", status)
	}
	if err.StatusCode == nil || *err.StatusCode != status {
		t.Fatalf("the request failed with status %v (%s), want %d", err.StatusCode, err.GetErrorString(), status)
	}
	if errType != "" && (err.Error == nil || err.Error.Type == nil || *err.Error.Type != errType) {
		t.Fatalf("the request failed with %q, want type %q", err.GetErrorString(), errType)
	}
}

// A rule target pinned to one key sends every request to that key, with healthy siblings beside it
// (plan row RR-07).
func TestScenarioRulePinnedKeyIsTheOnlyKeyUsed(t *testing.T) {
	u := newScenarioUpstream(t)
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m", KeyID: "a2"})}, nil)
	for range 30 {
		served, err := scenarioChat(client, sessionCtx(""), "", "m")
		requireServed(t, served, err, provA)
	}
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 30, "sk-a3": 0})
}

// pinBackoff is the initial retry backoff the transient pinned-key cases run with, long enough that
// a retry sent without waiting for it cannot pass for one that did.
const pinBackoff = 40 * time.Millisecond

// requireBackoff fails the test unless each retry in arrivals came after the backoff core owes it:
// retry n waits initial×2^(n-1) less at most 20% jitter.
func requireBackoff(t *testing.T, arrivals []time.Time, initial time.Duration) {
	t.Helper()
	for n := 1; n < len(arrivals); n++ {
		floor := time.Duration(float64(initial*time.Duration(1<<(n-1))) * 0.8)
		if gap := arrivals[n].Sub(arrivals[n-1]); gap < floor {
			t.Fatalf("retry %d came %s after the attempt before it, want at least the %s backoff", n, gap, floor)
		}
	}
}

// A pinned key, whether a rule's target pinned it or the caller sent x-bf-api-key-id, has nothing
// behind it: a transient error and a rate limit are retried on the pin and returned, and a rejected
// key is tried once. Within the retry budget that rejection becomes 502 upstream_credentials_exhausted;
// with no retries it is the provider's own 401. The transient retries wait out the backoff between
// attempts (plan rows RR-08, RR-09, RR-10 for the rule's pin and KH-02, KH-05, KH-04 for the
// caller's; one subtest per row and case, named after the row).
func TestScenarioPinnedKeyFailuresStayOnThePin(t *testing.T) {
	type pinSource struct {
		name  string
		route func() (schemas.Fallback, []schemas.Fallback)
		ctx   func() *schemas.BifrostContext
		named schemas.ModelProvider
		rows  []string // the plan row of each case below, in order
	}
	sources := []pinSource{
		{name: "rule pin", route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m", KeyID: "a2"}), ctx: func() *schemas.BifrostContext { return sessionCtx("") }, rows: []string{"RR-08", "RR-09", "RR-10", "RR-10"}},
		{name: "caller pin", ctx: func() *schemas.BifrostContext { return pinCtx("", "a2") }, named: provA, rows: []string{"KH-02", "KH-05", "KH-04", "KH-04"}},
	}
	for _, source := range sources {
		for i, tc := range []struct {
			name       string
			reply      scenarioReply
			maxRetries int
			hits       int
			status     int
			errType    string
			class      schemas.FailureClass
		}{
			{name: "transient 500", reply: scenarioReply{status: http.StatusInternalServerError, body: `{"error":{"message":"The server had an error","type":"server_error"}}`}, maxRetries: 2, hits: 3, status: 500, class: schemas.FailureClassTransient},
			{name: "rate limit 429", reply: scenarioReply{status: http.StatusTooManyRequests, body: `{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`, header: map[string]string{"Retry-After": "7"}}, maxRetries: 2, hits: 3, status: 429, class: schemas.FailureClassRateLimit},
			{name: "rejected 401 within the budget", reply: scenarioReply{status: http.StatusUnauthorized, body: `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`}, maxRetries: 2, hits: 1, status: 502, errType: "upstream_credentials_exhausted", class: schemas.FailureClassCredential},
			{name: "rejected 401 with no retries", reply: scenarioReply{status: http.StatusUnauthorized, body: `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`}, maxRetries: 0, hits: 1, status: 401, class: schemas.FailureClassCredential},
		} {
			t.Run(source.rows[i]+" "+source.name+"/"+tc.name, func(t *testing.T) {
				u := newScenarioUpstream(t)
				account := pinnedRuleAccount(u)
				setMaxRetries(account, provA, tc.maxRetries)
				if tc.class == schemas.FailureClassTransient {
					// A backoff long enough to measure: retry n waits initial×2^(n-1), give or take 20%.
					account.configs[provA].NetworkConfig.RetryBackoffInitial = pinBackoff
					account.configs[provA].NetworkConfig.RetryBackoffMax = time.Second
				}
				router := &scenarioRouter{route: source.route}
				client := scenarioClient(t, account, router, nil)
				u.reply("sk-a2", tc.reply)

				_, err := scenarioChat(client, source.ctx(), source.named, "m")
				requireStatus(t, err, tc.status, tc.errType)
				requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": tc.hits, "sk-a3": 0, "sk-b1": 0, "sk-b2": 0})
				if tc.class == schemas.FailureClassTransient {
					requireBackoff(t, u.arrivalsOf("sk-a2"), pinBackoff)
				}
				attempts := router.takeAttempts()
				if len(attempts) != 1 {
					t.Fatalf("recorded %d provider attempts, want 1", len(attempts))
				}
				if keys := trailKeys(attempts[0].trail); len(keys) != tc.hits || strings.Count(strings.Join(keys, ","), "a2") != tc.hits {
					t.Fatalf("attempt trail %v, want a2 ×%d", keys, tc.hits)
				}
				requireClass(t, attempts[0].trail, tc.class)
				if tc.reply.header != nil {
					if err.ExtraFields.RetryAfter != 7000 {
						t.Fatalf("the 429 came back with retry hint %dms, want the provider's 7000ms", err.ExtraFields.RetryAfter)
					}
				}
			})
		}
	}
}

// A rule's key pin takes the primary attempt over from the caller's x-bf-api-key-id, and with a rule
// pin present the caller's pin does not hold the chain still: a session the rule's fallback served
// starts there, whatever key the caller pins (plan row EK-09).
func TestScenarioRulePinBeatsTheCallersKeyPin(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: pinnedRule()}, kv)
	for range 5 {
		served, err := scenarioChat(client, pinCtx("s", "a2"), "", "m")
		requireServed(t, served, err, provA)
	}
	requireHits(t, u, map[string]int{"sk-a1": 5, "sk-a2": 0, "sk-a3": 0})

	// a1 fails once: the rule's fallback serves on its own pin, and the session moves to it.
	u.answer("sk-a1", http.StatusInternalServerError)
	u.clearHits()
	served, err := scenarioChat(client, pinCtx("s", "a2"), "", "m")
	requireServed(t, served, err, provB)
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-b1": 1})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")

	// a1 is healthy again, and the caller still pins a2, a key of the rule's target. The session
	// still starts on B: only a caller pin with no rule pin beside it keeps the target first.
	u.heal("sk-a1")
	u.clearHits()
	served, err = scenarioChat(client, pinCtx("s", "a2"), "", "m")
	requireServed(t, served, err, provB)
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 0, "sk-b1": 1})
}

// A request on the caller's own key stays on that key whatever it answers: a transient error and a
// rate limit are retried on it and returned, and the pool is never touched. Sent with
// x-bf-api-key-id as well, the direct key still wins (plan rows DK-03, DK-04, DK-13).
func TestScenarioDirectKeyFailuresStayOnTheCallersKey(t *testing.T) {
	keys := map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 1), scenarioKey("a2", 1)}}
	t.Run("transient 500", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		u.answer("sk-caller", http.StatusInternalServerError)
		_, err := scenarioChat(client, directKeyCtx(""), provA, "m")
		requireStatus(t, err, 500, "")
		requireHits(t, u, map[string]int{"sk-caller": 3, "sk-a1": 0, "sk-a2": 0})
	})
	t.Run("rate limit 429", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		u.answer("sk-caller", http.StatusTooManyRequests)
		_, err := scenarioChat(client, directKeyCtx(""), provA, "m")
		requireStatus(t, err, 429, "")
		requireHits(t, u, map[string]int{"sk-caller": 3, "sk-a1": 0, "sk-a2": 0})
	})
	t.Run("with x-bf-api-key-id", func(t *testing.T) {
		u := newScenarioUpstream(t)
		client := scenarioClient(t, scenarioAccount(u, keys), nil, nil)
		ctx := directKeyCtx("")
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "a2")
		served, err := scenarioChat(client, ctx, provA, "m")
		requireServed(t, served, err, provA)
		requireHits(t, u, map[string]int{"sk-caller": 1, "sk-a1": 0, "sk-a2": 0})
	})
}

// A direct-key turn neither reads nor writes the session's key binding on its provider: the turn
// after it is back on the bound key, though a free pick would now land elsewhere (plan row DK-10).
func TestScenarioDirectKeyLeavesTheSessionsKeyBinding(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 0), scenarioKey("a2", 1)}})
	client := scenarioClient(t, account, nil, kv)
	served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
	requireServed(t, served, err, provA)
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a2")

	// From here a free pick lands on a1; only the binding keeps the session on a2.
	account.SetKeysForProvider(provA, []schemas.Key{scenarioKey("a1", 1), scenarioKey("a2", 0)})
	u.clearHits()
	ctx := directKeyCtx("s")
	served, err = scenarioChat(client, ctx, provA, "m")
	requireServed(t, served, err, provA)
	requireHits(t, u, map[string]int{"sk-caller": 1, "sk-a1": 0, "sk-a2": 0})
	if logs := routingLogs(ctx); strings.Contains(logs, "Session reused key") || strings.Contains(logs, "no longer eligible") {
		t.Fatalf("the direct-key turn consulted the session's key binding:\n%s", logs)
	}
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a2")

	served, err = scenarioChat(client, sessionCtx("s"), provA, "m")
	requireServed(t, served, err, provA)
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-a2": 1})
}

// A bare turn served on the caller's key by the primary binds the session's route but no key, so the
// next turn without a direct key starts on the same provider and picks a pool key normally, with no
// stale key binding to drop (plan row DK-08).
func TestScenarioDirectKeyServedTurnBindsTheRouteOnly(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1)},
		provB: {scenarioKey("b1", 1), scenarioKey("b2", 1)},
	}), &scenarioRouter{route: func() (schemas.Fallback, []schemas.Fallback) {
		a, b := schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"}
		if rand.IntN(2) == 0 {
			a, b = b, a
		}
		return a, []schemas.Fallback{b}
	}}, kv)
	for i := range 10 {
		session := fmt.Sprintf("s-%d", i)
		first, err := scenarioChat(client, directKeyCtx(session), "", "m")
		if err != nil {
			t.Fatalf("direct-key turn failed: %s", err.GetErrorString())
		}
		requireSessionState(t, kv, session, SessionStateKindRoute, "", "m", string(first)+"/m")
		requireSessionState(t, kv, session, SessionStateKindKey, first, "m", "")

		ctx := sessionCtx(session)
		served, err := scenarioChat(client, ctx, "", "m")
		requireServed(t, served, err, first)
		if logs := routingLogs(ctx); strings.Contains(logs, "no longer eligible") {
			t.Fatalf("the turn after the direct-key turn found a stale key binding:\n%s", logs)
		}
		if bound, found := sessionState(kv, session, SessionStateKindKey, first, "m"); !found || bound == "header-provided" {
			t.Fatalf("the pool turn should bind a pool key, got %q", bound)
		}
	}
}

// A session's key refused for good, by any permanent per-key class, is tried once; the attempt is
// granted back, a sibling serves, and the session rebinds to it. At max_retries 0 the sibling's
// attempt can only be the granted one, so the grant is shown there as well as within a retry budget
// (plan row KS-06).
func TestScenarioSessionKeyRefusedForGoodRebinds(t *testing.T) {
	for _, name := range []string{"404 model", "429 quota", "410 retired", "403 region"} {
		for _, maxRetries := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s at max_retries %d", name, maxRetries), func(t *testing.T) {
				u := newScenarioUpstream(t)
				kv := newMockKVStore()
				router := &scenarioRouter{}
				account := pinnedRuleAccount(u)
				setMaxRetries(account, provA, maxRetries)
				client := scenarioClient(t, account, router, kv)
				bindSession(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
				u.reply("sk-a1", failureReplies[name])

				served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
				requireServed(t, served, err, provA)
				keys := trailKeys(router.takeAttempts()[0].trail)
				if len(keys) != 2 || keys[0] != "a1" || keys[1] == "a1" {
					t.Fatalf("attempt trail %v, want a1 once and then a sibling", keys)
				}
				requireHits(t, u, map[string]int{"sk-a1": 1, "sk-" + keys[1]: 1})
				requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", keys[1])
			})
		}
	}
}

// With max_retries 0 and a fresh session, a rate limit on the first pick is not retried, so a
// multi-key pool returns the 429 with no sibling tried and nothing bound; a refused key is walked
// past by the one granted attempt, a sibling serves, and the session binds the sibling. a2 and a3
// weigh 0 so the first pick is a1 (plan row KS-07; one subtest per case).
func TestScenarioZeroRetriesStillWalksDeadKeys(t *testing.T) {
	keys := map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 1), scenarioKey("a2", 0), scenarioKey("a3", 0)}}
	setup := func(t *testing.T) (*scenarioUpstream, *mockKVStore, *scenarioRouter, *Bifrost) {
		u := newScenarioUpstream(t)
		kv := newMockKVStore()
		router := &scenarioRouter{}
		account := scenarioAccount(u, keys)
		setMaxRetries(account, provA, 0)
		return u, kv, router, scenarioClient(t, account, router, kv)
	}
	t.Run("(a) a1 answers 429", func(t *testing.T) {
		u, kv, router, client := setup(t)
		u.answer("sk-a1", http.StatusTooManyRequests)
		_, err := scenarioChat(client, sessionCtx("s"), provA, "m")
		requireStatus(t, err, http.StatusTooManyRequests, "")
		requireHits(t, u, map[string]int{"sk-a1": 1, "sk-a2": 0, "sk-a3": 0})
		if keys := trailKeys(router.takeAttempts()[0].trail); len(keys) != 1 || keys[0] != "a1" {
			t.Fatalf("attempt trail %v, want a1 alone", keys)
		}
		requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "")
	})
	t.Run("(b) a1 answers 401", func(t *testing.T) {
		u, kv, router, client := setup(t)
		u.answer("sk-a1", http.StatusUnauthorized)
		served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
		requireServed(t, served, err, provA)
		keys := trailKeys(router.takeAttempts()[0].trail)
		if len(keys) != 2 || keys[0] != "a1" || keys[1] == "a1" {
			t.Fatalf("attempt trail %v, want a1 once and then a sibling", keys)
		}
		requireHits(t, u, map[string]int{"sk-a1": 1, "sk-" + keys[1]: 1})
		requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", keys[1])
	})
}

// Every key of A refuses the credential: each is tried exactly once, none twice, and nothing is
// bound. At max_retries 2 the three keys walked exceed the budget, so the provider's own 401 comes
// back; at max_retries 5 they fit within it, so the request ends as 502
// upstream_credentials_exhausted (plan row KS-08; one subtest per budget).
func TestScenarioEveryKeyRefusedIsTriedOnce(t *testing.T) {
	for _, tc := range []struct {
		maxRetries int
		status     int
		errType    string
	}{
		{maxRetries: 2, status: http.StatusUnauthorized},
		{maxRetries: 5, status: http.StatusBadGateway, errType: "upstream_credentials_exhausted"},
	} {
		t.Run(fmt.Sprintf("max_retries %d", tc.maxRetries), func(t *testing.T) {
			u := newScenarioUpstream(t)
			kv := newMockKVStore()
			router := &scenarioRouter{}
			account := pinnedRuleAccount(u)
			setMaxRetries(account, provA, tc.maxRetries)
			client := scenarioClient(t, account, router, kv)
			for _, token := range []string{"sk-a1", "sk-a2", "sk-a3"} {
				u.answer(token, http.StatusUnauthorized)
			}
			_, err := scenarioChat(client, sessionCtx("s"), provA, "m")
			requireStatus(t, err, tc.status, tc.errType)
			requireHits(t, u, map[string]int{"sk-a1": 1, "sk-a2": 1, "sk-a3": 1})
			if keys := trailKeys(router.takeAttempts()[0].trail); len(keys) != 3 {
				t.Fatalf("attempt trail %v, want each of the three keys once", keys)
			}
			requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "")
			if n := len(kv.data); n != 0 {
				t.Fatalf("a request every key refused wrote %d session entries", n)
			}
		})
	}
}

// Before a session has a key, a rate limit on the first pick rotates to another key within the same
// request, and the session binds to the key that served, never the one that refused (plan row KS-02).
func TestScenarioRateLimitBeforeABindingRotates(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	router := &scenarioRouter{}
	client := scenarioClient(t, pinnedRuleAccount(u), router, kv)
	u.answer("sk-a1", http.StatusTooManyRequests)
	rotated := false
	for i := 0; i < 200 && !rotated; i++ {
		session := fmt.Sprintf("s-%d", i)
		served, err := scenarioChat(client, sessionCtx(session), provA, "m")
		requireServed(t, served, err, provA)
		trail := router.takeAttempts()[0].trail
		bound, _ := sessionState(kv, session, SessionStateKindKey, provA, "m")
		if bound == "a1" || bound != trail[len(trail)-1].KeyID {
			t.Fatalf("the session bound %q; the key that served was %s", bound, trail[len(trail)-1].KeyID)
		}
		if trail[0].KeyID != "a1" {
			continue
		}
		rotated = true
		if len(trail) != 2 || trail[0].FailureClass != schemas.FailureClassRateLimit || !trail[0].TriggeredRotation || trail[1].KeyID == "a1" {
			t.Fatalf("a rate limit on the first pick should rotate once, trail %+v", trail)
		}
	}
	if !rotated {
		t.Fatal("no request's first pick landed on a1 in 200 tries")
	}
}

// A session's key that stops serving the model is dropped, and the sibling that serves becomes the
// binding (plan row KS-11).
func TestScenarioBoundKeyThatLeavesTheModelIsReplaced(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := pinnedRuleAccount(u)
	client := scenarioClient(t, account, nil, kv)
	bindSession(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
	a1 := scenarioKey("a1", 1)
	a1.Models = schemas.WhiteList{"m2"}
	account.SetKeysForProvider(provA, []schemas.Key{a1, scenarioKey("a2", 1), scenarioKey("a3", 1)})

	ctx := sessionCtx("s")
	served, err := scenarioChat(client, ctx, provA, "m")
	requireServed(t, served, err, provA)
	if !strings.Contains(routingLogs(ctx), "is no longer eligible") {
		t.Fatalf("the stale key binding should be reported:\n%s", routingLogs(ctx))
	}
	bound, _ := sessionState(kv, "s", SessionStateKindKey, provA, "m")
	if bound != "a2" && bound != "a3" {
		t.Fatalf("the session should rebind to the sibling that served, holds %q", bound)
	}
	requireHits(t, u, map[string]int{"sk-a1": 0, "sk-" + bound: 1})
}

// Keys a4 and a5 added to a provider after a session bound a1 leave the session on a1 for ten turns,
// while new sessions pick among all of them (plan row KS-12).
func TestScenarioKeysAddedDuringASession(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := pinnedRuleAccount(u)
	client := scenarioClient(t, account, nil, kv)
	bindSession(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
	bound := "a1"

	account.SetKeysForProvider(provA, []schemas.Key{scenarioKey("a1", 1), scenarioKey("a2", 1), scenarioKey("a3", 1), scenarioKey("a4", 1), scenarioKey("a5", 1)})
	u.clearHits()
	for range 10 {
		served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
		requireServed(t, served, err, provA)
	}
	requireHits(t, u, map[string]int{"sk-" + bound: 10})
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", bound)

	// Thirty new sessions all missing the two new keys happens once in about five million runs.
	u.clearHits()
	for i := range 30 {
		served, err := scenarioChat(client, sessionCtx(fmt.Sprintf("new-%d", i)), provA, "m")
		requireServed(t, served, err, provA)
	}
	if u.count("sk-a4")+u.count("sk-a5") == 0 {
		t.Fatal("no new session used a key added during the first session")
	}
}

// A provider with one key has nothing to choose, so the session's key level is not consulted on its
// turns. Those turns still write a key binding to the one key, once (set if absent), and a later turn
// on the single key does not consult it, so it is never refreshed. Once a second key is added, the
// session stays on that first key, as KS-12 has a session stay on its key when keys are added (plan
// row KS-13).
func TestScenarioSingleKeyProviderSkipsTheKeyLevel(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{provC: {scenarioKey("c1", 1)}})
	client := scenarioClient(t, account, nil, kv)
	for range 3 {
		ctx := sessionCtx("s")
		served, err := scenarioChat(client, ctx, provC, "m")
		requireServed(t, served, err, provC)
		if logs := routingLogs(ctx); strings.Contains(logs, "Session reused key") || strings.Contains(logs, "no longer eligible") {
			t.Fatalf("a single-key provider consulted the session's key binding:\n%s", logs)
		}
	}

	if bound, found := sessionState(kv, "s", SessionStateKindKey, provC, "m"); !found || bound != "c1" {
		t.Fatalf("the single-key turns should bind the session to c1, got %q (found %v)", bound, found)
	}

	account.SetKeysForProvider(provC, []schemas.Key{scenarioKey("c1", 1), scenarioKey("c2", 1)})
	u.clearHits()
	for range 5 {
		served, err := scenarioChat(client, sessionCtx("s"), provC, "m")
		requireServed(t, served, err, provC)
	}
	if bound, found := sessionState(kv, "s", SessionStateKindKey, provC, "m"); !found || bound != "c1" {
		t.Fatalf("after c2 is added the session should still hold c1, got %q (found %v)", bound, found)
	}
	// A free pick would leave c1 for c2 at least once in five turns 31 times in 32.
	requireHits(t, u, map[string]int{"sk-c1": 5, "sk-c2": 0})
}

// A fallback picks its key without the session, so the key binding the session already held on the
// fallback's provider is replaced by the key that served there, and the binding the request followed
// on the failed primary is dropped (plan row KS-15).
func TestScenarioFallbackReplacesTheSessionsKeyOnItsProvider(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	// b2 has weight 0, so a free pick on B lands on b1.
	client := scenarioClient(t, scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{
		provA: {scenarioKey("a1", 1), scenarioKey("a2", 1)},
		provB: {scenarioKey("b1", 1), scenarioKey("b2", 0)},
	}), &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"})}, kv)
	bindSession(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")
	bindSession(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
	bindSession(t, kv, "s", SessionStateKindKey, provB, "m", "b2")
	u.answer("sk-a1", http.StatusInternalServerError)

	served, err := scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provB)
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-b1": 1, "sk-b2": 0})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")
	requireSessionState(t, kv, "s", SessionStateKindKey, provB, "m", "b1")
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "")
}

// Both levels of stickiness through a failover and back: the session follows the fallback that
// served, keeps the key it served on, and returns to the first provider on a fresh key when the
// fallback fails in turn (plan row KS-16).
func TestScenarioStickinessThroughAFailoverAndBack(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	weighted := func() (schemas.Fallback, []schemas.Fallback) {
		a, b := schemas.Fallback{Provider: provA, Model: "m"}, schemas.Fallback{Provider: provB, Model: "m"}
		if rand.Float64() >= 1/1.5 {
			a, b = b, a
		}
		return a, []schemas.Fallback{b}
	}
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: weighted}, kv)
	bindSession(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")
	bindSession(t, kv, "s", SessionStateKindKey, provA, "m", "a2")
	turn := func() (schemas.ModelProvider, *schemas.BifrostError) {
		u.clearHits()
		return scenarioChat(client, sessionCtx("s"), "", "m")
	}

	served, err := turn()
	requireServed(t, served, err, provA)
	requireHits(t, u, map[string]int{"sk-a2": 1})

	// A=500: every key of A fails; the session's a2 is retried and never rotated.
	for _, token := range []string{"sk-a1", "sk-a2", "sk-a3"} {
		u.answer(token, http.StatusInternalServerError)
	}
	served, err = turn()
	requireServed(t, served, err, provB)
	requireHits(t, u, map[string]int{"sk-a2": 3, "sk-a1": 0, "sk-a3": 0})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "")
	bKey, _ := sessionState(kv, "s", SessionStateKindKey, provB, "m")
	if bKey == "" || u.count("sk-"+bKey) != 1 {
		t.Fatalf("B's key binding %q should be the key that served", bKey)
	}

	served, err = turn()
	requireServed(t, served, err, provB)
	requireHits(t, u, map[string]int{"sk-" + bKey: 1, "sk-a2": 0})

	// Heal A and set B=500: every key of B fails; the session's B key is retried, then A serves.
	for _, token := range []string{"sk-a1", "sk-a2", "sk-a3"} {
		u.heal(token)
	}
	for _, token := range []string{"sk-b1", "sk-b2"} {
		u.answer(token, http.StatusInternalServerError)
	}
	served, err = turn()
	requireServed(t, served, err, provA)
	otherB := "b1"
	if bKey == "b1" {
		otherB = "b2"
	}
	requireHits(t, u, map[string]int{"sk-" + bKey: 3, "sk-" + otherB: 0})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-a/m")
	aKey, _ := sessionState(kv, "s", SessionStateKindKey, provA, "m")
	if aKey == "" || u.count("sk-"+aKey) != 1 {
		t.Fatalf("A's key binding %q should be the key that served", aKey)
	}
}

// A rule target pinned to a key whose fallback is unpinned: the fallback serves on a key it picks
// freely, and both bindings follow it, so later turns start on the fallback and reuse that key
// (plan row EK-08).
func TestScenarioUnpinnedRuleFallbackKeepsItsKey(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m", KeyID: "a1"}, schemas.Fallback{Provider: provB, Model: "m"})}, kv)
	u.answer("sk-a1", http.StatusInternalServerError)

	served, err := scenarioChat(client, sessionCtx("s"), "", "m")
	requireServed(t, served, err, provB)
	requireHits(t, u, map[string]int{"sk-a1": 3, "sk-a2": 0, "sk-a3": 0})
	requireSessionState(t, kv, "s", SessionStateKindRoute, "", "m", "prov-b/m")
	bKey, _ := sessionState(kv, "s", SessionStateKindKey, provB, "m")
	if u.count("sk-"+bKey) != 1 {
		t.Fatalf("B's key binding %q should be the key that served", bKey)
	}

	for range 2 {
		u.clearHits()
		ctx := sessionCtx("s")
		served, err = scenarioChat(client, ctx, "", "m")
		requireServed(t, served, err, provB)
		requireHits(t, u, map[string]int{"sk-a1": 0, "sk-" + bKey: 1})
		if !strings.Contains(routingLogs(ctx), "Session reused key "+bKey+" for prov-b/m") {
			t.Fatalf("the turn should reuse the session's key on B:\n%s", routingLogs(ctx))
		}
	}
}

// The same rule without a session: A pinned to a2, which answers 500, and an unpinned fallback to B.
// Each of twenty requests retries a2 three times and is served by B, and with nothing to hold a key,
// B's weighted pick lands on both of its keys across the run (plan row RR-12). Twenty requests at
// equal weight all landing on one key happens about twice in a million runs.
func TestScenarioUnpinnedRuleFallbackSpreadsWithoutASession(t *testing.T) {
	u := newScenarioUpstream(t)
	client := scenarioClient(t, pinnedRuleAccount(u), &scenarioRouter{route: fixedRoute(schemas.Fallback{Provider: provA, Model: "m", KeyID: "a2"}, schemas.Fallback{Provider: provB, Model: "m"})}, nil)
	u.answer("sk-a2", http.StatusInternalServerError)
	for i := range 20 {
		info, err := scenarioChatInfo(client, sessionCtx(""), "", "m")
		requireServed(t, info.Provider, err, provB)
		if !info.IsFallback {
			t.Fatalf("request %d should be served by B as the fallback, routing info %+v", i+1, info)
		}
	}
	requireHits(t, u, map[string]int{"sk-a2": 60, "sk-a1": 0, "sk-a3": 0})
	if u.count("sk-b1") == 0 || u.count("sk-b2") == 0 || u.count("sk-b1")+u.count("sk-b2") != 20 {
		t.Fatalf("B should serve every request on a free pick across both keys, b1=%d b2=%d", u.count("sk-b1"), u.count("sk-b2"))
	}
}

// A session's key is read, never written, by the realtime and WebSocket Responses paths: HTTP turns
// bind a2, a realtime or WebSocket Responses key pick for the same session returns a2 though a free
// pick would land on a1, and the binding is unchanged afterwards, as the next HTTP turn's reuse of a2
// shows. A pick for a session with no binding writes nothing. When an HTTP turn moves the binding,
// the next realtime pick follows it (plan row KS-20).
func TestScenarioRealtimeReadsTheSessionKeyButNeverWritesIt(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	account := scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 0), scenarioKey("a2", 1), scenarioKey("a3", 0)}})
	client := scenarioClient(t, account, nil, kv)
	for range 2 {
		served, err := scenarioChat(client, sessionCtx("s"), provA, "m")
		requireServed(t, served, err, provA)
	}
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a2")

	// From here a free pick lands on a1; only the binding sends a pick to a2.
	account.SetKeysForProvider(provA, []schemas.Key{scenarioKey("a1", 1), scenarioKey("a2", 0), scenarioKey("a3", 0)})
	for _, requestType := range []schemas.RequestType{schemas.RealtimeRequest, schemas.WebSocketResponsesRequest} {
		key, err := client.SelectKeyForProviderRequestType(sessionCtx("s"), requestType, provA, "m")
		if err != nil || key.ID != "a2" {
			t.Fatalf("%s picked %q (err %v), want the session's a2", requestType, key.ID, err)
		}
		requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a2")
		before := len(kv.data)
		if key, err := client.SelectKeyForProviderRequestType(sessionCtx("fresh-"+string(requestType)), requestType, provA, "m"); err != nil || key.ID != "a1" {
			t.Fatalf("%s for a session with no binding picked %q (err %v), want the free pick a1", requestType, key.ID, err)
		}
		if after := len(kv.data); after != before {
			t.Fatalf("%s for a session with no binding wrote %d session entries", requestType, after-before)
		}
	}
	u.clearHits()
	ctx := sessionCtx("s")
	served, err := scenarioChat(client, ctx, provA, "m")
	requireServed(t, served, err, provA)
	requireHits(t, u, map[string]int{"sk-a2": 1, "sk-a1": 0})
	if !strings.Contains(routingLogs(ctx), "Session reused key a2 for prov-a/m") {
		t.Fatalf("the HTTP turn after the realtime picks should reuse a2:\n%s", routingLogs(ctx))
	}

	// An HTTP turn on which a2 is refused for good moves the binding to the sibling that serves; the
	// next realtime pick follows the new binding.
	u.answer("sk-a2", http.StatusUnauthorized)
	served, err = scenarioChat(client, sessionCtx("s"), provA, "m")
	requireServed(t, served, err, provA)
	moved, _ := sessionState(kv, "s", SessionStateKindKey, provA, "m")
	if moved == "" || moved == "a2" {
		t.Fatalf("the HTTP turn should move the binding off a2, holds %q", moved)
	}
	for _, requestType := range []schemas.RequestType{schemas.RealtimeRequest, schemas.WebSocketResponsesRequest} {
		if key, err := client.SelectKeyForProviderRequestType(sessionCtx("s"), requestType, provA, "m"); err != nil || key.ID != moved {
			t.Fatalf("%s after the switch picked %q (err %v), want the new binding %s", requestType, key.ID, err, moved)
		}
	}
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", moved)
}

// A direct key the provider refuses is the caller's own failure at every retry budget: with pool
// keys configured beside it, the provider's own 401 comes back from one attempt on the caller's key
// at max_retries 0, 1 and 3, never 502 upstream_credentials_exhausted, and no pool key is used
// (plan row DK-02; one subtest per budget).
func TestScenarioRefusedDirectKeyIsTheCallersOwn401(t *testing.T) {
	for _, maxRetries := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("max_retries %d", maxRetries), func(t *testing.T) {
			u := newScenarioUpstream(t)
			router := &scenarioRouter{}
			account := scenarioAccount(u, map[schemas.ModelProvider][]schemas.Key{provA: {scenarioKey("a1", 1), scenarioKey("a2", 1)}})
			setMaxRetries(account, provA, maxRetries)
			client := scenarioClient(t, account, router, nil)
			u.answer("sk-caller", http.StatusUnauthorized)
			_, err := scenarioChat(client, directKeyCtx(""), provA, "m")
			requireStatus(t, err, http.StatusUnauthorized, "")
			if err.Error == nil || !strings.Contains(err.Error.Message, "Incorrect API key") {
				t.Fatalf("the 401 should be the provider's own refusal, got %s", err.GetErrorString())
			}
			requireHits(t, u, map[string]int{"sk-caller": 1, "sk-a1": 0, "sk-a2": 0})
			if keys := trailKeys(router.takeAttempts()[0].trail); len(keys) != 1 {
				t.Fatalf("attempt trail %v, want the caller's key once", keys)
			}
		})
	}
}

// A session keeps one key binding per provider and model, so alternating two models of one provider
// keeps each on its own key (plan row KS-17). The session starts bound to a1 for m and to a2 for m2,
// so a binding shared across models would send one of them to the other's key.
func TestScenarioKeyBindingsArePerModel(t *testing.T) {
	u := newScenarioUpstream(t)
	kv := newMockKVStore()
	bindSession(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
	bindSession(t, kv, "s", SessionStateKindKey, provA, "m2", "a2")
	client := scenarioClient(t, pinnedRuleAccount(u), nil, kv)
	for range 6 {
		for _, model := range []string{"m", "m2"} {
			served, err := scenarioChat(client, sessionCtx("s"), provA, model)
			requireServed(t, served, err, provA)
		}
	}
	requireHits(t, u, map[string]int{
		"sk-a1|m": 6, "sk-a2|m": 0, "sk-a3|m": 0,
		"sk-a2|m2": 6, "sk-a1|m2": 0, "sk-a3|m2": 0,
	})
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m", "a1")
	requireSessionState(t, kv, "s", SessionStateKindKey, provA, "m2", "a2")
}
