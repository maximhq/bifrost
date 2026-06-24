package llmtests

import (
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// PRESET VALIDATION EXPECTATIONS FOR COMMON SCENARIOS
// =============================================================================

// BasicChatExpectations returns validation expectations for basic chat scenarios
func BasicChatExpectations() ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    true,
		ExpectedChoiceCount:  1, // Usually expect one choice, will be used on outputs for responses API
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		ShouldHaveLatency:    true, // Global expectation: latency should always be present
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
	expectations.ShouldHaveContent = false // Tool calls might not have text Content

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

	return expectations
}

// EmbeddingExpectations returns validation expectations for embedding scenarios
func EmbeddingExpectations(expectedTexts []string) ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:   false, // Embeddings don't have text content
		ExpectedChoiceCount: 0,     // Embeddings use different structure
		ShouldHaveModel:     true,
		ShouldHaveLatency:   true, // Global expectation: latency should always be present
		// Custom validation will be needed for embedding data
		ProviderSpecific: map[string]interface{}{
			"expected_embedding_count": len(expectedTexts),
			"expected_texts":           expectedTexts,
		},
	}
}

// CountTokensExpectations returns validation expectations for count tokens scenarios
func CountTokensExpectations() ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    false, // CountTokens doesn't return text content
		ExpectedChoiceCount:  0,
		ShouldHaveUsageStats: true,
		ShouldHaveModel:      true,
		ShouldHaveLatency:    true,
		ProviderSpecific: map[string]interface{}{
			"response_type": "count_tokens",
		},
	}
}

// StreamingExpectations returns validation expectations for streaming scenarios
func StreamingExpectations() ResponseExpectations {
	expectations := BasicChatExpectations()

	// Streaming consolidated responses are assembled from chunks.
	// The last chunk often does not carry created/model fields,
	// so we cannot reliably validate them on the consolidated response.
	expectations.ShouldHaveTimestamps = false
	expectations.ShouldHaveModel = false

	return expectations
}

// ConversationExpectations returns validation expectations for multi-turn conversation scenarios
func ConversationExpectations(contextKeywords []string) ResponseExpectations {
	expectations := BasicChatExpectations()
	expectations.ShouldContainAnyOf = contextKeywords // Should reference conversation context

	return expectations
}

// VisionExpectations returns validation expectations for vision/image processing scenarios
func VisionExpectations(expectedKeywords []string) ResponseExpectations {
	expectations := ImageAnalysisExpectations() // Use existing image analysis base
	if len(expectedKeywords) > 0 {
		expectations.ShouldContainKeywords = expectedKeywords
	}
	expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords,
		"cannot see", "unable to view", "no image", "can't see",
		"image not found", "invalid image", "corrupted image",
		"failed to load", "error processing",
	)
	expectations.IsRelevantToPrompt = true
	return expectations
}

// FileInputExpectations returns validation expectations for file input scenarios
func FileInputExpectations() ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:     true,
		ExpectedChoiceCount:   1,
		ShouldHaveUsageStats:  true,
		ShouldHaveTimestamps:  true,
		ShouldHaveModel:       true,
		ShouldHaveLatency:     true,
		ShouldContainKeywords: []string{"hello", "world"}, // Content from the test PDF
		ShouldNotContainWords: []string{
			"cannot", "unable", "error", "failed",
			"unsupported", "invalid", "corrupted",
			"can't read", "cannot read", "no file",
			"no document", "cannot process",
		},
		IsRelevantToPrompt: true,
	}
}

// SpeechExpectations returns validation expectations for speech synthesis scenarios
func SpeechExpectations(minAudioBytes int) ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    false, // Speech responses don't have text content
		ExpectedChoiceCount:  0,     // Speech responses don't have choices
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		ShouldHaveLatency:    true, // Global expectation: latency should always be present
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
		ShouldHaveLatency:    true, // Global expectation: latency should always be present
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

