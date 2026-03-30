package bifrost

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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
		handler := func() (string, *schemas.BifrostError) {
			callCount++
			return "success", nil
		}

		result, err := executeRequestWithRetries(
			ctx,
			config,
			handler,
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
		handler := func() (string, *schemas.BifrostError) {
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
		handler := func() (string, *schemas.BifrostError) {
			callCount++
			// Always fail with retryable error
			return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
		}

		result, err := executeRequestWithRetries(
			ctx,
			config,
			handler,
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
			handler := func() (string, *schemas.BifrostError) {
				callCount++
				return "", tc.error
			}

			result, err := executeRequestWithRetries(
				ctx,
				config,
				handler,
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
			handler := func() (string, *schemas.BifrostError) {
				callCount++
				return "", tc.error
			}

			result, err := executeRequestWithRetries(
				ctx,
				config,
				handler,
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

	handler := func() (string, *schemas.BifrostError) {
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

func TestPrepareFallbackRequest_AppliesFallbackParamsToExtraParams(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.AddProvider(schemas.Bedrock, 1, 1)

	b := &Bifrost{
		account: account,
		logger:  NewDefaultLogger(schemas.LogLevelError),
	}

	primaryReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{
					"reasoning_effort": "low",
					"base_param":       "keep-me",
				},
			},
		},
	}

	fallbackReq := b.prepareFallbackRequest(primaryReq, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		Params: map[string]any{
			"reasoning_effort": "high",
			"thinking_budget":  2048,
		},
	})

	if fallbackReq == nil || fallbackReq.ChatRequest == nil || fallbackReq.ChatRequest.Params == nil {
		t.Fatal("expected fallback chat request with params")
	}

	if fallbackReq.ChatRequest.Provider != schemas.Bedrock {
		t.Fatalf("expected fallback provider %s, got %s", schemas.Bedrock, fallbackReq.ChatRequest.Provider)
	}
	if fallbackReq.ChatRequest.Model != "us.anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Fatalf("unexpected fallback model: %s", fallbackReq.ChatRequest.Model)
	}

	extra := fallbackReq.ChatRequest.Params.ExtraParams
	if extra["reasoning_effort"] != "high" {
		t.Fatalf("expected fallback reasoning_effort override to high, got %#v", extra["reasoning_effort"])
	}
	if extra["thinking_budget"] != 2048 {
		t.Fatalf("expected fallback thinking_budget=2048, got %#v", extra["thinking_budget"])
	}
	if extra["base_param"] != "keep-me" {
		t.Fatalf("expected base param to be preserved, got %#v", extra["base_param"])
	}

	if primaryReq.ChatRequest.Params.ExtraParams["reasoning_effort"] != "low" {
		t.Fatalf("expected primary request params to remain unchanged, got %#v", primaryReq.ChatRequest.Params.ExtraParams["reasoning_effort"])
	}
	if _, exists := primaryReq.ChatRequest.Params.ExtraParams["thinking_budget"]; exists {
		t.Fatal("expected primary request params to not receive fallback-only thinking_budget")
	}
}

func TestPrepareFallbackRequest_WithoutFallbackParams_PreservesBaseParams(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.AddProvider(schemas.Bedrock, 1, 1)

	b := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}

	primaryReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{"base_param": "keep-me"},
			},
		},
	}

	fallbackReq := b.prepareFallbackRequest(primaryReq, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
	})

	if fallbackReq == nil || fallbackReq.ChatRequest == nil || fallbackReq.ChatRequest.Params == nil {
		t.Fatal("expected fallback request with chat params")
	}
	if fallbackReq.ChatRequest.Params.ExtraParams["base_param"] != "keep-me" {
		t.Fatalf("expected base param to be preserved, got %#v", fallbackReq.ChatRequest.Params.ExtraParams["base_param"])
	}
}

func TestPrepareFallbackRequest_WithBaseParamsNil_UsesFallbackParams(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.AddProvider(schemas.Bedrock, 1, 1)

	b := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}

	primaryReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Params:   nil,
		},
	}

	fallbackReq := b.prepareFallbackRequest(primaryReq, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		Params: map[string]any{
			"reasoning_effort": "high",
		},
	})

	if fallbackReq == nil || fallbackReq.ChatRequest == nil || fallbackReq.ChatRequest.Params == nil {
		t.Fatal("expected fallback request with initialized params")
	}
	if fallbackReq.ChatRequest.Params.ExtraParams["reasoning_effort"] != "high" {
		t.Fatalf("expected fallback-only param, got %#v", fallbackReq.ChatRequest.Params.ExtraParams["reasoning_effort"])
	}
}

