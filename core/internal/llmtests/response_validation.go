package llmtests

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// RESPONSE VALIDATION FRAMEWORK
// =============================================================================

// ResponseExpectations defines what we expect from a response
type ResponseExpectations struct {
	// Basic structure expectations
	ShouldHaveContent    bool    // Response should have non-empty content
	ExpectedChoiceCount  int     // Expected number of choices (0 = any)
	ExpectedFinishReason *string // Expected finish reason

	// Content expectations
	ShouldContainKeywords []string       // Content should contain ALL these keywords (AND logic)
	ShouldContainAnyOf    []string       // Content should contain AT LEAST ONE of these keywords (OR logic)
	ShouldNotContainWords []string       // Content should NOT contain these words
	ContentPattern        *regexp.Regexp // Content should match this pattern
	IsRelevantToPrompt    bool           // Content should be relevant to the original prompt

	// Tool calling expectations
	ExpectedToolCalls          []ToolCallExpectation // Expected tool calls
	ShouldNotHaveFunctionCalls bool                  // Should not have any function calls

	// Technical expectations
	ShouldHaveUsageStats bool // Should have token usage information
	ShouldHaveTimestamps bool // Should have created timestamp
	ShouldHaveModel      bool // Should have model field
	ShouldHaveLatency    bool // Should have latency information in ExtraFields

	// Provider-specific expectations
	ProviderSpecific map[string]interface{} // Provider-specific validation data
}

// ToolCallExpectation defines expectations for a specific tool call
type ToolCallExpectation struct {
	FunctionName     string                 // Expected function name
	RequiredArgs     []string               // Arguments that must be present
	ForbiddenArgs    []string               // Arguments that should NOT be present
	ArgumentTypes    map[string]string      // Expected types for arguments ("string", "number", "boolean", "array", "object")
	ArgumentValues   map[string]interface{} // Specific expected values for arguments
	ValidateArgsJSON bool                   // Whether arguments should be valid JSON
}

// ValidationResult contains the results of response validation
type ValidationResult struct {
	Passed           bool                   // Overall validation result
	Errors           []string               // List of validation errors
	Warnings         []string               // List of validation warnings
	MetricsCollected map[string]interface{} // Collected metrics for analysis
}

// =============================================================================
// MAIN VALIDATION FUNCTIONS
// =============================================================================

// ValidateChatResponse performs comprehensive validation for chat completion responses
func ValidateChatResponse(t *testing.T, response *schemas.BifrostChatResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate basic structure
	validateChatBasicStructure(t, response, expectations, &result)

	// Validate content
	validateChatContent(t, response, expectations, &result)

	// Validate tool calls
	validateChatToolCalls(t, response, expectations, &result)

	// Validate technical fields
	validateChatTechnicalFields(t, response, expectations, &result)

	// Collect metrics
	collectChatResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateTextCompletionResponse performs comprehensive validation for text completion responses
func ValidateTextCompletionResponse(t *testing.T, response *schemas.BifrostTextCompletionResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate basic structure
	validateTextCompletionBasicStructure(t, response, expectations, &result)

	// Validate content
	validateTextCompletionContent(t, response, expectations, &result)

	// Validate technical fields
	validateTextCompletionTechnicalFields(t, response, expectations, &result)

	// Collect metrics
	collectTextCompletionResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateResponsesResponse performs comprehensive validation for Responses API responses
func ValidateResponsesResponse(t *testing.T, response *schemas.BifrostResponsesResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate basic structure
	validateResponsesBasicStructure(response, expectations, &result)

	// Validate content
	validateResponsesContent(t, response, expectations, &result)

	// Validate tool calls
	validateResponsesToolCalls(t, response, expectations, &result)

	// Validate technical fields
	validateResponsesTechnicalFields(t, response, expectations, &result)

	// Collect metrics
	collectResponsesResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateSpeechResponse performs comprehensive validation for speech synthesis responses
func ValidateSpeechResponse(t *testing.T, response *schemas.BifrostSpeechResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate speech synthesis specific fields
	validateSpeechSynthesisResponse(t, response, expectations, &result)

	// Collect metrics
	collectSpeechResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateImageGenerationResponse performs comprehensive validation for image generation responses
func ValidateImageGenerationResponse(t *testing.T, response *schemas.BifrostImageGenerationResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate image generation specific fields
	validateImageGenerationFields(t, response, expectations, &result)

	// Collect metrics
	collectImageGenerationResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateTranscriptionResponse performs comprehensive validation for transcription responses
func ValidateTranscriptionResponse(t *testing.T, response *schemas.BifrostTranscriptionResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate transcription specific fields
	validateTranscriptionFields(t, response, expectations, &result)

	// Collect metrics
	collectTranscriptionResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateListModelsResponse performs comprehensive validation for list models responses
func ValidateListModelsResponse(t *testing.T, response *schemas.BifrostListModelsResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate list models specific fields
	validateListModelsFields(t, response, expectations, &result)

	// Collect metrics
	collectListModelsResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateEmbeddingResponse performs comprehensive validation for embedding responses
func ValidateEmbeddingResponse(t *testing.T, response *schemas.BifrostEmbeddingResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate embedding specific fields
	validateEmbeddingFields(t, response, expectations, &result)

	// Collect metrics
	collectEmbeddingResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// ValidateCountTokensResponse performs comprehensive validation for count tokens responses
func ValidateCountTokensResponse(t *testing.T, response *schemas.BifrostCountTokensResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	// If response is nil when we expected success, that's a failure
	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	validateCountTokensFields(t, response, expectations, &result)
	collectCountTokensResponseMetrics(response, &result)
	logValidationResults(t, result, scenarioName)

	return result
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - CHAT RESPONSE
// =============================================================================

// validateChatBasicStructure checks the basic structure of the chat response
func validateChatBasicStructure(t *testing.T, response *schemas.BifrostChatResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check choice count
	if expectations.ExpectedChoiceCount > 0 {
		actualCount := 0
		if response.Choices != nil {
			actualCount = len(response.Choices)
		}
		if actualCount != expectations.ExpectedChoiceCount {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Expected %d choices, got %d", expectations.ExpectedChoiceCount, actualCount))
		}
	}

	// Check finish reasons
	if expectations.ExpectedFinishReason != nil && response.Choices != nil {
		for i, choice := range response.Choices {
			if choice.FinishReason == nil {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Choice %d has no finish reason", i))
			} else if *choice.FinishReason != *expectations.ExpectedFinishReason {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Choice %d has finish reason '%s', expected '%s'",
						i, *choice.FinishReason, *expectations.ExpectedFinishReason))
			}
		}
	}
}

// validateChatContent checks the content of the chat response
func validateChatContent(t *testing.T, response *schemas.BifrostChatResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Skip content validation for responses that don't have text content
	if !expectations.ShouldHaveContent {
		return
	}

	content := GetChatContent(response)

	// Check if content exists when expected
	if expectations.ShouldHaveContent {
		if strings.TrimSpace(content) == "" {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected content but got empty response")
			return
		}
	}
	// Check required keywords (AND logic - ALL must be present)
	// Note: Converted to warnings as LLMs are non-deterministic and tests focus on functionality
	lowerContent := strings.ToLower(content)
	for _, keyword := range expectations.ShouldContainKeywords {
		if !strings.Contains(lowerContent, strings.ToLower(keyword)) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content expected to contain keyword '%s' but doesn't (LLMs are non-deterministic). Actual content: %s",
					keyword, truncateContentForError(content, 200)))
		}
	}

	// Check OR keywords (OR logic - AT LEAST ONE must be present)
	// Note: Converted to warnings as LLMs are non-deterministic
	if len(expectations.ShouldContainAnyOf) > 0 {
		foundAny := false
		for _, keyword := range expectations.ShouldContainAnyOf {
			if strings.Contains(lowerContent, strings.ToLower(keyword)) {
				foundAny = true
				break
			}
		}
		if !foundAny {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content expected to contain at least one of these keywords: %v, but doesn't (LLMs are non-deterministic). Actual content: %s",
					expectations.ShouldContainAnyOf, truncateContentForError(content, 200)))
		}
	}

	// Check forbidden words - Keep as warnings since these are often false positives with LLMs
	for _, word := range expectations.ShouldNotContainWords {
		if strings.Contains(lowerContent, strings.ToLower(word)) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content contains word '%s' which was not expected (may be false positive with LLMs). Actual content: %s",
					word, truncateContentForError(content, 200)))
		}
	}

	// Check content pattern - Converted to warnings
	if expectations.ContentPattern != nil {
		if !expectations.ContentPattern.MatchString(content) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content doesn't match expected pattern: %s (LLMs are non-deterministic). Actual content: %s",
					expectations.ContentPattern.String(), truncateContentForError(content, 200)))
		}
	}

	// Store content for metrics
	result.MetricsCollected["content_word_count"] = len(strings.Fields(content))
}

