package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mistralprovider "github.com/maximhq/bifrost/core/providers/mistral"
	schemas "github.com/maximhq/bifrost/core/schemas"
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

// Test that transientServerStatusCodes are properly defined.
// These are upstream-side failures unrelated to the credential — the same key is retried.
func TestTransientServerStatusCodes(t *testing.T) {
	expected := []int{500, 502, 503, 504}
	for _, code := range expected {
		if !transientServerStatusCodes[code] {
			t.Errorf("status code %d should be in transientServerStatusCodes", code)
		}
	}

	// Codes that must NOT be in transientServerStatusCodes: per-key codes (rotated, not
	// retried-same-key), success codes, and request-bound 4xx (terminal).
	notTransient := []int{200, 201, 400, 401, 402, 403, 404, 422, 429}
	for _, code := range notTransient {
		if transientServerStatusCodes[code] {
			t.Errorf("status code %d should not be in transientServerStatusCodes", code)
		}
	}
}

// Test that perKeyFailureStatusCodes are properly defined.
// These are credential/account-bound failures — rotate to the next key instead of retrying
// the same one.
func TestPerKeyFailureStatusCodes(t *testing.T) {
	expected := []int{401, 402, 403, 429}
	for _, code := range expected {
		if !perKeyFailureStatusCodes[code] {
			t.Errorf("status code %d should be in perKeyFailureStatusCodes", code)
		}
	}

	// Request-bound 4xx, success codes, and transient-server 5xx must not trigger rotation.
	notPerKey := []int{200, 201, 400, 404, 422, 500, 502, 503, 504}
	for _, code := range notPerKey {
		if perKeyFailureStatusCodes[code] {
			t.Errorf("status code %d should not be in perKeyFailureStatusCodes", code)
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

// mockOpenAIChatResponse returns a minimal valid OpenAI chat completion JSON body for use in test servers.
func mockOpenAIChatResponse(model string) []byte {
	resp := map[string]any{
		"id":     "chatcmpl-test",
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "hello"},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// safeCapture is a mutex-guarded recorder for httptest handler observations.
// Tests in this file run an httptest.Server whose handler goroutine writes
// captured request fields and then a test goroutine reads them after
// `bf.ChatCompletionRequest` returns. Today the writes happen-before the reads
// because ChatCompletionRequest blocks on the response body, but the formal
// race is enough to trip `go test -race` if the request path ever returns
// before the handler goroutine exits (e.g. streaming, async post-processing).
// safeCapture eliminates the race statically.
type safeCapture struct {
	mu         sync.Mutex
	auth       string
	host       string
	path       string
	sentinel   bool
	authSeen   []string
	attemptNum int
}

func (c *safeCapture) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.auth = r.Header.Get("Authorization")
	c.host = r.Host
	c.path = r.URL.Path
}
func (c *safeCapture) recordAuth(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.auth = r.Header.Get("Authorization")
}
func (c *safeCapture) markSentinel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sentinel = true
}
func (c *safeCapture) recordAttempt(r *http.Request) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authSeen = append(c.authSeen, r.Header.Get("Authorization"))
	c.attemptNum++
	return c.attemptNum
}
func (c *safeCapture) Auth() string   { c.mu.Lock(); defer c.mu.Unlock(); return c.auth }
func (c *safeCapture) Host() string   { c.mu.Lock(); defer c.mu.Unlock(); return c.host }
func (c *safeCapture) Path() string   { c.mu.Lock(); defer c.mu.Unlock(); return c.path }
func (c *safeCapture) Sentinel() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.sentinel }
func (c *safeCapture) AuthSeen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.authSeen...)
}

// Mock Account implementation for testing UpdateProvider
type MockAccount struct {
	mu      sync.RWMutex
	configs map[schemas.ModelProvider]*schemas.ProviderConfig
	keys    map[schemas.ModelProvider][]schemas.Key
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
			Models: schemas.WhiteList{"*"},
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
	// Return (nil, nil) to signal "not configured" — Bifrost may auto-init the provider.
	// A non-nil error is reserved for genuine lookup failures.
	return nil, nil
}