func TestPrepareFallbackRequest_FallbackParamsReachProviderPayloadGeneration(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.AddProvider(schemas.Bedrock, 1, 1)

	b := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}

	primaryReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: Ptr("hello"),
				},
			}},
			Params: &schemas.ChatParameters{
				ExtraParams: map[string]interface{}{"base_param": "keep-me"},
			},
		},
	}

	fallbackReq := b.prepareFallbackRequest(primaryReq, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		Params: map[string]any{
			"reasoning_effort": "high",
			"thinking_budget":  2048,
		},
	})
	if fallbackReq == nil || fallbackReq.ChatRequest == nil {
		t.Fatal("expected fallback chat request")
	}

	openAIReq := openai.ToOpenAIChatRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), fallbackReq.ChatRequest)
	if openAIReq == nil {
		t.Fatal("expected openai request conversion output")
	}

	jsonBody, err := sonic.MarshalIndent(openAIReq, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal openai request body: %v", err)
	}

	mergedBody, err := providerUtils.MergeExtraParamsIntoJSON(jsonBody, openAIReq.GetExtraParams())
	if err != nil {
		t.Fatalf("failed to merge extra params into request body: %v", err)
	}

	var payload map[string]interface{}
	if err := sonic.Unmarshal(mergedBody, &payload); err != nil {
		t.Fatalf("failed to unmarshal merged payload: %v", err)
	}

	if payload["reasoning_effort"] != "high" {
		t.Fatalf("expected reasoning_effort in provider payload, got %#v", payload["reasoning_effort"])
	}
	if payload["thinking_budget"] != float64(2048) {
		t.Fatalf("expected thinking_budget in provider payload, got %#v", payload["thinking_budget"])
	}
	if payload["base_param"] != "keep-me" {
		t.Fatalf("expected base_param in provider payload, got %#v", payload["base_param"])
	}

	t.Logf("fallback provider payload reasoning_effort=%v thinking_budget=%v base_param=%v", payload["reasoning_effort"], payload["thinking_budget"], payload["base_param"])
}

func TestPrepareFallbackRequest_ImageVariationParamsPropagation(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.AddProvider(schemas.Bedrock, 1, 1)

	b := &Bifrost{account: account, logger: NewDefaultLogger(schemas.LogLevelError)}

	primaryReq := &schemas.BifrostRequest{
		RequestType: schemas.ImageVariationRequest,
		ImageVariationRequest: &schemas.BifrostImageVariationRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-image-1",
			Input: &schemas.ImageVariationInput{
				Image: schemas.ImageInput{Image: []byte{0x89, 0x50, 0x4E, 0x47}},
			},
			Params: &schemas.ImageVariationParameters{
				ExtraParams: map[string]interface{}{
					"variation_strength": 0.2,
					"base_param":         "keep-me",
				},
			},
		},
	}

	fallbackReq := b.prepareFallbackRequest(primaryReq, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		Params: map[string]any{
			"variation_strength": 0.9,
			"reasoning_effort":   "high",
		},
	})

	if fallbackReq == nil || fallbackReq.ImageVariationRequest == nil || fallbackReq.ImageVariationRequest.Params == nil {
		t.Fatal("expected fallback image variation request with params")
	}

	if fallbackReq.ImageVariationRequest.Provider != schemas.Bedrock {
		t.Fatalf("expected fallback provider %s, got %s", schemas.Bedrock, fallbackReq.ImageVariationRequest.Provider)
	}
	if fallbackReq.ImageVariationRequest.Model != "us.anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Fatalf("unexpected fallback model: %s", fallbackReq.ImageVariationRequest.Model)
	}

	extra := fallbackReq.ImageVariationRequest.Params.ExtraParams
	if extra["variation_strength"] != 0.9 {
		t.Fatalf("expected variation_strength override to 0.9, got %#v", extra["variation_strength"])
	}
	if extra["reasoning_effort"] != "high" {
		t.Fatalf("expected fallback reasoning_effort=high, got %#v", extra["reasoning_effort"])
	}
	if extra["base_param"] != "keep-me" {
		t.Fatalf("expected base_param to be preserved, got %#v", extra["base_param"])
	}

	if primaryReq.ImageVariationRequest.Params.ExtraParams["variation_strength"] != 0.2 {
		t.Fatalf("expected primary request params to remain unchanged, got %#v", primaryReq.ImageVariationRequest.Params.ExtraParams["variation_strength"])
	}
	if _, exists := primaryReq.ImageVariationRequest.Params.ExtraParams["reasoning_effort"]; exists {
		t.Fatal("expected primary request params to not receive fallback-only reasoning_effort")
	}

	openAIReq := openai.ToOpenAIImageVariationRequest(fallbackReq.ImageVariationRequest)
	if openAIReq == nil {
		t.Fatal("expected openai image variation request conversion output")
	}

	jsonBody, err := sonic.MarshalIndent(openAIReq, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal openai image variation request body: %v", err)
	}

	mergedBody, err := providerUtils.MergeExtraParamsIntoJSON(jsonBody, openAIReq.GetExtraParams())
	if err != nil {
		t.Fatalf("failed to merge extra params into request body: %v", err)
	}

	var payload map[string]interface{}
	if err := sonic.Unmarshal(mergedBody, &payload); err != nil {
		t.Fatalf("failed to unmarshal merged payload: %v", err)
	}

	if payload["variation_strength"] != 0.9 {
		t.Fatalf("expected variation_strength in provider payload, got %#v", payload["variation_strength"])
	}
	if payload["reasoning_effort"] != "high" {
		t.Fatalf("expected reasoning_effort in provider payload, got %#v", payload["reasoning_effort"])
	}
	if payload["base_param"] != "keep-me" {
		t.Fatalf("expected base_param in provider payload, got %#v", payload["base_param"])
	}
}