// validateChatToolCalls checks tool calling aspects of chat response
func validateChatToolCalls(t *testing.T, response *schemas.BifrostChatResponse, expectations ResponseExpectations, result *ValidationResult) {
	totalToolCalls := 0

	// Count tool calls from Chat Completions API
	if response.Choices != nil {
		for _, choice := range response.Choices {
			if choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ToolCalls != nil {
				totalToolCalls += len(choice.Message.ChatAssistantMessage.ToolCalls)
			}
		}
	}

	// Check if we should have no function calls
	if expectations.ShouldNotHaveFunctionCalls && totalToolCalls > 0 {
		result.Passed = false
		actualToolNames := extractChatToolCallNames(response)
		result.Errors = append(result.Errors,
			fmt.Sprintf("Expected no function calls but found %d: %v", totalToolCalls, actualToolNames))
	}

	// Validate specific tool calls
	if len(expectations.ExpectedToolCalls) > 0 {
		validateChatSpecificToolCalls(response, expectations.ExpectedToolCalls, result)
	}

	result.MetricsCollected["tool_call_count"] = totalToolCalls
}

// validateChatTechnicalFields checks technical aspects of the chat response
func validateChatTechnicalFields(t *testing.T, response *schemas.BifrostChatResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check usage stats
	if expectations.ShouldHaveUsageStats {
		if response.Usage == nil {
			result.Warnings = append(result.Warnings, "Expected usage statistics but not present")
		} else {
			// Validate usage makes sense
			if response.Usage.TotalTokens < response.Usage.PromptTokens {
				result.Warnings = append(result.Warnings, "Total tokens less than prompt tokens")
			}
			if response.Usage.TotalTokens < response.Usage.CompletionTokens {
				result.Warnings = append(result.Warnings, "Total tokens less than completion tokens")
			}
		}
	}

	// Check timestamps
	if expectations.ShouldHaveTimestamps {
		if response.Created == 0 {
			result.Warnings = append(result.Warnings, "Expected created timestamp but not present")
		}
	}

	// Check model field
	if expectations.ShouldHaveModel {
		if strings.TrimSpace(response.Model) == "" {
			result.Warnings = append(result.Warnings, "Expected model field but not present or empty")
		}
	}

	// Check latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Check cached tokens percentage (for prompt caching tests)
	if expectations.ProviderSpecific != nil {
		if minPercentage, ok := expectations.ProviderSpecific["min_cached_tokens_percentage"].(float64); ok {
			if response.Usage == nil {
				result.Passed = false
				result.Errors = append(result.Errors, "Expected usage statistics for cached tokens validation but not present")
			} else if response.Usage.PromptTokensDetails == nil {
				result.Passed = false
				result.Errors = append(result.Errors, "Expected prompt tokens details for cached tokens validation but not present")
			} else {
				cachedTokens := response.Usage.PromptTokensDetails.CachedTokens
				promptTokens := response.Usage.PromptTokens
				if promptTokens > 0 {
					cachedPercentage := float64(cachedTokens) / float64(promptTokens)
					result.MetricsCollected["cached_tokens"] = cachedTokens
					result.MetricsCollected["prompt_tokens"] = promptTokens
					result.MetricsCollected["cached_percentage"] = cachedPercentage
					if cachedPercentage < minPercentage {
						result.Passed = false
						result.Errors = append(result.Errors, fmt.Sprintf("Cached tokens percentage %.2f%% is below required minimum %.2f%% (cached: %d, prompt: %d)",
							cachedPercentage*100, minPercentage*100, cachedTokens, promptTokens))
					}
				} else {
					result.Passed = false
					result.Errors = append(result.Errors, "Prompt tokens is 0, cannot validate cached tokens percentage")
				}
			}
		}
	}
}