func (ma *MockAccount) GetKeysForProvider(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
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

// Test selectKeyFromProviderForModelWithPool with session stickiness
func TestSelectKeyFromProviderForModel_SessionStickiness(t *testing.T) {
	kvStore := newMockKVStore()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	// Use 2 keys so we hit the keySelector path (single key returns early)
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

	// First call: cache miss, keySelector runs, key stored; returns single-element pool (canRotate=false)
	keys1, canRotate1, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatalf("first selectKeyFromProviderForModelWithPool: %v", err)
	}
	if canRotate1 {
		t.Error("first call: canRotate should be false for session-sticky request")
	}
	if len(keys1) != 1 || keys1[0].ID != "key-a" {
		t.Errorf("first call: expected [key-a], got %v", keys1)
	}
	if keySelectorCalls != 1 {
		t.Errorf("first call: expected 1 keySelector call, got %d", keySelectorCalls)
	}

	// Verify kvstore was written
	kvKey := buildSessionKey(schemas.OpenAI, "sess-123", "gpt-4")
	if raw, err := kvStore.Get(kvKey); err != nil || raw != "key-a" {
		t.Errorf("kvstore after first call: expected key-a, got %v (err=%v)", raw, err)
	}

	// Second call: cache hit, same key returned, keySelector NOT called
	keys2, canRotate2, err := bifrost.selectKeyFromProviderForModelWithPool(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatalf("second selectKeyFromProviderForModelWithPool: %v", err)
	}
	if canRotate2 {
		t.Error("second call: canRotate should be false for session-sticky request")
	}
	if len(keys2) != 1 || keys2[0].ID != "key-a" {
		t.Errorf("second call: expected [key-a] (sticky), got %v", keys2)
	}
	if keySelectorCalls != 1 {
		t.Errorf("second call: keySelector should not run (cache hit), got %d calls", keySelectorCalls)
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
	if _, err := kvStore.Get(buildSessionKey(schemas.OpenAI, "", "gpt-4")); err == nil {
		t.Error("kvstore should not have a sticky entry for an empty session id")
	}
}

// TestSelectKeyFromProviderForModel_SessionStickinessNoRotation verifies that when a session ID
// is present, rate-limit retries reuse the sticky key rather than rotating to another key.
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
	if len(pool) != 1 || pool[0].ID != "key-a" {
		t.Fatalf("expected sticky pool=[key-a], got %v", pool)
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

	t.Run("all keys blacklist model", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{ID: "k1", Name: "K1", Value: *schemas.NewSecretVar("sk-1"), Weight: 1, BlacklistedModels: []string{"gpt-4"}},
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
			{ID: "k1", Name: "K1", Value: *schemas.NewSecretVar("sk-1"), Weight: 1, BlacklistedModels: []string{"gpt-4"}},
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

		// Verify error message -- MockAccount returns (nil, nil) for unconfigured providers
		// per the Account interface contract, so *Bifrost.UpdateProvider hits the nil-config branch.
		expectedErrMsg := "config is nil for provider anthropic"
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

// TestProviderOverride verifies that the req.UpdateAPIKey / req.UpdateProviderBaseURL /
// req.UpdateProvider methods injected by a plugin PreLLMHook cause Bifrost to use the
// supplied credential and endpoint instead of the statically configured values. This is
// the mechanism that enables data-residency routing: a plugin resolves the user's geo
// constraint from their JWT and calls the Update* methods at request time.
func TestProviderOverride(t *testing.T) {
	t.Run("KeyAndBaseURLAreOverridden", func(t *testing.T) {
		// Verifies that UpdateAPIKey and UpdateProviderBaseURL in a PreLLMHook cause Bifrost
		// to use the injected key and URL rather than the statically configured values.
		const overrideKey = "sk-override-eu-key"

		cap := &safeCapture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.record(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		// Static provider points at a sentinel URL that must NOT be called.
		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, "http://static-should-not-be-called.invalid/v1")

		// Plugin calls UpdateAPIKey + UpdateProviderBaseURL — simulating what a
		// data-residency-aware hook does after reading the user's JWT claims.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
			// BaseURL is the host root only (no path prefix); Bifrost appends "/v1/chat/completions".
			LLMPlugins: []schemas.LLMPlugin{newKeyBaseURLPlugin(overrideKey, server.URL)},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		wantAuth := "Bearer " + overrideKey
		if got := cap.Auth(); got != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", got, wantAuth)
		}
		wantHost := strings.TrimPrefix(server.URL, "http://")
		if got := cap.Host(); got != wantHost {
			t.Errorf("Host header: got %q, want %q", got, wantHost)
		}
		if got := cap.Path(); got != "/v1/chat/completions" {
			t.Errorf("URL path: got %q, want %q", got, "/v1/chat/completions")
		}
	})

	t.Run("StaticConfigUsedWhenNoOverride", func(t *testing.T) {
		// Without a plugin override the static key and URL from account config must be used.
		cap := &safeCapture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.recordAuth(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, server.URL+"/v1")

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		// Static key from MockAccount.AddProviderWithBaseURL is "sk-test-openai".
		wantAuth := "Bearer sk-test-openai"
		if got := cap.Auth(); got != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", got, wantAuth)
		}
	})

	t.Run("ProviderAutoInitWithoutConfig", func(t *testing.T) {
		// Verifies the dynamicallyConfigurableProviders fallback in getProviderQueue:
		// a standard provider (OpenAI) should be auto-initialised on first use even when
		// no static config entry exists. The plugin supplies key and URL via Update* methods.
		const overrideKey = "sk-auto-init-key"

		cap := &safeCapture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.recordAuth(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		// Empty account — no providers registered at all. MockAccount returns (nil, nil)
		// for unregistered providers so getProviderQueue can auto-init OpenAI.
		account := NewMockAccount()

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{newKeyBaseURLPlugin(overrideKey, server.URL)},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed (provider should auto-init): %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		wantAuth := "Bearer " + overrideKey
		if got := cap.Auth(); got != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", got, wantAuth)
		}
	})

	t.Run("DialectSwitchViaUpdateProvider", func(t *testing.T) {
		// Verifies that req.UpdateProvider in a PreLLMHook switches the wire dialect.
		// The incoming request declares Provider=Anthropic; the plugin calls
		// req.UpdateProvider(OpenAI), redirecting to the OpenAI queue and hitting
		// overrideServer rather than sentinelServer.
		const overrideKey = "sk-dialect-switch-key"

		sentinel := &safeCapture{}
		sentinelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sentinel.markSentinel()
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer sentinelServer.Close()

		cap := &safeCapture{}
		overrideServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.recordAuth(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer overrideServer.Close()

		// Anthropic → sentinel (must never be contacted after dialect switch).
		// OpenAI is absent from static config; MockAccount returns (nil, nil) for it
		// so getProviderQueue can auto-init the OpenAI queue.
		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.Anthropic, 2, 100, sentinelServer.URL)

		// Plugin switches provider, key, and base URL.
		plugin := newProviderSwitchPlugin(schemas.OpenAI, overrideKey, overrideServer.URL)

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-3-5-sonnet-20241022",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		if sentinel.Sentinel() {
			t.Error("Anthropic sentinel server was called; dialect was not switched to OpenAI")
		}
		wantAuth := "Bearer " + overrideKey
		if got := cap.Auth(); got != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", got, wantAuth)
		}
	})

	t.Run("StreamingKeyAndBaseURLAreOverridden", func(t *testing.T) {
		// Verifies that UpdateAPIKey and UpdateProviderBaseURL are honoured on the
		// streaming path (tryStreamRequest), not only the non-streaming path (tryRequest).
		const overrideKey = "sk-stream-override-key"

		cap := &safeCapture{}

		// Minimal OpenAI-compatible SSE response: one content chunk then [DONE].
		sseBody := "data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\"," +
			"\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"," +
			"\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\"," +
			"\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{}," +
			"\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3," +
			"\"completion_tokens\":1,\"total_tokens\":4}}\n\n" +
			"data: [DONE]\n\n"

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.record(r)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sseBody))
		}))
		defer server.Close()

		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, "http://static-should-not-be-called.invalid/v1")

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{newKeyBaseURLPlugin(overrideKey, server.URL)},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ch, bifrostErr := bf.ChatCompletionStreamRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionStreamRequest failed: %v", bifrostErr)
		}
		if ch == nil {
			t.Fatal("expected non-nil stream channel")
		}

		// Drain the channel to let the stream complete.
		var streamErr *schemas.BifrostError
		for chunk := range ch {
			if chunk.BifrostError != nil {
				streamErr = chunk.BifrostError
			}
		}
		if streamErr != nil {
			t.Fatalf("stream returned error: %v", streamErr)
		}

		wantAuth := "Bearer " + overrideKey
		if got := cap.Auth(); got != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", got, wantAuth)
		}
		wantHost := strings.TrimPrefix(server.URL, "http://")
		if got := cap.Host(); got != wantHost {
			t.Errorf("Host header: got %q, want %q", got, wantHost)
		}
		if got := cap.Path(); got != "/v1/chat/completions" {
			t.Errorf("URL path: got %q, want %q", got, "/v1/chat/completions")
		}
	})

	t.Run("OverrideKeyDisablesRotationOnRateLimit", func(t *testing.T) {
		// With three keys in the static pool, a 429 on the first attempt would normally
		// rotate to a different key on retry (canRotate=true). When a plugin injects an
		// override key via UpdateAPIKey, selectKeyFromProviderForModelWithPool must return
		// canRotate=false so every retry reuses the override key. The attempt trail
		// should record the override key ID on every attempt.
		const (
			overrideKeyID    = "override-key-id"
			overrideKeyName  = "override-key-name"
			overrideKeyValue = "sk-override-no-rotate"
		)

		const failFirstN = 2
		cap := &safeCapture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := cap.recordAttempt(r)
			if n <= failFirstN {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, server.URL+"/v1")
		// Three-key pool — would normally rotate on 429 retries.
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{ID: "pool-a", Name: "Pool A", Value: *schemas.NewSecretVar("sk-pool-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "pool-b", Name: "Pool B", Value: *schemas.NewSecretVar("sk-pool-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "pool-c", Name: "Pool C", Value: *schemas.NewSecretVar("sk-pool-c"), Models: schemas.WhiteList{"*"}, Weight: 1},
		})

		plugin := newKeyBaseURLPluginWithID(schemas.Key{
			ID:     overrideKeyID,
			Name:   overrideKeyName,
			Value:  *schemas.NewSecretVar(overrideKeyValue),
			Models: schemas.WhiteList{"*"},
			Weight: 1,
		}, server.URL)

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}

		// Every attempt (including retries) must use the override key.
		seen := cap.AuthSeen()
		if len(seen) < failFirstN+1 {
			t.Fatalf("expected at least %d attempts, got %d (%v)", failFirstN+1, len(seen), seen)
		}
		wantAuth := "Bearer " + overrideKeyValue
		for i, got := range seen {
			if got != wantAuth {
				t.Errorf("attempt %d Authorization: got %q, want %q — override key rotated on retry", i, got, wantAuth)
			}
		}

		// Attempt trail must record the override key ID on every attempt.
		trail, _ := reqCtx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
		if len(trail) != len(seen) {
			t.Errorf("attempt trail length: got %d, want %d", len(trail), len(seen))
		}
		for i, rec := range trail {
			if rec.KeyID != overrideKeyID {
				t.Errorf("trail[%d].KeyID: got %q, want %q", i, rec.KeyID, overrideKeyID)
			}
		}
	})

	t.Run("OverrideRecordedInAttemptTrail", func(t *testing.T) {
		// Single successful attempt with an override key carrying explicit ID and Name.
		// The attempt trail must record both so downstream observability (logs, traces)
		// attributes the request to the override credential rather than the static pool.
		const (
			trailKeyID    = "trail-key-id"
			trailKeyName  = "trail-key-name"
			trailKeyValue = "sk-trail-override"
		)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, "http://static-should-not-be-called.invalid/v1")

		plugin := newKeyBaseURLPluginWithID(schemas.Key{
			ID:     trailKeyID,
			Name:   trailKeyName,
			Value:  *schemas.NewSecretVar(trailKeyValue),
			Models: schemas.WhiteList{"*"},
			Weight: 1,
		}, server.URL)

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}

		trail, ok := reqCtx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
		if !ok || len(trail) == 0 {
			t.Fatalf("expected non-empty attempt trail, got %v (ok=%v)", trail, ok)
		}
		if trail[0].KeyID != trailKeyID {
			t.Errorf("trail[0].KeyID: got %q, want %q", trail[0].KeyID, trailKeyID)
		}
		if trail[0].KeyName != trailKeyName {
			t.Errorf("trail[0].KeyName: got %q, want %q", trail[0].KeyName, trailKeyName)
		}
	})

	t.Run("OverrideWinsOverPinnedAPIKey", func(t *testing.T) {
		// Mirrors the precedence established by selectKeyFromProviderForModelWithPool:
		// a per-request override injected via PreLLMHook beats an explicit
		// BifrostContextKeyAPIKeyID pin set on the request context. The override is a
		// deliberate runtime decision made after all context has been gathered.
		const overrideKeyValue = "sk-override-wins"
		const pinnedKeyID = "pinned-id"
		const pinnedKeyValue = "sk-pinned-ctx-key"

		cap := &safeCapture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.recordAuth(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, server.URL+"/v1")
		// Seed a key with a known ID so BifrostContextKeyAPIKeyID can pin it.
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{ID: pinnedKeyID, Name: "Pinned", Value: *schemas.NewSecretVar(pinnedKeyValue), Models: schemas.WhiteList{"*"}, Weight: 1},
		})

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{newKeyBaseURLPlugin(overrideKeyValue, server.URL)},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		// Pin a specific key on the request context — the override injected by the
		// plugin must still win.
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		reqCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, pinnedKeyID)

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}

		wantAuth := "Bearer " + overrideKeyValue
		if got := cap.Auth(); got != wantAuth {
			t.Errorf("Authorization header: got %q, want %q — pinned API key beat the override", got, wantAuth)
		}
	})

	t.Run("OverrideBypassesSessionStickiness", func(t *testing.T) {
		// An override is a per-request decision; it must not be cached as the sticky key
		// for the session. selectKeyFromProviderForModelWithPool returns at the top for
		// overrides, skipping the stickiness block — so the KV store stays empty for this
		// session's sticky slot, leaving future requests free to run normal selection.
		const overrideKeyValue = "sk-override-bypass-sticky"
		const sessionID = "sess-bypass-sticky"

		cap := &safeCapture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.recordAuth(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		kvStore := newMockKVStore()
		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, server.URL+"/v1")
		// Multi-key pool so that, absent the override, stickiness would run keySelector
		// and persist a sticky entry in the KV store.
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{ID: "sticky-a", Name: "Sticky A", Value: *schemas.NewSecretVar("sk-sticky-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "sticky-b", Name: "Sticky B", Value: *schemas.NewSecretVar("sk-sticky-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
		})

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			KVStore:    kvStore,
			LLMPlugins: []schemas.LLMPlugin{newKeyBaseURLPlugin(overrideKeyValue, server.URL)},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		reqCtx.SetValue(schemas.BifrostContextKeySessionID, sessionID)

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		if got := cap.Auth(); got != "Bearer "+overrideKeyValue {
			t.Errorf("Authorization header: got %q, want Bearer %s", got, overrideKeyValue)
		}

		// No sticky entry must exist for this session — the override short-circuits above
		// the stickiness block. If one exists, the override would be "sticky" and the next
		// request with the same session ID (but no override) would reuse the wrong key.
		kvKey := buildSessionKey(schemas.OpenAI, sessionID, "gpt-4o")
		if _, err := kvStore.Get(kvKey); err == nil {
			t.Error("KV store should not have a sticky entry when override is in effect")
		}
	})

	t.Run("FallbackClearsOverrideThenReselectsFromPool", func(t *testing.T) {
		// Regression guard for prepareFallbackRequest: ProviderOverride is cleared on the
		// fallback request so a fallback without its own PreLLMHook override falls back to
		// the static key pool. If the primary's override leaked into the fallback, this
		// test would see the primary override key on fallbackServer.
		const (
			primaryOverrideValue = "sk-primary-override"
		)

		primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
		}))
		defer primaryServer.Close()

		fallbackCap := &safeCapture{}
		fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fallbackCap.recordAuth(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o-mini"))
		}))
		defer fallbackServer.Close()

		// Primary is Anthropic (hit by override) — will 429. Fallback is OpenAI with
		// static config and no plugin-injected override; it must use the static key pool.
		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.Anthropic, 10, 100, "http://anthropic-static-should-not-be-called.invalid")
		account.AddProviderWithBaseURL(schemas.OpenAI, 10, 100, fallbackServer.URL+"/v1")

		plugin := &primaryOnlyOverridePlugin{key: primaryOverrideValue, baseURL: primaryServer.URL}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-3-5-sonnet-20241022",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
			Fallbacks: []schemas.Fallback{
				{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
			},
		})
		if bifrostErr != nil {
			t.Fatalf("expected fallback to succeed, got error: %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response from fallback")
		}

		// Fallback must use the static key from OpenAI config, not the primary's override.
		wantAuth := "Bearer sk-test-openai"
		got := fallbackCap.Auth()
		if got != wantAuth {
			t.Errorf("fallback Authorization: got %q, want %q — primary override leaked into fallback", got, wantAuth)
		}
		if got == "Bearer "+primaryOverrideValue {
			t.Errorf("fallback used primary override key %q; ProviderOverride was not cleared", primaryOverrideValue)
		}
	})

	t.Run("EmptyProviderResolvedByPlugin", func(t *testing.T) {
		// Verifies that a request with Provider == "" reaches the plugin pipeline
		// instead of being rejected up-front. The HTTP transport leaves Provider
		// empty when the client's model string carries no known prefix and isn't
		// in the model catalog; plugins are then expected to resolve the provider
		// via req.UpdateProvider before dispatch.
		const overrideKey = "sk-empty-provider-deferred-key"

		cap := &safeCapture{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.recordAuth(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		account := NewMockAccount()
		plugin := newProviderSwitchPlugin(schemas.OpenAI, overrideKey, server.URL)

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			// Provider intentionally empty — plugin resolves it.
			Model: "gpt-4o",
			Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest failed (empty-provider request should reach plugin): %v", bifrostErr.Error.Message)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		wantAuth := "Bearer " + overrideKey
		if got := cap.Auth(); got != wantAuth {
			t.Errorf("Authorization header: got %q, want %q (plugin did not run)", got, wantAuth)
		}
	})

	t.Run("EmptyProviderNotResolvedByPlugin_Fails", func(t *testing.T) {
		// Negative case: when the plugin pipeline does NOT set a provider, the
		// post-plugin check in tryRequest must fail loud rather than reach a
		// provider with an empty key.
		account := NewMockAccount()

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account: account,
			Logger:  NewDefaultLogger(schemas.LogLevelError),
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Model: "gpt-4o",
			Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr == nil {
			t.Fatal("expected error when no plugin sets the provider")
		}
		if !strings.Contains(bifrostErr.Error.Message, "provider is required") {
			t.Errorf("expected 'provider is required' error, got: %s", bifrostErr.Error.Message)
		}
	})

	t.Run("EmptyProviderFailureClearsProviderOverrideBeforePostHook", func(t *testing.T) {
		account := NewMockAccount()
		plugin := &providerOverridePostHookObserverPlugin{}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		reqCtx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
			BaseURL: "https://stale.example.com",
		})

		_, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Model: "gpt-4o",
			Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr == nil {
			t.Fatal("expected error when no plugin sets the provider")
		}
		if got := atomic.LoadInt32(&plugin.postCalls); got != 1 {
			t.Fatalf("PostLLMHook calls: got %d, want 1", got)
		}
		if plugin.sawProviderOverride.Load() {
			t.Fatal("PostLLMHook saw stale ProviderOverride on empty-provider failure")
		}
	})
}

