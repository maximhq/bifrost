package testutil

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// RETRY FRAMEWORK FOR TEST SCENARIOS
// =============================================================================
//
// PURPOSE: This retry framework is designed to handle LLM behavior inconsistencies
// and content validation failures in test scenarios. The core principle is:
//
//   - VALIDATION FAILURES ALWAYS TRIGGER RETRIES
//     Content validation errors indicate functionality issues that should be retried
//     multiple times to verify the system works correctly. Network-level failures
//     are already handled by bifrost core, so these retries focus on content/functionality.
//
//   - RETRY CONDITIONS ARE SECONDARY
//     Additional retry conditions (empty responses, malformed data, etc.) provide
//     supplementary retry triggers, but validation failures take precedence.
//
// RETRY STRATEGY:
//   1. Execute operation and validate response
//   2. If validation passes → return success
//   3. If validation fails → ALWAYS retry (if attempts remaining)
//   4. Include validation errors in retry reason for debugging
//   5. Only fail test after exhausting all retry attempts
//
// =============================================================================

// DeepCopyBifrostStreamChunk creates a deep copy of a BifrostStreamChunk object to avoid pooling issues
func DeepCopyBifrostStreamChunk(original *schemas.BifrostStreamChunk) *schemas.BifrostStreamChunk {
	if original == nil {
		return nil
	}

	// Use reflection to create a deep copy
	return deepCopyReflect(original).(*schemas.BifrostStreamChunk)
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

// ImageGenerationRetryCondition defines an interface for checking if an image generation test operation should be retried
type ImageGenerationRetryCondition interface {
	ShouldRetry(response *schemas.BifrostImageGenerationResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// CountTokensRetryCondition defines an interface for checking if a count tokens test operation should be retried
type CountTokensRetryCondition interface {
	ShouldRetry(response *schemas.BifrostCountTokensResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// ListModelsRetryCondition defines an interface for checking if a list models test operation should be retried
type ListModelsRetryCondition interface {
	ShouldRetry(response *schemas.BifrostListModelsResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// BatchCreateRetryCondition defines an interface for checking if a batch create test operation should be retried
type BatchCreateRetryCondition interface {
	ShouldRetry(response *schemas.BifrostBatchCreateResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// BatchListRetryCondition defines an interface for checking if a batch list test operation should be retried
type BatchListRetryCondition interface {
	ShouldRetry(response *schemas.BifrostBatchListResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// BatchRetrieveRetryCondition defines an interface for checking if a batch retrieve test operation should be retried
type BatchRetrieveRetryCondition interface {
	ShouldRetry(response *schemas.BifrostBatchRetrieveResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// BatchCancelRetryCondition defines an interface for checking if a batch cancel test operation should be retried
type BatchCancelRetryCondition interface {
	ShouldRetry(response *schemas.BifrostBatchCancelResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// BatchResultsRetryCondition defines an interface for checking if a batch results test operation should be retried
type BatchResultsRetryCondition interface {
	ShouldRetry(response *schemas.BifrostBatchResultsResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// FileUploadRetryCondition defines an interface for checking if a file upload test operation should be retried
type FileUploadRetryCondition interface {
	ShouldRetry(response *schemas.BifrostFileUploadResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// FileListRetryCondition defines an interface for checking if a file list test operation should be retried
type FileListRetryCondition interface {
	ShouldRetry(response *schemas.BifrostFileListResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// FileRetrieveRetryCondition defines an interface for checking if a file retrieve test operation should be retried
type FileRetrieveRetryCondition interface {
	ShouldRetry(response *schemas.BifrostFileRetrieveResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// FileDeleteRetryCondition defines an interface for checking if a file delete test operation should be retried
type FileDeleteRetryCondition interface {
	ShouldRetry(response *schemas.BifrostFileDeleteResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// FileContentRetryCondition defines an interface for checking if a file content test operation should be retried
type FileContentRetryCondition interface {
	ShouldRetry(response *schemas.BifrostFileContentResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
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

// ImageGenerationRetryConfig configures retry behavior for image generation test scenarios
type ImageGenerationRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []ImageGenerationRetryCondition                  // Conditions that trigger retries
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

// CountTokensRetryConfig configures retry behavior for count tokens test scenarios
type CountTokensRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []CountTokensRetryCondition                      // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// ListModelsRetryConfig configures retry behavior for list models test scenarios
type ListModelsRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []ListModelsRetryCondition                       // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// BatchCreateRetryConfig configures retry behavior for batch create test scenarios
type BatchCreateRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []BatchCreateRetryCondition                      // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// BatchListRetryConfig configures retry behavior for batch list test scenarios
type BatchListRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []BatchListRetryCondition                        // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// BatchRetrieveRetryConfig configures retry behavior for batch retrieve test scenarios
type BatchRetrieveRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []BatchRetrieveRetryCondition                    // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// BatchCancelRetryConfig configures retry behavior for batch cancel test scenarios
type BatchCancelRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []BatchCancelRetryCondition                      // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// BatchResultsRetryConfig configures retry behavior for batch results test scenarios
type BatchResultsRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []BatchResultsRetryCondition                     // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// FileUploadRetryConfig configures retry behavior for file upload test scenarios
type FileUploadRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []FileUploadRetryCondition                       // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// FileListRetryConfig configures retry behavior for file list test scenarios
type FileListRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []FileListRetryCondition                         // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// FileRetrieveRetryConfig configures retry behavior for file retrieve test scenarios
type FileRetrieveRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []FileRetrieveRetryCondition                     // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// FileDeleteRetryConfig configures retry behavior for file delete test scenarios
type FileDeleteRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []FileDeleteRetryCondition                       // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// FileContentRetryConfig configures retry behavior for file content test scenarios
type FileContentRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []FileContentRetryCondition                      // Conditions that trigger retries
	OnRetry     func(attempt int, reason string, t *testing.T)   // Called before each retry
	OnFinalFail func(attempts int, finalErr error, t *testing.T) // Called on final failure
}

// DefaultTestRetryConfig returns a sensible default retry configuration for LLM tests
func DefaultTestRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
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

			// Validation failed - ALWAYS retry validation failures for functionality checks
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkChatRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures - this is the primary purpose of these tests
				// Content validation errors indicate functionality issues that should be retried
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					// Validation failures are the primary retry reason - ALWAYS prefix with ❌
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					// Append condition-based reason if present for additional context
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					// Fallback to condition-based retry if no validation errors (edge case)
					// Ensure ❌ prefix for consistency with error logging
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

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

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

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
		// Ensure error message has ❌ prefix if not already present
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
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

			// Validation failed - ALWAYS retry validation failures for functionality checks
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkResponsesRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures - this is the primary purpose of these tests
				// Content validation errors indicate functionality issues that should be retried
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					// Validation failures are the primary retry reason - ALWAYS prefix with ❌
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					// Append condition-based reason if present for additional context
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					// Fallback to condition-based retry if no validation errors (edge case)
					// Ensure ❌ prefix for consistency with error logging
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

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

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

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
		// Ensure error message has ❌ prefix if not already present
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithStreamRetry wraps a streaming operation with retry logic for LLM behavioral inconsistencies
func WithStreamRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	operation func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError),
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	var lastChannel chan *schemas.BifrostStreamChunk
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		lastChannel, lastError = operation()

		// If successful (no error), return immediately
		if lastError == nil {
			if attempt > 1 {
				t.Logf("✅ Stream retry succeeded on attempt %d for %s", attempt, context.ScenarioName)
			}
			return lastChannel, nil
		}

		// Log error with ❌ prefix for first attempt
		if attempt == 1 {
			errorMsg := GetErrorMessage(lastError)
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			t.Logf("❌ Stream request failed (attempt %d/%d) for %s: %s", attempt, config.MaxAttempts, context.ScenarioName, errorMsg)
		}

		// Check if we should retry
		if attempt < config.MaxAttempts {
			var shouldRetry bool
			var retryReason string

			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(lastError) {
				shouldRetry = true
				retryReason = fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(lastError))
			} else {
				// Check retry conditions
				shouldRetryFromConditions, conditionReason := checkStreamRetryConditions(lastError, context, config.Conditions)
				if shouldRetryFromConditions {
					shouldRetry = true
					// Ensure condition reason has ❌ prefix
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				} else {
					// Even if no condition matches, retry on any error for streaming
					// Network errors are handled by bifrost core, so these are likely transient
					shouldRetry = true
					errorMsg := GetErrorMessage(lastError)
					if !strings.Contains(errorMsg, "❌") {
						errorMsg = fmt.Sprintf("❌ %s", errorMsg)
					}
					retryReason = fmt.Sprintf("❌ streaming error (will retry): %s", errorMsg)
				}
			}

			if shouldRetry {
				// Use OnRetry callback if available, otherwise log directly
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				} else {
					t.Logf("🔄 Retrying stream request (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
		}

		// If we get here, all retries are exhausted
		// Log final failure with ❌ prefix
		if config.OnFinalFail != nil {
			errorMsg := GetErrorMessage(lastError)
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			finalErr := fmt.Errorf("❌ stream request failed after %d attempts: %s", attempt, errorMsg)
			config.OnFinalFail(attempt, finalErr, t)
		} else {
			errorMsg := GetErrorMessage(lastError)
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			t.Logf("❌ Stream retry failed after %d attempts for %s: %s", attempt, context.ScenarioName, errorMsg)
		}

		return lastChannel, lastError
	}

	// This should never be reached, but handle it just in case
	if lastError != nil {
		errorMsg := GetErrorMessage(lastError)
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		t.Logf("❌ Stream request failed for %s: %s", context.ScenarioName, errorMsg)
	}

	return lastChannel, lastError
}

// checkStreamRetryConditions evaluates retry conditions for streaming operations
func checkStreamRetryConditions(
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

var timeoutPhrases = []string{
	"request timed out",
	"request timeout",
	"timeout",
	"timed out",
	"deadline exceeded",
	"context deadline exceeded",
}

// isTimeoutError checks if an error is a timeout error
// This is used to ALWAYS retry on timeout errors regardless of other conditions
func isTimeoutError(err *schemas.BifrostError) bool {
	if err == nil {
		return false
	}

	// Check error category first (from ParseBifrostError categorization)
	// This catches errors categorized as timeout by the error parser
	if err.Error != nil && err.Error.Message != "" {
		errorMsg := strings.ToLower(err.Error.Message)
		// Check for various timeout-related phrases
		for _, phrase := range timeoutPhrases {
			if strings.Contains(errorMsg, phrase) {
				return true
			}
		}
	}

	// Also check the parsed error category if available
	// The error parser categorizes timeout errors based on message content
	// We can check the error message for timeout indicators
	errorMsg := GetErrorMessage(err)
	if errorMsg != "" {
		lowerMsg := strings.ToLower(errorMsg)
		for _, phrase := range timeoutPhrases {
			if strings.Contains(lowerMsg, phrase) {
				return true
			}
		}
	}

	return false
}

// Convenience functions for common retry configurations

// ToolCallRetryConfig creates a retry config optimized for tool calling tests
func ToolCallRetryConfig(expectedToolName string) TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10, // Tool calling can be very inconsistent
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
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
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
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
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
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

// FileInputRetryConfig creates a retry config for file/document input tests
func FileInputRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
			&FileNotProcessedCondition{},
			&GenericResponseCondition{},
			&ContentValidationCondition{}, // Retry when valid response lacks expected document content
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying file input test (attempt %d): %s", attempt, reason)
		},
	}
}

// FileInputResponsesRetryConfig creates a retry config for file/document input tests using Responses API
func FileInputResponsesRetryConfig() ResponsesRetryConfig {
	return ResponsesRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []ResponsesRetryCondition{
			&ResponsesEmptyCondition{},
			&ResponsesFileNotProcessedCondition{},
			&ResponsesGenericResponseCondition{},
			&ResponsesContentValidationCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying file input test (attempt %d): %s", attempt, reason)
		},
	}
}

// StreamingRetryConfig creates a retry config for streaming tests
func StreamingRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		// Only use stream-specific conditions, not EmptyResponseCondition
		// EmptyResponseCondition doesn't work with streaming since response is nil
		Conditions: []TestRetryCondition{
			&StreamErrorCondition{},      // Only retry on actual stream errors
			&IncompleteStreamCondition{}, // Check for incomplete streams
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			// reason already contains ❌ prefix from retry logic
			// attempt represents the current failed attempt number
			// Log with attempt+1 to show the next attempt that will run
			t.Logf("🔄 Retrying streaming test (attempt %d): %s", attempt+1, reason)
		},
		OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
			// finalErr already contains ❌ prefix from retry logic
			t.Logf("❌ Streaming test failed after %d attempts: %v", attempts, finalErr)
		},
	}
}

// ConversationRetryConfig creates a retry config for conversation-based tests
func ConversationRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
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
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
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
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&StreamErrorCondition{}, // Stream-specific errors
			&EmptySpeechCondition{}, // Check for missing audio data
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			// reason already contains ❌ prefix from retry logic
			t.Logf("🔄 Retrying streaming speech synthesis test (attempt %d): %s", attempt, reason)
		},
		OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
			// finalErr already contains ❌ prefix from retry logic
			t.Logf("❌ Streaming speech synthesis test failed after %d attempts: %v", attempts, finalErr)
		},
	}
}

// DefaultTranscriptionRetryConfig creates a retry config for transcription tests
func DefaultTranscriptionRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyTranscriptionCondition{}, // Check for missing transcription text
			&GenericResponseCondition{},    // Catch generic error responses
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying transcription test (attempt %d): %s", attempt, reason)
		},
	}
}

// DefaultImageGenerationRetryConfig creates a retry config for image tests
func DefaultImageGenerationRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyImageGenerationCondition{}, // Check for missing image generation data
			&GenericResponseCondition{},      // Catch generic error responses
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying image generation test (attempt %d): %s", attempt, reason)
		},
	}
}

// ReasoningRetryConfig creates a retry config for reasoning tests
func ReasoningRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{},
		},
	}
}

// DefaultEmbeddingRetryConfig creates a retry config for embedding tests
func DefaultEmbeddingRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyEmbeddingCondition{},
			&InvalidEmbeddingDimensionCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying embedding test (attempt %d): %s", attempt, reason)
		},
	}
}

// DefaultCountTokensRetryConfig creates a retry config for count tokens tests
func DefaultCountTokensRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   2000 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyCountTokensCondition{},
			&InvalidCountTokensCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying count tokens test (attempt %d): %s", attempt, reason)
		},
	}
}