func ImageGenerationExpectations(minImages int, expectedSize string) ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    false, // Image responses don't have text content
		ExpectedChoiceCount:  0,     // Image responses don't have choices
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		ShouldHaveLatency:    true, // Global expectation: latency should always be present
		ProviderSpecific: map[string]interface{}{
			"min_images":    minImages,
			"expected_size": expectedSize,
			"response_type": "image_generation",
		},
	}
}

// ReasoningExpectations returns validation expectations for reasoning scenarios
func ReasoningExpectations() ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    true,
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		ProviderSpecific: map[string]interface{}{
			"response_type":        "reasoning",
			"expects_step_by_step": true,
		},
	}
}

// ChatAudioExpectations returns validation expectations for chat audio scenarios
func ChatAudioExpectations() ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent:    false, // Chat audio responses may have audio/transcript but not text content
		ExpectedChoiceCount:  1,     // Should have one choice with audio data
		ShouldHaveUsageStats: true,
		ShouldHaveTimestamps: true,
		ShouldHaveModel:      true,
		ShouldHaveLatency:    true, // Global expectation: latency should always be present
		ProviderSpecific: map[string]interface{}{
			"response_type": "chat_audio",
		},
	}
}

// =============================================================================
// SCENARIO-SPECIFIC EXPECTATION BUILDERS
// =============================================================================

// GetExpectationsForScenario returns appropriate validation expectations for a given scenario
func GetExpectationsForScenario(scenarioName string, testConfig ComprehensiveTestConfig, customParams map[string]interface{}) ResponseExpectations {
	var expectations ResponseExpectations

	switch scenarioName {
	case "SimpleChat":
		expectations = BasicChatExpectations()

	case "TextCompletion":
		expectations = TextCompletionExpectations()

	case "ToolCalls":
		if toolName, ok := customParams["tool_name"].(string); ok {
			if args, ok := customParams["required_args"].([]string); ok {
				expectations = ToolCallExpectations(toolName, args)
				break
			}
		}
		expectations = WeatherToolExpectations() // Default to weather tool

	case "MultipleToolCalls":
		if tools, ok := customParams["tool_names"].([]string); ok {
			if argsPerTool, ok := customParams["required_args_per_tool"].([][]string); ok {
				expectations = MultipleToolExpectations(tools, argsPerTool)
				break
			}
		}
		// Default to weather and calculator
		expectations = MultipleToolExpectations(
			[]string{string(SampleToolTypeWeather), string(SampleToolTypeCalculate)},
			[][]string{{"location"}, {"expression"}},
		)

	case "End2EndToolCalling":
		expectations = ConversationExpectations([]string{"weather", "temperature", "result"})

	case "AutomaticFunctionCalling":
		expectations = WeatherToolExpectations()
		expectations.ShouldHaveContent = true // Should have follow-up text after tool call

	case "ImageURL", "ImageBase64":
		expectations = VisionExpectations([]string{"image", "picture", "see"})

	case "MultipleImages":
		expectations = VisionExpectations([]string{"compare", "similar", "different", "images"})

	case "FileInput":
		expectations = FileInputExpectations()

	case "ChatCompletionStream", "TextCompletionStream":
		expectations = StreamingExpectations()

	case "MultiTurnConversation":
		if keywords, ok := customParams["context_keywords"].([]string); ok {
			expectations = ConversationExpectations(keywords)
		} else {
			expectations = ConversationExpectations([]string{"context", "previous", "mentioned"})
		}

	case "Embedding":
		if texts, ok := customParams["input_texts"].([]string); ok {
			expectations = EmbeddingExpectations(texts)
		} else {
			expectations = EmbeddingExpectations([]string{"Hello, world!", "Hi, world!", "Goodnight, moon!"})
		}

	case "CountTokens":
		expectations = CountTokensExpectations()

	case "CompleteEnd2End":
		expectations = ConversationExpectations([]string{"complete", "comprehensive", "full"})

	case "SpeechSynthesis":
		if minBytes, ok := customParams["min_audio_bytes"].(int); ok {
			expectations = SpeechExpectations(minBytes)
		} else {
			expectations = SpeechExpectations(500) // Default minimum 500 bytes
		}

	case "Transcription":
		if minLength, ok := customParams["min_transcription_length"].(int); ok {
			expectations = TranscriptionExpectations(minLength)
		} else {
			expectations = TranscriptionExpectations(10) // Default minimum 10 characters
		}

	case "Reasoning":
		expectations = ReasoningExpectations()

	case "ChatAudio":
		expectations = ChatAudioExpectations()

	case "ProviderSpecific":
		expectations = BasicChatExpectations()
		expectations.ShouldContainKeywords = []string{"unique", "specific", "capability"}

	case "ImageGeneration":
		if minImages, ok := customParams["min_images"].(int); ok {
			if expectedSize, ok := customParams["expected_size"].(string); ok {
				expectations = ImageGenerationExpectations(minImages, expectedSize)
				break
			}
		}
		expectations = ImageGenerationExpectations(1, "1024x1024")

	case "ImageEdit", "ImageVariation":
		// Reuse image generation expectations since they use the same response structure
		if minImages, ok := customParams["min_images"].(int); ok {
			if expectedSize, ok := customParams["expected_size"].(string); ok {
				expectations = ImageGenerationExpectations(minImages, expectedSize)
				break
			}
		}
		expectations = ImageGenerationExpectations(1, "1024x1024")

	default:
		// Default to basic chat expectations
		expectations = BasicChatExpectations()
	}

	// Apply raw request/response expectations from test config
	isStreaming := strings.HasSuffix(scenarioName, "Stream") || strings.HasSuffix(scenarioName, "Streaming")
	isMultipartRequest := scenarioName == "Transcription" || scenarioName == "TranscriptionStream" ||
		scenarioName == "ImageEdit" || scenarioName == "ImageEditStream" ||
		scenarioName == "ImageVariation"
	// Skip raw request/response for CountTokens - not all providers support it uniformly
	if scenarioName != "CountTokens" {
		expectations = ApplyRawExpectations(expectations, testConfig, isStreaming, isMultipartRequest)
	}

	return expectations
}