// TestValidateRequest_DefersProviderCheck verifies that validateRequest does NOT
// reject empty-provider requests. The empty-provider check is intentionally
// deferred to tryRequest / tryStreamRequest so PreLLMHook plugins get a chance
// to resolve the provider via req.UpdateProvider before dispatch.
func TestValidateRequest_DefersProviderCheck(t *testing.T) {
	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}

	t.Run("empty provider passes validation", func(t *testing.T) {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Model: "gpt-4o",
				Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
			},
		}
		if err := validateRequestAfterPreRequestHooks(req); err != nil {
			t.Errorf("validateRequest rejected empty-provider request: %v", err.Error.Message)
		}
	})

	t.Run("missing model still rejected", func(t *testing.T) {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
			},
		}
		err := validateRequestAfterPreRequestHooks(req)
		if err == nil {
			t.Fatal("validateRequest accepted missing model — model check regressed")
		}
		if !strings.Contains(err.Error.Message, ModelAutoResolveErrorMessage) {
			t.Errorf("expected %q, got: %s", ModelAutoResolveErrorMessage, err.Error.Message)
		}
	})

	t.Run("nil request rejected", func(t *testing.T) {
		err := validateRequestAfterPreRequestHooks(nil)
		if err == nil {
			t.Fatal("validateRequest accepted nil request")
		}
		if !strings.Contains(err.Error.Message, "bifrost request cannot be nil") {
			t.Errorf("unexpected error message: %s", err.Error.Message)
		}
	})
}

// keyBaseURLPlugin is a test helper plugin that injects a static API key and base URL
// via req.UpdateAPIKey and req.UpdateProviderBaseURL on every request.
type keyBaseURLPlugin struct {
	key, baseURL string
}

func newKeyBaseURLPlugin(key, baseURL string) *keyBaseURLPlugin {
	return &keyBaseURLPlugin{key: key, baseURL: baseURL}
}

func (p *keyBaseURLPlugin) GetName() string { return "key-base-url-test-plugin" }
func (p *keyBaseURLPlugin) Cleanup() error  { return nil }
func (p *keyBaseURLPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *keyBaseURLPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewSecretVar(p.key)})
	req.UpdateProviderBaseURL(p.baseURL)
	return req, nil, nil
}
func (p *keyBaseURLPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

type providerOverridePostHookObserverPlugin struct {
	postCalls           int32
	sawProviderOverride atomic.Bool
}

func (p *providerOverridePostHookObserverPlugin) GetName() string {
	return "provider-override-post-hook-observer-test-plugin"
}
func (p *providerOverridePostHookObserverPlugin) Cleanup() error { return nil }
func (p *providerOverridePostHookObserverPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *providerOverridePostHookObserverPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *providerOverridePostHookObserverPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	atomic.AddInt32(&p.postCalls, 1)
	if override, ok := ctx.Value(schemas.BifrostContextKeyProviderOverride).(*schemas.ProviderOverride); ok && override != nil {
		p.sawProviderOverride.Store(true)
	}
	return resp, err, nil
}

// keyBaseURLPluginWithID is like keyBaseURLPlugin but injects a full Key struct (with
// explicit ID/Name) so tests can assert on attempt trail entries.
type keyBaseURLPluginWithID struct {
	key     schemas.Key
	baseURL string
}

func newKeyBaseURLPluginWithID(key schemas.Key, baseURL string) *keyBaseURLPluginWithID {
	return &keyBaseURLPluginWithID{key: key, baseURL: baseURL}
}

func (p *keyBaseURLPluginWithID) GetName() string { return "key-base-url-with-id-test-plugin" }
func (p *keyBaseURLPluginWithID) Cleanup() error  { return nil }
func (p *keyBaseURLPluginWithID) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *keyBaseURLPluginWithID) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	req.UpdateAPIKey(p.key)
	req.UpdateProviderBaseURL(p.baseURL)
	return req, nil, nil
}
func (p *keyBaseURLPluginWithID) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// primaryOnlyOverridePlugin injects override credentials only on the primary attempt
// (FallbackIndex == 0). On fallback attempts it leaves the request unchanged so the
// fallback provider's static key pool is used.
type primaryOnlyOverridePlugin struct {
	key, baseURL string
}

func (p *primaryOnlyOverridePlugin) GetName() string { return "primary-only-override-test-plugin" }
func (p *primaryOnlyOverridePlugin) Cleanup() error  { return nil }
func (p *primaryOnlyOverridePlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *primaryOnlyOverridePlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	idx, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
	if idx == 0 {
		req.UpdateAPIKey(schemas.Key{Value: *schemas.NewSecretVar(p.key)})
		req.UpdateProviderBaseURL(p.baseURL)
	}
	return req, nil, nil
}
func (p *primaryOnlyOverridePlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// providerSwitchPlugin is a test helper plugin that switches the provider dialect and
// injects credentials, using UpdateProvider, UpdateAPIKey, and UpdateProviderBaseURL.
type providerSwitchPlugin struct {
	provider     schemas.ModelProvider
	key, baseURL string
}

func newProviderSwitchPlugin(provider schemas.ModelProvider, key, baseURL string) *providerSwitchPlugin {
	return &providerSwitchPlugin{provider: provider, key: key, baseURL: baseURL}
}

func (p *providerSwitchPlugin) GetName() string { return "provider-switch-test-plugin" }
func (p *providerSwitchPlugin) Cleanup() error  { return nil }
func (p *providerSwitchPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *providerSwitchPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if err := req.UpdateProvider(p.provider); err != nil {
		return nil, nil, err
	}
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewSecretVar(p.key)})
	req.UpdateProviderBaseURL(p.baseURL)
	return req, nil, nil
}
func (p *providerSwitchPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// TestFallbackToUnconfiguredProvider verifies that prepareFallbackRequest allows
// dynamically-configurable providers (e.g. OpenAI, Anthropic) to be used as fallback
// destinations even when they have no static config entry. A plugin injects credentials
// via req.UpdateProvider / UpdateAPIKey / UpdateProviderBaseURL when it detects FallbackIndex > 0.
//
// Before the fix, prepareFallbackRequest called GetConfigForProvider and bailed out for
// any provider not in the static config, so the fallback was silently skipped and the
// primary error was returned instead. After the fix, dynamically-configurable providers
// are let through and getProviderQueue auto-initialises them.
func TestFallbackToUnconfiguredProvider(t *testing.T) {
	const fallbackKey = "sk-fallback-openai-key"

	// primaryServer always returns 429 to force fallback routing.
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer primaryServer.Close()

	// fallbackServer accepts the request and returns a valid response.
	fallbackCap := &safeCapture{}
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCap.recordAuth(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mockOpenAIChatResponse("gpt-4o-mini"))
	}))
	defer fallbackServer.Close()

	// Account has only the primary (Anthropic) configured. The fallback (OpenAI) is absent,
	// so GetConfigForProvider("openai") returns (nil, nil) — Bifrost must auto-init it.
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.Anthropic, 10, 100, primaryServer.URL)

	// plugin swaps in fallback credentials via ProviderOverride when FallbackIndex > 0.
	plugin := &fallbackOverridePlugin{
		fallbackKey:     fallbackKey,
		fallbackBaseURL: fallbackServer.URL,
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{plugin},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-3-5-sonnet-20241022",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	})

	if bifrostErr != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", bifrostErr)
	}
	if resp == nil {
		t.Fatal("expected non-nil response from fallback")
	}
	wantAuth := "Bearer " + fallbackKey
	if got := fallbackCap.Auth(); got != wantAuth {
		t.Errorf("fallback Authorization header: got %q, want %q", got, wantAuth)
	}
}

// TestFallbacksAddedByPreLLMHookAreRetried verifies request-scoped fallbacks
// added by plugin hooks during the primary attempt are used by the core
// fallback loop. Production's provider-chain plugin builds fallbacks this way:
// the inbound request has no static fallback list, then PreLLMHook resolves
// tenant provider config and attaches the fallbacks before dispatch.
func TestFallbacksAddedByPreLLMHookAreRetried(t *testing.T) {
	const (
		primaryModel  = "gpt-4o"
		fallbackModel = "gpt-4o-mini"
	)

	t.Run("NonStreaming", func(t *testing.T) {
		var primaryHits, fallbackHits atomic.Int32
		primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream 503","type":"server_error"}}`))
		}))
		defer primaryServer.Close()

		fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fallbackHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse(fallbackModel))
		}))
		defer fallbackServer.Close()

		bf := initDynamicFallbackBifrost(t, primaryModel, fallbackModel, primaryServer.URL, fallbackServer.URL)

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    primaryModel,
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})

		if bifrostErr != nil {
			t.Fatalf("expected dynamically attached fallback to succeed, got error: %+v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil response from dynamically attached fallback")
		}
		if primaryHits.Load() == 0 {
			t.Error("primary server was never hit")
		}
		if fallbackHits.Load() == 0 {
			t.Error("fallback server was never hit; dynamically attached fallbacks were not iterated")
		}
	})

	t.Run("Streaming", func(t *testing.T) {
		const fallbackSSE = "data: {\"id\":\"chatcmpl-fb\",\"object\":\"chat.completion.chunk\"," +
			"\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"," +
			"\"content\":\"hello-from-dynamic-fallback\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-fb\",\"object\":\"chat.completion.chunk\"," +
			"\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{}," +
			"\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"

		var primaryHits, fallbackHits atomic.Int32
		primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream 503","type":"server_error"}}`))
		}))
		defer primaryServer.Close()

		fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fallbackHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fallbackSSE))
		}))
		defer fallbackServer.Close()

		bf := initDynamicFallbackBifrost(t, primaryModel, fallbackModel, primaryServer.URL, fallbackServer.URL)

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ch, bifrostErr := bf.ChatCompletionStreamRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    primaryModel,
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("expected dynamically attached stream fallback to succeed, got error: %+v", bifrostErr)
		}
		if ch == nil {
			t.Fatal("expected non-nil stream channel from dynamically attached fallback")
		}

		var got strings.Builder
		for chunk := range ch {
			if chunk.BifrostError != nil {
				t.Fatalf("stream returned terminal error instead of fallback content: %+v", chunk.BifrostError)
			}
			if chunk.BifrostChatResponse == nil {
				continue
			}
			for _, choice := range chunk.BifrostChatResponse.Choices {
				if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil && choice.ChatStreamResponseChoice.Delta.Content != nil {
					got.WriteString(*choice.ChatStreamResponseChoice.Delta.Content)
				}
			}
		}
		if got.String() != "hello-from-dynamic-fallback" {
			t.Errorf("stream content: got %q, want %q", got.String(), "hello-from-dynamic-fallback")
		}
		if primaryHits.Load() == 0 {
			t.Error("primary server was never hit")
		}
		if fallbackHits.Load() == 0 {
			t.Error("fallback server was never hit; dynamically attached stream fallbacks were not iterated")
		}
	})
}

func initDynamicFallbackBifrost(t *testing.T, primaryModel, fallbackModel, primaryURL, fallbackURL string) *Bifrost {
	t.Helper()

	account := NewMockAccount()
	plugin := &dynamicFallbackPlugin{
		fallbackModel: fallbackModel,
		byModel: map[string]modelRoute{
			primaryModel:  {key: "sk-primary", baseURL: primaryURL},
			fallbackModel: {key: "sk-fallback", baseURL: fallbackURL},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{plugin},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })
	return bf
}

type dynamicFallbackPlugin struct {
	fallbackModel string
	byModel       map[string]modelRoute
}

func (p *dynamicFallbackPlugin) GetName() string { return "dynamic-fallback-test-plugin" }
func (p *dynamicFallbackPlugin) Cleanup() error  { return nil }

