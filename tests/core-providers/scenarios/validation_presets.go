package scenarios

import (
	"regexp"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// PRESET VALIDATION EXPECTATIONS FOR COMMON SCENARIOS
// =============================================================================

// BasicChatExpectations returns validation expectations for basic chat scenarios
func BasicChatExpectations() ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    true,
		MinContentLength:     5,    // At least a few characters
		MaxContentLength:     2000, // Reasonable upper bound
		ExpectedChoiceCount:  1,    // Usually expect one choice, will be used on outputs for responses API
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		ShouldNotContainWords: []string{
			"i can't", "i cannot", "i'm unable", "i am unable",
			"i don't know", "i'm not sure", "i am not sure",
		},
	}
}

// ToolCallExpectations returns validation expectations for tool calling scenarios
func ToolCallExpectations(toolName string, requiredArgs []string) ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.ExpectedToolCalls = []ToolCallExpectation{
		{
			FunctionName:     toolName,
			RequiredArgs:     requiredArgs,
			ValidateArgsJSON: true,
		},
	}
	// Tool calls might not have text content
	expectations.ShouldHaveContent = false
	expectations.MinContentLength = 0

	return expectations
}

// WeatherToolExpectations returns validation expectations for weather tool calls
func WeatherToolExpectations() ResponseExpectations {
	return ToolCallExpectations(string(SampleToolTypeWeather), []string{"location"})
}

// CalculatorToolExpectations returns validation expectations for calculator tool calls
func CalculatorToolExpectations() ResponseExpectations {
	return ToolCallExpectations(string(SampleToolTypeCalculate), []string{"expression"})
}

// TimeToolExpectations returns validation expectations for time tool calls
func TimeToolExpectations() ResponseExpectations {
	return ToolCallExpectations(string(SampleToolTypeTime), []string{"timezone"})
}

// MultipleToolExpectations returns validation expectations for multiple tool calls
func MultipleToolExpectations(tools []string, requiredArgsPerTool [][]string) ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.ShouldHaveContent = false // Tool calls might not have text content
	expectations.MinContentLength = 0

	for i, tool := range tools {
		var args []string
		if i < len(requiredArgsPerTool) {
			args = requiredArgsPerTool[i]
		}

		expectations.ExpectedToolCalls = append(expectations.ExpectedToolCalls, ToolCallExpectation{
			FunctionName:     tool,
			RequiredArgs:     args,
			ValidateArgsJSON: true,
		})
	}

	return expectations
}

// ImageAnalysisExpectations returns validation expectations for image analysis scenarios
func ImageAnalysisExpectations() ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.MinContentLength = 20 // Image descriptions should be more detailed
	expectations.ShouldContainKeywords = []string{"image", "picture", "photo", "see", "shows", "contains"}
	expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
		"i can't see", "i cannot see", "unable to see", "can't view",
		"cannot view", "no image", "not able to see", "i don't see",
	}...)

	return expectations
}

// TextCompletionExpectations returns validation expectations for text completion scenarios
func TextCompletionExpectations() ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.MinContentLength = 10 // Completions should have reasonable length

	return expectations
}

// EmbeddingExpectations returns validation expectations for embedding scenarios
func EmbeddingExpectations(expectedTexts []string) ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:   false, // Embeddings don't have text content
		ExpectedChoiceCount: 0,     // Embeddings use different structure
		ShouldHaveModel:     true,
		// Custom validation will be needed for embedding data
		ProviderSpecific: map[string]interface{}{
			"expected_embedding_count": len(expectedTexts),
			"expected_texts":           expectedTexts,
		},
	}
}

// StreamingExpectations returns validation expectations for streaming scenarios
func StreamingExpectations() ResponseExpectations {
	expectations := BasicChatExpectations()

	return expectations
}

// ConversationExpectations returns validation expectations for multi-turn conversation scenarios
func ConversationExpectations(contextKeywords []string) ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.MinContentLength = 15                   // Conversation responses should be more substantial
	expectations.ShouldContainKeywords = contextKeywords // Should reference conversation context

	return expectations
}

// VisionExpectations returns validation expectations for vision/image processing scenarios
func VisionExpectations(expectedKeywords []string) ResponseExpectations {
	expectations := ImageAnalysisExpectations() // Use existing image analysis base
	if len(expectedKeywords) > 0 {
		expectations.ShouldContainKeywords = expectedKeywords
	}
	expectations.MinContentLength = 20   // Vision responses should be descriptive
	expectations.MaxContentLength = 1200 // Vision models can be verbose
	expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords,
		"cannot see", "unable to view", "no image", "can't see",
		"image not found", "invalid image", "corrupted image",
		"failed to load", "error processing",
	)
	expectations.IsRelevantToPrompt = true
	return expectations
}

