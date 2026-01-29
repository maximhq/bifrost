package llmtests

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// BASIC RESPONSE CONDITIONS
// =============================================================================

// EmptyResponseCondition checks for empty or missing response content
type EmptyResponseCondition struct{}

func (c *EmptyResponseCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	// If there's an error, let the HTTP retry logic handle it
	if err != nil {
		return false, ""
	}

	// No response at all
	if response == nil {
		return true, "response is nil"
	}

	// Check if chat completions response exists
	if response.TextCompletionResponse == nil && response.ChatResponse == nil && response.ResponsesResponse == nil {
		return true, "response has no chat completions or responses data"
	}

	// Check if all choices are empty (no content AND no tool calls)
	hasContent := false

	// Check for textual content using the already robust GetResultContent function
	content := GetResultContent(response)
	if strings.TrimSpace(content) != "" {
		hasContent = true
	}

	// If no textual content, check for tool calls using the universal ExtractToolCalls function
	if !hasContent {
		toolCalls := ExtractToolCalls(response)
		if len(toolCalls) > 0 {
			// Validate that at least one tool call has a function name
			for _, toolCall := range toolCalls {
				if strings.TrimSpace(toolCall.Name) != "" {
					hasContent = true
					break
				}
			}
		}

		if len(toolCalls) == 0 {
			return true, "no tool calls found in response"
		}
	}

	if !hasContent {
		return true, "all choices have empty content and no tool calls"
	}

	return false, ""
}

func (c *EmptyResponseCondition) GetConditionName() string {
	return "EmptyResponse"
}

// =============================================================================
// TOOL CALLING CONDITIONS
// =============================================================================

// MissingToolCallCondition checks if expected tool call is missing
type MissingToolCallCondition struct {
	ExpectedToolName string // Name of the tool that should have been called
}

func (c *MissingToolCallCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	expectedTool := c.ExpectedToolName
	if expectedTool == "" {
		// Try to get from context
		if tool, ok := context.ExpectedBehavior["expected_tool_name"].(string); ok {
			expectedTool = tool
		} else {
			return false, ""
		}
	}

	// Extract tool calls from both API formats
	toolCalls := ExtractToolCalls(response)

	// Check if any tool call has the expected name
	for _, toolCall := range toolCalls {
		if toolCall.Name == expectedTool {
			return false, "" // Found the expected tool call
		}
	}

	return true, fmt.Sprintf("expected tool call '%s' not found in response", expectedTool)
}

func (c *MissingToolCallCondition) GetConditionName() string {
	return "MissingToolCall"
}

// MalformedToolArgsCondition checks for malformed tool call arguments
type MalformedToolArgsCondition struct{}

func (c *MalformedToolArgsCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	// Extract tool calls from both API formats
	toolCalls := ExtractToolCalls(response)

	// Check all tool calls for malformed arguments
	for i, toolCall := range toolCalls {
		if toolCall.Arguments == "" {
			continue // Skip empty arguments for now
		}

		// Try to parse arguments as JSON
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(toolCall.Arguments), &args); err != nil {
			return true, fmt.Sprintf("tool call %d has malformed JSON arguments: %s", i, err.Error())
		}

		// Check for empty arguments only when arguments are explicitly required
		// Some tools (like get_current_time) legitimately take no arguments
		requiresArgs := false
		if context.ExpectedBehavior != nil {
			// Check if this test expects arguments (default: false, allowing tools with no args)
			if expectArgs, ok := context.ExpectedBehavior["requires_arguments"].(bool); ok {
				requiresArgs = expectArgs
			}
		}

		if requiresArgs && len(args) == 0 && toolCall.Name != "" {
			return true, fmt.Sprintf("tool call %d (%s) has empty arguments but arguments are required", i, toolCall.Name)
		}
	}

	return false, ""
}

func (c *MalformedToolArgsCondition) GetConditionName() string {
	return "MalformedToolArgs"
}

// WrongToolCalledCondition checks if the wrong tool was called
type WrongToolCalledCondition struct {
	ExpectedToolName string
	ForbiddenTools   []string // Tools that should not be called
}

