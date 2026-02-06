package llmtests

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Shared test texts for TTS->SST round-trip validation
const (
	// Basic test text for simple round-trip validation
	TTSTestTextBasic = "Hello, this is a comprehensive test of speech synthesis capabilities from Bifrost AI Gateway. We are testing various aspects of text-to-speech conversion including clarity, pronunciation, and overall audio quality. This basic test should demonstrate the fundamental functionality of converting written text into natural-sounding speech audio."

	// Medium length text with punctuation for comprehensive testing
	TTSTestTextMedium = "Testing speech synthesis and transcription round-trip functionality with Bifrost AI Gateway. This comprehensive text includes various punctuation marks: commas, periods, exclamation points! Question marks? Semicolons; and colons: for thorough testing. We also include numbers like 123, 456.789, and technical terms such as API, HTTP, JSON, WebSocket, and machine learning algorithms. The system should handle abbreviations like Dr., Mr., Mrs., and acronyms like NASA, FBI, and CPU correctly. Additionally, we test special characters and symbols: @, #, $, %, &, *, +, =, and various currency symbols like €, £, ¥."

	// Technical text for comprehensive format testing
	TTSTestTextTechnical = "Bifrost AI Gateway is a sophisticated artificial intelligence proxy server that efficiently processes and routes audio requests, chat completions, embeddings, and various machine learning workloads across multiple provider endpoints. The system implements advanced load balancing algorithms, request queuing mechanisms, and intelligent failover strategies to ensure high availability and optimal performance. It supports multiple audio formats including MP3, WAV, FLAC, and OGG, with configurable bitrates, sample rates, and encoding parameters. The gateway handles authentication, rate limiting, request validation, response transformation, and comprehensive logging for enterprise-grade deployments. Performance metrics indicate sub-100ms latency for most operations with 99.9% uptime reliability."
)

func GetProviderDefaultFormat(provider schemas.ModelProvider) string {
	switch provider {
	case schemas.Gemini:
		return "wav"
	default:
		return "mp3"
	}
}

// GetProviderVoice returns an appropriate voice for the given provider
func GetProviderVoice(provider schemas.ModelProvider, voiceType string) string {
	switch provider {
	case schemas.OpenAI:
		switch voiceType {
		case "primary":
			return "alloy"
		case "secondary":
			return "nova"
		case "tertiary":
			return "echo"
		default:
			return "alloy"
		}
	case schemas.Gemini:
		switch voiceType {
		case "primary":
			return "achernar"
		case "secondary":
			return "aoede"
		case "tertiary":
			return "erinome"
		default:
			return "achernar"
		}
	case schemas.Elevenlabs:
		switch voiceType {
		case "primary":
			return "21m00Tcm4TlvDq8ikWAM"
		case "secondary":
			return "29vD33N1CtxCmqQRPOHJ"
		case "tertiary":
			return "2EiwWnXFnvU5JabPnv8n"
		default:
			return "21m00Tcm4TlvDq8ikWAM"
		}
	default:
		// Default to OpenAI voices for other providers
		switch voiceType {
		case "primary":
			return "alloy"
		case "secondary":
			return "nova"
		case "tertiary":
			return "echo"
		default:
			return "alloy"
		}
	}
}

type SampleToolType string

const (
	SampleToolTypeWeather   SampleToolType = "weather"
	SampleToolTypeCalculate SampleToolType = "calculate"
	SampleToolTypeTime      SampleToolType = "time"
)

var SampleToolFunctions = map[SampleToolType]*schemas.ChatToolFunction{
	SampleToolTypeWeather:   WeatherToolFunction,
	SampleToolTypeCalculate: CalculatorToolFunction,
	SampleToolTypeTime:      TimeToolFunction,
}

var sampleToolDescriptions = map[SampleToolType]string{
	SampleToolTypeWeather:   "Get the current weather in a given location",
	SampleToolTypeCalculate: "Perform basic mathematical calculations",
	SampleToolTypeTime:      "Get the current time in a specific timezone",
}