// Test selectKeyFromProviderForModel with session stickiness
func TestSelectKeyFromProviderForModel_SessionStickiness(t *testing.T) {
	kvStore := newMockKVStore()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	// Use 2 keys so we hit the keySelector path (single key returns early)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewEnvVar("sk-a"), Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewEnvVar("sk-b"), Weight: 1},
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

	// First call: cache miss, keySelector runs, key stored
	key1, err := bifrost.selectKeyFromProviderForModel(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatalf("first selectKeyFromProviderForModel: %v", err)
	}
	if key1.ID != "key-a" {
		t.Errorf("first call: expected key-a, got %s", key1.ID)
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
	key2, err := bifrost.selectKeyFromProviderForModel(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatalf("second selectKeyFromProviderForModel: %v", err)
	}
	if key2.ID != "key-a" {
		t.Errorf("second call: expected key-a (sticky), got %s", key2.ID)
	}
	if keySelectorCalls != 1 {
		t.Errorf("second call: keySelector should not run (cache hit), got %d calls", keySelectorCalls)
	}
}

// Test selectKeyFromProviderForModel - no stickiness when session ID absent
func TestSelectKeyFromProviderForModel_NoStickinessWithoutSessionID(t *testing.T) {
	kvStore := newMockKVStore()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewEnvVar("sk-a"), Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewEnvVar("sk-b"), Weight: 1},
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
	// No session ID set

	for i := 0; i < 2; i++ {
		key, err := bifrost.selectKeyFromProviderForModel(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err != nil {
			t.Fatalf("selectKeyFromProviderForModel call %d: %v", i+1, err)
		}
		if key.ID != "key-a" {
			t.Fatalf("call %d: expected key-a, got %s", i+1, key.ID)
		}
	}
	if keySelectorCalls != 2 {
		t.Errorf("expected 2 keySelector calls without a session id, got %d", keySelectorCalls)
	}
	// KVStore should not have a sticky entry for an empty session id
	if _, err := kvStore.Get(buildSessionKey(schemas.OpenAI, "", "gpt-4")); err == nil {
		t.Error("kvstore should not have a sticky entry for an empty session id")
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
		_, err := bifrost.selectKeyFromProviderForModel(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
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
		_, err := bifrost.selectKeyFromProviderForModel(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err == nil {
			t.Fatal("expected error when model is both allowed and blacklisted")
		}
	})

	t.Run("second key used when first blacklists", func(t *testing.T) {
		account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
			{ID: "k1", Name: "K1", Value: *schemas.NewEnvVar("sk-1"), Weight: 1, BlacklistedModels: []string{"gpt-4"}},
			{ID: "k2", Name: "K2", Value: *schemas.NewEnvVar("sk-2"), Weight: 1},
		})
		key, err := bifrost.selectKeyFromProviderForModel(bfCtx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key.ID != "k2" {
			t.Fatalf("expected k2, got %s", key.ID)
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
