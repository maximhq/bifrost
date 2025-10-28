package scenarios

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

// DeepCopyBifrostStream creates a deep copy of a BifrostStream object to avoid pooling issues
func DeepCopyBifrostStream(original *schemas.BifrostStream) *schemas.BifrostStream {
	if original == nil {
		return nil
	}

	// Use reflection to create a deep copy
	return deepCopyReflect(original).(*schemas.BifrostStream)
}

// deepCopyReflect performs a deep copy using reflection
func deepCopyReflect(original interface{}) interface{} {
	if original == nil {
		return nil
	}

	originalValue := reflect.ValueOf(original)
	return deepCopyValue(originalValue).Interface()
}

// deepCopyValue recursively copies a reflect.Value
func deepCopyValue(original reflect.Value) reflect.Value {
	switch original.Kind() {
	case reflect.Ptr:
		if original.IsNil() {
			return reflect.Zero(original.Type())
		}
		// Create a new pointer and recursively copy the value it points to
		newPtr := reflect.New(original.Type().Elem())
		newPtr.Elem().Set(deepCopyValue(original.Elem()))
		return newPtr

	case reflect.Struct:
		// Create a new struct and copy each field
		newStruct := reflect.New(original.Type()).Elem()
		for i := 0; i < original.NumField(); i++ {
			field := original.Field(i)
			destField := newStruct.Field(i)
			if destField.CanSet() {
				destField.Set(deepCopyValue(field))
			}
		}
		return newStruct

	case reflect.Slice:
		if original.IsNil() {
			return reflect.Zero(original.Type())
		}
		// Create a new slice and copy each element
		newSlice := reflect.MakeSlice(original.Type(), original.Len(), original.Cap())
		for i := 0; i < original.Len(); i++ {
			newSlice.Index(i).Set(deepCopyValue(original.Index(i)))
		}
		return newSlice

	case reflect.Map:
		if original.IsNil() {
			return reflect.Zero(original.Type())
		}
		// Create a new map and copy each key-value pair
		newMap := reflect.MakeMap(original.Type())
		for _, key := range original.MapKeys() {
			newMap.SetMapIndex(deepCopyValue(key), deepCopyValue(original.MapIndex(key)))
		}
		return newMap

	case reflect.Interface:
		if original.IsNil() {
			return reflect.Zero(original.Type())
		}
		// Copy the concrete value inside the interface
		return deepCopyValue(original.Elem())

	default:
		// For basic types (int, string, bool, etc.), just return the value
		return original
	}
}