// collectChatResponseMetrics collects metrics from the chat response for analysis
func collectChatResponseMetrics(response *schemas.BifrostChatResponse, result *ValidationResult) {
	result.MetricsCollected["choice_count"] = len(response.Choices)
	result.MetricsCollected["has_usage"] = response.Usage != nil
	result.MetricsCollected["has_model"] = response.Model != ""
	result.MetricsCollected["has_timestamp"] = response.Created > 0

	if response.Usage != nil {
		result.MetricsCollected["total_tokens"] = response.Usage.TotalTokens
		result.MetricsCollected["prompt_tokens"] = response.Usage.PromptTokens
		result.MetricsCollected["completion_tokens"] = response.Usage.CompletionTokens
	}
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - TEXT COMPLETION RESPONSE
// =============================================================================

// validateTextCompletionBasicStructure checks the basic structure of the text completion response
func validateTextCompletionBasicStructure(t *testing.T, response *schemas.BifrostTextCompletionResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check choice count
	if expectations.ExpectedChoiceCount > 0 {
		actualCount := 0
		if response.Choices != nil {
			actualCount = len(response.Choices)
		}
		if actualCount != expectations.ExpectedChoiceCount {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Expected %d choices, got %d", expectations.ExpectedChoiceCount, actualCount))
		}
	}

	// Check finish reasons
	if expectations.ExpectedFinishReason != nil && response.Choices != nil {
		for i, choice := range response.Choices {
			if choice.FinishReason == nil {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Choice %d has no finish reason", i))
			} else if *choice.FinishReason != *expectations.ExpectedFinishReason {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("Choice %d has finish reason '%s', expected '%s'",
						i, *choice.FinishReason, *expectations.ExpectedFinishReason))
			}
		}
	}
}

// validateTextCompletionContent checks the content of the text completion response
func validateTextCompletionContent(t *testing.T, response *schemas.BifrostTextCompletionResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Skip content validation for responses that don't have text content
	if !expectations.ShouldHaveContent {
		return
	}

	content := GetTextCompletionContent(response)

	// Check if content exists when expected
	if expectations.ShouldHaveContent {
		if strings.TrimSpace(content) == "" {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected content but got empty response")
			return
		}
	}

	// Check required keywords (AND logic - ALL must be present)
	// Note: Converted to warnings as LLMs are non-deterministic and tests focus on functionality
	lowerContent := strings.ToLower(content)
	for _, keyword := range expectations.ShouldContainKeywords {
		if !strings.Contains(lowerContent, strings.ToLower(keyword)) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content expected to contain keyword '%s' but doesn't (LLMs are non-deterministic). Actual content: %s",
					keyword, truncateContentForError(content, 200)))
		}
	}

	// Check OR keywords (OR logic - AT LEAST ONE must be present)
	// Note: Converted to warnings as LLMs are non-deterministic
	if len(expectations.ShouldContainAnyOf) > 0 {
		foundAny := false
		for _, keyword := range expectations.ShouldContainAnyOf {
			if strings.Contains(lowerContent, strings.ToLower(keyword)) {
				foundAny = true
				break
			}
		}
		if !foundAny {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content expected to contain at least one of these keywords: %v, but doesn't (LLMs are non-deterministic). Actual content: %s",
					expectations.ShouldContainAnyOf, truncateContentForError(content, 200)))
		}
	}

	// Check forbidden words - Keep as warnings since these are often false positives with LLMs
	for _, word := range expectations.ShouldNotContainWords {
		if strings.Contains(lowerContent, strings.ToLower(word)) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content contains word '%s' which was not expected (may be false positive with LLMs). Actual content: %s",
					word, truncateContentForError(content, 200)))
		}
	}

	// Check content pattern - Converted to warnings
	if expectations.ContentPattern != nil {
		if !expectations.ContentPattern.MatchString(content) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content doesn't match expected pattern: %s (LLMs are non-deterministic). Actual content: %s",
					expectations.ContentPattern.String(), truncateContentForError(content, 200)))
		}
	}

	// Store content for metrics
	result.MetricsCollected["content_word_count"] = len(strings.Fields(content))
}

// validateTextCompletionTechnicalFields checks technical aspects of the text completion response
func validateTextCompletionTechnicalFields(t *testing.T, response *schemas.BifrostTextCompletionResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check usage stats
	if expectations.ShouldHaveUsageStats {
		if response.Usage == nil {
			result.Warnings = append(result.Warnings, "Expected usage statistics but not present")
		} else {
			// Validate usage makes sense
			if response.Usage.TotalTokens < response.Usage.PromptTokens {
				result.Warnings = append(result.Warnings, "Total tokens less than prompt tokens")
			}
			if response.Usage.TotalTokens < response.Usage.CompletionTokens {
				result.Warnings = append(result.Warnings, "Total tokens less than completion tokens")
			}
		}
	}

	// Check timestamps - Text completion responses don't have a Created field
	if expectations.ShouldHaveTimestamps {
		// Text completion responses don't have timestamps, so skip this check
		result.Warnings = append(result.Warnings, "Text completion responses don't support timestamp validation")
	}

	// Check model field
	if expectations.ShouldHaveModel {
		if strings.TrimSpace(response.Model) == "" {
			result.Warnings = append(result.Warnings, "Expected model field but not present or empty")
		}
	}

	// Check latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}
}