// DefaultListModelsRetryConfig creates a retry config for list models tests
// IMPORTANT: List models should ALWAYS retry on any failure (errors, nil response, empty data, validation failures)
func DefaultListModelsRetryConfig() TestRetryConfig {
	return TestRetryConfig{
		MaxAttempts: 10,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		Conditions: []TestRetryCondition{
			&EmptyResponseCondition{}, // Retry on empty responses
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying list models test (attempt %d): %s", attempt, reason)
		},
		OnFinalFail: func(attempts int, finalErr error, t *testing.T) {
			t.Logf("❌ List models test failed after %d attempts: %v", attempts, finalErr)
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
//
// RETRY STRATEGY: Validation failures ALWAYS trigger retries (primary purpose: functionality checks)
// Network errors are handled by bifrost core, so retries here focus on content/functionality validation
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
			// ALWAYS retry on timeout errors - this takes precedence over all other conditions
			if (chatErr != nil && isTimeoutError(chatErr)) || (responsesErr != nil && isTimeoutError(responsesErr)) {
				var retryReason string
				if chatErr != nil && isTimeoutError(chatErr) {
					retryReason = fmt.Sprintf("Chat API timeout error: %s", GetErrorMessage(chatErr))
				}
				if responsesErr != nil && isTimeoutError(responsesErr) {
					if retryReason != "" {
						retryReason += fmt.Sprintf(" | Responses API timeout error: %s", GetErrorMessage(responsesErr))
					} else {
						retryReason = fmt.Sprintf("Responses API timeout error: %s", GetErrorMessage(responsesErr))
					}
				}

				// Log retry attempt
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

			// ALWAYS retry on validation failures - this is the primary purpose of these tests
			// Content validation errors indicate functionality issues that should be retried
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			shouldRetry := !chatValidationPassed || !responsesValidationPassed
			var retryReason string
			if !chatValidationPassed {
				retryReason = "❌ Chat API validation failed"
			}
			if !responsesValidationPassed {
				if retryReason != "" {
					retryReason += " and ❌ Responses API validation failed"
				} else {
					retryReason = "❌ Responses API validation failed"
				}
			}

			if shouldRetry {
				// Log retry attempt - ALWAYS prefix validation errors with ❌
				if config.OnRetry != nil {
					var reasons []string
					if !chatValidationPassed {
						reasons = append(reasons, fmt.Sprintf("❌ Chat Completions Validation: %s", strings.Join(chatValidationErrors, "; ")))
					}
					if !responsesValidationPassed {
						reasons = append(reasons, fmt.Sprintf("❌ Responses API Validation: %s", strings.Join(responsesValidationErrors, "; ")))
					}
					config.OnRetry(attempt, strings.Join(reasons, " | "), t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
		}

		// Final attempt failed - log details with ❌ prefix
		if config.OnFinalFail != nil {
			var errors []string
			if !chatValidationPassed {
				errors = append(errors, fmt.Sprintf("❌ Chat Completions failed: %s", strings.Join(chatValidationErrors, "; ")))
			}
			if !responsesValidationPassed {
				errors = append(errors, fmt.Sprintf("❌ Responses API failed: %s", strings.Join(responsesValidationErrors, "; ")))
			}
			finalErr := fmt.Errorf("❌ dual API test failed after %d attempts: %s", attempt, strings.Join(errors, " AND "))
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
func GetTestRetryConfigForScenario(scenarioName string, testConfig ComprehensiveTestConfig) TestRetryConfig {
	switch scenarioName {
	case "ToolCalls", "SingleToolCall":
		return ToolCallRetryConfig("") // Will be set by specific test
	case "MultipleToolCalls":
		return MultiToolRetryConfig(2, []string{}) // Will be customized by specific test
	case "End2EndToolCalling", "AutomaticFunctionCalling":
		return ToolCallRetryConfig("") // Tool-calling focused
	case "ImageURL", "ImageBase64", "MultipleImages":
		return ImageProcessingRetryConfig()
	case "FileInput":
		return FileInputRetryConfig() // Document processing with file-specific conditions
	case "CompleteEnd2End_Vision": // 🎯 Vision step of end-to-end test
		return ImageProcessingRetryConfig()
	case "CompleteEnd2End_Chat": // 💬 Chat step of end-to-end test
		return ConversationRetryConfig()
	case "ChatCompletionStream":
		return StreamingRetryConfig()
	case "Embedding":
		return DefaultEmbeddingRetryConfig()
	case "CountTokens":
		return DefaultCountTokensRetryConfig()
	case "SpeechSynthesis", "SpeechSynthesisHD", "SpeechSynthesis_Voice": // 🔊 Speech synthesis tests
		return DefaultSpeechRetryConfig()
	case "SpeechSynthesisStream", "SpeechSynthesisStreamHD", "SpeechSynthesisStreamVoice": // 🔊 Streaming speech tests
		return SpeechStreamRetryConfig()
	case "Transcription", "TranscriptionStream": // 🎙️ Transcription tests
		return DefaultTranscriptionRetryConfig()
	case "Reasoning":
		return ReasoningRetryConfig()
	case "ListModels", "ListModelsPagination":
		return DefaultListModelsRetryConfig()
	case "ImageGeneration", "ImageGenerationStream":
		return DefaultImageGenerationRetryConfig()
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

// WithTextCompletionTestRetry wraps a text completion test operation with retry logic for LLM behavior inconsistencies
func WithTextCompletionTestRetry(
	t *testing.T,
	config TextCompletionRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError),
) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostTextCompletionResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateTextCompletionResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - ALWAYS retry validation failures for functionality checks
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkTextCompletionRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures - this is the primary purpose of these tests
				// Content validation errors indicate functionality issues that should be retried
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					// Validation failures are the primary retry reason - ALWAYS prefix with ❌
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					// Append condition-based reason if present for additional context
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					// Fallback to condition-based retry if no validation errors (edge case)
					// Ensure ❌ prefix for consistency with error logging
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

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

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

			shouldRetry, retryReason := checkTextCompletionRetryConditions(response, err, context, config.Conditions)

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
		// Ensure error message has ❌ prefix if not already present
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithSpeechTestRetry wraps a speech test operation with retry logic for LLM behavior inconsistencies
func WithSpeechTestRetry(
	t *testing.T,
	config SpeechRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostSpeechResponse, *schemas.BifrostError),
) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostSpeechResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateSpeechResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - ALWAYS retry validation failures for functionality checks
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkSpeechRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures - this is the primary purpose of these tests
				// Content validation errors indicate functionality issues that should be retried
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					// Validation failures are the primary retry reason - ALWAYS prefix with ❌
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					// Append condition-based reason if present for additional context
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					// Fallback to condition-based retry if no validation errors (edge case)
					// Ensure ❌ prefix for consistency with error logging
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

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

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

			shouldRetry, retryReason := checkSpeechRetryConditions(response, err, context, config.Conditions)

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
		// Ensure error message has ❌ prefix if not already present
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// checkTextCompletionRetryConditions checks if any text completion retry conditions are met
func checkTextCompletionRetryConditions(response *schemas.BifrostTextCompletionResponse, err *schemas.BifrostError, context TestRetryContext, conditions []TextCompletionRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}

// checkSpeechRetryConditions checks if any speech retry conditions are met
func checkSpeechRetryConditions(response *schemas.BifrostSpeechResponse, err *schemas.BifrostError, context TestRetryContext, conditions []SpeechRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}

// WithEmbeddingTestRetry wraps an embedding test operation with retry logic for LLM behavior inconsistencies
func WithEmbeddingTestRetry(
	t *testing.T,
	config EmbeddingRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError),
) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostEmbeddingResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateEmbeddingResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - ALWAYS retry validation failures for functionality checks
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkEmbeddingRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures - this is the primary purpose of these tests
				// Content validation errors indicate functionality issues that should be retried
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					// Validation failures are the primary retry reason - ALWAYS prefix with ❌
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					// Append condition-based reason if present for additional context
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					// Fallback to condition-based retry if no validation errors (edge case)
					// Ensure ❌ prefix for consistency with error logging
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

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

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

			shouldRetry, retryReason := checkEmbeddingRetryConditions(response, err, context, config.Conditions)

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
		// Ensure error message has ❌ prefix if not already present
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithTranscriptionTestRetry wraps a transcription test operation with retry logic for LLM behavior inconsistencies
func WithTranscriptionTestRetry(
	t *testing.T,
	config TranscriptionRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError),
) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostTranscriptionResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateTranscriptionResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - ALWAYS retry validation failures for functionality checks
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkTranscriptionRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures - this is the primary purpose of these tests
				// Content validation errors indicate functionality issues that should be retried
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					// Validation failures are the primary retry reason - ALWAYS prefix with ❌
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					// Append condition-based reason if present for additional context
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					// Fallback to condition-based retry if no validation errors (edge case)
					// Ensure ❌ prefix for consistency with error logging
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

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

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

			shouldRetry, retryReason := checkTranscriptionRetryConditions(response, err, context, config.Conditions)

			// ALWAYS retry on non-structural errors (network errors are handled by bifrost core)
			// If no condition matches, still retry on any error as it's likely transient
			if !shouldRetry {
				shouldRetry = true
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				retryReason = fmt.Sprintf("❌ non-structural error (will retry): %s", errorMsg)
			} else if !strings.Contains(retryReason, "❌") {
				retryReason = fmt.Sprintf("❌ %s", retryReason)
			}

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
		// Ensure error message has ❌ prefix if not already present
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// checkEmbeddingRetryConditions checks if any embedding retry conditions are met
func checkEmbeddingRetryConditions(response *schemas.BifrostEmbeddingResponse, err *schemas.BifrostError, context TestRetryContext, conditions []EmbeddingRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}

// WithCountTokensTestRetry wraps a count tokens test operation with retry logic for LLM behavior inconsistencies
func WithCountTokensTestRetry(
	t *testing.T,
	config CountTokensRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostCountTokensResponse, *schemas.BifrostError),
) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostCountTokensResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateCountTokensResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkCountTokensRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

				if shouldRetry {
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

			shouldRetry, retryReason := checkCountTokensRetryConditions(response, err, context, config.Conditions)
			if shouldRetry {
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
		}

		break
	}

	// Final failure callback
	if config.OnFinalFail != nil && lastError != nil {
		errorMsg := "unknown error"
		if lastError.Error != nil {
			errorMsg = lastError.Error.Message
		}
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// checkCountTokensRetryConditions checks if any count tokens retry conditions are met
func checkCountTokensRetryConditions(response *schemas.BifrostCountTokensResponse, err *schemas.BifrostError, context TestRetryContext, conditions []CountTokensRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}
	return false, ""
}

// checkTranscriptionRetryConditions checks if any transcription retry conditions are met
func checkTranscriptionRetryConditions(response *schemas.BifrostTranscriptionResponse, err *schemas.BifrostError, context TestRetryContext, conditions []TranscriptionRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}

func WithImageGenerationRetry(
	t *testing.T,
	config ImageGenerationRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError),
) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostImageGenerationResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// If we have a response, validate it FIRST
		if response != nil {
			validationResult := ValidateImageGenerationResponse(t, response, err, expectations, scenarioName)

			// If validation passes, we're done!
			if validationResult.Passed {
				return response, err
			}

			// Validation failed - ALWAYS retry validation failures for functionality checks
			// Network errors are handled by bifrost core, so these are content/functionality validation errors
			if attempt < config.MaxAttempts {
				// ALWAYS retry on timeout errors - this takes precedence over all other conditions
				if err != nil && isTimeoutError(err) {
					retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					}

					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}

				// Check other retry conditions first (for logging/debugging)
				shouldRetryFromConditions, conditionReason := checkImageGenerationRetryConditions(response, err, context, config.Conditions)

				// ALWAYS retry on validation failures - this is the primary purpose of these tests
				// Content validation errors indicate functionality issues that should be retried
				shouldRetry := len(validationResult.Errors) > 0
				var retryReason string

				if shouldRetry {
					// Validation failures are the primary retry reason - ALWAYS prefix with ❌
					retryReason = fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
					// Append condition-based reason if present for additional context
					if shouldRetryFromConditions && conditionReason != "" {
						retryReason += fmt.Sprintf(" | also: %s", conditionReason)
					}
				} else if shouldRetryFromConditions {
					// Fallback to condition-based retry if no validation errors (edge case)
					// Ensure ❌ prefix for consistency with error logging
					shouldRetry = true
					if !strings.Contains(conditionReason, "❌") {
						retryReason = fmt.Sprintf("❌ %s", conditionReason)
					} else {
						retryReason = conditionReason
					}
				}

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

			// All retries failed validation - create a BifrostError to force test failure
			validationErrors := strings.Join(validationResult.Errors, "; ")

			if config.OnFinalFail != nil {
				finalErr := fmt.Errorf("❌ validation failed after %d attempts: %s", attempt, validationErrors)
				config.OnFinalFail(attempt, finalErr, t)
			}

			// Return nil response + BifrostError so calling test fails
			statusCode := 400
			testFailureError := &schemas.BifrostError{
				IsBifrostError: true,
				StatusCode:     &statusCode,
				Error: &schemas.ErrorField{
					Message: fmt.Sprintf("❌ Validation failed after %d attempts: %s", attempt, validationErrors),
				},
			}
			return nil, testFailureError
		}

		// If we have an error without a response, check if we should retry
		if err != nil && attempt < config.MaxAttempts {
			// ALWAYS retry on timeout errors - this takes precedence over other conditions
			if isTimeoutError(err) {
				retryReason := fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}

				// Calculate delay with exponential backoff
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}

			shouldRetry, retryReason := checkImageGenerationRetryConditions(response, err, context, config.Conditions)

			// ALWAYS retry on non-structural errors (network errors are handled by bifrost core)
			// If no condition matches, still retry on any error as it's likely transient
			if !shouldRetry {
				shouldRetry = true
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				retryReason = fmt.Sprintf("❌ non-structural error (will retry): %s", errorMsg)
			} else if !strings.Contains(retryReason, "❌") {
				retryReason = fmt.Sprintf("❌ %s", retryReason)
			}

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
		// Ensure error message has ❌ prefix if not already present
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// checkImageGenerationRetryConditions checks if any image generation retry conditions are met
func checkImageGenerationRetryConditions(response *schemas.BifrostImageGenerationResponse, err *schemas.BifrostError, context TestRetryContext, conditions []ImageGenerationRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}

// WithListModelsTestRetry wraps a list models test operation with retry logic
// IMPORTANT: ALWAYS retries on ANY failure condition (errors, nil response, empty data, validation failures)
// This ensures maximum resilience for list models tests
func WithListModelsTestRetry(
	t *testing.T,
	config ListModelsRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError),
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostListModelsResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// ALWAYS retry on ANY error condition - this is the key requirement for list models
		shouldRetry := false
		var retryReason string

		// Check for errors first
		if err != nil {
			shouldRetry = true
			// ALWAYS retry on timeout errors - this takes precedence
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		// Check for nil response
		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		// If we have a response, validate it
		if response != nil {
			validationResult := ValidateListModelsResponse(t, response, err, expectations, scenarioName)

			// If validation passes and no errors, we're done!
			if validationResult.Passed && err == nil {
				return response, err
			}

			// ALWAYS retry on validation failures
			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}

			// Also check for empty data (common failure case)
			if len(response.Data) == 0 {
				shouldRetry = true
				if retryReason != "" {
					retryReason += " | ❌ empty model list"
				} else {
					retryReason = "❌ empty model list"
				}
			}
		}

		// Retry if needed and attempts remaining
		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			// Calculate delay with exponential backoff
			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		// If we shouldn't retry or this is the last attempt, break
		if !shouldRetry {
			// Success case - no retry needed
			return response, err
		}

		// Final attempt failed - break to return error
		break
	}

	// Final failure callback
	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else if len(lastResponse.Data) == 0 {
			errorMsg = "empty model list"
		} else {
			// Validation failure
			validationResult := ValidateListModelsResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// checkListModelsRetryConditions checks if any list models retry conditions are met
func checkListModelsRetryConditions(response *schemas.BifrostListModelsResponse, err *schemas.BifrostError, context TestRetryContext, conditions []ListModelsRetryCondition) (bool, string) {
	for _, condition := range conditions {
		if shouldRetry, reason := condition.ShouldRetry(response, err, context); shouldRetry {
			return true, fmt.Sprintf("%s: %s", condition.GetConditionName(), reason)
		}
	}

	return false, ""
}

// =============================================================================
// BATCH API RETRY FUNCTIONS
// =============================================================================

// WithBatchCreateTestRetry wraps a batch create test operation with retry logic
func WithBatchCreateTestRetry(
	t *testing.T,
	config BatchCreateRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError),
) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostBatchCreateResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation
		response, err := operation()
		lastResponse = response
		lastError = err

		// ALWAYS retry on ANY error condition
		shouldRetry := false
		var retryReason string

		// Check for errors first
		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		// Check for nil response
		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		// If we have a response, validate it
		if response != nil {
			validationResult := ValidateBatchCreateResponse(t, response, err, expectations, scenarioName)

			// If validation passes and no errors, we're done!
			if validationResult.Passed && err == nil {
				return response, err
			}

			// ALWAYS retry on validation failures
			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		// Retry if needed and attempts remaining
		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateBatchCreateResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithBatchListTestRetry wraps a batch list test operation with retry logic
func WithBatchListTestRetry(
	t *testing.T,
	config BatchListRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostBatchListResponse, *schemas.BifrostError),
) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostBatchListResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateBatchListResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateBatchListResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithBatchRetrieveTestRetry wraps a batch retrieve test operation with retry logic
func WithBatchRetrieveTestRetry(
	t *testing.T,
	config BatchRetrieveRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError),
) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostBatchRetrieveResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateBatchRetrieveResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateBatchRetrieveResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithBatchCancelTestRetry wraps a batch cancel test operation with retry logic
func WithBatchCancelTestRetry(
	t *testing.T,
	config BatchCancelRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError),
) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostBatchCancelResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateBatchCancelResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateBatchCancelResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithBatchResultsTestRetry wraps a batch results test operation with retry logic
func WithBatchResultsTestRetry(
	t *testing.T,
	config BatchResultsRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError),
) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostBatchResultsResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateBatchResultsResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateBatchResultsResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// =============================================================================
// FILE API RETRY FUNCTIONS
// =============================================================================