// =============================================================================
// PROVIDER-SPECIFIC EXPECTATION MODIFIERS
// =============================================================================

// ModifyExpectationsForProvider adjusts expectations based on provider capabilities.
// Each provider is explicitly configured for: usage stats, timestamps, model, and latency.
// If a provider is not listed, defaults are kept (all true from BasicChatExpectations).
func ModifyExpectationsForProvider(expectations ResponseExpectations, provider schemas.ModelProvider) ResponseExpectations {
	// NOTE: This function must NOT set ShouldHaveTimestamps or ShouldHaveModel to true.
	// StreamingExpectations explicitly disables those fields, and overriding them here
	// would cause streaming tests to incorrectly assert on fields that consolidated
	// streaming responses cannot reliably carry.
	// ShouldHaveUsageStats and ShouldHaveLatency may still be enabled here because no
	// scenario preset disables them, and some presets (e.g. ReasoningExpectations) omit
	// ShouldHaveLatency entirely.
	switch provider {
	case schemas.OpenAI:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.Azure:
		// Azure OpenAI returns the same fields as OpenAI
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.Anthropic:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.Bedrock:
		// Bedrock returns usage stats for most calls via Bifrost normalization, but not all
		expectations.ShouldHaveTimestamps = false // Bedrock does not return created timestamps
		expectations.ShouldHaveLatency = true

	case schemas.Cohere:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveModel = false // Cohere does not return model field in all response types
		expectations.ShouldHaveLatency = true

	case schemas.Vertex:
		// Google Vertex AI returns usage and model but may not return timestamps
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveTimestamps = false // Vertex does not return created timestamps
		expectations.ShouldHaveLatency = true

	case schemas.Mistral:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.Ollama:
		// Local models may not return usage or timestamps
		expectations.ShouldHaveUsageStats = false
		expectations.ShouldHaveTimestamps = false
		expectations.ShouldHaveLatency = true

	case schemas.Groq:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.Gemini:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveTimestamps = false // Gemini does not return created timestamps
		expectations.ShouldHaveLatency = true

	case schemas.Perplexity:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveTimestamps = false // Perplexity does not return created timestamps
		expectations.ShouldHaveModel = false      // Perplexity does not return model field
		expectations.ShouldHaveLatency = true

	case schemas.Cerebras:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.OpenRouter:
		// OpenRouter proxies to multiple providers; returns OpenAI-compatible fields
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.XAI:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.Nebius:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.SGL:
		// SGLang local inference — may not return all fields
		expectations.ShouldHaveUsageStats = false
		expectations.ShouldHaveTimestamps = false
		expectations.ShouldHaveLatency = true

	case schemas.Parasail:
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveTimestamps = false // Parasail does not return created timestamps
		expectations.ShouldHaveModel = false      // Parasail does not return model field
		expectations.ShouldHaveLatency = true

	case schemas.Elevenlabs:
		// Elevenlabs is primarily audio — usage/timestamps may not apply to all calls
		expectations.ShouldHaveUsageStats = false
		expectations.ShouldHaveTimestamps = false
		expectations.ShouldHaveLatency = true

	case schemas.HuggingFace:
		expectations.ShouldHaveUsageStats = false
		expectations.ShouldHaveTimestamps = false
		expectations.ShouldHaveLatency = true

	case schemas.Replicate:
		expectations.ShouldHaveUsageStats = false
		expectations.ShouldHaveTimestamps = false
		expectations.ShouldHaveLatency = true

	case schemas.VLLM:
		// vLLM local inference — OpenAI-compatible
		expectations.ShouldHaveUsageStats = true
		expectations.ShouldHaveLatency = true

	case schemas.Runway:
		// Runway is primarily video/image generation
		expectations.ShouldHaveUsageStats = false
		expectations.ShouldHaveTimestamps = false
		expectations.ShouldHaveLatency = true

	default:
		// Keep default expectations — all true from BasicChatExpectations
	}

	return expectations
}