var WeatherToolFunction = &schemas.ChatToolFunction{
	Parameters: &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: &schemas.OrderedMap{
			"location": map[string]interface{}{
				"type":        "string",
				"description": "The city and state, e.g. San Francisco, CA",
			},
			"unit": map[string]interface{}{
				"type": "string",
				"enum": []string{"celsius", "fahrenheit"},
			},
		},
		Required: []string{"location"},
	},
}

var CalculatorToolFunction = &schemas.ChatToolFunction{
	Parameters: &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: &schemas.OrderedMap{
			"expression": map[string]interface{}{
				"type":        "string",
				"description": "The mathematical expression to evaluate, e.g. '2 + 3' or '10 * 5'",
			},
		},
		Required: []string{"expression"},
	},
}

var TimeToolFunction = &schemas.ChatToolFunction{
	Parameters: &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: &schemas.OrderedMap{
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "The timezone identifier, e.g. 'America/New_York' or 'UTC'",
			},
		},
		Required: []string{"timezone"},
	},
}

func GetSampleChatTool(toolName SampleToolType) *schemas.ChatTool {
	function, ok := SampleToolFunctions[toolName]
	if !ok {
		return nil
	}

	description, ok := sampleToolDescriptions[toolName]
	if !ok {
		return nil
	}

	return &schemas.ChatTool{
		Type: "function",
		Function: &schemas.ChatToolFunction{
			Name:        string(toolName),
			Description: bifrost.Ptr(description),
			Parameters:  function.Parameters,
		},
	}
}

func GetSampleResponsesTool(toolName SampleToolType) *schemas.ResponsesTool {
	function, ok := SampleToolFunctions[toolName]
	if !ok {
		return nil
	}

	description, ok := sampleToolDescriptions[toolName]
	if !ok {
		return nil
	}

	return &schemas.ResponsesTool{
		Type:        "function",
		Name:        bifrost.Ptr(string(toolName)),
		Description: bifrost.Ptr(description),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: function.Parameters,
		},
	}
}

// Test file URL
const TestFileURL = "https://www.berkshirehathaway.com/letters/2024ltr.pdf"

// Test image of an ant
const TestImageURL = "https://pestworldcdn-dcf2a8gbggazaghf.z01.azurefd.net/media/561791/carpenter-ant4.jpg"

// Test image of the Eiffel Tower
const TestImageURL2 = "https://images.pexels.com/photos/30662605/pexels-photo-30662605/free-photo-of-eiffel-tower-view-from-the-seine-river-in-paris.jpeg"

// Test image base64 of a grey solid
const TestImageBase64 = "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k="

// GetLionBase64Image loads and returns the lion base64 image data from file
func GetLionBase64Image() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to get current file path")
	}
	dir := filepath.Dir(filename)
	filePath := filepath.Join(dir, "scenarios", "media", "lion_base64.txt")

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + string(data), nil
}

// GetSampleAudioBase64 loads and returns the sample audio file as base64 encoded string
func GetSampleAudioBase64() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("failed to get current file path")
	}
	dir := filepath.Dir(filename)
	filePath := filepath.Join(dir, "scenarios", "media", "sample.mp3")

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// CreateSpeechRequest creates a basic speech input for testing
func CreateSpeechRequest(text, voice, format string) *schemas.BifrostSpeechRequest {
	return &schemas.BifrostSpeechRequest{
		Input: &schemas.SpeechInput{
			Input: text,
		},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{
				Voice: &voice,
			},
			ResponseFormat: format,
		},
	}
}

// CreateTranscriptionInput creates a basic transcription input for testing
func CreateTranscriptionInput(audioData []byte, language, responseFormat *string) *schemas.BifrostTranscriptionRequest {
	return &schemas.BifrostTranscriptionRequest{
		Input: &schemas.TranscriptionInput{
			File: audioData,
		},
		Params: &schemas.TranscriptionParameters{
			Language:       language,
			ResponseFormat: responseFormat,
		},
	}
}

// Helper functions for creating requests
func CreateBasicChatMessage(content string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateBasicResponsesMessage(content string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateImageChatMessage(text, imageURL string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: bifrost.Ptr(text)},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: imageURL}},
			},
		},
	}
}