// SpeechExpectations returns validation expectations for speech synthesis scenarios
func SpeechExpectations(minAudioBytes int) ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    false, // Speech responses don't have text content
		ExpectedChoiceCount:  0,     // Speech responses don't have choices
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		// Speech-specific validations stored in ProviderSpecific
		ProviderSpecific: map[string]interface{}{
			"min_audio_bytes":   minAudioBytes,
			"should_have_audio": true,
			"expected_format":   "audio", // General audio format
			"response_type":     "speech_synthesis",
		},
	}
}

// TranscriptionExpectations returns validation expectations for transcription scenarios
func TranscriptionExpectations(minTextLength int) ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    false, // Transcription has transcribed text, not chat content
		ExpectedChoiceCount:  0,     // Transcription responses don't have choices
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		// Transcription-specific validations
		ShouldNotContainWords: []string{
			"could not transcribe", "failed to process",
			"invalid audio", "corrupted audio",
			"unsupported format", "transcription error",
			"no audio detected", "silence detected",
		},
		ProviderSpecific: map[string]interface{}{
			"min_transcription_length":  minTextLength,
			"should_have_transcription": true,
			"response_type":             "transcription",
		},
	}
}

// =============================================================================
// SCENARIO-SPECIFIC EXPECTATION BUILDERS
// =============================================================================

// GetExpectationsForScenario returns appropriate validation expectations for a given scenario
func GetExpectationsForScenario(scenarioName string, testConfig config.ComprehensiveTestConfig, customParams map[string]interface{}) ResponseExpectations {
	switch scenarioName {
	case "SimpleChat":
		return BasicChatExpectations()

	case "TextCompletion":
		return TextCompletionExpectations()

	case "ToolCalls":
		if toolName, ok := customParams["tool_name"].(string); ok {
			if args, ok := customParams["required_args"].([]string); ok {
				return ToolCallExpectations(toolName, args)
			}
		}
		return WeatherToolExpectations() // Default to weather tool

	case "MultipleToolCalls":
		if tools, ok := customParams["tool_names"].([]string); ok {
			if argsPerTool, ok := customParams["required_args_per_tool"].([][]string); ok {
				return MultipleToolExpectations(tools, argsPerTool)
			}
		}
		// Default to weather and calculator
		return MultipleToolExpectations(
			[]string{string(SampleToolTypeWeather), string(SampleToolTypeCalculate)},
			[][]string{{"location"}, {"expression"}},
		)

	case "End2EndToolCalling":
		return ConversationExpectations([]string{"weather", "temperature", "result"})

	case "AutomaticFunctionCalling":
		expectations := WeatherToolExpectations()
		expectations.ShouldHaveContent = true // Should have follow-up text after tool call
		expectations.MinContentLength = 20
		return expectations

	case "ImageURL", "ImageBase64":
		return VisionExpectations([]string{"image", "picture", "see"})

	case "MultipleImages":
		return VisionExpectations([]string{"compare", "similar", "different", "images"})

	case "ChatCompletionStream":
		return StreamingExpectations()

	case "MultiTurnConversation":
		if keywords, ok := customParams["context_keywords"].([]string); ok {
			return ConversationExpectations(keywords)
		}
		return ConversationExpectations([]string{"context", "previous", "mentioned"})

	case "Embedding":
		if texts, ok := customParams["input_texts"].([]string); ok {
			return EmbeddingExpectations(texts)
		}
		return EmbeddingExpectations([]string{"Hello, world!", "Hi, world!", "Goodnight, moon!"})

	case "CompleteEnd2End":
		return ConversationExpectations([]string{"complete", "comprehensive", "full"})

	case "SpeechSynthesis":
		if minBytes, ok := customParams["min_audio_bytes"].(int); ok {
			return SpeechExpectations(minBytes)
		}
		return SpeechExpectations(500) // Default minimum 500 bytes

	case "Transcription":
		if minLength, ok := customParams["min_transcription_length"].(int); ok {
			return TranscriptionExpectations(minLength)
		}
		return TranscriptionExpectations(10) // Default minimum 10 characters

	case "ProviderSpecific":
		expectations := BasicChatExpectations()
		expectations.ShouldContainKeywords = []string{"unique", "specific", "capability"}
		return expectations

	default:
		// Default to basic chat expectations
		return BasicChatExpectations()
	}
}

// =============================================================================
// PROVIDER-SPECIFIC EXPECTATION MODIFIERS
// =============================================================================