// collectTextCompletionResponseMetrics collects metrics from the text completion response for analysis
func collectTextCompletionResponseMetrics(response *schemas.BifrostTextCompletionResponse, result *ValidationResult) {
	result.MetricsCollected["choice_count"] = len(response.Choices)
	result.MetricsCollected["has_usage"] = response.Usage != nil
	result.MetricsCollected["has_model"] = response.Model != ""
	result.MetricsCollected["has_timestamp"] = false // Text completion responses don't have timestamps

	if response.Usage != nil {
		result.MetricsCollected["total_tokens"] = response.Usage.TotalTokens
		result.MetricsCollected["prompt_tokens"] = response.Usage.PromptTokens
		result.MetricsCollected["completion_tokens"] = response.Usage.CompletionTokens
	}
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - RESPONSES API
// =============================================================================

// validateResponsesBasicStructure checks the basic structure of the Responses API response
func validateResponsesBasicStructure(response *schemas.BifrostResponsesResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check choice count
	if expectations.ExpectedChoiceCount > 0 {
		actualCount := 0
		if response.Output != nil {
			// For Responses API, count "logical choices" instead of raw message count
			// Group related messages (text + tool calls) as one logical choice
			actualCount = countLogicalChoicesInResponsesAPI(response.Output)
		}
		if actualCount != expectations.ExpectedChoiceCount {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Expected %d choices, got %d", expectations.ExpectedChoiceCount, actualCount))
		}
	}

	provider := response.ExtraFields.Provider
	model := response.ExtraFields.ModelDeployment

	// Verify top level status is present for OpenAI and Azure with  non-Claude models
	if provider != "" && (provider == schemas.OpenAI || provider == schemas.Azure) && !strings.Contains(strings.ToLower(model), "claude") {
		if response.Status == nil {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected status but not present")
		}
	}
}

// validateResponsesContent checks the content of the Responses API response
func validateResponsesContent(t *testing.T, response *schemas.BifrostResponsesResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Skip content validation for responses that don't have text content
	if !expectations.ShouldHaveContent {
		return
	}

	content := GetResponsesContent(response)

	// Check if content exists when expected
	if expectations.ShouldHaveContent {
		if strings.TrimSpace(content) == "" {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected content but got empty response")
			return
		}
	}

	// Check required keywords (AND logic - ALL must be present)
	// Note: Converted to warnings as LLMs are non-deterministic and tests focus on functionality
	lowerContent := strings.ToLower(content)
	for _, keyword := range expectations.ShouldContainKeywords {
		if !strings.Contains(lowerContent, strings.ToLower(keyword)) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content expected to contain keyword '%s' but doesn't (LLMs are non-deterministic). Actual content: %s",
					keyword, truncateContentForError(content, 200)))
		}
	}

	// Check OR keywords (OR logic - AT LEAST ONE must be present)
	// Note: Converted to warnings as LLMs are non-deterministic
	if len(expectations.ShouldContainAnyOf) > 0 {
		foundAny := false
		for _, keyword := range expectations.ShouldContainAnyOf {
			if strings.Contains(lowerContent, strings.ToLower(keyword)) {
				foundAny = true
				break
			}
		}
		if !foundAny {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content expected to contain at least one of these keywords: %v, but doesn't (LLMs are non-deterministic). Actual content: %s",
					expectations.ShouldContainAnyOf, truncateContentForError(content, 200)))
		}
	}

	// Check forbidden words - Keep as warnings since these are often false positives with LLMs
	for _, word := range expectations.ShouldNotContainWords {
		if strings.Contains(lowerContent, strings.ToLower(word)) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content contains word '%s' which was not expected (may be false positive with LLMs). Actual content: %s",
					word, truncateContentForError(content, 200)))
		}
	}

	// Check content pattern - Converted to warnings
	if expectations.ContentPattern != nil {
		if !expectations.ContentPattern.MatchString(content) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Content doesn't match expected pattern: %s (LLMs are non-deterministic). Actual content: %s",
					expectations.ContentPattern.String(), truncateContentForError(content, 200)))
		}
	}

	// Store content for metrics
	result.MetricsCollected["content_word_count"] = len(strings.Fields(content))
}

// validateResponsesToolCalls checks tool calling aspects of Responses API response
func validateResponsesToolCalls(t *testing.T, response *schemas.BifrostResponsesResponse, expectations ResponseExpectations, result *ValidationResult) {
	totalToolCalls := 0

	// Count tool calls from Responses API
	if response.Output != nil {
		for _, output := range response.Output {
			// Check if this message contains tool call data regardless of Type
			if output.ResponsesToolMessage != nil {
				totalToolCalls++
			}
		}
	}

	// Check if we should have no function calls
	if expectations.ShouldNotHaveFunctionCalls && totalToolCalls > 0 {
		result.Passed = false
		actualToolNames := extractResponsesToolCallNames(response)
		result.Errors = append(result.Errors,
			fmt.Sprintf("Expected no function calls but found %d: %v", totalToolCalls, actualToolNames))
	}

	// Validate specific tool calls
	if len(expectations.ExpectedToolCalls) > 0 {
		validateResponsesSpecificToolCalls(response, expectations.ExpectedToolCalls, result)
	}

	result.MetricsCollected["tool_call_count"] = totalToolCalls
}

// validateResponsesTechnicalFields checks technical aspects of the Responses API response
func validateResponsesTechnicalFields(t *testing.T, response *schemas.BifrostResponsesResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check usage stats
	if expectations.ShouldHaveUsageStats {
		if response.Usage == nil {
			result.Warnings = append(result.Warnings, "Expected usage statistics but not present")
		}
	}

	// Check timestamps
	if expectations.ShouldHaveTimestamps {
		if response.CreatedAt == 0 {
			result.Warnings = append(result.Warnings, "Expected created timestamp but not present")
		}
	}

	// Check latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}
}

// collectResponsesResponseMetrics collects metrics from the Responses API response for analysis
func collectResponsesResponseMetrics(response *schemas.BifrostResponsesResponse, result *ValidationResult) {
	if response.Output != nil {
		result.MetricsCollected["choice_count"] = len(response.Output)
	}
	result.MetricsCollected["has_usage"] = response.Usage != nil
	result.MetricsCollected["has_timestamp"] = response.CreatedAt > 0

	if response.Usage != nil {
		// Responses API has different usage structure
		result.MetricsCollected["usage_present"] = true
	}
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - SPEECH RESPONSE
// =============================================================================

// validateSpeechSynthesisResponse validates speech synthesis responses
func validateSpeechSynthesisResponse(t *testing.T, response *schemas.BifrostSpeechResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check if response has speech data
	if response.Audio == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Speech synthesis response missing Audio field")
		return
	}

	// Check if audio data exists
	shouldHaveAudio, _ := expectations.ProviderSpecific["should_have_audio"].(bool)
	if shouldHaveAudio && response.Audio == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Speech synthesis response missing audio data")
		return
	}

	// Check minimum audio bytes
	if minBytes, ok := expectations.ProviderSpecific["min_audio_bytes"].(int); ok {
		if response.Audio != nil {
			actualSize := len(response.Audio)
			if actualSize < minBytes {
				result.Passed = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("Audio data too small: got %d bytes, expected at least %d", actualSize, minBytes))
			} else {
				result.MetricsCollected["audio_bytes"] = actualSize
			}
		}
	}

	// Validate audio format if specified
	if expectedFormat, ok := expectations.ProviderSpecific["expected_format"].(string); ok {
		// This could be extended to validate actual audio format based on file headers
		result.MetricsCollected["expected_audio_format"] = expectedFormat
	}

	// Check latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	result.MetricsCollected["speech_validation"] = "completed"
}