func (p *dynamicFallbackPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *dynamicFallbackPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	idx, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
	if idx == 0 && req.ChatRequest != nil {
		replacement := req.Clone()
		replacement.ChatRequest.Fallbacks = []schemas.Fallback{{
			Provider: schemas.OpenAI,
			Model:    p.fallbackModel,
		}}
		req = &replacement
	}

	_, model, _ := req.GetRequestFields()
	route, ok := p.byModel[model]
	if !ok {
		return req, nil, nil
	}
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewSecretVar(route.key)})
	req.UpdateProviderBaseURL(route.baseURL)
	return req, nil, nil
}

func (p *dynamicFallbackPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// TestBifrostRequestClone verifies that Clone returns an independent copy: mutations
// to scalar fields (Provider, Model) and ProviderOverride on the clone do not affect
// the original, and vice versa.
func TestBifrostRequestClone(t *testing.T) {
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-original")}
	original := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-3-5-sonnet-20241022",
		},
		ProviderOverride: &schemas.ProviderOverride{Key: &key, BaseURL: "https://original.example.com"},
	}

	clone := original.Clone()

	// Mutate clone provider, model, and override pointer
	clone.SetProvider(schemas.OpenAI)
	_ = clone.UpdateModel("gpt-4o")
	clone.ProviderOverride = nil

	// Original provider and model must be unchanged
	origP, origM, _ := original.GetRequestFields()
	if origP != schemas.Anthropic {
		t.Errorf("original provider mutated: got %s, want %s", origP, schemas.Anthropic)
	}
	if origM != "claude-3-5-sonnet-20241022" {
		t.Errorf("original model mutated: got %s, want %s", origM, "claude-3-5-sonnet-20241022")
	}

	// Original ProviderOverride pointer must still be set
	if original.ProviderOverride == nil {
		t.Error("original ProviderOverride was nilled through clone")
	}

	// Clone must have the new values
	cloneP, cloneM, _ := clone.GetRequestFields()
	if cloneP != schemas.OpenAI {
		t.Errorf("clone provider not updated: got %s, want %s", cloneP, schemas.OpenAI)
	}
	if cloneM != "gpt-4o" {
		t.Errorf("clone model not updated: got %s, want %s", cloneM, "gpt-4o")
	}
	if clone.ProviderOverride != nil {
		t.Error("clone ProviderOverride should be nil after explicit assignment")
	}

	// Verify that mutating a ProviderOverride struct field on the clone does not
	// affect the original (Clone deep-copies the ProviderOverride struct itself).
	clone2 := original.Clone()
	clone2.ProviderOverride.BaseURL = "https://mutated.example.com"
	if original.ProviderOverride.BaseURL == "https://mutated.example.com" {
		t.Error("original ProviderOverride.BaseURL was mutated through clone")
	}
	if clone2.ProviderOverride.BaseURL != "https://mutated.example.com" {
		t.Errorf("clone2 ProviderOverride.BaseURL not updated: got %s", clone2.ProviderOverride.BaseURL)
	}
}

// TestPrepareFallbackRequest_DoesNotAliasOriginal is a regression test for the shallow-copy
// bug: without Clone, SetProvider/SetModel on a fallback request would mutate the shared
// inner struct pointer and corrupt subsequent fallback preparations from the same original.
func TestPrepareFallbackRequest_DoesNotAliasOriginal(t *testing.T) {
	account := NewMockAccount()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	const originalModel = "claude-3-5-sonnet-20241022"
	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	req := &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    originalModel,
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		},
	}

	// First fallback: explicit model
	fb0 := bf.prepareFallbackRequest(req, schemas.Fallback{Provider: schemas.OpenAI, Model: "gpt-4o"})
	p0, m0, _ := fb0.GetRequestFields()
	if p0 != schemas.OpenAI || m0 != "gpt-4o" {
		t.Errorf("fallback[0]: got (%s, %s), want (%s, gpt-4o)", p0, m0, schemas.OpenAI)
	}

	// Original must be untouched after first fallback preparation
	origP, origM, _ := req.GetRequestFields()
	if origP != schemas.Anthropic || origM != originalModel {
		t.Errorf("original mutated by fallback[0]: got (%s, %s), want (%s, %s)", origP, origM, schemas.Anthropic, originalModel)
	}

	// Second fallback: empty model -- must preserve original model, not inherit gpt-4o
	fb1 := bf.prepareFallbackRequest(req, schemas.Fallback{Provider: schemas.Gemini, Model: ""})
	p1, m1, _ := fb1.GetRequestFields()
	if p1 != schemas.Gemini {
		t.Errorf("fallback[1] provider: got %s, want %s", p1, schemas.Gemini)
	}
	if m1 != originalModel {
		t.Errorf("fallback[1] model: got %q, want %q (must not inherit fallback[0] model)", m1, originalModel)
	}
}

// fallbackOverridePlugin is a minimal LLMPlugin that injects per-request provider
// credentials via the req.Update* methods. On the primary attempt (FallbackIndex == 0)
// it does nothing — the request uses static account config. On fallback attempts it
// calls UpdateProvider, UpdateAPIKey, and UpdateProviderBaseURL for the fallback provider.
type fallbackOverridePlugin struct {
	fallbackKey     string
	fallbackBaseURL string
}

func (p *fallbackOverridePlugin) GetName() string { return "fallback-override-test-plugin" }
func (p *fallbackOverridePlugin) Cleanup() error  { return nil }

func (p *fallbackOverridePlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *fallbackOverridePlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	idx, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
	if idx > 0 {
		if err := req.UpdateProvider(schemas.OpenAI); err != nil {
			return nil, nil, err
		}
		req.UpdateAPIKey(schemas.Key{Value: *schemas.NewSecretVar(p.fallbackKey)})
		req.UpdateProviderBaseURL(p.fallbackBaseURL)
	}
	return req, nil, nil
}

func (p *fallbackOverridePlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
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

// TestPluginPipelineStreamingRace exercises accumulatePluginTiming, FinalizeStreamingPostHookSpans,
// resetPluginPipeline, and GetChunkCount concurrently to catch a regression of the
// "concurrent map read and map write" panic that previously surfaced in production. Running
// `go test -race` will fail fast on any reintroduced race. Restored from upstream commit
// 5cbcff978 (test fixes #3004) — the original move from core/internal/plugins/ into this file
// was dropped during the upstream merge.
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

// extraFieldsClobberPlugin returns BRAND NEW BifrostResponse / BifrostError values from
// PostLLMHook with empty ExtraFields. Bifrost's contract is that it re-populates
// RequestType/Provider/OriginalModelRequested/ResolvedModelUsed AFTER each plugin run, so
// callers always see populated ExtraFields regardless of what the plugin returns.
//
// This is the test counterpart of the regression that was silently introduced during the
// upstream merge: dropped `PopulateExtraFields(...)` calls after `RunPostLLMHooks` left
// callers staring at empty Provider/Model fields whenever a plugin substituted a fresh
// response or error. These tests pin the behaviour for the success path, the cancellation
// branch, and the streaming path.
type extraFieldsClobberPlugin struct {
	wantErrorMessage string
}

func (p *extraFieldsClobberPlugin) GetName() string { return "extra-fields-clobber-test-plugin" }
func (p *extraFieldsClobberPlugin) Cleanup() error  { return nil }
func (p *extraFieldsClobberPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *extraFieldsClobberPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *extraFieldsClobberPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if err != nil {
		// Replace with a fresh BifrostError that has empty ExtraFields; Bifrost must
		// re-populate them before returning to the caller.
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: p.wantErrorMessage},
		}, nil
	}
	if resp != nil {
		// Replace with a fresh BifrostResponse with empty ExtraFields.
		return &schemas.BifrostResponse{
			ChatResponse: resp.ChatResponse,
		}, nil, nil
	}
	return resp, err, nil
}

// TestPostLLMHook_PopulatesExtraFieldsOnReplacedResponse pins the rule that when a plugin
// returns a NEW BifrostResponse from PostLLMHook (with empty ExtraFields), the caller still
// receives Provider/Model/RequestType populated. Catches dropped post-hook
// PopulateExtraFields lines in `tryRequest`.
func TestPostLLMHook_PopulatesExtraFieldsOnReplacedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, server.URL+"/v1")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{&extraFieldsClobberPlugin{}},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	resp, bifrostErr := bf.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ExtraFields.Provider != schemas.OpenAI {
		t.Errorf("Provider: got %q, want %q (plugin returned fresh response with empty ExtraFields — Bifrost must re-populate)", resp.ExtraFields.Provider, schemas.OpenAI)
	}
	if resp.ExtraFields.OriginalModelRequested != "gpt-4o" {
		t.Errorf("OriginalModelRequested: got %q, want %q", resp.ExtraFields.OriginalModelRequested, "gpt-4o")
	}
	if resp.ExtraFields.RequestType != schemas.ChatCompletionRequest {
		t.Errorf("RequestType: got %q, want %q", resp.ExtraFields.RequestType, schemas.ChatCompletionRequest)
	}
}

// TestPostLLMHook_PopulatesExtraFieldsOnReplacedError pins the rule that when a plugin
// returns a NEW BifrostError from PostLLMHook (with empty ExtraFields), the caller still
// receives Provider/Model/RequestType populated. Catches dropped post-hook
// PopulateExtraFields lines on the error branch of `tryRequest`.
func TestPostLLMHook_PopulatesExtraFieldsOnReplacedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	defer server.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 2, 100, server.URL+"/v1")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{&extraFieldsClobberPlugin{wantErrorMessage: "plugin-replaced-error"}},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	_, bifrostErr := bf.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
	})
	if bifrostErr == nil {
		t.Fatal("expected error from upstream 500")
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "plugin-replaced-error" {
		t.Fatalf("expected plugin-replaced error message, got %+v", bifrostErr.Error)
	}
	if bifrostErr.ExtraFields.Provider != schemas.OpenAI {
		t.Errorf("Provider: got %q, want %q (plugin returned fresh error with empty ExtraFields — Bifrost must re-populate)", bifrostErr.ExtraFields.Provider, schemas.OpenAI)
	}
	if bifrostErr.ExtraFields.OriginalModelRequested != "gpt-4o" {
		t.Errorf("OriginalModelRequested: got %q, want %q", bifrostErr.ExtraFields.OriginalModelRequested, "gpt-4o")
	}
	if bifrostErr.ExtraFields.RequestType != schemas.ChatCompletionRequest {
		t.Errorf("RequestType: got %q, want %q", bifrostErr.ExtraFields.RequestType, schemas.ChatCompletionRequest)
	}
}