// ApplyRawExpectations applies raw request/response expectations based on test config.
// Call this after creating expectations directly (SpeechExpectations, TranscriptionExpectations, etc.)
// when not using GetExpectationsForScenario.
// Parameters:
//   - isStreaming: if true, skips RawResponse expectation (streaming has no single response body)
//   - options: variadic bool options:
//   - options[0] = isMultipartRequest: if true, skips RawRequest expectation (multipart form data can't return raw JSON request)
//   - options[1] = isBinaryResponse: if true, skips RawResponse expectation (binary responses like audio don't have JSON raw response)
func ApplyRawExpectations(expectations ResponseExpectations, testConfig ComprehensiveTestConfig, isStreaming bool, options ...bool) ResponseExpectations {
	if testConfig.ExpectRawRequestResponse {
		// options[0] = isMultipartRequest (skip RawRequest for multipart form data requests like transcription)
		// options[1] = isBinaryResponse (skip RawResponse for binary responses like speech synthesis audio)
		skipRawRequest := len(options) > 0 && options[0]
		skipRawResponse := len(options) > 1 && options[1]
		if !skipRawRequest {
			expectations.ShouldHaveRawRequest = true
		}
		if !isStreaming && !skipRawResponse {
			expectations.ShouldHaveRawResponse = true
		}
	}
	return expectations
}

// =============================================================================
// ADVANCED VALIDATION EXPECTATIONS
// =============================================================================

// SemanticCoherenceExpectations returns expectations for semantic coherence tests
func SemanticCoherenceExpectations(inputPrompt string, expectedTopics []string) ResponseExpectations {
	expectations := BasicChatExpectations()
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
		if exp.ShouldHaveLatency {
			base.ShouldHaveLatency = exp.ShouldHaveLatency
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