func CreateImageResponsesMessage(text, imageURL string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: bifrost.Ptr(text)},
				{Type: schemas.ResponsesInputMessageContentBlockTypeImage,
					ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
						ImageURL: bifrost.Ptr(imageURL),
					},
				},
			},
		},
	}
}

func CreateAudioChatMessage(text, audioData string, audioFormat string) schemas.ChatMessage {
	format := bifrost.Ptr(audioFormat)
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: bifrost.Ptr(text)},
				{
					Type: schemas.ChatContentBlockTypeInputAudio,
					InputAudio: &schemas.ChatInputAudio{
						Data:   audioData,
						Format: format,
					},
				},
			},
		},
	}
}

func CreateToolChatMessage(content string, toolCallID string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: bifrost.Ptr(toolCallID),
		},
	}
}

func CreateToolResponsesMessage(content string, toolCallID string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: bifrost.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		// Note: function_call_output messages don't have a role field per OpenAI API
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: bifrost.Ptr(toolCallID),
			// Set ResponsesFunctionToolCallOutput for OpenAI's native Responses API
			Output: &schemas.ResponsesToolMessageOutputStruct{
				ResponsesToolCallOutputStr: bifrost.Ptr(content),
			},
		},
	}
}

// ToolCallInfo represents extracted tool call information for both API formats
type ToolCallInfo struct {
	Name      string
	Arguments string
	ID        string
}

// GetChatContent returns the string content from a BifrostChatResponse
func GetChatContent(response *schemas.BifrostChatResponse) string {
	if response == nil || response.Choices == nil {
		return ""
	}

	// Try to find content from any choice, prioritizing non-empty content
	for _, choice := range response.Choices {
		if choice.Message.Content != nil {
			// Check if content has any data (either ContentStr or ContentBlocks)
			if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
				return *choice.Message.Content.ContentStr
			} else if choice.Message.Content.ContentBlocks != nil {
				var builder strings.Builder
				for _, block := range choice.Message.Content.ContentBlocks {
					if block.Text != nil {
						builder.WriteString(*block.Text)
					}
				}
				content := builder.String()
				if content != "" {
					return content
				}
			}
		}
	}

	return ""
}

// GetTextCompletionContent returns the string content from a BifrostTextCompletionResponse
func GetTextCompletionContent(response *schemas.BifrostTextCompletionResponse) string {
	if response == nil || response.Choices == nil {
		return ""
	}

	// Try to find content from any choice, prioritizing non-empty content
	for _, choice := range response.Choices {
		if choice.Text != nil && *choice.Text != "" {
			return *choice.Text
		}
	}

	return ""
}

// GetResponsesContent returns the string content from a BifrostResponsesResponse
func GetResponsesContent(response *schemas.BifrostResponsesResponse) string {
	if response == nil || response.Output == nil {
		return ""
	}

	for _, output := range response.Output {
		// Check for regular content first
		if output.Content != nil {
			if output.Content.ContentStr != nil && *output.Content.ContentStr != "" {
				return *output.Content.ContentStr
			} else if output.Content.ContentBlocks != nil {
				var builder strings.Builder
				for _, block := range output.Content.ContentBlocks {
					if block.Text != nil {
						builder.WriteString(*block.Text)
					}
				}
				content := builder.String()
				if content != "" {
					return content
				}
			}
		}

		// Check for reasoning content in summary field
		if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeReasoning {
			if output.ResponsesReasoning != nil && output.ResponsesReasoning.Summary != nil {
				var builder strings.Builder
				for _, summaryBlock := range output.ResponsesReasoning.Summary {
					if summaryBlock.Text != "" {
						if builder.Len() > 0 {
							builder.WriteString("\n\n")
						}
						builder.WriteString(summaryBlock.Text)
					}
				}
				content := builder.String()
				if content != "" {
					return content
				}
			}
		}
	}

	return ""
}