// TestResetBifrostRequest_ClearsAllFields uses reflection to verify that resetBifrostRequest
// clears every pointer/string/typed field on BifrostRequest. This guards against future
// additions to BifrostRequest that aren't matched by a corresponding clear in
// resetBifrostRequest — including, critically, ProviderOverride, whose leak across pooled
// requests would expose one tenant's API key to another. New fields trip the test
// automatically; the developer must update resetBifrostRequest to make it pass.
func TestResetBifrostRequest_ClearsAllFields(t *testing.T) {
	// Populate every accessible field with a non-zero sentinel so we can detect any
	// field that resetBifrostRequest forgets to clear.
	req := &schemas.BifrostRequest{
		RequestType:                  schemas.ChatCompletionRequest,
		ProviderOverride:             &schemas.ProviderOverride{Key: &schemas.Key{ID: "leak-canary"}, BaseURL: "https://leak.example"},
		ListModelsRequest:            &schemas.BifrostListModelsRequest{},
		TextCompletionRequest:        &schemas.BifrostTextCompletionRequest{},
		ChatRequest:                  &schemas.BifrostChatRequest{},
		ResponsesRequest:             &schemas.BifrostResponsesRequest{},
		CountTokensRequest:           &schemas.BifrostResponsesRequest{},
		EmbeddingRequest:             &schemas.BifrostEmbeddingRequest{},
		RerankRequest:                &schemas.BifrostRerankRequest{},
		OCRRequest:                   &schemas.BifrostOCRRequest{},
		SpeechRequest:                &schemas.BifrostSpeechRequest{},
		TranscriptionRequest:         &schemas.BifrostTranscriptionRequest{},
		ImageGenerationRequest:       &schemas.BifrostImageGenerationRequest{},
		ImageEditRequest:             &schemas.BifrostImageEditRequest{},
		ImageVariationRequest:        &schemas.BifrostImageVariationRequest{},
		VideoGenerationRequest:       &schemas.BifrostVideoGenerationRequest{},
		VideoRetrieveRequest:         &schemas.BifrostVideoRetrieveRequest{},
		VideoDownloadRequest:         &schemas.BifrostVideoDownloadRequest{},
		VideoListRequest:             &schemas.BifrostVideoListRequest{},
		VideoRemixRequest:            &schemas.BifrostVideoRemixRequest{},
		VideoDeleteRequest:           &schemas.BifrostVideoDeleteRequest{},
		FileUploadRequest:            &schemas.BifrostFileUploadRequest{},
		FileListRequest:              &schemas.BifrostFileListRequest{},
		FileRetrieveRequest:          &schemas.BifrostFileRetrieveRequest{},
		FileDeleteRequest:            &schemas.BifrostFileDeleteRequest{},
		FileContentRequest:           &schemas.BifrostFileContentRequest{},
		BatchCreateRequest:           &schemas.BifrostBatchCreateRequest{},
		BatchListRequest:             &schemas.BifrostBatchListRequest{},
		BatchRetrieveRequest:         &schemas.BifrostBatchRetrieveRequest{},
		BatchCancelRequest:           &schemas.BifrostBatchCancelRequest{},
		BatchDeleteRequest:           &schemas.BifrostBatchDeleteRequest{},
		BatchResultsRequest:          &schemas.BifrostBatchResultsRequest{},
		ContainerCreateRequest:       &schemas.BifrostContainerCreateRequest{},
		ContainerListRequest:         &schemas.BifrostContainerListRequest{},
		ContainerRetrieveRequest:     &schemas.BifrostContainerRetrieveRequest{},
		ContainerDeleteRequest:       &schemas.BifrostContainerDeleteRequest{},
		ContainerFileCreateRequest:   &schemas.BifrostContainerFileCreateRequest{},
		ContainerFileListRequest:     &schemas.BifrostContainerFileListRequest{},
		ContainerFileRetrieveRequest: &schemas.BifrostContainerFileRetrieveRequest{},
		ContainerFileContentRequest:  &schemas.BifrostContainerFileContentRequest{},
		ContainerFileDeleteRequest:   &schemas.BifrostContainerFileDeleteRequest{},
		PassthroughRequest:           &schemas.BifrostPassthroughRequest{},
	}

	resetBifrostRequest(req)

	v := reflect.ValueOf(req).Elem()
	tt := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := tt.Field(i).Name
		if !f.CanInterface() {
			continue
		}
		if !f.IsZero() {
			t.Errorf("resetBifrostRequest left field %q non-zero (value=%v) — every BifrostRequest field must be cleared so a pooled request cannot leak state across tenants", name, f.Interface())
		}
	}
}

// TestGetOverrideKey_NilPaths pins the contract that getOverrideKey returns
// (nil, nil) — never a non-nil error — when the context is nil, when no
// ProviderOverride is set, and when an override exists but has no Key. The
// upstream key-pool selectors fall back to normal pool-based selection when
// these paths return nil; if any of them ever returned a non-nil err for
// "no override," every plugin-less request would fail.
func TestGetOverrideKey_NilPaths(t *testing.T) {
	// nil context
	if k, err := getOverrideKey(nil, schemas.OpenAI); k != nil || err != nil {
		t.Errorf("nil ctx: got (%v, %v), want (nil, nil)", k, err)
	}

	// context without any override
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if k, err := getOverrideKey(ctx, schemas.OpenAI); k != nil || err != nil {
		t.Errorf("ctx without override: got (%v, %v), want (nil, nil)", k, err)
	}

	// context with ProviderOverride present but no Key (BaseURL-only override)
	ctxBaseURLOnly := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctxBaseURLOnly.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
		BaseURL: "https://eu.example",
	})
	if k, err := getOverrideKey(ctxBaseURLOnly, schemas.OpenAI); k != nil || err != nil {
		t.Errorf("ctx with BaseURL-only override: got (%v, %v), want (nil, nil)", k, err)
	}
}

// TestGetOverrideKey_ValidatesKey pins the contract that getOverrideKey runs
// validateKey on the override key before returning, surfacing per-provider
// invariant failures (e.g. Vertex's required VertexKeyConfig) as errors rather
// than letting an unvalidated key slip through to the provider layer. Without
// this, a plugin that injects a malformed key for a strict provider would
// produce a downstream nil-deref at request time.
func TestGetOverrideKey_ValidatesKey(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	// Vertex requires VertexKeyConfig; an override key without it is invalid.
	ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
		Key: &schemas.Key{ID: "missing-vertex-config"},
	})
	k, err := getOverrideKey(ctx, schemas.Vertex)
	if err == nil {
		t.Fatalf("expected validateKey to reject Vertex override without VertexKeyConfig, got key=%v err=nil", k)
	}
	if k != nil {
		t.Errorf("expected nil key on validation failure, got %v", k)
	}
}

// TestGetOverrideKey_DoesNotMutateCallerKey pins that validateKey's
// normalisation side-effects (e.g. materialising an empty BedrockKeyConfig) are
// applied to the returned copy only — the plugin's original Key struct must be
// untouched, since it may be reused across requests.
func TestGetOverrideKey_DoesNotMutateCallerKey(t *testing.T) {
	original := &schemas.Key{ID: "bedrock-irsa"}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyProviderOverride, &schemas.ProviderOverride{
		Key: original,
	})

	k, err := getOverrideKey(ctx, schemas.Bedrock)
	if err != nil {
		t.Fatalf("Bedrock override should validate (BedrockKeyConfig is optional): %v", err)
	}
	if k == nil {
		t.Fatal("expected non-nil validated key")
	}
	// Returned copy should have BedrockKeyConfig materialised.
	if k.BedrockKeyConfig == nil {
		t.Errorf("expected validateKey to materialise BedrockKeyConfig on returned copy")
	}
	// Original must remain untouched so the plugin can reuse it across requests.
	if original.BedrockKeyConfig != nil {
		t.Errorf("validateKey mutated the caller's original Key — getOverrideKey must operate on a copy")
	}
}

// TestBifrostRequest_Clone_ProviderOverrideIndependence pins that Clone produces
// a request whose ProviderOverride pointer is distinct from the original's, so
// prepareFallbackRequest's `clone.ProviderOverride = nil` cannot accidentally
// nil the original's override mid-flight.
func TestBifrostRequest_Clone_ProviderOverrideIndependence(t *testing.T) {
	original := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"},
		ProviderOverride: &schemas.ProviderOverride{
			BaseURL: "https://eu.example",
		},
	}

	clone := original.Clone()

	if clone.ProviderOverride == original.ProviderOverride {
		t.Fatal("Clone returned a request whose ProviderOverride is the same pointer as the original — fallbacks would corrupt the primary request's override")
	}
	if clone.ProviderOverride == nil {
		t.Fatal("Clone dropped ProviderOverride entirely")
	}
	if clone.ProviderOverride.BaseURL != original.ProviderOverride.BaseURL {
		t.Errorf("BaseURL: clone has %q, original %q — values must be copied by value", clone.ProviderOverride.BaseURL, original.ProviderOverride.BaseURL)
	}

	// Mutating the clone must not affect the original.
	clone.ProviderOverride.BaseURL = "https://mutated"
	if original.ProviderOverride.BaseURL == "https://mutated" {
		t.Errorf("mutation on clone leaked into original — Clone must deep-copy ProviderOverride")
	}
}

// postHookObserverPlugin records whether PreLLMHook and PostLLMHook were each
// invoked and captures the post-hook's view of the BifrostError. Used to pin
// that error paths after pre-hooks ran (such as queue-resolution failures) still
// route through the plugin pipeline rather than short-circuiting and skipping
// the symmetric post-hook callback.
type postHookObserverPlugin struct {
	preCalls    int32
	postCalls   int32
	lastPostErr atomic.Pointer[schemas.BifrostError]
}

func (p *postHookObserverPlugin) GetName() string { return "post-hook-observer-test-plugin" }
func (p *postHookObserverPlugin) Cleanup() error  { return nil }
func (p *postHookObserverPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *postHookObserverPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	atomic.AddInt32(&p.preCalls, 1)
	return req, nil, nil
}
func (p *postHookObserverPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	atomic.AddInt32(&p.postCalls, 1)
	p.lastPostErr.Store(err)
	return resp, err, nil
}

// captureFlagPlugin records the bifrost-written capture flag from the request
// context as observed inside PostLLMHook. requestWorker writes
// BifrostContextKeyCaptureRawRequest before provider dispatch based on the
// effective send-back/store decision, so this is the closest in-process probe
// for what requestWorker decided.
type captureFlagPlugin struct {
	captureReqSeen   atomic.Bool
	captureReqValue  atomic.Bool
	captureRespValue atomic.Bool
}

func (p *captureFlagPlugin) GetName() string { return "capture-flag-test-plugin" }
func (p *captureFlagPlugin) Cleanup() error  { return nil }
func (p *captureFlagPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *captureFlagPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *captureFlagPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if v, ok := ctx.Value(schemas.BifrostContextKeyCaptureRawRequest).(bool); ok {
		p.captureReqSeen.Store(true)
		p.captureReqValue.Store(v)
	}
	if v, ok := ctx.Value(schemas.BifrostContextKeyCaptureRawResponse).(bool); ok {
		p.captureRespValue.Store(v)
	}
	return resp, err, nil
}

// TestPerRequestRawOverride_GatedByAllowFlag pins the security contract that
// requestWorker honours the per-request `x-bf-send-back-raw-request` /
// `x-bf-send-back-raw-response` overrides ONLY when the operator opted in via
// AllowPerRequestRawOverride (propagated as BifrostContextKeyAllowPerRequestRawOverride
// by the transport). Without this gate, any client that sets the override
// header gets raw provider request bytes back even when the operator has
// disabled per-request overrides in ClientConfig — a credential / data
// exposure risk. Upstream commit 54afe9e7f added the gate; our merge dropped
// it; this test catches the regression.
func TestPerRequestRawOverride_GatedByAllowFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
	}))
	defer server.Close()

	doRequest := func(t *testing.T, allowOverride bool) *captureFlagPlugin {
		t.Helper()
		account := NewMockAccount()
		account.AddProviderWithBaseURL(schemas.OpenAI, 1, 10, server.URL+"/v1")
		observer := &captureFlagPlugin{}

		initCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(initCtx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{observer},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		// Per-request override always set; only the gate flag varies.
		ctx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)
		ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
		if allowOverride {
			ctx.SetValue(schemas.BifrostContextKeyAllowPerRequestRawOverride, true)
		}

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		_, bifrostErr := bf.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("unexpected error: %+v", bifrostErr)
		}
		if !observer.captureReqSeen.Load() {
			t.Fatal("PostLLMHook never observed BifrostContextKeyCaptureRawRequest — requestWorker did not run")
		}
		return observer
	}

	t.Run("OverrideIgnoredWhenGateOff", func(t *testing.T) {
		o := doRequest(t, false)
		if o.captureReqValue.Load() {
			t.Error("CaptureRawRequest was true even though AllowPerRequestRawOverride was unset — gate is missing, per-request override leaked through")
		}
		if o.captureRespValue.Load() {
			t.Error("CaptureRawResponse was true even though AllowPerRequestRawOverride was unset — gate is missing")
		}
	})

	t.Run("OverrideHonoredWhenGateOn", func(t *testing.T) {
		o := doRequest(t, true)
		if !o.captureReqValue.Load() {
			t.Error("CaptureRawRequest was false even though AllowPerRequestRawOverride was set — gate is over-restrictive, opt-in not honoured")
		}
		if !o.captureRespValue.Load() {
			t.Error("CaptureRawResponse was false even though AllowPerRequestRawOverride was set")
		}
	})
}