// collectSpeechResponseMetrics collects metrics from the speech response for analysis
func collectSpeechResponseMetrics(response *schemas.BifrostSpeechResponse, result *ValidationResult) {
	result.MetricsCollected["has_audio"] = response.Audio != nil
	if response.Audio != nil {
		result.MetricsCollected["audio_size"] = len(response.Audio)
	}
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - TRANSCRIPTION RESPONSE
// =============================================================================

// validateTranscriptionFields validates transcription responses
func validateTranscriptionFields(t *testing.T, response *schemas.BifrostTranscriptionResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check if transcribed text exists
	shouldHaveTranscription, _ := expectations.ProviderSpecific["should_have_transcription"].(bool)
	if shouldHaveTranscription && response.Text == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "Transcription response missing transcribed text")
		return
	}

	// Check minimum transcription length
	if minLength, ok := expectations.ProviderSpecific["min_transcription_length"].(int); ok {
		actualLength := len(response.Text)
		if actualLength < minLength {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Transcribed text too short: got %d characters, expected at least %d", actualLength, minLength))
		} else {
			result.MetricsCollected["transcription_length"] = actualLength
		}
	}

	// Check for common transcription failure indicators
	transcribedText := strings.ToLower(response.Text)
	for _, errorPhrase := range expectations.ShouldNotContainWords {
		if strings.Contains(transcribedText, errorPhrase) {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Transcribed text contains error indicator: '%s'", errorPhrase))
		}
	}

	// Validate additional transcription fields if available
	if response.Language != nil {
		result.MetricsCollected["detected_language"] = *response.Language
	}
	if response.Duration != nil {
		result.MetricsCollected["audio_duration"] = *response.Duration
	}

	// Check latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	result.MetricsCollected["transcription_validation"] = "completed"
}

