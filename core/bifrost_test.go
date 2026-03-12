package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

		var (
			capturedAuth string
			capturedHost string
			capturedPath string
		)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			capturedHost = r.Host
			capturedPath = r.URL.Path
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
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
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
		if capturedAuth != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", capturedAuth, wantAuth)
		}
		wantHost := strings.TrimPrefix(server.URL, "http://")
		if capturedHost != wantHost {
			t.Errorf("Host header: got %q, want %q", capturedHost, wantHost)
		}
		if capturedPath != "/v1/chat/completions" {
			t.Errorf("URL path: got %q, want %q", capturedPath, "/v1/chat/completions")
		}
	})

	t.Run("StaticConfigUsedWhenNoOverride", func(t *testing.T) {
		// Without a plugin override the static key and URL from account config must be used.
		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
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
		if capturedAuth != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", capturedAuth, wantAuth)
		}
	})

	t.Run("ProviderAutoInitWithoutConfig", func(t *testing.T) {
		// Verifies the dynamicallyConfigurableProviders fallback in getProviderQueue:
		// a standard provider (OpenAI) should be auto-initialised on first use even when
		// no static config entry exists. The plugin supplies key and URL via Update* methods.
		const overrideKey = "sk-auto-init-key"

		var capturedAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
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
		if capturedAuth != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", capturedAuth, wantAuth)
		}
	})

	t.Run("DialectSwitchViaUpdateProvider", func(t *testing.T) {
		// Verifies that req.UpdateProvider in a PreLLMHook switches the wire dialect.
		// The incoming request declares Provider=Anthropic; the plugin calls
		// req.UpdateProvider(OpenAI), redirecting to the OpenAI queue and hitting
		// overrideServer rather than sentinelServer.
		const overrideKey = "sk-dialect-switch-key"

		var sentinelHit bool
		sentinelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sentinelHit = true
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer sentinelServer.Close()

		var capturedAuth string
		overrideServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
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
		if sentinelHit {
			t.Error("Anthropic sentinel server was called; dialect was not switched to OpenAI")
		}
		wantAuth := "Bearer " + overrideKey
		if capturedAuth != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", capturedAuth, wantAuth)
		}
	})

	// ArbitraryProviderViaBaseProviderType verifies that a provider name not present in
	// any static config and not in dynamicallyConfigurableProviders can still be used
	// when the plugin specifies a BaseProviderType (dialect) via UpdateBaseProviderType.
	// This is the multi-tenant use case: "my-org-openai" routes through the OpenAI dialect.
	t.Run("ArbitraryProviderViaBaseProviderType", func(t *testing.T) {
		const arbitraryKey = "sk-arbitrary-tenant-key"
		var capturedAuth string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mockOpenAIChatResponse("gpt-4o"))
		}))
		defer server.Close()

		// Empty account — "my-org-openai" is not registered anywhere. MockAccount returns
		// (nil, nil) for unregistered providers (including the resolved "openai" base type)
		// so getProviderQueue can auto-init it.
		account := NewMockAccount()

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bf, err := Init(ctx, schemas.BifrostConfig{
			Account:    account,
			Logger:     NewDefaultLogger(schemas.LogLevelError),
			LLMPlugins: []schemas.LLMPlugin{newArbitraryProviderPlugin("my-org-openai", schemas.OpenAI, arbitraryKey, server.URL)},
		})
		if err != nil {
			t.Fatalf("Init() error = %v", err)
		}
		t.Cleanup(func() { bf.Shutdown() })

		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		content := schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}
		resp, bifrostErr := bf.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
			Provider: schemas.ModelProvider("my-org-openai"),
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &content}},
		})
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionRequest() error = %v", bifrostErr)
		}
		if resp == nil {
			t.Fatal("expected non-nil chat response")
		}

		wantAuth := "Bearer " + arbitraryKey
		if capturedAuth != wantAuth {
			t.Errorf("Authorization header: got %q, want %q", capturedAuth, wantAuth)
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
func (p *keyBaseURLPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewEnvVar(p.key)})
	req.UpdateProviderBaseURL(p.baseURL)
	return req, nil, nil
}
func (p *keyBaseURLPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// arbitraryProviderPlugin is a test helper plugin that routes a request to a provider name
// that is not in static config and not in dynamicallyConfigurableProviders, by setting a
// BaseProviderType (dialect) alongside credentials and base URL.
type arbitraryProviderPlugin struct {
	providerName schemas.ModelProvider
	baseType     schemas.ModelProvider
	key, baseURL string
}

func newArbitraryProviderPlugin(providerName, baseType schemas.ModelProvider, key, baseURL string) *arbitraryProviderPlugin {
	return &arbitraryProviderPlugin{providerName: providerName, baseType: baseType, key: key, baseURL: baseURL}
}

func (p *arbitraryProviderPlugin) GetName() string { return "arbitrary-provider-test-plugin" }
func (p *arbitraryProviderPlugin) Cleanup() error  { return nil }
func (p *arbitraryProviderPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	req.UpdateProvider(p.providerName) //nolint:errcheck
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewEnvVar(p.key)})
	req.UpdateProviderBaseURL(p.baseURL)
	req.UpdateBaseProviderType(p.baseType)
	return req, nil, nil
}
func (p *arbitraryProviderPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
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
func (p *providerSwitchPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	req.UpdateProvider(p.provider) //nolint:errcheck
	req.UpdateAPIKey(schemas.Key{Value: *schemas.NewEnvVar(p.key)})
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
	var fallbackCapturedAuth string
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCapturedAuth = r.Header.Get("Authorization")
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
	if fallbackCapturedAuth != wantAuth {
		t.Errorf("fallback Authorization header: got %q, want %q", fallbackCapturedAuth, wantAuth)
	}
}

// TestBifrostRequestClone verifies that Clone returns an independent copy: mutations
// to scalar fields (Provider, Model) and ProviderOverride on the clone do not affect
// the original, and vice versa.
func TestBifrostRequestClone(t *testing.T) {
	key := schemas.Key{Value: *schemas.NewEnvVar("sk-original")}
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

func (p *fallbackOverridePlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	idx, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
	if idx > 0 {
		req.UpdateProvider(schemas.OpenAI) //nolint:errcheck
		req.UpdateAPIKey(schemas.Key{Value: *schemas.NewEnvVar(p.fallbackKey)})
		req.UpdateProviderBaseURL(p.fallbackBaseURL)
	}
	return req, nil, nil
}

func (p *fallbackOverridePlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