func (c *WrongToolCalledCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	expectedTool := c.ExpectedToolName
	if expectedTool == "" {
		if tool, ok := context.ExpectedBehavior["expected_tool_name"].(string); ok {
			expectedTool = tool
		}
	}

	// Extract tool calls from both API formats
	toolCalls := ExtractToolCalls(response)

	// Check all tool calls
	for i, toolCall := range toolCalls {
		toolName := toolCall.Name
		if toolName == "" {
			continue
		}

		// Check if forbidden tool was called
		for _, forbidden := range c.ForbiddenTools {
			if toolName == forbidden {
				return true, fmt.Sprintf("tool call %d called forbidden tool '%s'", i, toolName)
			}
		}

		// If we have an expected tool and this isn't it
		if expectedTool != "" && toolName != expectedTool {
			return true, fmt.Sprintf("tool call %d called '%s' instead of expected '%s'", i, toolName, expectedTool)
		}
	}

	return false, ""
}

func (c *WrongToolCalledCondition) GetConditionName() string {
	return "WrongToolCalled"
}

// =============================================================================
// MULTIPLE TOOL CALL CONDITIONS
// =============================================================================

// PartialToolCallCondition checks if we got fewer tool calls than expected
type PartialToolCallCondition struct {
	ExpectedCount int // Expected number of tool calls
}

func (c *PartialToolCallCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	expectedCount := c.ExpectedCount
	if expectedCount == 0 {
		if count, ok := context.ExpectedBehavior["expected_tool_count"].(int); ok {
			expectedCount = count
		} else {
			return false, ""
		}
	}

	// Extract tool calls from both API formats and count them
	toolCalls := ExtractToolCalls(response)
	actualCount := len(toolCalls)

	if actualCount < expectedCount {
		return true, fmt.Sprintf("got %d tool calls, expected %d", actualCount, expectedCount)
	}

	return false, ""
}

func (c *PartialToolCallCondition) GetConditionName() string {
	return "PartialToolCall"
}

// WrongToolSequenceCondition checks if tools were called in wrong order
type WrongToolSequenceCondition struct {
	ExpectedTools []string // Expected sequence of tool names
}

func (c *WrongToolSequenceCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	expectedTools := c.ExpectedTools
	if len(expectedTools) == 0 {
		if tools, ok := context.ExpectedBehavior["expected_tool_sequence"].([]string); ok {
			expectedTools = tools
		} else {
			return false, ""
		}
	}

	// Extract tool calls from both API formats
	toolCalls := ExtractToolCalls(response)

	// If we don't have enough tool calls
	if len(toolCalls) < len(expectedTools) {
		return true, fmt.Sprintf("got %d tool calls, expected at least %d", len(toolCalls), len(expectedTools))
	}

	// Check sequence
	for j, expectedTool := range expectedTools {
		if j >= len(toolCalls) {
			break
		}

		actualTool := toolCalls[j].Name
		if actualTool != expectedTool {
			if actualTool == "" {
				actualTool = "nil"
			}
			return true, fmt.Sprintf("position %d: got '%s', expected '%s'", j, actualTool, expectedTool)
		}
	}

	return false, ""
}

func (c *WrongToolSequenceCondition) GetConditionName() string {
	return "WrongToolSequence"
}

// =============================================================================
// IMAGE PROCESSING CONDITIONS
// =============================================================================

// ImageNotProcessedCondition checks if image content was actually processed
type ImageNotProcessedCondition struct{}

func (c *ImageNotProcessedCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	// Get response content
	content := strings.ToLower(GetResultContent(response))

	// Check for generic responses that don't indicate image processing
	genericPhrases := []string{
		"i can't see",
		"i cannot see",
		"unable to see",
		"can't view",
		"cannot view",
		"no image",
		"not able to see",
		"i don't see",
		"i cannot process",
	}

	for _, phrase := range genericPhrases {
		if strings.Contains(content, phrase) {
			return true, fmt.Sprintf("response suggests image was not processed: contains '%s'", phrase)
		}
	}

	// If content is suspiciously short for image analysis
	if len(strings.TrimSpace(content)) < 20 {
		return true, "response too short for meaningful image analysis"
	}

	return false, ""
}