// WithFileUploadTestRetry wraps a file upload test operation with retry logic
func WithFileUploadTestRetry(
	t *testing.T,
	config FileUploadRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostFileUploadResponse, *schemas.BifrostError),
) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostFileUploadResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateFileUploadResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateFileUploadResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithFileListTestRetry wraps a file list test operation with retry logic
func WithFileListTestRetry(
	t *testing.T,
	config FileListRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostFileListResponse, *schemas.BifrostError),
) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostFileListResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateFileListResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateFileListResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithFileRetrieveTestRetry wraps a file retrieve test operation with retry logic
func WithFileRetrieveTestRetry(
	t *testing.T,
	config FileRetrieveRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError),
) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostFileRetrieveResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateFileRetrieveResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateFileRetrieveResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithFileDeleteTestRetry wraps a file delete test operation with retry logic
func WithFileDeleteTestRetry(
	t *testing.T,
	config FileDeleteRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError),
) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostFileDeleteResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateFileDeleteResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateFileDeleteResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// WithFileContentTestRetry wraps a file content test operation with retry logic
func WithFileContentTestRetry(
	t *testing.T,
	config FileContentRetryConfig,
	context TestRetryContext,
	expectations ResponseExpectations,
	scenarioName string,
	operation func() (*schemas.BifrostFileContentResponse, *schemas.BifrostError),
) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {

	var lastResponse *schemas.BifrostFileContentResponse
	var lastError *schemas.BifrostError

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		response, err := operation()
		lastResponse = response
		lastError = err

		shouldRetry := false
		var retryReason string

		if err != nil {
			shouldRetry = true
			if isTimeoutError(err) {
				retryReason = fmt.Sprintf("timeout error detected: %s", GetErrorMessage(err))
			} else {
				parsed := ParseBifrostError(err)
				retryReason = fmt.Sprintf("❌ error occurred: %s", FormatErrorConcise(parsed))
			}
		}

		if response == nil {
			shouldRetry = true
			if retryReason != "" {
				retryReason += " and ❌ response is nil"
			} else {
				retryReason = "❌ response is nil"
			}
		}

		if response != nil {
			validationResult := ValidateFileContentResponse(t, response, err, expectations, scenarioName)

			if validationResult.Passed && err == nil {
				return response, err
			}

			if !validationResult.Passed {
				shouldRetry = true
				if retryReason != "" {
					retryReason += fmt.Sprintf(" | ❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ validation failure: %s", strings.Join(validationResult.Errors, "; "))
				}
			}
		}

		if shouldRetry && attempt < config.MaxAttempts {
			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}

		if !shouldRetry {
			return response, err
		}

		break
	}

	if config.OnFinalFail != nil {
		var errorMsg string
		if lastError != nil {
			if lastError.Error != nil {
				errorMsg = lastError.Error.Message
			} else {
				errorMsg = "unknown error"
			}
		} else if lastResponse == nil {
			errorMsg = "response is nil"
		} else {
			validationResult := ValidateFileContentResponse(t, lastResponse, nil, expectations, scenarioName)
			errorMsg = strings.Join(validationResult.Errors, "; ")
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("final error: %s", errorMsg), t)
	}

	return lastResponse, lastError
}