// TestPostHookRunsForQueueResolutionFailure pins that when getProviderQueue
// fails after RunLLMPreHooks already executed (e.g. an unconfigured provider
// alias), the failure is routed through RunPostLLMHooks before being returned
// to the caller. Without this, plugins that rely on pre/post pairing — logging,
// accounting, request-state cleanup — silently leak in-flight state when the
// gateway can't even allocate a queue for the request. Catches regressions
// where the early `return nil, bifrostErr` from the queue-resolution branch
// bypasses the post-hook + plugin-log-drain sequence used by every other
// terminal error path in tryRequest/tryStreamRequest.
func TestPostHookRunsForQueueResolutionFailure(t *testing.T) {
	account := NewMockAccount()
	observer := &postHookObserverPlugin{}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{observer},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	// Use a non-standard provider name with no config — getProviderQueue will
	// fail with "config is nil for provider <name>" because the name isn't in
	// dynamicallyConfigurableProviders.
	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	_, bifrostErr := bf.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
		Provider: schemas.ModelProvider("never-configured-alias"),
		Model:    "any-model",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
	})
	if bifrostErr == nil {
		t.Fatal("expected queue-resolution failure for unconfigured provider")
	}

	if got := atomic.LoadInt32(&observer.preCalls); got != 1 {
		t.Fatalf("PreLLMHook calls: got %d, want 1 — observer must have run pre-hook before queue-resolution", got)
	}
	if got := atomic.LoadInt32(&observer.postCalls); got != 1 {
		t.Errorf("PostLLMHook calls: got %d, want 1 — queue-resolution failure must route through post-hooks so plugins observing pre/post pairs see the terminal error", got)
	}
	if observer.lastPostErr.Load() == nil {
		t.Errorf("PostLLMHook saw no error — the bifrostErr from getProviderQueue must be passed to plugins")
	}
}

// TestPostHookRunsForEnqueueDrop pins that when a request fails *during enqueue*
// — not just at queue resolution — the failure is still routed through
// RunPostLLMHooks. The pq.done / queue-full / ctx-cancelled-while-waiting
// branches all fire after RunLLMPreHooks has already executed; without this
// pairing, plugins that rely on pre/post symmetry (logging, accounting) leak
// in-flight state and plugin logs are dropped on backpressure.
//
// We exercise the queue-full + DropExcessRequests=true branch because it's
// the only enqueue-time failure that's deterministically reachable from a
// test: concurrency=1 + bufferSize=0 + a hanging handler guarantees the
// second concurrent request finds no worker and an unbuffered channel,
// hitting the `default:` arm of the outer enqueue select.
func TestPostHookRunsForEnqueueDrop(t *testing.T) {
	// Block the worker indefinitely so the queue-send for a second request finds
	// no receiver. Cleanup order matters because the worker is hung in the
	// handler at test end: close(release) must run first so the handler returns
	// and frees its connection, then server.Close, then bf.Shutdown. Since
	// t.Cleanup is LIFO, register them in reverse.
	release := make(chan struct{})
	hit := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
	}))

	account := NewMockAccount()
	// Concurrency=1 (one worker) + bufferSize=0 (unbuffered queue) so the second
	// request reliably hits the default branch in the enqueue select.
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 0, server.URL+"/v1")
	observer := &postHookObserverPlugin{}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:            account,
		Logger:             NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins:         []schemas.LLMPlugin{observer},
		DropExcessRequests: true,
	})
	if err != nil {
		server.Close()
		t.Fatalf("Init failed: %v", err)
	}
	// LIFO: registered last → runs first.
	t.Cleanup(func() { bf.Shutdown() })
	t.Cleanup(func() { server.Close() })
	t.Cleanup(func() { close(release) })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	mkReq := func() *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		}
	}

	// First request: launched in a goroutine because the handler hangs forever.
	// We only need it to reach the worker so subsequent enqueues see no receiver.
	go func() {
		_, _ = bf.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), mkReq())
	}()
	select {
	case <-hit:
	case <-time.After(2 * time.Second):
		t.Fatal("first request never reached the handler — cannot reliably exercise queue-full path")
	}

	// At this point the worker is parked inside the handler; the request channel
	// is unbuffered and has no receiver. The next request must hit the queue-full
	// drop branch in the enqueue select.
	preBefore := atomic.LoadInt32(&observer.preCalls)
	postBefore := atomic.LoadInt32(&observer.postCalls)

	_, bifrostErr := bf.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), mkReq())
	if bifrostErr == nil {
		t.Fatal("expected request-dropped error from queue-full enqueue")
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "queue is full") {
		t.Errorf("error message: got %+v, want it to mention 'queue is full'", bifrostErr.Error)
	}

	// Pre-hook ran for the dropped request; post-hook must have run too so the
	// pre/post pair is symmetric.
	if got := atomic.LoadInt32(&observer.preCalls) - preBefore; got != 1 {
		t.Fatalf("PreLLMHook calls for dropped request: got %d, want 1", got)
	}
	if got := atomic.LoadInt32(&observer.postCalls) - postBefore; got != 1 {
		t.Errorf("PostLLMHook calls for dropped request: got %d, want 1 — enqueue-time failures must route through post-hooks so plugins observing pre/post pairs see the terminal error", got)
	}
	if observer.lastPostErr.Load() == nil {
		t.Errorf("PostLLMHook saw no error — the queue-full bifrostErr must be passed to plugins")
	}
}

// nilRequestPlugin nils out the BifrostRequest in PreLLMHook to drive the
// "preReq == nil after plugin hooks" branch of tryRequest/tryStreamRequest.
// It also records PostLLMHook invocations so a test can pin that the
// nil-request error is routed through the plugin pipeline rather than
// returned directly to the caller.
type nilRequestPlugin struct {
	preCalls    int32
	postCalls   int32
	lastPostErr atomic.Pointer[schemas.BifrostError]
}

func (p *nilRequestPlugin) GetName() string { return "nil-request-test-plugin" }
func (p *nilRequestPlugin) Cleanup() error  { return nil }
func (p *nilRequestPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *nilRequestPlugin) PreLLMHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	atomic.AddInt32(&p.preCalls, 1)
	return nil, nil, nil
}
func (p *nilRequestPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	atomic.AddInt32(&p.postCalls, 1)
	p.lastPostErr.Store(err)
	return resp, err, nil
}

// TestPostHookRunsWhenPreHookNilsRequest pins that when a pre-hook returns
// (nil, nil, nil) — leaving preReq nil after the plugin pipeline — the
// resulting "request after plugin hooks cannot be nil" error is still routed
// through RunPostLLMHooks, matching every other terminal error path in
// tryRequest/tryStreamRequest. Without this routing, plugins that rely on
// pre/post symmetry (logging, accounting, request-state cleanup) silently
// leak in-flight state when a pre-hook nils the request.
func TestPostHookRunsWhenPreHookNilsRequest(t *testing.T) {
	t.Run("ChatCompletion", func(t *testing.T) {
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 1, 10)
		niller := &nilRequestPlugin{}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{niller},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		_, bifrostErr := bf.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr == nil {
			t.Fatal("expected nil-preReq error after pre-hook nilled the request")
		}
		if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "cannot be nil") {
			t.Errorf("error message: got %+v, want it to mention nil request after plugin hooks", bifrostErr.Error)
		}
		if got := atomic.LoadInt32(&niller.preCalls); got != 1 {
			t.Fatalf("PreLLMHook calls: got %d, want 1", got)
		}
		if got := atomic.LoadInt32(&niller.postCalls); got != 1 {
			t.Errorf("PostLLMHook calls: got %d, want 1 — nil-preReq error must route through post-hooks so plugins observing pre/post pairs see the terminal error", got)
		}
		if niller.lastPostErr.Load() == nil {
			t.Errorf("PostLLMHook saw no error — the nil-preReq bifrostErr must be passed to plugins")
		}
	})

	t.Run("ChatCompletionStream", func(t *testing.T) {
		account := NewMockAccount()
		account.AddProvider(schemas.OpenAI, 1, 10)
		niller := &nilRequestPlugin{}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{niller},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		ch, bifrostErr := bf.ChatCompletionStreamRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		// The pre-hook nils the request so the streaming path returns the
		// "cannot be nil" error. A post-hook may convert the error into a
		// response, but the contract this test pins is post-hook invocation,
		// not what the post-hook returns.
		if bifrostErr == nil && ch == nil {
			t.Fatal("expected either an error or a stream channel from streaming path")
		}
		if bifrostErr != nil && (bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "cannot be nil")) {
			t.Errorf("error message: got %+v, want it to mention nil request after plugin hooks", bifrostErr.Error)
		}
		if got := atomic.LoadInt32(&niller.preCalls); got != 1 {
			t.Fatalf("PreLLMHook calls: got %d, want 1", got)
		}
		if got := atomic.LoadInt32(&niller.postCalls); got != 1 {
			t.Errorf("PostLLMHook calls: got %d, want 1 — nil-preReq error in streaming path must route through post-hooks", got)
		}
		if niller.lastPostErr.Load() == nil {
			t.Errorf("PostLLMHook saw no error — the nil-preReq bifrostErr must be passed to plugins")
		}
	})
}

// TestConcurrentStreamingPostHookRunnerNoChannelMessageRace exercises the
// streaming postHookRunner closure under concurrent traffic with the race
// detector enabled. The closure captures the worker's *ChannelMessage by
// reference; that pointer is returned to channelMessagePool as soon as
// tryStreamRequest picks up the stream channel from msg.ResponseStream, so a
// later getChannelMessage call can overwrite msg.BifrostRequest (and therefore
// msg.RequestType) while the provider goroutine is still emitting chunks and
// invoking the closure. Reading req.RequestType inside the closure races with
// that overwrite. This test runs many concurrent streams, each producing
// several chunks with small artificial latency, so `go test -race` reliably
// observes the read/write conflict if the per-attempt snapshot is dropped.
func TestConcurrentStreamingPostHookRunnerNoChannelMessageRace(t *testing.T) {
	// Build a long SSE body (many chunks) so each stream's provider goroutine
	// stays alive emitting chunks long enough for *other* concurrent requests
	// to recycle the same pooled ChannelMessage out of sync.Pool.
	const chunksPerStream = 40
	var sseB strings.Builder
	for i := 0; i < chunksPerStream; i++ {
		sseB.WriteString("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\"," +
			"\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"," +
			"\"content\":\"x\"},\"finish_reason\":null}]}\n\n")
	}
	sseB.WriteString("data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\"," +
		"\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{}," +
		"\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3," +
		"\"completion_tokens\":1,\"total_tokens\":4}}\n\n" +
		"data: [DONE]\n\n")
	sseBody := sseB.String()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		// Flush between chunks with a small delay so each stream's provider
		// goroutine remains active for several milliseconds, widening the
		// window in which another request's getChannelMessage can recycle
		// the pooled ChannelMessage and overwrite its BifrostRequest.
		flusher, _ := w.(http.Flusher)
		for _, line := range strings.SplitAfter(sseBody, "\n\n") {
			if line == "" {
				continue
			}
			_, _ = w.Write([]byte(line))
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(500 * time.Microsecond)
		}
	}))
	defer server.Close()

	account := NewMockAccount()
	// Multiple workers + multi-slot buffer so requests overlap and reuse
	// pooled ChannelMessages while other streams are still emitting chunks.
	account.AddProviderWithBaseURL(schemas.OpenAI, 4, 16, server.URL+"/v1")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
		// Tiny pool forces sync.Pool to recycle the same physical
		// *ChannelMessage across concurrent requests, which is what makes
		// the race observable. With the default pool size (5000), 5000
		// fresh objects mask the race entirely.
		InitialPoolSize: 2,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	const concurrent = 64
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
			reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ch, bifrostErr := bf.ChatCompletionStreamRequest(reqCtx, &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
			})
			if bifrostErr != nil {
				t.Errorf("ChatCompletionStreamRequest failed: %v", bifrostErr)
				return
			}
			if ch == nil {
				t.Error("expected non-nil stream channel")
				return
			}
			for chunk := range ch {
				// Each chunk's ExtraFields.RequestType must match the request
				// type of the request that started this stream, not whatever
				// other concurrent request happened to recycle the pooled
				// ChannelMessage. If the snapshot is dropped, recycled writes
				// to req.BifrostRequest race with these reads.
				if chunk.BifrostChatResponse != nil &&
					chunk.BifrostChatResponse.ExtraFields.RequestType != schemas.ChatCompletionStreamRequest {
					t.Errorf("chunk RequestType: got %q, want %q",
						chunk.BifrostChatResponse.ExtraFields.RequestType,
						schemas.ChatCompletionStreamRequest)
				}
			}
		}()
	}
	wg.Wait()
}