// ExtractChatToolCalls extracts tool call information from a BifrostChatResponse
func ExtractChatToolCalls(response *schemas.BifrostChatResponse) []ToolCallInfo {
	var toolCalls []ToolCallInfo

	if response == nil || response.Choices == nil {
		return toolCalls
	}

	for _, choice := range response.Choices {
		if choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ToolCalls != nil {
			for _, toolCall := range choice.Message.ChatAssistantMessage.ToolCalls {
				info := ToolCallInfo{
					ID: *toolCall.ID,
				}
				if toolCall.Function.Name != nil {
					info.Name = *toolCall.Function.Name
				}
				info.Arguments = toolCall.Function.Arguments
				toolCalls = append(toolCalls, info)
			}
		}
	}

	return toolCalls
}

// ExtractResponsesToolCalls extracts tool call information from a BifrostResponsesResponse
func ExtractResponsesToolCalls(response *schemas.BifrostResponsesResponse) []ToolCallInfo {
	var toolCalls []ToolCallInfo

	if response == nil || response.Output == nil {
		return toolCalls
	}

	for _, output := range response.Output {
		if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeFunctionCall && output.ResponsesToolMessage != nil {
			info := ToolCallInfo{}
			if output.ResponsesToolMessage.Name != nil {
				info.Name = *output.ResponsesToolMessage.Name
			}
			if output.ResponsesToolMessage.Arguments != nil {
				info.Arguments = *output.ResponsesToolMessage.Arguments
			}
			if output.ResponsesToolMessage.CallID != nil {
				info.ID = *output.ResponsesToolMessage.CallID
			}
			toolCalls = append(toolCalls, info)
		}
	}

	return toolCalls
}

func GetResultContent(response *schemas.BifrostResponse) string {
	if response == nil {
		return ""
	}

	if response.ChatResponse != nil {
		return GetChatContent(response.ChatResponse)
	} else if response.ResponsesResponse != nil {
		return GetResponsesContent(response.ResponsesResponse)
	} else if response.TextCompletionResponse != nil {
		return GetTextCompletionContent(response.TextCompletionResponse)
	}
	return ""
}

func ExtractToolCalls(response *schemas.BifrostResponse) []ToolCallInfo {
	if response == nil {
		return []ToolCallInfo{}
	}

	if response.ChatResponse != nil {
		return ExtractChatToolCalls(response.ChatResponse)
	} else if response.ResponsesResponse != nil {
		return ExtractResponsesToolCalls(response.ResponsesResponse)
	}
	return []ToolCallInfo{}
}

// getEmbeddingVector extracts the float32 vector from a BifrostEmbeddingResponse
func getEmbeddingVector(embedding schemas.EmbeddingData) ([]float32, error) {

	if embedding.Embedding.EmbeddingArray != nil {
		return embedding.Embedding.EmbeddingArray, nil
	}

	if embedding.Embedding.Embedding2DArray != nil {
		// For 2D arrays, return the first vector
		if len(embedding.Embedding.Embedding2DArray) > 0 {
			return embedding.Embedding.Embedding2DArray[0], nil
		}
		return nil, fmt.Errorf("2D embedding array is empty")
	}

	if embedding.Embedding.EmbeddingStr != nil {
		return nil, fmt.Errorf("string embeddings not supported for vector extraction")
	}

	return nil, fmt.Errorf("no valid embedding data found")
}

// --- Additional test helpers appended below (imported on demand) ---

// NOTE: importing context, os, testing only in this block to avoid breaking existing imports.
// We duplicate types by fully qualifying to not touch import list above.