func (c *ImageNotProcessedCondition) GetConditionName() string {
	return "ImageNotProcessed"
}

// =============================================================================
// FILE/DOCUMENT PROCESSING CONDITIONS
// =============================================================================

// FileNotProcessedCondition checks if file/document was not properly processed
type FileNotProcessedCondition struct{}

func (c *FileNotProcessedCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	// Get response content
	content := strings.ToLower(GetResultContent(response))

	// Check for generic responses that don't indicate file/document processing
	fileProcessingFailurePhrases := []string{
		"i can't read",
		"i cannot read",
		"unable to read",
		"can't access",
		"cannot access",
		"no file",
		"no document",
		"not able to read",
		"i don't see",
		"i cannot process",
		"unable to process",
		"can't open",
		"cannot open",
		"invalid file",
		"corrupted",
		"unsupported format",
		"failed to load",
		"no pdf",
		"cannot view",
	}

	for _, phrase := range fileProcessingFailurePhrases {
		if strings.Contains(content, phrase) {
			return true, fmt.Sprintf("response suggests file was not processed: contains '%s'", phrase)
		}
	}

	// If content is suspiciously short for document analysis
	if len(strings.TrimSpace(content)) < 15 {
		return true, "response too short for meaningful document analysis"
	}

	return false, ""
}

func (c *FileNotProcessedCondition) GetConditionName() string {
	return "FileNotProcessed"
}

// GenericResponseCondition checks for generic/template responses
type GenericResponseCondition struct{}

func (c *GenericResponseCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.TextCompletionResponse == nil && response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	content := strings.ToLower(GetResultContent(response))

	// Generic phrases that suggest the model didn't engage with the specific request
	genericPhrases := []string{
		"as an ai",
		"as a language model",
		"i'm an ai",
		"i am an ai",
		"i'm a language model",
		"i am a language model",
		"i can help you with",
		"how can i assist you",
		"what would you like to know",
		"is there anything else",
	}

	// Check if response starts with generic phrases (more concerning)
	for _, phrase := range genericPhrases {
		if strings.HasPrefix(content, phrase) {
			return true, fmt.Sprintf("response starts with generic phrase: '%s'", phrase)
		}
	}

	// Check for overly generic responses (short and generic)
	if len(strings.TrimSpace(content)) < 30 {
		for _, phrase := range genericPhrases {
			if strings.Contains(content, phrase) {
				return true, fmt.Sprintf("short response contains generic phrase: '%s'", phrase)
			}
		}
	}

	return false, ""
}

func (c *GenericResponseCondition) GetConditionName() string {
	return "GenericResponse"
}

// =============================================================================
// CONTENT VALIDATION CONDITIONS
// =============================================================================

// ContentValidationCondition checks if response fails basic content validation
// This is crucial for vision tests where the AI might give different descriptions
type ContentValidationCondition struct{}