// SpeechStreamValidationResult represents the result of speech streaming validation
type SpeechStreamValidationResult struct {
	Passed       bool
	Errors       []string
	ReceivedData bool
	StreamErrors []string
	LastLatency  int64
}

// WithSpeechStreamValidationRetry wraps a speech streaming operation with retry logic that includes stream content validation
// This function wraps the entire operation (request + stream reading + validation) and retries on validation failures
func WithSpeechStreamValidationRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	operation func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError),
	validateStream func(chan *schemas.BifrostStreamChunk) SpeechStreamValidationResult,
) SpeechStreamValidationResult {
	var lastResult SpeechStreamValidationResult

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation to get the stream
		responseChannel, err := operation()

		// If we have an error getting the stream, check if we should retry
		if err != nil {
			// Log error with ❌ prefix for first attempt
			if attempt == 1 {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				t.Logf("❌ Speech stream request failed (attempt %d/%d) for %s: %s", attempt, config.MaxAttempts, context.ScenarioName, errorMsg)
			}

			// Check if we should retry
			if attempt < config.MaxAttempts {
				var shouldRetry bool
				var retryReason string

				// ALWAYS retry on timeout errors
				if isTimeoutError(err) {
					shouldRetry = true
					retryReason = fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				} else {
					// Check retry conditions
					shouldRetryFromConditions, conditionReason := checkStreamRetryConditions(err, context, config.Conditions)
					if shouldRetryFromConditions {
						shouldRetry = true
						if !strings.Contains(conditionReason, "❌") {
							retryReason = fmt.Sprintf("❌ %s", conditionReason)
						} else {
							retryReason = conditionReason
						}
					} else {
						// Retry on any error for streaming
						shouldRetry = true
						errorMsg := GetErrorMessage(err)
						if !strings.Contains(errorMsg, "❌") {
							errorMsg = fmt.Sprintf("❌ %s", errorMsg)
						}
						retryReason = fmt.Sprintf("❌ streaming error (will retry): %s", errorMsg)
					}
				}

				if shouldRetry {
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					} else {
						t.Logf("🔄 Retrying speech stream request (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
					}

					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries exhausted
			if config.OnFinalFail != nil {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				config.OnFinalFail(attempt, fmt.Errorf("❌ speech stream request failed after %d attempts: %s", attempt, errorMsg), t)
			}
			return SpeechStreamValidationResult{
				Passed: false,
				Errors: []string{fmt.Sprintf("❌ stream request failed: %s", GetErrorMessage(err))},
			}
		}

		if responseChannel == nil {
			if attempt < config.MaxAttempts {
				retryReason := "❌ response channel is nil"
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
			return SpeechStreamValidationResult{
				Passed: false,
				Errors: []string{"❌ response channel is nil"},
			}
		}

		// Validate the stream content
		validationResult := validateStream(responseChannel)
		lastResult = validationResult

		// If validation passes, we're done!
		if validationResult.Passed {
			return validationResult
		}

		// Validation failed - ALWAYS retry validation failures
		if attempt < config.MaxAttempts {
			retryReason := fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
			if len(validationResult.StreamErrors) > 0 {
				retryReason += fmt.Sprintf(" | stream errors: %s", strings.Join(validationResult.StreamErrors, "; "))
			}

			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			} else {
				t.Logf("🔄 Retrying speech stream validation (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}
	}

	// All retries exhausted - log final failure
	if config.OnFinalFail != nil {
		allErrors := append(lastResult.Errors, lastResult.StreamErrors...)
		errorMsg := strings.Join(allErrors, "; ")
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ speech stream validation failed after %d attempts: %s", config.MaxAttempts, errorMsg), t)
	}

	return lastResult
}

// ResponsesStreamValidationResult represents the result of responses streaming validation
type ResponsesStreamValidationResult struct {
	Passed       bool
	Errors       []string
	ReceivedData bool
	StreamErrors []string
	LastLatency  int64
}

// WithResponsesStreamValidationRetry wraps a responses streaming operation with retry logic that includes stream content validation
// This function wraps the entire operation (request + stream reading + validation) and retries on validation failures
func WithResponsesStreamValidationRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	operation func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError),
	validateStream func(chan *schemas.BifrostStreamChunk) ResponsesStreamValidationResult,
) ResponsesStreamValidationResult {
	var lastResult ResponsesStreamValidationResult

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Log attempt start (especially for retries)
		if attempt > 1 {
			t.Logf("🔄 Starting responses stream retry attempt %d/%d for %s", attempt, config.MaxAttempts, context.ScenarioName)
		} else {
			t.Logf("🔄 Starting responses stream test attempt %d/%d for %s", attempt, config.MaxAttempts, context.ScenarioName)
		}

		// Execute the operation to get the stream
		responseChannel, err := operation()

		// If we have an error getting the stream, check if we should retry
		if err != nil {
			// Log error with ❌ prefix for first attempt
			if attempt == 1 {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				t.Logf("❌ Responses stream request failed (attempt %d/%d) for %s: %s", attempt, config.MaxAttempts, context.ScenarioName, errorMsg)
			}

			// Check if we should retry
			if attempt < config.MaxAttempts {
				var shouldRetry bool
				var retryReason string

				// ALWAYS retry on timeout errors
				if isTimeoutError(err) {
					shouldRetry = true
					retryReason = fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				} else {
					// Check retry conditions
					shouldRetryFromConditions, conditionReason := checkStreamRetryConditions(err, context, config.Conditions)
					if shouldRetryFromConditions {
						shouldRetry = true
						if !strings.Contains(conditionReason, "❌") {
							retryReason = fmt.Sprintf("❌ %s", conditionReason)
						} else {
							retryReason = conditionReason
						}
					} else {
						// Retry on any non-structural error for streaming
						shouldRetry = true
						errorMsg := GetErrorMessage(err)
						if !strings.Contains(errorMsg, "❌") {
							errorMsg = fmt.Sprintf("❌ %s", errorMsg)
						}
						retryReason = fmt.Sprintf("❌ streaming error (will retry): %s", errorMsg)
					}
				}

				if shouldRetry {
					// Log the error and upcoming retry
					if attempt > 1 {
						t.Logf("❌ Responses stream request failed on attempt %d/%d for %s: %s", attempt, config.MaxAttempts, context.ScenarioName, retryReason)
					}

					if config.OnRetry != nil {
						// Pass current failed attempt number
						config.OnRetry(attempt, retryReason, t)
					} else {
						t.Logf("🔄 Retrying responses stream request (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
					}

					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					t.Logf("⏳ Waiting %v before retry...", delay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries exhausted - log final failure
			errorMsg := GetErrorMessage(err)
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			if config.OnFinalFail != nil {
				config.OnFinalFail(attempt, fmt.Errorf("❌ responses stream request failed after %d attempts: %s", attempt, errorMsg), t)
			} else {
				// Fallback logging if OnFinalFail is not set
				t.Logf("❌ Responses stream request failed after %d attempts for %s: %s", attempt, context.ScenarioName, errorMsg)
			}
			return ResponsesStreamValidationResult{
				Passed: false,
				Errors: []string{fmt.Sprintf("❌ stream request failed: %s", errorMsg)},
			}
		}

		if responseChannel == nil {
			if attempt < config.MaxAttempts {
				retryReason := "❌ response channel is nil"
				t.Logf("❌ Responses stream response channel is nil on attempt %d/%d for %s", attempt, config.MaxAttempts, context.ScenarioName)
				if config.OnRetry != nil {
					// Pass current failed attempt number
					config.OnRetry(attempt, retryReason, t)
				} else {
					t.Logf("🔄 Retrying responses stream request (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
				}
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				t.Logf("⏳ Waiting %v before retry...", delay)
				time.Sleep(delay)
				continue // CRITICAL: Must continue to retry, not return
			}
			return ResponsesStreamValidationResult{
				Passed: false,
				Errors: []string{"❌ response channel is nil"},
			}
		}

		// Validate the stream content
		validationResult := validateStream(responseChannel)
		lastResult = validationResult

		// If validation passes, we're done!
		if validationResult.Passed {
			return validationResult
		}

		// Validation failed - ALWAYS retry validation failures (non-structural issues)
		if attempt < config.MaxAttempts {
			// Check if this is a timeout error or empty stream - these should retry immediately
			isTimeout := false
			isEmptyStream := false
			allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
			for _, errMsg := range allErrors {
				lowerErr := strings.ToLower(errMsg)
				if strings.Contains(lowerErr, "timeout") {
					isTimeout = true
					break
				}
				if strings.Contains(lowerErr, "no data") || strings.Contains(lowerErr, "without receiving") {
					isEmptyStream = true
					break
				}
			}

			// Also check ReceivedData flag
			if !validationResult.ReceivedData {
				isEmptyStream = true
			}

			retryReason := fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
			if len(validationResult.StreamErrors) > 0 {
				retryReason += fmt.Sprintf(" | stream errors: %s", strings.Join(validationResult.StreamErrors, "; "))
			}

			// Use shorter delay for timeout/empty stream errors
			var delay time.Duration
			if isTimeout || isEmptyStream {
				// Use shorter delay for transient errors (timeout or empty stream)
				delay = config.BaseDelay / 2
				if delay < 500*time.Millisecond {
					delay = 500 * time.Millisecond
				}
				if isTimeout {
					retryReason = fmt.Sprintf("❌ timeout error detected: %s", strings.Join(validationResult.Errors, "; "))
				} else {
					retryReason = fmt.Sprintf("❌ empty stream detected (no data received): %s", strings.Join(validationResult.Errors, "; "))
				}
			} else {
				delay = calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			}

			if config.OnRetry != nil {
				// Pass current failed attempt number
				config.OnRetry(attempt, retryReason, t)
			} else {
				t.Logf("🔄 Retrying responses stream validation (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
			}

			time.Sleep(delay)
			continue
		}
	}

	// All retries exhausted - log final failure with ❌ prefix
	if config.OnFinalFail != nil {
		allErrors := append(lastResult.Errors, lastResult.StreamErrors...)
		errorMsg := strings.Join(allErrors, "; ")
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ responses stream validation failed after %d attempts: %s", config.MaxAttempts, errorMsg), t)
	} else {
		// Fallback logging if OnFinalFail is not set
		allErrors := append(lastResult.Errors, lastResult.StreamErrors...)
		errorMsg := strings.Join(allErrors, "; ")
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		t.Logf("❌ Responses stream validation failed after %d attempts for %s: %s", config.MaxAttempts, context.ScenarioName, errorMsg)
	}

	return lastResult
}

// ChatStreamValidationResult represents the result of chat streaming validation
type ChatStreamValidationResult struct {
	Passed           bool
	Errors           []string
	ReceivedData     bool
	StreamErrors     []string
	ToolCallDetected bool
	ResponseCount    int
}

// WithChatStreamValidationRetry wraps a chat streaming operation with retry logic that includes stream content validation
// This function wraps the entire operation (request + stream reading + validation) and retries on validation failures
func WithChatStreamValidationRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	operation func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError),
	validateStream func(chan *schemas.BifrostStreamChunk) ChatStreamValidationResult,
) ChatStreamValidationResult {
	var lastResult ChatStreamValidationResult

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation to get the stream
		responseChannel, err := operation()

		// If we have an error getting the stream, check if we should retry
		if err != nil {
			// Log error with ❌ prefix for first attempt
			if attempt == 1 {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				t.Logf("❌ Chat stream request failed (attempt %d/%d) for %s: %s", attempt, config.MaxAttempts, context.ScenarioName, errorMsg)
			}

			// Check if we should retry
			if attempt < config.MaxAttempts {
				var shouldRetry bool
				var retryReason string

				// ALWAYS retry on timeout errors
				if isTimeoutError(err) {
					shouldRetry = true
					retryReason = fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				} else {
					// Check retry conditions
					shouldRetryFromConditions, conditionReason := checkStreamRetryConditions(err, context, config.Conditions)
					if shouldRetryFromConditions {
						shouldRetry = true
						if !strings.Contains(conditionReason, "❌") {
							retryReason = fmt.Sprintf("❌ %s", conditionReason)
						} else {
							retryReason = conditionReason
						}
					} else {
						// Retry on any error for streaming
						shouldRetry = true
						errorMsg := GetErrorMessage(err)
						if !strings.Contains(errorMsg, "❌") {
							errorMsg = fmt.Sprintf("❌ %s", errorMsg)
						}
						retryReason = fmt.Sprintf("❌ streaming error (will retry): %s", errorMsg)
					}
				}

				if shouldRetry {
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					} else {
						t.Logf("🔄 Retrying chat stream request (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
					}

					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries exhausted
			if config.OnFinalFail != nil {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				config.OnFinalFail(attempt, fmt.Errorf("❌ chat stream request failed after %d attempts: %s", attempt, errorMsg), t)
			}
			return ChatStreamValidationResult{
				Passed: false,
				Errors: []string{fmt.Sprintf("❌ stream request failed: %s", GetErrorMessage(err))},
			}
		}

		if responseChannel == nil {
			if attempt < config.MaxAttempts {
				retryReason := "❌ response channel is nil"
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
			return ChatStreamValidationResult{
				Passed: false,
				Errors: []string{"❌ response channel is nil"},
			}
		}

		// Validate the stream content
		validationResult := validateStream(responseChannel)
		lastResult = validationResult

		// If validation passes, we're done!
		if validationResult.Passed {
			return validationResult
		}

		// Validation failed - ALWAYS retry validation failures
		if attempt < config.MaxAttempts {
			retryReason := fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
			if len(validationResult.StreamErrors) > 0 {
				retryReason += fmt.Sprintf(" | stream errors: %s", strings.Join(validationResult.StreamErrors, "; "))
			}

			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			} else {
				t.Logf("🔄 Retrying chat stream validation (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}
	}

	// All retries exhausted - log final failure
	if config.OnFinalFail != nil {
		allErrors := append(lastResult.Errors, lastResult.StreamErrors...)
		errorMsg := strings.Join(allErrors, "; ")
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ chat stream validation failed after %d attempts: %s", config.MaxAttempts, errorMsg), t)
	} else {
		// Fallback logging if OnFinalFail is not set
		allErrors := append(lastResult.Errors, lastResult.StreamErrors...)
		errorMsg := strings.Join(allErrors, "; ")
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		t.Logf("❌ Chat stream validation failed after %d attempts for %s: %s", config.MaxAttempts, context.ScenarioName, errorMsg)
	}

	return lastResult
}

type ImageGenerationStreamValidationResult struct {
	Passed       bool
	Errors       []string
	ReceivedData bool
	StreamErrors []string
	LastLatency  int64
}

// WithImageGenerationStreamRetry wraps an image generation streaming operation with retry logic that includes stream content validation
// This function wraps the entire operation (request + stream reading + validation) and retries on validation failures
func WithImageGenerationStreamRetry(
	t *testing.T,
	config TestRetryConfig,
	context TestRetryContext,
	operation func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError),
	validateStream func(chan *schemas.BifrostStreamChunk) ImageGenerationStreamValidationResult) ImageGenerationStreamValidationResult {

	var lastResult ImageGenerationStreamValidationResult

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		context.AttemptNumber = attempt

		// Execute the operation to get the stream
		responseChannel, err := operation()

		// If we have an error getting the stream, check if we should retry
		if err != nil {
			// Log error with ❌ prefix for first attempt
			if attempt == 1 {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				t.Logf("❌ Image generation stream request failed (attempt %d/%d) for %s: %s", attempt, config.MaxAttempts, context.ScenarioName, errorMsg)
			}

			// Check if we should retry
			if attempt < config.MaxAttempts {
				var shouldRetry bool
				var retryReason string

				// ALWAYS retry on timeout errors
				if isTimeoutError(err) {
					shouldRetry = true
					retryReason = fmt.Sprintf("❌ timeout error detected: %s", GetErrorMessage(err))
				} else {
					// Check retry conditions
					shouldRetryFromConditions, conditionReason := checkStreamRetryConditions(err, context, config.Conditions)
					if shouldRetryFromConditions {
						shouldRetry = true
						if !strings.Contains(conditionReason, "❌") {
							retryReason = fmt.Sprintf("❌ %s", conditionReason)
						} else {
							retryReason = conditionReason
						}
					} else {
						// Retry on any error for streaming
						shouldRetry = true
						errorMsg := GetErrorMessage(err)
						if !strings.Contains(errorMsg, "❌") {
							errorMsg = fmt.Sprintf("❌ %s", errorMsg)
						}
						retryReason = fmt.Sprintf("❌ streaming error (will retry): %s", errorMsg)
					}
				}

				if shouldRetry {
					if config.OnRetry != nil {
						config.OnRetry(attempt, retryReason, t)
					} else {
						t.Logf("🔄 Retrying image generation stream request (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
					}

					delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
					time.Sleep(delay)
					continue
				}
			}

			// All retries exhausted
			if config.OnFinalFail != nil {
				errorMsg := GetErrorMessage(err)
				if !strings.Contains(errorMsg, "❌") {
					errorMsg = fmt.Sprintf("❌ %s", errorMsg)
				}
				config.OnFinalFail(attempt, fmt.Errorf("❌ image generation stream request failed after %d attempts: %s", attempt, errorMsg), t)
			}
			return ImageGenerationStreamValidationResult{
				Passed: false,
				Errors: []string{fmt.Sprintf("❌ stream request failed: %s", GetErrorMessage(err))},
			}
		}

		if responseChannel == nil {
			if attempt < config.MaxAttempts {
				retryReason := "❌ response channel is nil"
				if config.OnRetry != nil {
					config.OnRetry(attempt, retryReason, t)
				}
				delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
				time.Sleep(delay)
				continue
			}
			return ImageGenerationStreamValidationResult{
				Passed: false,
				Errors: []string{"❌ response channel is nil"},
			}
		}

		// Validate the stream content
		validationResult := validateStream(responseChannel)
		lastResult = validationResult

		// If validation passes, we're done!
		if validationResult.Passed {
			return validationResult
		}

		// Validation failed - ALWAYS retry validation failures
		if attempt < config.MaxAttempts {
			retryReason := fmt.Sprintf("❌ validation failure (content/functionality check): %s", strings.Join(validationResult.Errors, "; "))
			if len(validationResult.StreamErrors) > 0 {
				retryReason += fmt.Sprintf(" | stream errors: %s", strings.Join(validationResult.StreamErrors, "; "))
			}

			if config.OnRetry != nil {
				config.OnRetry(attempt, retryReason, t)
			} else {
				t.Logf("🔄 Retrying image generation stream validation (attempt %d/%d) for %s: %s", attempt+1, config.MaxAttempts, context.ScenarioName, retryReason)
			}

			delay := calculateRetryDelay(attempt-1, config.BaseDelay, config.MaxDelay)
			time.Sleep(delay)
			continue
		}
	}

	// All retries exhausted - log final failure
	if config.OnFinalFail != nil {
		allErrors := append(lastResult.Errors, lastResult.StreamErrors...)
		errorMsg := strings.Join(allErrors, "; ")
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		config.OnFinalFail(config.MaxAttempts, fmt.Errorf("❌ image generation stream validation failed after %d attempts: %s", config.MaxAttempts, errorMsg), t)
	} else {
		// Fallback logging if OnFinalFail is not set
		allErrors := append(lastResult.Errors, lastResult.StreamErrors...)
		errorMsg := strings.Join(allErrors, "; ")
		if !strings.Contains(errorMsg, "❌") {
			errorMsg = fmt.Sprintf("❌ %s", errorMsg)
		}
		t.Logf("❌ Image generation stream validation failed after %d attempts for %s: %s", config.MaxAttempts, context.ScenarioName, errorMsg)
	}

	return lastResult
}
