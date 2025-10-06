package scenarios

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
	MinContentLength     int     // Minimum content length
	MaxContentLength     int     // Maximum content length (0 = no limit)
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

// ValidateResponse performs comprehensive response validation
func ValidateResponse(t *testing.T, response *schemas.BifrostResponse, err *schemas.BifrostError, expectations ResponseExpectations, scenarioName string) ValidationResult {
	result := ValidationResult{
		Passed:           true,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
		MetricsCollected: make(map[string]interface{}),
	}

	// If there's an error when we expected success, that's a failure
	if err != nil {
		result.Passed = false

		// Use the error parser to format the error nicely
		parsed := ParseBifrostError(err)
		result.Errors = append(result.Errors, fmt.Sprintf("Got error when expecting success: %s", FormatErrorConcise(parsed)))

		// Log the full error details for debugging
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
	validateBasicStructure(t, response, expectations, &result)

	// Validate content
	validateContent(t, response, expectations, &result)

	// Validate tool calls
	validateToolCalls(t, response, expectations, &result)

	// Validate technical fields
	validateTechnicalFields(t, response, expectations, &result)

	// Validate provider-specific requirements (speech, transcription, etc.)
	validateProviderSpecific(t, response, expectations, &result)

	// Collect metrics
	collectResponseMetrics(response, &result)

	// Log results
	logValidationResults(t, result, scenarioName)

	return result
}

// =============================================================================
// VALIDATION HELPER FUNCTIONS
// =============================================================================

// validateBasicStructure checks the basic structure of the response
func validateBasicStructure(t *testing.T, response *schemas.BifrostResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check choice count
	if expectations.ExpectedChoiceCount > 0 {
		actualCount := 0
		if response.Choices != nil {
			actualCount = len(response.Choices)
		}
		if response.ResponsesResponse != nil {
			// For Responses API, count "logical choices" instead of raw message count
			// Group related messages (text + tool calls) as one logical choice
			actualCount = countLogicalChoicesInResponsesAPI(response.ResponsesResponse.Output)
		}
		if actualCount != expectations.ExpectedChoiceCount {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Expected %d choices, got %d", expectations.ExpectedChoiceCount, actualCount))
		}
	}

	// Check if we have choices at all
	choices := []schemas.BifrostChatResponseChoice{}
	if response.Choices != nil {
		choices = response.Choices
	}

	// Check finish reasons
	if expectations.ExpectedFinishReason != nil {
		for i, choice := range choices {
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

// validateContent checks the content of the response
func validateContent(t *testing.T, response *schemas.BifrostResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Skip content validation for responses that don't have text content (e.g., speech synthesis)
	if !expectations.ShouldHaveContent {
		return
	}

	content := GetResultContent(response)

	// Check if content exists when expected
	if expectations.ShouldHaveContent {
		if strings.TrimSpace(content) == "" {
			result.Passed = false
			result.Errors = append(result.Errors, "Expected content but got empty response")
			return
		}
	}

	// Check content length
	contentLen := len(strings.TrimSpace(content))
	if expectations.MinContentLength > 0 && contentLen < expectations.MinContentLength {
		result.Passed = false
		result.Errors = append(result.Errors,
			fmt.Sprintf("Content length %d is below minimum %d", contentLen, expectations.MinContentLength))
	}

	if expectations.MaxContentLength > 0 && contentLen > expectations.MaxContentLength {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Content length %d exceeds maximum %d", contentLen, expectations.MaxContentLength))
	}

	// Check required keywords (AND logic - ALL must be present)
	lowerContent := strings.ToLower(content)
	for _, keyword := range expectations.ShouldContainKeywords {
		if !strings.Contains(lowerContent, strings.ToLower(keyword)) {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Content should contain keyword '%s' but doesn't. Actual content: %s",
					keyword, truncateContentForError(content, 200)))
		}
	}

	// Check OR keywords (OR logic - AT LEAST ONE must be present)
	if len(expectations.ShouldContainAnyOf) > 0 {
		foundAny := false
		for _, keyword := range expectations.ShouldContainAnyOf {
			if strings.Contains(lowerContent, strings.ToLower(keyword)) {
				foundAny = true
				break
			}
		}
		if !foundAny {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Content should contain at least one of these keywords: %v, but doesn't. Actual content: %s",
					expectations.ShouldContainAnyOf, truncateContentForError(content, 200)))
		}
	}

	// Check forbidden words
	for _, word := range expectations.ShouldNotContainWords {
		if strings.Contains(lowerContent, strings.ToLower(word)) {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Content contains forbidden word '%s'. Actual content: %s",
					word, truncateContentForError(content, 200)))
		}
	}

	// Check content pattern
	if expectations.ContentPattern != nil {
		if !expectations.ContentPattern.MatchString(content) {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Content doesn't match expected pattern: %s. Actual content: %s",
					expectations.ContentPattern.String(), truncateContentForError(content, 200)))
		}
	}

	// Store content for metrics
	result.MetricsCollected["content_length"] = contentLen
	result.MetricsCollected["content_word_count"] = len(strings.Fields(content))
}