func (c *ContentValidationCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.TextCompletionResponse == nil && response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	content := strings.ToLower(GetResultContent(response))

	// Skip if response is too short or generic (other conditions will handle these)
	if len(content) < 10 {
		return false, ""
	}

	// Only check content validation for vision-related scenarios
	scenarioName := strings.ToLower(context.ScenarioName)
	if !strings.Contains(scenarioName, "image") && !strings.Contains(scenarioName, "vision") {
		return false, ""
	}

	// Check if this looks like a valid vision response but might be missing keywords
	// Look for vision-related indicators that suggest the AI processed the image
	visionIndicators := []string{
		"see", "shows", "depicts", "contains", "features", "displays",
		"appears", "looks", "visible", "image", "picture", "photo",
		"color", "shape", "object", "animal", "person", "building",
		"in the", "there is", "there are", "this is", "i can see",
	}

	hasVisionContent := false
	for _, indicator := range visionIndicators {
		if strings.Contains(content, indicator) {
			hasVisionContent = true
			break
		}
	}

	// If it looks like a valid vision response, check if we should retry based on missing expected keywords
	if hasVisionContent {
		// Check if this test has expected keywords from the TestRetryContext
		if testMetadata, exists := context.TestMetadata["expected_keywords"]; exists {
			if expectedKeywords, ok := testMetadata.([]string); ok && len(expectedKeywords) > 0 {
				// Check if ANY of the expected keywords are present
				foundExpectedKeyword := false
				for _, keyword := range expectedKeywords {
					if strings.Contains(content, strings.ToLower(keyword)) {
						foundExpectedKeyword = true
						break
					}
				}

				// If valid vision response but missing ALL expected keywords, retry
				// Allow longer responses for complex vision tasks (comparisons, detailed descriptions)
				if !foundExpectedKeyword && len(content) > 20 && len(content) < 2000 {
					return true, fmt.Sprintf("valid vision response but missing expected keywords %v, might include them on retry", expectedKeywords)
				}
			}
		}

		// Fallback: Check expected behavior fields for dynamic validation
		if expectedAnimal, ok := context.ExpectedBehavior["should_identify_animal"].(string); ok && expectedAnimal != "" {
			// Parse expected animal from behavior context (e.g., "lion or animal")
			expectedTerms := strings.Split(strings.ToLower(expectedAnimal), " or ")
			foundExpected := false
			for _, term := range expectedTerms {
				term = strings.TrimSpace(term)
				if term != "" && strings.Contains(content, term) {
					foundExpected = true
					break
				}
			}
			if !foundExpected && len(content) > 20 && len(content) < 1500 {
				return true, fmt.Sprintf("valid vision response but missing expected animal terms '%s', might get more specific on retry", expectedAnimal)
			}
		}

		if expectedObject, ok := context.ExpectedBehavior["should_identify_object"].(string); ok && expectedObject != "" {
			// Parse expected object from behavior context (e.g., "ant or insect")
			expectedTerms := strings.Split(strings.ToLower(expectedObject), " or ")
			foundExpected := false
			for _, term := range expectedTerms {
				term = strings.TrimSpace(term)
				if term != "" && strings.Contains(content, term) {
					foundExpected = true
					break
				}
			}
			if !foundExpected && len(content) > 15 && len(content) < 1500 {
				return true, fmt.Sprintf("valid vision response but missing expected object terms '%s', might get more specific on retry", expectedObject)
			}
		}
	}

	return false, ""
}

func (c *ContentValidationCondition) GetConditionName() string {
	return "ContentValidation"
}

// =============================================================================
// STREAMING CONDITIONS
// =============================================================================

// StreamErrorCondition checks for streaming-specific errors that should trigger retries
type StreamErrorCondition struct{}

func (c *StreamErrorCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	// Only retry on actual stream errors, not when stream is successful but response is nil
	if err == nil {
		return false, ""
	}

	// Check for specific streaming errors that indicate retry-worthy conditions
	// Check both the Message field and nested Error field
	var errorMsg string
	if strings.TrimSpace(err.Error.Message) != "" {
		errorMsg = strings.ToLower(err.Error.Message)
	} else if err.Error.Error != nil {
		errorMsg = strings.ToLower(err.Error.Error.Error())
	} else {
		return false, ""
	}

	// Retry on connection/timeout issues during streaming
	if strings.Contains(errorMsg, "connection reset") ||
		strings.Contains(errorMsg, "connection refused") ||
		strings.Contains(errorMsg, "timeout") ||
		strings.Contains(errorMsg, "stream closed") ||
		strings.Contains(errorMsg, "stream interrupted") {
		return true, fmt.Sprintf("stream connection error: %s", errorMsg)
	}

	// Retry on temporary streaming API errors
	if strings.Contains(errorMsg, "rate limit") ||
		strings.Contains(errorMsg, "quota exceeded") ||
		strings.Contains(errorMsg, "service unavailable") ||
		strings.Contains(errorMsg, "server overloaded") {
		return true, fmt.Sprintf("temporary API error: %s", errorMsg)
	}

	// Don't retry on authentication, invalid request, or other permanent errors
	return false, ""
}