// collectTranscriptionResponseMetrics collects metrics from the transcription response for analysis
func collectTranscriptionResponseMetrics(response *schemas.BifrostTranscriptionResponse, result *ValidationResult) {
	result.MetricsCollected["has_text"] = response.Text != ""
	result.MetricsCollected["text_length"] = len(response.Text)
	result.MetricsCollected["has_language"] = response.Language != nil
	result.MetricsCollected["has_duration"] = response.Duration != nil
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - IMAGE GENERATION RESPONSE
// =============================================================================

func validateImageGenerationFields(t *testing.T, response *schemas.BifrostImageGenerationResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check if response has image data
	if len(response.Data) == 0 {
		result.Passed = false
		result.Errors = append(result.Errors, "Image generation response missing image data")
		return
	}

	// Check each image has either B64JSON or URL
	for i, img := range response.Data {
		if img.B64JSON == "" && img.URL == "" {
			result.Passed = false
			result.Errors = append(result.Errors, fmt.Sprintf("Image %d has no B64JSON or URL", i))
		}
	}

	// Check minimum number of images if specified
	if expectations.ProviderSpecific != nil {
		if minImagesVal, ok := expectations.ProviderSpecific["min_images"]; ok {
			var minImages int
			var parseErr error

			// Use type switch to handle various numeric types
			switch v := minImagesVal.(type) {
			case int:
				minImages = v
			case int64:
				minImages = int(v)
			case float64:
				minImages = int(v)
			case json.Number:
				var parsed int64
				parsed, parseErr = v.Int64()
				if parseErr == nil {
					minImages = int(parsed)
				}
			default:
				parseErr = fmt.Errorf("unsupported type for min_images: %T", v)
			}

			if parseErr != nil {
				// Skip the min_images check if conversion fails, but record a warning
				result.Errors = append(result.Errors,
					fmt.Sprintf("Failed to parse min_images: %v (skipping check)", parseErr))
			} else {
				actualCount := len(response.Data)
				result.MetricsCollected["image_count"] = actualCount
				if actualCount < minImages {
					result.Passed = false
					result.Errors = append(result.Errors,
						fmt.Sprintf("Too few images: got %d, expected at least %d", actualCount, minImages))
				}
			}
		}
	}

	// Validate image size if specified
	if expectedSize, ok := expectations.ProviderSpecific["expected_size"].(string); ok {
		result.MetricsCollected["expected_size"] = expectedSize
		// Note: Actual size validation would require downloading/decoding images
	}

	// Check latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	result.MetricsCollected["image_generation_validation"] = "completed"
}

func collectImageGenerationResponseMetrics(response *schemas.BifrostImageGenerationResponse, result *ValidationResult) {
	result.MetricsCollected["image_count"] = len(response.Data)
	result.MetricsCollected["has_images"] = len(response.Data) > 0

	// Count images with URLs vs B64JSON
	urlCount := 0
	b64Count := 0
	for _, img := range response.Data {
		if img.URL != "" {
			urlCount++
		}
		if img.B64JSON != "" {
			b64Count++
		}
	}
	result.MetricsCollected["images_with_url"] = urlCount
	result.MetricsCollected["images_with_b64"] = b64Count

	if response.Usage != nil {
		result.MetricsCollected["input_tokens"] = response.Usage.InputTokens
		result.MetricsCollected["output_tokens"] = response.Usage.OutputTokens
		result.MetricsCollected["total_tokens"] = response.Usage.TotalTokens
	}
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - EMBEDDING RESPONSE
// =============================================================================

// validateEmbeddingFields validates embedding responses
func validateEmbeddingFields(t *testing.T, response *schemas.BifrostEmbeddingResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check if response has embedding data
	if len(response.Data) == 0 {
		result.Passed = false
		result.Errors = append(result.Errors, "Embedding response missing data")
		return
	}

	// Check embedding dimensions
	if expectedDimensions, ok := expectations.ProviderSpecific["expected_dimensions"].(int); ok {
		for i, embedding := range response.Data {
			var actualDimensions int
			if embedding.Embedding.EmbeddingArray != nil {
				actualDimensions = len(embedding.Embedding.EmbeddingArray)
			} else if embedding.Embedding.Embedding2DArray != nil {
				if len(embedding.Embedding.Embedding2DArray) > 0 {
					actualDimensions = len(embedding.Embedding.Embedding2DArray[0])
				}
			}
			if actualDimensions != expectedDimensions {
				result.Passed = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("Embedding %d has %d dimensions, expected %d", i, actualDimensions, expectedDimensions))
			}
		}
	}

	// Check latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	result.MetricsCollected["embedding_validation"] = "completed"
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - COUNT TOKENS RESPONSE
// =============================================================================

func validateCountTokensFields(t *testing.T, response *schemas.BifrostCountTokensResponse, expectations ResponseExpectations, result *ValidationResult) {
	_ = t

	if strings.TrimSpace(response.Model) == "" && expectations.ShouldHaveModel {
		result.Passed = false
		result.Errors = append(result.Errors, "Expected model field but got empty")
	}

	if response.InputTokens <= 0 {
		result.Passed = false
		result.Errors = append(result.Errors, fmt.Sprintf("input_tokens should be > 0, got %d", response.InputTokens))
	}

	if response.OutputTokens != nil {
		if *response.OutputTokens < 0 {
			result.Passed = false
			result.Errors = append(result.Errors, fmt.Sprintf("output_tokens should be >= 0, got %d", *response.OutputTokens))
		}
	}

	if response.TotalTokens != nil {
		if *response.TotalTokens < response.InputTokens {
			result.Passed = false
			result.Errors = append(result.Errors, fmt.Sprintf("total_tokens (%d) should be >= input_tokens (%d)", *response.TotalTokens, response.InputTokens))
		}
	}

	if response.ExtraFields.RequestType != schemas.CountTokensRequest {
		result.Passed = false
		result.Errors = append(result.Errors, fmt.Sprintf("Request type mismatch: expected %s, got %s", schemas.CountTokensRequest, response.ExtraFields.RequestType))
	}

	if expectations.ProviderSpecific != nil {
		if expectedProvider, ok := expectations.ProviderSpecific["expected_provider"].(string); ok {
			if string(response.ExtraFields.Provider) != expectedProvider {
				result.Passed = false
				result.Errors = append(result.Errors, fmt.Sprintf("Provider mismatch: expected %s, got %s", expectedProvider, string(response.ExtraFields.Provider)))
			}
		}
	}

	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency < 0 {
			result.Passed = false
			result.Errors = append(result.Errors, fmt.Sprintf("Invalid latency: %d ms (should be non-negative)", response.ExtraFields.Latency))
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	result.MetricsCollected["count_tokens_validation"] = "completed"
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS - LIST MODELS RESPONSE
// =============================================================================

// validateListModelsFields validates list models responses
func validateListModelsFields(t *testing.T, response *schemas.BifrostListModelsResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check that we have models in the response
	if len(response.Data) == 0 {
		result.Passed = false
		result.Errors = append(result.Errors, "List models response contains no models")
		return
	}

	// Validate individual model entries
	validModels := 0
	for i, model := range response.Data {
		if model.ID == "" {
			result.Passed = false
			result.Errors = append(result.Errors, fmt.Sprintf("Model at index %d has empty ID", i))
			continue
		}
		validModels++
	}

	if validModels == 0 {
		result.Passed = false
		result.Errors = append(result.Errors, "No valid models found in response")
	}

	// Validate extra fields
	if expectations.ProviderSpecific != nil {
		if expectedProvider, ok := expectations.ProviderSpecific["expected_provider"].(string); ok {
			if string(response.ExtraFields.Provider) != expectedProvider {
				result.Passed = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("Provider mismatch: expected %s, got %s", expectedProvider, string(response.ExtraFields.Provider)))
			}
		}
	}

	// Validate request type
	if response.ExtraFields.RequestType != schemas.ListModelsRequest {
		result.Passed = false
		result.Errors = append(result.Errors,
			fmt.Sprintf("Request type mismatch: expected %s, got %s", schemas.ListModelsRequest, response.ExtraFields.RequestType))
	}

	// Validate latency field
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency < 0 {
			result.Passed = false
			result.Errors = append(result.Errors, fmt.Sprintf("Invalid latency: %d ms (should be non-negative)", response.ExtraFields.Latency))
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Check minimum model count if specified
	if minModels, ok := expectations.ProviderSpecific["min_model_count"].(int); ok {
		if len(response.Data) < minModels {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Expected at least %d models, got %d", minModels, len(response.Data)))
		}
	}

	result.MetricsCollected["list_models_validation"] = "completed"
}

// collectListModelsResponseMetrics collects metrics from the list models response for analysis
func collectListModelsResponseMetrics(response *schemas.BifrostListModelsResponse, result *ValidationResult) {
	result.MetricsCollected["model_count"] = len(response.Data)
	result.MetricsCollected["has_next_page_token"] = response.NextPageToken != ""
	result.MetricsCollected["has_provider"] = response.ExtraFields.Provider != ""
	result.MetricsCollected["has_request_type"] = response.ExtraFields.RequestType != ""
	result.MetricsCollected["has_latency"] = response.ExtraFields.Latency >= 0
}

// collectEmbeddingResponseMetrics collects metrics from the embedding response for analysis
func collectEmbeddingResponseMetrics(response *schemas.BifrostEmbeddingResponse, result *ValidationResult) {
	result.MetricsCollected["has_data"] = response.Data != nil
	result.MetricsCollected["embedding_count"] = len(response.Data)
	result.MetricsCollected["has_usage"] = response.Usage != nil
	if len(response.Data) > 0 {
		var dimensions int
		if response.Data[0].Embedding.EmbeddingArray != nil {
			dimensions = len(response.Data[0].Embedding.EmbeddingArray)
		} else if len(response.Data[0].Embedding.Embedding2DArray) > 0 {
			dimensions = len(response.Data[0].Embedding.Embedding2DArray[0])
		}
		result.MetricsCollected["embedding_dimensions"] = dimensions
	}
}

func collectCountTokensResponseMetrics(response *schemas.BifrostCountTokensResponse, result *ValidationResult) {
	result.MetricsCollected["input_tokens"] = response.InputTokens
	result.MetricsCollected["has_total_tokens"] = response.TotalTokens != nil
	if response.TotalTokens != nil {
		result.MetricsCollected["total_tokens"] = *response.TotalTokens
	}
	result.MetricsCollected["has_model"] = response.Model != ""
	result.MetricsCollected["request_type"] = response.ExtraFields.RequestType
}

// =============================================================================
// BATCH API VALIDATION FUNCTIONS
// =============================================================================

// ValidateBatchCreateResponse performs comprehensive validation for batch create responses
func ValidateBatchCreateResponse(t *testing.T, response *schemas.BifrostBatchCreateResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate batch ID is present
	if response.ID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "Batch ID is empty")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["batch_id"] = response.ID
	result.MetricsCollected["status"] = response.Status
	result.MetricsCollected["has_endpoint"] = response.Endpoint != ""

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateBatchListResponse performs comprehensive validation for batch list responses
func ValidateBatchListResponse(t *testing.T, response *schemas.BifrostBatchListResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["batch_count"] = len(response.Data)
	result.MetricsCollected["has_more"] = response.HasMore

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateBatchRetrieveResponse performs comprehensive validation for batch retrieve responses
func ValidateBatchRetrieveResponse(t *testing.T, response *schemas.BifrostBatchRetrieveResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate batch ID is present
	if response.ID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "Batch ID is empty")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["batch_id"] = response.ID
	result.MetricsCollected["status"] = response.Status
	result.MetricsCollected["has_request_counts"] = response.RequestCounts.Total > 0

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateBatchCancelResponse performs comprehensive validation for batch cancel responses
func ValidateBatchCancelResponse(t *testing.T, response *schemas.BifrostBatchCancelResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate batch ID is present
	if response.ID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "Batch ID is empty")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["batch_id"] = response.ID
	result.MetricsCollected["status"] = response.Status

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateBatchResultsResponse performs comprehensive validation for batch results responses
func ValidateBatchResultsResponse(t *testing.T, response *schemas.BifrostBatchResultsResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate batch ID is present
	if response.BatchID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "Batch ID is empty")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["batch_id"] = response.BatchID
	result.MetricsCollected["results_count"] = len(response.Results)
	result.MetricsCollected["has_more"] = response.HasMore

	logValidationResults(t, result, scenarioName)
	return result
}

// =============================================================================
// FILE API VALIDATION FUNCTIONS
// =============================================================================

// ValidateFileUploadResponse performs comprehensive validation for file upload responses
func ValidateFileUploadResponse(t *testing.T, response *schemas.BifrostFileUploadResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate file ID is present
	if response.ID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "File ID is empty")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["file_id"] = response.ID
	result.MetricsCollected["filename"] = response.Filename
	result.MetricsCollected["bytes"] = response.Bytes
	result.MetricsCollected["purpose"] = response.Purpose

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateFileListResponse performs comprehensive validation for file list responses
func ValidateFileListResponse(t *testing.T, response *schemas.BifrostFileListResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["file_count"] = len(response.Data)
	result.MetricsCollected["has_more"] = response.HasMore

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateFileRetrieveResponse performs comprehensive validation for file retrieve responses
func ValidateFileRetrieveResponse(t *testing.T, response *schemas.BifrostFileRetrieveResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate file ID is present
	if response.ID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "File ID is empty")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["file_id"] = response.ID
	result.MetricsCollected["filename"] = response.Filename
	result.MetricsCollected["bytes"] = response.Bytes
	result.MetricsCollected["status"] = response.Status

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateFileDeleteResponse performs comprehensive validation for file delete responses
func ValidateFileDeleteResponse(t *testing.T, response *schemas.BifrostFileDeleteResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate file ID is present
	if response.ID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "File ID is empty")
	}

	// Validate deleted flag
	if !response.Deleted {
		result.Passed = false
		result.Errors = append(result.Errors, "File was not marked as deleted")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["file_id"] = response.ID
	result.MetricsCollected["deleted"] = response.Deleted

	logValidationResults(t, result, scenarioName)
	return result
}

// ValidateFileContentResponse performs comprehensive validation for file content responses
func ValidateFileContentResponse(t *testing.T, response *schemas.BifrostFileContentResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	if err != nil {
		result.Passed = false
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))
		LogError(t, err, scenarioName)
		return result
	}

	if response == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Response is nil")
		return result
	}

	// Validate file ID is present
	if response.FileID == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "File ID is empty")
	}

	// Validate content is present
	if len(response.Content) == 0 {
		result.Passed = false
		result.Errors = append(result.Errors, "File content is empty")
	}

	// Validate latency if expected
	if expectations.ShouldHaveLatency {
		if response.ExtraFields.Latency <= 0 {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected latency information but not present or invalid")
		} else {
			result.MetricsCollected["latency_ms"] = response.ExtraFields.Latency
		}
	}

	// Collect metrics
	result.MetricsCollected["file_id"] = response.FileID
	result.MetricsCollected["content_length"] = len(response.Content)
	result.MetricsCollected["content_type"] = response.ContentType

	logValidationResults(t, result, scenarioName)
	return result
}