// truncateContentForError safely truncates content for error messages
func truncateContentForError(content string, maxLength int) string {
	content = strings.TrimSpace(content)
	if len(content) <= maxLength {
		return fmt.Sprintf("'%s'", content)
	}
	return fmt.Sprintf("'%s...' (truncated from %d chars)", content[:maxLength], len(content))
}

// extractToolCallNames extracts tool call function names from response for error messages
func extractToolCallNames(response *schemas.BifrostResponse) []string {
	var toolNames []string

	if response.ResponsesResponse != nil {
		for _, output := range response.ResponsesResponse.Output {
			if output.ResponsesToolMessage != nil && output.Name != nil {
				toolNames = append(toolNames, *output.Name)
			}
		}
	} else if response.Choices != nil {
		choices := []schemas.BifrostChatResponseChoice{}
		if response.Choices != nil {
			choices = response.Choices
		}

		for _, choice := range choices {
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

// validateToolCalls checks tool calling aspects of the response
func validateToolCalls(t *testing.T, response *schemas.BifrostResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Count total tool calls from both Chat Completions API and Responses API
	totalToolCalls := 0

	// Count tool calls from Chat Completions API
	if response.Choices != nil {
		for _, choice := range response.Choices {
			if choice.BifrostTextCompletionResponseChoice != nil {
				continue
			}

			if choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ToolCalls != nil {
				totalToolCalls += len(choice.Message.ChatAssistantMessage.ToolCalls)
			}
		}
	}

	// Count tool calls from Responses API
	if response.ResponsesResponse != nil && response.ResponsesResponse.Output != nil {
		for _, output := range response.ResponsesResponse.Output {
			// Check if this is a function_call type message
			if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeFunctionCall {
				totalToolCalls++
			}
		}
	}

	// Check if we should have no function calls
	if expectations.ShouldNotHaveFunctionCalls && totalToolCalls > 0 {
		result.Passed = false
		actualToolNames := extractToolCallNames(response)
		result.Errors = append(result.Errors,
			fmt.Sprintf("Expected no function calls but found %d: %v", totalToolCalls, actualToolNames))
	}

	// Validate specific tool calls
	if len(expectations.ExpectedToolCalls) > 0 {
		validateSpecificToolCalls(response, expectations.ExpectedToolCalls, result)
	}

	result.MetricsCollected["tool_call_count"] = totalToolCalls
}

// validateSpecificToolCalls validates individual tool call expectations
func validateSpecificToolCalls(response *schemas.BifrostResponse, expectedCalls []ToolCallExpectation, result *ValidationResult) {
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
		} else if response.ResponsesResponse != nil {
			for _, message := range response.ResponsesResponse.Output {
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
			actualToolNames := extractToolCallNames(response)
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

// validateTechnicalFields checks technical aspects of the response
func validateTechnicalFields(t *testing.T, response *schemas.BifrostResponse, expectations ResponseExpectations, result *ValidationResult) {
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
		if (response.ResponsesResponse == nil && response.Created == 0) || (response.ResponsesResponse != nil && response.ResponsesResponse.CreatedAt == 0) {
			result.Warnings = append(result.Warnings, "Expected created timestamp but not present")
		}
	}

	// Check model field
	if expectations.ShouldHaveModel {
		if strings.TrimSpace(response.Model) == "" {
			result.Warnings = append(result.Warnings, "Expected model field but not present or empty")
		}
	}
}

// =============================================================================
// UTILITY FUNCTIONS
// =============================================================================

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

// collectResponseMetrics collects metrics from the response for analysis
func collectResponseMetrics(response *schemas.BifrostResponse, result *ValidationResult) {
	result.MetricsCollected["choice_count"] = len(response.Choices)

	if response.ResponsesResponse != nil {
		result.MetricsCollected["choice_count"] = len(response.ResponsesResponse.Output)
	}

	result.MetricsCollected["has_usage"] = response.Usage != nil
	result.MetricsCollected["has_model"] = response.Model != ""
	result.MetricsCollected["has_timestamp"] = response.Created > 0

	if response.Usage != nil {
		result.MetricsCollected["total_tokens"] = response.Usage.TotalTokens
		if response.Usage.ResponsesExtendedResponseUsage != nil {
			result.MetricsCollected["input_tokens"] = response.Usage.ResponsesExtendedResponseUsage.InputTokens
			result.MetricsCollected["output_tokens"] = response.Usage.ResponsesExtendedResponseUsage.OutputTokens
		}
		result.MetricsCollected["prompt_tokens"] = response.Usage.PromptTokens
		result.MetricsCollected["completion_tokens"] = response.Usage.CompletionTokens
	}
}

// logValidationResults logs the validation results
func logValidationResults(t *testing.T, result ValidationResult, scenarioName string) {
	if result.Passed {
		t.Logf("✅ Validation passed for %s", scenarioName)
	} else {
		t.Logf("❌ Validation failed for %s with %d errors", scenarioName, len(result.Errors))
		for _, err := range result.Errors {
			t.Logf("   Error: %s", err)
		}
	}

	if len(result.Warnings) > 0 {
		t.Logf("⚠️  %d warnings for %s", len(result.Warnings), scenarioName)
		for _, warning := range result.Warnings {
			t.Logf("   Warning: %s", warning)
		}
	}
}

// validateProviderSpecific handles speech, transcription, and other provider-specific validations
func validateProviderSpecific(t *testing.T, response *schemas.BifrostResponse, expectations ResponseExpectations, result *ValidationResult) {
	if len(expectations.ProviderSpecific) == 0 {
		return
	}

	// Check response type to determine validation path
	responseType, hasResponseType := expectations.ProviderSpecific["response_type"].(string)
	if !hasResponseType {
		return
	}

	switch responseType {
	case "speech_synthesis":
		validateSpeechSynthesis(t, response, expectations, result)
	case "transcription":
		validateTranscription(t, response, expectations, result)
	}
}

// validateSpeechSynthesis validates speech synthesis responses
func validateSpeechSynthesis(t *testing.T, response *schemas.BifrostResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check if response has speech data
	if response.Speech == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Speech synthesis response missing Speech field")
		return
	}

	// Check if audio data exists
	shouldHaveAudio, _ := expectations.ProviderSpecific["should_have_audio"].(bool)
	if shouldHaveAudio && response.Speech.Audio == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Speech synthesis response missing audio data")
		return
	}

	// Check minimum audio bytes
	if minBytes, ok := expectations.ProviderSpecific["min_audio_bytes"].(int); ok {
		if response.Speech.Audio != nil {
			actualSize := len(response.Speech.Audio)
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

	result.MetricsCollected["speech_validation"] = "completed"
}

// validateTranscription validates transcription responses
func validateTranscription(t *testing.T, response *schemas.BifrostResponse, expectations ResponseExpectations, result *ValidationResult) {
	// Check if response has transcription data
	if response.Transcribe == nil {
		result.Passed = false
		result.Errors = append(result.Errors, "Transcription response missing Transcribe field")
		return
	}

	// Check if transcribed text exists
	shouldHaveTranscription, _ := expectations.ProviderSpecific["should_have_transcription"].(bool)
	if shouldHaveTranscription && response.Transcribe.Text == "" {
		result.Passed = false
		result.Errors = append(result.Errors, "Transcription response missing transcribed text")
		return
	}

	// Check minimum transcription length
	if minLength, ok := expectations.ProviderSpecific["min_transcription_length"].(int); ok {
		actualLength := len(response.Transcribe.Text)
		if actualLength < minLength {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Transcribed text too short: got %d characters, expected at least %d", actualLength, minLength))
		} else {
			result.MetricsCollected["transcription_length"] = actualLength
		}
	}

	// Check for common transcription failure indicators
	transcribedText := strings.ToLower(response.Transcribe.Text)
	for _, errorPhrase := range expectations.ShouldNotContainWords {
		if strings.Contains(transcribedText, errorPhrase) {
			result.Passed = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Transcribed text contains error indicator: '%s'", errorPhrase))
		}
	}

	// Validate additional transcription fields if available
	if response.Transcribe.Language != nil {
		result.MetricsCollected["detected_language"] = *response.Transcribe.Language
	}
	if response.Transcribe.Duration != nil {
		result.MetricsCollected["audio_duration"] = *response.Transcribe.Duration
	}

	result.MetricsCollected["transcription_validation"] = "completed"
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