func (c *StreamErrorCondition) GetConditionName() string {
	return "StreamError"
}

// IncompleteStreamCondition checks for incomplete streaming responses
type IncompleteStreamCondition struct{}

func (c *IncompleteStreamCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check both Chat Completions and Responses API formats
	if response.TextCompletionResponse == nil && response.ChatResponse == nil && response.ResponsesResponse == nil {
		return false, ""
	}

	// For Chat Completions API, check finish reasons in choices
	if response.ChatResponse != nil {
		for i, choice := range response.ChatResponse.Choices {
			if choice.FinishReason == nil {
				return true, fmt.Sprintf("choice %d has no finish reason (stream may be incomplete)", i)
			}

			// Check for incomplete finish reasons
			finishReason := string(*choice.FinishReason)
			if finishReason == "length" {
				// This might be okay depending on context, but could indicate truncation
				singleChoiceResponse := &schemas.BifrostResponse{
					ChatResponse: &schemas.BifrostChatResponse{
						Choices: []schemas.BifrostResponseChoice{choice},
					},
				}
				choiceContent := GetResultContent(singleChoiceResponse)
				if len(choiceContent) < 10 {
					return true, fmt.Sprintf("choice %d finished due to length but content is very short", i)
				}
			}
		}
	}

	if response.TextCompletionResponse != nil {
		for i, choice := range response.TextCompletionResponse.Choices {
			if choice.FinishReason == nil {
				return true, fmt.Sprintf("choice %d has no finish reason (stream may be incomplete)", i)
			}

			finishReason := string(*choice.FinishReason)
			if finishReason == "length" {
				// This might be okay depending on context, but could indicate truncation
				singleChoiceResponse := &schemas.BifrostResponse{
					TextCompletionResponse: &schemas.BifrostTextCompletionResponse{
						Choices: []schemas.BifrostResponseChoice{choice},
					},
				}
				choiceContent := GetResultContent(singleChoiceResponse)
				if len(choiceContent) < 10 {
					return true, fmt.Sprintf("choice %d finished due to length but content is very short", i)
				}
			}
		}
	}

	// For Responses API, check completion status in output messages
	if response.ResponsesResponse != nil {
		for i, output := range response.ResponsesResponse.Output {
			if output.Status == nil {
				return true, fmt.Sprintf("output %d has no status (stream may be incomplete)", i)
			}

			status := *output.Status
			if status == "incomplete" || status == "in_progress" {
				return true, fmt.Sprintf("output %d has incomplete status: %s", i, status)
			}
		}
	}

	return false, ""
}

func (c *IncompleteStreamCondition) GetConditionName() string {
	return "IncompleteStream"
}

// =============================================================================
// SPEECH SYNTHESIS CONDITIONS
// =============================================================================

// EmptySpeechCondition checks for missing or invalid audio data in speech synthesis responses
type EmptySpeechCondition struct{}

func (c *EmptySpeechCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	// If there's an error, let other conditions handle it
	if err != nil {
		return false, ""
	}

	// No response at all
	if response == nil {
		return true, "response is nil"
	}

	// Check if speech response exists
	if response.SpeechResponse == nil {
		return true, "response has no speech data"
	}

	// Check if audio data exists and is not empty
	if response.SpeechResponse.Audio == nil {
		return true, "response has no audio data"
	}

	// Check for unreasonably small audio files (likely errors)
	if len(response.SpeechResponse.Audio) < 100 {
		return true, fmt.Sprintf("audio data too small (%d bytes), likely an error", len(response.SpeechResponse.Audio))
	}

	return false, ""
}

func (c *EmptySpeechCondition) GetConditionName() string {
	return "EmptySpeech"
}

// =============================================================================
// TRANSCRIPTION CONDITIONS
// =============================================================================

// EmptyTranscriptionCondition checks for missing or invalid transcription text
type EmptyTranscriptionCondition struct{}