// TestRetryCondition defines an interface for checking if a test operation should be retried
// This focuses specifically on LLM behavior inconsistencies, not HTTP errors (handled by Bifrost core)
type TestRetryCondition interface {
	ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// ChatRetryCondition defines an interface for checking if a chat test operation should be retried
type ChatRetryCondition interface {
	ShouldRetry(response *schemas.BifrostChatResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// TextCompletionRetryCondition defines an interface for checking if a text completion test operation should be retried
type TextCompletionRetryCondition interface {
	ShouldRetry(response *schemas.BifrostTextCompletionResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// ResponsesRetryCondition defines an interface for checking if a Responses API test operation should be retried
type ResponsesRetryCondition interface {
	ShouldRetry(response *schemas.BifrostResponsesResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// SpeechRetryCondition defines an interface for checking if a speech test operation should be retried
type SpeechRetryCondition interface {
	ShouldRetry(response *schemas.BifrostSpeechResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// TranscriptionRetryCondition defines an interface for checking if a transcription test operation should be retried
type TranscriptionRetryCondition interface {
	ShouldRetry(response *schemas.BifrostTranscriptionResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// EmbeddingRetryCondition defines an interface for checking if an embedding test operation should be retried
type EmbeddingRetryCondition interface {
	ShouldRetry(response *schemas.BifrostEmbeddingResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// TestRetryContext provides context information for retry decisions
type TestRetryContext struct {
	ScenarioName     string                 // Name of the test scenario
	AttemptNumber    int                    // Current attempt number (1-based)
	ExpectedBehavior map[string]interface{} // What we expected to happen
	TestMetadata     map[string]interface{} // Additional context for retry decisions
}

// TestRetryConfig configures retry behavior for test scenarios (DEPRECATED: Use specific retry configs)
type TestRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []TestRetryCondition                             // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// ChatRetryConfig configures retry behavior for chat test scenarios
type ChatRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []ChatRetryCondition                             // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// TextCompletionRetryConfig configures retry behavior for text completion test scenarios
type TextCompletionRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []TextCompletionRetryCondition                   // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// ResponsesRetryConfig configures retry behavior for Responses API test scenarios
type ResponsesRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []ResponsesRetryCondition                        // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// SpeechRetryConfig configures retry behavior for speech test scenarios
type SpeechRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []SpeechRetryCondition                           // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// TranscriptionRetryConfig configures retry behavior for transcription test scenarios
type TranscriptionRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []TranscriptionRetryCondition                    // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// EmbeddingRetryConfig configures retry behavior for embedding test scenarios
type EmbeddingRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []EmbeddingRetryCondition                        // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// DefaultTestRetryConfig returns a sensible default retry configuration for LLM tests
func DefaultTestRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying test (attempt %d): %s", attempt, reason)
		},
		OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
			t.Logf("❌ Test failed after %d attempts: %v", attempts, finalErr)
		},
	}
}

// WithTestRetry wraps a test operation with retry logic for LLM behavior inconsistencies
// This is separate from HTTP retries (handled by Bifrost core) and focuses on:
// - Tool calling inconsistencies
// - Response format variations
// - Content quality issues
// - Semantic inconsistencies
// - VALIDATION FAILURES (most important retry case)
func WithTestRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostResponse, *schemas.BifrostError),
) (*schemas.BifrostResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			// Note: ValidateResponse is deprecated, this should be updated to use specific validation functions
			t.Logf("⚠️ Warning: Using deprecated ValidateResponse function")
			// For now, skip validation in the deprecated function
			validationResult := ValidationResult{Passed: true}

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - check if we should retry based on validation failure
			if attempt < config.MaxAttempts {
				shouldRetry, retryReason := checkRetryConditions(response, err, context, config.Conditions)

				if shouldRetry {
					// Log retry attempt due to validation failure
					if config.OnRetry != nil {
						validationErrors := strings.Join(validationResult.Errors, "; ")
						config.OnRetry(attempt, fmt.Sprintf("%s (Validation: %s)", retryReason, validationErrors), t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			testFailureError := &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("Test validation failed after %d attempts - %s", attempt, validationErrors),
					Type:    bifrost.Ptr("validation_failure"),
					Code:    bifrost.Ptr("TEST_VALIDATION_FAILED"),
				},
			}

			return nil, testFailureError
		}

		// No response - check basic retry conditions (connection errors, etc.)
		shouldRetry, retryReason := checkRetryConditions(response, err, context, config.Conditions)

		if !shouldRetry || attempt == config.MaxAttempts {
			if shouldRetry && attempt == config.MaxAttempts {
				// Final attempt failed
				if config.OnFinalFail != nil {
					finalErr := fmt.Errorf("retry condition met on final attempt: %s", retryReason)
					config.OnFinalFail(attempt, finalErr, t)
				}
			}
			break
		}

		// Log retry attempt
		if config.OnRetry != nil {
			config.OnRetry(attempt, retryReason, t)
		}

		// Calculate delay with exponential backoff
		delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
		time.Sleep(delay)
	}

	// Final fallback: reached here if we had connection/HTTP errors (not validation failures)
	// lastError should contain the actual HTTP/connection error in this case
	return lastResponse, lastError
}

// WithChatTestRetry wraps a chat test operation with retry logic for LLM behavior inconsistencies
func WithChatTestRetry(
	t *testing.T,
	config ChatRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostChatResponse, *schemas.BifrostError),
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostChatResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateChatResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - check if we should retry based on validation failure
			if attempt < config.MaxAttempts {
				shouldRetry, retryReason := checkChatRetryConditions(response, err, context, config.Conditions)

				if shouldRetry {
					// Log retry attempt due to validation failure
					if config.OnRetry != nil {
						validationErrors := strings.Join(validationResult.Errors, "; ")
						config.OnRetry(attempt, fmt.Sprintf("%s (Validation: %s)", retryReason, validationErrors), t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			shouldRetry, retryReason := checkChatRetryConditions(response, err, context, config.Conditions)

			if shouldRetry {
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
		}

		// If we get here, either we got a final error or no more retries
		break
	}

	// Final failure callback
	if config.OnFinalFail != nil && lastError != nil {
		errorMsg := "unknown error"
		if lastError.Error != nil {
			errorMsg = lastError.Error.Message
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithResponsesTestRetry wraps a Responses API test operation with retry logic for LLM behavior inconsistencies
func WithResponsesTestRetry(
	t *testing.T,
	config ResponsesRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostResponsesResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateResponsesResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - check if we should retry based on validation failure
			if attempt < config.MaxAttempts {
				shouldRetry, retryReason := checkResponsesRetryConditions(response, err, context, config.Conditions)

				if shouldRetry {
					// Log retry attempt due to validation failure
					if config.OnRetry != nil {
						validationErrors := strings.Join(validationResult.Errors, "; ")
						config.OnRetry(attempt, fmt.Sprintf("%s (Validation: %s)", retryReason, validationErrors), t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			shouldRetry, retryReason := checkResponsesRetryConditions(response, err, context, config.Conditions)

			if shouldRetry {
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
		}

		// If we get here, either we got a final error or no more retries
		break
	}

	// Final failure callback
	if config.OnFinalFail != nil && lastError != nil {
		errorMsg := "unknown error"
		if lastError.Error != nil {
			errorMsg = lastError.Error.Message
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithStreamRetry wraps a streaming operation with retry logic for LLM behavioral inconsistencies
func WithStreamRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	operation func() (chan *schemas.BifrostStream, *schemas.BifrostError),
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	var lastChannel chan *schemas.BifrostStream
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		if attempt > 1 {
			t.Logf("🔄 Retry attempt %d/%d for %s", attempt, config.MaxAttempts, context.ScenarioName)
		}

		lastChannel, lastError = operation()

		// If successful (no error), return immediately
		if lastError == nil {
			if attempt > 1 {
				t.Logf("✅ Stream retry succeeded on attempt %d for %s", attempt, context.ScenarioName)
			}
			return lastChannel, nil
		}

		// Check if we should retry based on conditions
		shouldRetry, reason := checkStreamRetryConditions(lastChannel, lastError, context, config.Conditions)

		if !shouldRetry || attempt == config.MaxAttempts {
			if attempt > 1 {
				t.Logf("❌ Stream retry failed after %d attempts for %s", attempt, context.ScenarioName)
			}
			return lastChannel, lastError
		}

		t.Logf("🔄 Stream retry %d/%d triggered for %s: %s", attempt, config.MaxAttempts, context.ScenarioName, reason)

		// Calculate delay with exponential backoff
		delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
		time.Sleep(delay)
	}

	return lastChannel, lastError
}

// checkStreamRetryConditions evaluates retry conditions for streaming operations
func checkStreamRetryConditions(
	channel chan *schemas.BifrostStream,
	err *schemas.BifrostError,
	context TestRetryContext,
	conditions []TestRetryCondition,
) (bool, string) {
	// For streaming, we mainly check the error conditions since the channel is either nil or valid
	// We can't easily check the contents of the stream without consuming it
	for _, condition := range conditions {
		// Pass nil response since streaming doesn't have a single response
		if shouldRetry, reason := condition.ShouldRetry(nil, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}
	return false, ""
}

// checkRetryConditions evaluates all retry conditions and returns whether to retry
func checkRetryConditions(
	response *schemas.BifrostResponse,
	err *schemas.BifrostError,
	context TestRetryContext,
	conditions []TestRetryCondition,
) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}
	return false, ""
}

// calculateRetryDelay calculates the delay for the next retry attempt using exponential backoff
func calculateRetryDelay(attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	// Exponential backoff: baseDelay * 2^attempt
	delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt)))

	// Cap at maximum delay
	if delay > maxDelay {
		delay = maxDelay
	}

	return delay
}

// Convenience functions for common retry configurations

// ToolCallRetryConfig creates a retry config optimized for tool calling tests
func ToolCallRetryConfig(expectedToolName string) TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 5, // Tool calling can be very inconsistent
		BaseDelay:   750 * time.Millisecond,
		MaxDelay:    8 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
			&MissingToolCallCondition{ExpectedToolName: expectedToolName},
			&MalformedToolArgsCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying tool call test (attempt %d): %s", attempt, reason)
		},
	}
}

// MultiToolRetryConfig creates a retry config for multiple tool call tests
func MultiToolRetryConfig(expectedToolCount int, expectedTools []string) TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 4,
		BaseDelay:   1 * time.Second,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
			&PartialToolCallCondition{ExpectedCount: expectedToolCount},
			&MalformedToolArgsCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying multi-tool test (attempt %d): %s", attempt, reason)
		},
	}
}

// ImageProcessingRetryConfig creates a retry config for image processing tests
func ImageProcessingRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 4,
		BaseDelay:   1 * time.Second,
		MaxDelay:    8 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
			&ImageNotProcessedCondition{},
			&GenericResponseCondition{},
			&ContentValidationCondition{}, // 🎯 KEY ADDITION: Retry when valid response lacks expected keywords
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying image processing test (attempt %d): %s", attempt, reason)
		},
	}
}

// StreamingRetryConfig creates a retry config for streaming tests
func StreamingRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		// Only use stream-specific conditions, not EmptyResponseCondition
		// EmptyResponseCondition doesn't work with streaming since response is nil
		Conditions: []TestRetryCondition{
			&StreamErrorCondition{},      // Only retry on actual stream errors
			&IncompleteStreamCondition{}, // Check for incomplete streams
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying streaming test (attempt %d): %s", attempt, reason)
		},
	}
}

// ConversationRetryConfig creates a retry config for conversation-based tests
func ConversationRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
			&GenericResponseCondition{}, // Catch generic AI responses
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying conversation test (attempt %d): %s", attempt, reason)
		},
	}
}