// ModifyExpectationsForProvider adjusts expectations based on provider capabilities
func ModifyExpectationsForProvider(expectations ResponseExpectations, provider schemas.ModelProvider) ResponseExpectations {
	switch provider {
	case schemas.OpenAI:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveTimestamps = true
		expectations.ShouldHaveModel = true

	case schemas.Anthropic:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveModel = true
		// Claude might have different response patterns

	case schemas.Bedrock:
		expectations.ShouldHaveModel = true
		// AWS Bedrock has different usage reporting
		expectations.ShouldHaveUsageStats = false // Often not included

	case schemas.Cohere:
		expectations.ShouldHaveModel = true
		expectations.ShouldHaveUsageStats = true

	case schemas.Vertex:
		expectations.ShouldHaveModel = true
		// Google Vertex AI has different metadata

	case schemas.Mistral:
		expectations.ShouldHaveModel = true
		expectations.ShouldHaveUsageStats = true

	case schemas.Ollama:
		// Local models might have different metadata expectations
		expectations.ShouldHaveUsageStats = false
		expectations.ShouldHaveTimestamps = false

	case schemas.Groq:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveModel = true

	case schemas.Gemini:
		expectations.ShouldHaveModel = true
		expectations.ShouldHaveUsageStats = true

	default:
		// Keep default expectations
	}

	return expectations
}

// =============================================================================
// ADVANCED VALIDATION EXPECTATIONS
// =============================================================================

// SemanticCoherenceExpectations returns expectations for semantic coherence tests
func SemanticCoherenceExpectations(inputPrompt string, expectedTopics []string) ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.MinContentLength = 30 // More substantial response needed
	expectations.ShouldContainKeywords = expectedTopics
	expectations.IsRelevantToPrompt = true

	// Add pattern for coherent responses (no contradictions, proper flow)
	expectations.ContentPattern = regexp.MustCompile(`^[A-Z].*[.!?]$`) // Should start with capital and end with punctuation

	return expectations
}

// ConsistencyExpectations returns expectations for consistency tests
func ConsistencyExpectations(expectedConsistencyMarkers []string) ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.ShouldContainKeywords = expectedConsistencyMarkers
	expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
		"however", "but", "on the other hand", // Contradiction markers
		"i'm not sure", "maybe", "possibly", "might be", // Uncertainty markers
	}...)

	return expectations
}

// =============================================================================
// UTILITY FUNCTIONS
// =============================================================================

// stringPtr returns a pointer to a string
func stringPtr(s string) *string {
	return &s
}

// CombineExpectations merges multiple expectations (later ones override earlier ones)
func CombineExpectations(expectations ...ResponseExpectations) ResponseExpectations {
	if len(expectations) == 0 {
		return BasicChatExpectations()
	}

	base := expectations[0]

	for _, exp := range expectations[1:] {
		// Override fields that are set in the new expectation
		if exp.ShouldHaveContent {
			base.ShouldHaveContent = exp.ShouldHaveContent
		}
		if exp.MinContentLength > 0 {
			base.MinContentLength = exp.MinContentLength
		}
		if exp.MaxContentLength > 0 {
			base.MaxContentLength = exp.MaxContentLength
		}
		if exp.ExpectedChoiceCount > 0 {
			base.ExpectedChoiceCount = exp.ExpectedChoiceCount
		}
		if exp.ExpectedFinishReason != nil {
			base.ExpectedFinishReason = exp.ExpectedFinishReason
		}

		// Append arrays
		base.ShouldContainKeywords = append(base.ShouldContainKeywords, exp.ShouldContainKeywords...)
		base.ShouldNotContainWords = append(base.ShouldNotContainWords, exp.ShouldNotContainWords...)
		base.ExpectedToolCalls = append(base.ExpectedToolCalls, exp.ExpectedToolCalls...)

		// Override other fields
		if exp.ContentPattern != nil {
			base.ContentPattern = exp.ContentPattern
		}
		if exp.IsRelevantToPrompt {
			base.IsRelevantToPrompt = exp.IsRelevantToPrompt
		}
		if exp.ShouldNotHaveFunctionCalls {
			base.ShouldNotHaveFunctionCalls = exp.ShouldNotHaveFunctionCalls
		}
		if exp.ShouldHaveUsageStats {
			base.ShouldHaveUsageStats = exp.ShouldHaveUsageStats
		}
		if exp.ShouldHaveTimestamps {
			base.ShouldHaveTimestamps = exp.ShouldHaveTimestamps
		}
		if exp.ShouldHaveModel {
			base.ShouldHaveModel = exp.ShouldHaveModel
		}

		// Merge provider specific data
		if len(exp.ProviderSpecific) > 0 {
			if base.ProviderSpecific == nil {
				base.ProviderSpecific = make(map[string]interface{})
			}
			for k, v := range exp.ProviderSpecific {
				base.ProviderSpecific[k] = v
			}
		}
	}

	return base
}