func (c *EmptyTranscriptionCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	// If there's an error, let other conditions handle it
	if err != nil {
		return false, ""
	}

	// No response at all
	if response == nil {
		return true, "response is nil"
	}

	// Check if transcription response exists
	if response.TranscriptionResponse == nil {
		return true, "response has no transcription data"
	}

	// Check if text exists and is not empty
	if response.TranscriptionResponse.Text == "" || strings.TrimSpace(response.TranscriptionResponse.Text) == "" {
		return true, "response has no transcription text"
	}

	// Check for unreasonably short transcriptions (likely errors)
	text := strings.TrimSpace(response.TranscriptionResponse.Text)
	if len(text) < 3 {
		return true, fmt.Sprintf("transcription text too short (%d chars): '%s'", len(text), text)
	}

	return false, ""
}

func (c *EmptyTranscriptionCondition) GetConditionName() string {
	return "EmptyTranscription"
}

// =============================================================================
// EMBEDDING CONDITIONS
// =============================================================================

// EmptyEmbeddingCondition checks for missing or empty embeddings
type EmptyEmbeddingCondition struct{}

func (c *EmptyEmbeddingCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check if we have embedding data
	if response.EmbeddingResponse == nil || len(response.EmbeddingResponse.Data) == 0 {
		return true, "response has no embedding data"
	}

	// Check each embedding
	for i, data := range response.EmbeddingResponse.Data {
		vec, extractErr := getEmbeddingVector(data)
		if extractErr != nil {
			return true, fmt.Sprintf("embedding %d: failed to extract vector: %s", i, extractErr.Error())
		}

		if len(vec) == 0 {
			return true, fmt.Sprintf("embedding %d: vector is empty", i)
		}

		// Check for all-zero vectors (sometimes indicates an error)
		allZero := true
		for _, val := range vec {
			if val != 0.0 {
				allZero = false
				break
			}
		}

		if allZero {
			return true, fmt.Sprintf("embedding %d: vector is all zeros", i)
		}
	}

	return false, ""
}

func (c *EmptyEmbeddingCondition) GetConditionName() string {
	return "EmptyEmbedding"
}

// InvalidEmbeddingDimensionCondition checks for inconsistent embedding dimensions
type InvalidEmbeddingDimensionCondition struct {
	ExpectedDimension int // Expected vector dimension (0 means any)
}

func (c *InvalidEmbeddingDimensionCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil || response.EmbeddingResponse == nil || len(response.EmbeddingResponse.Data) == 0 {
		return false, ""
	}

	expectedDim := c.ExpectedDimension
	if expectedDim == 0 {
		if dim, ok := context.ExpectedBehavior["expected_dimension"].(int); ok {
			expectedDim = dim
		}
	}

	var firstDimension int

	// Check each embedding
	for i, data := range response.EmbeddingResponse.Data {
		vec, extractErr := getEmbeddingVector(data)
		if extractErr != nil {
			return false, "" // Let EmptyEmbeddingCondition handle this
		}

		dimension := len(vec)

		// Set expected dimension from first embedding if not specified
		if i == 0 {
			firstDimension = dimension
			if expectedDim > 0 && dimension != expectedDim {
				return true, fmt.Sprintf("embedding %d: got dimension %d, expected %d", i, dimension, expectedDim)
			}
		} else {
			// Check consistency with first embedding
			if dimension != firstDimension {
				return true, fmt.Sprintf("embedding %d: dimension %d differs from first embedding dimension %d", i, dimension, firstDimension)
			}
		}

		// Check for unreasonably small dimensions (likely an error)
		if dimension < 50 {
			return true, fmt.Sprintf("embedding %d: dimension %d seems too small", i, dimension)
		}
	}

	return false, ""
}

func (c *InvalidEmbeddingDimensionCondition) GetConditionName() string {
	return "InvalidEmbeddingDimension"
}

// =============================================================================
// IMAGE CONDITIONS
// =============================================================================

// EmptyImageGenerationCondition checks for missing or invalid image data
type EmptyImageGenerationCondition struct{}

