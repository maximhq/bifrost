package scenarios

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Shared test texts for TTS->SST round-trip validation
const (
	// Basic test text for simple round-trip validation
	TTSTestTextBasic = "Hello, this is a test of speech synthesis from Bifrost."

	// Medium length text with punctuation for comprehensive testing
	TTSTestTextMedium = "Testing speech synthesis and transcription round-trip. This text includes punctuation, numbers like 123, and technical terms."

	// Short technical text for WAV format testing
	TTSTestTextTechnical = "Bifrost AI gateway processes audio requests efficiently."
)

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
			return "charon"
		default:
			return "achernar"
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
		Properties: map[string]interface{}{
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
		Properties: map[string]interface{}{
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
		Properties: map[string]interface{}{
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "The timezone identifier, e.g. 'America/New_York' or 'UTC'",
			},
		},
		Required: []string{"timezone"},
	},
}

func GetSampleTool(toolName SampleToolType, isResponsesAPI bool) *schemas.BifrostTool {
	function, ok := SampleToolFunctions[toolName]
	if !ok {
		return nil
	}

	description, ok := sampleToolDescriptions[toolName]
	if !ok {
		return nil
	}

	tool := schemas.BifrostTool{}

	if !isResponsesAPI {
		tool.ChatTools = []schemas.ChatTool{
			{
				Type: "function",
				Function: &schemas.ChatToolFunction{
					Name:        string(toolName),
					Description: bifrost.Ptr(description),
					Parameters:  function.Parameters,
				},
			},
		}
	} else {
		tool.ResponsesTools = []schemas.ResponsesTool{
			{
				Type:        "function",
				Name:        bifrost.Ptr(string(toolName)),
				Description: bifrost.Ptr(description),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: function.Parameters,
				},
			},
		}
	}

	return &tool
}

// Test image of an ant
const TestImageURL = "https://upload.wikimedia.org/wikipedia/commons/a/a7/Camponotus_flavomarginatus_ant.jpg"

// Test image of the Eiffel Tower
const TestImageURL2 = "https://upload.wikimedia.org/wikipedia/commons/thumb/4/4b/La_Tour_Eiffel_vue_de_la_Tour_Saint-Jacques%2C_Paris_ao%C3%BBt_2014_%282%29.jpg/960px-La_Tour_Eiffel_vue_de_la_Tour_Saint-Jacques%2C_Paris_ao%C3%BBt_2014_%282%29.jpg"

// Test image base64 of a grey solid
const TestImageBase64 = "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k="

// GetLionBase64Image loads and returns the lion base64 image data from file
func GetLionBase64Image() (string, error) {
	data, err := os.ReadFile("scenarios/media/lion_base64.txt")
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + string(data), nil
}

// CreateSpeechInput creates a basic speech input for testing
func CreateSpeechInput(text, voice, format string) *schemas.SpeechInput {
	return &schemas.SpeechInput{
		Input: text,
		VoiceConfig: schemas.SpeechVoiceInput{
			Voice: &voice,
		},
		ResponseFormat: format,
	}
}

// CreateTranscriptionInput creates a basic transcription input for testing
func CreateTranscriptionInput(audioData []byte, language, responseFormat *string) *schemas.TranscriptionInput {
	return &schemas.TranscriptionInput{
		File:           audioData,
		Language:       language,
		ResponseFormat: responseFormat,
	}
}

// Helper functions for creating requests
func CreateBasicChatMessage(content string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: schemas.ChatMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateBasicResponsesMessage(content string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateImageChatMessage(text, imageURL string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: schemas.ChatMessageContent{
			ContentBlocks: &[]schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: bifrost.Ptr(text)},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: imageURL}},
			},
		},
	}
}

func CreateImageResponsesMessage(text, imageURL string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: &[]schemas.ResponsesMessageContentBlock{
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

func CreateToolChatMessage(content string, toolCallID string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: schemas.ChatMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: bifrost.Ptr(toolCallID),
		},
	}
}

func CreateToolResponsesMessage(content string, toolCallID string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: bifrost.Ptr(toolCallID),
		},
	}
}