// extractChatToolCallNames extracts tool call function names from chat response for error messages
func extractChatToolCallNames(response *schemas.BifrostChatResponse) []string {
	var toolNames []string

	if response.Choices != nil {
		for _, choice := range response.Choices {
			if choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ToolCalls != nil {
				for _, toolCall := range choice.Message.ChatAssistantMessage.ToolCalls {
					if toolCall.Function.Name != nil {
						toolNames = append(toolNames, *toolCall.Function.Name)
					}
				}
			}
		}
	}
	return toolNames
}

// extractResponsesToolCallNames extracts tool call function names from Responses API response for error messages
func extractResponsesToolCallNames(response *schemas.BifrostResponsesResponse) []string {
	var toolNames []string

	if response.Output != nil {
		for _, output := range response.Output {
			if output.ResponsesToolMessage != nil && output.Name != nil {
				toolNames = append(toolNames, *output.Name)
			}
		}
	}
	return toolNames
}

// validateChatSpecificToolCalls validates individual tool call expectations for chat response
func validateChatSpecificToolCalls(response *schemas.BifrostChatResponse, expectedCalls []ToolCallExpectation, result *ValidationResult) {
	for _, expected := range expectedCalls {
		found := false

		if response.Choices != nil {
			for _, message := range response.Choices {
				if message.Message.ChatAssistantMessage != nil && message.Message.ChatAssistantMessage.ToolCalls != nil {
					for _, toolCall := range message.Message.ChatAssistantMessage.ToolCalls {
						if toolCall.Function.Name != nil && *toolCall.Function.Name == expected.FunctionName {
							arguments := toolCall.Function.Arguments
							found = true
							validateSingleToolCall(arguments, expected, 0, 0, result)
							break
						}
					}
				}
			}
		}

		if !found {
			result.Passed = false
			actualToolNames := extractChatToolCallNames(response)
			if len(actualToolNames) == 0 {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Expected tool call '%s' not found (no tool calls present)", expected.FunctionName))
			} else {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Expected tool call '%s' not found. Actual tool calls found: %v",
						expected.FunctionName, actualToolNames))
			}
		}
	}
}