func (c *EmptyImageGenerationCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	// If there's an error, let other conditions handle it
	if err != nil {
		return false, ""
	}

	// No response at all
	if response == nil {
		return true, "response is nil"
	}

	// Check if both response types are nil
	if response.ImageGenerationResponse == nil && response.ImageGenerationStreamResponse == nil {
		return true, "response has no image data"
	}

	// Check non-streaming response
	if response.ImageGenerationResponse != nil {
		if len(response.ImageGenerationResponse.Data) == 0 {
			return true, "response has no image data"
		}

		// Check each image has either B64JSON or URL
		for i, img := range response.ImageGenerationResponse.Data {
			if img.B64JSON == "" && img.URL == "" {
				return true, fmt.Sprintf("image %d has no B64JSON or URL", i)
			}
		}
	}

	// Check streaming response
	if response.ImageGenerationStreamResponse != nil {
		if response.ImageGenerationStreamResponse.B64JSON == "" && response.ImageGenerationStreamResponse.URL == "" {
			return true, "stream response has no B64JSON or URL"
		}
	}

	return false, ""
}

func (c *EmptyImageGenerationCondition) GetConditionName() string {
	return "EmptyImageGeneration"
}

// =============================================================================
// COUNT TOKENS CONDITIONS
// =============================================================================

// EmptyCountTokensCondition checks for missing or invalid token counts
type EmptyCountTokensCondition struct{}

func (c *EmptyCountTokensCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	// If there's an error, let other conditions handle it
	if err != nil {
		return false, ""
	}

	// No response at all
	if response == nil {
		return true, "response is nil"
	}

	// Check if count tokens response exists
	if response.CountTokensResponse == nil {
		return true, "count tokens response is nil"
	}

	countTokensResp := response.CountTokensResponse

	// Check if token counts are valid
	if countTokensResp.InputTokens <= 0 {
		return true, "input_tokens is zero or negative"
	}

	// Check if total tokens is at least as large as input tokens
	if countTokensResp.TotalTokens != nil {
		if *countTokensResp.TotalTokens < countTokensResp.InputTokens {
			return true, fmt.Sprintf("total_tokens (%d) is less than input_tokens (%d)", *countTokensResp.TotalTokens, countTokensResp.InputTokens)
		}
	}

	return false, ""
}

func (c *EmptyCountTokensCondition) GetConditionName() string {
	return "EmptyCountTokens"
}

// InvalidCountTokensCondition checks for invalid token count data
type InvalidCountTokensCondition struct{}

func (c *InvalidCountTokensCondition) ShouldRetry(response *schemas.BifrostResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	// Check if count tokens response exists
	if response.CountTokensResponse == nil {
		return false, ""
	}

	countTokensResp := response.CountTokensResponse

	// Check if model is set
	if strings.TrimSpace(countTokensResp.Model) == "" {
		return true, "model field is empty"
	}

	// Check if request type is set correctly
	if countTokensResp.ExtraFields.RequestType != schemas.CountTokensRequest {
		return true, fmt.Sprintf("invalid request type: got %s, expected %s", countTokensResp.ExtraFields.RequestType, schemas.CountTokensRequest)
	}

	return false, ""
}

func (c *InvalidCountTokensCondition) GetConditionName() string {
	return "InvalidCountTokens"
}

// =============================================================================
// RESPONSES API CONDITIONS
// These implement ResponsesRetryCondition for use with WithResponsesTestRetry
// =============================================================================

// ResponsesEmptyCondition checks for empty Responses API responses
type ResponsesEmptyCondition struct{}

func (c *ResponsesEmptyCondition) ShouldRetry(response *schemas.BifrostResponsesResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil {
		return false, ""
	}
	if response == nil {
		return true, "response is nil"
	}
	content := GetResponsesContent(response)
	if strings.TrimSpace(content) == "" {
		return true, "response has empty content"
	}
	return false, ""
}

func (c *ResponsesEmptyCondition) GetConditionName() string {
	return "ResponsesEmpty"
}

// ResponsesFileNotProcessedCondition checks if file/document was not properly processed in Responses API
type ResponsesFileNotProcessedCondition struct{}