// GenerateTTSAudioForTest generates real audio using TTS and writes a temp file.
// Returns audio bytes and temp filepath. Caller’s t will clean it up.
func GenerateTTSAudioForTest(ctx context.Context, t *testing.T, client *bifrost.Bifrost, provider schemas.ModelProvider, ttsModel string, text string, voiceType string, format string) ([]byte, string) {
	// inline import guard comment: context/testing/os are required at call sites; Go compiler will include them.
	voice := GetProviderVoice(provider, voiceType)
	if voice == "" {
		voice = GetProviderVoice(provider, "primary")
	}
	if format == "" {
		format = "mp3"
	}

	req := &schemas.BifrostSpeechRequest{
		Provider: provider,
		Model:    ttsModel,
		Input:    &schemas.SpeechInput{Input: text},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{
				Voice: &voice,
			},
			ResponseFormat: format,
		},
	}

	// Use retry framework for TTS generation in helper function
	// Use default speech retry config since we don't have full test config in helper
	retryConfig := DefaultSpeechRetryConfig()
	retryContext := TestRetryContext{
		ScenarioName: "GenerateTTSAudioForTest",
		ExpectedBehavior: map[string]interface{}{
			"should_generate_audio": true,
		},
		TestMetadata: map[string]interface{}{
			"provider": provider,
			"model":    ttsModel,
			"format":   format,
		},
	}
	expectations := SpeechExpectations(100) // Minimum expected bytes
	expectations = ModifyExpectationsForProvider(expectations, provider)
	speechRetryConfig := SpeechRetryConfig{
		MaxAttempts: retryConfig.MaxAttempts,
		BaseDelay:   retryConfig.BaseDelay,
		MaxDelay:    retryConfig.MaxDelay,
		Conditions:  []SpeechRetryCondition{},
		OnRetry:     retryConfig.OnRetry,
		OnFinalFail: retryConfig.OnFinalFail,
	}

	resp, err := WithSpeechTestRetry(t, speechRetryConfig, retryContext, expectations, "GenerateTTSAudioForTest", func() (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		return client.SpeechRequest(bfCtx, req)
	})
	if err != nil {
		t.Fatalf("TTS request failed after retries: %v", GetErrorMessage(err))
	}
	if resp == nil || resp.Audio == nil || len(resp.Audio) == 0 {
		t.Fatalf("TTS response missing audio data after retries")
	}

	suffix := "." + format
	f, cerr := os.CreateTemp("", "bifrost-tts-*"+suffix)
	if cerr != nil {
		t.Fatalf("failed to create temp audio file: %v", cerr)
	}
	tempPath := f.Name()
	if _, werr := f.Write(resp.Audio); werr != nil {
		_ = f.Close()
		t.Fatalf("failed to write temp audio file: %v", werr)
	}
	_ = f.Close()

	t.Cleanup(func() { _ = os.Remove(tempPath) })

	return resp.Audio, tempPath
}

func GetErrorMessage(err *schemas.BifrostError) string {
	if err == nil {
		return ""
	}

	// Check if err.Error is nil before accessing its fields
	if err.Error == nil {
		// Return a sensible default when Error field is nil
		if err.Type != nil && *err.Type != "" {
			return *err.Type
		}
		return "unknown error"
	}

	errorType := ""
	if err.Type != nil && *err.Type != "" {
		errorType = *err.Type
	}

	if errorType == "" && err.Error.Type != nil && *err.Error.Type != "" {
		errorType = *err.Error.Type
	}

	errorCode := ""
	if err.Error != nil && err.Error.Code != nil && *err.Error.Code != "" {
		errorCode = *err.Error.Code
	}

	errorMessage := err.Error.Message

	errorString := fmt.Sprintf("%s %s: %s", errorType, errorCode, errorMessage)

	return errorString
}

// ShouldRunParallel checks if a test should run in parallel based on environment
// variables and provider-specific configuration. It marks the test as parallel
// if parallel execution is allowed for this scenario.
//
// Parameters:
//   - t: the testing.T instance
//   - testConfig: the comprehensive test config containing DisableParallelFor settings
//   - scenario: the test scenario name (e.g., "Transcription", "SpeechSynthesis")
func ShouldRunParallel(t *testing.T, testConfig ComprehensiveTestConfig, scenario string) {
	// Check global environment variable first
	if os.Getenv("SKIP_PARALLEL_TESTS") == "true" {
		return
	}

	// Check if this scenario is disabled for this provider
	for _, disabled := range testConfig.DisableParallelFor {
		if disabled == scenario {
			return
		}
	}

	// Allow parallel execution
	t.Parallel()
}