// GetResultContent returns the string content from a BifrostResponse
// It looks through all choices and returns content from the first choice that has any
func GetResultContent(result *schemas.BifrostResponse) string {
	if result == nil || (result.Choices == nil && result.ResponsesResponse == nil) {
		return ""
	}

	if result.Choices != nil {
		// Try to find content from any choice, prioritizing non-empty content
		for _, choice := range result.Choices {
			if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
				return *choice.Message.Content.ContentStr
			} else if choice.Message.Content.ContentBlocks != nil {
				var builder strings.Builder
				for _, block := range *choice.Message.Content.ContentBlocks {
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

		// Fallback to first choice if no content found
		if result.Choices[0].Message.Content.ContentStr != nil {
			return *result.Choices[0].Message.Content.ContentStr
		} else if result.Choices[0].Message.Content.ContentBlocks != nil {
			var builder strings.Builder
			for _, block := range *result.Choices[0].Message.Content.ContentBlocks {
				if block.Text != nil {
					builder.WriteString(*block.Text)
				}
			}
			return builder.String()
		}
	} else if result.ResponsesResponse != nil {
		for _, output := range result.ResponsesResponse.Output {
			if output.Content.ContentStr != nil {
				return *output.Content.ContentStr
			} else if output.Content.ContentBlocks != nil {
				var builder strings.Builder
				for _, block := range *output.Content.ContentBlocks {
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

		if result.ResponsesResponse.Output[0].Content.ContentStr != nil {
			return *result.Output[0].Content.ContentStr
		} else if result.Output[0].Content.ContentBlocks != nil {
			var builder strings.Builder
			for _, block := range *result.Output[0].Content.ContentBlocks {
				if block.Text != nil {
					builder.WriteString(*block.Text)
				}
			}
			return builder.String()
		}
	}

	return ""
}

// MergeModelParameters performs a shallow merge of two ModelParameters instances.
// Non-nil fields from the override parameter take precedence over the base parameter.
// Returns a new ModelParameters instance with the merged values.
func MergeModelParameters(base *schemas.ModelParameters, override *schemas.ModelParameters) *schemas.ModelParameters {
	if base == nil && override == nil {
		return &schemas.ModelParameters{}
	}
	if base == nil {
		return copyModelParameters(override)
	}
	if override == nil {
		return copyModelParameters(base)
	}

	// Start with a copy of base parameters
	result := copyModelParameters(base)

	// Override with non-nil fields from override
	if override.MaxCompletionTokens != nil {
		result.MaxCompletionTokens = override.MaxCompletionTokens
	}
	if override.Temperature != nil {
		result.Temperature = override.Temperature
	}
	if override.TopP != nil {
		result.TopP = override.TopP
	}
	if override.TopLogProbs != nil {
		result.TopLogProbs = override.TopLogProbs
	}
	if override.FrequencyPenalty != nil {
		result.FrequencyPenalty = override.FrequencyPenalty
	}
	if override.PresencePenalty != nil {
		result.PresencePenalty = override.PresencePenalty
	}
	if override.Stop != nil {
		result.Stop = override.Stop
	}
	if override.Tools != nil {
		result.Tools = override.Tools
	}
	if override.ToolChoice != nil {
		result.ToolChoice = override.ToolChoice
	}
	if override.ParallelToolCalls != nil {
		result.ParallelToolCalls = override.ParallelToolCalls
	}
	if override.MaxCompletionTokens != nil {
		result.MaxCompletionTokens = override.MaxCompletionTokens
	}
	if override.ReasoningEffort != nil {
		result.ReasoningEffort = override.ReasoningEffort
	}
	if override.ResponseFormat != nil {
		result.ResponseFormat = override.ResponseFormat
	}
	if override.Seed != nil {
		result.Seed = override.Seed
	}
	if override.Stop != nil {
		result.Stop = override.Stop
	}
	if override.User != nil {
		result.User = override.User
	}
	if override.Verbosity != nil {
		result.Verbosity = override.Verbosity
	}
	if override.LogitBias != nil {
		result.LogitBias = override.LogitBias
	}
	if override.LogProbs != nil {
		result.LogProbs = override.LogProbs
	}
	if override.Metadata != nil {
		result.Metadata = override.Metadata
	}
	if override.Modalities != nil {
		result.Modalities = override.Modalities
	}
	if override.StreamOptions != nil {
		result.StreamOptions = override.StreamOptions
	}
	if override.EncodingFormat != nil {
		result.EncodingFormat = override.EncodingFormat
	}
	if override.Dimensions != nil {
		result.Dimensions = override.Dimensions
	}
	if override.User != nil {
		result.User = override.User
	}
	if override.ExtraParams != nil {
		result.ExtraParams = override.ExtraParams
	}
	if override.StreamOptions != nil {
		result.StreamOptions = override.StreamOptions
	}
	if override.EncodingFormat != nil {
		result.EncodingFormat = override.EncodingFormat
	}
	if override.Dimensions != nil {
		result.Dimensions = override.Dimensions
	}
	if override.User != nil {
		result.User = override.User
	}
	if override.ExtraParams != nil {
		result.ExtraParams = override.ExtraParams
	}

	return result
}

// copyModelParameters creates a shallow copy of a ModelParameters instance
func copyModelParameters(src *schemas.ModelParameters) *schemas.ModelParameters {
	if src == nil {
		return &schemas.ModelParameters{}
	}

	return &schemas.ModelParameters{
		CommonParameters:    src.CommonParameters,
		EmbeddingParameters: src.EmbeddingParameters,
		ChatParameters:      src.ChatParameters,
		ResponsesParameters: src.ResponsesParameters,
		ExtraParams:         src.ExtraParams,
	}
}

// ToolCallInfo represents extracted tool call information for both API formats
type ToolCallInfo struct {
	Name      string
	Arguments string
	ID        string
}

// ExtractToolCalls extracts tool call information from a BifrostResponse for both Chat Completions and Responses API
func ExtractToolCalls(response *schemas.BifrostResponse) []ToolCallInfo {
	if response == nil {
		return nil
	}

	var toolCalls []ToolCallInfo

	// Extract from Chat Completions API format
	if response.Choices != nil {
		for _, choice := range response.Choices {
			if choice.Message.ChatAssistantMessage != nil &&
				choice.Message.ChatAssistantMessage.ToolCalls != nil &&
				choice.Message.ChatAssistantMessage.ToolCalls != nil {

				chatToolCalls := *choice.Message.ChatAssistantMessage.ToolCalls
				for _, toolCall := range chatToolCalls {
					info := ToolCallInfo{
						Arguments: toolCall.Function.Arguments,
					}
					if toolCall.Function.Name != nil {
						info.Name = *toolCall.Function.Name
					}
					if toolCall.ID != nil {
						info.ID = *toolCall.ID
					}
					toolCalls = append(toolCalls, info)
				}
			}
		}
	}

	// Extract from Responses API format
	if response.ResponsesResponse != nil {
		for _, output := range response.ResponsesResponse.Output {
			// Check for function calls in assistant messages
			info := ToolCallInfo{}

			// Also check if the message itself is a function call type
			if output.ResponsesToolMessage != nil &&
				output.Type != nil &&
				*output.Type == "function_call" &&
				output.ResponsesToolMessage != nil && output.ResponsesToolMessage.CallID != nil {

				if output.Name != nil {
					info.Name = *output.Name
				}
				if output.ResponsesToolMessage.CallID != nil {
					info.ID = *output.ResponsesToolMessage.CallID
				}

				// Get arguments from embedded function tool call if available
				info.Arguments = *output.Arguments

				toolCalls = append(toolCalls, info)
			}
		}
	}

	return toolCalls
}

// getEmbeddingVector extracts the float32 vector from a BifrostEmbeddingResponse
func getEmbeddingVector(embedding schemas.BifrostEmbeddingResponse) ([]float32, error) {
	if embedding.EmbeddingArray != nil {
		return *embedding.EmbeddingArray, nil
	}

	if embedding.Embedding2DArray != nil {
		// For 2D arrays, return the first vector
		if len(*embedding.Embedding2DArray) > 0 {
			return (*embedding.Embedding2DArray)[0], nil
		}
		return nil, fmt.Errorf("2D embedding array is empty")
	}

	if embedding.EmbeddingStr != nil {
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

	req := &schemas.BifrostRequest{
		Provider: provider,
		Model:    ttsModel,
		Input: schemas.RequestInput{
			SpeechInput: &schemas.SpeechInput{
				Input: text,
				VoiceConfig: schemas.SpeechVoiceInput{
					Voice: &voice,
				},
				ResponseFormat: format,
			},
		},
	}

	resp, err := client.SpeechRequest(ctx, req)
	if err != nil {
		t.Fatalf("TTS request failed: %v", err)
	}
	if resp == nil || resp.Speech == nil || len(resp.Speech.Audio) == 0 {
		t.Fatalf("TTS response missing audio data")
	}

	suffix := "." + format
	f, cerr := os.CreateTemp("", "bifrost-tts-*"+suffix)
	if cerr != nil {
		t.Fatalf("failed to create temp audio file: %v", cerr)
	}
	tempPath := f.Name()
	if _, werr := f.Write(resp.Speech.Audio); werr != nil {
		_ = f.Close()
		t.Fatalf("failed to write temp audio file: %v", werr)
	}
	_ = f.Close()

	t.Cleanup(func() { _ = os.Remove(tempPath) })

	return resp.Speech.Audio, tempPath
}

func GetErrorMessage(err *schemas.BifrostError) string {
	if err == nil {
		return ""
	}

	errorType := ""
	if err.Type != nil && *err.Type != "" {
		errorType = *err.Type
	}

	if errorType == "" && err.Error.Type != nil && *err.Error.Type != "" {
		errorType = *err.Error.Type
	}

	errorCode := ""
	if err.Error.Code != nil && *err.Error.Code != "" {
		errorCode = *err.Error.Code
	}

	errorMessage := err.Error.Message

	errorString := fmt.Sprintf("%s %s: %s", errorType, errorCode, errorMessage)

	return errorString
}