// validateResponsesSpecificToolCalls validates individual tool call expectations for Responses API response
func validateResponsesSpecificToolCalls(response *schemas.BifrostResponsesResponse, expectedCalls []ToolCallExpectation, result *ValidationResult) {
	for _, expected := range expectedCalls {
		found := false

		if response.Output != nil {
			for _, message := range response.Output {
				if message.ResponsesToolMessage != nil &&
					message.ResponsesToolMessage.Name != nil &&
					*message.ResponsesToolMessage.Name == expected.FunctionName {
					if message.ResponsesToolMessage.Arguments != nil {
						arguments := *message.ResponsesToolMessage.Arguments
						found = true
						validateSingleToolCall(arguments, expected, 0, 0, result)
						break
					}
				}
			}
		}

		if !found {
			result.Passed = false
			actualToolNames := extractResponsesToolCallNames(response)
			if len(actualToolNames) == 0 {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Expected tool call '%s' not found (no tool calls present)", expected.FunctionName))
			} else {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Expected tool call '%s' not found. Actual tool calls found: %v",
						expected.FunctionName, actualToolNames))
			}
		}
	}
}

// =============================================================================
// UTILITY FUNCTIONS
// =============================================================================

// truncateContentForError safely truncates content for error messages
func truncateContentForError(content string, maxLength int) string {
	content = strings.TrimSpace(content)
	if len(content) <= maxLength {
		return fmt.Sprintf("'%s'", content)
	}
	return fmt.Sprintf("'%s...' (truncated from %d chars)", content[:maxLength], len(content))
}

// getJSONType returns the JSON type of a value
func getJSONType(value interface{}) string {
	switch value.(type) {
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case bool:
		return "boolean"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

// validateSingleToolCall validates a specific tool call against expectations
func validateSingleToolCall(arguments interface{}, expected ToolCallExpectation, choiceIdx, callIdx int, result *ValidationResult) {
	// Parse arguments with safe type handling
	var args map[string]interface{}

	if expected.ValidateArgsJSON {
		// Handle nil arguments
		if arguments == nil {
			args = nil
		} else if argsMap, ok := arguments.(map[string]interface{}); ok {
			// Already a map, use directly
			args = argsMap
		} else if argsMapInterface, ok := arguments.(map[interface{}]interface{}); ok {
			// Convert map[interface{}]interface{} to map[string]interface{}
			args = make(map[string]interface{})
			for k, v := range argsMapInterface {
				if keyStr, ok := k.(string); ok {
					args[keyStr] = v
				}
			}
		} else if argsStr, ok := arguments.(string); ok {
			// String type - unmarshal as JSON
			if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
				result.Passed = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("Tool call %s (choice %d, call %d) has invalid JSON arguments: %s",
						expected.FunctionName, choiceIdx, callIdx, err.Error()))
				return
			}
		} else if argsBytes, ok := arguments.([]byte); ok {
			// []byte type - unmarshal as JSON
			if err := json.Unmarshal(argsBytes, &args); err != nil {
				result.Passed = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("Tool call %s (choice %d, call %d) has invalid JSON arguments: %s",
						expected.FunctionName, choiceIdx, callIdx, err.Error()))
				return
			}
		} else {
			// Unsupported type
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Tool call %s (choice %d, call %d) has unsupported argument type: %T",
					expected.FunctionName, choiceIdx, callIdx, arguments))
			return
		}
	}

	// Check required arguments
	for _, reqArg := range expected.RequiredArgs {
		if _, exists := args[reqArg]; !exists {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Tool call %s missing required argument '%s'", expected.FunctionName, reqArg))
		}
	}

	// Check forbidden arguments
	for _, forbiddenArg := range expected.ForbiddenArgs {
		if _, exists := args[forbiddenArg]; exists {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Tool call %s has forbidden argument '%s'", expected.FunctionName, forbiddenArg))
		}
	}

	// Check argument types
	for argName, expectedType := range expected.ArgumentTypes {
		if value, exists := args[argName]; exists {
			actualType := getJSONType(value)
			if actualType != expectedType {
				result.Passed = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("Tool call %s argument '%s' is %s, expected %s",
						expected.FunctionName, argName, actualType, expectedType))
			}
		}
	}

	// Check specific argument values
	for argName, expectedValue := range expected.ArgumentValues {
		if actualValue, exists := args[argName]; exists {
			if actualValue != expectedValue {
				result.Passed = false
				result.Errors = append(result.Errors,
					fmt.Sprintf("Tool call %s argument '%s' is %v, expected %v",
						expected.FunctionName, argName, actualValue, expectedValue))
			}
		}
	}
}

// logValidationResults logs the validation results
func logValidationResults(t *testing.T, result ValidationResult, scenarioName string) {
	if result.Passed {
		t.Logf("✅ Validation passed for %s", scenarioName)
	} else {
		// LogF, not ErrorF else later retries will still fail the test
		t.Logf("❌ Validation failed for %s with %d errors", scenarioName, len(result.Errors))
		for _, err := range result.Errors {
			// Ensure each error line has ❌ prefix for consistency
			errorMsg := err
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			t.Logf("   %s", errorMsg)
		}
	}

	if len(result.Warnings) > 0 {
		t.Logf("⚠️  %d warnings for %s", len(result.Warnings), scenarioName)
		for _, warning := range result.Warnings {
			t.Logf("   Warning: %s", warning)
		}
	}
}

// countLogicalChoicesInResponsesAPI counts logical choices in Responses API format
// Groups related messages (text + tool calls) as one logical choice to match Chat Completions API behavior
func countLogicalChoicesInResponsesAPI(messages []schemas.ResponsesMessage) int {
	if len(messages) == 0 {
		return 0
	}

	// For tool call scenarios, we typically have:
	// 1. Text message (ResponsesMessageTypeMessage)
	// 2. Tool call message(s) (ResponsesMessageTypeFunctionCall)
	// These should count as 1 logical choice

	hasTextMessage := false
	hasToolCalls := false
	hasSeparateMessages := false

	for _, msg := range messages {
		if msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeMessage:
				hasTextMessage = true
			case schemas.ResponsesMessageTypeFunctionCall:
				hasToolCalls = true
			case schemas.ResponsesMessageTypeReasoning, schemas.ResponsesMessageTypeRefusal:
				hasSeparateMessages = true
			}
		}
	}

	// If we have both text and tool calls, count as 1 logical choice
	// This matches the Chat Completions API behavior where both are in the same choice
	if hasTextMessage && hasToolCalls {
		return 1 + (func() int {
			if hasSeparateMessages {
				return 1 // Add 1 for reasoning/refusal messages
			}
			return 0
		})()
	}

	// If only tool calls (no text), still count as 1 logical choice
	if hasToolCalls && !hasTextMessage {
		return 1
	}

	// If only text message(s) or other types, count actual messages
	return len(messages)
}