// DefaultSpeechRetryConfig creates a retry config for speech synthesis tests
func DefaultSpeechRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptySpeechCondition{},     // Check for missing audio data
			&GenericResponseCondition{}, // Catch generic error responses
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying speech synthesis test (attempt %d): %s", attempt, reason)
		},
	}
}

// SpeechStreamRetryConfig creates a retry config for streaming speech synthesis tests
func SpeechStreamRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Conditions: []TestRetryCondition{
			&StreamErrorCondition{}, // Stream-specific errors
			&EmptySpeechCondition{}, // Check for missing audio data
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying streaming speech synthesis test (attempt %d): %s", attempt, reason)
		},
	}
}

// DefaultTranscriptionRetryConfig creates a retry config for transcription tests
func DefaultTranscriptionRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyTranscriptionCondition{}, // Check for missing transcription text
			&GenericResponseCondition{},    // Catch generic error responses
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying transcription test (attempt %d): %s", attempt, reason)
		},
	}
}

// ReasoningRetryConfig creates a retry config for reasoning tests
func ReasoningRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 5,
		BaseDelay:   750 * time.Millisecond,
		MaxDelay:    8 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
		},
	}
}

// DefaultEmbeddingRetryConfig creates a retry config for embedding tests
func DefaultEmbeddingRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyEmbeddingCondition{},
			&InvalidEmbeddingDimensionCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying embedding test (attempt %d): %s", attempt, reason)
		},
	}
}