// TestStreamFallback_PrimaryReturns503_FallsOverToFallback verifies that the
// streaming entry point's fallback path actually fires when the primary provider
// returns an HTTP-level 503 (status check before any SSE chunk is emitted) and,
// as a separate subtest, when the primary returns 200 with the first SSE chunk
// being a Bifrost-shaped error (the path that CheckFirstStreamChunkForError
// exists to handle). Both subtests must end up calling the fallback server and
// returning fallback chunks to the caller. If either subtest fails, the bug is
// in core (handleStreamRequest / shouldTryFallbacks / tryStreamRequest) rather
// than in any caller's plugin or runtime config.
func TestStreamFallback_PrimaryReturns503_FallsOverToFallback(t *testing.T) {
	// Minimal OpenAI-compatible SSE response used by every fallback server.
	const fallbackSSE = "data: {\"id\":\"chatcmpl-fb\",\"object\":\"chat.completion.chunk\"," +
		"\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"," +
		"\"content\":\"hello-from-fallback\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-fb\",\"object\":\"chat.completion.chunk\"," +
		"\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{}," +
		"\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3," +
		"\"completion_tokens\":1,\"total_tokens\":4}}\n\n" +
		"data: [DONE]\n\n"

	// drainStream collects both content text from chunks and any terminal stream
	// error so each subtest can assert content == fallback and err == nil.
	drainStream := func(t *testing.T, ch chan *schemas.BifrostStreamChunk) (string, *schemas.BifrostError) {
		t.Helper()
		var content strings.Builder
		var streamErr *schemas.BifrostError
		for chunk := range ch {
			if chunk.BifrostError != nil {
				streamErr = chunk.BifrostError
				continue
			}
			if chunk.BifrostChatResponse == nil {
				continue
			}
			for _, c := range chunk.BifrostChatResponse.Choices {
				if c.ChatStreamResponseChoice != nil && c.ChatStreamResponseChoice.Delta != nil && c.ChatStreamResponseChoice.Delta.Content != nil {
					content.WriteString(*c.ChatStreamResponseChoice.Delta.Content)
				}
			}
		}
		return content.String(), streamErr
	}

	t.Run("HTTPLevel503", func(t *testing.T) {
		// Primary returns HTTP 503 immediately. ParseOpenAIError will surface this as
		// a *BifrostError to the worker, which routes via req.Err -> tryStreamRequest
		// -> handleStreamRequest -> shouldTryFallbacks (true) -> fallback attempt.
		var primaryHits, fallbackHits atomic.Int32
		primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream 503","type":"server_error"}}`))
		}))
		defer primaryServer.Close()

		fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fallbackHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fallbackSSE))
		}))
		defer fallbackServer.Close()

		// Primary and fallback attempts both ride the auto-initialised openai queue;
		// the plugin injects a per-attempt key and base URL keyed on the requested
		// model. This mirrors the per-org fallback chain shape that production uses
		// (PLATFORM-44): a plugin resolves provider config per attempt and points
		// each attempt at its own upstream via ProviderOverride.
		account := NewMockAccount()

		plugin := &perModelRoutePlugin{
			byModel: map[string]modelRoute{
				"gpt-4o":      {key: "sk-primary", baseURL: primaryServer.URL},
				"gpt-4o-mini": {key: "sk-fallback", baseURL: fallbackServer.URL},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ch, bifrostErr := bf.ChatCompletionStreamRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
			Fallbacks: []schemas.Fallback{
				{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
			},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionStreamRequest returned error before stream — fallback never engaged: %+v", bifrostErr)
		}
		if ch == nil {
			t.Fatal("expected non-nil stream channel from fallback")
		}

		got, streamErr := drainStream(t, ch)
		if streamErr != nil {
			t.Fatalf("stream returned terminal error instead of fallback content: %+v", streamErr)
		}
		if got != "hello-from-fallback" {
			t.Errorf("stream content: got %q, want %q (primary 503 should have triggered fallback)", got, "hello-from-fallback")
		}
		if primaryHits.Load() == 0 {
			t.Error("primary server was never hit")
		}
		if fallbackHits.Load() == 0 {
			t.Error("fallback server was never hit — fallback path did not fire on HTTP 503")
		}
	})

	t.Run("SSEEmbedded503AsFirstChunk", func(t *testing.T) {
		// Primary returns 200 OK, then the first SSE event is a Bifrost-shaped error
		// payload. Bifrost's HandleOpenAIChatCompletionStreaming parses non-data lines
		// it cannot decode as chat.completion.chunk into a streaming error chunk, and
		// CheckFirstStreamChunkForError converts it to a synchronous *BifrostError so
		// fallbacks can run. We use OpenAI's "error" SSE shape which the provider
		// recognises and emits as a stream error.
		var primaryHits, fallbackHits atomic.Int32
		primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			// First (and only) data event is an OpenAI-style error payload.
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"sse-embedded 503\",\"type\":\"server_error\",\"code\":\"503\"}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}))
		defer primaryServer.Close()

		fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fallbackHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fallbackSSE))
		}))
		defer fallbackServer.Close()

		account := NewMockAccount()
		plugin := &perModelRoutePlugin{
			byModel: map[string]modelRoute{
				"gpt-4o":      {key: "sk-primary", baseURL: primaryServer.URL},
				"gpt-4o-mini": {key: "sk-fallback", baseURL: fallbackServer.URL},
			},
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{plugin},
		})
		if err != nil {
			t.Fatalf("Init failed: %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ch, bifrostErr := bf.ChatCompletionStreamRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
			Fallbacks: []schemas.Fallback{
				{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
			},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionStreamRequest returned error before stream — fallback never engaged: %+v", bifrostErr)
		}
		if ch == nil {
			t.Fatal("expected non-nil stream channel from fallback")
		}

		got, streamErr := drainStream(t, ch)
		if streamErr != nil {
			t.Fatalf("stream returned terminal error instead of fallback content: %+v", streamErr)
		}
		if got != "hello-from-fallback" {
			t.Errorf("stream content: got %q, want %q (SSE-embedded error should have triggered fallback)", got, "hello-from-fallback")
		}
		if primaryHits.Load() == 0 {
			t.Error("primary server was never hit")
		}
		if fallbackHits.Load() == 0 {
			t.Error("fallback server was never hit — fallback path did not fire on SSE-embedded error")
		}
	})
}

// modelRoute describes how a single attempt should be routed: which API key to
// inject and which upstream base URL to send the request to. Used by
// perModelRoutePlugin and dynamicFallbackPlugin to point primary and fallback
// attempts at different httptest servers. extraHeaders, when set, is attached
// as a per-request NetworkConfig override.
type modelRoute struct {
	key          string
	baseURL      string
	extraHeaders map[string]string
}

// perModelRoutePlugin injects an API key and base URL via ProviderOverride for
// any request whose model matches one of the registered routes. This is the
// minimum machinery needed to point primary and fallback attempts at different
// httptest servers without adding the provider to the static account config
// (which would make the test exercise a different code path than the
// production "dynamically configurable provider" fallback fix).
type perModelRoutePlugin struct {
	byModel map[string]modelRoute
}

func (p *perModelRoutePlugin) GetName() string { return "per-model-route-test-plugin" }
func (p *perModelRoutePlugin) Cleanup() error  { return nil }

func (p *perModelRoutePlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *perModelRoutePlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	_, model, _ := req.GetRequestFields()
	route, ok := p.byModel[model]
	if !ok {
		return req, nil, nil
	}
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewSecretVar(route.key)})
	req.UpdateProviderBaseURL(route.baseURL)
	if route.extraHeaders != nil {
		req.UpdateProviderNetworkConfig(schemas.ProviderNetworkConfigOverride{ExtraHeaders: route.extraHeaders})
	}
	return req, nil, nil
}

func (p *perModelRoutePlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// TestAutoInitDoesNotAbsorbPerRequestNetworkOverride is a regression test for
// the auto-init config pollution bug: the FIRST request to an unconfigured
// provider triggers getProviderQueue's auto-init path, and that request's
// per-request NetworkConfig override must not be merged into the provider
// config that prepareProvider stores permanently. A later request without an
// override must not inherit the first request's settings.
func TestAutoInitDoesNotAbsorbPerRequestNetworkOverride(t *testing.T) {
	const overrideHeader = "X-Tenant-Override"

	var mu sync.Mutex
	headersByAuth := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headersByAuth[r.Header.Get("Authorization")] = r.Header.Get(overrideHeader)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
	}))
	defer server.Close()

	// Empty account — the openai queue is auto-initialised by the first request,
	// which carries a NetworkConfig override (the polluting scenario).
	account := NewMockAccount()
	plugin := &perModelRoutePlugin{
		byModel: map[string]modelRoute{
			"gpt-4o":      {key: "sk-first", baseURL: server.URL, extraHeaders: map[string]string{overrideHeader: "tenant-a"}},
			"gpt-4o-mini": {key: "sk-second", baseURL: server.URL},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{plugin},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	for _, model := range []string{"gpt-4o", "gpt-4o-mini"} {
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if _, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		}); bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest(%s) failed: %v", model, bifrostErr)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if got := headersByAuth["Bearer sk-first"]; got != "tenant-a" {
		t.Errorf("first request %s header = %q, want %q (per-request override must be applied)", overrideHeader, got, "tenant-a")
	}
	if got := headersByAuth["Bearer sk-second"]; got != "" {
		t.Errorf("second request %s header = %q, want empty — the first request's override leaked into the auto-initialised provider config", overrideHeader, got)
	}
}

// deadOverrideKeyPlugin injects a fixed per-request override key, points the
// request at the given base URL, and raises MaxRetries to 1 via a per-request
// NetworkConfig override.
type deadOverrideKeyPlugin struct {
	baseURL string
}

