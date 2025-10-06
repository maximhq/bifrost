package scenarios

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestRetryCondition defines an interface for checking if a test operation should be retried
// This focuses specifically on LLM behavior inconsistencies, not HTTP errors (handled by Bifrost core)
type TestRetryCondition interface {
	ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string)
	GetConditionName() string
}

// TestRetryContext provides context information for retry decisions
type TestRetryContext struct {
	ScenarioName     string                 // Name of the test scenario
	AttemptNumber    int                    // Current attempt number (1-based)
	ExpectedBehavior map[string]interface{} // What we expected to happen
	TestMetadata     map[string]interface{} // Additional context for retry decisions
}

// TestRetryConfig configures retry behavior for test scenarios
type TestRetryConfig struct {
	MaxAttempts int                                              // Maximum retry attempts (including initial attempt)
	BaseDelay   time.Duration                                    // Base delay between retries
	MaxDelay    time.Duration                                    // Maximum delay between retries
	Conditions  []TestRetryCondition                             // Conditions that trigger retries
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
			validationResult := ValidateResponse(t, response, err, expectations, scenarioName)

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
			// &WrongToolSequenceCondition{ExpectedTools: expectedTools},
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

// SpeechRetryConfig creates a retry config for speech synthesis tests
func SpeechRetryConfig() TestRetryConfig {
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

// TranscriptionRetryConfig creates a retry config for transcription tests
func TranscriptionRetryConfig() TestRetryConfig {
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

// EmbeddingRetryConfig creates a retry config for embedding tests
func EmbeddingRetryConfig() TestRetryConfig {
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
	ChatCompletionsResponse *schemas.BifrostResponse
	ChatCompletionsError    *schemas.BifrostError
	ResponsesAPIResponse    *schemas.BifrostResponse
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
	chatOperation func() (*schemas.BifrostResponse, *schemas.BifrostError),
	responsesOperation func() (*schemas.BifrostResponse, *schemas.BifrostError),
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
			chatValidationResult := ValidateResponse(t, chatResponse, chatErr, expectations, scenarioName+" (Chat Completions)")
			chatValidationPassed = chatValidationResult.Passed
			chatValidationErrors = chatValidationResult.Errors
		}

		// Validate Responses API response
		var responsesValidationPassed bool
		var responsesValidationErrors []string
		if responsesResponse != nil {
			responsesValidationResult := ValidateResponse(t, responsesResponse, responsesErr, expectations, scenarioName+" (Responses API)")
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
			// Check retry conditions for both responses
			chatShouldRetry, chatRetryReason := checkRetryConditions(chatResponse, chatErr, context, config.Conditions)
			responsesShouldRetry, responsesRetryReason := checkRetryConditions(responsesResponse, responsesErr, context, config.Conditions)

			shouldRetry := chatShouldRetry || responsesShouldRetry

			if shouldRetry {
				// Log retry attempt
				if config.OnRetry != nil {
					var reasons []string
					if chatShouldRetry {
						reasons = append(reasons, fmt.Sprintf("Chat Completions: %s", chatRetryReason))
					}
					if !chatValidationPassed {
						reasons = append(reasons, fmt.Sprintf("Chat Completions Validation: %s", strings.Join(chatValidationErrors, "; ")))
					}
					if responsesShouldRetry {
						reasons = append(reasons, fmt.Sprintf("Responses API: %s", responsesRetryReason))
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
		return EmbeddingRetryConfig()
	case "SpeechSynthesis", "SpeechSynthesisHD", "SpeechSynthesis_Voice": // 🔊 Speech synthesis tests
		return SpeechRetryConfig()
	case "SpeechSynthesisStream", "SpeechSynthesisStreamHD", "SpeechSynthesisStreamVoice": // 🔊 Streaming speech tests
		return SpeechStreamRetryConfig()
	case "Transcription", "TranscriptionStream": // 🎙️ Transcription tests
		return TranscriptionRetryConfig()
	default:
		// For basic scenarios like SimpleChat, TextCompletion
		return DefaultTestRetryConfig()
	}
}