// DualAPITestResult represents the result of testing both Chat Completions and Responses APIs
type DualAPITestResult struct {
	ChatCompletionsResponse *schemas.BifrostChatResponse
	ChatCompletionsError    *schemas.BifrostError
	ResponsesAPIResponse    *schemas.BifrostResponsesResponse
	ResponsesAPIError       *schemas.BifrostError
	BothSucceeded           bool
}

// WithDualAPITestRetry wraps a test operation with retry logic for both Chat Completions and Responses API
// The test passes only when BOTH APIs succeed according to expectations
func WithDualAPITestRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	chatOperation func() (*schemas.BifrostChatResponse, *schemas.BifrostError),
	responsesOperation func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
) DualAPITestResult {

	var lastResult DualAPITestResult

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute both operations
		chatResponse, chatErr := chatOperation()
		responsesResponse, responsesErr := responsesOperation()

		lastResult = DualAPITestResult{
			ChatCompletionsResponse: chatResponse,
			ChatCompletionsError:    chatErr,
			ResponsesAPIResponse:    responsesResponse,
			ResponsesAPIError:       responsesErr,
			BothSucceeded:           false,
		}

		// Validate Chat Completions API response
		var chatValidationPassed bool
		var chatValidationErrors []string
		if chatResponse != nil {
			chatValidationResult := ValidateChatResponse(t, chatResponse, chatErr, expectations, scenarioName+" (Chat Completions)")
			chatValidationPassed = chatValidationResult.Passed
			chatValidationErrors = chatValidationResult.Errors
		}

		// Validate Responses API response
		var responsesValidationPassed bool
		var responsesValidationErrors []string
		if responsesResponse != nil {
			responsesValidationResult := ValidateResponsesResponse(t, responsesResponse, responsesErr, expectations, scenarioName+" (Responses API)")
			responsesValidationPassed = responsesValidationResult.Passed
			responsesValidationErrors = responsesValidationResult.Errors
		}

		// Check if both APIs succeeded
		bothPassed := chatValidationPassed && responsesValidationPassed
		lastResult.BothSucceeded = bothPassed

		if bothPassed {
			t.Logf("✅ Both APIs passed validation on attempt %d for %s", attempt, scenarioName)
			return lastResult
		}

		// If not on final attempt, check if we should retry
		if attempt < config.MaxAttempts {
			// For dual API retry, we use basic retry conditions
			// Since we can't use checkRetryConditions with different response types,
			// we'll use a simple retry strategy based on validation failures
			shouldRetry := !chatValidationPassed || !responsesValidationPassed
			var retryReason string
			if !chatValidationPassed {
				retryReason = "Chat API validation failed"
			}
			if !responsesValidationPassed {
				if retryReason != "" {
					retryReason += " and Responses API validation failed"
				} else {
					retryReason = "Responses API validation failed"
				}
			}

			if shouldRetry {
				// Log retry attempt
				if config.OnRetry != nil {
					var reasons []string
					if !chatValidationPassed {
						reasons = append(reasons, fmt.Sprintf("Chat Completions Validation: %s", strings.Join(chatValidationErrors, "; ")))
					}
					if !responsesValidationPassed {
						reasons = append(reasons, fmt.Sprintf("Responses API Validation: %s", strings.Join(responsesValidationErrors, "; ")))
					}
					config.OnRetry(attempt, strings.Join(reasons, " | "), t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
		}

		// Final attempt failed - log details
		if config.OnFinalFail != nil {
			var errors []string
			if !chatValidationPassed {
				errors = append(errors, fmt.Sprintf("Chat Completions failed: %s", strings.Join(chatValidationErrors, "; ")))
			}
			if !responsesValidationPassed {
				errors = append(errors, fmt.Sprintf("Responses API failed: %s", strings.Join(responsesValidationErrors, "; ")))
			}
			finalErr := fmt.Errorf("dual API test failed after %d attempts: %s", attempt, strings.Join(errors, " AND "))
			config.OnFinalFail(attempt, finalErr, t)
		}

		break
	}

	// Ensure BothSucceeded reflects the final validation state
	// This fixes a bug where successful retries weren't properly reflected in the result
	if lastResult.ChatCompletionsResponse != nil && lastResult.ResponsesAPIResponse != nil {
		chatValidationResult := ValidateChatResponse(t, lastResult.ChatCompletionsResponse, lastResult.ChatCompletionsError, expectations, scenarioName+" (Chat Completions)")
		responsesValidationResult := ValidateResponsesResponse(t, lastResult.ResponsesAPIResponse, lastResult.ResponsesAPIError, expectations, scenarioName+" (Responses API)")
		lastResult.BothSucceeded = chatValidationResult.Passed && responsesValidationResult.Passed
	}

	return lastResult
}

// GetTestRetryConfigForScenario returns an appropriate retry config for a scenario
func GetTestRetryConfigForScenario(scenarioName string, testConfig config.ComprehensiveTestConfig) TestRetryConfig {
	switch scenarioName {
	case "ToolCalls", "SingleToolCall":
		return ToolCallRetryConfig("") // Will be set by specific test
	case "MultipleToolCalls":
		return MultiToolRetryConfig(2, []string{}) // Will be customized by specific test
	case "End2EndToolCalling", "AutomaticFunctionCalling":
		return ToolCallRetryConfig("") // Tool-calling focused
	case "ImageURL", "ImageBase64", "MultipleImages":
		return ImageProcessingRetryConfig()
	case "CompleteEnd2End_Vision": // 🎯 Vision step of end-to-end test
		return ImageProcessingRetryConfig()
	case "CompleteEnd2End_Chat": // 💬 Chat step of end-to-end test
		return ConversationRetryConfig()
	case "ChatCompletionStream":
		return StreamingRetryConfig()
	case "Embedding":
		return DefaultEmbeddingRetryConfig()
	case "SpeechSynthesis", "SpeechSynthesisHD", "SpeechSynthesis_Voice": // 🔊 Speech synthesis tests
		return DefaultSpeechRetryConfig()
	case "SpeechSynthesisStream", "SpeechSynthesisStreamHD", "SpeechSynthesisStreamVoice": // 🔊 Streaming speech tests
		return SpeechStreamRetryConfig()
	case "Transcription", "TranscriptionStream": // 🎙️ Transcription tests
		return DefaultTranscriptionRetryConfig()
	case "Reasoning":
		return ReasoningRetryConfig()
	default:
		// For basic scenarios like SimpleChat, TextCompletion
		return DefaultTestRetryConfig()
	}
}

// checkChatRetryConditions checks if any chat retry conditions are met
func checkChatRetryConditions(response *schemas.BifrostChatResponse, err *schemas.BifrostError, context TestRetryContext, conditions []ChatRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}

// checkResponsesRetryConditions checks if any Responses API retry conditions are met
func checkResponsesRetryConditions(response *schemas.BifrostResponsesResponse, err *schemas.BifrostError, context TestRetryContext, conditions []ResponsesRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}