func (p *deadOverrideKeyPlugin) GetName() string { return "dead-override-key-test-plugin" }
func (p *deadOverrideKeyPlugin) Cleanup() error  { return nil }
func (p *deadOverrideKeyPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *deadOverrideKeyPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	req.UpdateAPIKey(schemas.Key{ID: "ovr-dead-key", Value: *schemas.NewSecretVar("sk-revoked")})
	req.UpdateProviderBaseURL(p.baseURL)
	maxRetries := 1
	req.UpdateProviderNetworkConfig(schemas.ProviderNetworkConfigOverride{MaxRetries: &maxRetries})
	return req, nil, nil
}
func (p *deadOverrideKeyPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// TestOverrideKey_DeadKeySurfaces502CredentialsExhausted pins the interaction
// between the per-request override key and upstream's dead-key rotation: the
// override key flows into the fixed-key (canRotate=false) keyProvider branch,
// and when the upstream rejects it with 401 the retry loop must surface the
// synthetic 502 upstream_credentials_exhausted instead of the raw 401, without
// hitting the upstream a second time.
//
// The test doubles as coverage for the per-request MaxRetries override applied
// via effectiveConfig in executeRequestWithRetries: DefaultMaxRetries is 0, so
// without the override taking effect there is no second attempt and the raw 401
// (not 502) would be returned.
func TestOverrideKey_DeadKeySurfaces502CredentialsExhausted(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer server.Close()

	account := NewMockAccount()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{&deadOverrideKeyPlugin{baseURL: server.URL}},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
	})
	if bifrostErr == nil {
		t.Fatal("expected error for revoked override key, got success")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 502 {
		t.Fatalf("status code = %v, want 502 upstream_credentials_exhausted (raw 401 means the dead-key path or the per-request MaxRetries override did not engage)", bifrostErr.StatusCode)
	}
	if bifrostErr.Type == nil || *bifrostErr.Type != "upstream_credentials_exhausted" {
		t.Fatalf("error type = %v, want upstream_credentials_exhausted", bifrostErr.Type)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 — the dead key must short-circuit in the keyProvider, not burn another attempt against the upstream", got)
	}
}

// perProviderTimeoutConfig is the per-provider slice of a pushed provider
// config that matters for timeout routing: where the provider lives and how
// long this tenant is willing to wait for it. Mirrors the gateway's
// org-provider-config shape (base_url + default_request_timeout_in_seconds).
type perProviderTimeoutConfig struct {
	baseURL        string
	timeoutSeconds int
}

// providerConfigTimeoutPlugin plays the gateway interceptor's role: on every
// request it looks up the request's provider in its config table and injects
// that provider's credentials, BaseURL, and per-request timeout — exactly what
// injectProviderCreds does with a config fetched from Redis.
type providerConfigTimeoutPlugin struct {
	configs map[schemas.ModelProvider]perProviderTimeoutConfig
}

func (p *providerConfigTimeoutPlugin) GetName() string { return "provider-config-timeout-test-plugin" }
func (p *providerConfigTimeoutPlugin) Cleanup() error  { return nil }
func (p *providerConfigTimeoutPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *providerConfigTimeoutPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	provider, _, _ := req.GetRequestFields()
	cfg, ok := p.configs[provider]
	if !ok {
		return req, nil, nil
	}
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewSecretVar("sk-timeout-test")})
	req.UpdateProviderBaseURL(cfg.baseURL)
	timeoutSeconds := cfg.timeoutSeconds
	req.UpdateProviderNetworkConfig(schemas.ProviderNetworkConfigOverride{RequestTimeoutInSeconds: &timeoutSeconds})
	return req, nil, nil
}
func (p *providerConfigTimeoutPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// newHangingUpstream returns a mock LLM upstream that never answers on its
// own: it parks until the CLIENT gives up and drops the connection (the
// request context cancels), then returns immediately. This proves the
// per-request deadline actively closes the connection at the timeout — the
// mock holds no connection longer than the timeout under test — and keeps
// server.Close() from waiting on sleeping handlers.
func newHangingUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body first: net/http only starts the background
		// read that detects a client disconnect once the body is consumed, and
		// r.Context() never cancels without it.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			// Client disconnected (deadline fired) — release the handler.
		case <-time.After(30 * time.Second):
			t.Error("upstream handler still connected 30s in; the per-request deadline never closed the connection")
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// timeoutProbeResult is one provider's observed outcome for the assertions in
// TestPerRequestTimeoutOverride_TwoProvidersDistinctTimeouts.
type timeoutProbeResult struct {
	err     *schemas.BifrostError
	elapsed time.Duration
}

// TestPerRequestTimeoutOverride_TwoProvidersDistinctTimeouts pins the
// pushed-provider-config timeout contract end to end with TWO providers
// carrying DIFFERENT timeouts through one Bifrost instance: a plugin (standing
// in for the gateway interceptor) maps each request's provider to its own
// config — openai at 1s, anthropic at 2s — and each request must surface a
// 504 at ITS OWN provider's deadline. Both auto-initialised providers share
// nothing per-tenant: the shared clients keep the transport ceiling
// (DynamicProviderTransportTimeoutCeilingInSeconds) and the per-provider
// timeout rides each request. The upstreams release their handlers on client
// disconnect, so neither mock holds a connection past its provider's timeout.
func TestPerRequestTimeoutOverride_TwoProvidersDistinctTimeouts(t *testing.T) {
	openaiUpstream := newHangingUpstream(t)
	anthropicUpstream := newHangingUpstream(t)

	const (
		openaiTimeoutSeconds    = 1
		anthropicTimeoutSeconds = 2
	)

	account := NewMockAccount()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{&providerConfigTimeoutPlugin{
			configs: map[schemas.ModelProvider]perProviderTimeoutConfig{
				schemas.OpenAI:    {baseURL: openaiUpstream.URL, timeoutSeconds: openaiTimeoutSeconds},
				schemas.Anthropic: {baseURL: anthropicUpstream.URL, timeoutSeconds: anthropicTimeoutSeconds},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	probe := func(provider schemas.ModelProvider, model string) timeoutProbeResult {
		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		start := time.Now()
		_, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    model,
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		return timeoutProbeResult{err: bifrostErr, elapsed: time.Since(start)}
	}

	// Fire both providers concurrently: total wall clock is the slower
	// provider's timeout (~2s), and concurrency doubles as proof the two
	// in-flight overrides don't bleed into each other.
	var openaiResult, anthropicResult timeoutProbeResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); openaiResult = probe(schemas.OpenAI, "gpt-4o") }()
	go func() { defer wg.Done(); anthropicResult = probe(schemas.Anthropic, "claude-sonnet-4-6") }()
	wg.Wait()

	assertTimedOutAt := func(name string, result timeoutProbeResult, timeoutSeconds int) {
		t.Helper()
		if result.err == nil {
			t.Fatalf("%s: expected timeout error from %ds override against hanging upstream, got success", name, timeoutSeconds)
		}
		if result.err.StatusCode == nil || *result.err.StatusCode != 504 {
			t.Fatalf("%s: status = %v (err=%+v), want 504 from per-request timeout", name, result.err.StatusCode, result.err.Error)
		}
		want := time.Duration(timeoutSeconds) * time.Second
		if result.elapsed < want-100*time.Millisecond {
			t.Fatalf("%s: returned after %v, before its own %v deadline — a different provider's timeout leaked in", name, result.elapsed, want)
		}
		if result.elapsed > want+900*time.Millisecond {
			t.Fatalf("%s: returned after %v; the %v per-request deadline did not bound the call", name, result.elapsed, want)
		}
	}
	assertTimedOutAt("openai@1s", openaiResult, openaiTimeoutSeconds)
	assertTimedOutAt("anthropic@2s", anthropicResult, anthropicTimeoutSeconds)
}

// findPrivateIPv4 returns a non-loopback RFC 1918 IPv4 address bound to a
// local interface. Loopback can't exercise the dial-time private-IP policy
// (ConfigureDialer always allows it), so tests that pin RFC 1918 behaviour
// must bind a real private address — every dev machine and CI worker has one.
func findPrivateIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs failed: %v", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
	}
	t.Fatal("no RFC 1918 IPv4 interface address found — cannot exercise the private-IP dial policy")
	return ""
}

// TestAutoInitDynamicProviderAllowsPrivateNetworkUpstreams pins the dial
// policy for dynamic deployments: auto-initialised providers MUST be able to
// reach RFC 1918 upstreams (in-cluster inference services are the legitimate
// case), because the shared per-type client cannot carry per-tenant dial
// policy and config-write time is where tenant SSRF policy belongs. Upstream
// #3947's private-IP dial block broke this in deploy (gateway smoketests
// failed with "connection to private IP 10.48.x.x is not allowed") — loopback
// is always allowed, so httptest-on-127.0.0.1 tests never caught it. This
// test binds the mock upstream to the host's real private address.
// Link-local (cloud metadata endpoints) remains always-blocked regardless.
func TestAutoInitDynamicProviderAllowsPrivateNetworkUpstreams(t *testing.T) {
	privateIP := findPrivateIPv4(t)
	listener, err := net.Listen("tcp", net.JoinHostPort(privateIP, "0"))
	if err != nil {
		t.Fatalf("cannot listen on private address %s: %v", privateIP, err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		})},
	}
	server.Start()
	t.Cleanup(server.Close)

	account := NewMockAccount()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{&providerConfigTimeoutPlugin{
			configs: map[schemas.ModelProvider]perProviderTimeoutConfig{
				schemas.OpenAI: {baseURL: server.URL, timeoutSeconds: 10},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
	})
	if bifrostErr != nil {
		msg := ""
		if bifrostErr.Error != nil {
			msg = bifrostErr.Error.Message
		}
		if strings.Contains(msg, "private IP") {
			t.Fatalf("auto-init provider blocked the RFC 1918 upstream %s — AllowPrivateNetwork is not set on the auto-init config: %s", server.URL, msg)
		}
		t.Fatalf("request to private upstream %s failed: %+v", server.URL, bifrostErr)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("expected a completion from the private upstream, got empty response")
	}
}

// TestPerRequestTimeoutOverride_DynamicProvider pins the single-provider
// contract: the auto-initialised provider's shared HTTP client is built with
// the transport ceiling (DynamicProviderTransportTimeoutCeilingInSeconds), and
// the tenant's real timeout — injected by a plugin via
// UpdateProviderNetworkConfig — must surface a 504 in roughly 1 second, NOT
// wait for the upstream or the transport ceiling.
func TestPerRequestTimeoutOverride_DynamicProvider(t *testing.T) {
	server := newHangingUpstream(t)

	account := NewMockAccount()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bf, err := Init(ctx, schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{&providerConfigTimeoutPlugin{
			configs: map[schemas.ModelProvider]perProviderTimeoutConfig{
				schemas.OpenAI: {baseURL: server.URL, timeoutSeconds: 1},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { bf.Shutdown() })

	content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
	reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	start := time.Now()
	_, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
	})
	elapsed := time.Since(start)

	if bifrostErr == nil {
		t.Fatal("expected timeout error from 1s per-request override against hanging upstream, got success")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 504 {
		t.Fatalf("status = %v (err=%+v), want 504 from per-request timeout", bifrostErr.StatusCode, bifrostErr.Error)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("request returned after %v; the 1s per-request deadline did not bound the call", elapsed)
	}
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

// TestClearAnthropicPassthroughForNonNativeProvider verifies that Anthropic raw-body
// passthrough flags are cleared only when an Anthropic-integration request resolves to a
// provider that doesn't speak the Anthropic Messages API natively (e.g. Bedrock). This
// guards the fix for Claude-via-Bedrock tool calls breaking when the model is routed to
// Bedrock through a key alias (so the catalog-time guard never fires).
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
		wantCleared     bool
	}{
		{"anthropic integration to bedrock clears", "anthropic", schemas.Bedrock, true},
		{"anthropic integration to anthropic preserved", "anthropic", schemas.Anthropic, false},
		{"anthropic integration to vertex preserved", "anthropic", schemas.Vertex, false},
		{"anthropic integration to azure preserved", "anthropic", schemas.Azure, false},
		{"non-anthropic integration to bedrock preserved", "openai", schemas.Bedrock, false},
		{"no integration type to bedrock preserved", "", schemas.Bedrock, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tt.integrationType != "" {
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tt.integrationType)
			}
			for _, k := range flagKeys {
				ctx.SetValue(k, true)
			}

			clearAnthropicPassthroughForNonNativeProvider(ctx, tt.baseProvider)

			for _, k := range flagKeys {
				got, _ := ctx.Value(k).(bool)
				want := !tt.wantCleared // flags start true; cleared means false
				if got != want {
					t.Errorf("flag %v = %v, want %v", k, got, want)
				}
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