func (c *ResponsesFileNotProcessedCondition) ShouldRetry(response *schemas.BifrostResponsesResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	content := strings.ToLower(GetResponsesContent(response))

	// Check for generic responses that don't indicate file/document processing
	fileProcessingFailurePhrases := []string{
		"i can't read",
		"i cannot read",
		"unable to read",
		"can't access",
		"cannot access",
		"no file",
		"no document",
		"not able to read",
		"i don't see",
		"i cannot process",
		"unable to process",
		"can't open",
		"cannot open",
		"invalid file",
		"corrupted",
		"unsupported format",
		"failed to load",
		"no pdf",
		"cannot view",
	}

	for _, phrase := range fileProcessingFailurePhrases {
		if strings.Contains(content, phrase) {
			return true, fmt.Sprintf("response suggests file was not processed: contains '%s'", phrase)
		}
	}

	// If content is suspiciously short for document analysis
	if len(strings.TrimSpace(content)) < 15 {
		return true, "response too short for meaningful document analysis"
	}

	return false, ""
}

func (c *ResponsesFileNotProcessedCondition) GetConditionName() string {
	return "ResponsesFileNotProcessed"
}

// ResponsesGenericResponseCondition checks for generic/template responses in Responses API
type ResponsesGenericResponseCondition struct{}

func (c *ResponsesGenericResponseCondition) ShouldRetry(response *schemas.BifrostResponsesResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	content := strings.ToLower(GetResponsesContent(response))

	// Generic phrases that suggest the model didn't engage with the specific request
	genericPhrases := []string{
		"as an ai",
		"as a language model",
		"i'm an ai",
		"i am an ai",
		"i'm a language model",
		"i am a language model",
		"i can help you with",
		"how can i assist you",
		"what would you like to know",
		"is there anything else",
	}

	// Check if response starts with generic phrases (more concerning)
	for _, phrase := range genericPhrases {
		if strings.HasPrefix(content, phrase) {
			return true, fmt.Sprintf("response starts with generic phrase: '%s'", phrase)
		}
	}

	// Check for overly generic responses (short and generic)
	if len(strings.TrimSpace(content)) < 30 {
		for _, phrase := range genericPhrases {
			if strings.Contains(content, phrase) {
				return true, fmt.Sprintf("short response contains generic phrase: '%s'", phrase)
			}
		}
	}

	return false, ""
}

func (c *ResponsesGenericResponseCondition) GetConditionName() string {
	return "ResponsesGenericResponse"
}

// ResponsesContentValidationCondition checks if response fails basic content validation for Responses API
type ResponsesContentValidationCondition struct{}

func (c *ResponsesContentValidationCondition) ShouldRetry(response *schemas.BifrostResponsesResponse, err *schemas.BifrostError, context TestRetryContext) (bool, string) {
	if err != nil || response == nil {
		return false, ""
	}

	content := strings.ToLower(GetResponsesContent(response))

	// Skip if response is too short (other conditions will handle these)
	if len(content) < 10 {
		return false, ""
	}

	// Check for file/document processing scenarios
	scenarioName := strings.ToLower(context.ScenarioName)
	if strings.Contains(scenarioName, "file") || strings.Contains(scenarioName, "document") || strings.Contains(scenarioName, "pdf") {
		// Check if this test has expected keywords from the TestRetryContext
		if testMetadata, exists := context.TestMetadata["expected_keywords"]; exists {
			if expectedKeywords, ok := testMetadata.([]string); ok && len(expectedKeywords) > 0 {
				// Check if ANY of the expected keywords are present
				foundExpectedKeyword := false
				for _, keyword := range expectedKeywords {
					if strings.Contains(content, strings.ToLower(keyword)) {
						foundExpectedKeyword = true
						break
					}
				}

				// If valid response but missing ALL expected keywords, retry
				if !foundExpectedKeyword && len(content) > 20 && len(content) < 2000 {
					return true, fmt.Sprintf("response missing expected keywords %v, might include them on retry", expectedKeywords)
				}
			}
		}
	}

	return false, ""
}

func (c *ResponsesContentValidationCondition) GetConditionName() string {
	return "ResponsesContentValidation"
}
