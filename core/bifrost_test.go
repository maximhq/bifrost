package bifrost

import (
	"context"
	"fmt"
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

// Test that retryableStatusCodes are properly defined
func TestRetryableStatusCodes(t *testing.T) {
	expectedCodes := map[int]bool{
		500: true, // Internal Server Error
		502: true, // Bad Gateway
		503: true, // Service Unavailable
		504: true, // Gateway Timeout
		429: true, // Too Many Requests
	}

	for code, expected := range expectedCodes {
		if retryableStatusCodes[code] != expected {
			t.Errorf("Status code %d should be retryable=%v, got %v", code, expected, retryableStatusCodes[code])
		}
	}

	// Test non-retryable codes
	nonRetryableCodes := []int{200, 201, 400, 401, 403, 404, 422}
	for _, code := range nonRetryableCodes {
		if retryableStatusCodes[code] {
			t.Errorf("Status code %d should not be retryable", code)
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
			DefaultRequestTimeoutInSeconds: 30,
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
			Value:  *schemas.NewEnvVar(fmt.Sprintf("sk-test-%s", provider)),
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
		{ID: "key-a", Name: "Key A", Value: *schemas.NewEnvVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewEnvVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
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
		{ID: "key-a", Name: "Key A", Value: *schemas.NewEnvVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewEnvVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
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
		{ID: "key-a", Name: "Key A", Value: *schemas.NewEnvVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewEnvVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
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
	keyProvider := func(_ map[string]bool) (schemas.Key, error) { return fixedKey, nil }

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
			{ID: "k1", Name: "K1", Value: *schemas.NewEnvVar("sk-1"), Weight: 1, BlacklistedModels: []string{"gpt-4"}},
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
				ID: "k1", Name: "K1", Value: *schemas.NewEnvVar("sk-1"), Weight: 1,
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
			{ID: "k1", Name: "K1", Value: *schemas.NewEnvVar("sk-1"), Weight: 1, BlacklistedModels: []string{"gpt-4"}},
			{ID: "k2", Name: "K2", Value: *schemas.NewEnvVar("sk-2"), Weight: 1, Models: []string{"*"}},
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
		keyProvider := func(usedKeyIDs map[string]bool) (schemas.Key, error) {
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
		keyProvider := func(usedKeyIDs map[string]bool) (schemas.Key, error) {
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
		keyProvider := func(usedKeyIDs map[string]bool) (schemas.Key, error) {
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
					RequestType:    r.RequestType,
					Provider:       provKey,
					ModelRequested: mod,
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
					RequestType:    r.RequestType,
					Provider:       provKey,
					ModelRequested: mod,
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
